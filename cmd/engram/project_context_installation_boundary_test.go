package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/projectidentity"
)

func TestProjectContextCLIInstallationBoundary_NativeBindingUsesExistingLauncherIdentity(t *testing.T) {
	const firstID = "11111111-1111-4111-8111-111111111111"
	const secondID = "22222222-2222-4222-8222-222222222222"
	const token = "synthetic-installation-cli-boundary-keycard"
	var cleanupRoots []string
	t.Cleanup(func() {
		for _, root := range cleanupRoots {
			_, err := os.Lstat(root)
			assert.True(t, os.IsNotExist(err), "test-owned fixture root must be removed")
		}
		t.Logf("cleanup: checked removal of %d test-owned temporary roots", len(cleanupRoots))
	})
	home, root := t.TempDir(), t.TempDir()
	cleanupRoots = append(cleanupRoots, home, root)
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(key, home)
	}
	t.Setenv("ENGRAM_TLS_CA", "")
	t.Setenv("CLAUDE_SESSION_ID", "synthetic-session-is-not-an-installation")
	// Produce real fixture installations through the existing native producer,
	// then pass their already persisted IDs exactly as hooks/launcher do. The
	// project command itself must not generate or infer installation identity.
	var installations, ids []string
	var identityFiles [][]byte
	for range 2 {
		installation, err := filepath.EvalSymlinks(t.TempDir())
		require.NoError(t, err)
		cleanupRoots = append(cleanupRoots, installation)
		t.Setenv("ENGRAM_DATA_DIR", installation)
		t.Setenv("ENGRAM_CLIENT_INSTANCE_ID", "")
		id, err := directClientInstanceID()
		require.NoError(t, err)
		require.NotEmpty(t, id)
		stored, err := os.ReadFile(filepath.Join(installation, "client-instance-id"))
		require.NoError(t, err)
		require.True(t, bytes.Equal(stored, []byte(id+"\n")), "installation producer must persist its actual ID")
		installations, ids = append(installations, installation), append(ids, id)
		identityFiles = append(identityFiles, stored)
	}
	require.NotEqual(t, ids[0], ids[1])
	command := exec.CommandContext(t.Context(), "git", "-C", root, "init", "--quiet", "--template=")
	command.Env = projectidentity.RepositoryGitEnvironmentV3()
	output, err := command.CombinedOutput()
	require.NoError(t, err, "initialize isolated caller repository: %s", output)
	marker := []byte("{ \"id\": \"941b49\", \"name\": \"historical ambiguous membership\" }\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".engram-project"), marker, 0o600))
	var mu sync.Mutex
	catalogReads, legacyReads, deniedReads, unexpected, mutations := 0, 0, 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, no-store")
		if request.Method != http.MethodGet {
			mutations++
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+token {
			deniedReads++
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.Header.Get("Cookie") != "" {
			unexpected++
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch {
		case request.URL.Path == "/api/projects" && request.URL.RawQuery == "":
			catalogReads++
			_, _ = w.Write([]byte(`{"items":[{"project_id":"` + firstID + `","name":"Same current name","repository_qualifier":"example.test/coupled/first"},{"project_id":"` + secondID + `","name":"Same current name","repository_qualifier":"example.test/coupled/second"}],"unresolved":[]}`))
		case request.URL.Path == "/api/projects/resolve" && request.URL.RawQuery == "legacy_id=941b49":
			legacyReads++
			_, _ = w.Write([]byte(`{"status":"unresolved","reference":"urn:engram:migration:unresolved:installation-boundary"}`))
		default:
			unexpected++
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("ENGRAM_URL", server.URL)
	t.Setenv("ENGRAM_SERVER_URL", "")
	t.Setenv("ENGRAM_TOKEN", token)
	callerA := projectidentity.ProjectContextCaller{Cwd: root, CallerID: ids[0], ServerURL: server.URL, Token: token}
	callerB := projectidentity.ProjectContextCaller{Cwd: root, CallerID: ids[1], ServerURL: server.URL, Token: token}
	// Use the real selection API with the installation namespace explicitly.
	// The separate engramcore regression owns the module's incorrect projection
	// from muxcore's worktree ID; that defect must not mask this CLI boundary.
	choicesA, err := projectidentity.ProjectContextChoices(t.Context(), callerA)
	require.NoError(t, err)
	require.Len(t, choicesA.Choices, 2)
	selected, err := projectidentity.SelectProjectContext(t.Context(), callerA, choicesA.Choices[0].SelectionRef)
	require.NoError(t, err)
	require.Equal(t, firstID, selected.ProjectID)
	resolved, err := projectidentity.ResolveLogicalProjectWithRecovery(t.Context(), callerA)
	require.NoError(t, err, "the installation-scoped binding must be real and independently readable before invoking CLI")
	require.Equal(t, firstID, resolved.ProjectID)
	cli := func(installation int, args ...string) (bytes.Buffer, error) {
		t.Helper()
		t.Setenv("ENGRAM_DATA_DIR", installations[installation])
		t.Setenv("ENGRAM_CLIENT_INSTANCE_ID", ids[installation])
		var result bytes.Buffer
		err := runProjectCommand(args, &result)
		assert.False(t, strings.Contains(result.String(), token), "CLI must not expose a keycard")
		return result, err
	}
	sameOutput, sameErr := cli(0, "resolve", "--root", root, "--json")
	assert.NoError(t, sameErr, "the hook/launcher's existing installation ID must recover its exact native selection")
	var same projectidentity.Project
	require.NoError(t, json.Unmarshal(sameOutput.Bytes(), &same))
	assert.Equal(t, firstID, same.ProjectID, "same installation must see its native binding")
	otherOutput, otherErr := cli(1, "resolve", "--root", root, "--json")
	assert.Error(t, otherErr, "another installation must not borrow the first installation's binding")
	assert.False(t, strings.Contains(otherOutput.String(), firstID))
	choicesB, err := projectidentity.ProjectContextChoices(t.Context(), callerB)
	require.NoError(t, err)
	require.Len(t, choicesB.Choices, 2)
	cliChoicesOutput, err := cli(1, "choices", "--root", root, "--json")
	require.NoError(t, err)
	var cliChoices projectidentity.ProjectChoices
	require.NoError(t, json.Unmarshal(cliChoicesOutput.Bytes(), &cliChoices))
	require.Len(t, cliChoices.Choices, 2)
	assert.Equal(t, choicesB.Choices[1].SelectionRef, cliChoices.Choices[1].SelectionRef, "CLI choices must use the exact existing installation namespace")
	_, cliSelectErr := cli(1, "select", "--root", root, "--selection-ref", choicesB.Choices[1].SelectionRef, "--json")
	assert.NoError(t, cliSelectErr, "CLI must accept a fresh choice reference bound to this same real installation")
	resolvedB, nativeBErr := projectidentity.ResolveLogicalProjectWithRecovery(t.Context(), callerB)
	assert.NoError(t, nativeBErr, "native recovery must consume the binding selected by the same installation's CLI")
	assert.Equal(t, secondID, resolvedB.ProjectID)
	resolvedA, err := projectidentity.ResolveLogicalProjectWithRecovery(t.Context(), callerA)
	require.NoError(t, err)
	assert.Equal(t, firstID, resolvedA.ProjectID, "the second installation must not overwrite the first selection")
	actualMarker, err := os.ReadFile(filepath.Join(root, ".engram-project"))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(marker, actualMarker), "CLI must preserve the original legacy marker bytes")
	for index, installation := range installations {
		actual, err := os.ReadFile(filepath.Join(installation, "client-instance-id"))
		require.NoError(t, err)
		assert.True(t, bytes.Equal(identityFiles[index], actual), "project recovery must not regenerate installation identity")
		entries, err := os.ReadDir(installation)
		require.NoError(t, err)
		assert.Len(t, entries, 1, "no generated replacement identity, cache or temporary publication files")
	}
	locks, err := filepath.Glob(filepath.Join(root, ".engram-context*.lock"))
	require.NoError(t, err)
	assert.Empty(t, locks)
	_, err = os.Lstat(filepath.Join(root, ".git", "engram-project.json"))
	assert.True(t, os.IsNotExist(err), "selection must not initialize a canonical cache")
	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, mutations)
	assert.Zero(t, unexpected)
	assert.Zero(t, deniedReads)
	t.Logf("coupled consumer: same_installation_resolved=%t different_installation_refused=%t cli_select_succeeded=%t native_second_binding_resolved=%t catalog_reads=%d legacy_reads=%d backend_mutations=%d marker_sha256=%x installation_identity_sha256=%x,%x", sameErr == nil && same.ProjectID == firstID, otherErr != nil, cliSelectErr == nil, nativeBErr == nil && resolvedB.ProjectID == secondID, catalogReads, legacyReads, mutations, sha256.Sum256(marker), sha256.Sum256(identityFiles[0]), sha256.Sum256(identityFiles[1]))
}
