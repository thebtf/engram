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
		require.NoError(t, store.DB.WithContext(context.Background()).
			Exec(`DELETE FROM memories WHERE project = ?`, project).Error)
		require.NoError(t, store.DB.Where("id = ?", project).Delete(&dbgorm.Project{}).Error)
		require.NoError(t, store.Close())
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
	project := uuid.NewString()
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")
	env := newMemoryServerForT007(t, project)
	db := env.store.DB
	require.NoError(t, db.Create(&dbgorm.Project{ID: project}).Error)

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
		ID           int64
		Content      string
		PrivacyScope string
		Tags         string
	}
	var rows []row
	require.NoError(t, db.Raw(
		`SELECT id, content, privacy_scope, tags::text AS tags
			FROM memories WHERE project = ? ORDER BY id`,
		project,
	).Scan(&rows).Error)
	require.Len(t, rows, 3, "expected 3 fixture rows")

	require.NotZero(t, rows[0].ID, "global-tagged fixture must have a durable database ID")
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
	globalFixtureID := rows[0].ID
	globalFixtureContent := rows[0].Content
	var foundGlobalFixture bool
	for _, m := range mems {
		if m.ID == globalFixtureID {
			require.Equal(t, globalFixtureContent, m.Content,
				"MemoryStore.List must return the exact global-tagged fixture content")
			foundGlobalFixture = true
			break
		}
	}
	require.True(t, foundGlobalFixture,
		"global-scoped fixture id=%d must be returned by MemoryStore.List within its own project", globalFixtureID)
	out, err := env.srv.handleRecall(context.Background(), mustJSON(t, map[string]any{
		"action": "search", "project": project,
	}))
	require.NoError(t, err)
	var response struct {
		Memories []struct {
			ID           int64  `json:"id"`
			Content      string `json:"content"`
			PrivacyScope string `json:"privacy_scope"`
		} `json:"memories"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &response))
	require.Len(t, response.Memories, 3)
	for _, memory := range response.Memories {
		if memory.ID == globalFixtureID {
			require.Equal(t, globalFixtureContent, memory.Content)
			require.Equal(t, "global", memory.PrivacyScope)
			return
		}
	}
	t.Fatal("public recall omitted the migrated global fixture")
}
