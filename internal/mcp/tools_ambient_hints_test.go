package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
		{name: "master+s3+queue advertises tool", masterEnabled: true, s3Enabled: true, withQueue: true, wantTool: true},
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

func TestGetAmbientHintsDrainsBoundedSafeHints(t *testing.T) {
	setS3AmbientFlags(t, true, true)
	queue := cognitivecore.NewHintQueue()
	seedAmbientHintQueue(t, queue, "session-s3-poll",
		cognitivecore.HintProposalPayload{ID: "1", Title: "Release handoff checklist", Reason: "tag:handoff", Score: 0.92, Source: "s2.meta_index", CreatedAt: time.Now().UTC()},
		cognitivecore.HintProposalPayload{ID: "2", Title: "Retry failed command", Reason: "outcome:repair", Score: 0.87, Source: "s6.outcome_policy", CreatedAt: time.Now().UTC().Add(1 * time.Second)},
		cognitivecore.HintProposalPayload{ID: "3", Title: "Review PM oracle drift", Reason: "tag:oracle", Score: 0.83, Source: "s2.meta_index", CreatedAt: time.Now().UTC().Add(2 * time.Second)},
		cognitivecore.HintProposalPayload{ID: "4", Title: "Should be trimmed", Reason: "RAW_MEMORY_BODY_SHOULD_NOT_LEAK", Score: 0.79, Source: "s2.meta_index", CreatedAt: time.Now().UTC().Add(3 * time.Second)},
	)

	srv := NewServer(ServerOptions{Version: "s3-red-test"})
	srv.SetHintQueue(queue)

	out, err := srv.callTool(context.Background(), "get_ambient_hints", mustJSON(t, map[string]any{
		"session_id": "session-s3-poll",
		"limit":      99,
	}))
	require.NoError(t, err)
	payload := decodeAmbientHintsToolResponse(t, out)
	require.Len(t, payload.Hints, 3, "fallback polling must return at most 3 hints")
	assert.Equal(t, []string{"1", "2", "3"}, []string{payload.Hints[0].ID, payload.Hints[1].ID, payload.Hints[2].ID})
	assert.NotContains(t, out, "RAW_MEMORY_BODY_SHOULD_NOT_LEAK", "tool output must stay content-free")
	assert.NotContains(t, out, "\"content\"", "tool output must not expose raw memory content fields")

	outAgain, err := srv.callTool(context.Background(), "get_ambient_hints", mustJSON(t, map[string]any{
		"session_id": "session-s3-poll",
		"limit":      3,
	}))
	require.NoError(t, err)
	payloadAgain := decodeAmbientHintsToolResponse(t, outAgain)
	require.Empty(t, payloadAgain.Hints, "drain-once fallback must not replay already-delivered hints")
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
