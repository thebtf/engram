package engramcore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/module"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type observedRecoveryHandler struct {
	anchorDiscoveryHandler
	mu           sync.Mutex
	calls        int
	project      string
	session      string
	loseResponse bool
	args         string
}

func (h *observedRecoveryHandler) HandleToolCall(ctx context.Context, name string, args []byte) ([]byte, bool, error) {
	h.mu.Lock()
	h.calls++
	h.project = mcp.ProjectFromContext(ctx)
	h.session = auditcontext.SourceSession(ctx)
	h.args = string(args)
	h.mu.Unlock()
	result, isError, err := h.anchorDiscoveryHandler.HandleToolCall(ctx, name, args)
	if h.loseResponse {
		return nil, false, status.Error(codes.Unavailable, "simulated lost response")
	}
	return result, isError, err
}

func TestAutomaticHistoricalRecoveryProxy(t *testing.T) {
	t.Setenv("ENGRAM_TLS_CA", "")
	handler := &observedRecoveryHandler{anchorDiscoveryHandler: anchorDiscoveryHandler{server: mcp.NewServer(mcp.ServerOptions{Version: "recovery-test"})}}
	grpcServer, _ := grpcserver.New(handler, auth.NewValidator(markerRecoveryToken, nil))
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/projects/resolve" || r.URL.Query().Get("legacy_id") != "abcdef" || r.Header.Get("Authorization") != "Bearer "+markerRecoveryToken {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"status":"resolved","project_id":"` + markerRecoveryID + `","name":"Existing"}`))
	}), &http2.Server{}))
	t.Cleanup(func() { grpcServer.Stop(); server.Close() })
	_, mod, caller := buildContractDispatcher(t, "")
	caller.Env[config.EnvServerURL] = server.URL
	caller.Env[config.EnvWorkstationToken] = markerRecoveryToken
	caller.Env[config.EnvClaudeSessionID] = "automatic-recovery-session"
	marker := filepath.Join(caller.Cwd, ".engram-project")
	legacy := []byte(`{"id":"abcdef","name":"Existing"}`)
	if err := os.WriteFile(marker, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"content":"original recovery preview","dry_run":true}`)
	result, err := mod.ProxyHandleTool(t.Context(), caller, "store", args)
	if err != nil {
		t.Fatalf("original store preview was not forwarded: %v", err)
	}
	if !strings.Contains(string(result), "dry_run") {
		t.Fatalf("real store preview missing: %s", result)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.calls != 1 || handler.project != markerRecoveryID || handler.args != string(args) || handler.session != "automatic-recovery-session" {
		t.Fatalf("original operation not forwarded once with canonical identity: calls=%d project=%q args=%q session=%q", handler.calls, handler.project, handler.args, handler.session)
	}
	if current, err := os.ReadFile(marker); err != nil || string(current) != string(legacy) {
		t.Fatal("legacy marker changed")
	}
}

func TestAutomaticHistoricalRecoveryHoldsAndNoReplay(t *testing.T) {
	for _, scenario := range []string{"changed-marker", "changed-cache", "conflicting-cache", "other-caller", "unauthorized", "unresolved", "duplicate-key", "lost-response"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("ENGRAM_TLS_CA", "")
			handler := &observedRecoveryHandler{anchorDiscoveryHandler: anchorDiscoveryHandler{server: mcp.NewServer(mcp.ServerOptions{Version: "recovery-test"})}, loseResponse: scenario == "lost-response"}
			grpcServer, _ := grpcserver.New(handler, auth.NewValidator(markerRecoveryToken, nil))
			_, mod, caller := buildContractDispatcher(t, "")
			if out, err := exec.CommandContext(t.Context(), "git", "-C", caller.Cwd, "init", "--quiet", "--template=").CombinedOutput(); err != nil {
				t.Fatalf("git init: %v: %s", err, out)
			}
			marker := filepath.Join(caller.Cwd, ".engram-project")
			cache := filepath.Join(caller.Cwd, ".git", "engram-project.json")
			if err := os.WriteFile(marker, []byte(`{"id":"abcdef","name":"Existing"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if scenario == "conflicting-cache" {
				if err := os.WriteFile(cache, []byte(`{"project_id":"`+markerRecoveryOtherID+`"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var lookups atomic.Int32
			server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
					grpcServer.ServeHTTP(w, r)
					return
				}
				lookups.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/projects/resolve" || r.URL.Query().Get("legacy_id") != "abcdef" || r.Header.Get("Authorization") != "Bearer "+markerRecoveryToken {
					http.Error(w, "denied", http.StatusForbidden)
					return
				}
				switch scenario {
				case "unauthorized":
					w.WriteHeader(http.StatusForbidden)
					return
				case "unresolved":
					_, _ = w.Write([]byte(`{"status":"unresolved","reference":"urn:fixture","reason":"ambiguous"}`))
					return
				case "duplicate-key":
					_, _ = w.Write([]byte(`{"status":"resolved","project_id":"` + markerRecoveryOtherID + `","project_id":"` + markerRecoveryID + `"}`))
					return
				case "changed-marker":
					if err := os.WriteFile(marker, []byte(`{"id":"abcdef","name":"Changed"}`), 0o600); err != nil {
						t.Error(err)
					}
				case "changed-cache":
					if err := os.WriteFile(cache, []byte(`{"project_id":"`+markerRecoveryID+`"}`), 0o600); err != nil {
						t.Error(err)
					}
				}
				_, _ = w.Write([]byte(`{"status":"resolved","project_id":"` + markerRecoveryID + `","name":"Existing"}`))
			}), &http2.Server{}))
			t.Cleanup(func() { grpcServer.Stop(); server.Close() })
			caller.Env[config.EnvServerURL] = server.URL
			caller.Env[config.EnvWorkstationToken] = markerRecoveryToken
			caller.Env[config.EnvClaudeSessionID] = "automatic-recovery-session"
			if scenario == "other-caller" {
				caller.Cwd = t.TempDir()
				if out, err := exec.CommandContext(t.Context(), "git", "-C", caller.Cwd, "init", "--quiet", "--template=").CombinedOutput(); err != nil {
					t.Fatalf("other caller git init: %v: %s", err, out)
				}
			}
			args := json.RawMessage(`{"content":"original recovery preview","dry_run":true}`)
			if scenario == "lost-response" {
				args = json.RawMessage(`{"content":"original mutation with unknown outcome"}`)
			}
			_, err := mod.ProxyHandleTool(t.Context(), caller, "store", args)
			if err == nil {
				t.Fatal("expected caller-visible refusal or lost response")
			}
			if scenario != "lost-response" {
				want := map[string]string{"changed-marker": "PROJECT_SELECTION_STALE", "changed-cache": "PROJECT_SELECTION_STALE", "conflicting-cache": "PROJECT_IDENTITY_CONFLICT", "other-caller": "PROJECT_CONTEXT_REQUIRED", "unauthorized": "PROJECT_ACCESS_DENIED", "unresolved": "PROJECT_MIGRATION_REQUIRED", "duplicate-key": "PROJECT_IDENTITY_INVALID"}[scenario]
				var refusal *module.ModuleError
				if !errors.As(err, &refusal) || refusal.Code != want {
					t.Fatalf("refusal=%v, want %s", err, want)
				}
			}
			handler.mu.Lock()
			calls := handler.calls
			handler.mu.Unlock()
			if scenario == "lost-response" {
				if calls != 1 || lookups.Load() != 1 {
					t.Fatalf("lost response replayed: calls=%d lookups=%d", calls, lookups.Load())
				}
			} else if calls != 0 {
				t.Fatalf("held operation reached server: calls=%d", calls)
			}
			if scenario == "other-caller" && lookups.Load() != 0 {
				t.Fatal("other caller borrowed the first caller mapping")
			}
		})
	}
}
