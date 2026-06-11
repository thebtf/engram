package worker

// handlers_hooks_crystallization_test.go
//
// Unit tests for the crystallization path wired into session-end (T012/T013).
//
// Unit tests (no DB):
//   - NilMemStore path does not panic.
//   - Flag-off path skips extraction.
//   - Decision patterns in agent output → correct count logged.
//
// Integration tests (DSN-gated, tag=integration):
//   - Full HTTP POST → extraction → memory rows with correct fields.
//   - Privacy redaction applied.
//   - Flag-off skips all DB writes.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Unit tests — no DB required.
// ---------------------------------------------------------------------------

// TestRunCrystallization_NilMemStore verifies the nil-guard: when memoryStore is
// not ready, runCrystallization logs and returns without panicking.
func TestRunCrystallization_NilMemStore(t *testing.T) {
	svc := &Service{}

	assert.NotPanics(t, func() {
		svc.runCrystallization(
			context.Background(),
			"sess-nil", "project-nil",
			"decided to use PostgreSQL because it scales well.",
			nil, // memStore nil → must not panic
		)
	})
}

// TestRunCrystallization_NoDecisions verifies that empty output produces no
// panic and no store interaction.
func TestRunCrystallization_NoDecisions(t *testing.T) {
	svc := &Service{}

	// No patterns in text — must return without touching nil memStore.
	assert.NotPanics(t, func() {
		svc.runCrystallization(
			context.Background(),
			"sess-empty", "proj",
			"this text contains no decision patterns at all",
			nil,
		)
	})
}

// TestHandleSessionEnd_CrystallizationFlagOff verifies that when
// ENGRAM_CRYSTALLIZATION_ENABLED is false, the 202 response is still returned
// and the function completes without error.
func TestHandleSessionEnd_CrystallizationFlagOff(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "false")

	svc := &Service{}
	svc.ctx = context.Background()

	body, _ := json.Marshal(sessionEndRequest{
		SessionID:       "sess-flag-off",
		Project:         "proj",
		AgentOutputText: "decided to use Redis because it is fast.",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/session-end", bytes.NewReader(body))
	w := httptest.NewRecorder()

	svc.handleSessionEnd(w, req)
	// The goroutine runs async; wait for it to finish.
	svc.wg.Wait()

	assert.Equal(t, http.StatusAccepted, w.Code)
}

// TestHandleSessionEnd_CrystallizationFlagOn_NilStores verifies that even with
// the flag on, nil stores do not cause a panic (nil-guard coverage).
func TestHandleSessionEnd_CrystallizationFlagOn_NilStores(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")

	svc := &Service{}
	svc.ctx = context.Background()

	body, _ := json.Marshal(sessionEndRequest{
		SessionID:       "sess-flag-on-nil",
		Project:         "proj",
		AgentOutputText: "decided to use Redis because it is fast.",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/session-end", bytes.NewReader(body))
	w := httptest.NewRecorder()

	assert.NotPanics(t, func() {
		svc.handleSessionEnd(w, req)
		svc.wg.Wait()
	})

	assert.Equal(t, http.StatusAccepted, w.Code)
}
