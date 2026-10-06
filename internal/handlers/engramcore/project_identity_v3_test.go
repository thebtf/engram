package engramcore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/proxy"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const daemonV3CanonicalProject = "11111111-1111-4111-8111-111111111111"

func TestLegacySelectedScopeAcceptsFilesystemAliases(t *testing.T) {
	root := daemonV3Repository(t)
	selectedRoot := strings.ToUpper(root)
	if runtime.GOOS != "windows" {
		selectedRoot = filepath.Join(t.TempDir(), "repository-alias")
		if err := os.Symlink(root, selectedRoot); err != nil {
			t.Fatal(err)
		}
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	selectedInfo, err := os.Stat(selectedRoot)
	if err != nil || !os.SameFile(rootInfo, selectedInfo) {
		t.Fatalf("fixture alias is not the same directory: %v", err)
	}
	for _, suffix := range []string{"", "nested"} {
		t.Run(suffix, func(t *testing.T) {
			selected := filepath.Join(selectedRoot, suffix)
			if err := os.MkdirAll(selected, 0o700); err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command("git", "-C", selected, "rev-parse", "--show-toplevel").Output()
			if err != nil {
				t.Fatal(err)
			}
			gitRoot := strings.TrimSpace(string(output))
			if filepath.Clean(selectedRoot) == filepath.Clean(gitRoot) {
				t.Fatal("fixture did not produce distinct Git and selected root spellings")
			}
			if !legacySelectedScopeUnchanged(selected, gitRoot) {
				t.Fatal("same filesystem repository was refused for a different path spelling")
			}
		})
	}
	if runtime.GOOS != "windows" {
		srv := &mockEngramServer{initResp: &pb.InitializeResponse{}, callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
		_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
		project.Cwd = selectedRoot
		mod.v3ClientInstanceID = "fixture-daemon-install"
		if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := mod.ProxyTools(context.Background(), project); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(selectedRoot); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "nested"), selectedRoot); err != nil {
			t.Fatal(err)
		}
		if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if srv.callReq.GetProjectIdentity().GetRelativePath() != "nested/" {
			t.Fatal("retargeted filesystem alias reused the previous selected prefix")
		}
	}
}

func TestProxyLegacyColdScopeRefusesUntrackingDuringAdmission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real Git interposition requires a POSIX executable script")
	}
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{}, callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	if err := os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\n\"$ENGRAM_TEST_REAL_GIT\" \"$@\"\nstatus=$?\nif [ \"$3\" = ls-files ] && [ \"$status\" -eq 0 ]; then\n  \"$ENGRAM_TEST_REAL_GIT\" -C \"$2\" update-index --force-remove -- .engram-project || exit $?\nfi\nexit \"$status\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_TEST_REAL_GIT", realGit)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := mod.ProxyTools(context.Background(), project); err == nil {
		t.Fatal("marker untracked after admission reached legacy identity")
	}
	if output, err := exec.Command(realGit, "-C", project.Cwd, "ls-files", "--", ".engram-project").Output(); err != nil || len(output) != 0 {
		t.Fatalf("real Git untracking did not occur: %v: %s", err, output)
	}
	if srv.initReq != nil {
		t.Fatal("untracked marker reached Initialize")
	}
	key := cacheKey(project)
	if _, ok := mod.cache.legacy[key]; ok {
		t.Fatal("unstable admission retained legacy cache")
	}
	if _, ok := mod.cache.identities.Load(key); ok {
		t.Fatal("unstable admission retained V2 identity")
	}
	for range 2 {
		if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err == nil {
			t.Fatal("untracked marker reused legacy authority")
		}
		if srv.callReq != nil {
			t.Fatal("untracked marker reached memory scope")
		}
	}
}

func TestProxyLegacyColdScopeRefusesConfigTransition(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{}, callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	if err := os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	original := resolveLegacyGitIdentity
	t.Cleanup(func() { resolveLegacyGitIdentity = original })
	changed := false
	resolveLegacyGitIdentity = func(ctx context.Context, cwd, name string) (string, proxy.ProjectIdentityV2, error) {
		slug, identity, err := original(ctx, cwd, name)
		if err == nil && !changed {
			changed = true
			if output, err := exec.Command("git", "-C", cwd, "remote", "set-url", "origin", "https://git.example.test/new/repo.git").CombinedOutput(); err != nil {
				t.Fatalf("transition config: %v: %s", err, output)
			}
		}
		return slug, identity, err
	}
	if _, err := mod.ProxyTools(context.Background(), project); err == nil {
		t.Fatal("old cold identity accepted under new config fingerprints")
	}
	if srv.initReq != nil {
		t.Fatal("unstable cold identity reached Initialize")
	}
	for range 2 {
		if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if srv.callReq.GetProjectIdentity().GetGitRemote() != "https://git.example.test/new/repo.git" {
			t.Fatal("subsequent request reused stale origin")
		}
	}
}

func TestProxyLegacyWarmScopeRejectsNearerRepository(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{}, callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	root := daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	nearer := filepath.Join(root, "packages")
	project.Cwd = filepath.Join(nearer, "app")
	if err := os.MkdirAll(project.Cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.ProxyTools(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", nearer, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("nearer repo: %v: %s", err, output)
	}
	for range 2 {
		srv.callReq = nil
		if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err == nil {
			t.Fatal("nearer repository reused old scoped authority")
		}
		if srv.callReq != nil {
			t.Fatal("nearer repository reached old memory scope")
		}
	}
	after, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil || string(before) != string(after) {
		t.Fatal("topology transition mutated old index")
	}
}

func TestProxyLegacyWarmScopeRefreshesIncludedOrigin(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{}, callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	if err := os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	included := filepath.Join(project.Cwd, "origin.gitconfig")
	for _, args := range [][]string{{"config", "--unset", "remote.origin.url"}, {"config", "include.path", included}} {
		if output, err := exec.Command("git", append([]string{"-C", project.Cwd}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("include config: %v: %s", err, output)
		}
	}
	writeOrigin := func(remote string) {
		if err := os.WriteFile(included, []byte("[remote \"origin\"]\nurl = "+remote+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeOrigin("https://github.com/thebtf/engram.git")
	if _, err := mod.ProxyTools(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	writeOrigin("https://git.example.test/new/repo.git")
	for range 2 {
		if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if srv.callReq.GetProjectIdentity().GetGitRemote() != "https://git.example.test/new/repo.git" {
			t.Fatal("included config reused old scope")
		}
	}
}

func TestProxyLegacyNestedScopeIsReadOnly(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{}, callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	root := daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	project.Cwd = filepath.Join(root, "nested")
	if err := os.Mkdir(project.Cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := mod.ProxyTools(context.Background(), project); err != nil {
			t.Fatal(err)
		}
		if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if srv.callReq.GetProjectIdentity().GetRelativePath() != "nested/" {
			t.Fatal("nested identity dropped its selected prefix")
		}
	}
	after, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil || string(before) != string(after) {
		t.Fatal("memory requests mutated fixture Git index")
	}
	for _, marker := range []string{".engram-project", ".engram-project-v2.json"} {
		if _, err := os.Stat(filepath.Join(project.Cwd, marker)); !os.IsNotExist(err) {
			t.Fatal("memory requests created a selected-directory marker")
		}
	}
}

func TestProxyLegacyWarmScopeNeedsNoGitAndRechecksMarker(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{}, callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	marker := filepath.Join(project.Cwd, ".engram-project")
	if err := os.WriteFile(marker, []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.ProxyTools(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("warm identity still requires Git: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := mod.ProxyHandleTool(cancelled, project, "recall", json.RawMessage(`{}`)); err == nil {
		t.Fatal("cancelled warm request accepted")
	}
	if err := os.WriteFile(marker, []byte(`{"version":3,"name":"partial"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err == nil {
		t.Fatal("changed marker reused legacy cache")
	}
	if err := os.WriteFile(marker, []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", originalPath)
	if _, err := mod.ProxyTools(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	mod.OnProjectRemoved(project.ID)
	t.Setenv("PATH", t.TempDir())
	if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err == nil {
		t.Fatal("Forget retained a warm legacy classification")
	}
}

func TestProxyLegacyNameOnlyWorkspaceUsesExistingV2Scope(t *testing.T) {
	srv := &mockEngramServer{
		initResp: &pb.InitializeResponse{Tools: []*pb.ToolDefinition{{Name: "recall"}}},
		callResp: &pb.CallToolResponse{ContentJson: []byte(`[]`)},
	}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	if err := os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", project.Cwd, "remote", "set-url", "origin", "https://github.com/thebtf/engram.git").CombinedOutput(); err != nil {
		t.Fatalf("set fixture origin: %v: %s", err, output)
	}
	before, err := os.ReadFile(filepath.Join(project.Cwd, ".engram-project"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mod.ProxyTools(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if srv.initReq.GetProject() != "67e398f8" || srv.callReq.GetProject() != "67e398f8" {
		t.Fatalf("legacy scope changed: initialize=%v call=%v", srv.initReq, srv.callReq)
	}
	for _, identity := range []*pb.ProjectIdentityV2{srv.initReq.GetProjectIdentity(), srv.callReq.GetProjectIdentity()} {
		if identity.GetVersion() != 2 || identity.GetGitRemote() != "https://github.com/thebtf/engram.git" || identity.GetRelativePath() != "" {
			t.Fatalf("legacy metadata changed: %v", identity)
		}
	}
	if srv.initReq.GetProjectIdentityV3() != nil || srv.callReq.GetProjectIdentityV3() != nil {
		t.Fatal("legacy route carried V3 authority")
	}
	after, err := os.ReadFile(filepath.Join(project.Cwd, ".engram-project"))
	if err != nil || string(before) != string(after) {
		t.Fatal("legacy marker was mutated")
	}
	if _, err := os.Stat(filepath.Join(project.Cwd, ".engram-project-v2.json")); !os.IsNotExist(err) {
		t.Fatal("legacy dispatch minted a non-Git anchor")
	}
}

func TestProxyLegacyNameOnlyWorkspaceRejectsInvalidAnchors(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `{"name":""}`, `{"name":42}`, `{"name":" padded "}`,
		`{"name":"line\nfeed"}`, `{"name":"engram","extra":true}`,
		`{"name":"engram","version":2}`, `{"name":"engram","version":3}`,
		`{"name":"engram","project_id":"invalid","scope":"repository","version":3}`,
		`{"name":"engram","project_id":"22222222-2222-4222-8222-222222222222","scope":"invalid","version":3}`,
		`{"name":"engram","name":"other"}`, `{"name":"engram"} {}`, `{`,
	} {
		t.Run(raw, func(t *testing.T) {
			srv := &mockEngramServer{}
			_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
			mod.v3ClientInstanceID = "fixture-daemon-install"
			project.Cwd = daemonV3Repository(t)
			if err := os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := mod.ProxyTools(context.Background(), project); err == nil {
				t.Fatal("invalid discovery accepted")
			}
			if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err == nil {
				t.Fatal("invalid call accepted")
			}
			if srv.initReq != nil || srv.callReq != nil {
				t.Fatal("invalid anchor reached transport")
			}
		})
	}
}

func TestProxyLegacyNameOnlyWorkspaceRequiresTrackedGitScope(t *testing.T) {
	for _, boundary := range []string{"missing", "untracked", "missing-origin", "conflicting-directory-anchor"} {
		t.Run(boundary, func(t *testing.T) {
			srv := &mockEngramServer{}
			_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
			mod.v3ClientInstanceID = "fixture-daemon-install"
			project.Cwd = t.TempDir()
			for _, args := range [][]string{{"init", "--quiet"}, {"remote", "add", "origin", "https://github.com/thebtf/engram.git"}} {
				if output, err := exec.Command("git", append([]string{"-C", project.Cwd}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("fixture git: %v: %s", err, output)
				}
			}
			if boundary != "missing" {
				if err := os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), []byte(`{"name":"engram"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if boundary != "missing" && boundary != "untracked" {
				if output, err := exec.Command("git", "-C", project.Cwd, "add", ".engram-project").CombinedOutput(); err != nil {
					t.Fatalf("track marker: %v: %s", err, output)
				}
			}
			if boundary == "missing-origin" {
				if output, err := exec.Command("git", "-C", project.Cwd, "remote", "remove", "origin").CombinedOutput(); err != nil {
					t.Fatalf("remove fixture origin: %v: %s", err, output)
				}
			}
			if boundary == "conflicting-directory-anchor" {
				project.Cwd = filepath.Join(project.Cwd, "nested")
				if err := os.Mkdir(project.Cwd, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), []byte(`{"version":3,"name":"partial"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// Existing unborn-repository discovery may remain authority-free;
			// no missing or invalid scope may reach a V2 memory request.
			_, _ = mod.ProxyTools(context.Background(), project)
			if srv.initReq != nil && (srv.initReq.GetProject() != "" || srv.initReq.GetProjectIdentity() != nil) {
				t.Fatal("invalid scope reached V2 discovery")
			}
			if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err == nil {
				t.Fatal("invalid scope reached memory")
			}
			if srv.callReq != nil {
				t.Fatal("invalid scope reached CallTool")
			}
			if _, err := os.Stat(filepath.Join(project.Cwd, ".engram-project-v2.json")); !os.IsNotExist(err) {
				t.Fatal("invalid scope minted a V2 anchor")
			}
		})
	}
}

func TestProxyV3DescriptorForwardsWithoutV2Fallback(t *testing.T) {
	srv := &mockEngramServer{
		initResp: &pb.InitializeResponse{
			Tools:               []*pb.ToolDefinition{{Name: "recall", Description: "recall"}},
			CanonicalProject:    daemonV3CanonicalProject,
			ProjectResolutionV3: resolvedV3Response(),
		},
		callResp: &pb.CallToolResponse{
			ContentJson:         []byte(`[]`),
			CanonicalProject:    daemonV3CanonicalProject,
			ProjectResolutionV3: resolvedV3Response(),
		},
	}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"

	if _, err := mod.ProxyTools(context.Background(), project); err != nil {
		t.Fatalf("V3 Initialize: %v", err)
	}
	assertV3InitializeRequest(t, srv.initReq)

	if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("V3 CallTool: %v", err)
	}
	assertV3CallRequest(t, srv.callReq)
}

func TestProxyV3UnbornRepositoryAllowsOnlyUnscopedUCIDiscovery(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{Tools: []*pb.ToolDefinition{
		{Name: "recall", Description: "scoped memory tool"},
		{Name: "codebase_context", Description: "select an authorized checkout"},
	}}}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)
	project.Cwd = daemonV3UnbornRepository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"

	tools, err := mod.ProxyTools(context.Background(), project)
	if err != nil {
		t.Fatalf("unborn V3 discovery: %v", err)
	}
	if srv.initReq == nil || srv.initReq.GetProjectIdentityV3() != nil || srv.initReq.GetProject() != "" || srv.initReq.GetProjectIdentity() != nil {
		t.Fatalf("unborn discovery invented project authority: %#v", srv.initReq)
	}
	if len(tools) != 1 || tools[0].Name != "codebase_context" {
		t.Fatalf("unborn discovery tools=%#v, want only UCI checkout selection", tools)
	}
	_, err = mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`))
	var moduleErr *module.ModuleError
	if !errors.As(err, &moduleErr) || moduleErr.Code != "PROJECT_ANCHOR_INVALID" || srv.callReq != nil {
		t.Fatalf("unborn scoped proxy error=%v call=%#v, want local anchor refusal", err, srv.callReq)
	}

	anchorless := project
	anchorless.Cwd = t.TempDir()
	tools, err = mod.ProxyTools(context.Background(), anchorless)
	if err != nil || len(tools) != 1 || tools[0].Name != "codebase_context" {
		t.Fatalf("anchorless discovery tools=%#v error=%v, want only UCI checkout selection", tools, err)
	}
	_, err = mod.ProxyHandleTool(context.Background(), anchorless, "recall", json.RawMessage(`{}`))
	if !errors.As(err, &moduleErr) || moduleErr.Code != "PROJECT_ANCHOR_INVALID" {
		t.Fatalf("anchorless scoped proxy error=%v, want PROJECT_ANCHOR_INVALID", err)
	}
}

func TestProxyV3CarriesStableDaemonComparisonMetadata(t *testing.T) {
	srv := &mockEngramServer{
		initResp:     &pb.InitializeResponse{CanonicalProject: daemonV3CanonicalProject, ProjectResolutionV3: resolvedV3Response()},
		callResp:     &pb.CallToolResponse{ContentJson: []byte(`[]`), CanonicalProject: daemonV3CanonicalProject, ProjectResolutionV3: resolvedV3Response()},
		registerResp: &pb.RegisterProjectIdentityV3Response{ProjectResolutionV3: resolvedV3Response()},
	}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.Cwd = daemonV3Repository(t)
	if _, err := mod.ProxyTools(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := mod.HandleTool(context.Background(), project, projectIdentityV3RegistrationTool, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	for _, received := range []metadata.MD{srv.initMetadata, srv.callMetadata, srv.registerMetadata} {
		assertDaemonComparisonMetadata(t, received)
	}

	first := daemonComparisonContextV3(context.Background())
	second := daemonComparisonContextV3(first)
	firstMetadata, _ := metadata.FromOutgoingContext(first)
	secondMetadata, _ := metadata.FromOutgoingContext(second)
	if !reflect.DeepEqual(firstMetadata.Get("x-request-id"), secondMetadata.Get("x-request-id")) {
		t.Fatalf("replay request IDs differ: first=%q second=%q", firstMetadata.Get("x-request-id"), secondMetadata.Get("x-request-id"))
	}
}

func assertDaemonComparisonMetadata(t *testing.T, received metadata.MD) {
	t.Helper()
	if got := received.Get("x-engram-project-identity-adapter"); len(got) != 1 || got[0] != "daemon" {
		t.Fatalf("adapter metadata=%q", got)
	}
	requestIDs := received.Get("x-request-id")
	if len(requestIDs) != 1 {
		t.Fatalf("request IDs=%q", requestIDs)
	}
	if _, err := uuid.Parse(requestIDs[0]); err != nil {
		t.Fatalf("request ID=%q err=%v", requestIDs[0], err)
	}
}

func TestProxyV3RefusalClearsCompatibilityCacheAndExposesOnlyTypedOutcome(t *testing.T) {
	srv := &mockEngramServer{callErr: typedV3Refusal(t, "PROJECT_ONBOARDING_REQUIRED")}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-survive-refusal")

	_, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`))
	var moduleErr *module.ModuleError
	if !errors.As(err, &moduleErr) {
		t.Fatalf("error=%v, want typed module error", err)
	}
	if moduleErr.Code != "PROJECT_ONBOARDING_REQUIRED" || moduleErr.Message != "project identity resolution refused" {
		t.Fatalf("typed refusal=%#v", moduleErr)
	}
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("V3 refusal retained a compatibility cache entry")
	}
	if strings.Contains(err.Error(), project.ID) || strings.Contains(err.Error(), project.Cwd) || strings.Contains(err.Error(), "candidate") || strings.Contains(err.Error(), "credential") {
		t.Fatalf("refusal leaked private identity material: %v", err)
	}
	assertV3CallRequest(t, srv.callReq)
}

func TestV3OnboardingKeepsStaticRegistrationVisibleAndResumesProxy(t *testing.T) {
	srv := &mockEngramServer{
		initErr:      typedV3Refusal(t, "PROJECT_ONBOARDING_REQUIRED"),
		registerResp: &pb.RegisterProjectIdentityV3Response{ProjectResolutionV3: resolvedV3Response()},
		callResp: &pb.CallToolResponse{
			ContentJson:         []byte(`[]`),
			CanonicalProject:    daemonV3CanonicalProject,
			ProjectResolutionV3: resolvedV3Response(),
		},
	}
	grpcAddr := startMockGRPC(t, srv)
	dispatcher, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-survive-refusal")

	invoke := func(request []byte) ([]byte, error) {
		return dispatcher.HandleRequest(context.Background(), project, request)
	}
	assertCache := func() bool {
		return mod.cache.HasEntry(project.ID)
	}
	assertV3OnboardingStaticRegistration(t, srv, project.ID, project.Cwd, assertCache, invoke)
	assertV3OnboardingProxy(t, srv, assertCache, invoke)
}

func assertV3OnboardingStaticRegistration(t *testing.T, srv *mockEngramServer, projectID, projectCWD string, hasCache func() bool, invoke func([]byte) ([]byte, error)) {
	before, err := invoke(jsonrpcListReq(1))
	if err != nil {
		t.Fatalf("onboarding tools/list: %v", err)
	}
	assertOnlyRegistrationTool(t, before)
	if hasCache() {
		t.Fatal("onboarding refusal retained a compatibility cache entry")
	}
	if strings.Contains(string(before), projectID) || strings.Contains(string(before), projectCWD) {
		t.Fatalf("onboarding tools/list leaked local identity material: %s", before)
	}
	for id := 2; id <= 3; id++ {
		registration, err := invoke(jsonrpcCallReq(id, "project_identity.register_v3"))
		if err != nil {
			t.Fatalf("registration %d: %v", id, err)
		}
		assertRegistrationResolved(t, registration)
	}
	srv.mu.Lock()
	srv.initErr = nil
	srv.initResp = &pb.InitializeResponse{
		Tools:               []*pb.ToolDefinition{{Name: "recall", Description: "recall"}},
		CanonicalProject:    daemonV3CanonicalProject,
		ProjectResolutionV3: resolvedV3Response(),
	}
	registerCalls := srv.registerCalls
	registerReq := srv.registerReq
	initCalls := srv.initCalls
	callReq := srv.callReq
	srv.mu.Unlock()
	if initCalls != 1 || callReq != nil {
		t.Fatalf("registration bypassed static dispatch: Initialize calls=%d CallTool request=%#v", initCalls, callReq)
	}
	if registerCalls != 2 {
		t.Fatalf("registration calls=%d, want 2 to preserve server idempotency", registerCalls)
	}
	if registerReq == nil || registerReq.GetProjectIdentityV3() == nil {
		t.Fatalf("registration request=%#v, want descriptor-only request", registerReq)
	}
	assertV3Descriptor(t, registerReq.GetProjectIdentityV3())
	if strings.Contains(registerReq.String(), projectID) || strings.Contains(registerReq.String(), projectCWD) {
		t.Fatalf("registration request leaked raw mux identity: %s", registerReq)
	}
}

func assertV3OnboardingProxy(t *testing.T, srv *mockEngramServer, hasCache func() bool, invoke func([]byte) ([]byte, error)) {
	after, err := invoke(jsonrpcListReq(4))
	if err != nil {
		t.Fatalf("post-registration tools/list: %v", err)
	}
	assertRegistrationAndProxyTool(t, after, "recall")
	if _, err := invoke(jsonrpcCallReq(5, "recall")); err != nil {
		t.Fatalf("post-registration V3 tool: %v", err)
	}
	assertV3InitializeRequest(t, srv.initReq)
	assertV3CallRequest(t, srv.callReq)
	if hasCache() {
		t.Fatal("V3 registration or normal proxy route populated a compatibility cache entry")
	}
}

func TestV3RegistrationRejectsNonEmptyOrNullArgumentsWithoutCallingGRPC(t *testing.T) {
	srv := &mockEngramServer{}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-reach-registration")

	for _, args := range []json.RawMessage{
		json.RawMessage(`{"project_key":"must-not-be-accepted"}`),
		json.RawMessage(`null`),
	} {
		_, err := mod.HandleTool(context.Background(), project, projectIdentityV3RegistrationTool, args)
		var moduleErr *module.ModuleError
		if !errors.As(err, &moduleErr) || moduleErr.Code != "tool_input_invalid" {
			t.Fatalf("registration input error=%v, want safe typed refusal", err)
		}
		if strings.Contains(err.Error(), project.ID) || strings.Contains(err.Error(), project.Cwd) || strings.Contains(err.Error(), "must-not-be-accepted") {
			t.Fatalf("registration argument refusal leaked private input: %v", err)
		}
	}
	srv.mu.Lock()
	registerCalls := srv.registerCalls
	srv.mu.Unlock()
	if registerCalls != 0 {
		t.Fatalf("registration RPC calls=%d, want 0", registerCalls)
	}
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("registration argument refusal retained a compatibility cache entry")
	}
}

func TestV3NonOnboardingRefusalFailsClosedWithoutProxyTools(t *testing.T) {
	srv := &mockEngramServer{initErr: typedV3Refusal(t, "PROJECT_SCOPE_MISMATCH")}
	grpcAddr := startMockGRPC(t, srv)
	dispatcher, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-survive-refusal")

	response, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcListReq(1))
	if err != nil {
		t.Fatalf("non-onboarding tools/list: %v", err)
	}
	assertToolsListServiceUnavailable(t, response)
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("non-onboarding V3 refusal retained a compatibility cache entry")
	}
}

func TestV3MixedOnboardingRefusalFailsClosedWithoutProxyTools(t *testing.T) {
	st, err := status.New(codes.FailedPrecondition, "project identity resolution refused").WithDetails(
		&errdetails.ErrorInfo{
			Domain: "engram.project_identity.v3",
			Reason: string(projectidentity.ProjectOnboardingRequiredOutcomeV3),
		},
		&errdetails.ErrorInfo{
			Domain: "engram.project_identity.v3",
			Reason: string(projectidentity.ProjectScopeMismatchOutcomeV3),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	srv := &mockEngramServer{initErr: st.Err()}
	grpcAddr := startMockGRPC(t, srv)
	dispatcher, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-survive-refusal")

	response, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcListReq(1))
	if err != nil {
		t.Fatalf("mixed onboarding tools/list: %v", err)
	}
	assertToolsListServiceUnavailable(t, response)
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("mixed V3 refusal retained a compatibility cache entry")
	}
}

func TestV2DoesNotExposeRegistrationOrFallBackToV2(t *testing.T) {
	srv := &mockEngramServer{}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)

	if tools := mod.Tools(); len(tools) != 0 {
		t.Fatalf("V2 static tools=%v, want unchanged surface", tools)
	}
	_, err := mod.HandleTool(context.Background(), project, projectIdentityV3RegistrationTool, json.RawMessage(`{}`))
	var moduleErr *module.ModuleError
	if !errors.As(err, &moduleErr) || moduleErr.Code != "PROJECT_DESCRIPTOR_UNSUPPORTED" {
		t.Fatalf("V2 registration error=%v, want V3-only refusal", err)
	}
	srv.mu.Lock()
	registerCalls := srv.registerCalls
	srv.mu.Unlock()
	if registerCalls != 0 {
		t.Fatalf("V2 registration RPC calls=%d, want 0", registerCalls)
	}
}

func TestV2DynamicRegistrationToolIsHiddenAndNeverProxied(t *testing.T) {
	srv := &mockEngramServer{initResp: &pb.InitializeResponse{Tools: []*pb.ToolDefinition{
		{Name: projectIdentityV3RegistrationTool},
		{Name: "recall"},
	}}}
	grpcAddr := startMockGRPC(t, srv)
	dispatcher, mod, project := buildContractDispatcher(t, grpcAddr)
	mod.cache.ForceCacheEntry(project, "cached-v2-selector")

	listed, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcListReq(1))
	if err != nil {
		t.Fatalf("V2 tools/list: %v", err)
	}
	var tools struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(listed, &tools); err != nil {
		t.Fatalf("unmarshal V2 tools/list: %v", err)
	}
	if len(tools.Result.Tools) != 1 || tools.Result.Tools[0].Name != "recall" {
		t.Fatalf("V2 tools/list=%s, want only backend recall", listed)
	}

	response, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcCallReq(2, projectIdentityV3RegistrationTool))
	if err != nil {
		t.Fatalf("reserved V2 tools/call: %v", err)
	}
	if !strings.Contains(string(response), "PROJECT_DESCRIPTOR_UNSUPPORTED") {
		t.Fatalf("reserved V2 tools/call=%s, want safe V3-only refusal", response)
	}
	srv.mu.Lock()
	callReq := srv.callReq
	srv.mu.Unlock()
	if callReq != nil {
		t.Fatalf("reserved V2 tool reached backend CallTool: %#v", callReq)
	}
	if !mod.cache.HasEntry(project.ID) {
		t.Fatal("reserved V2 tool mutated the compatibility selector cache")
	}
}

type v3RegistrationFailureCase struct {
	name       string
	resolution *pb.ProjectResolutionResultV3
	mustHide   []string
}

func TestV3RegistrationResponseValidationFailsClosed(t *testing.T) {
	projectKey := daemonV3CanonicalProject
	repositoryScope := "repository"
	directoryScope := "directory"
	for _, test := range v3RegistrationFailureCases(projectKey, repositoryScope, directoryScope) {
		t.Run(test.name, func(t *testing.T) {
			runV3RegistrationFailureCase(t, test)
		})
	}
}

func v3RegistrationFailureCases(projectKey, repositoryScope, directoryScope string) []v3RegistrationFailureCase {
	return []v3RegistrationFailureCase{
		{name: "nil resolution"},
		{name: "refusal with authority", resolution: &pb.ProjectResolutionResultV3{Outcome: pb.ProjectResolutionOutcomeV3_PROJECT_SCOPE_MISMATCH, Correlation: "refusal-correlation", ProjectKey: &projectKey, ResolvedScope: &repositoryScope}, mustHide: []string{projectKey}},
		{name: "invalid canonical key", resolution: &pb.ProjectResolutionResultV3{Outcome: pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED, Correlation: "invalid-key-correlation", ProjectKey: stringPtr("private-client-asserted-key"), ResolvedScope: &repositoryScope}, mustHide: []string{"private-client-asserted-key"}},
		{name: "descriptor scope mismatch", resolution: &pb.ProjectResolutionResultV3{Outcome: pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED, Correlation: "scope-mismatch-correlation", ProjectKey: &projectKey, ResolvedScope: &directoryScope}, mustHide: []string{projectKey}},
		{name: "unsafe correlation", resolution: &pb.ProjectResolutionResultV3{Outcome: pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED, Correlation: "private/path/correlation", ProjectKey: &projectKey, ResolvedScope: &repositoryScope}, mustHide: []string{"private/path/correlation", projectKey}},
		{name: "resolved outcome with redirect reference", resolution: &pb.ProjectResolutionResultV3{Outcome: pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED, Correlation: "resolved-redirect-correlation", ProjectKey: &projectKey, ResolvedScope: &repositoryScope, RedirectReference: stringPtr("redirect-audit-17")}, mustHide: []string{"redirect-audit-17", projectKey}},
		{name: "redirected outcome without reference", resolution: &pb.ProjectResolutionResultV3{Outcome: pb.ProjectResolutionOutcomeV3_PROJECT_REDIRECTED, Correlation: "redirect-correlation", ProjectKey: &projectKey, ResolvedScope: &repositoryScope}, mustHide: []string{projectKey}},
	}
}

func runV3RegistrationFailureCase(t *testing.T, test v3RegistrationFailureCase) {
	srv := &mockEngramServer{registerResp: &pb.RegisterProjectIdentityV3Response{ProjectResolutionV3: test.resolution}}
	grpcAddr := startMockGRPC(t, srv)
	dispatcher, mod, project := buildV3ContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.cache.ForceCacheEntry(project, "raw-slug-must-not-survive-invalid-registration")
	response, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcCallReq(1, projectIdentityV3RegistrationTool))
	if err != nil {
		t.Fatalf("registration: %v", err)
	}
	if !strings.Contains(string(response), `"isError":true`) || !strings.Contains(string(response), "PROJECT_RESOLUTION_UNAVAILABLE") {
		t.Fatalf("registration response=%s, want safe typed error", response)
	}
	for _, private := range append(test.mustHide, project.ID, project.Cwd) {
		if strings.Contains(string(response), private) {
			t.Fatalf("invalid registration leaked %q: %s", private, response)
		}
	}
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("invalid registration retained a compatibility cache entry")
	}
}

func TestV3RegistrationValidResponseSerializesResolution(t *testing.T) {
	projectKey := daemonV3CanonicalProject
	repositoryScope := "repository"
	for _, test := range []struct {
		name       string
		resolution *pb.ProjectResolutionResultV3
		outcome    string
	}{
		{name: "resolved", resolution: resolvedV3Response(), outcome: "PROJECT_RESOLVED"},
		{
			name: "redirected",
			resolution: &pb.ProjectResolutionResultV3{
				Outcome:           pb.ProjectResolutionOutcomeV3_PROJECT_REDIRECTED,
				Correlation:       "redirected-correlation",
				ProjectKey:        &projectKey,
				ResolvedScope:     &repositoryScope,
				RedirectReference: stringPtr("redirect-audit-17"),
			},
			outcome: "PROJECT_REDIRECTED",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := &mockEngramServer{registerResp: &pb.RegisterProjectIdentityV3Response{ProjectResolutionV3: test.resolution}}
			grpcAddr := startMockGRPC(t, srv)
			dispatcher, mod, project := buildV3ContractDispatcher(t, grpcAddr)
			project.Cwd = daemonV3Repository(t)
			response, err := dispatcher.HandleRequest(context.Background(), project, jsonrpcCallReq(1, projectIdentityV3RegistrationTool))
			if err != nil {
				t.Fatalf("registration: %v", err)
			}
			if !strings.Contains(string(response), `"isError":false`) || !strings.Contains(string(response), test.outcome) {
				t.Fatalf("registration response=%s, want %s success", response, test.outcome)
			}
			if mod.cache.HasEntry(project.ID) {
				t.Fatal("valid V3 registration populated a compatibility cache entry")
			}
		})
	}
}

func stringPtr(value string) *string {
	return &value
}

func assertOnlyRegistrationTool(t *testing.T, response []byte) {
	t.Helper()
	var got struct {
		Error  any `json:"error"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &got); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	if got.Error != nil || len(got.Result.Tools) != 1 || got.Result.Tools[0].Name != "project_identity.register_v3" {
		t.Fatalf("onboarding tools/list=%s, want static registration tool only", response)
	}
}

func assertRegistrationAndProxyTool(t *testing.T, response []byte, proxyName string) {
	t.Helper()
	var got struct {
		Error  any `json:"error"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &got); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	if got.Error != nil || len(got.Result.Tools) != 2 || got.Result.Tools[0].Name != "project_identity.register_v3" || got.Result.Tools[1].Name != proxyName {
		t.Fatalf("post-registration tools/list=%s, want static registration and proxy tool", response)
	}
}

func assertRegistrationResolved(t *testing.T, response []byte) {
	t.Helper()
	if !strings.Contains(string(response), `"isError":false`) || !strings.Contains(string(response), `"PROJECT_RESOLVED"`) {
		t.Fatalf("registration response=%s, want typed resolved outcome", response)
	}
}

func TestProxyV3RejectsCanonicalProjectNotIssuedByResolution(t *testing.T) {
	srv := &mockEngramServer{callResp: &pb.CallToolResponse{
		ContentJson:         []byte(`[]`),
		CanonicalProject:    "client-selected-project-must-not-enter-context",
		ProjectResolutionV3: resolvedV3Response(),
	}}
	grpcAddr := startMockGRPC(t, srv)
	_, mod, project := buildContractDispatcher(t, grpcAddr)
	project.ID = "raw-mux-project-must-not-be-forwarded"
	project.Cwd = daemonV3Repository(t)
	mod.v3ClientInstanceID = "fixture-daemon-install"

	_, err := mod.ProxyHandleTool(context.Background(), project, "recall", json.RawMessage(`{}`))
	var moduleErr *module.ModuleError
	if !errors.As(err, &moduleErr) || moduleErr.Code != "PROJECT_RESOLUTION_UNAVAILABLE" {
		t.Fatalf("error=%v, want server-resolution-only refusal", err)
	}
	if mod.cache.HasEntry(project.ID) {
		t.Fatal("noncanonical V3 response retained a compatibility cache entry")
	}
	assertV3CallRequest(t, srv.callReq)
}

func assertV3InitializeRequest(t *testing.T, req *pb.InitializeRequest) {
	t.Helper()
	if req == nil || req.GetProjectIdentityV3() == nil {
		t.Fatalf("Initialize request=%#v, want V3 descriptor", req)
	}
	if req.GetProject() != "" || req.GetProjectIdentity() != nil {
		t.Fatalf("Initialize used V2/raw fallback: %#v", req)
	}
	assertV3Descriptor(t, req.GetProjectIdentityV3())
}

func assertV3CallRequest(t *testing.T, req *pb.CallToolRequest) {
	t.Helper()
	if req == nil || req.GetProjectIdentityV3() == nil {
		t.Fatalf("CallTool request=%#v, want V3 descriptor", req)
	}
	if req.GetProject() != "" || req.GetProjectIdentity() != nil {
		t.Fatalf("CallTool used V2/raw fallback: %#v", req)
	}
	assertV3Descriptor(t, req.GetProjectIdentityV3())
}

func assertV3Descriptor(t *testing.T, identity *pb.ProjectIdentityV3) {
	t.Helper()
	if identity.GetVersion() != 3 || identity.GetAnchorProjectId() != "22222222-2222-4222-8222-222222222222" || identity.GetName() != "daemon-v3" || identity.GetScope() != "repository" || identity.GetClientInstanceId() != "fixture-daemon-install" {
		t.Fatalf("descriptor=%#v", identity)
	}
	if got := identity.GetNormalizedGitRemotes(); len(got) != 1 || got[0] != "git.example.test/Platform/Daemon" {
		t.Fatalf("normalized remotes=%q", got)
	}
	if len(identity.GetLegacyIdentifiers()) != 0 {
		t.Fatalf("legacy identifiers=%#v, want none", identity.GetLegacyIdentifiers())
	}
}

func resolvedV3Response() *pb.ProjectResolutionResultV3 {
	projectKey := daemonV3CanonicalProject
	scope := "repository"
	return &pb.ProjectResolutionResultV3{
		Outcome:       pb.ProjectResolutionOutcomeV3_PROJECT_RESOLVED,
		Correlation:   "daemon-v3-correlation",
		ProjectKey:    &projectKey,
		ResolvedScope: &scope,
	}
}

func typedV3Refusal(t *testing.T, outcome string) error {
	t.Helper()
	st, err := status.New(codes.FailedPrecondition, "project identity resolution refused").WithDetails(&errdetails.ErrorInfo{
		Domain:   "engram.project_identity.v3",
		Reason:   outcome,
		Metadata: map[string]string{"correlation": "daemon-v3-correlation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return st.Err()
}

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

func daemonV3UnbornRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if output, err := exec.Command("git", "-C", root, "init").CombinedOutput(); err != nil {
		t.Fatalf("initialize unborn repository: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(root, "unborn.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatalf("write unborn fixture: %v", err)
	}
	if output, err := exec.Command("git", "-C", root, "rev-parse", "--verify", "HEAD").CombinedOutput(); err == nil {
		t.Fatalf("unborn fixture unexpectedly has HEAD: %s", output)
	}
	return root
}
