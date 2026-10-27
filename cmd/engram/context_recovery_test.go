package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextRecoveryProjectResolveCheckInspectsWithoutBootstrapOrPinning(t *testing.T) {
	for _, diagnostic := range []string{"resolved", "migration_required", "conflict", "unbound"} {
		t.Run(diagnostic, func(t *testing.T) {
			root := t.TempDir()
			gitProjectInit(t, root, "init", "--quiet", "--template=")
			gitProjectInit(t, root, "remote", "add", "origin", "https://example.test/Recovery/Check.git")
			marker := filepath.Join(root, ".engram-project")
			cache := filepath.Join(root, ".git", "engram-project.json")
			markerBytes := []byte("{ \"name\": \"Retained legacy marker\" }\n")
			cacheBytes := []byte("{ \"name\": \"Retained cache label\" }\n")
			require.NoError(t, os.WriteFile(marker, markerBytes, 0o600))
			require.NoError(t, os.WriteFile(cache, cacheBytes, 0o600))
			var reads, writes atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					writes.Add(1)
					http.Error(w, "bootstrap/mutation forbidden in check mode", 500)
					return
				}
				reads.Add(1)
				assert.Equal(t, "/api/projects/resolve", r.URL.Path)
				assert.Equal(t, "example.test/Recovery/Check", r.URL.Query().Get("repository_locator"))
				assert.Empty(t, r.URL.Query().Get("legacy_id"))
				w.Header().Set("Content-Type", "application/json")
				payload := map[string]any{"status": diagnostic}
				if diagnostic == "resolved" {
					payload["binding"] = map[string]any{"id": "11111111-1111-4111-8111-111111111111", "project_key": "11111111-1111-4111-8111-111111111111", "identity_status": "active", "match": "git_remote"}
				} else if diagnostic == "migration_required" {
					payload["binding"] = map[string]any{"id": "legacy-check-project", "project_key": nil, "identity_status": nil, "match": "git_remote"}
				}
				_ = json.NewEncoder(w).Encode(payload)
			}))
			defer backend.Close()
			t.Setenv("ENGRAM_URL", backend.URL)
			t.Setenv("ENGRAM_SERVER_URL", "")
			t.Setenv("ENGRAM_TOKEN", "")
			var output bytes.Buffer
			err := runProjectCommand([]string{"resolve", "--root", root, "--json", "--check"}, &output)
			t.Logf("actual CLI parser/handler: error=%v output=%s", err, output.String())
			var payload map[string]any
			assert.NoError(t, json.Unmarshal(output.Bytes(), &payload), "check must emit a usable diagnostic rather than reject the supported inspection flag")
			assert.Equal(t, diagnostic, payload["status"])
			assert.EqualValues(t, 1, reads.Load(), "check must inspect exactly the repository locator")
			require.Zero(t, writes.Load(), "check must never call bootstrap, Census/Preflight, migration or another mutation")
			for path, expected := range map[string][]byte{marker: markerBytes, cache: cacheBytes} {
				actual, readErr := os.ReadFile(path)
				require.NoError(t, readErr)
				require.Equal(t, expected, actual, "diagnostic must not publish or overwrite identity")
			}
		})
	}
}

func TestContextRecoveryProjectResolveCheckRespectsNestedMarkerBoundary(t *testing.T) {
	root := t.TempDir()
	gitProjectInit(t, root, "init", "--quiet", "--template=")
	gitProjectInit(t, root, "remote", "add", "origin", "https://example.test/Wrong/Outer.git")
	nested := filepath.Join(root, "explicit-boundary")
	require.NoError(t, os.Mkdir(nested, 0o700))
	marker := []byte("{\"project_id\":\"22222222-2222-4222-8222-222222222222\",\"name\":\"Explicit boundary\"}\n")
	require.NoError(t, os.WriteFile(filepath.Join(nested, ".engram-project"), marker, 0o600))
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "outer repository must not be selected", 500)
	}))
	defer backend.Close()
	t.Setenv("ENGRAM_URL", backend.URL)
	var output bytes.Buffer
	err := runProjectCommand([]string{"resolve", "--root", nested, "--json", "--check"}, &output)
	t.Logf("nested marker CLI check: error=%v output=%s", err, output.String())
	var payload map[string]any
	assert.NoError(t, json.Unmarshal(output.Bytes(), &payload))
	assert.Contains(t, output.String(), "22222222-2222-4222-8222-222222222222")
	require.Zero(t, calls.Load(), "explicit marker must not inspect another repository's origin")
	actual, err := os.ReadFile(filepath.Join(nested, ".engram-project"))
	require.NoError(t, err)
	require.Equal(t, marker, actual)
	_, err = os.Stat(filepath.Join(root, ".git", "engram-project.json"))
	require.True(t, os.IsNotExist(err), "even pinned marker inspection must not populate the Git cache")
}

func TestContextRecoveryProjectResolveCheckRejectsMalformedSuccess(t *testing.T) {
	root := t.TempDir()
	gitProjectInit(t, root, "init", "--quiet", "--template=")
	gitProjectInit(t, root, "remote", "add", "origin", "https://example.test/Recovery/Malformed.git")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"resolved"}`))
	}))
	defer backend.Close()
	t.Setenv("ENGRAM_URL", backend.URL)
	var output bytes.Buffer
	err := runProjectCommand([]string{"resolve", "--root", root, "--json", "--check"}, &output)
	require.Error(t, err, "status without the original binding is not a successful inspection")
	assert.Contains(t, output.String(), "PROJECT_IDENTITY_INVALID")
	for _, file := range []string{filepath.Join(root, ".engram-project"), filepath.Join(root, ".git", "engram-project.json")} {
		_, err := os.Lstat(file)
		require.True(t, os.IsNotExist(err))
	}
}
