package proxy_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/proxy"
)

// findRealRepoRoot returns the absolute path of the current git repository
// root. It exists solely for TestResolveProjectSlug_WorktreeMatchesMain,
// which MUST inspect a real engram repo because its purpose is to verify
// worktree-vs-main-checkout id stability in a real git environment. All
// other tests in this file use initSyntheticGitRepo for full isolation
// from the running checkout's git state.
func findRealRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("failed to determine git repo root: %v", err)
	}
	return filepath.Clean(strings.TrimSpace(string(out)))
}

// initSyntheticGitRepo creates a fresh, isolated git repository inside
// t.TempDir() with a fixed remote URL. This replaces the previous
// findRepoRoot helper, which was brittle when the test ran inside a git
// worktree checkout of the engram repo — git rev-parse --show-toplevel
// would return the worktree path, but proxy.ResolveProjectSlug would
// follow the gitdir pointer up to the main checkout and compute the hash
// over the MAIN repo's remote + relative path, producing mismatches with
// the worktree's directory basename. A self-contained test repo with a
// known remote eliminates that coupling entirely.
//
// Returns the absolute path of the new repo.
func initSyntheticGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.invalid")
	run("config", "user.name", "identity-test")
	run("remote", "add", "origin", "https://example.invalid/test/engram-identity-fixture.git")
	return filepath.Clean(dir)
}

// TestResolveProjectSlug_GitRepo verifies that a directory that is a git
// repo with a remote produces a pure 8-hex-char id, a non-empty gitRemote,
// and displayName equal to the directory base name.
//
// Isolation: creates a synthetic git repo in t.TempDir() with a known
// remote. The test does NOT depend on the running process's cwd git state,
// eliminating the worktree-vs-main-checkout brittleness flagged as engram
// issue #74.
func TestResolveProjectSlug_GitRepo(t *testing.T) {
	t.Parallel()

	repoDir := initSyntheticGitRepo(t)

	id, displayName, gitRemote, err := proxy.ResolveProjectSlug(repoDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Logf("id=%s  displayName=%s  gitRemote=%s", id, displayName, gitRemote)

	// ID must be exactly 8 lowercase hex characters — no dirName prefix.
	matched, _ := regexp.MatchString(`^[0-9a-f]{8}$`, id)
	if !matched {
		t.Errorf("id %q is not 8 hex chars", id)
	}

	// displayName must equal the base name of the directory.
	dirName := filepath.Base(repoDir)
	if displayName != dirName {
		t.Errorf("displayName %q does not match directory base name %q", displayName, dirName)
	}

	if gitRemote == "" {
		t.Error("gitRemote should be non-empty for a git repo with a remote")
	}

	// The remote URL must match exactly what we set in initSyntheticGitRepo.
	const expectedRemote = "https://example.invalid/test/engram-identity-fixture.git"
	if gitRemote != expectedRemote {
		t.Errorf("gitRemote %q, expected %q", gitRemote, expectedRemote)
	}
}

// TestResolveProjectSlug_NonGitDir verifies that a directory without a git repo
// falls back to a pure 6-hex-char id with an empty gitRemote.
// Uses a fresh temp dir to avoid .engram-project side effects from other tests.
func TestResolveProjectSlug_NonGitDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	id, displayName, gitRemote, err := proxy.ResolveProjectSlug(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Logf("id=%s  displayName=%s  gitRemote=%s", id, displayName, gitRemote)

	if gitRemote != "" {
		t.Errorf("expected empty gitRemote for non-git dir, got %q", gitRemote)
	}

	// ID must be exactly 6 lowercase hex characters — no dirName prefix.
	matched, _ := regexp.MatchString(`^[0-9a-f]{6}$`, id)
	if !matched {
		t.Errorf("id %q is not 6 hex chars", id)
	}

	// displayName must equal the directory base name.
	if displayName != filepath.Base(dir) {
		t.Errorf("displayName %q does not match directory base name %q", displayName, filepath.Base(dir))
	}
}

// TestResolveProjectSlug_ConsistentAcrossCalls verifies that calling
// ResolveProjectSlug twice with the same cwd produces identical results.
// Uses the synthetic git repo helper so the test does not depend on the
// real engram repo's git state.
func TestResolveProjectSlug_ConsistentAcrossCalls(t *testing.T) {
	t.Parallel()

	repoDir := initSyntheticGitRepo(t)

	id1, dn1, remote1, err1 := proxy.ResolveProjectSlug(repoDir)
	if err1 != nil {
		t.Fatalf("first call error: %v", err1)
	}

	id2, dn2, remote2, err2 := proxy.ResolveProjectSlug(repoDir)
	if err2 != nil {
		t.Fatalf("second call error: %v", err2)
	}

	if id1 != id2 {
		t.Errorf("ids differ across calls: %q vs %q", id1, id2)
	}
	if dn1 != dn2 {
		t.Errorf("displayNames differ across calls: %q vs %q", dn1, dn2)
	}
	if remote1 != remote2 {
		t.Errorf("gitRemotes differ across calls: %q vs %q", remote1, remote2)
	}
}

// TestResolveProjectSlug_WorktreeMatchesMain verifies that a worktree of the
// same repository produces the same id as the main checkout. Skipped when no
// worktree is present.
func TestResolveProjectSlug_WorktreeMatchesMain(t *testing.T) {
	t.Parallel()

	mainRepo := findRealRepoRoot(t)

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

	mainID, _, _, err := proxy.ResolveProjectSlug(mainRepo)
	if err != nil {
		t.Fatalf("main repo id error: %v", err)
	}

	// The id is a pure 8-hex hash of (remoteURL + relativePath).
	// A worktree checked out under a different directory name will have a different
	// displayName but the SAME id (same remote, same relative path from repo root).
	for _, wt := range worktreePaths {
		wtID, _, _, wtErr := proxy.ResolveProjectSlug(wt)
		if wtErr != nil {
			t.Errorf("worktree %s id error: %v", wt, wtErr)
			continue
		}
		if wtID != mainID {
			t.Errorf("worktree %s id %q != main id %q", wt, wtID, mainID)
		}
	}
}

// ---------------------------------------------------------------------------
// T006: .engram-project anchor file tests
// ---------------------------------------------------------------------------

// TestResolveProjectSlug_AnchorFile_CustomName verifies that a .engram-project
// file with {"name": "custom-name"} overrides displayName.
func TestResolveProjectSlug_AnchorFile_CustomName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	anchor := map[string]string{"name": "custom-name"}
	data, _ := json.Marshal(anchor)
	if err := os.WriteFile(filepath.Join(dir, ".engram-project"), data, 0644); err != nil {
		t.Fatalf("write anchor: %v", err)
	}

	_, displayName, _, err := proxy.ResolveProjectSlug(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if displayName != "custom-name" {
		t.Errorf("expected displayName %q, got %q", "custom-name", displayName)
	}
}

// TestResolveProjectSlug_AnchorFile_AutoCreated verifies that calling
// ResolveProjectSlug on a non-git dir without an anchor file creates one.
func TestResolveProjectSlug_AnchorFile_AutoCreated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	id, displayName, _, err := proxy.ResolveProjectSlug(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	anchorPath := filepath.Join(dir, ".engram-project")
	data, readErr := os.ReadFile(anchorPath)
	if readErr != nil {
		t.Fatalf(".engram-project not auto-created: %v", readErr)
	}

	var anchor struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(data, &anchor); err != nil {
		t.Fatalf("anchor file JSON invalid: %v", err)
	}

	if anchor.Name != displayName {
		t.Errorf("anchor name %q != displayName %q", anchor.Name, displayName)
	}
	if anchor.ID != id {
		t.Errorf("anchor id %q != id %q", anchor.ID, id)
	}
}

// TestResolveProjectSlug_AnchorFile_NonGitStoredID verifies that a non-git project
// reads its stable ID from the .engram-project anchor file.
func TestResolveProjectSlug_AnchorFile_NonGitStoredID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	anchor := map[string]string{"name": "notes", "id": "abc123"}
	data, _ := json.Marshal(anchor)
	if err := os.WriteFile(filepath.Join(dir, ".engram-project"), data, 0644); err != nil {
		t.Fatalf("write anchor: %v", err)
	}

	id, displayName, gitRemote, err := proxy.ResolveProjectSlug(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if id != "abc123" {
		t.Errorf("expected id %q from anchor, got %q", "abc123", id)
	}
	if displayName != "notes" {
		t.Errorf("expected displayName %q from anchor, got %q", "notes", displayName)
	}
	if gitRemote != "" {
		t.Errorf("expected empty gitRemote for non-git dir, got %q", gitRemote)
	}
}
