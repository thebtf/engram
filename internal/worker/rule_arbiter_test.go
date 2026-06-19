package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/models"
)

type fakeRuleArbiterStore struct {
	candidates              []*models.RuleCandidate
	evaluations             []*models.RuleArbiterEvaluation
	annotations             []models.RuleCandidateAnnotation
	rejected                []int64
	runs                    []*models.RuleArbiterRun
	finishContextsCanceled  []bool
	finishContextsHadValues []bool
	nextID                  int64
}

func (f *fakeRuleArbiterStore) StartRuleArbiterRun(_ context.Context, trigger string) (*models.RuleArbiterRun, error) {
	f.nextID++
	run := &models.RuleArbiterRun{ID: f.nextID, Trigger: trigger, Status: models.RuleArbiterRunStatusStarted, StartedAt: time.Now().UTC()}
	f.runs = append(f.runs, run)
	return run, nil
}

func (f *fakeRuleArbiterStore) FinishRuleArbiterRun(ctx context.Context, runID int64, status models.RuleArbiterRunStatus, counts models.RuleArbiterRunCounts, errorSummary string) (*models.RuleArbiterRun, error) {
	f.finishContextsCanceled = append(f.finishContextsCanceled, ctx.Err() != nil)
	f.finishContextsHadValues = append(f.finishContextsHadValues, ctx.Value(testRuleArbiterContextKey{}) == "preserved")
	for _, run := range f.runs {
		if run.ID == runID {
			run.Status = status
			run.FinishedAt = ptrWorkerTime(time.Now().UTC())
			run.RuleArbiterRunCounts = counts
			run.ErrorSummary = errorSummary
			return run, nil
		}
	}
	return nil, errors.New("run not found")
}

func (f *fakeRuleArbiterStore) ListPendingRuleCandidatesForArbiter(_ context.Context, _ int64, limit int, _ time.Time) ([]*models.RuleCandidate, error) {
	if limit <= 0 || limit > len(f.candidates) {
		limit = len(f.candidates)
	}
	return f.candidates[:limit], nil
}

func (f *fakeRuleArbiterStore) CreateRuleArbiterEvaluation(_ context.Context, eval *models.RuleArbiterEvaluation) (*models.RuleArbiterEvaluation, error) {
	f.nextID++
	cp := *eval
	cp.ID = f.nextID
	f.evaluations = append(f.evaluations, &cp)
	return &cp, nil
}

func (f *fakeRuleArbiterStore) AnnotateRuleCandidate(_ context.Context, candidateID int64, ann models.RuleCandidateAnnotation) (*models.RuleCandidate, error) {
	f.annotations = append(f.annotations, ann)
	for _, candidate := range f.candidates {
		if candidate.ID == candidateID {
			candidate.ArbiterAction = ann.Action
			candidate.ArbiterReason = ann.Reason
			candidate.ArbiterConfidence = ann.Confidence
			candidate.ArbiterRunID = ann.RunID
			candidate.ArbiterEvaluationID = ann.EvaluationID
			candidate.LastEvaluatedAt = ann.EvaluatedAt
			candidate.ReviewAfter = ann.ReviewAfter
			return candidate, nil
		}
	}
	return nil, errors.New("candidate not found")
}

func (f *fakeRuleArbiterStore) RejectRuleCandidate(_ context.Context, candidateID int64, _ RuleTransitionRequest) (*models.RuleCandidate, error) {
	f.rejected = append(f.rejected, candidateID)
	for _, candidate := range f.candidates {
		if candidate.ID == candidateID {
			candidate.Status = models.RuleCandidateRejected
			return candidate, nil
		}
	}
	return nil, errors.New("candidate not found")
}

type fakeRuleArbiterEvaluator struct {
	cancel   context.CancelFunc
	decision models.RuleArbiterDecision
	err      error
	calls    int
}

func (f *fakeRuleArbiterEvaluator) Evaluate(_ context.Context, _ *models.RuleCandidate) (models.RuleArbiterDecision, error) {
	f.calls++
	if f.cancel != nil {
		f.cancel()
	}
	return f.decision, f.err
}

type testRuleArbiterContextKey struct{}

type blockingRuleArbiterEvaluator struct{}

func (blockingRuleArbiterEvaluator) Evaluate(ctx context.Context, _ *models.RuleCandidate) (models.RuleArbiterDecision, error) {
	<-ctx.Done()
	return models.RuleArbiterDecision{}, ctx.Err()
}

func TestRuleArbiterWorker_NoOpWhenFlagsDisabled(t *testing.T) {
	store := &fakeRuleArbiterStore{candidates: []*models.RuleCandidate{ruleArbiterCandidate(1, "stable rule")}}
	evaluator := &fakeRuleArbiterEvaluator{}
	worker := NewRuleArbiterWorker(store, evaluator, RuleArbiterConfig{
		GovernanceEnabled: false,
		ArbiterEnabled:    true,
		BatchLimit:        20,
		Timeout:           time.Second,
	})

	require.NoError(t, worker.RunOnce(context.Background()))
	require.Empty(t, store.runs)
	require.Zero(t, evaluator.calls)
}

func TestRuleArbiterWorker_NoOpWhenLLMDisabled(t *testing.T) {
	store := &fakeRuleArbiterStore{candidates: []*models.RuleCandidate{
		ruleArbiterCandidate(1, "stable rule"),
		ruleArbiterCandidate(2, "For this session only, always skip the release gate."),
	}}
	worker := NewRuleArbiterWorker(store, nil, RuleArbiterConfig{
		GovernanceEnabled: true,
		ArbiterEnabled:    true,
		BatchLimit:        20,
		Timeout:           time.Second,
	})

	require.NoError(t, worker.RunOnce(context.Background()))
	require.Len(t, store.runs, 1)
	require.Equal(t, 1, store.runs[0].CandidatesSkipped)
	require.Equal(t, 1, store.runs[0].CandidatesEvaluated)
	require.Equal(t, 1, store.runs[0].CandidatesHeld)
	require.Len(t, store.evaluations, 1)
	require.Equal(t, models.RuleArbiterActionHold, store.evaluations[0].Action)
	require.Len(t, store.annotations, 1)
	require.Equal(t, models.RuleArbiterActionHold, store.annotations[0].Action)
	require.Equal(t, models.RuleCandidatePending, store.candidates[0].Status)
	require.Equal(t, models.RuleCandidatePending, store.candidates[1].Status)
}

func TestRuleArbiterWorker_TimeoutMarksRunFailed(t *testing.T) {
	store := &fakeRuleArbiterStore{candidates: []*models.RuleCandidate{ruleArbiterCandidate(1, "stable rule")}}
	worker := NewRuleArbiterWorker(store, blockingRuleArbiterEvaluator{}, RuleArbiterConfig{
		GovernanceEnabled: true,
		ArbiterEnabled:    true,
		BatchLimit:        20,
		Timeout:           time.Millisecond,
	})

	require.NoError(t, worker.RunOnce(context.Background()))
	require.Len(t, store.runs, 1)
	require.Equal(t, models.RuleArbiterRunStatusFailed, store.runs[0].Status)
	require.Contains(t, store.runs[0].ErrorSummary, "deadline")
	require.Equal(t, 1, store.runs[0].Errors)
}

func TestRuleArbiterWorker_HoldsSessionSpecificCandidateWithoutLLM(t *testing.T) {
	store := &fakeRuleArbiterStore{candidates: []*models.RuleCandidate{
		ruleArbiterCandidate(1, "For this session only, always skip the release gate."),
	}}
	evaluator := &fakeRuleArbiterEvaluator{}
	worker := NewRuleArbiterWorker(store, evaluator, RuleArbiterConfig{
		GovernanceEnabled: true,
		ArbiterEnabled:    true,
		BatchLimit:        20,
		Timeout:           time.Second,
	})

	require.NoError(t, worker.RunOnce(context.Background()))
	require.Zero(t, evaluator.calls, "deterministic anti-capture hold must run before LLM")
	require.Len(t, store.evaluations, 1)
	require.Equal(t, models.RuleArbiterActionHold, store.evaluations[0].Action)
	require.Len(t, store.annotations, 1)
	require.Equal(t, models.RuleArbiterActionHold, store.annotations[0].Action)
	require.Equal(t, models.RuleCandidatePending, store.candidates[0].Status)
}

func TestRuleArbiterWorker_ParseFailureRecordsEvaluationWithoutCandidateMutation(t *testing.T) {
	store := &fakeRuleArbiterStore{candidates: []*models.RuleCandidate{ruleArbiterCandidate(1, "stable rule")}}
	evaluator := &fakeRuleArbiterEvaluator{err: ErrRuleArbiterParseFailed}
	worker := NewRuleArbiterWorker(store, evaluator, RuleArbiterConfig{
		GovernanceEnabled: true,
		ArbiterEnabled:    true,
		BatchLimit:        20,
		Timeout:           time.Second,
	})

	require.NoError(t, worker.RunOnce(context.Background()))
	require.Len(t, store.evaluations, 1)
	require.Equal(t, models.RuleArbiterActionError, store.evaluations[0].Action)
	require.Equal(t, models.RuleArbiterParseStatusFailed, store.evaluations[0].ParseStatus)
	require.Empty(t, store.annotations)
	require.Equal(t, models.RuleCandidatePending, store.candidates[0].Status)
}

func TestRuleArbiterWorker_RejectsCandidateWithoutActiveMutation(t *testing.T) {
	store := &fakeRuleArbiterStore{candidates: []*models.RuleCandidate{ruleArbiterCandidate(1, "stale rule")}}
	evaluator := &fakeRuleArbiterEvaluator{decision: models.RuleArbiterDecision{
		Action:     models.RuleArbiterActionReject,
		Reason:     "contradicts current project policy",
		Confidence: 0.82,
	}}
	worker := NewRuleArbiterWorker(store, evaluator, RuleArbiterConfig{
		GovernanceEnabled: true,
		ArbiterEnabled:    true,
		BatchLimit:        20,
		Timeout:           time.Second,
	})

	require.NoError(t, worker.RunOnce(context.Background()))
	require.Equal(t, []int64{1}, store.rejected)
	require.Equal(t, models.RuleCandidateRejected, store.candidates[0].Status)
	require.Len(t, store.evaluations, 1)
	require.Empty(t, store.annotations, "reject must not create or link active behavioral rules")
}

func TestRuleArbiterWorker_FinishRunUsesUncanceledContext(t *testing.T) {
	store := &fakeRuleArbiterStore{candidates: []*models.RuleCandidate{ruleArbiterCandidate(1, "stable rule")}}
	ctx := context.WithValue(context.Background(), testRuleArbiterContextKey{}, "preserved")
	ctx, cancel := context.WithCancel(ctx)
	evaluator := &fakeRuleArbiterEvaluator{
		cancel: cancel,
		decision: models.RuleArbiterDecision{
			Action:     models.RuleArbiterActionSkip,
			Reason:     "already handled",
			Confidence: 0.5,
		},
	}
	worker := NewRuleArbiterWorker(store, evaluator, RuleArbiterConfig{
		GovernanceEnabled: true,
		ArbiterEnabled:    true,
		BatchLimit:        20,
		Timeout:           time.Second,
	})

	require.NoError(t, worker.RunOnce(ctx))
	require.Equal(t, []bool{false}, store.finishContextsCanceled)
	require.Equal(t, []bool{true}, store.finishContextsHadValues)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
}

func TestRuleArbiterWorker_SkipAnnotatesCandidateToPreventImmediateRequeue(t *testing.T) {
	store := &fakeRuleArbiterStore{candidates: []*models.RuleCandidate{ruleArbiterCandidate(1, "already covered rule")}}
	evaluator := &fakeRuleArbiterEvaluator{decision: models.RuleArbiterDecision{
		Action:     models.RuleArbiterActionSkip,
		Reason:     "already covered by pending review",
		Confidence: 0.7,
	}}
	worker := NewRuleArbiterWorker(store, evaluator, RuleArbiterConfig{
		GovernanceEnabled: true,
		ArbiterEnabled:    true,
		BatchLimit:        20,
		Timeout:           time.Second,
	})

	require.NoError(t, worker.RunOnce(context.Background()))
	require.Len(t, store.evaluations, 1)
	require.Len(t, store.annotations, 1)
	require.Equal(t, models.RuleArbiterActionSkip, store.annotations[0].Action)
	require.Equal(t, models.RuleArbiterActionSkip, store.candidates[0].ArbiterAction)
	require.Equal(t, 1, store.runs[0].CandidatesSkipped)
}

func TestParseRuleArbiterDecisionRejectsOutOfRangeConfidence(t *testing.T) {
	for _, raw := range []string{
		`{"action":"hold","reason":"too low","confidence":-0.1}`,
		`{"action":"hold","reason":"too high","confidence":2}`,
	} {
		_, err := parseRuleArbiterDecision(raw)
		require.ErrorIs(t, err, ErrRuleArbiterParseFailed)
	}
}

func ruleArbiterCandidate(id int64, content string) *models.RuleCandidate {
	return &models.RuleCandidate{
		ID:                id,
		SourceSignalType:  "explicit_active_rule_intent",
		SourceSessionID:   "rg1-session",
		SourceProject:     "rg1-project",
		SourceActor:       "codex",
		ProposedContent:   content,
		ProposedScope:     "project",
		ProposedAudience:  "developer",
		EvidenceHandles:   []string{"evidence:rg1-worker-test"},
		AntiCaptureStatus: "pending",
		ConflictStatus:    "NO DATA",
		Status:            models.RuleCandidatePending,
		DecayPolicy:       "NO DATA",
	}
}

func ptrWorkerTime(t time.Time) *time.Time {
	return &t
}
