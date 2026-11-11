package codeindex

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeTempFile creates a file at dir/relPath with the given content.
func writeTempFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", full, err)
	}
}

// sourceLines returns n short numbered lines suitable as test source content.
func sourceLines(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "// line %04d\n", i)
	}
	return sb.String()
}

func assertManifestPaths(t *testing.T, manifest Manifest, chunks []Chunk, present, absent []string) {
	t.Helper()
	manifestPaths := make(map[string]int)
	chunkPaths := make(map[string]int)
	for _, entry := range manifest {
		manifestPaths[entry.FilePath]++
	}
	for _, chunk := range chunks {
		chunkPaths[chunk.FilePath]++
	}
	for _, path := range present {
		if manifestPaths[path] != 1 || chunkPaths[path] != 1 {
			t.Errorf("%s: manifest count=%d, chunk count=%d; want one each", path, manifestPaths[path], chunkPaths[path])
		}
	}
	for _, path := range absent {
		if manifestPaths[path] != 0 || chunkPaths[path] != 0 {
			t.Errorf("%s: manifest count=%d, chunk count=%d; want neither", path, manifestPaths[path], chunkPaths[path])
		}
	}
}

// TestBuildManifestBasic verifies that a small tree is chunked and the
// manifest has the expected number of entries.
func TestBuildManifestBasic(t *testing.T) {
	dir := t.TempDir()
	want := map[string]string{"pkg/a.go": sourceLines(10), "pkg/b.go": sourceLines(5)}
	for path, content := range want {
		writeTempFile(t, dir, path, content)
	}

	opts := DefaultOptions()
	manifest, chunks, err := BuildManifest(dir, opts)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if len(manifest) != 2 || len(chunks) != 2 {
		t.Fatalf("manifest len=%d, chunks len=%d; want 2 each", len(manifest), len(chunks))
	}
	assertManifestPaths(t, manifest, chunks, []string{"pkg/a.go", "pkg/b.go"}, nil)
	for _, chunk := range chunks {
		if chunk.Content != want[chunk.FilePath] {
			t.Errorf("%s: content does not match source", chunk.FilePath)
		}
	}
}

// TestBuildManifestGitignoreHonored verifies that files matching .gitignore
// patterns are excluded from the manifest.
func TestBuildManifestGitignoreHonored(t *testing.T) {
	dir := t.TempDir()

	writeTempFile(t, dir, "included.go", sourceLines(5))
	writeTempFile(t, dir, "ignored_gen.go", sourceLines(5))
	writeTempFile(t, dir, ".gitignore", "ignored_gen.go\n")

	opts := DefaultOptions()
	manifest, chunks, err := BuildManifest(dir, opts)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	assertManifestPaths(t, manifest, chunks, []string{"included.go"}, []string{"ignored_gen.go"})
}

// TestBuildManifestEngramignoreHonored verifies .engramignore works the same way.
func TestBuildManifestEngramignoreHonored(t *testing.T) {
	dir := t.TempDir()

	writeTempFile(t, dir, "keep.go", sourceLines(5))
	writeTempFile(t, dir, "scratch.go", sourceLines(5))
	writeTempFile(t, dir, ".engramignore", "scratch.go\n")

	opts := DefaultOptions()
	manifest, chunks, err := BuildManifest(dir, opts)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	assertManifestPaths(t, manifest, chunks, []string{"keep.go"}, []string{"scratch.go"})
}

// TestBuildManifestBinarySkipped verifies that files with NUL bytes are skipped.
func TestBuildManifestBinarySkipped(t *testing.T) {
	dir := t.TempDir()

	// A file with a NUL byte → binary.
	binaryContent := []byte{0x7F, 0x45, 0x4C, 0x46, 0x00, 0x01} // ELF magic
	writeTempFile(t, dir, "binary.bin", string(binaryContent))
	// Even if named .go, NUL byte should trigger binary skip.
	binaryGo := append([]byte("package main\n"), 0x00)
	writeTempFile(t, dir, "weird.go", string(binaryGo))

	writeTempFile(t, dir, "normal.go", sourceLines(5))

	opts := DefaultOptions()
	manifest, _, err := BuildManifest(dir, opts)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	for _, entry := range manifest {
		if strings.HasSuffix(entry.FilePath, "binary.bin") {
			t.Errorf("binary file appeared in manifest: %s", entry.FilePath)
		}
		if strings.HasSuffix(entry.FilePath, "weird.go") {
			t.Errorf("NUL-containing .go file appeared in manifest: %s", entry.FilePath)
		}
	}
	// normal.go must be present.
	found := false
	for _, entry := range manifest {
		if strings.HasSuffix(entry.FilePath, "normal.go") {
			found = true
		}
	}
	if !found {
		t.Error("normal.go should appear in manifest")
	}
}

// TestBuildManifestBinaryExtensionSkipped verifies that known binary extensions
// are excluded even when the files contain text.
func TestBuildManifestBinaryExtensionSkipped(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "image.png", "not really a png\n")
	writeTempFile(t, dir, "lib.dll", "not really a dll\n")
	writeTempFile(t, dir, "upper.PNG", "also text\n")
	writeTempFile(t, dir, "real.go", sourceLines(3))

	manifest, chunks, err := BuildManifest(dir, DefaultOptions())
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	assertManifestPaths(t, manifest, chunks, []string{"real.go"}, []string{"image.png", "lib.dll", "upper.PNG"})
}

// TestBuildManifestDirSkipList verifies that node_modules and .git are pruned.
func TestBuildManifestDirSkipList(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "node_modules/lodash/index.js", sourceLines(5))
	writeTempFile(t, dir, ".git/HEAD", "ref: refs/heads/main\n")
	writeTempFile(t, dir, "src/node_modules/hidden.js", sourceLines(5))
	writeTempFile(t, dir, "src/main.go", sourceLines(5))

	manifest, chunks, err := BuildManifest(dir, DefaultOptions())
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	assertManifestPaths(t, manifest, chunks, []string{"src/main.go"},
		[]string{"node_modules/lodash/index.js", ".git/HEAD", "src/node_modules/hidden.js"})
}

// TestBuildManifestDeterminism verifies stable metadata and sorted positions.
func TestBuildManifestDeterminism(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "a.go", sourceLines(100))
	writeTempFile(t, dir, "b.ts", sourceLines(10))
	writeTempFile(t, dir, "c.py", sourceLines(5))

	opts := DefaultOptions()
	m1, _, err := BuildManifest(dir, opts)
	if err != nil {
		t.Fatalf("BuildManifest first: %v", err)
	}
	m2, _, err := BuildManifest(dir, opts)
	if err != nil {
		t.Fatalf("BuildManifest second: %v", err)
	}

	if len(m1) != 4 || len(m2) != 4 {
		t.Fatalf("manifest lengths: %d and %d; want 4 each", len(m1), len(m2))
	}
	if !reflect.DeepEqual(m1, m2) {
		t.Errorf("manifests differ: %+v vs %+v", m1, m2)
	}
	wantPaths := []string{"a.go", "a.go", "b.ts", "c.py"}
	wantStarts := []int{0, len(sourceLines(70)), 0, 0}
	for i, entry := range m1 {
		if entry.FilePath != wantPaths[i] || entry.ByteStart != wantStarts[i] {
			t.Errorf("entry %d: path=%q start=%d; want %q start=%d", i, entry.FilePath, entry.ByteStart, wantPaths[i], wantStarts[i])
		}
	}
}

// TestBuildManifestPathsNormalized verifies that all manifest FilePaths use
// forward slashes regardless of OS.
func TestBuildManifestPathsNormalized(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "sub/dir/main.go", sourceLines(5))

	manifest, chunks, err := BuildManifest(dir, DefaultOptions())
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if len(manifest) != 1 || len(chunks) != 1 {
		t.Fatalf("manifest len=%d, chunks len=%d; want 1 each", len(manifest), len(chunks))
	}
	assertManifestPaths(t, manifest, chunks, []string{"sub/dir/main.go"}, nil)
	id := sha256.Sum256([]byte("sub/dir/main.go:0"))
	if want := fmt.Sprintf("%x", id[:8]); manifest[0].ChunkID != want || chunks[0].ChunkID() != want {
		t.Errorf("normalized path ID: manifest=%q chunk=%q; want %q", manifest[0].ChunkID, chunks[0].ChunkID(), want)
	}
}

// TestBuildManifestManifestContentAbsent verifies that manifest entries do NOT
// carry Content (it stays in the Chunk slice only).
func TestBuildManifestManifestContentAbsent(t *testing.T) {
	dir := t.TempDir()
	sources := map[string]string{"x.go": sourceLines(75), "y.ts": sourceLines(5)}
	for path, content := range sources {
		writeTempFile(t, dir, path, content)
	}

	manifest, chunks, err := BuildManifest(dir, DefaultOptions())
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if len(manifest) != 3 || len(chunks) != 3 {
		t.Fatalf("manifest len=%d, chunks len=%d; want 3 each", len(manifest), len(chunks))
	}
	if _, ok := reflect.TypeOf(ManifestEntry{}).FieldByName("Content"); ok {
		t.Error("ManifestEntry must not contain a Content field")
	}
	wantPaths := []string{"x.go", "x.go", "y.ts"}
	wantStarts := []int{0, len(sourceLines(70)), 0}
	wantEnds := []int{len(sourceLines(70)), len(sources["x.go"]), len(sources["y.ts"])}
	wantLanguages := []string{"go", "go", "typescript"}
	for i, entry := range manifest {
		path, start, end := wantPaths[i], wantStarts[i], wantEnds[i]
		content := sources[path][start:end]
		id := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", path, start)))
		digest := sha256.Sum256([]byte(content))
		want := ManifestEntry{
			FilePath: path, ChunkID: fmt.Sprintf("%x", id[:8]), ContentSHA256: fmt.Sprintf("%x", digest),
			ByteStart: start, ByteEnd: end, Language: wantLanguages[i], ChunkType: ChunkTypeLineBlock,
		}
		if entry != want {
			t.Errorf("manifest[%d]=%+v; want %+v", i, entry, want)
		}
		chunk := chunks[i]
		if chunk.FilePath != path || chunk.ByteStart != start || chunk.ByteEnd != end ||
			chunk.Language != want.Language || chunk.ChunkType != want.ChunkType ||
			chunk.Content != content || chunk.ContentSHA256 != want.ContentSHA256 || chunk.ChunkID() != want.ChunkID {
			t.Errorf("chunks[%d]=%+v; want metadata %+v and source content", i, chunk, want)
		}
	}
}
