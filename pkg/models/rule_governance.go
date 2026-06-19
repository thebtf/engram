// Package models contains domain models for engram.
package models

import (
	"errors"
	"strings"
	"time"
)

const (
	RuleEscapeNoData = "NO DATA"
	RuleEscapeNull   = "null"
)

var (
	ErrInvalidRuleTransition    = errors.New("invalid_rule_transition")
	ErrRuleAuthorityDenied      = errors.New("rule_authority_denied")
	ErrRuleSnapshotRequired     = errors.New("rule_snapshot_required")
	ErrRuleRequiredFieldMissing = errors.New("rule_required_field_missing")
	ErrRuleInvalidEscape        = errors.New("rule_invalid_escape")
	ErrRuleDuplicateFingerprint = errors.New("rule_duplicate_fingerprint")
)

type RuleCandidateStatus string

const (
	RuleCandidatePending    RuleCandidateStatus = "pending"
	RuleCandidateDrafted    RuleCandidateStatus = "drafted"
	RuleCandidateRejected   RuleCandidateStatus = "rejected"
	RuleCandidateDuplicate  RuleCandidateStatus = "duplicate"
	RuleCandidateSuperseded RuleCandidateStatus = "superseded"
)

func (s RuleCandidateStatus) IsValid() bool {
	switch s {
	case RuleCandidatePending, RuleCandidateDrafted, RuleCandidateRejected,
		RuleCandidateDuplicate, RuleCandidateSuperseded:
		return true
	default:
		return false
	}
}

type RuleArbiterAction string

const (
	RuleArbiterActionPropose RuleArbiterAction = "propose"
	RuleArbiterActionHold    RuleArbiterAction = "hold"
	RuleArbiterActionReject  RuleArbiterAction = "reject"
	RuleArbiterActionSkip    RuleArbiterAction = "skip"
	RuleArbiterActionError   RuleArbiterAction = "error"
)

func (a RuleArbiterAction) IsValid() bool {
	switch a {
	case RuleArbiterActionPropose, RuleArbiterActionHold, RuleArbiterActionReject,
		RuleArbiterActionSkip, RuleArbiterActionError:
		return true
	default:
		return false
	}
}

type RuleArbiterRunStatus string

const (
	RuleArbiterRunStatusStarted   RuleArbiterRunStatus = "started"
	RuleArbiterRunStatusCompleted RuleArbiterRunStatus = "completed"
	RuleArbiterRunStatusFailed    RuleArbiterRunStatus = "failed"
)

func (s RuleArbiterRunStatus) IsValid() bool {
	switch s {
	case RuleArbiterRunStatusStarted, RuleArbiterRunStatusCompleted, RuleArbiterRunStatusFailed:
		return true
	default:
		return false
	}
}

type RuleArbiterParseStatus string

const (
	RuleArbiterParseStatusOK            RuleArbiterParseStatus = "ok"
	RuleArbiterParseStatusFailed        RuleArbiterParseStatus = "failed"
	RuleArbiterParseStatusNotApplicable RuleArbiterParseStatus = "not_applicable"
)

func (s RuleArbiterParseStatus) IsValid() bool {
	switch s {
	case RuleArbiterParseStatusOK, RuleArbiterParseStatusFailed, RuleArbiterParseStatusNotApplicable:
		return true
	default:
		return false
	}
}

type RuleVersionState string

const (
	RuleStateDraft         RuleVersionState = "draft"
	RuleStateShadow        RuleVersionState = "shadow"
	RuleStateCanary        RuleVersionState = "canary"
	RuleStateActiveProject RuleVersionState = "active_project"
	RuleStateActiveShared  RuleVersionState = "active_shared"
	RuleStateActiveGlobal  RuleVersionState = "active_global"
	RuleStateKernel        RuleVersionState = "kernel"
	RuleStateSuperseded    RuleVersionState = "superseded"
	RuleStateArchived      RuleVersionState = "archived"
	RuleStateRejected      RuleVersionState = "rejected"
)

func (s RuleVersionState) IsValid() bool {
	_, ok := validRuleVersionTransitions[s]
	return ok
}

type RuleActorKind string

const (
	RuleActorAgent      RuleActorKind = "agent"
	RuleActorOperator   RuleActorKind = "operator"
	RuleActorAdmin      RuleActorKind = "admin"
	RuleActorSystem     RuleActorKind = "system"
	RuleActorBackground RuleActorKind = "background"
	RuleActorLLM        RuleActorKind = "llm"
)

func (k RuleActorKind) IsValid() bool {
	switch k {
	case RuleActorAgent, RuleActorOperator, RuleActorAdmin, RuleActorSystem,
		RuleActorBackground, RuleActorLLM:
		return true
	default:
		return false
	}
}

type RuleTransitionRequest struct {
	Actor           string        `json:"actor"`
	ActorKind       RuleActorKind `json:"actor_kind"`
	Reason          string        `json:"reason"`
	EvidenceHandles []string      `json:"evidence_handles,omitempty"`
	SnapshotID      string        `json:"snapshot_id,omitempty"`
}

type RuleCandidate struct {
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
	ReviewAfter         *time.Time          `json:"review_after,omitempty"`
	LastEvaluatedAt     time.Time           `json:"last_evaluated_at,omitempty"`
	ArbiterRunID        *int64              `json:"arbiter_run_id,omitempty"`
	ArbiterEvaluationID *int64              `json:"arbiter_evaluation_id,omitempty"`
	ActivationPredicate map[string]any      `json:"activation_predicate,omitempty"`
	EvidenceHandles     []string            `json:"evidence_handles,omitempty"`
	SourceSignalType    string              `json:"source_signal_type"`
	SourceSessionID     string              `json:"source_session_id,omitempty"`
	SourceProject       string              `json:"source_project,omitempty"`
	SourceActor         string              `json:"source_actor"`
	ProposedContent     string              `json:"proposed_content"`
	ProposedScope       string              `json:"proposed_scope"`
	ProposedAudience    string              `json:"proposed_audience"`
	ArbiterAction       RuleArbiterAction   `json:"arbiter_action,omitempty"`
	ArbiterReason       string              `json:"arbiter_reason,omitempty"`
	AntiCaptureStatus   string              `json:"anti_capture_status"`
	AntiCaptureReason   string              `json:"anti_capture_reason,omitempty"`
	ConflictStatus      string              `json:"conflict_status"`
	Status              RuleCandidateStatus `json:"status"`
	Fingerprint         string              `json:"fingerprint,omitempty"`
	DecayPolicy         string              `json:"decay_policy"`
	ID                  int64               `json:"id"`
	ArbiterConfidence   float64             `json:"arbiter_confidence,omitempty"`
	Confidence          float64             `json:"confidence,omitempty"`
	RecurrenceCount     int                 `json:"recurrence_count,omitempty"`
}

type RuleArbiterRunCounts struct {
	CandidatesSeen      int `json:"candidates_seen"`
	CandidatesEvaluated int `json:"candidates_evaluated"`
	CandidatesProposed  int `json:"candidates_proposed"`
	CandidatesHeld      int `json:"candidates_held"`
	CandidatesRejected  int `json:"candidates_rejected"`
	CandidatesSkipped   int `json:"candidates_skipped"`
	Errors              int `json:"errors"`
}

type RuleArbiterRun struct {
	StartedAt            time.Time            `json:"started_at"`
	FinishedAt           *time.Time           `json:"finished_at,omitempty"`
	Trigger              string               `json:"trigger"`
	Status               RuleArbiterRunStatus `json:"status"`
	ErrorSummary         string               `json:"error_summary,omitempty"`
	ID                   int64                `json:"id"`
	RuleArbiterRunCounts                      // flattened intentionally for DB count fields
}

type RuleArbiterEvaluation struct {
	CreatedAt     time.Time              `json:"created_at"`
	Proposal      map[string]any         `json:"proposal,omitempty"`
	RawResponse   string                 `json:"raw_response,omitempty"`
	ErrorSummary  string                 `json:"error_summary,omitempty"`
	Action        RuleArbiterAction      `json:"action"`
	Reason        string                 `json:"reason"`
	ParseStatus   RuleArbiterParseStatus `json:"parse_status"`
	EvaluatorKind string                 `json:"evaluator_kind"`
	ID            int64                  `json:"id"`
	RunID         int64                  `json:"run_id"`
	CandidateID   int64                  `json:"candidate_id"`
	Confidence    float64                `json:"confidence"`
}

type RuleCandidateAnnotation struct {
	EvaluatedAt  time.Time
	ReviewAfter  *time.Time
	RunID        *int64
	EvaluationID *int64
	Action       RuleArbiterAction
	Reason       string
	Confidence   float64
}

type RuleArbiterDecision struct {
	Proposal   map[string]any    `json:"proposal,omitempty"`
	Action     RuleArbiterAction `json:"action"`
	Reason     string            `json:"reason"`
	Confidence float64           `json:"confidence"`
}

type RuleVersion struct {
	CreatedAt              time.Time        `json:"created_at"`
	UpdatedAt              time.Time        `json:"updated_at"`
	LastEvaluatedAt        *time.Time       `json:"last_evaluated_at,omitempty"`
	EffectiveFrom          *time.Time       `json:"effective_from,omitempty"`
	EffectiveUntil         *time.Time       `json:"effective_until,omitempty"`
	ArchivedAt             *time.Time       `json:"archived_at,omitempty"`
	ActivationPredicate    map[string]any   `json:"activation_predicate,omitempty"`
	EvidenceHandles        []string         `json:"evidence_handles,omitempty"`
	Content                string           `json:"content"`
	Summary                string           `json:"summary,omitempty"`
	Scope                  string           `json:"scope"`
	Owner                  string           `json:"owner"`
	Audience               string           `json:"audience"`
	State                  RuleVersionState `json:"state"`
	BudgetClass            string           `json:"budget_class"`
	AntiCaptureStatus      string           `json:"anti_capture_status"`
	ConflictStatus         string           `json:"conflict_status"`
	DecayPolicy            string           `json:"decay_policy"`
	ID                     int64            `json:"id"`
	FamilyID               int64            `json:"family_id"`
	SourceCandidateID      *int64           `json:"source_candidate_id,omitempty"`
	ActiveBehavioralRuleID *int64           `json:"active_behavioral_rule_id,omitempty"`
	SupersedesVersionID    *int64           `json:"supersedes_version_id,omitempty"`
	Priority               int              `json:"priority"`
	Protected              bool             `json:"protected"`
	Pinned                 bool             `json:"pinned"`
}

type RuleGovernanceSnapshot struct {
	CreatedAt    time.Time  `json:"created_at"`
	RolledBackAt *time.Time `json:"rolled_back_at,omitempty"`
	SnapshotID   string     `json:"snapshot_id"`
	OpType       string     `json:"op_type"`
	Actor        string     `json:"actor"`
	BeforeState  []byte     `json:"before_state"`
	AfterState   []byte     `json:"after_state,omitempty"`
	Status       string     `json:"status"`
	ID           int64      `json:"id"`
	Pinned       bool       `json:"pinned"`
}

func IsRuleLegalEscape(value string) bool {
	if value == RuleEscapeNoData || value == RuleEscapeNull {
		return true
	}
	for _, prefix := range []string{"HYPOTHESIS: ", "BLOCKED: ", "NEEDS CLARIFICATION: "} {
		if strings.HasPrefix(value, prefix) && strings.TrimSpace(strings.TrimPrefix(value, prefix)) != "" {
			return true
		}
	}
	return false
}

func ValidateRuleRequiredField(value string) error {
	if strings.TrimSpace(value) == "" {
		return ErrRuleRequiredFieldMissing
	}
	if strings.HasPrefix(value, "HYPOTHESIS") ||
		strings.HasPrefix(value, "BLOCKED") ||
		strings.HasPrefix(value, "NEEDS CLARIFICATION") {
		if !IsRuleLegalEscape(value) {
			return ErrRuleInvalidEscape
		}
	}
	return nil
}

func RequiresRuleSnapshot(from, to RuleVersionState) bool {
	return isRuleActiveState(from) || isRuleActiveState(to)
}

func ValidateRuleActorAuthority(from, to RuleVersionState, req RuleTransitionRequest) error {
	if !req.ActorKind.IsValid() {
		return ErrRuleAuthorityDenied
	}
	if isRuleActiveState(to) {
		switch req.ActorKind {
		case RuleActorBackground, RuleActorLLM, RuleActorSystem:
			return ErrRuleAuthorityDenied
		}
	}
	if to == RuleStateKernel {
		switch req.ActorKind {
		case RuleActorOperator, RuleActorAdmin:
			return nil
		default:
			return ErrRuleAuthorityDenied
		}
	}
	return nil
}

func ValidateRuleVersionTransition(from, to RuleVersionState, req RuleTransitionRequest) error {
	if !validRuleVersionTransitions[from][to] {
		return ErrInvalidRuleTransition
	}
	if err := ValidateRuleActorAuthority(from, to, req); err != nil {
		return err
	}
	if err := validateRuleTransitionRequest(req, RequiresRuleSnapshot(from, to)); err != nil {
		return err
	}
	return nil
}

func validateRuleTransitionRequest(req RuleTransitionRequest, requireSnapshot bool) error {
	if strings.TrimSpace(req.Actor) == "" ||
		strings.TrimSpace(string(req.ActorKind)) == "" ||
		strings.TrimSpace(req.Reason) == "" ||
		!hasNonBlankRuleEvidence(req.EvidenceHandles) {
		return ErrRuleRequiredFieldMissing
	}
	if !req.ActorKind.IsValid() {
		return ErrRuleAuthorityDenied
	}
	if requireSnapshot && strings.TrimSpace(req.SnapshotID) == "" {
		return ErrRuleSnapshotRequired
	}
	return nil
}

func hasNonBlankRuleEvidence(handles []string) bool {
	for _, handle := range handles {
		if strings.TrimSpace(handle) != "" {
			return true
		}
	}
	return false
}

func isRuleActiveState(state RuleVersionState) bool {
	switch state {
	case RuleStateActiveProject, RuleStateActiveShared, RuleStateActiveGlobal, RuleStateKernel:
		return true
	default:
		return false
	}
}

var validRuleVersionTransitions = map[RuleVersionState]map[RuleVersionState]bool{
	RuleStateDraft: {
		RuleStateShadow:   true,
		RuleStateRejected: true,
		RuleStateArchived: true,
	},
	RuleStateShadow: {
		RuleStateCanary:   true,
		RuleStateRejected: true,
		RuleStateArchived: true,
	},
	RuleStateCanary: {
		RuleStateActiveProject: true,
		RuleStateRejected:      true,
		RuleStateArchived:      true,
	},
	RuleStateActiveProject: {
		RuleStateActiveShared: true,
		RuleStateSuperseded:   true,
		RuleStateArchived:     true,
	},
	RuleStateActiveShared: {
		RuleStateActiveGlobal: true,
		RuleStateSuperseded:   true,
		RuleStateArchived:     true,
	},
	RuleStateActiveGlobal: {
		RuleStateKernel:     true,
		RuleStateSuperseded: true,
		RuleStateArchived:   true,
	},
	RuleStateKernel: {
		RuleStateSuperseded: true,
		RuleStateArchived:   true,
	},
	RuleStateSuperseded: {},
	RuleStateArchived:   {},
	RuleStateRejected:   {},
}
