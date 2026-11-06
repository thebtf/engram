package engramcore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func TestAutomaticOriginRecoveryProxy(t *testing.T) {
	testAutomaticOriginRecoveryProxy(t, func(path string) string { return path })
}

func testAutomaticOriginRecoveryProxy(t *testing.T, callerPath func(string) string) {
	t.Helper()
	t.Setenv("ENGRAM_TLS_CA", "")
	handler := &observedRecoveryHandler{anchorDiscoveryHandler: anchorDiscoveryHandler{server: mcp.NewServer(mcp.ServerOptions{Version: "origin-test"})}}
	grpcServer, _ := grpcserver.New(handler, auth.NewValidator(markerRecoveryToken, nil))
	var gets, posts atomic.Int32
	backend := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodPost {
			posts.Add(1)
			http.Error(w, "unexpected bootstrap", 500)
			return
		}
		gets.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/projects/resolve" || r.URL.Query().Get("repository_locator") != "example.test/Fixture/Repo" || r.Header.Get("Authorization") != "Bearer "+markerRecoveryToken {
			http.Error(w, "denied", 403)
			return
		}
		_, _ = w.Write([]byte(`{"status":"resolved","binding":{"id":"` + markerRecoveryID + `","project_key":"` + markerRecoveryID + `","identity_status":"active","match":"manual_alias"}}`))
	}), &http2.Server{}))
	t.Cleanup(func() { grpcServer.Stop(); backend.Close() })
	_, mod, caller := buildContractDispatcher(t, "")
	caller.Cwd = callerPath(caller.Cwd)
	caller.Env[config.EnvServerURL] = backend.URL
	caller.Env[config.EnvWorkstationToken] = markerRecoveryToken
	caller.Env[config.EnvClaudeSessionID] = "origin-session"
	for _, args := range [][]string{{"init", "--quiet", "--template="}, {"remote", "add", "origin", "https://example.test/Fixture/Repo.git"}} {
		if out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", caller.Cwd}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	marker := filepath.Join(caller.Cwd, ".engram-project")
	original := []byte(`{"name":"Existing"}`)
	if err := os.WriteFile(marker, original, 0o600); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"content":"origin recovery preview","dry_run":true}`)
	result, err := mod.ProxyHandleTool(t.Context(), caller, "store", args)
	if err != nil {
		t.Fatalf("original operation refused: %v", err)
	}
	if !strings.Contains(string(result), "dry_run") {
		t.Fatalf("actual preview missing: %s", result)
	}
	handler.mu.Lock()
	calls, project, forwarded, session := handler.calls, handler.project, handler.args, handler.session
	handler.mu.Unlock()
	if calls != 1 || project != markerRecoveryID || forwarded != string(args) || session != "origin-session" || gets.Load() != 1 || posts.Load() != 0 {
		t.Fatalf("calls=%d project=%s session=%s gets=%d posts=%d", calls, project, session, gets.Load(), posts.Load())
	}
	if current, err := os.ReadFile(marker); err != nil || string(current) != string(original) {
		t.Fatal("marker changed")
	}
	if _, err := os.Lstat(filepath.Join(caller.Cwd, ".git", "engram-project.json")); !os.IsNotExist(err) {
		t.Fatal("existing binding created a cache")
	}
}
