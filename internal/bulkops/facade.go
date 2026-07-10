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
	"gorm.io/gorm/clause"
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
	ids := sortedUniqueIDs(op.CandidateIDs)

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

	if len(ids) == 0 {
		return &ExecuteResult{DryRun: false, AffectedCount: 0, Promoted: []int64{}}, nil
	}
	if f.memoryStore == nil {
		return nil, fmt.Errorf("bulk_promote: memory store not available")
	}
	if f.snapshotStore == nil {
		return nil, fmt.Errorf("bulk_promote: snapshot store not available")
	}

	actor := resolveActor(identity)
	params := op.Parameters
	if len(params) == 0 {
		params, _ = json.Marshal(map[string]any{"candidate_ids": ids})
	}

	var result *ExecuteResult
	promotionAudits := make([]gormdb.AuditLogEntry, 0, len(ids))
	txErr := f.memoryStore.GetDB().WithContext(ctx).Transaction(func(tx *gormpkg.DB) error {
		txSnapshotStore := gormdb.NewSnapshotStore(tx)
		txCandidateStore := gormdb.NewCandidateStore(tx, nil)

		// Lock all existing candidates in one deterministic order before reading
		// any before-state. Under PostgreSQL READ COMMITTED, a row update that
		// commits while this SELECT waits becomes the locked/current row version.
		// The captured candidate, promoted memory input, and rollback snapshot
		// therefore describe the same committed state.
		captures, lockedIDs, capturedAt, captureErr := lockPromoteCandidatesTx(ctx, tx, txCandidateStore, ids)
		if captureErr != nil {
			return fmt.Errorf("capture locked candidates: %w", captureErr)
		}

		txResult := &ExecuteResult{
			DryRun:   false,
			Promoted: []int64{},
		}
		lockedSet := make(map[int64]struct{}, len(lockedIDs))
		for _, id := range lockedIDs {
			lockedSet[id] = struct{}{}
		}
		for _, id := range ids {
			if _, ok := lockedSet[id]; !ok {
				txResult.Errors = append(txResult.Errors, fmt.Sprintf("candidate %d: get: %v", id, gormpkg.ErrRecordNotFound))
			}
		}

		successEntries := make(map[string]models.SnapshotEntry, len(lockedIDs))
		successfulCandidateIDs := make([]int64, 0, len(lockedIDs))
		for _, id := range lockedIDs {
			capture := captures[id]
			candidate := capture.Candidate
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
			promoted, createdMemory, promErr := txCandidateStore.PromoteWithMemory(ctx, id, mem)
			if promErr != nil {
				txResult.Errors = append(txResult.Errors, fmt.Sprintf("candidate %d: %v", id, promErr))
				log.Warn().Err(promErr).Int64("candidate_id", id).Msg("bulk_promote: candidate promotion failed")
				continue
			}
			if promoted == nil {
				return fmt.Errorf("candidate %d promotion returned no post-promotion candidate state", id)
			}
			promotedAfter, marshalErr := json.Marshal(promoted)
			if marshalErr != nil {
				return fmt.Errorf("serialize promoted candidate %d after-state: %w", id, marshalErr)
			}
			txResult.AffectedCount++
			var promotedMemoryID int64
			if promoted.PromotedMemoryID != nil {
				promotedMemoryID = *promoted.PromotedMemoryID
			} else if createdMemory != nil {
				promotedMemoryID = createdMemory.ID
			}
			if promotedMemoryID == 0 {
				return fmt.Errorf("candidate %d promotion returned no memory ID", id)
			}
			txResult.Promoted = append(txResult.Promoted, promotedMemoryID)
			successEntries[fmt.Sprintf("candidate:%d", id)] = models.SnapshotEntry{
				Kind:   models.EntryKindRestore,
				Before: capture.Before,
				After:  promotedAfter,
			}
			successfulCandidateIDs = append(successfulCandidateIDs, id)
			if createdMemory != nil {
				promotionAudits = append(promotionAudits, gormdb.AuditLogEntry{
					Action:          "promote_candidate",
					Actor:           "system",
					SourceSessionID: promoted.SourceSessionID,
					Reason:          fmt.Sprintf("candidate %d promoted to memory %d", id, createdMemory.ID),
				})
			}
		}

		if len(txResult.Promoted) == 0 {
			result = txResult
			return nil
		}

		beforeState, marshalErr := json.Marshal(successEntries)
		if marshalErr != nil {
			return fmt.Errorf("serialize promote before_state: %w", marshalErr)
		}
		snap, snapshotErr := models.NewBulkOpSnapshot(
			uuid.New().String(),
			models.SnapshotOpBulkPromote,
			actor,
			json.RawMessage(beforeState),
		)
		if snapshotErr != nil {
			return fmt.Errorf("new_snapshot: %w", snapshotErr)
		}
		snap.CreatedAt = capturedAt
		snap.AffectedMemoryIDs = successfulCandidateIDs
		snap.SourceSessionID = op.SourceSessionID
		snap.Parameters = params

		createdSnapshot, createErr := txSnapshotStore.Create(ctx, snap)
		if createErr != nil {
			return fmt.Errorf("store_snapshot: %w", createErr)
		}
		txResult.SnapshotID = createdSnapshot.SnapshotID
		if amendErr := txSnapshotStore.AmendPromoteEntries(ctx, createdSnapshot.SnapshotID, txResult.Promoted); amendErr != nil {
			return fmt.Errorf("amend snapshot entries: %w", amendErr)
		}

		result = txResult
		return nil
	})
	if txErr != nil {
		return nil, fmt.Errorf("bulk_promote transaction: %w", txErr)
	}

	// Audit log.
	if f.auditStore != nil {
		for _, entry := range promotionAudits {
			_ = f.auditStore.Log(ctx, entry)
		}
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
	snapshotID, beforeState, capturedAt, err := f.captureMemoryBeforeState(ctx, ids)
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
	snap.CreatedAt = capturedAt
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
	snapshotID, beforeState, capturedAt, err := f.captureMemoryBeforeState(ctx, ids)
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
	snap.CreatedAt = capturedAt
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

type promoteCandidateCapture struct {
	Candidate *models.CrystallizationCandidate
	Before    json.RawMessage
}

// lockPromoteCandidatesTx locks all currently existing requested candidates in
// ascending ID order, then captures their current committed state from the same
// transaction. Missing IDs are intentionally absent from the result: an insert
// that races after this locked capture is not owned by the bulk operation and
// must not be promoted or included in its rollback snapshot.
func lockPromoteCandidatesTx(
	ctx context.Context,
	tx *gormpkg.DB,
	candidateStore *gormdb.CandidateStore,
	candidateIDs []int64,
) (map[int64]promoteCandidateCapture, []int64, time.Time, error) {
	ids := sortedUniqueIDs(candidateIDs)
	type candidateIDRow struct {
		ID int64
	}
	rows := make([]candidateIDRow, 0, len(ids))
	if len(ids) > 0 {
		if err := tx.WithContext(ctx).
			Table("crystallization_candidates").
			Select("id").
			Where("id IN ?", ids).
			Order("id ASC").
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Find(&rows).Error; err != nil {
			return nil, nil, time.Time{}, fmt.Errorf("lock candidates: %w", err)
		}
	}

	var capturedAt time.Time
	if err := tx.WithContext(ctx).
		Raw("SELECT clock_timestamp()").
		Scan(&capturedAt).Error; err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("capture authoritative snapshot time: %w", err)
	}
	capturedAt = capturedAt.UTC()

	captures := make(map[int64]promoteCandidateCapture, len(rows))
	lockedIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		candidate, err := candidateStore.Get(ctx, row.ID)
		if err != nil {
			return nil, nil, time.Time{}, fmt.Errorf("load locked candidate %d: %w", row.ID, err)
		}
		before, err := json.Marshal(candidate)
		if err != nil {
			return nil, nil, time.Time{}, fmt.Errorf("serialize locked candidate %d: %w", row.ID, err)
		}
		captures[row.ID] = promoteCandidateCapture{
			Candidate: candidate,
			Before:    json.RawMessage(before),
		}
		lockedIDs = append(lockedIDs, row.ID)
	}
	return captures, lockedIDs, capturedAt, nil
}

// captureMemoryBeforeState fetches memory rows and serializes them as JSONB.
func (f *Facade) captureMemoryBeforeState(ctx context.Context, ids []int64) (string, json.RawMessage, time.Time, error) {
	snapshotID := uuid.New().String()
	capturedAt, err := f.authoritativeCaptureTime(ctx)
	if err != nil {
		return "", nil, time.Time{}, err
	}
	state := make(map[string]json.RawMessage, len(ids))
	if f.memoryStore != nil {
		for _, id := range ids {
			var row gormdb.Memory
			err := f.memoryStore.GetDB().WithContext(ctx).
				Where("id = ? AND deleted_at IS NULL", id).
				First(&row).Error
			if err != nil {
				if errors.Is(err, gormpkg.ErrRecordNotFound) {
					state[fmt.Sprintf("%d", id)] = json.RawMessage("null")
					continue
				}
				return "", nil, time.Time{}, fmt.Errorf("load memory %d before_state: %w", id, err)
			}
			before, marshalErr := marshalMemoryRowSnapshot(&row)
			if marshalErr != nil {
				return "", nil, time.Time{}, fmt.Errorf("serialize memory %d before_state: %w", id, marshalErr)
			}
			state[fmt.Sprintf("%d", id)] = before
		}
	}
	bs, err := json.Marshal(state)
	if err != nil {
		return "", nil, time.Time{}, fmt.Errorf("serialize memory before_state: %w", err)
	}
	return snapshotID, json.RawMessage(bs), capturedAt, nil
}

// authoritativeCaptureTime returns one database-sourced boundary before any
// before-state row is read. The exact value is carried with that state and
// persisted as bulk_op_snapshots.created_at, avoiding application/DB clock skew
// and the read-to-insert timestamp gap.
func (f *Facade) authoritativeCaptureTime(ctx context.Context) (time.Time, error) {
	if f.memoryStore == nil {
		return time.Now().UTC(), nil
	}
	var capturedAt time.Time
	if err := f.memoryStore.GetDB().WithContext(ctx).
		Raw("SELECT clock_timestamp()").
		Scan(&capturedAt).Error; err != nil {
		return time.Time{}, fmt.Errorf("capture authoritative snapshot time: %w", err)
	}
	return capturedAt.UTC(), nil
}

// marshalMemoryRowSnapshot converts timestamp fields to UTC before JSON encoding.
// PostgreSQL timestamptz values are instants, but a driver may materialize the
// 9999-12-31 sentinel in a positive local offset as local year 10000. The same
// instant is JSON-safe in UTC, and rollback must preserve that instant exactly.
func marshalMemoryRowSnapshot(row *gormdb.Memory) (json.RawMessage, error) {
	if row == nil {
		return json.RawMessage("null"), nil
	}

	snapshot := *row
	snapshot.CreatedAt = snapshot.CreatedAt.UTC()
	snapshot.UpdatedAt = snapshot.UpdatedAt.UTC()
	snapshot.DeletedAt = utcTimePtr(snapshot.DeletedAt)
	snapshot.LastRetrievedAt = utcTimePtr(snapshot.LastRetrievedAt)
	snapshot.LastConfirmed = utcTimePtr(snapshot.LastConfirmed)
	snapshot.ReviewAfter = utcTimePtr(snapshot.ReviewAfter)
	snapshot.ValidFrom = utcTimePtr(snapshot.ValidFrom)
	snapshot.ValidUntil = utcTimePtr(snapshot.ValidUntil)

	encoded, err := json.Marshal(&snapshot)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

func utcTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := value.UTC()
	return &normalized
}

func candidateIDsToInt64(ids []int64) []int64 { return ids }
