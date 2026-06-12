// Package golang provides AST-aware chunking for Go source files. It parses
// Go source with go/parser and emits one Chunk per top-level declaration:
// functions, methods, types (struct, interface, alias), constants, and
// variables. The resulting chunks feed the code-intelligence retrieval pipeline.
package golang

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"

	"github.com/thebtf/engram/internal/chunking"
)

// Chunker implements AST-aware chunking for Go files.
type Chunker struct {
	options chunking.ChunkOptions
}

// NewChunker creates a new Go chunker with the given options.
func NewChunker(options chunking.ChunkOptions) *Chunker {
	return &Chunker{options: options}
}

// Language returns the language this chunker supports.
func (c *Chunker) Language() chunking.Language {
	return chunking.LanguageGo
}

// SupportedExtensions returns the file extensions this chunker handles.
func (c *Chunker) SupportedExtensions() []string {
	return []string{".go"}
}

// Chunk parses a Go source file and returns one semantic Chunk per top-level
// declaration. The context is threaded through for future cancellation support
// but is not yet checked internally (parsing is CPU-bound and fast).
func (c *Chunker) Chunk(ctx context.Context, filePath string) ([]chunking.Chunk, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	// Parse with comment retention so DocComment fields can be populated.
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, content, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse Go file: %w", err)
	}

	// Split into lines once; each extraction helper indexes into this slice to
	// avoid repeatedly scanning the raw byte slice.
	sourceLines := strings.Split(string(content), "\n")

	chunks := make([]chunking.Chunk, 0)
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if chunk := c.extractFunction(fset, d, sourceLines, filePath); chunk != nil {
				chunks = append(chunks, *chunk)
			}
		case *ast.GenDecl:
			chunks = append(chunks, c.extractGenDecl(fset, d, sourceLines, filePath)...)
		}
	}

	return chunks, nil
}

// extractFunction turns a function or method declaration into a Chunk.
// Returns nil when the declaration is unexported and IncludePrivate is false,
// which is the common case for filtering internal helpers from retrieval.
func (c *Chunker) extractFunction(fset *token.FileSet, fn *ast.FuncDecl, sourceLines []string, filePath string) *chunking.Chunk {
	if !c.options.IncludePrivate && !fn.Name.IsExported() {
		return nil
	}

	startPos := fset.Position(fn.Pos())
	endPos := fset.Position(fn.End())

	chunk := &chunking.Chunk{
		FilePath:  filePath,
		Language:  chunking.LanguageGo,
		Name:      fn.Name.Name,
		StartLine: startPos.Line,
		EndLine:   endPos.Line,
	}

	// Presence of a receiver list distinguishes methods from package-level functions.
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		chunk.Type = chunking.ChunkTypeMethod
		chunk.ParentName = c.extractReceiverType(fn.Recv)
	} else {
		chunk.Type = chunking.ChunkTypeFunction
	}

	chunk.Content = c.extractLines(sourceLines, startPos.Line, endPos.Line)
	chunk.Signature = c.extractFunctionSignature(fn, fset, sourceLines)

	if c.options.IncludeDocComments && fn.Doc != nil {
		chunk.DocComment = strings.TrimSpace(fn.Doc.Text())
	}

	return chunk
}

// extractGenDecl handles general declarations (type, const, var blocks). A
// single GenDecl may contain multiple specs (e.g. a const block), so this
// returns a slice.
func (c *Chunker) extractGenDecl(fset *token.FileSet, gd *ast.GenDecl, sourceLines []string, filePath string) []chunking.Chunk {
	var chunks []chunking.Chunk

	for _, spec := range gd.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if chunk := c.extractTypeSpec(fset, gd, s, sourceLines, filePath); chunk != nil {
				chunks = append(chunks, *chunk)
			}
		case *ast.ValueSpec:
			if chunk := c.extractValueSpec(fset, gd, s, sourceLines, filePath); chunk != nil {
				chunks = append(chunks, *chunk)
			}
		}
	}

	return chunks
}

// extractTypeSpec extracts a type declaration as a Chunk. Struct types are
// mapped to ChunkTypeClass so the retrieval layer can apply object-oriented
// queries uniformly across languages (Go structs fill the same conceptual role
// as classes in OO languages).
func (c *Chunker) extractTypeSpec(fset *token.FileSet, gd *ast.GenDecl, ts *ast.TypeSpec, sourceLines []string, filePath string) *chunking.Chunk {
	if !c.options.IncludePrivate && !ts.Name.IsExported() {
		return nil
	}

	startPos := fset.Position(gd.Pos())
	endPos := fset.Position(gd.End())

	chunk := &chunking.Chunk{
		FilePath:  filePath,
		Language:  chunking.LanguageGo,
		Name:      ts.Name.Name,
		StartLine: startPos.Line,
		EndLine:   endPos.Line,
		Content:   c.extractLines(sourceLines, startPos.Line, endPos.Line),
	}

	switch ts.Type.(type) {
	case *ast.StructType:
		chunk.Type = chunking.ChunkTypeClass // Structs model classes in the retrieval taxonomy.
	case *ast.InterfaceType:
		chunk.Type = chunking.ChunkTypeInterface
	default:
		chunk.Type = chunking.ChunkTypeType
	}

	if c.options.IncludeDocComments && gd.Doc != nil {
		chunk.DocComment = strings.TrimSpace(gd.Doc.Text())
	}

	return chunk
}

// extractValueSpec extracts a const or var declaration as a Chunk. The chunk
// is skipped when all declared names are unexported and IncludePrivate is false.
func (c *Chunker) extractValueSpec(fset *token.FileSet, gd *ast.GenDecl, vs *ast.ValueSpec, sourceLines []string, filePath string) *chunking.Chunk {
	if !c.options.IncludePrivate {
		// Only skip when every name in this spec is unexported. A spec can
		// legitimately mix exported and unexported names inside the same block.
		allUnexported := true
		for _, name := range vs.Names {
			if name.IsExported() {
				allUnexported = false
				break
			}
		}
		if allUnexported {
			return nil
		}
	}

	startPos := fset.Position(gd.Pos())
	endPos := fset.Position(gd.End())

	// Join multiple names (e.g. "A, B = 1, 2") into a single readable label.
	names := make([]string, len(vs.Names))
	for i, name := range vs.Names {
		names[i] = name.Name
	}

	chunk := &chunking.Chunk{
		FilePath:  filePath,
		Language:  chunking.LanguageGo,
		Name:      strings.Join(names, ", "),
		StartLine: startPos.Line,
		EndLine:   endPos.Line,
		Content:   c.extractLines(sourceLines, startPos.Line, endPos.Line),
	}

	if gd.Tok == token.CONST {
		chunk.Type = chunking.ChunkTypeConst
	} else {
		chunk.Type = chunking.ChunkTypeVar
	}

	if c.options.IncludeDocComments && gd.Doc != nil {
		chunk.DocComment = strings.TrimSpace(gd.Doc.Text())
	}

	return chunk
}

// extractReceiverType returns the base type name from a method receiver list.
// Both value receivers (T) and pointer receivers (*T) resolve to "T", because
// the receiver type — not the indirection — identifies the owning struct.
func (c *Chunker) extractReceiverType(recv *ast.FieldList) string {
	if len(recv.List) == 0 {
		return ""
	}

	field := recv.List[0]
	switch t := field.Type.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			return ident.Name
		}
	}

	return ""
}

// extractFunctionSignature returns the function signature text without the
// body braces. When the opening brace is on the same line as the declaration
// (the common Go style), it slices up to the brace. When the declaration spans
// multiple lines (unusual but valid), it takes all lines up to the brace line
// and trims the brace itself.
func (c *Chunker) extractFunctionSignature(fn *ast.FuncDecl, fset *token.FileSet, sourceLines []string) string {
	if fn.Body == nil {
		// External or interface stub — the whole declaration is the signature.
		startPos := fset.Position(fn.Pos())
		endPos := fset.Position(fn.End())
		return c.extractLines(sourceLines, startPos.Line, endPos.Line)
	}

	startPos := fset.Position(fn.Pos())
	bodyPos := fset.Position(fn.Body.Pos())

	if startPos.Line == bodyPos.Line {
		// Single-line declaration: slice the source line up to the opening brace.
		line := sourceLines[startPos.Line-1]
		if idx := strings.Index(line[startPos.Column-1:], "{"); idx >= 0 {
			return strings.TrimSpace(line[startPos.Column-1 : startPos.Column-1+idx])
		}
		return strings.TrimSpace(line[startPos.Column-1:])
	}

	// Multi-line declaration: collect lines through the brace line, then
	// strip the brace and any trailing whitespace.
	sig := c.extractLines(sourceLines, startPos.Line, bodyPos.Line)
	if idx := strings.Index(sig, "{"); idx >= 0 {
		sig = sig[:idx]
	}
	return strings.TrimSpace(sig)
}

// extractLines returns the source text for the 1-indexed inclusive range
// [start, end]. Returns empty string for invalid or out-of-bounds ranges.
func (c *Chunker) extractLines(lines []string, start, end int) string {
	if start < 1 || end < start || start > len(lines) {
		return ""
	}

	// Convert 1-indexed bounds to 0-indexed slice bounds.
	startIdx := start - 1
	endIdx := end
	if endIdx > len(lines) {
		endIdx = len(lines)
	}

	return strings.Join(lines[startIdx:endIdx], "\n")
}
