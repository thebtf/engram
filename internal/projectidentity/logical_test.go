package projectidentity

import (
	"encoding/json"
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

func gitLogical(t *testing.T, root string, args ...string) {
	t.Helper()
	command := anchorGitCommandV3(t.Context(), root, args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git: %v: %s", err, output)
	}
}
