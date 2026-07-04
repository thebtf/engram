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
	"sort"
	"strings"
	"time"
	"unicode"

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

const (
	defaultMetaMemoryLimit      = 10
	maxMetaMemoryLimit          = 25
	metaMemoryProbeFactor       = 5
	metaMemoryFTSScanPageBudget = 5
)

// MetaIndexQuery is the content-free S2 query contract shared by the store,
// the S2 proposer, and later MCP/session-start adapters.
type MetaIndexQuery struct {
	Project            string
	Query              string
	Tags               []string
	OwnerPrincipal     string
	OwnerPrincipalKind string
	AgentVisibility    string
	Domain             string
	Limit              int
}

// MetaIndexHit is the content-free result shape returned by S2 store queries.
// It deliberately excludes memory body fields; callers expand by ID when they
// explicitly need full memory content.
type MetaIndexHit struct {
	ID        int64     `json:"id"`
	Project   string    `json:"project"`
	Title     string    `json:"title"`
	Tags      []string  `json:"tags,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Score     float32   `json:"score"`
	Source    string    `json:"source"`
	Reason    string    `json:"reason,omitempty"`
}

// MetaMemoryRecord is the content-free metadata row used internally by S2
// meta-memory queries before score/source annotation is added.
type MetaMemoryRecord struct {
	ID        int64
	Title     string
	Tags      []string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type metaMemorySelectRow struct {
	ID        int64                  `gorm:"column:id"`
	Title     string                 `gorm:"column:title"`
	Tags      models.JSONStringArray `gorm:"column:tags"`
	CreatedAt time.Time              `gorm:"column:created_at"`
	UpdatedAt time.Time              `gorm:"column:updated_at"`
}

func normalizeMetaMemoryLimit(limit int) int {
	if limit <= 0 {
		return defaultMetaMemoryLimit
	}
	if limit > maxMetaMemoryLimit {
		return maxMetaMemoryLimit
	}
	return limit
}

func normalizeMetaMemoryProbeLimit(limit int) int {
	probe := normalizeMetaMemoryLimit(limit) * metaMemoryProbeFactor
	if probe < maxMetaMemoryLimit {
		probe = maxMetaMemoryLimit
	}
	if probe > 200 {
		probe = 200
	}
	return probe
}

func normalizeMetaMemoryFTSScanBudget(limit int) int {
	return normalizeMetaMemoryProbeLimit(limit) * metaMemoryFTSScanPageBudget
}

func sanitizeMetaText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)), " ")
	if maxRunes > 0 {
		runes := []rune(value)
		if len(runes) > maxRunes {
			value = string(runes[:maxRunes])
		}
	}
	return strings.TrimSpace(value)
}

func sanitizeMetaTags(tags []string) []string {
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = sanitizeMetaText(tag, 64)
		if tag == "" {
			continue
		}
		result = append(result, tag)
	}
	return result
}

func cloneMetaTags(tags []string) []string {
	if tags == nil {
		return nil
	}
	cloned := make([]string, len(tags))
	copy(cloned, tags)
	return cloned
}

func metaMemoryTitleFromLine(line string) string {
	line = sanitizeMetaText(line, 80)
	if line == "" {
		return "untitled"
	}
	return line
}

func metaMemoryRowsToRecords(rows []metaMemorySelectRow) []MetaMemoryRecord {
	result := make([]MetaMemoryRecord, len(rows))
	for i := range rows {
		result[i] = MetaMemoryRecord{
			ID:        rows[i].ID,
			Title:     metaMemoryTitleFromLine(rows[i].Title),
			Tags:      sanitizeMetaTags([]string(rows[i].Tags)),
			CreatedAt: rows[i].CreatedAt,
			UpdatedAt: rows[i].UpdatedAt,
		}
	}
	return result
}

func metaMemoryMatchesOptions(mem *models.Memory, opts ListOptions) bool {
	if mem == nil {
		return false
	}
	if owner := strings.TrimSpace(opts.OwnerPrincipal); owner != "" && mem.OwnerPrincipal != owner {
		return false
	}
	if kind := strings.TrimSpace(strings.ToLower(opts.OwnerPrincipalKind)); kind != "" && strings.ToLower(strings.TrimSpace(mem.OwnerPrincipalKind)) != kind {
		return false
	}
	if visibility := strings.TrimSpace(opts.AgentVisibility); visibility != "" && mem.AgentVisibility != visibility {
		return false
	}
	if domain := strings.TrimSpace(opts.Domain); domain != "" && mem.Domain != domain {
		return false
	}
	if opts.ConfidenceMin > 0 && mem.Confidence < opts.ConfidenceMin {
		return false
	}
	if len(opts.IDs) > 0 {
		matched := false
		for _, id := range opts.IDs {
			if mem.ID == id {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
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
	if err := validateMemoryOwnershipForCreate(mem); err != nil {
		return err
	}
	return nil
}

func validateMemoryOwnershipForCreate(mem *models.Memory) error {
	owner := strings.TrimSpace(mem.OwnerPrincipal)
	kind := strings.TrimSpace(mem.OwnerPrincipalKind)
	visibility := strings.TrimSpace(mem.AgentVisibility)

	if owner == "" {
		if kind != "" {
			return fmt.Errorf("memory.OwnerPrincipalKind requires memory.OwnerPrincipal")
		}
		if visibility != "" {
			return fmt.Errorf("invalid_agent_visibility: principal is required for agent_visibility")
		}
		return nil
	}

	if kind != "" && !isValidMemoryOwnerPrincipalKind(kind) {
		return fmt.Errorf("invalid_owner_principal_kind: %q must be one of human, agent, service", kind)
	}
	if visibility != "" && !models.IsValidAgentVisibility(visibility) {
		return fmt.Errorf("invalid_agent_visibility: %q must be one of private, shared", visibility)
	}
	return nil
}

func isValidMemoryOwnerPrincipalKind(kind string) bool {
	switch kind {
	case "human", "agent", "service":
		return true
	default:
		return false
	}
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

func copyPrincipalMemoryFields(row *Memory, mem *models.Memory) {
	row.Domain = strings.TrimSpace(mem.Domain)

	owner := strings.TrimSpace(mem.OwnerPrincipal)
	if owner == "" {
		return
	}

	kind := strings.TrimSpace(mem.OwnerPrincipalKind)
	if kind == "" {
		kind = "human"
	}
	visibility := strings.TrimSpace(mem.AgentVisibility)
	if visibility == "" {
		// Preserve existing team-visible behavior for owned writes; CR-004 will
		// add principal-aware recall filtering for explicit private memories.
		visibility = models.AgentVisibilityShared
	}

	row.OwnerPrincipal = owner
	row.OwnerPrincipalKind = kind
	row.AgentVisibility = visibility
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
	copyPrincipalMemoryFields(row, mem)

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
	copyPrincipalMemoryFields(row, mem)

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
	copyPrincipalMemoryFields(row, mem)
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
		copyPrincipalMemoryFields(row, mem)
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
// is a single WHERE-clause implementation path. The default predicate remains
// the legacy active-row predicate (status='active', no confidence floor).
func (s *MemoryStore) List(ctx context.Context, project string, limit int) ([]*models.Memory, error) {
	result, err := s.ListWithFilters(ctx, project, ListOptions{Limit: limit})
	if err != nil {
		return nil, fmt.Errorf("list memories for project %q: %w", project, err)
	}
	return result, nil
}

// ListOptions controls the optional filters applied by ListWithFilters.
// Zero-value opts replicate the WHERE clause of the legacy List method
// (status='active', no confidence floor), so callers delegate from List to
// ListWithFilters with default opts.
//
// T017a (engram vNext Milestone F TG3): store-level extension prerequisite.
type ListOptions struct {
	// ContentContains, when non-empty, adds a case-insensitive content
	// substring predicate. Callers may still apply richer tag/content matching in
	// memory, but recall(action="search") can keep its content-only predicate in
	// SQL instead of scanning every row.
	ContentContains string
	// ConfidenceMin, when > 0, adds WHERE confidence >= ConfidenceMin.
	// Default 0.0 means no confidence filter applied.
	ConfidenceMin float64
	// IncludeSuperseded, when true, relaxes the status filter to
	// WHERE status IN ('active','superseded'). Default false means
	// only 'active' rows are returned (same as legacy List).
	IncludeSuperseded bool
	// IncludeExpired relaxes the valid_until predicate only for explicit
	// ID-bounded principal-memory projections so temporal provenance can include
	// expired source memories without widening ordinary recall.
	IncludeExpired bool
	// OwnerPrincipal, when non-empty, restricts rows to memories attributed to
	// the named principal before LIMIT/OFFSET are applied.
	OwnerPrincipal string
	// OwnerPrincipalKind, when non-empty, restricts rows to human/agent/service
	// ownership kind. Callers that set OwnerPrincipal should normally set this too.
	OwnerPrincipalKind string
	// AgentVisibility, when non-empty, restricts rows to private/shared agent
	// visibility at SQL time.
	AgentVisibility string
	// Domain, when non-empty, restricts rows to a principal-memory domain.
	Domain string
	// Limit caps the number of returned rows. Values <= 0 default to 50.
	Limit int
	// Offset skips rows after SQL predicates are applied. Values < 0 default to
	// 0. This lets callers combine ListWithFilters with visibility backfill loops
	// without falling back to the active-only ListWithOffset seam.
	Offset int
	// IDs, when non-empty, restricts rows to the given memory IDs via
	// WHERE id IN (...). This is an ADDITIVE predicate: it composes with, and
	// never replaces, the owner/kind/visibility/domain access-policy filters, so
	// a by-id fetch still honours NFR-1 principal gating. Used by the experience
	// detail-by-id path so a specific memory:<id> lookup fetches that exact row
	// directly instead of scanning the newest-N projection.
	IDs []int64
	// ContentContainsAny, when non-empty, adds a grouped OR of case-insensitive
	// content substring predicates (one LIKE per term). This narrows the DB set
	// by relevance without the full-phrase ContentContains cliff (which dropped
	// rows whose content did not contain the exact query phrase) and without the
	// recency cliff of an unfiltered newest-N fetch. ADDITIVE: it never replaces
	// access-policy predicates.
	ContentContainsAny []string
}

// ListWithFilters returns memories for the given project with optional filters
// applied at the SQL layer. This is the TG3 store-level extension:
//
//   - IncludeSuperseded=false (default): WHERE status='active'.
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

	q := baseMemoryListQuery(s.db.WithContext(ctx)).
		Where("project = ?", project)

	result, err := findMemoryRows(q, opts)
	if err != nil {
		return nil, fmt.Errorf("list memories with filters for project %q: %w", project, err)
	}
	return result, nil
}

// ListPrincipalMemory returns rows using the principal-memory query seam.
// Unlike ListWithFilters, project is optional so cross-project principal views
// can be built without weakening the legacy ListWithFilters(project!="") guard.
//
// IncludeExpired is only honored for explicit ID-bounded projections
// (opts.IncludeExpired && len(opts.IDs) > 0); ordinary principal-memory recall
// stays on the current-validity predicate.
func (s *MemoryStore) ListPrincipalMemory(ctx context.Context, project string, opts ListOptions) ([]*models.Memory, error) {
	owner := strings.TrimSpace(opts.OwnerPrincipal)
	if strings.TrimSpace(project) == "" && owner == "" {
		return nil, fmt.Errorf("owner_principal must not be empty when project is empty")
	}

	q := basePrincipalMemoryQuery(s.db.WithContext(ctx), opts)
	if project = strings.TrimSpace(project); project != "" {
		q = q.Where("project = ?", project)
	}

	result, err := findMemoryRows(q, opts)
	if err != nil {
		if project == "" {
			return nil, fmt.Errorf("list principal memories: %w", err)
		}
		return nil, fmt.Errorf("list principal memories for project %q: %w", project, err)
	}
	return result, nil
}

func baseMemoryListQuery(q *gorm.DB) *gorm.DB {
	return applyCurrentMemoryValidity(q.Where("deleted_at IS NULL"))
}

func basePrincipalMemoryQuery(q *gorm.DB, opts ListOptions) *gorm.DB {
	q = q.Where("deleted_at IS NULL")
	if opts.IncludeExpired && len(opts.IDs) > 0 {
		return q.Where("valid_from IS NULL OR valid_from <= NOW()")
	}
	return applyCurrentMemoryValidity(q)
}

func applyCurrentMemoryValidity(q *gorm.DB) *gorm.DB {
	return q.
		Where("valid_from IS NULL OR valid_from <= NOW()").
		Where("valid_until IS NULL OR valid_until >= NOW()")
}

func findMemoryRows(q *gorm.DB, opts ListOptions) ([]*models.Memory, error) {
	q = applyMemoryListOptions(q, opts)

	var rows []Memory
	err := q.Order("created_at DESC, id DESC").
		Limit(normalizeMemoryListLimit(opts.Limit)).
		Offset(normalizeMemoryListOffset(opts.Offset)).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]*models.Memory, len(rows))
	for i := range rows {
		result[i] = memoryRowToModel(&rows[i])
	}
	return result, nil
}

func applyMemoryListOptions(q *gorm.DB, opts ListOptions) *gorm.DB {
	if opts.IncludeSuperseded {
		q = q.Where("status IN ('active','superseded')")
	} else {
		q = q.Where("status = 'active'")
	}
	if opts.ConfidenceMin > 0 {
		q = q.Where("confidence >= ?", opts.ConfidenceMin)
	}
	if content := strings.TrimSpace(opts.ContentContains); content != "" {
		q = q.Where("LOWER(content) LIKE ? ESCAPE '\\'", "%"+escapeSQLLike(strings.ToLower(content))+"%")
	}
	if len(opts.ContentContainsAny) > 0 {
		terms := make([]string, 0, len(opts.ContentContainsAny))
		args := make([]interface{}, 0, len(opts.ContentContainsAny))
		for _, term := range opts.ContentContainsAny {
			t := strings.TrimSpace(term)
			if t == "" {
				continue
			}
			terms = append(terms, "LOWER(content) LIKE ? ESCAPE '\\'")
			args = append(args, "%"+escapeSQLLike(strings.ToLower(t))+"%")
		}
		if len(terms) > 0 {
			q = q.Where(strings.Join(terms, " OR "), args...)
		}
	}
	if len(opts.IDs) > 0 {
		q = q.Where("id IN ?", opts.IDs)
	}
	if owner := strings.TrimSpace(opts.OwnerPrincipal); owner != "" {
		q = q.Where("owner_principal = ?", owner)
	}
	if kind := strings.TrimSpace(strings.ToLower(opts.OwnerPrincipalKind)); kind != "" {
		q = q.Where("owner_principal_kind = ?", kind)
	}
	if visibility := strings.TrimSpace(opts.AgentVisibility); visibility != "" {
		q = q.Where("agent_visibility = ?", visibility)
	}
	if domain := strings.TrimSpace(opts.Domain); domain != "" {
		q = q.Where("domain = ?", domain)
	}
	return q
}

func normalizeMemoryListLimit(limit int) int {
	const (
		defaultMemoryListLimit = 50
		maxMemoryListLimit     = 1000
	)
	if limit <= 0 {
		return defaultMemoryListLimit
	}
	if limit > maxMemoryListLimit {
		return maxMemoryListLimit
	}
	return limit
}

func normalizeMemoryListOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

func escapeSQLLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
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

// BatchIncrementInjected atomically increments injection_count for the given memory
// IDs in a single SQL statement. injection_count feeds the citation-rate denominator
// (citation_count / injection_count). Called fire-and-forget from the injection path
// so the citation feedback loop has a denominator regardless of which response
// strategy ran. It does NOT touch ts_beta: the Thompson "uncited" prior is managed
// separately at session-end by BatchIncrementUncited (cited memories take ts_alpha
// via BatchIncrementCited instead) — incrementing ts_beta here too would double-count.
// A nil/empty id slice is a no-op.
func (s *MemoryStore) BatchIncrementInjected(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Exec(
		"UPDATE memories SET injection_count = injection_count + 1, updated_at = now() WHERE id = ANY(?) AND deleted_at IS NULL",
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
// importanceFactor (rank-6) scales the outcome-sensitivity of the importance_base citation
// bump. The unscaled target growth is importance_base * ln(2 + citation_count); this scales the
// growth ABOVE the current base by importanceFactor, so a successful session promotes a cited
// memory harder for future injection (importance_base feeds ListForInjection ordering) than a
// failed one. importanceFactor == 1.0 reduces the expression to the historical formula exactly
// (base + (base*ln - base)*1 = base*ln), so existing/default-outcome behavior is unchanged; 0.0
// leaves importance_base at its current value. GREATEST keeps the result monotonic-up (no
// decrement), so this adds outcome sensitivity without permanent negative reinforcement.
func (s *MemoryStore) BatchIncrementCitedN(ctx context.Context, ids []int64, n, importanceFactor float64) error {
	return s.db.WithContext(ctx).Exec(
		`UPDATE memories SET
			ts_alpha = ts_alpha + ? / (1.0 + consecutive_citation_count * 0.1),
			citation_count = citation_count + 1,
			consecutive_citation_count = consecutive_citation_count + 1,
			importance_base = LEAST(1.0, GREATEST(importance_base,
				importance_base + (importance_base * ln(2.0 + citation_count) - importance_base) * ?)),
			updated_at = now()
		WHERE id = ANY(?)`,
		n, importanceFactor, pq.Array(ids),
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
		OwnerPrincipal:           row.OwnerPrincipal,
		OwnerPrincipalKind:       row.OwnerPrincipalKind,
		AgentVisibility:          row.AgentVisibility,
		Domain:                   row.Domain,
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

// SearchMetaMemoryTagPrefixIDs returns content-free candidate IDs for S2's tag-prefix leg.
// The SQL predicates are applied before LIMIT so hidden/newer rows cannot truncate older
// visible candidates.
func (s *MemoryStore) SearchMetaMemoryTagPrefixIDs(ctx context.Context, project, prefix string, opts ListOptions, limit int) ([]int64, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("project must not be empty")
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil, fmt.Errorf("prefix must not be empty")
	}
	prefixPattern := escapeSQLLike(strings.ToLower(prefix)) + "%"
	var ids []int64
	err := applyMemoryListOptions(
		basePrincipalMemoryQuery(s.db.WithContext(ctx).Model(&Memory{}), opts).Where("project = ?", project),
		opts,
	).
		Where(`EXISTS (SELECT 1 FROM jsonb_array_elements_text(tags) AS tag WHERE LOWER(tag) LIKE ? ESCAPE '\')`, prefixPattern).
		Order("created_at DESC, id DESC").
		Limit(normalizeMetaMemoryLimit(limit)).
		Pluck("id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("search meta-memory tag-prefix ids project=%q prefix=%q: %w", project, prefix, err)
	}
	return ids, nil
}

// SearchMetaMemoryFTSIDs returns content-free candidate IDs for S2's FTS leg.
// It intentionally over-fetches from SearchFTS before applying principal/domain
// filters so newer hidden rows do not truncate older visible matches.
func (s *MemoryStore) SearchMetaMemoryFTSIDs(ctx context.Context, project, query string, opts ListOptions, limit int) ([]int64, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("project must not be empty")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query must not be empty")
	}
	metaLimit := normalizeMetaMemoryLimit(limit)
	batchLimit := normalizeMetaMemoryProbeLimit(limit)
	scanBudget := normalizeMetaMemoryFTSScanBudget(limit)
	ids := make([]int64, 0, metaLimit)
	variants := searchFTSQueryVariants(query)
	for i, variant := range variants {
		sawRows := false
		for offset := 0; len(ids) < metaLimit && offset < scanBudget; {
			pageLimit := batchLimit
			if remaining := scanBudget - offset; remaining < pageLimit {
				pageLimit = remaining
			}
			if pageLimit <= 0 {
				break
			}
			batch, err := s.searchFTSPage(ctx, project, variant, pageLimit, offset)
			if err != nil {
				return nil, fmt.Errorf("search meta-memory fts ids project=%q query=%q: %w", project, query, err)
			}
			if len(batch) == 0 {
				break
			}
			sawRows = true
			for _, mem := range batch {
				if !metaMemoryMatchesOptions(mem, opts) {
					continue
				}
				ids = append(ids, mem.ID)
				if len(ids) == metaLimit {
					break
				}
			}
			if len(batch) < pageLimit {
				break
			}
			offset += len(batch)
		}
		if sawRows || i == len(variants)-1 {
			break
		}
	}
	return ids, nil
}

// GetMetaMemoryByIDs returns the content-free metadata projection for the given IDs,
// preserving the caller's requested order.
func (s *MemoryStore) GetMetaMemoryByIDs(ctx context.Context, project string, ids []int64) ([]MetaMemoryRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("project must not be empty")
	}
	var rows []metaMemorySelectRow
	err := s.db.WithContext(ctx).Raw(`
		SELECT id,
		       btrim(split_part(content, E'\n', 1)) AS title,
		       tags,
		       created_at,
		       updated_at
		FROM memories
		WHERE id = ANY(?)
		  AND project = ?
		  AND status = 'active'
		  AND deleted_at IS NULL
		  AND (valid_from IS NULL OR valid_from <= NOW())
		  AND (valid_until IS NULL OR valid_until >= NOW())
	`, pq.Array(ids), project).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("get meta-memory by ids project=%q: %w", project, err)
	}
	byID := make(map[int64]MetaMemoryRecord, len(rows))
	for _, row := range metaMemoryRowsToRecords(rows) {
		byID[row.ID] = row
	}
	result := make([]MetaMemoryRecord, 0, len(ids))
	for _, id := range ids {
		if row, ok := byID[id]; ok {
			result = append(result, row)
		}
	}
	return result, nil
}

// QueryMetaIndex runs the content-free S2 tag/FTS fusion query and returns
// bounded metadata-only hits.
func (s *MemoryStore) QueryMetaIndex(ctx context.Context, query MetaIndexQuery) ([]MetaIndexHit, error) {
	project := strings.TrimSpace(query.Project)
	if project == "" {
		return nil, fmt.Errorf("project must not be empty")
	}
	limit := normalizeMetaMemoryLimit(query.Limit)
	opts := ListOptions{
		OwnerPrincipal:     query.OwnerPrincipal,
		OwnerPrincipalKind: query.OwnerPrincipalKind,
		AgentVisibility:    query.AgentVisibility,
		Domain:             query.Domain,
	}

	tagIDs := make([]int64, 0, limit)
	hasTagQuery := false
	for _, tag := range query.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		hasTagQuery = true
		ids, err := s.SearchMetaMemoryTagPrefixIDs(ctx, project, tag, opts, limit)
		if err != nil {
			return nil, err
		}
		tagIDs = appendUniqueMetaIDs(tagIDs, ids)
	}

	textQuery := strings.TrimSpace(query.Query)
	hasTextQuery := textQuery != ""
	ftsIDs := make([]int64, 0, limit)
	if hasTextQuery {
		ids, err := s.SearchMetaMemoryFTSIDs(ctx, project, textQuery, opts, limit)
		if err != nil {
			return nil, err
		}
		ftsIDs = appendUniqueMetaIDs(ftsIDs, ids)
	}

	if !hasTagQuery && !hasTextQuery {
		return nil, fmt.Errorf("query or tags must not be empty")
	}
	if len(tagIDs) == 0 && len(ftsIDs) == 0 {
		return []MetaIndexHit{}, nil
	}

	ordered := metaIndexRRF(tagIDs, ftsIDs, 60)
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	records, err := s.GetMetaMemoryByIDs(ctx, project, ordered)
	if err != nil {
		return nil, err
	}
	scores := metaIndexScores(tagIDs, ftsIDs)
	tagSet := metaIDSet(tagIDs)
	ftsSet := metaIDSet(ftsIDs)
	hits := make([]MetaIndexHit, 0, len(records))
	for _, record := range records {
		hits = append(hits, MetaIndexHit{
			ID:        record.ID,
			Project:   project,
			Title:     record.Title,
			Tags:      cloneMetaTags(record.Tags),
			CreatedAt: record.CreatedAt,
			UpdatedAt: record.UpdatedAt,
			Score:     float32(scores[record.ID]),
			Source:    "s2.meta_index",
			Reason:    metaIndexReason(record.ID, tagSet, ftsSet),
		})
	}
	return hits, nil
}

func appendUniqueMetaIDs(dst, src []int64) []int64 {
	seen := metaIDSet(dst)
	for _, id := range src {
		if seen[id] {
			continue
		}
		dst = append(dst, id)
		seen[id] = true
	}
	return dst
}

func metaIDSet(ids []int64) map[int64]bool {
	set := make(map[int64]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func metaIndexScores(tagIDs, ftsIDs []int64) map[int64]float64 {
	scores := make(map[int64]float64, len(tagIDs)+len(ftsIDs))
	for rank, id := range tagIDs {
		scores[id] += 1.0 / float64(rank+61)
	}
	for rank, id := range ftsIDs {
		scores[id] += 1.0 / float64(rank+61)
	}
	return scores
}

func metaIndexRRF(tagIDs, ftsIDs []int64, k int) []int64 {
	if k <= 0 {
		k = 60
	}
	type scoredID struct {
		id    int64
		score float64
		best  int
	}
	scores := make(map[int64]float64, len(tagIDs)+len(ftsIDs))
	bestRank := make(map[int64]int, len(tagIDs)+len(ftsIDs))
	initBest := func(id int64, rank int) {
		if best, ok := bestRank[id]; !ok || rank < best {
			bestRank[id] = rank
		}
	}
	for rank, id := range tagIDs {
		scores[id] += 1.0 / float64(rank+k+1)
		initBest(id, rank)
	}
	for rank, id := range ftsIDs {
		scores[id] += 1.0 / float64(rank+k+1)
		initBest(id, rank)
	}
	merged := make([]scoredID, 0, len(scores))
	for id, score := range scores {
		merged = append(merged, scoredID{id: id, score: score, best: bestRank[id]})
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].score != merged[j].score {
			return merged[i].score > merged[j].score
		}
		if merged[i].best != merged[j].best {
			return merged[i].best < merged[j].best
		}
		return merged[i].id < merged[j].id
	})
	ordered := make([]int64, len(merged))
	for i := range merged {
		ordered[i] = merged[i].id
	}
	return ordered
}

func metaIndexReason(id int64, inTags, inFTS map[int64]bool) string {
	switch {
	case inTags[id] && inFTS[id]:
		return "tag_prefix+fts"
	case inTags[id]:
		return "tag_prefix"
	case inFTS[id]:
		return "fts"
	default:
		return "s2.meta_index"
	}
}

// tokenizeFTSTerms splits an FTS query into terms for the OR-fallback while
// preserving "double quoted phrases" as single terms and dropping any literal
// boolean operators the user typed. A naive strings.Fields split breaks both:
// `"mcp launcher" install` becomes `"mcp`, `launcher"`, `install` (the phrase is
// shattered and the stray quotes corrupt the rebuilt query), and `a OR b`
// becomes `a`, `OR`, `b` which re-joins to `a OR OR OR b`. Returned terms keep
// their surrounding quotes so websearch_to_tsquery still parses each phrase as a
// phrase in the OR pass.
func tokenizeFTSTerms(query string) []string {
	var terms []string
	var current strings.Builder
	inQuotes := false
	flush := func() {
		if current.Len() == 0 {
			return
		}
		term := current.String()
		current.Reset()
		// Drop literal boolean operators (case-insensitive) that the user typed —
		// the fallback supplies its own " OR " joins, so a bare OR/AND/NOT term
		// would produce malformed tsquery input like `a OR OR OR b`.
		switch strings.ToUpper(term) {
		case "OR", "AND", "NOT":
			return
		}
		terms = append(terms, term)
	}
	for _, r := range query {
		switch {
		case r == '"':
			current.WriteRune(r)
			inQuotes = !inQuotes
		case (r == ' ' || r == '\t' || r == '\n' || r == '\r') && !inQuotes:
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return terms
}

// hasNegationTerm reports whether any tokenized term is a websearch exclusion
// (`-term`). Such terms make the OR-fallback semantically wrong (see SearchFTS
// negation guard). A quoted phrase whose inner text starts with `-` is NOT an
// exclusion (the leading quote is the first rune), so the check looks past a
// leading quote only for the bare-term case.
func hasNegationTerm(terms []string) bool {
	for _, t := range terms {
		if strings.HasPrefix(t, "-") && len(t) > 1 {
			return true
		}
	}
	return false
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
	limit = normalizeSearchFTSLimit(limit)
	variants := searchFTSQueryVariants(query)
	for i, variant := range variants {
		rows, err := s.searchFTSPage(ctx, project, variant, limit, 0)
		if err != nil {
			if i > 0 {
				return nil, fmt.Errorf("SearchFTS project=%q (OR fallback): %w", project, err)
			}
			return nil, fmt.Errorf("SearchFTS project=%q: %w", project, err)
		}
		if len(rows) > 0 || i == len(variants)-1 {
			return rows, nil
		}
	}
	return []*models.Memory{}, nil
}

func normalizeSearchFTSLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func searchFTSQueryVariants(query string) []string {
	variants := []string{query}
	terms := tokenizeFTSTerms(query)
	if len(terms) >= 2 && !hasNegationTerm(terms) {
		orQuery := strings.Join(terms, " OR ")
		if orQuery != query {
			variants = append(variants, orQuery)
		}
	}
	return variants
}

func (s *MemoryStore) searchFTSPage(ctx context.Context, project, query string, limit int, offset int) ([]*models.Memory, error) {
	if project == "" {
		return nil, fmt.Errorf("SearchFTS: project must not be empty")
	}
	if query == "" {
		return nil, fmt.Errorf("SearchFTS: query must not be empty")
	}
	limit = normalizeSearchFTSLimit(limit)
	if offset < 0 {
		offset = 0
	}
	const ftsQuerySQL = `
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
		             COALESCE(NULLIF(parsed.wsq, ''::tsquery), parsed.ptq)) DESC,
		         m.created_at DESC,
		         m.id DESC
		LIMIT ?
		OFFSET ?
	`
	var rows []Memory
	err := s.db.WithContext(ctx).Raw(ftsQuerySQL, query, query, project, limit, offset).Scan(&rows).Error
	if err != nil {
		return nil, err
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

// RestoreRaw performs a full field restore of a memory row for rollback operations.
//
// Unlike Update (which only touches 4 fields and bumps version), RestoreRaw writes
// back all fields captured in the before_state JSONB during snapshot capture.
// It operates directly on the memories table, including clearing deleted_at when
// the pre-op row was not deleted.
//
// RestoreRaw is intended exclusively for Rollback (internal/bulkops) — do not use
// for normal update workflows.
func (s *MemoryStore) RestoreRaw(ctx context.Context, mem *models.Memory) error {
	if mem == nil {
		return fmt.Errorf("restoreRaw: memory must not be nil")
	}
	if mem.ID == 0 {
		return fmt.Errorf("restoreRaw: memory ID must be non-zero")
	}
	updates := map[string]any{
		"content":          mem.Content,
		"tags":             models.JSONStringArray(mem.Tags),
		"source_agent":     mem.SourceAgent,
		"edited_by":        mem.EditedBy,
		"status":           mem.Status,
		"tier":             mem.Tier,
		"epistemic_type":   mem.EpistemicType,
		"defeasibility":    mem.Defeasibility,
		"promotion_target": mem.PromotionTarget,
		"privacy_scope":    mem.PrivacyScope,
		"importance_base":  mem.ImportanceBase,
		"ts_alpha":         mem.TsAlpha,
		"ts_beta":          mem.TsBeta,
		"confidence":       mem.Confidence,
		"stability":        mem.Stability,
		"retrievability":   mem.Retrievability,
		"supersedes_id":    mem.SupersedesID,
		"superseded_by":    mem.SupersededBy,
		"deleted_at":       mem.DeletedAt,
		"updated_at":       mem.UpdatedAt,
		// version is deliberately not restored — keep current row version so conflicts are auditable.
	}
	result := s.db.WithContext(ctx).
		Unscoped().
		Model(&Memory{}).
		Where("id = ?", mem.ID).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("restoreRaw memory id=%d: %w", mem.ID, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("restoreRaw memory id=%d: row not found", mem.ID)
	}
	return nil
}

// RestoreRawTx is the transactional variant of RestoreRaw.
// It operates on the provided *gorm.DB transaction (tx) instead of s.db.
// Use inside db.Transaction closures where all rollback mutations must be atomic.
func (s *MemoryStore) RestoreRawTx(ctx context.Context, tx *gorm.DB, mem *models.Memory) error {
	if mem == nil {
		return fmt.Errorf("restoreRawTx: memory must not be nil")
	}
	if mem.ID == 0 {
		return fmt.Errorf("restoreRawTx: memory ID must be non-zero")
	}
	updates := map[string]any{
		"content":          mem.Content,
		"tags":             models.JSONStringArray(mem.Tags),
		"source_agent":     mem.SourceAgent,
		"edited_by":        mem.EditedBy,
		"status":           mem.Status,
		"tier":             mem.Tier,
		"epistemic_type":   mem.EpistemicType,
		"defeasibility":    mem.Defeasibility,
		"promotion_target": mem.PromotionTarget,
		"privacy_scope":    mem.PrivacyScope,
		"importance_base":  mem.ImportanceBase,
		"ts_alpha":         mem.TsAlpha,
		"ts_beta":          mem.TsBeta,
		"confidence":       mem.Confidence,
		"stability":        mem.Stability,
		"retrievability":   mem.Retrievability,
		"supersedes_id":    mem.SupersedesID,
		"superseded_by":    mem.SupersededBy,
		"deleted_at":       mem.DeletedAt,
		"updated_at":       mem.UpdatedAt,
	}
	result := tx.WithContext(ctx).
		Unscoped().
		Model(&Memory{}).
		Where("id = ?", mem.ID).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("restoreRawTx memory id=%d: %w", mem.ID, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("restoreRawTx memory id=%d: row not found", mem.ID)
	}
	return nil
}

// HardDeleteTx permanently removes a memory row within a transaction.
// Used by rollback to remove memories that were CREATED by a bulk_promote op
// (EntryKindDelete rows) — these have no pre-op state to restore.
// candidate.promoted_memory_id is SET NULL by the FK constraint (ON DELETE SET NULL),
// consistent with EC-F4, so the candidate row naturally returns to a restorable state.
func (s *MemoryStore) HardDeleteTx(ctx context.Context, tx *gorm.DB, id int64) error {
	if id == 0 {
		return fmt.Errorf("hardDeleteTx: memory ID must be non-zero")
	}
	result := tx.WithContext(ctx).
		Unscoped().
		Delete(&Memory{}, "id = ?", id)
	if result.Error != nil {
		return fmt.Errorf("hardDeleteTx memory id=%d: %w", id, result.Error)
	}
	// RowsAffected == 0 is not an error — the memory may have already been deleted.
	return nil
}

// GetDB returns the underlying *gorm.DB, used by the rollback path to open a transaction
// that spans MemoryStore + SnapshotStore mutations atomically.
func (s *MemoryStore) GetDB() *gorm.DB {
	return s.db
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
