package proxy_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/thebtf/engram/internal/proxy"
)

type identityVectorFile struct {
	IdentityVersion uint32           `json:"identity_version"`
	Vectors         []identityVector `json:"vectors"`
	InvalidVectors  []identityVector `json:"invalid_vectors"`
}

type identityVector struct {
	Name            string `json:"name"`
	InvalidTarget   string `json:"invalid_target"`
	Selector        string `json:"selector"`
	DisplayName     string `json:"display_name"`
	LegacyProjectID string `json:"legacy_project_id"`
	GitRemote       string `json:"git_remote"`
	RelativePath    string `json:"relative_path"`
	NonGitAnchor    string `json:"non_git_anchor"`
	AnchorShared    *bool  `json:"anchor_shared"`
}

func TestProjectIdentityV2_RejectsSharedInvalidMetadataVectors(t *testing.T) {
	vectors := loadIdentityVectors(t)
	for _, vector := range vectors.InvalidVectors {
		if vector.InvalidTarget != "identity" {
			continue
		}
		vector := vector
		t.Run(vector.Name, func(t *testing.T) {
			err := proxy.ValidateProjectIdentityV2(proxy.ProjectIdentityV2{
				Version:         vectors.IdentityVersion,
				LegacyProjectID: vector.LegacyProjectID,
				DisplayName:     vector.DisplayName,
				GitRemote:       vector.GitRemote,
				RelativePath:    vector.RelativePath,
				NonGitAnchor:    vector.NonGitAnchor,
				AnchorShared:    vector.AnchorShared,
			})
			if err == nil || !strings.Contains(err.Error(), "PROJECT_IDENTITY_INVALID") {
				t.Fatalf("invalid shared vector accepted: %v", err)
			}
		})
	}
}

func loadIdentityVectors(t *testing.T) identityVectorFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".agent", "specs", "security-project-identity", "evidence", "project-identity-v2-vectors.json"))
	if err != nil {
		t.Fatalf("read shared identity vectors: %v", err)
	}
	var vectors identityVectorFile
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("decode shared identity vectors: %v", err)
	}
	return vectors
}

func TestProjectIdentityV2_SharedVectors(t *testing.T) {
	vectors := loadIdentityVectors(t)
	if vectors.IdentityVersion != proxy.ProjectIdentityVersionV2 {
		t.Fatalf("vector identity version=%d, implementation=%d", vectors.IdentityVersion, proxy.ProjectIdentityVersionV2)
	}
	for _, vector := range vectors.Vectors {
		vector := vector
		t.Run(vector.Name, func(t *testing.T) {
			identity := proxy.ProjectIdentityV2{
				Version:         vectors.IdentityVersion,
				LegacyProjectID: vector.LegacyProjectID,
				DisplayName:     vector.DisplayName,
				GitRemote:       vector.GitRemote,
				RelativePath:    vector.RelativePath,
				NonGitAnchor:    vector.NonGitAnchor,
				AnchorShared:    vector.AnchorShared,
			}
			if err := proxy.ValidateProjectIdentityV2(identity); err != nil {
				t.Fatalf("shared vector rejected: %v", err)
			}
		})
	}
}

func TestResolveProjectIdentityV2_NonGitAnchorStrictAndStable(t *testing.T) {
	dir := t.TempDir()
	first, err := proxy.ResolveProjectIdentityV2(dir)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	second, err := proxy.ResolveProjectIdentityV2(dir)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if first.NonGitAnchor != second.NonGitAnchor {
		t.Fatalf("anchor changed: %q != %q", first.NonGitAnchor, second.NonGitAnchor)
	}
	if matched, _ := regexp.MatchString(`^[0-9a-f]{32}$`, first.NonGitAnchor); !matched {
		t.Fatalf("anchor %q is not a strict 128-bit lowercase hex value", first.NonGitAnchor)
	}
	if first.AnchorShared == nil || *first.AnchorShared {
		t.Fatalf("new anchor must explicitly default to unshared: %#v", first.AnchorShared)
	}
	other, err := proxy.ResolveProjectIdentityV2(t.TempDir())
	if err != nil {
		t.Fatalf("resolve independent project: %v", err)
	}
	if other.NonGitAnchor == first.NonGitAnchor {
		t.Fatal("independent projects received the same anchor; generator is not high entropy")
	}
	bad := first
	bad.NonGitAnchor = "path-derived"
	if err := proxy.ValidateProjectIdentityV2(bad); err == nil || !strings.Contains(err.Error(), "PROJECT_IDENTITY_INVALID") {
		t.Fatalf("invalid anchor error=%v", err)
	}
}

func TestResolveProjectIdentityV2_ConcurrentFirstUseConverges(t *testing.T) {
	dir := t.TempDir()
	const callers = 24
	identities := make([]proxy.ProjectIdentityV2, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			identities[i], errs[i] = proxy.ResolveProjectIdentityV2(dir)
		}(i)
	}
	wg.Wait()
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if identities[i].NonGitAnchor != identities[0].NonGitAnchor {
			t.Fatalf("caller %d got divergent anchor %q != %q", i, identities[i].NonGitAnchor, identities[0].NonGitAnchor)
		}
	}
	assertCompleteProjectAnchorV2(t, dir, identities[0].NonGitAnchor)
}

func TestResolveProjectIdentityV2_PreExistingAnchorsAreNeverReplaced(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		dir := t.TempDir()
		anchorPath := filepath.Join(dir, ".engram-project-v2.json")
		original := []byte("{\n  \"version\": 2,\n  \"anchor\": \"00112233445566778899aabbccddeeff\",\n  \"shared\": false\n}\n")
		if err := os.WriteFile(anchorPath, original, 0o600); err != nil {
			t.Fatal(err)
		}
		const callers = 16
		var wg sync.WaitGroup
		errs := make([]error, callers)
		for i := range callers {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				identity, err := proxy.ResolveProjectIdentityV2(dir)
				errs[i] = err
				if err == nil && identity.NonGitAnchor != "00112233445566778899aabbccddeeff" {
					errs[i] = &identityTestError{message: "pre-existing anchor changed"}
				}
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("caller %d: %v", i, err)
			}
		}
		got, err := os.ReadFile(anchorPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(original) {
			t.Fatalf("pre-existing valid anchor bytes changed:\n%s", got)
		}
		assertNoProjectAnchorTempFiles(t, dir)
	})

	t.Run("malformed", func(t *testing.T) {
		dir := t.TempDir()
		anchorPath := filepath.Join(dir, ".engram-project-v2.json")
		original := []byte(`{"version":2`)
		if err := os.WriteFile(anchorPath, original, 0o600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 8; i++ {
			_, err := proxy.ResolveProjectIdentityV2(dir)
			if err == nil || !strings.Contains(err.Error(), "PROJECT_IDENTITY_INVALID") {
				t.Fatalf("attempt %d error=%v, want fail-closed invalid", i, err)
			}
		}
		got, err := os.ReadFile(anchorPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(original) {
			t.Fatalf("malformed anchor was replaced: %q", got)
		}
		assertNoProjectAnchorTempFiles(t, dir)
	})

	t.Run("missing shared", func(t *testing.T) {
		dir := t.TempDir()
		anchorPath := filepath.Join(dir, ".engram-project-v2.json")
		original := []byte(`{"version":2,"anchor":"00112233445566778899aabbccddeeff"}`)
		if err := os.WriteFile(anchorPath, original, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := proxy.ResolveProjectIdentityV2(dir)
		if err == nil || !strings.Contains(err.Error(), "PROJECT_IDENTITY_INVALID") {
			t.Fatalf("error=%v, want missing shared rejection", err)
		}
		got, err := os.ReadFile(anchorPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(original) {
			t.Fatalf("anchor missing shared was replaced: %q", got)
		}
		assertNoProjectAnchorTempFiles(t, dir)
	})
}

type identityTestError struct{ message string }

func (e *identityTestError) Error() string { return e.message }

func assertCompleteProjectAnchorV2(t *testing.T, dir, expectedAnchor string) {
	t.Helper()
	anchorPath := filepath.Join(dir, ".engram-project-v2.json")
	data, err := os.ReadFile(anchorPath)
	if err != nil {
		t.Fatal(err)
	}
	var anchor struct {
		Version uint32 `json:"version"`
		Anchor  string `json:"anchor"`
		Shared  bool   `json:"shared"`
	}
	if err := json.Unmarshal(data, &anchor); err != nil {
		t.Fatalf("anchor is not complete JSON: %v\n%s", err, data)
	}
	if anchor.Version != 2 || anchor.Anchor != expectedAnchor || anchor.Shared {
		t.Fatalf("anchor=%#v", anchor)
	}
	info, err := os.Stat(anchorPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("anchor mode=%#o, want 0600", info.Mode().Perm())
	}
	assertNoProjectAnchorTempFiles(t, dir)
}

func assertNoProjectAnchorTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".engram-project-v2.json.tmp-") {
			t.Fatalf("temporary anchor residue: %s", entry.Name())
		}
	}
}

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

func TestResolveProjectIdentityV2_FailsClosedWhenGitCannotExecute(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("PATH", t.TempDir())

	_, err := proxy.ResolveProjectIdentityV2(workspace)
	if err == nil || !strings.Contains(err.Error(), "resolve git identity") {
		t.Fatalf("error=%v, want fail-closed git identity error", err)
	}
	if _, statErr := os.Stat(filepath.Join(workspace, ".engram-project-v2.json")); !os.IsNotExist(statErr) {
		t.Fatalf("non-git anchor was created after git execution failure: %v", statErr)
	}
}

func TestResolveProjectIdentityV2_GitRepositoryWithoutOriginUsesAnchor(t *testing.T) {
	workspace := t.TempDir()
	if output, err := exec.Command("git", "init", workspace).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}

	identity, err := proxy.ResolveProjectIdentityV2(workspace)
	if err != nil {
		t.Fatalf("resolve repository without origin: %v", err)
	}
	if identity.GitRemote != "" || identity.NonGitAnchor == "" {
		t.Fatalf("identity=%+v, want anchor fallback without git remote", identity)
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
	if err := os.WriteFile(filepath.Join(dir, ".engram-project"), data, 0o644); err != nil {
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
	if err := os.WriteFile(filepath.Join(dir, ".engram-project"), data, 0o644); err != nil {
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
