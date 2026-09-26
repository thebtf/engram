package gorm

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	booksdomain "github.com/thebtf/engram/internal/books"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	_ "modernc.org/sqlite"
)

func TestBooksStoreRetireNonterminalPreservesProvenanceAndIsIdempotent(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	db, err := gormlib.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gormlib.Config{SkipDefaultTransaction: true})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE books_jobs (
		id INTEGER PRIMARY KEY,
		status TEXT NOT NULL,
		source_ref TEXT NOT NULL,
		error TEXT,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE versioned_documents (
		id INTEGER PRIMARY KEY,
		metadata TEXT NOT NULL
	)`).Error)
	for _, row := range []struct {
		id     int64
		status booksdomain.Status
	}{
		{id: 1, status: booksdomain.StatusPending},
		{id: 2, status: booksdomain.StatusProcessing},
		{id: 3, status: booksdomain.StatusDone},
	} {
		require.NoError(t, db.Exec(
			"INSERT INTO books_jobs (id, status, source_ref, error, created_at, updated_at) VALUES (?, ?, ?, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)",
			row.id, string(row.status), "legacy.md",
		).Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO versioned_documents (id, metadata) VALUES (1, '{"source_book_job_id":1}')`).Error)

	store := &BooksStore{db: db}
	changed, err := store.RetireNonterminal(context.Background(), booksdomain.RetirementFailureReason)
	require.NoError(t, err)
	assert.Equal(t, int64(2), changed)

	for _, id := range []int64{1, 2} {
		job, statusErr := store.GetStatus(context.Background(), id)
		require.NoError(t, statusErr)
		assert.Equal(t, booksdomain.StatusFailed, job.Status)
		assert.Equal(t, booksdomain.RetirementFailureReason, job.Error)
	}
	done, err := store.GetStatus(context.Background(), 3)
	require.NoError(t, err)
	assert.Equal(t, booksdomain.StatusDone, done.Status)

	var documents int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM versioned_documents WHERE metadata = ?", `{"source_book_job_id":1}`).Scan(&documents).Error)
	assert.Equal(t, int64(1), documents)

	again, err := store.RetireNonterminal(context.Background(), booksdomain.RetirementFailureReason)
	require.NoError(t, err)
	assert.Zero(t, again)
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM versioned_documents WHERE metadata = ?", `{"source_book_job_id":1}`).Scan(&documents).Error)
	assert.Equal(t, int64(1), documents)
}
