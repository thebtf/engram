package experience

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/cognitive"
)

func TestArchiveTriggerTaxonomyIsFiniteAndRejectsUnknown(t *testing.T) {
	require.Equal(t, []cognitive.ExperienceArchiveTriggerClass{
		cognitive.ExperienceArchiveTriggerHistoricalWhy,
		cognitive.ExperienceArchiveTriggerRegressionOrRollback,
		cognitive.ExperienceArchiveTriggerRevisitOldDecision,
		cognitive.ExperienceArchiveTriggerSimilarPriorFailure,
		cognitive.ExperienceArchiveTriggerTemporalTruthChange,
		cognitive.ExperienceArchiveTriggerExplicitLookup,
	}, AllowedArchiveTriggerClasses())

	triggers, err := NormalizeArchiveTriggerClasses([]cognitive.ExperienceArchiveTriggerClass{
		cognitive.ExperienceArchiveTriggerRegressionOrRollback,
		cognitive.ExperienceArchiveTriggerRegressionOrRollback,
		cognitive.ExperienceArchiveTriggerClass("always_on_archive"),
	})

	require.ErrorContains(t, err, "invalid archive trigger class")
	require.Empty(t, triggers)
}

func TestServiceQueryExperienceSkipsArchiveWithoutTrigger(t *testing.T) {
	archive := &fakeArchiveSource{
		items: []cognitive.ExperienceResponse{
			archiveExperience("archive-1", "rollback archive context should not appear without a trigger"),
		},
	}
	service := NewServiceWithArchive([]cognitive.ExperienceResponse{
		fixtureExperience("hot-1", "ordinary retry experience", "engram", "s1"),
	}, archive)

	results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Query:          "retry experience",
		CurrentContext: "ordinary hot path request",
		Limit:          5,
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, 0, archive.calls)
	require.Empty(t, service.ArchiveEvidence())
}

func TestServiceQueryExperienceRejectsInvalidTriggerBeforeArchiveCall(t *testing.T) {
	archive := &fakeArchiveSource{items: []cognitive.ExperienceResponse{
		archiveExperience("archive-1", "archive context"),
	}}
	service := NewServiceWithArchive(nil, archive)

	_, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
		Project:               "engram",
		Query:                 "archive context",
		CurrentContext:        "archive context",
		ArchiveTriggerClasses: []cognitive.ExperienceArchiveTriggerClass{"always_on_archive"},
		Limit:                 5,
	})

	require.ErrorContains(t, err, "invalid archive trigger class")
	require.Equal(t, 0, archive.calls)
	require.Empty(t, service.ArchiveEvidence())
}

func TestServiceQueryExperienceAddsBoundedArchiveOnAllowedTrigger(t *testing.T) {
	archiveItems := make([]cognitive.ExperienceResponse, 0, MaxArchiveResurfacingLimit+3)
	for i := 0; i < MaxArchiveResurfacingLimit+3; i++ {
		archiveItems = append(archiveItems, archiveExperience(fmt.Sprintf("archive-%02d", i), "rollback regression archive lesson"))
	}
	archive := &fakeArchiveSource{items: archiveItems}
	service := NewServiceWithArchive(nil, archive)

	results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Principal:      "agent:developer",
		Query:          "rollback regression archive lesson",
		CurrentContext: "rollback investigation needs older regression context",
		ArchiveTriggerClasses: []cognitive.ExperienceArchiveTriggerClass{
			cognitive.ExperienceArchiveTriggerRegressionOrRollback,
		},
		Limit: MaxArchiveResurfacingLimit + 99,
	})

	require.NoError(t, err)
	require.Len(t, results, MaxArchiveResurfacingLimit)
	require.Equal(t, 1, archive.calls)
	require.Equal(t, MaxArchiveResurfacingLimit, archive.limit)
	require.Equal(t, []cognitive.ExperienceArchiveTriggerClass{cognitive.ExperienceArchiveTriggerRegressionOrRollback}, archive.triggers)
	for _, result := range results {
		require.Equal(t, "archive", result.SourceAttribution[0].Kind)
		require.Equal(t, []cognitive.ExperienceArchiveTriggerClass{cognitive.ExperienceArchiveTriggerRegressionOrRollback}, result.ArchiveTriggerClasses)
	}

	evidence := service.ArchiveEvidence()
	require.Len(t, evidence, 1)
	require.Equal(t, []cognitive.ExperienceArchiveTriggerClass{cognitive.ExperienceArchiveTriggerRegressionOrRollback}, evidence[0].TriggerClasses)
	require.Equal(t, "agent:developer", evidence[0].CallerPrincipal)
	require.Equal(t, "engram", evidence[0].Project)
	require.Equal(t, []string{"archive-session"}, evidence[0].SessionIDs)
	require.Equal(t, MaxArchiveResurfacingLimit, evidence[0].RequestedLimit)
	require.Equal(t, MaxArchiveResurfacingLimit, evidence[0].Returned)
	require.True(t, evidence[0].ExperienceRetrievalRan)
	require.False(t, evidence[0].AntiApplicabilityBlocked)
	require.Len(t, evidence[0].EvidenceRefs, MaxArchiveResurfacingLimit)
	require.Contains(t, evidence[0].EvidenceRefs, "archive:archive-00")
	require.Equal(t, "archive_resurfaced", evidence[0].Status)
	require.Equal(t, "explicit named archive trigger lookup", evidence[0].Reason)
}

func TestServiceArchiveEvidenceFiltersProjectBeforeAudit(t *testing.T) {
	engramItem := archiveExperience("archive-engram", "rollback regression archive lesson")
	otherProjectItem := archiveExperience("archive-other", "rollback regression archive lesson")
	otherProjectItem.SourceAttribution[0].Project = "other"
	otherProjectItem.SourceAttribution[0].SessionID = "other-session"
	archive := &fakeArchiveSource{items: []cognitive.ExperienceResponse{otherProjectItem, engramItem}}
	service := NewServiceWithArchive(nil, archive)

	results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Principal:      "agent:developer",
		Query:          "rollback regression archive lesson",
		CurrentContext: "rollback investigation needs older regression context",
		ArchiveTriggerClasses: []cognitive.ExperienceArchiveTriggerClass{
			cognitive.ExperienceArchiveTriggerRegressionOrRollback,
		},
		Limit: 5,
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "archive-engram", results[0].SourceAttribution[0].ID)
	evidence := service.ArchiveEvidence()
	require.Len(t, evidence, 1)
	require.Equal(t, 1, evidence[0].Returned)
	require.Equal(t, []string{"archive-session"}, evidence[0].SessionIDs)
	require.Contains(t, evidence[0].EvidenceRefs, "archive:archive-engram")
	require.NotContains(t, evidence[0].EvidenceRefs, "archive:archive-other")
}

func TestServiceArchiveEvidenceRecordsAntiApplicabilityBlock(t *testing.T) {
	blocked := archiveExperience("archive-blocked", "PowerShell rollback archive lesson")
	blocked.AntiApplicability = []cognitive.ExperienceAntiApplicability{
		{
			Condition: "Windows PowerShell command target",
			Rationale: "PowerShell command shape must not be reused silently.",
		},
	}
	archive := &fakeArchiveSource{items: []cognitive.ExperienceResponse{blocked}}
	service := NewServiceWithArchive(nil, archive)

	results, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Principal:      "agent:developer",
		Query:          "rollback command quoting",
		CurrentContext: "Windows PowerShell command target",
		ArchiveTriggerClasses: []cognitive.ExperienceArchiveTriggerClass{
			cognitive.ExperienceArchiveTriggerSimilarPriorFailure,
		},
		Limit: 1,
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, cognitive.ExperienceApplicabilityBlocked, results[0].Applicability.State)

	evidence := service.ArchiveEvidence()
	require.Len(t, evidence, 1)
	require.True(t, evidence[0].ExperienceRetrievalRan)
	require.True(t, evidence[0].AntiApplicabilityBlocked)
	require.Equal(t, []string{"archive-session"}, evidence[0].SessionIDs)
	require.Equal(t, []string{"archive:archive-blocked"}, evidence[0].EvidenceRefs)
}

func TestServiceArchiveEvidenceRetainsBoundedRecentEntries(t *testing.T) {
	archive := &fakeArchiveSource{
		items: []cognitive.ExperienceResponse{
			archiveExperience("archive-1", "rollback archive lesson"),
		},
	}
	service := NewServiceWithArchive(nil, archive)

	for i := 0; i < MaxArchiveEvidenceEntries+2; i++ {
		_, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
			Project:        "engram",
			Query:          "rollback archive lesson",
			CurrentContext: "rollback investigation needs archive context",
			ArchiveTriggerClasses: []cognitive.ExperienceArchiveTriggerClass{
				cognitive.ExperienceArchiveTriggerRegressionOrRollback,
			},
			Limit: 1,
		})
		require.NoError(t, err)
	}

	require.Len(t, service.ArchiveEvidence(), MaxArchiveEvidenceEntries)
}

func TestServiceArchiveEvidenceRecordsArchiveSourceError(t *testing.T) {
	archiveErr := errors.New("archive source unavailable")
	archive := &fakeArchiveSource{err: archiveErr}
	service := NewServiceWithArchive(nil, archive)

	_, err := service.QueryExperience(context.Background(), cognitive.ExperienceQueryRequest{
		Project:        "engram",
		Principal:      "agent:developer",
		Query:          "rollback archive lesson",
		CurrentContext: "rollback investigation needs older regression context",
		ArchiveTriggerClasses: []cognitive.ExperienceArchiveTriggerClass{
			cognitive.ExperienceArchiveTriggerRegressionOrRollback,
		},
		Limit: MaxArchiveResurfacingLimit + 99,
	})

	require.ErrorIs(t, err, archiveErr)
	require.Equal(t, 1, archive.calls)
	require.Equal(t, MaxArchiveResurfacingLimit, archive.limit)

	evidence := service.ArchiveEvidence()
	require.Len(t, evidence, 1)
	require.Equal(t, []cognitive.ExperienceArchiveTriggerClass{cognitive.ExperienceArchiveTriggerRegressionOrRollback}, evidence[0].TriggerClasses)
	require.Equal(t, "agent:developer", evidence[0].CallerPrincipal)
	require.Equal(t, "engram", evidence[0].Project)
	require.Equal(t, MaxArchiveResurfacingLimit, evidence[0].RequestedLimit)
	require.Zero(t, evidence[0].Returned)
	require.True(t, evidence[0].ExperienceRetrievalRan)
	require.False(t, evidence[0].AntiApplicabilityBlocked)
	require.Equal(t, []string{"archive_trigger:regression_or_rollback"}, evidence[0].EvidenceRefs)
	require.Equal(t, "archive_error", evidence[0].Status)
	require.Equal(t, "archive source returned error", evidence[0].Reason)
}

type fakeArchiveSource struct {
	items    []cognitive.ExperienceResponse
	triggers []cognitive.ExperienceArchiveTriggerClass
	calls    int
	limit    int
	err      error
}

func (f *fakeArchiveSource) QueryArchiveExperience(_ context.Context, _ cognitive.ExperienceQueryRequest, triggers []cognitive.ExperienceArchiveTriggerClass, limit int) ([]cognitive.ExperienceResponse, error) {
	f.calls++
	f.triggers = append([]cognitive.ExperienceArchiveTriggerClass(nil), triggers...)
	f.limit = limit
	if f.err != nil {
		return nil, f.err
	}
	if len(f.items) > limit {
		return append([]cognitive.ExperienceResponse(nil), f.items[:limit]...), nil
	}
	return append([]cognitive.ExperienceResponse(nil), f.items...), nil
}

func archiveExperience(id, lesson string) cognitive.ExperienceResponse {
	item := fixtureExperience(id, lesson, "engram", "archive-session")
	item.SourceAttribution[0].Kind = "archive"
	return item
}
