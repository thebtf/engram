package consolidation

import (
	"context"
	"errors"
	"database/sql"
	"testing"
	"time"

	"github.com/thebtf/claude-mnemonic-plus/internal/scoring"
	"github.com/thebtf/claude-mnemonic-plus/pkg/models"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

// SchedulerSuite validates scheduler lifecycle operations.
type SchedulerSuite struct {
	suite.Suite
	ctx context.Context
}

func TestSchedulerSuite(t *testing.T) {
	suite.Run(t, new(SchedulerSuite))
}

func (s *SchedulerSuite) SetupTest() {
	s.ctx = context.Background()
}

type mockObservationStore struct {
	getAllFn              func(context.Context) ([]*models.Observation, error)
	getRecentFn           func(context.Context, string, int) ([]*models.Observation, error)
	updateImportanceFn    func(context.Context, map[int64]float64) error
	archiveObservationFn  func(context.Context, int64, string) error

	getAllCalled            int
	getRecentCalled         int
	updateImportanceCalled  int
	archiveObservationCalls int
}

func (m *mockObservationStore) GetAllObservations(ctx context.Context) ([]*models.Observation, error) {
	m.getAllCalled++
	if m.getAllFn == nil {
		return []*models.Observation{}, nil
	}
	return m.getAllFn(ctx)
}

func (m *mockObservationStore) GetRecentObservations(ctx context.Context, project string, limit int) ([]*models.Observation, error) {
	m.getRecentCalled++
	if m.getRecentFn == nil {
		return []*models.Observation{}, nil
	}
	return m.getRecentFn(ctx, project, limit)
}

func (m *mockObservationStore) UpdateImportanceScores(ctx context.Context, scores map[int64]float64) error {
	m.updateImportanceCalled++
	if m.updateImportanceFn == nil {
		return nil
	}
	return m.updateImportanceFn(ctx, scores)
}

func (m *mockObservationStore) ArchiveObservation(ctx context.Context, id int64, reason string) error {
	m.archiveObservationCalls++
	if m.archiveObservationFn == nil {
		return nil
	}
	return m.archiveObservationFn(ctx, id, reason)
}

type mockRelationStore struct {
	getRelationsByIDFn func(context.Context, int64) ([]*models.ObservationRelation, error)
	storeRelationFn    func(context.Context, *models.ObservationRelation) (int64, error)
	getRelationCountFn func(context.Context, int64) (int, error)

	getRelationsByIDCalled int
	storeRelationCalled    int
	getRelationCountCalled int
}

func (m *mockRelationStore) GetRelationsByObservationID(ctx context.Context, obsID int64) ([]*models.ObservationRelation, error) {
	m.getRelationsByIDCalled++
	if m.getRelationsByIDFn == nil {
		return []*models.ObservationRelation{}, nil
	}
	return m.getRelationsByIDFn(ctx, obsID)
}

func (m *mockRelationStore) StoreRelation(ctx context.Context, relation *models.ObservationRelation) (int64, error) {
	m.storeRelationCalled++
	if m.storeRelationFn == nil {
		return 0, nil
	}
	return m.storeRelationFn(ctx, relation)
}

func (m *mockRelationStore) GetRelationCount(ctx context.Context, obsID int64) (int, error) {
	m.getRelationCountCalled++
	if m.getRelationCountFn == nil {
		return 0, nil
	}
	return m.getRelationCountFn(ctx, obsID)
}

func (s *SchedulerSuite) TestDefaultSchedulerConfigValues() {
	cfg := DefaultSchedulerConfig()
	assert.Equal(s.T(), 24*time.Hour, cfg.DecayInterval)
	assert.Equal(s.T(), 168*time.Hour, cfg.AssociationInterval)
	assert.Equal(s.T(), 2160*time.Hour, cfg.ForgetInterval)
	assert.False(s.T(), cfg.ForgetEnabled)
	assert.InDelta(s.T(), 0.01, cfg.ForgetThreshold, 0)
}

func (s *SchedulerSuite) TestRunDecay_EmptyObservationsReturnsNil() {
	obsStore := &mockObservationStore{}
	relStore := &mockRelationStore{}
	cfg := DefaultSchedulerConfig()
	cfg.ForgetEnabled = false

	scheduler := NewScheduler(
		scoreCalculatorWithZeroDecay(),
		nil,
		obsStore,
		relStore,
		cfg,
		zerolog.Nop(),
	)

	err := scheduler.RunDecay(s.ctx)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), 0, obsStore.updateImportanceCalled)
	assert.Equal(s.T(), 1, obsStore.getAllCalled)
}

func (s *SchedulerSuite) TestRunDecay_PropagatesGetAllError() {
	obsStore := &mockObservationStore{
		getAllFn: func(context.Context) ([]*models.Observation, error) {
			return nil, errors.New("load failed")
		},
	}
	relStore := &mockRelationStore{}
	scheduler := NewScheduler(
		scoreCalculatorWithZeroDecay(),
		nil,
		obsStore,
		relStore,
		DefaultSchedulerConfig(),
		zerolog.Nop(),
	)

	err := scheduler.RunDecay(s.ctx)
	assert.Error(s.T(), err)
	assert.Equal(s.T(), "load failed", err.Error())
	assert.Equal(s.T(), 1, obsStore.getAllCalled)
	assert.Equal(s.T(), 0, obsStore.updateImportanceCalled)
}

func (s *SchedulerSuite) TestRunDecay_StoresRelevanceForSingleObservation() {
	obsStore := &mockObservationStore{}
	relStore := &mockRelationStore{
		getRelationCountFn: func(context.Context, int64) (int, error) {
			return 0, nil
		},
		getRelationsByIDFn: func(context.Context, int64) ([]*models.ObservationRelation, error) {
			return []*models.ObservationRelation{}, nil
		},
	}

	captured := map[int64]float64{}
	obsStore.updateImportanceFn = func(_ context.Context, scores map[int64]float64) error {
		for id, score := range scores {
			captured[id] = score
		}
		return nil
	}

	obs := &models.Observation{
		ID:              42,
		CreatedAtEpoch:   time.Now().Add(-48 * time.Hour).UnixMilli(),
		ImportanceScore:  0.2,
		LastRetrievedAt:  sql.NullInt64{Valid: false},
		Type:            models.ObsTypeDiscovery,
	}
	obsStore.getAllFn = func(context.Context) ([]*models.Observation, error) {
		return []*models.Observation{obs}, nil
	}

	scheduler := NewScheduler(
		scoreCalculatorWithZeroDecay(),
		nil,
		obsStore,
		relStore,
		DefaultSchedulerConfig(),
		zerolog.Nop(),
	)

	err := scheduler.RunDecay(s.ctx)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), 1, obsStore.getAllCalled)
	assert.Equal(s.T(), 1, obsStore.updateImportanceCalled)
	assert.Equal(s.T(), 1, relStore.getRelationCountCalled)
	assert.Equal(s.T(), 1, relStore.getRelationsByIDCalled)

	expected := 0.6 * (0.5 + obs.ImportanceScore) * 0.85
	assert.InDelta(s.T(), expected, captured[obs.ID], 1e-12)
}

func (s *SchedulerSuite) TestRunForgetting_DisabledNoChanges() {
	obsStore := &mockObservationStore{getAllFn: func(context.Context) ([]*models.Observation, error) {
		return []*models.Observation{{ID: 1, ImportanceScore: 0}}, nil
	}}
	relStore := &mockRelationStore{}
	cfg := DefaultSchedulerConfig()
	cfg.ForgetEnabled = false

	scheduler := NewScheduler(
		scoreCalculatorWithZeroDecay(),
		nil,
		obsStore,
		relStore,
		cfg,
		zerolog.Nop(),
	)

	err := scheduler.RunForgetting(s.ctx)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), 0, obsStore.archiveObservationCalls)
	assert.Equal(s.T(), 0, obsStore.getAllCalled)
}

func (s *SchedulerSuite) TestRunForgetting_DoesNotArchiveProtectedObservations() {
	tests := []struct {
		name string
		obs  *models.Observation
	}{
		{
			name: "high importance protected",
			obs: &models.Observation{ID: 1, ImportanceScore: 0.9, CreatedAtEpoch: time.Now().Add(-100 * 24 * time.Hour).UnixMilli(), Type: models.ObsTypeFeature},
		},
		{
			name: "young age protected",
			obs: &models.Observation{ID: 2, ImportanceScore: 0.1, CreatedAtEpoch: time.Now().Add(-30 * 24 * time.Hour).UnixMilli(), Type: models.ObsTypeFeature},
		},
		{
			name: "decision type protected",
			obs: &models.Observation{ID: 3, ImportanceScore: 0.1, CreatedAtEpoch: time.Now().Add(-100 * 24 * time.Hour).UnixMilli(), Type: models.ObsTypeDecision},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			scheduler := newSchedulerForForgetting(tt.obs)
			err := scheduler.RunForgetting(s.ctx)
			assert.NoError(s.T(), err)
			assert.Equal(s.T(), 0, scheduler.obsStore.archiveObservationCalls)
		})
	}
}

func (s *SchedulerSuite) TestRunForgetting_ArchivesLowScoreObservation() {
	obsID := int64(99)

	recorded := struct {
		calls  int
		reason string
		id    int64
	}{}

	obs := &models.Observation{
		ID:             obsID,
		Type:           models.ObsTypeFeature,
		ImportanceScore: 0.01,
		CreatedAtEpoch:  time.Now().Add(-200 * 24 * time.Hour).UnixMilli(),
	}

	obsStore := &mockObservationStore{
		getAllFn: func(context.Context) ([]*models.Observation, error) {
			return []*models.Observation{obs}, nil
		},
		archiveObservationFn: func(_ context.Context, id int64, reason string) error {
			recorded.calls++
			recorded.id = id
			recorded.reason = reason
			return nil
		},
	}
	relStore := &mockRelationStore{}

	cfg := DefaultSchedulerConfig()
	cfg.ForgetEnabled = true
	cfg.ForgetThreshold = 0.4

	scheduler := NewScheduler(scoreCalculatorWithZeroDecay(), nil, obsStore, relStore, cfg, zerolog.Nop())
	err := scheduler.RunForgetting(s.ctx)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), 1, recorded.calls)
	assert.Equal(s.T(), obsID, recorded.id)
	assert.Equal(s.T(), "consolidation: below relevance threshold", recorded.reason)
}

func (s *SchedulerSuite) TestRunAssociations_SkipsWhenEngineIsNil() {
	obsStore := &mockObservationStore{}
	relStore := &mockRelationStore{}
	scheduler := NewScheduler(scoreCalculatorWithZeroDecay(), nil, obsStore, relStore, DefaultSchedulerConfig(), zerolog.Nop())

	err := scheduler.RunAssociations(s.ctx)
	assert.NoError(s.T(), err)
	assert.Equal(s.T(), 0, obsStore.getRecentCalled)
	assert.Equal(s.T(), 0, relStore.storeRelationCalled)
}

func (s *SchedulerSuite) TestStop_DoubleStopSafe() {
	scheduler := NewScheduler(scoreCalculatorWithZeroDecay(), nil, &mockObservationStore{}, &mockRelationStore{}, DefaultSchedulerConfig(), zerolog.Nop())

	assert.NotPanics(s.T(), func() {
		scheduler.Stop()
		scheduler.Stop()
	})
}

func scoreCalculatorWithZeroDecay() *scoring.RelevanceCalculator {
	cfg := scoring.DefaultRelevanceConfig()
	cfg.BaseDecayRate = 0
	cfg.AccessDecayRate = 0
	cfg.RelationWeight = 0
	cfg.MinRelevance = 0
	return scoring.NewRelevanceCalculator(cfg)
}

func newSchedulerForForgetting(obs *models.Observation) *Scheduler {
	obsStore := &mockObservationStore{
		getAllFn: func(context.Context) ([]*models.Observation, error) {
			return []*models.Observation{obs}, nil
		},
	}
	relStore := &mockRelationStore{}
	cfg := DefaultSchedulerConfig()
	cfg.ForgetEnabled = true
	cfg.ForgetThreshold = 0.4
	cfg.Project = "project"
	return NewScheduler(scoreCalculatorWithZeroDecay(), nil, obsStore, relStore, cfg, zerolog.Nop())
}
