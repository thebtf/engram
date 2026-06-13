// Package privacy provides privacy tag handling for engram.
package privacy

import (
	"regexp"
	"strings"
)

// Tag regexes use (?s) DOTALL + non-greedy `.*?` so multi-line content between
// open and close tags is captured without consuming adjacent blocks.
// These patterns are byte-contract: changing them breaks the privacy boundary
// between user-marked private content and stored memories.
var (
	// privateTagRegex matches <private>...</private> tags
	privateTagRegex = regexp.MustCompile(`(?s)<private>.*?</private>`)

	// memoryTagRegex matches <engram-context>...</engram-context> tags
	memoryTagRegex = regexp.MustCompile(`(?s)<engram-context>.*?</engram-context>`)
)

// StripPrivateTags removes all <private>...</private> content from text.
func StripPrivateTags(text string) string {
	return privateTagRegex.ReplaceAllString(text, "")
}

// StripMemoryTags removes all <engram-context>...</engram-context> content from text.
// These tags carry previously injected context and must be stripped before the
// content is processed or stored to avoid circular re-injection.
func StripMemoryTags(text string) string {
	return memoryTagRegex.ReplaceAllString(text, "")
}

// StripAllTags removes both private and memory context tags.
func StripAllTags(text string) string {
	text = StripPrivateTags(text)
	text = StripMemoryTags(text)
	return text
}

// IsEntirelyPrivate checks if the text is entirely within <private> tags.
// Used to short-circuit storage when the entire user message is marked private.
func IsEntirelyPrivate(text string) bool {
	stripped := StripPrivateTags(text)
	return strings.TrimSpace(stripped) == ""
}

// Clean performs full privacy cleaning on text.
// This is the main function to use before storing any user content.
func Clean(text string) string {
	// Strip both types of tags
	text = StripAllTags(text)
	// Trim whitespace
	return strings.TrimSpace(text)
}
