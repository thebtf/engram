// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/thebtf/engram/pkg/models"
)

type RuleTransitionRequest = models.RuleTransitionRequest

type RuleCandidateListParams struct {
	SourceProject string
	Status        models.RuleCandidateStatus
	Limit         int
}

type SnapshotRequest struct {
	SnapshotID  string
	OpType      string
	Actor       string
	BeforeState []byte
	AfterState  []byte
	Pinned      bool
}

type ruleCandidateRow struct {
	CreatedAt           time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt           time.Time  `gorm:"column:updated_at;autoUpdateTime"`
	ReviewAfter         *time.Time `gorm:"column:review_after"`
	LastEvaluatedAt     *time.Time `gorm:"column:last_evaluated_at"`
	SourceSignalType    string     `gorm:"column:source_signal_type;not null"`
	SourceSessionID     string     `gorm:"column:source_session_id"`
	SourceProject       string     `gorm:"column:source_project"`
	SourceActor         string     `gorm:"column:source_actor;not null"`
	ProposedContent     string     `gorm:"column:proposed_content;not null"`
	ProposedScope       string     `gorm:"column:proposed_scope;not null"`
	ProposedAudience    string     `gorm:"column:proposed_audience;not null"`
	ActivationPredicate JSONRaw    `gorm:"column:activation_predicate_json;type:jsonb;not null;default:'{}'"`
	EvidenceHandles     JSONRaw    `gorm:"column:evidence_handles_json;type:jsonb;not null;default:'[]'"`
	AntiCaptureStatus   string     `gorm:"column:anti_capture_status;not null"`
	AntiCaptureReason   string     `gorm:"column:anti_capture_reason"`
	ConflictStatus      string     `gorm:"column:conflict_status;not null"`
	Status              string     `gorm:"column:status;not null;default:'pending'"`
	Fingerprint         string     `gorm:"column:fingerprint;not null;default:''"`
	DecayPolicy         string     `gorm:"column:decay_policy;not null"`
	ID                  int64      `gorm:"primaryKey;autoIncrement"`
	Confidence          float64    `gorm:"column:confidence;not null;default:0"`
	RecurrenceCount     int        `gorm:"column:recurrence_count;not null;default:0"`
}

func (ruleCandidateRow) TableName() string { return "rule_candidates" }

type ruleFamilyRow struct {
	CreatedAt              time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt              time.Time `gorm:"column:updated_at;autoUpdateTime"`
	FamilyKey              string    `gorm:"column:family_key;not null;uniqueIndex"`
	CreatedFromCandidateID *int64    `gorm:"column:created_from_candidate_id"`
	ID                     int64     `gorm:"primaryKey;autoIncrement"`
}

func (ruleFamilyRow) TableName() string { return "rule_families" }

type ruleVersionRow struct {
	CreatedAt              time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt              time.Time  `gorm:"column:updated_at;autoUpdateTime"`
	LastEvaluatedAt        *time.Time `gorm:"column:last_evaluated_at"`
	EffectiveFrom          *time.Time `gorm:"column:effective_from"`
	EffectiveUntil         *time.Time `gorm:"column:effective_until"`
	ArchivedAt             *time.Time `gorm:"column:archived_at"`
	FamilyID               int64      `gorm:"column:family_id;not null"`
	SourceCandidateID      *int64     `gorm:"column:source_candidate_id"`
	ActiveBehavioralRuleID *int64     `gorm:"column:active_behavioral_rule_id"`
	Content                string     `gorm:"column:content;not null"`
	Summary                string     `gorm:"column:summary"`
	Scope                  string     `gorm:"column:scope;not null"`
	Owner                  string     `gorm:"column:owner;not null"`
	Audience               string     `gorm:"column:audience;not null"`
	ActivationPredicate    JSONRaw    `gorm:"column:activation_predicate_json;type:jsonb;not null;default:'{}'"`
	EvidenceHandles        JSONRaw    `gorm:"column:evidence_handles_json;type:jsonb;not null;default:'[]'"`
	State                  string     `gorm:"column:state;not null"`
	BudgetClass            string     `gorm:"column:budget_class;not null;default:'contextual'"`
	AntiCaptureStatus      string     `gorm:"column:anti_capture_status;not null"`
	ConflictStatus         string     `gorm:"column:conflict_status;not null"`
	DecayPolicy            string     `gorm:"column:decay_policy;not null"`
	SupersedesVersionID    *int64     `gorm:"column:supersedes_version_id"`
	ID                     int64      `gorm:"primaryKey;autoIncrement"`
	Priority               int        `gorm:"column:priority;not null;default:0"`
	Protected              bool       `gorm:"column:protected;not null;default:false"`
	Pinned                 bool       `gorm:"column:pinned;not null;default:false"`
}

func (ruleVersionRow) TableName() string { return "rule_versions" }

type ruleTransitionLogRow struct {
	CreatedAt       time.Time `gorm:"column:created_at;autoCreateTime"`
	RuleVersionID   *int64    `gorm:"column:rule_version_id"`
	CandidateID     *int64    `gorm:"column:candidate_id"`
	Actor           string    `gorm:"column:actor;not null"`
	ActorKind       string    `gorm:"column:actor_kind;not null"`
	Action          string    `gorm:"column:action;not null"`
	FromState       string    `gorm:"column:from_state"`
	ToState         string    `gorm:"column:to_state;not null"`
	Reason          string    `gorm:"column:reason;not null"`
	EvidenceHandles JSONRaw   `gorm:"column:evidence_handles_json;type:jsonb;not null;default:'[]'"`
	SnapshotID      string    `gorm:"column:snapshot_id"`
	ID              int64     `gorm:"primaryKey;autoIncrement"`
}

func (ruleTransitionLogRow) TableName() string { return "rule_transition_log" }

type ruleGovernanceSnapshotRow struct {
	CreatedAt    time.Time  `gorm:"column:created_at;autoCreateTime"`
	RolledBackAt *time.Time `gorm:"column:rolled_back_at"`
	SnapshotID   string     `gorm:"column:snapshot_id;not null;uniqueIndex"`
	OpType       string     `gorm:"column:op_type;not null"`
	Actor        string     `gorm:"column:actor;not null"`
	BeforeState  JSONRaw    `gorm:"column:before_state_json;type:jsonb;not null;default:'{}'"`
	AfterState   *JSONRaw   `gorm:"column:after_state_json;type:jsonb"`
	Status       string     `gorm:"column:status;not null;default:'committed'"`
	ID           int64      `gorm:"primaryKey;autoIncrement"`
	Pinned       bool       `gorm:"column:pinned;not null;default:false"`
}

func (ruleGovernanceSnapshotRow) TableName() string { return "rule_governance_snapshots" }

type RuleGovernanceStore struct {
	db *gorm.DB
}

func NewRuleGovernanceStore(db *gorm.DB) *RuleGovernanceStore {
	return &RuleGovernanceStore{db: db}
}

func (s *RuleGovernanceStore) CreateRuleCandidate(ctx context.Context, c *models.RuleCandidate) (*models.RuleCandidate, error) {
	if c == nil {
		return nil, fmt.Errorf("rule_governance create_candidate: candidate must not be nil")
	}
	if err := validateRuleCandidateForCreate(c); err != nil {
		return nil, err
	}
	if c.Fingerprint != "" {
		existing, err := s.GetRuleCandidateByFingerprint(ctx, c.Fingerprint)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
	}
	row := fromRuleCandidate(c)
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		if c.Fingerprint != "" && isUniqueViolation(err) {
			existing, getErr := s.GetRuleCandidateByFingerprint(ctx, c.Fingerprint)
			if getErr != nil {
				return nil, getErr
			}
			if existing != nil {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("rule_governance create_candidate: %w", err)
	}
	return toRuleCandidate(row), nil
}

func (s *RuleGovernanceStore) GetRuleCandidate(ctx context.Context, id int64) (*models.RuleCandidate, error) {
	var row ruleCandidateRow
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, fmt.Errorf("rule_governance get_candidate %d: %w", id, err)
	}
	return toRuleCandidate(&row), nil
}

func (s *RuleGovernanceStore) GetRuleCandidateByFingerprint(ctx context.Context, fingerprint string) (*models.RuleCandidate, error) {
	if fingerprint == "" {
		return nil, nil
	}
	var row ruleCandidateRow
	err := s.db.WithContext(ctx).
		Where("fingerprint = ? AND status IN ?", fingerprint, []string{
			string(models.RuleCandidatePending),
			string(models.RuleCandidateDrafted),
		}).
		Order("created_at ASC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rule_governance get_candidate_by_fingerprint: %w", err)
	}
	return toRuleCandidate(&row), nil
}

func (s *RuleGovernanceStore) ListRuleCandidates(ctx context.Context, params RuleCandidateListParams) ([]*models.RuleCandidate, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 50
	}
	q := s.db.WithContext(ctx).Order("created_at DESC").Limit(limit)
	if params.SourceProject != "" {
		q = q.Where("source_project = ?", params.SourceProject)
	}
	if params.Status != "" {
		q = q.Where("status = ?", string(params.Status))
	}
	var rows []ruleCandidateRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("rule_governance list_candidates: %w", err)
	}
	out := make([]*models.RuleCandidate, len(rows))
	for i := range rows {
		out[i] = toRuleCandidate(&rows[i])
	}
	return out, nil
}

func (s *RuleGovernanceStore) CreateDraftFromCandidate(ctx context.Context, candidateID int64, req RuleTransitionRequest) (*models.RuleVersion, error) {
	var result *models.RuleVersion
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		existing, err := getRuleVersionByCandidateTx(tx, candidateID)
		if err != nil {
			return err
		}
		if existing != nil {
			result = existing
			return nil
		}

		var candidate ruleCandidateRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&candidate, candidateID).Error; err != nil {
			return fmt.Errorf("rule_governance create_draft lock candidate %d: %w", candidateID, err)
		}
		if candidate.Status != string(models.RuleCandidatePending) {
			again, getErr := getRuleVersionByCandidateTx(tx, candidateID)
			if getErr != nil {
				return getErr
			}
			if again != nil {
				result = again
				return nil
			}
			return fmt.Errorf("%w: candidate %d status %s cannot draft", models.ErrInvalidRuleTransition, candidateID, candidate.Status)
		}
		if err := validateCandidateToDraftRequest(req); err != nil {
			return err
		}

		family, err := ensureRuleFamilyTx(tx, candidateID)
		if err != nil {
			return err
		}
		versionRow := ruleVersionRow{
			FamilyID:            family.ID,
			SourceCandidateID:   &candidate.ID,
			Content:             candidate.ProposedContent,
			Scope:               candidate.ProposedScope,
			Owner:               candidate.SourceActor,
			Audience:            candidate.ProposedAudience,
			ActivationPredicate: objectJSON(candidate.ActivationPredicate),
			EvidenceHandles:     objectOrArrayJSON(candidate.EvidenceHandles, `[]`),
			State:               string(models.RuleStateDraft),
			BudgetClass:         "contextual",
			AntiCaptureStatus:   candidate.AntiCaptureStatus,
			ConflictStatus:      candidate.ConflictStatus,
			DecayPolicy:         candidate.DecayPolicy,
			LastEvaluatedAt:     candidate.LastEvaluatedAt,
		}
		if versionRow.Owner == "" {
			versionRow.Owner = req.Actor
		}
		if err := tx.Create(&versionRow).Error; err != nil {
			if isUniqueViolation(err) {
				existing, getErr := getRuleVersionByCandidateTx(tx, candidateID)
				if getErr != nil {
					return getErr
				}
				if existing != nil {
					result = existing
					return nil
				}
			}
			return fmt.Errorf("rule_governance create_draft version: %w", err)
		}
		if err := tx.Model(&ruleCandidateRow{}).
			Where("id = ?", candidateID).
			Updates(map[string]any{
				"status":     string(models.RuleCandidateDrafted),
				"updated_at": time.Now().UTC(),
			}).Error; err != nil {
			return fmt.Errorf("rule_governance create_draft update candidate: %w", err)
		}
		if err := createRuleTransitionLogTx(tx, &versionRow.ID, &candidate.ID, "candidate_to_draft", "", string(models.RuleStateDraft), req); err != nil {
			return err
		}
		result = toRuleVersion(&versionRow)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *RuleGovernanceStore) RejectRuleCandidate(ctx context.Context, candidateID int64, req RuleTransitionRequest) (*models.RuleCandidate, error) {
	var result *models.RuleCandidate
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidate ruleCandidateRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&candidate, candidateID).Error; err != nil {
			return fmt.Errorf("rule_governance reject_candidate lock %d: %w", candidateID, err)
		}
		if candidate.Status != string(models.RuleCandidatePending) {
			return fmt.Errorf("%w: candidate %d status %s cannot reject", models.ErrInvalidRuleTransition, candidateID, candidate.Status)
		}
		if err := validateCandidateToDraftRequest(req); err != nil {
			return err
		}
		if err := tx.Model(&ruleCandidateRow{}).Where("id = ?", candidateID).
			Updates(map[string]any{
				"status":     string(models.RuleCandidateRejected),
				"updated_at": time.Now().UTC(),
			}).Error; err != nil {
			return fmt.Errorf("rule_governance reject_candidate update: %w", err)
		}
		if err := createRuleTransitionLogTx(tx, nil, &candidate.ID, "candidate_to_rejected", candidate.Status, string(models.RuleCandidateRejected), req); err != nil {
			return err
		}
		if err := tx.First(&candidate, candidateID).Error; err != nil {
			return fmt.Errorf("rule_governance reject_candidate reread: %w", err)
		}
		result = toRuleCandidate(&candidate)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *RuleGovernanceStore) TransitionRuleVersion(ctx context.Context, versionID int64, to models.RuleVersionState, req RuleTransitionRequest) (*models.RuleVersion, error) {
	var result *models.RuleVersion
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row ruleVersionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, versionID).Error; err != nil {
			return fmt.Errorf("rule_governance transition_version lock %d: %w", versionID, err)
		}
		from := models.RuleVersionState(row.State)
		if err := models.ValidateRuleVersionTransition(from, to, req); err != nil {
			return err
		}
		if models.RequiresRuleSnapshot(from, to) {
			before, err := json.Marshal(row)
			if err != nil {
				return fmt.Errorf("rule_governance transition_version marshal snapshot: %w", err)
			}
			if err := createRuleSnapshotTx(tx, SnapshotRequest{
				SnapshotID:  req.SnapshotID,
				OpType:      "rule_transition",
				Actor:       req.Actor,
				BeforeState: before,
			}); err != nil {
				return err
			}
		}
		updates := map[string]any{
			"state":      string(to),
			"updated_at": time.Now().UTC(),
		}
		if to == models.RuleStateArchived {
			now := time.Now().UTC()
			updates["archived_at"] = &now
		}
		if err := tx.Model(&ruleVersionRow{}).Where("id = ?", versionID).Updates(updates).Error; err != nil {
			return fmt.Errorf("rule_governance transition_version update: %w", err)
		}
		candidateID := row.SourceCandidateID
		if err := createRuleTransitionLogTx(tx, &row.ID, candidateID, "rule_version_transition", string(from), string(to), req); err != nil {
			return err
		}
		if err := tx.First(&row, versionID).Error; err != nil {
			return fmt.Errorf("rule_governance transition_version reread: %w", err)
		}
		result = toRuleVersion(&row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *RuleGovernanceStore) CreateRuleSnapshot(ctx context.Context, req SnapshotRequest) (*models.RuleGovernanceSnapshot, error) {
	var row *ruleGovernanceSnapshotRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		created, err := createRuleSnapshotRowTx(tx, req)
		if err != nil {
			return err
		}
		row = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return toRuleGovernanceSnapshot(row), nil
}

func fromRuleCandidate(c *models.RuleCandidate) *ruleCandidateRow {
	row := &ruleCandidateRow{
		ReviewAfter:         c.ReviewAfter,
		SourceSignalType:    c.SourceSignalType,
		SourceSessionID:     c.SourceSessionID,
		SourceProject:       c.SourceProject,
		SourceActor:         c.SourceActor,
		ProposedContent:     c.ProposedContent,
		ProposedScope:       c.ProposedScope,
		ProposedAudience:    c.ProposedAudience,
		ActivationPredicate: objectJSONFromMap(c.ActivationPredicate),
		EvidenceHandles:     arrayJSONFromStrings(c.EvidenceHandles),
		Confidence:          c.Confidence,
		RecurrenceCount:     c.RecurrenceCount,
		AntiCaptureStatus:   c.AntiCaptureStatus,
		AntiCaptureReason:   c.AntiCaptureReason,
		ConflictStatus:      c.ConflictStatus,
		Status:              string(c.Status),
		Fingerprint:         c.Fingerprint,
		DecayPolicy:         c.DecayPolicy,
	}
	if !c.LastEvaluatedAt.IsZero() {
		row.LastEvaluatedAt = &c.LastEvaluatedAt
	}
	if row.Status == "" {
		row.Status = string(models.RuleCandidatePending)
	}
	return row
}

func toRuleCandidate(row *ruleCandidateRow) *models.RuleCandidate {
	c := &models.RuleCandidate{
		ID:                  row.ID,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
		ReviewAfter:         row.ReviewAfter,
		SourceSignalType:    row.SourceSignalType,
		SourceSessionID:     row.SourceSessionID,
		SourceProject:       row.SourceProject,
		SourceActor:         row.SourceActor,
		ProposedContent:     row.ProposedContent,
		ProposedScope:       row.ProposedScope,
		ProposedAudience:    row.ProposedAudience,
		ActivationPredicate: decodeObject(row.ActivationPredicate),
		EvidenceHandles:     decodeStrings(row.EvidenceHandles),
		Confidence:          row.Confidence,
		RecurrenceCount:     row.RecurrenceCount,
		AntiCaptureStatus:   row.AntiCaptureStatus,
		AntiCaptureReason:   row.AntiCaptureReason,
		ConflictStatus:      row.ConflictStatus,
		Status:              models.RuleCandidateStatus(row.Status),
		Fingerprint:         row.Fingerprint,
		DecayPolicy:         row.DecayPolicy,
	}
	if row.LastEvaluatedAt != nil {
		c.LastEvaluatedAt = *row.LastEvaluatedAt
	}
	return c
}

func toRuleVersion(row *ruleVersionRow) *models.RuleVersion {
	return &models.RuleVersion{
		ID:                     row.ID,
		FamilyID:               row.FamilyID,
		SourceCandidateID:      row.SourceCandidateID,
		ActiveBehavioralRuleID: row.ActiveBehavioralRuleID,
		Content:                row.Content,
		Summary:                row.Summary,
		Scope:                  row.Scope,
		Owner:                  row.Owner,
		Audience:               row.Audience,
		ActivationPredicate:    decodeObject(row.ActivationPredicate),
		EvidenceHandles:        decodeStrings(row.EvidenceHandles),
		State:                  models.RuleVersionState(row.State),
		Protected:              row.Protected,
		Pinned:                 row.Pinned,
		Priority:               row.Priority,
		BudgetClass:            row.BudgetClass,
		AntiCaptureStatus:      row.AntiCaptureStatus,
		ConflictStatus:         row.ConflictStatus,
		DecayPolicy:            row.DecayPolicy,
		LastEvaluatedAt:        row.LastEvaluatedAt,
		SupersedesVersionID:    row.SupersedesVersionID,
		EffectiveFrom:          row.EffectiveFrom,
		EffectiveUntil:         row.EffectiveUntil,
		CreatedAt:              row.CreatedAt,
		UpdatedAt:              row.UpdatedAt,
		ArchivedAt:             row.ArchivedAt,
	}
}

func toRuleGovernanceSnapshot(row *ruleGovernanceSnapshotRow) *models.RuleGovernanceSnapshot {
	snap := &models.RuleGovernanceSnapshot{
		ID:           row.ID,
		SnapshotID:   row.SnapshotID,
		OpType:       row.OpType,
		Actor:        row.Actor,
		BeforeState:  []byte(row.BeforeState),
		Status:       row.Status,
		CreatedAt:    row.CreatedAt,
		RolledBackAt: row.RolledBackAt,
		Pinned:       row.Pinned,
	}
	if row.AfterState != nil {
		snap.AfterState = []byte(*row.AfterState)
	}
	return snap
}

func getRuleVersionByCandidateTx(tx *gorm.DB, candidateID int64) (*models.RuleVersion, error) {
	var row ruleVersionRow
	err := tx.Where("source_candidate_id = ?", candidateID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rule_governance get_version_by_candidate: %w", err)
	}
	return toRuleVersion(&row), nil
}

func ensureRuleFamilyTx(tx *gorm.DB, candidateID int64) (*ruleFamilyRow, error) {
	key := fmt.Sprintf("candidate:%d", candidateID)
	row := ruleFamilyRow{FamilyKey: key, CreatedFromCandidateID: &candidateID}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, fmt.Errorf("rule_governance create_family: %w", err)
	}
	if row.ID != 0 {
		return &row, nil
	}
	if err := tx.Where("family_key = ?", key).First(&row).Error; err != nil {
		return nil, fmt.Errorf("rule_governance get_family %q: %w", key, err)
	}
	return &row, nil
}

func createRuleTransitionLogTx(tx *gorm.DB, versionID *int64, candidateID *int64, action, from, to string, req RuleTransitionRequest) error {
	evidence := arrayJSONFromStrings(req.EvidenceHandles)
	row := ruleTransitionLogRow{
		RuleVersionID:   versionID,
		CandidateID:     candidateID,
		Actor:           req.Actor,
		ActorKind:       string(req.ActorKind),
		Action:          action,
		FromState:       from,
		ToState:         to,
		Reason:          req.Reason,
		EvidenceHandles: evidence,
		SnapshotID:      req.SnapshotID,
	}
	if err := tx.Create(&row).Error; err != nil {
		return fmt.Errorf("rule_governance transition_log: %w", err)
	}
	return nil
}

func createRuleSnapshotTx(tx *gorm.DB, req SnapshotRequest) error {
	_, err := createRuleSnapshotRowTx(tx, req)
	return err
}

func createRuleSnapshotRowTx(tx *gorm.DB, req SnapshotRequest) (*ruleGovernanceSnapshotRow, error) {
	if req.SnapshotID == "" || req.OpType == "" || req.Actor == "" {
		return nil, models.ErrRuleRequiredFieldMissing
	}
	before := req.BeforeState
	if len(before) == 0 {
		before = []byte(`{}`)
	}
	row := &ruleGovernanceSnapshotRow{
		SnapshotID:  req.SnapshotID,
		OpType:      req.OpType,
		Actor:       req.Actor,
		BeforeState: objectJSON(before),
		AfterState:  nullableObjectJSON(req.AfterState),
		Status:      "committed",
		Pinned:      req.Pinned,
	}
	if err := tx.Create(row).Error; err != nil {
		return nil, fmt.Errorf("rule_governance snapshot: %w", err)
	}
	return row, nil
}

func validateCandidateToDraftRequest(req RuleTransitionRequest) error {
	if req.Actor == "" || req.ActorKind == "" || req.Reason == "" || len(req.EvidenceHandles) == 0 {
		return models.ErrRuleRequiredFieldMissing
	}
	if !req.ActorKind.IsValid() {
		return models.ErrRuleAuthorityDenied
	}
	return nil
}

func validateRuleCandidateForCreate(c *models.RuleCandidate) error {
	required := []string{
		c.SourceSignalType,
		c.SourceActor,
		c.ProposedContent,
		c.ProposedScope,
		c.ProposedAudience,
		c.AntiCaptureStatus,
		c.ConflictStatus,
		c.DecayPolicy,
	}
	for _, value := range required {
		if err := models.ValidateRuleRequiredField(value); err != nil {
			return err
		}
	}
	return nil
}

func objectJSONFromMap(value map[string]any) JSONRaw {
	if value == nil {
		return JSONRaw(`{}`)
	}
	b, err := json.Marshal(value)
	if err != nil || len(b) == 0 {
		return JSONRaw(`{}`)
	}
	return JSONRaw(b)
}

func arrayJSONFromStrings(value []string) JSONRaw {
	if value == nil {
		return JSONRaw(`[]`)
	}
	b, err := json.Marshal(value)
	if err != nil || len(b) == 0 {
		return JSONRaw(`[]`)
	}
	return JSONRaw(b)
}

func objectJSON(value JSONRaw) JSONRaw {
	return objectOrArrayJSON(value, `{}`)
}

func objectOrArrayJSON(value JSONRaw, fallback string) JSONRaw {
	if len(value) == 0 {
		return JSONRaw(fallback)
	}
	return value
}

func nullableObjectJSON(value []byte) *JSONRaw {
	if len(value) == 0 {
		return nil
	}
	raw := objectJSON(JSONRaw(value))
	return &raw
}

func decodeObject(raw JSONRaw) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func decodeStrings(raw JSONRaw) []string {
	if len(raw) == 0 {
		return []string{}
	}
	out := []string{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return []string{}
	}
	return out
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate key") || strings.Contains(msg, "unique constraint")
}
