package aaak

import "strings"

// AAKResult holds the decoded components of an AAAK line.
type AAKResult struct {
	Entities []string
	Topics   []string
	Quotes   []string
	Emotions []string
	Flags    []string
}

// Decode parses an AAAK wire format line back into its components.
// Format: {entities}|{topics}|"{key_quote}"|{emotions}|{flags}
func Decode(aaak string) (*AAKResult, error) {
	if aaak == "" {
		return &AAKResult{}, nil
	}

	parts := strings.SplitN(aaak, "|", 5)

	result := &AAKResult{}

	if len(parts) > 0 && parts[0] != "" {
		result.Entities = splitNonEmpty(parts[0], ",")
	}

	if len(parts) > 1 && parts[1] != "" {
		result.Topics = splitNonEmpty(parts[1], ",")
	}

	if len(parts) > 2 && parts[2] != "" {
		quote := strings.TrimSpace(parts[2])
		// Remove surrounding quotes if present
		quote = strings.Trim(quote, `"`)
		if quote != "" {
			result.Quotes = []string{quote}
		}
	}

	if len(parts) > 3 && parts[3] != "" {
		result.Emotions = splitNonEmpty(parts[3], ",")
	}

	if len(parts) > 4 && parts[4] != "" {
		result.Flags = splitNonEmpty(parts[4], ",")
	}

	return result, nil
}

// splitNonEmpty splits a string by separator and returns only non-empty parts.
func splitNonEmpty(s, sep string) []string {
	parts := strings.Split(s, sep)
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
