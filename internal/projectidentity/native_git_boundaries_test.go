package projectidentity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeGitContextLinkedBareAndMalformed(t *testing.T) {
	root := t.TempDir()
	gitLogical(t, root, "init", "--quiet", "--template=")
	marker := []byte(`{"version":3,"project_id":"10000000-0000-4000-8000-000000000001","name":"fixture","scope":"repository"}`)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".engram-project"), marker, 0o600))
	gitLogical(t, root, "add", ".engram-project")
	gitLogical(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--quiet", "-m", "fixture")
	linked := filepath.Join(t.TempDir(), "linked")
	gitLogical(t, root, "worktree", "add", "--quiet", "--detach", linked, "HEAD")
	ctxA, locatorA, err := NativeGitContext(t.Context(), root)
	require.NoError(t, err)
	ctxB, locatorB, err := NativeGitContext(t.Context(), linked)
	require.NoError(t, err)
	require.NotEqual(t, locatorA, locatorB)
	first, err := CheckLogicalProject(ctxA, root, "", "")
	require.NoError(t, err)
	second, err := CheckLogicalProject(ctxB, linked, "", "")
	require.NoError(t, err)
	require.Equal(t, first.Binding.ID, second.Binding.ID)
	bare := t.TempDir()
	gitLogical(t, bare, "init", "--bare", "--quiet", "--template=")
	_, _, err = NativeGitContext(t.Context(), bare)
	require.Error(t, err)
	_, _, err = NativeGitContext(t.Context(), "relative")
	require.Error(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(linked, ".engram-project"), []byte(`{"version":3,"project_id":"malformed"}`), 0o600))
	_, err = CheckLogicalProject(ctxB, linked, "", "")
	require.Equal(t, "PROJECT_IDENTITY_INVALID", ProjectErrorCode(err))
	_, err = os.Stat(filepath.Join(root, ".git", "engram-project.json"))
	require.True(t, os.IsNotExist(err))
}
