package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/pkg/cognitive"
)

type fakeTemporalTruthStore struct {
	err            error
	rows           []gormdb.TemporalTruthStoredRecord
	query          cognitive.TemporalTruthQueryRequest
	refreshProject string
	refreshResult  gormdb.TemporalTruthAdmissionResult
	refreshErr     error
}

func (f *fakeTemporalTruthStore) LoadStoredRecords(_ context.Context, request cognitive.TemporalTruthQueryRequest) ([]gormdb.TemporalTruthStoredRecord, error) {
	f.query = request
	if f.err != nil {
		return nil, f.err
	}
	return append([]gormdb.TemporalTruthStoredRecord(nil), f.rows...), nil
}

func (f *fakeTemporalTruthStore) RefreshProject(_ context.Context, project string) (gormdb.TemporalTruthAdmissionResult, error) {
	f.refreshProject = project
	if f.refreshErr != nil {
		return gormdb.TemporalTruthAdmissionResult{}, f.refreshErr
	}
	return f.refreshResult, nil
}

type fakeTemporalTruthQueryService struct {
	err     error
	result  *principalmemory.PrincipalMemoryQueryResult
	request principalmemory.PrincipalMemoryQueryRequest
	called  bool
}

func (f *fakeTemporalTruthQueryService) Query(_ context.Context, req principalmemory.PrincipalMemoryQueryRequest) (*principalmemory.PrincipalMemoryQueryResult, error) {
	f.called = true
	f.request = req
	if f.err != nil {
		return nil, f.err
	}
	if f.result == nil {
		return &principalmemory.PrincipalMemoryQueryResult{}, nil
	}
	return f.result, nil
}

func TestMemoryTemporalTruthProviderUsesPrincipalQueryAndDropsHiddenRows(t *testing.T) {
	provider := newMemoryTemporalTruthProvider(&fakeTemporalTruthStore{rows: []gormdb.TemporalTruthStoredRecord{
		{
			FactID:          "42",
			FactClass:       "release_policy",
			Project:         "engram",
			Value:           "v6",
			ValidFrom:       time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
			SourceMemoryIDs: []int64{10},
		},
		{
			FactID:          "42",
			FactClass:       "release_policy",
			Project:         "engram",
			Value:           "v7",
			ValidFrom:       time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC),
			SourceMemoryIDs: []int64{11},
		},
	}}, &fakeTemporalTruthQueryService{result: &principalmemory.PrincipalMemoryQueryResult{Items: []principalmemory.PrincipalMemoryQueryItem{{
		ID:        11,
		Project:   "engram",
		Content:   "release policy visible",
		Domain:    "release",
		CreatedAt: time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC),
	}}}})
	querySvc := provider.querySvc.(*fakeTemporalTruthQueryService)
	store := provider.store.(*fakeTemporalTruthStore)
	ctx := auth.WithIdentity(context.Background(), auth.ClientWithPrincipal("read-write", "keycard-1", "agent/omp", auth.PrincipalKindAgent))

	response, err := provider.QueryTemporalTruth(ctx, cognitive.TemporalTruthQueryRequest{FactID: "42", Project: "engram"})

	require.NoError(t, err)
	require.True(t, querySvc.called)
	require.Equal(t, []int64{10, 11}, querySvc.request.IDs)
	require.Equal(t, "agent/omp", querySvc.request.Caller.Principal)
	require.Equal(t, "42", store.query.FactID)
	require.Equal(t, cognitive.TemporalTruthFound, response.State)
	require.NotNil(t, response.TrueNow)
	require.Equal(t, "v7", response.TrueNow.Value)
	require.Nil(t, response.TrueThen)
	require.Len(t, response.History, 1)
	require.Len(t, response.ProvenanceChain, 1)
	require.Equal(t, "memory:11", response.ProvenanceChain[0].ID)
}

func TestMemoryTemporalTruthProviderReturnsNotSelectedWhenAllRowsHidden(t *testing.T) {
	provider := newMemoryTemporalTruthProvider(&fakeTemporalTruthStore{rows: []gormdb.TemporalTruthStoredRecord{{
		FactID:          "42",
		FactClass:       "release_policy",
		Project:         "engram",
		Value:           "v7",
		ValidFrom:       time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC),
		SourceMemoryIDs: []int64{10},
	}}}, &fakeTemporalTruthQueryService{result: &principalmemory.PrincipalMemoryQueryResult{}})

	response, err := provider.QueryTemporalTruth(context.Background(), cognitive.TemporalTruthQueryRequest{FactID: "42", Project: "engram"})

	require.NoError(t, err)
	require.Equal(t, cognitive.TemporalTruthNotSelected, response.State)
}

func TestMemoryTemporalTruthProviderReturnsStoreError(t *testing.T) {
	provider := newMemoryTemporalTruthProvider(&fakeTemporalTruthStore{err: errors.New("store boom")}, &fakeTemporalTruthQueryService{})

	_, err := provider.QueryTemporalTruth(context.Background(), cognitive.TemporalTruthQueryRequest{FactID: "42", Project: "engram"})

	require.Error(t, err)
	require.ErrorContains(t, err, "store boom")
}

func TestMemoryTemporalTruthProviderRefreshProjectDelegatesToStore(t *testing.T) {
	store := &fakeTemporalTruthStore{refreshResult: gormdb.TemporalTruthAdmissionResult{Project: "engram", AdmittedFacts: 1, AdmittedRecords: 2}}
	provider := newMemoryTemporalTruthProvider(store, &fakeTemporalTruthQueryService{})

	result, err := provider.RefreshProject(context.Background(), " engram ")

	require.NoError(t, err)
	require.Equal(t, "engram", store.refreshProject)
	require.Equal(t, 1, result.AdmittedFacts)
	require.Equal(t, 2, result.AdmittedRecords)
}
