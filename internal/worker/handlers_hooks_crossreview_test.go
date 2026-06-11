package worker

// handlers_hooks_crossreview_test.go
//
// Tests addressing the cross-model review findings:
//
//   P1-2: Session-end accepts citation-pipeline nil-store cases. DB-backed
//         crystallization side effects are covered by the DSN-gated integration
//         tests in handlers_hooks_crystallization_integration_test.go.
//
//   P2-5: Idempotency — crystallizationFingerprint and skip logic.
//         - Same sessionID+content pair produces a stable fingerprint.
//         - Different content produces a different fingerprint.
//         - runCrystallization nil memStore guard does not panic.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/privacy"
)

// ---------------------------------------------------------------------------
// P1-2: Nil-store session-end acceptance.
// ---------------------------------------------------------------------------

// TestHandleSessionEnd_AcceptsNilCitationStores verifies that session-end still
// accepts the request when citation stores are nil.
//
// With nil memStore this unit test does not assert DB side effects; the
// DB-backed independence check lives in the integration test file.
func TestHandleSessionEnd_AcceptsNilCitationStores(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")

	svc := &Service{}
	svc.ctx = context.Background()
	// injectionStore, citationStore, memStore all nil.

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
		AgentOutputText: "", // empty — crystallization goroutine must not be spawned
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

// ---------------------------------------------------------------------------
// P2-5: Idempotency — crystallizationFingerprint correctness.
// ---------------------------------------------------------------------------

// TestCrystallizationFingerprint_Stable verifies that the same sessionID+content
// always produces the same 16-char hex fingerprint.
func TestCrystallizationFingerprint_Stable(t *testing.T) {
	fp1 := crystallizationFingerprint("sess-123", "decided to use PostgreSQL because it scales.")
	fp2 := crystallizationFingerprint("sess-123", "decided to use PostgreSQL because it scales.")

	require.Equal(t, fp1, fp2, "same inputs must produce the same fingerprint")
	require.Len(t, fp1, 16, "fingerprint must be exactly 16 hex chars")
	require.Regexp(t, `^[0-9a-f]{16}$`, fp1, "fingerprint must be lowercase hex")
}

// TestCrystallizationFingerprint_Distinct verifies that different inputs produce
// different fingerprints.
func TestCrystallizationFingerprint_Distinct(t *testing.T) {
	fp1 := crystallizationFingerprint("sess-123", "decided to use PostgreSQL because it scales.")
	fp2 := crystallizationFingerprint("sess-123", "decided to use Redis because it is fast.")
	fp3 := crystallizationFingerprint("sess-456", "decided to use PostgreSQL because it scales.")

	assert.NotEqual(t, fp1, fp2, "different content must produce different fingerprint")
	assert.NotEqual(t, fp1, fp3, "different sessionID must produce different fingerprint")
}

// TestCrystallizationFingerprint_SessionIsolation verifies that the same content
// from different sessions gets different fingerprints (prevents cross-session dedup).
func TestCrystallizationFingerprint_SessionIsolation(t *testing.T) {
	content := "decided to use PostgreSQL because it scales."
	fpA := crystallizationFingerprint("session-A", content)
	fpB := crystallizationFingerprint("session-B", content)

	assert.NotEqual(t, fpA, fpB,
		"same decision content from different sessions must have different fingerprints")
}

// TestCrystallizationFingerprint_RedactedSecretMarkersStayDistinct verifies the
// current privacy redaction contract: different secret values are not collapsed
// into a generic marker before fingerprinting.
func TestCrystallizationFingerprint_RedactedSecretMarkersStayDistinct(t *testing.T) {
	first := "decided to rotate SECRET_KEY=aaaaaaaaaaaaaaaaaaaaaaaa because it leaked."
	second := "decided to rotate SECRET_KEY=bbbbbbbbbbbbbbbbbbbbbbbb because it leaked."

	redactedFirst := privacy.RedactSecrets(first)
	redactedSecond := privacy.RedactSecrets(second)

	require.Contains(t, redactedFirst, "[REDACTED:")
	require.Contains(t, redactedSecond, "[REDACTED:")
	require.NotEqual(t, redactedFirst, redactedSecond, "different secrets retain distinct redaction markers")

	fpFirst := crystallizationFingerprint("sess-redacted", redactedFirst)
	fpSecond := crystallizationFingerprint("sess-redacted", redactedSecond)
	assert.NotEqual(t, fpFirst, fpSecond, "redacted decisions with different secret values must remain distinct")
}

// TestRunCrystallization_IdempotencyNilGuardNoPanic verifies the nil memStore
// guard only. Real idempotency/dedup behavior is DB-backed and covered in the
// integration tests.
func TestRunCrystallization_IdempotencyNilGuardNoPanic(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")

	svc := &Service{}

	assert.NotPanics(t, func() {
		svc.runCrystallization(
			context.Background(),
			"sess-idem-nil", "proj",
			"decided to use PostgreSQL because it scales.",
			nil, // nil memStore triggers nil-guard before any dedup logic
		)
	}, "nil memStore must not panic")
}
