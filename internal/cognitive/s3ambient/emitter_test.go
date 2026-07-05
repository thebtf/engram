package s3ambient

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/cognitive"
)

func TestS3AmbientEmitter_UserPromptSubmitRendersIgnorableBoundedContext(t *testing.T) {
	longTitle := strings.Repeat("T", 100)
	longReason := strings.Repeat("R", 150)
	hints := []cognitive.HintProposal{
		{ID: "1", Title: "Release handoff checklist", Reason: "tag:handoff", Score: 0.92, Source: "s2.meta_index", CreatedAt: time.Unix(1700030000, 0).UTC()},
		{ID: "2", Title: "Retry failed command", Reason: "outcome:recover", Score: 0.87, Source: "s6.outcome_policy", CreatedAt: time.Unix(1700030001, 0).UTC()},
		{ID: "3", Title: longTitle, Reason: longReason, Score: 0.81, Source: "s2.meta_index", CreatedAt: time.Unix(1700030002, 0).UTC()},
		{ID: "4", Title: "Should not appear", Reason: "overflow", Score: 0.10, Source: "s2.meta_index", CreatedAt: time.Unix(1700030003, 0).UTC()},
	}

	emitter := NewEmitter(true)
	delivery, err := emitter.Render(context.Background(), cognitive.HintSurfaceUserPromptSubmit, "session-s3-hook", hints)

	require.NoError(t, err)
	require.Equal(t, cognitive.HintSurfaceUserPromptSubmit, delivery.Surface)
	require.Empty(t, delivery.Hints, "user-prompt delivery must render plain additionalContext, not a structured hint array")
	require.Contains(t, delivery.AdditionalContext, "Memory suggests (you may ignore)", "same-turn hints must stay explicitly low-priority and ignorable")
	require.Contains(t, delivery.AdditionalContext, "Release handoff checklist")
	require.Contains(t, delivery.AdditionalContext, "Retry failed command")
	require.Contains(t, delivery.AdditionalContext, strings.Repeat("T", 60), "bounded titles should still preserve a recognizable prefix")
	require.NotContains(t, delivery.AdditionalContext, longTitle, "titles must be bounded to 80 characters before rendering into the next prompt turn")
	require.NotContains(t, delivery.AdditionalContext, longReason, "reasons must be bounded to 120 characters before rendering into the next prompt turn")
	require.NotContains(t, delivery.AdditionalContext, "Should not appear", "same-turn ambient rendering must keep only the top three hints")
	require.NotContains(t, delivery.AdditionalContext, "\"title\"", "user-prompt delivery must render curated text, not JSON-encoded proposal payloads")
	require.NotContains(t, delivery.AdditionalContext, "\"reason\"", "user-prompt delivery must render curated text, not JSON-encoded proposal payloads")
}

func TestS3AmbientEmitter_MCPPollReturnsStructuredTopThreeHints(t *testing.T) {
	longTitle := strings.Repeat("M", 96)
	longReason := strings.Repeat("N", 144)
	hints := []cognitive.HintProposal{
		{ID: "11", Title: "Release checklist", Reason: "tag:release", Score: 0.91, Source: "s2.meta_index", CreatedAt: time.Unix(1700030100, 0).UTC()},
		{ID: "12", Title: "Retry workflow", Reason: "outcome:repair", Score: 0.83, Source: "s6.outcome_policy", CreatedAt: time.Unix(1700030101, 0).UTC()},
		{ID: "13", Title: longTitle, Reason: longReason, Score: 0.77, Source: "s2.meta_index", CreatedAt: time.Unix(1700030102, 0).UTC()},
		{ID: "14", Title: "Should not survive top-3", Reason: "overflow", Score: 0.01, Source: "s2.meta_index", CreatedAt: time.Unix(1700030103, 0).UTC()},
	}

	emitter := NewEmitter(true)
	delivery, err := emitter.Render(context.Background(), cognitive.HintSurfaceMCPPoll, "session-s3-mcp", hints)

	require.NoError(t, err)
	require.Equal(t, cognitive.HintSurfaceMCPPoll, delivery.Surface)
	require.Empty(t, delivery.AdditionalContext, "MCP fallback should return the structured hint list, not a prompt-oriented text block")
	require.Len(t, delivery.Hints, 3, "MCP fallback must keep the same top-three safety bound as same-turn prompt delivery")
	require.Equal(t, []string{"11", "12", "13"}, proposalIDs(delivery.Hints))
	require.LessOrEqual(t, len(delivery.Hints[2].Title), 80, "structured MCP hints must bound title length just like user-prompt delivery")
	require.LessOrEqual(t, len(delivery.Hints[2].Reason), 120, "structured MCP hints must bound reason length just like user-prompt delivery")
	require.NotContains(t, delivery.Hints[2].Title, longTitle)
	require.NotContains(t, delivery.Hints[2].Reason, longReason)
}

func TestS3AmbientEmitter_DisabledOrEmptyIsNoOp(t *testing.T) {
	t.Run("disabled returns empty delivery", func(t *testing.T) {
		emitter := NewEmitter(false)
		delivery, err := emitter.Render(context.Background(), cognitive.HintSurfaceUserPromptSubmit, "session-s3-disabled", []cognitive.HintProposal{{ID: "1", Title: "ignored", Source: "s2.meta_index"}})
		require.NoError(t, err)
		require.Equal(t, cognitive.HintDelivery{}, delivery, "disabled emitter must fail open to an empty delivery")
	})

	t.Run("empty hints return zero payload for both surfaces", func(t *testing.T) {
		emitter := NewEmitter(true)
		for _, surface := range []cognitive.HintSurface{cognitive.HintSurfaceUserPromptSubmit, cognitive.HintSurfaceMCPPoll} {
			delivery, err := emitter.Render(context.Background(), surface, "session-s3-empty", nil)
			require.NoError(t, err)
			require.Equal(t, cognitive.HintDelivery{}, delivery, "empty hint renders must stay noop so callers can fail open without synthetic filler text")
		}
	})
}
