package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/auth"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
	"github.com/thebtf/engram/internal/worker"
	gormdriver "gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	uciRetrievalSliceSemanticQuery = "zqxv ultracatalog topology"
	uciRetrievalSliceProviderKey   = "uci-retrieval-synthetic-provider-key"
	uciRetrievalSliceOutOfViewPath = "hidden/out_of_view.go"
	uciRetrievalSliceLocator       = "file:///private/uci-retrieval-slice-locator"
)

// TestUCIRetrievalSlice exercises the current retrieval composition against one
// PostgreSQL-published View. Its only non-production collaborator is a local
// HTTP embedding provider: admission, publication, PostgreSQL selection, graph,
// and versioned reads all use their ordinary implementations.
func TestUCIRetrievalSlice(t *testing.T) {
	fixture := newUCIRetrievalSliceFixture(t)
	published := fixture.publishOneView(t)
	authorized := fixture.authorize(t, published.Context)

	exact := fixture.query(t, authorized, uci.QueryModeExactQualifiedSymbol, fixture.catalogEntrySymbol)
	fixture.requireBoundResponse(t, "exact Go symbol", exact)
	if exact.Status != uci.QueryStatusOK || exact.Retrieval == nil || exact.Retrieval.Mode != uci.QueryRetrievalExact {
		t.Fatalf("exact response = %#v, want exact available result", exact)
	}
	exactItem := requireUCIRetrievalSliceItemAtPath(t, exact, "src/widget_catalog.go")
	if !strings.Contains(exactItem.Excerpt, "WidgetService()") {
		t.Fatalf("exact Go excerpt = %q, want the published CatalogEntry source", exactItem.Excerpt)
	}

	for _, test := range []struct {
		name     string
		needle   string
		path     string
		language string
	}{
		{name: "Go FTS", needle: "goretrievalneedle", path: "src/widget_catalog.go", language: "go"},
		{name: "Markdown FTS", needle: "mdretrievalneedle", path: "docs/catalog.md", language: "markdown"},
		{name: "JSON FTS", needle: "jsonretrievalneedle", path: "config/catalog.json", language: "json"},
		{name: "YAML FTS", needle: "yamlretrievalneedle", path: "config/catalog.yaml", language: "yaml"},
		{name: "SQL FTS", needle: "sqlretrievalneedle", path: "schema/catalog.sql", language: "sql"},
		{name: "OpenAPI FTS", needle: "openapiretrievalneedle", path: "api/catalog.openapi.json", language: "openapi"},
		{name: "TypeScript FTS", needle: "typescriptretrievalneedle", path: "web/catalog.ts", language: "typescript"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := fixture.query(t, authorized, uci.QueryModeFTS, test.needle)
			fixture.requireBoundResponse(t, test.name, response)
			if response.Status != uci.QueryStatusOK || response.Retrieval == nil || response.Retrieval.Mode != uci.QueryRetrievalLexical {
				t.Fatalf("%s response = %#v, want lexical result", test.name, response)
			}
			item := requireUCIRetrievalSliceItemAtPath(t, response, test.path)
			if item.Language != test.language || !containsUCIRetrievalSliceMatchSource(item.MatchSources, uci.QueryMatchFTS) {
				t.Fatalf("%s item = %#v, want %q FTS evidence", test.name, item, test.language)
			}
		})
	}

	sharedScope := fixture.query(t, authorized, uci.QueryModeFTS, "sharedscopeneedle")
	fixture.requireBoundResponse(t, "same-token View scope", sharedScope)
	if sharedScope.Status != uci.QueryStatusOK {
		t.Fatalf("shared-scope response = %#v, want a current-View result", sharedScope)
	}
	_ = requireUCIRetrievalSliceItemAtPath(t, sharedScope, "docs/catalog.md")
	fixture.requireNoOutOfViewPath(t, "same-token View scope", sharedScope)

	empty := fixture.query(t, authorized, uci.QueryModeFTS, "outofviewonlyneedle")
	fixture.requireBoundResponse(t, "out-of-View-only query", empty)
	if empty.Status != uci.QueryStatusEmpty || empty.Items == nil || len(*empty.Items) != 0 {
		t.Fatalf("out-of-View-only response = %#v, want explicitly empty", empty)
	}

	read, err := fixture.application.ReadCodebase(fixture.callerContext, authorized, mcp.CodebaseReadInput{
		Ref:               exactItem.Ref,
		Span:              exactItem.Span,
		ContentDigest:     exactItem.ContentDigest,
		VerifyWorkingCopy: true,
		MaxBytes:          int(exactItem.Span.ByteEnd - exactItem.Span.ByteStart),
	})
	if err != nil {
		t.Fatalf("versioned read published Go citation: %v", err)
	}
	fixture.requireBoundResponse(t, "versioned read", read)
	if read.Status != uci.QueryStatusOK || read.Retrieval == nil || read.Retrieval.Mode != uci.QueryRetrievalExact {
		t.Fatalf("versioned read response = %#v, want exact result", read)
	}
	readItem := requireUCIRetrievalSliceItemAtPath(t, read, "src/widget_catalog.go")
	if readItem.Excerpt != exactItem.Excerpt || !containsUCIRetrievalSliceWarning(read.Warnings, uci.VersionedReadWorkingCopyNotVerifiedWarning) {
		t.Fatalf("versioned read = %#v, want exact stored bytes and no disk substitution", read)
	}

	staleRead, err := fixture.application.ReadCodebase(fixture.callerContext, authorized, mcp.CodebaseReadInput{
		Ref:               exactItem.Ref,
		Span:              exactItem.Span,
		ContentDigest:     uci.QueryContentDigest(strings.Repeat("0", 64)),
		VerifyWorkingCopy: true,
		MaxBytes:          int(exactItem.Span.ByteEnd - exactItem.Span.ByteStart),
	})
	if err != nil {
		t.Fatalf("versioned read stale citation: %v", err)
	}
	fixture.requireBoundResponse(t, "stale versioned read", staleRead)
	if staleRead.Status != uci.QueryStatusEmpty || staleRead.Items == nil || len(*staleRead.Items) != 0 {
		t.Fatalf("stale versioned read = %#v, want an empty result without replacement bytes", staleRead)
	}

	graph, err := fixture.application.ExploreCodebase(fixture.callerContext, authorized, mcp.CodebaseGraphInput{
		Action: uci.GraphActionNeighbors,
		Target: uci.GraphTarget{EntityKey: exactItem.Ref.EntityKey},
		Filter: uci.GraphFilter{
			Direction: uci.GraphDirectionOutgoing,
			Relations: []uci.IndexRelation{"calls"},
		},
		Budget: uci.GraphBudget{MaxDepth: 1, MaxVisited: 8, MaxNodes: 8, MaxEdges: 8},
	})
	if err != nil {
		t.Fatalf("View-pinned graph: %v", err)
	}
	fixture.requireBoundResponse(t, "View-pinned graph", graph)
	if graph.Status != uci.QueryStatusOK || graph.Retrieval == nil || graph.Retrieval.Mode != uci.QueryRetrievalGraph || graph.Graph == nil || len(graph.Graph.Edges) != 1 {
		t.Fatalf("graph response = %#v, want one resolved calls edge", graph)
	}
	fixture.requireNoOutOfViewPath(t, "View-pinned graph", graph)

	partialGraph, err := fixture.application.ExploreCodebase(fixture.callerContext, authorized, mcp.CodebaseGraphInput{
		Action: uci.GraphActionNeighbors,
		Target: uci.GraphTarget{EntityKey: exactItem.Ref.EntityKey},
		Filter: uci.GraphFilter{
			Direction: uci.GraphDirectionOutgoing,
			Relations: []uci.IndexRelation{"calls"},
		},
		Budget: uci.GraphBudget{MaxDepth: 0, MaxVisited: 8, MaxNodes: 8, MaxEdges: 8},
	})
	if err != nil {
		t.Fatalf("bounded partial graph: %v", err)
	}
	fixture.requireBoundResponse(t, "bounded partial graph", partialGraph)
	if partialGraph.Status != uci.QueryStatusPartial || partialGraph.Graph == nil || partialGraph.Graph.StopReason != uci.QueryGraphDepthCap || !containsUCIRetrievalSliceWarning(partialGraph.Warnings, "graph_outcome:unknown_or_truncated") {
		t.Fatalf("partial graph response = %#v, want explicit depth-capped partial outcome", partialGraph)
	}

	status, err := fixture.status.Status(fixture.callerContext, authorized, "")
	if err != nil {
		t.Fatalf("load published View status: %v", err)
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("published View status is invalid: %v", err)
	}
	if status.Context != published.Context || status.Coverage.Structural != uci.IndexCoverageComplete || status.Coverage.ExcludedFiles != 1 {
		t.Fatalf("published View status = %#v, want one explicit unsupported/excluded admission state", status)
	}

	fixture.embedEveryCurrentCandidate(t, authorized)
	semantic, err := fixture.application.SearchCodebase(fixture.callerContext, authorized, mcp.CodebaseSearchInput{
		Query: uciRetrievalSliceSemanticQuery,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("provider-backed semantic query: %v", err)
	}
	fixture.requireBoundResponse(t, "provider-backed semantic query", semantic)
	if semantic.Status != uci.QueryStatusOK || semantic.Retrieval == nil || semantic.Retrieval.Mode != uci.QueryRetrievalHybrid || semantic.Retrieval.VectorCoverage == nil || *semantic.Retrieval.VectorCoverage != 1 || len(semantic.Retrieval.DegradationReasons) != 0 {
		t.Fatalf("semantic response = %#v, want complete provider-backed hybrid result", semantic)
	}
	semanticItem := requireUCIRetrievalSliceItemContaining(t, semantic, "semanticvectortarget")
	if semanticItem.Path != "src/widget_catalog.go" || !containsUCIRetrievalSliceMatchSource(semanticItem.MatchSources, uci.QueryMatchVector) {
		t.Fatalf("semantic result item = %#v, want vector-only Go evidence", semanticItem)
	}
	if fixture.provider.corpusInputs() == 0 || fixture.provider.queryInputs() == 0 {
		t.Fatalf("embedding provider did not receive both corpus and semantic query input")
	}

	fixture.provider.failRequests()
	degraded, err := fixture.application.SearchCodebase(fixture.callerContext, authorized, mcp.CodebaseSearchInput{
		Query: "sharedscopeneedle",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("degraded semantic query: %v", err)
	}
	fixture.requireBoundResponse(t, "provider-degraded semantic query", degraded)
	if degraded.Status != uci.QueryStatusOK || degraded.Retrieval == nil || degraded.Retrieval.Mode != uci.QueryRetrievalLexical || degraded.Retrieval.VectorCoverage == nil || *degraded.Retrieval.VectorCoverage != 0 || !containsUCIRetrievalSliceString(degraded.Retrieval.DegradationReasons, "vector_provider_unavailable") {
		t.Fatalf("degraded semantic response = %#v, want an explicit lexical-only provider degradation", degraded)
	}
	if fixture.provider.failedRequests() == 0 {
		t.Fatal("local embedding provider was not exercised for the degraded response")
	}
	fixture.requireNoOutOfViewPath(t, "provider-degraded semantic query", degraded)
}

type uciRetrievalSliceFixture struct {
	store              *gormstore.Store
	contextStore       *gormstore.UCIContextStore
	projection         *gormstore.UCIProjectionStore
	authorizer         *gormstore.UCIContextAuthorizer
	resolver           *uci.ContextResolver
	queryService       *uci.QueryService
	semanticService    *uci.SemanticService
	application        *worker.UCIApplication
	status             *uci.IndexStatusService
	provider           *uciRetrievalSliceEmbeddingProvider
	providerURL        string
	providerCredential string
	privateLocator     string
	context            context.Context
	callerContext      context.Context
	indexCaller        uci.IndexCaller
	source             *gormstore.UCISource
	checkout           *gormstore.UCICheckout
	profile            *gormstore.UCIAnalysisProfile
	vectorProfile      uci.VectorProfile
	treeSitter         *uci.TreeSitterWorker
	clientSessionID    string
	token              string
	catalogEntrySymbol string
	visiblePaths       []string
	outOfViewPath      string
	published          uci.IndexPublishedView
}

func newUCIRetrievalSliceFixture(t *testing.T) *uciRetrievalSliceFixture {
	t.Helper()

	store := openUCIRetrievalSliceStore(t)
	provider := newUCIRetrievalSliceEmbeddingProvider(t)
	t.Setenv("ENGRAM_EMBEDDING_URL", provider.server.URL)
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "uci-retrieval-local-model")
	t.Setenv("ENGRAM_EMBEDDING_API_KEY", uciRetrievalSliceProviderKey)
	t.Setenv("ENGRAM_EMBEDDING_DIMENSIONS", "1536")
	embedder, err := embedding.NewClient()
	if err != nil {
		t.Fatalf("create local embedding client: %v", err)
	}

	treeSitter, bundleDigest := newUCIRetrievalSliceTreeSitterWorker(t)
	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	ctx := context.Background()
	contextStore := gormstore.NewUCIContextStore(store.GetDB())
	source, err := contextStore.CreateSource(ctx, gormstore.CreateSourceInput{
		AuthRealm:   string(auth.SourceClient),
		Kind:        gormstore.UCISourceGit,
		DisplayName: "uci-retrieval-slice-" + token,
	})
	if err != nil {
		t.Fatalf("create retrieval source: %v", err)
	}
	principal := "uci-retrieval-principal-" + token
	workstationID := uuid.NewString()
	checkout, err := contextStore.RegisterCheckout(ctx, gormstore.RegisterCheckoutInput{
		SourceID:       source.SourceID,
		WorkstationID:  workstationID,
		Kind:           gormstore.UCICheckoutWorkingTree,
		OwnerPrincipal: principal,
		LocatorRef:     uciRetrievalSliceLocator,
	})
	if err != nil {
		t.Fatalf("register retrieval checkout: %v", err)
	}
	profile, err := contextStore.CreateProfile(ctx, gormstore.CreateProfileInput{
		ParserBundleDigest:   string(bundleDigest),
		ResolverRevision:     "uci-retrieval-slice-resolver-v1",
		ChunkerRevision:      "uci-retrieval-slice-chunker-v1",
		IgnorePolicyDigest:   uciRetrievalSliceDigest("uci-retrieval-slice-ignore-v1"),
		BuildContextJSON:     `{"suite":"uci-retrieval-slice"}`,
		SecretPolicyRevision: "uci-retrieval-slice-secret-policy-v1",
	})
	if err != nil {
		t.Fatalf("create retrieval analysis profile: %v", err)
	}

	projection := gormstore.NewUCIProjectionStore(store.GetDB())
	authorizer := gormstore.NewUCIContextAuthorizer(contextStore)
	resolver := uci.NewContextResolver(contextStore, authorizer, contextStore)
	contextApplication, err := mcp.NewUCIContextApplication(resolver, contextStore)
	if err != nil {
		t.Fatalf("compose UCI context application: %v", err)
	}
	vectorProfile := uci.VectorProfile{
		ProviderRef:           provider.server.URL,
		Model:                 embedder.Model(),
		Dimension:             embedding.EmbeddingDim,
		PreprocessingRevision: "uci-retrieval-slice-semantic-v1",
		IncludeRelativePath:   true,
	}
	queryService := uci.NewQueryService(projection)
	semanticService := uci.NewSemanticService(vectorProfile, embedder, projection, projection)
	application, err := worker.NewUCIApplication(
		contextApplication,
		uci.NewAliasResolver(contextStore.LookupLegacyAliasRecords),
		queryService,
		semanticService,
		uci.NewGraphService(projection),
		uci.NewVersionedReadService(projection),
		uci.NewIndexStatusService(projection, nil),
	)
	if err != nil {
		t.Fatalf("compose UCI retrieval application: %v", err)
	}

	clientSessionID := "uci-retrieval-slice-session-" + token
	identity := auth.ClientWithPrincipal("read-write", workstationID, principal, auth.PrincipalKindAgent)
	callerContext := auth.WithIdentity(mcp.ContextWithSession(ctx, clientSessionID), identity)
	return &uciRetrievalSliceFixture{
		store:              store,
		contextStore:       contextStore,
		projection:         projection,
		authorizer:         authorizer,
		resolver:           resolver,
		queryService:       queryService,
		semanticService:    semanticService,
		application:        application,
		status:             uci.NewIndexStatusService(projection, nil),
		provider:           provider,
		providerURL:        provider.server.URL,
		providerCredential: uciRetrievalSliceProviderKey,
		privateLocator:     uciRetrievalSliceLocator,
		context:            ctx,
		callerContext:      callerContext,
		indexCaller: uci.IndexCaller{
			AuthRealm:     string(auth.SourceClient),
			Principal:     principal,
			OwnerInstance: "uci-retrieval-slice-owner-" + token,
		},
		source:          source,
		checkout:        checkout,
		profile:         profile,
		vectorProfile:   vectorProfile,
		treeSitter:      treeSitter,
		clientSessionID: clientSessionID,
		token:           token,
		outOfViewPath:   uciRetrievalSliceOutOfViewPath,
	}
}

func (fixture *uciRetrievalSliceFixture) publishOneView(t *testing.T) uci.IndexPublishedView {
	t.Helper()

	orphan := fixture.goArtifact(t, []byte(`package hidden

func OutOfView() string {
	return "sharedscopeneedle outofviewonlyneedle"
}
`))
	_, err := fixture.projection.AdmitIndexFrame(fixture.context, fixture.source.SourceID, fixture.profile.ProfileID, uci.IndexAdmissionFrame{
		Version:   uci.IndexAdmissionFrameVersion,
		Profile:   uci.IndexAdmissionProfile{ID: fixture.profile.ProfileID},
		Artifacts: []uci.IndexAdmissionArtifact{orphan},
	})
	if err != nil {
		t.Fatalf("admit deliberately unlisted out-of-View artifact: %v", err)
	}

	goArtifact := fixture.goArtifact(t, []byte(`package catalog

func CatalogEntry() string {
	return WidgetService()
}

func WidgetService() string {
	return "goretrievalneedle semanticvectortarget"
}
`))
	catalogEntry := requireUCIRetrievalSliceDefinition(t, goArtifact, "func:CatalogEntry")
	widgetService := requireUCIRetrievalSliceDefinition(t, goArtifact, "func:WidgetService")
	call := requireUCIRetrievalSliceCall(t, goArtifact, catalogEntry.LocalSymbolKey)
	fixture.catalogEntrySymbol = catalogEntry.SymbolKey

	markdown := fixture.markdownArtifact(t, []byte(`# Widget catalog

mdretrievalneedle sharedscopeneedle

`))
	jsonArtifact := fixture.jsonYAMLArtifact(t, uci.JSONYAMLFormatJSON, []byte(`{
  "catalog": {"marker": "jsonretrievalneedle", "kind": "widget"}
}
`))
	yamlArtifact := fixture.jsonYAMLArtifact(t, uci.JSONYAMLFormatYAML, []byte("catalog:\n  marker: yamlretrievalneedle\n  kind: widget\n"))
	sqlArtifact := fixture.sqlArtifact(t, []byte(`CREATE TABLE widget_catalog (
  id uuid PRIMARY KEY,
  marker text NOT NULL DEFAULT 'sqlretrievalneedle'
);
`))
	openAPIArtifact := fixture.openAPIArtifact(t, []byte(`{
  "openapi": "3.0.3",
  "info": {"title": "Widget catalog", "version": "1.0.0"},
  "paths": {
    "/widgets": {
      "get": {
        "operationId": "listWidgets",
        "responses": {"200": {"description": "openapiretrievalneedle"}}
      }
    }
  },
  "components": {"schemas": {"Widget": {"type": "object"}}}
}
`))
	typescript := fixture.typeScriptArtifact(t, []byte(`export interface WidgetCatalog {
  names(): string[];
}

export function listWidgets(): string {
  return "typescriptretrievalneedle";
}
`))

	artifacts := []struct {
		path     string
		artifact uci.IndexAdmissionArtifact
	}{
		{path: "src/widget_catalog.go", artifact: goArtifact},
		{path: "docs/catalog.md", artifact: markdown},
		{path: "config/catalog.json", artifact: jsonArtifact},
		{path: "config/catalog.yaml", artifact: yamlArtifact},
		{path: "schema/catalog.sql", artifact: sqlArtifact},
		{path: "api/catalog.openapi.json", artifact: openAPIArtifact},
		{path: "web/catalog.ts", artifact: typescript},
	}
	for _, item := range artifacts {
		if item.artifact.Status != uci.IndexAdmissionArtifactComplete {
			t.Fatalf("%s admission status = %q, want complete; diagnostics=%#v", item.path, item.artifact.Status, item.artifact.Diagnostics)
		}
		if len(item.artifact.Definitions) == 0 || len(item.artifact.Chunks) == 0 {
			t.Fatalf("%s admission artifact lacks source-grounded facts: %#v", item.path, item.artifact)
		}
		fixture.visiblePaths = append(fixture.visiblePaths, item.path)
	}

	goArtifactID := goArtifact.ArtifactID
	callSourceSymbol := catalogEntry.LocalSymbolKey
	callTargetSymbol := widgetService.LocalSymbolKey
	memberships := make([]uci.IndexAdmissionMembership, 0, len(artifacts)+1)
	replacements := make([]uci.IndexAdmissionEdgeReplacement, 0, len(artifacts))
	for _, item := range artifacts {
		artifactID := item.artifact.ArtifactID
		memberships = append(memberships, uci.IndexAdmissionMembership{
			PathKey:     item.path,
			DisplayPath: item.path,
			Mode:        "100644",
			State:       uci.IndexAdmissionMembershipPresent,
			ArtifactID:  &artifactID,
		})
		replacement := uci.IndexAdmissionEdgeReplacement{SourcePath: item.path, Edges: []uci.IndexAdmissionEdge{}}
		if item.path == "src/widget_catalog.go" {
			replacement.Edges = append(replacement.Edges, uci.IndexAdmissionEdge{
				EdgeKey:          "catalog-entry-calls-widget-service",
				SourceArtifactID: goArtifactID,
				SourceSymbolKey:  &callSourceSymbol,
				Target: &uci.IndexAdmissionEdgeTarget{
					PathKey:    "src/widget_catalog.go",
					ArtifactID: goArtifactID,
					SymbolKey:  &callTargetSymbol,
				},
				Relation:         "calls",
				EvidenceKind:     "resolved",
				ResolutionState:  "resolved",
				ResolverRevision: "uci-retrieval-slice-resolver-v1",
				Evidence: uci.IndexAdmissionEdgeEvidence{
					ReferenceSiteKey: call.SiteKey,
					Span:             call.Span,
					RuleKey:          "go-static-call",
					Explanation:      "same-artifact static call",
				},
			})
		}
		replacements = append(replacements, replacement)
	}
	memberships = append(memberships, uci.IndexAdmissionMembership{
		PathKey:     "vendor/unsupported.lock",
		DisplayPath: "vendor/unsupported.lock",
		Mode:        "100644",
		State:       uci.IndexAdmissionMembershipUnsupported,
	})
	replacements = append(replacements, uci.IndexAdmissionEdgeReplacement{SourcePath: "vendor/unsupported.lock", Edges: []uci.IndexAdmissionEdge{}})

	part, err := fixture.projection.AdmitIndexFrame(fixture.context, fixture.source.SourceID, fixture.profile.ProfileID, uci.IndexAdmissionFrame{
		Version:          uci.IndexAdmissionFrameVersion,
		Profile:          uci.IndexAdmissionProfile{ID: fixture.profile.ProfileID},
		Artifacts:        collectUCIRetrievalSliceArtifacts(artifacts),
		Memberships:      memberships,
		EdgeReplacements: replacements,
	})
	if err != nil {
		t.Fatalf("admit mixed retrieval corpus: %v", err)
	}
	if !hasUCIRetrievalSliceMembership(part.Memberships, "vendor/unsupported.lock", uci.IndexFileExcluded) {
		t.Fatalf("admitted part does not preserve the unsupported membership as explicit excluded state: %#v", part.Memberships)
	}

	publisher, err := fixture.projection.Publisher(fixture.authorizer, uci.IndexPublicationConfig{
		Limits:           uci.DefaultIndexPublicationLimits(),
		EmbeddingProfile: &fixture.vectorProfile,
	})
	if err != nil {
		t.Fatalf("create real UCI publisher: %v", err)
	}
	build, err := publisher.Begin(fixture.context, fixture.indexCaller, uci.IndexBeginInput{
		BuildKey: "uci-retrieval-slice-" + fixture.token,
		Scope: uci.IndexScope{
			SourceID:      fixture.source.SourceID,
			CheckoutID:    fixture.checkout.CheckoutID,
			IncarnationID: fixture.checkout.IncarnationID,
		},
		ProfileID: fixture.profile.ProfileID,
		Mode:      uci.IndexManifestFull,
		JobKind:   uci.IndexJobInitial,
	})
	if err != nil {
		t.Fatalf("begin real UCI publication: %v", err)
	}
	partDigest, err := uci.DigestIndexPart(part)
	if err != nil {
		t.Fatalf("digest admitted part: %v", err)
	}
	ack, err := publisher.Stage(fixture.context, fixture.indexCaller, uci.IndexStageInput{
		Build:    build.Build,
		Sequence: 0,
		Digest:   partDigest,
		Part:     part,
	})
	if err != nil {
		t.Fatalf("stage admitted retrieval corpus: %v", err)
	}
	partsDigest, err := uci.DigestIndexParts([]uci.IndexPartAck{ack})
	if err != nil {
		t.Fatalf("digest staged part acknowledgement: %v", err)
	}
	manifestDigest, err := uci.DigestIndexManifest(part.Memberships)
	if err != nil {
		t.Fatalf("digest retrieval manifest: %v", err)
	}
	edgesDigest, err := uci.DigestIndexEdges(part.EdgeReplacements)
	if err != nil {
		t.Fatalf("digest retrieval graph replacements: %v", err)
	}
	headOID := strings.Repeat("a", 40)
	objectFormat := "sha1"
	refLabel := "refs/heads/uci-retrieval-slice"
	scanStart := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	published, err := publisher.Finalize(fixture.context, fixture.indexCaller, uci.IndexFinalizeInput{
		Build: build.Build,
		Manifest: uci.IndexManifestCompletion{
			PartCount:      1,
			PartsDigest:    partsDigest,
			EntryCount:     uint64(len(part.Memberships)),
			ManifestDigest: manifestDigest,
			EdgeCount:      uint64(countUCIRetrievalSliceEdges(part.EdgeReplacements)),
			EdgesDigest:    edgesDigest,
			ScanOutcome:    uci.IndexScanComplete,
			CensusComplete: true,
			Observation: uci.IndexObservation{
				HeadOID:       &headOID,
				ObjectFormat:  &objectFormat,
				RefLabel:      &refLabel,
				ObservedFSSeq: 1,
				ScanStart:     scanStart,
				ScanEnd:       scanStart.Add(time.Second),
			},
			Coverage: uci.IndexCoverage{
				Structural:    uci.IndexCoverageComplete,
				Lexical:       uci.IndexCoverageComplete,
				Vector:        uci.IndexCoverageUnavailable,
				ExcludedFiles: 1,
			},
		},
	})
	if err != nil {
		t.Fatalf("publish retrieval View: %v", err)
	}
	fixture.published = published
	return published
}

func (fixture *uciRetrievalSliceFixture) authorize(t *testing.T, ref uci.ContextRef) uci.AuthorizedContext {
	t.Helper()
	authorized, err := fixture.resolver.Authorize(fixture.callerContext, uci.ResolveContextInput{
		ClientSessionID: fixture.clientSessionID,
		AuthRealm:       fixture.indexCaller.AuthRealm,
		Principal:       fixture.indexCaller.Principal,
		Ref:             &ref,
	})
	if err != nil {
		t.Fatalf("authorize published View: %v", err)
	}
	if authorized.Ref() != ref {
		t.Fatalf("authorized context = %#v, want %#v", authorized.Ref(), ref)
	}
	return authorized
}

func (fixture *uciRetrievalSliceFixture) query(t *testing.T, authorized uci.AuthorizedContext, mode uci.QueryMode, text string) uci.QueryResponse {
	t.Helper()
	result, err := fixture.queryService.Query(fixture.callerContext, authorized, uci.QuerySpec{
		ClientSessionID: fixture.clientSessionID,
		Mode:            mode,
		Text:            text,
		Order:           uci.QueryOrderRelevance,
		Limit:           25,
	})
	if err != nil {
		t.Fatalf("query %s %q: %v", mode, text, err)
	}
	return result.Response
}

func (fixture *uciRetrievalSliceFixture) embedEveryCurrentCandidate(t *testing.T, authorized uci.AuthorizedContext) {
	t.Helper()
	seen := make(map[string]struct{})
	for _, path := range fixture.visiblePaths {
		selected, err := fixture.projection.SelectCandidates(fixture.callerContext, authorized, uci.QuerySpec{
			ClientSessionID: fixture.clientSessionID,
			Mode:            uci.QueryModeExactRelativePath,
			Text:            path,
			Order:           uci.QueryOrderPath,
			Limit:           50,
		})
		if err != nil {
			t.Fatalf("select current semantic candidates for %q: %v", path, err)
		}
		if len(selected.Candidates) == 0 {
			t.Fatalf("no current semantic candidates for published path %q", path)
		}
		for _, candidate := range selected.Candidates {
			key := candidate.Proof.ArtifactID + "\x00" + candidate.RelativePath + "\x00" + candidate.EntityKey + "\x00" + string(candidate.Proof.ContentDigest)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			if err := fixture.semanticService.EnsureCandidateEmbedding(fixture.callerContext, authorized, candidate); err != nil {
				t.Fatalf("provider-embed current candidate %q: %v", candidate.RelativePath, err)
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("no current candidates reached the real semantic provider")
	}
}

func (fixture *uciRetrievalSliceFixture) requireBoundResponse(t *testing.T, name string, response uci.QueryResponse) {
	t.Helper()
	if err := response.ValidatePreExposure(); err != nil {
		t.Fatalf("%s response invalid before exposure: %v; response=%#v", name, err, response)
	}
	if response.Contexts == nil || len(*response.Contexts) != 1 {
		t.Fatalf("%s contexts = %#v, want one exact View context", name, response.Contexts)
	}
	contextRef := (*response.Contexts)[0]
	ref := fixture.published.Context
	if contextRef.SourceID != ref.SourceID || contextRef.CheckoutID != ref.CheckoutID || contextRef.ViewID != ref.ViewID || contextRef.ProfileID != ref.AnalysisProfileID || contextRef.Generation != ref.Generation {
		t.Fatalf("%s context = %#v, want %#v", name, contextRef, ref)
	}
	if response.Items != nil {
		for _, item := range *response.Items {
			if item.Ref.SourceID != ref.SourceID || item.Ref.ViewID != ref.ViewID {
				t.Fatalf("%s item escapes selected View: %#v", name, item)
			}
		}
	}
	if response.Graph != nil {
		for _, node := range response.Graph.Nodes {
			fixture.requireGraphRefBound(t, name+" graph node", node)
		}
		for _, edge := range response.Graph.Edges {
			fixture.requireGraphRefBound(t, name+" graph edge from", edge.From)
			fixture.requireGraphRefBound(t, name+" graph edge to", edge.To)
			for _, evidence := range edge.EvidenceRefs {
				fixture.requireGraphRefBound(t, name+" graph evidence", evidence)
			}
		}
	}
	fixture.requireNoSensitiveLeak(t, response)
}

func (fixture *uciRetrievalSliceFixture) requireGraphRefBound(t *testing.T, name string, ref uci.QueryEntityRef) {
	t.Helper()
	if ref.SourceID != fixture.published.Context.SourceID || ref.ViewID != fixture.published.Context.ViewID {
		t.Fatalf("%s = %#v, want Source/View from %#v", name, ref, fixture.published.Context)
	}
}

func (fixture *uciRetrievalSliceFixture) requireNoOutOfViewPath(t *testing.T, name string, response uci.QueryResponse) {
	t.Helper()
	if response.Items == nil {
		return
	}
	for _, item := range *response.Items {
		if item.Path == fixture.outOfViewPath {
			t.Fatalf("%s returned out-of-View path %q", name, item.Path)
		}
	}
}

func (fixture *uciRetrievalSliceFixture) requireNoSensitiveLeak(t *testing.T, response uci.QueryResponse) {
	t.Helper()
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal query response for redaction assertion: %v", err)
	}
	for _, forbidden := range []string{fixture.providerURL, fixture.providerCredential, fixture.privateLocator} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("query response leaked provider or private locator material")
		}
	}
}

func (fixture *uciRetrievalSliceFixture) goArtifact(t *testing.T, source []byte) uci.IndexAdmissionArtifact {
	t.Helper()
	profile := uci.GoExtractionProfile{ProfileKey: "uci-retrieval-slice-go", ParserKey: "go-parser-v1"}
	admissionProfile, err := uci.GoIndexAdmissionArtifactProfile(profile)
	if err != nil {
		t.Fatalf("derive Go admission profile: %v", err)
	}
	admissionProfile.ExtractionProfileDigest = uci.IndexDigest(fixture.profile.ParserBundleDigest)
	artifact, err := uci.NewIndexAdmissionArtifactFromGo(fixture.source.SourceID, admissionProfile, source, uci.ExtractGo(source, profile))
	if err != nil {
		t.Fatalf("build Go admission artifact: %v", err)
	}
	return artifact
}

func (fixture *uciRetrievalSliceFixture) markdownArtifact(t *testing.T, source []byte) uci.IndexAdmissionArtifact {
	t.Helper()
	profile := uci.DefaultMarkdownExtractionProfile("uci-retrieval-slice-markdown")
	admissionProfile, err := uci.MarkdownIndexAdmissionArtifactProfile(profile)
	if err != nil {
		t.Fatalf("derive Markdown admission profile: %v", err)
	}
	admissionProfile.ExtractionProfileDigest = uci.IndexDigest(fixture.profile.ParserBundleDigest)
	artifact, err := uci.NewIndexAdmissionArtifactFromMarkdown(fixture.source.SourceID, admissionProfile, profile, source, uci.ExtractMarkdown(source, profile))
	if err != nil {
		t.Fatalf("build Markdown admission artifact: %v", err)
	}
	return artifact
}

func (fixture *uciRetrievalSliceFixture) jsonYAMLArtifact(t *testing.T, format uci.JSONYAMLFormat, source []byte) uci.IndexAdmissionArtifact {
	t.Helper()
	profile := uci.DefaultJSONYAMLExtractionProfile("uci-retrieval-slice-"+string(format), format)
	admissionProfile, err := uci.JSONYAMLIndexAdmissionArtifactProfile(profile)
	if err != nil {
		t.Fatalf("derive %s admission profile: %v", format, err)
	}
	admissionProfile.ExtractionProfileDigest = uci.IndexDigest(fixture.profile.ParserBundleDigest)
	artifact, err := uci.NewIndexAdmissionArtifactFromJSONYAML(fixture.source.SourceID, admissionProfile, profile, source, uci.ExtractJSONYAML(source, profile))
	if err != nil {
		t.Fatalf("build %s admission artifact: %v", format, err)
	}
	return artifact
}

func (fixture *uciRetrievalSliceFixture) sqlArtifact(t *testing.T, source []byte) uci.IndexAdmissionArtifact {
	t.Helper()
	profile := uci.DefaultSQLExtractionProfile("uci-retrieval-slice-sql")
	admissionProfile, err := uci.SQLIndexAdmissionArtifactProfile(profile)
	if err != nil {
		t.Fatalf("derive SQL admission profile: %v", err)
	}
	admissionProfile.ExtractionProfileDigest = uci.IndexDigest(fixture.profile.ParserBundleDigest)
	artifact, err := uci.NewIndexAdmissionArtifactFromSQL(fixture.source.SourceID, admissionProfile, profile, source, uci.ExtractSQL(source, profile))
	if err != nil {
		t.Fatalf("build SQL admission artifact: %v", err)
	}
	return artifact
}

func (fixture *uciRetrievalSliceFixture) openAPIArtifact(t *testing.T, source []byte) uci.IndexAdmissionArtifact {
	t.Helper()
	profile := uci.DefaultOpenAPIExtractionProfile("uci-retrieval-slice-openapi", uci.OpenAPIFormatJSON)
	admissionProfile, err := uci.OpenAPIIndexAdmissionArtifactProfile(profile)
	if err != nil {
		t.Fatalf("derive OpenAPI admission profile: %v", err)
	}
	admissionProfile.ExtractionProfileDigest = uci.IndexDigest(fixture.profile.ParserBundleDigest)
	artifact, err := uci.NewIndexAdmissionArtifactFromOpenAPI(fixture.source.SourceID, admissionProfile, profile, source, uci.ExtractOpenAPI(source, profile))
	if err != nil {
		t.Fatalf("build OpenAPI admission artifact: %v", err)
	}
	return artifact
}

func (fixture *uciRetrievalSliceFixture) typeScriptArtifact(t *testing.T, source []byte) uci.IndexAdmissionArtifact {
	t.Helper()
	bundleDigest := uci.IndexDigest(fixture.profile.ParserBundleDigest)
	admissionProfile, err := uci.TreeSitterIndexAdmissionArtifactProfile(uci.TreeSitterLanguageTypeScript, bundleDigest)
	if err != nil {
		t.Fatalf("derive TypeScript admission profile: %v", err)
	}
	parsed, err := fixture.treeSitter.Parse(fixture.context, uci.TreeSitterParseRequest{
		Language:   uci.TreeSitterLanguageTypeScript,
		ProfileKey: "uci-retrieval-slice-typescript",
		Source:     source,
	})
	if err != nil {
		t.Fatalf("parse TypeScript with bundled worker: %v", err)
	}
	if parsed.Coverage != uci.IndexCoverageComplete {
		t.Fatalf("TypeScript parser coverage = %q, want complete; diagnostics=%#v", parsed.Coverage, parsed.Diagnostics)
	}
	artifact, err := uci.NewIndexAdmissionArtifactFromTreeSitter(fixture.source.SourceID, admissionProfile, source, parsed)
	if err != nil {
		t.Fatalf("build TypeScript admission artifact: %v", err)
	}
	return artifact
}

type uciRetrievalSliceEmbeddingProvider struct {
	server *httptest.Server

	mu           sync.Mutex
	fail         bool
	corpus       int
	query        int
	failed       int
	requestCount int
}

func newUCIRetrievalSliceEmbeddingProvider(t *testing.T) *uciRetrievalSliceEmbeddingProvider {
	t.Helper()
	provider := &uciRetrievalSliceEmbeddingProvider{}
	provider.server = httptest.NewServer(http.HandlerFunc(provider.serveHTTP(t)))
	t.Cleanup(provider.server.Close)
	return provider
}

func (provider *uciRetrievalSliceEmbeddingProvider) serveHTTP(t *testing.T) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/embeddings" {
			http.Error(writer, "unexpected embedding request", http.StatusBadRequest)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+uciRetrievalSliceProviderKey {
			http.Error(writer, "missing synthetic provider credential", http.StatusUnauthorized)
			return
		}
		var payload struct {
			Model      string   `json:"model"`
			Dimensions int      `json:"dimensions"`
			Input      []string `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			http.Error(writer, "invalid embedding request", http.StatusBadRequest)
			return
		}
		if payload.Model != "uci-retrieval-local-model" || payload.Dimensions != embedding.EmbeddingDim || len(payload.Input) == 0 {
			http.Error(writer, "invalid embedding contract", http.StatusBadRequest)
			return
		}

		provider.mu.Lock()
		provider.requestCount++
		for _, input := range payload.Input {
			if strings.Contains(input, `"content_digest"`) {
				provider.corpus++
			} else {
				provider.query++
			}
		}
		fail := provider.fail
		if fail {
			provider.failed++
		}
		provider.mu.Unlock()
		if fail {
			http.Error(writer, "planned local provider outage", http.StatusServiceUnavailable)
			return
		}

		data := make([]map[string]any, len(payload.Input))
		for index, input := range payload.Input {
			vector := make([]float32, embedding.EmbeddingDim)
			switch {
			case strings.Contains(input, `"text":"`+uciRetrievalSliceSemanticQuery+`"`):
				vector[0] = 1
			case strings.Contains(input, "semanticvectortarget"):
				vector[0] = 1
			default:
				vector[1] = 1
			}
			data[index] = map[string]any{"embedding": vector, "index": index}
		}
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(map[string]any{"data": data}); err != nil {
			t.Errorf("encode local embedding response: %v", err)
		}
	}
}

func (provider *uciRetrievalSliceEmbeddingProvider) failRequests() {
	provider.mu.Lock()
	provider.fail = true
	provider.mu.Unlock()
}

func (provider *uciRetrievalSliceEmbeddingProvider) corpusInputs() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.corpus
}

func (provider *uciRetrievalSliceEmbeddingProvider) queryInputs() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.query
}

func (provider *uciRetrievalSliceEmbeddingProvider) failedRequests() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.failed
}

func openUCIRetrievalSliceStore(t *testing.T) *gormstore.Store {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("DATABASE_DSN"))
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping UCI retrieval PostgreSQL acceptance test")
	}

	adminDB, err := gormlib.Open(gormdriver.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open PostgreSQL admin connection: %v", err)
	}
	adminSQL, err := adminDB.DB()
	if err != nil {
		t.Fatalf("resolve PostgreSQL admin pool: %v", err)
	}
	if err := adminDB.Exec(`CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public`).Error; err != nil {
		t.Fatalf("ensure pgvector extension: %v", err)
	}
	schema := "uci_retrieval_slice_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := adminDB.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create isolated retrieval schema: %v", err)
	}
	t.Cleanup(func() {
		if err := adminDB.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop isolated retrieval schema: %v", err)
		}
		if err := adminSQL.Close(); err != nil {
			t.Errorf("close PostgreSQL admin pool: %v", err)
		}
	})

	parsedDSN, err := url.Parse(dsn)
	if err != nil || parsedDSN.Scheme == "" {
		t.Fatalf("parse DATABASE_DSN as PostgreSQL URL: %v", err)
	}
	query := parsedDSN.Query()
	query.Set("search_path", schema+", public")
	parsedDSN.RawQuery = query.Encode()
	store, err := gormstore.NewStore(gormstore.Config{DSN: parsedDSN.String(), MaxConns: 2, LogLevel: logger.Silent})
	if err != nil {
		t.Fatalf("open isolated UCI store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close isolated UCI store: %v", err)
		}
	})
	var actualSchema string
	if err := store.GetDB().Raw(`SELECT current_schema()`).Scan(&actualSchema).Error; err != nil {
		t.Fatalf("read isolated current schema: %v", err)
	}
	if actualSchema != schema {
		t.Fatalf("test pool schema = %q, want %q", actualSchema, schema)
	}
	return store
}

func newUCIRetrievalSliceTreeSitterWorker(t *testing.T) (*uci.TreeSitterWorker, uci.IndexDigest) {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve retrieval acceptance test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", "..", ".."))
	executable := filepath.Join(t.TempDir(), "uci-parser")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	build := exec.Command("go", "build", "-o", executable, "./tools/uci-parser")
	build.Dir = repositoryRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=1")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build bundled TypeScript parser: %v\n%s", err, output)
	}

	probe := uci.TreeSitterWorkerWireRequest{
		Version:    uci.TreeSitterWorkerProtocolVersion,
		Language:   uci.TreeSitterLanguageTypeScript,
		ProfileKey: "uci-retrieval-slice-parser-probe",
		Source:     []byte("export const parserProbe = true;\n"),
	}
	encoded, err := json.Marshal(probe)
	if err != nil {
		t.Fatalf("encode TypeScript parser probe: %v", err)
	}
	command := exec.Command(executable)
	command.Dir = t.TempDir()
	command.Env = uciRetrievalSliceParserEnvironment()
	command.Stdin = strings.NewReader(string(encoded) + "\n")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("run TypeScript parser probe: %v", err)
	}
	var response uci.TreeSitterWorkerWireResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode TypeScript parser probe: %v", err)
	}
	if response.Version != uci.TreeSitterWorkerProtocolVersion {
		t.Fatalf("TypeScript parser probe version = %q", response.Version)
	}
	admissionProfile, err := uci.TreeSitterIndexAdmissionArtifactProfile(uci.TreeSitterLanguageTypeScript, response.BundleDigest)
	if err != nil {
		t.Fatalf("validate TypeScript parser bundle identity: %v", err)
	}
	worker, err := uci.NewTreeSitterWorker(uci.TreeSitterWorkerConfig{
		ExecutablePath:       executable,
		ExpectedBundleDigest: admissionProfile.GrammarDigest,
		MaxInputBytes:        1 << 20,
		MaxOutputBytes:       1 << 20,
		Timeout:              5 * time.Second,
		Environment:          uciRetrievalSliceParserEnvironment(),
	})
	if err != nil {
		t.Fatalf("configure TypeScript parser worker: %v", err)
	}
	return worker, admissionProfile.GrammarDigest
}

func uciRetrievalSliceParserEnvironment() []string {
	environment := make([]string, 0, 3)
	for _, name := range []string{"SYSTEMROOT", "WINDIR", "COMSPEC"} {
		if value := os.Getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

func collectUCIRetrievalSliceArtifacts(items []struct {
	path     string
	artifact uci.IndexAdmissionArtifact
},
) []uci.IndexAdmissionArtifact {
	artifacts := make([]uci.IndexAdmissionArtifact, 0, len(items))
	for _, item := range items {
		artifacts = append(artifacts, item.artifact)
	}
	return artifacts
}

func requireUCIRetrievalSliceDefinition(t *testing.T, artifact uci.IndexAdmissionArtifact, localKey string) uci.IndexAdmissionDefinition {
	t.Helper()
	for _, definition := range artifact.Definitions {
		if definition.LocalSymbolKey == localKey {
			return definition
		}
	}
	t.Fatalf("artifact %q has no definition %q", artifact.ArtifactID, localKey)
	return uci.IndexAdmissionDefinition{}
}

func requireUCIRetrievalSliceCall(t *testing.T, artifact uci.IndexAdmissionArtifact, owner string) uci.IndexAdmissionReference {
	t.Helper()
	for _, reference := range artifact.References {
		if reference.Relation == "calls" && reference.OwnerSymbolKey != nil && *reference.OwnerSymbolKey == owner {
			return reference
		}
	}
	t.Fatalf("artifact %q has no calls reference owned by %q", artifact.ArtifactID, owner)
	return uci.IndexAdmissionReference{}
}

func requireUCIRetrievalSliceItemAtPath(t *testing.T, response uci.QueryResponse, path string) uci.QueryItem {
	t.Helper()
	if response.Items != nil {
		for _, item := range *response.Items {
			if item.Path == path {
				return item
			}
		}
	}
	t.Fatalf("response has no item for %q: %#v", path, response)
	return uci.QueryItem{}
}

func requireUCIRetrievalSliceItemContaining(t *testing.T, response uci.QueryResponse, text string) uci.QueryItem {
	t.Helper()
	if response.Items != nil {
		for _, item := range *response.Items {
			if strings.Contains(item.Excerpt, text) {
				return item
			}
		}
	}
	t.Fatalf("response has no item containing %q: %#v", text, response)
	return uci.QueryItem{}
}

func containsUCIRetrievalSliceMatchSource(sources []uci.QueryMatchSource, want uci.QueryMatchSource) bool {
	for _, source := range sources {
		if source == want {
			return true
		}
	}
	return false
}

func containsUCIRetrievalSliceWarning(warnings *uci.QueryWarnings, want string) bool {
	if warnings == nil {
		return false
	}
	for _, warning := range *warnings {
		if warning == want {
			return true
		}
	}
	return false
}

func containsUCIRetrievalSliceString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasUCIRetrievalSliceMembership(memberships []uci.IndexMembership, path string, state uci.IndexFileState) bool {
	for _, membership := range memberships {
		if membership.PathKey == path && membership.State == state {
			return true
		}
	}
	return false
}

func countUCIRetrievalSliceEdges(replacements []uci.IndexEdgeReplacement) int {
	count := 0
	for _, replacement := range replacements {
		count += len(replacement.Edges)
	}
	return count
}

func uciRetrievalSliceDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
