// Package feedback implements citation detection for the closed-loop learning
// pipeline. It identifies which injected memories were actually referenced in
// the agent's output text so the Thompson Sampling priors (TsAlpha/TsBeta) can
// be updated toward more effective memories.
package feedback

import (
	"strings"
	"unicode"

	"github.com/thebtf/engram/pkg/models"
)

// CitationResult records whether a single injected memory was cited in the
// agent's output text.
type CitationResult struct {
	MemoryID int64
	Cited    bool
	// Excerpt is the first matching phrase from the output, up to 200 runes.
	// Empty when Cited is false.
	Excerpt string
}

// minMatchLength is the minimum number of runes a memory content fragment must
// have before we attempt substring matching. Very short strings produce too many
// false positives.
const minMatchLength = 10

// DetectCitations checks agentOutput for evidence that each memory in memories
// was referenced. A memory is considered cited when a normalised substring of at
// least minMatchLength runes from its content appears in the normalised output.
//
// Normalisation: lowercase, collapse whitespace runs to a single space, trim
// leading/trailing space. This is intentionally conservative — only close
// textual matches count; paraphrase is not detected.
//
// The returned slice has one entry per input memory, in the same order.
func DetectCitations(agentOutput string, memories []*models.Memory) []CitationResult {
	if len(memories) == 0 {
		return nil
	}

	normOutput := normalise(agentOutput)
	results := make([]CitationResult, len(memories))

	for i, m := range memories {
		if m == nil {
			results[i] = CitationResult{MemoryID: 0, Cited: false}
			continue
		}

		// Try to find any phrase from the memory content in the output.
		excerpt, found := findCitation(normOutput, m.Content)
		results[i] = CitationResult{
			MemoryID: m.ID,
			Cited:    found,
			Excerpt:  excerpt,
		}
	}

	return results
}

// findCitation checks whether any fragment of content can be found in the
// normalised output. It first tries the full normalised content, then falls back
// to sliding windows of sentences/clauses split on punctuation.
//
// Returns (excerpt, true) on the first match; ("", false) if no match is found.
func findCitation(normOutput, content string) (string, bool) {
	normContent := normalise(content)
	if len([]rune(normContent)) < minMatchLength {
		return "", false
	}

	// Fast path: full content match.
	if strings.Contains(normOutput, normContent) {
		return excerpt(normContent, 200), true
	}

	// Split into clauses on sentence-ending punctuation and try each clause.
	clauses := splitClauses(normContent)
	for _, clause := range clauses {
		if len([]rune(clause)) < minMatchLength {
			continue
		}
		if strings.Contains(normOutput, clause) {
			return excerpt(clause, 200), true
		}
	}

	return "", false
}

// normalise lowercases the string and collapses all whitespace runs to a single
// space.
func normalise(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteRune(' ')
			}
			prevSpace = true
		} else {
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

// splitClauses splits on common sentence/clause terminators (.!?;:) and returns
// the trimmed non-empty parts. Each part is already normalised (lowercased +
// collapsed) because the input s is the output of normalise().
func splitClauses(s string) []string {
	delimiters := ".!?;:"
	var parts []string
	start := 0
	runes := []rune(s)
	for i, r := range runes {
		if strings.ContainsRune(delimiters, r) {
			part := strings.TrimSpace(string(runes[start:i]))
			if part != "" {
				parts = append(parts, part)
			}
			start = i + 1
		}
	}
	if tail := strings.TrimSpace(string(runes[start:])); tail != "" {
		parts = append(parts, tail)
	}
	return parts
}

// excerpt returns up to maxRunes runes from s, appending "…" if truncated.
func excerpt(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}
