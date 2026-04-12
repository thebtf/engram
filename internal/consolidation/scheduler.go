// Package consolidation provides memory consolidation lifecycle management.
package consolidation

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"github.com/thebtf/engram/internal/scoring"
	"github.com/thebtf/engram/pkg/models"
)

// ObservationProvider is the subset of observation store methods needed by the scheduler.
type ObservationProvider interface {
	GetAllObservations(ctx context.Context) ([]*models.Observation, error)
	GetAllObservationsIterator(ctx context.Context, batchSize int, callback func([]*models.Observation) bool) error
	GetRecentObservations(ctx context.Context, project string, limit int) ([]*models.Observation, error)
	GetOldestObservations(ctx context.Context, project string, limit int) ([]*models.Observation, error)
	UpdateImportanceScores(ctx context.Context, scores map[int64]float64) error
	IncrementImportanceScores(ctx context.Context, deltas map[int64]float64, cap float64) error
	ArchiveObservation(ctx context.Context, id int64, reason string) error
}

// RelationProvider is the subset of relation store methods needed by the scheduler.
type RelationProvider interface {
	GetRelationsByObservationID(ctx context.Context, obsID int64) ([]*models.ObservationRelation, error)
	StoreRelation(ctx context.Context, relation *models.ObservationRelation) (int64, error)
	GetRelationCount(ctx context.Context, obsID int64) (int, error)
	GetRelationCountsBatch(ctx context.Context, obsIDs []int64) (map[int64]int, error)
	GetAvgConfidenceBatch(ctx context.Context, obsIDs []int64) (map[int64]float64, error)
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
		AssociationInterval: 24 * time.Hour, // Daily (was weekly)
		ForgetInterval:      2160 * time.Hour,
		ForgetEnabled:       true,
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

	scores := make(map[int64]float64)
	var iterErr error

	iterationErr := s.obsStore.GetAllObservationsIterator(ctx, 500, func(observations []*models.Observation) bool {
		if ctx.Err() != nil {
			iterErr = ctx.Err()
			return false
		}

		if len(observations) == 0 {
			return true
		}

		ids := make([]int64, 0, len(observations))
		for _, obs := range observations {
			ids = append(ids, obs.ID)
		}

		relCounts, err := s.relStore.GetRelationCountsBatch(ctx, ids)
		if err != nil {
			iterErr = err
			return false
		}

		avgConfs, err := s.relStore.GetAvgConfidenceBatch(ctx, ids)
		if err != nil {
			iterErr = err
			return false
		}

		now := time.Now()
		for _, obs := range observations {
			ageDays := now.Sub(time.UnixMilli(obs.CreatedAtEpoch)).Hours() / 24.0
			if ageDays < 0 {
				ageDays = 0
			}

			accessRecencyDays := ageDays
			if obs.LastRetrievedAt.Valid && obs.LastRetrievedAt.Int64 > 0 {
				accessRecencyDays = now.Sub(time.UnixMilli(obs.LastRetrievedAt.Int64)).Hours() / 24.0
				if accessRecencyDays < 0 {
					accessRecencyDays = 0
				}
			}

			relCount := 0
			if value, ok := relCounts[obs.ID]; ok {
				relCount = value
			}

			avgConf := 0.5
			if value, ok := avgConfs[obs.ID]; ok {
				avgConf = value
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

		return true
	})

	if iterationErr != nil {
		return iterationErr
	}

	if iterErr != nil {
		return iterErr
	}

	if len(scores) == 0 {
		return nil
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

// ImportanceBoostPerRelation is the default importance score boost per new relation.
const ImportanceBoostPerRelation = 0.05

// ImportanceScoreCap is the maximum importance score after boosting.
const ImportanceScoreCap = 1.0

// RunAssociations discovers creative associations between sampled observations.
// Uses stratified sampling: 50% recent + 50% oldest non-archived for cross-temporal discovery.
func (s *Scheduler) RunAssociations(ctx context.Context) error {
	if s.assocEngine == nil {
		s.logger.Debug().Msg("Association engine not available, skipping")
		return nil
	}

	start := time.Now()
	halfLimit := 50 // Half of 100 total pool

	// Stratified sampling: recent + oldest
	recent, err := s.obsStore.GetRecentObservations(ctx, s.config.Project, halfLimit)
	if err != nil {
		return err
	}
	oldest, err := s.obsStore.GetOldestObservations(ctx, s.config.Project, halfLimit)
	if err != nil {
		return err
	}

	// Merge and deduplicate by ID
	observations := mergeAndDedup(recent, oldest)

	results, err := s.assocEngine.DiscoverAssociations(ctx, observations)
	if err != nil {
		return err
	}

	stored := 0
	relatedIDs := make(map[int64]int) // ID -> new relation count
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
		relatedIDs[result.SourceID]++
		relatedIDs[result.TargetID]++
	}

	// Boost importance scores for observations that gained new relations
	if len(relatedIDs) > 0 {
		deltas := make(map[int64]float64, len(relatedIDs))
		for id, count := range relatedIDs {
			deltas[id] = float64(count) * ImportanceBoostPerRelation
		}
		if err := s.obsStore.IncrementImportanceScores(ctx, deltas, ImportanceScoreCap); err != nil {
			s.logger.Warn().Err(err).Msg("Failed to boost importance scores after associations")
		}
	}

	s.logger.Info().
		Int("pool_size", len(observations)).
		Int("recent", len(recent)).
		Int("oldest", len(oldest)).
		Int("discovered", len(results)).
		Int("stored", stored).
		Int("boosted", len(relatedIDs)).
		Dur("elapsed", time.Since(start)).
		Msg("Association cycle complete")

	return nil
}

// mergeAndDedup combines two observation slices and removes duplicates by ID.
func mergeAndDedup(a, b []*models.Observation) []*models.Observation {
	seen := make(map[int64]bool, len(a)+len(b))
	result := make([]*models.Observation, 0, len(a)+len(b))
	for _, obs := range a {
		if !seen[obs.ID] {
			seen[obs.ID] = true
			result = append(result, obs)
		}
	}
	for _, obs := range b {
		if !seen[obs.ID] {
			seen[obs.ID] = true
			result = append(result, obs)
		}
	}
	return result
}

// RunForgetting archives observations below the relevance threshold.
// Protected observations are never archived:
// - importance_score >= 0.7
// - age < 90 days
// - type in {decision, discovery, guidance, entity, wiki}
func (s *Scheduler) RunForgetting(ctx context.Context) error {
	if !s.config.ForgetEnabled {
		return nil
	}

	start := time.Now()

	archived := 0

	err := s.obsStore.GetAllObservationsIterator(ctx, 500, func(observations []*models.Observation) bool {
		if ctx.Err() != nil {
			return false
		}

		now := time.Now()
		for _, obs := range observations {
			if obs == nil {
				continue
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

			// Protection rule: important types (never archive these)
			switch obs.Type {
			case models.ObsTypeDecision, models.ObsTypeDiscovery, models.ObsTypeGuidance,
				models.ObsTypeEntity, models.ObsTypeWiki, models.ObsTypeTimeline:
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

		return true
	})
	if err != nil {
		return err
	}

	s.logger.Info().
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
