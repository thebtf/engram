package worker

// handlers_hooks_crystallization_integration_test.go — T013 integration tests.
//
// These tests require a real PostgreSQL database and exercise the full path:
//   POST /api/hooks/session-end → goroutine → crystallization.ExtractDecisions
//   → BuildMemories → MemoryStore.Create → DB rows with epistemic_type=decision.
//
// Run with:
//   DATABASE_DSN="postgres://user:pass@host:5432/db?sslmode=disable" \
//     go test ./... -run TestCrystallizationIntegration -v

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

// openIntegrationStore opens a Store using DATABASE_DSN, running all migrations.
// Skips the test if DATABASE_DSN is not set.
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

// cleanCrystallizationRows deletes test memories created by crystallization for a
// given project and session tag so tests are idempotent.
func cleanCrystallizationRows(t *testing.T, store *gormdb.Store, project, sessionTag string) {
	t.Helper()
	res := store.DB.Exec(
		"DELETE FROM memories WHERE source_agent = 'crystallization' AND project = ? AND tags::text LIKE ?",
		project, "%"+sessionTag+"%",
	)
	require.NoError(t, res.Error)
}

// buildIntegrationService wires a minimal Service with a real MemoryStore for
// integration testing. The crystallization flag must be set by the caller.
func buildIntegrationService(t *testing.T, store *gormdb.Store) *Service {
	t.Helper()
	memStore := gormdb.NewMemoryStore(store)
	svc := &Service{}
	svc.ctx = context.Background()
	svc.initMu.Lock()
	svc.memoryStore = memStore
	svc.initMu.Unlock()
	return svc
}

// countCrystallizationMemories counts memories stored by crystallization for a
// given project+sessionTag combination.
func countCrystallizationMemories(t *testing.T, store *gormdb.Store, project, sessionTag string) int64 {
	t.Helper()
	var count int64
	res := store.DB.Raw(`
		SELECT COUNT(*) FROM memories
		WHERE source_agent = 'crystallization'
		  AND project      = ?
		  AND epistemic_type = 'decision'
		  AND tier          = 'episodic'
		  AND tags::text LIKE ?
		  AND deleted_at IS NULL`,
		project, "%"+sessionTag+"%",
	).Scan(&count)
	require.NoError(t, res.Error)
	return count
}

// ---------------------------------------------------------------------------
// T013 integration tests — DSN-gated.
// ---------------------------------------------------------------------------

// TestCrystallizationIntegration_DecisionsStoredWithCorrectFields is the primary
// end-to-end test: 3 decision patterns → 3 DB rows with correct metadata. The
// test service intentionally leaves citation stores nil, proving crystallization
// runs independently when memoryStore is available.
func TestCrystallizationIntegration_DecisionsStoredWithCorrectFields(t *testing.T) {
	store := openIntegrationStore(t)
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")

	const sessionID = "integ-cryst-sess-001"
	const project = "integ-cryst-project"
	sessionTag := "session:" + sessionID
	t.Cleanup(func() { cleanCrystallizationRows(t, store, project, sessionTag) })

	svc := buildIntegrationService(t, store)

	agentOutput := `decided to use PostgreSQL because it is battle-tested.
We chose Go over Python for performance reasons.
Going forward, all new services will follow the same pattern.`

	body, _ := json.Marshal(sessionEndRequest{
		SessionID:       sessionID,
		Project:         project,
		AgentOutputText: agentOutput,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/session-end", bytes.NewReader(body))
	w := httptest.NewRecorder()

	svc.handleSessionEnd(w, req)
	svc.wg.Wait()

	assert.Equal(t, http.StatusAccepted, w.Code)

	count := countCrystallizationMemories(t, store, project, sessionTag)
	assert.GreaterOrEqual(t, count, int64(3),
		"expected ≥3 decision memories; got %d", count)
}

// TestCrystallizationIntegration_FlagOff_NothingStored verifies that with the
// flag off no memories are written even when decision patterns exist and
// memoryStore is available.
func TestCrystallizationIntegration_FlagOff_NothingStored(t *testing.T) {
	store := openIntegrationStore(t)
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "false")

	const sessionID = "integ-cryst-sess-002"
	const project = "integ-cryst-project-off"
	sessionTag := "session:" + sessionID
	t.Cleanup(func() { cleanCrystallizationRows(t, store, project, sessionTag) })

	svc := buildIntegrationService(t, store)

	body, _ := json.Marshal(sessionEndRequest{
		SessionID:       sessionID,
		Project:         project,
		AgentOutputText: "decided to use Redis because it is fast.",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/session-end", bytes.NewReader(body))
	w := httptest.NewRecorder()

	svc.handleSessionEnd(w, req)
	svc.wg.Wait()

	assert.Equal(t, http.StatusAccepted, w.Code)

	count := countCrystallizationMemories(t, store, project, sessionTag)
	assert.Equal(t, int64(0), count, "flag off: must not write any crystallization memories")
}

// TestCrystallizationIntegration_EmptyOutput_NothingStored verifies that empty
// agent output does not spawn crystallization even when memoryStore is wired.
func TestCrystallizationIntegration_EmptyOutput_NothingStored(t *testing.T) {
	store := openIntegrationStore(t)
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")

	const sessionID = "integ-cryst-sess-empty-003"
	const project = "integ-cryst-project-empty"
	sessionTag := "session:" + sessionID
	t.Cleanup(func() { cleanCrystallizationRows(t, store, project, sessionTag) })

	svc := buildIntegrationService(t, store)

	body, _ := json.Marshal(sessionEndRequest{
		SessionID:       sessionID,
		Project:         project,
		AgentOutputText: "",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/session-end", bytes.NewReader(body))
	w := httptest.NewRecorder()

	svc.handleSessionEnd(w, req)
	svc.wg.Wait()

	assert.Equal(t, http.StatusAccepted, w.Code)

	count := countCrystallizationMemories(t, store, project, sessionTag)
	assert.Equal(t, int64(0), count, "empty output: must not write any crystallization memories")
}

// TestCrystallizationIntegration_PrivacyRedaction verifies that secrets in agent
// output are redacted before the memory row is written to the database.
func TestCrystallizationIntegration_PrivacyRedaction(t *testing.T) {
	store := openIntegrationStore(t)
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")

	const sessionID = "integ-cryst-sess-003"
	const project = "integ-cryst-project-redact"
	sessionTag := "session:" + sessionID
	t.Cleanup(func() { cleanCrystallizationRows(t, store, project, sessionTag) })

	svc := buildIntegrationService(t, store)

	body, _ := json.Marshal(sessionEndRequest{
		SessionID: sessionID,
		Project:   project,
		// The decision text embeds a secret pattern.
		AgentOutputText: "decided to rotate SECRET_KEY=supersecretvalue99999superlong because it was exposed.",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/session-end", bytes.NewReader(body))
	w := httptest.NewRecorder()

	svc.handleSessionEnd(w, req)
	svc.wg.Wait()

	assert.Equal(t, http.StatusAccepted, w.Code)

	// Retrieve stored content and verify redaction.
	type row struct{ Content string }
	var rows []row
	res := store.DB.Raw(`
		SELECT content FROM memories
		WHERE source_agent = 'crystallization'
		  AND project = ?
		  AND tags::text LIKE ?
		  AND deleted_at IS NULL`,
		project, "%"+sessionTag+"%",
	).Scan(&rows)
	require.NoError(t, res.Error)

	require.NotEmpty(t, rows, "expected at least one memory row")
	for _, r := range rows {
		assert.NotContains(t, r.Content, "supersecretvalue99999superlong",
			"raw secret must not appear in stored content")
		assert.Contains(t, r.Content, "[REDACTED",
			"stored content must contain redaction marker")
	}
}

// TestCrystallizationIntegration_ConcurrentReplaySkipsDuplicateFingerprint
// verifies that duplicate session-end deliveries for the same session do not
// insert duplicate crystallization memories.
func TestCrystallizationIntegration_ConcurrentReplaySkipsDuplicateFingerprint(t *testing.T) {
	store := openIntegrationStore(t)
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")

	const sessionID = "integ-cryst-sess-replay-004"
	const project = "integ-cryst-project-replay"
	sessionTag := "session:" + sessionID
	t.Cleanup(func() { cleanCrystallizationRows(t, store, project, sessionTag) })

	svc := buildIntegrationService(t, store)
	body, _ := json.Marshal(sessionEndRequest{
		SessionID:       sessionID,
		Project:         project,
		AgentOutputText: "decided to use PostgreSQL because it is battle-tested.",
	})

	var handlers sync.WaitGroup
	for i := 0; i < 2; i++ {
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/hooks/session-end", bytes.NewReader(body))
			w := httptest.NewRecorder()
			svc.handleSessionEnd(w, req)
			assert.Equal(t, http.StatusAccepted, w.Code)
		}()
	}
	handlers.Wait()
	svc.wg.Wait()

	count := countCrystallizationMemories(t, store, project, sessionTag)
	assert.Equal(t, int64(1), count, "concurrent replay must persist one crystallized decision")
}
