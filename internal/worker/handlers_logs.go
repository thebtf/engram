package worker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/thebtf/engram/internal/logbuf"
)

// handleGetLogs godoc
// @Summary Get log entries or follow new entries
// @Description Without follow=true, returns a JSON array of recent matching entries. Only the exact value true enables SSE; follow ignores lines and sends only new entries after subscription, starting with a connected comment. Slow subscribers can miss entries; reconnecting does not replay them.
// @Description Both modes apply level and case-insensitive query filters and browser-session redaction. Available before database readiness, subject to authentication and rate limiting. Swagger 2 cannot express the query-dependent 200 response shape; see docs/arch/API_CONTRACTS.md.
// @Tags System
// @Produce json,text/event-stream
// @Security ApiKeyAuth
// @Param token query string false "Authentication token alternative; avoid exposing credentials in URLs"
// @Param lines query int false "Snapshot count (default 100, max 10000); ignored when following"
// @Param level query string false "Minimum level: trace, debug, info, warn, error, fatal, panic; unknown values pass through"
// @Param query query string false "Case-insensitive search of redacted message and raw log"
// @Param follow query string false "Only the exact value true enables SSE"
// @Success 200 "JSON snapshot array or SSE stream, depending on follow"
// @Failure 401 {string} string "unauthorized"
// @Failure 403 {string} string "keycard class forbidden on this route"
// @Failure 429 {string} string "rate limit exceeded"
// @Failure 500 {string} string "streaming not supported or authentication store unavailable"
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
			entry = logbuf.RedactBrowserSessionEntry(entry)
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
