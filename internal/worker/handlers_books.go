package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	booksdomain "github.com/thebtf/engram/internal/books"
	gormlib "gorm.io/gorm"
)

const defaultBooksProject = "engram"

type booksStore interface {
	Create(ctx context.Context, sourceRef string) (*booksdomain.Job, error)
	GetStatus(ctx context.Context, id int64) (*booksdomain.Job, error)
	UpdateStatus(ctx context.Context, id int64, status booksdomain.Status, errorMessage string) (*booksdomain.Job, error)
}

type booksPipelineRunner interface {
	Process(ctx context.Context, req booksdomain.ProcessRequest) error
}

type createBookJobRequest struct {
	SourceRef string `json:"source_ref"`
	Content   string `json:"content"`
	Project   string `json:"project,omitempty"`
	Author    string `json:"author,omitempty"`
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

func (s *Service) currentBooksPipeline() booksPipelineRunner {
	if s == nil {
		return nil
	}
	s.initMu.RLock()
	defer s.initMu.RUnlock()
	return s.booksPipeline
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

func (s *Service) handleCreateBookJob(w http.ResponseWriter, r *http.Request) {
	store := s.currentBooksStore()
	pipeline := s.currentBooksPipeline()
	if store == nil || pipeline == nil {
		writeBookError(w, http.StatusServiceUnavailable, "books pipeline not available")
		return
	}

	var req createBookJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBookError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	sourceRef := strings.TrimSpace(req.SourceRef)
	if sourceRef == "" {
		writeBookError(w, http.StatusBadRequest, "source_ref required")
		return
	}

	project := strings.TrimSpace(req.Project)
	if project == "" {
		project = defaultBooksProject
	}
	author := strings.TrimSpace(req.Author)
	if author == "" {
		author = candidateReviewActorFromContext(r.Context())
	}
	if author == "" {
		author = "operator-console"
	}

	job, err := store.Create(r.Context(), sourceRef)
	if err != nil {
		writeBookError(w, http.StatusInternalServerError, err.Error())
		return
	}

	processReq := booksdomain.ProcessRequest{
		JobID:     job.ID,
		SourceRef: sourceRef,
		Content:   req.Content,
		Project:   project,
		Author:    author,
	}

	runCtx := context.Background()
	if s != nil && s.ctx != nil {
		runCtx = s.ctx
	}
	go func(next booksdomain.ProcessRequest) {
		if err := pipeline.Process(runCtx, next); err != nil {
			log.Warn().Err(err).
				Int64("job_id", next.JobID).
				Str("source_ref", next.SourceRef).
				Str("project", next.Project).
				Msg("books pipeline run failed")
		}
	}(processReq)

	writeJSONStatus(w, http.StatusAccepted, bookJobResponseFromDomain(job))
}

func (s *Service) handleGetBookJobStatus(w http.ResponseWriter, r *http.Request) {
	store := s.currentBooksStore()
	if store == nil {
		writeBookError(w, http.StatusServiceUnavailable, "books pipeline not available")
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
