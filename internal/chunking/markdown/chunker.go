// Package markdown provides a smart markdown chunker using scored break points.
// Chunks are delimited by headings, code fences, horizontal rules, and blank
// lines. Each candidate break point is scored by its semantic weight and by
// how close it falls to an ideal chunk boundary (a multiple of targetSize
// lines), so that chunks stay roughly uniform in length.
package markdown

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/thebtf/claude-mnemonic-plus/internal/chunking"
)

// LanguageMarkdown is the Language constant for Markdown files.
const LanguageMarkdown chunking.Language = "markdown"

// ChunkTypeSection represents a heading-delimited markdown section.
const ChunkTypeSection chunking.ChunkType = "section"

// Break-type base scores. Higher score = stronger preference for splitting here.
const (
	scoreH1        = 100
	scoreH2        = 90
	scoreH3        = 80
	scoreCodeFence = 80
	scoreHR        = 60
	scoreBlank     = 20
)

// defaultTargetSize is the default target chunk size measured in lines.
const defaultTargetSize = 80

// breakPoint pairs a line index (0-based) with its effective score.
type breakPoint struct {
	line  int
	score float64
}

// scanBreakPoints scans lines and returns candidate break points.
//
// Scoring formula (squared-distance decay):
//
//	effectiveScore = baseScore * max(1 - (dist/window)^2 * 0.7, 0.3)
//
// where:
//   - dist   = distance to the nearest ideal chunk boundary (a multiple of targetSize)
//   - window = targetSize / 2
//
// Code fences toggle an "inside-fence" flag — no break points are emitted for
// lines that fall inside a fence block. The closing fence line itself is
// emitted as a break candidate.
func scanBreakPoints(lines []string, targetSize int) []breakPoint {
	if targetSize <= 0 {
		targetSize = defaultTargetSize
	}
	window := float64(targetSize) / 2.0

	var points []breakPoint
	inCodeFence := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Toggle code-fence state on opening/closing delimiters.
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inCodeFence = !inCodeFence
			if !inCodeFence {
				// Closing fence — emit it as a break candidate.
				points = append(points, breakPoint{
					line:  i,
					score: applyDecay(float64(scoreCodeFence), i, targetSize, window),
				})
			}
			continue
		}

		// Nothing inside a fence is a break candidate.
		if inCodeFence {
			continue
		}

		var baseScore float64
		switch {
		case strings.HasPrefix(trimmed, "# "):
			baseScore = scoreH1
		case strings.HasPrefix(trimmed, "## "):
			baseScore = scoreH2
		case strings.HasPrefix(trimmed, "### "),
			strings.HasPrefix(trimmed, "#### "),
			strings.HasPrefix(trimmed, "##### "),
			strings.HasPrefix(trimmed, "###### "):
			baseScore = scoreH3
		case trimmed == "---" || trimmed == "***" || trimmed == "___":
			baseScore = scoreHR
		case trimmed == "":
			baseScore = scoreBlank
		default:
			continue
		}

		points = append(points, breakPoint{
			line:  i,
			score: applyDecay(baseScore, i, targetSize, window),
		})
	}

	return points
}

// applyDecay returns baseScore reduced by a squared-distance factor.
//
// The distance is measured from lineIdx to the nearest ideal boundary
// (a positive multiple of targetSize). The result is clamped to at least
// 30 % of the base score so that even poorly-positioned breaks retain
// some preference value.
func applyDecay(baseScore float64, lineIdx, targetSize int, window float64) float64 {
	if window <= 0 {
		return baseScore
	}
	remainder := lineIdx % targetSize
	dist := float64(remainder)
	if remainder > targetSize/2 {
		dist = float64(targetSize - remainder)
	}
	decay := 1.0 - math.Pow(dist/window, 2)*0.7
	if decay < 0.3 {
		decay = 0.3
	}
	return baseScore * decay
}

// Chunker implements chunking.Chunker for Markdown files.
type Chunker struct {
	options chunking.ChunkOptions
}

// NewChunker creates a new Markdown chunker with the given options.
func NewChunker(options chunking.ChunkOptions) *Chunker {
	return &Chunker{options: options}
}

// Language returns the language this chunker supports.
func (c *Chunker) Language() chunking.Language {
	return LanguageMarkdown
}

// SupportedExtensions returns file extensions handled by this chunker.
func (c *Chunker) SupportedExtensions() []string {
	return []string{".md", ".mdx"}
}

// Chunk reads a markdown file from disk and returns semantic chunks.
func (c *Chunker) Chunk(ctx context.Context, filePath string) ([]chunking.Chunk, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	return c.ChunkContent(ctx, filePath, string(data))
}

// ChunkContent chunks markdown content supplied as a string.
// This is useful for content-addressable storage where the raw bytes are
// already available without a round-trip through the filesystem.
func (c *Chunker) ChunkContent(ctx context.Context, filePath, content string) ([]chunking.Chunk, error) {
	if content == "" {
		return nil, nil
	}

	lines := splitLines(content)

	targetSize := defaultTargetSize
	if c.options.MaxChunkSize > 0 {
		// Estimate ~80 characters per line to convert a byte budget to a line budget.
		targetSize = c.options.MaxChunkSize / 80
		if targetSize < 10 {
			targetSize = 10
		}
	}

	points := scanBreakPoints(lines, targetSize)

	// Short documents with no break candidates are returned as a single chunk.
	if len(points) == 0 {
		name := extractHeading(lines)
		if name == "" {
			name = "untitled-1"
		}
		return []chunking.Chunk{{
			FilePath:  filePath,
			Language:  LanguageMarkdown,
			Type:      ChunkTypeSection,
			Name:      name,
			Content:   content,
			StartLine: 1,
			EndLine:   len(lines),
		}}, nil
	}

	// Greedily select the highest-scoring break points that are at least
	// targetSize/2 lines apart from each other.
	selected := selectBreaks(points, len(lines), targetSize)

	// Materialise chunks from the selected split positions.
	var chunks []chunking.Chunk
	start := 0
	chunkIdx := 0

	for _, bp := range selected {
		if bp <= start {
			continue
		}
		chunks = append(chunks, buildChunk(lines, start, bp, filePath, chunkIdx))
		start = bp
		chunkIdx++
	}

	// Final chunk: from the last split to end of file.
	if start < len(lines) {
		chunks = append(chunks, buildChunk(lines, start, len(lines), filePath, chunkIdx))
	}

	return chunks, nil
}

// selectBreaks picks line indices at which to split the document.
// It sorts all candidate break points by score (descending) and greedily
// selects those that are at least minGap lines away from any already-selected
// break. The result is returned sorted in ascending line order.
func selectBreaks(points []breakPoint, totalLines, targetSize int) []int {
	if len(points) == 0 {
		return nil
	}

	minGap := targetSize / 2
	if minGap < 5 {
		minGap = 5
	}

	// Copy so we can sort without mutating the caller's slice.
	sorted := make([]breakPoint, len(points))
	copy(sorted, points)

	// Insertion sort by score descending; ties broken by line ascending.
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].score > sorted[i].score ||
				(sorted[j].score == sorted[i].score && sorted[j].line < sorted[i].line) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	var selected []int
	for _, bp := range sorted {
		tooClose := false
		for _, s := range selected {
			if absInt(bp.line-s) < minGap {
				tooClose = true
				break
			}
		}
		if tooClose {
			continue
		}
		selected = append(selected, bp.line)
	}

	// Sort selected indices in ascending line order.
	for i := 0; i < len(selected); i++ {
		for j := i + 1; j < len(selected); j++ {
			if selected[j] < selected[i] {
				selected[i], selected[j] = selected[j], selected[i]
			}
		}
	}

	return selected
}

// buildChunk constructs a Chunk from lines[start:end] (0-based, exclusive end).
// StartLine and EndLine in the returned Chunk are 1-based.
func buildChunk(lines []string, start, end int, filePath string, idx int) chunking.Chunk {
	chunkLines := lines[start:end]
	name := extractHeading(chunkLines)
	if name == "" {
		name = fmt.Sprintf("untitled-%d", idx+1)
	}
	return chunking.Chunk{
		FilePath:  filePath,
		Language:  LanguageMarkdown,
		Type:      ChunkTypeSection,
		Name:      name,
		Content:   strings.Join(chunkLines, "\n"),
		StartLine: start + 1, // convert to 1-based
		EndLine:   end,       // end is already inclusive (last line index + 1 = 1-based last line)
	}
}

// extractHeading returns the text of the first ATX heading found in lines.
// It checks H1 through H4 in descending priority and returns the first match.
func extractHeading(lines []string) string {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, prefix := range []string{"# ", "## ", "### ", "#### "} {
			if strings.HasPrefix(trimmed, prefix) {
				return strings.TrimPrefix(trimmed, prefix)
			}
		}
	}
	return ""
}

// splitLines splits content into individual lines using bufio.Scanner.
// Empty lines are preserved; the trailing newline (if any) does not produce
// an extra empty element.
func splitLines(content string) []string {
	scanner := bufio.NewScanner(strings.NewReader(content))
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
