package markdown

import (
	"context"
	"strings"
	"testing"

	"github.com/thebtf/claude-mnemonic-plus/internal/chunking"
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
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk, got none")
	}

	// The first (and dominant) chunk must be named after the H1 heading.
	found := false
	for _, ch := range chunks {
		if ch.Name == "Introduction" {
			found = true
			if ch.Language != LanguageMarkdown {
				t.Errorf("expected language %q, got %q", LanguageMarkdown, ch.Language)
			}
			if ch.Type != ChunkTypeSection {
				t.Errorf("expected type %q, got %q", ChunkTypeSection, ch.Type)
			}
			if ch.FilePath != "doc.md" {
				t.Errorf("expected filePath %q, got %q", "doc.md", ch.FilePath)
			}
			if ch.StartLine < 1 {
				t.Errorf("StartLine must be >= 1, got %d", ch.StartLine)
			}
			if ch.Content == "" {
				t.Error("Content must not be empty")
			}
			break
		}
	}
	if !found {
		t.Errorf("expected a chunk named %q; chunks returned: %v", "Introduction", chunkNames(chunks))
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
		t.Errorf("expected multiple chunks for a 3-section document, got %d", len(chunks))
	}

	// At least two of the three heading names must appear among chunk names.
	nameSet := make(map[string]bool)
	for _, ch := range chunks {
		nameSet[ch.Name] = true
	}
	matched := 0
	for _, s := range sections {
		if nameSet[s.heading] {
			matched++
		}
	}
	if matched < 2 {
		t.Errorf("expected at least 2 section headings among chunk names, got %d; names: %v", matched, chunkNames(chunks))
	}
}

// TestCodeFenceGuard verifies that headings inside a code fence do NOT create
// a break point, i.e. the fence content is treated as opaque text.
func TestCodeFenceGuard(t *testing.T) {
	lines := []string{
		"# Real Heading",
		"",
		"Some text before the fence.",
		"```markdown",
		"## This heading is inside a fence and must be ignored",
		"```",
		"",
		"Text after the fence.",
	}

	points := scanBreakPoints(lines, 20)

	// Verify that line 4 (the "## " inside the fence) is NOT a break point.
	for _, bp := range points {
		if bp.line == 4 {
			t.Errorf("line 4 (inside code fence) must not be a break point, but got score=%.2f", bp.score)
		}
	}
}

// TestSupportedExtensions verifies that the chunker declares ".md" and ".mdx".
func TestSupportedExtensions(t *testing.T) {
	c := newChunker()
	exts := c.SupportedExtensions()

	want := map[string]bool{".md": false, ".mdx": false}
	for _, ext := range exts {
		if _, ok := want[ext]; ok {
			want[ext] = true
		}
	}
	for ext, found := range want {
		if !found {
			t.Errorf("expected extension %q to be in SupportedExtensions(), got: %v", ext, exts)
		}
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

	// Scores for headings must be higher than for blank lines.
	h1Score := lineSet[0]
	blankScore, hasBlank := lineSet[1]
	if hasBlank && h1Score <= blankScore {
		t.Errorf("H1 score (%.2f) should be > blank score (%.2f)", h1Score, blankScore)
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
