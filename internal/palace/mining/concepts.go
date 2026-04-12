package mining

import "strings"

// DefaultBuckets returns the built-in topic keyword buckets.
func DefaultBuckets() map[string][]string {
	return map[string][]string{
		"technical":    {"code", "api", "database", "server", "deploy", "docker", "kubernetes"},
		"architecture": {"design", "pattern", "module", "component", "layer", "service"},
		"planning":     {"roadmap", "sprint", "milestone", "deadline", "priority", "backlog"},
		"decisions":    {"decided", "chose", "trade-off", "alternative", "evaluate"},
		"problems":     {"bug", "issue", "error", "crash", "regression", "broken"},
	}
}

// DetectConcepts scores text against the provided topic buckets and returns
// the names of the top-2 buckets with at least one keyword match.
// Scoring is a simple count of keyword occurrences in the lowercased text.
func DetectConcepts(text string, buckets map[string][]string) []string {
	lower := strings.ToLower(text)

	type scored struct {
		name  string
		score int
	}
	var scores []scored

	for name, keywords := range buckets {
		count := 0
		for _, kw := range keywords {
			// Count all occurrences of the keyword.
			count += countOccurrences(lower, kw)
		}
		if count > 0 {
			scores = append(scores, scored{name: name, score: count})
		}
	}

	// Sort descending by score (insertion sort — bucket count is always small).
	for i := 1; i < len(scores); i++ {
		for j := i; j > 0 && scores[j].score > scores[j-1].score; j-- {
			scores[j], scores[j-1] = scores[j-1], scores[j]
		}
	}

	// Return top-2 names.
	top := 2
	if len(scores) < top {
		top = len(scores)
	}
	result := make([]string, top)
	for i := 0; i < top; i++ {
		result[i] = scores[i].name
	}
	return result
}

// countOccurrences counts non-overlapping occurrences of substr in s.
func countOccurrences(s, substr string) int {
	if substr == "" {
		return 0
	}
	count := 0
	start := 0
	for {
		idx := strings.Index(s[start:], substr)
		if idx < 0 {
			break
		}
		count++
		start += idx + len(substr)
	}
	return count
}
