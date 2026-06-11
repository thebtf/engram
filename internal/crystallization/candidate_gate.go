// Package crystallization provides session-end decision and pattern extraction.
package crystallization

import (
	"context"
	"fmt"
	"os"

	"github.com/thebtf/engram/pkg/models"
)

// CandidateWriter is the interface the candidate gate uses to persist new candidates.
// Satisfied by *gorm.CandidateStore (injected at runtime when flag ON).
type CandidateWriter interface {
	// Create persists a new crystallization candidate and returns the stored version.
	Create(ctx context.Context, c *models.CrystallizationCandidate) (*models.CrystallizationCandidate, error)
	// GetByFingerprint looks up a pending candidate by fingerprint (idempotency check).
	// Returns nil, nil when absent.
	GetByFingerprint(ctx context.Context, fingerprint string) (*models.CrystallizationCandidate, error)
}

// VnextFEnabled reports whether the Milestone-F candidate routing is active.
// Centralised check: ENGRAM_VNEXT_F_ENABLED="true".
func VnextFEnabled() bool {
	return os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true"
}

// RouteDecisionResult is returned by RouteDecision.
type RouteDecisionResult struct {
	// CandidateID is set (> 0) when the decision was routed to candidates (flag ON, not a duplicate).
	CandidateID int64
	// MemoryID is set (> 0) when the decision was routed to memories (flag OFF).
	MemoryID int64
	// Duplicate is true when an existing pending candidate or memory with the same fingerprint exists.
	Duplicate bool
	// UsedCandidatePath reports whether the candidate path was active (flag ON).
	UsedCandidatePath bool
}

// RouteDecision determines where to persist an extracted decision.
//
// When ENGRAM_VNEXT_F_ENABLED=true AND candidateWriter is non-nil:
//   - Checks for an existing pending candidate with the same fingerprint (idempotency).
//   - On miss: creates a new pending candidate and returns CandidateID.
//   - On hit: returns Duplicate=true.
//
// When flag is OFF or candidateWriter is nil:
//   - Returns (nil, nil) to signal the caller should use the legacy memory path.
//
// B9 resolution: this function IS the mechanism by which operator decision #2766
// ("no auto-promotion of semantic→procedural") is satisfied when flag ON.
// Extracted decisions land as pending candidates requiring explicit promote_candidate
// instead of being auto-promoted to procedural memories. The candidate path is the
// gated promotion surface.
func RouteDecision(
	ctx context.Context,
	decision ExtractedDecision,
	sessionID string,
	project string,
	candidateWriter CandidateWriter,
) (*RouteDecisionResult, error) {
	if !VnextFEnabled() || candidateWriter == nil {
		// Flag OFF: caller should use legacy memory path unchanged.
		return nil, nil
	}

	// Build domain candidate to derive fingerprint.
	c, err := models.NewCrystallizationCandidate(
		sessionID,
		decision.Text,
		"rule", // default promotion target for session-end extracts
		models.CandidateOptions{
			AffectedProjects: []string{project},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("route_decision build candidate: %w", err)
	}

	// Idempotency: check for existing pending candidate with the same fingerprint.
	if c.Fingerprint != "" {
		existing, err := candidateWriter.GetByFingerprint(ctx, c.Fingerprint)
		if err != nil {
			return nil, fmt.Errorf("route_decision fingerprint check: %w", err)
		}
		if existing != nil {
			return &RouteDecisionResult{
				Duplicate:         true,
				UsedCandidatePath: true,
			}, nil
		}
	}

	created, err := candidateWriter.Create(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("route_decision create candidate: %w", err)
	}
	return &RouteDecisionResult{
		CandidateID:       created.ID,
		UsedCandidatePath: true,
	}, nil
}
