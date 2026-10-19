package proxy_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/proxy"
)

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

	id, displayName, gitRemote, err := proxy.ResolveProjectSlug(context.Background(), repoDir)
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
	id, displayName, gitRemote, err := proxy.ResolveProjectSlug(context.Background(), dir)
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

	id1, dn1, remote1, err1 := proxy.ResolveProjectSlug(context.Background(), repoDir)
	if err1 != nil {
		t.Fatalf("first call error: %v", err1)
	}

	id2, dn2, remote2, err2 := proxy.ResolveProjectSlug(context.Background(), repoDir)
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

// TestResolveProjectSlug_WorktreeMatchesMain verifies that a linked worktree of
// the same real Git repository produces the same identity as its main checkout.
func TestResolveProjectSlug_WorktreeMatchesMain(t *testing.T) {
	t.Parallel()

	mainRepo := initSyntheticGitRepo(t)
	if err := os.WriteFile(filepath.Join(mainRepo, "fixture.txt"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", mainRepo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("add", "fixture.txt")
	run("commit", "-q", "-m", "identity fixture")

	linkedRepo := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", "-q", "--detach", linkedRepo, "HEAD")

	mainID, mainName, mainRemote, err := proxy.ResolveProjectSlug(context.Background(), mainRepo)
	if err != nil {
		t.Fatalf("main repo id error: %v", err)
	}
	linkedID, linkedName, linkedRemote, err := proxy.ResolveProjectSlug(context.Background(), linkedRepo)
	if err != nil {
		t.Fatalf("linked worktree id error: %v", err)
	}
	if linkedID != mainID {
		t.Fatalf("linked worktree id %q != main id %q", linkedID, mainID)
	}
	if linkedRemote != mainRemote {
		t.Fatalf("linked worktree remote %q != main remote %q", linkedRemote, mainRemote)
	}
	if linkedName == mainName {
		t.Fatalf("fixture names unexpectedly match: %q", mainName)
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
	if err := os.WriteFile(filepath.Join(dir, ".engram-project"), data, 0o644); err != nil {
		t.Fatalf("write anchor: %v", err)
	}

	_, displayName, _, err := proxy.ResolveProjectSlug(context.Background(), dir)
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
	id, displayName, _, err := proxy.ResolveProjectSlug(context.Background(), dir)
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
	if err := os.WriteFile(filepath.Join(dir, ".engram-project"), data, 0o644); err != nil {
		t.Fatalf("write anchor: %v", err)
	}

	id, displayName, gitRemote, err := proxy.ResolveProjectSlug(context.Background(), dir)
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
