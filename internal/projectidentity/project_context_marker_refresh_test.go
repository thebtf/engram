package projectidentity

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestProjectContextMarkerRefresh(t *testing.T) {
	const (
		projectA = "11111111-1111-4111-8111-111111111111"
		projectB = "22222222-2222-4222-8222-222222222222"
		token    = "synthetic-marker-refresh-keycard"
		markerA  = `{"version":3,"project_id":"11111111-1111-4111-8111-111111111111","name":"Project A","scope":"repository"}`
		markerB  = `{"version":3,"project_id":"22222222-2222-4222-8222-222222222222","name":"Project B","scope":"repository"}`
		legacy   = `{"id":"941b49","name":"same historical label"}`
	)
	for _, scenario := range []struct {
		name    string
		initial string
		marker  string
		action  string
	}{
		{name: "same-canonical-id", marker: markerA + "\n"},
		{name: "different-canonical-id", marker: markerB + "\n", action: "conflict"},
		{name: "unchanged-legacy-id-remains-unpinned", initial: legacy, marker: legacy + "\n", action: "conflict"},
		{name: "revoked-after-binding", marker: markerA + "\n", action: "revoked"},
		{name: "marker-changes-during-catalog", marker: markerA + "\n", action: "marker-race"},
		{name: "binding-changes-during-catalog", marker: markerA + "\n", action: "binding-race"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			gitLogical(t, root, "init", "--quiet", "--template=")
			markerPath := filepath.Join(root, ".engram-project")
			initial := scenario.initial
			if initial == "" {
				initial = markerA
			}
			if err := os.WriteFile(markerPath, []byte(initial), 0o600); err != nil {
				t.Fatal(err)
			}

			var catalogReads atomic.Int64
			var mutate atomic.Bool
			var mutationFired atomic.Bool
			var mutationPath string
			var mutationBytes []byte
			mutationResult := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/projects" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+token {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				catalogReads.Add(1)
				if mutate.Load() && (scenario.action == "marker-race" || scenario.action == "binding-race") && mutationFired.CompareAndSwap(false, true) {
					mutationResult <- os.WriteFile(mutationPath, mutationBytes, 0o600)
				}
				w.Header().Set("Content-Type", "application/json")
				if mutate.Load() && scenario.action == "revoked" {
					_, _ = w.Write([]byte(`{"items":[{"project_id":"22222222-2222-4222-8222-222222222222","name":"Project B"}],"unresolved":[]}`))
				} else {
					_, _ = w.Write([]byte(`{"items":[{"project_id":"11111111-1111-4111-8111-111111111111","name":"Project A"},{"project_id":"22222222-2222-4222-8222-222222222222","name":"Project B"}],"unresolved":[]}`))
				}
			}))
			t.Cleanup(server.Close)
			caller := ProjectContextCaller{Cwd: root, CallerID: "synthetic-marker-refresh-caller", ServerURL: server.URL, Token: token}
			choices, err := ProjectContextChoices(t.Context(), caller)
			if err != nil || len(choices.Choices) != 2 || choices.Choices[0].ProjectID != projectA || choices.Choices[1].ProjectID != projectB {
				t.Fatalf("authorized catalog: choices=%+v code=%q", choices, ProjectErrorCode(err))
			}
			selected, err := SelectProjectContext(t.Context(), caller, choices.Choices[0].SelectionRef)
			if err != nil || selected.ProjectID != projectA {
				t.Fatalf("select A: project=%+v code=%q", selected, ProjectErrorCode(err))
			}
			bindingPath, err := projectContextPath(caller)
			if err != nil {
				t.Fatal(err)
			}
			binding, err := os.ReadFile(bindingPath)
			if err != nil || len(binding) == 0 {
				t.Fatalf("read persisted caller binding: %v", err)
			}
			resolved, err := ResolveLogicalProjectWithRecovery(t.Context(), caller)
			if err != nil || resolved.ProjectID != projectA {
				t.Fatalf("resolve before marker change: project=%+v code=%q", resolved, ProjectErrorCode(err))
			}
			if err := os.WriteFile(markerPath, []byte(scenario.marker), 0o600); err != nil {
				t.Fatal(err)
			}
			readsBefore := catalogReads.Load()
			expectedMarker := []byte(scenario.marker)
			expectedBinding := binding
			switch scenario.action {
			case "marker-race":
				mutationPath = markerPath
				mutationBytes = []byte(scenario.marker + "\n")
				expectedMarker = mutationBytes
			case "binding-race":
				mutationPath = bindingPath
				mutationBytes = append(bytes.Clone(binding), '\n')
				expectedBinding = mutationBytes
			}
			mutate.Store(true)
			resolved, err = ResolveLogicalProjectWithRecovery(t.Context(), caller)
			switch scenario.action {
			case "conflict":
				if resolved != (Project{}) || ProjectErrorCode(err) != "PROJECT_IDENTITY_CONFLICT" {
					t.Errorf("%s: project=%+v code=%q; want conflict", scenario.name, resolved, ProjectErrorCode(err))
				}
			case "revoked", "marker-race", "binding-race":
				if resolved != (Project{}) || ProjectErrorCode(err) != "PROJECT_SELECTION_STALE" {
					t.Errorf("%s: project=%+v code=%q; want stale selection", scenario.action, resolved, ProjectErrorCode(err))
				}
				if catalogReads.Load() <= readsBefore {
					t.Error("fresh catalog was not consulted")
				}
				if scenario.action != "revoked" {
					select {
					case mutationErr := <-mutationResult:
						if mutationErr != nil {
							t.Errorf("fixture mutation failed: %v", mutationErr)
						}
					default:
						t.Error("fixture mutation was not reached during catalog fetch")
					}
				}
			default:
				if err != nil || resolved.ProjectID != projectA {
					t.Errorf("same-ID marker refresh: project=%+v code=%q; want authorized project A", resolved, ProjectErrorCode(err))
				} else if catalogReads.Load() <= readsBefore {
					t.Error("same-ID recovery did not recheck the authorized catalog")
				}
			}
			currentMarker, readErr := os.ReadFile(markerPath)
			if readErr != nil || !bytes.Equal(currentMarker, expectedMarker) {
				t.Errorf("resolution changed marker: read_error=%v", readErr)
			}
			currentBinding, readErr := os.ReadFile(bindingPath)
			if readErr != nil || !bytes.Equal(currentBinding, expectedBinding) {
				t.Errorf("resolution changed caller binding: read_error=%v", readErr)
			}
			if scenario.initial == legacy {
				if _, err := os.Lstat(filepath.Join(root, ".git", "engram-project.json")); !os.IsNotExist(err) {
					t.Errorf("legacy marker case created a canonical cache: %v", err)
				}
			}
		})
	}
}
