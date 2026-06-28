package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/cognitive"
)

type fakeWorkerStatePlane struct {
	session     cognitive.SessionStateSlots
	project     cognitive.ProjectStateRecord
	packet      cognitive.ResumePacket
	lastRequest cognitive.ResumePacketRequest
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
	return f.packet, nil
}

func TestHandleGetStateResumeReturnsBoundedPacket(t *testing.T) {
	fakeStore := &fakeWorkerStatePlane{
		packet: cognitive.ResumePacket{
			Source:           cognitive.StatePacketSourceNative,
			Freshness:        cognitive.StateFreshnessFresh,
			Drift:            cognitive.StateDrift{Kind: cognitive.StateDriftNone, CheckedAt: time.Now().UTC()},
			NextAction:       cognitive.StateAction{Kind: cognitive.StateActionInstruction, Description: "continue T003"},
			NextVerification: cognitive.StateVerification{Kind: cognitive.StateVerificationCommand, Description: "focused tests", Command: "go test ./internal/worker"},
			GeneratedAt:      time.Now().UTC(),
			Project:          "engram",
			SessionID:        "session-1",
		},
	}
	svc := &Service{stateStore: fakeStore}

	req := httptest.NewRequest(http.MethodGet, "/api/state/resume?project=engram&session_id=session-1&allow_filesystem_fallback=true", nil)
	rec := httptest.NewRecorder()
	svc.handleGetStateResume(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var packet cognitive.ResumePacket
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &packet))
	require.Equal(t, cognitive.StatePacketSourceNative, packet.Source)
	require.Empty(t, packet.FallbackPath)
	require.True(t, fakeStore.lastRequest.AllowFilesystemFallback)
}
