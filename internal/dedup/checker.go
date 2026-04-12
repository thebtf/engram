// Package dedup provides shared near-duplicate detection for observations.
// Implements the Mem0 Algorithm 1 decision tree:
//   - cosine >= threshold (default 0.92): NOOP (near-duplicate, skip)
//   - cosine 0.75-threshold: UPDATE (supersede with EVOLVES_FROM)
//   - cosine < 0.75: ADD (new observation)
package dedup

import (
	"context"
	"fmt"

	"github.com/thebtf/engram/internal/vector"
	"gorm.io/gorm"
)

// Action represents the dedup decision.
type Action string

const (
	ActionAdd    Action = "ADD"    // New observation, no similar exists
	ActionUpdate Action = "UPDATE" // Supersede existing observation
	ActionNoop   Action = "NOOP"  // Near-duplicate, skip storage
)

// Result holds the dedup check outcome.
type Result struct {
	Action          Action
	ExistingID      int64   // ID of the matched observation (0 if ADD)
	Similarity      float64 // Cosine similarity of the best match (0 if ADD)
	CrossModelBoost bool    // True when UPDATE with different agent_sources → promote to cross_model
}

// CheckCrossModelPromotion determines if a dedup UPDATE result qualifies for
// cross-model promotion. Both agent_sources must be known (non-"unknown") and
// different from each other.
func CheckCrossModelPromotion(result *Result, newAgentSource, existingAgentSource string) bool {
	if result == nil || result.Action != ActionUpdate {
		return false
	}
	if newAgentSource == "" || newAgentSource == "unknown" {
		return false
	}
	if existingAgentSource == "" || existingAgentSource == "unknown" {
		return false
	}
	return newAgentSource != existingAgentSource
}

// DefaultNoopThreshold is the cosine similarity above which observations are considered
// near-duplicates and skipped (NOOP).
const DefaultNoopThreshold = 0.92

// DefaultUpdateThreshold is the cosine similarity above which observations supersede
// existing ones (UPDATE/EVOLVES_FROM).
const DefaultUpdateThreshold = 0.75

// CheckDuplicate queries the vector index for similar observations and returns
// the appropriate dedup action.
//
// Parameters:
//   - vectorClient: pgvector client for similarity search (nil → always ADD)
//   - db: gorm DB for suppression check (nil → skip suppression check)
//   - project: project scope for the search
//   - content: text content to check for duplicates
//   - noopThreshold: cosine similarity for NOOP (use DefaultNoopThreshold if 0)
func CheckDuplicate(
	ctx context.Context,
	vectorClient vector.Client,
	db *gorm.DB,
	project string,
	content string,
	noopThreshold float64,
) (*Result, error) {
	if vectorClient == nil || !vectorClient.IsConnected() {
		return &Result{Action: ActionAdd}, nil
	}

	if content == "" {
		return &Result{Action: ActionAdd}, nil
	}

	if noopThreshold <= 0 {
		noopThreshold = DefaultNoopThreshold
	}

	where := vector.BuildWhereFilter(vector.DocTypeObservation, project, true, nil)
	similar, err := vectorClient.Query(ctx, content, 5, where)
	if err != nil {
		return nil, fmt.Errorf("dedup vector query: %w", err)
	}

	if len(similar) == 0 {
		return &Result{Action: ActionAdd}, nil
	}

	topSim := similar[0].Similarity
	existingID := vector.ExtractRowID(similar[0].Metadata)

	// ExtractRowID can return 0 if metadata is missing — treat as no match.
	if existingID <= 0 {
		return &Result{Action: ActionAdd}, nil
	}

	// Check if the match is suppressed/archived — allow re-creation if so.
	if db != nil {
		var checkResult struct{ Count int64 }
		if checkErr := db.WithContext(ctx).
			Raw("SELECT COUNT(*) as count FROM observations WHERE id = ? AND (is_suppressed = TRUE OR COALESCE(is_archived, 0) != 0)", existingID).
			Scan(&checkResult).Error; checkErr == nil && checkResult.Count > 0 {
			// Existing match is suppressed/archived — treat as ADD
			return &Result{Action: ActionAdd}, nil
		}
	}

	if topSim >= noopThreshold {
		return &Result{
			Action:     ActionNoop,
			ExistingID: existingID,
			Similarity: topSim,
		}, nil
	}

	if topSim >= DefaultUpdateThreshold {
		return &Result{
			Action:     ActionUpdate,
			ExistingID: existingID,
			Similarity: topSim,
		}, nil
	}

	return &Result{Action: ActionAdd}, nil
}
