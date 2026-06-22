package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
)

// TestRecall_ScopeInvisibleNewestDoNotTruncate_CodexP1Cycle3 is the regression
// guard for the codex P1 cycle-3 finding on `4cb71be` (PR #221):
//
//	"Apply scope filtering before final recall limiting — when
//	 ENGRAM_VNEXT_F_ENABLED=true, handleRecallSearch applies scope.Resolve only
//	 after MemoryStore.List has already returned a bounded slice. If the newest
//	 candidates are mostly private rows the caller cannot see, this loop drops
//	 them and never backfills from older eligible rows, so callers can get far
//	 fewer (or zero) results even though visible matches exist in the same
//	 project."
//
// Setup:
//   - 3 newest rows: privacy_scope='private', source_workstation_id='other'
//     (invisible to the caller workstation)
//   - 2 older rows:  privacy_scope='project' (always visible)
//
// Recall as a SourceClient identity whose WorkstationID() returns 'self' (not
// 'other'), with limit=2 and ENGRAM_VNEXT_F_ENABLED=true. Without the
// batch-loop backfill the single-call List(limit=2) path returns the 2
// newest private rows, both fail scope.Resolve (workstation mismatch), and
// the response carries zero results. The fix in this PR adds a batch-loop
// via MemoryStore.ListWithOffset that keeps paging until limit visible
// results accumulate or the project's rows are exhausted.
//
// Anti-stub: replacing the batch-loop with a single ListWithOffset(limit) or
// reverting to List(fetchLimit=limit) breaks this test — the assertion of
// count==2 fails because the older project rows never enter the candidate
// pool.
func TestRecall_ScopeInvisibleNewestDoNotTruncate_CodexP1Cycle3(t *testing.T) {
	project := "t009-recall-backfill-" + uuid.NewString()
	env := newMemoryServerForT007(t, project)
	db := env.store.DB

	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	// Insert 2 older project-scoped rows first with explicit created_at
	// timestamps. CodeRabbit cycle-4 fix: previously the test relied on
	// implicit `now()` ordering between back-to-back INSERTs, which is
	// non-deterministic when multiple inserts complete within the same
	// timestamp tick. Explicit timestamps with multi-minute gaps make the
	// `ORDER BY created_at DESC, id DESC` recall pagination stable on any
	// PostgreSQL instance regardless of clock resolution. These rows are
	// always visible to anyone in the project.
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, source_workstation_id, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, now() - interval '5 minutes')`,
		project, "older project row A", `[]`, "project", "",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, source_workstation_id, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, now() - interval '4 minutes')`,
		project, "older project row B", `[]`, "project", "",
	).Error)

	// Insert 3 newest private rows owned by a DIFFERENT workstation with
	// strictly later timestamps. These are the rows that would truncate the
	// recall under the old single-call path because they fail scope.Resolve
	// and are the freshest by created_at.
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, source_workstation_id, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, now() - interval '3 minutes')`,
		project, "newest private row 1", `[]`, "private", "other-workstation",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, source_workstation_id, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, now() - interval '2 minutes')`,
		project, "newest private row 2", `[]`, "private", "other-workstation",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, source_workstation_id, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, now() - interval '1 minute')`,
		project, "newest private row 3", `[]`, "private", "other-workstation",
	).Error)

	// Build a context carrying a SourceClient identity whose WorkstationID()
	// returns 'self-workstation'. Per auth/identity.go:111-116, WorkstationID
	// returns KeycardID when Source == SourceClient.
	callerIdentity := auth.Identity{
		Role:      auth.Role("admin"),
		Source:    auth.SourceClient,
		KeycardID: "self-workstation",
	}
	ctx := auth.WithIdentity(context.Background(), callerIdentity)

	args, err := json.Marshal(map[string]any{
		"action":  "search",
		"project": project,
		"limit":   2,
	})
	require.NoError(t, err)

	out, err := env.srv.handleRecall(ctx, args)
	require.NoError(t, err)

	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &resp))

	count, _ := resp["count"].(float64)
	require.Equal(t, 2, int(count),
		"recall must backfill from older visible rows when newest are scope-invisible — "+
			"got count=%v which means batch-loop is not finding the project rows",
		int(count))

	mems, _ := resp["memories"].([]any)
	require.Len(t, mems, 2, "memories slice must carry exactly the 2 visible rows")

	// Both visible rows must be the older project-scoped fixtures, NOT the
	// newest private rows (which fail scope.Resolve).
	visibleContents := map[string]bool{}
	for _, raw := range mems {
		m, ok := raw.(map[string]any)
		require.True(t, ok)
		c, _ := m["content"].(string)
		visibleContents[c] = true
		ps, _ := m["privacy_scope"].(string)
		require.Equal(t, "project", ps,
			"every returned row must be project-scoped (private rows belong to other workstation)")
	}
	require.True(t, visibleContents["older project row A"], "older project row A must be returned")
	require.True(t, visibleContents["older project row B"], "older project row B must be returned")
}

func TestRecall_PrincipalPrivateInvisibleNewestDoNotTruncate_FlagOff(t *testing.T) {
	project := "pim-recall-backfill-" + uuid.NewString()
	env := newMemoryServerForT007(t, project)
	db := env.store.DB

	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")

	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, created_at)
			VALUES (?, ?, ?::jsonb, ?, now() - interval '5 minutes')`,
		project, "older visible row A", `[]`, "private",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, created_at)
			VALUES (?, ?, ?::jsonb, ?, now() - interval '4 minutes')`,
		project, "older visible row B", `[]`, "project",
	).Error)

	for i := 1; i <= 3; i++ {
		require.NoError(t, db.Exec(
			`INSERT INTO memories (project, content, tags, privacy_scope, owner_principal, owner_principal_kind, agent_visibility, created_at)
				VALUES (?, ?, ?::jsonb, ?, ?, ?, ?, now() - (?::int * interval '1 minute'))`,
			project, "newest principal-private row "+string(rune('0'+i)), `[]`, "project", "agent/bob", "agent", "private", i,
		).Error)
	}

	ctx := auth.WithIdentity(context.Background(),
		auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))
	args, err := json.Marshal(map[string]any{
		"action":  "search",
		"project": project,
		"limit":   2,
	})
	require.NoError(t, err)

	out, err := env.srv.handleRecall(ctx, args)
	require.NoError(t, err)

	var resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	require.Equal(t, 2, int(resp["count"].(float64)))

	mems := resp["memories"].([]any)
	contents := map[string]bool{}
	for _, raw := range mems {
		row := raw.(map[string]any)
		content := row["content"].(string)
		require.False(t, strings.Contains(content, "principal-private"),
			"cross-principal private row leaked through recall(action=search)")
		contents[content] = true
	}
	require.True(t, contents["older visible row A"])
	require.True(t, contents["older visible row B"])
}

func TestRecallMemory_PrincipalPrivateInvisibleAndSharedAttributed_FlagOff(t *testing.T) {
	project := "pim-recall-memory-" + uuid.NewString()
	env := newMemoryServerForT007(t, project)
	db := env.store.DB

	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "")
	t.Setenv("ENGRAM_VNEXT_ENABLED", "")

	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, owner_principal, owner_principal_kind, agent_visibility, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, ?, ?, now() - interval '2 minutes')`,
		project, "shared team memory", `["type:fact"]`, "project", "agent/bob", "agent", "shared",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, owner_principal, owner_principal_kind, agent_visibility, created_at)
			VALUES (?, ?, ?::jsonb, ?, ?, ?, ?, now() - interval '1 minute')`,
		project, "private bob memory", `["type:fact"]`, "project", "agent/bob", "agent", "private",
	).Error)

	ctx := auth.WithIdentity(context.Background(),
		auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent))
	args, err := json.Marshal(map[string]any{
		"query":   "memory",
		"project": project,
		"format":  "items",
		"limit":   5,
	})
	require.NoError(t, err)

	out, err := env.srv.handleRecallMemory(ctx, args)
	require.NoError(t, err)

	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, "shared team memory", rows[0]["content"])
	require.Equal(t, "agent/bob", rows[0]["owner_principal"])
	require.Equal(t, "agent", rows[0]["owner_principal_kind"])
	require.Equal(t, "shared", rows[0]["agent_visibility"])
}
