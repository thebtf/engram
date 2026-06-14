package worker

// handlers_hooks_crossreview_test.go
//
// Tests addressing the cross-model review findings for session-end nil-store handling.
//
// P1-2: Session-end accepts citation-pipeline nil-store cases.
//
// The crystallizationFingerprint and runCrystallization tests (P2-5) have been
// removed because those functions were deleted in T014 (legacy regex path removal).
// Fingerprinting for the candidate path is now performed inside
// crystallization.RouteDecision → models.NewCrystallizationCandidate, which is
// covered by its own unit tests in internal/crystallization/candidate_gate_test.go
// and pkg/models/candidate_test.go.

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
// P1-2: Nil-store session-end acceptance.
// ---------------------------------------------------------------------------

// TestHandleSessionEnd_AcceptsNilCitationStores verifies that session-end still
// accepts the request when citation stores are nil.
func TestHandleSessionEnd_AcceptsNilCitationStores(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")

	svc := &Service{}
	svc.ctx = context.Background()

	body, _ := json.Marshal(sessionEndRequest{
		SessionID:       "sess-crystal-indep",
		Project:         "proj-indep",
		AgentOutputText: "decided to use PostgreSQL because it scales well.",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/session-end", bytes.NewReader(body))
	w := httptest.NewRecorder()

	assert.NotPanics(t, func() {
		svc.handleSessionEnd(w, req)
		svc.wg.Wait()
	})

	assert.Equal(t, http.StatusAccepted, w.Code,
		"must return 202 even when injection/citation stores are nil")
}

// TestHandleSessionEnd_AcceptsEmptyOutputWithoutStores verifies that empty output
// still returns the session-end 202 response in the nil-store unit harness.
func TestHandleSessionEnd_AcceptsEmptyOutputWithoutStores(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")

	svc := &Service{}
	svc.ctx = context.Background()

	body, _ := json.Marshal(sessionEndRequest{
		SessionID:       "sess-empty-output",
		Project:         "proj-empty",
		AgentOutputText: "",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/session-end", bytes.NewReader(body))
	w := httptest.NewRecorder()

	assert.NotPanics(t, func() {
		svc.handleSessionEnd(w, req)
		svc.wg.Wait()
	})

	assert.Equal(t, http.StatusAccepted, w.Code)
}

// TestHandleSessionEnd_AcceptsFlagOffWithoutStores verifies that flag-off
// session-end requests still return the session-end 202 response.
func TestHandleSessionEnd_AcceptsFlagOffWithoutStores(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "false")

	svc := &Service{}
	svc.ctx = context.Background()

	body, _ := json.Marshal(sessionEndRequest{
		SessionID:       "sess-flag-off-no-goroutine",
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
