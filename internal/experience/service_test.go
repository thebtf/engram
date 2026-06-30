package experience

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/cognitive"
)

func TestServiceImplementsExperienceProvider(t *testing.T) {
	var _ cognitive.ExperienceProvider = (*Service)(nil)
}

func TestServiceQueryExperienceReturnsBoundedHistoricalPayloads(t *testing.T) {
	candidates := []cognitive.ExperienceResponse{
		fixtureExperience("oauth-retry", "Codex OAuth retry cooled down too early after transient failures; the fix was to retry once before cooldown.", "engram", "s1"),
		fixtureExperience("oauth-tests", "A prior OAuth regression was proven by a focused retry harness before broad release gates.", "engram", "s2"),
		fixtureExperience("other-project", "OAuth retry evidence from another project must not leak into this project.", "other", "s3"),
		fixtureExperience("irrelevant", "Unrelated dashboard theme decision.", "engram", "s4"),
	}
	service := NewService(candidates)

	results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Query:          "OAuth retry cooldown",
		CurrentContext: "debugging a transient OAuth retry failure",
		Limit:          2,
	})

	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, "oauth-retry", results[0].SourceAttribution[0].ID)
	require.Equal(t, "engram", results[0].SourceAttribution[0].Project)
	require.Equal(t, cognitive.ExperienceSourceProjection, results[0].Source)
	require.NotEmpty(t, results[0].Lesson)
	require.NotEmpty(t, results[0].SourceAttribution)
	require.NotEqual(t, "other-project", results[1].SourceAttribution[0].ID)
}

func TestServiceQueryExperienceCapsRequestedLimit(t *testing.T) {
	candidates := make([]cognitive.ExperienceResponse, 0, MaxQueryLimit+3)
	for i := range MaxQueryLimit + 3 {
		candidates = append(candidates, fixtureExperience(fmt.Sprintf("retry-%02d", i), "retry failure historical lesson", "engram", fmt.Sprintf("s-%02d", i)))
	}
	service := NewService(candidates)

	results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Query:          "retry failure",
		CurrentContext: "retry failure investigation",
		Limit:          MaxQueryLimit + 99,
	})

	require.NoError(t, err)
	require.Len(t, results, MaxQueryLimit)
}

func TestServiceQueryExperienceSetsApplicabilityStates(t *testing.T) {
	t.Run("applies", func(t *testing.T) {
		service := NewService([]cognitive.ExperienceResponse{
			fixtureExperience("applies", "Retry-once before cooldown applies to transient OAuth failures.", "engram", "s1"),
		})

		results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
			Project:        "engram",
			Query:          "transient OAuth retry",
			CurrentContext: "transient OAuth failure in the same retry path",
			Limit:          1,
		})

		require.NoError(t, err)
		require.Len(t, results, 1)
		require.Equal(t, cognitive.ExperienceApplicabilityApplies, results[0].Applicability.State)
		require.NotEmpty(t, results[0].Applicability.Rationale)
	})

	t.Run("uncertain", func(t *testing.T) {
		service := NewService([]cognitive.ExperienceResponse{
			fixtureExperience("uncertain", "Retry-once before cooldown may apply to OAuth failures.", "engram", "s1"),
		})

		results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
			Project: "engram",
			Query:   "OAuth retry",
			Limit:   1,
		})

		require.NoError(t, err)
		require.Len(t, results, 1)
		require.Equal(t, cognitive.ExperienceApplicabilityUncertain, results[0].Applicability.State)
		require.Contains(t, strings.ToLower(results[0].Applicability.Rationale), "current_context")
	})

	t.Run("blocked", func(t *testing.T) {
		blocked := fixtureExperience("blocked", "Use POSIX shell quoting for the retry harness command.", "engram", "s1")
		blocked.AntiApplicability = []cognitive.ExperienceAntiApplicability{
			{
				Condition: "Windows PowerShell command target",
				Rationale: "PowerShell quoting differs from POSIX shell quoting; do not auto-reuse this command shape.",
			},
		}
		service := NewService([]cognitive.ExperienceResponse{blocked})

		results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
			Project:        "engram",
			Query:          "retry harness command quoting",
			CurrentContext: "Windows PowerShell command target for retry harness",
			Limit:          1,
		})

		require.NoError(t, err)
		require.Len(t, results, 1)
		require.Equal(t, cognitive.ExperienceApplicabilityBlocked, results[0].Applicability.State)
		require.Contains(t, results[0].Applicability.Rationale, "PowerShell quoting differs")
		require.NotEmpty(t, results[0].AntiApplicability)
	})
}

func TestServiceQueryExperienceUsesExactTermsForRelevance(t *testing.T) {
	service := NewService([]cognitive.ExperienceResponse{
		fixtureExperience("candidate-1", "Cooldown-only rollback guidance belongs elsewhere.", "engram", "s1"),
	})

	results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Query:          "cool",
		CurrentContext: "cool triage request",
		Limit:          1,
	})

	require.NoError(t, err)
	require.Empty(t, results)
}

func TestServiceQueryExperienceMatchesFirstClassFacetTerms(t *testing.T) {
	tests := []struct {
		name  string
		match func(*cognitive.ExperienceResponse, *cognitive.ExperienceQueryRequest)
	}{
		{
			name: "situation",
			match: func(candidate *cognitive.ExperienceResponse, request *cognitive.ExperienceQueryRequest) {
				candidate.Situation = "situationmatch"
				request.Situation = "situationmatch"
			},
		},
		{
			name: "decision",
			match: func(candidate *cognitive.ExperienceResponse, request *cognitive.ExperienceQueryRequest) {
				candidate.Decision = "decisionmatch"
				request.Decision = "decisionmatch"
			},
		},
		{
			name: "action",
			match: func(candidate *cognitive.ExperienceResponse, request *cognitive.ExperienceQueryRequest) {
				candidate.Action = "actionmatch"
				request.Action = "actionmatch"
			},
		},
		{
			name: "outcome",
			match: func(candidate *cognitive.ExperienceResponse, request *cognitive.ExperienceQueryRequest) {
				candidate.Outcome = "outcomematch"
				request.Outcome = "outcomematch"
			},
		},
		{
			name: "revision",
			match: func(candidate *cognitive.ExperienceResponse, request *cognitive.ExperienceQueryRequest) {
				candidate.Revision = "revisionmatch"
				request.Revision = "revisionmatch"
			},
		},
		{
			name: "reversal",
			match: func(candidate *cognitive.ExperienceResponse, request *cognitive.ExperienceQueryRequest) {
				candidate.Reversal = "reversalmatch"
				request.Reversal = "reversalmatch"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := fixtureExperience("facet-"+tt.name, "historical guidance unrelated baseline", "engram", "s1")
			candidate.Situation = ""
			candidate.Decision = ""
			candidate.Action = ""
			candidate.Outcome = ""
			candidate.Revision = ""
			candidate.Reversal = ""
			request := cognitive.ExperienceQueryRequest{
				Project:        "engram",
				Query:          "nonmatching prompt",
				CurrentContext: "neutral context",
				Limit:          1,
			}
			tt.match(&candidate, &request)

			service := NewService([]cognitive.ExperienceResponse{candidate})
			results, err := service.QueryExperience(context.Background(), request)

			require.NoError(t, err)
			require.Len(t, results, 1)
			require.Equal(t, "facet-"+tt.name, results[0].SourceAttribution[0].ID)
		})
	}
}

func TestServiceQueryExperienceUsesExactTermsForAntiApplicability(t *testing.T) {
	candidate := fixtureExperience("candidate-1", "Retry harness guidance for Windows shell execution.", "engram", "s1")
	candidate.AntiApplicability = []cognitive.ExperienceAntiApplicability{
		{
			Condition: "win",
			Rationale: "A bare win token should not match Windows by substring.",
		},
	}
	service := NewService([]cognitive.ExperienceResponse{candidate})

	results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Query:          "retry harness",
		CurrentContext: "Windows shell execution path",
		Limit:          1,
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, cognitive.ExperienceApplicabilityApplies, results[0].Applicability.State)
}

func TestServiceQueryExperienceRequiresExplicitProjectAttributionForProjectScopedQueries(t *testing.T) {
	service := NewService([]cognitive.ExperienceResponse{
		fixtureExperience("engram-scoped", "OAuth retry guidance for engram only.", "engram", "s1"),
		{
			Source: cognitive.ExperienceSourceProjection,
			Lesson: "Unscoped lesson must not leak into project-scoped queries.",
			SourceAttribution: []cognitive.ExperienceSourceAttribution{
				{
					Kind:      "session",
					ID:        "unscoped",
					Project:   "",
					SessionID: "s2",
					CreatedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
				},
			},
		},
	})

	results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Query:          "OAuth retry guidance",
		CurrentContext: "investigating an engram retry failure",
		Limit:          5,
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "engram-scoped", results[0].SourceAttribution[0].ID)
}

func TestServiceQueryExperienceUsesProvenanceWhenSourceAttributionIsEmpty(t *testing.T) {
	candidate := fixtureExperience("provenance-only", "Provenance-only OAuth retry guidance for engram.", "engram", "s1")
	candidate.SourceAttribution = nil
	candidate.Provenance = []cognitive.ExperienceSourceAttribution{
		{
			Kind:      "session",
			ID:        "provenance-only",
			Project:   "engram",
			SessionID: "s1",
			CreatedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		},
	}
	service := NewService([]cognitive.ExperienceResponse{candidate})

	results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Query:          "OAuth retry guidance",
		CurrentContext: "investigating an engram retry failure",
		Limit:          1,
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "provenance-only", results[0].SourceAttribution[0].ID)
	require.Equal(t, results[0].Provenance, results[0].SourceAttribution)
}

func fixtureExperience(id, lesson, project, sessionID string) cognitive.ExperienceResponse {
	return cognitive.ExperienceResponse{
		Source:        cognitive.ExperienceSourceProjection,
		StorageOrigin: cognitive.ExperienceSourceProjection,
		Situation:     "prior retry investigation",
		Decision:      "retry once before cooldown",
		Action:        "add focused retry proof",
		Outcome:       "regression stayed bounded",
		Lesson:        lesson,
		SourceAttribution: []cognitive.ExperienceSourceAttribution{
			{
				Kind:      "session",
				ID:        id,
				Project:   project,
				SessionID: sessionID,
				CreatedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
			},
		},
		ArchiveTriggerClasses: []cognitive.ExperienceArchiveTriggerClass{
			cognitive.ExperienceArchiveTriggerSimilarPriorFailure,
		},
	}
}
