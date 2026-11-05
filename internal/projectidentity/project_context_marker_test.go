package projectidentity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInspectProjectMarkerBoundaries(t *testing.T) {
	root := t.TempDir()
	gitLogical(t, root, "init", "--quiet", "--template=")
	marker := filepath.Join(root, anchorFilenameV3)
	legacy := []byte(`{"id":"abcdef","name":"synthetic legacy"}`)
	if err := os.WriteFile(marker, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := InspectProjectMarker(t.Context(), root)
	if err != nil || !state.Present || state.LegacyID != "abcdef" || state.MarkerDigest != markerEvidenceDigest(legacy) {
		t.Fatalf("legacy inspection: state=%+v err=%v", state, err)
	}
	child := filepath.Join(root, "nested")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	nearest, err := InspectProjectMarker(t.Context(), child)
	if err != nil || nearest.LegacyID != "abcdef" || nearest.Target != "../.engram-project" || nearest.TargetRootDigest != markerEvidenceDigest([]byte(root)) {
		t.Fatalf("nearest in-worktree marker: %+v %v", nearest, err)
	}
	childMarker := filepath.Join(child, anchorFilenameV3)
	childRaw := []byte(`{"id":"fedcba","name":"nested legacy"}`)
	if err := os.WriteFile(childMarker, childRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	nested, err := InspectProjectMarker(t.Context(), child)
	if err != nil || nested.LegacyID != "fedcba" {
		t.Fatalf("nested explicit boundary: %+v %v", nested, err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != string(legacy) {
		t.Fatal("inspection changed outer marker")
	}
	if got, err := os.ReadFile(childMarker); err != nil || string(got) != string(childRaw) {
		t.Fatal("inspection changed nested marker")
	}
}

func TestInspectProjectMarkerGitRootIgnoresUnrelatedParent(t *testing.T) {
	parent := t.TempDir()
	parentMarker := []byte(`{"id":"fedcba","name":"unrelated parent"}`)
	if err := os.WriteFile(filepath.Join(parent, anchorFilenameV3), parentMarker, 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "repo")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	gitLogical(t, root, "init", "--quiet", "--template=")
	state, err := InspectProjectMarker(t.Context(), root)
	if err != nil || state.Present || state.Target != ".engram-project" {
		t.Fatalf("unrelated parent crossed Git boundary: %+v %v", state, err)
	}
	if got, err := os.ReadFile(filepath.Join(parent, anchorFilenameV3)); err != nil || string(got) != string(parentMarker) {
		t.Fatal("unrelated parent marker changed")
	}
}

func TestInspectProjectMarkerLinkedWorktreeWithoutCommonCache(t *testing.T) {
	root := t.TempDir()
	gitLogical(t, root, "init", "--quiet", "--template=")
	gitLogical(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "--quiet", "-m", "fixture")
	linked := filepath.Join(t.TempDir(), "linked")
	gitLogical(t, root, "worktree", "add", "--detach", linked, "HEAD")
	child := filepath.Join(linked, "nested")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := []byte(`{"id":"abcdef","name":"worktree fixture"}`)
	if err := os.WriteFile(filepath.Join(linked, anchorFilenameV3), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := InspectProjectMarker(t.Context(), child)
	if err != nil || state.LegacyID != "abcdef" || state.Target != "../.engram-project" || state.TargetRootDigest != markerEvidenceDigest([]byte(linked)) {
		t.Fatalf("linked worktree target: %+v %v", state, err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".git", "engram-project.json")); !os.IsNotExist(err) {
		t.Fatalf("inspection created common cache: %v", err)
	}
}

func TestInspectProjectMarkerRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "retained-marker")
	if err := os.WriteFile(target, []byte(`{"id":"abcdef","name":"synthetic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, anchorFilenameV3)); err != nil {
		t.Skipf("symlink capability unavailable: %v", err)
	}
	if _, err := InspectProjectMarker(t.Context(), root); ProjectErrorCode(err) != "PROJECT_IDENTITY_INVALID" {
		t.Fatalf("symlink marker was not rejected: %v", err)
	}
}
