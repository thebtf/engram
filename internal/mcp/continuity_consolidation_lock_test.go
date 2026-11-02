package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	gormlib "gorm.io/gorm"
)

type continuityConsolidationProbeKey struct{}

// This lock-only stand-in covers consolidationLock's sorted EXCLUSIVE relation
// order (audit_log before memories), not the full consolidation workflow.
func TestContinuitySlotClearConcurrentConsolidationRelationLocksPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("ENGRAM_MEMORY_MUTATION_TEST_DSN"))
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL/pgvector: set ENGRAM_MEMORY_MUTATION_TEST_DSN; DATABASE_DSN alone is never used")
	}
	expected, err := pgx.ParseConfig(dsn)
	require.True(t, err == nil, "INVALID: malformed explicit disposable-test DSN")
	require.NotEmpty(t, expected.Database, "INVALID: disposable-test DSN must resolve a database")
	require.NotEmpty(t, expected.User, "INVALID: disposable-test DSN must resolve a role")
	t.Setenv("DATABASE_DSN", dsn)
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "true")
	t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", "true")
	t.Cleanup(func() { reloadConfig(t) })
	reloadConfig(t)

	// Reuse the existing disposable-schema owner: it runs normal migrations in
	// a fresh schema and closes its pools before dropping only that owned schema.
	store, schema := openVaultCredentialTestStore(t)
	db := store.GetDB()
	var identity struct {
		Database string
		User     string
		Schema   string
	}
	require.NoError(t, db.Raw(`SELECT current_database() AS database, current_user AS "user", current_schema() AS schema`).Scan(&identity).Error)
	require.True(t, identity.Database == expected.Database, "INVALID: fixture database differs from explicit test DSN")
	require.True(t, identity.User == expected.User, "INVALID: fixture role differs from explicit test DSN")
	require.Equal(t, schema, identity.Schema, "INVALID: fixture did not select its owned schema")
	var ownedSchema, requiredTables int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_namespace n JOIN pg_roles r ON r.oid = n.nspowner
		WHERE n.nspname = ? AND r.rolname = current_user`, schema).Scan(&ownedSchema).Error)
	require.EqualValues(t, 1, ownedSchema, "INVALID: fixture role does not own its disposable schema")
	require.NoError(t, db.Raw(`SELECT count(*) FROM information_schema.tables WHERE table_schema = ?
		AND table_name IN ('audit_log', 'memories', 'project_continuity_slots')`, schema).Scan(&requiredTables).Error)
	require.EqualValues(t, 3, requiredTables, "INVALID: fixture tables must not fall back to public")
	t.Logf("fixture schema=%s; database and role match explicit test DSN; schema owner verified; normal migrations complete", schema)
	project := uuid.NewString()
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetMemoryStore(dbgorm.NewMemoryStore(store))
	slots := dbgorm.NewContinuitySlotStore(db)
	audits := dbgorm.NewAuditStore(db)
	srv.SetContinuitySlotStore(slots)
	srv.SetAuditStore(audits)
	env := continuitySlotTestEnv{srv: srv, store: store, slots: slots, audits: audits, project: project}
	t.Cleanup(func() {
		assert.NoError(t, db.Exec("DELETE FROM audit_log WHERE memory_id IN (SELECT id FROM memories WHERE project = ?)", project).Error)
		assert.NoError(t, db.Exec("DELETE FROM project_continuity_slots WHERE project = ?", project).Error)
		assert.NoError(t, db.Exec("DELETE FROM memories WHERE project = ?", project).Error)
		assert.NoError(t, db.Exec("DELETE FROM projects WHERE id = ?", project).Error)
		var remaining int64
		assert.NoError(t, db.Raw(`SELECT (SELECT count(*) FROM projects WHERE id = ?) +
			(SELECT count(*) FROM memories WHERE project = ?) +
			(SELECT count(*) FROM project_continuity_slots WHERE project = ?) +
			(SELECT count(*) FROM audit_log WHERE reason LIKE ?)`, project, project, project, "%project=\""+project+"\"%").Scan(&remaining).Error)
		assert.Zero(t, remaining, "test-owned rows must be removed")
		t.Logf("cleanup synthetic_project=%s remaining_rows=%d", project, remaining)
	})
	require.NoError(t, db.Create(&dbgorm.Project{ID: project}).Error)
	memory := env.createMemory(t, project, "agent/alice", "continuity-consolidation-lock", "project", "keycard-agent/alice")
	owner := continuitySlotContext(project, "agent/alice")
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	_, err = srv.callTool(owner, "continuity_slot", continuitySlotSetArgs(t, project, memory.ID, expires))
	require.NoError(t, err, "INVALID: initial authorized designation failed")
	var beforeTarget string
	require.NoError(t, db.Raw("SELECT to_jsonb(m)::text FROM memories m WHERE id = ?", memory.ID).Scan(&beforeTarget).Error)
	t.Logf("before=%s", continuityLockOrderSnapshot(t, db, project, memory.ID))

	ctx, cancel := context.WithTimeout(owner, 15*time.Second)
	defer cancel()
	clearCtx := context.WithValue(ctx, continuityConsolidationProbeKey{}, true)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var workers sync.WaitGroup
	clearPID, consolidationPID := 0, 0
	defer func() {
		unblock()
		cancel()
		workers.Wait()
		var active int64
		assert.NoError(t, db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE pid IN (?, ?) AND pid <> pg_backend_pid() AND xact_start IS NOT NULL`, clearPID, consolidationPID).Scan(&active).Error)
		assert.Zero(t, active, "test-owned transactions must be closed before row cleanup")
		t.Logf("cleanup clear_pid=%d consolidation_pid=%d open_transactions=%d", clearPID, consolidationPID, active)
	}()

	clearHeld := make(chan continuityLockOrderResult, 1)
	var holdOnce sync.Once
	callbacks := db.Callback().Query()
	callbackName := "continuity_consolidation_barrier_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, callbacks.After("gorm:query").Register(callbackName, func(tx *gormlib.DB) {
		if tx.Statement.Context.Value(continuityConsolidationProbeKey{}) != true || tx.Statement.Table != "project_continuity_slots" || tx.Error != nil || tx.RowsAffected == 0 {
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
	t.Cleanup(func() { assert.NoError(t, callbacks.Remove(callbackName)) })

	clearDone := make(chan continuityLockOrderResult, 1)
	consolidationDone := make(chan continuityLockOrderResult, 1)
	consolidationStarted := make(chan continuityLockOrderResult, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		out, err := srv.callTool(clearCtx, "continuity_slot", continuitySlotClearArgs(t, project))
		clearDone <- continuityLockOrderResult{output: out, err: err}
	}()
	held := continuityLockOrderAwait(t, ctx, clearHeld, "clear slot barrier")
	require.NoError(t, held.err, "INVALID: clear transaction PID")
	_, err = fmt.Sscan(held.output, &clearPID)
	require.NoError(t, err)
	workers.Add(1)
	go func() {
		defer workers.Done()
		err := db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
			var pid int
			err := tx.Raw("SELECT pg_backend_pid()").Scan(&pid).Error
			consolidationStarted <- continuityLockOrderResult{output: fmt.Sprint(pid), err: err}
			if err != nil {
				return err
			}
			if err := tx.Exec(`SET LOCAL lock_timeout='5s'`).Error; err != nil {
				return err
			}
			if err := tx.Exec(`LOCK TABLE "audit_log" IN EXCLUSIVE MODE`).Error; err != nil {
				return err
			}
			return tx.Exec(`LOCK TABLE "memories" IN EXCLUSIVE MODE`).Error
		})
		consolidationDone <- continuityLockOrderResult{err: err}
	}()
	started := continuityLockOrderAwait(t, ctx, consolidationStarted, "consolidation transaction PID")
	require.NoError(t, started.err, "INVALID: consolidation transaction PID")
	_, err = fmt.Sscan(started.output, &consolidationPID)
	require.NoError(t, err)
	require.NotEqual(t, clearPID, consolidationPID, "INVALID: operations share a backend")

	// Observe a real database wait without requiring which relation is first.
	// An audit-before-memory clear moves consolidation's wait to audit_log and is valid.
	var wait string
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
waitForLock:
	for {
		require.NoError(t, db.WithContext(ctx).Raw(`SELECT COALESCE((SELECT json_build_object(
			'pid', pid, 'wait_event_type', wait_event_type, 'wait_event', wait_event,
			'blockers', pg_blocking_pids(pid), 'query', query)::text
			FROM pg_stat_activity WHERE pid = ? AND state = 'active'
			AND wait_event_type = 'Lock' AND ? = ANY(pg_blocking_pids(pid))), '')`, consolidationPID, clearPID).Scan(&wait).Error, "INVALID: lock observer")
		if wait != "" {
			break waitForLock
		}
		select {
		case result := <-consolidationDone:
			t.Fatalf("INVALID: consolidation stand-in finished before observed wait: %v", result.err)
		case <-deadline.C:
			t.Fatal("INVALID: no database wait observed before releasing clear")
		case <-ctx.Done():
			t.Fatal("INVALID: fixture deadline before observed wait")
		case <-ticker.C:
		}
	}
	t.Logf("observed_lock_wait=%s", wait)
	var relationLocks string
	require.NoError(t, db.WithContext(ctx).Raw(`SELECT COALESCE(jsonb_agg(jsonb_build_object(
		'pid', pid, 'relation', relation::regclass::text, 'mode', mode, 'granted', granted)
		ORDER BY pid, relation, mode)::text, '[]') FROM pg_locks
		WHERE pid IN (?, ?) AND locktype = 'relation' AND relation IN ('audit_log'::regclass, 'memories'::regclass)`, clearPID, consolidationPID).Scan(&relationLocks).Error)
	t.Logf("relation_locks=%s", relationLocks)
	unblock()
	cleared := continuityLockOrderAwait(t, ctx, clearDone, "clear completion")
	consolidated := continuityLockOrderAwait(t, ctx, consolidationDone, "consolidation stand-in completion")
	t.Logf("clear output=%q error=%v", cleared.output, cleared.err)
	t.Logf("consolidation_lock_only error=%v", consolidated.err)
	t.Logf("after=%s", continuityLockOrderSnapshot(t, db, project, memory.ID))
	for _, result := range []continuityLockOrderResult{cleared, consolidated} {
		if result.err == nil {
			continue
		}
		var state interface{ SQLState() string }
		if errors.As(result.err, &state) && state.SQLState() == "40P01" {
			t.Fatalf("RED: authorized clear and consolidation relation-lock order deadlocked (SQLSTATE 40P01): %v", result.err)
		}
		t.Fatalf("INVALID: unexpected operation failure, not proven deadlock: %v", result.err)
	}
	require.JSONEq(t, `{"action":"clear","status":"ok"}`, cleared.output)
	_, err = slots.Get(context.Background(), project)
	require.ErrorIs(t, err, gormlib.ErrRecordNotFound)
	var afterTarget string
	require.NoError(t, db.Raw("SELECT to_jsonb(m)::text FROM memories m WHERE id = ?", memory.ID).Scan(&afterTarget).Error)
	require.Equal(t, beforeTarget, afterTarget, "clear and lock-only consolidation must not change target data")
	entries, err := audits.GetByMemory(context.Background(), memory.ID, 10)
	require.NoError(t, err)
	require.Len(t, entries, 2, "exactly one set and one committed clear audit")
	actions := make(map[string]int)
	for _, entry := range entries {
		actions[entry.Action]++
		require.Equal(t, "principal:agent:agent/alice", entry.Actor)
		require.Equal(t, "raw-session-42", entry.SourceSessionID)
		require.Nil(t, entry.BeforeState)
		require.Nil(t, entry.AfterState)
		require.Contains(t, entry.Reason, fmt.Sprintf("project=%q", project))
		require.Contains(t, entry.Reason, expires)
		require.NotContains(t, entry.Reason, memory.Content)
	}
	require.Equal(t, map[string]int{"continuity_slot_set": 1, "continuity_slot_clear": 1}, actions)
	t.Log("PASS: both operations committed without deadlock; slot cleared once, audits correct, target unchanged; lock-only consolidation scope")
}
