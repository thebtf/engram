package projectidentity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLegacyMarkerInspectionCannotGrantCanonicalAuthority(t *testing.T) {
	valid := ProjectInspection{Status: "migration_required", Binding: &ProjectBinding{ID: "941b49", Match: "legacy_marker"}}
	require.NoError(t, valid.Validate())
	canonical := "11111111-1111-4111-8111-111111111111"
	for _, test := range []struct {
		name    string
		status  string
		binding ProjectBinding
	}{
		{"resolved", "resolved", *valid.Binding},
		{"canonical-id", "migration_required", ProjectBinding{ID: canonical, Match: "legacy_marker"}},
		{"uppercase-id", "migration_required", ProjectBinding{ID: "941B49", Match: "legacy_marker"}},
		{"canonical-key", "migration_required", ProjectBinding{ID: "941b49", Match: "legacy_marker", ProjectKey: &canonical}},
		{"claimed-status", "migration_required", ProjectBinding{ID: "941b49", Match: "legacy_marker", IdentityStatus: &canonical}},
		{"removed", "migration_required", ProjectBinding{ID: "941b49", Match: "legacy_marker", Removed: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, "PROJECT_IDENTITY_INVALID", ProjectErrorCode((ProjectInspection{Status: test.status, Binding: &test.binding}).Validate()))
		})
	}
	root := t.TempDir()
	gitLogical(t, root, "init", "--quiet", "--template=")
	gitLogical(t, root, "remote", "add", "origin", "https://example.test/Recovery/Remote.git")
	var reads atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/projects/resolve", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(valid))
	}))
	defer backend.Close()
	inspection, err := CheckLogicalProject(t.Context(), root, backend.URL, "")
	require.Equal(t, "PROJECT_IDENTITY_INVALID", ProjectErrorCode(err), "a remote response cannot impersonate local marker evidence")
	require.Equal(t, ProjectInspection{}, inspection)
	require.EqualValues(t, 1, reads.Load())
	for _, path := range []string{filepath.Join(root, ".engram-project"), filepath.Join(root, ".git", "engram-project.json")} {
		_, err := os.Lstat(path)
		require.True(t, os.IsNotExist(err))
	}
}

func TestLegacyMarkerInitAndStrictShapesPreserveOriginalBytes(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"id":"941b49","name":"historical $(untrusted) label"}`),
		[]byte(`{"id":"941b49","name":null}`),
		[]byte(`{"id":"941b49","name":17}`),
		[]byte(`{"id":"941b49","name":"` + strings.Repeat("n", 257) + `"}`),
		append([]byte(`{"id":"941b49","name":"`), 0xff, '"', '}'),
	} {
		root := t.TempDir()
		gitLogical(t, root, "init", "--quiet", "--template=")
		path := filepath.Join(root, ".engram-project")
		require.NoError(t, os.WriteFile(path, raw, 0o600))
		project, err := InitLogicalProject(t.Context(), root, "replacement forbidden")
		want := "PROJECT_IDENTITY_INVALID"
		if strings.Contains(string(raw), "historical $(untrusted) label") {
			want = "PROJECT_MIGRATION_REQUIRED"
		}
		require.Equal(t, want, ProjectErrorCode(err))
		require.Equal(t, Project{}, project)
		require.NotContains(t, err.Error(), "$(untrusted)")
		preserved, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		require.Equal(t, raw, preserved)
		_, statErr := os.Lstat(filepath.Join(root, ".git", "engram-project.json"))
		require.True(t, os.IsNotExist(statErr), "explicit init may not convert a historical/invalid input to a new project")
	}
}

func TestLegacyMarkerUnknownErrorGuidancePreservesHold(t *testing.T) {
	for _, code := range []string{"PROJECT_IDENTITY_INVALID", "PROJECT_FUTURE_REFUSAL"} {
		guidance := RecoveryGuidance(code)
		require.Contains(t, guidance, "Preserve")
		require.Contains(t, guidance, "4096")
		require.NotContains(t, guidance, "--check")
		require.Contains(t, guidance, "owner")
	}
	require.Empty(t, RecoveryGuidance("PERMISSION_DENIED"), "no project recovery advice or metadata on a permission refusal")
}
