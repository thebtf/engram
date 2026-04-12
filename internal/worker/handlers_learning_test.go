package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
)

func TestHandleSetSessionOutcome_UsesCanonicalSessionIDForNumericInput(t *testing.T) {
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
	injectionStore := dbgorm.NewInjectionStore(store.DB)
	service := &Service{
		sessionStore:   sessionStore,
		injectionStore: injectionStore,
	}

	ctx := context.Background()
	claudeSessionID := "claude-learning-handler-" + uuid.NewString()
	dbID, err := sessionStore.CreateSDKSession(ctx, claudeSessionID, "agent-test", "prompt")
	require.NoError(t, err)

	err = injectionStore.RecordInjections(ctx, []dbgorm.InjectionRecord{{
		ObservationID:    1,
		SessionID:        claudeSessionID,
		InjectionSection: "core",
	}})
	require.NoError(t, err)

	requestBody := []byte(`{"outcome":"success","reason":"done"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+strconv.FormatInt(dbID, 10)+"/outcome", bytes.NewReader(requestBody))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", strconv.FormatInt(dbID, 10))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	service.handleSetSessionOutcome(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var response struct {
		SessionID            string `json:"session_id"`
		Outcome              string `json:"outcome"`
		ObservationsAffected int64  `json:"observations_affected"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	assert.Equal(t, claudeSessionID, response.SessionID)
	assert.Equal(t, "success", response.Outcome)
	assert.Equal(t, int64(1), response.ObservationsAffected)
}
