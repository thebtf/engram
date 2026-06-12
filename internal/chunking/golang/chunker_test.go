package golang

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/chunking"
)

// writeTemp writes src to a new .go file inside t.TempDir() and returns the path.
func writeTemp(t *testing.T, src string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "test.go")
	require.NoError(t, os.WriteFile(f, []byte(src), 0600))
	return f
}

// -----------------------------------------------------------------------------
// Contract: NewChunker / Language / SupportedExtensions
// - NewChunker stores the provided options and returns a non-nil *Chunker.
// - Language() always returns LanguageGo.
// - SupportedExtensions() returns exactly []string{".go"}.
// -----------------------------------------------------------------------------

func TestNewChunker_NotNil(t *testing.T) {
	c := NewChunker(chunking.DefaultChunkOptions())
	assert.NotNil(t, c)
}

func TestChunker_Language(t *testing.T) {
	c := NewChunker(chunking.DefaultChunkOptions())
	assert.Equal(t, chunking.LanguageGo, c.Language())
}

func TestChunker_SupportedExtensions(t *testing.T) {
	c := NewChunker(chunking.DefaultChunkOptions())
	exts := c.SupportedExtensions()
	assert.Equal(t, []string{".go"}, exts)
}

// -----------------------------------------------------------------------------
// Contract: Chunk — error paths
// - Returns an error when the file does not exist.
// - Returns an error when the file contains invalid Go syntax.
// -----------------------------------------------------------------------------

func TestChunk_FileNotFound(t *testing.T) {
	c := NewChunker(chunking.DefaultChunkOptions())
	_, err := c.Chunk(context.Background(), "/nonexistent/path/does_not_exist.go")
	assert.Error(t, err)
}

func TestChunk_InvalidGoSyntax(t *testing.T) {
	f := writeTemp(t, "package main\nfunc broken( {}")
	c := NewChunker(chunking.DefaultChunkOptions())
	_, err := c.Chunk(context.Background(), f)
	assert.Error(t, err)
}

// -----------------------------------------------------------------------------
// Contract: Chunk — exported functions
// - One Chunk per top-level function declaration.
// - chunk.Type == ChunkTypeFunction for non-method functions.
// - chunk.Language == LanguageGo.
// - chunk.Name matches the declaration name.
// - chunk.Content is non-empty.
// - chunk.Signature is non-empty and does not contain the body braces.
// - chunk.FilePath matches the path passed to Chunk.
// - StartLine >= 1, EndLine >= StartLine.
// -----------------------------------------------------------------------------

func TestChunk_ExportedFunctions(t *testing.T) {
	src := `package main

import "fmt"

// Greet prints a greeting.
func Greet(name string) {
	fmt.Println(name)
}

// Add returns the sum.
func Add(a, b int) int {
	return a + b
}
`
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 2)

	names := map[string]bool{}
	for _, ch := range chunks {
		names[ch.Name] = true
		assert.Equal(t, chunking.ChunkTypeFunction, ch.Type)
		assert.Equal(t, chunking.LanguageGo, ch.Language)
		assert.Equal(t, f, ch.FilePath)
		assert.NotEmpty(t, ch.Content)
		assert.NotEmpty(t, ch.Signature)
		assert.GreaterOrEqual(t, ch.StartLine, 1)
		assert.GreaterOrEqual(t, ch.EndLine, ch.StartLine)
	}
	assert.True(t, names["Greet"])
	assert.True(t, names["Add"])
}

// -----------------------------------------------------------------------------
// Contract: Chunk — private functions
// - With IncludePrivate=true (default): unexported functions are included.
// - With IncludePrivate=false: unexported functions are excluded.
// -----------------------------------------------------------------------------

func TestChunk_IncludePrivate_True(t *testing.T) {
	src := `package main

func Exported() {}
func unexported() {}
`
	f := writeTemp(t, src)
	opts := chunking.DefaultChunkOptions() // IncludePrivate: true
	c := NewChunker(opts)
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 2)

	names := map[string]bool{}
	for _, ch := range chunks {
		names[ch.Name] = true
	}
	assert.True(t, names["Exported"])
	assert.True(t, names["unexported"])
}

func TestChunk_IncludePrivate_False(t *testing.T) {
	src := `package main

func Exported() {}
func unexported() {}
`
	f := writeTemp(t, src)
	opts := chunking.ChunkOptions{IncludePrivate: false, IncludeDocComments: true}
	c := NewChunker(opts)
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Equal(t, "Exported", chunks[0].Name)
}

// -----------------------------------------------------------------------------
// Contract: Chunk — methods
// - chunk.Type == ChunkTypeMethod for function declarations with a receiver.
// - chunk.ParentName is the base type name (pointer "*T" → "T").
// -----------------------------------------------------------------------------

func TestChunk_Methods_PointerReceiver(t *testing.T) {
	src := `package main

type Server struct{ addr string }

func (s *Server) Start() {}
func (s *Server) Stop()  {}
`
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)

	var methods []chunking.Chunk
	for _, ch := range chunks {
		if ch.Type == chunking.ChunkTypeMethod {
			methods = append(methods, ch)
		}
	}
	require.Len(t, methods, 2)
	for _, m := range methods {
		assert.Equal(t, "Server", m.ParentName)
	}
}

func TestChunk_Methods_ValueReceiver(t *testing.T) {
	src := `package main

type Point struct{ X, Y int }

func (p Point) String() string { return "" }
`
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)

	var method *chunking.Chunk
	for i := range chunks {
		if chunks[i].Type == chunking.ChunkTypeMethod {
			method = &chunks[i]
		}
	}
	require.NotNil(t, method)
	assert.Equal(t, "String", method.Name)
	assert.Equal(t, "Point", method.ParentName)
}

// -----------------------------------------------------------------------------
// Contract: Chunk — struct types
// - Struct declarations → ChunkTypeClass (retrieval taxonomy mapping).
// - Interface declarations → ChunkTypeInterface.
// - Type aliases / other types → ChunkTypeType.
// -----------------------------------------------------------------------------

func TestChunk_StructType(t *testing.T) {
	src := `package main

// User is a domain user.
type User struct {
	ID   int
	Name string
}
`
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	ch := chunks[0]
	assert.Equal(t, "User", ch.Name)
	assert.Equal(t, chunking.ChunkTypeClass, ch.Type)
	assert.NotEmpty(t, ch.Content)
}

func TestChunk_InterfaceType(t *testing.T) {
	src := `package main

type Stringer interface {
	String() string
}
`
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	assert.Equal(t, chunking.ChunkTypeInterface, chunks[0].Type)
	assert.Equal(t, "Stringer", chunks[0].Name)
}

func TestChunk_TypeAlias(t *testing.T) {
	src := `package main

type MyInt int
`
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	assert.Equal(t, chunking.ChunkTypeType, chunks[0].Type)
	assert.Equal(t, "MyInt", chunks[0].Name)
}

func TestChunk_UnexportedType_FilteredOut(t *testing.T) {
	src := `package main

type Exported struct{}
type unexported struct{}
`
	f := writeTemp(t, src)
	opts := chunking.ChunkOptions{IncludePrivate: false}
	c := NewChunker(opts)
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Equal(t, "Exported", chunks[0].Name)
}

// -----------------------------------------------------------------------------
// Contract: Chunk — const / var declarations (extractValueSpec)
// - Const blocks → ChunkTypeConst; var blocks → ChunkTypeVar.
// - chunk.Name is the joined list of all declared names in the spec.
// - All-unexported specs are skipped when IncludePrivate=false.
// - An exported name in the spec keeps the chunk even if others are unexported.
// -----------------------------------------------------------------------------

func TestChunk_ConstDeclaration(t *testing.T) {
	src := `package main

const MaxRetries = 3
`
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	assert.Equal(t, chunking.ChunkTypeConst, chunks[0].Type)
	assert.Equal(t, "MaxRetries", chunks[0].Name)
}

func TestChunk_VarDeclaration(t *testing.T) {
	src := `package main

var DefaultTimeout = 30
`
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	assert.Equal(t, chunking.ChunkTypeVar, chunks[0].Type)
	assert.Equal(t, "DefaultTimeout", chunks[0].Name)
}

func TestChunk_ConstBlock_MultipleSpecs(t *testing.T) {
	src := `package main

const (
	Alpha = "a"
	Beta  = "b"
)
`
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	// Two separate ValueSpecs produce two chunks.
	assert.Len(t, chunks, 2)
	for _, ch := range chunks {
		assert.Equal(t, chunking.ChunkTypeConst, ch.Type)
	}
}

func TestChunk_UnexportedConst_FilteredOut(t *testing.T) {
	src := `package main

const unexportedConst = 42
const ExportedConst = 99
`
	f := writeTemp(t, src)
	opts := chunking.ChunkOptions{IncludePrivate: false}
	c := NewChunker(opts)
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Equal(t, "ExportedConst", chunks[0].Name)
}

// -----------------------------------------------------------------------------
// Contract: Chunk — doc comments
// - With IncludeDocComments=true: doc comment text is captured in DocComment.
// - With IncludeDocComments=false: DocComment is empty even if comments exist.
// - DocComment applies to functions, methods, types, and const/var specs via
//   the enclosing GenDecl.Doc.
// -----------------------------------------------------------------------------

func TestChunk_DocComment_Function(t *testing.T) {
	src := `package main

// Calculate performs a calculation.
// It takes two integers and returns their sum.
func Calculate(a, b int) int {
	return a + b
}
`
	f := writeTemp(t, src)
	opts := chunking.ChunkOptions{IncludeDocComments: true, IncludePrivate: true}
	c := NewChunker(opts)
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	expected := "Calculate performs a calculation.\nIt takes two integers and returns their sum."
	assert.Equal(t, expected, chunks[0].DocComment)
}

func TestChunk_DocComment_Disabled(t *testing.T) {
	src := `package main

// Documented has a doc comment.
func Documented() {}
`
	f := writeTemp(t, src)
	opts := chunking.ChunkOptions{IncludeDocComments: false, IncludePrivate: true}
	c := NewChunker(opts)
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	assert.Empty(t, chunks[0].DocComment)
}

func TestChunk_DocComment_Struct(t *testing.T) {
	src := `package main

// Config holds configuration.
type Config struct {
	Port int
}
`
	f := writeTemp(t, src)
	opts := chunking.ChunkOptions{IncludeDocComments: true, IncludePrivate: true}
	c := NewChunker(opts)
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	assert.Equal(t, "Config holds configuration.", chunks[0].DocComment)
}

func TestChunk_DocComment_Const(t *testing.T) {
	src := `package main

// MaxSize is the maximum buffer size.
const MaxSize = 4096
`
	f := writeTemp(t, src)
	opts := chunking.ChunkOptions{IncludeDocComments: true, IncludePrivate: true}
	c := NewChunker(opts)
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	assert.Equal(t, "MaxSize is the maximum buffer size.", chunks[0].DocComment)
}

// -----------------------------------------------------------------------------
// Contract: Chunk — function signature extraction
// - Signature contains the declaration up to but not including the "{".
// - Multi-line function declarations (params on multiple lines) are handled.
// - Body-less functions (interface stubs) use the whole declaration as signature.
// -----------------------------------------------------------------------------

func TestChunk_Signature_SingleLine(t *testing.T) {
	src := `package main

func Sum(a, b int) int { return a + b }
`
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	sig := chunks[0].Signature
	assert.NotEmpty(t, sig)
	assert.NotContains(t, sig, "{")
}

func TestChunk_Signature_MultiLine(t *testing.T) {
	src := `package main

func LongFunction(
	arg1 string,
	arg2 int,
) error {
	return nil
}
`
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	sig := chunks[0].Signature
	assert.NotEmpty(t, sig)
	assert.NotContains(t, sig, "{")
	assert.Contains(t, sig, "LongFunction")
}

// -----------------------------------------------------------------------------
// Contract: Chunk — empty file
// - A valid Go file with no declarations produces an empty chunk slice.
// -----------------------------------------------------------------------------

func TestChunk_EmptyFile(t *testing.T) {
	src := "package main\n"
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	assert.Empty(t, chunks)
}

// -----------------------------------------------------------------------------
// Contract: Chunk — struct + methods together
// - All declaration types within a single file are handled in one call.
// -----------------------------------------------------------------------------

func TestChunk_StructAndMethods(t *testing.T) {
	src := `package main

// Counter counts things.
type Counter struct {
	n int
}

func (c *Counter) Increment() { c.n++ }
func (c *Counter) Value() int { return c.n }
`
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 3)

	byName := map[string]chunking.Chunk{}
	for _, ch := range chunks {
		byName[ch.Name] = ch
	}

	assert.Equal(t, chunking.ChunkTypeClass, byName["Counter"].Type)
	assert.Equal(t, chunking.ChunkTypeMethod, byName["Increment"].Type)
	assert.Equal(t, "Counter", byName["Increment"].ParentName)
	assert.Equal(t, chunking.ChunkTypeMethod, byName["Value"].Type)
	assert.Equal(t, "Counter", byName["Value"].ParentName)
}

// -----------------------------------------------------------------------------
// Contract: Chunk — FilePath is preserved verbatim
// - The FilePath field in every returned chunk equals the path argument.
// -----------------------------------------------------------------------------

func TestChunk_FilePathPreserved(t *testing.T) {
	src := `package main

func Hello() {}
`
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	assert.Equal(t, f, chunks[0].FilePath)
}

// -----------------------------------------------------------------------------
// Contract: Chunk — line numbers
// - StartLine is 1-indexed and >= 1.
// - EndLine >= StartLine.
// - For multi-line declarations the span covers the full declaration.
// -----------------------------------------------------------------------------

func TestChunk_LineNumbers_Positive(t *testing.T) {
	src := `package main

func Alpha() {}
func Beta() {}
`
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)

	for _, ch := range chunks {
		assert.GreaterOrEqual(t, ch.StartLine, 1)
		assert.GreaterOrEqual(t, ch.EndLine, ch.StartLine)
	}
}

func TestChunk_LineNumbers_MultiLineFunction(t *testing.T) {
	src := `package main

func BigFunc(
	a int,
	b int,
) int {
	return a + b
}
`
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	assert.Greater(t, chunks[0].EndLine, chunks[0].StartLine)
}

// -----------------------------------------------------------------------------
// Contract: extractFunctionSignature — body-nil path
// - A body-less top-level function declaration (assembly/linkname stub) has
//   fn.Body == nil; the entire declaration text becomes the Signature.
// Go parser accepts "func Foo()" without a body at the source level.
// -----------------------------------------------------------------------------

func TestChunk_Signature_BodyNil(t *testing.T) {
	// Go parser allows a body-less function declaration syntactically.
	src := "package p\nfunc StubFunc()\n"
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	// Signature should be the full declaration text (no body to strip).
	assert.Contains(t, chunks[0].Signature, "StubFunc")
	// Content must also be non-empty (extractLines covers the whole declaration).
	assert.NotEmpty(t, chunks[0].Content)
}

// -----------------------------------------------------------------------------
// Contract: Chunk — multiple declaration kinds in one file
// - Functions, methods, structs, interfaces, type aliases, consts, and vars
//   all coexist in a single file and are all returned in one Chunk call.
// -----------------------------------------------------------------------------

func TestChunk_MixedDeclarations(t *testing.T) {
	src := `package main

const AppName = "engram"

var Version = "1.0"

type Status int

type Runner interface {
	Run() error
}

type Worker struct{ id int }

func (w *Worker) Start() {}

func standalone() {}
`
	f := writeTemp(t, src)
	c := NewChunker(chunking.DefaultChunkOptions())
	chunks, err := c.Chunk(context.Background(), f)
	require.NoError(t, err)

	// Expect: AppName(const), Version(var), Status(type), Runner(interface),
	// Worker(class), Start(method), standalone(function) = 7 chunks.
	assert.Len(t, chunks, 7)

	typeMap := map[string]chunking.ChunkType{}
	for _, ch := range chunks {
		typeMap[ch.Name] = ch.Type
	}

	assert.Equal(t, chunking.ChunkTypeConst, typeMap["AppName"])
	assert.Equal(t, chunking.ChunkTypeVar, typeMap["Version"])
	assert.Equal(t, chunking.ChunkTypeType, typeMap["Status"])
	assert.Equal(t, chunking.ChunkTypeInterface, typeMap["Runner"])
	assert.Equal(t, chunking.ChunkTypeClass, typeMap["Worker"])
	assert.Equal(t, chunking.ChunkTypeMethod, typeMap["Start"])
	assert.Equal(t, chunking.ChunkTypeFunction, typeMap["standalone"])
}
