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
