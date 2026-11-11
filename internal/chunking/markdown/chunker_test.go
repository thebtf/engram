package markdown

import (
	"context"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/chunking"
)

// newChunker returns a Chunker with sensible defaults for tests.
func newChunker() *Chunker {
	return NewChunker(chunking.DefaultChunkOptions())
}

// TestChunkContentEmpty verifies that empty input returns nil chunks without error.
func TestChunkContentEmpty(t *testing.T) {
	c := newChunker()
	chunks, err := c.ChunkContent(context.Background(), "test.md", "")
	if err != nil {
		t.Fatalf("expected no error for empty content, got: %v", err)
	}
	if chunks != nil {
		t.Errorf("expected nil chunks for empty content, got %d chunk(s)", len(chunks))
	}
}

// TestChunkContentSingleSection verifies that a document with a single heading
// and body text is returned as exactly one chunk named after the heading.
func TestChunkContentSingleSection(t *testing.T) {
	content := `# Introduction

This is the introduction section.
It contains several sentences that form a paragraph.
`
	c := newChunker()
	chunks, err := c.ChunkContent(context.Background(), "doc.md", content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected exactly one chunk, got %d: %v", len(chunks), chunkNames(chunks))
	}
	ch := chunks[0]
	if ch.Name != "Introduction" || ch.Language != LanguageMarkdown || ch.Type != ChunkTypeSection || ch.FilePath != "doc.md" {
		t.Errorf("unexpected chunk metadata: %+v", ch)
	}
	if ch.StartLine != 1 || ch.EndLine != 4 {
		t.Errorf("expected lines 1–4, got %d–%d", ch.StartLine, ch.EndLine)
	}
	if want := strings.TrimSuffix(content, "\n"); ch.Content != want {
		t.Errorf("chunk content = %q, want %q", ch.Content, want)
	}
}

// TestChunkContentMultipleSections verifies that a document with three clearly
// separated H1 sections produces multiple chunks, each named after its heading.
func TestChunkContentMultipleSections(t *testing.T) {
	// Build a document where each section is long enough to trigger a split.
	// Using a small MaxChunkSize so that the chunker is forced to split even
	// in a unit-test context (targetSize = MaxChunkSize/80, minimum 10 lines).
	var sb strings.Builder
	sections := []struct {
		heading string
		lines   int
	}{
		{"Alpha", 15},
		{"Beta", 15},
		{"Gamma", 15},
	}
	for _, s := range sections {
		sb.WriteString("# " + s.heading + "\n\n")
		for i := 0; i < s.lines; i++ {
			sb.WriteString("Content line for " + s.heading + ".\n")
		}
		sb.WriteString("\n")
	}
	content := sb.String()

	// Use a MaxChunkSize that maps to a targetSize of ~10 lines (10*80=800 bytes).
	c := NewChunker(chunking.ChunkOptions{
		MaxChunkSize:       800,
		IncludeDocComments: true,
		IncludePrivate:     true,
	})

	chunks, err := c.ChunkContent(context.Background(), "multi.md", content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for a 3-section document, got %d", len(chunks))
	}

	want := strings.TrimSuffix(content, "\n")
	lines := strings.Split(want, "\n")
	nameSet := make(map[string]bool)
	contents := make([]string, 0, len(chunks))
	nextLine := 1
	for _, ch := range chunks {
		nameSet[ch.Name] = true
		if ch.StartLine != nextLine || ch.EndLine < ch.StartLine {
			t.Errorf("non-contiguous or invalid chunk range: got %d–%d, expected start %d", ch.StartLine, ch.EndLine, nextLine)
		}
		if ch.StartLine >= 1 && ch.EndLine >= ch.StartLine && ch.EndLine <= len(lines) {
			if expected := strings.Join(lines[ch.StartLine-1:ch.EndLine], "\n"); ch.Content != expected {
				t.Errorf("content does not match lines %d–%d", ch.StartLine, ch.EndLine)
			}
		} else {
			t.Errorf("chunk range %d–%d outside document of %d lines", ch.StartLine, ch.EndLine, len(lines))
		}
		nextLine = ch.EndLine + 1
		contents = append(contents, ch.Content)
	}
	for _, s := range sections {
		if !nameSet[s.heading] {
			t.Errorf("missing section %q; names: %v", s.heading, chunkNames(chunks))
		}
	}
	if got := strings.Join(contents, "\n"); got != want {
		t.Errorf("reassembled content = %q, want %q", got, want)
	}
	if wantEnd := len(lines); nextLine != wantEnd+1 {
		t.Errorf("final line = %d, want %d", nextLine-1, wantEnd)
	}
}

// TestCodeFenceGuard verifies that headings inside a code fence do NOT create
// a break point, i.e. the fence content is treated as opaque text.
func TestCodeFenceGuard(t *testing.T) {
	for _, fence := range []string{"```", "~~~"} {
		t.Run(fence, func(t *testing.T) {
			lines := []string{
				"# Before", "", fence + "markdown", "## Inside", "", fence, "## After",
			}
			points := scanBreakPoints(lines, 20)
			lineSet := make(map[int]bool)
			for _, bp := range points {
				lineSet[bp.line] = true
			}
			for _, line := range []int{0, 1, 5, 6} {
				if !lineSet[line] {
					t.Errorf("expected break point at line %d, got %v", line, points)
				}
			}
			for _, line := range []int{2, 3, 4} {
				if lineSet[line] {
					t.Errorf("unexpected break point at fence line %d: %v", line, points)
				}
			}
		})
	}
}

// TestSupportedExtensions verifies that the chunker declares ".md" and ".mdx".
func TestSupportedExtensions(t *testing.T) {
	c := newChunker()
	exts := c.SupportedExtensions()

	want := map[string]bool{".md": true, ".mdx": true}
	if len(exts) != len(want) {
		t.Errorf("expected exactly %d extensions, got %v", len(want), exts)
	}
	for _, ext := range exts {
		if !want[ext] {
			t.Errorf("unexpected or duplicate extension %q in %v", ext, exts)
		}
		delete(want, ext)
	}
	for ext := range want {
		t.Errorf("missing extension %q in %v", ext, exts)
	}
}

// TestLanguage verifies that Language() returns the markdown constant.
func TestLanguage(t *testing.T) {
	c := newChunker()
	if c.Language() != LanguageMarkdown {
		t.Errorf("expected Language() == %q, got %q", LanguageMarkdown, c.Language())
	}
}

// TestResolveHeading verifies that extractHeading finds the first heading in a
// slice of lines and that the "#" prefix is stripped correctly.
func TestResolveHeading(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		expected string
	}{
		{
			name:     "H1 at start",
			lines:    []string{"# My Title", "", "body text"},
			expected: "My Title",
		},
		{
			name:     "H2 first",
			lines:    []string{"## Section One", "content"},
			expected: "Section One",
		},
		{
			name:     "H3 only",
			lines:    []string{"### Sub-section", "content"},
			expected: "Sub-section",
		},
		{
			name:     "H4 only",
			lines:    []string{"#### Deep section", "content"},
			expected: "Deep section",
		},
		{
			name:     "body text before heading",
			lines:    []string{"just some text", "## Found Heading", "more text"},
			expected: "Found Heading",
		},
		{
			name:     "no heading",
			lines:    []string{"no heading here", "just text"},
			expected: "",
		},
		{
			name:     "empty slice",
			lines:    []string{},
			expected: "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := extractHeading(tc.lines)
			if got != tc.expected {
				t.Errorf("extractHeading(%v) = %q; want %q", tc.lines, got, tc.expected)
			}
		})
	}
}

// TestScanBreakPointsNoFence verifies that scanBreakPoints correctly classifies
// headings and blank lines as break points in a fence-free document.
func TestScanBreakPointsNoFence(t *testing.T) {
	lines := []string{
		"# Title",      // line 0: H1  -> score ~100 (at boundary)
		"",             // line 1: blank
		"Body text.",   // line 2: no break
		"",             // line 3: blank
		"## Section 1", // line 4: H2
		"Content here.",
		"",
		"## Section 2", // line 7: H2
		"More content.",
	}

	points := scanBreakPoints(lines, 5)

	if len(points) == 0 {
		t.Fatal("expected at least one break point, got none")
	}

	// Collect line numbers that were identified as break candidates.
	lineSet := make(map[int]float64)
	for _, bp := range points {
		lineSet[bp.line] = bp.score
	}

	// H1 at line 0 must be a break point.
	if _, ok := lineSet[0]; !ok {
		t.Errorf("line 0 (H1) should be a break point; got points: %v", lineSet)
	}

	// H2 at line 4 must be a break point.
	if _, ok := lineSet[4]; !ok {
		t.Errorf("line 4 (H2) should be a break point; got points: %v", lineSet)
	}

	// H2 at line 7 must be a break point.
	if _, ok := lineSet[7]; !ok {
		t.Errorf("line 7 (H2) should be a break point; got points: %v", lineSet)
	}

	// Plain body text at line 2 must NOT be a break point.
	if _, ok := lineSet[2]; ok {
		t.Errorf("line 2 (plain text) must not be a break point")
	}

	for _, line := range []int{1, 3, 6} {
		if _, ok := lineSet[line]; !ok {
			t.Errorf("line %d (blank) should be a break point; got %v", line, lineSet)
		}
	}
	if h1Score, ok := lineSet[0]; ok {
		if blankScore, ok := lineSet[1]; ok && h1Score <= blankScore {
			t.Errorf("H1 score (%.2f) should be > blank score (%.2f)", h1Score, blankScore)
		}
	}
}

func TestApplyDecay(t *testing.T) {
	tests := []struct {
		name         string
		line, target int
		window, want float64
	}{
		{"boundary", 10, 10, 5, 100},
		{"half window", 2, 8, 4, 82.5},
		{"nearest next boundary", 7, 8, 4, 95.625},
		{"window edge", 4, 8, 4, 30},
		{"clamped", 4, 8, 2, 30},
		{"zero window", 4, 8, 0, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := applyDecay(100, tt.line, tt.target, tt.window); !(got >= tt.want-1e-9 && got <= tt.want+1e-9) {
				t.Errorf("applyDecay = %v, want %v", got, tt.want)
			}
		})
	}
}

// chunkNames is a test helper that returns the Name field of each chunk.
func chunkNames(chunks []chunking.Chunk) []string {
	names := make([]string, len(chunks))
	for i, ch := range chunks {
		names[i] = ch.Name
	}
	return names
}
