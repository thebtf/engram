package feedback

import (
	"strings"

	"github.com/thebtf/engram/pkg/models"
)

// CitationResult describes whether a single memory was cited in agent output.
type CitationResult struct {
	MemoryID  int64
	Cited     bool
	MatchType string // "title", "keyword", "both", "" (uncited)
}

// DetectCitations checks which injected memories appear in the agent's output text.
// Citation is detected via:
//
//	(a) Exact title match: memory content's first line (used as title) appears in output.
//	(b) Keyword match: ≥2 of the memory's tags appear in the same assistant turn.
//
// A memory is "cited" if either condition is met.
func DetectCitations(agentOutput string, memories []*models.Memory) []CitationResult {
	if agentOutput == "" || len(memories) == 0 {
		return nil
	}

	outputLower := strings.ToLower(agentOutput)
	results := make([]CitationResult, len(memories))

	for i, mem := range memories {
		result := CitationResult{MemoryID: mem.ID}

		titleMatch := matchTitle(mem.Content, outputLower)
		keywordMatch := matchKeywords(mem.Tags, outputLower)

		switch {
		case titleMatch && keywordMatch:
			result.Cited = true
			result.MatchType = "both"
		case titleMatch:
			result.Cited = true
			result.MatchType = "title"
		case keywordMatch:
			result.Cited = true
			result.MatchType = "keyword"
		}

		results[i] = result
	}

	return results
}

// matchTitle checks if the memory's title (first line of content) appears in the output.
func matchTitle(content, outputLower string) bool {
	title := extractTitle(content)
	if title == "" || len(title) < 5 {
		return false // skip very short titles to avoid false positives
	}
	return strings.Contains(outputLower, strings.ToLower(title))
}

// extractTitle returns the first line of content, trimmed, as the memory's title.
func extractTitle(content string) string {
	if idx := strings.IndexByte(content, '\n'); idx >= 0 {
		return strings.TrimSpace(content[:idx])
	}
	return strings.TrimSpace(content)
}

// matchKeywords checks if ≥2 of the memory's tags appear in the output text.
func matchKeywords(tags []string, outputLower string) bool {
	if len(tags) < 2 {
		return false
	}
	matches := 0
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" || len(tag) < 3 {
			continue // skip very short tags
		}
		if strings.Contains(outputLower, tag) {
			matches++
			if matches >= 2 {
				return true
			}
		}
	}
	return false
}
