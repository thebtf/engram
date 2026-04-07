package mining

import "strings"

// Chunk represents a text chunk with position info.
type Chunk struct {
	Text   string
	Offset int
	Index  int
}

// ChunkText splits text into chunks of at most maxSize characters with overlap
// characters of context carried forward from the previous chunk. Break points
// prefer double newlines (paragraph boundaries) then single newlines, falling
// back to a hard split only when no newline exists within the window.
// Chunks shorter than 50 characters are discarded.
func ChunkText(text string, maxSize, overlap int) []Chunk {
	if maxSize <= 0 {
		maxSize = 800
	}
	if overlap < 0 {
		overlap = 100
	}

	const minChunk = 50

	var chunks []Chunk
	start := 0
	index := 0

	for start < len(text) {
		end := start + maxSize
		if end >= len(text) {
			// Last chunk — take everything remaining.
			chunk := text[start:]
			if len(strings.TrimSpace(chunk)) >= minChunk {
				chunks = append(chunks, Chunk{Text: chunk, Offset: start, Index: index})
				index++
			}
			break
		}

		// Prefer a paragraph break (\n\n) searching backwards from end.
		breakAt := findBreak(text, start, end)

		chunk := text[start:breakAt]
		if len(strings.TrimSpace(chunk)) >= minChunk {
			chunks = append(chunks, Chunk{Text: chunk, Offset: start, Index: index})
			index++
		}

		// Advance start, backing up by overlap to carry context forward.
		start = breakAt
		if overlap > 0 && start-overlap > 0 {
			start = start - overlap
		}
		// Guard: must always make forward progress.
		if start <= chunks[len(chunks)-1].Offset {
			start = breakAt
		}
	}

	return chunks
}

// findBreak returns the best character position to end a chunk, searching
// backwards from end towards start for \n\n then \n. Returns end if none found.
func findBreak(text string, start, end int) int {
	// Search window: from end back to start+1.
	window := text[start:end]

	// Try double newline first.
	if idx := strings.LastIndex(window, "\n\n"); idx >= 0 {
		return start + idx + 2 // include the blank line
	}
	// Fall back to single newline.
	if idx := strings.LastIndex(window, "\n"); idx >= 0 {
		return start + idx + 1
	}
	// Hard split.
	return end
}
