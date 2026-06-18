package gorm

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

func TestMigration144_RuleGovernanceTables(t *testing.T) {
	db := openCandidateTestDB(t)

	for _, table := range []string{
		"rule_candidates",
		"rule_families",
		"rule_versions",
		"rule_transition_log",
		"rule_governance_snapshots",
	} {
		var exists bool
		require.NoError(t, db.Raw(`SELECT to_regclass(?) IS NOT NULL`, table).Scan(&exists).Error)
		require.True(t, exists, "expected table %s to exist", table)
	}
}

func TestRuleGovernanceStore_CandidateIdempotencyAndNoBehavioralRuleProjection(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()

	candidate := &models.RuleCandidate{
		SourceSignalType:    "explicit_agent_proposal",
		SourceSessionID:     "rg0-session-idempotency",
		SourceProject:       "rg0-project",
		SourceActor:         "codex",
		ProposedContent:     "Always verify rule governance transitions with tests.",
		ProposedScope:       "project",
		ProposedAudience:    "developer",
		EvidenceHandles:     []string{"evidence:rg0-test"},
		AntiCaptureStatus:   "passed",
		ConflictStatus:      "none",
		Status:              models.RuleCandidatePending,
		Fingerprint:         "rg0-idempotent-fingerprint",
		DecayPolicy:         "NO DATA",
		LastEvaluatedAt:     time.Now().UTC(),
		ActivationPredicate: map[string]any{"project": "rg0-project"},
	}

	created, err := store.CreateRuleCandidate(ctx, candidate)
	require.NoError(t, err)
	createdAgain, err := store.CreateRuleCandidate(ctx, candidate)
	require.NoError(t, err)
	require.Equal(t, created.ID, createdAgain.ID, "duplicate fingerprint must return existing candidate")

	var behavioralRuleCount int64
	require.NoError(t, db.Table("behavioral_rules").
		Where("content = ?", candidate.ProposedContent).
		Count(&behavioralRuleCount).Error)
	require.Zero(t, behavioralRuleCount, "rule candidates must not create active behavioral_rules")
}

func TestRuleGovernanceStore_CreateDraftFromCandidateIsIdempotent(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()

	candidate := &models.RuleCandidate{
		SourceSignalType:    "explicit_agent_proposal",
		SourceSessionID:     "rg0-session-draft",
		SourceProject:       "rg0-project",
		SourceActor:         "codex",
		ProposedContent:     "Treat global rule promotion as privileged.",
		ProposedScope:       "project",
		ProposedAudience:    "developer",
		EvidenceHandles:     []string{"evidence:rg0-draft"},
		AntiCaptureStatus:   "passed",
		ConflictStatus:      "none",
		Status:              models.RuleCandidatePending,
		Fingerprint:         "rg0-draft-fingerprint",
		DecayPolicy:         "NO DATA",
		ActivationPredicate: map[string]any{"project": "rg0-project"},
	}
	created, err := store.CreateRuleCandidate(ctx, candidate)
	require.NoError(t, err)

	req := RuleTransitionRequest{
		Actor:           "codex",
		ActorKind:       models.RuleActorAgent,
		Reason:          "minimum evidence exists",
		EvidenceHandles: []string{"evidence:rg0-draft"},
	}
	first, err := store.CreateDraftFromCandidate(ctx, created.ID, req)
	require.NoError(t, err)
	second, err := store.CreateDraftFromCandidate(ctx, created.ID, req)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID, "candidate -> draft retry must return existing version")
}

func TestRuleGovernanceStore_UnknownActorKindCannotCreateDraft(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()

	created, err := store.CreateRuleCandidate(ctx, ruleGovernanceCandidate("unknown-actor-kind"))
	require.NoError(t, err)

	req := transitionReq("draft with unknown actor kind", "")
	req.ActorKind = models.RuleActorKind("script")
	_, err = store.CreateDraftFromCandidate(ctx, created.ID, req)
	require.Error(t, err)
	require.True(t, errors.Is(err, models.ErrRuleAuthorityDenied), "expected authority denial, got %v", err)

	var status string
	require.NoError(t, db.Raw(`SELECT status FROM rule_candidates WHERE id = ?`, created.ID).Scan(&status).Error)
	require.Equal(t, string(models.RuleCandidatePending), status)

	var versionCount int64
	require.NoError(t, db.Table("rule_versions").Where("source_candidate_id = ?", created.ID).Count(&versionCount).Error)
	require.Zero(t, versionCount)
}

func TestRuleGovernanceStore_ActiveTransitionRequiresSnapshotAndRollsBack(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()
	version := createRuleGovernanceDraft(t, store, "snapshot-required")

	version, err := store.TransitionRuleVersion(ctx, version.ID, models.RuleStateShadow, transitionReq("shadow", ""))
	require.NoError(t, err)
	version, err = store.TransitionRuleVersion(ctx, version.ID, models.RuleStateCanary, transitionReq("canary", ""))
	require.NoError(t, err)

	_, err = store.TransitionRuleVersion(ctx, version.ID, models.RuleStateActiveProject, transitionReq("missing snapshot", ""))
	require.Error(t, err)
	require.True(t, errors.Is(err, models.ErrRuleSnapshotRequired), "expected snapshot required, got %v", err)
	require.Equal(t, models.RuleStateCanary, getRuleVersionState(t, db, version.ID))
}

func TestRuleGovernanceStore_ActiveTransitionWritesSnapshotAndLog(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()
	version := createRuleGovernanceDraft(t, store, "snapshot-log")

	version, err := store.TransitionRuleVersion(ctx, version.ID, models.RuleStateShadow, transitionReq("shadow", ""))
	require.NoError(t, err)
	version, err = store.TransitionRuleVersion(ctx, version.ID, models.RuleStateCanary, transitionReq("canary", ""))
	require.NoError(t, err)

	snapshotID := fmt.Sprintf("rg0-snapshot-log-%d", time.Now().UnixNano())
	version, err = store.TransitionRuleVersion(ctx, version.ID, models.RuleStateActiveProject, transitionReq("activate", snapshotID))
	require.NoError(t, err)
	require.Equal(t, models.RuleStateActiveProject, version.State)

	var snapshotCount int64
	require.NoError(t, db.Table("rule_governance_snapshots").
		Where("snapshot_id = ?", snapshotID).
		Count(&snapshotCount).Error)
	require.Equal(t, int64(1), snapshotCount)

	var logCount int64
	require.NoError(t, db.Table("rule_transition_log").
		Where("rule_version_id = ? AND to_state = ? AND snapshot_id = ?", version.ID, string(models.RuleStateActiveProject), snapshotID).
		Count(&logCount).Error)
	require.Equal(t, int64(1), logCount)
}

func TestRuleGovernanceStore_AuthorityDenialRollsBackGlobalPromotion(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()
	version := createRuleGovernanceDraft(t, store, "authority")

	var err error
	version, err = store.TransitionRuleVersion(ctx, version.ID, models.RuleStateShadow, transitionReq("shadow", ""))
	require.NoError(t, err)
	version, err = store.TransitionRuleVersion(ctx, version.ID, models.RuleStateCanary, transitionReq("canary", ""))
	require.NoError(t, err)
	version, err = store.TransitionRuleVersion(ctx, version.ID, models.RuleStateActiveProject, transitionReq("active project", uniqueSnapshot("active-project")))
	require.NoError(t, err)
	version, err = store.TransitionRuleVersion(ctx, version.ID, models.RuleStateActiveShared, transitionReq("active shared", uniqueSnapshot("active-shared")))
	require.NoError(t, err)

	req := transitionReq("llm attempted global", uniqueSnapshot("active-global-denied"))
	req.ActorKind = models.RuleActorLLM
	req.Actor = "arbiter-llm"
	_, err = store.TransitionRuleVersion(ctx, version.ID, models.RuleStateActiveGlobal, req)
	require.Error(t, err)
	require.True(t, errors.Is(err, models.ErrRuleAuthorityDenied), "expected authority denial, got %v", err)
	require.Equal(t, models.RuleStateActiveShared, getRuleVersionState(t, db, version.ID))
}

func TestRuleGovernanceStore_TransitionLogFailureRollsBackState(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()
	version := createRuleGovernanceDraft(t, store, "log-rollback")

	version, err := store.TransitionRuleVersion(ctx, version.ID, models.RuleStateShadow, transitionReq("shadow", ""))
	require.NoError(t, err)

	require.NoError(t, db.Exec(`ALTER TABLE rule_transition_log DROP CONSTRAINT IF EXISTS rg0_transition_log_fail_test`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE rule_transition_log ADD CONSTRAINT rg0_transition_log_fail_test CHECK (reason <> 'force-log-fail')`).Error)
	t.Cleanup(func() {
		_ = db.Exec(`ALTER TABLE rule_transition_log DROP CONSTRAINT IF EXISTS rg0_transition_log_fail_test`).Error
	})

	_, err = store.TransitionRuleVersion(ctx, version.ID, models.RuleStateCanary, transitionReq("force-log-fail", ""))
	require.Error(t, err)
	require.Equal(t, models.RuleStateShadow, getRuleVersionState(t, db, version.ID))
}

func TestRuleGovernanceStore_SnapshotFailureRollsBackState(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()
	version := createRuleGovernanceDraft(t, store, "snapshot-rollback")

	version, err := store.TransitionRuleVersion(ctx, version.ID, models.RuleStateShadow, transitionReq("shadow", ""))
	require.NoError(t, err)
	version, err = store.TransitionRuleVersion(ctx, version.ID, models.RuleStateCanary, transitionReq("canary", ""))
	require.NoError(t, err)

	snapshotID := uniqueSnapshot("duplicate")
	_, err = store.CreateRuleSnapshot(ctx, SnapshotRequest{
		SnapshotID:  snapshotID,
		OpType:      "test_preexisting_snapshot",
		Actor:       "codex",
		BeforeState: []byte(`{"preexisting":true}`),
	})
	require.NoError(t, err)

	_, err = store.TransitionRuleVersion(ctx, version.ID, models.RuleStateActiveProject, transitionReq("duplicate snapshot", snapshotID))
	require.Error(t, err)
	require.Equal(t, models.RuleStateCanary, getRuleVersionState(t, db, version.ID))
}

func TestRuleGovernanceStore_RejectsMalformedLegalEscapeOnCreate(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()

	candidate := ruleGovernanceCandidate("bad-escape")
	candidate.DecayPolicy = "HYPOTHESIS"
	_, err := store.CreateRuleCandidate(ctx, candidate)
	require.Error(t, err)
	require.True(t, errors.Is(err, models.ErrRuleInvalidEscape), "expected invalid escape, got %v", err)
}

func createRuleGovernanceDraft(t *testing.T, store *RuleGovernanceStore, suffix string) *models.RuleVersion {
	t.Helper()
	ctx := context.Background()
	candidate := ruleGovernanceCandidate(suffix)
	created, err := store.CreateRuleCandidate(ctx, candidate)
	require.NoError(t, err)
	version, err := store.CreateDraftFromCandidate(ctx, created.ID, transitionReq("draft", ""))
	require.NoError(t, err)
	return version
}

func ruleGovernanceCandidate(suffix string) *models.RuleCandidate {
	unique := fmt.Sprintf("%s-%d", suffix, time.Now().UnixNano())
	return &models.RuleCandidate{
		SourceSignalType:    "explicit_agent_proposal",
		SourceSessionID:     "rg0-session-" + unique,
		SourceProject:       "rg0-project",
		SourceActor:         "codex",
		ProposedContent:     "Rule governance fixture " + unique,
		ProposedScope:       "project",
		ProposedAudience:    "developer",
		EvidenceHandles:     []string{"evidence:" + unique},
		AntiCaptureStatus:   "passed",
		ConflictStatus:      "none",
		Status:              models.RuleCandidatePending,
		Fingerprint:         "rg0-" + unique,
		DecayPolicy:         "NO DATA",
		LastEvaluatedAt:     time.Now().UTC(),
		ActivationPredicate: map[string]any{"project": "rg0-project"},
	}
}

func transitionReq(reason string, snapshotID string) RuleTransitionRequest {
	return RuleTransitionRequest{
		Actor:           "codex",
		ActorKind:       models.RuleActorAgent,
		Reason:          reason,
		EvidenceHandles: []string{"evidence:rg0-transition"},
		SnapshotID:      snapshotID,
	}
}

func uniqueSnapshot(prefix string) string {
	return fmt.Sprintf("rg0-%s-%d", prefix, time.Now().UnixNano())
}

func getRuleVersionState(t *testing.T, db *gorm.DB, versionID int64) models.RuleVersionState {
	t.Helper()
	var state string
	require.NoError(t, db.Raw(`SELECT state FROM rule_versions WHERE id = ?`, versionID).Scan(&state).Error)
	return models.RuleVersionState(state)
}
