package engramcore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/config"
)

func TestProjectContextApprovedSeedDispatcher(t *testing.T) {
	t.Setenv("ENGRAM_TLS_CA", "")
	backend := &markerRecoveryBackend{unresolved: true}
	server := httptest.NewServer(http.HandlerFunc(backend.serve))
	defer server.Close()
	dispatcher, mod, caller := buildV3ContractDispatcher(t, "")
	caller.Env[config.EnvServerURL] = server.URL
	caller.Env[config.EnvWorkstationToken] = markerRecoveryToken
	if out, err := exec.CommandContext(t.Context(), "git", "-C", caller.Cwd, "init", "--quiet", "--template=").CombinedOutput(); err != nil {
		t.Fatalf("git: %v %s", err, out)
	}
	marker := []byte(`{"id":"abcdef","name":"synthetic"}`)
	v2 := []byte(`{"synthetic":"anchor"}`)
	markerPath := filepath.Join(caller.Cwd, ".engram-project")
	if err := os.WriteFile(markerPath, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	v2Path := filepath.Join(caller.Cwd, ".engram-project-v2.json")
	if err := os.WriteFile(v2Path, v2, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := func(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
	entry := map[string]string{"root_sha256": digest([]byte(caller.Cwd)), "server_sha256": digest([]byte(server.URL)), "marker_sha256": digest(marker), "v2_sha256": digest(v2), "project_id": markerRecoveryID, "approval_sha256": strings.Repeat("a", 64)}
	raw, err := json.Marshal(map[string]any{"version": 1, "entries": []any{entry}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod.deps.StorageDir, "project-recovery-seeds-v1.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	call := func() string {
		t.Helper()
		req := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"project_context","arguments":{"action":"repair"}}}`)
		result, err := dispatcher.HandleRequest(t.Context(), caller, req)
		if err != nil {
			t.Fatal(err)
		}
		return string(result)
	}
	result := call()
	if !strings.Contains(result, "approved_local_seed") || !strings.Contains(result, markerRecoveryID) || !strings.Contains(result, "v2_precondition") {
		t.Fatalf("seed action unavailable: %s", result)
	}
	if current, err := os.ReadFile(markerPath); err != nil || string(current) != string(marker) {
		t.Fatal("handler wrote marker")
	}
	entry["project_id"] = "33333333-3333-4333-8333-333333333333"
	unauthorized, err := json.Marshal(map[string]any{"version": 1, "entries": []any{entry}})
	if err != nil {
		t.Fatal(err)
	}
	seedPath := filepath.Join(mod.deps.StorageDir, "project-recovery-seeds-v1.json")
	if err := os.WriteFile(seedPath, unauthorized, 0o600); err != nil {
		t.Fatal(err)
	}
	if result = call(); !strings.Contains(result, "PROJECT_IDENTITY_CONFLICT") {
		t.Fatalf("unauthorized seed project accepted: %s", result)
	}
	if err := os.WriteFile(seedPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v2Path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result = call(); !strings.Contains(result, "PROJECT_SELECTION_STALE") {
		t.Fatalf("stale seed accepted: %s", result)
	}
}
