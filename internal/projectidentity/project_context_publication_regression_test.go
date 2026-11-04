package projectidentity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	publicationProjectA = "11111111-1111-4111-8111-111111111111"
	publicationProjectB = "22222222-2222-4222-8222-222222222222"
	publicationToken    = "synthetic-publication-keycard"
	publicationCatalog  = `{"items":[{"project_id":"11111111-1111-4111-8111-111111111111","name":"A"},{"project_id":"22222222-2222-4222-8222-222222222222","name":"B"}],"unresolved":[]}`
)

func publicationFixture(t *testing.T, handler http.HandlerFunc) (string, ProjectContextCaller) {
	t.Helper()
	root := t.TempDir()
	gitLogical(t, root, "init", "--quiet", "--template=")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/projects" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+publicationToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if handler != nil {
			handler(w, r)
			return
		}
		_, _ = w.Write([]byte(publicationCatalog))
	}))
	t.Cleanup(server.Close)
	caller := ProjectContextCaller{Cwd: root, CallerID: "synthetic-publication-caller", ServerURL: server.URL, Token: publicationToken}
	return root, caller
}

func publicationChoices(t *testing.T, caller ProjectContextCaller) []ProjectChoice {
	t.Helper()
	choices, err := ProjectContextChoices(t.Context(), caller)
	if err != nil || len(choices.Choices) != 2 || choices.Choices[0].ProjectID != publicationProjectA || choices.Choices[1].ProjectID != publicationProjectB {
		t.Fatalf("authorized choices: %+v code=%q", choices, ProjectErrorCode(err))
	}
	return choices.Choices
}

func publicationRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	return raw
}

func publicationWait(t *testing.T, arrived <-chan struct{}) {
	t.Helper()
	select {
	case <-arrived:
	case <-time.After(15 * time.Second):
		t.Fatal("selection did not reach the catalog barrier")
	}
}

func TestProjectContextPublicationPreservesOrphanLock(t *testing.T) {
	root, caller := publicationFixture(t, nil)
	marker := []byte(`{"version":3,"project_id":"11111111-1111-4111-8111-111111111111","name":"A","scope":"repository"}` + "\n")
	markerPath := filepath.Join(root, ".engram-project")
	cachePath := filepath.Join(root, ".git", "engram-project.json")
	for _, path := range []string{markerPath, cachePath} {
		if err := os.WriteFile(path, marker, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	choices := publicationChoices(t, caller)
	bindingPath, err := projectContextPath(caller)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := bindingPath + ".lock"
	lockBytes := []byte("synthetic crash orphan; do not remove or replace\n")
	if err := os.WriteFile(lockPath, lockBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	selected, selectErr := SelectProjectContext(t.Context(), caller, choices[0].SelectionRef)
	if selectErr != nil || selected.ProjectID != publicationProjectA {
		t.Errorf("orphan lock prevented authorized selection: project=%+v code=%q", selected, ProjectErrorCode(selectErr))
	}
	for path, want := range map[string][]byte{markerPath: marker, cachePath: marker, lockPath: lockBytes} {
		if got := publicationRead(t, path); !bytes.Equal(got, want) {
			t.Errorf("selection changed %s", filepath.Base(path))
		}
	}
	if selectErr == nil {
		binding := publicationRead(t, bindingPath)
		resolved, err := ResolveLogicalProjectWithRecovery(t.Context(), caller)
		if err != nil || resolved.ProjectID != publicationProjectA {
			t.Errorf("fresh resolution: project=%+v code=%q", resolved, ProjectErrorCode(err))
		}
		if got := publicationRead(t, bindingPath); !bytes.Equal(got, binding) {
			t.Error("resolution changed the binding")
		}
	}
}

func TestProjectContextPublicationCompetingSelections(t *testing.T) {
	for _, same := range []bool{true, false} {
		name := "different"
		if same {
			name = "same"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			armed := false
			arrived := make(chan struct{}, 2)
			release := make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			root, caller := publicationFixture(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				wait := armed
				mu.Unlock()
				if wait {
					arrived <- struct{}{}
					<-release
				}
				_, _ = w.Write([]byte(publicationCatalog))
			})
			markerPath := filepath.Join(root, ".engram-project")
			marker := []byte("{\"id\":\"941b49\",\"name\":\"historical\"}\n")
			if err := os.WriteFile(markerPath, marker, 0o600); err != nil {
				t.Fatal(err)
			}
			choices := publicationChoices(t, caller)
			bindingPath, err := projectContextPath(caller)
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			armed = true
			mu.Unlock()
			type result struct {
				project   Project
				err       error
				requested string
			}
			results := make(chan result, 2)
			second := 1
			if same {
				second = 0
			}
			for _, choice := range []ProjectChoice{choices[0], choices[second]} {
				go func(choice ProjectChoice) {
					project, err := SelectProjectContext(t.Context(), caller, choice.SelectionRef)
					results <- result{project, err, choice.ProjectID}
				}(choice)
			}
			publicationWait(t, arrived)
			publicationWait(t, arrived)
			close(release)
			r1, r2 := <-results, <-results
			winner := ""
			for _, result := range []result{r1, r2} {
				if result.err == nil {
					if result.project.ProjectID != result.requested || (winner != "" && winner != result.requested) {
						t.Errorf("selection authorized wrong or competing target: %+v", result)
					}
					winner = result.requested
				} else if result.project != (Project{}) || (ProjectErrorCode(result.err) != "PROJECT_SELECTION_STALE" && ProjectErrorCode(result.err) != "PROJECT_IDENTITY_CONFLICT") {
					t.Errorf("unsafe refusal: project=%+v code=%q", result.project, ProjectErrorCode(result.err))
				}
			}
			if winner == "" {
				t.Fatal("no selection won")
			}
			binding := publicationRead(t, bindingPath)
			var stored projectContextBinding
			if decodeProjectContextJSON(binding, &stored) != nil || stored.ProjectID != winner {
				t.Errorf("binding does not contain winner %s", winner)
			}
			if got := publicationRead(t, markerPath); !bytes.Equal(got, marker) {
				t.Error("selection changed marker")
			}
			if _, err := os.Lstat(filepath.Join(root, ".git", "engram-project.json")); !os.IsNotExist(err) {
				t.Errorf("selection changed absent cache: %v", err)
			}
			mu.Lock()
			armed = false
			mu.Unlock()
			resolved, err := ResolveLogicalProjectWithRecovery(t.Context(), caller)
			if err != nil || resolved.ProjectID != winner {
				t.Errorf("resolved=%+v code=%q; want winner %s", resolved, ProjectErrorCode(err), winner)
			}
			if got := publicationRead(t, bindingPath); !bytes.Equal(got, binding) {
				t.Error("winner binding changed")
			}
		})
	}
}

func TestProjectContextPublicationPreservesInterveningBinding(t *testing.T) {
	for _, payload := range []struct{ name, raw, code string }{
		{"malformed", "{not-json\n", "PROJECT_IDENTITY_INVALID"},
		{"unknown-field", "", "PROJECT_IDENTITY_INVALID"},
	} {
		t.Run(payload.name, func(t *testing.T) {
			var mu sync.Mutex
			armed := false
			var bindingPath string
			mutation := make(chan error, 1)
			root, caller := publicationFixture(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				write := armed
				armed = false
				mu.Unlock()
				if write {
					mutation <- os.WriteFile(bindingPath, []byte(payload.raw), 0o600)
				}
				_, _ = w.Write([]byte(publicationCatalog))
			})
			choices := publicationChoices(t, caller)
			var err error
			bindingPath, err = projectContextPath(caller)
			if err != nil {
				t.Fatal(err)
			}
			if payload.name == "unknown-field" {
				snapshot, err := inspectProjectContext(t.Context(), caller)
				if err != nil {
					t.Fatal(err)
				}
				payload.raw = fmt.Sprintf(`{"version":1,"project_id":%q,"scope_digest":%q,"marker_digest":%q,"selection_ref":%q,"unexpected":true}`+"\n", publicationProjectA, snapshot.scopeDigest, snapshot.markerDigest, choices[0].SelectionRef)
			}
			mu.Lock()
			armed = true
			mu.Unlock()
			project, err := SelectProjectContext(t.Context(), caller, choices[0].SelectionRef)
			if mutationErr := <-mutation; mutationErr != nil {
				t.Fatal(mutationErr)
			}
			if project != (Project{}) || ProjectErrorCode(err) != payload.code {
				t.Errorf("intervening binding: project=%+v code=%q", project, ProjectErrorCode(err))
			}
			if got := publicationRead(t, bindingPath); !bytes.Equal(got, []byte(payload.raw)) {
				t.Error("intervening binding was overwritten")
			}
			if _, err := os.Lstat(filepath.Join(root, ".git", "engram-project.json")); !os.IsNotExist(err) {
				t.Errorf("selection changed absent cache: %v", err)
			}
		})
	}
}

func TestProjectContextPublicationPreservesWinnerAgainstStaleSelection(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var mu sync.Mutex
	blockNext := false
	root, caller := publicationFixture(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		block := blockNext
		blockNext = false
		mu.Unlock()
		if block {
			close(entered)
			<-release
		}
		_, _ = w.Write([]byte(publicationCatalog))
	})
	choices := publicationChoices(t, caller)
	bindingPath, err := projectContextPath(caller)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	blockNext = true
	mu.Unlock()
	type selectionResult struct {
		project Project
		err     error
	}
	loser := make(chan selectionResult, 1)
	go func() {
		project, err := SelectProjectContext(t.Context(), caller, choices[1].SelectionRef)
		loser <- selectionResult{project, err}
	}()
	publicationWait(t, entered)
	winner, winnerErr := SelectProjectContext(t.Context(), caller, choices[0].SelectionRef)
	if winnerErr != nil || winner.ProjectID != publicationProjectA {
		close(release)
		<-loser
		t.Fatalf("winning selection: project=%+v code=%q", winner, ProjectErrorCode(winnerErr))
	}
	winnerBytes := publicationRead(t, bindingPath)
	close(release)
	result := <-loser
	if result.project != (Project{}) || (ProjectErrorCode(result.err) != "PROJECT_SELECTION_STALE" && ProjectErrorCode(result.err) != "PROJECT_IDENTITY_CONFLICT") {
		t.Errorf("stale competing selection: project=%+v code=%q", result.project, ProjectErrorCode(result.err))
	}
	if got := publicationRead(t, bindingPath); !bytes.Equal(got, winnerBytes) {
		t.Error("stale selection overwrote winner")
	}
	if _, err := os.Lstat(filepath.Join(root, ".git", "engram-project.json")); !os.IsNotExist(err) {
		t.Errorf("selection changed absent cache: %v", err)
	}
}

func TestProjectContextExistingBindingRevalidation(t *testing.T) {
	for _, scenario := range []string{"idempotent", "denied-catalog", "stale-marker"} {
		t.Run(scenario, func(t *testing.T) {
			var mu sync.Mutex
			denied := false
			root, caller := publicationFixture(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				isDenied := denied
				mu.Unlock()
				if isDenied {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				_, _ = w.Write([]byte(publicationCatalog))
			})
			markerPath := filepath.Join(root, ".engram-project")
			marker := []byte("{\"id\":\"941b49\",\"name\":\"historical\"}\n")
			if err := os.WriteFile(markerPath, marker, 0o600); err != nil {
				t.Fatal(err)
			}
			choices := publicationChoices(t, caller)
			selected, err := SelectProjectContext(t.Context(), caller, choices[0].SelectionRef)
			if err != nil || selected.ProjectID != publicationProjectA {
				t.Fatalf("initial selection: project=%+v code=%q", selected, ProjectErrorCode(err))
			}
			bindingPath, err := projectContextPath(caller)
			if err != nil {
				t.Fatal(err)
			}
			binding := publicationRead(t, bindingPath)
			freshChoices := publicationChoices(t, caller)
			if freshChoices[0].SelectionRef == choices[0].SelectionRef {
				t.Fatal("fresh reference did not reflect the newly present binding")
			}
			if scenario == "denied-catalog" {
				mu.Lock()
				denied = true
				mu.Unlock()
			}
			if scenario == "stale-marker" {
				marker = append(marker, '\n')
				if err := os.WriteFile(markerPath, marker, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			selected, err = SelectProjectContext(t.Context(), caller, freshChoices[0].SelectionRef)
			switch scenario {
			case "idempotent":
				if err != nil || selected.ProjectID != publicationProjectA {
					t.Errorf("fresh same-binding selection: project=%+v code=%q", selected, ProjectErrorCode(err))
				}
			case "denied-catalog":
				if selected != (Project{}) || ProjectErrorCode(err) != "PROJECT_RESOLUTION_UNAVAILABLE" {
					t.Errorf("denied selection: project=%+v code=%q", selected, ProjectErrorCode(err))
				}
			case "stale-marker":
				if selected != (Project{}) || ProjectErrorCode(err) != "PROJECT_IDENTITY_CONFLICT" {
					t.Errorf("stale marker selection: project=%+v code=%q", selected, ProjectErrorCode(err))
				}
			}
			if got := publicationRead(t, bindingPath); !bytes.Equal(got, binding) {
				t.Error("selection changed existing binding")
			}
			if got := publicationRead(t, markerPath); !bytes.Equal(got, marker) {
				t.Error("selection changed marker")
			}
			if _, err := os.Lstat(filepath.Join(root, ".git", "engram-project.json")); !os.IsNotExist(err) {
				t.Errorf("selection changed absent cache: %v", err)
			}
		})
	}
}

func TestProjectContextRelativeRootChoicesSelectResolve(t *testing.T) {
	for _, rootForm := range []string{"dot", "short"} {
		for _, direction := range []string{"relative-to-absolute", "absolute-to-relative"} {
			t.Run(rootForm+"/"+direction, func(t *testing.T) {
				root, absolute := publicationFixture(t, func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(`{"items":[{"project_id":"11111111-1111-4111-8111-111111111111","name":"atlas","repository_qualifier":"example.test/team/atlas"},{"project_id":"22222222-2222-4222-8222-222222222222","name":"other","repository_qualifier":"example.test/team/other"}],"unresolved":[]}`))
				})
				t.Chdir(root)
				relativeRoot := "."
				if rootForm == "short" {
					relativeRoot = "a"
					if err := os.Mkdir(filepath.Join(root, relativeRoot), 0o700); err != nil {
						t.Fatal(err)
					}
					absolute.Cwd = filepath.Join(root, relativeRoot)
				}
				markerPath := filepath.Join(absolute.Cwd, ".engram-project")
				marker := []byte("{\"id\":\"941b49\",\"name\":\"historical\"}\n")
				if err := os.WriteFile(markerPath, marker, 0o600); err != nil {
					t.Fatal(err)
				}
				relative := absolute
				relative.Cwd = relativeRoot
				listCaller, selectCaller := relative, absolute
				if direction == "absolute-to-relative" {
					listCaller, selectCaller = absolute, relative
				}
				choices := publicationChoices(t, listCaller)
				if choices[0].Name != "atlas" || choices[0].RepositoryQualifier != "example.test/team/atlas" {
					t.Fatal("legitimate dotted qualifier or name was not preserved")
				}
				selected, err := SelectProjectContext(t.Context(), selectCaller, choices[0].SelectionRef)
				if err != nil || selected.ProjectID != publicationProjectA {
					t.Fatalf("equivalent root selection: project=%+v code=%q", selected, ProjectErrorCode(err))
				}
				for _, caller := range []ProjectContextCaller{relative, absolute} {
					resolved, err := ResolveLogicalProjectWithRecovery(t.Context(), caller)
					if err != nil || resolved.ProjectID != publicationProjectA {
						t.Errorf("equivalent root resolution: project=%+v code=%q", resolved, ProjectErrorCode(err))
					}
				}
				if got := publicationRead(t, markerPath); !bytes.Equal(got, marker) {
					t.Error("selection changed the historical marker")
				}
				bindings, err := filepath.Glob(filepath.Join(absolute.Cwd, ".engram-context.*.json"))
				if err != nil || len(bindings) != 1 {
					t.Errorf("equivalent roots must share one binding: count=%d err=%v", len(bindings), err)
				}
			})
		}
	}
}

func TestProjectContextRelativeRootExistingAbsoluteBinding(t *testing.T) {
	root, absolute := publicationFixture(t, nil)
	t.Chdir(root)
	markerPath := filepath.Join(root, ".engram-project")
	marker := []byte("{\"id\":\"941b49\",\"name\":\"historical\"}\n")
	if err := os.WriteFile(markerPath, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	choices := publicationChoices(t, absolute)
	snapshot, err := inspectProjectContext(t.Context(), absolute)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve the 7.0.20 absolute-root binding format independently of any new path algorithm.
	scope := projectContextDigest("project-context-v1", root, root, absolute.CallerID, absolute.ServerURL+"/api/projects", projectContextDigest(absolute.Token))
	bindingPath := filepath.Join(root, ".engram-context."+scope+".json")
	binding, err := json.Marshal(projectContextBinding{Version: 1, ProjectID: publicationProjectA, ScopeDigest: scope, MarkerDigest: snapshot.markerDigest, SelectionRef: choices[0].SelectionRef})
	if err != nil {
		t.Fatal(err)
	}
	binding = append(binding, '\n')
	if err := os.WriteFile(bindingPath, binding, 0o600); err != nil {
		t.Fatal(err)
	}
	relative := absolute
	relative.Cwd = "."
	for _, caller := range []ProjectContextCaller{relative, absolute} {
		resolved, err := ResolveLogicalProjectWithRecovery(t.Context(), caller)
		if err != nil || resolved.ProjectID != publicationProjectA {
			t.Errorf("existing absolute binding resolution: project=%+v code=%q", resolved, ProjectErrorCode(err))
		}
	}
	fresh := publicationChoices(t, relative)
	selected, err := SelectProjectContext(t.Context(), absolute, fresh[1].SelectionRef)
	if selected != (Project{}) || ProjectErrorCode(err) != "PROJECT_IDENTITY_CONFLICT" {
		t.Errorf("existing binding must reject another project: project=%+v code=%q", selected, ProjectErrorCode(err))
	}
	if got := publicationRead(t, bindingPath); !bytes.Equal(got, binding) {
		t.Error("existing absolute binding was overwritten")
	}
	if got := publicationRead(t, markerPath); !bytes.Equal(got, marker) {
		t.Error("existing historical marker was overwritten")
	}
	bindings, err := filepath.Glob(filepath.Join(root, ".engram-context.*.json"))
	if err != nil || len(bindings) != 1 {
		t.Errorf("relative root must not create a second binding: count=%d err=%v", len(bindings), err)
	}
}

func TestProjectContextRelativeRootDoesNotManufactureMissingContext(t *testing.T) {
	root, caller := publicationFixture(t, nil)
	t.Chdir(root)
	choices := publicationChoices(t, caller)
	before := crossCallerFileSnapshot(t, root)
	for _, invalidRoot := range []string{"", "missing-root"} {
		invalid := caller
		invalid.Cwd = invalidRoot
		listed, err := ProjectContextChoices(t.Context(), invalid)
		if len(listed.Choices) != 0 || ProjectErrorCode(err) != "PROJECT_CONTEXT_REQUIRED" {
			t.Errorf("invalid root choices: count=%d code=%q", len(listed.Choices), ProjectErrorCode(err))
		}
		selected, err := SelectProjectContext(t.Context(), invalid, choices[0].SelectionRef)
		if selected != (Project{}) || ProjectErrorCode(err) != "PROJECT_CONTEXT_REQUIRED" {
			t.Errorf("invalid root selection: project=%+v code=%q", selected, ProjectErrorCode(err))
		}
		resolved, err := ResolveLogicalProjectWithRecovery(t.Context(), invalid)
		if resolved != (Project{}) || ProjectErrorCode(err) != "PROJECT_CONTEXT_REQUIRED" {
			t.Errorf("invalid root resolution: project=%+v code=%q", resolved, ProjectErrorCode(err))
		}
	}
	after := crossCallerFileSnapshot(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Error("missing or invalid caller context changed fixture files")
	}
}

func TestProjectContextRelativeRootRejectsPrivateCatalogText(t *testing.T) {
	for _, leaked := range []string{"absolute-root", "token"} {
		t.Run(leaked, func(t *testing.T) {
			var privateText string
			root, absolute := publicationFixture(t, func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"items":      []map[string]string{{"project_id": publicationProjectA, "name": "private " + privateText}},
					"unresolved": []any{},
				})
			})
			child := filepath.Join(root, "child")
			if err := os.Mkdir(child, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(child)
			privateText = root
			if leaked == "token" {
				privateText = publicationToken
			}
			relative := absolute
			relative.Cwd = ".."
			for _, caller := range []ProjectContextCaller{relative, absolute} {
				choices, err := ProjectContextChoices(t.Context(), caller)
				if len(choices.Choices) != 0 || ProjectErrorCode(err) != "PROJECT_IDENTITY_INVALID" {
					t.Errorf("private catalog text must be rejected: count=%d code=%q", len(choices.Choices), ProjectErrorCode(err))
				}
			}
		})
	}
}

func TestProjectContextCatalogPathBoundary(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		root      string
		qualifier string
	}{
		{name: "filesystem-root", root: "/", qualifier: "example.test/team/root"},
		{name: "app", root: "/app", qualifier: "example.test/team/app"},
		{name: "src", root: "/src", qualifier: "example.test/team/src"},
		{name: "work", root: "/work", qualifier: "example.test/team/work"},
		{name: "deep", root: "/srv/project/app", qualifier: "example.test/srv/project/app"},
		{name: "windows", root: `C:\Users\fixture\app`, qualifier: "example.test/team/app"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var name, qualifier string
			root, caller := publicationFixture(t, func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"items":      []map[string]string{{"project_id": publicationProjectA, "name": name, "repository_qualifier": qualifier}},
					"unresolved": []any{},
				})
			})
			before := crossCallerFileSnapshot(t, root)
			qualifier = scenario.qualifier
			urlRoot := strings.ReplaceAll(filepath.ToSlash(scenario.root), `\`, "/")
			for _, goodName := range []string{"ordinary project", "https://example.test", "https://example.test/team" + urlRoot, "https://example.test/team%20space" + urlRoot, "ssh://example.test/team" + urlRoot, "git://example.test/team" + urlRoot, "https://[::1]", "https://[::1]/team" + urlRoot} {
				name = goodName
				items, _, err := readProjectContextCatalog(t.Context(), caller, scenario.root)
				if err != nil || len(items) != 1 || items[0].Name != goodName || items[0].RepositoryQualifier != qualifier {
					t.Errorf("legitimate catalog descriptor rejected: count=%d code=%q", len(items), ProjectErrorCode(err))
				}
			}
			qualifier = "example.test/team/neutral"
			for _, leakedName := range []string{
				scenario.root,
				"open " + scenario.root + " now",
				"root=\"" + scenario.root + "\"",
				"root=" + scenario.root + "/child",
				"private " + publicationToken,
				"embedded" + scenario.root + "/child",
				"https://example.test/team" + urlRoot + " then " + scenario.root,
				"https://example.test/team" + urlRoot + "," + scenario.root,
				"https://example.test/team" + urlRoot + ";" + scenario.root,
				"https://[::1]/team" + urlRoot + "[" + scenario.root + "]",
				"https://[not-ipv6]/team" + scenario.root,
				"https://example.test/team" + urlRoot + " then " + publicationToken,
				"https://example.test/%zz" + scenario.root,
				"https://example.test" + scenario.root + "?bad=%zz",
				"https://example.test/path?local=" + scenario.root,
				"https://example.test?local=" + scenario.root,
				"https://example.test/path#local=" + scenario.root,
				"https://example.test:99999" + scenario.root,
				"https://user@example.test" + scenario.root,
				`https://example.test\bad` + scenario.root,
				"https://example.test/" + publicationToken + scenario.root,
			} {
				name = leakedName
				items, _, err := readProjectContextCatalog(t.Context(), caller, scenario.root)
				if len(items) != 0 || ProjectErrorCode(err) != "PROJECT_IDENTITY_INVALID" {
					t.Errorf("private catalog descriptor accepted: count=%d code=%q", len(items), ProjectErrorCode(err))
				}
			}
			name = scenario.root
			qualifier = scenario.qualifier
			items, _, err := readProjectContextCatalog(t.Context(), caller, scenario.root)
			if len(items) != 0 || ProjectErrorCode(err) != "PROJECT_IDENTITY_INVALID" {
				t.Errorf("combined qualifier and root disclosure accepted: count=%d code=%q", len(items), ProjectErrorCode(err))
			}
			name = "ordinary project"
			qualifier = "example.test/team/../invalid"
			items, _, err = readProjectContextCatalog(t.Context(), caller, scenario.root)
			if len(items) != 0 || ProjectErrorCode(err) != "PROJECT_IDENTITY_INVALID" {
				t.Errorf("malformed qualifier accepted: count=%d code=%q", len(items), ProjectErrorCode(err))
			}
			qualifier = "example.test/team/" + publicationToken
			items, _, err = readProjectContextCatalog(t.Context(), caller, scenario.root)
			if len(items) != 0 || ProjectErrorCode(err) != "PROJECT_IDENTITY_INVALID" {
				t.Errorf("token in valid qualifier accepted: count=%d code=%q", len(items), ProjectErrorCode(err))
			}
			name = "private /workspace/private dir/child"
			qualifier = "example.test/team/neutral"
			items, _, err = readProjectContextCatalog(t.Context(), caller, "/workspace/private dir")
			if len(items) != 0 || ProjectErrorCode(err) != "PROJECT_IDENTITY_INVALID" {
				t.Errorf("root containing spaces accepted: count=%d code=%q", len(items), ProjectErrorCode(err))
			}
			if after := crossCallerFileSnapshot(t, root); !reflect.DeepEqual(before, after) {
				t.Error("catalog reads changed fixture files")
			}
		})
	}
}
