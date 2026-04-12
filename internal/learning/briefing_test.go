package learning

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/models"
)

func TestGenerateProjectBriefing_NoChangeSentinel(t *testing.T) {
	llm := &mockLLMClient{response: ProjectBriefingNoChange}
	observations := []*models.Observation{
		{
			Type:      models.ObsTypeDecision,
			Title:     sql.NullString{String: "Adopt project briefing", Valid: true},
			Narrative: sql.NullString{String: "Maintenance should synthesize a compact project digest.", Valid: true},
		},
	}

	result, err := GenerateProjectBriefing(context.Background(), llm, "engram", "Current briefing", observations)
	require.NoError(t, err)
	assert.Equal(t, ProjectBriefingNoChange, result)
}

func TestGenerateProjectBriefing_ReturnsUpdatedBriefing(t *testing.T) {
	llm := &mockLLMClient{response: "Active Work\n- Build project briefing\n\nRecent Decisions\n- Enabled project briefings"}
	observations := []*models.Observation{
		{
			Type:      models.ObsTypeFeature,
			Title:     sql.NullString{String: "Project briefing generation", Valid: true},
			Narrative: sql.NullString{String: "Generate a compact per-project digest for session start.", Valid: true},
		},
	}

	result, err := GenerateProjectBriefing(context.Background(), llm, "engram", "", observations)
	require.NoError(t, err)
	assert.Contains(t, result, "Active Work")
	assert.Contains(t, result, "Recent Decisions")
}
