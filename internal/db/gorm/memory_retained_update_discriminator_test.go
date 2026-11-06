package gorm

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/scope"
	gormlib "gorm.io/gorm"
)

func TestSyntheticRetainedBookkeepingPreservesIdentity(t *testing.T) {
	consolidationTestDatabaseConfig(t, os.Getenv("DATABASE_DSN"), "source")
	db, _ := openInterventionReceiptMigrationTestDB(t)
	ctx := t.Context()
	project := sessionTestProject(t, db)
	const legacy = "synthetic-former-alpha"
	oldUUID := uuid.NewString()
	seed := db.Begin()
	require.NoError(t, seed.Error)
	defer seed.Rollback()
	require.NoError(t, seed.Exec("SET LOCAL session_replication_role=replica").Error)
	rows := []Memory{
		{Project: legacy, Content: "synthetic historical row", Status: "active", PrivacyScope: "project", Version: 1},
		{Project: project, Content: "synthetic canonical row", Status: "active", PrivacyScope: "project", Version: 1},
		{Project: oldUUID, Content: "synthetic historical UUID row", Status: "active", PrivacyScope: "project", Version: 1},
		{Project: "synthetic-unresolved", Content: "synthetic unresolved row", Status: "active", PrivacyScope: "project", Version: 1},
		{Project: "", Content: "synthetic project-free row", Status: "active", PrivacyScope: "project", Version: 1},
	}
	for i := range rows {
		require.NoError(t, seed.Create(&rows[i]).Error)
	}
	for _, i := range []int{0, 2} {
		require.NoError(t, seed.Create(&ConsolidationMembership{
			Reference: fmt.Sprintf("urn:engram:fixture:bookkeeping:%d", i), Table: "memories",
			RowKey: JSONRaw(fmt.Sprintf(`{"id":%d}`, rows[i].ID)), FieldPath: JSONRaw(`["project"]`),
			ProjectID: &project, PlanFingerprint: "synthetic-bookkeeping",
		}).Error)
	}
	require.NoError(t, consolidationRedirect(seed, legacy, "", project, "synthetic-bookkeeping"))
	require.NoError(t, seed.Commit().Error)
	var role string
	require.NoError(t, db.Raw("SELECT current_setting('session_replication_role')").Scan(&role).Error)
	require.Equal(t, "origin", role)
	store := NewMemoryStore(&Store{DB: db})
	for _, i := range []int{0, 1, 2} {
		visible, err := store.GetVisible(ctx, rows[i].ID, project, scope.KeycardContext{}, scope.MemoryVisibilityOptions{})
		require.NoError(t, err)
		require.Equal(t, project, visible.Project)
		require.Equal(t, rows[i].Content, visible.Content)
	}
	capture := func(query string, args ...any) string {
		t.Helper()
		var raw string
		require.NoError(t, db.Raw(query, args...).Row().Scan(&raw))
		return raw
	}
	rowSQL := "SELECT row_to_json(m)::text FROM memories m WHERE m.id = ?"
	memberSQL := "SELECT row_to_json(p)::text FROM project_consolidation_memberships p WHERE p.table_name = 'memories' AND p.row_key->>'id' = ?"
	before := capture(rowSQL, rows[0].ID)
	membershipBefore := capture(memberSQL, fmt.Sprint(rows[0].ID))
	for i := 0; i < 3; i++ {
		updated, err := store.UpdateRecallBookkeeping(ctx, rows[i].ID, map[string]any{"access_count": gormlib.Expr("access_count + 1"), "last_retrieved_at": gormlib.Expr("now()")})
		require.NoError(t, err)
		require.Equal(t, i != 0, updated)
	}
	require.Equal(t, before, capture(rowSQL, rows[0].ID))
	require.Equal(t, membershipBefore, capture(memberSQL, fmt.Sprint(rows[0].ID)))
	require.NoError(t, store.BatchIncrementInjected(ctx, []int64{rows[0].ID, rows[1].ID, rows[2].ID}))
	require.Equal(t, before, capture(rowSQL, rows[0].ID), "mixed batch must preserve the historical row byte-for-byte")
	require.Equal(t, membershipBefore, capture(memberSQL, fmt.Sprint(rows[0].ID)))
	for _, i := range []int{1, 2} {
		var got Memory
		require.NoError(t, db.First(&got, rows[i].ID).Error)
		require.Equal(t, rows[i].Project, got.Project)
		require.EqualValues(t, 1, got.AccessCount)
		require.EqualValues(t, 1, got.InjectionCount)
	}
	unresolvedBefore := capture(rowSQL, rows[3].ID)
	updated, err := store.UpdateRecallBookkeeping(ctx, rows[3].ID, map[string]any{"access_count": gormlib.Expr("access_count + 1")})
	require.NoError(t, err)
	require.False(t, updated)
	require.NoError(t, store.BatchIncrementInjected(ctx, []int64{rows[3].ID}))
	require.Equal(t, unresolvedBefore, capture(rowSQL, rows[3].ID))
	require.ErrorContains(t, store.UpdateLifecycleFields(ctx, rows[3].ID, map[string]any{"access_count": gormlib.Expr("access_count + 1")}), "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")
	updated, err = store.UpdateRecallBookkeeping(ctx, rows[4].ID, map[string]any{"access_count": gormlib.Expr("access_count + 1")})
	require.NoError(t, err)
	require.True(t, updated)
	require.NoError(t, store.BatchIncrementInjected(ctx, []int64{rows[4].ID}))
	var global Memory
	require.NoError(t, db.First(&global, rows[4].ID).Error)
	require.Equal(t, "", global.Project)
	require.EqualValues(t, 1, global.AccessCount)
	require.EqualValues(t, 1, global.InjectionCount)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = store.UpdateRecallBookkeeping(canceled, rows[1].ID, map[string]any{"access_count": gormlib.Expr("access_count + 1")})
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, store.BatchIncrementInjected(canceled, []int64{rows[1].ID}), context.Canceled)
}

func TestSyntheticRetainedBookkeepingMembershipChange(t *testing.T) {
	consolidationTestDatabaseConfig(t, os.Getenv("DATABASE_DSN"), "source")
	db, _ := openInterventionReceiptMigrationTestDB(t)
	ctx := t.Context()
	project, other := sessionTestProject(t, db), sessionTestProject(t, db)
	const legacy = "synthetic-former-race"
	seed := db.Begin()
	require.NoError(t, seed.Error)
	defer seed.Rollback()
	require.NoError(t, seed.Exec("SET LOCAL session_replication_role=replica").Error)
	rows := make([]Memory, 4)
	for i := range rows {
		rows[i] = Memory{Project: legacy, Content: fmt.Sprintf("synthetic race %d", i), Status: "active", PrivacyScope: "project", Version: 1}
		require.NoError(t, seed.Create(&rows[i]).Error)
		require.NoError(t, seed.Create(&ConsolidationMembership{Reference: fmt.Sprintf("urn:engram:fixture:race:%d", i), Table: "memories", RowKey: JSONRaw(fmt.Sprintf(`{"id":%d}`, rows[i].ID)), FieldPath: JSONRaw(`["project"]`), ProjectID: &project, PlanFingerprint: "synthetic-race"}).Error)
	}
	require.NoError(t, consolidationRedirect(seed, legacy, "", project, "synthetic-race"))
	require.NoError(t, seed.Commit().Error)
	store := NewMemoryStore(&Store{DB: db})
	for _, row := range rows {
		visible, err := store.GetVisible(ctx, row.ID, project, scope.KeycardContext{}, scope.MemoryVisibilityOptions{})
		require.NoError(t, err)
		require.Equal(t, project, visible.Project)
	}
	for i, row := range rows {
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			key := fmt.Sprintf(`{"id":%d}`, row.ID)
			if i%2 == 0 {
				require.NoError(t, db.Create(&ConsolidationMembership{Reference: fmt.Sprintf("urn:engram:fixture:race:conflict:%d", i), Table: "memories", RowKey: JSONRaw(key), FieldPath: JSONRaw(`["project"]`), ProjectID: &other, PlanFingerprint: "synthetic-race"}).Error)
			} else {
				require.NoError(t, db.Where("table_name = ? AND row_key = ?::jsonb AND field_path = ?::jsonb", "memories", key, `["project"]`).Delete(&ConsolidationMembership{}).Error)
			}
			var before, after string
			require.NoError(t, db.Raw("SELECT row_to_json(m)::text FROM memories m WHERE m.id = ?", row.ID).Row().Scan(&before))
			if i < 2 {
				updated, err := store.UpdateRecallBookkeeping(ctx, row.ID, map[string]any{"access_count": gormlib.Expr("access_count + 1")})
				require.NoError(t, err)
				if updated {
					t.Errorf("recall wrote historical row after membership changed")
				}
			} else {
				require.NoError(t, store.BatchIncrementInjected(ctx, []int64{row.ID}))
			}
			require.NoError(t, db.Raw("SELECT row_to_json(m)::text FROM memories m WHERE m.id = ?", row.ID).Row().Scan(&after))
			t.Logf("before=%s after=%s", before, after)
			require.Equal(t, before, after)
		})
	}
}
