package writegate

import (
	"strings"
	"unicode"
)

// Jaccard computes word-token Jaccard similarity between two strings.
// Returns 0.0 for completely different text, 1.0 for identical.
func Jaccard(a, b string) float64 {
	tokensA := tokenize(a)
	tokensB := tokenize(b)
	if len(tokensA) == 0 && len(tokensB) == 0 {
		return 1.0
	}
	if len(tokensA) == 0 || len(tokensB) == 0 {
		return 0.0
	}

	setA := make(map[string]struct{}, len(tokensA))
	for _, t := range tokensA {
		setA[t] = struct{}{}
	}

	intersection := 0
	setB := make(map[string]struct{}, len(tokensB))
	for _, t := range tokensB {
		setB[t] = struct{}{}
		if _, ok := setA[t]; ok {
			intersection++
		}
	}

	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 1.0
	}
	return float64(intersection) / float64(union)
}

// tokenize splits text into lowercase word tokens, filtering punctuation.
func tokenize(s string) []string {
	lower := strings.ToLower(s)
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return words
}
