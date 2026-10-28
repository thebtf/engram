package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/cognitive/s4directives"
)

type deferredDirectiveCaptureService struct{ calls int }

func (service *deferredDirectiveCaptureService) RememberDirective(context.Context, string, string, s4directives.RememberDirectiveRequest) (*s4directives.StoredAttentionEvent, error) {
	service.calls++
	return nil, nil
}

func TestRememberDirective_RetiredFlagsCannotActivateCapture(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S4A_DIRECTIVES_CAPTURE", "true")
	service := &deferredDirectiveCaptureService{}
	server := NewServer(ServerOptions{Version: "test"})
	server.SetDirectiveCaptureService(service)
	for _, tool := range server.ListTools() {
		require.NotEqual(t, "remember_directive", tool.Name)
	}
	_, err := server.callTool(context.Background(), "remember_directive", json.RawMessage(`{"text":"retained"}`))
	require.Error(t, err)
	require.Zero(t, service.calls)
}
