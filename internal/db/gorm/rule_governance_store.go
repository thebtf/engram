// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
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

type RuleVersionRenderListParams struct {
	Audience string
	Limit    int
}

type RuleGovernanceHealthParams struct {
	Since                         time.Time
	Project                       string
	Limit                         int
	IncludeGlobalArbiterRunCounts bool
}

type RuleGovernanceHealth struct {
	Since                    time.Time
	CandidateStatusCounts    map[models.RuleCandidateStatus]int
	VersionStateCounts       map[models.RuleVersionState]int
	ArbiterRunStatusCounts   map[models.RuleArbiterRunStatus]int
	TransitionActionCounts   map[string]int
	SnapshotStatusCounts     map[string]int
	InjectionEventTypeCounts map[models.RuleInjectionEventType]int
	EvidenceHandles          []string
	Project                  string
	Limit                    int
	NoData                   bool
}

type RuleGovernanceExceptionQueueParams struct {
	Project string
	Limit   int
}

type RuleGovernanceExceptionQueueItem struct {
	LastActivityAt         time.Time
	EvidenceHandles        []string
	RecommendedNextActions []string
	EntityType             string
	Project                string
	Scope                  string
	Reason                 string
	EntityID               int64
}

type RuleGovernanceExceptionQueueGroup struct {
	Items                  []RuleGovernanceExceptionQueueItem
	RecommendedNextActions []string
	Reason                 string
	Count                  int
}

type RuleGovernanceSnapshotListParams struct {
	Project string
	Limit   int
}

type RuleGovernanceSnapshotSummary struct {
	CreatedAt    time.Time
	RolledBackAt *time.Time
	SnapshotID   string
	OpType       string
	Actor        string
	Status       string
	Pinned       bool
}

type RuleGovernanceRollbackResult struct {
	RestoredVersionIDs []int64
	ConflictVersionIDs []int64
	SnapshotID         string
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
	CreatedAt           time.Time      `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt           time.Time      `gorm:"column:updated_at;autoUpdateTime"`
	ReviewAfter         *time.Time     `gorm:"column:review_after"`
	LastEvaluatedAt     *time.Time     `gorm:"column:last_evaluated_at"`
	ArbiterRunID        *int64         `gorm:"column:arbiter_run_id"`
	ArbiterEvaluationID *int64         `gorm:"column:arbiter_evaluation_id"`
	ArbiterClaimRunID   *int64         `gorm:"column:arbiter_claim_run_id"`
	SourceSignalType    string         `gorm:"column:source_signal_type;not null"`
	SourceSessionID     sql.NullString `gorm:"column:source_session_id"`
	SourceProject       sql.NullString `gorm:"column:source_project"`
	SourceActor         string         `gorm:"column:source_actor;not null"`
	ProposedContent     string         `gorm:"column:proposed_content;not null"`
	ProposedScope       string         `gorm:"column:proposed_scope;not null"`
	ProposedAudience    string         `gorm:"column:proposed_audience;not null"`
	ActivationPredicate JSONRaw        `gorm:"column:activation_predicate_json;type:jsonb;not null;default:'{}'"`
	EvidenceHandles     JSONRaw        `gorm:"column:evidence_handles_json;type:jsonb;not null;default:'[]'"`
	ArbiterAction       string         `gorm:"column:arbiter_action;not null;default:''"`
	ArbiterReason       string         `gorm:"column:arbiter_reason;not null;default:''"`
	AntiCaptureStatus   string         `gorm:"column:anti_capture_status;not null"`
	AntiCaptureReason   sql.NullString `gorm:"column:anti_capture_reason"`
	ConflictStatus      string         `gorm:"column:conflict_status;not null"`
	Status              string         `gorm:"column:status;not null;default:'pending'"`
	Fingerprint         string         `gorm:"column:fingerprint;not null;default:''"`
	DecayPolicy         string         `gorm:"column:decay_policy;not null"`
	ID                  int64          `gorm:"primaryKey;autoIncrement"`
	ArbiterConfidence   float64        `gorm:"column:arbiter_confidence;not null;default:0"`
	Confidence          float64        `gorm:"column:confidence;not null;default:0"`
	RecurrenceCount     int            `gorm:"column:recurrence_count;not null;default:0"`
}

func (ruleCandidateRow) TableName() string { return "rule_candidates" }

type ruleArbiterRunRow struct {
	StartedAt           time.Time      `gorm:"column:started_at;autoCreateTime"`
	FinishedAt          *time.Time     `gorm:"column:finished_at"`
	Trigger             string         `gorm:"column:trigger;not null"`
	Status              string         `gorm:"column:status;not null"`
	ErrorSummary        sql.NullString `gorm:"column:error_summary"`
	ID                  int64          `gorm:"primaryKey;autoIncrement"`
	CandidatesSeen      int            `gorm:"column:candidates_seen;not null;default:0"`
	CandidatesEvaluated int            `gorm:"column:candidates_evaluated;not null;default:0"`
	CandidatesProposed  int            `gorm:"column:candidates_proposed;not null;default:0"`
	CandidatesHeld      int            `gorm:"column:candidates_held;not null;default:0"`
	CandidatesRejected  int            `gorm:"column:candidates_rejected;not null;default:0"`
	CandidatesSkipped   int            `gorm:"column:candidates_skipped;not null;default:0"`
	Errors              int            `gorm:"column:errors;not null;default:0"`
}

func (ruleArbiterRunRow) TableName() string { return "rule_arbiter_runs" }

type ruleArbiterEvaluationRow struct {
	CreatedAt     time.Time      `gorm:"column:created_at;autoCreateTime"`
	Proposal      JSONRaw        `gorm:"column:proposal_json;type:jsonb;not null;default:'{}'"`
	RawResponse   sql.NullString `gorm:"column:raw_response"`
	ErrorSummary  sql.NullString `gorm:"column:error_summary"`
	EvaluatorKind string         `gorm:"column:evaluator_kind;not null"`
	Action        string         `gorm:"column:action;not null"`
	Reason        string         `gorm:"column:reason;not null"`
	ParseStatus   string         `gorm:"column:parse_status;not null"`
	ID            int64          `gorm:"primaryKey;autoIncrement"`
	RunID         int64          `gorm:"column:run_id;not null"`
	CandidateID   int64          `gorm:"column:candidate_id;not null"`
	Confidence    float64        `gorm:"column:confidence;not null;default:0"`
}

func (ruleArbiterEvaluationRow) TableName() string { return "rule_arbiter_evaluations" }

func nullableString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func stringFromNull(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func validRuleConfidence(value float64) bool {
	return value >= 0 && value <= 1
}

type ruleFamilyRow struct {
	CreatedAt              time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt              time.Time `gorm:"column:updated_at;autoUpdateTime"`
	FamilyKey              string    `gorm:"column:family_key;not null;uniqueIndex"`
	CreatedFromCandidateID *int64    `gorm:"column:created_from_candidate_id"`
	ID                     int64     `gorm:"primaryKey;autoIncrement"`
}

func (ruleFamilyRow) TableName() string { return "rule_families" }

type ruleVersionRow struct {
	CreatedAt              time.Time      `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt              time.Time      `gorm:"column:updated_at;autoUpdateTime"`
	LastEvaluatedAt        *time.Time     `gorm:"column:last_evaluated_at"`
	EffectiveFrom          *time.Time     `gorm:"column:effective_from"`
	EffectiveUntil         *time.Time     `gorm:"column:effective_until"`
	ArchivedAt             *time.Time     `gorm:"column:archived_at"`
	FamilyID               int64          `gorm:"column:family_id;not null"`
	SourceCandidateID      *int64         `gorm:"column:source_candidate_id"`
	ActiveBehavioralRuleID *int64         `gorm:"column:active_behavioral_rule_id"`
	Content                string         `gorm:"column:content;not null"`
	Summary                sql.NullString `gorm:"column:summary"`
	Scope                  string         `gorm:"column:scope;not null"`
	Owner                  string         `gorm:"column:owner;not null"`
	Audience               string         `gorm:"column:audience;not null"`
	ActivationPredicate    JSONRaw        `gorm:"column:activation_predicate_json;type:jsonb;not null;default:'{}'"`
	EvidenceHandles        JSONRaw        `gorm:"column:evidence_handles_json;type:jsonb;not null;default:'[]'"`
	State                  string         `gorm:"column:state;not null"`
	BudgetClass            string         `gorm:"column:budget_class;not null;default:'contextual'"`
	AntiCaptureStatus      string         `gorm:"column:anti_capture_status;not null"`
	ConflictStatus         string         `gorm:"column:conflict_status;not null"`
	DecayPolicy            string         `gorm:"column:decay_policy;not null"`
	SupersedesVersionID    *int64         `gorm:"column:supersedes_version_id"`
	ID                     int64          `gorm:"primaryKey;autoIncrement"`
	Priority               int            `gorm:"column:priority;not null;default:0"`
	Protected              bool           `gorm:"column:protected;not null;default:false"`
	Pinned                 bool           `gorm:"column:pinned;not null;default:false"`
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

func (s *RuleGovernanceStore) ListRenderableRuleVersions(ctx context.Context, params RuleVersionRenderListParams) ([]*models.RuleVersion, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 50
	}
	states := []string{
		string(models.RuleStateKernel),
		string(models.RuleStateActiveProject),
		string(models.RuleStateActiveShared),
		string(models.RuleStateActiveGlobal),
	}
	q := s.db.WithContext(ctx).
		Where("state IN ?", states).
		Order("priority DESC, created_at DESC").
		Limit(limit)
	if strings.TrimSpace(params.Audience) != "" {
		q = q.Where("audience = ?", strings.TrimSpace(params.Audience))
	}

	var rows []ruleVersionRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("rule_governance list_renderable_versions: %w", err)
	}
	out := make([]*models.RuleVersion, len(rows))
	for i := range rows {
		out[i] = toRuleVersion(&rows[i])
	}
	return out, nil
}

func (s *RuleGovernanceStore) ListLegacyBehavioralRuleFallback(ctx context.Context, project *string, limit int) ([]*models.BehavioralRule, error) {
	if limit <= 0 {
		limit = 50
	}
	q := s.db.WithContext(ctx).
		Where("deleted_at IS NULL").
		Order("priority DESC, created_at DESC").
		Limit(limit)
	if project == nil {
		q = q.Where("project IS NULL")
	} else {
		q = q.Where("project = ? OR project IS NULL", *project)
	}

	var rows []BehavioralRule
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("rule_governance list_legacy_behavioral_fallback: %w", err)
	}
	out := make([]*models.BehavioralRule, len(rows))
	for i := range rows {
		out[i] = behavioralRuleRowToModel(&rows[i])
	}
	return out, nil
}

func (s *RuleGovernanceStore) ListPendingRuleCandidatesForArbiter(ctx context.Context, runID int64, limit int, now time.Time) ([]*models.RuleCandidate, error) {
	if runID <= 0 {
		return nil, models.ErrRuleRequiredFieldMissing
	}
	if limit <= 0 {
		limit = 20
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var rows []ruleCandidateRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run ruleArbiterRunRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&run, runID).Error; err != nil {
			return fmt.Errorf("rule_governance list_pending_for_arbiter lock run %d: %w", runID, err)
		}
		if run.Status != string(models.RuleArbiterRunStatusStarted) {
			return fmt.Errorf("%w: arbiter run %d status %s cannot claim candidates", models.ErrInvalidRuleTransition, runID, run.Status)
		}

		q := tx.Where("status = ?", string(models.RuleCandidatePending)).
			Where("review_after IS NULL OR review_after <= ?", now).
			Where("arbiter_action = '' OR review_after <= ?", now).
			Where("arbiter_claim_run_id IS NULL").
			Order("created_at ASC").
			Limit(limit)
		if tx.Dialector.Name() == "postgres" {
			q = q.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		}
		if err := q.Find(&rows).Error; err != nil {
			return fmt.Errorf("rule_governance list_pending_for_arbiter: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		ids := make([]int64, 0, len(rows))
		for i := range rows {
			ids = append(ids, rows[i].ID)
			rows[i].ArbiterClaimRunID = &runID
		}
		if err := tx.Model(&ruleCandidateRow{}).
			Where("id IN ? AND arbiter_claim_run_id IS NULL", ids).
			Updates(map[string]any{
				"arbiter_claim_run_id": runID,
				"updated_at":           time.Now().UTC(),
			}).Error; err != nil {
			return fmt.Errorf("rule_governance list_pending_for_arbiter claim: %w", err)
		}
		rows = nil
		if err := tx.Where("id IN ? AND arbiter_claim_run_id = ?", ids, runID).
			Order("created_at ASC").
			Find(&rows).Error; err != nil {
			return fmt.Errorf("rule_governance list_pending_for_arbiter claimed reread: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*models.RuleCandidate, len(rows))
	for i := range rows {
		out[i] = toRuleCandidate(&rows[i])
	}
	return out, nil
}

func (s *RuleGovernanceStore) StartRuleArbiterRun(ctx context.Context, trigger string) (*models.RuleArbiterRun, error) {
	trigger = strings.TrimSpace(trigger)
	if trigger == "" {
		trigger = "scheduled"
	}
	row := &ruleArbiterRunRow{
		Trigger: trigger,
		Status:  string(models.RuleArbiterRunStatusStarted),
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("rule_governance start_arbiter_run: %w", err)
	}
	return toRuleArbiterRun(row), nil
}

func (s *RuleGovernanceStore) FinishRuleArbiterRun(ctx context.Context, runID int64, status models.RuleArbiterRunStatus, counts models.RuleArbiterRunCounts, errorSummary string) (*models.RuleArbiterRun, error) {
	if runID <= 0 {
		return nil, fmt.Errorf("rule_governance finish_arbiter_run: run id required")
	}
	if !status.IsValid() || status == models.RuleArbiterRunStatusStarted {
		return nil, fmt.Errorf("%w: invalid arbiter run status %q", models.ErrInvalidRuleTransition, status)
	}
	if hasNegativeRuleArbiterCount(counts) {
		return nil, models.ErrRuleRequiredFieldMissing
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"finished_at":          &now,
		"status":               string(status),
		"error_summary":        nullableString(errorSummary),
		"candidates_seen":      counts.CandidatesSeen,
		"candidates_evaluated": counts.CandidatesEvaluated,
		"candidates_proposed":  counts.CandidatesProposed,
		"candidates_held":      counts.CandidatesHeld,
		"candidates_rejected":  counts.CandidatesRejected,
		"candidates_skipped":   counts.CandidatesSkipped,
		"errors":               counts.Errors,
	}
	var row ruleArbiterRunRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&ruleArbiterRunRow{}).
			Where("id = ? AND status = ?", runID, string(models.RuleArbiterRunStatusStarted)).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("rule_governance finish_arbiter_run: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("%w: arbiter run %d is not started or does not exist", models.ErrInvalidRuleTransition, runID)
		}
		if err := tx.Model(&ruleCandidateRow{}).
			Where("arbiter_claim_run_id = ?", runID).
			Update("arbiter_claim_run_id", nil).Error; err != nil {
			return fmt.Errorf("rule_governance finish_arbiter_run clear_claims: %w", err)
		}
		if err := tx.First(&row, runID).Error; err != nil {
			return fmt.Errorf("rule_governance finish_arbiter_run reread: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return toRuleArbiterRun(&row), nil
}

func (s *RuleGovernanceStore) CreateRuleArbiterEvaluation(ctx context.Context, eval *models.RuleArbiterEvaluation) (*models.RuleArbiterEvaluation, error) {
	if eval == nil {
		return nil, fmt.Errorf("rule_governance create_arbiter_evaluation: evaluation must not be nil")
	}
	if eval.RunID <= 0 || eval.CandidateID <= 0 || !eval.Action.IsValid() || !eval.ParseStatus.IsValid() || !validRuleConfidence(eval.Confidence) {
		return nil, models.ErrRuleRequiredFieldMissing
	}
	if strings.TrimSpace(eval.Reason) == "" {
		return nil, models.ErrRuleRequiredFieldMissing
	}
	eval.Reason = strings.TrimSpace(eval.Reason)
	if strings.TrimSpace(eval.EvaluatorKind) == "" {
		eval.EvaluatorKind = "llm"
	} else {
		eval.EvaluatorKind = strings.TrimSpace(eval.EvaluatorKind)
	}
	var row *ruleArbiterEvaluationRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run ruleArbiterRunRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&run, eval.RunID).Error; err != nil {
			return fmt.Errorf("rule_governance create_arbiter_evaluation lock run %d: %w", eval.RunID, err)
		}
		if run.Status != string(models.RuleArbiterRunStatusStarted) {
			return fmt.Errorf("%w: arbiter run %d status %s cannot accept evaluations", models.ErrInvalidRuleTransition, eval.RunID, run.Status)
		}
		var candidate ruleCandidateRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&candidate, eval.CandidateID).Error; err != nil {
			return fmt.Errorf("rule_governance create_arbiter_evaluation lock candidate %d: %w", eval.CandidateID, err)
		}
		if candidate.Status != string(models.RuleCandidatePending) {
			return fmt.Errorf("%w: candidate %d status %s cannot be evaluated", models.ErrInvalidRuleTransition, eval.CandidateID, candidate.Status)
		}
		if candidate.ArbiterClaimRunID == nil || *candidate.ArbiterClaimRunID != eval.RunID {
			return fmt.Errorf("%w: candidate %d is not claimed by arbiter run %d", models.ErrInvalidRuleTransition, eval.CandidateID, eval.RunID)
		}
		row = fromRuleArbiterEvaluation(eval)
		if err := tx.Create(row).Error; err != nil {
			return fmt.Errorf("rule_governance create_arbiter_evaluation: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return toRuleArbiterEvaluation(row), nil
}

func (s *RuleGovernanceStore) AnnotateRuleCandidate(ctx context.Context, candidateID int64, ann models.RuleCandidateAnnotation) (*models.RuleCandidate, error) {
	if candidateID <= 0 || !ann.Action.IsValid() || strings.TrimSpace(ann.Reason) == "" || !validRuleConfidence(ann.Confidence) {
		return nil, models.ErrRuleRequiredFieldMissing
	}
	if ann.RunID == nil || *ann.RunID <= 0 || ann.EvaluationID == nil || *ann.EvaluationID <= 0 {
		return nil, models.ErrRuleRequiredFieldMissing
	}
	ann.Reason = strings.TrimSpace(ann.Reason)
	evaluatedAt := ann.EvaluatedAt
	if evaluatedAt.IsZero() {
		evaluatedAt = time.Now().UTC()
	}
	updates := map[string]any{
		"arbiter_action":        string(ann.Action),
		"arbiter_reason":        ann.Reason,
		"arbiter_confidence":    ann.Confidence,
		"arbiter_run_id":        ann.RunID,
		"arbiter_evaluation_id": ann.EvaluationID,
		"last_evaluated_at":     &evaluatedAt,
		"review_after":          ann.ReviewAfter,
		"updated_at":            time.Now().UTC(),
	}
	var row ruleCandidateRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidate ruleCandidateRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&candidate, candidateID).Error; err != nil {
			return fmt.Errorf("rule_governance annotate_candidate lock candidate %d: %w", candidateID, err)
		}
		if candidate.Status != string(models.RuleCandidatePending) {
			return fmt.Errorf("%w: candidate %d status %s cannot be annotated", models.ErrInvalidRuleTransition, candidateID, candidate.Status)
		}
		if candidate.ArbiterClaimRunID == nil || *candidate.ArbiterClaimRunID != *ann.RunID {
			return fmt.Errorf("%w: candidate %d is not claimed by arbiter run %d", models.ErrInvalidRuleTransition, candidateID, *ann.RunID)
		}
		var eval ruleArbiterEvaluationRow
		if err := tx.Where("id = ? AND candidate_id = ? AND run_id = ? AND action = ?",
			*ann.EvaluationID, candidateID, *ann.RunID, string(ann.Action)).
			First(&eval).Error; err != nil {
			return fmt.Errorf("%w: evaluation %d does not match candidate/run/action", models.ErrInvalidRuleTransition, *ann.EvaluationID)
		}
		if err := tx.Model(&ruleCandidateRow{}).
			Where("id = ?", candidateID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("rule_governance annotate_candidate: %w", err)
		}
		if err := tx.First(&row, candidateID).Error; err != nil {
			return fmt.Errorf("rule_governance annotate_candidate reread: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return toRuleCandidate(&row), nil
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
			before, err := ruleGovernanceSnapshotStateForRuleVersion(&row, from)
			if err != nil {
				return fmt.Errorf("rule_governance transition_version marshal snapshot: %w", err)
			}
			after, err := ruleGovernanceSnapshotStateForRuleVersion(&row, to)
			if err != nil {
				return fmt.Errorf("rule_governance transition_version marshal after snapshot: %w", err)
			}
			if err := createRuleSnapshotTx(tx, SnapshotRequest{
				SnapshotID:  req.SnapshotID,
				OpType:      "rule_transition",
				Actor:       req.Actor,
				BeforeState: before,
				AfterState:  after,
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

func (s *RuleGovernanceStore) GetLifecycleHealth(ctx context.Context, params RuleGovernanceHealthParams) (RuleGovernanceHealth, error) {
	if s == nil || s.db == nil {
		return RuleGovernanceHealth{}, fmt.Errorf("rule_governance health: store is not initialized")
	}
	params.Project = strings.TrimSpace(params.Project)
	if params.Limit <= 0 {
		params.Limit = 100
	}
	health := RuleGovernanceHealth{
		Project:                  params.Project,
		Since:                    params.Since,
		Limit:                    params.Limit,
		CandidateStatusCounts:    map[models.RuleCandidateStatus]int{},
		VersionStateCounts:       map[models.RuleVersionState]int{},
		ArbiterRunStatusCounts:   map[models.RuleArbiterRunStatus]int{},
		TransitionActionCounts:   map[string]int{},
		SnapshotStatusCounts:     map[string]int{},
		InjectionEventTypeCounts: map[models.RuleInjectionEventType]int{},
	}

	if err := s.countCandidateStatuses(ctx, params, &health); err != nil {
		return health, err
	}
	if err := s.countVersionStates(ctx, params, &health); err != nil {
		return health, err
	}
	if err := s.countArbiterRunStatuses(ctx, params, &health); err != nil {
		return health, err
	}
	if err := s.countTransitionActions(ctx, params, &health); err != nil {
		return health, err
	}
	if err := s.countSnapshotStatuses(ctx, params, &health); err != nil {
		return health, err
	}
	if err := s.countInjectionEventTypes(ctx, params, &health); err != nil {
		return health, err
	}
	health.NoData = healthCountTotal(health) == 0
	return health, nil
}

func (s *RuleGovernanceStore) ListExceptionQueueGroups(ctx context.Context, params RuleGovernanceExceptionQueueParams) ([]RuleGovernanceExceptionQueueGroup, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("rule_governance exception_queue: store is not initialized")
	}
	if params.Limit <= 0 {
		params.Limit = 100
	}
	project := strings.TrimSpace(params.Project)
	var candidateRows []ruleCandidateRow
	candidateQ := s.db.WithContext(ctx).
		Where("status IN ?", []string{
			string(models.RuleCandidatePending),
			string(models.RuleCandidateDrafted),
		}).
		Where(
			"(LOWER(proposed_scope) IN ? OR (LOWER(proposed_scope) = ? AND COALESCE(source_project, '') = '') OR LOWER(conflict_status) LIKE ? OR anti_capture_status = ?)",
			[]string{"", "global", "kernel", "private", "personal", "unknown"},
			"project",
			"%conflict%",
			"reject_review_hold",
		).
		Order("updated_at DESC, id DESC").
		Limit(params.Limit)
	if project != "" {
		candidateQ = candidateQ.Where("source_project = ?", project)
	}
	if err := candidateQ.Find(&candidateRows).Error; err != nil {
		return nil, fmt.Errorf("rule_governance exception_queue candidates: %w", err)
	}
	candidates := make([]*models.RuleCandidate, 0, len(candidateRows))
	for i := range candidateRows {
		candidates = append(candidates, toRuleCandidate(&candidateRows[i]))
	}

	groups := map[string]*RuleGovernanceExceptionQueueGroup{}
	addQueueItem := func(reason string, item RuleGovernanceExceptionQueueItem, next []string) {
		group := groups[reason]
		if group == nil {
			group = &RuleGovernanceExceptionQueueGroup{
				Reason:                 reason,
				RecommendedNextActions: append([]string{}, next...),
			}
			groups[reason] = group
		}
		item.Reason = reason
		item.RecommendedNextActions = append([]string{}, next...)
		group.Items = append(group.Items, item)
		group.Count = len(group.Items)
	}
	addCandidateItem := func(reason string, candidate *models.RuleCandidate, next []string) {
		item := RuleGovernanceExceptionQueueItem{
			EntityID:               candidate.ID,
			EntityType:             "rule_candidate",
			Project:                candidate.SourceProject,
			Scope:                  candidate.ProposedScope,
			Reason:                 reason,
			EvidenceHandles:        append([]string{}, candidate.EvidenceHandles...),
			LastActivityAt:         candidate.UpdatedAt,
			RecommendedNextActions: append([]string{}, next...),
		}
		addQueueItem(reason, item, next)
	}

	for _, candidate := range candidates {
		scope := strings.TrimSpace(candidate.ProposedScope)
		scopeLower := strings.ToLower(scope)
		if scopeLower == "global" || scopeLower == "kernel" {
			addCandidateItem("global_kernel_escalation", candidate, []string{"manual_operator_review"})
		}
		if strings.Contains(strings.ToLower(candidate.ConflictStatus), "conflict") {
			addCandidateItem("active_rule_conflict", candidate, []string{"compare_active_rule_family"})
		}
		if strings.EqualFold(candidate.AntiCaptureStatus, "reject_review_hold") {
			addCandidateItem("reject_review_hold", candidate, []string{"review_anti_capture_evidence"})
		}
		if scope == "" || scopeLower == "private" || scopeLower == "personal" || scopeLower == "unknown" || (scopeLower == "project" && strings.TrimSpace(candidate.SourceProject) == "") {
			addCandidateItem("unclear_scope_private_risk", candidate, []string{"clarify_scope_before_promotion"})
		}
	}

	var canaryRows []ruleVersionRow
	canaryQ := s.db.WithContext(ctx).
		Where("state = ?", string(models.RuleStateCanary)).
		Order("updated_at DESC, id DESC").
		Limit(params.Limit)
	if project != "" {
		canaryQ = canaryQ.Where("activation_predicate_json ->> 'project' = ?", project)
	}
	if err := canaryQ.Find(&canaryRows).Error; err != nil {
		return nil, fmt.Errorf("rule_governance exception_queue canary_versions: %w", err)
	}
	for i := range canaryRows {
		row := &canaryRows[i]
		itemProject := projectFromRuleVersionRow(row)
		if itemProject == "" {
			itemProject = project
		}
		addQueueItem("canary_result_review", RuleGovernanceExceptionQueueItem{
			EntityID:        row.ID,
			EntityType:      "rule_version",
			Project:         itemProject,
			Scope:           row.Scope,
			EvidenceHandles: append([]string{fmt.Sprintf("rule_version:%d", row.ID)}, decodeStrings(row.EvidenceHandles)...),
			LastActivityAt:  row.UpdatedAt,
		}, []string{"review_canary_usefulness_metrics"})
	}

	var snapshotRows []ruleGovernanceSnapshotRow
	snapshotQ := s.db.WithContext(ctx).
		Where("status IN ?", []string{"conflict", "rollback_conflict", "restore_conflict", "archive_conflict", "failed"}).
		Order("created_at DESC, id DESC").
		Limit(params.Limit)
	if project != "" {
		snapshotQ = snapshotQ.Where("before_state_json ->> 'project' = ? OR after_state_json ->> 'project' = ?", project, project)
	}
	if err := snapshotQ.Find(&snapshotRows).Error; err != nil {
		return nil, fmt.Errorf("rule_governance exception_queue snapshot_conflicts: %w", err)
	}
	for i := range snapshotRows {
		row := &snapshotRows[i]
		addQueueItem("rollback_archive_restore_conflict", RuleGovernanceExceptionQueueItem{
			EntityID:        row.ID,
			EntityType:      "rule_governance_snapshot",
			Project:         projectFromSnapshotRow(row),
			Scope:           row.OpType,
			EvidenceHandles: []string{"rule_governance_snapshot:" + row.SnapshotID},
			LastActivityAt:  row.CreatedAt,
		}, []string{"inspect_snapshot_conflict"})
	}

	var eventRows []ruleInjectionEventRow
	eventQ := s.db.WithContext(ctx).
		Where("event_type IN ? AND (reason ILIKE ? OR reason ILIKE ? OR reason ILIKE ? OR reason ILIKE ? OR surface ILIKE ?)",
			[]string{
				string(models.RuleInjectionFallbackLegacy),
				string(models.RuleInjectionRouterError),
				string(models.RuleInjectionSuppressedState),
			},
			"%stale%",
			"%revoked%",
			"%archived%",
			"%superseded%",
			"%cache%",
		).
		Order("created_at DESC, id DESC").
		Limit(params.Limit)
	if project != "" {
		eventQ = eventQ.Where("project = ?", project)
	}
	if err := eventQ.Find(&eventRows).Error; err != nil {
		return nil, fmt.Errorf("rule_governance exception_queue stale_cache_events: %w", err)
	}
	for i := range eventRows {
		row := &eventRows[i]
		evidence := []string{fmt.Sprintf("rule_injection_event:%d", row.ID), "session:" + row.SessionID}
		if row.RuleVersionID.Valid {
			evidence = append(evidence, fmt.Sprintf("rule_version:%d", row.RuleVersionID.Int64))
		}
		if row.LegacyBehavioralRuleID.Valid {
			evidence = append(evidence, fmt.Sprintf("legacy_behavioral_rule:%d", row.LegacyBehavioralRuleID.Int64))
		}
		addQueueItem("stale_cache_revocation_anomaly", RuleGovernanceExceptionQueueItem{
			EntityID:        row.ID,
			EntityType:      "rule_injection_event",
			Project:         row.Project,
			Scope:           row.Surface,
			EvidenceHandles: evidence,
			LastActivityAt:  row.CreatedAt,
		}, []string{"inspect_stale_cache_or_revocation_event"})
	}

	order := []string{
		"global_kernel_escalation",
		"active_rule_conflict",
		"reject_review_hold",
		"unclear_scope_private_risk",
		"canary_result_review",
		"rollback_archive_restore_conflict",
		"stale_cache_revocation_anomaly",
	}
	out := make([]RuleGovernanceExceptionQueueGroup, 0, len(groups))
	for _, reason := range order {
		if group := groups[reason]; group != nil {
			out = append(out, *group)
		}
	}
	return out, nil
}

func (s *RuleGovernanceStore) ListRuleGovernanceSnapshots(ctx context.Context, params RuleGovernanceSnapshotListParams) ([]RuleGovernanceSnapshotSummary, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("rule_governance list_snapshots: store is not initialized")
	}
	if params.Limit <= 0 {
		params.Limit = 50
	}
	q := s.db.WithContext(ctx).Order("created_at DESC, id DESC").Limit(params.Limit)
	if strings.TrimSpace(params.Project) != "" {
		project := strings.TrimSpace(params.Project)
		q = q.Where("before_state_json ->> 'project' = ? OR after_state_json ->> 'project' = ?", project, project)
	}
	var rows []ruleGovernanceSnapshotRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("rule_governance list_snapshots: %w", err)
	}
	out := make([]RuleGovernanceSnapshotSummary, 0, len(rows))
	for i := range rows {
		out = append(out, toRuleGovernanceSnapshotSummary(&rows[i]))
	}
	return out, nil
}

func (s *RuleGovernanceStore) PinRuleGovernanceSnapshot(ctx context.Context, snapshotID string, pinned bool) (RuleGovernanceSnapshotSummary, error) {
	if s == nil || s.db == nil {
		return RuleGovernanceSnapshotSummary{}, fmt.Errorf("rule_governance pin_snapshot: store is not initialized")
	}
	snapshotID = strings.TrimSpace(snapshotID)
	if snapshotID == "" {
		return RuleGovernanceSnapshotSummary{}, models.ErrRuleRequiredFieldMissing
	}
	var row ruleGovernanceSnapshotRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&ruleGovernanceSnapshotRow{}).
			Where("snapshot_id = ?", snapshotID).
			Update("pinned", pinned)
		if result.Error != nil {
			return fmt.Errorf("rule_governance pin_snapshot: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("rule_governance pin_snapshot %q: %w", snapshotID, gorm.ErrRecordNotFound)
		}
		if err := tx.Where("snapshot_id = ?", snapshotID).First(&row).Error; err != nil {
			return fmt.Errorf("rule_governance pin_snapshot reread: %w", err)
		}
		return nil
	})
	if err != nil {
		return RuleGovernanceSnapshotSummary{}, err
	}
	return toRuleGovernanceSnapshotSummary(&row), nil
}

func (s *RuleGovernanceStore) RollbackRuleGovernanceSnapshot(ctx context.Context, snapshotID string, req RuleTransitionRequest) (RuleGovernanceRollbackResult, error) {
	result := RuleGovernanceRollbackResult{SnapshotID: strings.TrimSpace(snapshotID)}
	if s == nil || s.db == nil {
		return result, fmt.Errorf("rule_governance rollback_snapshot: store is not initialized")
	}
	if result.SnapshotID == "" {
		return result, models.ErrRuleRequiredFieldMissing
	}
	if err := validateCandidateToDraftRequest(req); err != nil {
		return result, err
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row ruleGovernanceSnapshotRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("snapshot_id = ?", result.SnapshotID).First(&row).Error; err != nil {
			return fmt.Errorf("rule_governance rollback_snapshot lock: %w", err)
		}
		if row.Status != "committed" {
			return fmt.Errorf("%w: snapshot %q status %s cannot rollback", models.ErrInvalidRuleTransition, result.SnapshotID, row.Status)
		}
		before := decodeRuleGovernanceSnapshotState(row.BeforeState)
		if len(before.RuleVersions) == 0 {
			return fmt.Errorf("%w: snapshot %q has no rollback rule versions", models.ErrInvalidRuleTransition, result.SnapshotID)
		}
		after := decodeRuleGovernanceSnapshotStatePtr(row.AfterState)
		expectedCurrent := after.RuleVersions
		if len(expectedCurrent) == 0 {
			expectedCurrent = before.RuleVersions
		}
		for _, expected := range expectedCurrent {
			var version ruleVersionRow
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&version, expected.ID).Error; err != nil {
				return fmt.Errorf("rule_governance rollback_snapshot version %d: %w", expected.ID, err)
			}
			if expected.State != "" && version.State != expected.State {
				result.ConflictVersionIDs = append(result.ConflictVersionIDs, expected.ID)
				continue
			}
			if version.Protected || version.Pinned {
				result.ConflictVersionIDs = append(result.ConflictVersionIDs, expected.ID)
			}
		}
		if len(result.ConflictVersionIDs) > 0 {
			return fmt.Errorf("%w: rollback conflicts detected", models.ErrInvalidRuleTransition)
		}
		for _, restore := range before.RuleVersions {
			updates := map[string]any{
				"state":      restore.State,
				"updated_at": time.Now().UTC(),
			}
			if err := tx.Model(&ruleVersionRow{}).Where("id = ?", restore.ID).Updates(updates).Error; err != nil {
				return fmt.Errorf("rule_governance rollback_snapshot restore %d: %w", restore.ID, err)
			}
			result.RestoredVersionIDs = append(result.RestoredVersionIDs, restore.ID)
		}
		now := time.Now().UTC()
		if err := tx.Model(&ruleGovernanceSnapshotRow{}).Where("id = ?", row.ID).Updates(map[string]any{
			"status":         "rolled_back",
			"rolled_back_at": &now,
		}).Error; err != nil {
			return fmt.Errorf("rule_governance rollback_snapshot mark: %w", err)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, models.ErrInvalidRuleTransition) && len(result.ConflictVersionIDs) > 0 {
			if updateErr := s.db.WithContext(ctx).Model(&ruleGovernanceSnapshotRow{}).
				Where("snapshot_id = ?", result.SnapshotID).
				Update("status", "rollback_conflict").Error; updateErr != nil {
				return result, fmt.Errorf("rule_governance rollback_snapshot mark_conflict: %w", updateErr)
			}
		}
		return result, err
	}
	return result, nil
}

func fromRuleCandidate(c *models.RuleCandidate) *ruleCandidateRow {
	row := &ruleCandidateRow{
		ReviewAfter:         c.ReviewAfter,
		SourceSignalType:    c.SourceSignalType,
		SourceSessionID:     nullableString(c.SourceSessionID),
		SourceProject:       nullableString(c.SourceProject),
		SourceActor:         c.SourceActor,
		ProposedContent:     c.ProposedContent,
		ProposedScope:       c.ProposedScope,
		ProposedAudience:    c.ProposedAudience,
		ActivationPredicate: objectJSONFromMap(c.ActivationPredicate),
		EvidenceHandles:     arrayJSONFromStrings(c.EvidenceHandles),
		Confidence:          c.Confidence,
		RecurrenceCount:     c.RecurrenceCount,
		AntiCaptureStatus:   c.AntiCaptureStatus,
		AntiCaptureReason:   nullableString(c.AntiCaptureReason),
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
		ArbiterRunID:        row.ArbiterRunID,
		ArbiterEvaluationID: row.ArbiterEvaluationID,
		SourceSignalType:    row.SourceSignalType,
		SourceSessionID:     stringFromNull(row.SourceSessionID),
		SourceProject:       stringFromNull(row.SourceProject),
		SourceActor:         row.SourceActor,
		ProposedContent:     row.ProposedContent,
		ProposedScope:       row.ProposedScope,
		ProposedAudience:    row.ProposedAudience,
		ActivationPredicate: decodeObject(row.ActivationPredicate),
		EvidenceHandles:     decodeStrings(row.EvidenceHandles),
		ArbiterAction:       models.RuleArbiterAction(row.ArbiterAction),
		ArbiterReason:       row.ArbiterReason,
		ArbiterConfidence:   row.ArbiterConfidence,
		Confidence:          row.Confidence,
		RecurrenceCount:     row.RecurrenceCount,
		AntiCaptureStatus:   row.AntiCaptureStatus,
		AntiCaptureReason:   stringFromNull(row.AntiCaptureReason),
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

func toRuleArbiterRun(row *ruleArbiterRunRow) *models.RuleArbiterRun {
	return &models.RuleArbiterRun{
		ID:           row.ID,
		Trigger:      row.Trigger,
		Status:       models.RuleArbiterRunStatus(row.Status),
		StartedAt:    row.StartedAt,
		FinishedAt:   row.FinishedAt,
		ErrorSummary: stringFromNull(row.ErrorSummary),
		RuleArbiterRunCounts: models.RuleArbiterRunCounts{
			CandidatesSeen:      row.CandidatesSeen,
			CandidatesEvaluated: row.CandidatesEvaluated,
			CandidatesProposed:  row.CandidatesProposed,
			CandidatesHeld:      row.CandidatesHeld,
			CandidatesRejected:  row.CandidatesRejected,
			CandidatesSkipped:   row.CandidatesSkipped,
			Errors:              row.Errors,
		},
	}
}

func fromRuleArbiterEvaluation(eval *models.RuleArbiterEvaluation) *ruleArbiterEvaluationRow {
	evaluatorKind := eval.EvaluatorKind
	if evaluatorKind == "" {
		evaluatorKind = "llm"
	}
	return &ruleArbiterEvaluationRow{
		RunID:         eval.RunID,
		CandidateID:   eval.CandidateID,
		EvaluatorKind: evaluatorKind,
		Action:        string(eval.Action),
		Reason:        eval.Reason,
		Confidence:    eval.Confidence,
		ParseStatus:   string(eval.ParseStatus),
		Proposal:      objectJSONFromMap(eval.Proposal),
		RawResponse:   nullableString(eval.RawResponse),
		ErrorSummary:  nullableString(eval.ErrorSummary),
	}
}

func toRuleArbiterEvaluation(row *ruleArbiterEvaluationRow) *models.RuleArbiterEvaluation {
	return &models.RuleArbiterEvaluation{
		ID:            row.ID,
		RunID:         row.RunID,
		CandidateID:   row.CandidateID,
		EvaluatorKind: row.EvaluatorKind,
		Action:        models.RuleArbiterAction(row.Action),
		Reason:        row.Reason,
		Confidence:    row.Confidence,
		ParseStatus:   models.RuleArbiterParseStatus(row.ParseStatus),
		Proposal:      decodeObject(row.Proposal),
		RawResponse:   stringFromNull(row.RawResponse),
		ErrorSummary:  stringFromNull(row.ErrorSummary),
		CreatedAt:     row.CreatedAt,
	}
}

func toRuleVersion(row *ruleVersionRow) *models.RuleVersion {
	return &models.RuleVersion{
		ID:                     row.ID,
		FamilyID:               row.FamilyID,
		SourceCandidateID:      row.SourceCandidateID,
		ActiveBehavioralRuleID: row.ActiveBehavioralRuleID,
		Content:                row.Content,
		Summary:                stringFromNull(row.Summary),
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

func toRuleGovernanceSnapshotSummary(row *ruleGovernanceSnapshotRow) RuleGovernanceSnapshotSummary {
	return RuleGovernanceSnapshotSummary{
		SnapshotID:   row.SnapshotID,
		OpType:       row.OpType,
		Actor:        row.Actor,
		Status:       row.Status,
		CreatedAt:    row.CreatedAt,
		RolledBackAt: row.RolledBackAt,
		Pinned:       row.Pinned,
	}
}

func projectFromRuleVersionRow(row *ruleVersionRow) string {
	if row == nil {
		return ""
	}
	if value, ok := decodeObject(row.ActivationPredicate)["project"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func projectFromSnapshotRow(row *ruleGovernanceSnapshotRow) string {
	if row == nil {
		return ""
	}
	for _, state := range []ruleGovernanceSnapshotState{
		decodeRuleGovernanceSnapshotState(row.BeforeState),
		decodeRuleGovernanceSnapshotStatePtr(row.AfterState),
	} {
		if strings.TrimSpace(state.Project) != "" {
			return strings.TrimSpace(state.Project)
		}
	}
	return ""
}

type ruleCountRow struct {
	Key   string
	Count int64
}

func (s *RuleGovernanceStore) countCandidateStatuses(ctx context.Context, params RuleGovernanceHealthParams, health *RuleGovernanceHealth) error {
	q := s.db.WithContext(ctx).Table("rule_candidates").Select("status AS key, COUNT(*) AS count").Group("status")
	q = applyRuleSince(q, "created_at", params.Since)
	if params.Project != "" {
		q = q.Where("source_project = ?", params.Project)
	}
	var rows []ruleCountRow
	if err := q.Scan(&rows).Error; err != nil {
		return fmt.Errorf("rule_governance health candidate_status: %w", err)
	}
	for _, row := range rows {
		health.CandidateStatusCounts[models.RuleCandidateStatus(row.Key)] = int(row.Count)
	}

	var candidates []ruleCandidateRow
	evidenceQ := s.db.WithContext(ctx).Order("created_at ASC").Limit(params.Limit)
	evidenceQ = applyRuleSince(evidenceQ, "created_at", params.Since)
	if params.Project != "" {
		evidenceQ = evidenceQ.Where("source_project = ?", params.Project)
	}
	if err := evidenceQ.Find(&candidates).Error; err != nil {
		return fmt.Errorf("rule_governance health candidate_evidence: %w", err)
	}
	for i := range candidates {
		health.EvidenceHandles = append(health.EvidenceHandles, fmt.Sprintf("rule_candidate:%d", candidates[i].ID))
	}
	return nil
}

func (s *RuleGovernanceStore) countVersionStates(ctx context.Context, params RuleGovernanceHealthParams, health *RuleGovernanceHealth) error {
	q := s.db.WithContext(ctx).Table("rule_versions").Select("state AS key, COUNT(*) AS count").Group("state")
	q = applyRuleSince(q, "created_at", params.Since)
	if params.Project != "" {
		q = q.Where("activation_predicate_json ->> 'project' = ?", params.Project)
	}
	var rows []ruleCountRow
	if err := q.Scan(&rows).Error; err != nil {
		return fmt.Errorf("rule_governance health version_state: %w", err)
	}
	for _, row := range rows {
		health.VersionStateCounts[models.RuleVersionState(row.Key)] = int(row.Count)
	}
	return nil
}

func (s *RuleGovernanceStore) countArbiterRunStatuses(ctx context.Context, params RuleGovernanceHealthParams, health *RuleGovernanceHealth) error {
	if params.Project != "" && !params.IncludeGlobalArbiterRunCounts {
		return nil
	}
	q := s.db.WithContext(ctx).Table("rule_arbiter_runs").Select("status AS key, COUNT(*) AS count").Group("status")
	q = applyRuleSince(q, "started_at", params.Since)
	var rows []ruleCountRow
	if err := q.Scan(&rows).Error; err != nil {
		return fmt.Errorf("rule_governance health arbiter_run_status: %w", err)
	}
	for _, row := range rows {
		health.ArbiterRunStatusCounts[models.RuleArbiterRunStatus(row.Key)] = int(row.Count)
	}
	return nil
}

func (s *RuleGovernanceStore) countTransitionActions(ctx context.Context, params RuleGovernanceHealthParams, health *RuleGovernanceHealth) error {
	q := s.db.WithContext(ctx).Table("rule_transition_log AS rtl").
		Select("rtl.action AS key, COUNT(*) AS count").
		Joins("LEFT JOIN rule_candidates rc ON rc.id = rtl.candidate_id").
		Joins("LEFT JOIN rule_versions rv ON rv.id = rtl.rule_version_id").
		Group("rtl.action")
	q = applyRuleSince(q, "rtl.created_at", params.Since)
	if params.Project != "" {
		q = q.Where("rc.source_project = ? OR rv.activation_predicate_json ->> 'project' = ?", params.Project, params.Project)
	}
	var rows []ruleCountRow
	if err := q.Scan(&rows).Error; err != nil {
		return fmt.Errorf("rule_governance health transition_action: %w", err)
	}
	for _, row := range rows {
		health.TransitionActionCounts[row.Key] = int(row.Count)
	}
	return nil
}

func (s *RuleGovernanceStore) countSnapshotStatuses(ctx context.Context, params RuleGovernanceHealthParams, health *RuleGovernanceHealth) error {
	q := s.db.WithContext(ctx).Table("rule_governance_snapshots").
		Select("status AS key, COUNT(*) AS count").
		Group("status")
	q = applyRuleSince(q, "created_at", params.Since)
	if params.Project != "" {
		q = q.Where("before_state_json ->> 'project' = ? OR after_state_json ->> 'project' = ?", params.Project, params.Project)
	}
	var rows []ruleCountRow
	if err := q.Scan(&rows).Error; err != nil {
		return fmt.Errorf("rule_governance health snapshot_status: %w", err)
	}
	for _, row := range rows {
		health.SnapshotStatusCounts[row.Key] = int(row.Count)
	}
	return nil
}

func (s *RuleGovernanceStore) countInjectionEventTypes(ctx context.Context, params RuleGovernanceHealthParams, health *RuleGovernanceHealth) error {
	q := s.db.WithContext(ctx).Table("rule_injection_events").
		Select("event_type AS key, COUNT(*) AS count").
		Group("event_type")
	q = applyRuleSince(q, "created_at", params.Since)
	if params.Project != "" {
		q = q.Where("project = ?", params.Project)
	}
	var rows []ruleCountRow
	if err := q.Scan(&rows).Error; err != nil {
		return fmt.Errorf("rule_governance health injection_event_type: %w", err)
	}
	for _, row := range rows {
		health.InjectionEventTypeCounts[models.RuleInjectionEventType(row.Key)] = int(row.Count)
	}
	return nil
}

func applyRuleSince(q *gorm.DB, column string, since time.Time) *gorm.DB {
	if since.IsZero() {
		return q
	}
	return q.Where(column+" >= ?", since)
}

func healthCountTotal(health RuleGovernanceHealth) int {
	total := 0
	for _, count := range health.CandidateStatusCounts {
		total += count
	}
	for _, count := range health.VersionStateCounts {
		total += count
	}
	for _, count := range health.ArbiterRunStatusCounts {
		total += count
	}
	for _, count := range health.TransitionActionCounts {
		total += count
	}
	for _, count := range health.SnapshotStatusCounts {
		total += count
	}
	for _, count := range health.InjectionEventTypeCounts {
		total += count
	}
	return total
}

type ruleGovernanceSnapshotState struct {
	Project      string                              `json:"project"`
	RuleVersions []ruleGovernanceSnapshotRuleVersion `json:"rule_versions"`
}

type ruleGovernanceSnapshotRuleVersion struct {
	State string `json:"state"`
	ID    int64  `json:"id"`
}

func ruleGovernanceSnapshotStateForRuleVersion(row *ruleVersionRow, state models.RuleVersionState) ([]byte, error) {
	if row == nil {
		return nil, models.ErrRuleRequiredFieldMissing
	}
	return json.Marshal(ruleGovernanceSnapshotState{
		Project: projectFromRuleVersionRow(row),
		RuleVersions: []ruleGovernanceSnapshotRuleVersion{
			{
				ID:    row.ID,
				State: string(state),
			},
		},
	})
}

func decodeRuleGovernanceSnapshotState(raw JSONRaw) ruleGovernanceSnapshotState {
	var state ruleGovernanceSnapshotState
	if len(raw) == 0 {
		return state
	}
	_ = json.Unmarshal(raw, &state)
	if len(state.RuleVersions) > 0 || strings.TrimSpace(state.Project) != "" {
		return state
	}
	var legacy ruleVersionRow
	if err := json.Unmarshal(raw, &legacy); err == nil && legacy.ID > 0 {
		return ruleGovernanceSnapshotState{
			Project: projectFromRuleVersionRow(&legacy),
			RuleVersions: []ruleGovernanceSnapshotRuleVersion{{
				ID:    legacy.ID,
				State: legacy.State,
			}},
		}
	}
	return state
}

func decodeRuleGovernanceSnapshotStatePtr(raw *JSONRaw) ruleGovernanceSnapshotState {
	if raw == nil {
		return ruleGovernanceSnapshotState{}
	}
	return decodeRuleGovernanceSnapshotState(*raw)
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
	if strings.TrimSpace(req.SnapshotID) == "" || strings.TrimSpace(req.OpType) == "" || strings.TrimSpace(req.Actor) == "" {
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
	if strings.TrimSpace(req.Actor) == "" || strings.TrimSpace(string(req.ActorKind)) == "" || strings.TrimSpace(req.Reason) == "" || !hasNonBlankEvidenceHandle(req.EvidenceHandles) {
		return models.ErrRuleRequiredFieldMissing
	}
	if !req.ActorKind.IsValid() {
		return models.ErrRuleAuthorityDenied
	}
	return nil
}

func hasNonBlankEvidenceHandle(handles []string) bool {
	for _, handle := range handles {
		if strings.TrimSpace(handle) != "" {
			return true
		}
	}
	return false
}

func hasNegativeRuleArbiterCount(counts models.RuleArbiterRunCounts) bool {
	return counts.CandidatesSeen < 0 ||
		counts.CandidatesEvaluated < 0 ||
		counts.CandidatesProposed < 0 ||
		counts.CandidatesHeld < 0 ||
		counts.CandidatesRejected < 0 ||
		counts.CandidatesSkipped < 0 ||
		counts.Errors < 0
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
	if c.Status != "" && c.Status != models.RuleCandidatePending {
		return fmt.Errorf("%w: create_candidate status %s must be pending", models.ErrInvalidRuleTransition, c.Status)
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
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return true
	}
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505"
}
