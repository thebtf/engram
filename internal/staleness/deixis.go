// Package staleness detects temporal-deixis (relative-time language) in memory
// content. A memory that says "currently X" or "the flag is now Y" reads as an
// authoritative current fact long after the code it describes has changed — the
// silent-staleness friction (rank-3). This package powers two surfaces, both using
// ONLY live, always-populated fields (content + created_at):
//
//   - write-time advisory: store_memory warns the author when content carries
//     relative-time terms, nudging toward an absolute date / version anchor.
//   - serve-time hint: recall flags a result as a stale candidate when its content
//     carries relative-time terms AND it is older than a freshness window.
//
// It deliberately does NOT depend on the lifecycle fields (review_after, stability,
// retrievability): those are dormant (only populated under ENGRAM_LIFECYCLE_ENABLED)
// and reviving them is a separate initiative — see AGENTS.md "V5 DEMOLITION GUARD".
package staleness

import (
	"regexp"
	"time"
)

// DefaultFreshnessWindow is how long a memory carrying relative-time language is
// treated as still-fresh. Past this age, such a memory is a stale candidate: its
// "currently"/"recently" framing is old enough that the fact it asserts may have
// moved. 30 days is a deliberately conservative default — long enough not to nag on
// recent notes, short enough to catch quarter-stale code facts.
const DefaultFreshnessWindow = 30 * 24 * time.Hour

// relativeTimeRe matches relative-time ("deictic") phrases whose truth value drifts
// with the calendar. Curated to favor clearly-temporal multi-word phrases and to
// avoid high-false-positive bare words: "now" alone is excluded (it matches "known",
// "now that", and code-change narration) but its temporal compounds ("right now",
// "as of now", "for now") are included. All matching is case-insensitive on word
// boundaries. Advisory only — a false positive costs at most a gentle note, never a
// blocked write or a dropped recall result.
// Inter-word spaces are written \s+ (not a literal space) so multi-space and
// newline-separated phrases ("last   week") still match; matched text is whitespace-
// collapsed afterward for dedup.
var relativeTimeRe = regexp.MustCompile(`(?i)\b(` +
	`currently|presently|nowadays|recently|lately|` +
	`today|yesterday|tomorrow|` +
	`right\s+now|as\s+of\s+now|for\s+now|at\s+present|at\s+the\s+moment|these\s+days|` +
	`last\s+(?:week|month|year)|next\s+(?:week|month|year)|this\s+(?:week|month|year)` +
	`)\b`)

// DetectRelativeTime returns the distinct relative-time phrases found in content
// (lower-cased, first-seen order). An empty slice means none were found.
func DetectRelativeTime(content string) []string {
	matches := relativeTimeRe.FindAllString(content, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		key := normalizeWhitespaceLower(m)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

// IsStaleCandidate reports whether a memory should be flagged as a stale candidate:
// its content carries relative-time language AND it has not changed within window.
// lastChanged must be the memory's most-recent content change — the LATER of created_at
// and updated_at (use EffectiveLastChanged). Measuring from created_at alone would mark
// a just-edited old memory stale even though its content was rewritten today, telling the
// agent to distrust a freshly-corrected fact (Codex review). window <= 0 falls back to
// DefaultFreshnessWindow.
//
// This is a HEURISTIC HINT, never a hard signal — it tells the consuming agent
// "re-verify before trusting", it does not hide, drop, or down-rank the memory.
func IsStaleCandidate(content string, lastChanged, now time.Time, window time.Duration) bool {
	if window <= 0 {
		window = DefaultFreshnessWindow
	}
	// Age check first: it is a single subtraction, whereas DetectRelativeTime runs a
	// regex and allocates. Short-circuiting on fresh memories skips the scan entirely.
	if now.Sub(lastChanged) <= window {
		return false
	}
	return len(DetectRelativeTime(content)) > 0
}

// EffectiveLastChanged returns the later of createdAt and updatedAt — the moment the
// memory's content most recently changed. A zero updatedAt (never updated) falls back
// to createdAt. This is the timestamp the staleness window should measure from, so an
// old memory edited today is treated as fresh.
func EffectiveLastChanged(createdAt, updatedAt time.Time) time.Time {
	if updatedAt.After(createdAt) {
		return updatedAt
	}
	return createdAt
}

// normalizeWhitespaceLower lowercases and collapses internal whitespace runs to a
// single space so "last   week" and "last week" dedup to the same key.
func normalizeWhitespaceLower(s string) string {
	return whitespaceRe.ReplaceAllString(toLowerASCII(s), " ")
}

var whitespaceRe = regexp.MustCompile(`\s+`)

// toLowerASCII lowercases without importing strings for a single call; the regexp
// already matched case-insensitively so inputs are ASCII letters/spaces.
func toLowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
