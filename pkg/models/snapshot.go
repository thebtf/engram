// Package models contains domain models for engram.
package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// SnapshotOpType is the type of bulk/review operation that produced a snapshot.
// Valid values are enforced by the migrations 133/153/154 CHECK constraint chain.
type SnapshotOpType string

const (
	// SnapshotOpIngestDoc represents a document ingest operation.
	SnapshotOpIngestDoc SnapshotOpType = "ingest_doc"
	// SnapshotOpBulkPromote represents a bulk candidate promotion.
	SnapshotOpBulkPromote SnapshotOpType = "bulk_promote"
	// SnapshotOpBulkDelete represents a bulk memory deletion.
	SnapshotOpBulkDelete SnapshotOpType = "bulk_delete"
	// SnapshotOpBulkSupersede represents a bulk memory supersession.
	SnapshotOpBulkSupersede SnapshotOpType = "bulk_supersede"
	// SnapshotOpCandidateReviewAction represents a single candidate review action pre-mutation snapshot.
	SnapshotOpCandidateReviewAction SnapshotOpType = "candidate_review_action"
	// SnapshotOpForgettingReviewAction represents a forgetting/consolidation review action pre-mutation snapshot.
	SnapshotOpForgettingReviewAction SnapshotOpType = "forgetting_review_action"
)

// IsValid returns true iff s is one of the 6 legal SnapshotOpType values.
func (s SnapshotOpType) IsValid() bool {
	switch s {
	case SnapshotOpIngestDoc, SnapshotOpBulkPromote,
		SnapshotOpBulkDelete, SnapshotOpBulkSupersede,
		SnapshotOpCandidateReviewAction, SnapshotOpForgettingReviewAction:
		return true
	}
	return false
}

// SnapshotStatus is the lifecycle state of a bulk_op_snapshot row.
type SnapshotStatus string

const (
	// SnapshotStatusPreview is the status for dry-run previews (no DB row created for dry-run).
	SnapshotStatusPreview SnapshotStatus = "preview"
	// SnapshotStatusCommitted is the default status after a bulk op completes.
	SnapshotStatusCommitted SnapshotStatus = "committed"
	// SnapshotStatusRolledBack is set after a successful rollback.
	SnapshotStatusRolledBack SnapshotStatus = "rolled_back"
)

// IsValid returns true iff s is one of the 3 legal SnapshotStatus values.
func (s SnapshotStatus) IsValid() bool {
	switch s {
	case SnapshotStatusPreview, SnapshotStatusCommitted, SnapshotStatusRolledBack:
		return true
	}
	return false
}

// BulkOpSnapshot is the domain model for the bulk_op_snapshots table.
// It captures the before-state of affected rows for rollback support.
// Snapshots are created immediately before a destructive bulk operation
// so that rollback can restore the exact pre-op state per ADR-F-003.
type BulkOpSnapshot struct {
	// CreatedAt is the wall-clock time when the snapshot was captured.
	CreatedAt time.Time `json:"created_at"`
	// RolledBackAt is set when the snapshot is rolled back.
	RolledBackAt *time.Time `json:"rolled_back_at,omitempty"`
	// SnapshotID is the ULID/UUID unique identifier for this snapshot.
	SnapshotID string `json:"snapshot_id"`
	// OpType is the type of bulk operation.
	OpType SnapshotOpType `json:"op_type"`
	// Actor is the auth.Identity.KeycardID or "master" identifying who ran the op.
	Actor string `json:"actor"`
	// SourceSessionID is the session that initiated the bulk op (may be empty).
	SourceSessionID string `json:"source_session_id,omitempty"`
	// Parameters is the raw JSON of the operation input parameters.
	Parameters json.RawMessage `json:"parameters,omitempty"`
	// AffectedMemoryIDs is the list of memory IDs affected by the op.
	AffectedMemoryIDs []int64 `json:"affected_memory_ids"`
	// BeforeState is the full JSONB snapshot of affected rows before the op.
	// Structure is operation-specific: legacy memory snapshots use numeric row-ID keys;
	// candidate review snapshots may use entity-prefixed keys like "candidate:<id>" and "memory:<id>".
	BeforeState json.RawMessage `json:"before_state"`
	// Status tracks the snapshot lifecycle.
	Status SnapshotStatus `json:"status"`
	// Pinned exempts this snapshot from auto-pruning (T049).
	Pinned bool `json:"pinned"`
	// ID is the database primary key.
	ID int64 `json:"id"`
}

// SnapshotEntryKind distinguishes how a rollback should handle each row captured in
// before_state.
//
//   - EntryKindRestore: the row existed before the op; rollback must restore it from BeforeData.
//   - EntryKindDelete: the row was CREATED by the op (e.g., memory promoted from a candidate);
//     rollback must hard-delete it.
//
// The kind is stored inline inside before_state JSONB so no schema migration is needed:
//
//	"<id>":           {"kind": "restore", "before": <row JSON>}
//	"memory:<id>":    {"kind": "delete"}
//	"candidate:<id>": {"kind": "restore", "before": <candidate JSON>}
type SnapshotEntryKind string

const (
	EntryKindRestore SnapshotEntryKind = "restore"
	EntryKindDelete  SnapshotEntryKind = "delete"
)

// SnapshotEntry is the typed unit stored inside before_state for each affected row.
// Stored as JSONB — no new column required.
type SnapshotEntry struct {
	Kind            SnapshotEntryKind `json:"kind"`
	Before          json.RawMessage   `json:"before,omitempty"`           // populated only for EntryKindRestore
	PostStateToken  string            `json:"post_state_token,omitempty"` // immutable hash of the exact post-mutation row state
	ExpectedVersion *int              `json:"expected_version,omitempty"` // compatibility proof for pre-token typed snapshots
}

// SnapshotStateToken returns the stable SHA-256 token for a persisted row state.
// PostgreSQL stores timestamps at microsecond precision and may read them back in
// a different zone, so supported row models are canonicalized before hashing.
func SnapshotStateToken(row any) (string, error) {
	encoded, err := json.Marshal(canonicalSnapshotState(row))
	if err != nil {
		return "", fmt.Errorf("snapshot state token: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalSnapshotState(row any) any {
	switch value := row.(type) {
	case *Memory:
		if value == nil {
			return value
		}
		clone := *value
		canonicalizeMemorySnapshotTimes(&clone)
		return &clone
	case Memory:
		clone := value
		canonicalizeMemorySnapshotTimes(&clone)
		return clone
	case *CrystallizationCandidate:
		if value == nil {
			return value
		}
		clone := *value
		canonicalizeCandidateSnapshotTimes(&clone)
		return &clone
	case CrystallizationCandidate:
		clone := value
		canonicalizeCandidateSnapshotTimes(&clone)
		return clone
	default:
		return row
	}
}

func canonicalizeMemorySnapshotTimes(memory *Memory) {
	memory.CreatedAt = canonicalSnapshotTime(memory.CreatedAt)
	memory.UpdatedAt = canonicalSnapshotTime(memory.UpdatedAt)
	memory.DeletedAt = canonicalSnapshotTimePtr(memory.DeletedAt)
	memory.LastRetrievedAt = canonicalSnapshotTimePtr(memory.LastRetrievedAt)
	memory.LastConfirmed = canonicalSnapshotTimePtr(memory.LastConfirmed)
	memory.ReviewAfter = canonicalSnapshotTimePtr(memory.ReviewAfter)
	memory.ValidFrom = canonicalSnapshotTimePtr(memory.ValidFrom)
	memory.ValidUntil = canonicalSnapshotTimePtr(memory.ValidUntil)
}

func canonicalizeCandidateSnapshotTimes(candidate *CrystallizationCandidate) {
	candidate.CreatedAt = canonicalSnapshotTime(candidate.CreatedAt)
	candidate.UpdatedAt = canonicalSnapshotTime(candidate.UpdatedAt)
	candidate.ReviewAfter = canonicalSnapshotTimePtr(candidate.ReviewAfter)
}

func canonicalSnapshotTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func canonicalSnapshotTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	canonical := canonicalSnapshotTime(*value)
	return &canonical
}

// NewBulkOpSnapshot constructs a BulkOpSnapshot with validation.
// Returns an error if required fields are absent or if OpType/Status are invalid.
// SnapshotID must be provided by the caller (ULID or UUID).
// Anti-stub: returning an empty snapshot without validation will fail the
// NewBulkOpSnapshot_Validation test in snapshot_test.go.
func NewBulkOpSnapshot(snapshotID string, opType SnapshotOpType, actor string, beforeState json.RawMessage) (*BulkOpSnapshot, error) {
	if snapshotID == "" {
		return nil, fmt.Errorf("new_bulk_op_snapshot: snapshot_id is required")
	}
	if !opType.IsValid() {
		return nil, fmt.Errorf("new_bulk_op_snapshot: invalid op_type %q", opType)
	}
	if actor == "" {
		return nil, fmt.Errorf("new_bulk_op_snapshot: actor is required")
	}
	if len(beforeState) == 0 {
		// allow empty JSON object but require non-nil
		beforeState = json.RawMessage(`{}`)
	}
	return &BulkOpSnapshot{
		SnapshotID:        snapshotID,
		OpType:            opType,
		Actor:             actor,
		BeforeState:       beforeState,
		Status:            SnapshotStatusCommitted,
		AffectedMemoryIDs: []int64{},
		CreatedAt:         time.Now().UTC(),
	}, nil
}
