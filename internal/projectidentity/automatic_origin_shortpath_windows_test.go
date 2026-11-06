package projectidentity

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestAutomaticOriginResolutionWindowsShortPath(t *testing.T) {
	root := t.TempDir()
	short := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(windows.StringToUTF16Ptr(root), &short[0], uint32(len(short)))
	if err != nil || n == 0 || int(n) >= len(short) {
		t.Fatalf("get short path: n=%d err=%v", n, err)
	}
	shortRoot := windows.UTF16ToString(short[:n])
	if strings.EqualFold(shortRoot, root) {
		t.Skip("volume has no alternate short path for this fixture")
	}
	gitLogical(t, root, "init", "--quiet", "--template=")
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	gitLogical(t, root, "remote", "add", "origin", "https://example.test/Fixture/Repo.git")
	original := []byte(`{"name":"Existing"}`)
	if err := os.WriteFile(filepath.Join(root, ".engram-project"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	const id = "11111111-1111-4111-8111-111111111111"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/projects/resolve" || r.URL.Query().Get("repository_locator") != "example.test/Fixture/Repo" || r.Header.Get("Authorization") != "Bearer fixture-keycard" {
			t.Errorf("unexpected lookup: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprintf(w, `{"status":"resolved","binding":{"id":"%s","project_key":"%s","identity_status":"active","match":"manual_alias"}}`, id, id)
	}))
	defer server.Close()
	for _, caller := range []string{shortRoot, filepath.Join(shortRoot, "nested")} {
		state, err := InspectProjectMarker(t.Context(), caller)
		if err != nil || state.RootDigest != markerEvidenceDigest([]byte(caller)) || state.TargetRootDigest != markerEvidenceDigest([]byte(shortRoot)) {
			t.Fatalf("raw path fingerprints changed: state=%+v err=%v", state, err)
		}
		project, err := ResolveLogicalProjectForBackend(t.Context(), caller, server.URL, "fixture-keycard")
		if err != nil || project.ProjectID != id {
			t.Fatalf("short-path recovery: project=%+v err=%v", project, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(root, ".engram-project")); err != nil || string(got) != string(original) {
		t.Fatal("marker changed")
	}
}

func TestInspectProjectMarkerRejectsOutsideAncestorThroughSymlink(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repository")
	outside := filepath.Join(root, "outside")
	child := filepath.Join(repo, "sub", "child")
	for _, dir := range []string{child, outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	gitLogical(t, repo, "init", "--quiet", "--template=")
	link := filepath.Join(outside, "link")
	if err := os.Symlink(filepath.Join(repo, "sub"), link); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, ".engram-project"), []byte(`{"project_id":"11111111-1111-4111-8111-111111111111","name":"outside"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	caller := filepath.Join(link, "child")
	gitRoot, common, rootErr := logicalProjectDirectories(t.Context(), caller)
	if rootErr != nil || common == "" || !samePath(gitRoot, repo) {
		t.Fatalf("symlink caller did not resolve fixture repository: root=%q err=%v", gitRoot, rootErr)
	}
	state, err := InspectProjectMarker(t.Context(), caller)
	if ProjectErrorCode(err) != "PROJECT_CONTEXT_REQUIRED" {
		t.Fatalf("outside ancestor must not select a marker: state=%+v err=%v", state, err)
	}
}
