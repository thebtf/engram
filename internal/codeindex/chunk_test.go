package codeindex

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// --- Language detection ---

func TestDetectLanguage(t *testing.T) {
	cases := []struct {
		filename string
		want     string
	}{
		{"main.go", "go"},
		{"index.ts", "typescript"},
		{"App.tsx", "typescript"},
		{"app.js", "javascript"},
		{"Component.jsx", "javascript"},
		{"server.py", "python"},
		{"lib.rs", "rust"},
		{"Main.java", "java"},
		{"util.c", "c"},
		{"header.h", "c"},
		{"algo.cpp", "cpp"},
		{"algo.cc", "cpp"},
		{"script.rb", "ruby"},
		{"Controller.php", "php"},
		{"README.md", "markdown"},
		{"config.json", "json"},
		{"deploy.yaml", "yaml"},
		{"values.yml", "yaml"},
		{"build.sh", "bash"},
		{"schema.sql", "sql"},
		{"Makefile", ""},
		{"no_extension", ""},
		{"file.unknown", ""},
	}
	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			got := DetectLanguage(tc.filename)
			if got != tc.want {
				t.Errorf("DetectLanguage(%q) = %q; want %q", tc.filename, got, tc.want)
			}
		})
	}
}

// --- ChunkID stability ---

func TestChunkIDStability(t *testing.T) {
	c1 := Chunk{FilePath: "pkg/foo.go", ByteStart: 42}
	c2 := Chunk{FilePath: "pkg/foo.go", ByteStart: 42}
	if c1.ChunkID() != c2.ChunkID() {
		t.Errorf("ChunkID not stable: %s != %s", c1.ChunkID(), c2.ChunkID())
	}
	// Different path → different ID.
	c3 := Chunk{FilePath: "pkg/bar.go", ByteStart: 42}
	if c1.ChunkID() == c3.ChunkID() {
		t.Errorf("ChunkID should differ for different paths")
	}
	// Different byteStart → different ID.
	c4 := Chunk{FilePath: "pkg/foo.go", ByteStart: 0}
	if c1.ChunkID() == c4.ChunkID() {
		t.Errorf("ChunkID should differ for different byteStarts")
	}
	// ID is 16 hex chars (8 bytes).
	id := c1.ChunkID()
	if len(id) != 16 {
		t.Errorf("ChunkID length = %d; want 16", len(id))
	}
}

func TestChunkIDSameContentDifferentFiles(t *testing.T) {
	// Same content in two files → same ContentSHA256 but DIFFERENT ChunkID.
	content := []byte("hello world\n")
	opts := DefaultOptions()
	chunks1, err := ChunkFile("pkg/a.go", content, opts)
	if err != nil || len(chunks1) == 0 {
		t.Fatalf("ChunkFile a.go: err=%v chunks=%d", err, len(chunks1))
	}
	chunks2, err := ChunkFile("pkg/b.go", content, opts)
	if err != nil || len(chunks2) == 0 {
		t.Fatalf("ChunkFile b.go: err=%v chunks=%d", err, len(chunks2))
	}
	if chunks1[0].ContentSHA256 != chunks2[0].ContentSHA256 {
		t.Errorf("ContentSHA256 should be equal for identical content")
	}
	if chunks1[0].ChunkID() == chunks2[0].ChunkID() {
		t.Errorf("ChunkID should differ for different file paths")
	}
}

func TestContentSHA256MatchesCryptoSHA256(t *testing.T) {
	content := []byte("func main() {}\n")
	opts := DefaultOptions()
	chunks, err := ChunkFile("cmd/main.go", content, opts)
	if err != nil || len(chunks) == 0 {
		t.Fatalf("ChunkFile: %v", err)
	}
	h := sha256.Sum256([]byte(chunks[0].Content))
	want := fmt.Sprintf("%x", h[:])
	if chunks[0].ContentSHA256 != want {
		t.Errorf("ContentSHA256 mismatch: got %s want %s", chunks[0].ContentSHA256, want)
	}
}

// --- ChunkFile: basic cases ---

func TestChunkFileEmpty(t *testing.T) {
	chunks, err := ChunkFile("empty.go", []byte{}, DefaultOptions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty file, got %d", len(chunks))
	}
}

func TestChunkFileSmall(t *testing.T) {
	// 10 lines → 1 block with default LinesPerBlock=70.
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	content := []byte(strings.Join(lines, "\n") + "\n")

	opts := DefaultOptions()
	chunks, err := ChunkFile("src/small.go", content, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}
	// Contiguous byte coverage.
	assertContiguous(t, content, chunks)
}

func TestChunkFileExactLinesPerBlock(t *testing.T) {
	// 140 lines with LinesPerBlock=70 → 2 chunks.
	opts := DefaultOptions()
	opts.LinesPerBlock = 70
	content := makeLines(140)

	chunks, err := ChunkFile("src/exact.go", content, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 2 {
		t.Errorf("expected 2 chunks, got %d", len(chunks))
	}
	assertContiguous(t, content, chunks)
}

func TestChunkFileMultipleBlocks(t *testing.T) {
	// 215 lines, LinesPerBlock=70 → ceil(215/70)=4 chunks.
	opts := DefaultOptions()
	opts.LinesPerBlock = 70
	content := makeLines(215)

	chunks, err := ChunkFile("src/multi.go", content, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 4 {
		t.Errorf("expected 4 chunks, got %d", len(chunks))
	}
	assertContiguous(t, content, chunks)
}

func TestChunkFileLanguageDetection(t *testing.T) {
	content := []byte("hello\n")
	for ext, wantLang := range map[string]string{
		"go":   "go",
		"ts":   "typescript",
		"py":   "python",
		"rs":   "rust",
		"java": "java",
	} {
		chunks, err := ChunkFile("file."+ext, content, DefaultOptions())
		if err != nil || len(chunks) == 0 {
			t.Errorf("ext=%s: err=%v chunks=%d", ext, err, len(chunks))
			continue
		}
		if chunks[0].Language != wantLang {
			t.Errorf("ext=%s: Language=%q want %q", ext, chunks[0].Language, wantLang)
		}
	}
}

// --- Char-cap guard ---

func TestChunkFileCharCapSplitsAtRuneBoundary(t *testing.T) {
	// Build a single line that is 3× MaxChunkBytes long using multi-byte runes.
	opts := DefaultOptions()
	opts.MaxChunkBytes = 64
	opts.LinesPerBlock = 1 // Force each line into its own block consideration.
	// Raise the minified thresholds so this synthetic test content is not skipped
	// by the minified guard (the guard exists for real minified JS, not test fixtures).
	opts.MinifiedAvgLineLen = 100_000
	opts.MinifiedSingleLineBytes = 100_000

	// Use "€" (U+20AC, 3 bytes in UTF-8) to ensure we hit mid-rune boundaries.
	euro := "€"
	// Build a single very long line: 100 euros, no newlines until the end.
	longLine := strings.Repeat(euro, 100) + "\n" // 300 bytes + 1 newline = 301 bytes
	content := []byte(longLine)

	chunks, err := ChunkFile("minified/big.go", content, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}
	// Every chunk must be valid UTF-8.
	for i, c := range chunks {
		if !utf8.ValidString(c.Content) {
			t.Errorf("chunk %d content is not valid UTF-8", i)
		}
		// No chunk should exceed MaxChunkBytes + 2 (at most 2 extra bytes to
		// complete a rune).
		if len(c.Content) > opts.MaxChunkBytes+2 {
			t.Errorf("chunk %d size=%d exceeds MaxChunkBytes+2=%d",
				i, len(c.Content), opts.MaxChunkBytes+2)
		}
	}
	// All chunks together cover every byte.
	assertContiguous(t, content, chunks)
}

func TestChunkFileInvalidUTF8Skipped(t *testing.T) {
	// Non-NUL invalid UTF-8 (e.g. Latin-1 bytes, a truncated multi-byte sequence,
	// or all continuation bytes) must be skipped — not chunked into invalid-UTF-8
	// Content. This guards the "Content is always valid UTF-8" contract for the
	// pathological char-cap path the reviewer flagged.
	cases := map[string][]byte{
		"all continuation bytes": bytes.Repeat([]byte{0x80}, 200),
		"latin1 é (0xE9)":        []byte("caf\xe9 au lait\n"),
		"truncated multibyte":    append([]byte("hello "), 0xF0, 0x9F), // start of a 4-byte rune, cut short
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			opts := DefaultOptions()
			opts.MaxChunkBytes = 8 // small cap to force the pathological path on the all-continuation case
			chunks, err := ChunkFile("data/weird.txt", content, opts)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if chunks != nil {
				t.Fatalf("expected nil chunks for invalid UTF-8, got %d", len(chunks))
			}
		})
	}
}

func TestChunkFileCRLF(t *testing.T) {
	// CRLF line endings: findLineStarts splits on \n and leaves \r in the line
	// body. A small CRLF file must still produce contiguous coverage of every
	// byte (the \r bytes belong to their line's chunk).
	content := []byte("line one\r\nline two\r\nline three\r\n")
	chunks, err := ChunkFile("src/crlf.go", content, DefaultOptions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk for a CRLF file")
	}
	for i, c := range chunks {
		if !utf8.ValidString(c.Content) {
			t.Errorf("chunk %d not valid UTF-8", i)
		}
	}
	assertContiguous(t, content, chunks)
}

// --- Minified guards ---

func TestChunkFileMinifiedName(t *testing.T) {
	content := makeLines(10)
	chunks, err := ChunkFile("dist/bundle.min.js", content, DefaultOptions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chunks != nil {
		t.Errorf("expected nil chunks for .min. file, got %d", len(chunks))
	}
}

func TestChunkFileMinifiedAvgLineLen(t *testing.T) {
	opts := DefaultOptions()
	opts.MinifiedAvgLineLen = 50
	// Build content where avg line length > 50.
	line := strings.Repeat("x", 100) + "\n"
	content := []byte(strings.Repeat(line, 5))

	chunks, err := ChunkFile("src/generated.js", content, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chunks != nil {
		t.Errorf("expected nil chunks for high avg-line-length file, got %d", len(chunks))
	}
}

func TestChunkFileMinifiedSingleLongLine(t *testing.T) {
	opts := DefaultOptions()
	opts.MinifiedSingleLineBytes = 100
	// One very long line.
	longLine := strings.Repeat("a", 200) + "\n"
	content := []byte(longLine)

	chunks, err := ChunkFile("src/bundle.js", content, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chunks != nil {
		t.Errorf("expected nil chunks for single-very-long-line file, got %d", len(chunks))
	}
}

// --- Helpers ---

// makeLines builds content with n numbered lines, each short enough to avoid
// triggering the minified heuristic.
func makeLines(n int) []byte {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "// line %04d\n", i)
	}
	return []byte(sb.String())
}

// assertContiguous verifies that chunks cover every byte of content exactly
// once and in order, with no gaps or overlaps.
func assertContiguous(t *testing.T, content []byte, chunks []Chunk) {
	t.Helper()
	if len(chunks) == 0 {
		return
	}
	// First chunk starts at 0.
	if chunks[0].ByteStart != 0 {
		t.Errorf("first chunk ByteStart=%d; want 0", chunks[0].ByteStart)
	}
	for i := 1; i < len(chunks); i++ {
		if chunks[i].ByteStart != chunks[i-1].ByteEnd {
			t.Errorf("gap between chunk %d (end=%d) and chunk %d (start=%d)",
				i-1, chunks[i-1].ByteEnd, i, chunks[i].ByteStart)
		}
	}
	// Last chunk ends at len(content).
	last := chunks[len(chunks)-1]
	if last.ByteEnd != len(content) {
		t.Errorf("last chunk ByteEnd=%d; want %d", last.ByteEnd, len(content))
	}
	// Content field matches byte slice.
	for i, c := range chunks {
		got := string(content[c.ByteStart:c.ByteEnd])
		if c.Content != got {
			t.Errorf("chunk %d Content mismatch: len=%d wantLen=%d",
				i, len(c.Content), len(got))
		}
	}
}
