// Package chunking provides AST-aware code chunking for semantic indexing.
// It splits source files at logical boundaries — functions, methods, types —
// so that each embedded unit corresponds to one coherent piece of code rather
// than an arbitrary byte window.
package chunking

import (
	"context"
	"fmt"
	"strings"
)

// ChunkType classifies the kind of declaration a Chunk represents.
type ChunkType string

const (
	// ChunkTypeFunction is a top-level function declaration.
	ChunkTypeFunction ChunkType = "function"
	// ChunkTypeMethod is a method bound to a struct or named type.
	ChunkTypeMethod ChunkType = "method"
	// ChunkTypeClass is a struct or class-like composite type definition.
	ChunkTypeClass ChunkType = "class"
	// ChunkTypeInterface is an interface declaration.
	ChunkTypeInterface ChunkType = "interface"
	// ChunkTypeType is a named type alias or type definition.
	ChunkTypeType ChunkType = "type"
	// ChunkTypeConst covers constant declaration blocks.
	ChunkTypeConst ChunkType = "const"
	// ChunkTypeVar covers package-level variable declarations.
	ChunkTypeVar ChunkType = "var"
)

// Language identifies the source programming language of a chunk.
type Language string

const (
	// LanguageGo is the Go programming language.
	LanguageGo Language = "go"
)

// Chunk is a single semantic unit extracted from a source file.
// Boundaries come from the AST so that the chunk maps to one declaration
// rather than a fixed byte range that may straddle logical boundaries.
type Chunk struct {
	// Metadata holds arbitrary key-value annotations set by the chunker.
	Metadata   map[string]interface{}
	// FilePath is the absolute path of the source file this chunk came from.
	FilePath   string
	// Language is the source language detected for this file.
	Language   Language
	// Type classifies the declaration (function, method, type, …).
	Type       ChunkType
	// Name is the declared identifier (e.g. function name, type name).
	Name       string
	// ParentName is the receiver type for methods, empty for top-level declarations.
	ParentName string
	// Content is the full source text of the declaration.
	Content    string
	// Signature is the declaration header without the body, used for ranking.
	Signature  string
	// DocComment is the doc-comment that immediately precedes the declaration.
	DocComment string
	// StartLine is the 1-based line number of the opening token.
	StartLine  int
	// EndLine is the 1-based line number of the closing token.
	EndLine    int
}

// Identifier returns a stable human-readable label for this chunk.
// Methods are formatted as "ReceiverType.MethodName"; top-level declarations
// use the bare name.
func (c *Chunk) Identifier() string {
	if c.ParentName != "" {
		return fmt.Sprintf("%s.%s", c.ParentName, c.Name)
	}
	return c.Name
}

// LineRange returns the source span as a compact label, e.g. "L12-L34".
func (c *Chunk) LineRange() string {
	return fmt.Sprintf("L%d-L%d", c.StartLine, c.EndLine)
}

// SearchableContent assembles the text that will be embedded for semantic search.
// The signature comes first so similarity queries match on the declaration
// header before falling back to body text; the doc-comment follows for
// natural-language proximity; the content body comes last.
func (c *Chunk) SearchableContent() string {
	var parts []string

	if c.Signature != "" {
		parts = append(parts, c.Signature)
	}

	if c.DocComment != "" {
		parts = append(parts, c.DocComment)
	}

	if c.Content != "" {
		parts = append(parts, c.Content)
	}

	return strings.Join(parts, "\n\n")
}

// Chunker is the contract that language-specific parsers implement.
// Each Chunker handles one language and registers itself by file extension.
type Chunker interface {
	// Chunk parses filePath and returns the semantic units it contains.
	// An error is returned when the file cannot be read or the AST walk fails.
	Chunk(ctx context.Context, filePath string) ([]Chunk, error)

	// Language reports the programming language this chunker handles.
	Language() Language

	// SupportedExtensions lists the file extensions (with leading dot) that
	// this chunker claims, e.g. []string{".go"}.
	SupportedExtensions() []string
}

// ChunkOptions controls how the Manager filters chunks after parsing.
type ChunkOptions struct {
	// MaxChunkSize is the upper byte limit for a chunk's Content field.
	// Chunks that exceed this limit are dropped rather than truncated so that
	// semantic boundaries remain intact. 0 disables the limit.
	MaxChunkSize int

	// IncludeDocComments controls whether doc-comment text is populated on
	// returned chunks. When false, DocComment is left empty.
	IncludeDocComments bool

	// IncludePrivate controls whether unexported (private) symbols are returned.
	// Set to false to index only the public API surface.
	IncludePrivate bool

	// MinLines is the minimum line span a chunk must cover to be kept.
	// Chunks shorter than this are silently dropped. 0 disables the minimum.
	MinLines int
}

// DefaultChunkOptions returns the options used when none are specified explicitly.
// The defaults are tuned for comprehensive in-repo semantic search: all symbols
// are indexed (including private ones), doc-comments are captured, and only an
// 8 KB upper bound is enforced to stay well under typical embedding token limits.
func DefaultChunkOptions() ChunkOptions {
	return ChunkOptions{
		MaxChunkSize:       8192, // ~8KB per chunk (well under token limit)
		IncludeDocComments: true,
		IncludePrivate:     true, // Include all symbols for comprehensive search
		MinLines:           0,    // No minimum - include even single-line functions
	}
}
