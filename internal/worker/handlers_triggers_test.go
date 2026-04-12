package worker

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleMemoryTriggers_InvalidJSON(t *testing.T) {
	svc := &Service{}

	req := httptest.NewRequest(http.MethodPost, "/api/memory/triggers", strings.NewReader("{"))
	w := httptest.NewRecorder()

	svc.handleMemoryTriggers(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleMemoryTriggers_ValidRequestReturnsEmptyArray(t *testing.T) {
	svc := &Service{}

	body, err := json.Marshal(MemoryTriggerRequest{
		Tool:      "Bash",
		Params:    map[string]any{"command": "ls -la"},
		Project:   "engram",
		SessionID: "session-1",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/memory/triggers", bytes.NewReader(body))
	w := httptest.NewRecorder()

	svc.handleMemoryTriggers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %q", ct)
	}
	if trimmed := strings.TrimSpace(w.Body.String()); trimmed != "[]" {
		t.Fatalf("expected response body [], got %s", trimmed)
	}

	var matches []MemoryTriggerMatch
	if err := json.Unmarshal(w.Body.Bytes(), &matches); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if matches == nil {
		t.Fatal("expected non-nil slice, got nil")
	}
	if len(matches) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matches))
	}
}
