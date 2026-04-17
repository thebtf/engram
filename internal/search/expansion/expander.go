// Package expansion provides context-aware query expansion for improved search recall.
package expansion

import (
	"context"
	"regexp"
	"strings"
	"sync"

	"github.com/thebtf/engram/pkg/strutil"
	"github.com/rs/zerolog/log"
)

// QueryIntent represents the detected intent of a query.
type QueryIntent string

const (
	// IntentQuestion indicates a question-type query (how, why, what, etc.)
	IntentQuestion QueryIntent = "question"
	// IntentError indicates an error/debugging query
	IntentError QueryIntent = "error"
	// IntentImplementation indicates an implementation/coding query
	IntentImplementation QueryIntent = "implementation"
	// IntentArchitecture indicates an architecture/design query
	IntentArchitecture QueryIntent = "architecture"
	// IntentGeneral indicates a general lookup query
	IntentGeneral QueryIntent = "general"
)

// ExpandedQuery represents a query variant with metadata.
type ExpandedQuery struct {
	Query  string      `json:"query"`
	Source string      `json:"source"`
	Intent QueryIntent `json:"intent"`
	Weight float64     `json:"weight"`
}

// Expander provides context-aware query expansion.
type Expander struct {
	hydeGen        *HyDEGenerator
	intentPatterns map[QueryIntent][]*regexp.Regexp
	vocabulary     []VocabEntry
	vocabMu        sync.RWMutex
}

// VocabEntry represents a vocabulary term from observations.
type VocabEntry struct {
	Term   string
	Source string
	Weight float64
}

// Config holds expander configuration.
type Config struct {
	// MaxExpansions limits the number of expanded queries returned
	MaxExpansions int
	// MinSimilarity is the minimum similarity score for vocabulary expansion
	MinSimilarity float64
	// EnableVocabularyExpansion enables finding related terms from observations
	EnableVocabularyExpansion bool
	// EnableHyDE enables hypothetical document embedding expansion
	EnableHyDE bool
}

// DefaultConfig returns sensible default configuration.
// EnableVocabularyExpansion is intentionally false in v5: vocabulary expansion
// requires vector embeddings which were removed in the v5 cleanup.
func DefaultConfig() Config {
	return Config{
		MaxExpansions:             4,
		MinSimilarity:             0.5,
		EnableVocabularyExpansion: false,
	}
}

// NewExpander creates a new query expander.
// The hydeGen parameter is optional (nil disables HyDE expansion).
func NewExpander(hydeGen *HyDEGenerator) *Expander {
	e := &Expander{
		hydeGen:        hydeGen,
		intentPatterns: buildIntentPatterns(),
	}
	return e
}

// buildIntentPatterns creates regex patterns for intent detection.
func buildIntentPatterns() map[QueryIntent][]*regexp.Regexp {
	patterns := make(map[QueryIntent][]*regexp.Regexp)

	// Question patterns
	patterns[IntentQuestion] = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^(how|why|what|when|where|which|who)\b`),
		regexp.MustCompile(`(?i)\?$`),
		regexp.MustCompile(`(?i)\b(explain|describe|understand)\b`),
	}

	// Error/debugging patterns
	patterns[IntentError] = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(error|bug|issue|problem|fail|crash|exception|panic)\b`),
		regexp.MustCompile(`(?i)\b(fix|debug|troubleshoot|resolve)\b`),
		regexp.MustCompile(`(?i)\b(doesn't work|not working|broken)\b`),
	}

	// Implementation patterns
	patterns[IntentImplementation] = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(implement|add|create|build|write|code)\b`),
		regexp.MustCompile(`(?i)\b(function|method|handler|endpoint|api)\b`),
		regexp.MustCompile(`(?i)\b(feature|functionality)\b`),
	}

	// Architecture patterns
	patterns[IntentArchitecture] = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(architecture|design|pattern|structure)\b`),
		regexp.MustCompile(`(?i)\b(component|module|layer|service)\b`),
		regexp.MustCompile(`(?i)\b(flow|pipeline|workflow)\b`),
	}

	return patterns
}

// DetectIntent analyzes a query to determine its intent.
func (e *Expander) DetectIntent(query string) QueryIntent {
	query = strings.TrimSpace(query)
	if query == "" {
		return IntentGeneral
	}

	// Check patterns in priority order
	intentOrder := []QueryIntent{IntentError, IntentQuestion, IntentImplementation, IntentArchitecture}

	for _, intent := range intentOrder {
		patterns := e.intentPatterns[intent]
		for _, pattern := range patterns {
			if pattern.MatchString(query) {
				return intent
			}
		}
	}

	return IntentGeneral
}

// Expand generates expanded query variants based on the original query.
func (e *Expander) Expand(ctx context.Context, query string, cfg Config) []ExpandedQuery {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}

	intent := e.DetectIntent(query)
	expansions := make([]ExpandedQuery, 0, cfg.MaxExpansions)

	// Always include the original query with highest weight
	expansions = append(expansions, ExpandedQuery{
		Query:  query,
		Weight: 1.0,
		Source: "original",
		Intent: intent,
	})

	// Generate intent-based expansions
	intentExpansions := e.expandByIntent(query, intent)
	expansions = append(expansions, intentExpansions...)

	// Generate HyDE expansion if enabled
	if cfg.EnableHyDE && e.hydeGen != nil {
		if hypothesis := e.hydeGen.Generate(ctx, query, intent); hypothesis != "" {
			expansions = append(expansions, ExpandedQuery{
				Query:  hypothesis,
				Weight: 0.9,
				Source: "hyde",
				Intent: intent,
			})
		}
	}

	// Generate vocabulary-based expansions if enabled and we have vocabulary
	if cfg.EnableVocabularyExpansion && len(e.vocabulary) > 0 {
		vocabExpansions := e.expandByVocabulary(ctx, query, cfg.MinSimilarity)
		expansions = append(expansions, vocabExpansions...)
	}

	// Deduplicate and limit
	expansions = deduplicateExpansions(expansions)
	if len(expansions) > cfg.MaxExpansions {
		expansions = expansions[:cfg.MaxExpansions]
	}

	log.Debug().
		Str("query", truncate(query, 50)).
		Str("intent", string(intent)).
		Int("expansions", len(expansions)).
		Msg("Query expanded")

	return expansions
}

// expandByIntent generates expansions based on detected query intent.
func (e *Expander) expandByIntent(query string, intent QueryIntent) []ExpandedQuery {
	var expansions []ExpandedQuery

	// Extract key terms from query for context-aware expansion
	keyTerms := extractKeyTerms(query)

	switch intent {
	case IntentQuestion:
		// For questions, create a declarative variant
		declarative := makeDeclarative(query)
		if declarative != query {
			expansions = append(expansions, ExpandedQuery{
				Query:  declarative,
				Weight: 0.85,
				Source: "intent:declarative",
				Intent: intent,
			})
		}

	case IntentError:
		// For errors, expand with solution-oriented terms
		if len(keyTerms) > 0 {
			solutionQuery := strings.Join(keyTerms, " ") + " solution fix"
			expansions = append(expansions, ExpandedQuery{
				Query:  solutionQuery,
				Weight: 0.8,
				Source: "intent:solution",
				Intent: intent,
			})
		}

	case IntentImplementation:
		// For implementation queries, focus on the what/how
		if len(keyTerms) > 0 {
			howQuery := "how " + strings.Join(keyTerms, " ")
			expansions = append(expansions, ExpandedQuery{
				Query:  howQuery,
				Weight: 0.75,
				Source: "intent:how",
				Intent: intent,
			})
		}

	case IntentArchitecture:
		// For architecture queries, expand with design context
		if len(keyTerms) > 0 {
			designQuery := strings.Join(keyTerms, " ") + " design structure"
			expansions = append(expansions, ExpandedQuery{
				Query:  designQuery,
				Weight: 0.75,
				Source: "intent:design",
				Intent: intent,
			})
		}

	case IntentGeneral:
		// For general queries, try noun phrase extraction
		// No additional expansion - rely on vocabulary expansion
	}

	return expansions
}

// expandByVocabulary is a no-op in v5: vocabulary expansion requires vector
// embeddings which were removed in the v5 cleanup. Always returns nil.
func (e *Expander) expandByVocabulary(_ context.Context, _ string, _ float64) []ExpandedQuery {
	return nil
}

// Helper functions

// extractKeyTerms extracts meaningful terms from a query.
func extractKeyTerms(query string) []string {
	// Common stop words to filter out
	stopWords := map[string]bool{
		"a": true, "an": true, "the": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "could": true, "should": true,
		"may": true, "might": true, "must": true, "can": true,
		"i": true, "me": true, "my": true, "we": true, "our": true,
		"you": true, "your": true, "it": true, "its": true,
		"this": true, "that": true, "these": true, "those": true,
		"what": true, "which": true, "who": true, "whom": true,
		"how": true, "why": true, "when": true, "where": true,
		"to": true, "for": true, "with": true, "about": true, "from": true,
		"in": true, "on": true, "at": true, "by": true, "of": true,
		"and": true, "or": true, "but": true, "if": true, "then": true,
	}

	// Split and filter
	words := strings.Fields(strings.ToLower(query))
	var terms []string

	for _, word := range words {
		// Remove punctuation
		word = strings.Trim(word, ".,?!;:'\"()[]{}")
		if len(word) < 2 {
			continue
		}
		if stopWords[word] {
			continue
		}
		terms = append(terms, word)
	}

	return terms
}

// makeDeclarative converts a question to a declarative statement.
func makeDeclarative(query string) string {
	query = strings.TrimSpace(query)

	// Remove question mark
	query = strings.TrimSuffix(query, "?")

	// Handle common question patterns
	patterns := []struct {
		prefix      string
		replacement string
	}{
		{"how do i ", ""},
		{"how to ", ""},
		{"how does ", ""},
		{"how is ", ""},
		{"what is ", ""},
		{"what are ", ""},
		{"why does ", ""},
		{"why is ", ""},
		{"where is ", ""},
		{"where are ", ""},
		{"when does ", ""},
		{"when is ", ""},
	}

	lower := strings.ToLower(query)
	for _, p := range patterns {
		if strings.HasPrefix(lower, p.prefix) {
			return strings.TrimSpace(query[len(p.prefix):])
		}
	}

	return query
}

// deduplicateExpansions removes duplicate queries while preserving order.
func deduplicateExpansions(expansions []ExpandedQuery) []ExpandedQuery {
	seen := make(map[string]bool)
	result := make([]ExpandedQuery, 0, len(expansions))

	for _, exp := range expansions {
		normalized := strings.ToLower(strings.TrimSpace(exp.Query))
		if !seen[normalized] {
			seen[normalized] = true
			result = append(result, exp)
		}
	}

	return result
}


var truncate = strutil.Truncate
