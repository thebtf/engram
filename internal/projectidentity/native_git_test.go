package projectidentity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeGitContextReusesOnlyVerifiedRequestDirectories(t *testing.T) {
	root := t.TempDir()
	gitLogical(t, root, "init", "--quiet", "--template=")
	marker := []byte(`{"version":3,"project_id":"10000000-0000-4000-8000-000000000001","name":"fixture","scope":"repository"}`)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".engram-project"), marker, 0o600))
	selected := filepath.Join(root, "nested")
	require.NoError(t, os.Mkdir(selected, 0o700))
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "foreign.git"))
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	ctx, locator, err := NativeGitContext(t.Context(), selected)
	require.NoError(t, err)
	require.Contains(t, locator, "file:///")
	// Removing Git proves the check consumes the sealed facts, not another subprocess.
	t.Setenv("PATH", "")
	inspection, err := CheckLogicalProject(ctx, selected, "", "")
	require.NoError(t, err)
	require.Equal(t, "resolved", inspection.Status)
	require.Equal(t, "10000000-0000-4000-8000-000000000001", inspection.Binding.ID)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = CheckLogicalProject(cancelled, selected, "", "")
	require.ErrorIs(t, err, context.Canceled)
	other := t.TempDir()
	_, _, borrowed := nativeGitDirectoriesFor(ctx, other)
	require.False(t, borrowed, "native facts must not be reused for another directory")
	actual, err := os.ReadFile(filepath.Join(root, ".engram-project"))
	require.NoError(t, err)
	require.Equal(t, marker, actual)
	_, err = os.Stat(filepath.Join(root, ".git", "engram-project.json"))
	require.True(t, os.IsNotExist(err))
}

func TestNativeGitInspectionRequiresExistingLocalIdentity(t *testing.T) {
	root := t.TempDir()
	gitLogical(t, root, "init", "--quiet", "--template=")
	gitLogical(t, root, "remote", "add", "origin", "https://example.test/Native/Existing.git")
	ctx, _, err := NativeGitContext(t.Context(), root)
	require.NoError(t, err)
	before, err := os.ReadFile(filepath.Join(root, ".git", "config"))
	require.NoError(t, err)
	var reads atomic.Int32
	project := "10000000-0000-4000-8000-000000000002"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ProjectInspection{Status: "resolved", Binding: &ProjectBinding{ID: project, Match: "git_remote"}})
	}))
	defer backend.Close()
	previousClient := http.DefaultClient
	http.DefaultClient = backend.Client()
	t.Cleanup(func() { http.DefaultClient = previousClient })
	// The explicit recovery control proves this fixture is reachable without a proxy.
	inspection, err := CheckLogicalProject(t.Context(), root, backend.URL, "")
	require.NoError(t, err)
	require.Equal(t, project, inspection.Binding.ID)
	require.EqualValues(t, 1, reads.Load())
	reads.Store(0)
	inspection, err = CheckLogicalProject(ctx, root, backend.URL, "")
	t.Logf("native_status=%s remote_reads=%d", inspection.Status, reads.Load())
	require.Error(t, err, "native prompt inspection must not resolve through remote recovery")
	require.Equal(t, "PROJECT_CONTEXT_REQUIRED", ProjectErrorCode(err))
	require.Equal(t, ProjectInspection{}, inspection)
	require.Zero(t, reads.Load(), "prompt inspection must not recover identity through another Git/remote lookup")
	after, err := os.ReadFile(filepath.Join(root, ".git", "config"))
	require.NoError(t, err)
	require.Equal(t, before, after)
	for _, path := range []string{filepath.Join(root, ".engram-project"), filepath.Join(root, ".git", "engram-project.json")} {
		_, err := os.Stat(path)
		require.True(t, os.IsNotExist(err), "read-only inspection must not publish identity")
	}
}
