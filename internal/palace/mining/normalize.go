package mining

import (
	"regexp"
	"strings"
)

// Format represents detected conversation format.
type Format int

const (
	FormatGeneric Format = iota
	FormatClaude          // Human:/Assistant:
	FormatChatGPT         // User:/ChatGPT:
	FormatQuote           // > markers
)

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// Normalize strips ANSI escape codes, normalizes line endings, collapses
// runs of 3+ blank lines to 2, and detects the conversation format by
// scanning the first 50 lines for known turn markers.
func Normalize(text string) (string, Format) {
	// Strip ANSI escape sequences.
	clean := ansiEscape.ReplaceAllString(text, "")

	// Normalize Windows line endings.
	clean = strings.ReplaceAll(clean, "\r\n", "\n")

	// Collapse 3+ consecutive blank lines to 2.
	for strings.Contains(clean, "\n\n\n") {
		clean = strings.ReplaceAll(clean, "\n\n\n", "\n\n")
	}

	// Detect format from first 50 lines.
	format := detectFormat(clean)

	return clean, format
}

func detectFormat(text string) Format {
	lines := strings.Split(text, "\n")
	if len(lines) > 50 {
		lines = lines[:50]
	}

	var hasHuman, hasAssistant, hasUser, hasChatGPT, hasQuote bool
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Human:"):
			hasHuman = true
		case strings.HasPrefix(trimmed, "Assistant:"):
			hasAssistant = true
		case strings.HasPrefix(trimmed, "User:"):
			hasUser = true
		case strings.HasPrefix(trimmed, "ChatGPT:"):
			hasChatGPT = true
		case strings.HasPrefix(trimmed, ">"):
			hasQuote = true
		}
	}

	switch {
	case hasHuman && hasAssistant:
		return FormatClaude
	case hasUser && hasChatGPT:
		return FormatChatGPT
	case hasQuote:
		return FormatQuote
	default:
		return FormatGeneric
	}
}
