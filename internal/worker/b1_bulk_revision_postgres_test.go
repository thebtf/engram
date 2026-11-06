package worker

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/bulkops"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestB1TypedBulkRollbackPreservesRevisionHistoryPostgres(t *testing.T) {
	for _, op := range []models.SnapshotOpType{models.SnapshotOpBulkDelete, models.SnapshotOpBulkSupersede} {
		t.Run(string(op), func(t *testing.T) {
			f := newOwnCardContextFixture(t, false)
			owner := f.registered(t, "bulk-history")
			created := f.create(t, owner, ownCardContextPayload("typed bulk original"))
			path := "/api/memories/" + strconv.FormatInt(created.ID, 10)
			revised := ownCardContextDecode(t, f.call(t, http.MethodPatch, path, map[string]any{"version": created.Version, "content": "typed bulk revision"}, owner.caller()), http.StatusOK)
			snapshotStore := gormdb.NewSnapshotStore(f.store.DB)
			audit := gormdb.NewAuditStore(f.store.DB)
			facade := bulkops.NewFacade(snapshotStore, nil, f.service.memoryStore, audit)
			result, err := facade.Execute(t.Context(), auth.Admin(), bulkops.BulkOp{Type: op, MemoryIDs: []int64{created.ID}})
			require.NoError(t, err)
			require.Equal(t, 1, result.AffectedCount)
			var after gormdb.Memory
			require.NoError(t, f.store.DB.Unscoped().First(&after, created.ID).Error)
			require.Equal(t, revised.Version+1, after.Version)
			rollback, err := bulkops.Rollback(t.Context(), auth.Admin(), result.SnapshotID, snapshotStore, f.service.memoryStore, audit, nil)
			require.NoError(t, err)
			require.Equal(t, 1, rollback.RestoredCount)
			current := ownCardContextDecode(t, f.call(t, http.MethodGet, path, nil, owner.caller()), http.StatusOK)
			require.Equal(t, revised.Version, current.Version)
			require.Equal(t, revised.Content, current.Content)
			history := f.call(t, http.MethodGet, path+"/revisions", nil, owner.caller())
			require.Equal(t, http.StatusOK, history.Code)
			require.Contains(t, history.Body.String(), "typed bulk original")
			next := ownCardContextDecode(t, f.call(t, http.MethodPatch, path, map[string]any{"version": current.Version, "content": "typed bulk next"}, owner.caller()), http.StatusOK)
			require.Equal(t, current.Version+1, next.Version)
			history = f.call(t, http.MethodGet, path+"/revisions", nil, owner.caller())
			require.Equal(t, http.StatusOK, history.Code)
			require.Contains(t, history.Body.String(), "typed bulk original")
			require.Contains(t, history.Body.String(), "typed bulk revision")
		})
	}
}

func TestB1TypedRestoreSerializesWithRevisionPostgres(t *testing.T) {
	f := newOwnCardContextFixture(t, false)
	owner := f.registered(t, "restore-race")
	created := f.create(t, owner, ownCardContextPayload("restore race original"))
	store := f.service.memoryStore
	snapshot, err := store.GetForSnapshot(t.Context(), created.ID)
	require.NoError(t, err)
	pool, err := f.store.DB.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(4)
	tx := f.store.DB.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	var pid int
	require.NoError(t, tx.Raw("SELECT pg_backend_pid()").Scan(&pid).Error)
	var locked gormdb.Memory
	require.NoError(t, tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, created.ID).Error)
	caller := scope.KeycardContext{Principal: owner.identity.Principal, PrincipalKind: owner.identity.PrincipalKind, WorkstationID: owner.id}
	owning := models.MemoryContext{Kind: models.MemoryContextSelf, Principal: caller.Principal, PrincipalKind: caller.PrincipalKind}
	restoreResult := make(chan error, 1)
	reviseResult := make(chan error, 1)
	go func() { restoreResult <- store.RestoreRaw(context.Background(), snapshot) }()
	go func() {
		_, err := store.ReviseInContext(context.Background(), created.ID, owning, "", caller, created.Version, "restore race revised")
		reviseResult <- err
	}()
	var blocked int64
	var probeErr error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		probeErr = f.store.DB.Raw(`WITH RECURSIVE blocked(pid) AS (
			SELECT ?::integer
			UNION
			SELECT a.pid FROM pg_stat_activity a JOIN blocked b ON b.pid = ANY(pg_blocking_pids(a.pid))
			WHERE a.datname = current_database() AND a.pid <> ? AND a.wait_event_type = 'Lock'
		) SELECT count(*) - 1 FROM blocked`, pid, pid).Scan(&blocked).Error
		if probeErr == nil && blocked == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if probeErr != nil || blocked != 2 {
		var activities []struct {
			PID                                int
			WaitEventType, WaitEvent, Blockers string
		}
		_ = f.store.DB.Raw(`SELECT pid, COALESCE(wait_event_type, '') AS wait_event_type, COALESCE(wait_event, '') AS wait_event, pg_blocking_pids(pid)::text AS blockers FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()`).Scan(&activities).Error
		select {
		case err := <-restoreResult:
			t.Logf("restore completed early: %v", err)
		default:
			t.Log("restore still incomplete")
		}
		select {
		case err := <-reviseResult:
			t.Logf("revise completed early: %v", err)
		default:
			t.Log("revise still incomplete")
		}
		t.Fatalf("lock wait chain blocked=%d probe error=%v activities=%+v", blocked, probeErr, activities)
	}
	select {
	case err := <-restoreResult:
		t.Fatalf("restore completed while row lock held: %v", err)
	default:
	}
	select {
	case err := <-reviseResult:
		t.Fatalf("revise completed while row lock held: %v", err)
	default:
	}
	require.NoError(t, tx.Commit().Error)
	require.NoError(t, <-reviseResult)
	if err := <-restoreResult; err != nil {
		require.Contains(t, err.Error(), "typed content or version conflict")
	}
	path := fmt.Sprintf("/api/memories/%d", created.ID)
	current := ownCardContextDecode(t, f.call(t, http.MethodGet, path, nil, owner.caller()), http.StatusOK)
	require.Equal(t, "restore race revised", current.Content)
	require.Equal(t, created.Version+1, current.Version)
	history := f.call(t, http.MethodGet, path+"/revisions", nil, owner.caller())
	require.Equal(t, http.StatusOK, history.Code)
	require.Contains(t, history.Body.String(), "restore race original")
	stale := *snapshot
	stale.Content = current.Content
	require.ErrorContains(t, store.RestoreRaw(t.Context(), &stale), "typed revision conflict")
	require.ErrorContains(t, store.RestoreRaw(t.Context(), snapshot), "typed content or version conflict")
	require.Error(t, f.store.DB.Transaction(func(tx *gorm.DB) error { return store.RestoreRawTx(t.Context(), tx, snapshot) }))
}
