package aaak

import (
	"fmt"
	"strings"
)

// CompressMeta provides context for AAAK compression.
type CompressMeta struct {
	EntityCodes map[string]string // lowercase name → 3-char code
	Project     string
	Type        string
}

// Compress converts a narrative into AAAK wire format:
// {entities}|{topics}|"{key_quote}"|{emotions}|{flags}
//
// Target: ≥20x token compression ratio.
func Compress(narrative string, meta CompressMeta) string {
	if narrative == "" {
		return ""
	}

	// Extract components
	entities := extractEntityCodes(narrative, meta.EntityCodes)
	topics := ExtractTopics(narrative, 3)
	quote := extractKeyQuote(narrative)
	emotions := DetectEmotions(narrative)
	flags := DetectFlags(narrative)

	// Build wire format
	var parts [5]string
	parts[0] = strings.Join(entities, ",")
	parts[1] = strings.Join(topics, ",")
	if quote != "" {
		// Escape pipe chars in quote to prevent breaking wire format delimiter
		safeQuote := strings.ReplaceAll(quote, "|", "/")
		parts[2] = fmt.Sprintf("%q", safeQuote)
	}
	parts[3] = strings.Join(emotions, ",")
	parts[4] = strings.Join(flags, ",")

	return strings.Join(parts[:], "|")
}

// extractEntityCodes finds entity names in text and replaces with their codes.
func extractEntityCodes(text string, codes map[string]string) []string {
	if len(codes) == 0 {
		return nil
	}

	lower := strings.ToLower(text)
	var found []string
	seen := make(map[string]bool)

	for name, code := range codes {
		if strings.Contains(lower, name) && !seen[code] {
			seen[code] = true
			found = append(found, code)
		}
	}

	return found
}

// extractKeyQuote finds the most information-dense sentence in the text.
// Uses a simple heuristic: longest sentence with proper nouns or technical terms,
// truncated to 40 chars for token efficiency.
func extractKeyQuote(text string) string {
	sentences := strings.FieldsFunc(text, func(r rune) bool {
		return r == '.' || r == '!' || r == '?'
	})

	if len(sentences) == 0 {
		return truncate(text, 80)
	}

	best := ""
	bestScore := 0

	for _, s := range sentences {
		s = strings.TrimSpace(s)
		if len(s) < 10 {
			continue
		}

		score := len(s)
		// Boost sentences with technical indicators
		for _, indicator := range []string{":", "→", "=", "(", "API", "server", "database"} {
			if strings.Contains(s, indicator) {
				score += 20
			}
		}
		// Boost sentences with uppercase words (proper nouns, acronyms)
		words := strings.Fields(s)
		for _, w := range words {
			if len(w) >= 2 && w[0] >= 'A' && w[0] <= 'Z' {
				score += 5
			}
		}

		if score > bestScore {
			bestScore = score
			best = s
		}
	}

	return truncate(best, 40)
}

// truncate limits string to maxLen, adding "…" if truncated.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}
