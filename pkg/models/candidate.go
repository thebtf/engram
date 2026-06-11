// Package models contains domain models for engram.
package models

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// CandidateStatus represents the lifecycle state of a crystallization candidate.
// Valid values: pending, promoted, rejected, superseded, decayed.
// The status CHECK constraint in migration 132 enforces the same enum server-side.
type CandidateStatus string

const (
	// CandidateStatusPending is the initial state for newly extracted candidates.
	CandidateStatusPending CandidateStatus = "pending"
	// CandidateStatusPromoted means the candidate was approved and a Memory row was created.
	CandidateStatusPromoted CandidateStatus = "promoted"
	// CandidateStatusRejected means an operator explicitly declined the candidate.
	CandidateStatusRejected CandidateStatus = "rejected"
	// CandidateStatusSuperseded means a newer candidate replaces this one.
	CandidateStatusSuperseded CandidateStatus = "superseded"
	// CandidateStatusDecayed means the sleep cycle expired the candidate without promotion.
	CandidateStatusDecayed CandidateStatus = "decayed"
)

// IsValid returns true iff s is one of the 5 legal CandidateStatus values.
func (s CandidateStatus) IsValid() bool {
	switch s {
	case CandidateStatusPending, CandidateStatusPromoted,
		CandidateStatusRejected, CandidateStatusSuperseded, CandidateStatusDecayed:
		return true
	}
	return false
}

// reviewAfterDurations maps proposed_promotion_target values to the initial
// review_after delay per spec §FR-F4 candidate lifecycle.
//
// Targets and durations:
//   - "rule"      → 7 days  (rules need operator confirmation before hardening)
//   - "semantic"  → 14 days (semantic memories; longer review window)
//   - "procedural"→ 30 days (procedural knowledge; highest bar)
//   - "episodic"  → 3 days  (short-lived episodic notes; fast decay path)
//   - "none"/"" → 7 days  (default / unspecified)
var reviewAfterDurations = map[string]time.Duration{
	"rule":       7 * 24 * time.Hour,
	"semantic":   14 * 24 * time.Hour,
	"procedural": 30 * 24 * time.Hour,
	"episodic":   3 * 24 * time.Hour,
}

// defaultReviewAfterDuration is used when proposed_promotion_target is absent or unknown.
const defaultReviewAfterDuration = 7 * 24 * time.Hour

// ReviewAfterForTarget returns the review_after delay for the given proposed_promotion_target.
// Unknown targets fall through to defaultReviewAfterDuration.
func ReviewAfterForTarget(target string) time.Duration {
	if d, ok := reviewAfterDurations[target]; ok {
		return d
	}
	return defaultReviewAfterDuration
}

// CrystallizationCandidate is the domain model for the crystallization_candidates table.
// It represents an extracted decision that has not yet been promoted to a full Memory.
//
// Idempotency: the Fingerprint field (sha256 of source_session_id+proposed_content)
// carries a partial unique index on status='pending' (migration 132), preventing
// duplicate pending candidates for the same session+content — same concept as
// CreateWithLifecycleIfTagAbsent's fp-tag mechanism on the memories table.
type CrystallizationCandidate struct {
	CreatedAt               time.Time    `json:"created_at"`
	UpdatedAt               time.Time    `json:"updated_at"`
	ReviewAfter             *time.Time   `json:"review_after,omitempty"`
	SourceSessionID         string       `json:"source_session_id"`
	ProposedContent         string       `json:"proposed_content"`
	ProposedTier            string       `json:"proposed_tier,omitempty"`
	ProposedEpistemicType   string       `json:"proposed_epistemic_type,omitempty"`
	ProposedPromotionTarget string       `json:"proposed_promotion_target,omitempty"`
	EvidenceHandles         []string     `json:"evidence_handles,omitempty"`
	PrivacyScope            string       `json:"privacy_scope,omitempty"`
	Status                  CandidateStatus `json:"status"`
	// Fingerprint is sha256(source_session_id + "\x00" + proposed_content) hex-encoded.
	// Empty string disables idempotency guard (used when session_id unknown).
	Fingerprint             string       `json:"fingerprint,omitempty"`
	AffectedProjects        []string     `json:"affected_projects,omitempty"`
	// PromotedMemoryID is set when status='promoted'. ON DELETE SET NULL via FK.
	PromotedMemoryID        *int64       `json:"promoted_memory_id,omitempty"`
	ID                      int64        `json:"id"`
	Confidence              float32      `json:"confidence,omitempty"`
	RecurrenceCount         int          `json:"recurrence_count,omitempty"`
}

// CandidateOptions carries optional fields for NewCrystallizationCandidate.
type CandidateOptions struct {
	Tier            string
	EpistemicType   string
	PrivacyScope    string
	EvidenceHandles []string
	AffectedProjects []string
	Confidence      float32
	RecurrenceCount int
}

// NewCrystallizationCandidate constructs a validated CrystallizationCandidate.
// Required: sourceSessionID (may be empty string — disables fingerprint guard),
// proposedContent (must be non-empty), proposedPromotionTarget (may be empty → defaults to "none").
// review_after is derived from proposedPromotionTarget per spec §FR-F4.
//
// Returns a non-nil error when proposedContent is empty.
func NewCrystallizationCandidate(
	sourceSessionID string,
	proposedContent string,
	proposedPromotionTarget string,
	opts CandidateOptions,
) (*CrystallizationCandidate, error) {
	if proposedContent == "" {
		return nil, fmt.Errorf("crystallization candidate: proposed_content must not be empty")
	}
	if proposedPromotionTarget == "" {
		proposedPromotionTarget = "none"
	}

	tier := opts.Tier
	if tier == "" {
		tier = "episodic"
	}
	epistemicType := opts.EpistemicType
	if epistemicType == "" {
		epistemicType = "observation"
	}
	privacyScope := opts.PrivacyScope
	if privacyScope == "" {
		privacyScope = "project"
	}
	confidence := opts.Confidence
	if confidence <= 0 {
		confidence = 0.5
	}
	recurrence := opts.RecurrenceCount
	if recurrence <= 0 {
		recurrence = 1
	}

	delay := ReviewAfterForTarget(proposedPromotionTarget)
	reviewAt := time.Now().UTC().Add(delay)

	fp := computeFingerprint(sourceSessionID, proposedContent)

	return &CrystallizationCandidate{
		SourceSessionID:         sourceSessionID,
		ProposedContent:         proposedContent,
		ProposedTier:            tier,
		ProposedEpistemicType:   epistemicType,
		ProposedPromotionTarget: proposedPromotionTarget,
		EvidenceHandles:         opts.EvidenceHandles,
		AffectedProjects:        opts.AffectedProjects,
		PrivacyScope:            privacyScope,
		Status:                  CandidateStatusPending,
		ReviewAfter:             &reviewAt,
		Confidence:              confidence,
		RecurrenceCount:         recurrence,
		Fingerprint:             fp,
	}, nil
}

// computeFingerprint returns a sha256 hex fingerprint of session+content.
// Empty session_id produces an empty fingerprint (idempotency guard disabled).
func computeFingerprint(sessionID, content string) string {
	if sessionID == "" {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(sessionID))
	h.Write([]byte{0}) // NUL separator
	h.Write([]byte(content))
	return fmt.Sprintf("%x", h.Sum(nil))
}
