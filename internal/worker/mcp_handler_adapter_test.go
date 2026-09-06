package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

func TestMCPHandlerAdapter_ReadOnlyMutationIsToolErrorOverGRPC(t *testing.T) {
	mcpServer := mcp.NewServer(mcp.ServerOptions{Version: "test"})
	_, grpcServer := grpcserver.New(&mcpHandlerAdapter{mcpServer: mcpServer}, nil)
	ctx := auth.WithIdentity(context.Background(), auth.Client("read-only", "keycard-read-only"))

	response, err := grpcServer.CallTool(ctx, &pb.CallToolRequest{
		ToolName:      "docs",
		ArgumentsJson: []byte(`{"action":"create","content":"api_key=must-not-appear"}`),
	})
	require.NoError(t, err)
	require.True(t, response.IsError)

	var toolError struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.ContentJson, &toolError))
	assert.Equal(t, -32000, toolError.Code)
	assert.Equal(t, "Tool error: read_only: action is not permitted", toolError.Message)
	assert.Equal(t, "read_only: action is not permitted", toolError.Data)
	assert.NotContains(t, string(response.ContentJson), "must-not-appear")
}

func TestMCPHandlerAdapterUsesForwardedCorrelationForUCIExposureReplay(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	ref := adapterUCIRequestIdentityContextRef()
	catalog := &adapterUCIRequestIdentityCatalog{record: uci.ContextRecord{Ref: ref, AuthRealm: string(auth.SourceClient)}}
	application := &adapterUCIRequestIdentityApplication{
		resolver: uci.NewContextResolver(catalog, adapterUCIRequestIdentityAuthorizer{}, catalog),
		ref:      ref,
		response: adapterUCIRequestIdentityQueryResponse(t, ref),
	}
	mcpServer := mcp.NewServer(mcp.ServerOptions{Version: "test"})
	mcpServer.SetCodebaseContextApplication(application)
	exposureStore := &adapterUCIRequestIdentityExposureStore{}
	mcpServer.SetUCIExposureRecorder(uci.NewExposureRecorder(exposureStore, nil))
	adapter := &mcpHandlerAdapter{mcpServer: mcpServer}

	stringCorrelation, ok := auditcontext.NewUCIRequestCorrelation(json.RawMessage(`"daemon-request-42"`))
	require.True(t, ok)
	numericCorrelation, ok := auditcontext.NewUCIRequestCorrelation(json.RawMessage(`42`))
	require.True(t, ok)

	first := adapterUCIRequestIdentityCall(t, adapter, stringCorrelation)
	replay := adapterUCIRequestIdentityCall(t, adapter, stringCorrelation)
	require.Equal(t, first, replay, "an exact caller-ID replay must reuse its exposure")
	require.Len(t, exposureStore.records, 1)

	numeric := adapterUCIRequestIdentityCall(t, adapter, numericCorrelation)
	require.NotEqual(t, first, numeric, "a numeric ID must remain distinct from a string ID")
	require.Len(t, exposureStore.records, 2)

	missingCorrelationCtx := mcp.ContextWithSession(context.Background(), "adapter-session")
	missingCorrelationCtx = auth.WithIdentity(missingCorrelationCtx, auth.ClientWithPrincipal("read-write", "adapter-keycard", "agent/adapter", auth.PrincipalKindAgent))
	missingCorrelationCtx = auditcontext.WithUCIRequestCorrelationRequired(missingCorrelationCtx)
	_, _, err := adapter.HandleToolCall(missingCorrelationCtx, "codebase_search", []byte(`{"query":"Fixture"}`))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "daemon-request-42")
	require.Len(t, exposureStore.records, 2)

	for _, record := range exposureStore.records {
		require.NotContains(t, record.RequestRef, "daemon-request-42")
		require.NotContains(t, record.IdempotencyKey, "daemon-request-42")
	}
}

func adapterUCIRequestIdentityCall(t *testing.T, adapter *mcpHandlerAdapter, correlation auditcontext.UCIRequestCorrelation) string {
	t.Helper()
	ctx := mcp.ContextWithSession(context.Background(), "adapter-session")
	ctx = auth.WithIdentity(ctx, auth.ClientWithPrincipal("read-write", "adapter-keycard", "agent/adapter", auth.PrincipalKindAgent))
	ctx = auditcontext.WithUCIRequestCorrelationRequired(ctx)
	if correlation.Valid() {
		ctx = auditcontext.WithUCIRequestCorrelation(ctx, correlation)
	}
	result, isError, err := adapter.HandleToolCall(ctx, "codebase_search", []byte(`{"query":"Fixture"}`))
	require.NoError(t, err)
	require.False(t, isError)

	var envelope struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(result, &envelope))
	require.Len(t, envelope.Content, 1)
	var response uci.QueryResponse
	require.NoError(t, json.Unmarshal([]byte(envelope.Content[0].Text), &response))
	require.NotNil(t, response.Exposure)
	return response.Exposure.ExposureRef
}

type adapterUCIRequestIdentityApplication struct {
	resolver *uci.ContextResolver
	ref      uci.ContextRef
	response uci.QueryResponse
}

func (application *adapterUCIRequestIdentityApplication) Resolve(ctx context.Context, input uci.ResolveContextInput) (uci.AuthorizedContext, error) {
	ref := application.ref
	input.Ref = &ref
	return application.resolver.Resolve(ctx, input)
}

func (application *adapterUCIRequestIdentityApplication) Authorize(ctx context.Context, input uci.ResolveContextInput) (uci.AuthorizedContext, error) {
	return application.resolver.Authorize(ctx, input)
}

func (application *adapterUCIRequestIdentityApplication) List(_ context.Context, _ uci.ResolveContextInput) ([]uci.ContextRef, error) {
	return []uci.ContextRef{application.ref}, nil
}

func (*adapterUCIRequestIdentityApplication) Project(context.Context, uci.ContextRef) (map[string]string, error) {
	return map[string]string{"source": "fixture", "checkout": "fixture", "view": "fixture"}, nil
}

func (*adapterUCIRequestIdentityApplication) ResolveLegacyProject(context.Context, uci.AuthorizedContext, string) (uci.AliasTarget, error) {
	return uci.AliasTarget{}, errors.New("legacy project compatibility is not used by this fixture")
}

func (application *adapterUCIRequestIdentityApplication) SearchCodebase(context.Context, uci.AuthorizedContext, mcp.CodebaseSearchInput) (uci.QueryResponse, error) {
	return application.response, nil
}

func (*adapterUCIRequestIdentityApplication) CodebaseStatus(context.Context, uci.AuthorizedContext) (mcp.CodebaseStatusSnapshot, error) {
	return mcp.CodebaseStatusSnapshot{}, nil
}

type adapterUCIRequestIdentityCatalog struct {
	record uci.ContextRecord
}

func (catalog *adapterUCIRequestIdentityCatalog) LoadContext(context.Context, uci.ContextRef) (uci.ContextRecord, error) {
	return catalog.record, nil
}

func (*adapterUCIRequestIdentityCatalog) LoadIndexBinding(context.Context, uci.IndexBindingSelector) (uci.IndexBinding, error) {
	return uci.IndexBinding{}, errors.New("index bindings are not used by this fixture")
}

type adapterUCIRequestIdentityAuthorizer struct{}

func (adapterUCIRequestIdentityAuthorizer) AuthorizeContext(context.Context, uci.ContextAccess) error {
	return nil
}

type adapterUCIRequestIdentityExposureStore struct {
	records []uci.ExposureRecord
}

func (store *adapterUCIRequestIdentityExposureStore) AppendExposure(_ context.Context, record uci.ExposureRecord) (uci.ExposureRecord, error) {
	for _, existing := range store.records {
		if existing.IdempotencyKey != record.IdempotencyKey {
			continue
		}
		if existing.BindingDigest != record.BindingDigest {
			return uci.ExposureRecord{}, uci.ErrIdempotencyMismatch
		}
		return existing, nil
	}
	record.ExposureRef = uci.NewExposureRef()
	store.records = append(store.records, record)
	return record, nil
}

func (store *adapterUCIRequestIdentityExposureStore) AppendCompletion(_ context.Context, evidence uci.CompletionEvidence) (uci.CompletionEvidence, error) {
	return evidence, nil
}

func adapterUCIRequestIdentityContextRef() uci.ContextRef {
	return uci.ContextRef{
		SourceID:          "20000000-0000-4000-8000-000000000001",
		CheckoutID:        "30000000-0000-4000-8000-000000000001",
		ViewID:            "40000000-0000-4000-8000-000000000001",
		AnalysisProfileID: "50000000-0000-4000-8000-000000000001",
		Generation:        7,
	}
}

func adapterUCIRequestIdentityQueryResponse(t *testing.T, ref uci.ContextRef) uci.QueryResponse {
	t.Helper()
	zero := int64(0)
	truncated := false
	contexts := uci.QueryContexts{uci.QueryContextRef{
		SourceID:   ref.SourceID,
		CheckoutID: ref.CheckoutID,
		ViewID:     ref.ViewID,
		Generation: ref.Generation,
		ProfileID:  ref.AnalysisProfileID,
		SpaceID:    ref.SpaceID,
	}}
	items := uci.QueryItems{uci.QueryItem{
		Ref: uci.QueryEntityRef{
			SourceID:  ref.SourceID,
			ViewID:    ref.ViewID,
			EntityKey: "Fixture.Symbol",
		},
		Path:          "internal/fixture.go",
		Span:          uci.QuerySpan{ByteStart: 0, ByteEnd: 12, LineStart: 1, LineEnd: 1},
		ContentDigest: uci.QueryContentDigest("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Kind:          uci.QueryItemCode,
		Language:      "go",
		Excerpt:       "package demo",
		MatchSources:  []uci.QueryMatchSource{uci.QueryMatchFTS},
	}}
	warnings := uci.QueryWarnings{}
	continuation := uci.QueryContinuation{}
	response := uci.QueryResponse{
		Schema:   uci.QueryResponseSchema,
		Status:   uci.QueryStatusOK,
		Contexts: &contexts,
		Freshness: &uci.QueryFreshness{
			State:          uci.QueryFreshnessObservedCurrent,
			Method:         uci.QueryFreshnessWatchWatermark,
			PendingChanges: &zero,
			EnrichmentWatermark: uci.QueryEnrichmentWatermark{
				Sequence: ref.Generation,
				State:    uci.QueryEnrichmentCurrent,
			},
		},
		Retrieval: &uci.QueryRetrieval{
			Mode:               uci.QueryRetrievalLexical,
			DegradationReasons: []string{},
		},
		Coverage: &uci.QueryCoverage{
			Structural:       uci.IndexCoverageComplete,
			UnresolvedSites:  &zero,
			UnsupportedFiles: &zero,
		},
		Items:        &items,
		Truncated:    &truncated,
		Warnings:     &warnings,
		Continuation: &continuation,
	}
	require.NoError(t, response.ValidatePreExposure())
	return response
}
