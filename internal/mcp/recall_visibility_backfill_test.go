package mcp

import (
	"context"
	"encoding/json"
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

	// Insert 2 older project-scoped rows first (created_at DESC means newest
	// last-inserted; older rows come first in chronological order). These are
	// always visible to anyone in the project.
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, source_workstation_id)
			VALUES (?, ?, ?::jsonb, ?, ?)`,
		project, "older project row A", `[]`, "project", "",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, source_workstation_id)
			VALUES (?, ?, ?::jsonb, ?, ?)`,
		project, "older project row B", `[]`, "project", "",
	).Error)

	// Insert 3 newest private rows owned by a DIFFERENT workstation. These
	// are the rows that would truncate the recall under the old single-call
	// path because they fail scope.Resolve and are the freshest by created_at.
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, source_workstation_id)
			VALUES (?, ?, ?::jsonb, ?, ?)`,
		project, "newest private row 1", `[]`, "private", "other-workstation",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, source_workstation_id)
			VALUES (?, ?, ?::jsonb, ?, ?)`,
		project, "newest private row 2", `[]`, "private", "other-workstation",
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO memories (project, content, tags, privacy_scope, source_workstation_id)
			VALUES (?, ?, ?::jsonb, ?, ?)`,
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
