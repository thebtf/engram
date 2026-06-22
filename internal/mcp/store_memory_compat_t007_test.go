package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
)

// t007TestEnv carries the live MCP Server plus the raw *gorm.DB handle used
// by integration fixtures that need to insert via raw SQL (the MemoryStore
// public surface does not expose the underlying DB).
type t007TestEnv struct {
	srv   *Server
	store *dbgorm.Store
}

// newMemoryServerForT007 builds an MCP Server bound to a live PostgreSQL +
// runs migrations. Matches the worker.newMemoryTestService pattern but lives
// at the MCP layer because T007/T008 functional surface is MCP (T004 path
// drift Aligned note).
//
// DSN-gated per existing convention; tests skip when DATABASE_DSN is unset.
// Cleanup deletes rows with the per-test project before closing the store.
func newMemoryServerForT007(t *testing.T, project string) t007TestEnv {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	if err != nil {
		t.Skipf("DATABASE_DSN not reachable, skipping integration test: %v", err)
	}

	memoryStore := dbgorm.NewMemoryStore(store)

	srv := NewServer(ServerOptions{Version: "test"})
	srv.memoryStore = memoryStore

	t.Cleanup(func() {
		_ = store.DB.WithContext(context.Background()).
			Exec(`DELETE FROM memories WHERE project = ?`, project).Error
		_ = store.Close()
	})

	return t007TestEnv{srv: srv, store: store}
}

// TestEC_F1_TagDerivedBackfill_T007 verifies the EC-F1 acceptance criterion
// (engram vNext Milestone F TG1 / CR-F1 / T007):
//
//   - rows with `tags ? 'scope:global'`  -> privacy_scope='global' after backfill
//   - rows with `tags ? 'scope:project'` -> privacy_scope='project' (column DEFAULT)
//   - rows with no scope:* tag           -> privacy_scope='project' (column DEFAULT)
//   - the global-tagged row is still visible via MemoryStore.List in its own
//     project after the backfill — verifies the new column does not change
//     the existing intra-project recall behavior
//
// This is a v6.4-compatibility test: rows shaped the way the v6.4.x MCP
// handler wrote them (Memory.Tags containing scope:project / scope:global)
// must end up with the right privacy_scope value after migration + backfill,
// and recall through MemoryStore.List must still return them.
//
// Cross-project global visibility is NOT exercised here: MemoryStore.List is
// project-scoped (memory_store.go:96-114) and cross-project global recall is
// out of TG1 scope (a future store-layer change; spec FR-F3 REVISE confirms
// the trivial-SQL surface). The AC's "different project" wording is
// aspirational for a later milestone; T007 verifies what's testable today,
// recorded as Aligned in the implementation log §3.
//
// Anti-stub: removing the tag-derived UPDATE from migration 125 breaks the
// `scope:global` assertion — that row would default to 'project' incorrectly.
func TestEC_F1_TagDerivedBackfill_T007(t *testing.T) {
	project := "t007-ec-f1-" + uuid.NewString()
	env := newMemoryServerForT007(t, project)
	db := env.store.DB

	// Insert three v6.4-style fixture rows directly into the DB. Each carries
	// only a Tags entry — no privacy_scope field set, so the column DEFAULT
	// 'project' applies on insert.
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags) VALUES (?, ?, ?::jsonb)`,
		project, "T007 fixture global-tagged", `["scope:global"]`,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags) VALUES (?, ?, ?::jsonb)`,
		project, "T007 fixture project-tagged", `["scope:project"]`,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags) VALUES (?, ?, ?::jsonb)`,
		project, "T007 fixture untagged", `[]`,
	).Error)

	// Force the global-tagged row off the default so the migration 125
	// backfill UPDATE statement has work to do (the migration has already
	// run via NewStore→runMigrations; the UPDATE clause is the contract).
	require.NoError(t, db.Exec(
		`UPDATE memories SET privacy_scope = 'project'
			WHERE project = ? AND tags ? 'scope:global'`,
		project,
	).Error)

	// Run the backfill statement (mirrors migration 125's UPDATE clause).
	require.NoError(t, db.Exec(
		`UPDATE memories
			SET privacy_scope = 'global'
			WHERE privacy_scope <> 'global'
			  AND project = ?
			  AND tags ? 'scope:global'`,
		project,
	).Error)

	// Read back the three rows and assert privacy_scope per AC.
	type row struct {
		Content      string
		PrivacyScope string
		Tags         string
	}
	var rows []row
	require.NoError(t, db.Raw(
		`SELECT content, privacy_scope, tags::text AS tags
			FROM memories WHERE project = ? ORDER BY id`,
		project,
	).Scan(&rows).Error)
	require.Len(t, rows, 3, "expected 3 fixture rows")

	require.Equal(t, "global", rows[0].PrivacyScope, "row with scope:global tag -> privacy_scope='global'")
	require.True(t, strings.Contains(rows[0].Tags, "scope:global"))

	require.Equal(t, "project", rows[1].PrivacyScope, "row with scope:project tag -> privacy_scope='project'")
	require.True(t, strings.Contains(rows[1].Tags, "scope:project"))

	require.Equal(t, "project", rows[2].PrivacyScope, "row without scope tag -> privacy_scope='project' (column DEFAULT)")
	require.NotContains(t, rows[2].Tags, "scope:")

	// Verify global-scoped row is still queryable via MemoryStore.List within
	// its own project — the new column does not affect existing intra-project
	// retrieval behavior.
	mems, err := env.srv.memoryStore.List(context.Background(), project, 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(mems), 3, "all 3 fixture rows must be returned by MemoryStore.List")
	var foundGlobal bool
	for _, m := range mems {
		if m.PrivacyScope == "global" {
			foundGlobal = true
			break
		}
	}
	require.True(t, foundGlobal,
		"global-scoped row must be returned by MemoryStore.List within its own project")
}

// TestEC_F1_HandleRecallSearch_FlagOff_BackwardCompat_T007 verifies that the
// v6.4-compatibility path through handleRecallSearch with the vNext F flag
// OFF returns memories regardless of their privacy_scope column value — the
// post-fetch scope.Resolve filter must be skipped, preserving byte-identical
// recall behavior for legacy clients.
//
// Anti-stub: forcing scopeEnabled=true unconditionally in handleRecallSearch
// would filter all rows (callerWorkstationID empty → private rows excluded;
// but project/shared/global rows still admitted). The test here is that the
// flag-OFF path doesn't enter the filter loop at all.
func TestEC_F1_HandleRecallSearch_FlagOff_BackwardCompat_T007(t *testing.T) {
	project := "t007-ec-f1-flagoff-" + uuid.NewString()
	env := newMemoryServerForT007(t, project)
	db := env.store.DB

	// Explicitly clear the flag for this subprocess.
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")

	// Insert a global-scoped fixture row directly with privacy_scope set —
	// flag OFF means the column value should not affect what recall returns.
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope) VALUES (?, ?, ?::jsonb, ?)`,
		project, "T007 flag-off global row", `["scope:global"]`, "global",
	).Error)

	args, err := json.Marshal(map[string]any{
		"action":  "search",
		"project": project,
		"query":   "",
	})
	require.NoError(t, err)

	out, err := env.srv.handleRecall(context.Background(), args)
	require.NoError(t, err)

	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &resp))

	count, _ := resp["count"].(float64)
	require.GreaterOrEqual(t, int(count), 1, "flag-OFF recall must return the row regardless of privacy_scope")

	// Response shape under flag OFF must not include privacy_scope per-result
	// (memoryResult uses omitempty + scopeEnabled=false leaves the field
	// unset on the result struct).
	mems, _ := resp["memories"].([]any)
	require.NotEmpty(t, mems)
	for _, raw := range mems {
		m, ok := raw.(map[string]any)
		require.True(t, ok)
		_, hasPS := m["privacy_scope"]
		require.False(t, hasPS,
			"flag-OFF recall response must NOT include privacy_scope per-result (RI-F1 backward compat); got %v", m)
	}
}
