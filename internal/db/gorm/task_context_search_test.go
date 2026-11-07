package gorm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"
)

func TestTaskContextDocumentSearchCurrentScopedBody(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewVersionedDocumentStore(&Store{DB: db})
	ctx := t.Context()
	project, other := uuid.NewString(), uuid.NewString()
	_, err := store.Create(ctx, "current.md", project, "obsolete hedgehog passage", "markdown", "{}", "fixture")
	require.NoError(t, err)
	body := strings.Repeat("meaningful hedgehog body ", 800)
	id, err := store.Create(ctx, "current.md", project, body, "markdown", "{}", "fixture")
	require.NoError(t, err)
	_, err = store.Create(ctx, "secret.md", other, "hedgehog confidential", "markdown", "{}", "fixture")
	require.NoError(t, err)
	rows, err := store.SearchTaskContext(ctx, project, "hedgehog", 3)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, id, rows[0].ID)
	require.Equal(t, 2, rows[0].Version)
	require.Equal(t, body, rows[0].Content)
	digest := sha256.Sum256([]byte(body))
	require.Equal(t, hex.EncodeToString(digest[:]), rows[0].ContentHash)
	rows, err = store.SearchTaskContext(ctx, project, "hedgehog absentuniquetoken", 3)
	require.NoError(t, err)
	require.Empty(t, rows, "query must not be widened into an arbitrary OR")
	rows, err = store.SearchTaskContext(ctx, project, "obsolete", 3)
	require.NoError(t, err)
	require.Empty(t, rows)
	rows, err = store.SearchTaskContext(ctx, other, "meaningful", 3)
	require.NoError(t, err)
	require.Empty(t, rows)
	_, err = store.Create(ctx, "rank-low.md", project, "rankneedle", "markdown", "{}", "fixture")
	require.NoError(t, err)
	highID, err := store.Create(ctx, "rank-high.md", project, "rankneedle rankneedle rankneedle rankneedle", "markdown", "{}", "fixture")
	require.NoError(t, err)
	_, err = store.Create(ctx, "rank-private.md", other, strings.Repeat("rankneedle ", 30), "markdown", "{}", "fixture")
	require.NoError(t, err)
	rows, err = store.SearchTaskContext(ctx, project, "rankneedle", 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, highID, rows[0].ID, "ranking and project scope must precede the limit")
}

func TestTaskContextDocumentSearchJITBudget(t *testing.T) {
	db := openCandidateTestDB(t)
	var poolSetting string
	require.NoError(t, db.Raw("SHOW jit").Scan(&poolSetting).Error)
	project := uuid.NewString()
	store := NewVersionedDocumentStore(&Store{DB: db})
	id, err := store.Create(t.Context(), "budget.md", project, "Alpha preserve the synthetic backup 😀", "markdown", "{}", "fixture")
	require.NoError(t, err)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	require.NoError(t, tx.Exec("SET LOCAL jit = on").Error)
	require.NoError(t, tx.Exec("SET LOCAL jit_above_cost = 0").Error)
	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	rows, err := NewVersionedDocumentStore(&Store{DB: tx}).SearchTaskContext(ctx, project, "Alpha", 3)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, id, rows[0].ID)
	var setting string
	require.NoError(t, tx.Raw("SHOW jit").Scan(&setting).Error)
	require.Equal(t, "on", setting, "search must not change the caller transaction's JIT setting")
	_, err = NewVersionedDocumentStore(&Store{DB: tx}).SearchTaskContext(t.Context(), uuid.NewString(), "Alpha", 3)
	require.ErrorIs(t, err, ErrKnowledgeProjectForbidden)
	require.NoError(t, tx.Raw("SHOW jit").Scan(&setting).Error)
	require.Equal(t, "on", setting, "failed search must not leave JIT disabled")
	cancelCtx, stop := context.WithCancel(t.Context())
	callbacks := db.Callback().Query()
	require.NoError(t, callbacks.Before("gorm:query").Register("task-context-cancel", func(q *gormlib.DB) {
		if q.Statement.Table == "latest" {
			stop()
		}
	}))
	_, err = NewVersionedDocumentStore(&Store{DB: tx}).SearchTaskContext(cancelCtx, project, "Alpha", 3)
	require.Error(t, err)
	require.ErrorIs(t, cancelCtx.Err(), context.Canceled)
	require.NoError(t, callbacks.Remove("task-context-cancel"))
	require.NoError(t, tx.Raw("SHOW jit").Scan(&setting).Error)
	require.Equal(t, "on", setting, "cancelled search must not leave JIT disabled")
	activeCtx, activeStop := context.WithCancel(t.Context())
	defer activeStop()
	require.NoError(t, callbacks.Before("gorm:query").Register("task-context-active-cancel", func(q *gormlib.DB) {
		if q.Statement.Table == "latest" {
			timer := time.AfterFunc(40*time.Millisecond, activeStop)
			q.AddError(q.Session(&gormlib.Session{NewDB: true}).WithContext(q.Statement.Context).Exec("SELECT pg_sleep(30)").Error)
			timer.Stop()
		}
	}))
	_, err = NewVersionedDocumentStore(&Store{DB: tx}).SearchTaskContext(activeCtx, project, "Alpha", 3)
	require.Error(t, err)
	require.ErrorIs(t, activeCtx.Err(), context.Canceled)
	require.NoError(t, callbacks.Remove("task-context-active-cancel"))
	err = tx.Raw("SHOW jit").Scan(&setting).Error
	if err == nil {
		require.Equal(t, "on", setting, "in-flight cancellation must not leave JIT disabled")
	} else {
		require.True(t, errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrTxDone), "unexpected unusable transaction error: %v", err)
	}
	var after string
	require.NoError(t, db.Raw("SHOW jit").Scan(&after).Error)
	require.Equal(t, poolSetting, after, "cancelled query must not leak settings into the pool")
}

func TestTaskContextNestedPrecancelPreservesCaller(t *testing.T) {
	db := openCandidateTestDB(t)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	project := uuid.NewString()
	require.NoError(t, tx.Create(&Project{ID: project}).Error)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := NewVersionedDocumentStore(&Store{DB: tx}).SearchTaskContext(ctx, project, "Alpha", 3)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, tx.Commit().Error, "pre-cancelled read must not abort caller writes")
	var count int64
	require.NoError(t, db.Model(&Project{}).Where("id = ?", project).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

type taskContextStallingConn struct {
	gormlib.Tx
	prefix   string
	observed bool
}

func (conn *taskContextStallingConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.HasPrefix(query, conn.prefix) {
		conn.observed = true
		if _, err := conn.Tx.ExecContext(ctx, "SELECT pg_sleep(2)"); err != nil {
			return nil, err
		}
	}
	return conn.Tx.ExecContext(ctx, query, args...)
}

func TestTaskContextNestedCleanupBounded(t *testing.T) {
	db := openCandidateTestDB(t)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	underlying, ok := tx.Statement.ConnPool.(gormlib.Tx)
	require.True(t, ok)
	stall := &taskContextStallingConn{Tx: underlying, prefix: "RELEASE SAVEPOINT task_context_"}
	tx.Statement.ConnPool = stall
	started := time.Now()
	err := taskContextNestedRead(t.Context(), tx, func(*gormlib.DB) error { return nil })
	elapsed := time.Since(started)
	require.True(t, stall.observed, "the actual release reached the stalled SQL seam")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, elapsed, 1500*time.Millisecond, "stalled cleanup exceeded its finite bound")
}

func TestTaskContextNestedRollbackBounded(t *testing.T) {
	db := openCandidateTestDB(t)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	underlying, ok := tx.Statement.ConnPool.(gormlib.Tx)
	require.True(t, ok)
	stall := &taskContextStallingConn{Tx: underlying, prefix: "ROLLBACK TO SAVEPOINT task_context_"}
	tx.Statement.ConnPool = stall
	failure := errors.New("synthetic read failure")
	started := time.Now()
	err := taskContextNestedRead(t.Context(), tx, func(*gormlib.DB) error { return failure })
	require.True(t, stall.observed, "the actual rollback reached the stalled SQL seam")
	require.ErrorIs(t, err, failure)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), 1500*time.Millisecond)
}

func TestTaskContextNestedPreparedCacheBounded(t *testing.T) {
	db := openCandidateTestDB(t).Session(&gormlib.Session{PrepareStmt: true})
	failure := errors.New("synthetic read failure")
	for index := range 4 {
		tx := db.Begin()
		require.NoError(t, tx.Error)
		prepared, ok := tx.Statement.ConnPool.(*gormlib.PreparedStmtTX)
		require.True(t, ok, "must exercise the production GORM prepared transaction")
		err := taskContextNestedRead(t.Context(), tx, func(*gormlib.DB) error {
			if index == 3 {
				return failure
			}
			return nil
		})
		if index == 3 {
			require.ErrorIs(t, err, failure)
		} else {
			require.NoError(t, err)
		}
		require.NoError(t, tx.Rollback().Error)
		var controls []string
		for _, key := range prepared.PreparedStmtDB.Stmts.Keys() {
			if strings.Contains(key, "SAVEPOINT task_context_") {
				controls = append(controls, key)
			}
		}
		t.Logf("prepared control statements after transaction %d: %d", index+1, len(controls))
		if index == 3 {
			require.Empty(t, controls, "per-read savepoint SQL must not enter the shared prepared statement cache")
		}
	}
}
