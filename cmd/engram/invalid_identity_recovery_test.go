package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInvalidIdentityLegacyMarkerRecovery(t *testing.T) {
	for _, scenario := range []string{"no-remotes", "origin-present", "canonical-cache"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			gitProjectInit(t, root, "init", "--quiet", "--template=")
			if scenario == "origin-present" {
				gitProjectInit(t, root, "remote", "add", "origin", "https://example.test/Wrong/Replacement.git")
			}
			marker := []byte("{\"id\":\"941b49\",\"name\":\"ai-warden\"}\n")
			protected := map[string][]byte{
				".engram-project":         marker,
				".engram-project-v2.json": []byte("{\"version\":2,\"anchor\":\"00112233445566778899aabbccddeeff\",\"shared\":false}\n"),
				"retained-data.json":      []byte("{\"legacy_project\":\"941b49\",\"content\":\"must remain recoverable\"}\n"),
			}
			if scenario == "canonical-cache" {
				protected[".git/engram-project.json"] = []byte("{\"project_id\":\"11111111-1111-4111-8111-111111111111\",\"name\":\"Not marker authority\"}\n")
			}
			for path, raw := range protected {
				require.NoError(t, os.WriteFile(filepath.Join(root, path), raw, 0o600))
			}
			var calls atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				http.Error(w, "local historical evidence must not trigger HTTP or bootstrap", http.StatusInternalServerError)
			}))
			defer backend.Close()
			t.Setenv("ENGRAM_URL", backend.URL)
			t.Setenv("ENGRAM_SERVER_URL", "")
			t.Setenv("ENGRAM_TOKEN", "")
			for _, check := range []bool{false, true, false} {
				args := []string{"resolve", "--root", root, "--json"}
				if check {
					args = append(args, "--check")
				}
				var output bytes.Buffer
				err := runProjectCommand(args, &output)
				t.Logf("public CLI handler: args=%q err=%v stdout=%s", args, err, output.String())
				var payload map[string]any
				require.NoError(t, json.Unmarshal(output.Bytes(), &payload))
				if check {
					assert.NoError(t, err, "supported check must expose local migration evidence without requiring a remote")
					assert.Equal(t, map[string]any{"status": "migration_required", "binding": map[string]any{"id": "941b49", "project_key": nil, "identity_status": nil, "match": "legacy_marker"}}, payload)
				} else {
					assert.Error(t, err)
					assert.Equal(t, "PROJECT_MIGRATION_REQUIRED", payload["code"], "legacy id is migration input, never a replacement canonical identity")
					guidance, _ := payload["guidance"].(string)
					assert.Contains(t, guidance, "GET /api/projects/resolve?legacy_id=", "the existing exact legacy reader is the supported no-remote next action")
					assert.Contains(t, strings.ToLower(guidance), "preserve")
				}
				assert.Zero(t, calls.Load(), "neither check nor ordinary held resolution may query/bootstrap a replacement")
				for path, expected := range protected {
					actual, readErr := os.ReadFile(filepath.Join(root, path))
					require.NoError(t, readErr)
					assert.Equal(t, expected, actual, "protected historical bytes: %s", path)
				}
				if scenario != "canonical-cache" {
					_, statErr := os.Lstat(filepath.Join(root, ".git", "engram-project.json"))
					assert.True(t, os.IsNotExist(statErr), "inspection/hold must not pin a cache")
				}
			}
		})
	}
}

func TestInvalidIdentityMalformedAndCacheRemainProtected(t *testing.T) {
	cases := map[string]string{
		"uppercase-id":   `{"id":"941B49","name":"untrusted"}`,
		"short-id":       `{"id":"941b4","name":"untrusted"}`,
		"missing-name":   `{"id":"941b49"}`,
		"control-name":   `{"id":"941b49","name":"unsafe\nname"}`,
		"unknown-field":  `{"id":"941b49","name":"untrusted","remote":"https://wrong.test/repo"}`,
		"duplicate-id":   `{"id":"941b49","id":"ffffff","name":"untrusted"}`,
		"mixed-identity": `{"id":"941b49","name":"untrusted","project_id":"11111111-1111-4111-8111-111111111111"}`,
		"oversized":      `{"id":"941b49","name":"` + strings.Repeat("a", 4096) + `"}`,
		"legacy-cache":   `{"id":"941b49","name":"cache is not a legacy marker"}`,
	}
	for name, marker := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			gitProjectInit(t, root, "init", "--quiet", "--template=")
			gitProjectInit(t, root, "remote", "add", "origin", "https://example.test/Wrong/Replacement.git")
			path := filepath.Join(root, ".engram-project")
			if name == "legacy-cache" {
				path = filepath.Join(root, ".git", "engram-project.json")
			}
			raw := []byte(marker)
			require.NoError(t, os.WriteFile(path, raw, 0o600))
			var calls atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				http.Error(w, "invalid identity must stop before HTTP", http.StatusInternalServerError)
			}))
			defer backend.Close()
			t.Setenv("ENGRAM_URL", backend.URL)
			for _, check := range []bool{false, true} {
				args := []string{"resolve", "--root", root, "--json"}
				if check {
					args = append(args, "--check")
				}
				var output bytes.Buffer
				assert.Error(t, runProjectCommand(args, &output))
				var payload map[string]any
				require.NoError(t, json.Unmarshal(output.Bytes(), &payload))
				assert.Equal(t, "PROJECT_IDENTITY_INVALID", payload["code"])
				guidance, _ := payload["guidance"].(string)
				assert.Contains(t, strings.ToLower(guidance), "preserve", "INVALID needs a bounded inspection/protection action, not an empty hint")
				assert.Contains(t, strings.ToLower(guidance), "marker")
				assert.NotContains(t, guidance, "--check", "already-invalid inspection must not prescribe the same failing check again")
				assert.NotContains(t, guidance, "unsafe\nname", "metadata is never an executable instruction")
				actual, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, raw, actual)
				assert.Zero(t, calls.Load())
			}
		})
	}
}

func TestInvalidIdentityNestedAndSymlinkBoundaries(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "nested-legacy", true: "symlink"}[symlink], func(t *testing.T) {
			root := t.TempDir()
			gitProjectInit(t, root, "init", "--quiet", "--template=")
			gitProjectInit(t, root, "remote", "add", "origin", "https://example.test/Wrong/Outer.git")
			nested := filepath.Join(root, "explicit-boundary")
			require.NoError(t, os.Mkdir(nested, 0o700))
			raw := []byte(`{"id":"941b49","name":"historical boundary"}`)
			path := filepath.Join(nested, ".engram-project")
			target := path
			if symlink {
				target = filepath.Join(root, "retained-target.json")
				require.NoError(t, os.WriteFile(target, raw, 0o600))
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlink fixture capability unavailable: %v", err)
				}
			} else {
				require.NoError(t, os.WriteFile(path, raw, 0o600))
			}
			var calls atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				http.Error(w, "nested boundary must not borrow outer repository", http.StatusInternalServerError)
			}))
			defer backend.Close()
			t.Setenv("ENGRAM_URL", backend.URL)
			for _, check := range []bool{false, true} {
				args := []string{"resolve", "--root", nested, "--json"}
				if check {
					args = append(args, "--check")
				}
				var output bytes.Buffer
				err := runProjectCommand(args, &output)
				var payload map[string]any
				require.NoError(t, json.Unmarshal(output.Bytes(), &payload))
				if symlink {
					assert.Error(t, err)
					assert.Equal(t, "PROJECT_IDENTITY_INVALID", payload["code"])
				} else if !check {
					assert.Error(t, err)
					assert.Equal(t, "PROJECT_MIGRATION_REQUIRED", payload["code"])
				}
				assert.NotEqual(t, "resolved", payload["status"])
				assert.NotContains(t, payload, "project_id")
				assert.Zero(t, calls.Load())
				actual, readErr := os.ReadFile(target)
				require.NoError(t, readErr)
				assert.Equal(t, raw, actual)
				_, statErr := os.Lstat(filepath.Join(root, ".git", "engram-project.json"))
				assert.True(t, os.IsNotExist(statErr))
			}
		})
	}
}

func TestInvalidIdentityPublicCLI(t *testing.T) {
	binary := os.Getenv("ENGRAM_RECOVERY_CLI")
	if binary == "" {
		t.Skip("set ENGRAM_RECOVERY_CLI to the candidate-built native CLI for public-entry proof")
	}
	root := t.TempDir()
	gitProjectInit(t, root, "init", "--quiet", "--template=")
	raw := []byte("{\"id\":\"941b49\",\"name\":\"ai-warden\"}\n")
	marker := filepath.Join(root, ".engram-project")
	require.NoError(t, os.WriteFile(marker, raw, 0o600))
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "public CLI must not initialize this historical project", http.StatusInternalServerError)
	}))
	defer backend.Close()
	t.Setenv("ENGRAM_URL", backend.URL)
	t.Setenv("ENGRAM_SERVER_URL", "")
	t.Setenv("ENGRAM_TOKEN", "")
	for _, check := range []bool{false, true} {
		args := []string{"project", "resolve", "--root", root, "--json"}
		if check {
			args = append(args, "--check")
		}
		command := exec.CommandContext(t.Context(), binary, args...)
		command.Dir = root
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		require.NotNil(t, command.ProcessState, "native process did not start: %v; stderr=%s", err, stderr.String())
		t.Logf("native public entry: exe=%q args=%q exit=%d err=%v stdout=%s stderr=%s", binary, args, command.ProcessState.ExitCode(), err, stdout.String(), stderr.String())
		var payload map[string]any
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &payload))
		if check {
			assert.NoError(t, err)
			assert.Equal(t, "migration_required", payload["status"])
			assert.Empty(t, stderr.String())
		} else {
			assert.Error(t, err)
			assert.Equal(t, 1, command.ProcessState.ExitCode())
			assert.Equal(t, "PROJECT_MIGRATION_REQUIRED", payload["code"])
			assert.Contains(t, stderr.String(), "PROJECT_MIGRATION_REQUIRED")
		}
		actual, readErr := os.ReadFile(marker)
		require.NoError(t, readErr)
		assert.Equal(t, raw, actual)
		_, statErr := os.Lstat(filepath.Join(root, ".git", "engram-project.json"))
		assert.True(t, os.IsNotExist(statErr))
		assert.Zero(t, calls.Load())
	}
}
