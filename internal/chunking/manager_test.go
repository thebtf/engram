package chunking

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// mgTestChunker is a multi-extension test chunker for manager-level integration scenarios.
// It handles .go, .py, and .ts to allow ChunkFiles tests across extension variety.
type mgTestChunker struct{}

func (m *mgTestChunker) Chunk(_ context.Context, filePath string) ([]Chunk, error) {
	return []Chunk{
		{
			FilePath:  filePath,
			Language:  LanguageGo,
			Type:      ChunkTypeFunction,
			Name:      "TestFunc",
			Content:   "func TestFunc() {}",
			StartLine: 1,
			EndLine:   5,
		},
	}, nil
}

func (m *mgTestChunker) Language() Language          { return LanguageGo }
func (m *mgTestChunker) SupportedExtensions() []string { return []string{".go", ".py", ".ts"} }

// mgMultiExtChunker registers a configurable set of extensions and returns no chunks.
// Used for SupportedExtensions aggregation tests.
type mgMultiExtChunker struct {
	exts []string
}

func (m *mgMultiExtChunker) Chunk(_ context.Context, _ string) ([]Chunk, error) {
	return nil, nil
}

func (m *mgMultiExtChunker) Language() Language          { return LanguageGo }
func (m *mgMultiExtChunker) SupportedExtensions() []string { return m.exts }

// =============================================================================
// CONTRACT: Manager — multi-extension integration (ChunkFiles across file types)
// =============================================================================

// TestManager_ChunkFiles_MultiExtensionIntegration verifies that ChunkFiles routes
// files with different extensions to the same registered chunker and collects all results.
func TestManager_ChunkFiles_MultiExtensionIntegration(t *testing.T) {
	tmpDir := t.TempDir()

	goFile := filepath.Join(tmpDir, "main.go")
	pyFile := filepath.Join(tmpDir, "script.py")
	tsFile := filepath.Join(tmpDir, "app.ts")
	for _, f := range []string{goFile, pyFile, tsFile} {
		if err := os.WriteFile(f, []byte("content"), 0600); err != nil {
			t.Fatalf("create %s: %v", f, err)
		}
	}

	m := NewManager([]Chunker{&mgTestChunker{}}, DefaultChunkOptions())

	// All three extensions must be recognized.
	for _, f := range []string{goFile, pyFile, tsFile} {
		if !m.SupportsFile(f) {
			t.Errorf("SupportsFile(%q) = false, want true", f)
		}
	}
	// An unregistered extension must not be recognized.
	if m.SupportsFile(filepath.Join(tmpDir, "data.txt")) {
		t.Error("SupportsFile(.txt) = true, want false")
	}

	results, errs := m.ChunkFiles(context.Background(), []string{goFile, pyFile, tsFile})
	if len(errs) > 0 {
		t.Fatalf("ChunkFiles errors: %v", errs)
	}
	if len(results) != 3 {
		t.Errorf("want results for 3 files, got %d", len(results))
	}
	for _, f := range []string{goFile, pyFile, tsFile} {
		if chunks := results[f]; len(chunks) == 0 {
			t.Errorf("no chunks for %s", f)
		}
	}
}

// =============================================================================
// CONTRACT: Manager.SupportedExtensions — aggregation across multiple chunkers
// =============================================================================

// TestManager_SupportedExtensions_AggregatesAllChunkers verifies that
// SupportedExtensions returns the union of extensions from all registered chunkers.
func TestManager_SupportedExtensions_AggregatesAllChunkers(t *testing.T) {
	m := NewManager([]Chunker{
		&mgMultiExtChunker{exts: []string{".go"}},
		&mgMultiExtChunker{exts: []string{".py", ".pyw"}},
	}, DefaultChunkOptions())

	exts := m.SupportedExtensions()
	want := map[string]bool{".go": false, ".py": false, ".pyw": false}

	for _, ext := range exts {
		if _, ok := want[ext]; ok {
			want[ext] = true
		} else {
			t.Errorf("unexpected extension: %s", ext)
		}
	}
	for ext, found := range want {
		if !found {
			t.Errorf("expected extension %s not found in SupportedExtensions", ext)
		}
	}
}

// TestManager_SupportedExtensions_LastChunkerWinsOnCollision verifies that when two
// chunkers register the same extension, the later registration overwrites the earlier one
// and the extension appears exactly once in SupportedExtensions.
func TestManager_SupportedExtensions_LastChunkerWinsOnCollision(t *testing.T) {
	m := NewManager([]Chunker{
		&mgMultiExtChunker{exts: []string{".go", ".ts"}},
		&mgMultiExtChunker{exts: []string{".go", ".py"}}, // overlaps on .go
	}, DefaultChunkOptions())

	count := 0
	for _, ext := range m.SupportedExtensions() {
		if ext == ".go" {
			count++
		}
	}
	if count != 1 {
		t.Errorf(".go should appear exactly once after collision, got %d", count)
	}
}

// =============================================================================
// CONTRACT: Manager — ChunkFiles error isolation
// =============================================================================

// TestManager_ChunkFiles_UnrecognizedFilesProduceErrors verifies that files with
// unregistered extensions are reported as errors and do not prevent other files
// from being processed.
func TestManager_ChunkFiles_UnrecognizedFilesProduceErrors(t *testing.T) {
	tmpDir := t.TempDir()

	goFile := filepath.Join(tmpDir, "ok.go")
	txtFile := filepath.Join(tmpDir, "bad.txt")
	for _, f := range []string{goFile, txtFile} {
		if err := os.WriteFile(f, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	m := NewManager([]Chunker{&mgTestChunker{}}, DefaultChunkOptions())
	results, errs := m.ChunkFiles(context.Background(), []string{goFile, txtFile})

	if len(errs) != 1 {
		t.Errorf("want 1 error, got %d: %v", len(errs), errs)
	}
	if _, ok := results[goFile]; !ok {
		t.Error("goFile should appear in results despite error on txtFile")
	}
	if _, ok := results[txtFile]; ok {
		t.Error("txtFile should not appear in results")
	}
}

// TestManager_ChunkFiles_FilesWithNoChunksAreExcluded verifies that a file whose
// chunker returns zero chunks is omitted from the result map (no empty entry).
func TestManager_ChunkFiles_FilesWithNoChunksAreExcluded(t *testing.T) {
	tmpDir := t.TempDir()
	f := filepath.Join(tmpDir, "empty.go")
	if err := os.WriteFile(f, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}

	// Use mgMultiExtChunker which always returns nil (no chunks).
	m := NewManager([]Chunker{&mgMultiExtChunker{exts: []string{".go"}}}, DefaultChunkOptions())
	results, errs := m.ChunkFiles(context.Background(), []string{f})

	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if _, ok := results[f]; ok {
		t.Error("file producing zero chunks should be omitted from results map")
	}
}

// TestManager_ChunkFiles_EmptyInputProducesEmptyOutput verifies ChunkFiles is a
// no-op for an empty file list.
func TestManager_ChunkFiles_EmptyInputProducesEmptyOutput(t *testing.T) {
	m := NewManager([]Chunker{&mgTestChunker{}}, DefaultChunkOptions())
	results, errs := m.ChunkFiles(context.Background(), nil)
	if len(results) != 0 || len(errs) != 0 {
		t.Errorf("empty input should yield empty results and errors; got results=%d errs=%d",
			len(results), len(errs))
	}
}
