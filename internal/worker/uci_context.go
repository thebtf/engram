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
	"google.golang.org/grpc/metadata"
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
	indexTargets       *grpcserver.IndexIntentTargetRegistry
	handlePort         *mcp.UCIContextHandlePort
	aliasResolver      *uci.AliasResolver
	exposureRecorder   *uci.ExposureRecorder
	transport          grpcserver.UCITransport
	indexIntentStore   *gormstore.UCIIndexIntentStore
}

type operatorCodeServerContextStore interface {
	GetSource(context.Context, string) (*gormstore.UCISource, error)
	GetCheckout(context.Context, string) (*gormstore.UCICheckout, error)
}

type operatorCodeServerResolver interface {
	Authorize(context.Context, uci.ResolveContextInput) (uci.AuthorizedContext, error)
}

type operatorCodeIndexIntentContextStore interface {
	GetCheckout(context.Context, string) (*gormstore.UCICheckout, error)
	LoadIndexBinding(context.Context, uci.IndexBindingSelector) (uci.IndexBinding, error)
}

// operatorCodeIndexIntentComposition adds the durable index-intent capability
// without widening the pre-existing UCI application surface.
type operatorCodeIndexIntentComposition struct {
	*UCIApplication
	indexIntentStore *gormstore.UCIIndexIntentStore
	contextStore     operatorCodeIndexIntentContextStore
}

type operatorCodeIndexIntentBinding struct {
	scope        uci.IndexScope
	profileID    string
	previousView *uci.ContextRef
}

type operatorCodeIndexIntentRetryAuthorizer struct {
	binding operatorCodeIndexIntentBinding
}

var (
	_ operatorCodeApplication                  = (*operatorCodeIndexIntentComposition)(nil)
	_ operatorCodeIndexIntentApplication       = (*operatorCodeIndexIntentComposition)(nil)
	_ operatorCodeNoViewIndexIntentApplication = (*operatorCodeIndexIntentComposition)(nil)
	_ uci.IndexIntentRetryAuthorizer           = (*operatorCodeIndexIntentRetryAuthorizer)(nil)
)

func (application *operatorCodeIndexIntentComposition) SubmitIndexIntent(ctx context.Context, authorized uci.AuthorizedContext, requestRef string, kind uci.IndexIntentKind) (uci.IndexIntent, error) {
	binding, err := application.indexIntentBinding(ctx, authorized)
	if err != nil {
		return uci.IndexIntent{}, err
	}
	return application.submitIndexIntent(ctx, binding, requestRef, kind)
}

func (application *operatorCodeIndexIntentComposition) GetIndexIntent(ctx context.Context, authorized uci.AuthorizedContext, intentRef string) (uci.IndexIntent, error) {
	binding, err := application.indexIntentBinding(ctx, authorized)
	if err != nil {
		return uci.IndexIntent{}, err
	}
	return application.getIndexIntent(ctx, binding, intentRef)
}

func (application *operatorCodeIndexIntentComposition) RetryIndexIntent(ctx context.Context, authorized uci.AuthorizedContext, intentRef string) (uci.IndexIntent, error) {
	binding, err := application.indexIntentBinding(ctx, authorized)
	if err != nil {
		return uci.IndexIntent{}, err
	}
	return application.retryIndexIntent(ctx, binding, intentRef)
}

func (application *operatorCodeIndexIntentComposition) getIndexIntent(ctx context.Context, binding operatorCodeIndexIntentBinding, intentRef string) (uci.IndexIntent, error) {
	intent, err := application.indexIntentStore.GetIndexIntent(ctx, intentRef)
	if err != nil {
		return uci.IndexIntent{}, err
	}
	if !binding.matches(intent) {
		return uci.IndexIntent{}, uci.ErrIndexIntentBindingMismatch
	}
	return intent, nil
}

func (application *operatorCodeIndexIntentComposition) indexIntentBinding(ctx context.Context, authorized uci.AuthorizedContext) (operatorCodeIndexIntentBinding, error) {
	if application == nil || application.indexIntentStore == nil || application.contextStore == nil {
		return operatorCodeIndexIntentBinding{}, errors.New("operator code index intent composition is not configured")
	}
	ref := authorized.Ref()
	if ref.SpaceID != nil {
		return operatorCodeIndexIntentBinding{}, uci.ErrIndexIntentBindingMismatch
	}
	checkout, err := application.contextStore.GetCheckout(ctx, ref.CheckoutID)
	if err != nil || checkout == nil || checkout.CheckoutID != ref.CheckoutID || checkout.SourceID != ref.SourceID {
		return operatorCodeIndexIntentBinding{}, uci.ErrIndexIntentBindingMismatch
	}
	return application.indexIntentBindingForCheckout(ctx, uci.IndexScope{SourceID: ref.SourceID, CheckoutID: ref.CheckoutID, IncarnationID: checkout.IncarnationID}, ref.AnalysisProfileID, &ref, false)
}

// SubmitNoViewIndexIntent creates the first durable index request for a
// server-reauthorized registered checkout. No View exists at admission.
func (application *operatorCodeIndexIntentComposition) SubmitNoViewIndexIntent(ctx context.Context, scope uci.IndexScope, profileID, requestRef string, kind uci.IndexIntentKind) (uci.IndexIntent, error) {
	binding, err := application.indexIntentBindingForCheckout(ctx, scope, profileID, nil, true)
	if err != nil {
		return uci.IndexIntent{}, err
	}
	return application.submitIndexIntent(ctx, binding, requestRef, kind)
}

// NoViewIndexIntentByRequestRef loads the durable first-index binding before
// the HTTP boundary reauthorizes the current tab, grant, and target tuple.
func (application *operatorCodeIndexIntentComposition) NoViewIndexIntentByRequestRef(ctx context.Context, requestRef string) (uci.IndexIntent, error) {
	if application == nil || application.indexIntentStore == nil {
		return uci.IndexIntent{}, errors.New("operator code index intent composition is not configured")
	}
	intent, err := application.indexIntentStore.GetIndexIntentByRequestRef(ctx, requestRef)
	if err != nil {
		return uci.IndexIntent{}, err
	}
	if intent.PreviousView != nil {
		return uci.IndexIntent{}, uci.ErrIndexIntentBindingMismatch
	}
	return intent, nil
}

// ReplayNoViewIndexIntent returns an existing durable request after the
// current registered checkout has been reauthorized. It cannot admit a new one.
func (application *operatorCodeIndexIntentComposition) ReplayNoViewIndexIntent(ctx context.Context, scope uci.IndexScope, profileID, requestRef string, kind uci.IndexIntentKind) (uci.IndexIntent, error) {
	binding, err := application.indexIntentBindingForCheckout(ctx, scope, profileID, nil, false)
	if err != nil {
		return uci.IndexIntent{}, err
	}
	intent, err := application.NoViewIndexIntentByRequestRef(ctx, requestRef)
	if err != nil {
		return uci.IndexIntent{}, err
	}
	if intent.Kind != kind || !binding.matches(intent) {
		return uci.IndexIntent{}, uci.ErrIndexIntentBindingMismatch
	}
	return intent, nil
}

// NoViewIndexIntentTarget returns only the server-stored scope needed to
// reauthorize a first-index status or retry request before it is released.
func (application *operatorCodeIndexIntentComposition) NoViewIndexIntentTarget(ctx context.Context, intentRef string) (uci.IndexScope, string, error) {
	if application == nil || application.indexIntentStore == nil {
		return uci.IndexScope{}, "", errors.New("operator code index intent composition is not configured")
	}
	intent, err := application.indexIntentStore.GetIndexIntent(ctx, intentRef)
	if err != nil || intent.PreviousView != nil {
		return uci.IndexScope{}, "", uci.ErrIndexIntentBindingMismatch
	}
	return intent.Scope, intent.ProfileID, nil
}

func (application *operatorCodeIndexIntentComposition) GetNoViewIndexIntent(ctx context.Context, scope uci.IndexScope, profileID, intentRef string) (uci.IndexIntent, error) {
	binding, err := application.indexIntentBindingForCheckout(ctx, scope, profileID, nil, false)
	if err != nil {
		return uci.IndexIntent{}, err
	}
	return application.getIndexIntent(ctx, binding, intentRef)
}

func (application *operatorCodeIndexIntentComposition) RetryNoViewIndexIntent(ctx context.Context, scope uci.IndexScope, profileID, intentRef string) (uci.IndexIntent, error) {
	binding, err := application.indexIntentBindingForCheckout(ctx, scope, profileID, nil, false)
	if err != nil {
		return uci.IndexIntent{}, err
	}
	return application.retryIndexIntent(ctx, binding, intentRef)
}

func (application *operatorCodeIndexIntentComposition) submitIndexIntent(ctx context.Context, binding operatorCodeIndexIntentBinding, requestRef string, kind uci.IndexIntentKind) (uci.IndexIntent, error) {
	intent, err := application.indexIntentStore.SubmitIndexIntent(ctx, uci.IndexIntentInput{
		RequestRef: requestRef, Kind: kind, Scope: binding.scope, ProfileID: binding.profileID, PreviousView: binding.previousView,
	})
	if err != nil || intent.State != uci.IndexIntentSubmitted {
		return intent, err
	}
	queued, err := application.indexIntentStore.QueueIndexIntent(ctx, intent.ID)
	if errors.Is(err, uci.ErrIndexIntentInvalidTransition) {
		return application.getIndexIntent(ctx, binding, intent.ID)
	}
	return queued, err
}

func (application *operatorCodeIndexIntentComposition) retryIndexIntent(ctx context.Context, binding operatorCodeIndexIntentBinding, intentRef string) (uci.IndexIntent, error) {
	if application == nil || application.indexIntentStore == nil {
		return uci.IndexIntent{}, errors.New("operator code index intent composition is not configured")
	}
	return application.indexIntentStore.RetryIndexIntent(ctx, intentRef, &operatorCodeIndexIntentRetryAuthorizer{binding: binding})
}

func (application *operatorCodeIndexIntentComposition) indexIntentBindingForCheckout(ctx context.Context, scope uci.IndexScope, profileID string, previous *uci.ContextRef, requireNoView bool) (operatorCodeIndexIntentBinding, error) {
	if application == nil || application.indexIntentStore == nil || application.contextStore == nil {
		return operatorCodeIndexIntentBinding{}, errors.New("operator code index intent composition is not configured")
	}
	selector, err := uci.CheckoutIndexBindingSelector(uci.RegisteredCheckoutSelector{Scope: scope, ProfileID: profileID})
	if err != nil {
		return operatorCodeIndexIntentBinding{}, uci.ErrIndexIntentBindingMismatch
	}
	current, err := application.contextStore.LoadIndexBinding(ctx, selector)
	if err != nil || current.Scope != scope || current.ProfileID != profileID || (requireNoView && current.Context != nil) {
		return operatorCodeIndexIntentBinding{}, uci.ErrIndexIntentBindingMismatch
	}
	if previous != nil && (current.Context == nil || !operatorCodeIndexIntentContextsEqual(*current.Context, *previous)) {
		return operatorCodeIndexIntentBinding{}, uci.ErrIndexIntentBindingMismatch
	}
	var previousCopy *uci.ContextRef
	if previous != nil {
		copy := previous.Clone()
		previousCopy = &copy
	}
	return operatorCodeIndexIntentBinding{scope: scope, profileID: profileID, previousView: previousCopy}, nil
}

func (binding operatorCodeIndexIntentBinding) matches(intent uci.IndexIntent) bool {
	if intent.Scope != binding.scope || intent.ProfileID != binding.profileID {
		return false
	}
	if binding.previousView == nil {
		return intent.PreviousView == nil
	}
	return intent.PreviousView != nil &&
		intent.PreviousView.SourceID == binding.previousView.SourceID &&
		intent.PreviousView.CheckoutID == binding.previousView.CheckoutID &&
		intent.PreviousView.AnalysisProfileID == binding.previousView.AnalysisProfileID
}

func (authorizer *operatorCodeIndexIntentRetryAuthorizer) AuthorizeIndexIntentRetry(_ context.Context, intent uci.IndexIntent) error {
	if authorizer == nil || !authorizer.binding.matches(intent) {
		return uci.ErrIndexIntentBindingMismatch
	}
	return nil
}

func operatorCodeIndexIntentContextsEqual(left, right uci.ContextRef) bool {
	return left.SpaceID == nil && right.SpaceID == nil &&
		left.SourceID == right.SourceID &&
		left.CheckoutID == right.CheckoutID &&
		left.ViewID == right.ViewID &&
		left.AnalysisProfileID == right.AnalysisProfileID &&
		left.Generation == right.Generation
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

func composeOperatorCodeHTTPAdapter(db *gormlib.DB, composition *uciContextComposition) (*OperatorCodeHTTPAdapter, error) {
	if db == nil || composition == nil || composition.contextStore == nil || composition.resolver == nil || composition.application == nil || composition.exposureRecorder == nil || composition.indexIntentStore == nil || composition.indexTargets == nil {
		return nil, errors.New("operator code HTTP composition requires UCI context dependencies")
	}
	grants := NewCodeGrantApplication(gormstore.NewBrowserReadGrantStore(db))
	adapter := NewOperatorCodeHTTPAdapter(
		grants,
		NewBrowserBindingApplication(gormstore.NewBrowserTabBindingStore(db)),
		newOperatorCodeServerAuthorizer(composition.contextStore, composition.resolver),
		&operatorCodeIndexIntentComposition{
			UCIApplication: composition.application, indexIntentStore: composition.indexIntentStore, contextStore: composition.contextStore,
		},
		composition.exposureRecorder,
	)
	adapter.onboarding = grants
	adapter.contexts = gormstore.NewBrowserCodeContextStore(db)
	adapter.indexTargets = composition.indexTargets
	adapter.graphSources = composition.projectionStore
	return adapter, nil
}

const (
	operatorCollectionSelectionDomain = "rules"
	documentSelectionDomain           = "documents"
)

// operatorCollectionScopeAuthority owns only selection identity. Domain actions
// remain responsible for their own authorization and state/version checks.
type operatorCollectionScopeAuthority struct{}

func (operatorCollectionScopeAuthority) ResolveOperatorCollectionScope(_ context.Context, identity auth.Identity, sessionID, domain string) (gormstore.CollectionSelectionScope, error) {
	subject, ok := identity.SessionBrowserSubject()
	if !ok || !operatorCodeText(sessionID) {
		return gormstore.CollectionSelectionScope{}, errors.New("operator collection scope denied")
	}
	switch domain {
	case operatorCollectionSelectionDomain, queueCandidateSelectionDomain, documentSelectionDomain:
	default:
		return gormstore.CollectionSelectionScope{}, errors.New("operator collection scope denied")
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("operator-collection-scope/v1\x00%d\x00%s\x00%s", subject.UserID, sessionID, domain)))
	return gormstore.CollectionSelectionScope{
		SubjectUserID:      subject.UserID,
		SessionID:          sessionID,
		Domain:             domain,
		ContextFingerprint: "sha256:" + hex.EncodeToString(digest[:]),
		AuthorizationEpoch: 1,
		CollectionVersion:  1,
	}, nil
}

func composeOperatorCollectionHTTPAdapter(db *gormlib.DB, queueEnabled bool) (*OperatorCollectionHTTPAdapter, error) {
	if db == nil {
		return nil, errors.New("operator collection HTTP composition requires a database")
	}
	rules := gormstore.NewBehavioralRulesStoreFromDB(db)
	providers := operatorCollectionProviderRouter{operatorCollectionSelectionDomain: rules}
	if queueEnabled {
		providers[queueCandidateSelectionDomain] = newQueueCandidateCollectionProvider(gormstore.NewCandidateStore(db, nil))
	}
	return NewOperatorCollectionHTTPAdapter(
		gormstore.NewCollectionSelectionStore(db),
		operatorCollectionScopeAuthority{},
		providers,
		providers,
		providers,
	), nil
}

func composeQueueCandidateSelectionHandler(service *Service, db *gormlib.DB) (*QueueCandidateSelectionHandler, error) {
	if service == nil || db == nil {
		return nil, errors.New("queue candidate selection composition requires a service and database")
	}
	return NewQueueCandidateSelectionHandler(service, gormstore.NewCollectionSelectionStore(db), operatorCollectionScopeAuthority{}), nil
}

func verifiedUCIParserBundle(ctx context.Context) (bool, error) {
	incoming, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false, nil
	}
	values := incoming.Get("x-engram-verified-parser-bundle")
	if len(values) == 0 {
		return false, nil
	}
	if len(values) != 1 || values[0] != string(uci.TreeSitterBundleDigest()) {
		return false, uci.NewContextError(uci.ContextMismatch, nil)
	}
	return true, nil
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
	contextApplication.SetLocalGitRegistration(func(ctx context.Context, caller uci.ResolveContextInput, sourceID, label, locator string, parserBundle *bool) (uci.RegisteredCheckoutSelector, error) {
		available, err := verifiedUCIParserBundle(ctx)
		if err != nil {
			return uci.RegisteredCheckoutSelector{}, err
		}
		if parserBundle != nil && *parserBundle && !available {
			return uci.RegisteredCheckoutSelector{}, uci.NewContextError(uci.ContextMismatch, nil)
		}
		registered, err := contextStore.RegisterLocalGit(ctx, gormstore.RegisterLocalGitInput{
			AuthRealm: caller.AuthRealm, Principal: caller.Principal, WorkstationID: caller.WorkstationID,
			SourceID: sourceID, SourceLabel: label, Locator: locator, ParserBundle: parserBundle,
			DefaultParserBundle: available,
		})
		if err != nil {
			return uci.RegisteredCheckoutSelector{}, err
		}
		return uci.RegisteredCheckoutSelector{
			Scope:     uci.IndexScope{SourceID: registered.SourceID, CheckoutID: registered.CheckoutID, IncarnationID: registered.IncarnationID},
			ProfileID: registered.ProfileID,
		}, nil
	})

	projectionStore := gormstore.NewUCIProjectionStore(db)
	indexIntentStore := gormstore.NewUCIIndexIntentStore(db)
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
	indexTargets := grpcserver.NewIndexIntentTargetRegistry()
	runtime, err := grpcserver.NewContextAwareUCIRuntimeWithIndexTargets(contextStore, projectionStore, publisher, indexIntentStore, indexTargets)
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
	semanticService := uci.NewSemanticService(semantic.profile, semantic.embedder, projectionStore, projectionStore, projectionStore)
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
		indexTargets:       indexTargets,
		handlePort:         handlePort,
		aliasResolver:      aliasResolver,
		exposureRecorder:   exposureRecorder,
		transport:          transport,
		indexIntentStore:   indexIntentStore,
	}, nil
}
