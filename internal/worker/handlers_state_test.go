package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/pkg/cognitive"
)

type fakeWorkerStatePlane struct {
	session     cognitive.SessionStateSlots
	project     cognitive.ProjectStateRecord
	packet      cognitive.ResumePacket
	lastRequest cognitive.ResumePacketRequest
	resumeErr   error
}

func (f *fakeWorkerStatePlane) WriteSessionState(context.Context, string, cognitive.SessionStateSlots) error {
	return nil
}

func (f *fakeWorkerStatePlane) WriteProjectState(context.Context, string, cognitive.ProjectStateRecord) error {
	return nil
}

func (f *fakeWorkerStatePlane) ReadSessionState(context.Context, string) (cognitive.SessionStateSlots, error) {
	return f.session, nil
}

func (f *fakeWorkerStatePlane) ReadProjectState(context.Context, string) (cognitive.ProjectStateRecord, error) {
	return f.project, nil
}

func (f *fakeWorkerStatePlane) ReadResumePacket(_ context.Context, request cognitive.ResumePacketRequest) (cognitive.ResumePacket, error) {
	f.lastRequest = request
	if f.resumeErr != nil {
		return cognitive.ResumePacket{}, f.resumeErr
	}
	return f.packet, nil
}

func TestHandleGetStateResumeReturnsBoundedPacket(t *testing.T) {
	fakeStore := &fakeWorkerStatePlane{
		packet: cognitive.ResumePacket{
			Source:           cognitive.StatePacketSourceNative,
			Freshness:        cognitive.StateFreshnessFresh,
			Drift:            cognitive.StateDrift{Kind: cognitive.StateDriftNone, Conflicts: []cognitive.StateConflict{}, CheckedAt: time.Now().UTC()},
			NextAction:       cognitive.StateAction{Kind: cognitive.StateActionInstruction, Description: "continue T003"},
			NextVerification: cognitive.StateVerification{Kind: cognitive.StateVerificationCommand, Description: "focused tests", Command: "go test ./internal/worker"},
			GeneratedAt:      time.Now().UTC(),
			Project:          "engram",
			Principal:        "agent:developer",
			SessionID:        "session-1",
		},
	}
	svc := &Service{stateStore: fakeStore}

	req := httptest.NewRequest(http.MethodGet, "/api/state/resume?project=engram&principal=agent:developer&session_id=session-1", nil)
	rec := httptest.NewRecorder()
	svc.handleGetStateResume(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var packet cognitive.ResumePacket
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &packet))
	require.Equal(t, cognitive.StatePacketSourceNative, packet.Source)
	require.Empty(t, packet.FallbackPath)
	require.False(t, fakeStore.lastRequest.AllowFilesystemFallback)
	require.Equal(t, "agent:developer", fakeStore.lastRequest.Principal)
	require.Equal(t, "session-1", fakeStore.lastRequest.SessionID)
	require.ElementsMatch(t, []cognitive.StateScopeKind{cognitive.StateScopeSession, cognitive.StateScopeProject}, fakeStore.lastRequest.Scopes)
}

func TestHandleGetStateResumeUsesAuthenticatedPrincipalOverQuery(t *testing.T) {
	fakeStore := &fakeWorkerStatePlane{
		packet: cognitive.ResumePacket{
			Source:    cognitive.StatePacketSourceNative,
			Project:   "engram",
			Principal: "agent/alice",
			SessionID: "session-1",
		},
	}
	svc := &Service{stateStore: fakeStore}

	req := httptest.NewRequest(http.MethodGet, "/api/state/resume?project=engram&principal=agent/mallory&session_id=session-1&goal_id=goal-1&task_id=task-1", nil).
		WithContext(auth.WithIdentity(context.Background(), auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent)))
	rec := httptest.NewRecorder()
	svc.handleGetStateResume(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "agent/alice", fakeStore.lastRequest.Principal)
	require.Equal(t, "engram", fakeStore.lastRequest.Project)
	require.Equal(t, "session-1", fakeStore.lastRequest.SessionID)
	require.Equal(t, "goal-1", fakeStore.lastRequest.GoalID)
	require.Equal(t, "task-1", fakeStore.lastRequest.TaskID)
	require.ElementsMatch(t, []cognitive.StateScopeKind{cognitive.StateScopeSession, cognitive.StateScopeProject, cognitive.StateScopeGoal, cognitive.StateScopeTask}, fakeStore.lastRequest.Scopes)
}

func TestHandleGetStateResumeRejectsFilesystemFallbackOption(t *testing.T) {
	svc := &Service{stateStore: &fakeWorkerStatePlane{}}

	req := httptest.NewRequest(http.MethodGet, "/api/state/resume?project=engram&principal=agent:developer&session_id=session-1&allow_filesystem_fallback=true", nil)
	rec := httptest.NewRecorder()
	svc.handleGetStateResume(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "allow_filesystem_fallback is not supported")
}

func TestHandleGetStateResumeRequiresPrincipalWithoutAuth(t *testing.T) {
	svc := &Service{stateStore: &fakeWorkerStatePlane{}}

	req := httptest.NewRequest(http.MethodGet, "/api/state/resume?project=engram&session_id=session-1", nil)
	rec := httptest.NewRecorder()
	svc.handleGetStateResume(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "principal is required")
}

func TestHandleGetStateResumeRequiresNonBlankSessionID(t *testing.T) {
	svc := &Service{stateStore: &fakeWorkerStatePlane{}}

	req := httptest.NewRequest(http.MethodGet, "/api/state/resume?project=engram&principal=agent:developer&session_id=%20%20", nil)
	rec := httptest.NewRecorder()
	svc.handleGetStateResume(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "session_id is required")
}

func TestHandleGetStateResumeHidesInternalReadError(t *testing.T) {
	fakeStore := &fakeWorkerStatePlane{
		resumeErr: errors.New("database dsn credential leaked in wrapped error"),
	}
	svc := &Service{stateStore: fakeStore}

	req := httptest.NewRequest(http.MethodGet, "/api/state/resume?project=engram&principal=agent:developer&session_id=session-1", nil)
	rec := httptest.NewRecorder()
	svc.handleGetStateResume(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "state read failed")
	require.NotContains(t, rec.Body.String(), "database dsn credential")
}
