package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestMemoryTransitionAccessSecurityPostgres(t *testing.T) {
	// Only the existing explicit disposable-fixture DSN opts into PostgreSQL.
	// No inherited production/mirror DSN or OpenClaw host is used by this test.
	db := memoryMutationPostgresDB(t)

	t.Run("foreign_principal_does_not_wait", func(t *testing.T) {
		f := newMemoryMutationSecurityFixture(t, db)
		target := f.seed(t, f.tokenA, "project", "private", "")
		witness := f.seed(t, f.tokenB, "project", "private", "")
		require.NoError(t, gormdb.NewContinuitySlotStore(db).Upsert(context.Background(), memoryMutationSlot(target)))
		read := f.serve(f.request(t, context.Background(), http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", target.ID, f.project), f.tokenB, nil))
		require.Equal(t, http.StatusNotFound, read.status, read.body)
		require.NotContains(t, read.body, target.Content)
		before := memoryMutationSnapshot(t, db)
		holder, _ := memoryMutationBeginLock(t, db, target.ID)
		defer func() { _ = holder.Rollback().Error }()

		// The real router and bearer middleware must refuse before waiting on
		// an independently held foreign row, including for an ID-only caller.
		result := memoryMutationAwaitResult(t, memoryMutationStartRequest(t, f,
			http.MethodPost, fmt.Sprintf("/api/memories/%d/transition", target.ID), f.tokenB,
			map[string]any{"action": "suppress"}))
		require.Equal(t, http.StatusNotFound, result.status, result.body)
		require.NotContains(t, result.body, target.Content)
		require.NotContains(t, result.body, target.OwnerPrincipal)
		require.NotContains(t, result.body, witness.Content)
		memoryMutationAssertLockHeld(t, db, target.ID)
		require.Equal(t, before, memoryMutationSnapshot(t, db), "denial must preserve every memory field/version, saved slot and audit entry while the foreign lock is still held")
	})

	t.Run("owner_retains_slot_and_deleted_at", func(t *testing.T) {
		f := newMemoryMutationSecurityFixture(t, db)
		target := f.seed(t, f.tokenA, "project", "private", "")
		witness := f.seed(t, f.tokenB, "project", "private", "")
		require.Nil(t, target.DeletedAt)
		require.NoError(t, gormdb.NewContinuitySlotStore(db).Upsert(context.Background(), memoryMutationSlot(target)))
		before := memoryMutationSnapshot(t, db)
		for i, transition := range []struct{ action, status string }{
			{"suppress", "flagged"}, {"unsuppress", "active"}, {"archive", "archived"},
		} {
			result := f.serve(f.request(t, context.Background(), http.MethodPost,
				fmt.Sprintf("/api/memories/%d/transition", target.ID), f.tokenA,
				map[string]any{"action": transition.action}))
			require.Equal(t, http.StatusOK, result.status, result.body)
			var receipt memoryTransitionResponse
			require.NoError(t, json.Unmarshal([]byte(result.body), &receipt))
			require.Equal(t, memoryTransitionResponse{
				ID: target.ID, Action: transition.action, Status: transition.status,
				Version: target.Version + i + 1, Project: f.project,
			}, receipt)
			var after, witnessAfter gormdb.Memory
			require.NoError(t, db.First(&after, target.ID).Error)
			want := target
			want.Status, want.Version, want.UpdatedAt = transition.status, target.Version+i+1, after.UpdatedAt
			require.Equal(t, want, after, "a retained transition may change only status, version and updated_at")
			require.NoError(t, db.First(&witnessAfter, witness.ID).Error)
			require.Equal(t, witness, witnessAfter)
			state := memoryMutationSnapshot(t, db)
			require.Len(t, state.memories, len(before.memories))
			require.Equal(t, before.slots, state.slots, "retained transitions must not clear or rewrite the saved continuity slot")
			require.Equal(t, before.audit, state.audit, "retained transitions must not emit a DeleteTx slot-clear audit")
			read := f.serve(f.request(t, context.Background(), http.MethodGet,
				fmt.Sprintf("/api/memories/%d?project=%s", target.ID, f.project), f.tokenA, nil))
			require.Equal(t, http.StatusOK, read.status, read.body)
			require.Contains(t, read.body, target.Content)
		}
	})

	t.Run("different_memories_overlap_without_global_serialization", func(t *testing.T) {
		first := newMemoryMutationSecurityFixture(t, db)
		second := newMemoryMutationSecurityFixture(t, db)
		firstTarget := first.seed(t, first.tokenA, "project", "private", "")
		secondTarget := second.seed(t, second.tokenA, "project", "private", "")
		witness := first.seed(t, first.tokenB, "project", "private", "")
		for _, target := range []gormdb.Memory{firstTarget, secondTarget} {
			require.NoError(t, gormdb.NewContinuitySlotStore(db).Upsert(context.Background(), memoryMutationSlot(target)))
		}
		before := memoryMutationSnapshot(t, db)
		holder, holderPID := memoryMutationBeginLock(t, db, firstTarget.ID)
		defer func() { _ = holder.Rollback().Error }()
		firstResults := memoryMutationStartRequest(t, first, http.MethodPost,
			fmt.Sprintf("/api/memories/%d/transition", firstTarget.ID), first.tokenA,
			map[string]any{"action": "suppress"})
		memoryMutationAwaitBlocked(t, db, holderPID)

		// The first authorized route is already in a real PostgreSQL lock
		// wait. A different target must still commit before that holder releases.
		secondResult := memoryMutationAwaitResult(t, memoryMutationStartRequest(t, second,
			http.MethodPost, fmt.Sprintf("/api/memories/%d/transition", secondTarget.ID), second.tokenA,
			map[string]any{"action": "suppress"}))
		require.Equal(t, http.StatusOK, secondResult.status, secondResult.body)
		var firstWhileBlocked, secondAfter gormdb.Memory
		require.NoError(t, db.First(&firstWhileBlocked, firstTarget.ID).Error)
		require.Equal(t, firstTarget, firstWhileBlocked)
		require.NoError(t, db.First(&secondAfter, secondTarget.ID).Error)
		require.Equal(t, "flagged", secondAfter.Status)
		require.Equal(t, secondTarget.Version+1, secondAfter.Version)
		require.Equal(t, secondTarget.DeletedAt, secondAfter.DeletedAt)
		memoryMutationAssertLockHeld(t, db, firstTarget.ID)
		select {
		case early := <-firstResults:
			t.Fatalf("the first route finished before its independent row-lock holder released: HTTP %d: %s", early.status, early.body)
		default:
		}
		require.NoError(t, holder.Rollback().Error)
		firstResult := memoryMutationAwaitResult(t, firstResults)
		require.Equal(t, http.StatusOK, firstResult.status, firstResult.body)

		for _, target := range []gormdb.Memory{firstTarget, secondTarget} {
			var after gormdb.Memory
			require.NoError(t, db.First(&after, target.ID).Error)
			want := target
			want.Status, want.Version, want.UpdatedAt = "flagged", target.Version+1, after.UpdatedAt
			require.Equal(t, want, after)
		}
		var witnessAfter gormdb.Memory
		require.NoError(t, db.First(&witnessAfter, witness.ID).Error)
		require.Equal(t, witness, witnessAfter)
		state := memoryMutationSnapshot(t, db)
		require.Len(t, state.memories, len(before.memories))
		require.Equal(t, before.slots, state.slots)
		require.Equal(t, before.audit, state.audit)
	})
}
