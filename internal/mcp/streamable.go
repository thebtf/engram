// Package mcp provides Streamable HTTP MCP transport (2025-03-26 spec).
//
// Streamable HTTP is the modern MCP transport: a single POST endpoint
// that accepts JSON-RPC requests and returns JSON-RPC responses inline.
// Unlike SSE transport, no long-lived connection is needed.
package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"

	"github.com/rs/zerolog/log"
)

// StreamableHandler implements the MCP Streamable HTTP transport.
// It accepts POST requests with JSON-RPC payloads, dispatches them
// to the MCP server, and returns JSON-RPC responses synchronously.
type StreamableHandler struct {
	server *Server
	health *MCPHealth
}

// NewStreamableHandler creates a new Streamable HTTP handler.
func NewStreamableHandler(server *Server, health *MCPHealth) *StreamableHandler {
	return &StreamableHandler{server: server, health: health}
}

// ServeHTTP handles POST requests with JSON-RPC MCP messages.
func (h *StreamableHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS preflight — not counted as a request.
	if r.Method == http.MethodOptions {
		h.writeCORS(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if h.health != nil {
		h.health.RecordRequest()
	}

	if r.Method != http.MethodPost {
		if h.health != nil {
			h.health.RecordError()
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	defer r.Body.Close()

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Error().Err(err).Msg("Failed to decode Streamable HTTP MCP request")
		if h.health != nil {
			h.health.RecordError()
		}
		writeJSONError(w, nil, -32700, "Parse error")
		return
	}

	// Recover from panics inside handleRequest so that:
	// 1. The full stack trace is logged via zerolog (visible in /api/logs)
	// 2. The client gets a proper JSON-RPC error instead of HTTP 500 + empty body
	// Without this, Chi's Recoverer only writes to stderr and returns bare 500.
	var response *Response
	func() {
		defer func() {
			if rvr := recover(); rvr != nil {
				stack := string(debug.Stack())
				log.Error().
					Str("panic", fmt.Sprintf("%v", rvr)).
					Str("stack", stack).
					Str("method", req.Method).
					Msg("PANIC in MCP handleRequest")
				response = &Response{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error: &Error{
						Code:    -32603,
						Message: fmt.Sprintf("Internal error: %v", rvr),
					},
				}
			}
		}()
		response = h.server.handleRequest(r.Context(), &req)
	}()

	h.writeCORS(w)

	// Notifications return nil — no response to send, return 204 No Content.
	if response == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Count JSON-RPC error responses as errors.
	if response.Error != nil && h.health != nil {
		h.health.RecordError()
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
