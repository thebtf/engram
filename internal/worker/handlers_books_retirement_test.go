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

func booksRetirementRouter(service *Service) *chi.Mux {
	router := chi.NewRouter()
	router.Post("/api/books", service.handleCreateBookJob)
	router.Get("/api/books/{id}/status", service.handleGetBookJobStatus)
	return router
}

func TestHandlersBooksRetireAdmissionAndPreserveHistoricalStatus(t *testing.T) {
	job := &booksdomain.Job{ID: 7, Status: booksdomain.StatusFailed, SourceRef: "legacy.md", Error: booksdomain.RetirementFailureReason, UpdatedAt: time.Now()}
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart_%t", restart), func(t *testing.T) {
			service := &Service{booksStore: historicalBookStatusStore{job: job}}
			router := booksRetirementRouter(service)

			writer := httptest.NewRecorder()
			router.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/api/books", nil))
			require.Equal(t, http.StatusGone, writer.Code, writer.Body.String())
			assert.Contains(t, writer.Body.String(), "retired")

			reader := httptest.NewRecorder()
			router.ServeHTTP(reader, httptest.NewRequest(http.MethodGet, "/api/books/7/status", nil))
			require.Equal(t, http.StatusOK, reader.Code, reader.Body.String())
			assert.Contains(t, reader.Body.String(), booksdomain.DocumentPathPrefix(job.ID))
			assert.Contains(t, reader.Body.String(), booksdomain.RetirementFailureReason)
		})
	}
}
