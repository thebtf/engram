// Package mcp provides MCP SSE transport handling.
package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/rs/zerolog/log"
)

type SSEHandler struct {
	server   *Server
	sessions sync.Map // sessionID -> chan *Response
}

func NewSSEHandler(server *Server) *SSEHandler {
	return &SSEHandler{server: server}
}

// ServeHTTP routes GET /sse -> handleSSE, POST /message -> handleMessage, OPTIONS -> CORS preflight
func (h *SSEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch {
	case r.URL.Path == "/sse":
		h.handleSSE(w, r)
	case r.URL.Path == "/message":
		h.handleMessage(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *SSEHandler) newSessionID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func (h *SSEHandler) getSession(sessionID string) (chan *Response, bool) {
	value, ok := h.sessions.Load(sessionID)
	if !ok {
		return nil, false
	}

	ch, ok := value.(chan *Response)
	return ch, ok
}

func (h *SSEHandler) writeSSEEvent(w http.ResponseWriter, event string, payload string) error {
	if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

// handleSSE opens SSE stream, emits endpoint event, and forwards session responses.
func (h *SSEHandler) handleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var sessionID string
	for {
		id, err := h.newSessionID()
		if err != nil {
			http.Error(w, "failed to create session", http.StatusInternalServerError)
			log.Error().Err(err).Msg("Failed to create MCP SSE session ID")
			return
		}
		if _, ok := h.sessions.Load(id); ok {
			continue
		}
		sessionID = id
		break
	}

	responses := make(chan *Response, 32)
	h.sessions.Store(sessionID, responses)
	defer func() {
		h.sessions.Delete(sessionID)
		close(responses)
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type")

	if _, err := fmt.Fprintf(w, "event: endpoint\ndata: /message?sessionId=%s\n\n", sessionID); err != nil {
		http.Error(w, "failed to initialize stream", http.StatusInternalServerError)
		log.Error().Err(err).Str("sessionId", sessionID).Msg("Failed to write MCP SSE endpoint")
		return
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case response, ok := <-responses:
			if !ok {
				return
			}
			responseJSON, err := json.Marshal(response)
			if err != nil {
				log.Error().Err(err).Str("sessionId", sessionID).Msg("Failed to marshal MCP SSE response")
				continue
			}
			if err := h.writeSSEEvent(w, "message", string(responseJSON)); err != nil {
				log.Error().Err(err).Str("sessionId", sessionID).Msg("Failed to write MCP SSE response")
				return
			}
		}
	}
}

// handleMessage decodes a request and dispatches it to server.handleRequest.
func (h *SSEHandler) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "missing sessionId", http.StatusBadRequest)
		return
	}

	responseChannel, ok := h.getSession(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	defer r.Body.Close()
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		log.Error().Err(err).Str("sessionId", sessionID).Msg("Failed to decode MCP SSE message")
		return
	}

	response := h.server.handleRequest(r.Context(), &req)

	// Notifications return nil — no response to send, return 204 No Content.
	if response == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	select {
	case responseChannel <- response:
	default:
		log.Warn().Str("sessionId", sessionID).Msg("Response channel full, dropping response")
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
}

// Close closes all active session channels.
func (h *SSEHandler) Close() {
	h.sessions.Range(func(key, value any) bool {
		if ch, ok := value.(chan *Response); ok {
			defer func() {
				if recover() != nil {
					log.Warn().Msg("Session channel already closed")
				}
			}()
			close(ch)
		}
		h.sessions.Delete(key)
		return true
	})
}
