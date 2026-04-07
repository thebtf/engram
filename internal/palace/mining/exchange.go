package mining

import "strings"

// Exchange represents one user→AI turn pair.
type Exchange struct {
	UserTurn string
	AITurn   string
	Index    int
}

const maxAIParagraphs = 8

// turnSegment is an internal type used during exchange extraction.
type turnSegment struct {
	role    string // "user" or "ai"
	content string
}

// ExtractExchanges detects turn markers according to the detected Format and
// groups them into user→AI Exchange pairs. Unknown or Generic format falls back
// to paragraph-based splitting (odd paragraphs = user, even = AI).
func ExtractExchanges(text string, format Format) []Exchange {
	switch format {
	case FormatClaude:
		return extractMarkerExchanges(text, "Human:", "Assistant:")
	case FormatChatGPT:
		return extractMarkerExchanges(text, "User:", "ChatGPT:")
	case FormatQuote:
		return extractQuoteExchanges(text)
	default:
		return extractGenericExchanges(text)
	}
}

// extractMarkerExchanges splits text on userMarker / aiMarker prefixes and
// pairs consecutive segments into exchanges.
func extractMarkerExchanges(text, userMarker, aiMarker string) []Exchange {
	lines := strings.Split(text, "\n")
	var segments []turnSegment
	var current *turnSegment

	flush := func() {
		if current != nil && strings.TrimSpace(current.content) != "" {
			segments = append(segments, *current)
			current = nil
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, userMarker):
			flush()
			tail := strings.TrimPrefix(trimmed, userMarker)
			current = &turnSegment{role: "user", content: strings.TrimSpace(tail) + "\n"}
		case strings.HasPrefix(trimmed, aiMarker):
			flush()
			tail := strings.TrimPrefix(trimmed, aiMarker)
			current = &turnSegment{role: "ai", content: strings.TrimSpace(tail) + "\n"}
		default:
			if current != nil {
				current.content += line + "\n"
			}
		}
	}
	flush()

	return pairSegments(segments)
}

// pairSegments merges adjacent same-role segments then groups them into
// user→ai Exchange pairs.
func pairSegments(segments []turnSegment) []Exchange {
	// Merge adjacent same-role segments.
	merged := make([]turnSegment, 0, len(segments))
	for _, s := range segments {
		if len(merged) > 0 && merged[len(merged)-1].role == s.role {
			merged[len(merged)-1].content += s.content
		} else {
			merged = append(merged, s)
		}
	}

	var exchanges []Exchange
	for i := 0; i+1 < len(merged); i++ {
		u := merged[i]
		a := merged[i+1]
		if u.role != "user" || a.role != "ai" {
			continue
		}
		exchanges = append(exchanges, Exchange{
			UserTurn: strings.TrimSpace(u.content),
			AITurn:   capParagraphs(strings.TrimSpace(a.content), maxAIParagraphs),
			Index:    len(exchanges),
		})
		i++ // skip the AI segment we just consumed
	}
	return exchanges
}

// extractQuoteExchanges treats lines starting with ">" as user turns and
// non-quoted runs as AI turns, then pairs them.
func extractQuoteExchanges(text string) []Exchange {
	lines := strings.Split(text, "\n")
	var segments []turnSegment
	var current *turnSegment

	flush := func() {
		if current != nil && strings.TrimSpace(current.content) != "" {
			segments = append(segments, *current)
			current = nil
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		role := "ai"
		if strings.HasPrefix(trimmed, ">") {
			role = "user"
		}
		if current == nil || current.role != role {
			flush()
			current = &turnSegment{role: role}
		}
		current.content += line + "\n"
	}
	flush()

	return pairSegments(segments)
}

// extractGenericExchanges splits text into paragraphs (double-newline) and
// treats odd-indexed paragraphs as user turns and even-indexed as AI turns.
func extractGenericExchanges(text string) []Exchange {
	paragraphs := splitParagraphs(text)
	var exchanges []Exchange
	for i := 0; i+1 < len(paragraphs); i += 2 {
		user := strings.TrimSpace(paragraphs[i])
		ai := strings.TrimSpace(paragraphs[i+1])
		if user == "" && ai == "" {
			continue
		}
		exchanges = append(exchanges, Exchange{
			UserTurn: user,
			AITurn:   capParagraphs(ai, maxAIParagraphs),
			Index:    len(exchanges),
		})
	}
	return exchanges
}

// splitParagraphs splits text on blank lines, discarding empty paragraphs.
func splitParagraphs(text string) []string {
	raw := strings.Split(text, "\n\n")
	result := make([]string, 0, len(raw))
	for _, p := range raw {
		if t := strings.TrimSpace(p); t != "" {
			result = append(result, t)
		}
	}
	return result
}

// capParagraphs limits AI text to the first n paragraphs.
func capParagraphs(text string, n int) string {
	paragraphs := strings.Split(text, "\n\n")
	if len(paragraphs) <= n {
		return text
	}
	return strings.Join(paragraphs[:n], "\n\n")
}
