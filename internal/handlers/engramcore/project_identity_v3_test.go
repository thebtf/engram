package engramcore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/projectidentity"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

func TestProxyHandleTool_InvalidPortableMarkerFailsClosed(t *testing.T) {
	for name, raw := range map[string]string{
		"null":            `null`,
		"empty":           `{}`,
		"malformed":       `{`,
		"trailing-object": `{"project_id":"` + contractProjectID + `"} {}`,
		"legacy-selector": `{"project_id":"engram_67e398f8"}`,
		"nil-uuid":        `{"project_id":"00000000-0000-0000-0000-000000000000"}`,
		"uuid-type":       `{"project_id":42}`,
		"name-type":       `{"project_id":"` + contractProjectID + `","name":42}`,
		"control-name":    `{"project_id":"` + contractProjectID + `","name":"private\nname"}`,
		"unknown-field":   `{"project_id":"` + contractProjectID + `","credential":"private-credential"}`,
		"partial-v3":      `{"version":3,"name":"private-display-name"}`,
		"oversized":       strings.Repeat(" ", 4097),
	} {
		t.Run(name, func(t *testing.T) {
			srv := &mockEngramServer{callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
			_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
			writeContractProjectMarker(t, project.Cwd, contractProjectID)
			if _, err := mod.ProxyHandleTool(t.Context(), project, "recall", json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
			srv.mu.Lock()
			previous := srv.callReq
			srv.mu.Unlock()
			if err := os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := mod.ProxyHandleTool(t.Context(), project, "recall", json.RawMessage(`{}`))
			var refusal *module.ModuleError
			if !errors.As(err, &refusal) || refusal.Code != "PROJECT_IDENTITY_INVALID" {
				t.Fatalf("invalid marker error=%v, want PROJECT_IDENTITY_INVALID", err)
			}
			for _, private := range []string{project.Cwd, project.ID, contractProjectID, "private-display-name", "private-credential"} {
				if strings.Contains(err.Error(), private) {
					t.Fatal("marker refusal disclosed private context")
				}
			}
			srv.mu.Lock()
			defer srv.mu.Unlock()
			if srv.callReq != previous {
				t.Fatal("changed invalid marker reused a prior project or reached registration")
			}
		})
	}
}

func TestProxyHandleTool_NonRegularPortableMarkerFailsClosed(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "dangling-symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "fifo" && runtime.GOOS == "windows" {
				t.Skip("FIFO fixtures require POSIX")
			}
			srv := &mockEngramServer{}
			_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
			marker := filepath.Join(project.Cwd, ".engram-project")
			switch kind {
			case "directory":
				if err := os.Mkdir(marker, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink", "dangling-symlink":
				target := filepath.Join(t.TempDir(), "marker.json")
				if kind == "symlink" {
					if err := os.WriteFile(target, []byte(`{"project_id":"`+contractProjectID+`"}`), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, marker); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "fifo":
				if output, err := exec.Command("mkfifo", marker).CombinedOutput(); err != nil {
					t.Fatalf("FIFO fixture: %v: %s", err, output)
				}
			}
			_, err := mod.ProxyHandleTool(t.Context(), project, "recall", json.RawMessage(`{}`))
			var refusal *module.ModuleError
			if !errors.As(err, &refusal) || refusal.Code != "PROJECT_IDENTITY_INVALID" {
				t.Fatalf("nonregular marker error=%v, want PROJECT_IDENTITY_INVALID", err)
			}
			srv.mu.Lock()
			defer srv.mu.Unlock()
			if srv.callReq != nil {
				t.Fatal("nonregular marker reached backend scope resolution")
			}
		})
	}
}

func TestProxyHandleTool_MissingProjectDoesNotInventIdentity(t *testing.T) {
	for _, marker := range []string{"", `{"name":"private-display-name"}`} {
		t.Run(marker, func(t *testing.T) {
			srv := &mockEngramServer{}
			_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
			// TempDir may live inside another repository; fence off its identity and origin.
			command := exec.Command("git", "-C", project.Cwd, "init", "--quiet", "--template=")
			command.Env = projectidentity.RepositoryGitEnvironmentV3()
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("initialize unbound repository: %v: %s", err, output)
			}
			command = exec.Command("git", "-C", project.Cwd, "remote", "get-url", "origin")
			command.Env = projectidentity.RepositoryGitEnvironmentV3()
			if err := command.Run(); err == nil {
				t.Fatal("missing-project fixture has a bootstrap locator")
			}
			path := filepath.Join(project.Cwd, ".engram-project")
			if marker != "" {
				if err := os.WriteFile(path, []byte(marker), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := mod.ProxyHandleTool(t.Context(), project, "recall", json.RawMessage(`{}`))
			var refusal *module.ModuleError
			if !errors.As(err, &refusal) || refusal.Code != "PROJECT_CONTEXT_REQUIRED" {
				t.Fatalf("missing project error=%v, want PROJECT_CONTEXT_REQUIRED", err)
			}
			if marker == "" {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("opening a directory created a project: %v", err)
				}
			} else if after, err := os.ReadFile(path); err != nil || string(after) != marker {
				t.Fatal("missing project resolution replaced the name-only marker")
			}
			if _, err := os.Lstat(filepath.Join(project.Cwd, ".git", "engram-project.json")); !os.IsNotExist(err) {
				t.Fatalf("missing project resolution published a canonical cache: %v", err)
			}
			srv.mu.Lock()
			defer srv.mu.Unlock()
			if srv.callReq != nil {
				t.Fatal("missing project reached backend data or registration handlers")
			}
		})
	}
}

func TestProxyHandleTool_NestedProjectBoundaryPreservesRepository(t *testing.T) {
	srv := &mockEngramServer{callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	root := daemonV3Repository(t)
	writeContractProjectMarker(t, root, contractProjectID)
	project.Cwd = filepath.Join(root, "nested", "source with spaces")
	if err := os.MkdirAll(project.Cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(root, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	rootMarker, err := os.ReadFile(filepath.Join(root, ".engram-project"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{contractProjectID, otherContractProjectID, contractProjectID} {
		if expected == otherContractProjectID {
			writeContractProjectMarker(t, project.Cwd, otherContractProjectID)
		}
		if _, err := mod.ProxyHandleTool(t.Context(), project, "recall", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		srv.mu.Lock()
		request := srv.callReq
		srv.mu.Unlock()
		if request == nil || request.GetProject() != expected {
			t.Fatalf("selected nested project=%#v, want %s", request, expected)
		}
		if expected == otherContractProjectID {
			if err := os.Remove(filepath.Join(project.Cwd, ".engram-project")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if after, err := os.ReadFile(index); err != nil || string(after) != string(before) {
		t.Fatal("project resolution changed the repository index")
	}
	if after, err := os.ReadFile(filepath.Join(root, ".engram-project")); err != nil || string(after) != string(rootMarker) {
		t.Fatal("nested project resolution changed the root marker")
	}
	if _, err := os.Lstat(filepath.Join(project.Cwd, ".engram-project")); !os.IsNotExist(err) {
		t.Fatal("ordinary subdirectory resolution created a project boundary")
	}
	var cached struct {
		ProjectID string `json:"project_id"`
	}
	raw, err := os.ReadFile(filepath.Join(root, ".git", "engram-project.json"))
	if err != nil || json.Unmarshal(raw, &cached) != nil || cached.ProjectID != contractProjectID {
		t.Fatalf("nested project replaced the pinned repository UUID: %s: %v", raw, err)
	}
}

func TestProxyHandleTool_MarkerCacheConflictDoesNotDispatch(t *testing.T) {
	srv := &mockEngramServer{callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	project.Cwd = daemonV3Repository(t)
	writeContractProjectMarker(t, project.Cwd, contractProjectID)
	if _, err := mod.ProxyHandleTool(t.Context(), project, "recall", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	previous := srv.callReq
	srv.mu.Unlock()
	writeContractProjectMarker(t, project.Cwd, otherContractProjectID)
	_, err := mod.ProxyHandleTool(t.Context(), project, "recall", json.RawMessage(`{}`))
	var refusal *module.ModuleError
	if !errors.As(err, &refusal) || refusal.Code != "PROJECT_IDENTITY_CONFLICT" {
		t.Fatalf("marker/cache conflict error=%v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.callReq != previous {
		t.Fatal("conflicting project identity reached backend data or registration handlers")
	}
	var cached struct {
		ProjectID string `json:"project_id"`
	}
	raw, err := os.ReadFile(filepath.Join(project.Cwd, ".git", "engram-project.json"))
	if err != nil || json.Unmarshal(raw, &cached) != nil || cached.ProjectID != contractProjectID {
		t.Fatal("conflicting marker overwrote the pinned UUID")
	}
}

func TestProxyHandleTool_CancelledProjectCallDoesNotDispatch(t *testing.T) {
	srv := &mockEngramServer{callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	writeContractProjectMarker(t, project.Cwd, contractProjectID)
	if _, err := mod.ProxyHandleTool(t.Context(), project, "recall", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	previous := srv.callReq
	srv.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := mod.ProxyHandleTool(ctx, project, "recall", json.RawMessage(`{}`)); err == nil {
		t.Fatal("cancelled project call succeeded")
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.callReq != previous {
		t.Fatal("cancelled project call reached backend data or registration handlers")
	}
}

// Retained for native Source/Checkout registration tests, not descriptor discovery.
func daemonV3Repository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"remote", "add", "origin", "https://git.example.test/Platform/Daemon.git"},
		{"add", ".engram-project"},
	} {
		if args[0] == "add" {
			if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte("{\"version\":3,\"project_id\":\"22222222-2222-4222-8222-222222222222\",\"name\":\"daemon-v3\",\"scope\":\"repository\"}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return root
}
