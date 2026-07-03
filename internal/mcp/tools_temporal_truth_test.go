package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/cognitive"
)

type fakeTemporalTruthProvider struct {
	response       cognitive.TemporalTruthResponse
	err            error
	request        cognitive.TemporalTruthQueryRequest
	refreshResult  gormdb.TemporalTruthAdmissionResult
	refreshErr     error
	refreshProject string
	queryCalls     int
	refreshCalls   int
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
	f.refreshCalls++
	f.refreshProject = project
	if f.refreshErr != nil {
		return gormdb.TemporalTruthAdmissionResult{}, f.refreshErr
	}
	return f.refreshResult, nil
}

func TestTemporalTruthToolAdvertisedWhenProviderWiredAndFlagOn(t *testing.T) {
	t.Setenv("ENGRAM_TEMPORAL_TRUTH_ENABLED", " true ")
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetTemporalTruthProvider(&fakeTemporalTruthProvider{})

	props := findToolProperties(t, srv.ListTools(), "temporal_truth")
	require.Contains(t, props, "fact_id")
	require.Contains(t, props, "as_of")
}

func TestTemporalTruthRefreshToolAdvertisedWhenProviderWiredAndFlagOn(t *testing.T) {
	t.Setenv("ENGRAM_TEMPORAL_TRUTH_ENABLED", "true")
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetTemporalTruthProvider(&fakeTemporalTruthProvider{})

	props := findToolProperties(t, srv.ListTools(), "temporal_truth_refresh")
	require.Contains(t, props, "project")
}

func TestTemporalTruthToolsAbsentWhenFlagOff(t *testing.T) {
	t.Setenv("ENGRAM_TEMPORAL_TRUTH_ENABLED", "false")
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetTemporalTruthProvider(&fakeTemporalTruthProvider{})

	names := listedToolNames(srv.ListTools())
	require.NotContains(t, names, "temporal_truth")
	require.NotContains(t, names, "temporal_truth_refresh")
}

func TestTemporalTruthDirectCallFailsClosedWhenFeatureGateUnsatisfied(t *testing.T) {
	provider := &fakeTemporalTruthProvider{}
	t.Setenv("ENGRAM_TEMPORAL_TRUTH_ENABLED", "false")
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetTemporalTruthProvider(provider)

	resp := callTemporalTruthTool(t, srv, "temporal_truth", map[string]any{
		"project": "engram",
		"fact_id": "42",
	})

	require.NotNil(t, resp.Error)
	require.Contains(t, resp.Error.Message, "temporal truth feature flag required")
	require.Zero(t, provider.queryCalls)

	t.Setenv("ENGRAM_TEMPORAL_TRUTH_ENABLED", "true")
	srv = NewServer(ServerOptions{Version: "test"})

	resp = callTemporalTruthTool(t, srv, "temporal_truth", map[string]any{
		"project": "engram",
		"fact_id": "42",
	})

	require.NotNil(t, resp.Error)
	require.Contains(t, resp.Error.Message, "temporal truth provider not configured")
}

func TestTemporalTruthRefreshDirectCallFailsClosedWhenFeatureGateUnsatisfied(t *testing.T) {
	provider := &fakeTemporalTruthProvider{}
	t.Setenv("ENGRAM_TEMPORAL_TRUTH_ENABLED", "false")
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetTemporalTruthProvider(provider)

	resp := callTemporalTruthTool(t, srv, "temporal_truth_refresh", map[string]any{"project": "engram"})

	require.NotNil(t, resp.Error)
	require.Contains(t, resp.Error.Message, "temporal truth feature flag required")
	require.Zero(t, provider.refreshCalls)

	t.Setenv("ENGRAM_TEMPORAL_TRUTH_ENABLED", "true")
	srv = NewServer(ServerOptions{Version: "test"})

	resp = callTemporalTruthTool(t, srv, "temporal_truth_refresh", map[string]any{"project": "engram"})

	require.NotNil(t, resp.Error)
	require.Contains(t, resp.Error.Message, "temporal truth provider not configured")
}

func TestHandleTemporalTruthReturnsBoundedResponse(t *testing.T) {
	t.Setenv("ENGRAM_TEMPORAL_TRUTH_ENABLED", "true")
	provider := &fakeTemporalTruthProvider{response: cognitive.TemporalTruthResponse{
		Scope: cognitive.TemporalTruthScope{FactID: "42", FactClass: "release_policy", Project: "engram", Selected: true},
		State: cognitive.TemporalTruthFound,
		TrueNow: &cognitive.TemporalTruthEntry{
			Value:     "v7",
			ValidFrom: time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC),
		},
	}}
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetTemporalTruthProvider(provider)
	args, err := json.Marshal(map[string]any{
		"project":    "engram",
		"fact_id":    "42",
		"fact_class": "release_policy",
		"as_of":      "2026-03-01T00:00:00Z",
		"limit":      5,
	})
	require.NoError(t, err)

	result, err := srv.handleTemporalTruth(context.Background(), args)

	require.NoError(t, err)
	var response cognitive.TemporalTruthResponse
	require.NoError(t, json.Unmarshal([]byte(result), &response))
	require.Equal(t, cognitive.TemporalTruthFound, response.State)
	require.Equal(t, "42", provider.request.FactID)
	require.NotNil(t, provider.request.AsOf)
	require.Equal(t, 5, provider.request.Limit)
}

func TestHandleTemporalTruthRefreshReturnsAdmissionResult(t *testing.T) {
	t.Setenv("ENGRAM_TEMPORAL_TRUTH_ENABLED", "true")
	provider := &fakeTemporalTruthProvider{refreshResult: gormdb.TemporalTruthAdmissionResult{Project: "engram", AdmittedFacts: 1, AdmittedRecords: 2}}
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetTemporalTruthProvider(provider)
	args, err := json.Marshal(map[string]any{"project": "engram"})
	require.NoError(t, err)

	result, err := srv.handleTemporalTruthRefresh(context.Background(), args)

	require.NoError(t, err)
	require.Equal(t, "engram", provider.refreshProject)
	var response gormdb.TemporalTruthAdmissionResult
	require.NoError(t, json.Unmarshal([]byte(result), &response))
	require.Equal(t, 1, response.AdmittedFacts)
	require.Equal(t, 2, response.AdmittedRecords)
}

func TestHandleTemporalTruthRefreshRequiresProject(t *testing.T) {
	t.Setenv("ENGRAM_TEMPORAL_TRUTH_ENABLED", "true")
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetTemporalTruthProvider(&fakeTemporalTruthProvider{})
	args, err := json.Marshal(map[string]any{})
	require.NoError(t, err)

	_, err = srv.handleTemporalTruthRefresh(context.Background(), args)

	require.ErrorContains(t, err, "project is required")
}

func TestHandleTemporalTruthRequiresProject(t *testing.T) {
	t.Setenv("ENGRAM_TEMPORAL_TRUTH_ENABLED", "true")
	provider := &fakeTemporalTruthProvider{}
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetTemporalTruthProvider(provider)

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "missing project", args: map[string]any{"fact_id": "42"}},
		{name: "blank project", args: map[string]any{"project": "  ", "fact_id": "42"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, err := json.Marshal(tc.args)
			require.NoError(t, err)

			_, err = srv.handleTemporalTruth(context.Background(), args)

			require.ErrorContains(t, err, "project is required")
			require.Zero(t, provider.queryCalls)
		})
	}
}

func TestHandleTemporalTruthRejectsInvalidAsOf(t *testing.T) {
	t.Setenv("ENGRAM_TEMPORAL_TRUTH_ENABLED", "true")
	srv := NewServer(ServerOptions{Version: "test"})
	srv.SetTemporalTruthProvider(&fakeTemporalTruthProvider{})
	args, err := json.Marshal(map[string]any{
		"project": "engram",
		"fact_id": "42",
		"as_of":   "not-a-time",
	})
	require.NoError(t, err)

	_, err = srv.handleTemporalTruth(context.Background(), args)

	require.ErrorContains(t, err, "as_of must be RFC3339")
}

func callTemporalTruthTool(t *testing.T, srv *Server, name string, args map[string]any) *Response {
	t.Helper()
	params, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	require.NoError(t, err)
	resp := srv.HandleRequest(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})
	require.NotNil(t, resp)
	return resp
}
