// Package bulkops implements admin-only bulk operations on memories and
// crystallization candidates with rollback snapshots.
//
// bulk_promote captures candidate state under the promotion transaction so a
// rollback contains only successfully mutated candidates.
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
// Committed operations create rollback snapshots. bulk_promote captures each
// successful candidate's full before-state under its promotion transaction.
// On partial failure, errors are collected in ExecuteResult.Errors.
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

	if f.candidateStore == nil {
		return nil, fmt.Errorf("bulk_promote: candidate store not available")
	}
	if op.DryRun {
		return f.previewBulkPromote(ctx, ids)
	}
	if f.snapshotStore == nil {
		return nil, fmt.Errorf("bulk_promote: snapshot store not available")
	}
	if len(ids) == 0 {
		return &ExecuteResult{DryRun: false, AffectedCount: 0, Promoted: []int64{}}, nil
	}
	if f.auditStore == nil {
		return nil, fmt.Errorf("bulk_promote: audit store not available")
	}

	actor := resolveActor(identity)
	snapshotID := uuid.NewString()
	beforeState := json.RawMessage(`{}`)
	params := op.Parameters
	if len(params) == 0 {
		params, _ = json.Marshal(map[string]any{"candidate_ids": ids})
	}
	snap, err := models.NewBulkOpSnapshot(snapshotID, models.SnapshotOpBulkPromote, actor, beforeState)
	if err != nil {
		return nil, fmt.Errorf("bulk_promote new_snapshot: %w", err)
	}
	snap.AffectedMemoryIDs = []int64{}
	snap.SourceSessionID = op.SourceSessionID
	snap.Parameters = params

	type promotionAudit struct {
		candidateID int64
		candidate   *models.CrystallizationCandidate
		memory      *models.Memory
	}
	candidateBefore := make(map[int64]json.RawMessage, len(ids))
	var promotionAudits []promotionAudit
	result := &ExecuteResult{SnapshotID: snapshotID, Promoted: []int64{}}
	txErr := f.candidateStore.GetDB().WithContext(ctx).Transaction(func(tx *gormpkg.DB) error {
		created, createErr := f.snapshotStore.CreateTx(ctx, tx, snap)
		if createErr != nil {
			return fmt.Errorf("bulk_promote store_snapshot: %w", createErr)
		}
		result.SnapshotID = created.SnapshotID

		for _, id := range ids {
			candidate, getErr := f.candidateStore.GetForUpdateTx(ctx, tx, id)
			if getErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("candidate %d: get: %v", id, getErr))
				log.Warn().Err(getErr).Int64("candidate_id", id).Msg("bulk_promote: get candidate failed")
				continue
			}
			before, marshalErr := json.Marshal(candidate)
			if marshalErr != nil {
				return fmt.Errorf("bulk_promote capture candidate %d: %w", id, marshalErr)
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
			candidateBefore[id] = before
			promotionAudits = append(promotionAudits, promotionAudit{candidateID: id, candidate: promoted, memory: createdMemory})
		}
		if amendErr := f.snapshotStore.AmendPromoteEntriesWithCandidatesTx(ctx, tx, created.SnapshotID, candidateBefore, result.Promoted); amendErr != nil {
			return fmt.Errorf("bulk_promote amend snapshot entries: %w", amendErr)
		}
		if auditErr := f.auditStore.LogTx(ctx, tx, gormdb.AuditLogEntry{
			Action: "bulk_promote",
			Actor:  actor,
			Reason: fmt.Sprintf("bulk_promote snapshot=%s affected=%d", result.SnapshotID, result.AffectedCount),
		}); auditErr != nil {
			return fmt.Errorf("bulk_promote audit: %w", auditErr)
		}
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	for _, promotion := range promotionAudits {
		f.candidateStore.LogPromoteAudit(promotion.candidateID, promotion.candidate, promotion.memory)
	}

	return result, nil
}

// --- bulk_delete ---

func (f *Facade) executeBulkDelete(ctx context.Context, identity auth.Identity, op BulkOp) (*ExecuteResult, error) {
	return f.executeBulkMemoryMutation(ctx, identity, op, func(ctx context.Context, tx *gormpkg.DB, id int64) error {
		return f.memoryStore.DeleteTx(ctx, tx, id)
	})
}

// --- bulk_supersede ---

func (f *Facade) executeBulkSupersede(ctx context.Context, identity auth.Identity, op BulkOp) (*ExecuteResult, error) {
	return f.executeBulkMemoryMutation(ctx, identity, op, func(ctx context.Context, tx *gormpkg.DB, id int64) error {
		_, err := f.memoryStore.SupersedeTx(ctx, tx, id)
		return err
	})
}

func (f *Facade) executeBulkMemoryMutation(ctx context.Context, identity auth.Identity, op BulkOp, mutate func(context.Context, *gormpkg.DB, int64) error) (*ExecuteResult, error) {
	if f.memoryStore == nil {
		return nil, fmt.Errorf("%s: memory store not available", op.Type)
	}
	if op.DryRun {
		return f.previewBulkMemoryMutation(ctx, op)
	}
	if f.snapshotStore == nil {
		return nil, fmt.Errorf("%s: snapshot store not available", op.Type)
	}
	if len(op.MemoryIDs) == 0 {
		return &ExecuteResult{DryRun: false, AffectedCount: 0}, nil
	}
	if f.auditStore == nil {
		return nil, fmt.Errorf("%s: audit store not available", op.Type)
	}

	result := &ExecuteResult{}
	actor := resolveActor(identity)
	params := op.Parameters
	if len(params) == 0 {
		params, _ = json.Marshal(map[string]any{"memory_ids": op.MemoryIDs})
	}

	txErr := f.memoryStore.GetDB().WithContext(ctx).Transaction(func(tx *gormpkg.DB) error {
		entries, capturedIDs, missingIDs, err := f.captureMemoryBeforeState(ctx, tx, op.MemoryIDs)
		if err != nil {
			return fmt.Errorf("%s snapshot capture: %w", op.Type, err)
		}
		for _, id := range missingIDs {
			result.Errors = append(result.Errors, fmt.Sprintf("memory %d: %v", id, gormpkg.ErrRecordNotFound))
		}
		successfulIDs := make([]int64, 0, len(capturedIDs))
		for _, id := range capturedIDs {
			if err := mutate(ctx, tx, id); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("memory %d: %v", id, err))
				continue
			}
			successfulIDs = append(successfulIDs, id)
		}
		if len(successfulIDs) == 0 {
			return nil
		}

		beforeState := make(map[string]models.SnapshotEntry, len(successfulIDs))
		for _, id := range successfulIDs {
			beforeState[fmt.Sprintf("%d", id)] = entries[id]
		}
		encodedBeforeState, err := json.Marshal(beforeState)
		if err != nil {
			return fmt.Errorf("%s serialize before_state: %w", op.Type, err)
		}
		snap, err := models.NewBulkOpSnapshot(uuid.NewString(), op.Type, actor, encodedBeforeState)
		if err != nil {
			return fmt.Errorf("%s new_snapshot: %w", op.Type, err)
		}
		snap.AffectedMemoryIDs = successfulIDs
		snap.SourceSessionID = op.SourceSessionID
		snap.Parameters = params
		created, err := f.snapshotStore.CreateTx(ctx, tx, snap)
		if err != nil {
			return fmt.Errorf("%s store_snapshot: %w", op.Type, err)
		}
		if err := f.auditStore.LogTx(ctx, tx, gormdb.AuditLogEntry{
			Action: string(op.Type),
			Actor:  actor,
			Reason: fmt.Sprintf("%s snapshot=%s affected=%d", op.Type, created.SnapshotID, len(successfulIDs)),
		}); err != nil {
			return fmt.Errorf("%s audit: %w", op.Type, err)
		}
		result.SnapshotID = created.SnapshotID
		result.AffectedCount = len(successfulIDs)
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	return result, nil
}

func (f *Facade) previewBulkPromote(ctx context.Context, ids []int64) (*ExecuteResult, error) {
	result := &ExecuteResult{DryRun: true, Promoted: []int64{}}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		candidate, err := f.candidateStore.Get(ctx, id)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("candidate %d: get: %v", id, err))
			continue
		}
		if candidate.Status != models.CandidateStatusPending {
			result.Errors = append(result.Errors, fmt.Sprintf("candidate %d: %v: %s → promoted", id, gormdb.ErrInvalidTransition, candidate.Status))
			continue
		}
		result.WouldAffect++
	}
	return result, nil
}

func (f *Facade) previewBulkMemoryMutation(ctx context.Context, op BulkOp) (*ExecuteResult, error) {
	result := &ExecuteResult{DryRun: true}
	seen := make(map[int64]struct{}, len(op.MemoryIDs))
	for _, id := range op.MemoryIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		mem, err := f.memoryStore.GetForSnapshot(ctx, id)
		if err != nil {
			if errors.Is(err, gormpkg.ErrRecordNotFound) {
				result.Errors = append(result.Errors, fmt.Sprintf("memory %d: %v", id, gormpkg.ErrRecordNotFound))
				continue
			}
			return nil, fmt.Errorf("%s dry-run read memory %d: %w", op.Type, id, err)
		}
		if op.Type == models.SnapshotOpBulkSupersede && mem.Status != "active" {
			result.Errors = append(result.Errors, fmt.Sprintf("memory %d: not found or already superseded", id))
			continue
		}
		result.WouldAffect++
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

// captureMemoryBeforeState fetches active memory rows for a bulk mutation.
// Missing IDs are omitted; all other read failures abort before mutation.
func (f *Facade) captureMemoryBeforeState(ctx context.Context, tx *gormpkg.DB, ids []int64) (map[int64]models.SnapshotEntry, []int64, []int64, error) {
	state := make(map[int64]models.SnapshotEntry, len(ids))
	capturedIDs := make([]int64, 0, len(ids))
	missingIDs := make([]int64, 0)
	for _, id := range ids {
		mem, err := f.memoryStore.GetForSnapshotTx(ctx, tx, id)
		if err != nil {
			if errors.Is(err, gormpkg.ErrRecordNotFound) {
				missingIDs = append(missingIDs, id)
				continue
			}
			return nil, nil, nil, fmt.Errorf("read memory %d before_state: %w", id, err)
		}
		before, err := json.Marshal(mem)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("serialize memory %d before_state: %w", id, err)
		}
		expectedVersion := mem.Version + 1
		state[id] = models.SnapshotEntry{
			Kind:            models.EntryKindRestore,
			Before:          before,
			ExpectedVersion: &expectedVersion,
		}
		capturedIDs = append(capturedIDs, id)
	}
	return state, capturedIDs, missingIDs, nil
}

func candidateIDsToInt64(ids []int64) []int64 {
	return ids
}

// captureCandidateBeforeStateAt is a timestamp-capturing variant used by rollback.
// Returns the snapshot time so rollback can compare updated_at values.
func SnapshotTime() time.Time {
	return time.Now().UTC()
}
