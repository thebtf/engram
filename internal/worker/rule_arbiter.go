package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/llm"
	"github.com/thebtf/engram/pkg/models"
)

var ErrRuleArbiterParseFailed = errors.New("rule_arbiter_parse_failed")

type RuleTransitionRequest = gormdb.RuleTransitionRequest

type RuleArbiterConfig struct {
	Timeout           time.Duration
	GovernanceEnabled bool
	ArbiterEnabled    bool
	BatchLimit        int
}

type RuleArbiterStore interface {
	StartRuleArbiterRun(ctx context.Context, trigger string) (*models.RuleArbiterRun, error)
	FinishRuleArbiterRun(ctx context.Context, runID int64, status models.RuleArbiterRunStatus, counts models.RuleArbiterRunCounts, errorSummary string) (*models.RuleArbiterRun, error)
	ListPendingRuleCandidatesForArbiter(ctx context.Context, runID int64, limit int, now time.Time) ([]*models.RuleCandidate, error)
	CreateRuleArbiterEvaluation(ctx context.Context, eval *models.RuleArbiterEvaluation) (*models.RuleArbiterEvaluation, error)
	AnnotateRuleCandidate(ctx context.Context, candidateID int64, ann models.RuleCandidateAnnotation) (*models.RuleCandidate, error)
	RejectRuleCandidate(ctx context.Context, candidateID int64, req RuleTransitionRequest) (*models.RuleCandidate, error)
}

type RuleArbiterEvaluator interface {
	Evaluate(ctx context.Context, candidate *models.RuleCandidate) (models.RuleArbiterDecision, error)
}

type RuleArbiterWorker struct {
	store     RuleArbiterStore
	evaluator RuleArbiterEvaluator
	config    RuleArbiterConfig
}

func NewRuleArbiterWorker(store RuleArbiterStore, evaluator RuleArbiterEvaluator, cfg RuleArbiterConfig) *RuleArbiterWorker {
	if cfg.BatchLimit <= 0 {
		cfg.BatchLimit = 20
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 8 * time.Second
	}
	return &RuleArbiterWorker{store: store, evaluator: evaluator, config: cfg}
}

func (w *RuleArbiterWorker) RunOnce(ctx context.Context) error {
	if w == nil || w.store == nil || !w.config.GovernanceEnabled || !w.config.ArbiterEnabled {
		return nil
	}
	runCtx, cancel := context.WithTimeout(ctx, w.config.Timeout)
	defer cancel()

	run, err := w.store.StartRuleArbiterRun(runCtx, "scheduled")
	if err != nil {
		return err
	}
	counts := models.RuleArbiterRunCounts{}
	status := models.RuleArbiterRunStatusCompleted
	errorSummary := ""

	candidates, err := w.store.ListPendingRuleCandidatesForArbiter(runCtx, run.ID, w.config.BatchLimit, time.Now().UTC())
	if err != nil {
		status = models.RuleArbiterRunStatusFailed
		errorSummary = err.Error()
		_, finishErr := w.store.FinishRuleArbiterRun(ctx, run.ID, status, counts, errorSummary)
		return errors.Join(err, finishErr)
	}
	counts.CandidatesSeen = len(candidates)

	for _, candidate := range candidates {
		if runCtx.Err() != nil {
			status = models.RuleArbiterRunStatusFailed
			errorSummary = runCtx.Err().Error()
			break
		}
		w.evaluateCandidate(runCtx, run.ID, candidate, &counts)
	}
	if runCtx.Err() != nil && status == models.RuleArbiterRunStatusCompleted {
		status = models.RuleArbiterRunStatusFailed
		errorSummary = runCtx.Err().Error()
	}

	_, err = w.store.FinishRuleArbiterRun(ctx, run.ID, status, counts, errorSummary)
	return err
}

func (w *RuleArbiterWorker) evaluateCandidate(ctx context.Context, runID int64, candidate *models.RuleCandidate, counts *models.RuleArbiterRunCounts) {
	if candidate == nil {
		counts.CandidatesSkipped++
		return
	}
	if decision, ok := deterministicRuleHold(candidate); ok {
		counts.CandidatesEvaluated++
		eval := w.recordEvaluation(ctx, runID, candidate.ID, "deterministic", decision, models.RuleArbiterParseStatusNotApplicable, "")
		if eval == nil {
			counts.Errors++
			return
		}
		if err := w.annotateCandidate(ctx, candidate.ID, runID, eval.ID, decision); err != nil {
			counts.Errors++
			log.Warn().Err(err).Int64("candidate_id", candidate.ID).Msg("rule arbiter: deterministic hold annotation failed")
			return
		}
		counts.CandidatesHeld++
		return
	}

	if w.evaluator == nil {
		counts.CandidatesSkipped++
		return
	}

	decision, err := w.evaluator.Evaluate(ctx, candidate)
	if err != nil {
		counts.CandidatesEvaluated++
		counts.Errors++
		if errors.Is(err, ErrRuleArbiterParseFailed) {
			_ = w.recordEvaluation(ctx, runID, candidate.ID, "llm", models.RuleArbiterDecision{
				Action:     models.RuleArbiterActionError,
				Reason:     "LLM arbiter response failed strict JSON parsing",
				Confidence: 0,
			}, models.RuleArbiterParseStatusFailed, err.Error())
			return
		}
		_ = w.recordEvaluation(ctx, runID, candidate.ID, "llm", models.RuleArbiterDecision{
			Action:     models.RuleArbiterActionError,
			Reason:     "LLM arbiter evaluation failed",
			Confidence: 0,
		}, models.RuleArbiterParseStatusNotApplicable, err.Error())
		return
	}

	if !decision.Action.IsProposalDecision() || strings.TrimSpace(decision.Reason) == "" {
		counts.CandidatesEvaluated++
		counts.Errors++
		_ = w.recordEvaluation(ctx, runID, candidate.ID, "llm", models.RuleArbiterDecision{
			Action:     models.RuleArbiterActionError,
			Reason:     "LLM arbiter decision omitted a valid action or reason",
			Confidence: 0,
		}, models.RuleArbiterParseStatusFailed, "invalid decision shape")
		return
	}

	counts.CandidatesEvaluated++
	eval := w.recordEvaluation(ctx, runID, candidate.ID, "llm", decision, models.RuleArbiterParseStatusOK, "")
	if eval == nil {
		counts.Errors++
		return
	}

	switch decision.Action {
	case models.RuleArbiterActionReject:
		if _, err := w.store.RejectRuleCandidate(ctx, candidate.ID, RuleTransitionRequest{
			Actor:           "rule-arbiter",
			ActorKind:       models.RuleActorBackground,
			Reason:          decision.Reason,
			EvidenceHandles: []string{fmt.Sprintf("rule_arbiter_evaluation:%d", eval.ID)},
		}); err != nil {
			counts.Errors++
			log.Warn().Err(err).Int64("candidate_id", candidate.ID).Msg("rule arbiter: reject failed")
			return
		}
		counts.CandidatesRejected++
	case models.RuleArbiterActionHold:
		if err := w.annotateCandidate(ctx, candidate.ID, runID, eval.ID, decision); err != nil {
			counts.Errors++
			log.Warn().Err(err).Int64("candidate_id", candidate.ID).Msg("rule arbiter: hold annotation failed")
			return
		}
		counts.CandidatesHeld++
	case models.RuleArbiterActionPropose:
		if err := w.annotateCandidate(ctx, candidate.ID, runID, eval.ID, decision); err != nil {
			counts.Errors++
			log.Warn().Err(err).Int64("candidate_id", candidate.ID).Msg("rule arbiter: propose annotation failed")
			return
		}
		counts.CandidatesProposed++
	case models.RuleArbiterActionSkip:
		if err := w.annotateCandidate(ctx, candidate.ID, runID, eval.ID, decision); err != nil {
			counts.Errors++
			log.Warn().Err(err).Int64("candidate_id", candidate.ID).Msg("rule arbiter: skip annotation failed")
			return
		}
		counts.CandidatesSkipped++
	default:
		counts.Errors++
	}
}

func (w *RuleArbiterWorker) recordEvaluation(ctx context.Context, runID, candidateID int64, evaluatorKind string, decision models.RuleArbiterDecision, parseStatus models.RuleArbiterParseStatus, errorSummary string) *models.RuleArbiterEvaluation {
	eval, err := w.store.CreateRuleArbiterEvaluation(ctx, &models.RuleArbiterEvaluation{
		RunID:         runID,
		CandidateID:   candidateID,
		EvaluatorKind: evaluatorKind,
		Action:        decision.Action,
		Reason:        decision.Reason,
		Confidence:    decision.Confidence,
		ParseStatus:   parseStatus,
		Proposal:      decision.Proposal,
		ErrorSummary:  errorSummary,
	})
	if err != nil {
		log.Warn().Err(err).Int64("candidate_id", candidateID).Msg("rule arbiter: evaluation write failed")
		return nil
	}
	return eval
}

func (w *RuleArbiterWorker) annotateCandidate(ctx context.Context, candidateID, runID, evaluationID int64, decision models.RuleArbiterDecision) error {
	now := time.Now().UTC()
	_, err := w.store.AnnotateRuleCandidate(ctx, candidateID, models.RuleCandidateAnnotation{
		Action:       decision.Action,
		Reason:       decision.Reason,
		Confidence:   decision.Confidence,
		RunID:        &runID,
		EvaluationID: &evaluationID,
		EvaluatedAt:  now,
	})
	return err
}

func deterministicRuleHold(candidate *models.RuleCandidate) (models.RuleArbiterDecision, bool) {
	text := strings.ToLower(candidate.ProposedContent)
	needles := []string{
		"this session only",
		"only this session",
		"for this session",
		"current session only",
		"только в этой сессии",
		"только для этой сессии",
	}
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return models.RuleArbiterDecision{
				Action:     models.RuleArbiterActionHold,
				Reason:     "Session-specific rule intent requires operator review before it can become a reusable rule",
				Confidence: 1,
				Proposal: map[string]any{
					"anti_capture": "session_specific",
				},
			}, true
		}
	}
	return models.RuleArbiterDecision{}, false
}

type LLMRuleArbiterEvaluator struct {
	client llm.Completer
}

func NewLLMRuleArbiterEvaluator(client llm.Completer) *LLMRuleArbiterEvaluator {
	return &LLMRuleArbiterEvaluator{client: client}
}

func (e *LLMRuleArbiterEvaluator) Evaluate(ctx context.Context, candidate *models.RuleCandidate) (models.RuleArbiterDecision, error) {
	if e == nil || e.client == nil {
		return models.RuleArbiterDecision{}, llm.ErrLLMDisabled
	}
	payload, err := json.Marshal(map[string]any{
		"id":                candidate.ID,
		"source_signal":     candidate.SourceSignalType,
		"source_project":    candidate.SourceProject,
		"proposed_content":  candidate.ProposedContent,
		"proposed_scope":    candidate.ProposedScope,
		"proposed_audience": candidate.ProposedAudience,
		"evidence_handles":  candidate.EvidenceHandles,
	})
	if err != nil {
		return models.RuleArbiterDecision{}, fmt.Errorf("rule arbiter marshal candidate: %w", err)
	}
	raw, err := e.client.Complete(ctx, ruleArbiterSystemPrompt, string(payload))
	if err != nil {
		return models.RuleArbiterDecision{}, err
	}
	decision, err := parseRuleArbiterDecision(raw)
	if err != nil {
		return models.RuleArbiterDecision{}, err
	}
	return decision, nil
}

const ruleArbiterSystemPrompt = `You are Engram's background rule-governance arbiter.
Evaluate one pending rule candidate. You are proposal-only: never activate, promote, or persist a behavioral rule.
Return exactly one JSON object with:
  "action": "propose" | "hold" | "reject" | "skip"
  "reason": short reason
  "confidence": number from 0 to 1
  "proposal": optional object with operator-facing notes
Use "hold" for session-specific, ambiguous, conflicting, or insufficiently evidenced candidates.
Use "reject" only when the candidate is clearly unsafe, obsolete, or contradicted.
Use "propose" only when it is reusable and well-scoped.
No markdown fences, no prose.`

func parseRuleArbiterDecision(raw string) (models.RuleArbiterDecision, error) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(raw)))
	decoder.DisallowUnknownFields()
	var parsed struct {
		Proposal   map[string]any           `json:"proposal"`
		Action     models.RuleArbiterAction `json:"action"`
		Reason     string                   `json:"reason"`
		Confidence float64                  `json:"confidence"`
	}
	if err := decoder.Decode(&parsed); err != nil {
		return models.RuleArbiterDecision{}, fmt.Errorf("%w: %v", ErrRuleArbiterParseFailed, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return models.RuleArbiterDecision{}, fmt.Errorf("%w: trailing JSON content", ErrRuleArbiterParseFailed)
	}
	if !parsed.Action.IsProposalDecision() || strings.TrimSpace(parsed.Reason) == "" {
		return models.RuleArbiterDecision{}, fmt.Errorf("%w: missing valid action or reason", ErrRuleArbiterParseFailed)
	}
	if parsed.Confidence < 0 || parsed.Confidence > 1 {
		return models.RuleArbiterDecision{}, fmt.Errorf("%w: confidence must be between 0 and 1", ErrRuleArbiterParseFailed)
	}
	return models.RuleArbiterDecision{
		Action:     parsed.Action,
		Reason:     strings.TrimSpace(parsed.Reason),
		Confidence: parsed.Confidence,
		Proposal:   parsed.Proposal,
	}, nil
}

func (s *Service) startRuleArbiterWorker(ctx context.Context, store *gormdb.RuleGovernanceStore) {
	if s.config == nil || store == nil || !s.config.RuleGovernanceEnabled || !s.config.RuleArbiterEnabled {
		return
	}

	var evaluator RuleArbiterEvaluator
	client, err := llm.NewClient()
	if err != nil {
		if errors.Is(err, llm.ErrLLMDisabled) {
			log.Info().Msg("rule arbiter: LLM disabled, runs will skip candidates")
		} else {
			log.Warn().Err(err).Msg("rule arbiter: failed to create LLM client, runs will skip candidates")
		}
	} else {
		evaluator = NewLLMRuleArbiterEvaluator(client)
	}

	interval := time.Duration(s.config.RuleArbiterIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	worker := NewRuleArbiterWorker(store, evaluator, RuleArbiterConfig{
		GovernanceEnabled: true,
		ArbiterEnabled:    true,
		BatchLimit:        s.config.RuleArbiterBatchLimit,
		Timeout:           time.Duration(s.config.RuleArbiterTimeoutMS) * time.Millisecond,
	})

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		runRuleArbiterOnce(ctx, worker)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runRuleArbiterOnce(ctx, worker)
			}
		}
	}()
}

func runRuleArbiterOnce(ctx context.Context, worker *RuleArbiterWorker) {
	if err := worker.RunOnce(ctx); err != nil {
		log.Warn().Err(err).Msg("rule arbiter: run failed")
	}
}
