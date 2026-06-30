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

type resumeAuditCall struct {
	request cognitive.ResumePacketRequest
	packet  cognitive.ResumePacket
	action  string
	result  string
	reason  string
}

type fakeNativePlane struct {
	packet   cognitive.ResumePacket
	err      error
	reads    int
	requests []cognitive.ResumePacketRequest
	audits   []resumeAuditCall
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

func (f *fakeNativePlane) ReadResumePacket(_ context.Context, request cognitive.ResumePacketRequest) (cognitive.ResumePacket, error) {
	f.reads++
	f.requests = append(f.requests, request)
	if f.err != nil {
		return cognitive.ResumePacket{}, f.err
	}
	return f.packet, nil
}

func (f *fakeNativePlane) LogResumeReadAudit(_ context.Context, request cognitive.ResumePacketRequest, packet cognitive.ResumePacket, action, result, reason string) {
	f.audits = append(f.audits, resumeAuditCall{
		request: request,
		packet:  packet,
		action:  action,
		result:  result,
		reason:  reason,
	})
}

type countingFallbackReader struct {
	packet   cognitive.ResumePacket
	err      error
	reads    int
	requests []cognitive.ResumePacketRequest
}

func (f *countingFallbackReader) ReadResumePacket(_ context.Context, request cognitive.ResumePacketRequest) (cognitive.ResumePacket, error) {
	f.reads++
	f.requests = append(f.requests, request)
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
		Scopes:    []cognitive.StateScopeKind{cognitive.StateScopeSession},
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.StatePacketSourceNative, packet.Source)
	require.Equal(t, "native next", packet.NextAction.Description)
	require.Equal(t, 1, native.reads)
	require.Equal(t, 0, fallback.reads)
	require.Empty(t, packet.FallbackPath)
}

func TestServiceReadResumePacketNormalizesAndDeduplicatesScopesBeforeReads(t *testing.T) {
	native := &fakeNativePlane{err: errors.New("native missing")}
	fallback := &countingFallbackReader{packet: fallbackPacket("fallback next", "fallback.json")}
	service := NewService(native, fallback)

	packet, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 " engram ",
		Principal:               " agent:developer ",
		SessionID:               " session-1 ",
		Scopes:                  []cognitive.StateScopeKind{" session ", cognitive.StateScopeSession, " project ", cognitive.StateScopeProject},
		AllowFilesystemFallback: true,
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.StatePacketSourceFilesystemFallback, packet.Source)
	require.Equal(t, 1, native.reads)
	require.Equal(t, 1, fallback.reads)
	wantRequest := cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		Scopes:                  []cognitive.StateScopeKind{cognitive.StateScopeSession, cognitive.StateScopeProject},
		AllowFilesystemFallback: true,
	}
	require.Equal(t, []cognitive.ResumePacketRequest{wantRequest}, native.requests)
	require.Equal(t, []cognitive.ResumePacketRequest{wantRequest}, fallback.requests)
}

func TestServiceReadResumePacketRejectsUnscopedRequest(t *testing.T) {
	native := &fakeNativePlane{packet: nativePacket("native next", "go test ./internal/stateplane")}
	fallback := &countingFallbackReader{packet: fallbackPacket("fallback next", "fallback.json")}
	service := NewService(native, fallback)

	_, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		AllowFilesystemFallback: true,
	})

	require.ErrorContains(t, err, "scopes is required")
	require.Equal(t, 0, native.reads)
	require.Equal(t, 0, fallback.reads)
}

func TestServiceReadResumePacketRejectsScopeWithoutIdentifier(t *testing.T) {
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
			want: "session_id is required for session scope",
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
			native := &fakeNativePlane{packet: nativePacket("native next", "go test ./internal/stateplane")}
			service := NewService(native, nil)

			_, err := service.ReadResumePacket(context.Background(), tt.request)

			require.ErrorContains(t, err, tt.want)
			require.Equal(t, 0, native.reads)
		})
	}
}

func TestServiceReadResumePacketRejectsNativePacketMissingDeterministicFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*cognitive.ResumePacket)
		want   string
	}{
		{name: "source", mutate: func(packet *cognitive.ResumePacket) { packet.Source = "" }, want: "source"},
		{name: "packet id", mutate: func(packet *cognitive.ResumePacket) { packet.PacketID = "" }, want: "packet_id is required"},
		{name: "state version", mutate: func(packet *cognitive.ResumePacket) { packet.StateVersion = "" }, want: "state_version is required"},
		{name: "generated at", mutate: func(packet *cognitive.ResumePacket) { packet.GeneratedAt = time.Time{} }, want: "generated_at is required"},
		{name: "evidence refs", mutate: func(packet *cognitive.ResumePacket) { packet.EvidenceRefs = nil }, want: "evidence_refs is required"},
		{name: "scopes", mutate: func(packet *cognitive.ResumePacket) { packet.Scopes = nil }, want: "scopes is required"},
		{name: "next action kind", mutate: func(packet *cognitive.ResumePacket) { packet.NextAction.Kind = "" }, want: "next_action.kind is required"},
		{name: "next action description", mutate: func(packet *cognitive.ResumePacket) { packet.NextAction.Description = "" }, want: "next_action.description is required"},
		{name: "next verification kind", mutate: func(packet *cognitive.ResumePacket) { packet.NextVerification.Kind = "" }, want: "next_verification.kind is required"},
		{name: "next verification description", mutate: func(packet *cognitive.ResumePacket) { packet.NextVerification.Description = "" }, want: "next_verification.description is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			packet := nativePacket("native next", "go test ./internal/stateplane")
			tt.mutate(&packet)
			service := NewService(&fakeNativePlane{packet: packet}, &countingFallbackReader{packet: fallbackPacket("fallback next", "fallback.json")})

			_, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
				Project:   "engram",
				Principal: "agent:developer",
				SessionID: "session-1",
				Scopes:    []cognitive.StateScopeKind{cognitive.StateScopeSession},
			})

			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestServiceReadResumePacketRejectsCommandKindMissingCommand(t *testing.T) {
	tests := []struct {
		name     string
		fallback bool
		mutate   func(*cognitive.ResumePacket)
		want     string
	}{
		{
			name:   "native action",
			mutate: func(packet *cognitive.ResumePacket) { packet.NextAction.Command = "" },
			want:   "next_action.command is required",
		},
		{
			name:   "native verification",
			mutate: func(packet *cognitive.ResumePacket) { packet.NextVerification.Command = "" },
			want:   "next_verification.command is required",
		},
		{
			name:     "fallback action",
			fallback: true,
			mutate:   func(packet *cognitive.ResumePacket) { packet.NextAction.Command = "" },
			want:     "next_action.command is required",
		},
		{
			name:     "fallback verification",
			fallback: true,
			mutate:   func(packet *cognitive.ResumePacket) { packet.NextVerification.Command = "" },
			want:     "next_verification.command is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			packet := nativePacket("native next", "go test ./internal/stateplane")
			if tt.fallback {
				packet = fallbackPacket("fallback next", "fallback.json")
			}
			tt.mutate(&packet)

			request := cognitive.ResumePacketRequest{
				Project:   "engram",
				Principal: "agent:developer",
				SessionID: "session-1",
				Scopes:    []cognitive.StateScopeKind{cognitive.StateScopeSession},
			}
			service := NewService(&fakeNativePlane{packet: packet}, nil)
			if tt.fallback {
				request.AllowFilesystemFallback = true
				service = NewService(
					&fakeNativePlane{err: errors.New("native missing")},
					&countingFallbackReader{packet: packet},
				)
			}

			_, err := service.ReadResumePacket(context.Background(), request)

			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestServiceReadResumePacketRejectsNativePacketWithFallbackBoundaryMarkers(t *testing.T) {
	native := nativePacket("native next", "go test ./internal/stateplane")
	native.FallbackUsed = true
	native.FallbackPath = "fallback.json"
	native.EvidenceRefs = append(native.EvidenceRefs, "filesystem_fallback:fallback.json")
	fallback := &countingFallbackReader{packet: fallbackPacket("fallback next", "fallback.json")}
	service := NewService(&fakeNativePlane{packet: native}, fallback)

	_, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		Scopes:                  []cognitive.StateScopeKind{cognitive.StateScopeSession},
		AllowFilesystemFallback: true,
	})

	require.ErrorContains(t, err, "fallback_used")
	require.Equal(t, 0, fallback.reads)
}

func TestServiceReadResumePacketExplicitFallbackMarkerOnNativeMiss(t *testing.T) {
	fallbackPath := writeFallbackPacket(t, fallbackPacket("fallback next", ""))
	native := &fakeNativePlane{err: errors.New("native missing")}
	service := NewService(native, JSONFileFallbackReader{Path: fallbackPath})

	packet, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		Scopes:                  []cognitive.StateScopeKind{cognitive.StateScopeSession},
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
	require.Contains(t, packet.EvidenceRefs, "filesystem_fallback:"+fallbackPath)
	require.Len(t, native.audits, 1)
	require.Equal(t, "read_resume_fallback", native.audits[0].action)
	require.Equal(t, "fallback_after_native_error", native.audits[0].result)
	require.Equal(t, packet.PacketID, native.audits[0].packet.PacketID)
	require.True(t, native.audits[0].request.AllowFilesystemFallback)
	require.Contains(t, native.audits[0].reason, "fallback")
}

func TestJSONFileFallbackReaderMarksExportAsFallback(t *testing.T) {
	stored := fallbackPacket("fallback next", "claimed-primary.json")
	stored.Source = cognitive.StatePacketSourceNative
	stored.FallbackUsed = false
	stored.EvidenceRefs = []string{"agent_session_state:session-1@2026-06-28T12:00:00Z"}
	fallbackPath := writeFallbackPacketJSON(t, stored)

	packet, err := JSONFileFallbackReader{Path: fallbackPath}.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:   "engram",
		Principal: "agent:developer",
		SessionID: "session-1",
		Scopes:    []cognitive.StateScopeKind{cognitive.StateScopeSession},
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.StatePacketSourceFilesystemFallback, packet.Source)
	require.Equal(t, cognitive.StateFreshnessUnknown, packet.Freshness)
	require.Equal(t, cognitive.StateDriftUnknown, packet.Drift.Kind)
	require.Equal(t, "agent:developer", packet.Principal)
	require.True(t, packet.FallbackUsed)
	require.Equal(t, fallbackPath, packet.FallbackPath)
	require.Contains(t, packet.EvidenceRefs, "filesystem_fallback:"+fallbackPath)
}

func TestServiceReadResumePacketSynthesizesStableFallbackIdentityFromGeneratedAt(t *testing.T) {
	fallbackPath := writeFallbackPacket(t, fallbackPacket("fallback next", ""))
	service := NewService(
		&fakeNativePlane{err: errors.New("native missing")},
		JSONFileFallbackReader{Path: fallbackPath},
	)
	request := cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		GoalID:                  "goal-1",
		TaskID:                  "task-1",
		Scopes:                  []cognitive.StateScopeKind{cognitive.StateScopeSession, cognitive.StateScopeGoal, cognitive.StateScopeTask},
		AllowFilesystemFallback: true,
	}

	service.now = func() time.Time { return time.Date(2026, 6, 28, 13, 0, 0, 0, time.UTC) }
	packet, err := service.ReadResumePacket(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, "2026-06-28T12:00:00Z", packet.StateVersion)
	require.NotEmpty(t, packet.PacketID)
	require.Len(t, packet.PacketID, len("resume:")+64)
	require.Equal(t, "goal-1", packet.GoalID)
	require.Equal(t, "task-1", packet.TaskID)

	service.now = func() time.Time { return time.Date(2026, 6, 28, 14, 0, 0, 0, time.UTC) }
	again, err := service.ReadResumePacket(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, packet.StateVersion, again.StateVersion)
	require.Equal(t, packet.PacketID, again.PacketID)
}

func TestServiceReadResumePacketConflictWhenFallbackDisagrees(t *testing.T) {
	fallbackPath := writeFallbackPacket(t, fallbackPacket("fallback next", ""))
	native := nativePacket("native next", "go test ./internal/stateplane")
	native.PacketID = "resume:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	nativePlane := &fakeNativePlane{packet: native}
	service := NewService(
		nativePlane,
		JSONFileFallbackReader{Path: fallbackPath},
	)

	packet, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		Scopes:                  []cognitive.StateScopeKind{cognitive.StateScopeSession},
		AllowFilesystemFallback: true,
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.StatePacketSourceMixed, packet.Source)
	require.NotEmpty(t, packet.PacketID)
	require.Len(t, packet.PacketID, len("resume:")+64)
	require.NotEqual(t, native.PacketID, packet.PacketID)
	require.Equal(t, cognitive.StateDriftConflict, packet.Drift.Kind)
	require.Equal(t, fallbackPath, packet.FallbackPath)
	require.NotEmpty(t, packet.Drift.Conflicts)
	require.Equal(t, "agent:developer", packet.Principal)
	require.True(t, packet.FallbackUsed)
	require.Contains(t, packet.EvidenceRefs, "filesystem_fallback:"+fallbackPath)
	require.Equal(t, "next_action", packet.Drift.Conflicts[0].Field)
	require.Equal(t, "native_retained_until_resolved", packet.Drift.Conflicts[0].Resolution)
	require.Len(t, nativePlane.audits, 1)
	require.Equal(t, "read_resume_conflict", nativePlane.audits[0].action)
	require.Equal(t, "mixed_conflict", nativePlane.audits[0].result)
	require.Equal(t, packet.PacketID, nativePlane.audits[0].packet.PacketID)
	require.Contains(t, nativePlane.audits[0].reason, "disagreed")
}

func TestServiceReadResumePacketReportsNewerFallbackDriftWhenActionsMatch(t *testing.T) {
	native := nativePacket("native next", "go test ./internal/stateplane")
	native.PacketID = "resume:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	fallback := fallbackPacket("native next", "fallback.json")
	fallback.NextVerification = native.NextVerification
	fallback.GeneratedAt = native.GeneratedAt.Add(time.Hour)
	nativePlane := &fakeNativePlane{packet: native}
	service := NewService(
		nativePlane,
		&countingFallbackReader{packet: fallback},
	)
	synthesizedAt := native.GeneratedAt.Add(2 * time.Hour)
	service.now = func() time.Time { return synthesizedAt }

	packet, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		Scopes:                  []cognitive.StateScopeKind{cognitive.StateScopeSession},
		AllowFilesystemFallback: true,
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.StatePacketSourceMixed, packet.Source)
	require.NotEmpty(t, packet.PacketID)
	require.Len(t, packet.PacketID, len("resume:")+64)
	require.NotEqual(t, native.PacketID, packet.PacketID)
	require.Equal(t, cognitive.StateFreshnessStale, packet.Freshness)
	require.Equal(t, cognitive.StateDriftFallbackNewer, packet.Drift.Kind)
	require.Equal(t, "fallback.json", packet.FallbackPath)
	require.Equal(t, "agent:developer", packet.Principal)
	require.True(t, packet.FallbackUsed)
	require.Contains(t, packet.EvidenceRefs, "filesystem_fallback:fallback.json")
	require.Equal(t, synthesizedAt, packet.GeneratedAt)
	require.Equal(t, "native next", packet.NextAction.Description)
	require.Len(t, nativePlane.audits, 1)
	require.Equal(t, "read_resume_fallback_newer", nativePlane.audits[0].action)
	require.Equal(t, "mixed_fallback_newer", nativePlane.audits[0].result)
	require.Equal(t, packet.PacketID, nativePlane.audits[0].packet.PacketID)
	require.Contains(t, nativePlane.audits[0].reason, "newer")
}

func TestServiceReadResumePacketAuditsFallbackReadErrorWhenNativeRetained(t *testing.T) {
	native := nativePacket("native next", "go test ./internal/stateplane")
	nativePlane := &fakeNativePlane{packet: native}
	service := NewService(
		nativePlane,
		&countingFallbackReader{err: errors.New("fallback export missing")},
	)

	packet, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		Scopes:                  []cognitive.StateScopeKind{cognitive.StateScopeSession},
		AllowFilesystemFallback: true,
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.StatePacketSourceNative, packet.Source)
	require.False(t, packet.FallbackUsed)
	require.Len(t, nativePlane.audits, 1)
	require.Equal(t, "read_resume_fallback_error", nativePlane.audits[0].action)
	require.Equal(t, "native_after_fallback_error", nativePlane.audits[0].result)
	require.Equal(t, packet.PacketID, nativePlane.audits[0].packet.PacketID)
	require.Contains(t, nativePlane.audits[0].reason, "fallback export missing")
}

func TestServiceReadResumePacketComparesFallbackAgainstNativeStateVersion(t *testing.T) {
	native := nativePacket("native next", "go test ./internal/stateplane")
	native.PacketID = "resume:1111111111111111111111111111111111111111111111111111111111111111"
	native.StateVersion = native.GeneratedAt.Format(time.RFC3339Nano)
	fallback := fallbackPacket("native next", "fallback.json")
	fallback.PacketID = "resume:2222222222222222222222222222222222222222222222222222222222222222"
	fallback.NextVerification = native.NextVerification
	fallback.GeneratedAt = native.GeneratedAt.Add(time.Hour)
	nativeReadAt := fallback.GeneratedAt.Add(time.Hour)

	packet := readMixedResumePacketWithNativeGeneratedAt(t, native, fallback, nativeReadAt, nativeReadAt.Add(time.Hour))

	require.Equal(t, cognitive.StatePacketSourceMixed, packet.Source)
	require.Equal(t, cognitive.StateDriftFallbackNewer, packet.Drift.Kind)
	require.Equal(t, cognitive.StateFreshnessStale, packet.Freshness)
	require.Equal(t, "fallback.json", packet.FallbackPath)
}

func TestServiceReadResumePacketConflictPacketIDIgnoresNativeGeneratedAt(t *testing.T) {
	native := nativePacket("native next", "go test ./internal/stateplane")
	native.PacketID = "resume:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	native.StateVersion = "native-stable-version"
	fallback := fallbackPacket("fallback next", "fallback.json")
	fallback.PacketID = "resume:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	fallback.StateVersion = "fallback-stable-version"
	fallback.GeneratedAt = native.GeneratedAt.Add(time.Hour)

	first := readMixedResumePacketWithNativeGeneratedAt(t, native, fallback, native.GeneratedAt, native.GeneratedAt.Add(2*time.Hour))
	second := readMixedResumePacketWithNativeGeneratedAt(t, native, fallback, native.GeneratedAt.Add(30*time.Minute), native.GeneratedAt.Add(3*time.Hour))

	require.Equal(t, cognitive.StateDriftConflict, first.Drift.Kind)
	require.Equal(t, cognitive.StateDriftConflict, second.Drift.Kind)
	require.NotEmpty(t, first.PacketID)
	require.NotEmpty(t, second.PacketID)
	require.NotEqual(t, native.PacketID, first.PacketID)
	require.NotEqual(t, native.PacketID, second.PacketID)
	require.Equal(t, first.PacketID, second.PacketID)
}

func TestServiceReadResumePacketFallbackNewerPacketIDIgnoresNativeGeneratedAt(t *testing.T) {
	native := nativePacket("native next", "go test ./internal/stateplane")
	native.PacketID = "resume:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	native.StateVersion = "native-stable-version"
	fallback := fallbackPacket("native next", "fallback.json")
	fallback.PacketID = "resume:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	fallback.StateVersion = "fallback-stable-version"
	fallback.NextVerification = native.NextVerification
	fallback.GeneratedAt = native.GeneratedAt.Add(2 * time.Hour)

	first := readMixedResumePacketWithNativeGeneratedAt(t, native, fallback, native.GeneratedAt, native.GeneratedAt.Add(3*time.Hour))
	second := readMixedResumePacketWithNativeGeneratedAt(t, native, fallback, native.GeneratedAt.Add(30*time.Minute), native.GeneratedAt.Add(4*time.Hour))

	require.Equal(t, cognitive.StateDriftFallbackNewer, first.Drift.Kind)
	require.Equal(t, cognitive.StateDriftFallbackNewer, second.Drift.Kind)
	require.NotEmpty(t, first.PacketID)
	require.NotEmpty(t, second.PacketID)
	require.NotEqual(t, native.PacketID, first.PacketID)
	require.NotEqual(t, native.PacketID, second.PacketID)
	require.Equal(t, first.PacketID, second.PacketID)
}

func readMixedResumePacketWithNativeGeneratedAt(t *testing.T, native, fallback cognitive.ResumePacket, nativeGeneratedAt, readAt time.Time) cognitive.ResumePacket {
	t.Helper()
	native.GeneratedAt = nativeGeneratedAt
	service := NewService(
		&fakeNativePlane{packet: native},
		&countingFallbackReader{packet: fallback},
	)
	service.now = func() time.Time { return readAt }

	packet, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		Scopes:                  []cognitive.StateScopeKind{cognitive.StateScopeSession},
		AllowFilesystemFallback: true,
	})
	require.NoError(t, err)
	return packet
}

func TestServiceReadResumePacketNativeErrorWithoutFallbackAllowed(t *testing.T) {
	nativeErr := errors.New("native missing")
	service := NewService(&fakeNativePlane{err: nativeErr}, JSONFileFallbackReader{Path: "fallback.json"})

	_, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:   "engram",
		Principal: "agent:developer",
		SessionID: "session-1",
		Scopes:    []cognitive.StateScopeKind{cognitive.StateScopeSession},
	})

	require.ErrorIs(t, err, nativeErr)
}

func TestServiceReadResumePacketRejectsFallbackWithoutPathOnNativeMiss(t *testing.T) {
	fallback := fallbackPacket("fallback next", "")
	fallback.EvidenceRefs = []string{"legacy-continuity-export"}
	service := NewService(
		&fakeNativePlane{err: errors.New("native missing")},
		&countingFallbackReader{packet: fallback},
	)

	_, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		Scopes:                  []cognitive.StateScopeKind{cognitive.StateScopeSession},
		AllowFilesystemFallback: true,
	})

	require.ErrorContains(t, err, "fallback_path is required")
}

func TestServiceReadResumePacketRejectsFallbackWithoutPathBeforeMixedPacket(t *testing.T) {
	fallback := fallbackPacket("fallback next", "")
	fallback.EvidenceRefs = []string{"legacy-continuity-export"}
	service := NewService(
		&fakeNativePlane{packet: nativePacket("native next", "go test ./internal/stateplane")},
		&countingFallbackReader{packet: fallback},
	)

	_, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		Scopes:                  []cognitive.StateScopeKind{cognitive.StateScopeSession},
		AllowFilesystemFallback: true,
	})

	require.ErrorContains(t, err, "fallback_path is required")
}

func TestServiceReadResumePacketRejectsFallbackMissingStableIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fallback-resume.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"source":"filesystem_fallback",
		"freshness":"unknown",
		"drift":{"kind":"unknown"},
		"next_action":{"kind":"command","description":"fallback next","command":"go test ./internal/stateplane"},
		"next_verification":{"kind":"command","description":"focused stateplane tests","command":"go test ./..."},
		"project":"engram",
		"session_id":"session-1",
		"scopes":["session"]
	}`), 0o600))
	service := NewService(
		&fakeNativePlane{err: errors.New("native missing")},
		JSONFileFallbackReader{Path: path},
	)
	service.now = func() time.Time { return time.Date(2026, 6, 28, 13, 0, 0, 0, time.UTC) }

	_, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		Principal:               "agent:developer",
		SessionID:               "session-1",
		Scopes:                  []cognitive.StateScopeKind{cognitive.StateScopeSession},
		AllowFilesystemFallback: true,
	})

	require.ErrorContains(t, err, "generated_at or state_version is required")
}

func TestServiceReadResumePacketRejectsMissingPrincipalBeforeFallback(t *testing.T) {
	service := NewService(
		&fakeNativePlane{err: errors.New("native missing")},
		&countingFallbackReader{packet: fallbackPacket("fallback next", "fallback.json")},
	)

	_, err := service.ReadResumePacket(context.Background(), cognitive.ResumePacketRequest{
		Project:                 "engram",
		SessionID:               "session-1",
		Scopes:                  []cognitive.StateScopeKind{cognitive.StateScopeSession},
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
		Scopes:                  []cognitive.StateScopeKind{cognitive.StateScopeSession},
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
		Scopes:                  []cognitive.StateScopeKind{cognitive.StateScopeSession},
		AllowFilesystemFallback: true,
	})

	require.ErrorContains(t, err, "session_id")
	require.ErrorContains(t, err, "resume request")
}

func nativePacket(actionDescription, verificationCommand string) cognitive.ResumePacket {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	return cognitive.ResumePacket{
		PacketID:         "resume:0000000000000000000000000000000000000000000000000000000000000000",
		StateVersion:     now.Format(time.RFC3339Nano),
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
	packet.EvidenceRefs = nil
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
