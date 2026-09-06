package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
	gormlib "gorm.io/gorm"
)

const (
	uciSemanticDefaultEmbeddingModel  = "text-embedding"
	uciSemanticPreprocessingRevision  = "uci-semantic-preprocess/identity-v1"
	uciSemanticProviderUnavailableRef = "uci-semantic-provider-unavailable"
)

// uciSemanticConfig binds the one profile-scoped semantic service constructed
// with the UCI projection store during worker initialization.
type uciSemanticConfig struct {
	profile  uci.VectorProfile
	embedder uci.SemanticEmbedder
}

func newUCISemanticConfig(
	ctx context.Context,
	resolver embedding.SettingsResolver,
	profileClient *embedding.Client,
	vectorClient *embedding.Client,
) uciSemanticConfig {
	config := uciSemanticConfig{
		profile: newUCISemanticProfile(ctx, resolver, profileClient, uciSemanticPreprocessingRevision),
	}
	if vectorClient != nil {
		config.embedder = vectorClient
	}
	return config
}

func newUCISemanticProfile(
	ctx context.Context,
	resolver embedding.SettingsResolver,
	client *embedding.Client,
	preprocessingRevision string,
) uci.VectorProfile {
	endpoint := resolveUCIEmbeddingSetting(ctx, resolver, "ENGRAM_EMBEDDING_URL", embedding.SettingKeyEmbedURL)
	model := resolveUCIEmbeddingSetting(ctx, resolver, "ENGRAM_EMBEDDING_MODEL", embedding.SettingKeyEmbedModel)
	if client != nil {
		model = client.Model()
	}
	if model == "" {
		model = uciSemanticDefaultEmbeddingModel
	}
	return uci.VectorProfile{
		ProviderRef:           uciSemanticProviderRef(endpoint),
		Model:                 model,
		Dimension:             embedding.EmbeddingDim,
		PreprocessingRevision: preprocessingRevision,
		IncludeRelativePath:   true,
	}
}

// resolveUCIEmbeddingSetting mirrors embedding.NewClientWithSettings's
// env-first precedence without reading the API-key setting.
func resolveUCIEmbeddingSetting(ctx context.Context, resolver embedding.SettingsResolver, envKey, settingKey string) string {
	if value := os.Getenv(envKey); value != "" {
		return value
	}
	if resolver != nil {
		if value, ok := resolver.Get(ctx, settingKey); ok {
			return value
		}
	}
	return ""
}

// uciSemanticProviderRef keeps the configured endpoint process-local. Only its
// opaque digest participates in the persisted semantic-profile identity.
func uciSemanticProviderRef(endpoint string) string {
	if endpoint == "" {
		return uciSemanticProviderUnavailableRef
	}
	digest := sha256.Sum256([]byte(endpoint))
	return "sha256:" + hex.EncodeToString(digest[:])
}

// uciContextComposition holds the one shared context authority used by MCP and
// private UCI gRPC calls for the lifetime of a worker.
type uciContextComposition struct {
	contextStore       *gormstore.UCIContextStore
	authorizer         *gormstore.UCIContextAuthorizer
	resolver           *uci.ContextResolver
	contextApplication *mcp.UCIContextApplication
	application        *UCIApplication
	projectionStore    *gormstore.UCIProjectionStore
	runtime            grpcserver.ContextAwareUCIRuntime
	handlePort         *mcp.UCIContextHandlePort
	aliasResolver      *uci.AliasResolver
	exposureRecorder   *uci.ExposureRecorder
	transport          grpcserver.UCITransport
}

// composeUCIContext creates and installs the narrow UCI context capability.
// The disabled path returns before it allocates or installs any UCI dependency.
func composeUCIContext(
	enabled bool,
	db *gormlib.DB,
	mcpServer *mcp.Server,
	semantic uciSemanticConfig,
) (*uciContextComposition, error) {
	if !enabled {
		return nil, nil
	}
	if db == nil {
		return nil, errors.New("UCI context composition requires a database")
	}
	if mcpServer == nil {
		return nil, errors.New("UCI context composition requires an MCP server")
	}

	contextStore := gormstore.NewUCIContextStore(db)
	authorizer := gormstore.NewUCIContextAuthorizer(contextStore)
	resolver := uci.NewContextResolver(contextStore, authorizer, contextStore)
	contextApplication, err := mcp.NewUCIContextApplication(resolver, contextStore)
	if err != nil {
		return nil, fmt.Errorf("create UCI MCP context application: %w", err)
	}

	projectionStore := gormstore.NewUCIProjectionStore(db)
	publisher, err := projectionStore.Publisher(authorizer, uci.DefaultIndexPublicationLimits())
	if err != nil {
		return nil, fmt.Errorf("create UCI index publisher: %w", err)
	}
	runtime, err := grpcserver.NewContextAwareUCIRuntime(contextStore, projectionStore, publisher)
	if err != nil {
		return nil, fmt.Errorf("create UCI runtime: %w", err)
	}
	handlePort, err := mcp.NewUCIContextHandlePort(mcpServer, contextStore, authorizer)
	if err != nil {
		return nil, fmt.Errorf("create UCI context handle port: %w", err)
	}
	aliasResolver := uci.NewAliasResolver(contextStore.LookupLegacyAliasRecords)
	queryService := uci.NewQueryService(projectionStore)
	graphService := uci.NewGraphService(projectionStore)
	versionedReadService := uci.NewVersionedReadService(projectionStore)
	indexStatusService := uci.NewIndexStatusService(projectionStore)
	semanticService := uci.NewSemanticService(semantic.profile, semantic.embedder, projectionStore, projectionStore)
	application, err := NewUCIApplication(
		contextApplication,
		aliasResolver,
		queryService,
		semanticService,
		graphService,
		versionedReadService,
		indexStatusService,
	)
	if err != nil {
		return nil, fmt.Errorf("create UCI application: %w", err)
	}
	exposureRecorder := uci.NewExposureRecorder(gormstore.NewUCIExposureStore(db), nil)
	transport := grpcserver.NewContextAwareUCITransport(resolver, aliasResolver, runtime, handlePort)

	mcpServer.SetCodebaseContextApplication(application)
	mcpServer.SetUCIExposureRecorder(exposureRecorder)

	return &uciContextComposition{
		contextStore:       contextStore,
		authorizer:         authorizer,
		resolver:           resolver,
		contextApplication: contextApplication,
		application:        application,
		projectionStore:    projectionStore,
		runtime:            runtime,
		handlePort:         handlePort,
		aliasResolver:      aliasResolver,
		exposureRecorder:   exposureRecorder,
		transport:          transport,
	}, nil
}
