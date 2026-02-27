// Package consolidation provides memory consolidation lifecycle management.
package consolidation

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/lukaszraczylo/claude-mnemonic/internal/embedding"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
	"github.com/rs/zerolog"
)

// AssociationConfig contains parameters for creative association discovery.
type AssociationConfig struct {
	// SampleSize is the number of observations to sample per run (default 20).
	SampleSize int `json:"sample_size"`
	// ThemeSimilarity is the minimum cosine similarity for SHARES_THEME (default 0.7).
	ThemeSimilarity float64 `json:"theme_similarity"`
	// ExplainSimilarity is the minimum similarity for EXPLAINS relation (default 0.5).
	ExplainSimilarity float64 `json:"explain_similarity"`
	// ParallelMaxDays is the max age gap in days for PARALLEL_CONTEXT (default 7).
	ParallelMaxDays int `json:"parallel_max_days"`
	// ParallelMaxSim is the max similarity for PARALLEL_CONTEXT (default 0.4).
	ParallelMaxSim float64 `json:"parallel_max_sim"`
	// ContradictMaxSim is the max similarity for CONTRADICTS between decisions (default 0.3).
	ContradictMaxSim float64 `json:"contradict_max_sim"`
	// MinConfidence is the minimum confidence threshold for storing a relation (default 0.4).
	MinConfidence float64 `json:"min_confidence"`
}

// DefaultAssociationConfig returns the default association configuration.
func DefaultAssociationConfig() AssociationConfig {
	return AssociationConfig{
		SampleSize:        20,
		ThemeSimilarity:   0.7,
		ExplainSimilarity: 0.5,
		ParallelMaxDays:   7,
		ParallelMaxSim:    0.4,
		ContradictMaxSim:  0.3,
		MinConfidence:     0.4,
	}
}

// AssociationEngine discovers creative associations between observations.
type AssociationEngine struct {
	embedSvc *embedding.Service
	config   AssociationConfig
	logger   zerolog.Logger
}

// NewAssociationEngine creates a new association discovery engine.
func NewAssociationEngine(embedSvc *embedding.Service, config AssociationConfig, logger zerolog.Logger) *AssociationEngine {
	return &AssociationEngine{
		embedSvc: embedSvc,
		config:   config,
		logger:   logger.With().Str("component", "associations").Logger(),
	}
}

// DiscoverAssociations takes a list of observations and finds creative associations.
// It samples up to SampleSize observations and checks all pairs for type-pair rule matches.
// Returns relation detection results for new associations found.
func (e *AssociationEngine) DiscoverAssociations(ctx context.Context, observations []*models.Observation) ([]*models.RelationDetectionResult, error) {
	if len(observations) == 0 {
		return nil, nil
	}

	// Sample if needed
	sample := observations
	if len(sample) > e.config.SampleSize {
		sample = sampleObservations(observations, e.config.SampleSize)
	}

	// Pre-compute embeddings for all sampled observations
	embeddings := make(map[int64][]float32, len(sample))
	for _, obs := range sample {
		text := observationText(obs)
		if text == "" {
			continue
		}

		emb, err := e.embedSvc.Embed(text)
		if err != nil {
			e.logger.Warn().Err(err).Int64("obs_id", obs.ID).Msg("Failed to embed observation")
			continue
		}
		embeddings[obs.ID] = emb
	}

	// Check all pairs
	var results []*models.RelationDetectionResult
	for i := 0; i < len(sample); i++ {
		for j := i + 1; j < len(sample); j++ {
			if ctx.Err() != nil {
				return results, ctx.Err()
			}

			a := sample[i]
			b := sample[j]

			embA, okA := embeddings[a.ID]
			embB, okB := embeddings[b.ID]
			if !okA || !okB {
				continue
			}

			sim := CosineSimilarity(embA, embB)
			result := e.applyTypePairRules(a, b, sim)
			if result != nil {
				results = append(results, result)
			}
		}
	}

	e.logger.Info().
		Int("sample_size", len(sample)).
		Int("associations_found", len(results)).
		Msg("Creative association discovery complete")

	return results, nil
}

// applyTypePairRules checks type-pair rules for two observations and returns a relation if matched.
func (e *AssociationEngine) applyTypePairRules(a, b *models.Observation, similarity float64) *models.RelationDetectionResult {
	// Rule 1: Two Decisions + low similarity → CONTRADICTS
	if a.Type == models.ObsTypeDecision && b.Type == models.ObsTypeDecision && similarity < e.config.ContradictMaxSim {
		return &models.RelationDetectionResult{
			SourceID:        a.ID,
			TargetID:        b.ID,
			RelationType:    models.RelationContradicts,
			Confidence:      0.6,
			DetectionSource: models.DetectionSourceCreativeAssociation,
			Reason:          fmt.Sprintf("two decisions with low similarity (%.2f)", similarity),
		}
	}

	// Rule 2: {Insight, Pattern} types + similarity > threshold → EXPLAINS
	if isInsightOrPattern(a, b) && similarity > e.config.ExplainSimilarity {
		return &models.RelationDetectionResult{
			SourceID:        a.ID,
			TargetID:        b.ID,
			RelationType:    models.RelationExplains,
			Confidence:      0.7,
			DetectionSource: models.DetectionSourceCreativeAssociation,
			Reason:          fmt.Sprintf("insight/pattern pair with high similarity (%.2f)", similarity),
		}
	}

	// Rule 3: Any types + high similarity → SHARES_THEME
	if similarity > e.config.ThemeSimilarity {
		return &models.RelationDetectionResult{
			SourceID:        a.ID,
			TargetID:        b.ID,
			RelationType:    models.RelationSharesTheme,
			Confidence:      similarity,
			DetectionSource: models.DetectionSourceCreativeAssociation,
			Reason:          fmt.Sprintf("high cosine similarity (%.2f)", similarity),
		}
	}

	// Rule 4: Within N days + low similarity → PARALLEL_CONTEXT
	ageDiffDays := ageDifferenceDays(a.CreatedAtEpoch, b.CreatedAtEpoch)
	if ageDiffDays <= float64(e.config.ParallelMaxDays) && similarity < e.config.ParallelMaxSim {
		return &models.RelationDetectionResult{
			SourceID:        a.ID,
			TargetID:        b.ID,
			RelationType:    models.RelationParallelCtx,
			Confidence:      0.5,
			DetectionSource: models.DetectionSourceCreativeAssociation,
			Reason:          fmt.Sprintf("temporal proximity (%.0f days) with low similarity (%.2f)", ageDiffDays, similarity),
		}
	}

	return nil
}


// isInsightOrPattern returns true if either observation has insight/discovery or pattern/refactor type.
func isInsightOrPattern(a, b *models.Observation) bool {
	isInsight := func(o *models.Observation) bool {
		return o.Type == models.ObsTypeDiscovery || o.Type == models.ObsTypeBugfix
	}
	isPattern := func(o *models.Observation) bool {
		return o.Type == models.ObsTypeRefactor || o.Type == models.ObsTypeFeature
	}
	return (isInsight(a) && isPattern(b)) || (isPattern(a) && isInsight(b))
}

// ageDifferenceDays returns the absolute difference in days between two epoch millisecond timestamps.
func ageDifferenceDays(epochA, epochB int64) float64 {
	diffMs := epochA - epochB
	if diffMs < 0 {
		diffMs = -diffMs
	}
	return float64(diffMs) / (24 * 60 * 60 * 1000)
}

// observationText builds a searchable text from an observation's title, narrative, and facts.
func observationText(obs *models.Observation) string {
	var parts []string
	if obs.Title.Valid && obs.Title.String != "" {
		parts = append(parts, obs.Title.String)
	}
	if obs.Narrative.Valid && obs.Narrative.String != "" {
		parts = append(parts, obs.Narrative.String)
	}
	for _, fact := range obs.Facts {
		if fact != "" {
			parts = append(parts, fact)
		}
	}
	return strings.Join(parts, " ")
}

// sampleObservations returns a random sample of n observations from the given slice.
func sampleObservations(observations []*models.Observation, n int) []*models.Observation {
	if n >= len(observations) {
		return observations
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	indices := rng.Perm(len(observations))

	sample := make([]*models.Observation, n)
	for i := 0; i < n; i++ {
		sample[i] = observations[indices[i]]
	}
	return sample
}

// Note: The new detection source "creative_association" is specific to the consolidation
// package and not stored as a constant in pkg/models to keep models package clean.
