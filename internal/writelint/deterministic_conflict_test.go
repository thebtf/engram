// Package writelint — finding 11 fix: deterministic conflict and supersede scenario tests.
// These tests replace the early-return skip pattern by using content that
// reliably triggers CorrectionPatterns (conflict) or high Jaccard (supersede).
// No conditional skips — content is chosen to guarantee signal detection.
package writelint_test

import (
	"context"
	"testing"

	"github.com/thebtf/engram/internal/writelint"
	"github.com/thebtf/engram/pkg/models"
)

// correctionContent matches CorrectionPatterns `(?i)\bcorrection:\s*` reliably.
// Used to guarantee a conflict signal fires in deterministic tests.
const correctionContent = "correction: max_connections should be 100 not 200 for this workload"

// supersedingContent has very high Jaccard with the seeded similar memory
// makeDupMemory(). Used to guarantee a dup/supersede signal fires.
// (dupContent is defined in orchestrator_test.go and shared across the package.)

// conflictSeedMemory is a memory whose content triggers DetectConflictsWithExisting
// when correctionContent is the new observation.
func conflictSeedMemory() *models.Memory {
	return &models.Memory{
		ID:           55,
		Project:      "test-det",
		Content:      "max_connections is set to 200 for production PostgreSQL",
		Tags:         []string{"postgres", "config"},
		PrivacyScope: "project",
		Status:       "active",
	}
}

// TestOrchestrator_Conflict_Deterministic verifies that a new observation using
// explicit correction language (CorrectionPatterns) reliably produces a
// conflict signal and a link_contradiction option.
// finding 11 fix: no early-return skip; correctionContent guarantees signal.
func TestOrchestrator_Conflict_Deterministic(t *testing.T) {
	orc, _, closer := buildOrchestrator([]*models.Memory{conflictSeedMemory()})
	defer closer()
	ctx := context.Background()

	p1, err := orc.Phase1(ctx, correctionContent, "test-det", "system")
	if err != nil {
		t.Fatalf("Phase1: %v", err)
	}

	// correctionContent is designed to fire DetectExplicitCorrection or high-Jaccard.
	// If stored=true the conflict detection did not fire — that indicates a regression.
	if p1.Stored {
		// High Jaccard is the alternative path: correctionContent may still match
		// the seed well enough to fire a dup signal instead of a conflict signal.
		// Either signal → stored=false. stored=true would be a regression.
		t.Fatalf("Phase1 deterministic conflict: stored=true, expected stored=false. "+
			"correctionContent %q did not trigger any lint signal against seed %q. "+
			"Check CorrectionPatterns and/or Jaccard threshold.",
			correctionContent, conflictSeedMemory().Content)
	}

	// Verify signals are non-empty.
	if len(p1.LintSignals) == 0 {
		t.Error("Phase1 deterministic conflict: expected at least one lint signal")
	}

	// Resolution options must include link_contradiction or supersede or merge_with.
	optionSet := map[string]bool{}
	for _, o := range p1.ResolutionOptions {
		optionSet[o.Option] = true
	}
	hasResolutionOption := optionSet["link_contradiction"] || optionSet["supersede"] || optionSet["merge_with"]
	if !hasResolutionOption {
		t.Errorf("Phase1 deterministic conflict: expected link_contradiction/supersede/merge_with, got: %v",
			p1.ResolutionOptions)
	}

	// Phase2: link_contradiction
	memID := int64(55)
	p2, p2Err := orc.Phase2(ctx, writelint.Phase2Request{
		Token:          p1.ResolutionToken,
		Option:         "link_contradiction",
		TargetMemoryID: &memID,
		Content:        correctionContent,
		Project:        "test-det",
		Actor:          "system",
	})
	if p2Err != nil {
		t.Fatalf("Phase2 link_contradiction: %v", p2Err)
	}
	if !p2.Stored {
		t.Error("Phase2 link_contradiction: expected stored=true")
	}
	// Action taken should reflect contradiction wiring (with or without graph store).
	validActions := map[string]bool{
		"store_with_contradiction_edge":        true,
		"store_with_contradiction_noted":       true,
		"store_with_contradiction_noted_edge_failed": true,
	}
	if !validActions[p2.ActionTaken] {
		t.Errorf("Phase2 link_contradiction: unexpected action_taken=%q", p2.ActionTaken)
	}
}

// TestOrchestrator_Supersede_Deterministic verifies that a supersede option is
// committed correctly when the signal fires and Phase2 is called with a valid token.
// finding 11 fix: uses dupContent (high Jaccard) to guarantee a dup/supersede signal.
func TestOrchestrator_Supersede_Deterministic(t *testing.T) {
	seedMem := makeDupMemory()
	orc, _, closer := buildOrchestrator([]*models.Memory{seedMem})
	defer closer()
	ctx := context.Background()

	p1, err := orc.Phase1(ctx, dupContent, "test", "system")
	if err != nil {
		t.Fatalf("Phase1: %v", err)
	}

	// dupContent has Jaccard >= 0.85 with makeDupMemory().Content by design.
	// stored=true would indicate a threshold regression.
	if p1.Stored {
		t.Fatalf("Phase1 deterministic supersede: stored=true, expected stored=false. "+
			"dupContent Jaccard with seed is below 0.85. Check dupContent and makeDupMemory content.")
	}

	memID := seedMem.ID
	p2, p2Err := orc.Phase2(ctx, writelint.Phase2Request{
		Token:          p1.ResolutionToken,
		Option:         "supersede",
		TargetMemoryID: &memID,
		Content:        dupContent,
		Project:        "test",
		Actor:          "system",
	})
	if p2Err != nil {
		t.Fatalf("Phase2 supersede: %v", p2Err)
	}
	if !p2.Stored {
		t.Error("Phase2 supersede: expected stored=true")
	}
	if p2.ActionTaken != "supersede_with_candidate" {
		t.Errorf("Phase2 supersede: expected action_taken='supersede_with_candidate', got %q", p2.ActionTaken)
	}
}
