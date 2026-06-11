package models

import (
	"strings"
	"testing"
	"time"
)

// TestCandidateStatus_IsValid verifies all 5 valid statuses are accepted and unknown values rejected.
func TestCandidateStatus_IsValid(t *testing.T) {
	valid := []CandidateStatus{
		CandidateStatusPending,
		CandidateStatusPromoted,
		CandidateStatusRejected,
		CandidateStatusSuperseded,
		CandidateStatusDecayed,
	}
	for _, s := range valid {
		if !s.IsValid() {
			t.Errorf("status %q should be valid", s)
		}
	}
	invalid := []CandidateStatus{"", "orphan_promoted", "active", "expired"}
	for _, s := range invalid {
		if s.IsValid() {
			t.Errorf("status %q should be invalid", s)
		}
	}
}

// TestNewCrystallizationCandidate_RequiresContent verifies empty content is rejected.
func TestNewCrystallizationCandidate_RequiresContent(t *testing.T) {
	_, err := NewCrystallizationCandidate("session-1", "", "rule", CandidateOptions{})
	if err == nil {
		t.Fatal("expected error for empty proposed_content")
	}
}

// TestNewCrystallizationCandidate_Defaults verifies field defaults are applied correctly.
func TestNewCrystallizationCandidate_Defaults(t *testing.T) {
	c, err := NewCrystallizationCandidate("s1", "some decision text", "", CandidateOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ProposedPromotionTarget != "none" {
		t.Errorf("expected default target 'none', got %q", c.ProposedPromotionTarget)
	}
	if c.ProposedTier != "episodic" {
		t.Errorf("expected default tier 'episodic', got %q", c.ProposedTier)
	}
	if c.ProposedEpistemicType != "observation" {
		t.Errorf("expected default epistemic_type 'observation', got %q", c.ProposedEpistemicType)
	}
	if c.PrivacyScope != "project" {
		t.Errorf("expected default privacy_scope 'project', got %q", c.PrivacyScope)
	}
	if c.Status != CandidateStatusPending {
		t.Errorf("expected status pending, got %q", c.Status)
	}
	if c.Confidence != 0.5 {
		t.Errorf("expected default confidence 0.5, got %v", c.Confidence)
	}
	if c.RecurrenceCount != 1 {
		t.Errorf("expected default recurrence_count 1, got %v", c.RecurrenceCount)
	}
}

// TestNewCrystallizationCandidate_ReviewAfterDurations verifies review_after is set
// per proposed_promotion_target per spec §FR-F4.
func TestNewCrystallizationCandidate_ReviewAfterDurations(t *testing.T) {
	cases := []struct {
		target  string
		minDays float64
		maxDays float64
	}{
		{"rule", 6.9, 7.1},
		{"semantic", 13.9, 14.1},
		{"procedural", 29.9, 30.1},
		{"episodic", 2.9, 3.1},
		{"none", 6.9, 7.1},    // default
		{"unknown", 6.9, 7.1}, // unknown → default
		{"", 6.9, 7.1},        // empty → "none" default
	}
	for _, tc := range cases {
		c, err := NewCrystallizationCandidate("s1", "content", tc.target, CandidateOptions{})
		if err != nil {
			t.Fatalf("target=%q unexpected error: %v", tc.target, err)
		}
		if c.ReviewAfter == nil {
			t.Fatalf("target=%q review_after must not be nil", tc.target)
		}
		days := time.Until(*c.ReviewAfter).Hours() / 24
		if days < tc.minDays || days > tc.maxDays {
			t.Errorf("target=%q: review_after days=%.2f not in [%.1f, %.1f]",
				tc.target, days, tc.minDays, tc.maxDays)
		}
	}
}

// TestNewCrystallizationCandidate_Fingerprint verifies fingerprint computation.
func TestNewCrystallizationCandidate_Fingerprint(t *testing.T) {
	c1, _ := NewCrystallizationCandidate("session-A", "decision text", "rule", CandidateOptions{})
	c2, _ := NewCrystallizationCandidate("session-A", "decision text", "rule", CandidateOptions{})
	c3, _ := NewCrystallizationCandidate("session-B", "decision text", "rule", CandidateOptions{})
	c4, _ := NewCrystallizationCandidate("session-A", "different text", "rule", CandidateOptions{})
	c5, _ := NewCrystallizationCandidate("", "content with no session", "rule", CandidateOptions{})

	// Same session+content → same fingerprint (deterministic).
	if c1.Fingerprint != c2.Fingerprint {
		t.Error("same session+content must produce identical fingerprints")
	}
	// Different session → different fingerprint.
	if c1.Fingerprint == c3.Fingerprint {
		t.Error("different sessions must produce different fingerprints")
	}
	// Different content → different fingerprint.
	if c1.Fingerprint == c4.Fingerprint {
		t.Error("different contents must produce different fingerprints")
	}
	// Non-empty fingerprint looks like a hex sha256 (64 chars).
	if len(c1.Fingerprint) != 64 {
		t.Errorf("fingerprint should be 64 hex chars, got %d: %s", len(c1.Fingerprint), c1.Fingerprint)
	}
	if !isHex(c1.Fingerprint) {
		t.Errorf("fingerprint should be hex, got %q", c1.Fingerprint)
	}
	// Empty session → empty fingerprint (idempotency guard disabled).
	if c5.Fingerprint != "" {
		t.Errorf("empty session_id should produce empty fingerprint, got %q", c5.Fingerprint)
	}
}

func isHex(s string) bool {
	if len(s) == 0 {
		return false
	}
	const hexChars = "0123456789abcdef"
	for _, r := range strings.ToLower(s) {
		if !strings.ContainsRune(hexChars, r) {
			return false
		}
	}
	return true
}

// TestReviewAfterForTarget_GoldenValues verifies all documented target durations.
func TestReviewAfterForTarget_GoldenValues(t *testing.T) {
	tests := []struct {
		target   string
		wantDays float64
	}{
		{"rule", 7},
		{"semantic", 14},
		{"procedural", 30},
		{"episodic", 3},
		{"none", 7},
		{"", 7},
		{"anything_else", 7},
	}
	for _, tt := range tests {
		d := ReviewAfterForTarget(tt.target)
		gotDays := d.Hours() / 24
		if gotDays != tt.wantDays {
			t.Errorf("ReviewAfterForTarget(%q) = %.0f days, want %.0f", tt.target, gotDays, tt.wantDays)
		}
	}
}
