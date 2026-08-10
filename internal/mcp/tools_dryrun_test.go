// Package mcp — tools_dryrun_test.go: T044 dry-run parameter tests.
// RED suite: verifies store_memory, promote_candidate, and bulk_* return
// dry-run previews with zero DB side effects.
//
// These tests are unit-only (no DATABASE_DSN required) for the dry-run paths.
// Integration paths that verify SELECT count(*) unchanged require DATABASE_DSN.
package mcp

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/gorm/logger"
)

// TestStoreMemory_DryRun_NilStore verifies that store_memory with dry_run=true
// short-circuits before hitting the memory store (nil store guard must NOT fire).
// This exercises the TG5-absent nil-safe seam: when the write-lint orchestrator
// is not present, store_memory dry_run uses a legacy path preview (no DB write).
func TestStoreMemory_DryRun_NilStore(t *testing.T) {
	s := NewServer(ServerOptions{Version: "test"})
	// memoryStore is nil — dry_run must return before reaching the nil-store guard.

	adminID := auth.Identity{Role: auth.RoleAdmin, Source: auth.SourceMaster}
	ctx := auth.WithIdentity(context.Background(), adminID)

	args := json.RawMessage(`{
		"content": "test memory content",
		"project": "test-project",
		"dry_run": true
	}`)

	result, err := s.handleStoreMemory(ctx, args)
	require.NoError(t, err, "store_memory dry_run must not return error even with nil store")
	assert.NotEmpty(t, result, "store_memory dry_run must return a preview JSON")

	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &out), "dry_run result must be valid JSON")
	assert.Equal(t, true, out["dry_run"], "dry_run field must be true in response")
	assert.NotEmpty(t, out["would_store"], "preview must include would_store content preview")
}

// TestStoreMemory_DryRun_RequiresContent verifies that content validation still
// fires before dry_run returns (content is required even for previews).
func TestStoreMemory_DryRun_RequiresContent(t *testing.T) {
	s := NewServer(ServerOptions{Version: "test"})

	adminID := auth.Identity{Role: auth.RoleAdmin, Source: auth.SourceMaster}
	ctx := auth.WithIdentity(context.Background(), adminID)

	args := json.RawMessage(`{"project": "test-project", "dry_run": true}`)

	_, err := s.handleStoreMemory(ctx, args)
	require.Error(t, err, "store_memory dry_run with no content must error")
	assert.Contains(t, err.Error(), "content is required")
}

func TestStoreMemoryCreateUsesLosslessSupersedesSelector(t *testing.T) {
	s := NewServer(ServerOptions{Version: "strict-store"})
	ctx := auth.WithIdentity(context.Background(), auth.Admin())
	_, err := s.handleStoreMemory(ctx, json.RawMessage(`{"content":"exact","project":"test","supersedes":[9007199254740993],"dry_run":true}`))
	require.NoError(t, err)
	for _, raw := range []string{`[1.5]`, `["1"]`, `[9223372036854775808]`} {
		_, err := s.handleStoreMemory(ctx, json.RawMessage(`{"content":"bad","project":"test","supersedes":`+raw+`,"dry_run":true}`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "supersedes")
	}
}

// TestPromoteCandidate_DryRun_NilStore verifies promote_candidate dry_run=true
// returns a preview without hitting candidateStore (nil-safe path).
func TestPromoteCandidate_DryRun_NilStore(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	s := NewServer(ServerOptions{Version: "test"})
	// candidateStore is nil — vnextFEnabled() guard would normally block, but
	// dry_run should be able to return without a live store.
	// NOTE: promote_candidate still requires candidateStore to load the candidate
	// for the "what would be promoted" preview. If store is nil, dry_run returns
	// a preview with the id only.

	adminID := auth.Identity{Role: auth.RoleAdmin, Source: auth.SourceMaster}
	ctx := auth.WithIdentity(context.Background(), adminID)

	args := json.RawMessage(`{"id": 42, "dry_run": true}`)

	result, err := s.handlePromoteCandidate(ctx, args)
	require.NoError(t, err, "promote_candidate dry_run must not return error with nil store")
	assert.NotEmpty(t, result)

	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &out))
	assert.Equal(t, true, out["dry_run"])
}

// TestBulkPromote_DryRun_NilFacade verifies bulk_promote dry_run=true
// returns would_affect count without hitting any store.
func TestBulkPromote_DryRun_NilFacade(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	s := NewServer(ServerOptions{Version: "test"})
	// bulkFacade is nil — dry_run must still return a preview.

	adminID := auth.Identity{Role: auth.RoleAdmin, Source: auth.SourceMaster}
	ctx := auth.WithIdentity(context.Background(), adminID)

	args := json.RawMessage(`{"candidate_ids": [1, 2, 3], "dry_run": true}`)

	result, err := s.handleBulkPromote(ctx, args)
	require.NoError(t, err, "bulk_promote dry_run with nil facade must not error")
	assert.NotEmpty(t, result)

	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &out))
	assert.Equal(t, true, out["dry_run"])
	assert.Equal(t, float64(3), out["would_affect"], "would_affect must equal len(candidate_ids)")
}

// TestBulkDelete_DryRun_NilFacade verifies bulk_delete dry_run=true
// returns would_affect count without hitting any store.
func TestBulkDelete_DryRun_NilFacade(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	s := NewServer(ServerOptions{Version: "test"})

	adminID := auth.Identity{Role: auth.RoleAdmin, Source: auth.SourceMaster}
	ctx := auth.WithIdentity(context.Background(), adminID)

	args := json.RawMessage(`{"memory_ids": [10, 20], "dry_run": true}`)

	result, err := s.handleBulkDelete(ctx, args)
	require.NoError(t, err, "bulk_delete dry_run with nil facade must not error")
	assert.NotEmpty(t, result)

	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &out))
	assert.Equal(t, true, out["dry_run"])
	assert.Equal(t, float64(2), out["would_affect"])
}

// TestBulkSupersede_DryRun_NilFacade verifies bulk_supersede dry_run=true
// returns would_affect count without hitting any store.
func TestBulkSupersede_DryRun_NilFacade(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	s := NewServer(ServerOptions{Version: "test"})

	adminID := auth.Identity{Role: auth.RoleAdmin, Source: auth.SourceMaster}
	ctx := auth.WithIdentity(context.Background(), adminID)

	args := json.RawMessage(`{"memory_ids": [5], "dry_run": true}`)

	result, err := s.handleBulkSupersede(ctx, args)
	require.NoError(t, err, "bulk_supersede dry_run with nil facade must not error")
	assert.NotEmpty(t, result)

	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &out))
	assert.Equal(t, true, out["dry_run"])
	assert.Equal(t, float64(1), out["would_affect"])
}

// TestBulkPromote_NonAdmin_ReturnsAdminRequired verifies that bulk_promote
// rejects non-admin callers.
func TestBulkPromote_NonAdmin_ReturnsAdminRequired(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	s := NewServer(ServerOptions{Version: "test"})

	roID := auth.Identity{Role: auth.RoleReadOnly, Source: auth.SourceClient}
	ctx := auth.WithIdentity(context.Background(), roID)

	args := json.RawMessage(`{"candidate_ids": [1, 2, 3]}`)

	_, err := s.handleBulkPromote(ctx, args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "admin_required")
}

// TestBulkOps_FlagOff_NotAdvertised verifies bulk_promote/delete/supersede
// are not in ListTools when ENGRAM_VNEXT_F_ENABLED=false.
func TestBulkOps_FlagOff_NotAdvertised(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "false")
	s := NewServer(ServerOptions{Version: "test"})
	tools := s.ListTools()
	for _, tool := range tools {
		assert.NotEqual(t, "bulk_promote", tool.Name)
		assert.NotEqual(t, "bulk_delete", tool.Name)
		assert.NotEqual(t, "bulk_supersede", tool.Name)
	}
}

func TestBulkMutationsUseLosslessSelectors(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	ctx := auth.WithIdentity(context.Background(), auth.Admin())
	for _, tc := range []struct{ tool, field string }{{"bulk_promote", "candidate_ids"}, {"bulk_delete", "memory_ids"}, {"bulk_supersede", "memory_ids"}} {
		s := NewServer(ServerOptions{Version: "strict-bulk"})
		out, err := s.callTool(ctx, tc.tool, json.RawMessage(`{"`+tc.field+`":[9007199254740993],"dry_run":true}`))
		require.NoError(t, err, tc.tool)
		require.Contains(t, out, `"would_affect":1`)
		for _, ids := range []string{`[1.5]`, `["1"]`, `[9223372036854775808]`} {
			out, err := s.callTool(ctx, tc.tool, json.RawMessage(`{"`+tc.field+`":`+ids+`,"dry_run":true}`))
			require.Error(t, err, tc.tool)
			require.Empty(t, out, tc.tool)
		}
		out, err = s.callTool(ctx, tc.tool, json.RawMessage(`{"`+tc.field+`":[1],"dry_run":"true"}`))
		require.Error(t, err, tc.tool)
		require.Empty(t, out, tc.tool)
	}
}

func TestBulkMutationsAcceptExactIntegralDecimalAndExponentSelectors(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	ctx := auth.WithIdentity(context.Background(), auth.Admin())
	for _, tc := range []struct{ tool, field string }{{"bulk_promote", "candidate_ids"}, {"bulk_delete", "memory_ids"}, {"bulk_supersede", "memory_ids"}} {
		s := NewServer(ServerOptions{Version: "strict-bulk"})
		out, err := s.callTool(ctx, tc.tool, json.RawMessage(`{"`+tc.field+`":[1.0,1e0,9007199254740993],"dry_run":true}`))
		require.NoError(t, err, tc.tool)
		require.Contains(t, out, `"would_affect":3`, tc.tool)
	}
}

// TestDryRun_Integration_StoreMemory_ZeroSideEffects verifies store_memory dry_run=true
// leaves the memories table unchanged.
// Skipped when DATABASE_DSN is absent.
func TestDryRun_Integration_StoreMemory_ZeroSideEffects(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set — skipping integration dry-run test")
	}
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	// Build a server with live stores (same helper pattern as governance tests).
	s := dryRunTestServer(t, dsn)

	adminID := auth.Identity{Role: auth.RoleAdmin, Source: auth.SourceMaster}
	ctx := auth.WithIdentity(context.Background(), adminID)

	// Capture count before.
	countBefore := s.countMemories(t, ctx)

	args := json.RawMessage(`{
		"content": "dry-run test memory that must not be stored",
		"project": "dryrun-test",
		"dry_run": true
	}`)
	result, err := s.handleStoreMemory(ctx, args)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &out))
	assert.Equal(t, true, out["dry_run"])

	// Count must be unchanged.
	countAfter := s.countMemories(t, ctx)
	assert.Equal(t, countBefore, countAfter, "store_memory dry_run must not add any rows")
}

// dryRunTestServer builds a minimal Server connected to a live DB for integration tests.
// When dsn is non-empty the server's memoryStore is wired so countMemories reflects real DB state.
func dryRunTestServer(t *testing.T, dsn string) *dryRunServer {
	t.Helper()
	s := NewServer(ServerOptions{Version: "test"})
	if dsn != "" {
		store, err := gormdb.NewStore(gormdb.Config{
			DSN:      dsn,
			LogLevel: logger.Warn,
		})
		require.NoError(t, err, "dryRunTestServer: NewStore")
		t.Cleanup(func() { store.Close() })
		s.SetMemoryStore(gormdb.NewMemoryStore(store))
	}
	return &dryRunServer{s: s, dsn: dsn}
}

// dryRunServer wraps Server for integration test helpers.
type dryRunServer struct {
	s   *Server
	dsn string
}

func (d *dryRunServer) handleStoreMemory(ctx context.Context, args json.RawMessage) (string, error) {
	return d.s.handleStoreMemory(ctx, args)
}

func (d *dryRunServer) countMemories(t *testing.T, ctx context.Context) int {
	t.Helper()
	// Direct DB count via memoryStore.
	if d.s.memoryStore == nil {
		return 0
	}
	// Use List as a proxy count; limit=999999 to get all.
	mems, err := d.s.memoryStore.List(ctx, "dryrun-test", 999999)
	if err != nil {
		t.Logf("countMemories: list error: %v", err)
		return 0
	}
	return len(mems)
}
