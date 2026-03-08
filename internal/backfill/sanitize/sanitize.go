// Package sanitize provides text sanitization for session backfill processing.
package sanitize

import (
	"fmt"
	"regexp"
)

// Patterns for content that should be stripped before LLM processing.
var (
	// Base64Regex matches large base64/hex payloads (500+ chars).
	Base64Regex = regexp.MustCompile(`(?m)[A-Za-z0-9+/=]{500,}`)

	// SystemReminderRegex matches <system-reminder>...</system-reminder> blocks.
	SystemReminderRegex = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
)

// DefaultMaxBlockSize is the maximum size of a single text block before truncation.
const DefaultMaxBlockSize = 3000

// Text sanitizes a text block by removing system reminders, base64 payloads,
// and truncating overly long blocks.
func Text(text string) string {
	return TextWithLimit(text, DefaultMaxBlockSize)
}

// TextWithLimit sanitizes text with a custom truncation limit.
func TextWithLimit(text string, maxBlockSize int) string {
	// Strip system-reminder blocks (injected by Claude Code, not user content)
	text = SystemReminderRegex.ReplaceAllString(text, "[SYSTEM-REMINDER REMOVED]")

	// Strip large base64/hex payloads
	text = Base64Regex.ReplaceAllString(text, "[BASE64 REMOVED]")

	// Truncate very long individual text blocks (tool outputs etc.)
	if maxBlockSize > 0 && len(text) > maxBlockSize {
		headSize := maxBlockSize / 3
		tailSize := maxBlockSize / 3
		removed := len(text) - headSize - tailSize
		return text[:headSize] + fmt.Sprintf("\n... [TRUNCATED %d chars] ...\n", removed) + text[len(text)-tailSize:]
	}
	return text
}
