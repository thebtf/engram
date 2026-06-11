// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

// MemoryStore provides memory-related database operations using GORM.
// It targets the dedicated memories table created by migration 088.
//
// Immutability contract: Create and Update return NEW *models.Memory values populated
// from the database row. The caller's input struct is never mutated.
type MemoryStore struct {
	db *gorm.DB
}

// NewMemoryStore creates a new MemoryStore backed by the given Store.
func NewMemoryStore(store *Store) *MemoryStore {
	return &MemoryStore{db: store.DB}
}

func validateMemoryForCreate(mem *models.Memory) error {
	if mem == nil {
		return fmt.Errorf("memory must not be nil")
	}
	if mem.Project == "" {
		return fmt.Errorf("memory.Project must not be empty")
	}
	if mem.Content == "" {
		return fmt.Errorf("memory.Content must not be empty")
	}
	return nil
}

func memoryRowForCreate(mem *models.Memory, now time.Time, includeLifecycle bool) *Memory {
	row := &Memory{
		Project:        mem.Project,
		Content:        mem.Content,
		Tags:           models.JSONStringArray(mem.Tags),
		SourceAgent:    mem.SourceAgent,
		EditedBy:       mem.EditedBy,
		Status:         "active",
		ImportanceBase: 0.5,
		TsAlpha:        1.0,
		TsBeta:         1.0,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if mem.Version > 0 {
		row.Version = mem.Version
	}
	if mem.Status != "" {
		row.Status = mem.Status
	}
	if mem.ImportanceBase > 0 {
		row.ImportanceBase = mem.ImportanceBase
	}
	if mem.SupersedesID != nil {
		row.SupersedesID = mem.SupersedesID
	}
	if includeLifecycle {
		// Lifecycle fields only override DB defaults when caller supplies them.
		if mem.Tier != "" {
			row.Tier = mem.Tier
		}
		if mem.EpistemicType != "" {
			row.EpistemicType = mem.EpistemicType
		}
		if mem.Defeasibility != "" {
			row.Defeasibility = mem.Defeasibility
		}
	}
	return row
}

// copyPrivacyFields transfers privacy_scope, source_workstation_id, and
// source_sessions from mem to row. Called by Create, CreateWithLifecycle, and
// CreateWithLifecycleIfTagAbsent to ensure all insert paths persist the
// migration-125/130 columns consistently (codex P1 review #221 finding:
// CreateWithLifecycle omitted these copies that Create already performed).
//
// When mem.PrivacyScope is empty the row is set to 'project' (the DB DEFAULT),
// unless the Tags slice contains the legacy "scope:global" marker in which case
// the row is promoted to 'global' (mirrors the migration-125 T006 backfill).
func copyPrivacyFields(row *Memory, mem *models.Memory) {
	if mem.PrivacyScope != "" {
		row.PrivacyScope = mem.PrivacyScope
	} else {
		row.PrivacyScope = "project"
		for _, t := range mem.Tags {
			if t == "scope:global" {
				row.PrivacyScope = "global"
				break
			}
		}
	}
	row.SourceWorkstationID = mem.SourceWorkstationID
	if len(mem.SourceSessions) > 0 {
		row.SourceSessions = pq.StringArray(mem.SourceSessions)
	}
}

func advisoryLockKey(parts ...string) int64 {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

func tagContainmentJSON(tag string) (string, error) {
	tagBytes, err := json.Marshal([]string{tag})
	if err != nil {
		return "", fmt.Errorf("marshal tag containment JSON: %w", err)
	}
	return string(tagBytes), nil
}

// Create inserts a new memory row. Returns a new *models.Memory populated with the
// database-assigned ID and timestamps. The caller's input is never mutated.
//
// Lifecycle contract: Create intentionally does NOT persist Tier, EpistemicType, or
// Defeasibility fields — the DB schema defaults remain authoritative for all ordinary
// callers (store_memory MCP tool, extraction, correction, ingest). Flag-gated paths
// that need lifecycle metadata must use CreateWithLifecycle instead.
func (s *MemoryStore) Create(ctx context.Context, mem *models.Memory) (*models.Memory, error) {
	if err := validateMemoryForCreate(mem); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	row := memoryRowForCreate(mem, now, false)
	// Lifecycle fields (Tier, EpistemicType, Defeasibility) are intentionally
	// NOT copied here. Use CreateWithLifecycle for flag-gated paths.

	// T002 + T001b + T003b (engram vNext Milestone F TG1): persist privacy
	// metadata into migration-125/130 columns. See copyPrivacyFields for
	// the detailed rationale (codex P1 fix-forward on 3e4a4b1 / PR #221).
	copyPrivacyFields(row, mem)

	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("create memory for project %q: %w", mem.Project, err)
	}
	return memoryRowToModel(row), nil
}

// CreateWithLifecycle inserts a new memory row including Tier, EpistemicType, and
// Defeasibility fields. It MUST only be called from flag-gated paths:
//   - crystallization bridge (ENGRAM_CRYSTALLIZATION_ENABLED)
//   - MCP store_memory tool (ENGRAM_LIFECYCLE_ENABLED)
//   - ingest tool (ENGRAM_VNEXT_ENABLED or ENGRAM_LIFECYCLE_ENABLED)
//
// Callers that are NOT behind one of these flags must call Create instead to
// preserve the default-off byte-identity contract (milestone-B cycle-3).
func (s *MemoryStore) CreateWithLifecycle(ctx context.Context, mem *models.Memory) (*models.Memory, error) {
	if err := validateMemoryForCreate(mem); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	row := memoryRowForCreate(mem, now, true)
	// Lifecycle fields: only override when caller supplies non-empty value so
	// that DB schema defaults remain authoritative for unspecified fields.

	// T002 + T001b + T003b: persist privacy metadata. Previously omitted on
	// this path, causing privacy_scope to fall back to the DB default 'project'
	// even when the caller set a non-project scope (codex P1 PR #221 fix).
	copyPrivacyFields(row, mem)

	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("create memory with lifecycle for project %q: %w", mem.Project, err)
	}
	return memoryRowToModel(row), nil
}

// createMemoryWithLifecycleTx inserts a lifecycle memory row using an existing
// GORM transaction (tx). This is a package-internal helper used by
// CandidateStore.PromoteWithMemory to create the promoted memory atomically
// within the same database transaction as the candidate status update.
//
// Callers MUST validate mem before calling (validateMemoryForCreate).
func createMemoryWithLifecycleTx(ctx context.Context, tx *gorm.DB, mem *models.Memory) (*models.Memory, error) {
	now := time.Now().UTC()
	row := memoryRowForCreate(mem, now, true)
	copyPrivacyFields(row, mem)
	if err := tx.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("create memory with lifecycle (tx) for project %q: %w", mem.Project, err)
	}
	return memoryRowToModel(row), nil
}

// CreateWithLifecycleIfTagAbsent inserts a lifecycle memory only when no active
// memory from the same project/source_agent already carries uniqueTag. The
// check and insert are protected by a PostgreSQL transaction-level advisory
// lock keyed by project, source_agent, and tag so concurrent callers cannot
// double-insert the same fingerprint.
func (s *MemoryStore) CreateWithLifecycleIfTagAbsent(
	ctx context.Context,
	mem *models.Memory,
	uniqueTag string,
) (*models.Memory, bool, error) {
	if err := validateMemoryForCreate(mem); err != nil {
		return nil, false, err
	}
	if mem.SourceAgent == "" {
		return nil, false, fmt.Errorf("memory.SourceAgent must not be empty")
	}
	if uniqueTag == "" {
		return nil, false, fmt.Errorf("uniqueTag must not be empty")
	}

	var created *models.Memory
	duplicate := false
	lockKey := advisoryLockKey("mem-tag", mem.Project, mem.SourceAgent, uniqueTag)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", lockKey).Error; err != nil {
			return fmt.Errorf("lock memory tag project=%q agent=%q tag=%q: %w", mem.Project, mem.SourceAgent, uniqueTag, err)
		}

		tagJSON, err := tagContainmentJSON(uniqueTag)
		if err != nil {
			return err
		}
		var existing Memory
		err = tx.Select("id").
			Where("project = ? AND source_agent = ? AND status = 'active' AND deleted_at IS NULL AND tags @> ?::jsonb", mem.Project, mem.SourceAgent, tagJSON).
			Limit(1).
			Take(&existing).Error
		if err == nil {
			duplicate = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("check memory tag project=%q agent=%q tag=%q: %w", mem.Project, mem.SourceAgent, uniqueTag, err)
		}

		row := memoryRowForCreate(mem, time.Now().UTC(), true)
		// T002 + T001b + T003b: persist privacy metadata (same fix as
		// CreateWithLifecycle — codex P1 PR #221).
		copyPrivacyFields(row, mem)
		if err := tx.Create(row).Error; err != nil {
			return fmt.Errorf("create memory with lifecycle for project %q: %w", mem.Project, err)
		}
		created = memoryRowToModel(row)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return created, duplicate, nil
}

// Get returns the active (non-soft-deleted) memory with the given ID.
// Returns a wrapped gorm.ErrRecordNotFound if no active row exists.
func (s *MemoryStore) Get(ctx context.Context, id int64) (*models.Memory, error) {
	if id == 0 {
		return nil, fmt.Errorf("id must be non-zero")
	}
	var row Memory
	err := s.db.WithContext(ctx).
		Where("id = ? AND deleted_at IS NULL", id).
		First(&row).Error
	if err != nil {
		return nil, fmt.Errorf("get memory id=%d: %w", id, err)
	}
	return memoryRowToModel(&row), nil
}

// List returns active (non-soft-deleted) memories for the given project,
// ordered by created_at DESC, limited to limit rows.
// project must not be empty.
//
// T017a: List now delegates to ListWithFilters with zero-value opts so there
// is a single WHERE-clause implementation path. Behavior is byte-identical to
// the previous standalone implementation (status='active', no confidence floor).
func (s *MemoryStore) List(ctx context.Context, project string, limit int) ([]*models.Memory, error) {
	result, err := s.ListWithFilters(ctx, project, ListOptions{Limit: limit})
	if err != nil {
		return nil, fmt.Errorf("list memories for project %q: %w", project, err)
	}
	return result, nil
}

// ListOptions controls the optional filters applied by ListWithFilters.
// Zero-value opts replicate the exact WHERE clause of the legacy List method
// (status='active', no confidence floor), ensuring byte-identical behavior when
// callers delegate from List to ListWithFilters with default opts.
//
// T017a (engram vNext Milestone F TG3): store-level extension prerequisite.
type ListOptions struct {
	// ConfidenceMin, when > 0, adds WHERE confidence >= ConfidenceMin.
	// Default 0.0 means no confidence filter applied.
	ConfidenceMin float64
	// IncludeSuperseded, when true, relaxes the status filter to
	// WHERE status IN ('active','superseded'). Default false means
	// only 'active' rows are returned (same as legacy List).
	IncludeSuperseded bool
	// Limit caps the number of returned rows. Values <= 0 default to 50.
	Limit int
}

// ListWithFilters returns memories for the given project with optional filters
// applied at the SQL layer. This is the TG3 store-level extension:
//
//   - IncludeSuperseded=false (default): WHERE status='active' — byte-identical to List.
//   - IncludeSuperseded=true: WHERE status IN ('active','superseded').
//   - ConfidenceMin > 0: AND confidence >= ConfidenceMin on the Memory.Confidence column.
//
// Legacy List now delegates here with zero-value opts to guarantee a single
// implementation path and remove the risk of WHERE-clause divergence.
//
// T017a: anti-stub guarantee — replacing the body with the original List
// returns no rows for IncludeSuperseded=true test cases.
func (s *MemoryStore) ListWithFilters(ctx context.Context, project string, opts ListOptions) ([]*models.Memory, error) {
	if project == "" {
		return nil, fmt.Errorf("project: must not be empty")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	q := s.db.WithContext(ctx).
		Where("project = ? AND deleted_at IS NULL", project).
		Where("valid_from IS NULL OR valid_from <= NOW()").
		Where("valid_until IS NULL OR valid_until >= NOW()")

	if opts.IncludeSuperseded {
		q = q.Where("status IN ('active','superseded')")
	} else {
		q = q.Where("status = 'active'")
	}
	if opts.ConfidenceMin > 0 {
		q = q.Where("confidence >= ?", opts.ConfidenceMin)
	}

	var rows []Memory
	err := q.Order("created_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list memories with filters for project %q: %w", project, err)
	}
	result := make([]*models.Memory, len(rows))
	for i := range rows {
		result[i] = memoryRowToModel(&rows[i])
	}
	return result, nil
}

// ListWithOffset returns a page of active (non-soft-deleted) memories for the
// given project, ordered by created_at DESC, limited to limit rows starting
// from offset. project must not be empty. limit and offset default to safe
// values when <= 0 / < 0.
//
// T004 + codex P1 cycle-3 fix on 4cb71be: introduced to support batch-loop
// scope filtering in handleRecallSearch. The previous single-call List path
// would fetch up to `limit` rows and then drop scope-invisible ones in Go,
// truncating recall when the newest rows happened to be private to other
// callers. ListWithOffset lets the caller keep paging until enough visible
// rows accumulate or the DB stream is exhausted.
func (s *MemoryStore) ListWithOffset(ctx context.Context, project string, limit int, offset int) ([]*models.Memory, error) {
	if project == "" {
		return nil, fmt.Errorf("project: must not be empty")
	}
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var rows []Memory
	// Codex P2 cycle-4 fix on 783c0be: add `id DESC` as a deterministic
	// secondary order key so offset-paged scans cannot skip or repeat rows
	// when multiple memories share the same created_at value. handleRecallSearch
	// invokes ListWithOffset in a loop with changing OFFSET, and ties on
	// created_at would otherwise destabilise page boundaries — eligible rows
	// could be missed before the visible-result limit is reached.
	//
	// Use NOW() (DB server clock) instead of a Go-side timestamp for valid_from /
	// valid_until comparisons. The DB DEFAULT for valid_from is now(), evaluated
	// at INSERT time. If we compare against a Go-side time.Now() captured before
	// the SELECT, a just-inserted row's valid_from can be fractionally newer than
	// the Go clock value, causing the row to be excluded from the first List call.
	err := s.db.WithContext(ctx).
		Where("project = ? AND status = 'active' AND deleted_at IS NULL", project).
		Where("valid_from IS NULL OR valid_from <= NOW()").
		Where("valid_until IS NULL OR valid_until >= NOW()").
		Order("created_at DESC, id DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list memories with offset for project %q: %w", project, err)
	}
	result := make([]*models.Memory, len(rows))
	for i := range rows {
		result[i] = memoryRowToModel(&rows[i])
	}
	return result, nil
}

// ListForInjection returns active memories for the given project ordered by
// importance_base DESC, created_at DESC — suitable for context injection.
// project must not be empty. limit defaults to 50 when <= 0.
func (s *MemoryStore) ListForInjection(ctx context.Context, project string, limit int) ([]*models.Memory, error) {
	if project == "" {
		return nil, fmt.Errorf("project must not be empty")
	}
	if limit <= 0 {
		limit = 50
	}
	lifecycleEnabled := os.Getenv("ENGRAM_LIFECYCLE_ENABLED") == "true"
	var rows []Memory
	q := s.db.WithContext(ctx).
		Where("project = ? AND status = 'active' AND deleted_at IS NULL", project).
		Where("valid_from IS NULL OR valid_from <= NOW()").
		Where("valid_until IS NULL OR valid_until >= NOW()")

	if lifecycleEnabled {
		q = q.Where("tier != 'working'").
			Where("retrievability > ?", 0.3)
	}

	err := q.Order("importance_base DESC, created_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list memories for injection project=%q: %w", project, err)
	}
	result := make([]*models.Memory, len(rows))
	for i := range rows {
		result[i] = memoryRowToModel(&rows[i])
	}
	return result, nil
}

// Update updates an existing memory row by ID.
// Bumps version and sets updated_at. Returns a NEW populated model.
// The caller's input struct is never mutated.
func (s *MemoryStore) Update(ctx context.Context, mem *models.Memory) (*models.Memory, error) {
	if mem == nil {
		return nil, fmt.Errorf("memory must not be nil")
	}
	if mem.ID == 0 {
		return nil, fmt.Errorf("memory.ID must be set for Update")
	}
	if mem.Content == "" {
		return nil, fmt.Errorf("memory.Content must not be empty")
	}

	now := time.Now().UTC()

	// Perform the update using a map to avoid GORM zero-value omission issues.
	updates := map[string]any{
		"content":      mem.Content,
		"tags":         models.JSONStringArray(mem.Tags),
		"source_agent": mem.SourceAgent,
		"edited_by":    mem.EditedBy,
		"updated_at":   now,
		"version":      gorm.Expr("version + 1"),
	}

	result := s.db.WithContext(ctx).
		Model(&Memory{}).
		Where("id = ? AND deleted_at IS NULL", mem.ID).
		Updates(updates)
	if result.Error != nil {
		return nil, fmt.Errorf("update memory id=%d: %w", mem.ID, result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, fmt.Errorf("update memory id=%d: %w", mem.ID, gorm.ErrRecordNotFound)
	}

	// Re-fetch to return the fully-populated model.
	return s.Get(ctx, mem.ID)
}

// Delete soft-deletes the memory by setting deleted_at = NOW().
// Returns gorm.ErrRecordNotFound if no active row exists.
func (s *MemoryStore) Delete(ctx context.Context, id int64) error {
	if id == 0 {
		return fmt.Errorf("memory id must be non-zero")
	}
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).
		Model(&Memory{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Updates(map[string]any{
			"deleted_at": now,
			"updated_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("delete memory id=%d: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("delete memory id=%d: %w", id, gorm.ErrRecordNotFound)
	}
	return nil
}

// Supersede marks an existing memory as superseded and returns the memory's importance_base
// BEFORE the penalty was applied (for the caller to compute the new memory's importance).
//
// The old memory receives status='superseded' and importance_base *= 0.1.
// Returns an error when the memory is not found or is already superseded/deleted.
func (s *MemoryStore) Supersede(ctx context.Context, id int64) (oldImportance float64, err error) {
	if id == 0 {
		return 0, fmt.Errorf("memory id must be non-zero")
	}
	// Read current importance before update.
	var row Memory
	if err := s.db.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", id).First(&row).Error; err != nil {
		return 0, fmt.Errorf("supersede memory id=%d: %w", id, err)
	}
	oldImportance = row.ImportanceBase

	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&Memory{}).
		Where("id = ? AND deleted_at IS NULL AND status = 'active'", id).
		Updates(map[string]any{
			"status":          "superseded",
			"importance_base": row.ImportanceBase * 0.1,
			"updated_at":      now,
		})
	if result.Error != nil {
		return 0, fmt.Errorf("supersede memory id=%d: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return 0, fmt.Errorf("supersede memory id=%d: not found or already superseded", id)
	}
	return oldImportance, nil
}

// MarkSuperseded atomically sets status='superseded' and superseded_by=newID on
// the memory identified by olderID. It does NOT scale importance_base (that
// remains the caller's decision). Returns an error when olderID is not found,
// already deleted, or when the UPDATE affects 0 rows (already superseded is
// treated as success to keep the operation idempotent).
//
// This is distinct from Supersede: Supersede also scales importance and does
// not record the successor ID. MarkSuperseded is the precise link-recording
// counterpart used by the writelint Phase2 supersede path.
func (s *MemoryStore) MarkSuperseded(ctx context.Context, olderID, newID int64) error {
	if olderID == 0 {
		return fmt.Errorf("MarkSuperseded: olderID must be non-zero")
	}
	if newID == 0 {
		return fmt.Errorf("MarkSuperseded: newID must be non-zero")
	}
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&Memory{}).
		Where("id = ? AND deleted_at IS NULL", olderID).
		Updates(map[string]any{
			"status":        "superseded",
			"superseded_by": newID,
			"updated_at":    now,
		})
	if result.Error != nil {
		return fmt.Errorf("MarkSuperseded memory id=%d: %w", olderID, result.Error)
	}
	// 0 rows affected means the memory was not found or already deleted.
	// An already-superseded row still exists and should be updated if found.
	if result.RowsAffected == 0 {
		return fmt.Errorf("MarkSuperseded memory id=%d: %w", olderID, gorm.ErrRecordNotFound)
	}
	return nil
}

// UpdateLifecycleFields updates specific lifecycle fields on a memory without
// touching content, tags, or version. Used by feedback and injection pipelines.
func (s *MemoryStore) UpdateLifecycleFields(ctx context.Context, id int64, fields map[string]any) error {
	if id == 0 {
		return fmt.Errorf("memory id must be non-zero")
	}
	if fields == nil {
		return fmt.Errorf("fields must not be nil")
	}
	fields["updated_at"] = time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&Memory{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Updates(fields)
	if result.Error != nil {
		return fmt.Errorf("update lifecycle fields memory id=%d: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("update lifecycle fields: memory id=%d not found", id)
	}
	return nil
}

// IncrementInjectionCount atomically increments injection_count for a memory.
func (s *MemoryStore) IncrementInjectionCount(ctx context.Context, id int64) error {
	return s.db.WithContext(ctx).Model(&Memory{}).
		Where("id = ? AND deleted_at IS NULL", id).
		UpdateColumn("injection_count", gorm.Expr("injection_count + 1")).Error
}

// BatchIncrementCited atomically increments ts_alpha, citation_count, and recalculates
// importance_base for the given memory IDs. All updates are applied in a single SQL statement.
func (s *MemoryStore) BatchIncrementCited(ctx context.Context, ids []int64) error {
	return s.db.WithContext(ctx).Exec(
		"UPDATE memories SET ts_alpha = ts_alpha + 1, citation_count = citation_count + 1, importance_base = LEAST(1.0, GREATEST(importance_base, importance_base * ln(2.0 + citation_count))), updated_at = now() WHERE id = ANY(?)",
		pq.Array(ids),
	).Error
}

// BatchIncrementUncited atomically increments ts_beta for the given memory IDs.
// All updates are applied in a single SQL statement.
func (s *MemoryStore) BatchIncrementUncited(ctx context.Context, ids []int64) error {
	return s.db.WithContext(ctx).Exec(
		"UPDATE memories SET ts_beta = ts_beta + 1, updated_at = now() WHERE id = ANY(?)",
		pq.Array(ids),
	).Error
}

// GetProjectCitationRate returns the aggregate citation rate for a project:
// sum(citation_count) / max(sum(injection_count), 1). Returns 0.5 when fewer
// than minSamples memories exist (insufficient data for an informed prior).
func (s *MemoryStore) GetProjectCitationRate(ctx context.Context, project string, minSamples int) (float64, error) {
	var result struct {
		TotalCitations  float64
		TotalInjections float64
		MemoryCount     int64
	}
	err := s.db.WithContext(ctx).Raw(`
		SELECT COALESCE(SUM(citation_count), 0) AS total_citations,
		       COALESCE(SUM(injection_count), 0) AS total_injections,
		       COUNT(*) AS memory_count
		FROM memories
		WHERE project = ? AND deleted_at IS NULL AND status != 'flagged'`,
		project,
	).Scan(&result).Error
	if err != nil {
		return 0.5, err
	}
	if result.MemoryCount < int64(minSamples) || result.TotalInjections < 1 {
		return 0.5, nil
	}
	rate := result.TotalCitations / result.TotalInjections
	if rate > 1.0 {
		rate = 1.0
	}
	return rate, nil
}

// BatchIncrementCitedN increments ts_alpha by a damped amount for the given memory IDs.
// The actual boost is: n / (1 + consecutive_citation_count * damping_factor).
// Also increments consecutive_citation_count for diminishing returns tracking.
func (s *MemoryStore) BatchIncrementCitedN(ctx context.Context, ids []int64, n float64) error {
	return s.db.WithContext(ctx).Exec(
		`UPDATE memories SET
			ts_alpha = ts_alpha + ? / (1.0 + consecutive_citation_count * 0.1),
			citation_count = citation_count + 1,
			consecutive_citation_count = consecutive_citation_count + 1,
			importance_base = LEAST(1.0, GREATEST(importance_base, importance_base * ln(2.0 + citation_count))),
			updated_at = now()
		WHERE id = ANY(?)`,
		n, pq.Array(ids),
	).Error
}

// BatchIncrementUncitedN increments ts_beta by n and resets the consecutive
// citation counter for the given memory IDs.
func (s *MemoryStore) BatchIncrementUncitedN(ctx context.Context, ids []int64, n float64) error {
	return s.db.WithContext(ctx).Exec(
		"UPDATE memories SET ts_beta = ts_beta + ?, consecutive_citation_count = 0, updated_at = now() WHERE id = ANY(?)",
		n, pq.Array(ids),
	).Error
}

// BatchIncrementViolated applies a strong ts_beta penalty for violated memories.
func (s *MemoryStore) BatchIncrementViolated(ctx context.Context, ids []int64, n float64) error {
	return s.db.WithContext(ctx).Exec(
		"UPDATE memories SET ts_beta = ts_beta + ?, updated_at = now() WHERE id = ANY(?)",
		n, pq.Array(ids),
	).Error
}

// memoryRowToModel converts an internal GORM Memory row to the pkg/models.Memory type.
func memoryRowToModel(row *Memory) *models.Memory {
	m := &models.Memory{
		ID:                       row.ID,
		Project:                  row.Project,
		Content:                  row.Content,
		Tags:                     []string(row.Tags),
		SourceAgent:              row.SourceAgent,
		EditedBy:                 row.EditedBy,
		Status:                   row.Status,
		Tier:                     row.Tier,
		EpistemicType:            row.EpistemicType,
		Defeasibility:            row.Defeasibility,
		PromotionTarget:          row.PromotionTarget,
		Version:                  row.Version,
		CreatedAt:                row.CreatedAt,
		UpdatedAt:                row.UpdatedAt,
		DeletedAt:                row.DeletedAt,
		LastRetrievedAt:          row.LastRetrievedAt,
		LastConfirmed:            row.LastConfirmed,
		ReviewAfter:              row.ReviewAfter,
		ValidFrom:                row.ValidFrom,
		ValidUntil:               row.ValidUntil,
		SupersedesID:             row.SupersedesID,
		SupersededBy:             row.SupersededBy,
		ImportanceBase:           row.ImportanceBase,
		TsAlpha:                  row.TsAlpha,
		TsBeta:                   row.TsBeta,
		Confidence:               row.Confidence,
		Stability:                row.Stability,
		Retrievability:           row.Retrievability,
		CitationCount:            row.CitationCount,
		InjectionCount:           row.InjectionCount,
		AccessCount:              row.AccessCount,
		RecurrenceCount:          row.RecurrenceCount,
		ConsecutiveCitationCount: row.ConsecutiveCitationCount,
	}
	// T002 + T001b + T003b (engram vNext Milestone F TG1): read-back of privacy
	// metadata from migration-125/130 columns. Gated behind the vNext-F flag —
	// under flag OFF we leave the privacy fields empty so the `omitempty` JSON
	// tags on pkg/models.Memory preserve v6.4.x byte-identity for REST and MCP
	// responses (RI-F1). Codex P1 cycle-3 fix-forward on 4cb71be: without the
	// flag gate the field always reads back as 'project' (migration-125 DB
	// DEFAULT) and leaks into every flag-OFF response.
	if os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true" {
		m.PrivacyScope = row.PrivacyScope
		m.SourceWorkstationID = row.SourceWorkstationID
		m.SourceSessions = []string(row.SourceSessions)
	}
	return m
}

// ListBySourceAgentAndTag returns active memories for a project where source_agent
// matches sourceAgent AND the tags JSONB column contains the given tag string.
// Used by the crystallization pipeline for idempotency checks (P2-5).
// Returns at most 500 rows; callers that need exhaustive scans should query a
// narrower tag or use a paged query.
func (s *MemoryStore) ListBySourceAgentAndTag(ctx context.Context, project, sourceAgent, tag string) ([]*models.Memory, error) {
	if project == "" {
		return nil, fmt.Errorf("project must not be empty")
	}
	if sourceAgent == "" {
		return nil, fmt.Errorf("sourceAgent must not be empty")
	}
	if tag == "" {
		return nil, fmt.Errorf("tag must not be empty")
	}
	// Use PostgreSQL JSONB containment: tags @> '["<tag>"]'::jsonb
	tagJSON, err := tagContainmentJSON(tag)
	if err != nil {
		return nil, err
	}
	var rows []Memory
	err = s.db.WithContext(ctx).
		Where("project = ? AND source_agent = ? AND status = 'active' AND deleted_at IS NULL AND tags @> ?::jsonb", project, sourceAgent, tagJSON).
		Order("id ASC").
		Limit(500).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list memories by source_agent+tag project=%q agent=%q tag=%q: %w", project, sourceAgent, tag, err)
	}
	result := make([]*models.Memory, len(rows))
	for i := range rows {
		result[i] = memoryRowToModel(&rows[i])
	}
	return result, nil
}

// SearchFTS performs a full-text search against the memories table using the
// search_vector GENERATED ALWAYS column (migration 088). The query string is
// parsed with websearch_to_tsquery (supports quoted phrases, + for AND, - for NOT).
// Falls back to plainto_tsquery when websearch_to_tsquery produces an empty result
// (e.g. stop-word-only queries). Returns memories ordered by ts_rank_cd DESC,
// limited to limit rows (capped at 200 internally to prevent unbounded scans).
// Returns an empty slice — not an error — when no rows match.
func (s *MemoryStore) SearchFTS(ctx context.Context, project, query string, limit int) ([]*models.Memory, error) {
	if project == "" {
		return nil, fmt.Errorf("SearchFTS: project must not be empty")
	}
	if query == "" {
		return nil, fmt.Errorf("SearchFTS: query must not be empty")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}

	// Try websearch_to_tsquery first; fall back to plainto_tsquery for
	// stop-word-only inputs that yield an empty tsquery.
	// Use SQL NOW() instead of a Go-side timestamp to avoid clock-skew where a
	// Go-captured time pre-dates valid_from set by DB DEFAULT now() on freshly
	// inserted rows (see ListWithOffset comment for full rationale).
	// NULLIF comparison uses ''::tsquery cast — PostgreSQL has no implicit
	// tsquery ↔ unknown conversion and raises "operator does not exist" otherwise.
	var rows []Memory
	err := s.db.WithContext(ctx).Raw(`
		WITH parsed AS (
			SELECT websearch_to_tsquery('english', ?) AS wsq,
			       plainto_tsquery('english', ?)      AS ptq
		)
		SELECT m.*
		FROM   memories m, parsed
		WHERE  m.project    = ?
		AND    m.status     = 'active'
		AND    m.deleted_at IS NULL
		AND   (m.valid_from IS NULL OR m.valid_from <= NOW())
		AND   (m.valid_until IS NULL OR m.valid_until >= NOW())
		AND    m.search_vector @@ COALESCE(NULLIF(parsed.wsq, ''::tsquery), parsed.ptq)
		ORDER BY ts_rank_cd(m.search_vector,
		             COALESCE(NULLIF(parsed.wsq, ''::tsquery), parsed.ptq)) DESC
		LIMIT ?
	`, query, query, project, limit).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("SearchFTS project=%q: %w", project, err)
	}
	result := make([]*models.Memory, len(rows))
	for i := range rows {
		result[i] = memoryRowToModel(&rows[i])
	}
	return result, nil
}

// GetByIDs fetches active memories by a list of IDs scoped to project, preserving the ID order.
// The project filter prevents cross-project leakage when IDs originate from the vector leg
// (content_chunks has no project column; project scoping must be enforced here as second defence).
// project must be non-empty; an empty project returns an error.
func (s *MemoryStore) GetByIDs(ctx context.Context, project string, ids []int64) ([]*models.Memory, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if project == "" {
		return nil, fmt.Errorf("GetByIDs: project must be non-empty")
	}
	// Use SQL NOW() to avoid clock-skew with freshly-inserted rows whose
	// valid_from is set by DB DEFAULT now() (same rationale as ListWithOffset).
	var rows []Memory
	err := s.db.WithContext(ctx).
		Where("id = ANY(?) AND project = ? AND status = 'active' AND deleted_at IS NULL", pq.Array(ids), project).
		Where("valid_from IS NULL OR valid_from <= NOW()").
		Where("valid_until IS NULL OR valid_until >= NOW()").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("GetByIDs: %w", err)
	}
	// Rebuild in original id order.
	byID := make(map[int64]*models.Memory, len(rows))
	for i := range rows {
		m := memoryRowToModel(&rows[i])
		byID[m.ID] = m
	}
	result := make([]*models.Memory, 0, len(ids))
	for _, id := range ids {
		if m, ok := byID[id]; ok {
			result = append(result, m)
		}
	}
	return result, nil
}

// CountActiveSince returns the count of active memories with id > afterID.
// Used by the sleep cycle to count new memories since the last cycle run
// without fetching all rows. Pass afterID=0 to count all active memories.
func (s *MemoryStore) CountActiveSince(ctx context.Context, afterID int64) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Model(&Memory{}).
		Where("status = 'active' AND deleted_at IS NULL AND id > ?", afterID).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count active memories since id %d: %w", afterID, err)
	}
	return count, nil
}

// MaxActiveID returns the maximum id among active memories, or 0 if none exist.
// Used by the sleep cycle to record a high-water mark at cycle completion.
func (s *MemoryStore) MaxActiveID(ctx context.Context) (int64, error) {
	var maxID int64
	err := s.db.WithContext(ctx).
		Model(&Memory{}).
		Where("status = 'active' AND deleted_at IS NULL").
		Select("COALESCE(MAX(id), 0)").
		Scan(&maxID).Error
	if err != nil {
		return 0, fmt.Errorf("max active memory id: %w", err)
	}
	return maxID, nil
}

// ListAllActive returns a batch of active memories for sleep cycle processing.
func (s *MemoryStore) ListAllActive(ctx context.Context, batchSize int, offset int) ([]*models.Memory, error) {
	if batchSize <= 0 {
		batchSize = 500
	}
	var rows []Memory
	err := s.db.WithContext(ctx).
		Where("status = 'active' AND deleted_at IS NULL").
		Order("id ASC").
		Limit(batchSize).
		Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list all active memories: %w", err)
	}
	result := make([]*models.Memory, len(rows))
	for i := range rows {
		result[i] = memoryRowToModel(&rows[i])
	}
	return result, nil
}
