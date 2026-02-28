// Package mcp provides Streamable HTTP MCP transport (2025-03-26 spec).
//
// Streamable HTTP is the modern MCP transport: a single POST endpoint
// that accepts JSON-RPC requests and returns JSON-RPC responses inline.
// Unlike SSE transport, no long-lived connection is needed.
package mcp

import (
	"encoding/json"
	"net/http"

	"github.com/rs/zerolog/log"
)

// StreamableHandler implements the MCP Streamable HTTP transport.
// It accepts POST requests with JSON-RPC payloads, dispatches them
// to the MCP server, and returns JSON-RPC responses synchronously.
type StreamableHandler struct {
	server *Server
}

// NewStreamableHandler creates a new Streamable HTTP handler.
func NewStreamableHandler(server *Server) *StreamableHandler {
	return &StreamableHandler{server: server}
}

// ServeHTTP handles POST requests with JSON-RPC MCP messages.
func (h *StreamableHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS preflight
	if r.Method == http.MethodOptions {
		h.writeCORS(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	defer r.Body.Close()

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Error().Err(err).Msg("Failed to decode Streamable HTTP MCP request")
		writeJSONError(w, nil, -32700, "Parse error")
		return
	}

	response := h.server.handleRequest(r.Context(), &req)

	h.writeCORS(w)

	// Notifications return nil — no response to send, return 204 No Content.
	if response == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Error().Err(err).Msg("Failed to encode Streamable HTTP MCP response")
	}
}

func (h *StreamableHandler) writeCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
}

func writeJSONError(w http.ResponseWriter, id any, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &Error{Code: code, Message: message},
	}
	_ = json.NewEncoder(w).Encode(resp)
}
