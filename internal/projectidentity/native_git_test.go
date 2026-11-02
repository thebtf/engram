package projectidentity

import (
	"context"
	"os"
	"path/filepath"
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
