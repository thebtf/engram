package worker

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/metadata"
	gormdriver "gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestComposeUCIContextDisabledKeepsMCPToolsDark(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	mcpServer := mcp.NewServer(mcp.ServerOptions{Version: "uci-context-disabled"})

	composition, err := composeUCIContext(false, nil, mcpServer, workerUCISemanticConfig())

	require.NoError(t, err)
	require.Nil(t, composition)
	for _, name := range []string{"codebase_context", "codebase_search", "codebase_read", "codebase_graph", "codebase_status"} {
		require.Falsef(t, workerUCIHasMCPTool(mcpServer, name), "disabled composition advertised %q", name)
	}
}

func TestComposeUCIContextEnabledRejectsMissingDependencies(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	mcpServer := mcp.NewServer(mcp.ServerOptions{Version: "uci-context-missing-dependency"})

	composition, err := composeUCIContext(true, nil, mcpServer, workerUCISemanticConfig())

	require.Nil(t, composition)
	require.ErrorContains(t, err, "requires a database")
	require.False(t, workerUCIHasMCPTool(mcpServer, "codebase_context"))
}

func TestComposeUCIContextEnabledInstallsCompositeViewCapabilities(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	mcpServer := mcp.NewServer(mcp.ServerOptions{Version: "uci-context-enabled"})

	// This exercises constructor composition only; no store operation runs against
	// the zero-value DB in this test.
	composition, err := composeUCIContext(true, &gormlib.DB{}, mcpServer, workerUCISemanticConfig())

	require.NoError(t, err)
	require.NotNil(t, composition)
	require.Nil(t, composition.embeddingProfile)
	require.Nil(t, composition.embeddingWorker)
	require.NotNil(t, composition.application.semanticService)
	for _, name := range []string{"codebase_context", "codebase_search", "codebase_read", "codebase_graph"} {
		require.Truef(t, workerUCIHasMCPTool(mcpServer, name), "composite composition did not advertise %q", name)
	}

	query, err := composition.runtime.QueryCode(context.Background(), uci.AuthorizedContext{}, nil)
	require.Nil(t, query)
	require.ErrorContains(t, err, "recorder-owned response is unavailable")
	explore, err := composition.runtime.ExploreCode(context.Background(), uci.AuthorizedContext{}, nil)
	require.Nil(t, explore)
	require.ErrorContains(t, err, "recorder-owned response is unavailable")
}

func TestComposeUCIContextSharesMCPCheckoutHandleWithPrivateGRPC(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	store := openWorkerUCIContextCompositionStore(t)
	mcpServer := mcp.NewServer(mcp.ServerOptions{Version: "uci-context-integration"})
	composition, err := composeUCIContext(true, store.GetDB(), mcpServer, workerUCISemanticConfig())
	require.NoError(t, err)

	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	const realm = "client"
	principal := "agent/worker-uci-" + token
	workstationID := "worker-uci-" + token
	ctx := context.Background()
	source, err := composition.contextStore.CreateSource(ctx, gormstore.CreateSourceInput{
		AuthRealm:   realm,
		Kind:        gormstore.UCISourceGit,
		DisplayName: "worker-uci-source-" + token,
	})
	require.NoError(t, err)
	checkout, err := composition.contextStore.RegisterCheckout(ctx, gormstore.RegisterCheckoutInput{
		SourceID:       source.SourceID,
		WorkstationID:  workstationID,
		Kind:           gormstore.UCICheckoutWorkingTree,
		OwnerPrincipal: principal,
		LocatorRef:     "file:///worker-uci/" + token,
	})
	require.NoError(t, err)
	profile, err := composition.contextStore.CreateProfile(ctx, gormstore.CreateProfileInput{
		ParserBundleDigest:   workerUCIContextDigest("a"),
		ResolverRevision:     "worker-uci-resolver-" + token,
		ChunkerRevision:      "worker-uci-chunker-" + token,
		IgnorePolicyDigest:   workerUCIContextDigest("b"),
		BuildContextJSON:     `{"fixture":"worker-uci"}`,
		SecretPolicyRevision: "worker-uci-secret-policy-" + token,
	})
	require.NoError(t, err)

	const clientSessionID = "worker-uci-client-session"
	identity := auth.ClientWithPrincipal("read-write", workstationID, principal, auth.PrincipalKindAgent)
	mcpContext := auth.WithIdentity(mcp.ContextWithSession(context.Background(), clientSessionID), identity)
	handle := workerUCISelectCheckout(t, mcpServer, mcpContext, source.SourceID, checkout, profile.ProfileID)

	_, grpcInternal := grpcserver.New(nil, nil)
	grpcInternal.SetUCITransport(composition.transport)
	grpcContext := auth.WithIdentity(context.Background(), identity)
	grpcContext = metadata.NewIncomingContext(grpcContext, metadata.Pairs(auditcontext.SourceSessionMetadataKey, clientSessionID))
	bound, err := grpcInternal.BindCodeContext(grpcContext, &pb.BindCodeContextRequest{
		ClientSessionId: clientSessionID,
		ContextHandle:   handle,
	})
	require.NoError(t, err)
	require.Equal(t, handle, bound.GetContextHandle())
	require.Nil(t, bound.GetContext(), "registered checkout selection must stay View-free before publication")
	require.NotNil(t, bound.GetIndexScope())
	require.Equal(t, source.SourceID, bound.GetIndexScope().GetSourceId())
	require.Equal(t, checkout.CheckoutID, bound.GetIndexScope().GetCheckoutId())
	require.Equal(t, checkout.IncarnationID, bound.GetIndexScope().GetIncarnationId())
	require.Equal(t, profile.ProfileID, bound.GetIndexScope().GetAnalysisProfileId())
	require.Equal(t, checkout.LocatorRef, bound.GetLocalRootId())
	require.Equal(t, workstationID, bound.GetWorkstationId())
}

func TestUCISemanticProfileUsesOpaqueCacheIdentity(t *testing.T) {
	ctx := context.Background()
	const (
		endpointA = "https://vectors-a.example.test/v1"
		endpointB = "https://vectors-b.example.test/v1"
		modelA    = "settings-model-a"
		modelB    = "settings-model-b"
		apiKey    = "uci-profile-api-key-secret"
	)
	t.Setenv("ENGRAM_EMBEDDING_URL", "")
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "")
	t.Setenv("ENGRAM_EMBEDDING_API_KEY", apiKey)

	disabled := newUCISemanticProfile(ctx, nil, nil, uciSemanticPreprocessingRevision)
	require.Equal(t, uciSemanticProviderUnavailableRef, disabled.ProviderRef)
	require.Equal(t, uciSemanticDefaultEmbeddingModel, disabled.Model)
	require.Equal(t, embedding.EmbeddingDim, disabled.Dimension)
	require.Equal(t, uciSemanticPreprocessingRevision, disabled.PreprocessingRevision)
	require.True(t, disabled.IncludeRelativePath)

	settings := workerUCIEmbeddingSettings{
		embedding.SettingKeyEmbedURL:   endpointA,
		embedding.SettingKeyEmbedModel: modelA,
	}
	profile := newUCISemanticProfile(ctx, settings, nil, uciSemanticPreprocessingRevision)
	require.Equal(t, modelA, profile.Model)
	require.Equal(t, embedding.EmbeddingDim, profile.Dimension)
	require.Equal(t, uciSemanticPreprocessingRevision, profile.PreprocessingRevision)
	require.True(t, profile.IncludeRelativePath)
	encoded, err := json.Marshal(profile)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), endpointA)
	require.NotContains(t, string(encoded), apiKey)

	endpointChanged := newUCISemanticProfile(ctx, workerUCIEmbeddingSettings{
		embedding.SettingKeyEmbedURL:   endpointB,
		embedding.SettingKeyEmbedModel: modelA,
	}, nil, uciSemanticPreprocessingRevision)
	modelChanged := newUCISemanticProfile(ctx, workerUCIEmbeddingSettings{
		embedding.SettingKeyEmbedURL:   endpointA,
		embedding.SettingKeyEmbedModel: modelB,
	}, nil, uciSemanticPreprocessingRevision)
	preprocessingChanged := newUCISemanticProfile(ctx, settings, nil, "uci-semantic-preprocess/identity-v2")
	require.NotEqual(t, profile, endpointChanged)
	require.NotEqual(t, profile, modelChanged)
	require.NotEqual(t, profile, preprocessingChanged)

	t.Setenv("ENGRAM_EMBEDDING_URL", endpointB)
	t.Setenv("ENGRAM_EMBEDDING_MODEL", modelB)
	envProfile := newUCISemanticProfile(ctx, settings, nil, uciSemanticPreprocessingRevision)
	require.Equal(t, modelB, envProfile.Model)
	require.NotEqual(t, profile.ProviderRef, envProfile.ProviderRef)

	sharedClient, err := embedding.NewClientWithSettings(ctx, settings)
	require.NoError(t, err)
	clientProfile := newUCISemanticProfile(ctx, settings, sharedClient, uciSemanticPreprocessingRevision)
	require.Equal(t, sharedClient.Model(), clientProfile.Model)
	disabledConfig := newUCISemanticConfig(ctx, settings, sharedClient, nil)
	require.Nil(t, disabledConfig.profilePtr)
	require.Nil(t, disabledConfig.embedder)
	configuredConfig := newUCISemanticConfig(ctx, settings, sharedClient, sharedClient)
	require.NotNil(t, configuredConfig.profilePtr)
	require.Equal(t, configuredConfig.profile, *configuredConfig.profilePtr)
}

type workerUCIEmbeddingWorkerFake struct {
	started chan string
	stopped chan struct{}
}

func (worker *workerUCIEmbeddingWorkerFake) Run(ctx context.Context, owner string) error {
	worker.started <- owner
	<-ctx.Done()
	close(worker.stopped)
	return nil
}

func TestServiceUCIEmbeddingWorkerJoinsOnShutdown(t *testing.T) {
	rootCtx, cancel := context.WithCancel(context.Background())
	worker := &workerUCIEmbeddingWorkerFake{started: make(chan string, 1), stopped: make(chan struct{})}
	service := &Service{ctx: rootCtx, cancel: cancel}
	service.startUCIEmbeddingWorker(worker)

	select {
	case owner := <-worker.started:
		_, err := uuid.Parse(owner)
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("UCI embedding worker did not start")
	}

	require.NoError(t, service.Shutdown(context.Background()))
	select {
	case <-worker.stopped:
	case <-time.After(time.Second):
		t.Fatal("Service shutdown did not join the UCI embedding worker")
	}
}

func workerUCIHasMCPTool(server *mcp.Server, name string) bool {
	for _, tool := range server.ListTools() {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func workerUCISelectCheckout(t *testing.T, server *mcp.Server, ctx context.Context, sourceID string, checkout *gormstore.UCICheckout, profileID string) string {
	t.Helper()
	params, err := json.Marshal(map[string]any{
		"name": "codebase_context",
		"arguments": map[string]any{
			"action": "select",
			"checkout": map[string]any{
				"source_id":           sourceID,
				"checkout_id":         checkout.CheckoutID,
				"incarnation_id":      checkout.IncarnationID,
				"analysis_profile_id": profileID,
			},
		},
	})
	require.NoError(t, err)
	response := server.HandleRequest(ctx, &mcp.Request{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})
	require.NotNil(t, response)
	require.Nil(t, response.Error)
	result, ok := response.Result.(map[string]any)
	require.True(t, ok)
	content, ok := result["content"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	text, ok := content[0]["text"].(string)
	require.True(t, ok)
	var payload struct {
		ContextHandle string `json:"context_handle"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.NotEmpty(t, payload.ContextHandle)
	return payload.ContextHandle
}

func workerUCIContextDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func workerUCISemanticConfig() uciSemanticConfig {
	return newUCISemanticConfig(context.Background(), nil, nil, nil)
}

type workerUCIEmbeddingSettings map[string]string

func (settings workerUCIEmbeddingSettings) Get(_ context.Context, key string) (string, bool) {
	value, ok := settings[key]
	return value, ok
}

func openWorkerUCIContextCompositionStore(t *testing.T) *gormstore.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping UCI worker composition PostgreSQL integration test")
	}

	adminDB, err := gormlib.Open(gormdriver.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	adminSQL, err := adminDB.DB()
	require.NoError(t, err)
	schema := "worker_uci_context_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, adminDB.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() {
		if err := adminDB.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop UCI worker composition schema %s: %v", schema, err)
		}
		if err := adminSQL.Close(); err != nil {
			t.Errorf("close UCI worker composition admin pool: %v", err)
		}
	})

	parsedDSN, err := url.Parse(dsn)
	require.NoError(t, err)
	require.NotEmpty(t, parsedDSN.Scheme, "worker UCI tests require a PostgreSQL URL DSN")
	query := parsedDSN.Query()
	query.Set("search_path", schema+", public")
	parsedDSN.RawQuery = query.Encode()
	store, err := gormstore.NewStore(gormstore.Config{DSN: parsedDSN.String(), MaxConns: 2, LogLevel: logger.Silent})
	require.NoError(t, err)
	var actualSchema string
	require.NoError(t, store.GetDB().Raw(`SELECT current_schema()`).Scan(&actualSchema).Error)
	require.Equal(t, schema, actualSchema, "worker UCI test pool must resolve its isolated schema before public")
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close UCI worker composition store: %v", err)
		}
	})
	return store
}
