package projectidentity

import (
	"bytes"
	"crypto/sha256"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestProjectContextCrossCallerIsolation(t *testing.T) {
	const (
		projectA = "11111111-1111-4111-8111-111111111111"
		projectB = "22222222-2222-4222-8222-222222222222"
	)
	for _, scenario := range []struct {
		name           string
		callerA        string
		callerB        string
		separateTokens bool
	}{
		{name: "native-and-cli", callerA: projectA},
		{name: "separately-authorized-callers", separateTokens: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var root string
			t.Cleanup(func() {
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Errorf("test-owned fixture root was not removed: %v", err)
				} else {
					t.Log("cleanup: test-owned fixture root removed")
				}
			})
			root = t.TempDir()
			gitLogical(t, root, "init", "--quiet", "--template=")
			marker := []byte("{ \"id\": \"941b49\", \"name\": \"same historical label\" }\n")
			markerPath := filepath.Join(root, ".engram-project")
			if err := os.WriteFile(markerPath, marker, 0o600); err != nil {
				t.Fatal(err)
			}
			originalFiles := crossCallerFileSnapshot(t, root)
			catalog := []byte(`{"items":[{"project_id":"11111111-1111-4111-8111-111111111111","name":"Current shared name","repository_qualifier":"example.test/team/first"},{"project_id":"22222222-2222-4222-8222-222222222222","name":"Current shared name","repository_qualifier":"example.test/team/second"}],"unresolved":[]}`)
			originalCatalog := bytes.Clone(catalog)
			legacyReply := []byte(`{"status":"unresolved","reference":"urn:engram:migration:unresolved:cross-caller","reason":"Existing membership is ambiguous"}`)
			t.Logf("synthetic inputs: marker_sha256=%x catalog_sha256=%x legacy_reply_sha256=%x", sha256.Sum256(marker), sha256.Sum256(catalog), sha256.Sum256(legacyReply))

			// Tokens are fixture-only authorization inputs. Never print them, even
			// when a request violates the expected authentication boundary.
			tokenA := "synthetic-cross-caller-keycard-a"
			tokenB := tokenA
			if scenario.separateTokens {
				tokenB = "synthetic-cross-caller-keycard-b"
			}
			var mu sync.Mutex
			var authorizedReads, deniedReads, mutationRequests, unexpectedRequests int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if request.Method != http.MethodGet {
					mutationRequests++
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				if request.Header.Get("Cookie") != "" {
					unexpectedRequests++
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				authorization := request.Header.Get("Authorization")
				if authorization != "Bearer "+tokenA && authorization != "Bearer "+tokenB {
					deniedReads++
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				authorizedReads++
				switch {
				case request.URL.Path == "/api/projects" && request.URL.RawQuery == "":
					_, _ = w.Write(catalog)
				case request.URL.Path == "/api/projects/resolve" && request.URL.RawQuery == "legacy_id=941b49":
					_, _ = w.Write(legacyReply)
				default:
					unexpectedRequests++
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			t.Cleanup(func() {
				after := crossCallerFileSnapshot(t, root)
				for path, digest := range originalFiles {
					if actual, present := after[path]; !present || actual != digest {
						t.Errorf("selection changed an original fixture file: %s", path)
					}
				}
				if raw, err := os.ReadFile(markerPath); err != nil || !bytes.Equal(raw, marker) {
					t.Errorf("selection changed the legacy marker: read_error=%v", err)
				}
				for _, path := range []string{filepath.Join(root, ".git", "engram-project.json"), filepath.Join(root, ".engram-context.json.lock")} {
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Errorf("selection published canonical cache or left a lock: %s, %v", filepath.Base(path), err)
					}
				}
				scopedLocks, err := filepath.Glob(filepath.Join(root, ".engram-context.*.json.lock"))
				if err != nil {
					t.Errorf("inspect scoped selection locks: %v", err)
				}
				for _, path := range scopedLocks {
					t.Errorf("selection left a scoped lock: %s", filepath.Base(path))
				}
				mu.Lock()
				defer mu.Unlock()
				if !bytes.Equal(catalog, originalCatalog) || mutationRequests != 0 || unexpectedRequests != 0 || authorizedReads == 0 || deniedReads == 0 {
					t.Errorf("fixture invariants: catalog_unchanged=%t authorized_reads=%d denied_reads=%d mutation_requests=%d unexpected_requests=%d", bytes.Equal(catalog, originalCatalog), authorizedReads, deniedReads, mutationRequests, unexpectedRequests)
				}
				t.Logf("preservation: legacy marker and original files unchanged; catalog_unchanged=%t; authorized_reads=%d denied_reads=%d mutation_requests=%d unexpected_requests=%d", bytes.Equal(catalog, originalCatalog), authorizedReads, deniedReads, mutationRequests, unexpectedRequests)
			})

			callerA := ProjectContextCaller{Cwd: root, CallerID: scenario.callerA, ServerURL: server.URL, Token: tokenA}
			callerB := ProjectContextCaller{Cwd: root, CallerID: scenario.callerB, ServerURL: server.URL, Token: tokenB}
			for _, caller := range []ProjectContextCaller{callerA, callerB} {
				project, err := ResolveLogicalProjectWithRecovery(t.Context(), caller)
				if project != (Project{}) || ProjectErrorCode(err) != "PROJECT_MIGRATION_REQUIRED" {
					t.Fatalf("synthetic legacy ambiguity was not reached: project=%+v code=%q", project, ProjectErrorCode(err))
				}
			}
			list := func(caller ProjectContextCaller) []ProjectChoice {
				t.Helper()
				choices, err := ProjectContextChoices(t.Context(), caller)
				if err != nil {
					t.Fatalf("initial authorized catalog read failed: code=%q", ProjectErrorCode(err))
				}
				if len(choices.Choices) != 2 || choices.Choices[0].ProjectID != projectA || choices.Choices[1].ProjectID != projectB || choices.Choices[0].Name != "Current shared name" || choices.Choices[1].Name != "Current shared name" || choices.Choices[0].RepositoryQualifier == choices.Choices[1].RepositoryQualifier {
					t.Fatal("authorized choices did not preserve both existing distinguishable project descriptors")
				}
				return choices.Choices
			}
			choicesA, choicesB := list(callerA), list(callerB)
			if choicesA[0].SelectionRef == "" || choicesB[0].SelectionRef == "" || choicesA[0].SelectionRef == choicesB[0].SelectionRef {
				t.Fatal("the fixture did not establish two distinct caller-bound references")
			}
			reject := func(t *testing.T, caller ProjectContextCaller, reference, wantCode string) {
				t.Helper()
				before := crossCallerFileSnapshot(t, root)
				project, err := SelectProjectContext(t.Context(), caller, reference)
				if project != (Project{}) || ProjectErrorCode(err) != wantCode {
					t.Errorf("reference rejection: project=%+v code=%q, want empty project and %s", project, ProjectErrorCode(err), wantCode)
				}
				if !reflect.DeepEqual(before, crossCallerFileSnapshot(t, root)) {
					t.Error("rejected selection changed local fixture bytes")
				}
			}
			t.Run("references-do-not-grant-authorization", func(t *testing.T) {
				before := crossCallerFileSnapshot(t, root)
				reject(t, callerB, choicesA[0].SelectionRef, "PROJECT_SELECTION_STALE")
				reject(t, callerA, choicesB[1].SelectionRef, "PROJECT_SELECTION_STALE")
				unauthorized := callerA
				unauthorized.Token = ""
				reject(t, unauthorized, choicesA[0].SelectionRef, "PROJECT_RESOLUTION_UNAVAILABLE")
				choices, err := ProjectContextChoices(t.Context(), unauthorized)
				if len(choices.Choices) != 0 || ProjectErrorCode(err) != "PROJECT_RESOLUTION_UNAVAILABLE" {
					t.Errorf("unauthorized catalog yielded choices or unexpected error: count=%d code=%q", len(choices.Choices), ProjectErrorCode(err))
				}
				if !reflect.DeepEqual(before, crossCallerFileSnapshot(t, root)) {
					t.Error("unauthorized catalog or reference changed local fixture bytes")
				}
			})

			selectedA, err := SelectProjectContext(t.Context(), callerA, choicesA[0].SelectionRef)
			if err != nil || selectedA != (Project{ProjectID: projectA, Name: "Current shared name"}) {
				t.Fatalf("caller A could not bind its authorized project: project=%+v code=%q", selectedA, ProjectErrorCode(err))
			}
			t.Run("same-caller-stale-reference-is-read-only", func(t *testing.T) {
				reject(t, callerA, choicesA[1].SelectionRef, "PROJECT_SELECTION_STALE")
			})
			// A must not consume B's independent selection evidence. Re-list for
			// B after A binds, then exercise select even if that listing collides.
			choicesAfterA, listErr := ProjectContextChoices(t.Context(), callerB)
			referenceB := choicesB[1].SelectionRef
			if listErr != nil {
				t.Errorf("cross-caller collision: caller B choices after caller A selected %s: code=%q; want independently authorized choices", projectA, ProjectErrorCode(listErr))
			} else {
				if len(choicesAfterA.Choices) != 2 || choicesAfterA.Choices[1].ProjectID != projectB {
					t.Fatal("caller B lost its independently authorized project")
				}
				referenceB = choicesAfterA.Choices[1].SelectionRef
			}
			selectedB, selectErr := SelectProjectContext(t.Context(), callerB, referenceB)
			if selectErr != nil || selectedB != (Project{ProjectID: projectB, Name: "Current shared name"}) {
				t.Errorf("cross-caller collision: caller B select after caller A binding: project=%+v code=%q; want %s", selectedB, ProjectErrorCode(selectErr), projectB)
			}
			for _, selected := range []struct {
				label  string
				caller ProjectContextCaller
				want   string
				bound  bool
			}{
				{"A", callerA, projectA, true},
				{"B", callerB, projectB, selectErr == nil && selectedB.ProjectID == projectB},
			} {
				if !selected.bound {
					t.Logf("fresh caller %s selected-ID check NOT_REACHED: its selection was refused", selected.label)
					continue
				}
				fresh := ProjectContextCaller{Cwd: selected.caller.Cwd, CallerID: selected.caller.CallerID, ServerURL: selected.caller.ServerURL, Token: selected.caller.Token}
				project, err := ResolveLogicalProjectWithRecovery(t.Context(), fresh)
				if err != nil || project.ProjectID != selected.want {
					t.Errorf("fresh caller %s resolved project=%+v code=%q; want only its selected ID %s", selected.label, project, ProjectErrorCode(err), selected.want)
				} else {
					t.Logf("fresh caller %s resolved only its selected ID %s", selected.label, project.ProjectID)
				}
			}
		})
	}
}

func crossCallerFileSnapshot(t *testing.T, root string) map[string][sha256.Size]byte {
	t.Helper()
	files := make(map[string][sha256.Size]byte)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[relative] = sha256.Sum256(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot test-owned fixture: %v", err)
	}
	return files
}
