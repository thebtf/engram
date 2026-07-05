package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMemorySignificanceUpdater struct {
	calls []memorySignificanceUpdateCall
	err   error
}

type memorySignificanceUpdateCall struct {
	id     int64
	rating string
}

func (f *fakeMemorySignificanceUpdater) RateMemorySignificance(_ context.Context, id int64, rating string) error {
	f.calls = append(f.calls, memorySignificanceUpdateCall{id: id, rating: rating})
	return f.err
}

func TestRateMemorySignificanceToolAdvertisedWithDedicatedSchema(t *testing.T) {
	t.Parallel()

	srv := NewServer(ServerOptions{Version: "s6-red-test"})
	srv.setTestMemorySignificanceUpdater(&fakeMemorySignificanceUpdater{})

	tools := srv.ListTools()
	var tool *Tool
	for i := range tools {
		if tools[i].Name == "rate_memory_significance" {
			tool = &tools[i]
			break
		}
	}
	require.NotNil(t, tool, "S6 self-rating must expose the dedicated rate_memory_significance MCP tool")

	required, ok := tool.InputSchema["required"].([]string)
	require.True(t, ok, "rate_memory_significance schema must declare required fields")
	assert.ElementsMatch(t, []string{"id", "rating"}, required)

	props, ok := tool.InputSchema["properties"].(map[string]any)
	require.True(t, ok, "rate_memory_significance schema must declare properties")
	assert.Contains(t, props, "id")

	ratingProp, ok := props["rating"].(map[string]any)
	require.True(t, ok, "rating property must be an object schema")
	ratingEnum, ok := ratingProp["enum"].([]string)
	require.True(t, ok, "rating schema must enumerate accepted values")
	assert.ElementsMatch(t, []string{"useful", "not_useful"}, ratingEnum)
}

func TestRateMemorySignificanceToolCallUpdatesLearningForUsefulAndNotUseful(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		rating string
	}{
		{name: "useful", rating: "useful"},
		{name: "not useful", rating: "not_useful"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			updater := &fakeMemorySignificanceUpdater{}
			srv := NewServer(ServerOptions{Version: "s6-red-test"})
			srv.setTestMemorySignificanceUpdater(updater)

			out, err := srv.callTool(context.Background(), "rate_memory_significance", mustJSON(t, map[string]any{
				"id":     42,
				"rating": tc.rating,
			}))

			require.NoError(t, err)
			require.Equal(t, []memorySignificanceUpdateCall{{id: 42, rating: tc.rating}}, updater.calls,
				"dedicated self-rating must update the learning seam exactly once with the requested memory and rating")

			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(out), &body), "successful self-rating response must be machine-readable JSON")
			assert.Equal(t, "rated", body["status"])
			assert.Equal(t, float64(42), body["id"])
			assert.Equal(t, tc.rating, body["rating"])
		})
	}
}

func TestRateMemorySignificanceRejectsInvalidIDWithoutWrite(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "missing id", args: map[string]any{"rating": "useful"}},
		{name: "zero id", args: map[string]any{"id": 0, "rating": "useful"}},
		{name: "negative id", args: map[string]any{"id": -7, "rating": "useful"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			updater := &fakeMemorySignificanceUpdater{}
			srv := NewServer(ServerOptions{Version: "s6-red-test"})
			srv.setTestMemorySignificanceUpdater(updater)

			_, err := srv.callTool(context.Background(), "rate_memory_significance", mustJSON(t, tc.args))

			require.Error(t, err)
			assert.Contains(t, err.Error(), "id")
			assert.Empty(t, updater.calls, "invalid memory ids must fail before any learning update")
		})
	}
}

func TestRateMemorySignificanceRejectsInvalidRatingWithoutWrite(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "missing rating", args: map[string]any{"id": 42}},
		{name: "unknown rating", args: map[string]any{"id": 42, "rating": "helpful"}},
		{name: "empty rating", args: map[string]any{"id": 42, "rating": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			updater := &fakeMemorySignificanceUpdater{}
			srv := NewServer(ServerOptions{Version: "s6-red-test"})
			srv.setTestMemorySignificanceUpdater(updater)

			_, err := srv.callTool(context.Background(), "rate_memory_significance", mustJSON(t, tc.args))

			require.Error(t, err)
			assert.Contains(t, err.Error(), "rating")
			assert.Empty(t, updater.calls, "invalid ratings must fail before any learning update")
		})
	}
}

func TestRateMemorySignificanceMissingUpdaterFailsExplicitly(t *testing.T) {
	t.Parallel()

	srv := NewServer(ServerOptions{Version: "s6-red-test"})

	_, err := srv.callTool(context.Background(), "rate_memory_significance", mustJSON(t, map[string]any{
		"id":     42,
		"rating": "useful",
	}))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "memory significance updater not available")
}

func TestRateMemorySignificanceLegacyRatePathsRemainUnsupported(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		tool string
		args map[string]any
	}{
		{
			name: "legacy rate_memory tool",
			tool: "rate_memory",
			args: map[string]any{"id": 42, "rating": "useful"},
		},
		{
			name: "consolidated feedback rate action",
			tool: "feedback",
			args: map[string]any{"action": "rate", "id": 42, "rating": "not_useful"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			updater := &fakeMemorySignificanceUpdater{}
			srv := NewServer(ServerOptions{Version: "s6-red-test"})
			srv.setTestMemorySignificanceUpdater(updater)

			_, err := srv.callTool(context.Background(), tc.tool, mustJSON(t, tc.args))

			require.Error(t, err)
			assert.Contains(t, err.Error(), "rate_memory removed in v5 (US3): memories table has no rating field yet")
			assert.Empty(t, updater.calls, "legacy rate compatibility must remain explicitly unsupported, not silently routed to S6")
		})
	}
}
