package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt in with a dedicated loopback retained_runtime_* database; real startup runs migrations.
func TestServiceRetainedReadersInitializeVaultWithoutCodeIntel(t *testing.T) {
	dsn := os.Getenv("ENGRAM_RETAINED_STARTUP_TEST_DSN")
	if dsn == "" {
		t.Skip("requires explicitly assigned retained startup runtime database")
	}
	target, err := url.Parse(dsn)
	if err != nil || target.Hostname() != "127.0.0.1" || target.Port() == "" || !strings.HasPrefix(target.Path, "/retained_runtime_") {
		t.Fatal("startup regression requires an explicitly assigned loopback retained_runtime_* database")
	}
	t.Setenv("DATABASE_DSN", dsn)
	t.Setenv("DATABASE_MAX_CONNS", "2")
	t.Setenv("ENGRAM_AUTH_DISABLED", "true")
	t.Setenv("ENGRAM_AUTH_ADMIN_TOKEN", "")
	t.Setenv("ENGRAM_AUTH_ALLOWED_ORIGINS", "")
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "false")
	t.Setenv("ENGRAM_VNEXT_ENABLED", "false")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "false")
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "false")
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "false")
	t.Setenv("ENGRAM_RULE_ARBITER_ENABLED", "false")
	t.Setenv("ENGRAM_TELEMETRY_ENABLED", "false")
	t.Setenv("ENGRAM_VAULT_KEY", "")
	t.Setenv("ENGRAM_ENCRYPTION_KEY", "")
	t.Setenv("ENGRAM_EMBEDDING_URL", "")
	t.Setenv("ENGRAM_RERANK_URL", "")

	for _, valid := range []bool{true, false} {
		name := "existing-valid-key"
		if !valid {
			name = "existing-invalid-key"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("ENGRAM_DB_PATH", filepath.Join(home, ".engram", "engram.db"))
			keyPath := filepath.Join(home, "protected-existing.key")
			key := []byte("invalid-existing-key-must-not-be-replaced")
			if valid {
				key = []byte(strings.Repeat("51", 32))
			}
			if err := os.WriteFile(keyPath, key, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ENGRAM_ENCRYPTION_KEY_FILE", keyPath)
			// Check preservation after Shutdown, before t.TempDir removes this test-owned home.
			t.Cleanup(func() {
				got, err := os.ReadFile(keyPath)
				if err != nil || !bytes.Equal(got, key) {
					t.Error("startup changed the protected existing key")
				}
				if _, err := os.Stat(filepath.Join(home, ".engram", "vault.key")); !os.IsNotExist(err) {
					t.Error("startup generated a fallback key despite an explicit existing key")
				}
			})
			service, err := NewService("retained-startup-regression", nil)
			if err != nil {
				t.Fatalf("NewService: %v", err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if err := service.Shutdown(ctx); err != nil {
					t.Errorf("Shutdown: %v", err)
				}
			})
			done := make(chan struct{})
			go func() { service.initWG.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(60 * time.Second):
				t.Fatal("real service initialization did not finish")
			}
			if service.operatorCodeAdapter != nil {
				t.Error("CodeIntel-disabled startup installed an operator code adapter")
			}
			if !valid {
				if service.GetInitError() == nil || service.ready.Load() {
					t.Error("invalid existing Vault key must fail shared initialization before readiness")
				}
				response := httptest.NewRecorder()
				service.handleReady(response, httptest.NewRequest(http.MethodGet, "/api/ready", nil))
				if response.Code != http.StatusInternalServerError {
					t.Errorf("invalid Vault readiness status = %d, want 500", response.Code)
				}
				return
			}
			if err := service.GetInitError(); err != nil {
				t.Fatalf("real startup failed before reader assertions: %v", err)
			}
			if !service.ready.Load() {
				t.Fatal("successful shared initialization did not publish readiness")
			}
			if service.vault == nil {
				t.Error("successful CodeIntel-disabled startup left shared Vault uninitialized")
			}
			if _, err := service.retainedRecordCipher(); err != nil {
				t.Errorf("retained cursor capability unavailable after real startup: %v", err)
			}
			for _, kind := range []string{"", "memory", "document", "issue"} {
				path := "/api/projects"
				if kind != "" {
					path += "?view=retained_records&attribution_scope=unresolved_history&kind=" + kind + "&q=retained-startup-regression-no-match&limit=1"
				}
				response := httptest.NewRecorder()
				service.router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				if response.Code != http.StatusOK {
					t.Errorf("CodeIntel-disabled %s status = %d, want 200", path, response.Code)
					continue
				}
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
					t.Errorf("reader returned invalid JSON: %v", err)
					continue
				}
				if _, failed := payload["error"]; failed {
					t.Error("successful reader returned an unavailable/error envelope")
				}
				if kind == "" {
					if payload["items"] == nil || payload["unresolved"] == nil {
						t.Error("no-view catalog lost its ordinary envelope")
					}
				} else if string(payload["view"]) != `"retained_records"` || string(payload["items"]) != "[]" || payload["next_cursor"] == nil {
					t.Error("no-match retained query must return an honest empty work page with explicit continuation")
				}
			}
		})
	}
}
