package worker

// handlers_hooks_crystallization_integration_test.go verifies the live
// session-end contract: flag-gated, redacted transcript persistence. Decision
// extraction and candidate routing occur later in the dream cycle.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func openIntegrationStore(t *testing.T) *gormdb.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn})
	require.NoError(t, err, "open test database with migrations")
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSessionEndIntegration_WritesRedactedTranscript(t *testing.T) {
	store := openIntegrationStore(t)
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")

	const sessionID = "integ-transcript-sess-001"
	const project = "integ-transcript-project"
	t.Cleanup(func() {
		_ = store.DB.Where("session_id = ? AND project = ?", sessionID, project).
			Delete(&gormdb.SessionTranscript{}).Error
	})

	svc := &Service{ctx: context.Background()}
	svc.initMu.Lock()
	svc.transcriptStore = gormdb.NewTranscriptStore(store.DB)
	svc.initMu.Unlock()

	body, err := json.Marshal(sessionEndRequest{
		SessionID:       sessionID,
		Project:         project,
		AgentOutputText: "decided to rotate SECRET_KEY=supersecretvalue99999superlong because it was exposed.",
	})
	require.NoError(t, err)
	w := httptest.NewRecorder()
	svc.handleSessionEnd(w, httptest.NewRequest(http.MethodPost, "/api/hooks/session-end", bytes.NewReader(body)))
	svc.wg.Wait()

	assert.Equal(t, http.StatusAccepted, w.Code)
	var rows []gormdb.SessionTranscript
	require.NoError(t, store.DB.Where("session_id = ? AND project = ?", sessionID, project).Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.NotContains(t, rows[0].Content, "supersecretvalue99999superlong")
	assert.Contains(t, rows[0].Content, "[REDACTED")
}
