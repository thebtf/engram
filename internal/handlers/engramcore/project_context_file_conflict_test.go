package engramcore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/config"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
)

func TestProjectContextFileConflictScopedRead(t *testing.T) {
	testProjectContextScopedConflict(t, false)
}

func TestProjectContextLegacyCacheConflictScopedRead(t *testing.T) {
	testProjectContextScopedConflict(t, true)
}

func testProjectContextScopedConflict(t *testing.T, legacy bool) {
	t.Helper()
	t.Setenv("ENGRAM_TLS_CA", "")
	backend := &markerRecoveryBackend{}
	grpcServer := grpc.NewServer()
	pb.RegisterEngramServiceServer(grpcServer, backend)
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		backend.serve(w, r)
	}), &http2.Server{}))
	t.Cleanup(func() { grpcServer.Stop(); server.Close() })
	dispatcher, mod, caller := buildV3ContractDispatcher(t, "")
	caller.Env[config.EnvServerURL] = server.URL
	caller.Env[config.EnvWorkstationToken] = markerRecoveryToken
	if out, err := exec.CommandContext(t.Context(), "git", "-C", caller.Cwd, "init", "--quiet", "--template=").CombinedOutput(); err != nil {
		t.Fatalf("git: %v %s", err, out)
	}
	markerPath := filepath.Join(caller.Cwd, ".engram-project")
	cachePath := filepath.Join(caller.Cwd, ".git", "engram-project.json")
	marker := []byte(`{"project_id":"` + markerRecoveryID + `"}`)
	if legacy {
		marker = []byte(`{"id":"abcdef","name":"synthetic legacy"}`)
	}
	cache := []byte(`{"project_id":"` + markerRecoveryOtherID + `"}`)
	if err := os.WriteFile(markerPath, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, cache, 0o600); err != nil {
		t.Fatal(err)
	}
	ctxA := auditcontext.WithUCITransportSession(t.Context(), "synthetic-conflict-a")
	ctxB := auditcontext.WithUCITransportSession(t.Context(), "synthetic-conflict-b")
	call := func(ctx context.Context, tool string, args any) (string, bool) {
		t.Helper()
		req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		raw, err := dispatcher.HandleRequest(ctx, caller, req)
		if err != nil {
			t.Fatal(err)
		}
		var reply struct {
			Error  any `json:"error"`
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatal(err)
		}
		if reply.Error != nil || reply.Result.IsError || len(reply.Result.Content) != 1 {
			return string(raw), false
		}
		return reply.Result.Content[0].Text, true
	}
	args := json.RawMessage(`{"query":"existing-memory-sentinel","limit":5,"filter":{"x":1,"y":[true,2]}}`)
	reordered := json.RawMessage(`{"filter":{"y":[true,2],"x":1},"limit":5,"query":"existing-memory-sentinel"}`)
	wantError := "PROJECT_IDENTITY_CONFLICT"
	if legacy {
		wantError = "PROJECT_MIGRATION_REQUIRED"
	}
	if wire, ok := call(ctxA, "recall_memory", args); ok || !strings.Contains(wire, wantError) {
		t.Fatalf("initial read was not held: %s", wire)
	}
	wire, ok := call(ctxA, "project_context", map[string]string{"action": "repair"})
	if !ok {
		t.Fatalf("choices refused: %s", wire)
	}
	var options struct {
		Status  string `json:"status"`
		Choices []struct {
			Provenance string `json:"provenance"`
			Ref        string `json:"selection_ref"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(wire), &options) != nil || options.Status != "user_input_required" || len(options.Choices) != 2 {
		t.Fatalf("bad candidates: %s", wire)
	}
	refs := map[string]string{}
	for _, v := range options.Choices {
		refs[v.Provenance] = v.Ref
	}
	firstProvenance := "portable_marker"
	if legacy {
		firstProvenance = "legacy_mapping"
	}
	if refs[firstProvenance] == "" || refs["common_cache"] == "" {
		t.Fatalf("missing provenance: %s", wire)
	}
	selectArgs := map[string]string{"action": "repair", "selection_ref": refs["common_cache"]}
	other := json.RawMessage(`{"query":"different-read","limit":5}`)
	if wire, ok := call(ctxA, "recall_memory", other); ok {
		t.Fatalf("second read was not held: %s", wire)
	}
	if wire, ok := call(ctxA, "project_context", selectArgs); ok || !strings.Contains(wire, "PROJECT_SELECTION_STALE") {
		t.Fatalf("choice A bound read B: %s", wire)
	}
	if wire, ok := call(ctxA, "recall_memory", args); ok {
		t.Fatalf("original read unexpectedly succeeded: %s", wire)
	}
	wire, ok = call(ctxA, "project_context", map[string]string{"action": "choices"})
	if !ok || json.Unmarshal([]byte(wire), &options) != nil || len(options.Choices) != 2 {
		t.Fatalf("fresh choices missing: %s", wire)
	}
	for _, v := range options.Choices {
		refs[v.Provenance] = v.Ref
	}
	selectArgs["selection_ref"] = refs["common_cache"]
	if wire, ok := call(ctxB, "project_context", selectArgs); ok {
		t.Fatalf("cross-session choice accepted: %s", wire)
	}
	originalID := caller.ID
	caller.ID = "synthetic-other-caller"
	if wire, ok := call(ctxA, "project_context", selectArgs); ok {
		t.Fatalf("cross-caller choice accepted: %s", wire)
	}
	caller.ID = originalID
	if err := os.WriteFile(cachePath, append(append([]byte(nil), cache...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if wire, ok := call(ctxA, "project_context", selectArgs); ok || !strings.Contains(wire, "PROJECT_SELECTION_STALE") {
		t.Fatalf("changed cache accepted: %s", wire)
	}
	if err := os.WriteFile(cachePath, cache, 0o600); err != nil {
		t.Fatal(err)
	}
	if legacy {
		backend.mu.Lock()
		backend.unresolved = true
		backend.mu.Unlock()
		if wire, ok := call(ctxA, "project_context", selectArgs); ok {
			t.Fatalf("stale historical mapping accepted: %s", wire)
		}
		backend.mu.Lock()
		backend.unresolved = false
		backend.mu.Unlock()
	}
	mod.projectChoicesMu.Lock()
	mod.projectReadBindings = make(map[string]projectChoiceEvidence)
	for i := range 4096 {
		mod.projectReadBindings[strconv.Itoa(i)] = projectChoiceEvidence{expires: time.Now().Add(time.Minute)}
	}
	mod.projectChoicesMu.Unlock()
	if wire, ok := call(ctxA, "project_context", selectArgs); ok || !strings.Contains(wire, "PROJECT_RESOLUTION_UNAVAILABLE") {
		t.Fatalf("binding capacity not enforced: %s", wire)
	}
	mod.projectChoicesMu.Lock()
	mod.projectReadBindings = nil
	mod.projectChoicesMu.Unlock()
	if wire, ok := call(ctxA, "project_context", selectArgs); !ok || !strings.Contains(wire, "session_read_bound") {
		t.Fatalf("selection failed: %s", wire)
	}
	if legacy {
		backend.mu.Lock()
		backend.unresolved = true
		backend.mu.Unlock()
		if wire, ok := call(ctxA, "recall_memory", args); ok {
			t.Fatalf("unresolved mapping used for read: %s", wire)
		}
		backend.mu.Lock()
		backend.unresolved = false
		backend.mu.Unlock()
		if wire, ok := call(ctxA, "recall_memory", args); ok {
			t.Fatalf("stale binding survived mapping change: %s", wire)
		}
		wire, ok = call(ctxA, "project_context", map[string]string{"action": "choices"})
		if !ok || json.Unmarshal([]byte(wire), &options) != nil {
			t.Fatalf("fresh mapping choices unavailable: %s", wire)
		}
		for _, v := range options.Choices {
			refs[v.Provenance] = v.Ref
		}
		selectArgs["selection_ref"] = refs["common_cache"]
		if wire, ok := call(ctxA, "project_context", selectArgs); !ok || !strings.Contains(wire, "session_read_bound") {
			t.Fatalf("fresh mapping selection failed: %s", wire)
		}
	}
	if wire, ok := call(ctxA, "store", map[string]string{"content": "synthetic"}); ok {
		t.Fatalf("mutation used read-only binding: %s", wire)
	}
	if wire, ok := call(ctxB, "recall_memory", args); ok {
		t.Fatalf("other session used binding: %s", wire)
	}
	duplicate := json.RawMessage(`{"query":"existing-memory-sentinel","query":"other","limit":5}`)
	if wire, ok := call(ctxA, "recall_memory", duplicate); ok {
		t.Fatalf("duplicate keys accepted: %s", wire)
	}
	if wire, ok := call(ctxA, "recall_memory", reordered); !ok || !strings.Contains(wire, "existing-memory-sentinel") {
		t.Fatalf("safe recall failed: %s", wire)
	}
	backend.mu.Lock()
	if len(backend.calls) != 1 || backend.calls[0].GetProject() != markerRecoveryOtherID {
		t.Error("wrong canonical binding")
	}
	backend.mu.Unlock()
	if wire, ok := call(ctxA, "recall_memory", args); ok {
		t.Fatalf("binding replayed: %s", wire)
	}
	untagged, ok := call(t.Context(), "project_context", map[string]string{"action": "repair"})
	if !ok || json.Unmarshal([]byte(untagged), &options) != nil || len(options.Choices) != 2 {
		t.Fatalf("untagged choices missing: %s", untagged)
	}
	untaggedSelect := map[string]string{"action": "repair", "selection_ref": options.Choices[0].Ref}
	if wire, ok := call(t.Context(), "project_context", untaggedSelect); ok || !strings.Contains(wire, "PROJECT_SESSION_BINDING_UNAVAILABLE") {
		t.Fatalf("unverified session accepted: %s", wire)
	}
	for path, want := range map[string][]byte{markerPath: marker, cachePath: cache} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatal("conflict bytes changed")
		}
	}
}
