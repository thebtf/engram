package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
)

type ambientHintResult struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Tags   []string `json:"tags,omitempty"`
	Score  float64  `json:"score,omitempty"`
	Source string   `json:"source,omitempty"`
	Reason string   `json:"reason,omitempty"`
}

type ambientHintsToolResponse struct {
	Hints []ambientHintResult `json:"hints,omitempty"`
}

func setS3AmbientFlags(t *testing.T, masterEnabled, s3Enabled bool) {
	t.Helper()
	if masterEnabled {
		t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	} else {
		t.Setenv("ENGRAM_V7_PLUG_ENABLED", "false")
	}
	if s3Enabled {
		t.Setenv("ENGRAM_V7_S3_AMBIENT", "true")
	} else {
		t.Setenv("ENGRAM_V7_S3_AMBIENT", "false")
	}
}

func seedAmbientHintQueue(t *testing.T, queue cognitivecore.HintQueue, sessionID string, hints ...cognitivecore.HintProposalPayload) {
	t.Helper()
	for _, hint := range hints {
		require.NoError(t, queue.Enqueue(context.Background(), sessionID, hint))
	}
}

func decodeAmbientHintsToolResponse(t *testing.T, raw string) ambientHintsToolResponse {
	t.Helper()
	var payload ambientHintsToolResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &payload), raw)
	return payload
}

func TestGetAmbientHintsToolAdvertisedOnlyWhenS3FlagAndQueuePresent(t *testing.T) {
	for _, tc := range []struct {
		name          string
		masterEnabled bool
		s3Enabled     bool
		withQueue     bool
		wantTool      bool
	}{
		{name: "master off hides tool", masterEnabled: false, s3Enabled: true, withQueue: true, wantTool: false},
		{name: "s3 off hides tool", masterEnabled: true, s3Enabled: false, withQueue: true, wantTool: false},
		{name: "missing queue hides tool", masterEnabled: true, s3Enabled: true, withQueue: false, wantTool: false},
		{name: "retired flags cannot activate tool", masterEnabled: true, s3Enabled: true, withQueue: true, wantTool: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setS3AmbientFlags(t, tc.masterEnabled, tc.s3Enabled)
			srv := NewServer(ServerOptions{Version: "s3-red-test"})
			if tc.withQueue {
				srv.SetHintQueue(cognitivecore.NewHintQueue())
			}

			names := listedToolNames(srv.ListTools())
			require.Equal(t, tc.wantTool, names["get_ambient_hints"])
		})
	}
}

func TestGetAmbientHintsReturnsEmptyForDisabledStaleAndEmptyQueue(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name          string
		masterEnabled bool
		s3Enabled     bool
		seed          []cognitivecore.HintProposalPayload
	}{
		{name: "disabled flag", masterEnabled: true, s3Enabled: false, seed: []cognitivecore.HintProposalPayload{{ID: "1", Title: "disabled", Source: "s2.meta_index", CreatedAt: now}}},
		{name: "empty queue", masterEnabled: true, s3Enabled: true, seed: nil},
		{name: "stale queue", masterEnabled: true, s3Enabled: true, seed: []cognitivecore.HintProposalPayload{{ID: "2", Title: "stale", Source: "s2.meta_index", CreatedAt: now.Add(-25 * time.Hour)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setS3AmbientFlags(t, tc.masterEnabled, tc.s3Enabled)
			queue := cognitivecore.NewHintQueue()
			seedAmbientHintQueue(t, queue, "session-s3-empty", tc.seed...)

			srv := NewServer(ServerOptions{Version: "s3-red-test"})
			srv.SetHintQueue(queue)

			out, err := srv.callTool(context.Background(), "get_ambient_hints", mustJSON(t, map[string]any{
				"session_id": "session-s3-empty",
				"limit":      3,
			}))
			require.NoError(t, err)
			payload := decodeAmbientHintsToolResponse(t, out)
			require.Empty(t, payload.Hints, "disabled/stale/empty fallback cases must fail open to an empty hint list")
		})
	}
}
