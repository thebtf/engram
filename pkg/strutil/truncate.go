// Package strutil provides common string utility functions shared across engram packages.
package strutil

import "strings"

// Truncate truncates s to maxLen characters, appending "..." if truncated.
// The returned string may be up to maxLen+3 characters long.
func Truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// TruncateTrimmed trims whitespace from s before truncating.
func TruncateTrimmed(s string, maxLen int) string {
	return Truncate(strings.TrimSpace(s), maxLen)
}

// ContainsAny reports whether s contains any of the given substrings.
func ContainsAny(s string, substrs []string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
