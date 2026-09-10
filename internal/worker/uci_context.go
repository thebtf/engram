package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/thebtf/engram/internal/auth"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
	gormlib "gorm.io/gorm"
)

const (
	uciSemanticDefaultEmbeddingModel  = "text-embedding"
	uciSemanticPreprocessingRevision  = "uci-semantic-preprocess/chunk-v2"
	uciSemanticProviderUnavailableRef = "uci-semantic-provider-unavailable"
)

// uciSemanticConfig binds the one profile-scoped semantic service constructed
// with the UCI projection store during worker initialization.
type uciSemanticConfig struct {
	profile    uci.VectorProfile
	profilePtr *uci.VectorProfile
	embedder   uci.SemanticEmbedder
}

func newUCISemanticConfig(
	ctx context.Context,
	resolver embedding.SettingsResolver,
	profileClient *embedding.Client,
	vectorClient *embedding.Client,
) uciSemanticConfig {
	profile := newUCISemanticProfile(ctx, resolver, profileClient, uciSemanticPreprocessingRevision)
	config := uciSemanticConfig{profile: profile}
	if vectorClient != nil {
		config.profilePtr = &profile
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
	embeddingProfile   *uci.VectorProfile
	embeddingWorker    uciEmbeddingWorkerRunner
	runtime            grpcserver.ContextAwareUCIRuntime
	handlePort         *mcp.UCIContextHandlePort
	aliasResolver      *uci.AliasResolver
	exposureRecorder   *uci.ExposureRecorder
	transport          grpcserver.UCITransport
}

type operatorCodeServerContextStore interface {
	GetSource(context.Context, string) (*gormstore.UCISource, error)
	GetCheckout(context.Context, string) (*gormstore.UCICheckout, error)
	ListAuthorizedContexts(context.Context, string, string, int) ([]uci.ContextRef, error)
}

type operatorCodeServerResolver interface {
	Authorize(context.Context, uci.ResolveContextInput) (uci.AuthorizedContext, error)
}

// operatorCodeServerAuthorizer derives the realm and owner principal from the
// context registry. The browser caller supplies only a binding-pinned View.
type operatorCodeServerAuthorizer struct {
	contexts operatorCodeServerContextStore
	resolver operatorCodeServerResolver
}

var _ operatorCodeContextAuthorizer = (*operatorCodeServerAuthorizer)(nil)

func newOperatorCodeServerAuthorizer(contexts operatorCodeServerContextStore, resolver operatorCodeServerResolver) *operatorCodeServerAuthorizer {
	return &operatorCodeServerAuthorizer{contexts: contexts, resolver: resolver}
}

func (authorizer *operatorCodeServerAuthorizer) AuthorizeOperatorCode(ctx context.Context, caller operatorCodeVerifiedCaller) (uci.AuthorizedContext, error) {
	if authorizer == nil || authorizer.contexts == nil || authorizer.resolver == nil || !caller.Subject.Valid() || caller.BindingID == "" || caller.Context.SpaceID != nil {
		return uci.AuthorizedContext{}, errors.New("operator code context authorizer is not configured")
	}
	source, err := authorizer.contexts.GetSource(ctx, caller.Context.SourceID)
	if err != nil || source == nil || source.SourceID != caller.Context.SourceID {
		return uci.AuthorizedContext{}, errors.New("operator code source scope is unavailable")
	}
	checkout, err := authorizer.contexts.GetCheckout(ctx, caller.Context.CheckoutID)
	if err != nil || checkout == nil || checkout.CheckoutID != caller.Context.CheckoutID || checkout.SourceID != caller.Context.SourceID {
		return uci.AuthorizedContext{}, errors.New("operator code checkout scope is unavailable")
	}
	ref := caller.Context
	return authorizer.resolver.Authorize(ctx, uci.ResolveContextInput{
		ClientSessionID: "operator-code/" + caller.BindingID,
		AuthRealm:       source.AuthRealm,
		Principal:       checkout.OwnerPrincipal,
		Ref:             &ref,
	})
}

// ResolveCurrentOperatorCode selects the current published View only after the
// grant application established that exactly one active grant exists. Neither
// a browser label nor a browser ContextRef participates in this resolution.
func (authorizer *operatorCodeServerAuthorizer) ResolveCurrentOperatorCode(ctx context.Context, caller operatorCodeBindingCaller, grant gormstore.BrowserReadGrant) (uci.AuthorizedContext, error) {
	if authorizer == nil || authorizer.contexts == nil || !caller.Subject.Valid() || caller.BindingID == "" || grant.SubjectUserID != caller.Subject.UserID {
		return uci.AuthorizedContext{}, errors.New("operator code current context is unavailable")
	}
	source, err := authorizer.contexts.GetSource(ctx, grant.SourceID)
	if err != nil || source == nil || source.SourceID != grant.SourceID || source.AuthRealm != grant.AuthRealm {
		return uci.AuthorizedContext{}, errors.New("operator code current source scope is unavailable")
	}
	checkout, err := authorizer.contexts.GetCheckout(ctx, grant.CheckoutID)
	if err != nil || checkout == nil || checkout.CheckoutID != grant.CheckoutID || checkout.SourceID != grant.SourceID {
		return uci.AuthorizedContext{}, errors.New("operator code current checkout scope is unavailable")
	}
	refs, err := authorizer.contexts.ListAuthorizedContexts(ctx, source.AuthRealm, checkout.OwnerPrincipal, 64)
	if err != nil {
		return uci.AuthorizedContext{}, errors.New("operator code current context list is unavailable")
	}
	var current *uci.ContextRef
	for _, ref := range refs {
		if ref.SpaceID == nil && ref.SourceID == grant.SourceID && ref.CheckoutID == grant.CheckoutID {
			if current != nil {
				return uci.AuthorizedContext{}, errors.New("operator code current context is ambiguous")
			}
			candidate := ref
			current = &candidate
		}
	}
	if current == nil {
		return uci.AuthorizedContext{}, errors.New("operator code current context is unavailable")
	}
	return authorizer.AuthorizeOperatorCode(ctx, operatorCodeVerifiedCaller{
		Subject:   caller.Subject,
		SessionID: caller.SessionID,
		BindingID: caller.BindingID,
		Context:   *current,
	})
}

func composeOperatorCodeHTTPAdapter(db *gormlib.DB, composition *uciContextComposition) (*OperatorCodeHTTPAdapter, error) {
	if db == nil || composition == nil || composition.contextStore == nil || composition.resolver == nil || composition.application == nil || composition.exposureRecorder == nil {
		return nil, errors.New("operator code HTTP composition requires UCI context dependencies")
	}
	return NewOperatorCodeHTTPAdapter(
		NewCodeGrantApplication(gormstore.NewBrowserReadGrantStore(db)),
		NewBrowserBindingApplication(gormstore.NewBrowserTabBindingStore(db)),
		newOperatorCodeServerAuthorizer(composition.contextStore, composition.resolver),
		composition.application,
		composition.exposureRecorder,
	), nil
}

const operatorCollectionSelectionDomain = "rules"

// operatorCollectionScopeAuthority owns only selection identity. Domain actions
// remain responsible for their own authorization and state/version checks.
type operatorCollectionScopeAuthority struct{}

func (operatorCollectionScopeAuthority) ResolveOperatorCollectionScope(_ context.Context, identity auth.Identity, sessionID, domain string) (gormstore.CollectionSelectionScope, error) {
	subject, ok := identity.SessionBrowserSubject()
	if !ok || !operatorCodeText(sessionID) || domain != operatorCollectionSelectionDomain {
		return gormstore.CollectionSelectionScope{}, errors.New("operator collection scope denied")
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("operator-collection-scope/v1\x00%d\x00%s\x00%s", subject.UserID, sessionID, operatorCollectionSelectionDomain)))
	return gormstore.CollectionSelectionScope{
		SubjectUserID:      subject.UserID,
		SessionID:          sessionID,
		Domain:             operatorCollectionSelectionDomain,
		ContextFingerprint: "sha256:" + hex.EncodeToString(digest[:]),
		AuthorizationEpoch: 1,
		CollectionVersion:  1,
	}, nil
}

func composeOperatorCollectionHTTPAdapter(db *gormlib.DB) (*OperatorCollectionHTTPAdapter, error) {
	if db == nil {
		return nil, errors.New("operator collection HTTP composition requires a database")
	}
	return NewOperatorCollectionHTTPAdapter(
		gormstore.NewCollectionSelectionStore(db),
		operatorCollectionScopeAuthority{},
		nil,
		nil,
	), nil
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
	publisher, err := projectionStore.Publisher(authorizer, uci.IndexPublicationConfig{
		Limits:           uci.DefaultIndexPublicationLimits(),
		EmbeddingProfile: semantic.profilePtr,
	})
	if err != nil {
		return nil, fmt.Errorf("create UCI index publisher: %w", err)
	}
	var embeddingWorker uciEmbeddingWorkerRunner
	if semantic.profilePtr != nil && semantic.embedder != nil {
		worker, err := uci.NewEmbeddingWorker(
			*semantic.profilePtr,
			semantic.embedder,
			projectionStore,
			resolver,
			uci.DefaultEmbeddingWorkerLimits(),
		)
		if err != nil {
			return nil, fmt.Errorf("create UCI embedding worker: %w", err)
		}
		embeddingWorker = worker
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
	indexStatusService := uci.NewIndexStatusService(projectionStore, semantic.profilePtr)
	semanticService := uci.NewSemanticService(semantic.profile, semantic.embedder, projectionStore, projectionStore)
	exposureRecorder := uci.NewExposureRecorder(gormstore.NewUCIExposureStore(db), nil)
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
		embeddingProfile:   semantic.profilePtr,
		embeddingWorker:    embeddingWorker,
		runtime:            runtime,
		handlePort:         handlePort,
		aliasResolver:      aliasResolver,
		exposureRecorder:   exposureRecorder,
		transport:          transport,
	}, nil
}
