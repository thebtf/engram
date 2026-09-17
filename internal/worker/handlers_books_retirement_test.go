package worker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	booksdomain "github.com/thebtf/engram/internal/books"
)

type historicalBookStatusStore struct {
	job *booksdomain.Job
}

func (s historicalBookStatusStore) GetStatus(_ context.Context, id int64) (*booksdomain.Job, error) {
	if s.job == nil || s.job.ID != id {
		return nil, fmt.Errorf("book job %d not found", id)
	}
	return s.job, nil
}

type quiescedResidualBookStore struct {
	calls  int
	reason string
}

func (store *quiescedResidualBookStore) RetireNonterminal(_ context.Context, reason string) (int64, error) {
	store.calls++
	store.reason = reason
	return 2, nil
}

func TestRetireQuiescedBookJobsUsesNonDestructiveTransition(t *testing.T) {
	store := &quiescedResidualBookStore{}
	require.NoError(t, retireQuiescedBookJobs(context.Background(), store))
	require.Equal(t, 1, store.calls)
	require.Equal(t, booksdomain.RetirementFailureReason, store.reason)
}

func TestServiceRoutesRetireBookWriterAndPreserveHistoricalStatus(t *testing.T) {
	job := &booksdomain.Job{ID: 7, Status: booksdomain.StatusFailed, SourceRef: "legacy.md", Error: booksdomain.RetirementFailureReason, UpdatedAt: time.Now()}
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart_%t", restart), func(t *testing.T) {
			service := &Service{router: chi.NewRouter(), booksStore: historicalBookStatusStore{job: job}}
			service.setupRoutes()
			service.ready.Store(true)

			writer := httptest.NewRecorder()
			service.router.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/api/books", nil))
			require.Equal(t, http.StatusMethodNotAllowed, writer.Code, writer.Body.String())

			reader := httptest.NewRecorder()
			service.router.ServeHTTP(reader, httptest.NewRequest(http.MethodGet, "/api/books/7/status", nil))
			require.Equal(t, http.StatusOK, reader.Code, reader.Body.String())
			assert.Contains(t, reader.Body.String(), booksdomain.DocumentPathPrefix(job.ID))
			assert.Contains(t, reader.Body.String(), booksdomain.RetirementFailureReason)
		})
	}
}
