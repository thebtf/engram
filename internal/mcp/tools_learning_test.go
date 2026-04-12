package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

func TestHandleSetSessionOutcomeMCP_UsesCanonicalSessionIDForNumericInput(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, store.Close())
	}()

	sessionStore := dbgorm.NewSessionStore(store)
	observationStore := dbgorm.NewObservationStore(store, nil)
	injectionStore := dbgorm.NewInjectionStore(store.DB)
	server := NewServer(nil, "test", observationStore, nil, nil, sessionStore, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	server.SetInjectionStore(injectionStore)
	defer observationStore.Close()

	ctx := context.Background()
	claudeSessionID := "claude-mcp-learning-" + uuid.NewString()
	dbID, err := sessionStore.CreateSDKSession(ctx, claudeSessionID, "mcp-project", "prompt")
	require.NoError(t, err)

	observationID, _, err := observationStore.StoreObservation(ctx, claudeSessionID, "mcp-project", &models.ParsedObservation{
		Type:       models.ObsTypeDiscovery,
		SourceType: models.SourceManual,
		MemoryType: models.MemTypeContext,
		Title:      "MCP outcome canonical session test",
		Narrative:  "Observation to verify canonical session propagation.",
		Scope:      models.ScopeProject,
	}, int(dbID), 1)
	require.NoError(t, err)

	err = injectionStore.RecordInjections(ctx, []dbgorm.InjectionRecord{{
		ObservationID:    observationID,
		SessionID:        claudeSessionID,
		InjectionSection: "core",
	}})
	require.NoError(t, err)

	args := json.RawMessage(fmt.Sprintf(`{"session_id":"%s","outcome":"success","reason":"done"}`, strconv.FormatInt(dbID, 10)))
	result, err := server.handleSetSessionOutcomeMCP(ctx, args)
	require.NoError(t, err)
	assert.Contains(t, result, claudeSessionID)

	// Background propagation is async. Poll briefly for utility_propagated_at to be set,
	// which confirms downstream operations used canonical Claude session ID.
	deadline := time.Now().Add(3 * time.Second)
	for {
		sess, readErr := sessionStore.FindAnySDKSession(ctx, claudeSessionID)
		require.NoError(t, readErr)
		require.NotNil(t, sess)
		if sess.UtilityPropagatedAt.Valid {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("utility_propagated_at was not set for session %s", claudeSessionID)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
