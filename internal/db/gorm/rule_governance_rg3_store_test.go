package gorm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

func TestRuleGovernanceStore_GetLifecycleHealthAggregatesGovernanceTables(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	eventStore := NewRuleInjectionEventStore(db)
	ctx := context.Background()
	project := fmt.Sprintf("rg3-health-%d", time.Now().UnixNano())
	since := time.Now().Add(-1 * time.Minute).UTC()

	pending := ruleGovernanceCandidate(project + "-pending")
	pending.SourceProject = project
	pending.ProposedScope = "project"
	pending.ActivationPredicate = map[string]any{"project": project}
	createdPending, err := store.CreateRuleCandidate(ctx, pending)
	require.NoError(t, err)

	rejected := ruleGovernanceCandidate(project + "-rejected")
	rejected.SourceProject = project
	rejected.ProposedScope = "project"
	rejected.ActivationPredicate = map[string]any{"project": project}
	createdRejected, err := store.CreateRuleCandidate(ctx, rejected)
	require.NoError(t, err)
	_, err = store.RejectRuleCandidate(ctx, createdRejected.ID, transitionReq("rg3 rejected for health", ""))
	require.NoError(t, err)

	activeVersionID := insertRG3RuleVersionFixture(t, db, project, models.RuleStateActiveProject, "developer", 50)
	supersededVersionID := insertRG3RuleVersionFixture(t, db, project, models.RuleStateSuperseded, "developer", 40)
	draftVersionID := insertRG3RuleVersionFixture(t, db, project, models.RuleStateDraft, "developer", 30)
	baseline, err := store.GetLifecycleHealth(ctx, RuleGovernanceHealthParams{
		Project:                       project,
		Since:                         since,
		Limit:                         50,
		IncludeGlobalArbiterRunCounts: true,
	})
	require.NoError(t, err)
	run, err := store.StartRuleArbiterRun(ctx, "rg3-health")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Delete(&models.RuleArbiterRun{}, run.ID).Error) })
	_, err = store.FinishRuleArbiterRun(ctx, run.ID, models.RuleArbiterRunStatusCompleted, models.RuleArbiterRunCounts{
		CandidatesSeen:      2,
		CandidatesEvaluated: 2,
		CandidatesProposed:  1,
		CandidatesRejected:  1,
	}, "")
	require.NoError(t, err)

	_, err = store.TransitionRuleVersion(ctx, draftVersionID, models.RuleStateShadow, transitionReq("rg3 shadow for health", ""))
	require.NoError(t, err)
	_, err = store.CreateRuleSnapshot(ctx, SnapshotRequest{
		SnapshotID:  uniqueSnapshot("rg3-health-committed"),
		OpType:      "rg3_health_fixture",
		Actor:       "codex",
		BeforeState: []byte(fmt.Sprintf(`{"project":%q}`, project)),
	})
	require.NoError(t, err)
	require.NoError(t, eventStore.RecordEvents(ctx, []*models.RuleInjectionEvent{
		{
			SessionID:     project + "-session",
			Project:       project,
			Surface:       "session-start",
			EventType:     models.RuleInjectionEmittedContextual,
			RuleVersionID: &activeVersionID,
		},
		{
			SessionID:     project + "-session",
			Project:       project,
			Surface:       "session-start",
			EventType:     models.RuleInjectionSuppressedState,
			RuleVersionID: &supersededVersionID,
			Reason:        "archived_or_superseded",
		},
	}))

	health, err := store.GetLifecycleHealth(ctx, RuleGovernanceHealthParams{
		Project:                       project,
		Since:                         since,
		Limit:                         50,
		IncludeGlobalArbiterRunCounts: true,
	})
	require.NoError(t, err)

	require.Equal(t, project, health.Project)
	require.False(t, health.NoData)
	require.Equal(t, 1, health.CandidateStatusCounts[models.RuleCandidatePending])
	require.Equal(t, 1, health.CandidateStatusCounts[models.RuleCandidateRejected])
	require.Equal(t, 1, health.VersionStateCounts[models.RuleStateActiveProject])
	require.Equal(t, 1, health.VersionStateCounts[models.RuleStateSuperseded])
	require.Equal(t, baseline.ArbiterRunStatusCounts[models.RuleArbiterRunStatusCompleted]+1, health.ArbiterRunStatusCounts[models.RuleArbiterRunStatusCompleted])
	require.Equal(t, 1, health.TransitionActionCounts["candidate_to_rejected"])
	require.Equal(t, 1, health.TransitionActionCounts["rule_version_transition"])
	require.Equal(t, 1, health.SnapshotStatusCounts["committed"])
	require.Equal(t, 1, health.InjectionEventTypeCounts[models.RuleInjectionEmittedContextual])
	require.Equal(t, 1, health.InjectionEventTypeCounts[models.RuleInjectionSuppressedState])
	require.Contains(t, health.EvidenceHandles, fmt.Sprintf("rule_candidate:%d", createdPending.ID))
}

func TestRuleGovernanceStore_GetLifecycleHealthOmitsGlobalArbiterRunsForProjectScopedReads(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()
	project := fmt.Sprintf("rg3-health-scoped-%d", time.Now().UnixNano())
	baseline, err := store.GetLifecycleHealth(ctx, RuleGovernanceHealthParams{
		Project:                       project,
		Limit:                         50,
		IncludeGlobalArbiterRunCounts: true,
	})
	require.NoError(t, err)
	run, err := store.StartRuleArbiterRun(ctx, "rg3-health-scoped")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Delete(&models.RuleArbiterRun{}, run.ID).Error) })
	_, err = store.FinishRuleArbiterRun(ctx, run.ID, models.RuleArbiterRunStatusCompleted, models.RuleArbiterRunCounts{
		CandidatesSeen: 1,
	}, "")
	require.NoError(t, err)

	scoped, err := store.GetLifecycleHealth(ctx, RuleGovernanceHealthParams{
		Project: project,
		Limit:   50,
	})
	require.NoError(t, err)
	require.Empty(t, scoped.ArbiterRunStatusCounts)
	require.True(t, scoped.NoData)

	admin, err := store.GetLifecycleHealth(ctx, RuleGovernanceHealthParams{
		Project:                       project,
		Limit:                         50,
		IncludeGlobalArbiterRunCounts: true,
	})
	require.NoError(t, err)
	require.Equal(t, baseline.ArbiterRunStatusCounts[models.RuleArbiterRunStatusCompleted]+1, admin.ArbiterRunStatusCounts[models.RuleArbiterRunStatusCompleted])
	require.False(t, admin.NoData)
}

func TestRuleGovernanceStore_ListExceptionQueueGroupsByReason(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	eventStore := NewRuleInjectionEventStore(db)
	ctx := context.Background()
	project := fmt.Sprintf("rg3-queue-%d", time.Now().UnixNano())

	global := ruleGovernanceCandidate(project + "-global")
	global.SourceProject = project
	global.ProposedScope = "global"
	global.AntiCaptureStatus = "passed"
	global.ConflictStatus = "none"
	global.EvidenceHandles = []string{"evidence:" + project + ":global"}
	_, err := store.CreateRuleCandidate(ctx, global)
	require.NoError(t, err)

	conflict := ruleGovernanceCandidate(project + "-conflict")
	conflict.SourceProject = project
	conflict.ProposedScope = "project"
	conflict.ConflictStatus = "active_rule_conflict"
	conflict.EvidenceHandles = []string{"evidence:" + project + ":conflict"}
	_, err = store.CreateRuleCandidate(ctx, conflict)
	require.NoError(t, err)

	hold := ruleGovernanceCandidate(project + "-hold")
	hold.SourceProject = project
	hold.ProposedScope = "project"
	hold.AntiCaptureStatus = "reject_review_hold"
	hold.EvidenceHandles = []string{"evidence:" + project + ":hold"}
	_, err = store.CreateRuleCandidate(ctx, hold)
	require.NoError(t, err)

	unclear := ruleGovernanceCandidate(project + "-unclear")
	unclear.SourceProject = project
	unclear.ProposedScope = "private"
	unclear.ConflictStatus = "none"
	unclear.EvidenceHandles = []string{"evidence:" + project + ":unclear"}
	_, err = store.CreateRuleCandidate(ctx, unclear)
	require.NoError(t, err)

	canaryVersionID := insertRG3RuleVersionFixture(t, db, project, models.RuleStateCanary, "developer", 50)

	snapshot, err := store.CreateRuleSnapshot(ctx, SnapshotRequest{
		SnapshotID:  uniqueSnapshot("rg3-queue-conflict"),
		OpType:      "rollback",
		Actor:       "codex",
		BeforeState: []byte(fmt.Sprintf(`{"project":%q,"rule_versions":[{"id":%d,"state":"canary"}]}`, project, canaryVersionID)),
		AfterState:  []byte(fmt.Sprintf(`{"project":%q,"rule_versions":[{"id":%d,"state":"active_project"}]}`, project, canaryVersionID)),
	})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`UPDATE rule_governance_snapshots SET status = 'rollback_conflict' WHERE snapshot_id = ?`, snapshot.SnapshotID).Error)

	require.NoError(t, eventStore.RecordEvents(ctx, []*models.RuleInjectionEvent{
		{
			SessionID:     project + "-stale-cache-session",
			Project:       project,
			Surface:       "session-start-cache",
			EventType:     models.RuleInjectionFallbackLegacy,
			RuleVersionID: &canaryVersionID,
			Reason:        "stale_cache_revoked_rule",
		},
	}))

	groups, err := store.ListExceptionQueueGroups(ctx, RuleGovernanceExceptionQueueParams{
		Project: project,
		Limit:   20,
	})
	require.NoError(t, err)

	byReason := map[string]RuleGovernanceExceptionQueueGroup{}
	for _, group := range groups {
		byReason[group.Reason] = group
	}
	require.Len(t, byReason["global_kernel_escalation"].Items, 1)
	require.Len(t, byReason["active_rule_conflict"].Items, 1)
	require.Len(t, byReason["reject_review_hold"].Items, 1)
	require.Len(t, byReason["unclear_scope_private_risk"].Items, 1)
	require.Len(t, byReason["canary_result_review"].Items, 1)
	require.Len(t, byReason["rollback_archive_restore_conflict"].Items, 1)
	require.Len(t, byReason["stale_cache_revocation_anomaly"].Items, 1)
	require.Equal(t, "manual_operator_review", byReason["global_kernel_escalation"].RecommendedNextActions[0])
	require.Equal(t, "compare_active_rule_family", byReason["active_rule_conflict"].RecommendedNextActions[0])
	require.Equal(t, "review_anti_capture_evidence", byReason["reject_review_hold"].RecommendedNextActions[0])
	require.Equal(t, "clarify_scope_before_promotion", byReason["unclear_scope_private_risk"].RecommendedNextActions[0])
	require.Equal(t, "review_canary_usefulness_metrics", byReason["canary_result_review"].RecommendedNextActions[0])
	require.Equal(t, "inspect_snapshot_conflict", byReason["rollback_archive_restore_conflict"].RecommendedNextActions[0])
	require.Equal(t, "inspect_stale_cache_or_revocation_event", byReason["stale_cache_revocation_anomaly"].RecommendedNextActions[0])
	require.NotEmpty(t, byReason["global_kernel_escalation"].Items[0].EvidenceHandles)
}

func TestRuleGovernanceStore_ListExceptionQueueFiltersCandidatesBeforeLimitAndStatus(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()
	project := fmt.Sprintf("rg3-queue-filter-%d", time.Now().UnixNano())

	liveConflict := ruleGovernanceCandidate(project + "-live-conflict")
	liveConflict.SourceProject = project
	liveConflict.ProposedScope = "project"
	liveConflict.ConflictStatus = "active_rule_conflict"
	createdLiveConflict, err := store.CreateRuleCandidate(ctx, liveConflict)
	require.NoError(t, err)

	resolvedConflict := ruleGovernanceCandidate(project + "-resolved-conflict")
	resolvedConflict.SourceProject = project
	resolvedConflict.ProposedScope = "project"
	resolvedConflict.ConflictStatus = "active_rule_conflict"
	createdResolvedConflict, err := store.CreateRuleCandidate(ctx, resolvedConflict)
	require.NoError(t, err)
	_, err = store.RejectRuleCandidate(ctx, createdResolvedConflict.ID, transitionReq("resolved conflict must not queue", ""))
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		noise := ruleGovernanceCandidate(fmt.Sprintf("%s-noise-%d", project, i))
		noise.SourceProject = project
		noise.ProposedScope = "project"
		noise.ConflictStatus = "none"
		_, err = store.CreateRuleCandidate(ctx, noise)
		require.NoError(t, err)
	}

	groups, err := store.ListExceptionQueueGroups(ctx, RuleGovernanceExceptionQueueParams{
		Project: project,
		Limit:   1,
	})
	require.NoError(t, err)

	byReason := map[string]RuleGovernanceExceptionQueueGroup{}
	for _, group := range groups {
		byReason[group.Reason] = group
	}
	require.Len(t, byReason["active_rule_conflict"].Items, 1)
	require.Equal(t, createdLiveConflict.ID, byReason["active_rule_conflict"].Items[0].EntityID)
}

func TestRuleGovernanceStore_ListPinAndRollbackSnapshotsDetectsConflict(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()
	project := fmt.Sprintf("rg3-snapshot-%d", time.Now().UnixNano())
	versionID := insertRG3RuleVersionFixture(t, db, project, models.RuleStateActiveProject, "developer", 10)
	snapshotID := uniqueSnapshot("rg3-rollback")

	snapshot, err := store.CreateRuleSnapshot(ctx, SnapshotRequest{
		SnapshotID:  snapshotID,
		OpType:      "rule_transition",
		Actor:       "codex",
		BeforeState: []byte(fmt.Sprintf(`{"project":%q,"rule_versions":[{"id":%d,"state":"canary"}]}`, project, versionID)),
		AfterState:  []byte(fmt.Sprintf(`{"project":%q,"rule_versions":[{"id":%d,"state":"active_project"}]}`, project, versionID)),
	})
	require.NoError(t, err)

	pinned, err := store.PinRuleGovernanceSnapshot(ctx, snapshot.SnapshotID, true)
	require.NoError(t, err)
	require.True(t, pinned.Pinned)

	snapshots, err := store.ListRuleGovernanceSnapshots(ctx, RuleGovernanceSnapshotListParams{
		Project: project,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	require.Equal(t, snapshot.SnapshotID, snapshots[0].SnapshotID)
	require.True(t, snapshots[0].Pinned)

	_, err = store.TransitionRuleVersion(ctx, versionID, models.RuleStateSuperseded, transitionReq("diverge before rollback", uniqueSnapshot("rg3-diverge")))
	require.NoError(t, err)

	result, err := store.RollbackRuleGovernanceSnapshot(ctx, snapshot.SnapshotID, transitionReq("rollback divergent active rule", uniqueSnapshot("rg3-rollback-conflict")))
	require.Error(t, err)
	require.Empty(t, result.RestoredVersionIDs)
	require.Contains(t, result.ConflictVersionIDs, versionID)
	require.Equal(t, models.RuleStateSuperseded, getRuleVersionState(t, db, versionID))

	snapshots, err = store.ListRuleGovernanceSnapshots(ctx, RuleGovernanceSnapshotListParams{
		Project: project,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, snapshots, 2)
	statusByID := map[string]string{}
	for _, snapshot := range snapshots {
		statusByID[snapshot.SnapshotID] = snapshot.Status
	}
	require.Equal(t, "rollback_conflict", statusByID[snapshotID])

	groups, err := store.ListExceptionQueueGroups(ctx, RuleGovernanceExceptionQueueParams{
		Project: project,
		Limit:   10,
	})
	require.NoError(t, err)
	byReason := map[string]RuleGovernanceExceptionQueueGroup{}
	for _, group := range groups {
		byReason[group.Reason] = group
	}
	require.Len(t, byReason["rollback_archive_restore_conflict"].Items, 1)
}

func TestRuleGovernanceStore_RollbackSnapshotRestoresBeforeState(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()
	project := fmt.Sprintf("rg3-rollback-success-%d", time.Now().UnixNano())
	versionID := insertRG3RuleVersionFixture(t, db, project, models.RuleStateActiveProject, "developer", 10)
	snapshotID := uniqueSnapshot("rg3-rollback-success")

	_, err := store.CreateRuleSnapshot(ctx, SnapshotRequest{
		SnapshotID:  snapshotID,
		OpType:      "rule_transition",
		Actor:       "codex",
		BeforeState: []byte(fmt.Sprintf(`{"project":%q,"rule_versions":[{"id":%d,"state":"canary"}]}`, project, versionID)),
		AfterState:  []byte(fmt.Sprintf(`{"project":%q,"rule_versions":[{"id":%d,"state":"active_project"}]}`, project, versionID)),
	})
	require.NoError(t, err)

	result, err := store.RollbackRuleGovernanceSnapshot(ctx, snapshotID, transitionReq("rollback active project rule", uniqueSnapshot("rg3-rollback-success-transition")))
	require.NoError(t, err)
	require.Contains(t, result.RestoredVersionIDs, versionID)
	require.Empty(t, result.ConflictVersionIDs)
	require.Equal(t, models.RuleStateCanary, getRuleVersionState(t, db, versionID))

	snapshots, err := store.ListRuleGovernanceSnapshots(ctx, RuleGovernanceSnapshotListParams{
		Project: project,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	require.Equal(t, "rolled_back", snapshots[0].Status)
	require.NotNil(t, snapshots[0].RolledBackAt)
}

func TestRuleGovernanceStore_RollbackTransitionCreatedSnapshotRestoresBeforeState(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()
	project := fmt.Sprintf("rg3-rollback-transition-%d", time.Now().UnixNano())
	versionID := insertRG3RuleVersionFixture(t, db, project, models.RuleStateCanary, "developer", 10)
	snapshotID := uniqueSnapshot("rg3-transition-created")

	_, err := store.TransitionRuleVersion(ctx, versionID, models.RuleStateActiveProject, transitionReq("promote canary for rollback fixture", snapshotID))
	require.NoError(t, err)
	require.Equal(t, models.RuleStateActiveProject, getRuleVersionState(t, db, versionID))

	result, err := store.RollbackRuleGovernanceSnapshot(ctx, snapshotID, transitionReq("rollback transition-created snapshot", uniqueSnapshot("rg3-transition-created-rollback")))
	require.NoError(t, err)
	require.Contains(t, result.RestoredVersionIDs, versionID)
	require.Empty(t, result.ConflictVersionIDs)
	require.Equal(t, models.RuleStateCanary, getRuleVersionState(t, db, versionID))

	snapshots, err := store.ListRuleGovernanceSnapshots(ctx, RuleGovernanceSnapshotListParams{
		Project: project,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	require.Equal(t, "rolled_back", snapshots[0].Status)
	require.NotNil(t, snapshots[0].RolledBackAt)
}

func TestRuleGovernanceStore_RollbackSnapshotRejectsNonCommittedStatus(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()
	project := fmt.Sprintf("rg3-rollback-status-%d", time.Now().UnixNano())
	versionID := insertRG3RuleVersionFixture(t, db, project, models.RuleStateActiveProject, "developer", 10)
	snapshotID := uniqueSnapshot("rg3-rollback-status")

	_, err := store.CreateRuleSnapshot(ctx, SnapshotRequest{
		SnapshotID:  snapshotID,
		OpType:      "rule_transition",
		Actor:       "codex",
		BeforeState: []byte(fmt.Sprintf(`{"project":%q,"rule_versions":[{"id":%d,"state":"canary"}]}`, project, versionID)),
		AfterState:  []byte(fmt.Sprintf(`{"project":%q,"rule_versions":[{"id":%d,"state":"active_project"}]}`, project, versionID)),
	})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`UPDATE rule_governance_snapshots SET status = 'failed' WHERE snapshot_id = ?`, snapshotID).Error)

	result, err := store.RollbackRuleGovernanceSnapshot(ctx, snapshotID, transitionReq("rollback failed snapshot", uniqueSnapshot("rg3-rollback-status-transition")))
	require.Error(t, err)
	require.Empty(t, result.RestoredVersionIDs)
	require.Empty(t, result.ConflictVersionIDs)
	require.Contains(t, err.Error(), "cannot rollback")
	require.Equal(t, models.RuleStateActiveProject, getRuleVersionState(t, db, versionID))

	snapshots, err := store.ListRuleGovernanceSnapshots(ctx, RuleGovernanceSnapshotListParams{
		Project: project,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	require.Equal(t, "failed", snapshots[0].Status)
	require.Nil(t, snapshots[0].RolledBackAt)
}

func TestRuleGovernanceStore_RollbackSnapshotRefusesPinnedOrProtectedVersions(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
	ctx := context.Background()
	project := fmt.Sprintf("rg3-rollback-protected-%d", time.Now().UnixNano())
	protectedVersionID := insertRG3RuleVersionFixture(t, db, project, models.RuleStateActiveProject, "developer", 10)
	pinnedVersionID := insertRG3RuleVersionFixture(t, db, project, models.RuleStateActiveProject, "developer", 9)
	require.NoError(t, db.Exec(`UPDATE rule_versions SET protected = true WHERE id = ?`, protectedVersionID).Error)
	require.NoError(t, db.Exec(`UPDATE rule_versions SET pinned = true WHERE id = ?`, pinnedVersionID).Error)
	snapshotID := uniqueSnapshot("rg3-rollback-protected")

	_, err := store.CreateRuleSnapshot(ctx, SnapshotRequest{
		SnapshotID: snapshotID,
		OpType:     "rule_transition",
		Actor:      "codex",
		BeforeState: []byte(fmt.Sprintf(
			`{"project":%q,"rule_versions":[{"id":%d,"state":"canary"},{"id":%d,"state":"canary"}]}`,
			project,
			protectedVersionID,
			pinnedVersionID,
		)),
		AfterState: []byte(fmt.Sprintf(
			`{"project":%q,"rule_versions":[{"id":%d,"state":"active_project"},{"id":%d,"state":"active_project"}]}`,
			project,
			protectedVersionID,
			pinnedVersionID,
		)),
	})
	require.NoError(t, err)

	result, err := store.RollbackRuleGovernanceSnapshot(ctx, snapshotID, transitionReq("rollback protected active rule", uniqueSnapshot("rg3-rollback-protected-transition")))
	require.Error(t, err)
	require.Empty(t, result.RestoredVersionIDs)
	require.Contains(t, result.ConflictVersionIDs, protectedVersionID)
	require.Contains(t, result.ConflictVersionIDs, pinnedVersionID)
	require.Equal(t, models.RuleStateActiveProject, getRuleVersionState(t, db, protectedVersionID))
	require.Equal(t, models.RuleStateActiveProject, getRuleVersionState(t, db, pinnedVersionID))
}

func insertRG3RuleVersionFixture(t *testing.T, db *gorm.DB, project string, state models.RuleVersionState, audience string, priority int) int64 {
	t.Helper()
	var familyID int64
	require.NoError(t, db.Raw(`INSERT INTO rule_families (family_key) VALUES (?) RETURNING id`,
		fmt.Sprintf("rg3-family-%s-%d", project, time.Now().UnixNano()),
	).Scan(&familyID).Error)

	var versionID int64
	require.NoError(t, db.Raw(`INSERT INTO rule_versions (
		family_id,
		content,
		scope,
		owner,
		audience,
		activation_predicate_json,
		evidence_handles_json,
		state,
		budget_class,
		anti_capture_status,
		conflict_status,
		decay_policy,
		priority
	) VALUES (?, ?, ?, ?, ?, CAST(? AS jsonb), '[]'::jsonb, ?, ?, ?, ?, ?, ?) RETURNING id`,
		familyID,
		"RG-3 fixture "+project,
		"project",
		"codex",
		audience,
		fmt.Sprintf(`{"project":%q}`, project),
		string(state),
		"contextual",
		"passed",
		"none",
		"NO DATA",
		priority,
	).Scan(&versionID).Error)
	return versionID
}
