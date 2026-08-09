// Package bulkops implements admin-only bulk operations on memories and
// crystallization candidates with pre-op snapshot capture for rollback (FR-F6.a).
//
// All exported operations require auth.Identity.Role == auth.RoleAdmin.
// Non-admin callers receive ErrAdminRequired.
//
// Dry-run mode (BulkOp.DryRun = true) returns a preview without any DB writes.
// No snapshot row is created for dry-run runs — the caller sees would_affect counts.
package bulkops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	gormpkg "gorm.io/gorm"
)

// ErrAdminRequired is returned when a non-admin caller attempts a bulk operation.
// HTTP handlers translate this to 403 with error_code='admin_required'.
var ErrAdminRequired = errors.New("admin_required")

// BulkOpType mirrors the models.SnapshotOpType enum.
type BulkOpType = models.SnapshotOpType

// BulkOp is the input to Facade.Execute.
type BulkOp struct {
	// Type identifies the operation.
	Type BulkOpType
	// CandidateIDs is the list of candidate IDs to act on (for promote/delete/supersede).
	CandidateIDs []int64
	// MemoryIDs is the list of memory IDs to act on (for delete/supersede of memories directly).
	MemoryIDs []int64
	// DryRun when true causes Execute to return a preview without writing to the DB.
	DryRun bool
	// Actor is the auth.Identity actor string for audit and snapshot attribution.
	Actor string
	// SourceSessionID is the optional session ID for provenance.
	SourceSessionID string
	// Parameters is free-form JSON of op input (stored in snapshot for debugging).
	Parameters json.RawMessage
}

// ExecuteResult is the outcome of Facade.Execute.
type ExecuteResult struct {
	// SnapshotID is non-empty after a committed (non-dry-run) op.
	SnapshotID string `json:"snapshot_id,omitempty"`
	// DryRun is true when no writes occurred.
	DryRun bool `json:"dry_run"`
	// AffectedCount is the number of rows that were (or would be) affected.
	AffectedCount int `json:"affected_count"`
	// WouldAffect is the preview count for dry-run mode.
	WouldAffect int `json:"would_affect,omitempty"`
	// Promoted is the list of new memory IDs created (for bulk_promote).
	Promoted []int64 `json:"promoted,omitempty"`
	// Errors contains any per-row errors that occurred during execution.
	Errors []string `json:"errors,omitempty"`
}

// Facade provides admin-only bulk operations with snapshot capture.
type Facade struct {
	snapshotStore  *gormdb.SnapshotStore
	candidateStore *gormdb.CandidateStore
	memoryStore    *gormdb.MemoryStore
	auditStore     *gormdb.AuditStore
}

// NewFacade creates a new Facade with required dependencies.
// snapshotStore and auditStore are required; candidateStore/memoryStore may be nil
// when the corresponding op types are not used.
func NewFacade(
	snapshotStore *gormdb.SnapshotStore,
	candidateStore *gormdb.CandidateStore,
	memoryStore *gormdb.MemoryStore,
	auditStore *gormdb.AuditStore,
) *Facade {
	return &Facade{
		snapshotStore:  snapshotStore,
		candidateStore: candidateStore,
		memoryStore:    memoryStore,
		auditStore:     auditStore,
	}
}

// Execute runs a bulk operation.
//
// Admin gate: identity.Role must be auth.RoleAdmin — returns ErrAdminRequired otherwise.
// Note: the "operator" SSO role at middleware.go:416 is NOT admin (it is the non-admin
// autoprovisioned role). Only auth.RoleAdmin ("admin") grants bulk-op access.
//
// Dry-run: when op.DryRun=true, Execute computes the would-affect count and returns
// immediately with DryRun=true and no DB mutations. No snapshot row is created.
//
// Committed: before executing the op, a snapshot is captured with the full before-state
// of affected rows (JSONB per ADR-F-003). The snapshot is marked 'committed' after
// the op completes. On partial failure, errors are collected in ExecuteResult.Errors.
func (f *Facade) Execute(ctx context.Context, identity auth.Identity, op BulkOp) (*ExecuteResult, error) {
	// Admin gate — spec §FR-F6 CHK010-ADDED.
	if identity.Role != auth.RoleAdmin {
		return nil, ErrAdminRequired
	}

	if !op.Type.IsValid() {
		return nil, fmt.Errorf("bulk_execute: invalid op_type %q", op.Type)
	}

	switch op.Type {
	case models.SnapshotOpBulkPromote:
		return f.executeBulkPromote(ctx, identity, op)
	case models.SnapshotOpBulkDelete:
		return f.executeBulkDelete(ctx, identity, op)
	case models.SnapshotOpBulkSupersede:
		return f.executeBulkSupersede(ctx, identity, op)
	case models.SnapshotOpIngestDoc:
		return f.executeIngestDoc(ctx, identity, op)
	default:
		return nil, fmt.Errorf("bulk_execute: unhandled op_type %q", op.Type)
	}
}

// --- bulk_promote ---

func (f *Facade) executeBulkPromote(ctx context.Context, identity auth.Identity, op BulkOp) (*ExecuteResult, error) {
	ids := op.CandidateIDs

	if op.DryRun {
		return &ExecuteResult{
			DryRun:      true,
			WouldAffect: len(ids),
			Promoted:    []int64{},
		}, nil
	}
	if f.candidateStore == nil {
		return nil, fmt.Errorf("bulk_promote: candidate store not available")
	}
	if f.snapshotStore == nil {
		return nil, fmt.Errorf("bulk_promote: snapshot store not available")
	}
	if len(ids) == 0 {
		return &ExecuteResult{DryRun: false, AffectedCount: 0, Promoted: []int64{}}, nil
	}

	actor := resolveActor(identity)
	snapshotID, beforeState, err := f.capturePromoteBeforeState(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("bulk_promote snapshot capture: %w", err)
	}
	params := op.Parameters
	if len(params) == 0 {
		params, _ = json.Marshal(map[string]any{"candidate_ids": ids})
	}
	snap, err := models.NewBulkOpSnapshot(snapshotID, models.SnapshotOpBulkPromote, actor, beforeState)
	if err != nil {
		return nil, fmt.Errorf("bulk_promote new_snapshot: %w", err)
	}
	snap.AffectedMemoryIDs = ids
	snap.SourceSessionID = op.SourceSessionID
	snap.Parameters = params

	result := &ExecuteResult{SnapshotID: snapshotID, Promoted: []int64{}}
	txErr := f.candidateStore.GetDB().WithContext(ctx).Transaction(func(tx *gormpkg.DB) error {
		created, createErr := f.snapshotStore.CreateTx(ctx, tx, snap)
		if createErr != nil {
			return fmt.Errorf("bulk_promote store_snapshot: %w", createErr)
		}
		result.SnapshotID = created.SnapshotID

		for _, id := range ids {
			candidate, getErr := f.candidateStore.Get(ctx, id)
			if getErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("candidate %d: get: %v", id, getErr))
				log.Warn().Err(getErr).Int64("candidate_id", id).Msg("bulk_promote: get candidate failed")
				continue
			}
			project := ""
			if len(candidate.AffectedProjects) > 0 {
				project = candidate.AffectedProjects[0]
			}
			mem := &models.Memory{
				Content:       candidate.ProposedContent,
				Project:       project,
				Tier:          candidate.ProposedTier,
				EpistemicType: "decision",
				Tags:          []string{fmt.Sprintf("candidate:%d", id), "crystallized"},
				SourceAgent:   "crystallization",
			}
			promoted, createdMemory, promoteErr := f.candidateStore.PromoteWithMemoryTx(ctx, tx, id, mem)
			if promoteErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("candidate %d: %v", id, promoteErr))
				log.Warn().Err(promoteErr).Int64("candidate_id", id).Msg("bulk_promote: candidate promotion failed")
				continue
			}
			result.AffectedCount++
			if promoted != nil && promoted.PromotedMemoryID != nil {
				result.Promoted = append(result.Promoted, *promoted.PromotedMemoryID)
			} else if createdMemory != nil {
				result.Promoted = append(result.Promoted, createdMemory.ID)
			}
		}
		if amendErr := f.snapshotStore.AmendPromoteEntriesTx(ctx, tx, created.SnapshotID, result.Promoted); amendErr != nil {
			return fmt.Errorf("bulk_promote amend snapshot entries: %w", amendErr)
		}
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}

	if f.auditStore != nil {
		_ = f.auditStore.Log(ctx, gormdb.AuditLogEntry{
			Action: "bulk_promote",
			Actor:  actor,
			Reason: fmt.Sprintf("bulk_promote snapshot=%s affected=%d", result.SnapshotID, result.AffectedCount),
		})
	}
	return result, nil
}

// --- bulk_delete ---

func (f *Facade) executeBulkDelete(ctx context.Context, identity auth.Identity, op BulkOp) (*ExecuteResult, error) {
	ids := op.MemoryIDs

	if op.DryRun {
		return &ExecuteResult{DryRun: true, WouldAffect: len(ids)}, nil
	}

	if f.memoryStore == nil {
		return nil, fmt.Errorf("bulk_delete: memory store not available")
	}

	if len(ids) == 0 {
		return &ExecuteResult{DryRun: false, AffectedCount: 0}, nil
	}

	actor := resolveActor(identity)
	snapshotID, beforeState, err := f.captureMemoryBeforeState(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("bulk_delete snapshot capture: %w", err)
	}

	params := op.Parameters
	if len(params) == 0 {
		params, _ = json.Marshal(map[string]any{"memory_ids": ids})
	}
	snap, err := models.NewBulkOpSnapshot(snapshotID, models.SnapshotOpBulkDelete, actor, beforeState)
	if err != nil {
		return nil, fmt.Errorf("bulk_delete new_snapshot: %w", err)
	}
	snap.AffectedMemoryIDs = ids
	snap.SourceSessionID = op.SourceSessionID
	snap.Parameters = params

	created, err := f.snapshotStore.Create(ctx, snap)
	if err != nil {
		return nil, fmt.Errorf("bulk_delete store_snapshot: %w", err)
	}

	result := &ExecuteResult{SnapshotID: created.SnapshotID}
	for _, id := range ids {
		if err := f.memoryStore.Delete(ctx, id); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("memory %d: %v", id, err))
			continue
		}
		result.AffectedCount++
	}

	if f.auditStore != nil {
		_ = f.auditStore.Log(ctx, gormdb.AuditLogEntry{
			Action: "bulk_delete",
			Actor:  actor,
			Reason: fmt.Sprintf("bulk_delete snapshot=%s affected=%d", created.SnapshotID, result.AffectedCount),
		})
	}

	return result, nil
}

// --- bulk_supersede ---

func (f *Facade) executeBulkSupersede(ctx context.Context, identity auth.Identity, op BulkOp) (*ExecuteResult, error) {
	ids := op.MemoryIDs

	if op.DryRun {
		return &ExecuteResult{DryRun: true, WouldAffect: len(ids)}, nil
	}

	if f.memoryStore == nil {
		return nil, fmt.Errorf("bulk_supersede: memory store not available")
	}

	if len(ids) == 0 {
		return &ExecuteResult{DryRun: false, AffectedCount: 0}, nil
	}

	actor := resolveActor(identity)
	snapshotID, beforeState, err := f.captureMemoryBeforeState(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("bulk_supersede snapshot capture: %w", err)
	}

	params := op.Parameters
	if len(params) == 0 {
		params, _ = json.Marshal(map[string]any{"memory_ids": ids})
	}
	snap, err := models.NewBulkOpSnapshot(snapshotID, models.SnapshotOpBulkSupersede, actor, beforeState)
	if err != nil {
		return nil, fmt.Errorf("bulk_supersede new_snapshot: %w", err)
	}
	snap.AffectedMemoryIDs = ids
	snap.SourceSessionID = op.SourceSessionID
	snap.Parameters = params

	created, err := f.snapshotStore.Create(ctx, snap)
	if err != nil {
		return nil, fmt.Errorf("bulk_supersede store_snapshot: %w", err)
	}

	result := &ExecuteResult{SnapshotID: created.SnapshotID}
	for _, id := range ids {
		if _, err := f.memoryStore.Supersede(ctx, id); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("memory %d: %v", id, err))
			continue
		}
		result.AffectedCount++
	}

	if f.auditStore != nil {
		_ = f.auditStore.Log(ctx, gormdb.AuditLogEntry{
			Action: "bulk_supersede",
			Actor:  actor,
			Reason: fmt.Sprintf("bulk_supersede snapshot=%s affected=%d", created.SnapshotID, result.AffectedCount),
		})
	}

	return result, nil
}

// --- ingest_doc ---

func (f *Facade) executeIngestDoc(ctx context.Context, identity auth.Identity, op BulkOp) (*ExecuteResult, error) {
	// ingest_doc snapshot is created after ingestion; dry-run is handled upstream.
	// This facade entry point is a thin wrapper for snapshot attribution on non-dry-run ingest.
	if op.DryRun {
		return &ExecuteResult{DryRun: true, WouldAffect: 0}, nil
	}
	actor := resolveActor(identity)
	snapshotID := uuid.New().String()
	snap, err := models.NewBulkOpSnapshot(snapshotID, models.SnapshotOpIngestDoc, actor, json.RawMessage(`{}`))
	if err != nil {
		return nil, fmt.Errorf("ingest_doc new_snapshot: %w", err)
	}
	snap.SourceSessionID = op.SourceSessionID
	snap.Parameters = op.Parameters
	created, err := f.snapshotStore.Create(ctx, snap)
	if err != nil {
		return nil, fmt.Errorf("ingest_doc store_snapshot: %w", err)
	}
	return &ExecuteResult{SnapshotID: created.SnapshotID, AffectedCount: 0}, nil
}

// --- helpers ---

func resolveActor(identity auth.Identity) string {
	if identity.KeycardID != "" {
		return identity.KeycardID
	}
	if identity.Source == auth.SourceMaster {
		return "master"
	}
	return string(identity.Source)
}

// captureCandidateBeforeState fetches candidate rows and serializes them as JSONB.
// The before_state JSONB is keyed by candidate ID (string for JSON compat).
// Deprecated: use capturePromoteBeforeState for bulk_promote (typed entries).
func (f *Facade) captureCandidateBeforeState(ctx context.Context, ids []int64) (string, json.RawMessage, error) {
	snapshotID := uuid.New().String()
	state := make(map[string]any, len(ids))
	for _, id := range ids {
		c, err := f.candidateStore.Get(ctx, id)
		if err != nil {
			// Treat missing as empty entry — snapshot still proceeds.
			state[fmt.Sprintf("%d", id)] = nil
			continue
		}
		state[fmt.Sprintf("%d", id)] = c
	}
	bs, err := json.Marshal(state)
	if err != nil {
		return "", nil, fmt.Errorf("serialize before_state: %w", err)
	}
	return snapshotID, json.RawMessage(bs), nil
}

// capturePromoteBeforeState captures candidate before-state as typed SnapshotEntry rows.
// Each candidate ID is stored as EntryKindRestore with the candidate body as Before data.
// Promoted memory IDs (created by the op) are added later via AmendPromoteEntries as
// EntryKindDelete (no before needed — they did not exist pre-op).
func (f *Facade) capturePromoteBeforeState(ctx context.Context, candidateIDs []int64) (string, json.RawMessage, error) {
	snapshotID := uuid.New().String()
	state := make(map[string]models.SnapshotEntry, len(candidateIDs))
	for _, id := range candidateIDs {
		c, err := f.candidateStore.Get(ctx, id)
		if err != nil {
			// Missing candidate: still record a restore entry with empty Before.
			state[fmt.Sprintf("%d", id)] = models.SnapshotEntry{Kind: models.EntryKindRestore}
			continue
		}
		before, marshalErr := json.Marshal(c)
		if marshalErr != nil {
			return "", nil, fmt.Errorf("capturePromoteBeforeState: marshal candidate %d: %w", id, marshalErr)
		}
		state[fmt.Sprintf("%d", id)] = models.SnapshotEntry{Kind: models.EntryKindRestore, Before: json.RawMessage(before)}
	}
	bs, err := json.Marshal(state)
	if err != nil {
		return "", nil, fmt.Errorf("capturePromoteBeforeState: serialize: %w", err)
	}
	return snapshotID, json.RawMessage(bs), nil
}

// captureMemoryBeforeState fetches memory rows and serializes them as JSONB.
func (f *Facade) captureMemoryBeforeState(ctx context.Context, ids []int64) (string, json.RawMessage, error) {
	snapshotID := uuid.New().String()
	state := make(map[string]any, len(ids))
	if f.memoryStore != nil {
		for _, id := range ids {
			mem, err := f.memoryStore.Get(ctx, id)
			if err != nil {
				state[fmt.Sprintf("%d", id)] = nil
				continue
			}
			state[fmt.Sprintf("%d", id)] = mem
		}
	}
	bs, err := json.Marshal(state)
	if err != nil {
		return "", nil, fmt.Errorf("serialize memory before_state: %w", err)
	}
	return snapshotID, json.RawMessage(bs), nil
}

func candidateIDsToInt64(ids []int64) []int64 {
	return ids
}

// captureCandidateBeforeStateAt is a timestamp-capturing variant used by rollback.
// Returns the snapshot time so rollback can compare updated_at values.
func SnapshotTime() time.Time {
	return time.Now().UTC()
}
