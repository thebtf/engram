// Package models contains domain models for engram.
package models

import (
	"regexp"
	"strings"
	"time"
)

// ConflictType classifies the nature of a conflict between two observations.
// The write-lint path (TG5) uses these values to decide correction priority.
type ConflictType string

const (
	// ConflictSuperseded: newer observation replaces older on the same topic.
	ConflictSuperseded ConflictType = "superseded"
	// ConflictContradicts: the two observations carry directly opposing claims.
	ConflictContradicts ConflictType = "contradicts"
	// ConflictOutdatedPattern: an observation codifies a practice that has since been deprecated.
	ConflictOutdatedPattern ConflictType = "outdated_pattern"
)

// ConflictResolution indicates which observation a consumer should prefer.
type ConflictResolution string

const (
	// ResolutionPreferNewer: trust the later observation (the common case for corrections).
	ResolutionPreferNewer ConflictResolution = "prefer_newer"
	// ResolutionPreferOlder: rare — used when a rollback explicitly reinstates an earlier state.
	ResolutionPreferOlder ConflictResolution = "prefer_older"
	// ResolutionManual: neither heuristic is safe; a human must decide.
	ResolutionManual ConflictResolution = "manual"
)

// ObservationConflict is the persisted record of a detected conflict between two observations.
// It is written by the write-lint path (TG5) and read by the retrieval layer to suppress
// superseded observations from injection.
type ObservationConflict struct {
	ResolvedAt      *string            `db:"resolved_at" json:"resolved_at,omitempty"`
	ConflictType    ConflictType       `db:"conflict_type" json:"conflict_type"`
	Resolution      ConflictResolution `db:"resolution" json:"resolution"`
	Reason          string             `db:"reason" json:"reason"`
	DetectedAt      string             `db:"detected_at" json:"detected_at"`
	ID              int64              `db:"id" json:"id"`
	NewerObsID      int64              `db:"newer_obs_id" json:"newer_obs_id"`
	OlderObsID      int64              `db:"older_obs_id" json:"older_obs_id"`
	DetectedAtEpoch int64              `db:"detected_at_epoch" json:"detected_at_epoch"`
	Resolved        bool               `db:"resolved" json:"resolved"`
}

// ConflictDetectionResult is the in-memory result of running conflict detection.
// Callers accumulate these before deciding which records to persist.
type ConflictDetectionResult struct {
	Type        ConflictType
	Resolution  ConflictResolution
	Reason      string
	OlderObsIDs []int64
	HasConflict bool
}

// NewObservationConflict constructs a conflict record ready for persistence.
// The timestamp is captured at construction time so the record is self-describing.
func NewObservationConflict(newerID, olderID int64, conflictType ConflictType, resolution ConflictResolution, reason string) *ObservationConflict {
	now := time.Now()
	return &ObservationConflict{
		NewerObsID:      newerID,
		OlderObsID:      olderID,
		ConflictType:    conflictType,
		Resolution:      resolution,
		Reason:          reason,
		DetectedAt:      now.Format(time.RFC3339),
		DetectedAtEpoch: now.UnixMilli(),
		Resolved:        false,
	}
}

// CorrectionPatterns holds the compiled regexes that signal an explicit correction
// in an observation's text.  TG5 write-lint depends on all 14 patterns being present.
// Pattern VALUES are load-bearing — the write-lint integration test pins the regex strings.
var CorrectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bactually[,\s]+that\s+was\s+wrong\b`),
	regexp.MustCompile(`(?i)\bactually[,\s]+that's\s+(wrong|incorrect|not\s+right)\b`),
	regexp.MustCompile(`(?i)\bpreviously\s+(said|mentioned|noted)\s+.*\s+but\b`),
	regexp.MustCompile(`(?i)\bcorrection:\s*`),
	regexp.MustCompile(`(?i)\bignore\s+(the\s+)?(previous|earlier)\b`),
	regexp.MustCompile(`(?i)\bdisregard\s+(the\s+)?(previous|earlier)\b`),
	regexp.MustCompile(`(?i)\bwas\s+(wrong|incorrect|mistaken)\b`),
	regexp.MustCompile(`(?i)\bturns\s+out\s+.*(wrong|incorrect|not\s+the\s+case)\b`),
	regexp.MustCompile(`(?i)\b(supersedes|replaces|overrides)\s+(the\s+)?(previous|earlier|old)\b`),
	regexp.MustCompile(`(?i)\b(don't|do\s+not)\s+use\s+.*\s+anymore\b`),
	regexp.MustCompile(`(?i)\bno\s+longer\s+(valid|applicable|correct|recommended)\b`),
	regexp.MustCompile(`(?i)\bdeprecated\s+(approach|method|pattern|way)\b`),
	regexp.MustCompile(`(?i)\bshould\s+have\s+(been|used)\b.*instead\b`),
	regexp.MustCompile(`(?i)\bbetter\s+(approach|way|method|solution)\s+is\b`),
}

// OpposingChangePatterns maps an action verb to the verb that contradicts it.
// A newer observation containing the value is opposed by an older containing the key, and vice-versa.
// Used by DetectOpposingFileChanges to identify add/remove style conflicts.
var OpposingChangePatterns = map[string]string{
	"add":     "remove",
	"added":   "removed",
	"create":  "delete",
	"created": "deleted",
	"enable":  "disable",
	"enabled": "disabled",
	"include": "exclude",
	"allow":   "deny",
	"permit":  "block",
}

// DetectExplicitCorrection reports whether text contains language that signals the author
// is overriding a prior statement.  Returns the matched fragment as context for the caller.
func DetectExplicitCorrection(text string) (bool, string) {
	for _, pattern := range CorrectionPatterns {
		if match := pattern.FindString(text); match != "" {
			return true, "Explicit correction detected: " + match
		}
	}
	return false, ""
}

// DetectOpposingFileChanges returns true when newer and older both modified at least one
// common file AND their titles/narratives contain opposing change verbs (add vs. remove, etc.).
// This catches cases where two observations describe contradictory work on the same path.
func DetectOpposingFileChanges(newer, older *Observation) (bool, string) {
	// Index newer's modified files for O(1) lookup.
	newerFiles := make(map[string]bool, len(newer.FilesModified))
	for _, f := range newer.FilesModified {
		newerFiles[f] = true
	}

	// Find files that appear in both observations.
	var overlapping []string
	for _, f := range older.FilesModified {
		if newerFiles[f] {
			overlapping = append(overlapping, f)
		}
	}

	// No shared path — cannot be an opposing-change conflict.
	if len(overlapping) == 0 {
		return false, ""
	}

	newerText := strings.ToLower(newer.Title.String + " " + newer.Narrative.String)
	olderText := strings.ToLower(older.Title.String + " " + older.Narrative.String)

	// Check every action/opposite pair in both directions.
	for action, opposite := range OpposingChangePatterns {
		newerHasAction := strings.Contains(newerText, action)
		olderHasOpposite := strings.Contains(olderText, opposite)
		newerHasOpposite := strings.Contains(newerText, opposite)
		olderHasAction := strings.Contains(olderText, action)

		if (newerHasAction && olderHasOpposite) || (newerHasOpposite && olderHasAction) {
			return true, "Opposing changes on files: " + strings.Join(overlapping, ", ")
		}
	}

	return false, ""
}

// DetectConceptTagMismatch reports a conflict when newer and older share concept tags
// AND have overlapping modified files.  Concept overlap alone is not sufficient — the
// shared file requirement prevents false positives for unrelated observations about the
// same broad topic (e.g., two unrelated auth improvements in different packages).
func DetectConceptTagMismatch(newer, older *Observation) (bool, string) {
	// Index newer's concepts for O(1) lookup.
	newerConcepts := make(map[string]bool, len(newer.Concepts))
	for _, c := range newer.Concepts {
		newerConcepts[c] = true
	}

	// Collect concepts shared by both.
	var overlapping []string
	for _, c := range older.Concepts {
		if newerConcepts[c] {
			overlapping = append(overlapping, c)
		}
	}

	// No shared concepts — definitely no concept-tag conflict.
	if len(overlapping) == 0 {
		return false, ""
	}

	// Require at least one overlapping file to confirm the observations touch the same code.
	newerFiles := make(map[string]bool, len(newer.FilesModified))
	for _, f := range newer.FilesModified {
		newerFiles[f] = true
	}
	for _, f := range older.FilesModified {
		if newerFiles[f] {
			return true, "Same concepts (" + strings.Join(overlapping, ", ") + ") with overlapping file changes"
		}
	}

	return false, ""
}

// DetectConflict runs all conflict detectors against a (newer, older) pair and returns
// the first conflict found.  Detection order is intentional:
//  1. Explicit correction — highest signal, checked first so it wins over weaker signals.
//  2. Opposing file changes — structural contradiction on a shared path.
//  3. Concept-tag mismatch — softer signal, checked last.
//
// The caller is responsible for deciding whether to persist the result.
func DetectConflict(newer, older *Observation) *ConflictDetectionResult {
	result := &ConflictDetectionResult{}

	// 1. Explicit correction in narrative text.
	if newer.Narrative.Valid {
		if isCorrection, reason := DetectExplicitCorrection(newer.Narrative.String); isCorrection {
			result.HasConflict = true
			result.Type = ConflictContradicts
			result.Resolution = ResolutionPreferNewer
			result.Reason = reason
			result.OlderObsIDs = append(result.OlderObsIDs, older.ID)
			return result
		}
	}

	// 2. Explicit correction in title (titles are shorter but equally authoritative).
	if newer.Title.Valid {
		if isCorrection, reason := DetectExplicitCorrection(newer.Title.String); isCorrection {
			result.HasConflict = true
			result.Type = ConflictContradicts
			result.Resolution = ResolutionPreferNewer
			result.Reason = reason
			result.OlderObsIDs = append(result.OlderObsIDs, older.ID)
			return result
		}
	}

	// 3. Opposing change verbs on shared files.
	if isOpposing, reason := DetectOpposingFileChanges(newer, older); isOpposing {
		result.HasConflict = true
		result.Type = ConflictSuperseded
		result.Resolution = ResolutionPreferNewer
		result.Reason = reason
		result.OlderObsIDs = append(result.OlderObsIDs, older.ID)
		return result
	}

	// 4. Same concept tags touching the same files.
	if isMismatch, reason := DetectConceptTagMismatch(newer, older); isMismatch {
		result.HasConflict = true
		result.Type = ConflictSuperseded
		result.Resolution = ResolutionPreferNewer
		result.Reason = reason
		result.OlderObsIDs = append(result.OlderObsIDs, older.ID)
		return result
	}

	return result
}

// DetectConflictsWithExisting checks a newly-arrived observation against a pool of
// existing observations and returns every conflict found.
//
// Filtering rules applied before detection:
//   - Self-comparison is skipped (newer.ID == older.ID).
//   - Project-scoped observations only conflict within the same project.
//     Either party being ScopeGlobal opens cross-project comparison.
func DetectConflictsWithExisting(newer *Observation, existing []*Observation) []*ConflictDetectionResult {
	var results []*ConflictDetectionResult

	for _, older := range existing {
		// Skip self.
		if older.ID == newer.ID {
			continue
		}

		// Skip cross-project pairs where neither party is global.
		if newer.Project != older.Project && newer.Scope != ScopeGlobal && older.Scope != ScopeGlobal {
			continue
		}

		if result := DetectConflict(newer, older); result.HasConflict {
			results = append(results, result)
		}
	}

	return results
}
