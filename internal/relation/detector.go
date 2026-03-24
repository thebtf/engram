// Package relation provides async relation and conflict detection for observations.
package relation

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/vector"
	"github.com/thebtf/engram/pkg/models"
)

// queueSize is the buffer size for the detection queue.
const queueSize = 256

// detectionTimeout is the maximum time for a single detection run.
const detectionTimeout = 5 * time.Second

// similarityThreshold is the minimum cosine similarity for candidate observations.
const similarityThreshold = 0.6

// supersedeSimilarityThreshold is the minimum similarity for supersession detection.
const supersedeSimilarityThreshold = 0.92

// contradictSimilarityThreshold is the minimum similarity for contradiction detection.
const contradictSimilarityThreshold = 0.7

// evolvesFromSimilarityThreshold is the minimum similarity for evolves_from detection.
const evolvesFromSimilarityThreshold = 0.7

// candidateLimit is the number of similar observations to retrieve from vector search.
const candidateLimit = 20

// detectRequest represents a queued detection request.
type detectRequest struct {
	ObsID   int64
	Project string
}

// Detector performs async relation and conflict detection after observation creation.
type Detector struct {
	vectorClient     vector.Client
	relationStore    *gorm.RelationStore
	conflictStore    *gorm.ConflictStore
	observationStore *gorm.ObservationStore
	parentCtx        context.Context
	queue            chan detectRequest
}

// NewDetector creates a new relation detector.
// All parameters are required; passing nil for any will cause panics at detection time.
func NewDetector(
	vectorClient vector.Client,
	relationStore *gorm.RelationStore,
	conflictStore *gorm.ConflictStore,
	observationStore *gorm.ObservationStore,
) *Detector {
	return &Detector{
		vectorClient:     vectorClient,
		relationStore:    relationStore,
		conflictStore:    conflictStore,
		observationStore: observationStore,
		queue:            make(chan detectRequest, queueSize),
	}
}

// Start launches the background goroutine that processes detection requests.
// It blocks until ctx is cancelled; callers should run this in a goroutine.
func (d *Detector) Start(ctx context.Context) {
	d.parentCtx = ctx
	log.Info().Msg("Relation detector started")
	for {
		select {
		case <-ctx.Done():
			// Drain remaining items before exiting
			for {
				select {
				case req := <-d.queue:
					d.processRequest(req)
				default:
					log.Info().Msg("Relation detector stopped")
					return
				}
			}
		case req := <-d.queue:
			d.processRequest(req)
		}
	}
}

// Enqueue adds a detection request to the queue. Non-blocking: drops the request
// and logs a warning if the queue is full.
func (d *Detector) Enqueue(obsID int64, project string) {
	select {
	case d.queue <- detectRequest{ObsID: obsID, Project: project}:
		// Successfully queued
	default:
		log.Warn().
			Int64("obs_id", obsID).
			Str("project", project).
			Msg("Relation detection queue full, dropping request")
	}
}

// processRequest runs detection for a single request with a timeout.
func (d *Detector) processRequest(req detectRequest) {
	ctx, cancel := context.WithTimeout(d.parentCtx, detectionTimeout)
	defer cancel()

	if err := d.Detect(ctx, req.ObsID, req.Project); err != nil {
		log.Warn().Err(err).
			Int64("obs_id", req.ObsID).
			Str("project", req.Project).
			Msg("Relation detection failed")
	}
}

// BackfillRelations processes existing observations to detect relations.
// Processes in batches, calling Detect() for each observation.
// Returns the number of observations processed and relations created.
func (d *Detector) BackfillRelations(ctx context.Context, project string, batchSize int, onProgress func(processed, total int)) (int, int, error) {
	if batchSize <= 0 {
		batchSize = 50
	}

	var totalProcessed, totalRelations int
	offset := 0

	for {
		// Check context cancellation between batches
		select {
		case <-ctx.Done():
			return totalProcessed, totalRelations, ctx.Err()
		default:
		}

		var observations []*models.Observation
		var total int64
		var err error

		if project != "" {
			observations, total, err = d.observationStore.GetObservationsByProjectStrictPaginated(ctx, project, batchSize, offset)
		} else {
			observations, total, err = d.observationStore.GetAllRecentObservationsPaginated(ctx, batchSize, offset)
		}
		if err != nil {
			return totalProcessed, totalRelations, fmt.Errorf("fetch observations batch at offset %d: %w", offset, err)
		}

		if len(observations) == 0 {
			break
		}

		for _, obs := range observations {
			detectCtx, cancel := context.WithTimeout(ctx, detectionTimeout)
			if err := d.Detect(detectCtx, obs.ID, obs.Project); err != nil {
				log.Warn().Err(err).Int64("obs_id", obs.ID).Msg("Backfill relation detection failed for observation")
			} else {
				totalRelations++ // approximate: counts observations where Detect ran successfully
			}
			cancel()
			totalProcessed++
		}

		if onProgress != nil {
			onProgress(totalProcessed, int(total))
		}

		offset += batchSize

		// Sleep between batches to avoid overwhelming DB
		select {
		case <-ctx.Done():
			return totalProcessed, totalRelations, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}

	return totalProcessed, totalRelations, nil
}

// Detect runs relation and conflict detection for a single observation.
func (d *Detector) Detect(ctx context.Context, obsID int64, project string) error {
	// 1. Load the new observation
	obs, err := d.observationStore.GetObservationByID(ctx, obsID)
	if err != nil {
		return fmt.Errorf("load observation %d: %w", obsID, err)
	}
	if obs == nil {
		return fmt.Errorf("observation %d not found", obsID)
	}

	// 2. Build embedding text from title and narrative
	embedText := buildEmbedText(obs)
	if embedText == "" {
		return nil // Nothing to embed, skip detection
	}

	// 3. Vector similarity search for candidates in same project
	where := vector.BuildWhereFilter(vector.DocTypeObservation, project, true)
	results, err := d.vectorClient.Query(ctx, embedText, candidateLimit, where)
	if err != nil {
		return fmt.Errorf("vector query: %w", err)
	}

	// 4. Extract candidate observation IDs (excluding self)
	candidateIDs := extractCandidateIDs(results, obsID, project)
	if len(candidateIDs) == 0 {
		return nil // No similar observations found
	}

	// 5. Load candidate observations
	candidates, err := d.observationStore.GetObservationsByIDsPreserveOrder(ctx, candidateIDs)
	if err != nil {
		return fmt.Errorf("load candidates: %w", err)
	}

	// 6. Build similarity map from vector results
	similarityMap := buildSimilarityMap(results)

	// 7. Classify relations and store results
	var relationsStored, conflictsStored int
	for _, candidate := range candidates {
		candidateSimilarity := similarityMap[candidate.ID]
		if candidateSimilarity < similarityThreshold {
			continue
		}

		relationType, confidence := classifyRelation(obs, candidate, candidateSimilarity)
		if relationType == "" {
			continue
		}

		// Store relation
		relation := models.NewObservationRelation(
			obs.ID,
			candidate.ID,
			relationType,
			confidence,
			models.DetectionSourceEmbeddingSimilarity,
			fmt.Sprintf("similarity=%.3f", candidateSimilarity),
		)
		if _, err := d.relationStore.StoreRelation(ctx, relation); err != nil {
			log.Warn().Err(err).
				Int64("source", obs.ID).
				Int64("target", candidate.ID).
				Str("type", string(relationType)).
				Msg("Failed to store relation")
			continue
		}
		relationsStored++

		// Store conflict for supersedes and contradicts relations
		if relationType == models.RelationSupersedes {
			conflict := models.NewObservationConflict(
				obs.ID,
				candidate.ID,
				models.ConflictSuperseded,
				models.ResolutionPreferNewer,
				fmt.Sprintf("superseded by observation %d (similarity=%.3f)", obs.ID, candidateSimilarity),
			)
			if _, err := d.conflictStore.StoreConflict(ctx, conflict); err != nil {
				log.Warn().Err(err).
					Int64("newer", obs.ID).
					Int64("older", candidate.ID).
					Msg("Failed to store supersede conflict")
			} else {
				conflictsStored++
				// Mark old observation as superseded
				if err := d.conflictStore.MarkObservationSuperseded(ctx, candidate.ID); err != nil {
					log.Warn().Err(err).
						Int64("obs_id", candidate.ID).
						Msg("Failed to mark observation as superseded")
				}
			}
		}

		if relationType == models.RelationContradicts {
			conflict := models.NewObservationConflict(
				obs.ID,
				candidate.ID,
				models.ConflictContradicts,
				models.ResolutionManual,
				fmt.Sprintf("contradicts observation %d (similarity=%.3f)", candidate.ID, candidateSimilarity),
			)
			if _, err := d.conflictStore.StoreConflict(ctx, conflict); err != nil {
				log.Warn().Err(err).
					Int64("newer", obs.ID).
					Int64("older", candidate.ID).
					Msg("Failed to store contradiction conflict")
			} else {
				conflictsStored++
			}
		}
	}

	if relationsStored > 0 || conflictsStored > 0 {
		log.Debug().
			Int64("obs_id", obsID).
			Int("relations", relationsStored).
			Int("conflicts", conflictsStored).
			Msg("Relation detection completed")
	}

	return nil
}

// buildEmbedText constructs the text to embed from an observation's title and narrative.
func buildEmbedText(obs *models.Observation) string {
	var text string
	if obs.Title.Valid && obs.Title.String != "" {
		text = obs.Title.String
	}
	if obs.Narrative.Valid && obs.Narrative.String != "" {
		if text != "" {
			text += " "
		}
		text += obs.Narrative.String
	}
	return text
}

// extractCandidateIDs extracts observation IDs from vector results, excluding the source observation.
func extractCandidateIDs(results []vector.QueryResult, excludeID int64, project string) []int64 {
	ids := vector.ExtractObservationIDs(results, project)
	filtered := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id != excludeID {
			filtered = append(filtered, id)
		}
	}
	return filtered
}

// buildSimilarityMap creates a map of observation ID -> similarity score from vector results.
func buildSimilarityMap(results []vector.QueryResult) map[int64]float64 {
	m := make(map[int64]float64, len(results))
	for _, r := range results {
		id := vector.ExtractRowID(r.Metadata)
		if id != 0 {
			// Keep highest similarity per ID (multiple vectors per observation possible)
			if existing, ok := m[id]; !ok || r.Similarity > existing {
				m[id] = r.Similarity
			}
		}
	}
	return m
}

// classifyRelation determines the relation type between two observations based on similarity
// and observation metadata. Returns empty string and 0 if no relation is detected.
func classifyRelation(newObs, candidate *models.Observation, similarity float64) (models.RelationType, float64) {
	// supersedes: very high similarity, same type, same project
	if similarity > supersedeSimilarityThreshold && newObs.Type == candidate.Type {
		return models.RelationSupersedes, similarity
	}

	// fixes: new bugfix relates to existing problem/discovery
	if newObs.Type == models.ObsTypeBugfix && (candidate.Type == models.ObsTypeBugfix || candidate.Type == models.ObsTypeDiscovery) {
		if conceptOverlap(newObs, candidate) > 0.3 {
			return models.RelationFixes, similarity * 0.9
		}
	}

	// explains: new discovery/feature explains existing
	if newObs.Type == models.ObsTypeDiscovery || newObs.Type == models.ObsTypeFeature {
		if conceptOverlap(newObs, candidate) > 0.4 {
			return models.RelationExplains, similarity * 0.85
		}
	}

	// contradicts: decisions with different conclusions on same topic.
	// Exclude guidance/behavioral rules — they don't contradict each other,
	// they are independent user preferences on different topics.
	if newObs.Type == models.ObsTypeDecision && candidate.Type == models.ObsTypeDecision && similarity > contradictSimilarityThreshold {
		isGuidanceNew := hasGuidanceConcept(newObs)
		isGuidanceCandidate := hasGuidanceConcept(candidate)
		if !isGuidanceNew && !isGuidanceCandidate {
			// Different titles suggest different conclusions on same topic
			if newObs.Title.Valid && candidate.Title.Valid && newObs.Title.String != candidate.Title.String {
				return models.RelationContradicts, similarity * 0.8
			}
		}
	}

	// evolves_from: same type, moderate similarity
	if newObs.Type == candidate.Type && similarity > evolvesFromSimilarityThreshold && similarity <= supersedeSimilarityThreshold {
		return models.RelationEvolvesFrom, similarity * 0.75
	}

	return "", 0 // no relation detected
}

// hasGuidanceConcept checks if an observation is a behavioral rule (user preference).
// These should not be classified as contradictions with each other.
func hasGuidanceConcept(obs *models.Observation) bool {
	if obs.Type == models.ObsTypeGuidance {
		return true
	}
	for _, c := range obs.Concepts {
		if c == "user-preference" {
			return true
		}
	}
	// Title heuristic: imported rules start with "Rule: "
	if obs.Title.Valid && len(obs.Title.String) > 6 && obs.Title.String[:6] == "Rule: " {
		return true
	}
	return false
}

// conceptOverlap calculates the Jaccard similarity of concept tags between two observations.
func conceptOverlap(a, b *models.Observation) float64 {
	if len(a.Concepts) == 0 || len(b.Concepts) == 0 {
		return 0
	}

	set := make(map[string]bool, len(a.Concepts))
	for _, c := range a.Concepts {
		set[c] = true
	}

	var intersection int
	for _, c := range b.Concepts {
		if set[c] {
			intersection++
		}
	}

	union := len(set) + len(b.Concepts) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
