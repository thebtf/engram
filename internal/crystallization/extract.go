// Package crystallization provides session-end decision and pattern extraction.
package crystallization

import (
	"regexp"
	"strings"
)

// ExtractedDecision represents a decision found in agent output.
type ExtractedDecision struct {
	Text     string `json:"text"`
	Pattern  string `json:"pattern"`
	Position int    `json:"position"`
}

var decisionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:decided|decision)\s+(?:to\s+)?(.{10,200}?)(?:\.|$)`),
	regexp.MustCompile(`(?i)chose\s+(.{3,200}?)\s+(?:over|instead of)\s+(.{3,200}?)(?:\.|$)`),
	regexp.MustCompile(`(?i)the reason (?:is|was)\s+(.{10,200}?)(?:\.|$)`),
	regexp.MustCompile(`(?i)going forward[,:]?\s+(.{10,200}?)(?:\.|$)`),
	regexp.MustCompile(`(?i)we should\s+(.{10,200}?)(?:\.|$)`),
}

// ExtractDecisions scans agent output text for decision patterns.
// Returns deduplicated decisions found via deterministic pattern matching.
func ExtractDecisions(text string) []ExtractedDecision {
	if text == "" {
		return nil
	}

	var results []ExtractedDecision
	seen := make(map[string]bool)

	for _, pat := range decisionPatterns {
		matches := pat.FindAllStringSubmatchIndex(text, -1)
		for _, loc := range matches {
			fullMatch := strings.TrimSpace(text[loc[0]:loc[1]])
			normalized := strings.ToLower(fullMatch)
			if seen[normalized] {
				continue
			}
			seen[normalized] = true
			results = append(results, ExtractedDecision{
				Text:     fullMatch,
				Pattern:  pat.String(),
				Position: loc[0],
			})
		}
	}
	return results
}
