// Package crystallization provides session-end decision and pattern extraction.
package crystallization

import (
	"fmt"
	"strings"
	"time"
)

// DigestMode controls how BuildDigest formats a set of transcripts.
type DigestMode string

const (
	// ModePerSession produces a labeled block for a single transcript.
	// Used for short single-agent sessions.
	ModePerSession DigestMode = "per-session"

	// ModePerBatch concatenates multiple transcripts with separators into one digest.
	// Used when multiple sessions, a long time span, or a shared project are detected.
	ModePerBatch DigestMode = "per-batch"
)

// SelectMode returns the appropriate DigestMode for the given context.
//
// Thresholds (documented here for transparency):
//   - sessionCount > 1  → ModePerBatch   (multiple sessions share state)
//   - span > 24h        → ModePerBatch   (long sessions accumulate more context)
//   - sharedProject     → ModePerBatch   (project shared across agents)
//   - otherwise         → ModePerSession
func SelectMode(sessionCount int, span time.Duration, sharedProject bool) DigestMode {
	if sessionCount > 1 || span > 24*time.Hour || sharedProject {
		return ModePerBatch
	}
	return ModePerSession
}

// BuildDigest assembles transcripts into a single digest string for the extractor.
//
// ModePerSession: formats the first (and typically only) transcript as a labeled
// "SESSION" block. Additional transcripts beyond the first are appended as additional
// blocks using the same labeling scheme, so the function remains safe when called
// with multiple transcripts in this mode.
//
// ModePerBatch: concatenates all transcripts separated by a batch-separator line,
// allowing the LLM to see the full multi-session context at once.
//
// Both modes produce DISTINCT output for the same input set (assertable in tests).
func BuildDigest(transcripts []string, mode DigestMode) string {
	if len(transcripts) == 0 {
		return ""
	}

	switch mode {
	case ModePerBatch:
		const sep = "\n--- BATCH_SEPARATOR ---\n"
		var sb strings.Builder
		sb.WriteString("=== BATCH DIGEST ===\n")
		for i, t := range transcripts {
			if i > 0 {
				sb.WriteString(sep)
			}
			sb.WriteString(fmt.Sprintf("[TRANSCRIPT %d]\n", i+1))
			sb.WriteString(t)
			sb.WriteString("\n")
		}
		return sb.String()

	default: // ModePerSession
		var sb strings.Builder
		sb.WriteString("=== SESSION DIGEST ===\n")
		for i, t := range transcripts {
			sb.WriteString(fmt.Sprintf("[SESSION %d]\n", i+1))
			sb.WriteString(t)
			sb.WriteString("\n")
		}
		return sb.String()
	}
}
