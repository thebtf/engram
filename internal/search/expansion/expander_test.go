// Package expansion provides context-aware query expansion for improved search recall.
package expansion

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

// ExpanderSuite tests the Expander functionality.
type ExpanderSuite struct {
	suite.Suite
	expander *Expander
}

func TestExpanderSuite(t *testing.T) {
	suite.Run(t, new(ExpanderSuite))
}

func (s *ExpanderSuite) SetupTest() {
	s.expander = NewExpander(nil)
}

// TestNewExpander tests expander creation.
func (s *ExpanderSuite) TestNewExpander() {
	e := NewExpander(nil)
	s.NotNil(e)
	s.NotNil(e.intentPatterns)
}

// TestDetectIntent tests intent detection.
func (s *ExpanderSuite) TestDetectIntent() {
	tests := []struct {
		name     string
		query    string
		expected QueryIntent
	}{
		// Question intents
		{"how_question", "how do I implement auth?", IntentQuestion},
		{"why_question", "why does this fail?", IntentError}, // "fail" triggers error first
		{"what_question", "what is the purpose of this function?", IntentQuestion},
		{"question_mark", "the handler for auth?", IntentQuestion},
		{"explain", "explain the architecture", IntentQuestion},

		// Error intents
		{"error_word", "authentication error in login", IntentError},
		{"bug_word", "bug in user registration", IntentError},
		{"fix_word", "fix the memory leak", IntentError},
		{"not_working", "login not working", IntentError},
		{"crash", "application crash on startup", IntentError},

		// Implementation intents
		{"implement", "implement user authentication", IntentImplementation},
		{"add_feature", "add new endpoint for users", IntentImplementation},
		{"create", "create a handler for uploads", IntentImplementation},
		{"function", "function to validate input", IntentImplementation},

		// Architecture intents
		{"architecture", "architecture of the system", IntentArchitecture},
		{"design", "design pattern for observers", IntentArchitecture},
		{"component", "component structure", IntentArchitecture},
		{"flow", "data flow in the pipeline", IntentArchitecture},

		// General intents
		{"general", "user authentication", IntentGeneral},
		{"empty", "", IntentGeneral},
		{"simple", "database", IntentGeneral},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			result := s.expander.DetectIntent(tt.query)
			s.Equal(tt.expected, result, "Query: %s", tt.query)
		})
	}
}

// TestExpand tests basic query expansion.
func (s *ExpanderSuite) TestExpand() {
	ctx := context.Background()
	cfg := DefaultConfig()

	tests := []struct {
		name           string
		query          string
		expectedIntent QueryIntent
		minExpansions  int
		hasOriginal    bool
	}{
		{name: "question", query: "how do I implement auth", expectedIntent: IntentQuestion, minExpansions: 1, hasOriginal: true},
		{name: "error", query: "fix the bug in login", expectedIntent: IntentError, minExpansions: 1, hasOriginal: true},
		{name: "implementation", query: "implement user handler", expectedIntent: IntentImplementation, minExpansions: 1, hasOriginal: true},
		{name: "architecture", query: "architecture design", expectedIntent: IntentArchitecture, minExpansions: 1, hasOriginal: true},
		{name: "general", query: "database connection", expectedIntent: IntentGeneral, minExpansions: 1, hasOriginal: true},
		{name: "empty", query: "", expectedIntent: IntentGeneral, minExpansions: 0, hasOriginal: false},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			expansions := s.expander.Expand(ctx, tt.query, cfg)

			if tt.minExpansions == 0 {
				s.Empty(expansions)
				return
			}

			s.GreaterOrEqual(len(expansions), tt.minExpansions)

			if tt.hasOriginal {
				// First expansion should be the original
				s.Equal(tt.query, expansions[0].Query)
				s.Equal(1.0, expansions[0].Weight)
				s.Equal("original", expansions[0].Source)
			}
		})
	}
}

// TestExpandWithConfig tests expansion with custom config.
func (s *ExpanderSuite) TestExpandWithConfig() {
	ctx := context.Background()

	cfg := Config{
		MaxExpansions: 2,
	}

	expansions := s.expander.Expand(ctx, "how to implement authentication", cfg)
	s.LessOrEqual(len(expansions), cfg.MaxExpansions)
}

// TestExpandDeduplication tests that duplicates are removed.
func (s *ExpanderSuite) TestExpandDeduplication() {
	ctx := context.Background()
	cfg := DefaultConfig()

	// Query that might generate duplicate expansions
	query := "how to fix authentication"
	expansions := s.expander.Expand(ctx, query, cfg)

	// Check for duplicates
	seen := make(map[string]bool)
	for _, exp := range expansions {
		normalized := exp.Query
		s.False(seen[normalized], "Duplicate expansion found: %s", exp.Query)
		seen[normalized] = true
	}
}

// TestExtractKeyTerms tests key term extraction.
func TestExtractKeyTerms(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected []string
	}{
		{
			name:     "simple",
			query:    "user authentication handler",
			expected: []string{"user", "authentication", "handler"},
		},
		{
			name:     "with_stop_words",
			query:    "how to implement the user login",
			expected: []string{"implement", "user", "login"},
		},
		{
			name:     "with_punctuation",
			query:    "fix the bug, please!",
			expected: []string{"fix", "bug", "please"},
		},
		{
			name:     "empty",
			query:    "",
			expected: nil,
		},
		{
			name:     "only_stop_words",
			query:    "the a an is are",
			expected: nil,
		},
		{
			name:     "short_words_filtered",
			query:    "a b c auth",
			expected: []string{"auth"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractKeyTerms(tt.query)
			if tt.expected == nil {
				assert.Empty(t, result)
			} else {
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestMakeDeclarative tests question to declarative conversion.
func TestMakeDeclarative(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{
			name:     "how_do_i",
			query:    "how do I implement auth?",
			expected: "implement auth",
		},
		{
			name:     "how_to",
			query:    "how to fix the bug",
			expected: "fix the bug",
		},
		{
			name:     "what_is",
			query:    "what is the purpose of this?",
			expected: "the purpose of this",
		},
		{
			name:     "why_does",
			query:    "why does this fail?",
			expected: "this fail",
		},
		{
			name:     "already_declarative",
			query:    "user authentication",
			expected: "user authentication",
		},
		{
			name:     "question_mark_only",
			query:    "authentication?",
			expected: "authentication",
		},
		{
			name:     "case_insensitive",
			query:    "How To Fix Auth?",
			expected: "Fix Auth",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := makeDeclarative(tt.query)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestDeduplicateExpansions tests deduplication.
func TestDeduplicateExpansions(t *testing.T) {
	expansions := []ExpandedQuery{
		{Query: "auth handler", Weight: 1.0},
		{Query: "AUTH HANDLER", Weight: 0.8},  // Duplicate (case insensitive)
		{Query: "auth handler ", Weight: 0.7}, // Duplicate (whitespace)
		{Query: "user auth", Weight: 0.6},
	}

	result := deduplicateExpansions(expansions)
	assert.Len(t, result, 2) // "auth handler" and "user auth"
	assert.Equal(t, "auth handler", result[0].Query)
	assert.Equal(t, 1.0, result[0].Weight) // First one preserved
	assert.Equal(t, "user auth", result[1].Query)
}


// TestDefaultConfig tests default configuration.
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.Equal(t, 4, cfg.MaxExpansions)
}

// TestExpandedQueryStruct tests ExpandedQuery struct.
func TestExpandedQueryStruct(t *testing.T) {
	eq := ExpandedQuery{
		Query:  "test query",
		Weight: 0.85,
		Source: "vocabulary:auth",
		Intent: IntentQuestion,
	}

	assert.Equal(t, "test query", eq.Query)
	assert.Equal(t, 0.85, eq.Weight)
	assert.Equal(t, "vocabulary:auth", eq.Source)
	assert.Equal(t, IntentQuestion, eq.Intent)
}

// TestIntentConstants tests intent constant values.
func TestIntentConstants(t *testing.T) {
	assert.Equal(t, QueryIntent("question"), IntentQuestion)
	assert.Equal(t, QueryIntent("error"), IntentError)
	assert.Equal(t, QueryIntent("implementation"), IntentImplementation)
	assert.Equal(t, QueryIntent("architecture"), IntentArchitecture)
	assert.Equal(t, QueryIntent("general"), IntentGeneral)
}

// TestTruncate tests the truncate helper.
func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		maxLen   int
	}{
		{name: "short", input: "hello", expected: "hello", maxLen: 10},
		{name: "exact", input: "hello", expected: "hello", maxLen: 5},
		{name: "long", input: "hello world", expected: "hello...", maxLen: 5},
		{name: "empty", input: "", expected: "", maxLen: 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncate(tt.input, tt.maxLen)
			assert.Equal(t, tt.expected, result)
		})
	}
}


// TestExpandByIntentError tests error intent expansion.
func (s *ExpanderSuite) TestExpandByIntentError() {
	expansions := s.expander.expandByIntent("fix authentication bug", IntentError)
	s.NotEmpty(expansions)

	// Should have solution-oriented expansion
	hasSolution := false
	for _, exp := range expansions {
		if exp.Source == "intent:solution" {
			hasSolution = true
			break
		}
	}
	s.True(hasSolution)
}

// TestExpandByIntentQuestion tests question intent expansion.
func (s *ExpanderSuite) TestExpandByIntentQuestion() {
	expansions := s.expander.expandByIntent("how do I implement auth", IntentQuestion)
	s.NotEmpty(expansions)

	// Should have declarative expansion
	hasDeclarative := false
	for _, exp := range expansions {
		if exp.Source == "intent:declarative" {
			hasDeclarative = true
			break
		}
	}
	s.True(hasDeclarative)
}

// TestExpandByIntentImplementation tests implementation intent expansion.
func (s *ExpanderSuite) TestExpandByIntentImplementation() {
	expansions := s.expander.expandByIntent("implement user handler", IntentImplementation)
	s.NotEmpty(expansions)

	// Should have how expansion
	hasHow := false
	for _, exp := range expansions {
		if exp.Source == "intent:how" {
			hasHow = true
			break
		}
	}
	s.True(hasHow)
}

// TestExpandByIntentArchitecture tests architecture intent expansion.
func (s *ExpanderSuite) TestExpandByIntentArchitecture() {
	expansions := s.expander.expandByIntent("system architecture design", IntentArchitecture)
	s.NotEmpty(expansions)

	// Should have design expansion
	hasDesign := false
	for _, exp := range expansions {
		if exp.Source == "intent:design" {
			hasDesign = true
			break
		}
	}
	s.True(hasDesign)
}

// TestExpandByIntentGeneral tests general intent returns no expansions.
func (s *ExpanderSuite) TestExpandByIntentGeneral() {
	expansions := s.expander.expandByIntent("database", IntentGeneral)
	s.Empty(expansions) // General intent doesn't add intent-based expansions
}

// TestIntentPatternsExist tests that all intents have patterns.
func (s *ExpanderSuite) TestIntentPatternsExist() {
	s.NotEmpty(s.expander.intentPatterns[IntentQuestion])
	s.NotEmpty(s.expander.intentPatterns[IntentError])
	s.NotEmpty(s.expander.intentPatterns[IntentImplementation])
	s.NotEmpty(s.expander.intentPatterns[IntentArchitecture])
}
