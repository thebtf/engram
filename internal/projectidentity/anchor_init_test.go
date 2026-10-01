package projectidentity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitRepositoryAnchorV3CreateReplayAndPreserveMalformed(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	path := filepath.Join(root, anchorFilenameV3)
	first, created, tracked, err := InitRepositoryAnchorV3(root, "first")
	if err != nil || !created || tracked {
		t.Fatalf("create = (%+v, %t, %t, %v)", first, created, tracked, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	second, created, tracked, err := InitRepositoryAnchorV3(root, "second")
	if err != nil || created || tracked || second != first {
		t.Fatalf("replay = (%+v, %t, %t, %v), first = %+v", second, created, tracked, err, first)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("replay changed anchor: %v", err)
	}
	malformed := []byte(`{"version":3`)
	if err := os.WriteFile(path, malformed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := InitRepositoryAnchorV3(root, "third"); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("malformed anchor refusal = %v", err)
	}
	after, err = os.ReadFile(path)
	if err != nil || string(after) != string(malformed) {
		t.Fatalf("malformed anchor changed: %v", err)
	}
}

func TestInitRepositoryAnchorV3RefusesConflictingFilesystemAndGitState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T, string)
		want    string
	}{
		{"directory anchor", func(t *testing.T, root string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, anchorFilenameV3), []byte(`{"version":3,"project_id":"11111111-1111-4111-8111-111111111111","name":"other","scope":"directory"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "not a valid repository V3 anchor"},
		{"directory at anchor path", func(t *testing.T, root string) {
			t.Helper()
			if err := os.Mkdir(filepath.Join(root, anchorFilenameV3), 0o700); err != nil {
				t.Fatal(err)
			}
		}, "not a regular file"},
		{"tracked deletion", func(t *testing.T, root string) {
			t.Helper()
			path := filepath.Join(root, anchorFilenameV3)
			if err := os.WriteFile(path, []byte(validRepositoryAnchorV3), 0o600); err != nil {
				t.Fatal(err)
			}
			runGit(t, root, "add", "--", anchorFilenameV3)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}, "tracked but missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			runGit(t, root, "init")
			tc.prepare(t, root)
			if _, _, _, err := InitRepositoryAnchorV3(root, "new"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestInitRepositoryAnchorV3RequiresExactCurrentGitRootAndValidName(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := InitRepositoryAnchorV3(nested, "valid"); err == nil || !strings.Contains(err.Error(), "current Git root") {
		t.Fatalf("nested root refusal = %v", err)
	}
	if _, _, _, err := InitRepositoryAnchorV3(root, strings.Repeat("x", 257)); err == nil || !strings.Contains(err.Error(), "invalid project name") {
		t.Fatalf("invalid name refusal = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, anchorFilenameV3)); !os.IsNotExist(err) {
		t.Fatalf("invalid name created anchor: %v", err)
	}
}

func TestInitRepositoryAnchorV3RefusesSymlink(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	target := filepath.Join(root, "original")
	if err := os.WriteFile(target, []byte(validRepositoryAnchorV3), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, anchorFilenameV3)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, _, err := InitRepositoryAnchorV3(root, "new"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlink refusal = %v", err)
	}
}
