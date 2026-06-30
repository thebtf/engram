package experience

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/cognitive"
)

func TestArchiveTriggerTaxonomyIsFiniteAndRejectsUnknown(t *testing.T) {
	require.Equal(t, []cognitive.ExperienceArchiveTriggerClass{
		cognitive.ExperienceArchiveTriggerWhyChanged,
		cognitive.ExperienceArchiveTriggerRegression,
		cognitive.ExperienceArchiveTriggerRollback,
		cognitive.ExperienceArchiveTriggerOldDecisionRevisit,
		cognitive.ExperienceArchiveTriggerSimilarFailure,
	}, AllowedArchiveTriggerClasses())

	triggers, err := NormalizeArchiveTriggerClasses([]cognitive.ExperienceArchiveTriggerClass{
		cognitive.ExperienceArchiveTriggerRegression,
		cognitive.ExperienceArchiveTriggerRegression,
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
		Query:          "rollback regression archive lesson",
		CurrentContext: "rollback investigation needs older regression context",
		ArchiveTriggerClasses: []cognitive.ExperienceArchiveTriggerClass{
			cognitive.ExperienceArchiveTriggerRollback,
		},
		Limit: MaxArchiveResurfacingLimit + 99,
	})

	require.NoError(t, err)
	require.Len(t, results, MaxArchiveResurfacingLimit)
	require.Equal(t, 1, archive.calls)
	require.Equal(t, MaxArchiveResurfacingLimit, archive.limit)
	require.Equal(t, []cognitive.ExperienceArchiveTriggerClass{cognitive.ExperienceArchiveTriggerRollback}, archive.triggers)
	for _, result := range results {
		require.Equal(t, "archive", result.SourceAttribution[0].Kind)
		require.Equal(t, []cognitive.ExperienceArchiveTriggerClass{cognitive.ExperienceArchiveTriggerRollback}, result.ArchiveTriggerClasses)
	}

	evidence := service.ArchiveEvidence()
	require.Len(t, evidence, 1)
	require.Equal(t, []cognitive.ExperienceArchiveTriggerClass{cognitive.ExperienceArchiveTriggerRollback}, evidence[0].TriggerClasses)
	require.Equal(t, MaxArchiveResurfacingLimit, evidence[0].RequestedLimit)
	require.Equal(t, MaxArchiveResurfacingLimit, evidence[0].Returned)
	require.Equal(t, "archive_resurfaced", evidence[0].Status)
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
				cognitive.ExperienceArchiveTriggerRollback,
			},
			Limit: 1,
		})
		require.NoError(t, err)
	}

	require.Len(t, service.ArchiveEvidence(), MaxArchiveEvidenceEntries)
}

type fakeArchiveSource struct {
	items    []cognitive.ExperienceResponse
	triggers []cognitive.ExperienceArchiveTriggerClass
	calls    int
	limit    int
}

func (f *fakeArchiveSource) QueryArchiveExperience(_ context.Context, _ cognitive.ExperienceQueryRequest, triggers []cognitive.ExperienceArchiveTriggerClass, limit int) ([]cognitive.ExperienceResponse, error) {
	f.calls++
	f.triggers = append([]cognitive.ExperienceArchiveTriggerClass(nil), triggers...)
	f.limit = limit
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
