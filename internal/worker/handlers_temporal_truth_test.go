package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/cognitive"
)

type fakeTemporalTruthProvider struct {
	response             cognitive.TemporalTruthResponse
	responseAfterRefresh *cognitive.TemporalTruthResponse
	err                  error
	request              cognitive.TemporalTruthQueryRequest
	refreshResult        gormdb.TemporalTruthAdmissionResult
	refreshErr           error
	refreshProject       string
	queryCalls           int
}

func (f *fakeTemporalTruthProvider) QueryTemporalTruth(_ context.Context, request cognitive.TemporalTruthQueryRequest) (cognitive.TemporalTruthResponse, error) {
	f.queryCalls++
	f.request = request
	if f.err != nil {
		return cognitive.TemporalTruthResponse{}, f.err
	}
	return f.response, nil
}

func (f *fakeTemporalTruthProvider) RefreshProject(_ context.Context, project string) (gormdb.TemporalTruthAdmissionResult, error) {
	f.refreshProject = project
	if f.refreshErr != nil {
		return gormdb.TemporalTruthAdmissionResult{}, f.refreshErr
	}
	if f.responseAfterRefresh != nil {
		f.response = *f.responseAfterRefresh
	}
	return f.refreshResult, nil
}

func TestHandleTemporalTruthReadReturnsBoundedResponse(t *testing.T) {
	provider := &fakeTemporalTruthProvider{response: cognitive.TemporalTruthResponse{
		Scope: cognitive.TemporalTruthScope{FactID: "42", FactClass: "release_policy", Project: "engram", Selected: true},
		State: cognitive.TemporalTruthFound,
		TrueNow: &cognitive.TemporalTruthEntry{
			Value:     "v7",
			ValidFrom: time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC),
		},
	}}
	service := &Service{temporalTruthEnabled: true, temporalTruthProvider: provider}
	req := httptest.NewRequest(http.MethodGet, "/api/temporal-truth?project=engram&fact_id=42&fact_class=release_policy&as_of=2026-03-01T00:00:00Z&limit=5", nil)
	w := httptest.NewRecorder()

	service.handleTemporalTruthRead(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var response cognitive.TemporalTruthResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, cognitive.TemporalTruthFound, response.State)
	require.Equal(t, "42", provider.request.FactID)
	require.Equal(t, "release_policy", provider.request.FactClass)
	require.Equal(t, "engram", provider.request.Project)
	require.NotNil(t, provider.request.AsOf)
	require.Equal(t, 5, provider.request.Limit)
}

func TestHandleTemporalTruthRefreshThenReadReturnsTrueNow(t *testing.T) {
	provider := &fakeTemporalTruthProvider{
		response: cognitive.TemporalTruthResponse{
			Scope: cognitive.TemporalTruthScope{FactID: "42", FactClass: "release_policy", Project: "engram", Selected: true},
			State: cognitive.TemporalTruthNotSelected,
		},
		responseAfterRefresh: &cognitive.TemporalTruthResponse{
			Scope: cognitive.TemporalTruthScope{FactID: "42", FactClass: "release_policy", Project: "engram", Selected: true},
			State: cognitive.TemporalTruthFound,
			TrueNow: &cognitive.TemporalTruthEntry{
				Value:     "v7",
				ValidFrom: time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC),
				Provenance: []cognitive.TemporalTruthProvenance{{
					Kind:       "memory",
					ID:         "memory:11",
					Project:    "engram",
					ObservedAt: time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC),
				}},
			},
		},
		refreshResult: gormdb.TemporalTruthAdmissionResult{Project: "engram", AdmittedFacts: 1, AdmittedRecords: 2},
	}
	service := &Service{temporalTruthEnabled: true, temporalTruthProvider: provider}
	refreshReq := httptest.NewRequest(http.MethodPost, "/api/temporal-truth/refresh?project=engram", nil)
	refreshW := httptest.NewRecorder()

	service.handleTemporalTruthRefresh(refreshW, refreshReq)

	require.Equal(t, http.StatusOK, refreshW.Code)
	require.Equal(t, "engram", provider.refreshProject)

	readReq := httptest.NewRequest(http.MethodGet, "/api/temporal-truth?project=engram&fact_id=42&fact_class=release_policy", nil)
	readW := httptest.NewRecorder()

	service.handleTemporalTruthRead(readW, readReq)

	require.Equal(t, http.StatusOK, readW.Code)
	var response cognitive.TemporalTruthResponse
	require.NoError(t, json.Unmarshal(readW.Body.Bytes(), &response))
	require.Equal(t, cognitive.TemporalTruthFound, response.State)
	require.NotNil(t, response.TrueNow)
	require.Equal(t, "v7", response.TrueNow.Value)
	require.Len(t, response.TrueNow.Provenance, 1)
}

func TestHandleTemporalTruthRefreshRejectsMissingProject(t *testing.T) {
	service := &Service{temporalTruthEnabled: true, temporalTruthProvider: &fakeTemporalTruthProvider{}}
	req := httptest.NewRequest(http.MethodPost, "/api/temporal-truth/refresh", nil)
	w := httptest.NewRecorder()

	service.handleTemporalTruthRefresh(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "project is required")
}

func TestHandleTemporalTruthRefreshDisabledWhenFlagOff(t *testing.T) {
	service := &Service{temporalTruthEnabled: false, temporalTruthProvider: &fakeTemporalTruthProvider{}}
	req := httptest.NewRequest(http.MethodPost, "/api/temporal-truth/refresh?project=engram", nil)
	w := httptest.NewRecorder()

	service.handleTemporalTruthRefresh(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "temporal truth feature flag required")
}

func TestHandleTemporalTruthReadDisabledWhenFlagOff(t *testing.T) {
	service := &Service{temporalTruthEnabled: false, temporalTruthProvider: &fakeTemporalTruthProvider{}}
	req := httptest.NewRequest(http.MethodGet, "/api/temporal-truth?project=engram&fact_id=42", nil)
	w := httptest.NewRecorder()

	service.handleTemporalTruthRead(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "temporal truth feature flag required")
}

func TestHandleTemporalTruthReadGatedWhenProviderMissing(t *testing.T) {
	service := &Service{temporalTruthEnabled: true}
	req := httptest.NewRequest(http.MethodGet, "/api/temporal-truth?project=engram&fact_id=42", nil)
	w := httptest.NewRecorder()

	service.handleTemporalTruthRead(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "temporal truth provider not configured")
}

func TestHandleTemporalTruthReadRejectsMissingOrBlankProject(t *testing.T) {
	provider := &fakeTemporalTruthProvider{}
	service := &Service{temporalTruthEnabled: true, temporalTruthProvider: provider}

	for _, tc := range []struct {
		name string
		url  string
	}{
		{name: "missing project", url: "/api/temporal-truth?fact_id=42"},
		{name: "blank project", url: "/api/temporal-truth?project=%20%20&fact_id=42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			w := httptest.NewRecorder()

			service.handleTemporalTruthRead(w, req)

			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Contains(t, w.Body.String(), "project is required")
			require.Zero(t, provider.queryCalls)
		})
	}
}

func TestHandleTemporalTruthReadRejectsInvalidAsOf(t *testing.T) {
	service := &Service{temporalTruthEnabled: true, temporalTruthProvider: &fakeTemporalTruthProvider{}}
	req := httptest.NewRequest(http.MethodGet, "/api/temporal-truth?project=engram&fact_id=42&as_of=nope", nil)
	w := httptest.NewRecorder()

	service.handleTemporalTruthRead(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "as_of must be RFC3339")
}
