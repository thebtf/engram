// Package consolidation provides memory consolidation lifecycle management.
package consolidation

import (
	"context"
	"time"

	"github.com/lukaszraczylo/claude-mnemonic/internal/scoring"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
	"github.com/rs/zerolog"
)

// ObservationProvider is the subset of observation store methods needed by the scheduler.
type ObservationProvider interface {
	GetAllObservations(ctx context.Context) ([]*models.Observation, error)
	GetRecentObservations(ctx context.Context, project string, limit int) ([]*models.Observation, error)
	UpdateImportanceScores(ctx context.Context, scores map[int64]float64) error
	ArchiveObservation(ctx context.Context, id int64, reason string) error
}

// RelationProvider is the subset of relation store methods needed by the scheduler.
type RelationProvider interface {
	GetRelationsByObservationID(ctx context.Context, obsID int64) ([]*models.ObservationRelation, error)
	StoreRelation(ctx context.Context, relation *models.ObservationRelation) (int64, error)
	GetRelationCount(ctx context.Context, obsID int64) (int, error)
}

// SchedulerConfig contains scheduling intervals and thresholds.
type SchedulerConfig struct {
	// DecayInterval is the period between relevance recalculations (default 24h).
	DecayInterval time.Duration `json:"decay_interval"`
	// AssociationInterval is the period between creative association runs (default 168h / 1 week).
	AssociationInterval time.Duration `json:"association_interval"`
	// ForgetInterval is the period between forgetting cycles (default 2160h / 90 days).
	ForgetInterval time.Duration `json:"forget_interval"`
	// ForgetEnabled controls whether the forgetting cycle runs (default false).
	ForgetEnabled bool `json:"forget_enabled"`
	// ForgetThreshold is the relevance score below which observations may be archived (default 0.01).
	ForgetThreshold float64 `json:"forget_threshold"`
	// Project is the project scope for queries (empty = all projects).
	Project string `json:"project"`
}

// DefaultSchedulerConfig returns the default scheduler configuration.
func DefaultSchedulerConfig() SchedulerConfig {
	return SchedulerConfig{
		DecayInterval:       24 * time.Hour,
		AssociationInterval: 168 * time.Hour,
		ForgetInterval:      2160 * time.Hour,
		ForgetEnabled:       false,
		ForgetThreshold:     0.01,
	}
}

// Scheduler runs memory consolidation lifecycle tasks on a schedule.
type Scheduler struct {
	relevanceCalc *scoring.RelevanceCalculator
	assocEngine   *AssociationEngine
	obsStore      ObservationProvider
	relStore      RelationProvider
	config        SchedulerConfig
	logger        zerolog.Logger
	stopCh        chan struct{}
}

// NewScheduler creates a new consolidation scheduler.
func NewScheduler(
	relevanceCalc *scoring.RelevanceCalculator,
	assocEngine *AssociationEngine,
	obsStore ObservationProvider,
	relStore RelationProvider,
	config SchedulerConfig,
	logger zerolog.Logger,
) *Scheduler {
	return &Scheduler{
		relevanceCalc: relevanceCalc,
		assocEngine:   assocEngine,
		obsStore:      obsStore,
		relStore:      relStore,
		config:        config,
		logger:        logger.With().Str("component", "consolidation-scheduler").Logger(),
		stopCh:        make(chan struct{}),
	}
}

// Start begins the scheduler's background loops. Call from a goroutine.
func (s *Scheduler) Start(ctx context.Context) {
	s.logger.Info().
		Dur("decay_interval", s.config.DecayInterval).
		Dur("association_interval", s.config.AssociationInterval).
		Bool("forget_enabled", s.config.ForgetEnabled).
		Msg("Consolidation scheduler started")

	decayTicker := time.NewTicker(s.config.DecayInterval)
	assocTicker := time.NewTicker(s.config.AssociationInterval)
	defer decayTicker.Stop()
	defer assocTicker.Stop()

	// Conditionally create forget ticker
	var forgetCh <-chan time.Time
	if s.config.ForgetEnabled {
		forgetTicker := time.NewTicker(s.config.ForgetInterval)
		defer forgetTicker.Stop()
		forgetCh = forgetTicker.C
	}

	for {
		select {
		case <-ctx.Done():
			s.logger.Info().Msg("Consolidation scheduler stopping (context done)")
			return
		case <-s.stopCh:
			s.logger.Info().Msg("Consolidation scheduler stopping (stop signal)")
			return
		case <-decayTicker.C:
			if err := s.RunDecay(ctx); err != nil {
				s.logger.Error().Err(err).Msg("Decay cycle failed")
			}
		case <-assocTicker.C:
			if err := s.RunAssociations(ctx); err != nil {
				s.logger.Error().Err(err).Msg("Association cycle failed")
			}
		case <-forgetCh:
			if err := s.RunForgetting(ctx); err != nil {
				s.logger.Error().Err(err).Msg("Forgetting cycle failed")
			}
		}
	}
}

// Stop signals the scheduler to shut down gracefully.
func (s *Scheduler) Stop() {
	select {
	case <-s.stopCh:
		// Already stopped
	default:
		close(s.stopCh)
	}
}

// RunDecay recalculates relevance scores for all non-archived observations.
func (s *Scheduler) RunDecay(ctx context.Context) error {
	start := time.Now()

	observations, err := s.obsStore.GetAllObservations(ctx)
	if err != nil {
		return err
	}

	if len(observations) == 0 {
		return nil
	}

	now := time.Now()
	scores := make(map[int64]float64, len(observations))

	for _, obs := range observations {
		ageDays := now.Sub(time.UnixMilli(obs.CreatedAtEpoch)).Hours() / 24.0
		if ageDays < 0 {
			ageDays = 0
		}

		// Compute access recency
		accessRecencyDays := ageDays // default: same as age if never accessed
		if obs.LastRetrievedAt.Valid && obs.LastRetrievedAt.Int64 > 0 {
			accessRecencyDays = now.Sub(time.UnixMilli(obs.LastRetrievedAt.Int64)).Hours() / 24.0
			if accessRecencyDays < 0 {
				accessRecencyDays = 0
			}
		}

		// Get relation count
		relCount, err := s.relStore.GetRelationCount(ctx, obs.ID)
		if err != nil {
			relCount = 0
		}

		// Get average confidence
		avgConf := 0.5
		rels, err := s.relStore.GetRelationsByObservationID(ctx, obs.ID)
		if err == nil && len(rels) > 0 {
			totalConf := 0.0
			for _, r := range rels {
				totalConf += r.Confidence
			}
			avgConf = totalConf / float64(len(rels))
		}

		relevance := s.relevanceCalc.CalculateRelevance(scoring.RelevanceParams{
			AgeDays:           ageDays,
			AccessRecencyDays: accessRecencyDays,
			RelationCount:     relCount,
			ImportanceScore:   obs.ImportanceScore,
			AvgRelConfidence:  avgConf,
		})

		scores[obs.ID] = relevance
	}

	if err := s.obsStore.UpdateImportanceScores(ctx, scores); err != nil {
		return err
	}

	s.logger.Info().
		Int("count", len(scores)).
		Dur("elapsed", time.Since(start)).
		Msg("Decay cycle complete: relevance scores recalculated")

	return nil
}

// RunAssociations discovers creative associations between sampled observations.
func (s *Scheduler) RunAssociations(ctx context.Context) error {
	if s.assocEngine == nil {
		s.logger.Debug().Msg("Association engine not available, skipping")
		return nil
	}

	start := time.Now()

	observations, err := s.obsStore.GetRecentObservations(ctx, s.config.Project, 100)
	if err != nil {
		return err
	}

	results, err := s.assocEngine.DiscoverAssociations(ctx, observations)
	if err != nil {
		return err
	}

	stored := 0
	for _, result := range results {
		rel := models.NewObservationRelation(
			result.SourceID,
			result.TargetID,
			result.RelationType,
			result.Confidence,
			result.DetectionSource,
			result.Reason,
		)
		if _, err := s.relStore.StoreRelation(ctx, rel); err != nil {
			s.logger.Warn().Err(err).
				Int64("source", result.SourceID).
				Int64("target", result.TargetID).
				Msg("Failed to store association")
			continue
		}
		stored++
	}

	s.logger.Info().
		Int("discovered", len(results)).
		Int("stored", stored).
		Dur("elapsed", time.Since(start)).
		Msg("Association cycle complete")

	return nil
}

// RunForgetting archives observations below the relevance threshold.
// Protected observations are never archived:
// - importance_score >= 0.7
// - age < 90 days
// - type in {decision, discovery}
func (s *Scheduler) RunForgetting(ctx context.Context) error {
	if !s.config.ForgetEnabled {
		return nil
	}

	start := time.Now()

	observations, err := s.obsStore.GetAllObservations(ctx)
	if err != nil {
		return err
	}

	now := time.Now()
	archived := 0

	for _, obs := range observations {
		if ctx.Err() != nil {
			break
		}

		// Protection rule: high importance
		if obs.ImportanceScore >= 0.7 {
			continue
		}

		// Protection rule: age < 90 days
		ageDays := now.Sub(time.UnixMilli(obs.CreatedAtEpoch)).Hours() / 24.0
		if ageDays < 90 {
			continue
		}

		// Protection rule: important types
		if obs.Type == models.ObsTypeDecision || obs.Type == models.ObsTypeDiscovery {
			continue
		}

		// Check if below threshold (using importance_score as proxy for relevance)
		if obs.ImportanceScore >= s.config.ForgetThreshold {
			continue
		}

		if err := s.obsStore.ArchiveObservation(ctx, obs.ID, "consolidation: below relevance threshold"); err != nil {
			s.logger.Warn().Err(err).Int64("obs_id", obs.ID).Msg("Failed to archive observation")
			continue
		}
		archived++
	}

	s.logger.Info().
		Int("total", len(observations)).
		Int("archived", archived).
		Dur("elapsed", time.Since(start)).
		Msg("Forgetting cycle complete")

	return nil
}

// RunAll triggers all consolidation tasks in sequence.
func (s *Scheduler) RunAll(ctx context.Context) error {
	if err := s.RunDecay(ctx); err != nil {
		return err
	}
	if err := s.RunAssociations(ctx); err != nil {
		return err
	}
	if s.config.ForgetEnabled {
		if err := s.RunForgetting(ctx); err != nil {
			return err
		}
	}
	return nil
}
