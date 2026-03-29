package worker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/thebtf/engram/internal/logbuf"
)

// handleGetLogs godoc
// @Summary Get log entries
// @Description Returns log entries as JSON snapshot or SSE stream (follow mode). Supports level filtering and text search.
// @Tags System
// @Produce json
// @Security ApiKeyAuth
// @Param lines query int false "Number of entries (default 100, max 10000)"
// @Param level query string false "Minimum log level: trace, debug, info, warn, error, fatal"
// @Param query query string false "Case-insensitive text search"
// @Param follow query bool false "If true, switches to SSE streaming mode"
// @Success 200 {array} object
// @Failure 503 {string} string "log buffer not initialized"
// @Router /api/logs [get]
func (s *Service) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	if s.logBuffer == nil {
		http.Error(w, "log buffer not initialized", http.StatusServiceUnavailable)
		return
	}

	level := r.URL.Query().Get("level")
	query := r.URL.Query().Get("query")
	follow := r.URL.Query().Get("follow") == "true"

	if follow {
		s.handleLogsSSE(w, r, level, query)
		return
	}

	lines := 100
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 10000 {
				n = 10000
			}
			lines = n
		}
	}

	entries := s.logBuffer.Snapshot(lines, level, query)
	if entries == nil {
		entries = []logbuf.LogEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

// handleLogsSSE streams log entries as Server-Sent Events.
func (s *Service) handleLogsSSE(w http.ResponseWriter, r *http.Request, level, query string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsub := s.logBuffer.Subscribe()
	defer unsub()

	// Send initial keepalive
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case entry, ok := <-ch:
			if !ok {
				return
			}

			if level != "" && !logbuf.LevelAtLeast(entry.Level, level) {
				continue
			}
			if query != "" && !logbuf.EntryMatchesQuery(entry, query) {
				continue
			}

			data, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()

		case <-r.Context().Done():
			return
		}
	}
}
