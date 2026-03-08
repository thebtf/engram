// Package chunk provides exchange-aware chunking for session backfill.
package chunk

import (
	"fmt"
	"strings"

	"github.com/thebtf/engram/internal/backfill/sanitize"
	"github.com/thebtf/engram/internal/sessions"
)

// Default chunking parameters.
const (
	DefaultMaxChunkChars    = 120000 // ~30K tokens at 4 chars/token
	DefaultOverlapExchanges = 3      // Overlap between chunks for context continuity
)

// Chunk represents a contiguous range of exchanges from a session.
type Chunk struct {
	StartExchange int    // 1-indexed for display
	EndExchange   int    // inclusive
	Text          string // sanitized, concatenated exchange text
}

// Exchanges splits session exchanges into overlapping chunks that fit within maxChunkChars.
// Each chunk includes sanitized user and assistant text with exchange numbering.
func Exchanges(exchanges []sessions.Exchange, maxChunkChars, overlapExchanges int) []Chunk {
	if len(exchanges) == 0 {
		return nil
	}

	if maxChunkChars <= 0 {
		maxChunkChars = DefaultMaxChunkChars
	}
	if overlapExchanges < 0 {
		overlapExchanges = DefaultOverlapExchanges
	}

	// Build sanitized exchange texts
	type exText struct {
		text string
		size int
	}
	exTexts := make([]exText, len(exchanges))
	for i, ex := range exchanges {
		var buf strings.Builder
		buf.WriteString(fmt.Sprintf("--- Exchange %d ---\nUSER:\n", i+1))
		buf.WriteString(sanitize.Text(ex.UserText))
		buf.WriteString("\nASSISTANT:\n")
		buf.WriteString(sanitize.Text(ex.AssistantText))
		buf.WriteString("\n\n")
		t := buf.String()
		exTexts[i] = exText{text: t, size: len(t)}
	}

	var chunks []Chunk
	start := 0

	for start < len(exTexts) {
		var buf strings.Builder
		end := start

		for end < len(exTexts) {
			if buf.Len()+exTexts[end].size > maxChunkChars && end > start {
				break
			}
			buf.WriteString(exTexts[end].text)
			end++
		}

		chunks = append(chunks, Chunk{
			StartExchange: start + 1, // 1-indexed for display
			EndExchange:   end,
			Text:          buf.String(),
		})

		// Advance with overlap
		next := end - overlapExchanges
		if next <= start {
			next = end // Prevent infinite loop on very large exchanges
		}
		start = next
	}

	return chunks
}
