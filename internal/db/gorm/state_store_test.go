package gorm

import (
	"context"
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

	before := countAuditRows(t, db, "write_session_state")
	require.NoError(t, store.WriteSessionState(ctx, sessionID, cognitive.SessionStateSlots{
		Focus: map[string]interface{}{"objective": "audit state write"},
	}))

	require.Eventually(t, func() bool {
		return countAuditRows(t, db, "write_session_state") > before
	}, 2*time.Second, 25*time.Millisecond, "state write must create audit evidence")
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
		},
	}))
	require.NoError(t, store.WriteProjectState(ctx, project, cognitive.ProjectStateRecord{
		Phase:     "state-surface",
		Pressure:  "normal",
		UpdatedBy: "agent",
	}))

	packet, err := store.ReadResumePacket(ctx, cognitive.ResumePacketRequest{
		Project:   project,
		SessionID: sessionID,
	})
	require.NoError(t, err)
	require.Equal(t, cognitive.StatePacketSourceNative, packet.Source)
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
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.StatePacketSourceNative, packet.Source)
	require.Equal(t, project, packet.Project)
	require.Equal(t, sessionID, packet.SessionID)
	require.Contains(t, packet.Scopes, cognitive.StateScopeSession)
	require.NotContains(t, packet.Scopes, cognitive.StateScopeProject)
	require.Equal(t, "continue from session-only state", packet.NextAction.Description)
}
