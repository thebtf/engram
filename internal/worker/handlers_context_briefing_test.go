package worker

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/models"
)

func TestProjectBriefingNarrative_ReturnsNarrativeWhenEnabled(t *testing.T) {
	briefing := &models.Observation{
		Type:      models.ObsTypeWiki,
		Narrative: sql.NullString{String: "Active Work\n- Build briefing", Valid: true},
	}

	value := projectBriefingNarrative(true, briefing)
	require.Equal(t, "Active Work\n- Build briefing", value)
}

func TestProjectBriefingNarrative_ReturnsNilWhenDisabledOrMissing(t *testing.T) {
	briefing := &models.Observation{
		Type:      models.ObsTypeWiki,
		Narrative: sql.NullString{String: "Active Work\n- Build briefing", Valid: true},
	}

	require.Nil(t, projectBriefingNarrative(false, briefing))
	require.Nil(t, projectBriefingNarrative(true, nil))
	require.Nil(t, projectBriefingNarrative(true, &models.Observation{Narrative: sql.NullString{String: "   ", Valid: true}}))
}
