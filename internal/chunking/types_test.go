package chunking

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// CONTRACT: ChunkType constants
// Each constant must carry the string value named in its identifier.
// =============================================================================

func TestChunkType_Values(t *testing.T) {
	assert.Equal(t, ChunkType("function"), ChunkTypeFunction)
	assert.Equal(t, ChunkType("method"), ChunkTypeMethod)
	assert.Equal(t, ChunkType("class"), ChunkTypeClass)
	assert.Equal(t, ChunkType("interface"), ChunkTypeInterface)
	assert.Equal(t, ChunkType("type"), ChunkTypeType)
	assert.Equal(t, ChunkType("const"), ChunkTypeConst)
	assert.Equal(t, ChunkType("var"), ChunkTypeVar)
}

// =============================================================================
// CONTRACT: Language constants
// =============================================================================

func TestLanguage_Values(t *testing.T) {
	assert.Equal(t, Language("go"), LanguageGo)
}

// =============================================================================
// CONTRACT: Chunk.Identifier
// - Returns "ParentName.Name" when ParentName is non-empty.
// - Returns "Name" when ParentName is empty.
// =============================================================================

func TestChunk_Identifier_TopLevel(t *testing.T) {
	c := Chunk{Name: "MyFunction"}
	assert.Equal(t, "MyFunction", c.Identifier())
}

func TestChunk_Identifier_Method(t *testing.T) {
	c := Chunk{Name: "Process", ParentName: "Handler"}
	assert.Equal(t, "Handler.Process", c.Identifier())
}

func TestChunk_Identifier_EmptyName(t *testing.T) {
	c := Chunk{Name: "", ParentName: ""}
	assert.Equal(t, "", c.Identifier())
}

func TestChunk_Identifier_ParentButNoName(t *testing.T) {
	// Edge: ParentName set, Name empty → "Parent."
	c := Chunk{Name: "", ParentName: "Parent"}
	assert.Equal(t, "Parent.", c.Identifier())
}

// =============================================================================
// CONTRACT: Chunk.LineRange
// - Returns "L<start>-L<end>" using 1-indexed line numbers.
// =============================================================================

func TestChunk_LineRange_SingleLine(t *testing.T) {
	c := Chunk{StartLine: 10, EndLine: 10}
	assert.Equal(t, "L10-L10", c.LineRange())
}

func TestChunk_LineRange_MultiLine(t *testing.T) {
	c := Chunk{StartLine: 25, EndLine: 50}
	assert.Equal(t, "L25-L50", c.LineRange())
}

func TestChunk_LineRange_FirstLine(t *testing.T) {
	c := Chunk{StartLine: 1, EndLine: 5}
	assert.Equal(t, "L1-L5", c.LineRange())
}

func TestChunk_LineRange_LargeNumbers(t *testing.T) {
	c := Chunk{StartLine: 1000, EndLine: 2500}
	assert.Equal(t, "L1000-L2500", c.LineRange())
}

// =============================================================================
// CONTRACT: Chunk.SearchableContent
// - Joins non-empty Signature, DocComment, and Content with "\n\n" separators.
// - An empty chunk returns "".
// - Each non-empty field appears in the output.
// =============================================================================

func TestChunk_SearchableContent_AllFields(t *testing.T) {
	c := Chunk{
		Signature:  "func ProcessData(input []byte) error",
		DocComment: "ProcessData handles incoming data",
		Content:    "func ProcessData(input []byte) error {\n\treturn nil\n}",
	}
	assert.Equal(t, "func ProcessData(input []byte) error\n\nProcessData handles incoming data\n\nfunc ProcessData(input []byte) error {\n\treturn nil\n}", c.SearchableContent())
}

func TestChunk_SearchableContent_SignatureOnly(t *testing.T) {
	c := Chunk{Signature: "func Hello()"}
	result := c.SearchableContent()
	assert.Equal(t, "func Hello()", result)
}

func TestChunk_SearchableContent_ContentOnly(t *testing.T) {
	c := Chunk{Content: "some code here"}
	assert.Equal(t, "some code here", c.SearchableContent())
}

func TestChunk_SearchableContent_DocCommentOnly(t *testing.T) {
	c := Chunk{DocComment: "Important documentation"}
	assert.Equal(t, "Important documentation", c.SearchableContent())
}

func TestChunk_SearchableContent_Empty(t *testing.T) {
	c := Chunk{}
	assert.Equal(t, "", c.SearchableContent())
}

func TestChunk_SearchableContent_SignatureAndContent(t *testing.T) {
	c := Chunk{Signature: "func Foo()", Content: "func Foo() {}"}
	assert.Equal(t, "func Foo()\n\nfunc Foo() {}", c.SearchableContent())
}

// =============================================================================
// CONTRACT: DefaultChunkOptions
// - MaxChunkSize > 0 (~8KB).
// - IncludeDocComments == true.
// - IncludePrivate == true.
// - MinLines == 0 (no minimum).
// =============================================================================

func TestDefaultChunkOptions(t *testing.T) {
	opts := DefaultChunkOptions()
	assert.Greater(t, opts.MaxChunkSize, 0, "MaxChunkSize must be positive")
	assert.GreaterOrEqual(t, opts.MaxChunkSize, 1024)
	assert.LessOrEqual(t, opts.MaxChunkSize, 1024*1024)
	assert.True(t, opts.IncludeDocComments, "IncludeDocComments must default to true")
	assert.True(t, opts.IncludePrivate, "IncludePrivate must default to true")
	assert.Equal(t, 0, opts.MinLines, "MinLines must default to 0")
}

// =============================================================================
// CONTRACT: Manager — helper mocks (in same package, no import needed)
// =============================================================================

// singleChunkMock returns exactly one Chunk for every call.
type singleChunkMock struct {
	ext string
}

func (m *singleChunkMock) Chunk(_ context.Context, filePath string) ([]Chunk, error) {
	return []Chunk{
		{
			FilePath:  filePath,
			Language:  LanguageGo,
			Type:      ChunkTypeFunction,
			Name:      "MockFunc",
			StartLine: 1,
			EndLine:   10,
			Content:   strings.Repeat("x", 20),
		},
	}, nil
}
func (m *singleChunkMock) Language() Language            { return LanguageGo }
func (m *singleChunkMock) SupportedExtensions() []string { return []string{m.ext} }

// errorChunkMock always returns an error.
type errorChunkMock struct{ err error }

func (e *errorChunkMock) Chunk(_ context.Context, _ string) ([]Chunk, error) {
	return nil, e.err
}
func (e *errorChunkMock) Language() Language            { return LanguageGo }
func (e *errorChunkMock) SupportedExtensions() []string { return []string{".err"} }

type fixedChunksMock struct {
	ext    string
	chunks []Chunk
}

func (m *fixedChunksMock) Chunk(_ context.Context, _ string) ([]Chunk, error) {
	return m.chunks, nil
}
func (m *fixedChunksMock) Language() Language            { return LanguageGo }
func (m *fixedChunksMock) SupportedExtensions() []string { return []string{m.ext} }

// largeMock returns one chunk whose Content exceeds MaxChunkSize.
type largeMock struct{}

func (l *largeMock) Chunk(_ context.Context, filePath string) ([]Chunk, error) {
	return []Chunk{
		{
			FilePath:  filePath,
			Language:  LanguageGo,
			Type:      ChunkTypeFunction,
			Name:      "Big",
			StartLine: 1,
			EndLine:   100,
			Content:   strings.Repeat("y", 5000), // exceeds MaxChunkSize=100 in test
		},
	}, nil
}
func (l *largeMock) Language() Language            { return LanguageGo }
func (l *largeMock) SupportedExtensions() []string { return []string{".large"} }

// shortMock returns a chunk that is fewer than MinLines.
type shortMock struct{}

func (s *shortMock) Chunk(_ context.Context, filePath string) ([]Chunk, error) {
	return []Chunk{
		{
			FilePath:  filePath,
			Language:  LanguageGo,
			Type:      ChunkTypeFunction,
			Name:      "Short",
			StartLine: 1,
			EndLine:   1, // 1 line
			Content:   "x",
		},
	}, nil
}
func (s *shortMock) Language() Language            { return LanguageGo }
func (s *shortMock) SupportedExtensions() []string { return []string{".short"} }

// =============================================================================
// CONTRACT: Manager — NewManager / SupportsFile / SupportedExtensions
// =============================================================================

func TestNewManager_RegistersExtensions(t *testing.T) {
	m := NewManager([]Chunker{
		&singleChunkMock{ext: ".go"},
		&singleChunkMock{ext: ".py"},
	}, DefaultChunkOptions())
	assert.True(t, m.SupportsFile("main.go"))
	assert.True(t, m.SupportsFile("script.py"))
	assert.False(t, m.SupportsFile("file.txt"))
}

func TestNewManager_Empty(t *testing.T) {
	m := NewManager(nil, DefaultChunkOptions())
	assert.False(t, m.SupportsFile("anything.go"))
	exts := m.SupportedExtensions()
	assert.Empty(t, exts)
}

func TestManager_SupportsFile_CaseInsensitive(t *testing.T) {
	m := NewManager([]Chunker{&singleChunkMock{ext: ".go"}}, DefaultChunkOptions())
	// Extension comparison is lowercased.
	assert.True(t, m.SupportsFile("main.go"))
	assert.True(t, m.SupportsFile("main.GO"))
	assert.True(t, m.SupportsFile("main.Go"))
}

// =============================================================================
// CONTRACT: Manager.ChunkFile
// - Returns an error for unsupported extensions.
// - Propagates errors from the underlying Chunker.
// - Filters chunks where lineCount < MinLines (when MinLines > 0).
// - Filters chunks where len(Content) > MaxChunkSize (when MaxChunkSize > 0).
// - Returns all passing chunks when no filters apply.
// =============================================================================

func TestManager_ChunkFile_UnsupportedExtension(t *testing.T) {
	m := NewManager([]Chunker{&singleChunkMock{ext: ".go"}}, DefaultChunkOptions())
	tmpFile := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("data"), 0o600))
	_, err := m.ChunkFile(context.Background(), tmpFile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), ".txt")
}

func TestManager_ChunkFile_ChunkerError(t *testing.T) {
	cause := errors.New("mock chunker error")
	m := NewManager([]Chunker{&errorChunkMock{err: cause}}, DefaultChunkOptions())
	tmpFile := filepath.Join(t.TempDir(), "file.err")
	require.NoError(t, os.WriteFile(tmpFile, []byte("data"), 0o600))
	chunks, err := m.ChunkFile(context.Background(), tmpFile)
	require.Error(t, err)
	assert.ErrorIs(t, err, cause)
	assert.Contains(t, err.Error(), tmpFile)
	assert.Empty(t, chunks)
}

func TestManager_ChunkFile_Success(t *testing.T) {
	m := NewManager([]Chunker{&singleChunkMock{ext: ".go"}}, DefaultChunkOptions())
	tmpFile := filepath.Join(t.TempDir(), "file.go")
	require.NoError(t, os.WriteFile(tmpFile, []byte("package main"), 0o600))
	chunks, err := m.ChunkFile(context.Background(), tmpFile)
	require.NoError(t, err)
	assert.Equal(t, []Chunk{{
		FilePath: tmpFile, Language: LanguageGo, Type: ChunkTypeFunction,
		Name: "MockFunc", StartLine: 1, EndLine: 10, Content: strings.Repeat("x", 20),
	}}, chunks)
}

func TestManager_ChunkFile_FilterByMinLines(t *testing.T) {
	input := []Chunk{
		{Name: "one", StartLine: 1, EndLine: 1, Content: "x"},
		{Name: "below", StartLine: 2, EndLine: 5, Content: "x"},
		{Name: "at", StartLine: 2, EndLine: 6, Content: "x"},
		{Name: "above", StartLine: 2, EndLine: 7, Content: "x"},
	}
	m := NewManager([]Chunker{&fixedChunksMock{ext: ".short", chunks: input}}, ChunkOptions{MinLines: 5})
	tmpFile := filepath.Join(t.TempDir(), "file.short")
	require.NoError(t, os.WriteFile(tmpFile, []byte("x"), 0o600))
	chunks, err := m.ChunkFile(context.Background(), tmpFile)
	require.NoError(t, err)
	assert.ElementsMatch(t, []Chunk{
		{Name: "at", StartLine: 2, EndLine: 6, Content: "x"},
		{Name: "above", StartLine: 2, EndLine: 7, Content: "x"},
	}, chunks)
}

func TestManager_ChunkFile_MinLines_ZeroMeansNoFilter(t *testing.T) {
	opts := ChunkOptions{MinLines: 0}
	m := NewManager([]Chunker{&shortMock{}}, opts)
	tmpFile := filepath.Join(t.TempDir(), "file.short")
	require.NoError(t, os.WriteFile(tmpFile, []byte("x"), 0o600))
	chunks, err := m.ChunkFile(context.Background(), tmpFile)
	require.NoError(t, err)
	assert.Len(t, chunks, 1, "MinLines=0 means no filtering")
}

func TestManager_ChunkFile_FilterByMaxChunkSize(t *testing.T) {
	input := []Chunk{
		{Name: "below", Content: strings.Repeat("x", 99)},
		{Name: "at", Content: strings.Repeat("x", 100)},
		{Name: "above", Content: strings.Repeat("x", 101)},
		{Name: "unicode", Content: strings.Repeat("é", 51)},
		{Name: "large", Content: strings.Repeat("y", 5000)},
	}
	m := NewManager([]Chunker{&fixedChunksMock{ext: ".large", chunks: input}}, ChunkOptions{MaxChunkSize: 100})
	tmpFile := filepath.Join(t.TempDir(), "file.large")
	require.NoError(t, os.WriteFile(tmpFile, []byte("x"), 0o600))
	chunks, err := m.ChunkFile(context.Background(), tmpFile)
	require.NoError(t, err)
	assert.ElementsMatch(t, []Chunk{
		{Name: "below", Content: strings.Repeat("x", 99)},
		{Name: "at", Content: strings.Repeat("x", 100)},
	}, chunks)
}

func TestManager_ChunkFile_MaxChunkSize_ZeroMeansNoFilter(t *testing.T) {
	opts := ChunkOptions{MaxChunkSize: 0}
	m := NewManager([]Chunker{&largeMock{}}, opts)
	tmpFile := filepath.Join(t.TempDir(), "file.large")
	require.NoError(t, os.WriteFile(tmpFile, []byte("x"), 0o600))
	chunks, err := m.ChunkFile(context.Background(), tmpFile)
	require.NoError(t, err)
	assert.Len(t, chunks, 1, "MaxChunkSize=0 means no filtering")
}

// =============================================================================
// CONTRACT: Manager.ChunkFiles
// - Processes each file independently; errors for one file do not stop others.
// - Files with no chunks (empty result from chunker) are excluded from the map.
// - Files with errors are reported in the returned error slice.
// =============================================================================

func TestManager_ChunkFiles_MultipleFiles(t *testing.T) {
	m := NewManager([]Chunker{&singleChunkMock{ext: ".go"}}, DefaultChunkOptions())
	tmpDir := t.TempDir()

	files := []string{
		filepath.Join(tmpDir, "a.go"),
		filepath.Join(tmpDir, "b.go"),
	}
	for _, f := range files {
		require.NoError(t, os.WriteFile(f, []byte("package main"), 0o600))
	}

	results, errs := m.ChunkFiles(context.Background(), files)
	assert.Empty(t, errs)
	assert.Len(t, results, 2)
	for _, f := range files {
		assert.NotEmpty(t, results[f])
	}
}

func TestManager_ChunkFiles_AllErrors(t *testing.T) {
	cause := errors.New("mock chunker error")
	m := NewManager([]Chunker{&errorChunkMock{err: cause}, &singleChunkMock{ext: ".go"}}, DefaultChunkOptions())
	tmpDir := t.TempDir()
	first := filepath.Join(tmpDir, "first.err")
	second := filepath.Join(tmpDir, "second.err")
	success := filepath.Join(tmpDir, "success.go")
	for _, f := range []string{first, second, success} {
		require.NoError(t, os.WriteFile(f, []byte("data"), 0o600))
	}
	checkErrors := func(t *testing.T, errs []error) {
		t.Helper()
		require.Len(t, errs, 2)
		paths := make([]string, 0, len(errs))
		for _, err := range errs {
			require.Error(t, err)
			assert.ErrorIs(t, err, cause)
			for _, path := range []string{first, second} {
				if strings.Contains(err.Error(), path) {
					paths = append(paths, path)
				}
			}
		}
		assert.ElementsMatch(t, []string{first, second}, paths)
	}

	t.Run("all errors", func(t *testing.T) {
		results, errs := m.ChunkFiles(context.Background(), []string{first, second})
		assert.Empty(t, results)
		checkErrors(t, errs)
	})
	t.Run("continues after errors", func(t *testing.T) {
		results, errs := m.ChunkFiles(context.Background(), []string{first, second, success})
		checkErrors(t, errs)
		require.Len(t, results, 1)
		require.Contains(t, results, success)
		assert.Equal(t, []Chunk{{
			FilePath: success, Language: LanguageGo, Type: ChunkTypeFunction,
			Name: "MockFunc", StartLine: 1, EndLine: 10, Content: strings.Repeat("x", 20),
		}}, results[success])
	})
}
