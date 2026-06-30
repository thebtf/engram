package gorm

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/cognitive"
)

type stateStoreReadWriteSeam interface {
	cognitive.StateWriter
	ReadSessionState(ctx context.Context, sessionID string) (cognitive.SessionStateSlots, error)
	ReadProjectState(ctx context.Context, project string) (cognitive.ProjectStateRecord, error)
}

var _ stateStoreReadWriteSeam = (*StateStore)(nil)
var _ cognitive.StatePlane = (*StateStore)(nil)

func TestStateStore_SessionStateRoundTrip(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	store := NewStateStore(db, auditStore)
	ctx := context.Background()

	sessionID := fmt.Sprintf("state-session-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM agent_session_state WHERE session_id = ?`, sessionID).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE source_session_id = ? AND action = 'write_session_state'`, sessionID).Error
	})

	want := cognitive.SessionStateSlots{
		Focus: map[string]interface{}{
			"objective": "finish T002",
		},
		Execution: map[string]interface{}{
			"next_action": "run state-store tests",
		},
		Horizons: map[string]interface{}{
			"next_verification": "go test ./internal/db/gorm",
		},
	}

	require.NoError(t, store.WriteSessionState(ctx, sessionID, want))

	got, err := store.ReadSessionState(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, want.Focus, got.Focus)
	require.Equal(t, want.Execution, got.Execution)
	require.Equal(t, want.Horizons, got.Horizons)
}

func TestStateStore_ProjectStateRoundTrip(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	store := NewStateStore(db, auditStore)
	ctx := context.Background()

	project := fmt.Sprintf("state-project-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM agent_project_state WHERE project = ?`, project).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE action = 'write_project_state' AND reason LIKE ?`, "%"+project+"%").Error
	})

	deadline := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	want := cognitive.ProjectStateRecord{
		Phase:        "implementation",
		DeadlineDate: &deadline,
		Pressure:     "normal",
		UpdatedBy:    "agent",
	}

	require.NoError(t, store.WriteProjectState(ctx, project, want))

	got, err := store.ReadProjectState(ctx, project)
	require.NoError(t, err)
	require.Equal(t, want.Phase, got.Phase)
	require.NotNil(t, got.DeadlineDate)
	require.True(t, want.DeadlineDate.Equal(*got.DeadlineDate))
	require.Equal(t, want.Pressure, got.Pressure)
	require.Equal(t, want.UpdatedBy, got.UpdatedBy)
}

func TestStateStore_ProjectStateRejectsNonAgentUpdatedBy(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	store := NewStateStore(db, auditStore)
	ctx := context.Background()

	project := fmt.Sprintf("state-project-updated-by-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM agent_project_state WHERE project = ?`, project).Error
	})

	err := store.WriteProjectState(ctx, project, cognitive.ProjectStateRecord{
		Phase:     "operator-write",
		Pressure:  "normal",
		UpdatedBy: "operator",
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "updated_by")
	require.ErrorContains(t, err, "agent")
}

func TestStateStore_AuditLogWrittenOnSessionWrite(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	store := NewStateStore(db, auditStore)
	ctx := context.Background()

	sessionID := fmt.Sprintf("state-audit-session-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM agent_session_state WHERE session_id = ?`, sessionID).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE source_session_id = ? AND action = 'write_session_state'`, sessionID).Error
	})

	var entry AuditLogEntry
	require.NoError(t, store.WriteSessionState(ctx, sessionID, cognitive.SessionStateSlots{
		Focus: map[string]interface{}{"objective": "audit state write"},
	}))

	require.Eventually(t, func() bool {
		return db.Where("action = ? AND actor = ? AND source_session_id = ?", "write_session_state", "agent", sessionID).
			Order("id DESC").
			First(&entry).Error == nil
	}, 2*time.Second, 25*time.Millisecond, "state write must create audit evidence")
	require.NotZero(t, entry.CreatedAt)
	require.Contains(t, entry.Reason, "session state updated")
	require.NotNil(t, entry.AfterState)
	var after map[string]interface{}
	require.NoError(t, json.Unmarshal(*entry.AfterState, &after))
	focus, ok := after["focus"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "audit state write", focus["objective"])
}

func TestStateStore_AuditLogWrittenOnProjectWrite(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	store := NewStateStore(db, auditStore)
	ctx := context.Background()

	project := fmt.Sprintf("state-audit-project-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM agent_project_state WHERE project = ?`, project).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE action = 'write_project_state' AND reason LIKE ?`, "%"+project+"%").Error
	})

	require.NoError(t, store.WriteProjectState(ctx, project, cognitive.ProjectStateRecord{
		Phase:     "implementation",
		Pressure:  "normal",
		UpdatedBy: "agent",
	}))

	var entry AuditLogEntry
	require.Eventually(t, func() bool {
		return db.Where("action = ? AND actor = ? AND reason LIKE ?", "write_project_state", "agent", "%"+project+"%").
			Order("id DESC").
			First(&entry).Error == nil
	}, 2*time.Second, 25*time.Millisecond, "project state write must create audit evidence")
	require.NotZero(t, entry.CreatedAt)
	require.Contains(t, entry.Reason, project)
	require.NotNil(t, entry.AfterState)
	var after map[string]interface{}
	require.NoError(t, json.Unmarshal(*entry.AfterState, &after))
	require.Equal(t, "implementation", after["phase"])
	require.Equal(t, "normal", after["pressure"])
	require.Equal(t, "agent", after["updated_by"])
}

func TestStateStore_AuditLogWrittenOnResumeReadSourceChoice(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	store := NewStateStore(db, auditStore)
	ctx := context.Background()

	sessionID := fmt.Sprintf("state-audit-resume-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM agent_session_state WHERE session_id = ?`, sessionID).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE source_session_id = ?`, sessionID).Error
	})

	require.NoError(t, store.WriteSessionState(ctx, sessionID, cognitive.SessionStateSlots{
		Execution: map[string]interface{}{
			"next_action": "continue from audited native state",
		},
		Horizons: map[string]interface{}{
			"next_verification": "inspect read_resume_state audit row",
		},
	}))

	packet, err := store.ReadResumePacket(ctx, cognitive.ResumePacketRequest{
		Principal: "agent:developer",
		SessionID: sessionID,
		Scopes:    []cognitive.StateScopeKind{cognitive.StateScopeSession},
	})
	require.NoError(t, err)

	var entry AuditLogEntry
	require.Eventually(t, func() bool {
		return db.Where("action = ? AND actor = ? AND source_session_id = ?", "read_resume_state", "agent:developer", sessionID).
			Order("id DESC").
			First(&entry).Error == nil
	}, 2*time.Second, 25*time.Millisecond, "resume read must create source-choice audit evidence")

	require.NotZero(t, entry.CreatedAt)
	require.Contains(t, entry.Reason, "native")
	require.NotNil(t, entry.AfterState)
	var after map[string]interface{}
	require.NoError(t, json.Unmarshal(*entry.AfterState, &after))
	require.Equal(t, "native", after["result"])
	require.Equal(t, string(cognitive.StatePacketSourceNative), after["source"])
	require.Equal(t, packet.PacketID, after["packet_id"])
	require.Equal(t, packet.StateVersion, after["state_version"])
	require.Equal(t, false, after["allow_filesystem_fallback"])
	require.Equal(t, false, after["fallback_used"])
	require.Equal(t, float64(0), after["conflict_count"])
	require.Equal(t, []interface{}{"session"}, after["requested_scopes"])
}

func TestStateStore_LogResumeReadAuditWritesConflictEvidence(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	store := NewStateStore(db, auditStore)
	ctx := context.Background()

	sessionID := fmt.Sprintf("state-audit-conflict-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM audit_log WHERE source_session_id = ?`, sessionID).Error
	})

	packet := cognitive.ResumePacket{
		PacketID:     "resume:conflict-audit",
		Project:      "engram",
		Principal:    "agent:developer",
		SessionID:    sessionID,
		StateVersion: "2026-06-28T12:00:00Z",
		Source:       cognitive.StatePacketSourceMixed,
		FallbackUsed: true,
		FallbackPath: "fallback.json",
		Freshness:    cognitive.StateFreshnessStale,
		Drift: cognitive.StateDrift{
			Kind: cognitive.StateDriftConflict,
			Conflicts: []cognitive.StateConflict{{
				Field:      "next_action",
				Resolution: "native_retained_until_resolved",
			}},
		},
		EvidenceRefs: []string{"agent_session_state:" + sessionID + "@2026-06-28T12:00:00Z", "filesystem_fallback:fallback.json"},
		Scopes:       []cognitive.StateScopeKind{cognitive.StateScopeSession},
	}
	store.LogResumeReadAudit(ctx, cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               sessionID,
		Scopes:                  []cognitive.StateScopeKind{cognitive.StateScopeSession},
		AllowFilesystemFallback: true,
	}, packet, "read_resume_conflict", "mixed_conflict", "native and filesystem fallback disagreed")

	var entry AuditLogEntry
	require.Eventually(t, func() bool {
		return db.Where("action = ? AND actor = ? AND source_session_id = ?", "read_resume_conflict", "agent:developer", sessionID).
			Order("id DESC").
			First(&entry).Error == nil
	}, 2*time.Second, 25*time.Millisecond, "conflict read must create audit evidence")
	require.Contains(t, entry.Reason, "fallback")
	require.NotNil(t, entry.AfterState)
	var after map[string]interface{}
	require.NoError(t, json.Unmarshal(*entry.AfterState, &after))
	require.Equal(t, "mixed_conflict", after["result"])
	require.Equal(t, string(cognitive.StatePacketSourceMixed), after["source"])
	require.Equal(t, true, after["allow_filesystem_fallback"])
	require.Equal(t, true, after["fallback_used"])
	require.Equal(t, "fallback.json", after["fallback_path"])
	require.Equal(t, string(cognitive.StateDriftConflict), after["drift_kind"])
	require.Equal(t, float64(1), after["conflict_count"])
}

func TestStateStore_ResumePacketNativeRoundTrip(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	store := NewStateStore(db, auditStore)
	ctx := context.Background()

	sessionID := fmt.Sprintf("state-resume-session-%d", time.Now().UnixNano())
	project := fmt.Sprintf("state-resume-project-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM agent_session_state WHERE session_id = ?`, sessionID).Error
		_ = db.Exec(`DELETE FROM agent_project_state WHERE project = ?`, project).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE source_session_id = ? OR reason LIKE ?`, sessionID, "%"+project+"%").Error
	})

	require.NoError(t, store.WriteSessionState(ctx, sessionID, cognitive.SessionStateSlots{
		Execution: map[string]interface{}{
			"next_action": map[string]interface{}{
				"kind":        "command",
				"description": "run state surface tests",
				"command":     "go test ./internal/mcp ./internal/worker",
			},
		},
		Horizons: map[string]interface{}{
			"next_verification": map[string]interface{}{
				"kind":        "command",
				"description": "run full suite",
				"command":     "go test ./...",
			},
			"evidence_refs": []interface{}{"state:evidence:resume", "audit:write_session_state"},
		},
	}))
	require.NoError(t, store.WriteProjectState(ctx, project, cognitive.ProjectStateRecord{
		Phase:     "state-surface",
		Pressure:  "normal",
		UpdatedBy: "agent",
	}))

	packet, err := store.ReadResumePacket(ctx, cognitive.ResumePacketRequest{
		Project:   project,
		Principal: "agent:developer",
		SessionID: sessionID,
		GoalID:    "goal-1",
		TaskID:    "task-1",
		Scopes:    []cognitive.StateScopeKind{cognitive.StateScopeSession, cognitive.StateScopeProject, cognitive.StateScopeGoal, cognitive.StateScopeTask},
	})
	require.NoError(t, err)
	require.NotEmpty(t, packet.PacketID)
	require.Len(t, packet.PacketID, len("resume:")+64)
	require.NotEqual(t,
		resumePacketID(cognitive.ResumePacketRequest{Project: "a", Principal: "b:c", SessionID: "d"}, "e"),
		resumePacketID(cognitive.ResumePacketRequest{Project: "a:b", Principal: "c", SessionID: "d"}, "e"),
	)
	require.NotEmpty(t, packet.StateVersion)
	require.Equal(t, project, packet.Project)
	require.Equal(t, "agent:developer", packet.Principal)
	require.Equal(t, sessionID, packet.SessionID)
	require.Equal(t, "goal-1", packet.GoalID)
	require.Equal(t, "task-1", packet.TaskID)
	require.ElementsMatch(t, []cognitive.StateScopeKind{cognitive.StateScopeSession, cognitive.StateScopeProject, cognitive.StateScopeGoal, cognitive.StateScopeTask}, packet.Scopes)
	require.Equal(t, fmt.Sprintf("agent_session_state:%s@%s", sessionID, packet.StateVersion), packet.EvidenceRefs[0])
	require.Contains(t, packet.EvidenceRefs, "state:evidence:resume")
	require.Contains(t, packet.EvidenceRefs, "audit:write_session_state")
	require.Equal(t, cognitive.StatePacketSourceNative, packet.Source)
	require.False(t, packet.FallbackUsed)
	require.Equal(t, cognitive.StateFreshnessFresh, packet.Freshness)
	require.Equal(t, cognitive.StateDriftNone, packet.Drift.Kind)
	require.Equal(t, cognitive.StateActionCommand, packet.NextAction.Kind)
	require.Equal(t, "run state surface tests", packet.NextAction.Description)
	require.Equal(t, "go test ./internal/mcp ./internal/worker", packet.NextAction.Command)
	require.Equal(t, cognitive.StateVerificationCommand, packet.NextVerification.Kind)
	require.Equal(t, "run full suite", packet.NextVerification.Description)
	require.Equal(t, "go test ./...", packet.NextVerification.Command)
	require.NotContains(t, string(packet.Source), "filesystem")
}

func TestStateStore_ResumePacketProjectOnlyRequiresSessionID(t *testing.T) {
	store := &StateStore{}

	_, err := store.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:   "engram",
		Principal: "agent:developer",
		Scopes:    []cognitive.StateScopeKind{cognitive.StateScopeProject},
	})

	require.ErrorContains(t, err, "state_store read_resume: session_id is required")
}

func TestResumePacketIDIncludesGoalAndTaskBindings(t *testing.T) {
	stateVersion := "2026-06-28T12:00:00Z"
	base := cognitive.ResumePacketRequest{
		Project:   "engram",
		Principal: "agent:developer",
		SessionID: "session-1",
		GoalID:    "goal-a",
		TaskID:    "task-a",
		Scopes:    []cognitive.StateScopeKind{cognitive.StateScopeSession},
	}

	differentGoal := base
	differentGoal.GoalID = "goal-b"
	differentTask := base
	differentTask.TaskID = "task-b"
	differentScopes := base
	differentScopes.Scopes = []cognitive.StateScopeKind{cognitive.StateScopeSession, cognitive.StateScopeTask}
	spaced := base
	spaced.GoalID = " goal-a "
	spaced.TaskID = " task-a "

	baseID := resumePacketID(base, stateVersion)
	require.NotEqual(t, baseID, resumePacketID(differentGoal, stateVersion))
	require.NotEqual(t, baseID, resumePacketID(differentTask, stateVersion))
	require.NotEqual(t, baseID, resumePacketID(differentScopes, stateVersion))
	require.Equal(t, baseID, resumePacketID(spaced, stateVersion))
}

func TestStateStore_ResumePacketDoesNotRequireProjectState(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	store := NewStateStore(db, auditStore)
	ctx := context.Background()

	sessionID := fmt.Sprintf("state-resume-session-only-%d", time.Now().UnixNano())
	project := fmt.Sprintf("state-resume-missing-project-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM agent_session_state WHERE session_id = ?`, sessionID).Error
		_ = db.Exec(`DELETE FROM agent_project_state WHERE project = ?`, project).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE source_session_id = ? OR reason LIKE ?`, sessionID, "%"+project+"%").Error
	})

	require.NoError(t, store.WriteSessionState(ctx, sessionID, cognitive.SessionStateSlots{
		Execution: map[string]interface{}{
			"next_action": "continue from session-only state",
		},
		Horizons: map[string]interface{}{
			"next_verification": "go test ./internal/db/gorm",
		},
	}))

	packet, err := store.ReadResumePacket(ctx, cognitive.ResumePacketRequest{
		Project:   project,
		SessionID: sessionID,
		Principal: "agent:developer",
		Scopes:    []cognitive.StateScopeKind{cognitive.StateScopeSession},
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.StatePacketSourceNative, packet.Source)
	require.Equal(t, project, packet.Project)
	require.Equal(t, sessionID, packet.SessionID)
	require.Contains(t, packet.Scopes, cognitive.StateScopeSession)
	require.Equal(t, "agent:developer", packet.Principal)
	require.Equal(t, []string{fmt.Sprintf("agent_session_state:%s@%s", sessionID, packet.StateVersion)}, packet.EvidenceRefs)
	require.False(t, packet.FallbackUsed)
	require.NotContains(t, packet.Scopes, cognitive.StateScopeProject)
	require.Equal(t, "continue from session-only state", packet.NextAction.Description)
}

func TestStateStore_ResumePacketProjectScopeRequiresNativeProjectState(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	store := NewStateStore(db, auditStore)
	ctx := context.Background()

	sessionID := fmt.Sprintf("state-resume-project-missing-%d", time.Now().UnixNano())
	project := fmt.Sprintf("state-resume-project-scope-missing-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM agent_session_state WHERE session_id = ?`, sessionID).Error
		_ = db.Exec(`DELETE FROM agent_project_state WHERE project = ?`, project).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE source_session_id = ? OR reason LIKE ?`, sessionID, "%"+project+"%").Error
	})

	require.NoError(t, store.WriteSessionState(ctx, sessionID, cognitive.SessionStateSlots{
		Execution: map[string]interface{}{
			"next_action": "continue from native session state",
		},
		Horizons: map[string]interface{}{
			"next_verification": "go test ./internal/db/gorm",
		},
	}))

	_, err := store.ReadResumePacket(ctx, cognitive.ResumePacketRequest{
		Project:   project,
		SessionID: sessionID,
		Principal: "agent:developer",
		Scopes:    []cognitive.StateScopeKind{cognitive.StateScopeSession, cognitive.StateScopeProject},
	})

	require.ErrorContains(t, err, "read_resume project")
	require.ErrorContains(t, err, project)
}

func TestStateStore_ResumePacketRejectsMissingPrincipalBeforeDB(t *testing.T) {
	store := &StateStore{}

	_, err := store.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:   "state-resume-project",
		SessionID: "session-1",
	})

	require.ErrorContains(t, err, "principal is required")
}

func TestStateStore_ResumePacketRejectsMissingPrincipal(t *testing.T) {
	db := openCandidateTestDB(t)
	auditStore := NewAuditStore(db)
	store := NewStateStore(db, auditStore)
	ctx := context.Background()

	sessionID := fmt.Sprintf("state-resume-missing-principal-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM agent_session_state WHERE session_id = ?`, sessionID).Error
		_ = db.Exec(`DELETE FROM audit_log WHERE source_session_id = ?`, sessionID).Error
	})

	require.NoError(t, store.WriteSessionState(ctx, sessionID, cognitive.SessionStateSlots{
		Execution: map[string]interface{}{
			"next_action": "continue from session state",
		},
		Horizons: map[string]interface{}{
			"next_verification": "run focused state tests",
		},
	}))

	_, err := store.ReadResumePacket(ctx, cognitive.ResumePacketRequest{
		Project:   "state-resume-project",
		SessionID: sessionID,
	})

	require.ErrorContains(t, err, "principal is required")
}

func TestStateStore_ResumePacketRejectsUnscopedRequestBeforeDB(t *testing.T) {
	store := &StateStore{}

	_, err := store.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Principal: "agent:developer",
		SessionID: "session-1",
	})

	require.ErrorContains(t, err, "scopes is required")
}

func TestStateStore_ResumePacketRejectsScopeWithoutIdentifierBeforeDB(t *testing.T) {
	tests := []struct {
		name    string
		request cognitive.ResumePacketRequest
		want    string
	}{
		{
			name: "session scope missing session id",
			request: cognitive.ResumePacketRequest{
				Project:   "engram",
				Principal: "agent:developer",
				Scopes:    []cognitive.StateScopeKind{cognitive.StateScopeSession},
			},
			want: "session_id is required",
		},
		{
			name: "project scope missing project",
			request: cognitive.ResumePacketRequest{
				Principal: "agent:developer",
				SessionID: "session-1",
				Scopes:    []cognitive.StateScopeKind{cognitive.StateScopeProject},
			},
			want: "project is required for project scope",
		},
		{
			name: "goal scope missing goal id",
			request: cognitive.ResumePacketRequest{
				Project:   "engram",
				Principal: "agent:developer",
				SessionID: "session-1",
				Scopes:    []cognitive.StateScopeKind{cognitive.StateScopeGoal},
			},
			want: "goal_id is required for goal scope",
		},
		{
			name: "task scope missing task id",
			request: cognitive.ResumePacketRequest{
				Project:   "engram",
				Principal: "agent:developer",
				SessionID: "session-1",
				Scopes:    []cognitive.StateScopeKind{cognitive.StateScopeTask},
			},
			want: "task_id is required for task scope",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &StateStore{}

			_, err := store.ReadResumePacket(context.Background(), tt.request)

			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestStateEvidenceRefsFromSlotsIncludesNativeAndSlotRefs(t *testing.T) {
	refs := stateEvidenceRefsFromSlots(cognitive.SessionStateSlots{
		Focus: map[string]interface{}{
			"evidence_refs": []string{"slot:focus"},
		},
		Execution: map[string]interface{}{
			"evidence_refs": "slot:execution",
		},
		Horizons: map[string]interface{}{
			"evidence_refs": []interface{}{"slot:horizon", 42, true, map[string]interface{}{"bad": "ref"}, "slot:horizon"},
		},
	}, "session-1", "v2")

	require.Equal(t, []string{
		"agent_session_state:session-1@v2",
		"slot:horizon",
		"slot:execution",
		"slot:focus",
	}, refs)
}
