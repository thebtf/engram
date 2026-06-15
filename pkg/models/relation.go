// Package models contains domain models for engram.
package models

import (
	"strings"
	"time"
)

// RelationType labels the directed relationship between two observations.
// Values are stored in the database and must match migration 077's CHECK constraint.
type RelationType string

const (
	// RelationCauses: source observation caused target to happen.
	// Example: "This architectural decision caused this bug"
	RelationCauses RelationType = "causes"
	// RelationFixes: source observation resolves the issue described in target.
	// Example: "This bugfix addresses that discovered issue"
	RelationFixes RelationType = "fixes"
	// RelationSupersedes: source replaces target as the current valid approach.
	// Example: "This new approach replaces the old workaround"
	RelationSupersedes RelationType = "supersedes"
	// RelationDependsOn: source builds on or requires target.
	// Example: "This feature relies on that architectural decision"
	RelationDependsOn RelationType = "depends_on"
	// RelationRelatesTo: source and target are topically linked but no causal edge exists.
	// Example: "Both deal with authentication"
	RelationRelatesTo RelationType = "relates_to"
	// RelationEvolvesFrom: source is a refined or improved form of target.
	// Example: "This refined pattern evolved from that initial discovery"
	RelationEvolvesFrom   RelationType = "evolves_from"
	RelationLeadsTo       RelationType = "leads_to"
	RelationSimilarTo     RelationType = "similar_to"
	RelationContradicts   RelationType = "contradicts"
	RelationReinforces    RelationType = "reinforces"
	RelationInvalidatedBy RelationType = "invalidated_by"
	RelationExplains      RelationType = "explains"
	RelationSharesTheme   RelationType = "shares_theme"
	RelationParallelCtx   RelationType = "parallel_context"
	RelationSummarizes    RelationType = "summarizes"
	RelationPartOf        RelationType = "part_of"
	RelationPrefersOver   RelationType = "prefers_over"
	// RelationModifies: source touched a file also modified by target.
	// Added in migration 077 — file-relation detector.
	RelationModifies RelationType = "modifies"
	// RelationReads: source reads a file that target modified.
	// Added in migration 077 — file-relation detector.
	RelationReads RelationType = "reads"
	// RelationFollows: source observation temporally follows target within the same thread.
	// Added in migration 077 — detector FR-4.
	RelationFollows RelationType = "follows"
	// RelationPromptedBy: source was explicitly prompted by target.
	// Added in migration 077 — detector FR-5.
	RelationPromptedBy RelationType = "prompted_by"
	// RelationReferences: source explicitly references target.
	// Added in migration 077 — detector FR-36.
	RelationReferences RelationType = "references"
	// RelationReferencedBy: source is explicitly referenced by target (inverse of RelationReferences).
	// Added in migration 077 — detector FR-36.
	RelationReferencedBy RelationType = "referenced_by"
)

// AllRelationTypes is the single source of truth for the complete set of valid relation types.
// Keep in sync with:
//   - migration 077 CHECK constraint in internal/db/gorm/migrations.go
//   - GORM struct tag in internal/db/gorm/models.go (ObservationRelation.RelationType)
//
// The relation_test.go AllRelationTypes completeness test pins the count at 23 constants.
var AllRelationTypes = []RelationType{
	RelationCauses,
	RelationFixes,
	RelationSupersedes,
	RelationDependsOn,
	RelationRelatesTo,
	RelationEvolvesFrom,
	RelationLeadsTo,
	RelationSimilarTo,
	RelationContradicts,
	RelationReinforces,
	RelationInvalidatedBy,
	RelationExplains,
	RelationSharesTheme,
	RelationParallelCtx,
	RelationSummarizes,
	RelationPartOf,
	RelationPrefersOver,
	// Added in migration 077
	RelationModifies,
	RelationReads,
	RelationFollows,
	RelationPromptedBy,
	RelationReferences,
	RelationReferencedBy,
}

// RelationDetectionSource records how the relation was inferred.
// Stored in the database so consumers can weight relations by detection quality.
type RelationDetectionSource string

const (
	// DetectionSourceFileOverlap: both observations reference the same file paths.
	DetectionSourceFileOverlap RelationDetectionSource = "file_overlap"
	// DetectionSourceEmbeddingSimilarity: vector similarity above threshold.
	DetectionSourceEmbeddingSimilarity RelationDetectionSource = "embedding_similarity"
	// DetectionSourceTemporalProximity: observations share a session and are close in time.
	DetectionSourceTemporalProximity RelationDetectionSource = "temporal_proximity"
	// DetectionSourceNarrativeMention: newer observation's text contains relationship language.
	DetectionSourceNarrativeMention RelationDetectionSource = "narrative_mention"
	// DetectionSourceConceptOverlap: observations share concept tags.
	DetectionSourceConceptOverlap RelationDetectionSource = "concept_overlap"
	// DetectionSourceTypeProgression: observation types follow a natural lifecycle order.
	DetectionSourceTypeProgression RelationDetectionSource = "type_progression"
	// DetectionSourceCreativeAssociation: consolidation association engine produced the link.
	DetectionSourceCreativeAssociation RelationDetectionSource = "creative_association"
)

// ObservationRelation is the persisted directed edge between two observations.
// ValidFrom/ValidTo are used by time-bounded knowledge (e.g., temporary workarounds).
type ObservationRelation struct {
	RelationType    RelationType            `db:"relation_type" json:"relation_type"`
	DetectionSource RelationDetectionSource `db:"detection_source" json:"detection_source"`
	Reason          string                  `db:"reason" json:"reason,omitempty"`
	CreatedAt       string                  `db:"created_at" json:"created_at"`
	ID              int64                   `db:"id" json:"id"`
	SourceID        int64                   `db:"source_id" json:"source_id"`
	TargetID        int64                   `db:"target_id" json:"target_id"`
	Confidence      float64                 `db:"confidence" json:"confidence"`
	CreatedAtEpoch  int64                   `db:"created_at_epoch" json:"created_at_epoch"`
	ValidFrom       *time.Time              `db:"valid_from" json:"valid_from,omitempty"`
	ValidTo         *time.Time              `db:"valid_to" json:"valid_to,omitempty"`
}

// NewObservationRelation constructs a relation record ready for persistence.
// Timestamp is captured at construction time for audit purposes.
func NewObservationRelation(sourceID, targetID int64, relType RelationType, confidence float64, source RelationDetectionSource, reason string) *ObservationRelation {
	now := time.Now()
	return &ObservationRelation{
		SourceID:        sourceID,
		TargetID:        targetID,
		RelationType:    relType,
		Confidence:      confidence,
		DetectionSource: source,
		Reason:          reason,
		CreatedAt:       now.Format(time.RFC3339),
		CreatedAtEpoch:  now.UnixMilli(),
	}
}

// RelationDetectionResult is the in-memory result of a single relation detection pass.
// Callers accumulate these, deduplicate by target, and persist only the highest-confidence winner.
type RelationDetectionResult struct {
	RelationType    RelationType
	DetectionSource RelationDetectionSource
	Reason          string
	SourceID        int64
	TargetID        int64
	Confidence      float64
}

// DetectFileOverlapRelation checks for shared file references between newer and older.
// A shared modified file scores higher than a read-then-modified dependency.
// Observation type pairs are used to upgrade from the default RelationRelatesTo to a more
// specific type (fixes, evolves_from, depends_on, supersedes).
func DetectFileOverlapRelation(newer, older *Observation) *RelationDetectionResult {
	// Index newer's modified files for O(1) lookup.
	newerModified := make(map[string]bool, len(newer.FilesModified))
	for _, f := range newer.FilesModified {
		newerModified[f] = true
	}

	// Index older's modified files for cross-referencing.
	olderModified := make(map[string]bool, len(older.FilesModified))
	for _, f := range older.FilesModified {
		olderModified[f] = true
	}

	// Files both observations modified.
	var sharedModified []string
	for f := range newerModified {
		if olderModified[f] {
			sharedModified = append(sharedModified, f)
		}
	}

	// Files newer reads that older previously modified — read-after-write dependency.
	var newerReadsOlderModified []string
	for _, f := range newer.FilesRead {
		if olderModified[f] {
			newerReadsOlderModified = append(newerReadsOlderModified, f)
		}
	}

	overlap := len(sharedModified) + len(newerReadsOlderModified)
	if overlap == 0 {
		return nil
	}

	// Base confidence: 0.5 + 0.1 per overlapping file (capped at 1.0 after type boost).
	relType := RelationRelatesTo
	confidence := 0.5 + float64(overlap)*0.1

	// Type-pair inference: lift the relation type and add a confidence bonus.
	switch {
	case newer.Type == ObsTypeBugfix && (older.Type == ObsTypeDecision || older.Type == ObsTypeFeature):
		relType = RelationFixes
		confidence += 0.2
	case newer.Type == ObsTypeRefactor && older.Type == ObsTypeDiscovery:
		relType = RelationEvolvesFrom
		confidence += 0.15
	case newer.Type == older.Type && len(sharedModified) > 0:
		// Same type on the same files: newer supersedes older.
		relType = RelationSupersedes
		confidence += 0.1
	case newer.Type == ObsTypeFeature && older.Type == ObsTypeDecision:
		relType = RelationDependsOn
		confidence += 0.15
	}

	if confidence > 1.0 {
		confidence = 1.0
	}

	return &RelationDetectionResult{
		SourceID:        newer.ID,
		TargetID:        older.ID,
		RelationType:    relType,
		Confidence:      confidence,
		DetectionSource: DetectionSourceFileOverlap,
		Reason:          buildFileOverlapReason(sharedModified, newerReadsOlderModified),
	}
}

// buildFileOverlapReason produces a human-readable reason string for a file-overlap relation.
// Lists are truncated to 3 items to keep stored reasons terse.
func buildFileOverlapReason(shared, readsModified []string) string {
	var parts []string
	if len(shared) > 0 {
		parts = append(parts, "both modified: "+strings.Join(truncateList(shared, 3), ", "))
	}
	if len(readsModified) > 0 {
		parts = append(parts, "reads files modified by older: "+strings.Join(truncateList(readsModified, 3), ", "))
	}
	return strings.Join(parts, "; ")
}

// DetectConceptOverlapRelation checks whether observations share concept tags.
// Confidence scales with the Jaccard-like overlap ratio, boosted for high-value concepts
// (security, architecture, etc.) that carry stronger relational signal.
func DetectConceptOverlapRelation(newer, older *Observation) *RelationDetectionResult {
	newerConcepts := make(map[string]bool, len(newer.Concepts))
	for _, c := range newer.Concepts {
		newerConcepts[c] = true
	}

	var shared []string
	for _, c := range older.Concepts {
		if newerConcepts[c] {
			shared = append(shared, c)
		}
	}

	if len(shared) == 0 {
		return nil
	}

	// Union size for Jaccard denominator.
	totalUnique := len(newerConcepts)
	for _, c := range older.Concepts {
		if !newerConcepts[c] {
			totalUnique++
		}
	}

	overlapRatio := float64(len(shared)) / float64(totalUnique)
	confidence := 0.3 + overlapRatio*0.5

	// High-value concepts carry extra signal (security issues, architectural patterns).
	for _, c := range shared {
		if isHighValueConcept(c) {
			confidence += 0.1
		}
	}
	if confidence > 1.0 {
		confidence = 1.0
	}

	return &RelationDetectionResult{
		SourceID:        newer.ID,
		TargetID:        older.ID,
		RelationType:    RelationRelatesTo,
		Confidence:      confidence,
		DetectionSource: DetectionSourceConceptOverlap,
		Reason:          "shared concepts: " + strings.Join(truncateList(shared, 5), ", "),
	}
}

// isHighValueConcept returns true for concept tags that strongly indicate a meaningful relation.
// These topics recur across observations in ways that justify a confidence boost.
func isHighValueConcept(concept string) bool {
	highValue := map[string]bool{
		"security":       true,
		"architecture":   true,
		"gotcha":         true,
		"anti-pattern":   true,
		"best-practice":  true,
		"error-handling": true,
	}
	return highValue[concept]
}

// DetectTypeProgressionRelation checks for natural lifecycle progressions between observation types.
// The progression map encodes domain knowledge: discoveries precede decisions, decisions enable
// features, features develop bugs, etc.  A valid progression earns a base confidence of 0.4,
// raised for the most semantically precise pairings.
func DetectTypeProgressionRelation(newer, older *Observation) *RelationDetectionResult {
	// Maps each newer type to the older types that are natural predecessors.
	progressions := map[ObservationType][]ObservationType{
		ObsTypeBugfix:   {ObsTypeDiscovery, ObsTypeFeature, ObsTypeDecision},
		ObsTypeFeature:  {ObsTypeDiscovery, ObsTypeDecision},
		ObsTypeRefactor: {ObsTypeDiscovery, ObsTypeFeature, ObsTypeBugfix},
		ObsTypeDecision: {ObsTypeDiscovery},
		ObsTypeChange:   {ObsTypeDiscovery, ObsTypeDecision},
	}

	validPredecessors, ok := progressions[newer.Type]
	if !ok {
		return nil
	}

	// Check whether older.Type is a valid predecessor of newer.Type.
	isValid := false
	for _, pred := range validPredecessors {
		if older.Type == pred {
			isValid = true
			break
		}
	}
	if !isValid {
		return nil
	}

	var relType RelationType
	confidence := 0.4

	// Specific pairings carry higher semantic certainty.
	switch {
	case newer.Type == ObsTypeBugfix && older.Type == ObsTypeDiscovery:
		relType = RelationFixes
		confidence = 0.6
	case newer.Type == ObsTypeBugfix && older.Type == ObsTypeFeature:
		relType = RelationFixes
		confidence = 0.5
	case newer.Type == ObsTypeFeature && older.Type == ObsTypeDecision:
		relType = RelationDependsOn
		confidence = 0.6
	case newer.Type == ObsTypeRefactor:
		relType = RelationEvolvesFrom
		confidence = 0.5
	default:
		relType = RelationRelatesTo
	}

	return &RelationDetectionResult{
		SourceID:        newer.ID,
		TargetID:        older.ID,
		RelationType:    relType,
		Confidence:      confidence,
		DetectionSource: DetectionSourceTypeProgression,
		Reason:          string(older.Type) + " -> " + string(newer.Type) + " progression",
	}
}

// DetectTemporalProximityRelation links observations that share a session and are within
// 5 minutes of each other.  This is the weakest detector (base 0.3) and is only applied
// as a fallback when no stronger signal was found.
func DetectTemporalProximityRelation(newer, older *Observation) *RelationDetectionResult {
	// Different sessions cannot be temporally related by this detector.
	if newer.SDKSessionID != older.SDKSessionID {
		return nil
	}

	timeDiffMs := newer.CreatedAtEpoch - older.CreatedAtEpoch
	if timeDiffMs < 0 {
		timeDiffMs = -timeDiffMs
	}

	const fiveMinutesMs = int64(5 * 60 * 1000)
	if timeDiffMs > fiveMinutesMs {
		return nil
	}

	// Closer in time → higher confidence (linear from 0.3 at 5 min to 0.7 at 0 min).
	proximityRatio := 1.0 - (float64(timeDiffMs) / float64(fiveMinutesMs))
	confidence := 0.3 + proximityRatio*0.4

	return &RelationDetectionResult{
		SourceID:        newer.ID,
		TargetID:        older.ID,
		RelationType:    RelationRelatesTo,
		Confidence:      confidence,
		DetectionSource: DetectionSourceTemporalProximity,
		Reason:          "same session, close timestamps",
	}
}

// NarrativeMentionPatterns lists substring patterns and their associated relation types.
// A match in the newer observation's narrative upgrades the relation type and boosts confidence.
// Pattern strings are load-bearing — the relation_test.go suite pins their presence and values.
var NarrativeMentionPatterns = []struct {
	Pattern      string
	RelationType RelationType
	ConfBoost    float64
}{
	{" caused ", RelationCauses, 0.3},
	{" causes ", RelationCauses, 0.3},
	{" because of ", RelationCauses, 0.25},
	{" due to ", RelationCauses, 0.2},
	{" fixes ", RelationFixes, 0.3},
	{" fixed ", RelationFixes, 0.3},
	{" resolves ", RelationFixes, 0.3},
	{" addresses ", RelationFixes, 0.25},
	{" replaces ", RelationSupersedes, 0.3},
	{" supersedes ", RelationSupersedes, 0.35},
	{" instead of ", RelationSupersedes, 0.25},
	{" depends on ", RelationDependsOn, 0.3},
	{" requires ", RelationDependsOn, 0.25},
	{" builds on ", RelationDependsOn, 0.25},
	{" based on ", RelationDependsOn, 0.2},
	{" related to ", RelationRelatesTo, 0.2},
	{" similar to ", RelationRelatesTo, 0.2},
	{" evolved from ", RelationEvolvesFrom, 0.3},
	{" improved from ", RelationEvolvesFrom, 0.25},
	{" refined from ", RelationEvolvesFrom, 0.25},
}

// DetectNarrativeMentionRelation checks whether the newer observation's narrative contains
// language that explicitly names a relationship.  First match wins; patterns are ordered
// from strongest to weakest signal.
func DetectNarrativeMentionRelation(newer, older *Observation) *RelationDetectionResult {
	if !newer.Narrative.Valid || newer.Narrative.String == "" {
		return nil
	}

	narrative := strings.ToLower(newer.Narrative.String)

	for _, p := range NarrativeMentionPatterns {
		if !strings.Contains(narrative, p.Pattern) {
			continue
		}

		confidence := 0.4 + p.ConfBoost
		if confidence > 1.0 {
			confidence = 1.0
		}

		return &RelationDetectionResult{
			SourceID:        newer.ID,
			TargetID:        older.ID,
			RelationType:    p.RelationType,
			Confidence:      confidence,
			DetectionSource: DetectionSourceNarrativeMention,
			Reason:          "narrative contains '" + strings.TrimSpace(p.Pattern) + "' language",
		}
	}

	return nil
}

// DetectRelationsWithExisting is the main entry point for relation detection.
// It compares a newly-arrived observation against a pool of existing ones and returns
// the highest-confidence result per target observation.
//
// Detection order (highest to lowest priority):
//  1. File overlap
//  2. Concept overlap
//  3. Type progression
//  4. Temporal proximity (fallback only — not used if a stronger signal exists)
//  5. Narrative mention (can override any of the above if confidence is higher)
//
// Filtering rules mirror DetectConflictsWithExisting:
//   - Self-comparison skipped.
//   - Superseded observations skipped.
//   - Cross-project pairs filtered unless either party is ScopeGlobal.
func DetectRelationsWithExisting(newer *Observation, existing []*Observation, minConfidence float64) []*RelationDetectionResult {
	var results []*RelationDetectionResult
	seen := make(map[int64]bool)

	for _, older := range existing {
		// Skip self.
		if older.ID == newer.ID {
			continue
		}
		// Skip superseded observations — they are no longer authoritative.
		if older.IsSuperseded {
			continue
		}
		// Cross-project filter: skip unless either party is global.
		if newer.Project != older.Project && newer.Scope != ScopeGlobal && older.Scope != ScopeGlobal {
			continue
		}

		var best *RelationDetectionResult

		// 1. File overlap.
		if r := DetectFileOverlapRelation(newer, older); r != nil && r.Confidence >= minConfidence {
			if best == nil || r.Confidence > best.Confidence {
				best = r
			}
		}

		// 2. Concept overlap.
		if r := DetectConceptOverlapRelation(newer, older); r != nil && r.Confidence >= minConfidence {
			if best == nil || r.Confidence > best.Confidence {
				best = r
			}
		}

		// 3. Type progression.
		if r := DetectTypeProgressionRelation(newer, older); r != nil && r.Confidence >= minConfidence {
			if best == nil || r.Confidence > best.Confidence {
				best = r
			}
		}

		// 4. Temporal proximity — only apply when no stronger detection found.
		if r := DetectTemporalProximityRelation(newer, older); r != nil && r.Confidence >= minConfidence {
			if best == nil {
				best = r
			}
		}

		// 5. Narrative mention — can upgrade the best result if higher confidence.
		if r := DetectNarrativeMentionRelation(newer, older); r != nil && r.Confidence >= minConfidence {
			if best == nil || r.Confidence > best.Confidence {
				best = r
			}
		}

		if best != nil && !seen[older.ID] {
			results = append(results, best)
			seen[older.ID] = true
		}
	}

	return results
}

// truncateList returns items[:maxLen] with a trailing "..." element when truncation occurs.
// Keeps stored reason strings terse without losing all context.
func truncateList(items []string, maxLen int) []string {
	if len(items) <= maxLen {
		return items
	}
	return append(items[:maxLen], "...")
}

// NOTE (provenance-cleanup CR-2a): RelationWithDetails and RelationGraph were
// removed here. They were response DTOs for the graph/related HTTP handlers
// (/api/observations/{id}/graph, /api/relations/*), all deleted in CR-2a. The
// observation_relations table was subsequently dropped by migration 137 in
// CR-2b. The ObservationRelation / ObservationConflict structs that remain in
// this package are now orphaned persistence types with no DB callers — kept
// only to avoid an unrelated API churn; a follow-up may remove them.
