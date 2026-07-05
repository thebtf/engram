package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/cognitive/s4directives"
)

type fakeDirectiveCaptureService struct {
	calls     int
	project   string
	sessionID string
	request   s4directives.RememberDirectiveRequest
	response  *s4directives.StoredAttentionEvent
	err       error
}

func (f *fakeDirectiveCaptureService) RememberDirective(_ context.Context, project, sessionID string, req s4directives.RememberDirectiveRequest) (*s4directives.StoredAttentionEvent, error) {
	f.calls++
	f.project = project
	f.sessionID = sessionID
	f.request = req
	if f.err != nil {
		return nil, f.err
	}
	if f.response != nil {
		return f.response, nil
	}
	return &s4directives.StoredAttentionEvent{
		ID:             77,
		Project:        project,
		SessionID:      sessionID,
		SourceTurnHash: "sha256:testhash",
		DerivedIntent:  "keep release notes short",
		AgentConfirmed: true,
		Horizon:        "project",
		PrivacyClass:   "internal",
		CreatedAt:      time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC),
	}, nil
}

func TestRememberDirectiveToolAdvertisedOnlyWhenS4AFlagAndServiceArePresent(t *testing.T) {
	t.Run("absent when flag disabled even with service", func(t *testing.T) {
		t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
		t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "false")
		srv := NewServer(ServerOptions{Version: "test"})
		srv.SetDirectiveCaptureService(&fakeDirectiveCaptureService{})

		names := listedToolNames(srv.ListTools())
		require.False(t, names["remember_directive"])
	})

	t.Run("absent when master flag disabled even if s4a flag and service are present", func(t *testing.T) {
		t.Setenv("ENGRAM_V7_PLUG_ENABLED", "false")
		t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "true")
		srv := NewServer(ServerOptions{Version: "test"})
		srv.SetDirectiveCaptureService(&fakeDirectiveCaptureService{})

		names := listedToolNames(srv.ListTools())
		require.False(t, names["remember_directive"])
	})

	t.Run("absent when service is missing even with flag enabled", func(t *testing.T) {
		t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
		t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "true")
		srv := NewServer(ServerOptions{Version: "test"})

		names := listedToolNames(srv.ListTools())
		require.False(t, names["remember_directive"])
	})

	t.Run("advertised with bounded input schema when flag and service are present", func(t *testing.T) {
		t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
		t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "true")
		srv := NewServer(ServerOptions{Version: "test"})
		srv.SetDirectiveCaptureService(&fakeDirectiveCaptureService{})

		props := findToolProperties(t, srv.ListTools(), "remember_directive")
		require.Contains(t, props, "text")
		require.Contains(t, props, "source_turn")
		require.Contains(t, props, "horizon")
		require.Contains(t, props, "privacy_class")
	})
}

func TestRememberDirectiveDirectCallFailsClosedBeforeDelegation(t *testing.T) {
	t.Run("flag disabled", func(t *testing.T) {
		t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
		t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "false")
		service := &fakeDirectiveCaptureService{}
		srv := NewServer(ServerOptions{Version: "test"})
		srv.SetDirectiveCaptureService(service)

		_, err := srv.callTool(contextWithProject(contextWithSession(context.Background(), "session-1"), "engram"), "remember_directive", json.RawMessage(`{"text":"remember this"}`))

		require.ErrorContains(t, err, "remember_directive feature flag required")
		require.Zero(t, service.calls)
	})

	t.Run("master flag disabled even if s4a flag is enabled", func(t *testing.T) {
		t.Setenv("ENGRAM_V7_PLUG_ENABLED", "false")
		t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "true")
		service := &fakeDirectiveCaptureService{}
		srv := NewServer(ServerOptions{Version: "test"})
		srv.SetDirectiveCaptureService(service)

		_, err := srv.callTool(contextWithProject(contextWithSession(context.Background(), "session-1"), "engram"), "remember_directive", json.RawMessage(`{"text":"remember this"}`))

		require.ErrorContains(t, err, "remember_directive feature flag required")
		require.Zero(t, service.calls)
	})

	t.Run("service missing", func(t *testing.T) {
		t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
		t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "true")
		srv := NewServer(ServerOptions{Version: "test"})

		_, err := srv.callTool(contextWithProject(contextWithSession(context.Background(), "session-1"), "engram"), "remember_directive", json.RawMessage(`{"text":"remember this"}`))

		require.ErrorContains(t, err, "remember_directive service not configured")
	})

	t.Run("project context missing", func(t *testing.T) {
		t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
		t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "true")
		service := &fakeDirectiveCaptureService{}
		srv := NewServer(ServerOptions{Version: "test"})
		srv.SetDirectiveCaptureService(service)

		_, err := srv.callTool(contextWithSession(context.Background(), "session-1"), "remember_directive", json.RawMessage(`{"text":"remember this"}`))

		require.ErrorContains(t, err, "project is required")
		require.Zero(t, service.calls)
	})

	t.Run("session context missing", func(t *testing.T) {
		t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
		t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "true")
		service := &fakeDirectiveCaptureService{}
		srv := NewServer(ServerOptions{Version: "test"})
		srv.SetDirectiveCaptureService(service)

		_, err := srv.callTool(contextWithProject(context.Background(), "engram"), "remember_directive", json.RawMessage(`{"text":"remember this"}`))

		require.ErrorContains(t, err, "session_id is required")
		require.Zero(t, service.calls)
	})
}

func TestRememberDirectiveDirectCallDelegatesContextAndReturnsSanitizedRecord(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "true")
	service := &fakeDirectiveCaptureService{response: &s4directives.StoredAttentionEvent{
		ID:             91,
		Project:        "engram",
		SessionID:      "session-1",
		SourceTurnHash: "sha256:sanitized",
		DerivedIntent:  "keep release notes short",
		AgentConfirmed: true,
		Horizon:        "permanent",
		PrivacyClass:   "secret",
		CreatedAt:      time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC),
	}}
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetDirectiveCaptureService(service)
	ctx := contextWithProject(contextWithSession(context.Background(), "session-1"), "engram")

	out, err := srv.callTool(ctx, "remember_directive", json.RawMessage(`{
		"text":"RAW_DIRECTIVE_NEVER_RETURN",
		"source_turn":"RAW_SOURCE_TURN_NEVER_RETURN",
		"horizon":"permanent",
		"privacy_class":"secret"
	}`))

	require.NoError(t, err)
	require.Equal(t, 1, service.calls)
	require.Equal(t, "engram", service.project)
	require.Equal(t, "session-1", service.sessionID)
	require.Equal(t, "RAW_DIRECTIVE_NEVER_RETURN", service.request.Text)
	require.Equal(t, "RAW_SOURCE_TURN_NEVER_RETURN", service.request.SourceTurn)
	require.Equal(t, "permanent", service.request.Horizon)
	require.Equal(t, "secret", service.request.PrivacyClass)
	require.NotContains(t, out, "RAW_DIRECTIVE_NEVER_RETURN")
	require.NotContains(t, out, "RAW_SOURCE_TURN_NEVER_RETURN")

	var decoded s4directives.StoredAttentionEvent
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	require.Equal(t, int64(91), decoded.ID)
	require.Equal(t, "sha256:sanitized", decoded.SourceTurnHash)
	require.Equal(t, "keep release notes short", decoded.DerivedIntent)
	require.Equal(t, "secret", decoded.PrivacyClass)
	require.True(t, decoded.AgentConfirmed)
}
