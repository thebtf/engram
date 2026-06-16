package codeindex

import (
	"fmt"
	"os"
	"path/filepath"
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

// TestBuildManifestBasic verifies that a small tree is chunked and the
// manifest has the expected number of entries.
func TestBuildManifestBasic(t *testing.T) {
	dir := t.TempDir()
	content := sourceLines(10)
	writeTempFile(t, dir, "pkg/a.go", content)
	writeTempFile(t, dir, "pkg/b.go", content)

	opts := DefaultOptions()
	manifest, chunks, err := BuildManifest(dir, opts)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if len(manifest) == 0 {
		t.Fatal("expected non-empty manifest")
	}
	if len(manifest) != len(chunks) {
		t.Errorf("manifest len=%d != chunks len=%d", len(manifest), len(chunks))
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
	manifest, _, err := BuildManifest(dir, opts)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	for _, entry := range manifest {
		if strings.Contains(entry.FilePath, "ignored_gen.go") {
			t.Errorf("gitignore-matched file appeared in manifest: %s", entry.FilePath)
		}
	}
}

// TestBuildManifestEngramignoreHonored verifies .engramignore works the same way.
func TestBuildManifestEngramignoreHonored(t *testing.T) {
	dir := t.TempDir()

	writeTempFile(t, dir, "keep.go", sourceLines(5))
	writeTempFile(t, dir, "scratch.go", sourceLines(5))
	writeTempFile(t, dir, ".engramignore", "scratch.go\n")

	opts := DefaultOptions()
	manifest, _, err := BuildManifest(dir, opts)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	for _, entry := range manifest {
		if strings.Contains(entry.FilePath, "scratch.go") {
			t.Errorf(".engramignore-matched file appeared in manifest: %s", entry.FilePath)
		}
	}
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

// TestBuildManifestBinaryExtensionSkipped verifies that files with known
// binary extensions are skipped without reading content.
func TestBuildManifestBinaryExtensionSkipped(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "image.png", "not really a png\n")
	writeTempFile(t, dir, "lib.dll", "not really a dll\n")
	writeTempFile(t, dir, "real.go", sourceLines(3))

	manifest, _, err := BuildManifest(dir, DefaultOptions())
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	for _, entry := range manifest {
		if strings.HasSuffix(entry.FilePath, ".png") || strings.HasSuffix(entry.FilePath, ".dll") {
			t.Errorf("binary-extension file appeared in manifest: %s", entry.FilePath)
		}
	}
}

// TestBuildManifestDirSkipList verifies that node_modules and .git are pruned.
func TestBuildManifestDirSkipList(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "node_modules/lodash/index.js", sourceLines(5))
	writeTempFile(t, dir, ".git/HEAD", "ref: refs/heads/main\n")
	writeTempFile(t, dir, "src/main.go", sourceLines(5))

	manifest, _, err := BuildManifest(dir, DefaultOptions())
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	for _, entry := range manifest {
		if strings.HasPrefix(entry.FilePath, "node_modules/") {
			t.Errorf("node_modules file in manifest: %s", entry.FilePath)
		}
		if strings.HasPrefix(entry.FilePath, ".git/") {
			t.Errorf(".git file in manifest: %s", entry.FilePath)
		}
	}
}

// TestBuildManifestValidChunkIDs verifies that all manifest entries have
// non-empty 16-char ChunkIDs.
func TestBuildManifestValidChunkIDs(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "a.go", sourceLines(5))

	manifest, _, err := BuildManifest(dir, DefaultOptions())
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	for _, entry := range manifest {
		if len(entry.ChunkID) != 16 {
			t.Errorf("ChunkID=%q has length %d; want 16", entry.ChunkID, len(entry.ChunkID))
		}
	}
}

// TestBuildManifestDeterminism verifies that two calls on the same tree
// produce byte-identical manifests.
func TestBuildManifestDeterminism(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.go", "b.ts", "c.py"} {
		writeTempFile(t, dir, name, sourceLines(30))
	}

	opts := DefaultOptions()
	m1, _, err := BuildManifest(dir, opts)
	if err != nil {
		t.Fatalf("BuildManifest first: %v", err)
	}
	m2, _, err := BuildManifest(dir, opts)
	if err != nil {
		t.Fatalf("BuildManifest second: %v", err)
	}

	if len(m1) != len(m2) {
		t.Fatalf("manifest lengths differ: %d vs %d", len(m1), len(m2))
	}
	for i := range m1 {
		e1, e2 := m1[i], m2[i]
		if e1.ChunkID != e2.ChunkID || e1.ContentSHA256 != e2.ContentSHA256 ||
			e1.FilePath != e2.FilePath || e1.ByteStart != e2.ByteStart {
			t.Errorf("entry %d differs: %+v vs %+v", i, e1, e2)
		}
	}
}

// TestBuildManifestPathsNormalized verifies that all manifest FilePaths use
// forward slashes regardless of OS.
func TestBuildManifestPathsNormalized(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "sub/dir/main.go", sourceLines(5))

	manifest, _, err := BuildManifest(dir, DefaultOptions())
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	for _, entry := range manifest {
		if strings.Contains(entry.FilePath, "\\") {
			t.Errorf("FilePath contains backslash: %s", entry.FilePath)
		}
	}
}

// TestBuildManifestSubsetChunking verifies that ChunkFile can be called
// directly on a subset of files, enabling delta-only uploads for CR-003.
func TestBuildManifestSubsetChunking(t *testing.T) {
	dir := t.TempDir()
	contentA := sourceLines(5)
	contentB := sourceLines(20)
	writeTempFile(t, dir, "a.go", contentA)
	writeTempFile(t, dir, "b.go", contentB)

	// Simulate CR-003: only re-chunk b.go.
	chunks, err := ChunkFile("b.go", []byte(contentB), DefaultOptions())
	if err != nil {
		t.Fatalf("ChunkFile: %v", err)
	}
	if len(chunks) == 0 {
		t.Error("expected at least 1 chunk for b.go")
	}
	// Verify byte coverage.
	assertContiguous(t, []byte(contentB), chunks)
}

// TestBuildManifestManifestContentAbsent verifies that manifest entries do NOT
// carry Content (it stays in the Chunk slice only).
func TestBuildManifestManifestContentAbsent(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "x.go", sourceLines(5))

	manifest, chunks, err := BuildManifest(dir, DefaultOptions())
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	// ManifestEntry has no Content field by design; just verify alignment.
	if len(manifest) != len(chunks) {
		t.Errorf("manifest(%d) and chunks(%d) must have the same length", len(manifest), len(chunks))
	}
	for i, entry := range manifest {
		if entry.ChunkID != chunks[i].ChunkID() {
			t.Errorf("manifest[%d].ChunkID=%s != chunks[%d].ChunkID()=%s",
				i, entry.ChunkID, i, chunks[i].ChunkID())
		}
		if entry.ContentSHA256 != chunks[i].ContentSHA256 {
			t.Errorf("manifest[%d].ContentSHA256 mismatch", i)
		}
	}
}
