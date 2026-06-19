package gorm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

func TestMigration145_RuleArbiterBackgroundTables(t *testing.T) {
	db := openCandidateTestDB(t)
	requireRuleArbiterTableState(t, db, true)
	require.True(t, db.Migrator().HasColumn(&ruleCandidateRow{}, "arbiter_action"))
	require.True(t, db.Migrator().HasColumn(&ruleCandidateRow{}, "arbiter_reason"))
	require.True(t, db.Migrator().HasColumn(&ruleCandidateRow{}, "arbiter_confidence"))
}

func TestMigration145_RuleArbiterBackgroundRollbackAndReapply(t *testing.T) {
	db := openCandidateTestDB(t)
	migration := ruleArbiterBackgroundMigration145()
	t.Cleanup(func() {
		_ = migration.Migrate(db)
	})

	requireRuleArbiterTableState(t, db, true)
	require.NoError(t, migration.Rollback(db))
	requireRuleArbiterTableState(t, db, false)
	require.NoError(t, migration.Migrate(db))
	requireRuleArbiterTableState(t, db, true)
}

func TestRuleGovernanceStore_ArbiterRunEvaluationAndCandidateAnnotation(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()

	candidate, err := store.CreateRuleCandidate(ctx, ruleGovernanceCandidate("arbiter-annotation"))
	require.NoError(t, err)

	run, err := store.StartRuleArbiterRun(ctx, "unit-test")
	require.NoError(t, err)
	require.NotZero(t, run.ID)
	require.Equal(t, models.RuleArbiterRunStatusStarted, run.Status)
	claimed := claimRuleArbiterCandidates(t, store, run.ID, time.Now().UTC())
	require.Contains(t, ruleCandidateIDs(claimed), candidate.ID)

	eval, err := store.CreateRuleArbiterEvaluation(ctx, &models.RuleArbiterEvaluation{
		RunID:       run.ID,
		CandidateID: candidate.ID,
		Action:      models.RuleArbiterActionHold,
		Reason:      "session-specific phrasing needs human review",
		Confidence:  0.91,
		ParseStatus: models.RuleArbiterParseStatusNotApplicable,
		Proposal:    map[string]any{"note": "held deterministically"},
	})
	require.NoError(t, err)
	require.NotZero(t, eval.ID)

	annotated, err := store.AnnotateRuleCandidate(ctx, candidate.ID, models.RuleCandidateAnnotation{
		Action:       models.RuleArbiterActionHold,
		Reason:       "session-specific phrasing needs human review",
		Confidence:   0.91,
		RunID:        &run.ID,
		EvaluationID: &eval.ID,
		EvaluatedAt:  time.Now().UTC(),
		ReviewAfter:  ptrTime(time.Now().UTC().Add(time.Hour)),
	})
	require.NoError(t, err)
	require.Equal(t, models.RuleArbiterActionHold, annotated.ArbiterAction)
	require.Equal(t, "session-specific phrasing needs human review", annotated.ArbiterReason)
	require.InDelta(t, 0.91, annotated.ArbiterConfidence, 0.0001)
	require.Equal(t, run.ID, *annotated.ArbiterRunID)
	require.Equal(t, eval.ID, *annotated.ArbiterEvaluationID)
	require.False(t, annotated.LastEvaluatedAt.IsZero())

	finished, err := store.FinishRuleArbiterRun(ctx, run.ID, models.RuleArbiterRunStatusCompleted, models.RuleArbiterRunCounts{
		CandidatesSeen:      1,
		CandidatesEvaluated: 1,
		CandidatesHeld:      1,
	}, "")
	require.NoError(t, err)
	require.Equal(t, models.RuleArbiterRunStatusCompleted, finished.Status)
	require.NotNil(t, finished.FinishedAt)
	require.Equal(t, 1, finished.CandidatesHeld)
}

func TestRuleGovernanceStore_ArbiterRejectsTerminalRunEvaluation(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()

	candidate, err := store.CreateRuleCandidate(ctx, ruleGovernanceCandidate("arbiter-terminal-run"))
	require.NoError(t, err)
	run, err := store.StartRuleArbiterRun(ctx, "unit-test")
	require.NoError(t, err)
	claimed := claimRuleArbiterCandidates(t, store, run.ID, time.Now().UTC())
	require.Contains(t, ruleCandidateIDs(claimed), candidate.ID)

	_, err = store.FinishRuleArbiterRun(ctx, run.ID, models.RuleArbiterRunStatusCompleted, models.RuleArbiterRunCounts{}, "")
	require.NoError(t, err)

	_, err = store.CreateRuleArbiterEvaluation(ctx, &models.RuleArbiterEvaluation{
		RunID:       run.ID,
		CandidateID: candidate.ID,
		Action:      models.RuleArbiterActionHold,
		Reason:      "late evaluation should fail",
		ParseStatus: models.RuleArbiterParseStatusOK,
	})
	require.ErrorIs(t, err, models.ErrInvalidRuleTransition)

	_, err = store.FinishRuleArbiterRun(ctx, run.ID, models.RuleArbiterRunStatusFailed, models.RuleArbiterRunCounts{}, "second finish")
	require.ErrorIs(t, err, models.ErrInvalidRuleTransition)
}

func TestRuleGovernanceStore_AnnotateRequiresMatchingEvaluation(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()

	candidate, err := store.CreateRuleCandidate(ctx, ruleGovernanceCandidate("arbiter-annotation-mismatch"))
	require.NoError(t, err)
	other, err := store.CreateRuleCandidate(ctx, ruleGovernanceCandidate("arbiter-annotation-other"))
	require.NoError(t, err)
	run, err := store.StartRuleArbiterRun(ctx, "unit-test")
	require.NoError(t, err)
	claimed := claimRuleArbiterCandidates(t, store, run.ID, time.Now().UTC())
	require.Contains(t, ruleCandidateIDs(claimed), candidate.ID)
	require.Contains(t, ruleCandidateIDs(claimed), other.ID)
	eval, err := store.CreateRuleArbiterEvaluation(ctx, &models.RuleArbiterEvaluation{
		RunID:       run.ID,
		CandidateID: other.ID,
		Action:      models.RuleArbiterActionHold,
		Reason:      "belongs to another candidate",
		ParseStatus: models.RuleArbiterParseStatusOK,
	})
	require.NoError(t, err)

	_, err = store.AnnotateRuleCandidate(ctx, candidate.ID, models.RuleCandidateAnnotation{
		Action:       models.RuleArbiterActionHold,
		Reason:       "mismatched eval should fail",
		RunID:        &run.ID,
		EvaluationID: &eval.ID,
		EvaluatedAt:  time.Now().UTC(),
	})
	require.ErrorIs(t, err, models.ErrInvalidRuleTransition)
}

func TestRuleGovernanceStore_AnnotatedCandidateWaitsUntilReviewAfter(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()

	candidate, err := store.CreateRuleCandidate(ctx, ruleGovernanceCandidate("arbiter-requeue"))
	require.NoError(t, err)
	run, err := store.StartRuleArbiterRun(ctx, "unit-test")
	require.NoError(t, err)
	claimed := claimRuleArbiterCandidates(t, store, run.ID, time.Now().UTC())
	require.Contains(t, ruleCandidateIDs(claimed), candidate.ID)
	eval, err := store.CreateRuleArbiterEvaluation(ctx, &models.RuleArbiterEvaluation{
		RunID:       run.ID,
		CandidateID: candidate.ID,
		Action:      models.RuleArbiterActionHold,
		Reason:      "wait for more evidence",
		ParseStatus: models.RuleArbiterParseStatusOK,
	})
	require.NoError(t, err)

	future := time.Now().UTC().Add(time.Hour)
	_, err = store.AnnotateRuleCandidate(ctx, candidate.ID, models.RuleCandidateAnnotation{
		Action:       models.RuleArbiterActionHold,
		Reason:       "wait for more evidence",
		RunID:        &run.ID,
		EvaluationID: &eval.ID,
		EvaluatedAt:  time.Now().UTC(),
		ReviewAfter:  &future,
	})
	require.NoError(t, err)

	nextRun, err := store.StartRuleArbiterRun(ctx, "unit-test-requeue")
	require.NoError(t, err)
	dueNow, err := store.ListPendingRuleCandidatesForArbiter(ctx, nextRun.ID, 20, time.Now().UTC())
	require.NoError(t, err)
	require.NotContains(t, ruleCandidateIDs(dueNow), candidate.ID)
	_, err = store.FinishRuleArbiterRun(ctx, nextRun.ID, models.RuleArbiterRunStatusCompleted, models.RuleArbiterRunCounts{}, "")
	require.NoError(t, err)

	laterRun, err := store.StartRuleArbiterRun(ctx, "unit-test-requeue-later")
	require.NoError(t, err)
	dueLater, err := store.ListPendingRuleCandidatesForArbiter(ctx, laterRun.ID, 20, future.Add(time.Second))
	require.NoError(t, err)
	require.Contains(t, ruleCandidateIDs(dueLater), candidate.ID)
}

func TestRuleGovernanceStore_ArbiterRejectsUnclaimedEvaluationAndConcurrentClaim(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()

	candidate, err := store.CreateRuleCandidate(ctx, ruleGovernanceCandidate("arbiter-claim-race"))
	require.NoError(t, err)
	firstRun, err := store.StartRuleArbiterRun(ctx, "unit-test-claim-first")
	require.NoError(t, err)
	firstClaimed := claimRuleArbiterCandidates(t, store, firstRun.ID, time.Now().UTC())
	require.Contains(t, ruleCandidateIDs(firstClaimed), candidate.ID)

	secondRun, err := store.StartRuleArbiterRun(ctx, "unit-test-claim-second")
	require.NoError(t, err)
	secondClaimed, err := store.ListPendingRuleCandidatesForArbiter(ctx, secondRun.ID, 20, time.Now().UTC())
	require.NoError(t, err)
	require.NotContains(t, ruleCandidateIDs(secondClaimed), candidate.ID)

	_, err = store.CreateRuleArbiterEvaluation(ctx, &models.RuleArbiterEvaluation{
		RunID:       secondRun.ID,
		CandidateID: candidate.ID,
		Action:      models.RuleArbiterActionHold,
		Reason:      "second run must not evaluate a candidate claimed by the first run",
		Confidence:  0.5,
		ParseStatus: models.RuleArbiterParseStatusOK,
	})
	require.ErrorIs(t, err, models.ErrInvalidRuleTransition)

	_, err = store.FinishRuleArbiterRun(ctx, firstRun.ID, models.RuleArbiterRunStatusCompleted, models.RuleArbiterRunCounts{}, "")
	require.NoError(t, err)
	afterClear, err := store.ListPendingRuleCandidatesForArbiter(ctx, secondRun.ID, 20, time.Now().UTC())
	require.NoError(t, err)
	require.Contains(t, ruleCandidateIDs(afterClear), candidate.ID)
}

func TestRuleGovernanceStore_ArbiterRejectsInvalidConfidence(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()

	candidate, err := store.CreateRuleCandidate(ctx, ruleGovernanceCandidate("arbiter-confidence"))
	require.NoError(t, err)
	run, err := store.StartRuleArbiterRun(ctx, "unit-test-confidence")
	require.NoError(t, err)
	claimed := claimRuleArbiterCandidates(t, store, run.ID, time.Now().UTC())
	require.Contains(t, ruleCandidateIDs(claimed), candidate.ID)

	_, err = store.CreateRuleArbiterEvaluation(ctx, &models.RuleArbiterEvaluation{
		RunID:       run.ID,
		CandidateID: candidate.ID,
		Action:      models.RuleArbiterActionHold,
		Reason:      "confidence below range should fail",
		Confidence:  -0.01,
		ParseStatus: models.RuleArbiterParseStatusOK,
	})
	require.ErrorIs(t, err, models.ErrRuleRequiredFieldMissing)

	eval, err := store.CreateRuleArbiterEvaluation(ctx, &models.RuleArbiterEvaluation{
		RunID:       run.ID,
		CandidateID: candidate.ID,
		Action:      models.RuleArbiterActionHold,
		Reason:      "valid confidence",
		Confidence:  0.5,
		ParseStatus: models.RuleArbiterParseStatusOK,
	})
	require.NoError(t, err)

	_, err = store.AnnotateRuleCandidate(ctx, candidate.ID, models.RuleCandidateAnnotation{
		Action:       models.RuleArbiterActionHold,
		Reason:       "confidence above range should fail",
		Confidence:   1.01,
		RunID:        &run.ID,
		EvaluationID: &eval.ID,
		EvaluatedAt:  time.Now().UTC(),
	})
	require.ErrorIs(t, err, models.ErrRuleRequiredFieldMissing)
}

func requireRuleArbiterTableState(t *testing.T, db *gorm.DB, exists bool) {
	t.Helper()
	for _, table := range []string{"rule_arbiter_runs", "rule_arbiter_evaluations"} {
		require.Equal(t, exists, db.Migrator().HasTable(table), "table state mismatch for %s", table)
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

func claimRuleArbiterCandidates(t *testing.T, store *RuleGovernanceStore, runID int64, now time.Time) []*models.RuleCandidate {
	t.Helper()
	claimed, err := store.ListPendingRuleCandidatesForArbiter(context.Background(), runID, 20, now)
	require.NoError(t, err)
	return claimed
}

func ruleCandidateIDs(candidates []*models.RuleCandidate) []int64 {
	ids := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.ID)
	}
	return ids
}
