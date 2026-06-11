package gorm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// PurgeReceipt records what was deleted during a project purge.
// JSON-serializable so it can be stored in audit_log.after_state.
type PurgeReceipt struct {
	Project     string    `json:"project"`
	PurgedAt    time.Time `json:"purged_at"`
	MemoryCount int64     `json:"memory_count"`
	RuleCount   int64     `json:"rule_count"`
	EdgeCount   int64     `json:"edge_count"`
	AuditCount  int64     `json:"audit_count"`
}

// PurgeStore handles project-scoped hard deletion.
type PurgeStore struct {
	db *gorm.DB
}

// NewPurgeStore creates a PurgeStore backed by the given Store.
func NewPurgeStore(store *Store) *PurgeStore {
	return &PurgeStore{db: store.DB}
}

// PurgeProject permanently deletes all memories, behavioral rules, and
// associated edges/audit rows for the given project in a single transaction.
//
// Deletion order (FK-safe):
//  1. Count rows per table for the receipt snapshot.
//  2. knowledge_edges where source_id or target_id belong to the project's
//     memory IDs — migration 121 added ON DELETE CASCADE, but we count first
//     so the receipt is accurate, then hard-delete so the count is exact
//     regardless of partial cascade state.
//  3. audit_log rows whose memory_id references the project's memories (they
//     reference purged IDs and would become orphan noise). A single new
//     audit row with action="purge" is written AFTER deletion to preserve
//     the audit trail for the purge event itself.
//  4. memories WHERE project = ? — credentials live in the separate
//     `credentials` table and are explicitly excluded (vault concern).
//  5. behavioral_rules WHERE project = ? (project column is *string / NULLable;
//     NULL = global rule — those are never touched).
//
// Returns a PurgeReceipt with row counts snapshotted before deletion.
// All operations are wrapped in a single GORM transaction; any failure
// rolls back the entire purge.
func (s *PurgeStore) PurgeProject(ctx context.Context, project string) (PurgeReceipt, error) {
	if project == "" {
		return PurgeReceipt{}, fmt.Errorf("project must not be empty")
	}

	var receipt PurgeReceipt
	receipt.Project = project
	receipt.PurgedAt = time.Now().UTC()

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// --- Step 1: snapshot counts before any deletion ---

		// Count memories for this project (including soft-deleted rows — hard purge removes all).
		if err := tx.Model(&Memory{}).
			Where("project = ?", project).
			Count(&receipt.MemoryCount).Error; err != nil {
			return fmt.Errorf("count memories: %w", err)
		}

		// Collect memory IDs so we can count/delete edges and audit rows.
		var memIDs []int64
		if receipt.MemoryCount > 0 {
			if err := tx.Model(&Memory{}).
				Where("project = ?", project).
				Pluck("id", &memIDs).Error; err != nil {
				return fmt.Errorf("collect memory ids: %w", err)
			}
		}

		// Count knowledge_edges touching this project's memories.
		if len(memIDs) > 0 {
			if err := tx.Table("knowledge_edges").
				Where("source_id IN ? OR target_id IN ?", memIDs, memIDs).
				Count(&receipt.EdgeCount).Error; err != nil {
				return fmt.Errorf("count edges: %w", err)
			}
		}

		// Count audit_log rows referencing this project's memories.
		// audit_log.memory_id is *int64 (nullable); only count non-NULL matches.
		if len(memIDs) > 0 {
			if err := tx.Table("audit_log").
				Where("memory_id IN ?", memIDs).
				Count(&receipt.AuditCount).Error; err != nil {
				return fmt.Errorf("count audit rows: %w", err)
			}
		}

		// Count behavioral_rules for this project.
		// BehavioralRule.Project is *string / NULLable — NULL means global rule.
		if err := tx.Model(&BehavioralRule{}).
			Where("project = ?", project).
			Count(&receipt.RuleCount).Error; err != nil {
			return fmt.Errorf("count rules: %w", err)
		}

		// --- Step 2: delete knowledge_edges ---
		// ON DELETE CASCADE (migration 121) would remove edges when memories are
		// deleted, but we hard-delete explicitly here so the count is authoritative.
		if len(memIDs) > 0 {
			if err := tx.Exec(
				"DELETE FROM knowledge_edges WHERE source_id IN ? OR target_id IN ?",
				memIDs, memIDs,
			).Error; err != nil {
				return fmt.Errorf("delete edges: %w", err)
			}
		}

		// --- Step 3: delete audit_log rows referencing the project's memories ---
		// These rows reference memory IDs that are about to be deleted. Keeping them
		// would leave orphan audit entries pointing to non-existent memory IDs.
		// The purge event itself is recorded as a new audit row AFTER this transaction
		// completes (caller's responsibility, or via a separate AuditStore.Log call).
		if len(memIDs) > 0 {
			if err := tx.Exec(
				"DELETE FROM audit_log WHERE memory_id IN ?",
				memIDs,
			).Error; err != nil {
				return fmt.Errorf("delete audit rows: %w", err)
			}
		}

		// --- Step 4: delete memories (credentials table untouched — vault concern) ---
		if err := tx.Exec(
			"DELETE FROM memories WHERE project = ?",
			project,
		).Error; err != nil {
			return fmt.Errorf("delete memories: %w", err)
		}

		// --- Step 5: delete behavioral_rules for this project ---
		// NULL-project rows are global rules and must not be affected.
		if err := tx.Exec(
			"DELETE FROM behavioral_rules WHERE project = ?",
			project,
		).Error; err != nil {
			return fmt.Errorf("delete rules: %w", err)
		}

		// --- Step 6: write the purge audit row inside the transaction ---
		// memory_id is nullable (*int64); pass nil to indicate project-level event.
		// after_state carries the receipt JSON so the event is self-describing.
		receiptJSON, err := json.Marshal(receipt)
		if err != nil {
			return fmt.Errorf("marshal receipt: %w", err)
		}
		raw := json.RawMessage(receiptJSON)
		purgeEntry := AuditLogEntry{
			MemoryID:   nil, // project-level event; no single memory_id
			Action:     "purge",
			Actor:      "admin",
			AfterState: &raw,
		}
		if err := tx.Create(&purgeEntry).Error; err != nil {
			return fmt.Errorf("write purge audit: %w", err)
		}

		return nil
	})
	if err != nil {
		return PurgeReceipt{}, err
	}

	return receipt, nil
}
