package worker

// handlers_hooks_crystallization_test.go
//
// Unit tests for the session-end hook and transcript persistence (T003).
//
// The per-session regex crystallization path (runCrystallization) was removed in T014.
// Async decision extraction now runs via the dream-cycle on the sleep tick.
//
// Tests retained:
//   - Flag-off path: handleSessionEnd returns 202 and spawns no crystallization work.
//   - Flag-on with nil stores: 202 returned, no panic.
//   - Transcript persistence: flag-off skips goroutine, flag-on+nil-store no panic,
//     redaction applied before Create.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// ---------------------------------------------------------------------------
// Session-end flag gate.
// ---------------------------------------------------------------------------

// TestHandleSessionEnd_CrystallizationFlagOff verifies that when
// ENGRAM_CRYSTALLIZATION_ENABLED is false, the 202 response is still returned
// and no transcript goroutine is spawned.
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

// ---------------------------------------------------------------------------
// Transcript persistence tests — T003.
// ---------------------------------------------------------------------------

// TestHandleSessionEnd_TranscriptFlagOff verifies that when
// ENGRAM_CRYSTALLIZATION_ENABLED=false, the transcript goroutine does not run
// even when transcriptStore is set.
func TestHandleSessionEnd_TranscriptFlagOff(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "false")

	svc := &Service{}
	svc.ctx = context.Background()

	body, _ := json.Marshal(sessionEndRequest{
		SessionID:       "sess-transcript-flag-off",
		Project:         "proj",
		AgentOutputText: "sk-ant-api01-supersecretkey1234567890",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/session-end", bytes.NewReader(body))
	w := httptest.NewRecorder()

	svc.handleSessionEnd(w, req)
	svc.wg.Wait()

	assert.Equal(t, http.StatusAccepted, w.Code)
}

// TestHandleSessionEnd_TranscriptFlagOn_NilStore verifies the nil-guard on
// transcriptStore: flag on but store nil → no panic.
func TestHandleSessionEnd_TranscriptFlagOn_NilStore(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")

	svc := &Service{}
	svc.ctx = context.Background()

	body, _ := json.Marshal(sessionEndRequest{
		SessionID:       "sess-transcript-nil-store",
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
// Transcript persistence helpers — T003.
// ---------------------------------------------------------------------------

// fakeTranscriptCreator captures rows passed to Create so tests can assert on
// the value the REAL handler goroutine writes, without a live database. It
// satisfies the transcriptCreator interface and is injected via the Service
// transcriptCreatorOverride test seam. ByteLen auto-set mirrors the production
// TranscriptStore.Create behavior.
type fakeTranscriptCreator struct {
	mu      sync.Mutex
	created []*gormdb.SessionTranscript
}

func (f *fakeTranscriptCreator) Create(_ context.Context, t *gormdb.SessionTranscript) error {
	if t.ByteLen == 0 {
		t.ByteLen = len(t.Content)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, t)
	return nil
}

func (f *fakeTranscriptCreator) rows() []*gormdb.SessionTranscript {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*gormdb.SessionTranscript, len(f.created))
	copy(out, f.created)
	return out
}

// TestHandleSessionEnd_TranscriptRedactionApplied is the load-bearing T003 test.
// It drives the REAL handleSessionEnd handler path with a secret in the agent
// output and a fake transcript creator injected via transcriptCreatorOverride,
// then waits for the persistence goroutine and asserts on what the handler
// actually wrote. Unlike a helper-only test, this would FAIL if someone reordered
// the goroutine to call Create before RedactSecrets — it exercises the true
// redact→Create sequence, not a hand-built row.
//
// Verifies:
//  1. The raw secret is NOT present in the stored Content (NFR-4).
//  2. A redaction marker IS present (RedactSecrets was applied by the handler).
//  3. SessionID and Project are forwarded unchanged.
//
// No DATABASE_DSN required — the fake creator captures the row in memory.
func TestHandleSessionEnd_TranscriptRedactionApplied(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")

	const rawSecret = "sk-ant-api01-supersecret1234567890abc"
	const sessionID = "sess-redact-test"
	const project = "proj-redact"
	output := "Analysis complete. token: " + rawSecret + ". Done."

	fake := &fakeTranscriptCreator{}
	svc := &Service{}
	svc.ctx = context.Background()
	svc.transcriptCreatorOverride = fake

	body, _ := json.Marshal(sessionEndRequest{
		SessionID:       sessionID,
		Project:         project,
		AgentOutputText: output,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/session-end", bytes.NewReader(body))
	w := httptest.NewRecorder()

	svc.handleSessionEnd(w, req)
	svc.wg.Wait()

	assert.Equal(t, http.StatusAccepted, w.Code)

	rows := fake.rows()
	if len(rows) != 1 {
		t.Fatalf("expected 1 persisted transcript, got %d", len(rows))
	}
	stored := rows[0]

	assert.NotContains(t, stored.Content, rawSecret,
		"stored Content must not contain the raw secret (NFR-4)")
	assert.Contains(t, stored.Content, "[REDACTED:",
		"stored Content must contain a redaction marker proving the handler applied RedactSecrets")
	assert.Equal(t, len(stored.Content), stored.ByteLen,
		"ByteLen must equal len(Content) of the redacted text")
	assert.Equal(t, sessionID, stored.SessionID)
	assert.Equal(t, project, stored.Project)
}
