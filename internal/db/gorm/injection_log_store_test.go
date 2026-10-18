package gorm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/models"
)

// TestInjectionLog_CitationLoopRewire is the CR-1 (provenance-cleanup) integration
// test: it proves the rewired citation loop works end-to-end through injection_log
// (mig 106) — the sole injection sink after the legacy observation_injections path
// was removed. DB-gated; skips without DATABASE_DSN.
//
// The flow under test (what processCitationsAsync + the injection path now do):
//  1. a memory is injected → InjectionLogStore.Record(session, project, [id])
//  2. MemoryStore.BatchIncrementInjected([id]) raises injection_count (drift T1 fix)
//  3. at session end, InjectionLogStore.GetBySession(session) returns that id so
//     citation detection can load the memory and score it.
//
// Anti-stub: asserts the specific seeded ID round-trips AND injection_count rose
// by exactly 1 — a stubbed Record/GetBySession/BatchIncrementInjected fails.
func TestInjectionLog_CitationLoopRewire(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	proj := sessionTestProject(t, db)
	defer db.Exec(`DELETE FROM injection_log WHERE project = ?`, proj)
	defer db.Exec(`DELETE FROM memories WHERE project = ?`, proj)

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	ils := NewInjectionLogStore(store)
	ctx := context.Background()

	// Seed a memory and capture its injection_count baseline.
	created, err := ms.Create(ctx, &models.Memory{
		Project: proj,
		Content: "CR-1 rewire: injection_log is the sole citation sink",
	})
	require.NoError(t, err)
	id := created.ID
	before, err := ms.Get(ctx, id)
	require.NoError(t, err)

	const sessionID = "test-injlog-cr1-session"

	// Step 1: record the injection to injection_log (the rewired sink).
	require.NoError(t, ils.Record(ctx, sessionID, proj, []int64{id}),
		"InjectionLogStore.Record must persist the injected memory id")

	// Step 2: increment injection_count (citation-rate denominator, drift T1).
	require.NoError(t, ms.BatchIncrementInjected(ctx, []int64{id}),
		"BatchIncrementInjected must raise injection_count")

	// Step 3: session-end read — GetBySession returns the injected id so
	// processCitationsAsync can load + score it.
	got, err := ils.GetBySession(ctx, sessionID)
	require.NoError(t, err)
	require.Contains(t, got, id,
		"GetBySession must return the injected memory id (citation read path)")

	// injection_count rose by exactly 1.
	after, err := ms.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, before.InjectionCount+1, after.InjectionCount,
		"injection_count must increment by exactly 1 after one injection")
}

func TestInjectionLog_RetentionPreservesConsolidationMembership(t *testing.T) {
	store := openStoreForStoreTest(t)
	logs := NewInjectionLogStore(store)
	ctx := context.Background()
	projectID := "b0d60abf-27f8-4b3c-8691-c6dc7b36385a"
	require.NoError(t, store.DB.Create(&Project{ID: projectID}).Error)
	for _, session := range []string{"retention-retained", "retention-expired", "retention-current"} {
		require.NoError(t, logs.Record(ctx, session, projectID, []int64{1}))
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -90)
	require.NoError(t, store.DB.Table("injection_log").
		Where("session_id IN ?", []string{"retention-retained", "retention-expired"}).
		Update("injected_at", cutoff.Add(-24*time.Hour)).Error)
	var retainedID, expiredID int64
	require.NoError(t, store.DB.Raw("SELECT id FROM injection_log WHERE session_id = ?", "retention-retained").Row().Scan(&retainedID))
	require.NoError(t, store.DB.Raw("SELECT id FROM injection_log WHERE session_id = ?", "retention-expired").Row().Scan(&expiredID))
	retainedKey := JSONRaw(fmt.Sprintf(`{"id":%d}`, retainedID))
	otherKey := JSONRaw(fmt.Sprintf(`{"id":%d}`, expiredID))
	memberships := []ConsolidationMembership{
		{Reference: "retention-injection", Table: "injection_log", RowKey: retainedKey, SourceRowKey: retainedKey, FieldPath: JSONRaw(`["project"]`), ProjectID: &projectID, PlanFingerprint: "retention-fixture"},
		{Reference: "retention-other-table", Table: "audit_log", RowKey: otherKey, SourceRowKey: otherKey, FieldPath: JSONRaw(`[""]`), ProjectID: &projectID, PlanFingerprint: "retention-fixture"},
	}
	require.NoError(t, store.DB.Create(&memberships).Error)

	deleted, err := logs.DeleteOlderThan(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	retained, err := logs.GetBySession(ctx, "retention-retained")
	require.NoError(t, err)
	require.Equal(t, []int64{1}, retained)
	expired, err := logs.GetBySession(ctx, "retention-expired")
	require.NoError(t, err)
	require.Empty(t, expired, "the same numeric key in another table must not retain the injection row")
	current, err := logs.GetBySession(ctx, "retention-current")
	require.NoError(t, err)
	require.Equal(t, []int64{1}, current)
}

// TestBatchIncrementInjected_NoOpOnEmpty locks the empty-slice guard (pure, no DB
// rows needed beyond the connection) so a fire-and-forget call with no ids is safe.
func TestBatchIncrementInjected_NoOpOnEmpty(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	ms := NewMemoryStore(&Store{DB: db})
	require.NoError(t, ms.BatchIncrementInjected(context.Background(), nil),
		"BatchIncrementInjected(nil) must be a no-op, not an error")
}
