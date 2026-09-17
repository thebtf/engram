package worker

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	booksdomain "github.com/thebtf/engram/internal/books"
	gormlib "gorm.io/gorm"
)

type booksStore interface {
	GetStatus(ctx context.Context, id int64) (*booksdomain.Job, error)
}

type bookErrorResponse struct {
	Error string `json:"error"`
}

type bookJobResponse struct {
	ID                  int64  `json:"id"`
	Status              string `json:"status"`
	SourceRef           string `json:"source_ref"`
	Error               string `json:"error,omitempty"`
	CreatedAt           string `json:"created_at,omitempty"`
	UpdatedAt           string `json:"updated_at,omitempty"`
	DocumentsPathPrefix string `json:"documents_path_prefix,omitempty"`
	DocumentsLink       string `json:"documents_link,omitempty"`
}

func (s *Service) currentBooksStore() booksStore {
	if s == nil {
		return nil
	}
	s.initMu.RLock()
	defer s.initMu.RUnlock()
	return s.booksStore
}

func writeBookError(w http.ResponseWriter, status int, message string) {
	writeJSONStatus(w, status, bookErrorResponse{Error: message})
}

func bookRFC3339(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func bookJobResponseFromDomain(job *booksdomain.Job) bookJobResponse {
	if job == nil {
		return bookJobResponse{}
	}
	return bookJobResponse{
		ID:                  job.ID,
		Status:              string(job.Status),
		SourceRef:           job.SourceRef,
		Error:               strings.TrimSpace(job.Error),
		CreatedAt:           bookRFC3339(job.CreatedAt),
		UpdatedAt:           bookRFC3339(job.UpdatedAt),
		DocumentsPathPrefix: booksdomain.DocumentPathPrefix(job.ID),
		DocumentsLink:       "/documents",
	}
}

func parseBookIDParam(r *http.Request) (int64, error) {
	return parseGraphIDParam(r, "id")
}

func (s *Service) handleGetBookJobStatus(w http.ResponseWriter, r *http.Request) {
	store := s.currentBooksStore()
	if store == nil {
		writeBookError(w, http.StatusServiceUnavailable, "books status store not available")
		return
	}

	jobID, err := parseBookIDParam(r)
	if err != nil {
		writeBookError(w, http.StatusBadRequest, err.Error())
		return
	}

	job, err := store.GetStatus(r.Context(), jobID)
	if err != nil {
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			writeBookError(w, http.StatusNotFound, "book job not found")
			return
		}
		writeBookError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSONStatus(w, http.StatusOK, bookJobResponseFromDomain(job))
}
