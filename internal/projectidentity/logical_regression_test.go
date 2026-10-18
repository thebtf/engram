package projectidentity

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLogicalProjectConcurrentResolveAndInitKeepOneIdentity(t *testing.T) {
	for _, schedule := range []string{"init-wins", "init-cache-publication-busy"} {
		t.Run(schedule, func(t *testing.T) {
			root := t.TempDir()
			gitLogical(t, root, "init", "--quiet")
			gitLogical(t, root, "remote", "add", "origin", "https://example.test/Identity/Concurrent.git")
			unrelated := t.TempDir()
			gitLogical(t, unrelated, "init", "--quiet")
			preserved := map[string][]byte{
				filepath.Join(unrelated, anchorFilenameV3):              []byte("{ \"project_id\":\"33333333-3333-4333-8333-333333333333\", \"name\":\"Other marker\" }\n"),
				filepath.Join(unrelated, ".git", "engram-project.json"): []byte("{ \"name\":\"Other cache\", \"project_id\":\"33333333-3333-4333-8333-333333333333\" }\n"),
			}
			for path, raw := range preserved {
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			defer func() {
				for path, before := range preserved {
					if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, before) {
						t.Errorf("concurrent operations changed unrelated identity file %s: %q, %v", path, after, err)
					}
				}
			}()
			remote := Project{ProjectID: "11111111-1111-4111-8111-111111111111", Name: "Online A"}
			type result struct {
				project Project
				err     error
			}
			entered := make(chan BootstrapRequest, 1)
			resume := make(chan struct{})
			done := make(chan result, 1)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			go func() {
				project, err := ResolveLogicalProject(ctx, root, func(ctx context.Context, input BootstrapRequest) (Project, error) {
					entered <- input
					select {
					case <-resume:
						return remote, nil
					case <-ctx.Done():
						return Project{}, ctx.Err()
					}
				})
				done <- result{project, err}
			}()
			select {
			case input := <-entered:
				if input.RepositoryLocator != "example.test/Identity/Concurrent" {
					t.Fatalf("bootstrap locator = %q", input.RepositoryLocator)
				}
			case early := <-done:
				t.Fatalf("online resolution did not reach bootstrap: %+v, %v", early.project, early.err)
			case <-t.Context().Done():
				t.Fatal("test cancelled before bootstrap")
			}
			cacheLock := filepath.Join(root, ".git", "engram-project.json.lock")
			lockBytes := []byte("test-owned cache publication barrier\n")
			if schedule == "init-cache-publication-busy" {
				lock, err := os.OpenFile(cacheLock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := lock.Write(lockBytes); err != nil {
					lock.Close()
					t.Fatal(err)
				}
				if err := lock.Close(); err != nil {
					t.Fatal(err)
				}
			}
			initialized, initErr := InitLogicalProject(ctx, root, "Explicit B")
			if schedule == "init-cache-publication-busy" {
				if after, err := os.ReadFile(cacheLock); err != nil || !bytes.Equal(after, lockBytes) {
					t.Errorf("Init changed the occupied cache lock: %q, %v", after, err)
				}
				if err := os.Remove(cacheLock); err != nil {
					t.Fatal(err)
				}
			}
			close(resume)
			online := <-done
			t.Logf("chronology: Resolve entered bootstrap; cache barrier=%t; Init returned project=%+v code=%q; test-owned barrier released; Resolve returned project=%+v code=%q", schedule == "init-cache-publication-busy", initialized, ProjectErrorCode(initErr), online.project, ProjectErrorCode(online.err))
			for _, contender := range []result{{initialized, initErr}, online} {
				if contender.err != nil {
					code := ProjectErrorCode(contender.err)
					if contender.project != (Project{}) || code != "PROJECT_IDENTITY_CONFLICT" && code != "PROJECT_RESOLUTION_UNAVAILABLE" {
						t.Errorf("failed contender returned authority or unexpected error: %+v, %v", contender.project, contender.err)
					}
				}
			}
			marker := readLogicalProjectFile(t, filepath.Join(root, anchorFilenameV3))
			cache := readLogicalProjectFile(t, filepath.Join(root, ".git", "engram-project.json"))
			if marker.ProjectID != cache.ProjectID {
				t.Fatalf("normal concurrent operations split local identity: marker=%+v cache=%+v", marker, cache)
			}
			if initErr != nil && online.err != nil {
				t.Fatalf("no successful contender: init=%v resolve=%v", initErr, online.err)
			}
			for _, contender := range []result{{initialized, initErr}, online} {
				if contender.err == nil && contender.project.ProjectID != marker.ProjectID {
					t.Errorf("successful contender differs from published identity: %+v, marker=%+v", contender.project, marker)
				}
			}
			if resolved, err := ResolveLogicalProject(t.Context(), root, nil); err != nil || resolved.ProjectID != marker.ProjectID {
				t.Errorf("offline resolution after contenders = %+v, %v", resolved, err)
			}
		})
	}
}

func TestLogicalProjectMarkerPublicationFailurePreservesLock(t *testing.T) {
	root := t.TempDir()
	gitLogical(t, root, "init", "--quiet")
	gitLogical(t, root, "remote", "add", "origin", "https://example.test/Identity/Locked.git")
	markerPath := filepath.Join(root, anchorFilenameV3)
	lockPath := markerPath + ".lock"
	lockBytes := []byte("another local writer owns this lock\n")
	if err := os.WriteFile(lockPath, lockBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	remote := Project{ProjectID: "11111111-1111-4111-8111-111111111111", Name: "Online A"}
	calls := 0
	got, err := ResolveLogicalProject(t.Context(), root, func(context.Context, BootstrapRequest) (Project, error) {
		calls++
		return remote, nil
	})
	if got != (Project{}) || ProjectErrorCode(err) != "PROJECT_RESOLUTION_UNAVAILABLE" || calls != 1 {
		t.Fatalf("occupied marker lock: project=%+v error=%v bootstrap_calls=%d", got, err, calls)
	}
	if after, err := os.ReadFile(lockPath); err != nil || !bytes.Equal(after, lockBytes) {
		t.Errorf("contender changed another writer's lock: %q, %v", after, err)
	}
	if _, err := os.Lstat(markerPath); !os.IsNotExist(err) {
		t.Errorf("occupied marker lock allowed marker publication: %v", err)
	}
	cachePath := filepath.Join(root, ".git", "engram-project.json")
	if _, err := os.Lstat(cachePath); err == nil {
		cached := readLogicalProjectFile(t, cachePath)
		if cached != remote {
			t.Errorf("partial publication changed bootstrap identity: %+v", cached)
		}
		t.Logf("marker publication failed; cache contains bootstrap identity %+v (not marker B/cache A evidence)", cached)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestLogicalProjectConflictPreservesPublishedBytes(t *testing.T) {
	root := t.TempDir()
	gitLogical(t, root, "init", "--quiet")
	files := map[string][]byte{
		filepath.Join(root, anchorFilenameV3):                   []byte("{\n  \"project_id\":\"22222222-2222-4222-8222-222222222222\",\"name\":\"Manual B\"\n}\n"),
		filepath.Join(root, ".git", "engram-project.json"):      []byte("{\"name\":\"Pinned A\", \"project_id\":\"11111111-1111-4111-8111-111111111111\"}\n"),
		filepath.Join(root, ".git", "unrelated-identity.json"):  []byte("unrelated cache bytes\n"),
		filepath.Join(root, ".git", "engram-project.json.lock"): []byte("unrelated cache writer lock\n"),
		filepath.Join(root, anchorFilenameV3+".lock"):           []byte("unrelated marker writer lock\n"),
	}
	for path, raw := range files {
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, operation := range []string{"resolve", "init"} {
		t.Run(operation, func(t *testing.T) {
			var got Project
			var err error
			if operation == "resolve" {
				got, err = ResolveLogicalProject(t.Context(), root, nil)
			} else {
				got, err = InitLogicalProject(t.Context(), root, "Must not replace manual label")
			}
			if got != (Project{}) || ProjectErrorCode(err) != "PROJECT_IDENTITY_CONFLICT" {
				t.Errorf("existing manual conflict must fail closed: project=%+v error=%v", got, err)
			}
			for path, before := range files {
				if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, before) {
					t.Errorf("failed operation changed %s: %q, %v", path, after, err)
				}
			}
		})
	}
}

func TestInitLogicalProjectPreservesNameOnlyLabel(t *testing.T) {
	for _, git := range []bool{false, true} {
		for _, tc := range []struct{ name, override, want string }{
			{"default", "", "Old label"},
			{"explicit", "New label", "New label"},
		} {
			kind := "non-git/"
			if git {
				kind = "git/"
			}
			t.Run(kind+tc.name, func(t *testing.T) {
				var root string
				if git {
					root = t.TempDir()
					gitLogical(t, root, "init", "--quiet")
				} else {
					root = nonGitLogicalTempDir(t)
				}
				markerPath := filepath.Join(root, anchorFilenameV3)
				legacy := []byte("{ \"name\" : \"Old label\" }\n")
				if err := os.WriteFile(markerPath, legacy, 0o600); err != nil {
					t.Fatal(err)
				}
				before, err := ResolveLogicalProject(t.Context(), root, nil)
				if before != (Project{}) || ProjectErrorCode(err) != "PROJECT_CONTEXT_REQUIRED" {
					t.Fatalf("name-only transition became authority before init: %+v, %v", before, err)
				}
				if raw, err := os.ReadFile(markerPath); err != nil || !bytes.Equal(raw, legacy) {
					t.Fatalf("offline read changed transition input: %q, %v", raw, err)
				}
				cachePath := filepath.Join(root, ".git", "engram-project.json")
				if _, err := os.Lstat(cachePath); !os.IsNotExist(err) {
					t.Fatalf("name-only transition pinned cache before init: %v", err)
				}
				project, err := InitLogicalProject(t.Context(), root, tc.override)
				if err != nil || !ValidProjectID(project.ProjectID) {
					t.Fatalf("explicit init did not issue canonical UUID: %+v, %v", project, err)
				}
				t.Logf("chronology: name-only offline read refused unchanged; Init(name=%q) returned %+v", tc.override, project)
				if project.Name != tc.want {
					t.Errorf("init label = %q, want %q", project.Name, tc.want)
				}
				marker := readLogicalProjectFile(t, markerPath)
				if marker != project {
					t.Errorf("marker differs from init result: %+v, result=%+v", marker, project)
				}
				if git {
					if cached := readLogicalProjectFile(t, cachePath); cached != project {
						t.Errorf("cache differs from init result: %+v, result=%+v", cached, project)
					}
				}
				if resolved, err := ResolveLogicalProject(t.Context(), root, nil); err != nil || resolved != project {
					t.Errorf("offline read after init = %+v, %v", resolved, err)
				}
			})
		}
	}
}

func readLogicalProjectFile(t *testing.T, path string) Project {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var project Project
	if err := json.Unmarshal(raw, &project); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProject(project); err != nil {
		t.Fatalf("invalid published project at %s: %+v, %v", path, project, err)
	}
	return project
}
