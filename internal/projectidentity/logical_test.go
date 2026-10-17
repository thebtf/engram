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

func TestLogicalProjectNoImplicitNonGitCreation(t *testing.T) {
	root := t.TempDir()
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

func gitLogical(t *testing.T, root string, args ...string) {
	t.Helper()
	command := anchorGitCommandV3(t.Context(), root, args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git: %v: %s", err, output)
	}
}
