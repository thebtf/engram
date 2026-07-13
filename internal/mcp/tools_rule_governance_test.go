package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

type fakeRuleGovernanceStore struct {
	transitionReq gormdb.RuleTransitionRequest
	rollbackReq   gormdb.RuleTransitionRequest
	health        gormdb.RuleGovernanceHealth
	groups        []gormdb.RuleGovernanceExceptionQueueGroup
	snaps         []gormdb.RuleGovernanceSnapshotSummary
	transitionTo  models.RuleVersionState
	pinSnapshotID string
	rollbackID    string
	rollbackErr   error
	transitionID  int64
	pinValue      bool
}

func (f *fakeRuleGovernanceStore) CreateRuleCandidate(_ context.Context, c *models.RuleCandidate) (*models.RuleCandidate, error) {
	return c, nil
}

func (f *fakeRuleGovernanceStore) GetLifecycleHealth(_ context.Context, params gormdb.RuleGovernanceHealthParams) (gormdb.RuleGovernanceHealth, error) {
	health := f.health
	health.Project = params.Project
	health.Since = params.Since
	health.Limit = params.Limit
	if health.CandidateStatusCounts == nil {
		health.CandidateStatusCounts = map[models.RuleCandidateStatus]int{}
	}
	if health.VersionStateCounts == nil {
		health.VersionStateCounts = map[models.RuleVersionState]int{}
	}
	if health.ArbiterRunStatusCounts == nil {
		health.ArbiterRunStatusCounts = map[models.RuleArbiterRunStatus]int{}
	}
	if health.TransitionActionCounts == nil {
		health.TransitionActionCounts = map[string]int{}
	}
	if health.SnapshotStatusCounts == nil {
		health.SnapshotStatusCounts = map[string]int{}
	}
	if health.InjectionEventTypeCounts == nil {
		health.InjectionEventTypeCounts = map[models.RuleInjectionEventType]int{}
	}
	return health, nil
}

func (f *fakeRuleGovernanceStore) ListExceptionQueueGroups(_ context.Context, _ gormdb.RuleGovernanceExceptionQueueParams) ([]gormdb.RuleGovernanceExceptionQueueGroup, error) {
	return f.groups, nil
}

func (f *fakeRuleGovernanceStore) ListRuleGovernanceSnapshots(_ context.Context, _ gormdb.RuleGovernanceSnapshotListParams) ([]gormdb.RuleGovernanceSnapshotSummary, error) {
	return f.snaps, nil
}

func (f *fakeRuleGovernanceStore) TransitionRuleVersion(_ context.Context, versionID int64, to models.RuleVersionState, req gormdb.RuleTransitionRequest) (*models.RuleVersion, error) {
	f.transitionID = versionID
	f.transitionTo = to
	f.transitionReq = req
	return &models.RuleVersion{
		ID:              versionID,
		State:           to,
		Scope:           "project",
		Audience:        "developer",
		EvidenceHandles: req.EvidenceHandles,
	}, nil
}

func (f *fakeRuleGovernanceStore) PinRuleGovernanceSnapshot(_ context.Context, snapshotID string, pinned bool) (gormdb.RuleGovernanceSnapshotSummary, error) {
	f.pinSnapshotID = snapshotID
	f.pinValue = pinned
	return gormdb.RuleGovernanceSnapshotSummary{
		SnapshotID: snapshotID,
		Status:     "committed",
		Pinned:     pinned,
	}, nil
}

func (f *fakeRuleGovernanceStore) RollbackRuleGovernanceSnapshot(_ context.Context, snapshotID string, req gormdb.RuleTransitionRequest) (gormdb.RuleGovernanceRollbackResult, error) {
	f.rollbackID = snapshotID
	f.rollbackReq = req
	if f.rollbackErr != nil {
		return gormdb.RuleGovernanceRollbackResult{
			SnapshotID:         snapshotID,
			ConflictVersionIDs: []int64{99},
			RestoredVersionIDs: nil,
		}, f.rollbackErr
	}
	return gormdb.RuleGovernanceRollbackResult{
		SnapshotID:         snapshotID,
		RestoredVersionIDs: []int64{42},
	}, nil
}

type fakeRuleInjectionTelemetry struct {
	aggregate gormdb.RuleInjectionTelemetryAggregate
}

func (f *fakeRuleInjectionTelemetry) AggregateByProjectRuleAndEventType(_ context.Context, params gormdb.RuleInjectionTelemetryParams) (gormdb.RuleInjectionTelemetryAggregate, error) {
	aggregate := f.aggregate
	aggregate.Project = params.Project
	aggregate.RuleVersionID = params.RuleVersionID
	return aggregate, nil
}

func TestRuleGovernanceReadToolsAdvertisedWhenStoresWired(t *testing.T) {
	s := NewServer(ServerOptions{Version: "test"})
	s.SetRuleGovernanceStore(&fakeRuleGovernanceStore{})
	s.SetRuleInjectionTelemetryStore(&fakeRuleInjectionTelemetry{})

	names := listedToolNames(s.ListTools())
	require.Contains(t, names, "rule_governance_health")
	require.Contains(t, names, "rule_governance_queue")
	require.Contains(t, names, "rule_governance_snapshots")
	require.Contains(t, names, "rule_governance_usefulness")
	require.Contains(t, names, "rule_governance_transition")
	require.Contains(t, names, "rule_governance_pin_snapshot")
	require.Contains(t, names, "rule_governance_rollback")
	require.NotContains(t, names, "list_snapshots", "rule governance snapshots must not overload bulk-op snapshot tools")
}

func TestRuleGovernanceReadToolsHiddenWhenStoreMissing(t *testing.T) {
	s := NewServer(ServerOptions{Version: "test"})

	names := listedToolNames(s.ListTools())
	require.NotContains(t, names, "rule_governance_health")
	require.NotContains(t, names, "rule_governance_queue")
	require.NotContains(t, names, "rule_governance_snapshots")
	require.NotContains(t, names, "rule_governance_usefulness")
}

func TestRuleGovernanceHealthReadOnlyCallerGetsNoData(t *testing.T) {
	s := NewServer(ServerOptions{Version: "test"})
	s.SetRuleGovernanceStore(&fakeRuleGovernanceStore{
		health: gormdb.RuleGovernanceHealth{NoData: true},
	})
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Role: auth.RoleReadOnly, Source: auth.SourceClient})

	out, err := s.callTool(ctx, "rule_governance_health", json.RawMessage(`{"project":"engram","since":"2026-06-19T00:00:00Z"}`))
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	require.Equal(t, "engram", decoded["project"])
	require.Equal(t, true, decoded["no_data"])
	require.Equal(t, models.RuleEscapeNoData, decoded["legal_escape"])
	require.Equal(t, true, decoded["side_effect_free"])
}

func TestRuleGovernanceQueueAndSnapshotsReadModels(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	s := NewServer(ServerOptions{Version: "test"})
	s.SetRuleGovernanceStore(&fakeRuleGovernanceStore{
		groups: []gormdb.RuleGovernanceExceptionQueueGroup{
			{
				Reason:                 "global_kernel_escalation",
				Count:                  1,
				RecommendedNextActions: []string{"manual_operator_review"},
				Items: []gormdb.RuleGovernanceExceptionQueueItem{
					{
						EntityID:               42,
						EntityType:             "rule_candidate",
						Project:                "engram",
						Scope:                  "global",
						Reason:                 "global_kernel_escalation",
						EvidenceHandles:        []string{"rule_candidate:42", "rule_candidate:private-token-42", "session:abc-123", "private:raw-note", "freeform-note"},
						LastActivityAt:         now,
						RecommendedNextActions: []string{"manual_operator_review"},
					},
				},
			},
		},
		snaps: []gormdb.RuleGovernanceSnapshotSummary{
			{
				SnapshotID: "rg-snap-1",
				OpType:     "rule_transition",
				Actor:      "operator",
				Status:     "committed",
				CreatedAt:  now,
				Pinned:     true,
			},
		},
	})
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Role: auth.RoleReadOnly, Source: auth.SourceClient})

	queueOut, err := s.callTool(ctx, "rule_governance_queue", json.RawMessage(`{"project":"engram"}`))
	require.NoError(t, err)
	var queue map[string]any
	require.NoError(t, json.Unmarshal([]byte(queueOut), &queue))
	require.Equal(t, float64(1), queue["total_count"])
	require.Equal(t, false, queue["empty"])
	groups := queue["groups"].([]any)
	firstGroup := groups[0].(map[string]any)
	firstItems := firstGroup["items"].([]any)
	firstItem := firstItems[0].(map[string]any)
	require.Equal(t, []any{
		"rule_candidate:42",
		"evidence:<redacted-sensitive>",
		"session:<redacted>",
		"evidence:<redacted>",
	}, firstItem["evidence_handles"])

	snapshotOut, err := s.callTool(ctx, "rule_governance_snapshots", json.RawMessage(`{"project":"engram"}`))
	require.NoError(t, err)
	var snapshots map[string]any
	require.NoError(t, json.Unmarshal([]byte(snapshotOut), &snapshots))
	require.Equal(t, "rule_governance_snapshots", snapshots["source_table"])
	require.Equal(t, false, snapshots["bulk_op_surface"])
	require.Equal(t, float64(1), snapshots["count"])
}

func TestRuleGovernanceUsefulnessNoDataAndProjectGuard(t *testing.T) {
	s := NewServer(ServerOptions{Version: "test"})
	s.SetRuleGovernanceStore(&fakeRuleGovernanceStore{})
	s.SetRuleInjectionTelemetryStore(&fakeRuleInjectionTelemetry{
		aggregate: gormdb.RuleInjectionTelemetryAggregate{NoData: true},
	})
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Role: auth.RoleReadOnly, Source: auth.SourceClient})

	_, missingProjectErr := s.callTool(ctx, "rule_governance_usefulness", json.RawMessage(`{}`))
	require.Error(t, missingProjectErr)
	require.Contains(t, missingProjectErr.Error(), "project_required")

	out, err := s.callTool(ctx, "rule_governance_usefulness", json.RawMessage(`{"project":"engram","rule_version_id":7}`))
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	require.Equal(t, "engram", decoded["project"])
	require.Equal(t, float64(7), decoded["rule_version_id"])
	require.Equal(t, true, decoded["no_data"])
	require.Equal(t, models.RuleEscapeNoData, decoded["legal_escape"])
	require.Equal(t, true, decoded["advisory_only"])
	require.Equal(t, false, decoded["auto_promotion"])
}

func TestRuleGovernanceReadToolsRequireProjectForNonAdminAllProjectReads(t *testing.T) {
	s := NewServer(ServerOptions{Version: "test"})
	s.SetRuleGovernanceStore(&fakeRuleGovernanceStore{
		health: gormdb.RuleGovernanceHealth{NoData: true},
	})
	readOnly := auth.WithIdentity(context.Background(), auth.Identity{Role: auth.RoleReadOnly, Source: auth.SourceClient})
	admin := auth.WithIdentity(context.Background(), auth.Admin())

	for _, toolName := range []string{
		"rule_governance_health",
		"rule_governance_queue",
		"rule_governance_snapshots",
	} {
		_, err := s.callTool(readOnly, toolName, json.RawMessage(`{}`))
		require.Error(t, err, toolName)
		require.Contains(t, err.Error(), "project_required", toolName)

		_, err = s.callTool(admin, toolName, json.RawMessage(`{}`))
		require.NoError(t, err, toolName)
	}
}

func TestRuleGovernanceReadToolsNilStoreErrors(t *testing.T) {
	s := NewServer(ServerOptions{Version: "test"})
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Role: auth.RoleReadOnly, Source: auth.SourceClient})
	_, err := s.callTool(ctx, "rule_governance_health", json.RawMessage(`{}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "rule governance store not available")
}

func TestRuleGovernanceReadToolsRequireIdentityWhenAuthEnabled(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "")
	s := NewServer(ServerOptions{Version: "test"})
	s.SetRuleGovernanceStore(&fakeRuleGovernanceStore{})

	_, err := s.callTool(context.Background(), "rule_governance_health", json.RawMessage(`{}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "auth_required")
}

func TestRuleGovernanceReadToolsRejectZeroIdentity(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "")
	s := NewServer(ServerOptions{Version: "test"})
	s.SetRuleGovernanceStore(&fakeRuleGovernanceStore{})
	ctx := auth.WithIdentity(context.Background(), auth.Identity{})

	_, err := s.callTool(ctx, "rule_governance_health", json.RawMessage(`{}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "auth_required")
}

func TestRuleGovernanceMutationToolsRequireAdmin(t *testing.T) {
	s := NewServer(ServerOptions{Version: "test"})
	s.SetRuleGovernanceStore(&fakeRuleGovernanceStore{})
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Role: auth.RoleReadOnly, Source: auth.SourceClient})

	_, err := s.callTool(ctx, "rule_governance_transition", json.RawMessage(`{"rule_version_id":1}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "admin_required")
}

func TestRuleGovernanceTransitionToolUsesStateMachineStore(t *testing.T) {
	store := &fakeRuleGovernanceStore{}
	s := NewServer(ServerOptions{Version: "test"})
	s.SetRuleGovernanceStore(store)
	ctx := auth.WithIdentity(context.Background(), auth.Admin())

	out, err := s.callTool(ctx, "rule_governance_transition", json.RawMessage(`{
		"rule_version_id": 7,
		"to_state": "active_project",
		"actor": "operator-a",
		"actor_kind": "operator",
		"reason": "canary evidence is sufficient",
		"evidence_handles": ["report:rg3"],
		"snapshot_id": "rg-snap-7"
	}`))
	require.NoError(t, err)
	require.Equal(t, int64(7), store.transitionID)
	require.Equal(t, models.RuleStateActiveProject, store.transitionTo)
	require.Equal(t, "operator-a", store.transitionReq.Actor)
	require.Equal(t, models.RuleActorOperator, store.transitionReq.ActorKind)
	require.Equal(t, []string{"report:rg3"}, store.transitionReq.EvidenceHandles)
	require.Equal(t, "rg-snap-7", store.transitionReq.SnapshotID)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	require.Equal(t, float64(7), decoded["rule_version_id"])
	require.Equal(t, "active_project", decoded["state"])
	require.Equal(t, "rule_versions", decoded["source_table"])
}

func TestRuleGovernancePinSnapshotAndRollbackUseRuleGovernanceSnapshots(t *testing.T) {
	store := &fakeRuleGovernanceStore{}
	s := NewServer(ServerOptions{Version: "test"})
	s.SetRuleGovernanceStore(store)
	ctx := auth.WithIdentity(context.Background(), auth.Admin())

	pinOut, err := s.callTool(ctx, "rule_governance_pin_snapshot", json.RawMessage(`{"snapshot_id":"rg-snap-1","pinned":false}`))
	require.NoError(t, err)
	require.Equal(t, "rg-snap-1", store.pinSnapshotID)
	require.False(t, store.pinValue)
	var pinDecoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(pinOut), &pinDecoded))
	require.Equal(t, "rule_governance_snapshots", pinDecoded["source_table"])
	require.Equal(t, false, pinDecoded["bulk_op_surface"])

	rollbackOut, err := s.callTool(ctx, "rule_governance_rollback", json.RawMessage(`{
		"snapshot_id": "rg-snap-1",
		"actor": "admin-a",
		"actor_kind": "admin",
		"reason": "rollback bad active rule",
		"evidence_handles": ["incident:rg3"]
	}`))
	require.NoError(t, err)
	require.Equal(t, "rg-snap-1", store.rollbackID)
	require.Equal(t, "admin-a", store.rollbackReq.Actor)
	require.Equal(t, models.RuleActorAdmin, store.rollbackReq.ActorKind)
	var rollbackDecoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(rollbackOut), &rollbackDecoded))
	require.Equal(t, "rule_governance_snapshots", rollbackDecoded["source_table"])
	require.Equal(t, false, rollbackDecoded["bulk_op_surface"])
	require.Equal(t, []any{float64(42)}, rollbackDecoded["restored_version_ids"])
}

func TestRuleGovernanceMutationsRejectMalformedPresentValuesBeforeTransition(t *testing.T) {
	ctx := auth.WithIdentity(context.Background(), auth.Admin())
	for _, tc := range []struct {
		name string
		tool string
		raw  string
	}{
		{name: "transition numeric string id", tool: "rule_governance_transition", raw: `{"rule_version_id":"7","to_state":"active_project"}`},
		{name: "transition fraction id", tool: "rule_governance_transition", raw: `{"rule_version_id":7.5,"to_state":"active_project"}`},
		{name: "transition exponent id", tool: "rule_governance_transition", raw: `{"rule_version_id":1e3,"to_state":"active_project"}`},
		{name: "transition overflow id", tool: "rule_governance_transition", raw: `{"rule_version_id":9223372036854775808,"to_state":"active_project"}`},
		{name: "transition wrong state type", tool: "rule_governance_transition", raw: `{"rule_version_id":7,"to_state":true}`},
		{name: "transition mixed evidence", tool: "rule_governance_transition", raw: `{"rule_version_id":7,"to_state":"active_project","evidence_handles":["report:ok",9]}`},
		{name: "pin wrong snapshot type", tool: "rule_governance_pin_snapshot", raw: `{"snapshot_id":7,"pinned":true}`},
		{name: "pin string boolean", tool: "rule_governance_pin_snapshot", raw: `{"snapshot_id":"rg-snap","pinned":"false"}`},
		{name: "rollback wrong reason type", tool: "rule_governance_rollback", raw: `{"snapshot_id":"rg-snap","reason":9}`},
		{name: "rollback null evidence", tool: "rule_governance_rollback", raw: `{"snapshot_id":"rg-snap","evidence_handles":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeRuleGovernanceStore{}
			s := NewServer(ServerOptions{Version: "strict-rule-governance"})
			s.SetRuleGovernanceStore(store)

			out, err := s.callTool(ctx, tc.tool, json.RawMessage(tc.raw))

			require.Error(t, err)
			require.Empty(t, out)
			require.Zero(t, store.transitionID)
			require.Empty(t, store.pinSnapshotID)
			require.Empty(t, store.rollbackID)
		})
	}
}

func TestRuleGovernanceTransitionPreservesExactLargeIntegerSelector(t *testing.T) {
	store := &fakeRuleGovernanceStore{}
	s := NewServer(ServerOptions{Version: "strict-rule-governance"})
	s.SetRuleGovernanceStore(store)
	ctx := auth.WithIdentity(context.Background(), auth.Admin())

	_, err := s.callTool(ctx, "rule_governance_transition", json.RawMessage(`{
		"rule_version_id":9007199254740993,
		"to_state":"active_project",
		"actor":"operator-a",
		"actor_kind":"operator",
		"reason":"exact selector",
		"evidence_handles":[]
	}`))

	require.NoError(t, err)
	require.Equal(t, int64(9007199254740993), store.transitionID)
}

func TestRuleGovernanceRollbackReturnsStructuredConflictResult(t *testing.T) {
	store := &fakeRuleGovernanceStore{rollbackErr: errors.New("invalid_rule_transition: rollback conflicts detected")}
	s := NewServer(ServerOptions{Version: "test"})
	s.SetRuleGovernanceStore(store)
	ctx := auth.WithIdentity(context.Background(), auth.Admin())

	out, err := s.callTool(ctx, "rule_governance_rollback", json.RawMessage(`{
		"snapshot_id": "rg-snap-conflict",
		"actor": "admin-a",
		"actor_kind": "admin",
		"reason": "rollback conflicting rule",
		"evidence_handles": ["incident:rg3"]
	}`))
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	require.Equal(t, "rg-snap-conflict", decoded["snapshot_id"])
	require.Equal(t, "rollback_conflict", decoded["status"])
	require.Equal(t, false, decoded["ok"])
	require.Equal(t, []any{float64(99)}, decoded["conflict_version_ids"])
}

func listedToolNames(tools []Tool) map[string]bool {
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}
	return names
}
