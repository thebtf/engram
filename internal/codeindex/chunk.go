// Package codeindex provides a pure-Go, CGo-free line-fallback chunker and
// manifest builder for the CI-A SocratiCode-replacement track (ADR-001 §3.2 + §6).
//
// Design summary
//
// Chunking strategy: fixed-size line-blocks (default 70 lines) with a
// hard byte-cap guard per chunk (default 8 KB) so that minified or
// unusually long lines never produce oversized embedding payloads.
// Byte offsets are exact and contiguous: every byte in a non-skipped file
// belongs to exactly one chunk.
//
// Minified-file detection: files whose average line length exceeds 200 bytes,
// whose name contains ".min.", or whose single longest line exceeds 50 KB are
// classified as minified/generated and skipped entirely.
//
// gitignore / .engramignore: parsed without an external dependency using
// stdlib path.Match. The minimal parser handles globs, directory prefixes,
// and negation ("!pattern"). A built-in skip-list covers .git, node_modules,
// vendor, dist, build, .agent and common binary extensions so the most common
// cases work even without a .gitignore present.
//
// AST chunking (tree-sitter / CGo) is explicitly deferred to CR-002b and
// guarded by a build tag. THIS package has zero CGo.
package codeindex

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
)

// ChunkType classifies how the chunk boundary was chosen.
// "line-block" is the only value produced by this CR; AST-derived types are
// added in CR-002b behind a build tag.
type ChunkType string

const (
	// ChunkTypeLineBlock indicates a chunk produced by the line-fallback strategy.
	ChunkTypeLineBlock ChunkType = "line-block"
)

// Chunk is a contiguous, non-overlapping byte range extracted from a source file.
// Fields mirror the CodeIndex protocol described in ADR-001 §6.
type Chunk struct {
	// FilePath is the repository-relative path with forward-slash separators.
	FilePath string
	// ByteStart is the inclusive start byte offset in the original file content.
	ByteStart int
	// ByteEnd is the exclusive end byte offset (content[ByteStart:ByteEnd]).
	ByteEnd int
	// Language is the detected language from the file extension, e.g. "go".
	// Empty string when the extension is not in the known map.
	Language string
	// ChunkType describes the boundary strategy used to produce this chunk.
	ChunkType ChunkType
	// Content is the UTF-8 text of the chunk (content[ByteStart:ByteEnd]).
	Content string
	// ContentSHA256 is the lowercase hex SHA-256 of Content, used as the
	// delta-negotiation key: equal hash ↔ server already has this chunk.
	ContentSHA256 string
}

// ChunkID returns the stable 16-hex-character identifier for this chunk.
// It is derived from sha256(filePath + ":" + byteStart) so that the same
// logical chunk position always has the same ID regardless of content changes.
// This matches the client-side ID definition in ADR-001 §6.
func (c Chunk) ChunkID() string {
	h := sha256.Sum256([]byte(c.FilePath + ":" + strconv.Itoa(c.ByteStart)))
	return fmt.Sprintf("%x", h[:8]) // 8 bytes → 16 hex chars
}

// ManifestEntry carries the lightweight metadata sent to CodeIndexNegotiate.
// Content is intentionally absent so only the delta (differing chunks) needs
// to be transmitted over the wire.
type ManifestEntry struct {
	FilePath      string    `json:"file_path"`
	ChunkID       string    `json:"chunk_id"`
	ContentSHA256 string    `json:"content_sha256"`
	ByteStart     int       `json:"byte_start"`
	ByteEnd       int       `json:"byte_end"`
	Language      string    `json:"language"`
	ChunkType     ChunkType `json:"chunk_type"`
}

// Manifest is an ordered list of ManifestEntry values, one per chunk, sorted
// by (FilePath, ByteStart). The same tree always produces the same Manifest.
type Manifest []ManifestEntry

// BuildManifestFromChunks constructs a Manifest from a slice of Chunks.
// The caller is responsible for ensuring the slice is sorted if deterministic
// order matters (BuildManifest guarantees this by construction).
func BuildManifestFromChunks(chunks []Chunk) Manifest {
	m := make(Manifest, len(chunks))
	for i, c := range chunks {
		m[i] = ManifestEntry{
			FilePath:      c.FilePath,
			ChunkID:       c.ChunkID(),
			ContentSHA256: c.ContentSHA256,
			ByteStart:     c.ByteStart,
			ByteEnd:       c.ByteEnd,
			Language:      c.Language,
			ChunkType:     c.ChunkType,
		}
	}
	return m
}

// Options controls chunking and walking behaviour.
// All fields have documented defaults; use DefaultOptions() to obtain them.
type Options struct {
	// LinesPerBlock is the target number of source lines per chunk.
	// The last block in a file may be smaller. Default: 70.
	LinesPerBlock int

	// MaxChunkBytes is the hard byte-cap for a single chunk's Content.
	// When a line-block would exceed this size the content is split at a UTF-8
	// rune boundary at MaxChunkBytes. Default: 8192 (8 KB). Values below
	// utf8.UTFMax (4) are raised to utf8.UTFMax so a rune-safe split is always
	// possible — a cap smaller than one max-width rune could strand a lone byte.
	MaxChunkBytes int

	// MaxFileBytes is the maximum file size to process.
	// Files larger than this are skipped. Default: 1 048 576 (1 MB).
	MaxFileBytes int

	// MinifiedAvgLineLen is the average line-length threshold (bytes/line) above
	// which a file is treated as minified/generated and skipped. Default: 200.
	MinifiedAvgLineLen int

	// MinifiedSingleLineBytes is the single-line byte length that triggers the
	// minified heuristic regardless of average. Default: 51200 (50 KB).
	MinifiedSingleLineBytes int
}

// DefaultOptions returns a sensible set of options for general code indexing.
func DefaultOptions() Options {
	return Options{
		LinesPerBlock:           70,
		MaxChunkBytes:           8192,
		MaxFileBytes:            1 << 20, // 1 MB
		MinifiedAvgLineLen:      200,
		MinifiedSingleLineBytes: 50 * 1024, // 50 KB
	}
}

// languageByExtension maps lower-case file extensions to language tags.
// Metadata only — the chunker does not parse or interpret any language.
var languageByExtension = map[string]string{
	".go":   "go",
	".ts":   "typescript",
	".tsx":  "typescript",
	".js":   "javascript",
	".jsx":  "javascript",
	".py":   "python",
	".rs":   "rust",
	".java": "java",
	".c":    "c",
	".h":    "c",
	".cpp":  "cpp",
	".cc":   "cpp",
	".rb":   "ruby",
	".php":  "php",
	".md":   "markdown",
	".json": "json",
	".yaml": "yaml",
	".yml":  "yaml",
	".sh":   "bash",
	".sql":  "sql",
}

// DetectLanguage returns the language tag for the given filename based on its
// file extension. Returns "" for unknown extensions.
func DetectLanguage(filename string) string {
	// Find the last dot.
	dot := strings.LastIndex(filename, ".")
	if dot < 0 {
		return ""
	}
	return languageByExtension[strings.ToLower(filename[dot:])]
}

// contentSHA256Hex returns the lowercase hex SHA-256 of b.
func contentSHA256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:])
}
