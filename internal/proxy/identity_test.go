package proxy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/proxy"
)

// findRepoRoot returns the absolute path of the current git repository root.
// It fails the test immediately if the git command fails.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("failed to determine git repo root: %v", err)
	}
	return filepath.Clean(strings.TrimSpace(string(out)))
}

// TestResolveProjectSlug_GitRepo verifies that a directory that is a git repo
// with a remote produces a slug of the form "engram_<8hexchars>" and a non-empty
// gitRemote.
func TestResolveProjectSlug_GitRepo(t *testing.T) {
	t.Parallel()

	repoDir := findRepoRoot(t)

	slug, gitRemote, err := proxy.ResolveProjectSlug(repoDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Logf("slug=%s  gitRemote=%s", slug, gitRemote)

	// The slug prefix must equal the base name of the directory passed in.
	dirName := filepath.Base(repoDir)
	expectedPrefix := dirName + "_"
	if !strings.HasPrefix(slug, expectedPrefix) {
		t.Errorf("slug %q does not start with %q", slug, expectedPrefix)
	}

	// Suffix must be exactly 8 lowercase hex characters.
	suffix := strings.TrimPrefix(slug, expectedPrefix)
	matched, _ := regexp.MatchString(`^[0-9a-f]{8}$`, suffix)
	if !matched {
		t.Errorf("slug suffix %q is not 8 hex chars", suffix)
	}

	if gitRemote == "" {
		t.Error("gitRemote should be non-empty for a git repo with a remote")
	}
}

// TestResolveProjectSlug_NonGitDir verifies that a directory without a git repo
// falls back to a path-based slug of the form "<dirName>_<6hexchars>" with an
// empty gitRemote. os.TempDir() is a reliable non-git directory.
func TestResolveProjectSlug_NonGitDir(t *testing.T) {
	t.Parallel()

	dir := os.TempDir()
	slug, gitRemote, err := proxy.ResolveProjectSlug(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Logf("slug=%s  gitRemote=%s", slug, gitRemote)

	if gitRemote != "" {
		t.Errorf("expected empty gitRemote for non-git dir, got %q", gitRemote)
	}

	// Slug must end with exactly 6 lowercase hex characters after an underscore.
	matched, _ := regexp.MatchString(`^.+_[0-9a-f]{6}$`, slug)
	if !matched {
		t.Errorf("slug %q does not match <dirName>_<6hexchars>", slug)
	}
}

// TestResolveProjectSlug_ConsistentAcrossCalls verifies that calling
// ResolveProjectSlug twice with the same cwd produces identical results.
func TestResolveProjectSlug_ConsistentAcrossCalls(t *testing.T) {
	t.Parallel()

	repoDir := findRepoRoot(t)

	slug1, remote1, err1 := proxy.ResolveProjectSlug(repoDir)
	if err1 != nil {
		t.Fatalf("first call error: %v", err1)
	}

	slug2, remote2, err2 := proxy.ResolveProjectSlug(repoDir)
	if err2 != nil {
		t.Fatalf("second call error: %v", err2)
	}

	if slug1 != slug2 {
		t.Errorf("slugs differ across calls: %q vs %q", slug1, slug2)
	}
	if remote1 != remote2 {
		t.Errorf("gitRemotes differ across calls: %q vs %q", remote1, remote2)
	}
}

// TestResolveProjectSlug_WorktreeMatchesMain verifies that a worktree of the
// same repository produces the same slug as the main checkout. Skipped when no
// worktree is present.
func TestResolveProjectSlug_WorktreeMatchesMain(t *testing.T) {
	t.Parallel()

	mainRepo := findRepoRoot(t)

	out, err := exec.Command("git", "-C", mainRepo, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Skip("git worktree list failed, skipping")
	}

	// Parse worktree paths: lines starting with "worktree ".
	var worktreePaths []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		path := strings.TrimPrefix(line, "worktree ")
		// Use filepath.Clean for portable cross-platform path comparison.
		if !strings.EqualFold(filepath.Clean(path), filepath.Clean(mainRepo)) {
			worktreePaths = append(worktreePaths, path)
		}
	}

	if len(worktreePaths) == 0 {
		t.Skip("no additional worktrees found, skipping")
	}

	mainSlug, _, err := proxy.ResolveProjectSlug(mainRepo)
	if err != nil {
		t.Fatalf("main repo slug error: %v", err)
	}

	// The slug format is "<dirName>_<hash8>". A worktree checked out under a
	// different directory name will have a different dirName prefix, but the
	// hash suffix encodes the repo identity (remoteURL + relativePath) and MUST
	// be identical across worktrees of the same repo.
	mainParts := strings.SplitN(mainSlug, "_", 2)
	if len(mainParts) != 2 {
		t.Fatalf("unexpected main slug format: %q", mainSlug)
	}
	mainHash := mainParts[1]

	for _, wt := range worktreePaths {
		wtSlug, _, wtErr := proxy.ResolveProjectSlug(wt)
		if wtErr != nil {
			t.Errorf("worktree %s slug error: %v", wt, wtErr)
			continue
		}
		wtParts := strings.SplitN(wtSlug, "_", 2)
		if len(wtParts) != 2 {
			t.Errorf("unexpected worktree slug format: %q", wtSlug)
			continue
		}
		wtHash := wtParts[1]
		if wtHash != mainHash {
			t.Errorf("worktree %s hash suffix %q != main hash suffix %q (full slugs: %q vs %q)",
				wt, wtHash, mainHash, wtSlug, mainSlug)
		}
	}
}
