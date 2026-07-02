package books_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	booksdomain "github.com/thebtf/engram/internal/books"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/gorm/logger"
)

func openBooksTestStore(t *testing.T) *gormdb.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping books integration test")
	}

	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, LogLevel: logger.Warn})
	require.NoError(t, err, "open books integration store")
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})
	return store
}

func uniqueBookFixture(ext string) (project string, sourceRef string) {
	suffix := time.Now().UnixNano()
	return fmt.Sprintf("books-pipeline-%d", suffix), fmt.Sprintf("fixture-%d%s", suffix, ext)
}

func cleanupBooksArtifacts(t *testing.T, store *gormdb.Store, project, sourceRef string) {
	t.Helper()
	t.Cleanup(func() {
		db := store.GetDB()
		require.NoError(t, db.Exec("DELETE FROM versioned_documents WHERE project = ? AND path LIKE ?", project, "books/jobs/%").Error)
		require.NoError(t, db.Exec("DELETE FROM books_jobs WHERE source_ref = ?", sourceRef).Error)
	})
}

// TestBooksStore_CreateStartsPending is the T017 RED/GREEN anchor for the store:
// every new job must begin at pending before the pipeline mutates it.
func TestBooksStore_CreateStartsPending(t *testing.T) {
	store := openBooksTestStore(t)
	booksStore := gormdb.NewBooksStore(store)
	project, sourceRef := uniqueBookFixture(".md")
	cleanupBooksArtifacts(t, store, project, sourceRef)

	job, err := booksStore.Create(context.Background(), sourceRef)
	require.NoError(t, err)
	require.Equal(t, booksdomain.StatusPending, job.Status)
	require.Equal(t, sourceRef, job.SourceRef)
	require.Empty(t, job.Error)
	assert.NotZero(t, job.ID)

	persisted, err := booksStore.GetStatus(context.Background(), job.ID)
	require.NoError(t, err)
	assert.Equal(t, booksdomain.StatusPending, persisted.Status)
	assert.Equal(t, sourceRef, persisted.SourceRef)
}

// TestPipeline_ProcessSuccessWritesVersionedDocuments proves the happy path:
// pending -> processing -> done and produced documents carry source_book_job_id.
func TestPipeline_ProcessSuccessWritesVersionedDocuments(t *testing.T) {
	store := openBooksTestStore(t)
	booksStore := gormdb.NewBooksStore(store)
	documents := gormdb.NewVersionedDocumentStore(store)
	pipeline := booksdomain.NewPipeline(booksStore, documents)
	project, sourceRef := uniqueBookFixture(".md")
	cleanupBooksArtifacts(t, store, project, sourceRef)

	job, err := booksStore.Create(context.Background(), sourceRef)
	require.NoError(t, err)

	content := "# Chapter 1\n\nDistributed systems start with message passing.\n\n## Vector clocks\n\nVector clocks capture causal ordering across nodes.\n\n## Two phase commit\n\nTwo phase commit coordinates distributed agreement.\n"
	err = pipeline.Process(context.Background(), booksdomain.ProcessRequest{
		JobID:     job.ID,
		SourceRef: sourceRef,
		Content:   content,
		Project:   project,
		Author:    "operator-test",
	})
	require.NoError(t, err)

	status, err := booksStore.GetStatus(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, booksdomain.StatusDone, status.Status)
	require.Empty(t, status.Error)

	docs, err := documents.List(context.Background(), project, "", booksdomain.DocumentPathPrefix(job.ID), 50)
	require.NoError(t, err)
	require.NotEmpty(t, docs, "pipeline must write chunk documents into VersionedDocumentStore")
	assert.True(t, strings.HasPrefix(docs[0].Path, booksdomain.DocumentPathPrefix(job.ID)), "document path should visibly tag the source book job")

	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(docs[0].Metadata), &metadata))
	assert.Equal(t, float64(job.ID), metadata["source_book_job_id"])
	assert.Equal(t, sourceRef, metadata["source_ref"])
	assert.Equal(t, float64(len(docs)), metadata["chunk_total"], "metadata should carry chunk_total provenance")
}

// TestPipeline_ProcessFailureMarksJobFailed proves the failed extraction path:
// unsupported input transitions to failed and persists the error message.
func TestPipeline_ProcessFailureMarksJobFailed(t *testing.T) {
	store := openBooksTestStore(t)
	booksStore := gormdb.NewBooksStore(store)
	documents := gormdb.NewVersionedDocumentStore(store)
	pipeline := booksdomain.NewPipeline(booksStore, documents)
	project, sourceRef := uniqueBookFixture(".pdf")
	cleanupBooksArtifacts(t, store, project, sourceRef)

	job, err := booksStore.Create(context.Background(), sourceRef)
	require.NoError(t, err)

	err = pipeline.Process(context.Background(), booksdomain.ProcessRequest{
		JobID:     job.ID,
		SourceRef: sourceRef,
		Content:   "%PDF-1.7 placeholder bytes",
		Project:   project,
		Author:    "operator-test",
	})
	require.Error(t, err)

	status, statusErr := booksStore.GetStatus(context.Background(), job.ID)
	require.NoError(t, statusErr)
	require.Equal(t, booksdomain.StatusFailed, status.Status)
	assert.Contains(t, status.Error, "unsupported source format")

	docs, docErr := documents.List(context.Background(), project, "", booksdomain.DocumentPathPrefix(job.ID), 50)
	require.NoError(t, docErr)
	assert.Empty(t, docs, "failed extraction must not emit documents")
}

type fakeBooksStore struct {
	statuses []booksdomain.Status
	errors   []string
}

func (f *fakeBooksStore) Create(context.Context, string) (*booksdomain.Job, error) {
	return &booksdomain.Job{ID: 1, Status: booksdomain.StatusPending}, nil
}

func (f *fakeBooksStore) GetStatus(context.Context, int64) (*booksdomain.Job, error) {
	return &booksdomain.Job{ID: 1, Status: booksdomain.StatusPending}, nil
}

func (f *fakeBooksStore) UpdateStatus(_ context.Context, _ int64, status booksdomain.Status, errorMessage string) (*booksdomain.Job, error) {
	f.statuses = append(f.statuses, status)
	f.errors = append(f.errors, errorMessage)
	return &booksdomain.Job{ID: 1, Status: status, Error: errorMessage}, nil
}

type fakeDocumentWriter struct {
	creates      int
	cleanupJobID int64
	failAfter    int
}

func (f *fakeDocumentWriter) Create(context.Context, string, string, string, string, string, string) (int64, error) {
	f.creates++
	if f.failAfter > 0 && f.creates > f.failAfter {
		return 0, fmt.Errorf("writer failure")
	}
	return int64(f.creates), nil
}

func (f *fakeDocumentWriter) DeleteBySourceBookJobID(_ context.Context, jobID int64) (int64, error) {
	f.cleanupJobID = jobID
	return int64(f.creates - 1), nil
}

func TestPipeline_ProcessFailureCleansPartialWrites(t *testing.T) {
	store := &fakeBooksStore{}
	writer := &fakeDocumentWriter{failAfter: 1}
	pipeline := booksdomain.NewPipeline(store, writer)
	content := "# Chapter 1\n\n" + strings.Repeat("alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu\n", 400) + "\n# Chapter 2\n\n" + strings.Repeat("nu xi omicron pi rho sigma tau upsilon phi chi psi omega\n", 400)
	err := pipeline.Process(context.Background(), booksdomain.ProcessRequest{JobID: 7, SourceRef: "book.md", Content: content, Project: "engram", Author: "tester"})
	require.Error(t, err)
	require.Equal(t, int64(7), writer.cleanupJobID)
	require.Contains(t, store.errors[len(store.errors)-1], "writer failure")
}
