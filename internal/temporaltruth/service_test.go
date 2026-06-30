package temporaltruth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/cognitive"
)

func TestService_QuerySelectedFactReturnsTrueNowThenHistoryAndProvenance(t *testing.T) {
	validFromThen := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	invalidatedAt := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	validFromNow := invalidatedAt
	asOfThen := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	service := NewService([]Record{
		{
			FactID:                "deploy.primary_region",
			FactClass:             "deployment_setting",
			Project:               "engram",
			Value:                 "us-east-1",
			ValidFrom:             validFromThen,
			ValidUntil:            &invalidatedAt,
			InvalidatedAt:         &invalidatedAt,
			InvalidationRationale: "operator moved the primary region after latency review",
			Provenance: []cognitive.TemporalTruthProvenance{
				{Kind: "decision", ID: "adr:old-region", Project: "engram"},
			},
		},
		{
			FactID:    "deploy.primary_region",
			FactClass: "deployment_setting",
			Project:   "engram",
			Value:     "eu-central-1",
			ValidFrom: validFromNow,
			Provenance: []cognitive.TemporalTruthProvenance{
				{Kind: "decision", ID: "adr:new-region", Project: "engram"},
			},
		},
	})

	response, err := service.QueryTemporalTruth(context.Background(), cognitive.TemporalTruthQueryRequest{
		FactID:  "deploy.primary_region",
		Project: "engram",
		AsOf:    &asOfThen,
		Limit:   5,
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.TemporalTruthFound, response.State)
	require.True(t, response.Scope.Selected)
	require.Equal(t, "deploy.primary_region", response.Scope.FactID)
	require.Equal(t, "deployment_setting", response.Scope.FactClass)
	require.NotNil(t, response.TrueNow)
	require.Equal(t, "eu-central-1", response.TrueNow.Value)
	require.NotNil(t, response.TrueThen)
	require.Equal(t, "us-east-1", response.TrueThen.Value)
	require.Equal(t, "operator moved the primary region after latency review", response.TrueThen.InvalidationRationale)
	require.Len(t, response.TrueNow.Provenance, 1)
	require.Len(t, response.TrueThen.Provenance, 1)
	require.Len(t, response.History, 2)
}

func TestService_QueryUnselectedFactStaysNarrow(t *testing.T) {
	service := NewService([]Record{
		{
			FactID:    "deploy.primary_region",
			FactClass: "deployment_setting",
			Project:   "engram",
			Value:     "eu-central-1",
			ValidFrom: time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC),
			Provenance: []cognitive.TemporalTruthProvenance{
				{Kind: "decision", ID: "adr:new-region", Project: "engram"},
			},
		},
	})

	response, err := service.QueryTemporalTruth(context.Background(), cognitive.TemporalTruthQueryRequest{
		FactID:  "unselected.company_brain_everything",
		Project: "engram",
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.TemporalTruthNotSelected, response.State)
	require.False(t, response.Scope.Selected)
	require.Empty(t, response.History)
	require.Nil(t, response.TrueNow)
}

func TestService_TruthChangeExposesInvalidationAndProvenanceChain(t *testing.T) {
	validFromThen := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	invalidatedAt := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	service := NewService([]Record{
		{
			FactID:                "release.supported_version",
			FactClass:             "release_policy",
			Project:               "engram",
			Value:                 "v6",
			ValidFrom:             validFromThen,
			ValidUntil:            &invalidatedAt,
			InvalidatedAt:         &invalidatedAt,
			InvalidationRationale: "v7 became the supported release line",
			Provenance: []cognitive.TemporalTruthProvenance{
				{Kind: "release", ID: "release:v6", Project: "engram"},
			},
		},
		{
			FactID:    "release.supported_version",
			FactClass: "release_policy",
			Project:   "engram",
			Value:     "v7",
			ValidFrom: invalidatedAt,
			Provenance: []cognitive.TemporalTruthProvenance{
				{Kind: "release", ID: "release:v7", Project: "engram"},
			},
		},
	})

	response, err := service.QueryTemporalTruth(context.Background(), cognitive.TemporalTruthQueryRequest{
		FactID:  "release.supported_version",
		Project: "engram",
	})

	require.NoError(t, err)
	require.NotNil(t, response.TrueNow)
	require.Equal(t, "v7", response.TrueNow.Value)
	require.Len(t, response.History, 2)
	require.Equal(t, "v6", response.History[0].Value)
	require.Equal(t, "v7 became the supported release line", response.History[0].InvalidationRationale)
	require.Len(t, response.ProvenanceChain, 2)
	require.Equal(t, "release:v6", response.ProvenanceChain[0].ID)
	require.Equal(t, "release:v7", response.ProvenanceChain[1].ID)
}

func TestService_QueryTemporalTruthIgnoresFutureDatedTrueNow(t *testing.T) {
	now := time.Now().UTC()
	futureStart := now.Add(2 * time.Hour)
	futureAsOf := now.Add(3 * time.Hour)
	service := NewService([]Record{
		{
			FactID:     "release.supported_version",
			FactClass:  "release_policy",
			Project:    "engram",
			Value:      "v7",
			ValidFrom:  now.Add(-24 * time.Hour),
			ValidUntil: &futureStart,
			Provenance: []cognitive.TemporalTruthProvenance{
				{Kind: "release", ID: "release:v7", Project: "engram"},
			},
		},
		{
			FactID:    "release.supported_version",
			FactClass: "release_policy",
			Project:   "engram",
			Value:     "v8",
			ValidFrom: futureStart,
			Provenance: []cognitive.TemporalTruthProvenance{
				{Kind: "release", ID: "release:v8", Project: "engram"},
			},
		},
	})

	response, err := service.QueryTemporalTruth(context.Background(), cognitive.TemporalTruthQueryRequest{
		FactID:  "release.supported_version",
		Project: "engram",
		AsOf:    &futureAsOf,
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.TemporalTruthFound, response.State)
	require.NotNil(t, response.TrueNow)
	require.Equal(t, "v7", response.TrueNow.Value)
	require.Nil(t, response.TrueThen)
	require.Len(t, response.History, 1)
	require.Equal(t, "v7", response.History[0].Value)
	require.Len(t, response.ProvenanceChain, 1)
	require.Equal(t, "release:v7", response.ProvenanceChain[0].ID)
}

func TestService_QueryTemporalTruthFutureOnlyFactIsUnknown(t *testing.T) {
	service := NewService([]Record{
		{
			FactID:    "release.supported_version",
			FactClass: "release_policy",
			Project:   "engram",
			Value:     "v8",
			ValidFrom: time.Now().UTC().Add(2 * time.Hour),
		},
	})

	response, err := service.QueryTemporalTruth(context.Background(), cognitive.TemporalTruthQueryRequest{
		FactID:  "release.supported_version",
		Project: "engram",
	})

	require.NoError(t, err)
	require.Equal(t, cognitive.TemporalTruthUnknown, response.State)
	require.Nil(t, response.TrueNow)
}
