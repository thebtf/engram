package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	gormlib "gorm.io/gorm"
)

type continuityLockOrderContextKey struct{}

type continuityLockOrderResult struct {
	output string
	err    error
}

// The barrier delays a real clear after its slot lookup, without prescribing
// whether clear must first lock the memory. Either safe serialization is valid.
func TestContinuitySlotClearConcurrentAuthorizedInvalidationPostgres(t *testing.T) {
	dsn := os.Getenv("ENGRAM_MEMORY_MUTATION_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_MEMORY_MUTATION_TEST_DSN not set; requires explicit disposable PostgreSQL")
	}
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "true")
	t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", "true")
	t.Cleanup(func() { reloadConfig(t) })
	reloadConfig(t)

	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 4})
	require.NoError(t, err, "INVALID: fixture connection or normal migrations failed; not a behavioral RED")
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	db := store.GetDB()
	var database string
	require.NoError(t, db.Raw("SELECT current_database()").Scan(&database).Error, "INVALID: fixture identity lookup")
	t.Logf("fixture database=%s", database)
	var foreignKey string
	require.NoError(t, db.Raw(`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'audit_log'::regclass AND conname = 'fk_audit_log_memory'
		AND contype = 'f' AND convalidated AND NOT condeferrable`).Scan(&foreignKey).Error)
	require.Contains(t, foreignKey, "FOREIGN KEY (memory_id) REFERENCES memories(id) ON DELETE SET NULL", "INVALID: normal immediate audit FK is missing")
	t.Logf("audit FK=%s", foreignKey)

	project := uuid.NewString()
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetMemoryStore(dbgorm.NewMemoryStore(store))
	slots := dbgorm.NewContinuitySlotStore(db)
	audits := dbgorm.NewAuditStore(db)
	srv.SetContinuitySlotStore(slots)
	srv.SetAuditStore(audits)
	env := continuitySlotTestEnv{srv: srv, store: store, slots: slots, audits: audits, project: project}
	t.Cleanup(func() {
		require.NoError(t, db.Exec("DELETE FROM audit_log WHERE memory_id IN (SELECT id FROM memories WHERE project = ?)", project).Error)
		require.NoError(t, db.Exec("DELETE FROM project_continuity_slots WHERE project = ?", project).Error)
		require.NoError(t, db.Exec("DELETE FROM memories WHERE project = ?", project).Error)
		require.NoError(t, db.Exec("DELETE FROM projects WHERE id = ?", project).Error)
	})
	require.NoError(t, db.Create(&dbgorm.Project{ID: project}).Error)
	memory := env.createMemory(t, project, "agent/alice", "continuity-lock-order", "project", "keycard-agent/alice")
	owner := continuitySlotContext(project, "agent/alice")
	clearArgs := continuitySlotClearArgs(t, project)
	suppressArgs, err := json.Marshal(map[string]any{"action": "suppress", "id": memory.ID, "project": project})
	require.NoError(t, err)
	_, err = srv.callTool(owner, "continuity_slot", continuitySlotSetArgs(t, project, memory.ID, time.Now().UTC().Add(time.Hour).Format(time.RFC3339)))
	require.NoError(t, err, "INVALID: authorized initial designation failed")
	before := continuityLockOrderSnapshot(t, db, project, memory.ID)
	denied := continuitySlotContext(project, "agent/bob")
	_, err = srv.callTool(denied, "continuity_slot", clearArgs)
	require.EqualError(t, err, continuitySlotAuthorizationError)
	_, err = srv.callTool(denied, "feedback", suppressArgs)
	require.ErrorContains(t, err, "not found")
	require.Equal(t, before, continuityLockOrderSnapshot(t, db, project, memory.ID), "denied operations must preserve memory, slot, audits and bulk snapshots")
	t.Logf("before=%s", before)

	ctx, cancel := context.WithTimeout(owner, 15*time.Second)
	defer cancel()
	clearCtx := context.WithValue(ctx, continuityLockOrderContextKey{}, "clear")
	invalidateCtx := context.WithValue(ctx, continuityLockOrderContextKey{}, "invalidate")
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var workers sync.WaitGroup
	defer func() {
		unblock()
		cancel()
		workers.Wait()
	}()
	clearHeld := make(chan continuityLockOrderResult, 1)
	invalidatePID := make(chan continuityLockOrderResult, 1)
	var holdOnce, pidOnce sync.Once
	callbacks := db.Callback().Query()
	marker := strings.ReplaceAll(uuid.NewString(), "-", "")
	beforeName, afterName := "continuity_lock_before_"+marker, "continuity_lock_after_"+marker
	require.NoError(t, callbacks.Before("gorm:query").Register(beforeName, func(tx *gormlib.DB) {
		if tx.Statement.Context.Value(continuityLockOrderContextKey{}) != "invalidate" {
			return
		}
		pidOnce.Do(func() {
			var pid int
			err := tx.Session(&gormlib.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error
			invalidatePID <- continuityLockOrderResult{output: fmt.Sprint(pid), err: err}
		})
	}))
	require.NoError(t, callbacks.After("gorm:query").Register(afterName, func(tx *gormlib.DB) {
		if tx.Statement.Context.Value(continuityLockOrderContextKey{}) != "clear" || tx.Statement.Table != "project_continuity_slots" || tx.Error != nil || tx.RowsAffected == 0 {
			return
		}
		holdOnce.Do(func() {
			var pid int
			err := tx.Session(&gormlib.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error
			clearHeld <- continuityLockOrderResult{output: fmt.Sprint(pid), err: err}
			select {
			case <-release:
			case <-ctx.Done():
				tx.AddError(ctx.Err())
			}
		})
	}))
	t.Cleanup(func() {
		require.NoError(t, callbacks.Remove(beforeName))
		require.NoError(t, callbacks.Remove(afterName))
	})

	clearDone := make(chan continuityLockOrderResult, 1)
	invalidateDone := make(chan continuityLockOrderResult, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		out, err := srv.callTool(clearCtx, "continuity_slot", clearArgs)
		clearDone <- continuityLockOrderResult{output: out, err: err}
	}()
	held := continuityLockOrderAwait(t, ctx, clearHeld, "clear slot barrier")
	require.NoError(t, held.err, "INVALID: clear transaction PID lookup")
	workers.Add(1)
	go func() {
		defer workers.Done()
		out, err := srv.callTool(invalidateCtx, "feedback", suppressArgs)
		invalidateDone <- continuityLockOrderResult{output: out, err: err}
	}()
	waiter := continuityLockOrderAwait(t, ctx, invalidatePID, "invalidation transaction PID")
	require.NoError(t, waiter.err, "INVALID: invalidation transaction PID lookup")
	require.NotEqual(t, held.output, waiter.output, "INVALID: concurrent operations share a backend")

	// Observe the second operation waiting on the first backend, not a specific
	// table: a corrected memory-before-slot clear naturally moves this wait.
	var lockWait string
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
waitForLock:
	for {
		require.NoError(t, db.WithContext(ctx).Raw(`SELECT COALESCE((SELECT json_build_object(
			'pid', pid, 'wait_event_type', wait_event_type, 'wait_event', wait_event,
			'blockers', pg_blocking_pids(pid), 'query', query)::text
			FROM pg_stat_activity WHERE pid = ?::integer AND state = 'active'
			AND wait_event_type = 'Lock' AND ?::integer = ANY(pg_blocking_pids(pid))), '')`, waiter.output, held.output).Scan(&lockWait).Error, "INVALID: lock observer failed")
		if lockWait != "" {
			break waitForLock
		}
		select {
		case result := <-invalidateDone:
			t.Fatalf("INVALID: invalidation finished before observed contention: output=%q error=%v", result.output, result.err)
		case <-deadline.C:
			t.Fatal("INVALID: no database lock wait observed before releasing clear")
		case <-ctx.Done():
			t.Fatal("INVALID: fixture timed out before observing lock contention")
		case <-ticker.C:
		}
	}
	t.Logf("observed_lock_wait=%s", lockWait)
	unblock()
	cleared := continuityLockOrderAwait(t, ctx, clearDone, "clear completion")
	invalidated := continuityLockOrderAwait(t, ctx, invalidateDone, "invalidation completion")
	t.Logf("clear output=%q error=%v", cleared.output, cleared.err)
	t.Logf("invalidation output=%q error=%v", invalidated.output, invalidated.err)
	if invalidated.err == nil {
		require.Eventually(t, func() bool {
			var count int64
			err := db.Table("audit_log").Where("memory_id = ? AND action = 'delete'", memory.ID).Count(&count).Error
			return err == nil && count == 1
		}, 3*time.Second, 10*time.Millisecond, "INVALID: successful suppression's real async audit did not finish")
	}
	t.Logf("after=%s", continuityLockOrderSnapshot(t, db, project, memory.ID))
	for _, result := range []continuityLockOrderResult{cleared, invalidated} {
		if result.err == nil {
			continue
		}
		var state interface{ SQLState() string }
		if errors.As(result.err, &state) && state.SQLState() == "40P01" {
			t.Fatalf("RED: real PostgreSQL deadlock (SQLSTATE 40P01) between authorized clear and invalidation: %v", result.err)
		}
		t.Fatalf("INVALID: unexpected operation failure, not proven deadlock: %v", result.err)
	}
	require.JSONEq(t, `{"action":"clear","status":"ok"}`, cleared.output)
	require.Equal(t, fmt.Sprintf("Memory %d suppressed", memory.ID), invalidated.output)
	var row dbgorm.Memory
	require.NoError(t, db.Unscoped().First(&row, memory.ID).Error)
	require.NotNil(t, row.DeletedAt)
	require.Equal(t, memory.Content, row.Content)
	require.Equal(t, memory.Version+1, row.Version)
	_, err = slots.Get(context.Background(), project)
	require.ErrorIs(t, err, gormlib.ErrRecordNotFound)
	entries, err := audits.GetByMemory(context.Background(), memory.ID, 10)
	require.NoError(t, err)
	require.Len(t, entries, 3, "set, committed clear, and real suppression audit; no duplicate lifecycle clear")
	actions := make(map[string]int)
	for _, entry := range entries {
		actions[entry.Action]++
		if entry.Action == "continuity_slot_clear" {
			require.Equal(t, "principal:agent:agent/alice", entry.Actor)
			require.Equal(t, "raw-session-42", entry.SourceSessionID)
			require.Nil(t, entry.BeforeState)
			require.Nil(t, entry.AfterState)
			require.NotContains(t, entry.Reason, memory.Content)
		}
		if entry.Action == "delete" {
			require.NotNil(t, entry.BeforeState)
			require.Nil(t, entry.AfterState)
			require.Equal(t, "raw-session-42", entry.SourceSessionID)
		}
	}
	require.Equal(t, map[string]int{"continuity_slot_set": 1, "continuity_slot_clear": 1, "delete": 1}, actions)
	t.Log("PASS: both authorized operations committed safely; denied calls preserved snapshots; memory, slot and audit state correct")
}

func continuityLockOrderAwait(t *testing.T, ctx context.Context, results <-chan continuityLockOrderResult, phase string) continuityLockOrderResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-ctx.Done():
		t.Fatalf("INVALID: %s did not finish within fixture deadline: %v", phase, ctx.Err())
		return continuityLockOrderResult{}
	}
}

func continuityLockOrderSnapshot(t *testing.T, db *gormlib.DB, project string, memoryID int64) string {
	t.Helper()
	var snapshot string
	require.NoError(t, db.Raw(`SELECT jsonb_build_object(
		'memory', (SELECT to_jsonb(m) FROM memories m WHERE id = ?),
		'slot', (SELECT to_jsonb(s) FROM project_continuity_slots s WHERE project = ?),
		'audits', COALESCE((SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM audit_log a WHERE memory_id = ?), '[]'::jsonb),
		'bulk_snapshots', COALESCE((SELECT jsonb_agg(to_jsonb(b) ORDER BY snapshot_id) FROM bulk_op_snapshots b WHERE ? = ANY(affected_memory_ids)), '[]'::jsonb)
	)::text`, memoryID, project, memoryID, memoryID).Scan(&snapshot).Error, "INVALID: durable snapshot query failed")
	return snapshot
}
