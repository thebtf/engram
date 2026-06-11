// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/thebtf/engram/pkg/models"
)

// ErrInvalidTransition is returned when a state-machine transition is not permitted.
var ErrInvalidTransition = errors.New("invalid_transition")

// candidateRow is the GORM model for the crystallization_candidates table.
// It mirrors the schema created by migration 132.
type candidateRow struct {
	CreatedAt               time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt               time.Time  `gorm:"column:updated_at;autoUpdateTime"`
	ReviewAfter             *time.Time `gorm:"column:review_after"`
	SourceSessionID         string     `gorm:"column:source_session_id;not null;default:''"`
	ProposedContent         string     `gorm:"column:proposed_content;not null"`
	ProposedTier            string     `gorm:"column:proposed_tier;not null;default:'episodic'"`
	ProposedEpistemicType   string     `gorm:"column:proposed_epistemic_type;not null;default:'observation'"`
	ProposedPromotionTarget string     `gorm:"column:proposed_promotion_target;not null;default:'none'"`
	EvidenceHandles         JSONRaw    `gorm:"column:evidence_handles;type:jsonb;not null;default:'[]'"`
	PrivacyScope            string     `gorm:"column:privacy_scope;not null;default:'project'"`
	Status                  string     `gorm:"column:status;not null;default:'pending'"`
	Fingerprint             string     `gorm:"column:fingerprint;not null;default:''"`
	AffectedProjects        pq.StringArray `gorm:"column:affected_projects;type:text[]"`
	PromotedMemoryID        *int64     `gorm:"column:promoted_memory_id"`
	ID                      int64      `gorm:"primaryKey;autoIncrement"`
	Confidence              float32    `gorm:"column:confidence;not null;default:0.5"`
	RecurrenceCount         int        `gorm:"column:recurrence_count;not null;default:1"`
}

func (candidateRow) TableName() string { return "crystallization_candidates" }

// JSONRaw is a []byte type that stores/retrieves raw JSON from GORM.
type JSONRaw []byte

func (j JSONRaw) Value() (interface{}, error) {
	if len(j) == 0 {
		return "[]", nil
	}
	return string(j), nil
}

func (j *JSONRaw) Scan(src interface{}) error {
	if src == nil {
		*j = []byte("[]")
		return nil
	}
	switch v := src.(type) {
	case []byte:
		*j = append((*j)[:0], v...)
	case string:
		*j = []byte(v)
	}
	return nil
}

// toDomainCandidate converts a candidateRow to a models.CrystallizationCandidate.
func toDomainCandidate(r *candidateRow) *models.CrystallizationCandidate {
	c := &models.CrystallizationCandidate{
		ID:                      r.ID,
		SourceSessionID:         r.SourceSessionID,
		ProposedContent:         r.ProposedContent,
		ProposedTier:            r.ProposedTier,
		ProposedEpistemicType:   r.ProposedEpistemicType,
		ProposedPromotionTarget: r.ProposedPromotionTarget,
		PrivacyScope:            r.PrivacyScope,
		Status:                  models.CandidateStatus(r.Status),
		Fingerprint:             r.Fingerprint,
		PromotedMemoryID:        r.PromotedMemoryID,
		Confidence:              r.Confidence,
		RecurrenceCount:         r.RecurrenceCount,
		CreatedAt:               r.CreatedAt,
		UpdatedAt:               r.UpdatedAt,
		ReviewAfter:             r.ReviewAfter,
		AffectedProjects:        []string(r.AffectedProjects),
	}
	return c
}

// fromDomainCandidate converts a models.CrystallizationCandidate to a candidateRow.
func fromDomainCandidate(c *models.CrystallizationCandidate) *candidateRow {
	r := &candidateRow{
		SourceSessionID:         c.SourceSessionID,
		ProposedContent:         c.ProposedContent,
		ProposedTier:            c.ProposedTier,
		ProposedEpistemicType:   c.ProposedEpistemicType,
		ProposedPromotionTarget: c.ProposedPromotionTarget,
		PrivacyScope:            c.PrivacyScope,
		Status:                  string(c.Status),
		Fingerprint:             c.Fingerprint,
		PromotedMemoryID:        c.PromotedMemoryID,
		Confidence:              c.Confidence,
		RecurrenceCount:         c.RecurrenceCount,
		ReviewAfter:             c.ReviewAfter,
		AffectedProjects:        pq.StringArray(c.AffectedProjects),
	}
	if r.Status == "" {
		r.Status = "pending"
	}
	return r
}

// CandidateStore provides CRUD and state-machine operations for crystallization_candidates.
type CandidateStore struct {
	db         *gorm.DB
	auditStore *AuditStore
}

// NewCandidateStore creates a new CandidateStore.
func NewCandidateStore(db *gorm.DB, auditStore *AuditStore) *CandidateStore {
	return &CandidateStore{db: db, auditStore: auditStore}
}

// Create inserts a new crystallization candidate.
// Returns ErrDuplicateFingerprint if a pending candidate with the same fingerprint exists
// (enforced by the partial unique index idx_candidates_fingerprint_pending).
func (s *CandidateStore) Create(ctx context.Context, c *models.CrystallizationCandidate) (*models.CrystallizationCandidate, error) {
	row := fromDomainCandidate(c)
	result := s.db.WithContext(ctx).Create(row)
	if result.Error != nil {
		return nil, fmt.Errorf("candidate_store create: %w", result.Error)
	}
	return toDomainCandidate(row), nil
}

// Get retrieves a candidate by ID. Returns gorm.ErrRecordNotFound if absent.
func (s *CandidateStore) Get(ctx context.Context, id int64) (*models.CrystallizationCandidate, error) {
	var row candidateRow
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, fmt.Errorf("candidate_store get %d: %w", id, err)
	}
	return toDomainCandidate(&row), nil
}

// ListByStatus returns candidates filtered by project (via affected_projects array) and status.
// project="" returns candidates regardless of project.
// limit <= 0 defaults to 50.
func (s *CandidateStore) ListByStatus(ctx context.Context, project string, status models.CandidateStatus, limit int) ([]*models.CrystallizationCandidate, error) {
	if limit <= 0 {
		limit = 50
	}
	q := s.db.WithContext(ctx).
		Where("status = ?", string(status)).
		Order("created_at DESC").
		Limit(limit)
	if project != "" {
		q = q.Where("? = ANY(affected_projects)", project)
	}
	var rows []candidateRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("candidate_store list: %w", err)
	}
	out := make([]*models.CrystallizationCandidate, len(rows))
	for i := range rows {
		out[i] = toDomainCandidate(&rows[i])
	}
	return out, nil
}

// ListExpiredPending returns pending candidates where review_after < now AND recurrence_count < threshold.
// Used by the decay batch in T028.
func (s *CandidateStore) ListExpiredPending(ctx context.Context, threshold int, batchSize int) ([]*models.CrystallizationCandidate, error) {
	if batchSize <= 0 {
		batchSize = 100
	}
	now := time.Now().UTC()
	var rows []candidateRow
	err := s.db.WithContext(ctx).
		Where("status = 'pending' AND review_after < ? AND recurrence_count < ?", now, threshold).
		Limit(batchSize).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("candidate_store list_expired: %w", err)
	}
	out := make([]*models.CrystallizationCandidate, len(rows))
	for i := range rows {
		out[i] = toDomainCandidate(&rows[i])
	}
	return out, nil
}

// validTransitions defines which status transitions are legal.
// key = current status → set of valid next statuses.
var validTransitions = map[models.CandidateStatus]map[models.CandidateStatus]bool{
	models.CandidateStatusPending: {
		models.CandidateStatusPromoted:   true,
		models.CandidateStatusRejected:   true,
		models.CandidateStatusSuperseded: true,
		models.CandidateStatusDecayed:    true,
	},
	// Terminal states: no further transitions allowed.
	models.CandidateStatusPromoted:   {},
	models.CandidateStatusRejected:   {},
	models.CandidateStatusSuperseded: {},
	models.CandidateStatusDecayed:    {},
}

// transitionStatus performs a state-machine transition on a candidate using
// SELECT ... FOR UPDATE to serialize concurrent transitions (EC-F10).
// On success it writes an audit_log entry with the given action.
// auditNote is appended to the audit Reason when non-empty (used by reject to record caller reason).
// Returns ErrInvalidTransition when the transition is not permitted.
func (s *CandidateStore) transitionStatus(
	ctx context.Context,
	id int64,
	newStatus models.CandidateStatus,
	auditAction string,
	extraUpdates map[string]interface{},
	auditNote string,
) (*models.CrystallizationCandidate, error) {
	var result *models.CrystallizationCandidate

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// SELECT ... FOR UPDATE — serialises concurrent transitions per EC-F10.
		var row candidateRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&row, id).Error; err != nil {
			return fmt.Errorf("candidate_store lock %d: %w", id, err)
		}

		current := models.CandidateStatus(row.Status)
		if !validTransitions[current][newStatus] {
			return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, current, newStatus)
		}

		updates := map[string]interface{}{
			"status":     string(newStatus),
			"updated_at": time.Now().UTC(),
		}
		for k, v := range extraUpdates {
			updates[k] = v
		}

		if err := tx.Model(&candidateRow{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return fmt.Errorf("candidate_store update %d: %w", id, err)
		}

		// Re-read after update for the returned value.
		if err := tx.First(&row, id).Error; err != nil {
			return fmt.Errorf("candidate_store re-read %d: %w", id, err)
		}
		result = toDomainCandidate(&row)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Audit log after successful transaction.
	// Run asynchronously — matches the runAuditAsync pattern — so that a slow or
	// unavailable audit backend does not block the MCP hot path.
	// Transitions are rare and audit ordering within a session is not a hard requirement.
	if s.auditStore != nil {
		reason := fmt.Sprintf("candidate %d transitioned to %s", id, newStatus)
		if auditNote != "" {
			reason = reason + ": " + auditNote
		}
		entry := AuditLogEntry{
			Action:          auditAction,
			Actor:           "system",
			SourceSessionID: result.SourceSessionID,
			Reason:          reason,
		}
		auditStore := s.auditStore
		go func() {
			defer func() {
				if r := recover(); r != nil {
					// Non-fatal panic in audit goroutine — swallow silently.
					_ = r
				}
			}()
			auditCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if logErr := auditStore.Log(auditCtx, entry); logErr != nil {
				// Non-fatal: audit failure does not roll back the state change.
				_ = logErr
			}
		}()
	}
	return result, nil
}

// TransitionToPromoted transitions a pending candidate to promoted.
// promotedMemoryID must be the ID of the newly created Memory row.
// Returns ErrInvalidTransition if the candidate is not pending.
func (s *CandidateStore) TransitionToPromoted(ctx context.Context, id int64, promotedMemoryID int64) (*models.CrystallizationCandidate, error) {
	return s.transitionStatus(ctx, id, models.CandidateStatusPromoted, "promote_candidate",
		map[string]interface{}{"promoted_memory_id": promotedMemoryID}, "")
}

// PromoteWithMemory atomically creates a memory row AND transitions the candidate to
// promoted within a single database transaction. This closes the partial-failure
// dual-write gap: if the memory insert succeeds but the transition fails (or vice
// versa), both writes are rolled back and the caller receives an error it can safely
// retry — TransitionToPromoted will reject an already-promoted candidate, so retry
// is idempotent after a genuine failure.
//
// Tx-seam choice: CandidateStore and MemoryStore share the same underlying *gorm.DB
// (both sourced from store.GetDB()); PromoteWithMemory opens a single transaction on
// that shared DB and calls the package-internal createMemoryWithLifecycleTx helper for
// the memory insert. This avoids circular imports and keeps all promotion logic within
// the gorm package, consistent with the layering used elsewhere.
func (s *CandidateStore) PromoteWithMemory(
	ctx context.Context,
	candidateID int64,
	mem *models.Memory,
) (*models.CrystallizationCandidate, *models.Memory, error) {
	if err := validateMemoryForCreate(mem); err != nil {
		return nil, nil, fmt.Errorf("promote_with_memory: invalid memory: %w", err)
	}

	var updatedCandidate *models.CrystallizationCandidate
	var createdMemory *models.Memory

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Step A: lock the candidate row to detect concurrent promotions.
		var row candidateRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&row, candidateID).Error; err != nil {
			return fmt.Errorf("promote_with_memory lock %d: %w", candidateID, err)
		}
		current := models.CandidateStatus(row.Status)
		if !validTransitions[current][models.CandidateStatusPromoted] {
			return fmt.Errorf("%w: %s → promoted", ErrInvalidTransition, current)
		}

		// Step B: create the memory within the same transaction.
		created, err := createMemoryWithLifecycleTx(ctx, tx, mem)
		if err != nil {
			return err
		}
		createdMemory = created

		// Step C: transition the candidate using the new memory ID.
		updates := map[string]interface{}{
			"status":             string(models.CandidateStatusPromoted),
			"promoted_memory_id": created.ID,
			"updated_at":         time.Now().UTC(),
		}
		if err := tx.Model(&candidateRow{}).Where("id = ?", candidateID).Updates(updates).Error; err != nil {
			return fmt.Errorf("promote_with_memory update %d: %w", candidateID, err)
		}
		if err := tx.First(&row, candidateID).Error; err != nil {
			return fmt.Errorf("promote_with_memory re-read %d: %w", candidateID, err)
		}
		updatedCandidate = toDomainCandidate(&row)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	// Audit log asynchronously — same pattern as transitionStatus.
	if s.auditStore != nil {
		entry := AuditLogEntry{
			Action:          "promote_candidate",
			Actor:           "system",
			SourceSessionID: updatedCandidate.SourceSessionID,
			Reason:          fmt.Sprintf("candidate %d promoted to memory %d", candidateID, createdMemory.ID),
		}
		auditStore := s.auditStore
		go func() {
			defer func() { recover() }() //nolint:errcheck
			auditCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = auditStore.Log(auditCtx, entry)
		}()
	}

	return updatedCandidate, createdMemory, nil
}

// TransitionToRejected transitions a pending candidate to rejected with a reason.
// The reason is recorded in the audit_log Reason field; proposed_content is preserved unchanged.
// Returns ErrInvalidTransition if the candidate is not pending.
func (s *CandidateStore) TransitionToRejected(ctx context.Context, id int64, reason string) (*models.CrystallizationCandidate, error) {
	return s.transitionStatus(ctx, id, models.CandidateStatusRejected, "reject_candidate", nil, reason)
}

// TransitionToSuperseded transitions a pending candidate to superseded.
// Returns ErrInvalidTransition if the candidate is not pending.
func (s *CandidateStore) TransitionToSuperseded(ctx context.Context, id int64) (*models.CrystallizationCandidate, error) {
	return s.transitionStatus(ctx, id, models.CandidateStatusSuperseded, "supersede_candidate", nil, "")
}

// TransitionToDecayed transitions a pending candidate to decayed (used by sleep cycle).
// Returns ErrInvalidTransition if the candidate is not pending.
func (s *CandidateStore) TransitionToDecayed(ctx context.Context, id int64) (*models.CrystallizationCandidate, error) {
	return s.transitionStatus(ctx, id, models.CandidateStatusDecayed, "decay_candidate", nil, "")
}

// GetByFingerprint looks up a pending candidate by fingerprint.
// Returns nil, nil when no pending candidate with that fingerprint exists.
func (s *CandidateStore) GetByFingerprint(ctx context.Context, fingerprint string) (*models.CrystallizationCandidate, error) {
	if fingerprint == "" {
		return nil, nil
	}
	var row candidateRow
	err := s.db.WithContext(ctx).
		Where("fingerprint = ? AND status = 'pending'", fingerprint).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("candidate_store get_by_fingerprint: %w", err)
	}
	return toDomainCandidate(&row), nil
}
