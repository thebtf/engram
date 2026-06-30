package stateplane

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/cognitive"
)

type fakeNativePlane struct {
	packet cognitive.ResumePacket
	err    error
	reads  int
}

func (f *fakeNativePlane) WriteSessionState(context.Context, string, cognitive.SessionStateSlots) error {
	return nil
}

func (f *fakeNativePlane) WriteProjectState(context.Context, string, cognitive.ProjectStateRecord) error {
	return nil
}

func (f *fakeNativePlane) ReadSessionState(context.Context, string) (cognitive.SessionStateSlots, error) {
	return cognitive.SessionStateSlots{}, nil
}

func (f *fakeNativePlane) ReadProjectState(context.Context, string) (cognitive.ProjectStateRecord, error) {
	return cognitive.ProjectStateRecord{}, nil
}

func (f *fakeNativePlane) ReadResumePacket(context.Context, cognitive.ResumePacketRequest) (cognitive.ResumePacket, error) {
	f.reads++
	if f.err != nil {
		return cognitive.ResumePacket{}, f.err
	}
	return f.packet, nil
}

type countingFallbackReader struct {
	packet cognitive.ResumePacket
	err    error
	reads  int
}

func (f *countingFallbackReader) ReadResumePacket(context.Context, cognitive.ResumePacketRequest) (cognitive.ResumePacket, error) {
	f.reads++
	if f.err != nil {
		return cognitive.ResumePacket{}, f.err
	}
	return f.packet, nil
}

func TestServiceReadResumePacketNativeFirstDoesNotOpenFallback(t *testing.T) {
	native := &fakeNativePlane{packet: nativePacket("native next", "go test ./internal/stateplane")}
	fallback := &countingFallbackReader{packet: fallbackPacket("fallback next", "fallback.json")}
	service := NewService(native, fallback)

	packet, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:   "engram",
		Principal: "agent:developer",
		SessionID: "session-1",
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.StatePacketSourceNative, packet.Source)
	require.Equal(t, "native next", packet.NextAction.Description)
	require.Equal(t, 1, native.reads)
	require.Equal(t, 0, fallback.reads)
	require.Empty(t, packet.FallbackPath)
}

func TestServiceReadResumePacketExplicitFallbackMarkerOnNativeMiss(t *testing.T) {
	fallbackPath := writeFallbackPacket(t, fallbackPacket("fallback next", ""))
	service := NewService(
		&fakeNativePlane{err: errors.New("native missing")},
		JSONFileFallbackReader{Path: fallbackPath},
	)

	packet, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		AllowFilesystemFallback: true,
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.StatePacketSourceFilesystemFallback, packet.Source)
	require.Equal(t, cognitive.StateFreshnessUnknown, packet.Freshness)
	require.Equal(t, cognitive.StateDriftUnknown, packet.Drift.Kind)
	require.Equal(t, fallbackPath, packet.FallbackPath)
	require.Equal(t, "fallback next", packet.NextAction.Description)
	require.Equal(t, "agent:developer", packet.Principal)
	require.True(t, packet.FallbackUsed)
	require.NotEmpty(t, packet.EvidenceRefs)
}

func TestServiceReadResumePacketConflictWhenFallbackDisagrees(t *testing.T) {
	fallbackPath := writeFallbackPacket(t, fallbackPacket("fallback next", ""))
	service := NewService(
		&fakeNativePlane{packet: nativePacket("native next", "go test ./internal/stateplane")},
		JSONFileFallbackReader{Path: fallbackPath},
	)

	packet, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		AllowFilesystemFallback: true,
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.StatePacketSourceMixed, packet.Source)
	require.Equal(t, cognitive.StateDriftConflict, packet.Drift.Kind)
	require.Equal(t, fallbackPath, packet.FallbackPath)
	require.NotEmpty(t, packet.Drift.Conflicts)
	require.Equal(t, "agent:developer", packet.Principal)
	require.True(t, packet.FallbackUsed)
	require.NotEmpty(t, packet.EvidenceRefs)
	require.Equal(t, "next_action", packet.Drift.Conflicts[0].Field)
	require.Equal(t, "native_retained_until_resolved", packet.Drift.Conflicts[0].Resolution)
}

func TestServiceReadResumePacketReportsNewerFallbackDriftWhenActionsMatch(t *testing.T) {
	native := nativePacket("native next", "go test ./internal/stateplane")
	fallback := fallbackPacket("native next", "fallback.json")
	fallback.NextVerification = native.NextVerification
	fallback.GeneratedAt = native.GeneratedAt.Add(time.Hour)
	service := NewService(
		&fakeNativePlane{packet: native},
		&countingFallbackReader{packet: fallback},
	)

	packet, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		AllowFilesystemFallback: true,
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.StatePacketSourceMixed, packet.Source)
	require.Equal(t, cognitive.StateFreshnessStale, packet.Freshness)
	require.Equal(t, cognitive.StateDriftFallbackNewer, packet.Drift.Kind)
	require.Equal(t, "fallback.json", packet.FallbackPath)
	require.Equal(t, "agent:developer", packet.Principal)
	require.True(t, packet.FallbackUsed)
	require.NotEmpty(t, packet.EvidenceRefs)
	require.Equal(t, "native next", packet.NextAction.Description)
}

func TestServiceReadResumePacketNativeErrorWithoutFallbackAllowed(t *testing.T) {
	nativeErr := errors.New("native missing")
	service := NewService(&fakeNativePlane{err: nativeErr}, JSONFileFallbackReader{Path: "fallback.json"})

	_, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:   "engram",
		Principal: "agent:developer",
		SessionID: "session-1",
	})

	require.ErrorIs(t, err, nativeErr)
}

func TestServiceReadResumePacketRejectsIndeterminateFallbackPacket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fallback-resume.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"source":"filesystem_fallback"}`), 0o600))
	service := NewService(
		&fakeNativePlane{err: errors.New("native missing")},
		JSONFileFallbackReader{Path: path},
	)

	_, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		AllowFilesystemFallback: true,
	})

	require.ErrorContains(t, err, "next_action.description is required")
}

func TestServiceReadResumePacketRejectsMissingPrincipalBeforeFallback(t *testing.T) {
	service := NewService(
		&fakeNativePlane{err: errors.New("native missing")},
		&countingFallbackReader{packet: fallbackPacket("fallback next", "fallback.json")},
	)

	_, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		SessionID:               "session-1",
		AllowFilesystemFallback: true,
	})

	require.ErrorContains(t, err, "principal is required")
}

func TestServiceReadResumePacketRejectsFallbackRequestProjectMismatchOnNativeMiss(t *testing.T) {
	packet := fallbackPacket("fallback next", "")
	packet.Project = "other-project"
	packet.SessionID = "session-1"
	path := writeFallbackPacketJSON(t, packet)
	service := NewService(
		&fakeNativePlane{err: errors.New("native missing")},
		JSONFileFallbackReader{Path: path},
	)

	_, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		AllowFilesystemFallback: true,
	})

	require.ErrorContains(t, err, "project")
	require.ErrorContains(t, err, "resume request")
}

func TestServiceReadResumePacketRejectsFallbackRequestSessionMismatchBeforeConflict(t *testing.T) {
	packet := fallbackPacket("fallback next", "")
	packet.Project = "engram"
	packet.SessionID = "other-session"
	path := writeFallbackPacketJSON(t, packet)
	service := NewService(
		&fakeNativePlane{packet: nativePacket("native next", "go test ./internal/stateplane")},
		JSONFileFallbackReader{Path: path},
	)

	_, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		AllowFilesystemFallback: true,
	})

	require.ErrorContains(t, err, "session_id")
	require.ErrorContains(t, err, "resume request")
}

func nativePacket(actionDescription, verificationCommand string) cognitive.ResumePacket {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	return cognitive.ResumePacket{
		Source:           cognitive.StatePacketSourceNative,
		Freshness:        cognitive.StateFreshnessFresh,
		Drift:            cognitive.StateDrift{Kind: cognitive.StateDriftNone, CheckedAt: now},
		NextAction:       cognitive.StateAction{Kind: cognitive.StateActionCommand, Description: actionDescription, Command: "go test ./internal/stateplane"},
		NextVerification: cognitive.StateVerification{Kind: cognitive.StateVerificationCommand, Description: "focused stateplane tests", Command: verificationCommand},
		GeneratedAt:      now,
		Project:          "engram",
		SessionID:        "session-1",
		Principal:        "agent:developer",
		Scopes:           []cognitive.StateScopeKind{cognitive.StateScopeSession},
		EvidenceRefs:     []string{"agent_session_state:session-1@2026-06-28T12:00:00Z"},
	}
}

func fallbackPacket(actionDescription, fallbackPath string) cognitive.ResumePacket {
	packet := nativePacket(actionDescription, "go test ./...")
	packet.Source = cognitive.StatePacketSourceFilesystemFallback
	packet.Freshness = cognitive.StateFreshnessUnknown
	packet.Drift = cognitive.StateDrift{Kind: cognitive.StateDriftUnknown, CheckedAt: packet.GeneratedAt}
	packet.FallbackPath = fallbackPath
	packet.FallbackUsed = true
	return packet
}

func writeFallbackPacket(t *testing.T, packet cognitive.ResumePacket) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fallback-resume.json")
	data := []byte(`{
		"source":"filesystem_fallback",
		"freshness":"unknown",
		"drift":{"kind":"unknown"},
		"next_action":{"kind":"command","description":"` + packet.NextAction.Description + `","command":"go test ./internal/stateplane"},
		"next_verification":{"kind":"command","description":"focused stateplane tests","command":"go test ./..."},
		"generated_at":"2026-06-28T12:00:00Z",
		"project":"engram",
		"session_id":"session-1",
		"scopes":["session"]
	}`)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func writeFallbackPacketJSON(t *testing.T, packet cognitive.ResumePacket) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fallback-resume.json")
	data, err := json.Marshal(packet)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}
