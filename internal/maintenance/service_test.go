package maintenance

import (
	"context"
	"os"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/learning"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/gorm/logger"
)

type mockBriefingLLM struct {
	response string
	err      error
}

func (m *mockBriefingLLM) Complete(_ context.Context, _, _ string) (string, error) {
	return m.response, m.err
}

func testLogger() zerolog.Logger {
	return zerolog.Nop()
}

func testMaintenanceStore(t *testing.T) (*dbgorm.Store, *dbgorm.ObservationStore, func()) {
	t.Helper()

	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	cfg := dbgorm.Config{
		DSN:      dsn,
		MaxConns: 2,
		LogLevel: logger.Silent,
	}
	store, err := dbgorm.NewStore(cfg)
	require.NoError(t, err)
	obsStore := dbgorm.NewObservationStore(store, nil)
	cleanup := func() {
		obsStore.Close()
		require.NoError(t, store.Close())
	}
	return store, obsStore, cleanup
}

func TestGenerateProjectBriefing_NoChange(t *testing.T) {
	store, obsStore, cleanup := testMaintenanceStore(t)
	defer cleanup()

	cfg := config.Default()
	cfg.ProjectBriefingEnabled = true
	llm := &mockBriefingLLM{response: learning.ProjectBriefingNoChange}
	service := NewService(store, obsStore, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, llm, testLogger())

	ctx := context.Background()
	_, _, err := obsStore.StoreObservation(ctx, "claude-1", "project-alpha", &models.ParsedObservation{
		Type:      models.ObsTypeDecision,
		Title:     "Adopt project briefing",
		Narrative: "Need a compact summary for session start.",
		Scope:     models.ScopeProject,
	}, 1, 10)
	require.NoError(t, err)

	generated, err := service.generateProjectBriefing(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, generated)
}

func TestGenerateProjectBriefing_StoresBriefing(t *testing.T) {
	store, obsStore, cleanup := testMaintenanceStore(t)
	defer cleanup()

	cfg := config.Default()
	cfg.ProjectBriefingEnabled = true
	llm := &mockBriefingLLM{response: "Active Work\n- Build project briefing\n\nRecent Decisions\n- Enabled project briefings"}
	service := NewService(store, obsStore, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, llm, testLogger())

	ctx := context.Background()
	_, _, err := obsStore.StoreObservation(ctx, "claude-1", "project-alpha", &models.ParsedObservation{
		Type:      models.ObsTypeDecision,
		Title:     "Adopt project briefing",
		Narrative: "Need a compact summary for session start.",
		Scope:     models.ScopeProject,
	}, 1, 10)
	require.NoError(t, err)

	generated, err := service.generateProjectBriefing(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, generated)

	briefing, err := obsStore.GetProjectBriefingObservation(ctx, "project-alpha")
	require.NoError(t, err)
	require.NotNil(t, briefing)
	require.Equal(t, models.ObsTypeWiki, briefing.Type)
	require.Contains(t, briefing.Narrative.String, "Active Work")
}
