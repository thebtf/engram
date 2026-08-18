package gorm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// PurgeReceipt records what was deleted during a project purge.
// JSON-serializable so it can be stored in audit_log.after_state.
type PurgeReceipt struct {
	Project             string    `json:"project"`
	PurgedAt            time.Time `json:"purged_at"`
	MemoryCount         int64     `json:"memory_count"`
	RuleCount           int64     `json:"rule_count"`
	EdgeCount           int64     `json:"edge_count"`
	AuditCount          int64     `json:"audit_count"`
	AttentionEventCount int64     `json:"attention_event_count"`
	CitationCount       int64     `json:"citation_count"`
	ChunkCount          int64     `json:"chunk_count"`
	PromotionCount      int64     `json:"promotion_count"`
}

// PurgeStore handles project-scoped hard deletion.
type PurgeStore struct {
	db *gorm.DB
}

// NewPurgeStore creates a PurgeStore backed by the given Store.
func NewPurgeStore(store *Store) *PurgeStore {
	return &PurgeStore{db: store.DB}
}

// PurgeProject permanently deletes all memories, behavioral rules, reasoning
// traces, and associated edges/audit rows for the given project in a single
// transaction.
//
// The project name is trimmed of surrounding whitespace; a whitespace-only
// project name is rejected as empty. When no rows exist for the project, the
// purge succeeds and all receipt counts are zero — idempotent re-purge is a
// legitimate caller pattern.
//
// Deletion order (FK-safe):
//  1. Acquire pg_advisory_xact_lock scoped to the project to serialise
//     concurrent purges of the same project.
//  2. citation_log rows referencing the project's memories — NOT NULL FK,
//     no ON DELETE CASCADE.
//  3. content_chunks rows referencing the project's memories — NOT NULL FK,
//     no ON DELETE CASCADE.
//  4. promotion_log rows referencing the project's memories — NOT NULL FK,
//     no ON DELETE CASCADE.
//  5. knowledge_edges where source_id or target_id belong to the project's
//     memory IDs — migration 121 added ON DELETE CASCADE, but we delete
//     explicitly so the RowsAffected count is authoritative.
//  6. audit_log rows whose memory_id references the project's memories —
//     nullable column, no FK constraint, but rows would become orphan noise.
//  7. attention_events WHERE project = ? — project-scoped S4A captures with
//     no memory FK, but they must be purged with the rest of the project's data.
//  8. project_continuity_slots WHERE project = ? OR memory_id references one
//     of the project's memories — migration 161 adds a RESTRICT target FK;
//     project-key rows are also cleared to prevent stale designations.
//  9. NULL out supersedes_id on memories in other projects that reference
//     this project's memories — prevents FK violation on step 10.
//  10. memories WHERE project = ? — credentials live in the separate
//     `credentials` table and are explicitly excluded (vault concern).
//  11. behavioral_rules WHERE project = ? (project column is *string / NULLable;
//     NULL = global rule — those are never touched).
//     (Former step 12 — reasoning_traces — removed in CR-2a; store + writers gone.)
//
// All child-table deletes use SQL subqueries (DELETE … WHERE … IN (SELECT id
// FROM memories WHERE project = ?)) rather than a Go-side ID slice, which
// avoids the PostgreSQL 65 535-parameter limit for large projects and keeps
// memory usage flat regardless of project size.
//
// Receipt counts are taken from RowsAffected of each DELETE statement, making
// them authoritative regardless of concurrent writes during the transaction.
// All operations are wrapped in a single GORM transaction; any failure
// rolls back the entire purge.
func (s *PurgeStore) PurgeProject(ctx context.Context, project string) (PurgeReceipt, error) {
	// Trim and validate — reject whitespace-only project names.
	project = strings.TrimSpace(project)
	if project == "" {
		return PurgeReceipt{}, fmt.Errorf("project must not be empty")
	}

	var receipt PurgeReceipt
	receipt.Project = project
	receipt.PurgedAt = time.Now().UTC()

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// --- Step 1: advisory lock scoped to this project ---
		// Serialises concurrent purges of the same project. hashtext() maps the
		// project string to a 32-bit integer; the 'engram_purge_' prefix
		// namespace-isolates the lock from any other advisory lock users.
		if err := tx.Exec(
			"SELECT pg_advisory_xact_lock(hashtext('engram_purge_' || ?))",
			project,
		).Error; err != nil {
			return fmt.Errorf("acquire advisory lock: %w", err)
		}

		// --- Step 2: citation_log (NOT NULL FK, no CASCADE) ---
		// citation_log.memory_id BIGINT NOT NULL REFERENCES memories(id)
		r := tx.Exec(
			"DELETE FROM citation_log WHERE memory_id IN (SELECT id FROM memories WHERE project = ?)",
			project,
		)
		if r.Error != nil {
			return fmt.Errorf("delete citation_log: %w", r.Error)
		}
		receipt.CitationCount = r.RowsAffected

		// --- Step 3: content_chunks (NOT NULL FK, no CASCADE) ---
		// content_chunks.memory_id BIGINT NOT NULL REFERENCES memories(id)
		r = tx.Exec(
			"DELETE FROM content_chunks WHERE memory_id IN (SELECT id FROM memories WHERE project = ?)",
			project,
		)
		if r.Error != nil {
			return fmt.Errorf("delete content_chunks: %w", r.Error)
		}
		receipt.ChunkCount = r.RowsAffected

		// --- Step 4: promotion_log (NOT NULL FK, no CASCADE) ---
		// promotion_log.memory_id BIGINT NOT NULL REFERENCES memories(id)
		r = tx.Exec(
			"DELETE FROM promotion_log WHERE memory_id IN (SELECT id FROM memories WHERE project = ?)",
			project,
		)
		if r.Error != nil {
			return fmt.Errorf("delete promotion_log: %w", r.Error)
		}
		receipt.PromotionCount = r.RowsAffected

		// --- Step 5: knowledge_edges (ON DELETE CASCADE via migration 121) ---
		// Explicit delete so RowsAffected count is authoritative.
		r = tx.Exec(
			"DELETE FROM knowledge_edges WHERE source_id IN (SELECT id FROM memories WHERE project = ?) OR target_id IN (SELECT id FROM memories WHERE project = ?)",
			project, project,
		)
		if r.Error != nil {
			return fmt.Errorf("delete edges: %w", r.Error)
		}
		receipt.EdgeCount = r.RowsAffected

		// --- Step 6: audit_log rows referencing the project's memories ---
		// audit_log.memory_id is nullable with no FK constraint; rows would
		// become orphan noise pointing to non-existent memory IDs.
		// The purge event itself is recorded as a new audit row below.
		r = tx.Exec(
			"DELETE FROM audit_log WHERE memory_id IN (SELECT id FROM memories WHERE project = ?)",
			project,
		)
		if r.Error != nil {
			return fmt.Errorf("delete audit rows: %w", r.Error)
		}
		receipt.AuditCount = r.RowsAffected

		// --- Step 7: project-scoped S4A attention_events ---
		r = tx.Exec("DELETE FROM attention_events WHERE project = ?", project)
		if r.Error != nil {
			return fmt.Errorf("delete attention_events: %w", r.Error)
		}
		receipt.AttentionEventCount = r.RowsAffected

		// --- Step 8: continuity slots for this project or its memory targets ---
		// migration 161 makes memory_id a RESTRICT FK, so these rows must be
		// removed before deleting the target memories below. Include project-key
		// matches because the schema permits a slot to target another project.
		r = tx.Exec(
			"DELETE FROM project_continuity_slots WHERE project = ? OR memory_id IN (SELECT id FROM memories WHERE project = ?)",
			project, project,
		)
		if r.Error != nil {
			return fmt.Errorf("delete continuity slots: %w", r.Error)
		}

		// --- Step 9: clear supersedes_id references from outside this project ---
		// memories.supersedes_id BIGINT REFERENCES memories(id) (no CASCADE, no SET NULL).
		// Rows in other projects that point at this project's memories would cause a
		// FK violation when the memories are deleted in step 9. NULL them out first.
		if err := tx.Exec(
			"UPDATE memories SET supersedes_id = NULL WHERE supersedes_id IN (SELECT id FROM memories WHERE project = ?)",
			project,
		).Error; err != nil {
			return fmt.Errorf("clear supersedes_id references: %w", err)
		}

		// --- Step 10: memories (credentials table untouched — vault concern) ---
		r = tx.Exec("DELETE FROM memories WHERE project = ?", project)
		if r.Error != nil {
			return fmt.Errorf("delete memories: %w", r.Error)
		}
		receipt.MemoryCount = r.RowsAffected

		// --- Step 11: behavioral_rules for this project ---
		// NULL-project rows are global rules and must not be affected.
		r = tx.Exec("DELETE FROM behavioral_rules WHERE project = ?", project)
		if r.Error != nil {
			return fmt.Errorf("delete rules: %w", r.Error)
		}
		receipt.RuleCount = r.RowsAffected

		// (Former step 12 deleted reasoning_traces rows. The reasoning_traces table
		// is dropped by migration 137 in CR-2b of provenance-cleanup — the same
		// change that removes this coupled DELETE. The table no longer exists post-
		// migration, so the statement would error; it is removed here per the
		// coupling note added in CR-2a.)

		// --- Step 13: write the purge audit row inside the transaction ---
		// memory_id is nullable (*int64); pass nil to indicate project-level event.
		// after_state carries the receipt JSON so the event is self-describing.
		// Note: receipt counts at this point reflect actual rows deleted (RowsAffected);
		// zero counts are legitimate when the project had no data (idempotent re-purge).
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
