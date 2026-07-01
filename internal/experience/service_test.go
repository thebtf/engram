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

func TestServiceQueryExperienceMatchesProjectFromProvenanceWhenSourceAttributionDiffers(t *testing.T) {
	candidate := fixtureExperience("legacy-other", "Project-scoped OAuth evidence for engram.", "other", "legacy-session")
	candidate.Provenance = []cognitive.ExperienceSourceAttribution{
		{
			Kind:      "session",
			ID:        "canonical-engram",
			Project:   "engram",
			SessionID: "canonical-session",
			CreatedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		},
	}
	service := NewService([]cognitive.ExperienceResponse{candidate})

	results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Query:          "Project-scoped OAuth evidence",
		CurrentContext: "investigating engram OAuth evidence",
		Limit:          1,
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "legacy-other", results[0].SourceAttribution[0].ID)
	require.Equal(t, "other", results[0].SourceAttribution[0].Project)
	require.Equal(t, "canonical-engram", results[0].Provenance[0].ID)
	require.Equal(t, "engram", results[0].Provenance[0].Project)
}

func TestServiceQueryExperienceMatchesProvenanceTermsWhenSourceAttributionIsPresent(t *testing.T) {
	candidate := fixtureExperience("legacy-only", "historical baseline", "engram", "legacy-session")
	candidate.Situation = ""
	candidate.Decision = ""
	candidate.Action = ""
	candidate.Outcome = ""
	candidate.Revision = ""
	candidate.Reversal = ""
	candidate.ArchiveTriggerClasses = nil
	candidate.Provenance = []cognitive.ExperienceSourceAttribution{
		{
			Kind:      "session",
			ID:        "canonicalmatch",
			Project:   "canonical-project",
			SessionID: "session777",
			CreatedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		},
	}
	service := NewService([]cognitive.ExperienceResponse{candidate})

	results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Query:          "canonicalmatch session777",
		CurrentContext: "lookup tokens",
		Limit:          1,
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "legacy-only", results[0].SourceAttribution[0].ID)
	require.Equal(t, "canonicalmatch", results[0].Provenance[0].ID)
}

func TestServiceQueryExperienceFillsApplicabilityEnvelope(t *testing.T) {
	t.Run("blocked carries anti-applicability block reason", func(t *testing.T) {
		blocked := fixtureExperience("blocked-envelope", "Use POSIX shell quoting for the retry harness command.", "engram", "s1")
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
		applicability := results[0].Applicability
		require.Equal(t, cognitive.ExperienceApplicabilityBlocked, applicability.State)
		require.Equal(t, []string{"Windows PowerShell command target"}, applicability.DoesNotApplyWhen)
		require.Equal(t, "high", applicability.Confidence)
		require.Contains(t, applicability.BlockReason, "PowerShell quoting differs")
		require.NotEmpty(t, applicability.OverrideEvidence)
	})

	t.Run("uncertain names required context", func(t *testing.T) {
		service := NewService([]cognitive.ExperienceResponse{
			fixtureExperience("uncertain-envelope", "Retry-once before cooldown may apply to OAuth failures.", "engram", "s1"),
		})

		results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
			Project: "engram",
			Query:   "OAuth retry",
			Limit:   1,
		})

		require.NoError(t, err)
		require.Len(t, results, 1)
		applicability := results[0].Applicability
		require.Equal(t, cognitive.ExperienceApplicabilityUncertain, applicability.State)
		require.Equal(t, []string{"current_context"}, applicability.RequiredContext)
		require.Equal(t, "missing current_context", applicability.BlockReason)
		require.Equal(t, "low", applicability.Confidence)
	})
}

func TestReadHistoryReturnsFirstClassReadEnvelopeWithoutArchiveHotPath(t *testing.T) {
	archive := &fakeArchiveSource{
		items: []cognitive.ExperienceResponse{
			archiveExperience("archive-hotpath", "rollback archive context should not appear without a trigger"),
		},
	}
	service := NewServiceWithArchive([]cognitive.ExperienceResponse{
		fixtureExperience("hot-history", "ordinary retry experience", "engram", "s1"),
	}, archive)

	response, err := ReadHistory(context.Background(), service, cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Principal:      "agent:developer",
		Query:          "retry experience",
		CurrentContext: "ordinary hot path request",
		Limit:          5,
	}, time.Date(2026, time.July, 1, 3, 0, 0, 0, time.UTC))

	require.NoError(t, err)
	require.Equal(t, HistoryStateLive, response.State)
	require.Len(t, response.Results, 1)
	require.Equal(t, "session:hot-history", response.Results[0].ExperienceID)
	require.Equal(t, cognitive.ExperienceSourceProjection, response.Results[0].StorageOrigin)
	require.Equal(t, cognitive.ExperienceApplicabilityApplies, response.Results[0].ApplicabilityOutcome)
	require.Equal(t, "archive_skipped", response.ArchiveTrace.Status)
	require.False(t, response.ArchiveTrace.ArchiveUsed)
	require.Equal(t, 0, archive.calls)
}

func TestReadHistoryArchiveTraceUsesNamedTriggerAndReportsBlock(t *testing.T) {
	blocked := archiveExperience("archive-blocked-detail", "PowerShell rollback archive lesson")
	blocked.AntiApplicability = []cognitive.ExperienceAntiApplicability{
		{
			Condition: "Windows PowerShell command target",
			Rationale: "PowerShell command shape must not be reused silently.",
		},
	}
	archive := &fakeArchiveSource{items: []cognitive.ExperienceResponse{blocked}}
	service := NewServiceWithArchive(nil, archive)
	now := time.Date(2026, time.July, 1, 3, 15, 0, 0, time.UTC)

	response, err := ReadHistory(context.Background(), service, cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Principal:      "agent:developer",
		Query:          "rollback command quoting",
		CurrentContext: "Windows PowerShell command target",
		ArchiveTriggerClasses: []cognitive.ExperienceArchiveTriggerClass{
			cognitive.ExperienceArchiveTriggerSimilarPriorFailure,
		},
		Limit: 1,
	}, now)

	require.NoError(t, err)
	require.Equal(t, HistoryStateBlockedApplicability, response.State)
	require.Len(t, response.Results, 1)
	require.Equal(t, "archive:archive-blocked-detail", response.Results[0].ExperienceID)
	require.Equal(t, cognitive.ExperienceApplicabilityBlocked, response.Results[0].ApplicabilityOutcome)
	require.True(t, response.ArchiveTrace.ArchiveUsed)
	require.True(t, response.ArchiveTrace.BlockedByAntiApplicability)
	require.Equal(t, "archive_resurfaced", response.ArchiveTrace.Status)
	require.Equal(t, 1, response.ArchiveTrace.ResultCap)
	require.Contains(t, response.ArchiveTrace.EvidenceRefs, "archive:archive-blocked-detail")

	detail, err := ReadHistoryDetail(context.Background(), service, HistoryDetailRequest{
		Project:        "engram",
		Principal:      "agent:developer",
		ExperienceID:   "archive:archive-blocked-detail",
		CurrentContext: "Windows PowerShell command target",
		ArchiveTriggerClasses: []cognitive.ExperienceArchiveTriggerClass{
			cognitive.ExperienceArchiveTriggerSimilarPriorFailure,
		},
	}, now)

	require.NoError(t, err)
	require.Equal(t, HistoryStateBlockedApplicability, detail.State)
	require.NotNil(t, detail.ExperienceDetail)
	require.Equal(t, cognitive.ExperienceSourceProjection, detail.StorageOrigin)
	require.Equal(t, cognitive.ExperienceApplicabilityBlocked, detail.ApplicabilityEvidence.State)
	require.Contains(t, detail.ProvenanceRefs, "archive:archive-blocked-detail")
}

func TestReadHistoryUsesPerRequestArchiveTraceInsteadOfGlobalEvidence(t *testing.T) {
	provider := &perRequestTraceProvider{
		items: []cognitive.ExperienceResponse{
			archiveExperience("current-trace", "current per-request archive trace"),
		},
		perCallEvidence: []ArchiveEvidenceEntry{
			{
				TriggerClasses:           []cognitive.ExperienceArchiveTriggerClass{cognitive.ExperienceArchiveTriggerSimilarPriorFailure},
				ExperienceRetrievalRan:   true,
				RequestedLimit:           1,
				Returned:                 1,
				AntiApplicabilityBlocked: false,
				EvidenceRefs:             []string{"archive:current-trace"},
				Status:                   "archive_resurfaced",
				Reason:                   "current request evidence",
			},
		},
		globalEvidence: []ArchiveEvidenceEntry{
			{
				TriggerClasses:         []cognitive.ExperienceArchiveTriggerClass{cognitive.ExperienceArchiveTriggerHistoricalWhy},
				ExperienceRetrievalRan: true,
				RequestedLimit:         1,
				Returned:               1,
				EvidenceRefs:           []string{"archive:wrong-request"},
				Status:                 "archive_resurfaced",
				Reason:                 "wrong global evidence",
			},
		},
	}

	response, err := ReadHistory(context.Background(), provider, cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Query:          "current trace",
		CurrentContext: "similar prior failure lookup",
		ArchiveTriggerClasses: []cognitive.ExperienceArchiveTriggerClass{
			cognitive.ExperienceArchiveTriggerSimilarPriorFailure,
		},
		Limit: 1,
	}, time.Date(2026, time.July, 1, 4, 0, 0, 0, time.UTC))

	require.NoError(t, err)
	require.Equal(t, "archive_resurfaced", response.ArchiveTrace.Status)
	require.Equal(t, []string{"archive:current-trace"}, response.ArchiveTrace.EvidenceRefs)
	require.NotContains(t, response.ArchiveTrace.EvidenceRefs, "archive:wrong-request")
}

func TestReadHistoryDetailFindsExactIDBeyondRelevanceLimit(t *testing.T) {
	candidates := make([]cognitive.ExperienceResponse, 0, MaxQueryLimit+2)
	for i := 0; i < MaxQueryLimit+2; i++ {
		candidates = append(candidates, fixtureExperience(fmt.Sprintf("%02d", i), "generic session detail lesson", "engram", fmt.Sprintf("s-%02d", i)))
	}
	service := NewService(candidates)
	targetID := fmt.Sprintf("session:%02d", MaxQueryLimit+1)

	detail, err := ReadHistoryDetail(context.Background(), service, HistoryDetailRequest{
		Project:        "engram",
		ExperienceID:   targetID,
		CurrentContext: "detail lookup",
	}, time.Date(2026, time.July, 1, 4, 5, 0, 0, time.UTC))

	require.NoError(t, err)
	require.Equal(t, HistoryStateLive, detail.State)
	require.NotNil(t, detail.ExperienceDetail)
	require.Equal(t, targetID, detail.ExperienceDetail.ExperienceID)
}

func TestReadHistoryDetailCanonicalizesTopLevelExperienceID(t *testing.T) {
	service := NewService([]cognitive.ExperienceResponse{
		fixtureExperience("raw-detail", "detail canonical id lesson", "engram", "canonical-session"),
	})

	detail, err := ReadHistoryDetail(context.Background(), service, HistoryDetailRequest{
		Project:        "engram",
		ExperienceID:   "raw-detail",
		CurrentContext: "detail lookup",
	}, time.Date(2026, time.July, 1, 4, 7, 0, 0, time.UTC))

	require.NoError(t, err)
	require.Equal(t, HistoryStateLive, detail.State)
	require.NotNil(t, detail.ExperienceDetail)
	require.Equal(t, "session:raw-detail", detail.ExperienceID)
	require.Equal(t, detail.ExperienceDetail.ExperienceID, detail.ExperienceID)
}

func TestReadHistoryArchiveFilteringDoesNotMutateArchiveSource(t *testing.T) {
	otherProject := archiveExperience("archive-other", "rollback regression archive lesson")
	otherProject.SourceAttribution[0].Project = "other"
	engramItem := archiveExperience("archive-engram", "rollback regression archive lesson")
	archive := &cachingArchiveSource{items: []cognitive.ExperienceResponse{otherProject, engramItem}}
	service := NewServiceWithArchive(nil, archive)

	results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Query:          "rollback regression archive lesson",
		CurrentContext: "needs rollback regression evidence",
		ArchiveTriggerClasses: []cognitive.ExperienceArchiveTriggerClass{
			cognitive.ExperienceArchiveTriggerRegressionOrRollback,
		},
		Limit: 5,
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, 1, archive.calls)
	require.Equal(t, "other", archive.items[0].SourceAttribution[0].Project)
	require.Equal(t, "archive-other", archive.items[0].SourceAttribution[0].ID)
}

func TestReadHistoryDetailPassesExperienceIDToArchiveLookup(t *testing.T) {
	archive := &cachingArchiveSource{
		targetQuery: "archive:archive-target",
		items: []cognitive.ExperienceResponse{
			archiveExperience("archive-target", "detail archive lesson"),
		},
	}
	service := NewServiceWithArchive(nil, archive)

	detail, err := ReadHistoryDetail(context.Background(), service, HistoryDetailRequest{
		Project:      "engram",
		ExperienceID: "archive:archive-target",
		ArchiveTriggerClasses: []cognitive.ExperienceArchiveTriggerClass{
			cognitive.ExperienceArchiveTriggerExplicitLookup,
		},
	}, time.Date(2026, time.July, 1, 4, 10, 0, 0, time.UTC))

	require.NoError(t, err)
	require.Equal(t, 1, archive.calls)
	require.Equal(t, "archive:archive-target", archive.lastQuery)
	require.Equal(t, HistoryStateLive, detail.State)
	require.NotNil(t, detail.ExperienceDetail)
	require.Equal(t, "archive:archive-target", detail.ExperienceDetail.ExperienceID)
}

type cachingArchiveSource struct {
	items       []cognitive.ExperienceResponse
	targetQuery string
	lastQuery   string
	calls       int
}

func (c *cachingArchiveSource) QueryArchiveExperience(_ context.Context, request cognitive.ExperienceQueryRequest, _ []cognitive.ExperienceArchiveTriggerClass, limit int) ([]cognitive.ExperienceResponse, error) {
	c.calls++
	c.lastQuery = request.Query
	if c.targetQuery != "" && request.Query != c.targetQuery {
		return nil, nil
	}
	if len(c.items) > limit {
		return c.items[:limit], nil
	}
	return c.items, nil
}

type perRequestTraceProvider struct {
	items           []cognitive.ExperienceResponse
	perCallEvidence []ArchiveEvidenceEntry
	globalEvidence  []ArchiveEvidenceEntry
}

func (p *perRequestTraceProvider) QueryExperience(_ context.Context, _ cognitive.ExperienceQueryRequest) ([]cognitive.ExperienceResponse, error) {
	return cloneResponses(p.items), nil
}

func (p *perRequestTraceProvider) QueryExperienceWithArchiveEvidence(_ context.Context, _ cognitive.ExperienceQueryRequest) ([]cognitive.ExperienceResponse, []ArchiveEvidenceEntry, error) {
	return cloneResponses(p.items), append([]ArchiveEvidenceEntry(nil), p.perCallEvidence...), nil
}

func (p *perRequestTraceProvider) ArchiveEvidence() []ArchiveEvidenceEntry {
	return append([]ArchiveEvidenceEntry(nil), p.globalEvidence...)
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
