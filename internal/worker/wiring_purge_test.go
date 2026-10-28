package worker

// wiring_purge_test.go — regression guard for the PurgeStore wiring and vnext gate (T008).
//
// Context: commit 231e321 added PurgeStore + MCP handler + SetPurgeStore setter,
// but service.go never constructed NewPurgeStore nor called SetPurgeStore — so
// purge_project always hit the nil-guard error in production (T008 wiring gap).
//
// Test coverage here targets the SERVICE-SIDE wiring contract:
//
//  1. Without SetPurgeStore: a fully-valid purge_project request returns
//     "purge store not available" (nil-guard fires).
//  2. With SetPurgeStore (&gormdb.PurgeStore{} has nil *gorm.DB): the nil-guard
//     is bypassed and execution reaches s.purgeStore.PurgeProject — which panics
//     on the nil *gorm.DB. The panic proves the guard was passed; assert.Panics
//     is the observable.
//
// NOTE: DB tests require live Postgres (DATABASE_DSN). CI has no Postgres; those
// tests skip automatically. See docs/PRODUCTION-TESTING-PLAYBOOK.md for the
// live-DB test workflow. Residual DB coverage gap is accepted scope per finding 5.
//
// The mcp-side unit tests live in internal/mcp/tools_admin_purge_test.go.
// Tests here drive through mcp.Server.HandleRequest (exported JSON-RPC path)
// to stay outside the internal/mcp package boundary.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
)

// adminCtxW returns a context carrying an admin identity (worker-package local alias).
func adminCtxW() context.Context {
	return auth.WithIdentity(context.Background(), auth.Admin())
}

// makePurgeRequest builds a tools/call JSON-RPC request for purge_project.
func makePurgeRequest(t *testing.T, project, confirm string) *mcp.Request {
	t.Helper()
	actionArgs := map[string]any{
		"action":  "purge_project",
		"project": project,
		"confirm": confirm,
	}
	argsJSON, err := json.Marshal(actionArgs)
	require.NoError(t, err)

	params := map[string]any{
		"name":      "admin",
		"arguments": json.RawMessage(argsJSON),
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	return &mcp.Request{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  json.RawMessage(paramsJSON),
	}
}

// responseText extracts the first content text from a tools/call response.
func responseText(resp *mcp.Response) string {
	if resp == nil {
		return "<nil response>"
	}
	if resp.Error != nil {
		b, _ := json.Marshal(resp.Error)
		return string(b)
	}
	if result, ok := resp.Result.(map[string]any); ok {
		if content, ok := result["content"].([]any); ok {
			for _, c := range content {
				if cm, ok := c.(map[string]any); ok {
					if text, ok := cm["text"].(string); ok {
						return text
					}
				}
			}
		}
	}
	return ""
}

// TestWiring_PurgeStore_NilGuardFiresWhenNotWired verifies that a fresh
// mcp.Server with SetPurgeStore NOT called returns "purge store not available"
// for a fully-valid purge_project request. Requires ENGRAM_VNEXT_ENABLED=true
// so the vnext gate does not short-circuit to "unknown admin action".
func TestWiring_PurgeStore_NilGuardFiresWhenNotWired(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	srv := mcp.NewServer(mcp.ServerOptions{Version: "wiring-test"})
	// SetPurgeStore deliberately NOT called.

	req := makePurgeRequest(t, "test-project", "test-project")
	resp := srv.HandleRequest(adminCtxW(), req)
	text := responseText(resp)
	assert.Contains(t, text, "purge store not available",
		"nil-guard must fire when SetPurgeStore has not been called")
}

// TestWiring_PurgeStore_NilGuardBypassedAfterSetPurgeStore verifies that after
// SetPurgeStore is called the nil-guard no longer fires.
//
// Observable: with &gormdb.PurgeStore{} (nil *gorm.DB), calling HandleRequest
// with a valid project panics at the GORM layer (nil DB dereference) rather than
// returning "purge store not available". The panic proves execution passed the
// nil-guard and reached s.purgeStore.PurgeProject.
func TestWiring_PurgeStore_NilGuardBypassedAfterSetPurgeStore(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	srv := mcp.NewServer(mcp.ServerOptions{Version: "wiring-test"})
	srv.SetPurgeStore(&gormdb.PurgeStore{}) // nil DB inside — panics past nil-guard

	req := makePurgeRequest(t, "test-project", "test-project")
	assert.Panics(t, func() {
		srv.HandleRequest(adminCtxW(), req)
	}, "execution must reach PurgeProject (nil-guard bypassed), then panic on nil DB — "+
		"if this fails with no panic, the nil-guard fired instead (SetPurgeStore not wired)")
}
