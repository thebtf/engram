package codeintel_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/handlers/codeintel"
	"github.com/thebtf/engram/internal/handlers/engramcore"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
	"github.com/thebtf/engram/internal/worker"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

func TestCodebaseStatusPinnedViewReaderAuthority(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	ref := uci.ContextRef{
		SourceID:          "11111111-1111-4111-8111-111111111111",
		CheckoutID:        "22222222-2222-4222-8222-222222222222",
		ViewID:            "33333333-3333-4333-8333-333333333333",
		AnalysisProfileID: "44444444-4444-4444-8444-444444444444",
		Generation:        1,
	}
	ownerWorkstation, valid := uci.NoAuthCodeWorkstationForInstance("status-owner-install")
	require.True(t, valid)
	store := &plainStatusAuthorityStore{ref: ref, workstation: ownerWorkstation}
	store.allowed.Store(true)
	resolver := uci.NewContextResolver(store, store, store)
	contextApp, err := mcp.NewUCIContextApplication(resolver, store)
	require.NoError(t, err)
	app, err := worker.NewUCIApplication(contextApp, &uci.AliasResolver{}, &uci.QueryService{}, nil, &uci.GraphService{}, &uci.VersionedReadService{}, uci.NewIndexStatusService(store, nil))
	require.NoError(t, err)
	server := mcp.NewServer(mcp.ServerOptions{Version: "plain-status-authority"})
	server.SetCodebaseContextApplication(app)
	port, err := mcp.NewUCIContextHandlePort(server, store, store)
	require.NoError(t, err)
	grpcServer, transport := grpcserver.New(plainStatusMCPHandler{server}, nil)
	transport.SetUCITransport(grpcserver.NewContextAwareUCITransport(resolver, nil, nil, port))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go grpcServer.Serve(listener)
	t.Cleanup(grpcServer.Stop)
	core := engramcore.NewModuleWithClientInstanceID("status-reader-install")
	t.Cleanup(func() { require.NoError(t, core.Shutdown(context.Background())) })
	module := codeintel.NewModule(core)
	ctx := auditcontext.WithUCITransportSession(context.Background(), "plain-status-reader")
	project := muxcore.ProjectContext{Cwd: "unregistered-directory-is-not-selection-authority", Env: map[string]string{config.EnvServerURL: "http://" + listener.Addr().String()}}
	selection, err := json.Marshal(map[string]any{"action": "select", "source_id": ref.SourceID, "checkout_id": ref.CheckoutID, "view_id": ref.ViewID, "analysis_profile_id": ref.AnalysisProfileID, "generation": ref.Generation})
	require.NoError(t, err)
	selected, err := core.ProxyHandleTool(ctx, project, "codebase_context", selection)
	require.NoError(t, err)
	var block struct {
		Text string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(selected, &block))
	var selectionResult struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal([]byte(block.Text), &selectionResult))
	require.Len(t, selectionResult.Content, 1)
	var chosen struct {
		Handle string `json:"context_handle"`
	}
	require.NoError(t, json.Unmarshal([]byte(selectionResult.Content[0].Text), &chosen))
	require.NotEmpty(t, chosen.Handle)
	arguments, err := json.Marshal(map[string]any{"context_handle": chosen.Handle})
	require.NoError(t, err)

	t.Run("readable nonowner pinned View", func(t *testing.T) {
		raw, err := module.HandleTool(ctx, project, "codebase_status", arguments)
		require.NoError(t, err)
		var result struct {
			Status  *string `json:"status"`
			Context struct {
				SourceID          string `json:"source_id"`
				CheckoutID        string `json:"checkout_id"`
				ViewID            string `json:"view_id"`
				AnalysisProfileID string `json:"profile_id"`
				Generation        int64  `json:"generation"`
			} `json:"context"`
			Total     int64              `json:"total_chunks"`
			Freshness uci.QueryFreshness `json:"freshness"`
		}
		require.NoError(t, json.Unmarshal(raw, &result))
		require.Equal(t, ref, uci.ContextRef{SourceID: result.Context.SourceID, CheckoutID: result.Context.CheckoutID, ViewID: result.Context.ViewID, AnalysisProfileID: result.Context.AnalysisProfileID, Generation: result.Context.Generation})
		require.Equal(t, int64(3), result.Total)
		require.Nil(t, result.Status, "a reader must not invent index-owner daemon liveness")
		require.Equal(t, uci.QueryFreshnessHistorical, result.Freshness.State)
		require.Equal(t, uci.QueryFreshnessPinnedHistory, result.Freshness.Method)
	})
	t.Run("read access does not grant indexing", func(t *testing.T) {
		_, err := module.HandleTool(ctx, project, "codebase_index", arguments)
		require.ErrorContains(t, err, "PermissionDenied")
	})
	t.Run("local token retains index authority", func(t *testing.T) {
		barrier, err := json.Marshal(map[string]any{"context_handle": chosen.Handle, "after_barrier": map[string]any{"token": "uci-run-v1-other-owner", "wait_ms": 60000}})
		require.NoError(t, err)
		_, err = module.HandleTool(ctx, project, "codebase_status", barrier)
		require.ErrorContains(t, err, "PermissionDenied")
	})
	t.Run("foreign client handle remains denied", func(t *testing.T) {
		foreign := auditcontext.WithUCITransportSession(context.Background(), "foreign-status-reader")
		_, err := module.HandleTool(foreign, project, "codebase_status", arguments)
		require.Error(t, err)
	})
	t.Run("revoked read scope remains denied", func(t *testing.T) {
		store.allowed.Store(false)
		_, err := module.HandleTool(ctx, project, "codebase_status", arguments)
		require.Error(t, err)
		store.allowed.Store(true)
	})
	t.Run("replaced registry epoch invalidates handle", func(t *testing.T) {
		server.SetCodebaseContextApplication(app)
		_, err := module.HandleTool(ctx, project, "codebase_status", arguments)
		require.Error(t, err)
	})
	t.Run("owned unpublished checkout retains never-indexed", func(t *testing.T) {
		store.unpublished.Store(true)
		owner := engramcore.NewModuleWithClientInstanceID("status-owner-install")
		t.Cleanup(func() { require.NoError(t, owner.Shutdown(context.Background())) })
		ownerContext := auditcontext.WithUCITransportSession(context.Background(), "unpublished-status-owner")
		selector, err := json.Marshal(map[string]any{"action": "select", "checkout": map[string]any{"source_id": ref.SourceID, "checkout_id": ref.CheckoutID, "incarnation_id": "55555555-5555-4555-8555-555555555555", "analysis_profile_id": ref.AnalysisProfileID}})
		require.NoError(t, err)
		selected, err := owner.ProxyHandleTool(ownerContext, project, "codebase_context", selector)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(selected, &block))
		require.NoError(t, json.Unmarshal([]byte(block.Text), &selectionResult))
		require.Len(t, selectionResult.Content, 1)
		require.NoError(t, json.Unmarshal([]byte(selectionResult.Content[0].Text), &chosen))
		noViewArgs, err := json.Marshal(map[string]any{"context_handle": chosen.Handle})
		require.NoError(t, err)
		raw, err := codeintel.NewModule(owner).HandleTool(ownerContext, project, "codebase_status", noViewArgs)
		require.NoError(t, err)
		var result struct {
			Status         string `json:"status"`
			ServerCounts   bool   `json:"server_counts_available"`
			CurrentContext any    `json:"current_context"`
		}
		require.NoError(t, json.Unmarshal(raw, &result))
		require.Equal(t, "never_indexed", result.Status)
		require.False(t, result.ServerCounts)
		require.Nil(t, result.CurrentContext)
	})
}

type plainStatusAuthorityStore struct {
	ref         uci.ContextRef
	workstation string
	allowed     atomic.Bool
	unpublished atomic.Bool
}

func (store *plainStatusAuthorityStore) LoadContext(_ context.Context, ref uci.ContextRef) (uci.ContextRecord, error) {
	if store.unpublished.Load() || ref != store.ref {
		return uci.ContextRecord{}, errors.New("unknown View")
	}
	return uci.ContextRecord{Ref: store.ref, AuthRealm: uci.NoAuthCodeRealm}, nil
}

func (store *plainStatusAuthorityStore) LoadIndexBinding(_ context.Context, selector uci.IndexBindingSelector) (uci.IndexBinding, error) {
	ref, pinned := selector.Context()
	if pinned {
		if ref != store.ref {
			return uci.IndexBinding{}, errors.New("unknown index selector")
		}
	} else {
		checkout, valid := selector.Checkout()
		if !valid || checkout.Scope.SourceID != store.ref.SourceID || checkout.Scope.CheckoutID != store.ref.CheckoutID || checkout.Scope.IncarnationID != "55555555-5555-4555-8555-555555555555" || checkout.ProfileID != store.ref.AnalysisProfileID {
			return uci.IndexBinding{}, errors.New("unknown checkout selector")
		}
		ref = store.ref
	}
	binding := uci.IndexBinding{
		Scope:     uci.IndexScope{SourceID: ref.SourceID, CheckoutID: ref.CheckoutID, IncarnationID: "55555555-5555-4555-8555-555555555555"},
		ProfileID: ref.AnalysisProfileID, LocalRootID: "status-owner-root", WorkstationID: store.workstation,
	}
	if !store.unpublished.Load() {
		binding.Context = &ref
	}
	return binding, nil
}

func (store *plainStatusAuthorityStore) AuthorizeContext(_ context.Context, access uci.ContextAccess) error {
	if !store.allowed.Load() || access.AuthRealm != uci.NoAuthCodeRealm || access.Principal != uci.NoAuthCodePrincipal || access.SourceID != store.ref.SourceID || access.CheckoutID != store.ref.CheckoutID {
		return errors.New("read scope denied")
	}
	return nil
}

func (store *plainStatusAuthorityStore) ListAuthorizedContexts(_ context.Context, realm, principal string, _ int) ([]uci.ContextRef, error) {
	if !store.allowed.Load() || realm != uci.NoAuthCodeRealm || principal != uci.NoAuthCodePrincipal {
		return nil, errors.New("read scope denied")
	}
	return []uci.ContextRef{store.ref}, nil
}

func (store *plainStatusAuthorityStore) LoadContextMetadata(_ context.Context, ref uci.ContextRef) (uci.ContextMetadata, error) {
	if ref != store.ref {
		return uci.ContextMetadata{}, errors.New("unknown View")
	}
	return uci.ContextMetadata{Source: "status source", Checkout: "status checkout", View: "historical snapshot"}, nil
}

func (store *plainStatusAuthorityStore) LoadIndexStatus(_ context.Context, authorized uci.AuthorizedContext, _ *uci.VectorProfile) (uci.IndexStatusSnapshot, error) {
	if authorized.Ref() != store.ref {
		return uci.IndexStatusSnapshot{}, errors.New("wrong status View")
	}
	stamp := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	return uci.IndexStatusSnapshot{
		Context: store.ref, SourceState: uci.IndexStatusSourceActive, CheckoutState: uci.IndexStatusCheckoutRegistered,
		ViewState: uci.IndexStatusViewSuperseded, CurrentViewRelation: uci.IndexStatusCurrentViewDifferent,
		PublishedAt: stamp, ScanStartedAt: stamp, ScanCompletedAt: stamp, ChunkCount: 3,
		Coverage:  uci.IndexCoverage{Structural: uci.IndexCoverageComplete, Lexical: uci.IndexCoveragePartial, Vector: uci.IndexCoverageUnavailable},
		Embedding: uci.EmbeddingStatus{Coverage: uci.IndexCoverageUnavailable},
		Freshness: uci.QueryFreshness{State: uci.QueryFreshnessHistorical, Method: uci.QueryFreshnessPinnedHistory, EnrichmentWatermark: uci.QueryEnrichmentWatermark{State: uci.QueryEnrichmentCurrent}},
	}, nil
}

type plainStatusMCPHandler struct{ server *mcp.Server }

func (handler plainStatusMCPHandler) HandleToolCall(ctx context.Context, name string, args []byte) ([]byte, bool, error) {
	params, err := json.Marshal(map[string]any{"name": name, "arguments": json.RawMessage(args)})
	if err != nil {
		return nil, false, err
	}
	response := handler.server.HandleRequest(ctx, &mcp.Request{JSONRPC: "2.0", ID: float64(1), Method: "tools/call", Params: params})
	if response.Error != nil {
		body, err := json.Marshal(response.Error)
		return body, true, err
	}
	body, err := json.Marshal(response.Result)
	return body, false, err
}

func (handler plainStatusMCPHandler) ToolDefinitions() []grpcserver.ToolDef {
	tools := handler.server.ListTools()
	definitions := make([]grpcserver.ToolDef, len(tools))
	for index, tool := range tools {
		schema, _ := json.Marshal(tool.InputSchema)
		definitions[index] = grpcserver.ToolDef{Name: tool.Name, Description: tool.Description, InputSchemaJSON: schema}
	}
	return definitions
}

func (handler plainStatusMCPHandler) ServerInfo() (string, string) {
	return "engram", handler.server.Version()
}
