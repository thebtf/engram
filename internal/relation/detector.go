// Package relation provides async relation and conflict detection for observations.
package relation

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// intentionalLinkPattern matches [[obs:1234]] syntax in observation narratives.
var intentionalLinkPattern = regexp.MustCompile(`\[\[obs:(\d+)\]\]`)

// hashString returns a stable int64 hash of a string (for file path → node ID mapping).
func hashString(s string) int64 {
	var h uint64 = 14695981039346656037 // FNV-1a offset basis
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211 // FNV-1a prime
	}
	return int64(h >> 1) // Ensure positive by right-shifting
}

// queueSize is the buffer size for the detection queue.
const queueSize = 256

// detectionTimeout is the maximum time for a single detection run.
const detectionTimeout = 5 * time.Second

// detectRequest represents a queued detection request.
type detectRequest struct {
	ObsID   int64
	Project string
}

// Detector performs async relation and conflict detection after observation creation.
// In v5, vector-based similarity detection is removed; only structural relations
// (temporal chain, file edges, intentional [[obs:N]] links) are detected.
type Detector struct {
	relationStore    *gorm.RelationStore
	conflictStore    *gorm.ConflictStore
	observationStore *gorm.ObservationStore
	promptStore      *gorm.PromptStore
	parentCtx        context.Context
	queue            chan detectRequest
}

// NewDetector creates a new relation detector.
// The vectorClient parameter is accepted for call-site compatibility but ignored in v5.
func NewDetector(
	vectorClient any,
	relationStore *gorm.RelationStore,
	conflictStore *gorm.ConflictStore,
	observationStore *gorm.ObservationStore,
	promptStore *gorm.PromptStore,
) *Detector {
	return &Detector{
		relationStore:    relationStore,
		conflictStore:    conflictStore,
		observationStore: observationStore,
		promptStore:      promptStore,
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
			observations, total, err = d.observationStore.GetObservationsByProjectStrictPaginated(ctx, project, "", "", "", "", batchSize, offset)
		} else {
			observations, total, err = d.observationStore.GetAllRecentObservationsPaginated(ctx, "", "", "", "", batchSize, offset)
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

	// 1a. Temporal chain linking: create "follows" relation to previous observation in same session (FR-4)
	if obs.SDKSessionID != "" && obs.PromptNumber.Valid && obs.PromptNumber.Int64 > 0 {
		promptNum := int(obs.PromptNumber.Int64)
		prevObs, prevErr := d.observationStore.GetPreviousObservationInSession(ctx, obs.SDKSessionID, promptNum)
		if prevErr != nil {
			log.Debug().Err(prevErr).Int64("obs_id", obsID).Msg("Temporal chain lookup failed")
		} else if prevObs != nil {
			rel := &models.ObservationRelation{
				SourceID:     prevObs.ID,
				TargetID:     obsID,
				RelationType: "follows",
				Confidence:   1.0,
			}
			if _, storeErr := d.relationStore.StoreRelation(ctx, rel); storeErr != nil {
				log.Debug().Err(storeErr).Int64("from", prevObs.ID).Int64("to", obsID).Msg("Failed to store follows relation")
			}
		}
	}

	// 1b. Prompt-observation linking: create "prompted_by" relation to triggering user prompt (FR-5)
	if obs.SDKSessionID != "" && obs.PromptNumber.Valid && obs.PromptNumber.Int64 > 0 && d.promptStore != nil {
		promptNum := int(obs.PromptNumber.Int64)
		promptID, promptErr := d.promptStore.GetPromptForObservation(ctx, obs.SDKSessionID, promptNum)
		if promptErr != nil {
			log.Debug().Err(promptErr).Int64("obs_id", obsID).Msg("Prompt-observation linking failed")
		} else if promptID > 0 {
			rel := &models.ObservationRelation{
				SourceID:     promptID,
				TargetID:     obsID,
				RelationType: "prompted_by",
				Confidence:   1.0,
			}
			if _, storeErr := d.relationStore.StoreRelation(ctx, rel); storeErr != nil {
				log.Debug().Err(storeErr).Int64("prompt_id", promptID).Int64("obs_id", obsID).Msg("Failed to store prompted_by relation")
			}
		}
	}

	// 1c. Intentional link parsing: [[obs:1234]] syntax in narrative → references/referenced_by edges (FR-36)
	if obs.Narrative.Valid && obs.Narrative.String != "" {
		refsFound := intentionalLinkPattern.FindAllStringSubmatch(obs.Narrative.String, -1)
		for _, match := range refsFound {
			if len(match) < 2 {
				continue
			}
			refID, parseErr := strconv.ParseInt(match[1], 10, 64)
			if parseErr != nil || refID == obsID {
				continue
			}
			// Create bidirectional edges: current → referenced, referenced → current
			refRel := &models.ObservationRelation{
				SourceID:     obsID,
				TargetID:     refID,
				RelationType: "references",
				Confidence:   1.0,
			}
			if _, storeErr := d.relationStore.StoreRelation(ctx, refRel); storeErr != nil {
				log.Debug().Err(storeErr).Int64("from", obsID).Int64("to", refID).Msg("Failed to store references relation")
			}
			backRel := &models.ObservationRelation{
				SourceID:     refID,
				TargetID:     obsID,
				RelationType: "referenced_by",
				Confidence:   1.0,
			}
			if _, storeErr := d.relationStore.StoreRelation(ctx, backRel); storeErr != nil {
				log.Debug().Err(storeErr).Int64("from", refID).Int64("to", obsID).Msg("Failed to store referenced_by relation")
			}
		}
	}

	// 1d. File→observation graph edges: files_modified/files_read → modifies/reads edges (FR-37)
	// These create graph edges for "what observations touch this file?" traversal
	for _, filePath := range obs.FilesModified {
		if filePath == "" {
			continue
		}
		// Use file path hash as a pseudo node ID (negative to avoid collision with observation IDs)
		fileNodeID := -int64(hashString(filePath))
		rel := &models.ObservationRelation{
			SourceID:        obsID,
			TargetID:        fileNodeID,
			RelationType:    "modifies",
			Confidence:      1.0,
			DetectionSource: models.DetectionSourceFileOverlap,
		}
		if _, storeErr := d.relationStore.StoreRelation(ctx, rel); storeErr != nil {
			log.Debug().Err(storeErr).Str("file", filePath).Int64("obs_id", obsID).Msg("Failed to store modifies relation")
		}
	}
	for _, filePath := range obs.FilesRead {
		if filePath == "" {
			continue
		}
		fileNodeID := -int64(hashString(filePath))
		rel := &models.ObservationRelation{
			SourceID:        obsID,
			TargetID:        fileNodeID,
			RelationType:    "reads",
			Confidence:      1.0,
			DetectionSource: models.DetectionSourceFileOverlap,
		}
		if _, storeErr := d.relationStore.StoreRelation(ctx, rel); storeErr != nil {
			log.Debug().Err(storeErr).Str("file", filePath).Int64("obs_id", obsID).Msg("Failed to store reads relation")
		}
	}

	// Vector similarity search removed in v5 (content_chunks table dropped).
	// Structural relations (temporal chain, file edges, intentional links) are detected above.

	return nil
}

