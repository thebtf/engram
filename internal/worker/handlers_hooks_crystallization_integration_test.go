package worker

// These PostgreSQL integration tests cover the current session-end side of the
// crystallization pipeline. The removed per-session regex path must not be
// resurrected: session-end stores a redacted transcript, while the separate
// dream cycle performs extraction, candidate gating, and fingerprint handling.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// openIntegrationStore opens a Store using DATABASE_DSN, running all
// migrations. The release gate treats this skip as fatal; the package-level
// helper retains the established local no-DSN behavior.
func openIntegrationStore(t *testing.T) *gormdb.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn})
	require.NoError(t, err, "open test database with migrations")
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func cleanTranscriptRows(t *testing.T, store *gormdb.Store, sessionID, project string) {
	t.Helper()
	require.NoError(t, store.DB.Exec(
		"DELETE FROM session_transcripts WHERE session_id = ? AND project = ?",
		sessionID,
		project,
	).Error)
}

func buildIntegrationService(t *testing.T, store *gormdb.Store) *Service {
	t.Helper()
	svc := &Service{ctx: context.Background()}
	svc.initMu.Lock()
	svc.transcriptStore = gormdb.NewTranscriptStore(store.DB)
	svc.initMu.Unlock()
	return svc
}

func readTranscriptRows(t *testing.T, store *gormdb.Store, sessionID, project string) []gormdb.SessionTranscript {
	t.Helper()
	var rows []gormdb.SessionTranscript
	require.NoError(t, store.DB.
		Where("session_id = ? AND project = ?", sessionID, project).
		Order("id ASC").
		Find(&rows).Error)
	return rows
}

func postSessionEnd(t *testing.T, svc *Service, reqBody sessionEndRequest) int {
	t.Helper()
	body, err := json.Marshal(reqBody)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/session-end", bytes.NewReader(body))
	w := httptest.NewRecorder()
	svc.handleSessionEnd(w, req)
	return w.Code
}

func TestCrystallizationIntegration_TranscriptStoredForDreamCycle(t *testing.T) {
	store := openIntegrationStore(t)
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")

	const sessionID = "integ-cryst-transcript-001"
	const project = "integ-cryst-project"
	const agentOutput = `decided to use PostgreSQL because it is battle-tested.
We chose Go over Python for performance reasons.`
	cleanTranscriptRows(t, store, sessionID, project)
	t.Cleanup(func() { cleanTranscriptRows(t, store, sessionID, project) })

	svc := buildIntegrationService(t, store)
	status := postSessionEnd(t, svc, sessionEndRequest{
		SessionID:       sessionID,
		Project:         project,
		AgentOutputText: agentOutput,
	})
	svc.wg.Wait()

	require.Equal(t, http.StatusAccepted, status)
	rows := readTranscriptRows(t, store, sessionID, project)
	require.Len(t, rows, 1)
	assert.Equal(t, agentOutput, rows[0].Content)
	assert.Equal(t, len(agentOutput), rows[0].ByteLen)
	assert.False(t, rows[0].CreatedAt.IsZero())
	assert.Nil(t, rows[0].ProcessedAt)

	var memoryCount int64
	require.NoError(t, store.DB.Model(&gormdb.Memory{}).
		Where("source_agent = ? AND project = ?", "crystallization", project).
		Count(&memoryCount).Error)
	assert.Zero(t, memoryCount, "session-end must not resurrect direct decision-memory extraction")
}

func TestCrystallizationIntegration_FlagOff_NoTranscriptStored(t *testing.T) {
	store := openIntegrationStore(t)
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "false")

	const sessionID = "integ-cryst-transcript-off-002"
	const project = "integ-cryst-project-off"
	cleanTranscriptRows(t, store, sessionID, project)
	t.Cleanup(func() { cleanTranscriptRows(t, store, sessionID, project) })

	svc := buildIntegrationService(t, store)
	status := postSessionEnd(t, svc, sessionEndRequest{
		SessionID:       sessionID,
		Project:         project,
		AgentOutputText: "decided to use Redis because it is fast.",
	})
	svc.wg.Wait()

	require.Equal(t, http.StatusAccepted, status)
	assert.Empty(t, readTranscriptRows(t, store, sessionID, project))
}

func TestCrystallizationIntegration_EmptyOutput_NoTranscriptStored(t *testing.T) {
	store := openIntegrationStore(t)
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")

	const sessionID = "integ-cryst-transcript-empty-003"
	const project = "integ-cryst-project-empty"
	cleanTranscriptRows(t, store, sessionID, project)
	t.Cleanup(func() { cleanTranscriptRows(t, store, sessionID, project) })

	svc := buildIntegrationService(t, store)
	status := postSessionEnd(t, svc, sessionEndRequest{
		SessionID: sessionID,
		Project:   project,
	})
	svc.wg.Wait()

	require.Equal(t, http.StatusAccepted, status)
	assert.Empty(t, readTranscriptRows(t, store, sessionID, project))
}

func TestCrystallizationIntegration_TranscriptPrivacyRedaction(t *testing.T) {
	store := openIntegrationStore(t)
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")

	const sessionID = "integ-cryst-transcript-redact-004"
	const project = "integ-cryst-project-redact"
	const secret = "supersecretvalue99999superlong"
	cleanTranscriptRows(t, store, sessionID, project)
	t.Cleanup(func() { cleanTranscriptRows(t, store, sessionID, project) })

	svc := buildIntegrationService(t, store)
	status := postSessionEnd(t, svc, sessionEndRequest{
		SessionID:       sessionID,
		Project:         project,
		AgentOutputText: "decided to rotate SECRET_KEY=" + secret + " because it was exposed.",
	})
	svc.wg.Wait()

	require.Equal(t, http.StatusAccepted, status)
	rows := readTranscriptRows(t, store, sessionID, project)
	require.Len(t, rows, 1)
	assert.NotContains(t, rows[0].Content, secret)
	assert.Contains(t, rows[0].Content, "[REDACTED")
	assert.Equal(t, len(rows[0].Content), rows[0].ByteLen)
}

func TestCrystallizationIntegration_ConcurrentDeliveriesPersistTranscripts(t *testing.T) {
	store := openIntegrationStore(t)
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")

	const sessionID = "integ-cryst-transcript-concurrent-005"
	const project = "integ-cryst-project-concurrent"
	const agentOutput = "decided to use PostgreSQL because it is battle-tested."
	cleanTranscriptRows(t, store, sessionID, project)
	t.Cleanup(func() { cleanTranscriptRows(t, store, sessionID, project) })

	svc := buildIntegrationService(t, store)
	body, err := json.Marshal(sessionEndRequest{
		SessionID:       sessionID,
		Project:         project,
		AgentOutputText: agentOutput,
	})
	require.NoError(t, err)
	var handlers sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/hooks/session-end", bytes.NewReader(body))
			w := httptest.NewRecorder()
			svc.handleSessionEnd(w, req)
			statuses <- w.Code
		}()
	}
	handlers.Wait()
	close(statuses)
	for status := range statuses {
		assert.Equal(t, http.StatusAccepted, status)
	}
	svc.wg.Wait()

	rows := readTranscriptRows(t, store, sessionID, project)
	require.Len(t, rows, 2, "raw deliveries remain separate; downstream candidate gating owns fingerprint dedupe")
	for _, row := range rows {
		assert.Equal(t, agentOutput, row.Content)
	}
}
