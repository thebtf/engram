package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	experiencehistory "github.com/thebtf/engram/internal/experience"
	"github.com/thebtf/engram/pkg/cognitive"
)

func TestExperienceHistoryToolsAdvertisedWhenProviderWired(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetExperienceProvider(experiencehistory.NewService(nil))

	readProps := findToolProperties(t, srv.ListTools(), "experience_history.read")
	require.Contains(t, readProps, "query_text")
	require.Contains(t, readProps, "explicit_archive_lookup")
	require.Contains(t, readProps, "archive_trigger_classes")

	detailProps := findToolProperties(t, srv.ListTools(), "experience_history.detail")
	require.Contains(t, detailProps, "experience_id")
}

func TestHandleExperienceHistoryReadReturnsBlockedApplicabilityEnvelope(t *testing.T) {
	candidate := cognitive.ExperienceResponse{
		Source:        cognitive.ExperienceSourceProjection,
		StorageOrigin: cognitive.ExperienceSourceProjection,
		Situation:     "prior shell retry investigation",
		Decision:      "use POSIX shell quoting",
		Action:        "copy retry command",
		Outcome:       "worked on Linux shell",
		Lesson:        "POSIX quoting is not portable to PowerShell retry commands.",
		AntiApplicability: []cognitive.ExperienceAntiApplicability{
			{
				Condition: "Windows PowerShell command target",
				Rationale: "PowerShell quoting differs from POSIX shell quoting; block silent reuse.",
			},
		},
		SourceAttribution: []cognitive.ExperienceSourceAttribution{
			{Kind: "session", ID: "shell-lesson", Project: "engram", SessionID: "s1", CreatedAt: time.Date(2026, 7, 1, 2, 0, 0, 0, time.UTC)},
		},
	}
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetExperienceProvider(experiencehistory.NewService([]cognitive.ExperienceResponse{candidate}))
	args, err := json.Marshal(map[string]any{
		"project":         "engram",
		"query_text":      "PowerShell retry command",
		"current_context": "Windows PowerShell command target",
		"limit":           1,
	})
	require.NoError(t, err)

	result, err := srv.handleExperienceHistoryRead(context.Background(), args)

	require.NoError(t, err)
	var response experiencehistory.HistoryReadResponse
	require.NoError(t, json.Unmarshal([]byte(result), &response))
	require.Equal(t, experiencehistory.HistoryStateBlockedApplicability, response.State)
	require.Len(t, response.Results, 1)
	require.Equal(t, "session:shell-lesson", response.Results[0].ExperienceID)
	require.Equal(t, cognitive.ExperienceApplicabilityBlocked, response.Results[0].ApplicabilityOutcome)
	require.Equal(t, "high", response.Results[0].Applicability.Confidence)
	require.Contains(t, response.Results[0].Applicability.BlockReason, "PowerShell quoting differs")
	require.Equal(t, "archive_skipped", response.ArchiveTrace.Status)
}

func TestHandleExperienceHistoryReadRejectsInvalidArchiveTrigger(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetExperienceProvider(experiencehistory.NewService(nil))
	args, err := json.Marshal(map[string]any{
		"project":                 "engram",
		"query_text":              "history",
		"archive_trigger_classes": []string{"always_on_archive"},
	})
	require.NoError(t, err)

	_, err = srv.handleExperienceHistoryRead(context.Background(), args)

	require.ErrorContains(t, err, "invalid archive trigger class")
}
