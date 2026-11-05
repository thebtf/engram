package projectidentity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLogicalProjectVectors(t *testing.T) {
	raw, err := os.ReadFile("../../specs/001-engram-architectural-recovery/contracts/logical-project-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Locators        []struct{ Input, Expected string }
		InvalidLocators []string `json:"invalid_locators"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, vector := range vectors.Locators {
		got, err := NormalizeRepositoryLocator(vector.Input)
		if err != nil || got != vector.Expected {
			t.Fatalf("locator: got %q want %q err %v", got, vector.Expected, err)
		}
		if err := ValidateRepositoryLocator(got); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range vectors.InvalidLocators {
		if _, err := NormalizeRepositoryLocator(raw); err == nil {
			t.Fatal("invalid locator accepted")
		}
	}
}

func TestLogicalProjectCommonDirAndConflict(t *testing.T) {
	root := t.TempDir()
	gitLogical(t, root, "init", "--quiet")
	gitLogical(t, root, "-c", "user.name=Identity", "-c", "user.email=identity@example.test", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "--quiet", "-m", "before marker")
	project, err := InitLogicalProject(t.Context(), root, "Example")
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "src", "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveLogicalProject(t.Context(), nested, nil)
	if err != nil || got.ProjectID != project.ProjectID {
		t.Fatalf("nested: %+v %v", got, err)
	}
	// An external old/detached worktree has no marker and must share the real common-dir cache.
	linked := filepath.Join(t.TempDir(), "external")
	gitLogical(t, root, "worktree", "add", "--detach", linked, "HEAD")
	got, err = ResolveLogicalProject(t.Context(), linked, nil)
	if err != nil || got.ProjectID != project.ProjectID {
		t.Fatalf("external: %+v %v", got, err)
	}
	gitLogical(t, root, "remote", "add", "origin", "https://example.test/Renamed/Repo.git")
	got, err = ResolveLogicalProject(t.Context(), root, nil)
	if err != nil || got.ProjectID != project.ProjectID {
		t.Fatal("remote rename changed pinned project")
	}
	other := Project{ProjectID: "22222222-2222-4222-8222-222222222222"}
	raw, _ := json.Marshal(other)
	if err := os.WriteFile(filepath.Join(linked, ".engram-project"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveLogicalProject(t.Context(), linked, nil); ProjectErrorCode(err) != "PROJECT_IDENTITY_CONFLICT" {
		t.Fatalf("conflict: %v", err)
	}
}

func TestLogicalProjectPortableMarkerDoesNotRequireCacheWrite(t *testing.T) {
	root := t.TempDir()
	gitLogical(t, root, "init", "--quiet", "--template=")
	project := Project{ProjectID: "11111111-1111-4111-8111-111111111111", Name: "Existing"}
	raw, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, anchorFilenameV3), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(root, ".git", "engram-project.json")
	if err := os.WriteFile(cache+".lock", []byte("other writer"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveLogicalProject(t.Context(), root, nil)
	if err != nil || got != project {
		probe := anchorGitCommandV3(t.Context(), root, "rev-parse", "--path-format=absolute", "--git-common-dir", "--is-bare-repository")
		output, probeErr := probe.CombinedOutput()
		t.Logf("git directory probe: err=%v output=%q", probeErr, output)
		probe = anchorGitCommandV3(t.Context(), root, "rev-parse", "--show-toplevel")
		output, probeErr = probe.CombinedOutput()
		t.Logf("git root probe: err=%v output=%q", probeErr, output)
		t.Fatalf("portable marker without derived cache: project=%+v err=%v", got, err)
	}
	if _, err := os.Lstat(cache); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only marker resolution unexpectedly wrote cache: %v", err)
	}
}

func TestLogicalProjectPortableMarkerAcrossIndependentCheckouts(t *testing.T) {
	project := Project{ProjectID: "11111111-1111-4111-8111-111111111111", Name: "Synthetic"}
	raw, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		root := t.TempDir()
		gitLogical(t, root, "init", "--quiet", "--template=")
		if err := os.WriteFile(filepath.Join(root, anchorFilenameV3), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ResolveLogicalProject(t.Context(), root, nil)
		if err != nil || got != project {
			t.Fatalf("independent portable checkout: %+v %v", got, err)
		}
		if _, err := os.Lstat(filepath.Join(root, ".git", "engram-project.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected common cache: %v", err)
		}
	}
}

func TestLogicalProjectNoImplicitNonGitCreation(t *testing.T) {
	root := nonGitLogicalTempDir(t)
	if _, err := ResolveLogicalProject(t.Context(), root, nil); ProjectErrorCode(err) != "PROJECT_CONTEXT_REQUIRED" {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".engram-project")); !os.IsNotExist(err) {
		t.Fatal("browsing created marker")
	}
	project, err := InitLogicalProject(t.Context(), root, "Selected")
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "subdir")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveLogicalProject(t.Context(), child, nil)
	if err != nil || got != project {
		t.Fatalf("non-Git: %+v %v", got, err)
	}
}

func TestResolveLogicalProjectRejectsDuplicateProjectID(t *testing.T) {
	root := t.TempDir()
	gitLogical(t, root, "init", "--quiet")
	gitLogical(t, root, "remote", "add", "origin", "https://example.test/Identity/Duplicate.git")
	markerPath := filepath.Join(root, anchorFilenameV3)
	cachePath := filepath.Join(root, ".git", "engram-project.json")
	if _, err := os.Lstat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected cold project cache: %v", err)
	}
	raw := []byte(`{"project_id":"11111111-1111-4111-8111-111111111111","project_id":"22222222-2222-4222-8222-222222222222","name":"Duplicate"}`)
	if err := os.WriteFile(markerPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	bootstrapCalls := 0
	got, err := ResolveLogicalProject(t.Context(), root, func(context.Context, BootstrapRequest) (Project, error) {
		bootstrapCalls++
		return Project{ProjectID: "11111111-1111-4111-8111-111111111111", Name: "Bootstrap"}, nil
	})
	var identityErr *LogicalError
	if !errors.As(err, &identityErr) || identityErr == nil || identityErr.Code != "PROJECT_IDENTITY_INVALID" {
		t.Errorf("duplicate project_id must return typed PROJECT_IDENTITY_INVALID: project=%+v error=%T %v", got, err, err)
	}
	if got != (Project{}) {
		t.Errorf("duplicate project_id returned an authoritative project: %+v", got)
	}
	if bootstrapCalls != 0 {
		t.Errorf("duplicate project_id triggered %d bootstrap/backend calls", bootstrapCalls)
	}
	if _, err := os.Lstat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("duplicate project_id published project cache authority: %v", err)
	}
	after, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, raw) {
		t.Errorf("duplicate project_id marker was rewritten: %s", after)
	}
}

func nonGitLogicalTempDir(t *testing.T) string {
	t.Helper()
	base := os.Getenv("ENGRAM_TEST_LOGICAL_NONGIT_TMPDIR")
	if base == "" {
		return t.TempDir()
	}
	root, err := os.MkdirTemp(base, "logical-project-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	return root
}

func gitLogical(t *testing.T, root string, args ...string) {
	t.Helper()
	command := anchorGitCommandV3(t.Context(), root, args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git: %v: %s", err, output)
	}
}

func TestCodeContextProjectRequirementActionAware(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tool      string
		arguments string
		want      bool
	}{
		{"register", "codebase_context", `{"action":"register","source_label":"native-fixture"}`, true},
		{"list", "codebase_context", `{"action":"list"}`, false},
		{"resolve", "codebase_context", `{"action":"resolve"}`, false},
		{"select", "codebase_context", `{"action":"select","context_handle":"opaque-selection"}`, false},
		{"index", "codebase_index", `{}`, false},
		{"status", "codebase_status", `{}`, false},
		{"search", "codebase_search", `{"query":"fixture"}`, false},
		{"read", "codebase_read", `{}`, false},
		{"graph", "codebase_graph", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolRequiresProject(tc.tool, []byte(tc.arguments)); got != tc.want {
				t.Errorf("ToolRequiresProject(%q, %s) = %t, want %t", tc.tool, tc.arguments, got, tc.want)
			}
		})
	}
}
