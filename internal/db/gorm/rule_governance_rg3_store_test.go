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
	run, err := store.StartRuleArbiterRun(ctx, "rg3-health")
	require.NoError(t, err)
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
		Project: project,
		Since:   since,
		Limit:   50,
	})
	require.NoError(t, err)

	require.Equal(t, project, health.Project)
	require.False(t, health.NoData)
	require.Equal(t, 1, health.CandidateStatusCounts[models.RuleCandidatePending])
	require.Equal(t, 1, health.CandidateStatusCounts[models.RuleCandidateRejected])
	require.Equal(t, 1, health.VersionStateCounts[models.RuleStateActiveProject])
	require.Equal(t, 1, health.VersionStateCounts[models.RuleStateSuperseded])
	require.Equal(t, 1, health.ArbiterRunStatusCounts[models.RuleArbiterRunStatusCompleted])
	require.Equal(t, 1, health.TransitionActionCounts["candidate_to_rejected"])
	require.Equal(t, 1, health.TransitionActionCounts["rule_version_transition"])
	require.Equal(t, 1, health.SnapshotStatusCounts["committed"])
	require.Equal(t, 1, health.InjectionEventTypeCounts[models.RuleInjectionEmittedContextual])
	require.Equal(t, 1, health.InjectionEventTypeCounts[models.RuleInjectionSuppressedState])
	require.Contains(t, health.EvidenceHandles, fmt.Sprintf("rule_candidate:%d", createdPending.ID))
}

func TestRuleGovernanceStore_ListExceptionQueueGroupsByReason(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewRuleGovernanceStore(db)
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
	require.Equal(t, "manual_operator_review", byReason["global_kernel_escalation"].RecommendedNextActions[0])
	require.Equal(t, "compare_active_rule_family", byReason["active_rule_conflict"].RecommendedNextActions[0])
	require.Equal(t, "review_anti_capture_evidence", byReason["reject_review_hold"].RecommendedNextActions[0])
	require.NotEmpty(t, byReason["global_kernel_escalation"].Items[0].EvidenceHandles)
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
