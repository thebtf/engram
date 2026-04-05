// Package worker provides indexed session REST handlers for the dashboard.
package worker

import (
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/sessions"
)

const (
	maxSessionsLimit = 200 // Server-side cap to prevent huge response payloads.
)

// handleListIndexedSessions godoc
// @Summary List indexed sessions
// @Description Returns indexed Claude Code sessions with optional project and workstation filters.
// @Tags Sessions
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Filter by project ID"
// @Param workstation query string false "Filter by workstation ID"
// @Param limit query int false "Number of results (default 20, max 200)"
// @Param offset query int false "Pagination offset"
// @Success 200 {array} object
// @Failure 500 {string} string "internal error"
// @Router /api/sessions-index [get]
func (s *Service) handleListIndexedSessions(w http.ResponseWriter, r *http.Request) {
	if s.sessionIdxStore == nil {
		http.Error(w, "session indexing not configured", http.StatusServiceUnavailable)
		return
	}

	limit := 20
	if val := r.URL.Query().Get("limit"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			limit = min(parsed, maxSessionsLimit)
		}
	}
	offset := 0
	if val := r.URL.Query().Get("offset"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	opts := sessions.ListOptions{
		WorkstationID: r.URL.Query().Get("workstation"),
		ProjectID:     r.URL.Query().Get("project"),
		Limit:         limit,
		Offset:        offset,
	}

	list, err := s.sessionIdxStore.ListSessions(r.Context(), opts)
	if err != nil {
		log.Error().Err(err).Msg("list indexed sessions failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	type sessionItem struct {
		ID            string `json:"id"`
		WorkstationID string `json:"workstation_id"`
		ProjectID     string `json:"project_id"`
		ProjectPath   string `json:"project_path,omitempty"`
		ExchangeCount int    `json:"exchange_count"`
		GitBranch     string `json:"git_branch,omitempty"`
		LastMsgAt     string `json:"last_msg_at,omitempty"`
	}

	out := make([]sessionItem, 0, len(list))
	for _, sess := range list {
		item := sessionItem{
			ID:            sess.ID,
			WorkstationID: sess.WorkstationID,
			ProjectID:     sess.ProjectID,
			ExchangeCount: sess.ExchangeCount,
		}
		if sess.ProjectPath.Valid {
			item.ProjectPath = sess.ProjectPath.String
		}
		if sess.GitBranch.Valid {
			item.GitBranch = sess.GitBranch.String
		}
		if sess.LastMsgAt.Valid {
			item.LastMsgAt = sess.LastMsgAt.Time.UTC().Format(time.RFC3339)
		}
		out = append(out, item)
	}

	writeJSON(w, out)
}

// handleSearchIndexedSessions godoc
// @Summary Full-text search across session transcripts
// @Description Searches indexed session content using PostgreSQL full-text search.
// @Tags Sessions
// @Produce json
// @Security ApiKeyAuth
// @Param query query string true "Search query"
// @Param project query string false "Filter by project"
// @Param limit query int false "Number of results (default 10)"
// @Success 200 {array} object
// @Failure 400 {string} string "bad request"
// @Failure 500 {string} string "internal error"
// @Router /api/sessions-index/search [get]
func (s *Service) handleSearchIndexedSessions(w http.ResponseWriter, r *http.Request) {
	if s.sessionIdxStore == nil {
		http.Error(w, "session indexing not configured", http.StatusServiceUnavailable)
		return
	}

	query := r.URL.Query().Get("query")
	if query == "" {
		http.Error(w, "query parameter is required", http.StatusBadRequest)
		return
	}

	limit := 10
	if val := r.URL.Query().Get("limit"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			limit = min(parsed, maxSessionsLimit)
		}
	}

	results, err := s.sessionIdxStore.SearchSessions(r.Context(), query, limit)
	if err != nil {
		log.Error().Err(err).Str("query", query).Msg("search indexed sessions failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	type sessionResult struct {
		ID            string  `json:"id"`
		WorkstationID string  `json:"workstation_id"`
		ProjectPath   string  `json:"project_path,omitempty"`
		ExchangeCount int     `json:"exchange_count"`
		Rank          float64 `json:"rank"`
		Snippet       string  `json:"snippet,omitempty"`
	}

	out := make([]sessionResult, 0, len(results))
	for _, res := range results {
		sr := sessionResult{
			ID:            res.Session.ID,
			WorkstationID: res.Session.WorkstationID,
			ExchangeCount: res.Session.ExchangeCount,
			Rank:          res.Rank,
		}
		if res.Session.ProjectPath.Valid {
			sr.ProjectPath = res.Session.ProjectPath.String
		}
		if res.Session.Content.Valid && len(res.Session.Content.String) > 0 {
			snippet := res.Session.Content.String
			if len(snippet) > 200 {
				snippet = snippet[:200]
			}
			sr.Snippet = snippet
		}
		out = append(out, sr)
	}

	writeJSON(w, out)
}
