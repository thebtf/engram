package codeindex

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// ChunkFile splits content into non-overlapping line-blocks and returns the
// resulting Chunk slice. filePath must be a forward-slash-normalized,
// repository-relative path (used verbatim in Chunk.FilePath and ChunkID).
//
// Contract:
//   - All chunks together cover every byte of content exactly once.
//   - ChunkType is always ChunkTypeLineBlock.
//   - Chunk.Content is always valid UTF-8 (the char-cap split finds a rune boundary).
//   - Minified / generated files (see Options) return (nil, nil) — not an error.
//   - Binary content (NUL byte) or non-UTF-8 content returns (nil, nil) — not an
//     error. This makes ChunkFile self-guarding so CR-003's direct delta callers
//     get the same protection BuildManifest applies before walking.
//   - Empty files return an empty (not nil) slice.
func ChunkFile(filePath string, content []byte, opts Options) ([]Chunk, error) {
	if opts.LinesPerBlock <= 0 {
		opts.LinesPerBlock = DefaultOptions().LinesPerBlock
	}
	if opts.MaxChunkBytes <= 0 {
		opts.MaxChunkBytes = DefaultOptions().MaxChunkBytes
	}
	// Rune-safe split needs at least one full max-width rune of headroom; below
	// utf8.UTFMax the char-cap walk-back could strand a lone leading byte.
	if opts.MaxChunkBytes < utf8.UTFMax {
		opts.MaxChunkBytes = utf8.UTFMax
	}

	lang := DetectLanguage(filePath)

	if len(content) == 0 {
		return []Chunk{}, nil
	}

	// Self-guard: NUL-byte binaries and non-UTF-8 content (Latin-1, truncated
	// multi-byte, etc.) are skipped here, not just in BuildManifest, so the
	// "Content is always valid UTF-8" contract holds for every caller — including
	// CR-003 calling ChunkFile directly on the negotiation delta. Without this,
	// the char-cap pathological path could emit an invalid-UTF-8 chunk and corrupt
	// downstream embedding.
	if isBinaryContent(content) || !utf8.Valid(content) {
		return nil, nil
	}

	// Minified-name heuristic: ".min." anywhere in the base filename.
	base := filePath
	if idx := strings.LastIndex(filePath, "/"); idx >= 0 {
		base = filePath[idx+1:]
	}
	if strings.Contains(base, ".min.") {
		return nil, nil
	}

	// Compute per-line metrics for the minified-content heuristics.
	// We scan once to find line boundaries, which we reuse for chunking.
	lineStarts := findLineStarts(content)
	numLines := len(lineStarts)

	if numLines > 0 {
		// Average line length heuristic.
		avgLineLen := len(content) / numLines
		if avgLineLen > opts.MinifiedAvgLineLen {
			return nil, nil
		}

		// Single longest-line heuristic: scan lineStarts to find the max.
		for i, start := range lineStarts {
			var end int
			if i+1 < numLines {
				end = lineStarts[i+1]
			} else {
				end = len(content)
			}
			lineLen := end - start
			// Strip the trailing newline bytes from the length measurement.
			for lineLen > 0 && (content[start+lineLen-1] == '\n' || content[start+lineLen-1] == '\r') {
				lineLen--
			}
			if lineLen > opts.MinifiedSingleLineBytes {
				return nil, nil
			}
		}
	}

	// Produce line-blocks.
	var chunks []Chunk

	lineIdx := 0
	for lineIdx < numLines {
		blockStart := lineStarts[lineIdx]

		endLineIdx := lineIdx + opts.LinesPerBlock
		if endLineIdx > numLines {
			endLineIdx = numLines
		}

		var blockEnd int
		if endLineIdx < numLines {
			blockEnd = lineStarts[endLineIdx]
		} else {
			blockEnd = len(content)
		}

		// Apply the byte-cap guard: if the block exceeds MaxChunkBytes, split
		// it at a UTF-8 rune boundary at opts.MaxChunkBytes.
		for blockStart < blockEnd {
			segEnd := blockEnd
			if segEnd-blockStart > opts.MaxChunkBytes {
				segEnd = blockStart + opts.MaxChunkBytes
				// Walk back to a rune boundary (utf8.RuneStart reports whether
				// the byte at index i is the start of a rune).
				for segEnd > blockStart && !utf8.RuneStart(content[segEnd]) {
					segEnd--
				}
				if segEnd == blockStart {
					// Pathological: single byte that is not a rune start.
					// Advance one byte to avoid an infinite loop.
					segEnd = blockStart + 1
				}
			}

			seg := content[blockStart:segEnd]
			chunks = append(chunks, Chunk{
				FilePath:      filePath,
				ByteStart:     blockStart,
				ByteEnd:       segEnd,
				Language:      lang,
				ChunkType:     ChunkTypeLineBlock,
				Content:       string(seg),
				ContentSHA256: contentSHA256Hex(seg),
			})

			blockStart = segEnd
			if blockStart >= blockEnd {
				break
			}
			// If the cap split us mid-block, keep accumulating lines into the
			// next segment up to the original blockEnd.
		}

		lineIdx = endLineIdx
	}

	return chunks, nil
}

// findLineStarts returns the byte offset of the first byte of each line in b.
// The first element is always 0. Lines are separated by '\n'; '\r\n' sequences
// are treated as a single newline (the '\r' remains in the line content).
func findLineStarts(b []byte) []int {
	if len(b) == 0 {
		return nil
	}
	starts := []int{0}
	for i, c := range b {
		if c == '\n' && i+1 < len(b) {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// isBinaryContent returns true if b contains a NUL byte within the first 8 KB,
// which is a reliable heuristic for binary (non-text) files.
func isBinaryContent(b []byte) bool {
	probe := b
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	return bytes.IndexByte(probe, 0) >= 0
}
