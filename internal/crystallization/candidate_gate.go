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

// MemoryFingerprintChecker is an optional interface used by RouteDecision to detect
// flag-flip ON-direction duplicates: if a memory with the same fp-tag already exists
// (created while flag was OFF), a new candidate should not be created.
// Satisfied by *gorm.MemoryStore.
// When nil, the memory check is skipped (backwards-compatible).
type MemoryFingerprintChecker interface {
	// ListBySourceAgentAndTag returns memories for the given project/source_agent/tag tuple.
	// Returns an empty slice (not an error) when none match.
	ListBySourceAgentAndTag(ctx context.Context, project, sourceAgent, tag string) ([]*models.Memory, error)
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
//   - When memChecker is non-nil, also checks for an existing memory with fp:<fingerprint>
//     tag to handle flag-flip ON-direction duplicates (decision written as memory while
//     flag was OFF; flag flipped ON; same session fires again).
//   - On miss (neither candidate nor memory found): creates a new pending candidate.
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
//
// Flag-flip OFF-direction (candidate exists, flag flips OFF, memory created via legacy
// path): not guarded here — the legacy path already uses CreateWithLifecycleIfTagAbsent
// with fp-tag uniqueness, so the worst outcome is a pending orphan candidate that decays.
// This boundary is documented and accepted; the decay cycle handles cleanup.
func RouteDecision(
	ctx context.Context,
	decision ExtractedDecision,
	sessionID string,
	project string,
	candidateWriter CandidateWriter,
	memChecker MemoryFingerprintChecker,
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

	if c.Fingerprint != "" {
		// Idempotency check A: existing pending candidate with the same fingerprint.
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

		// Idempotency check B: existing memory with fp:<fingerprint> tag (flag-flip ON guard).
		// Skipped when memChecker is nil (backwards-compatible; integration tests without a
		// live DB pass nil and the skip is correct for the candidate-only test scenarios).
		if memChecker != nil && project != "" {
			fpTag := "fp:" + c.Fingerprint
			mems, err := memChecker.ListBySourceAgentAndTag(ctx, project, "crystallization", fpTag)
			if err != nil {
				// Non-fatal: log and continue (a failed check should not block new candidates).
				_ = err
			} else if len(mems) > 0 {
				return &RouteDecisionResult{
					Duplicate:         true,
					UsedCandidatePath: true,
				}, nil
			}
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
