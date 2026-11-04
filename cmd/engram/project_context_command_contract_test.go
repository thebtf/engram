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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/projectidentity"
)

// Proposed additive CLI spelling for the chosen choices/select consumer
// contract. Tests invoke the existing entry directly; no new Go API, user shell
// command, database, or project initialization is needed.
func TestProjectContextCLIChoices_NonGitSelectsExistingSecondProjectAndResolves(t *testing.T) {
	const firstID = "11111111-1111-4111-8111-111111111111"
	const secondID = "22222222-2222-4222-8222-222222222222"
	const token = "synthetic-project-choice-cli-keycard"
	marker := []byte("{ \"id\": \"941b49\", \"name\": \"Historical label is not selection\" }\n")
	root := t.TempDir()
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(key, home)
	}
	command := exec.CommandContext(t.Context(), "git", "-C", root, "rev-parse", "--is-inside-work-tree")
	command.Env = projectidentity.RepositoryGitEnvironmentV3()
	output, err := command.CombinedOutput()
	require.Error(t, err, "test root must really be outside Git: %s", output)
	var exitError *exec.ExitError
	require.ErrorAs(t, err, &exitError, "a missing Git executable is not evidence of a non-Git root")
	require.Equal(t, 128, exitError.ExitCode())
	require.NoError(t, os.WriteFile(filepath.Join(root, ".engram-project"), marker, 0o600))
	var mu sync.Mutex
	catalogReads := 0
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		assert.Equal(t, "Bearer "+token, request.Header.Get("Authorization"))
		assert.Empty(t, request.Header.Get("Cookie"))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, no-store")
		if request.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch request.URL.Path {
		case "/api/projects":
			catalogReads++
			assert.Empty(t, request.URL.RawQuery)
			_, _ = w.Write([]byte(`{"items":[{"project_id":"` + firstID + `","name":"Same current name","repository_qualifier":"example.test/cli/first"},{"project_id":"` + secondID + `","name":"Same current name","repository_qualifier":"example.test/cli/second"}],"unresolved":[]}`))
		case "/api/projects/resolve":
			assert.Equal(t, "legacy_id=941b49", request.URL.RawQuery)
			_, _ = w.Write([]byte(`{"status":"unresolved","reference":"urn:engram:migration:unresolved:cli-fixture"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("ENGRAM_URL", server.URL)
	t.Setenv("ENGRAM_SERVER_URL", "")
	t.Setenv("ENGRAM_TOKEN", token)

	var choicesOutput bytes.Buffer
	err = runProjectCommand([]string{"choices", "--root", root, "--json"}, &choicesOutput)
	require.NoError(t, err, "shared CLI choices must support an actual non-Git legacy caller")
	var choices struct {
		Choices []struct {
			ProjectID           string `json:"project_id"`
			Name                string `json:"name"`
			RepositoryQualifier string `json:"repository_qualifier"`
			SelectionRef        string `json:"selection_ref"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(choicesOutput.Bytes(), &choices))
	require.Len(t, choices.Choices, 2)
	assert.Equal(t, firstID, choices.Choices[0].ProjectID)
	assert.Equal(t, secondID, choices.Choices[1].ProjectID)
	assert.Equal(t, "Same current name", choices.Choices[0].Name)
	assert.Equal(t, choices.Choices[0].Name, choices.Choices[1].Name)
	assert.Equal(t, "example.test/cli/second", choices.Choices[1].RepositoryQualifier)
	require.NotEmpty(t, choices.Choices[0].SelectionRef)
	require.NotEmpty(t, choices.Choices[1].SelectionRef)
	assert.NotEqual(t, choices.Choices[0].SelectionRef, choices.Choices[1].SelectionRef)
	raw, err := os.ReadFile(filepath.Join(root, ".engram-project"))
	require.NoError(t, err)
	assert.Equal(t, marker, raw, "read-only choices must not initialize a replacement project")
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1, "choices must not create a local root cache")

	var selectedOutput bytes.Buffer
	err = runProjectCommand([]string{"select", "--root", root, "--selection-ref", choices.Choices[1].SelectionRef, "--json"}, &selectedOutput)
	require.NoError(t, err, "explicit selection uses an opaque catalog reference, not a name or raw project UUID")
	mu.Lock()
	readsAfterSelection := catalogReads
	mu.Unlock()
	assert.GreaterOrEqual(t, readsAfterSelection, 2, "select revalidates the current authorized catalog rather than trusting a stale list")

	var resolvedOutput bytes.Buffer
	err = runProjectCommand([]string{"resolve", "--root", root, "--json"}, &resolvedOutput)
	require.NoError(t, err, "ordinary resolve after selection uses the retained caller work-context binding")
	var resolved projectidentity.Project
	require.NoError(t, json.Unmarshal(resolvedOutput.Bytes(), &resolved))
	assert.Equal(t, secondID, resolved.ProjectID)
	raw, err = os.ReadFile(filepath.Join(root, ".engram-project"))
	require.NoError(t, err)
	assert.Equal(t, marker, raw, "selection must not overwrite the legacy marker or migrate data")

	other := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(other, ".engram-project"), marker, 0o600))
	var otherOutput bytes.Buffer
	err = runProjectCommand([]string{"resolve", "--root", other, "--json"}, &otherOutput)
	require.Error(t, err, "same client/server does not implicitly bind another root")
	assert.NotContains(t, otherOutput.String(), secondID)
	for _, result := range []string{choicesOutput.String(), selectedOutput.String(), resolvedOutput.String(), otherOutput.String()} {
		for _, secret := range []string{token, root, other, filepath.ToSlash(root), filepath.ToSlash(other), "Bearer ", "auth_context"} {
			assert.NotContains(t, result, secret)
		}
		for _, path := range []string{root, other} {
			encoded, err := json.Marshal(path)
			require.NoError(t, err)
			assert.NotContains(t, result, string(encoded[1:len(encoded)-1]))
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, request := range requests {
		assert.True(t, strings.HasPrefix(request, "GET /api/projects"), "only existing read endpoints may be used: %s", request)
		assert.NotContains(t, request, "bootstrap")
	}
}

func TestProjectContextCLIChoices_RequiresExplicitRootAndOpaqueSelector(t *testing.T) {
	for _, key := range []string{"ENGRAM_URL", "ENGRAM_SERVER_URL", "ENGRAM_TOKEN"} {
		t.Setenv(key, "")
	}
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(key, home)
	}
	for _, args := range [][]string{
		{"choices", "--json"},
		{"select", "--json", "--selection-ref", "opaque-but-unbound"},
		{"select", "--root", "", "--json", "--selection-ref", "opaque-but-unbound"},
		{"select", "--root", t.TempDir(), "--json"},
		{"select", "--root", t.TempDir(), "--json", "--selection-ref", ""},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var output bytes.Buffer
			err := runProjectCommand(args, &output)
			require.Error(t, err, "must not borrow process cwd, guess a project or accept an absent selection")
		})
	}
}