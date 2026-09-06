package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
)

const (
	// UCIProductMeasurementRecordPathEnv is deliberately separate from the
	// accounting runner input: this test only records caller-requested evidence.
	UCIProductMeasurementRecordPathEnv = "ENGRAM_UCI_PRODUCT_RECORD_PATH"

	// UCIProductMeasurementCorpusManifestDigest binds the fixed synthetic corpus
	// shape below. The associated corpus commit is intentionally resolved from
	// product_tasks.json when this recorder executes.
	UCIProductMeasurementCorpusManifestDigest = "sha256:896f4728de0e7876e373bab3963a3472a8e1d6ead1c087936e8a51ae9931bc59"
	UCIProductMeasurementProvider             = "uci-product-local-http"
	UCIProductMeasurementModel                = "uci-product-local-model-v1"
	UCIProductMeasurementProfile              = "uci-product-local-semantic-v1"

	uciProductMeasurementProviderCredential = "uci-product-local-test-credential"
	uciProductMeasurementManifest           = "engram.uci-product-corpus/v1\n" +
		"api/source-view.openapi.json\n" +
		"docs/source-view.md\n" +
		"schema/embedding_profiles.sql\n" +
		"specs/010-unified-code-intelligence/acceptance/uci-acceptance.md\n" +
		"src/dirty_view.go\n" +
		"src/flows.go\n" +
		"src/recovery.go\n" +
		"src/restart.go\n" +
		"src/vector_current.go\n" +
		"src/worktree_dirty.go\n" +
		"tests/uci/acceptance/slo_runner.go\n"
)

var uciProductMeasurementSemanticQueries = []string{
	"Где сохраняется отдельное несохранённое изменение рабочей копии без смешения с другой?",
	"Как запрос проходит от поиска к объяснению зависимостей в текущем снимке исходников?",
	"Почему старый векторный результат не должен отвечать за файл после нового сохранения?",
	"Где английский обработчик связывает русское описание ошибки с кодом восстановления индекса?",
}

var uciProductMeasurementSemanticRoutes = []string{
	"semantic-route:dirty",
	"semantic-route:search",
	"semantic-route:vector-",
	"semantic-route:recovery",
}

// TestUCIRecordProductTaskResult is an opt-in evidence recorder. It exercises
// the production admission, publication, PostgreSQL retrieval, graph, exact
// read, and semantic paths, then writes exactly one strict accounting input.
func TestUCIRecordProductTaskResult(t *testing.T) {
	recordPath := strings.TrimSpace(os.Getenv(UCIProductMeasurementRecordPathEnv))
	if recordPath == "" {
		t.Skip("UCI product recorder is disabled; set ENGRAM_UCI_PRODUCT_RECORD_PATH with a caller-owned output path")
	}
	if strings.TrimSpace(os.Getenv("DATABASE_DSN")) == "" {
		t.Skip("UCI product recorder requires a disposable DATABASE_DSN")
	}
	if got := uciProductMeasurementDigest(uciProductMeasurementManifest); got != UCIProductMeasurementCorpusManifestDigest {
		t.Fatalf("deterministic corpus manifest digest = %q, want %q", got, UCIProductMeasurementCorpusManifestDigest)
	}

	corpus, baseline, err := loadUCIProductTaskContracts()
	if err != nil {
		t.Fatalf("load resolved UCI product task contracts: %v", err)
	}
	if corpus.CorpusManifest.DirtyManifest != UCIProductMeasurementCorpusManifestDigest {
		t.Fatalf("resolved product corpus digest = %q, want recorder corpus %q", corpus.CorpusManifest.DirtyManifest, UCIProductMeasurementCorpusManifestDigest)
	}
	if corpus.ProviderProfile.Provider != UCIProductMeasurementProvider || corpus.ProviderProfile.Model != UCIProductMeasurementModel || corpus.ProviderProfile.Profile != UCIProductMeasurementProfile {
		t.Fatalf("resolved product provider profile does not match the recorder's fixed local provider identity")
	}

	repositoryRoot := uciProductMeasurementRepositoryRoot(t)
	candidate := uciProductMeasurementCandidate(t, repositoryRoot)
	fixture := newUCIProductMeasurementFixture(t)
	fixture.requireDatabaseFacts(t)

	first := fixture.publish(t, fixture.primaryCheckout, nil, "primary-v1", uciProductMeasurementSources("semantic-route:vector-old", "worktree-dirty-body-a", "restart-generation-one"))
	firstAuthorized := fixture.authorize(t, fixture.resolver, first.view.Context)
	fixture.ensureEmbeddings(t, firstAuthorized, []string{"src/vector_current.go"})

	currentSources := uciProductMeasurementSources("semantic-route:vector-current", "worktree-dirty-body-a", "restart-generation-two-current")
	current := fixture.publish(t, fixture.primaryCheckout, &first.view.Context, "primary-v2", currentSources)
	secondary := fixture.publish(t, fixture.secondaryCheckout, nil, "secondary-v1", uciProductMeasurementSources("semantic-route:vector-current", "worktree-dirty-body-b", "restart-generation-one"))

	fixture.recompose(t)
	currentAuthorized := fixture.authorize(t, fixture.resolver, current.view.Context)
	secondaryAuthorized := fixture.authorize(t, fixture.resolver, secondary.view.Context)
	currentPaths := make([]string, 0, len(currentSources))
	for _, file := range currentSources {
		currentPaths = append(currentPaths, file.path)
	}
	fixture.ensureEmbeddings(t, currentAuthorized, currentPaths)

	results := fixture.productResults(t, corpus, current, secondary, currentAuthorized, secondaryAuthorized)
	input := UCIProductTaskResultInput{
		SchemaVersion: uciProductResultSchemaVersion,
		Candidate: UCIProductCandidate{
			Commit:          candidate.commit,
			Tree:            candidate.tree,
			ArtifactDigests: uciProductMeasurementBuildCandidateArtifacts(t, repositoryRoot),
		},
		Corpus: UCIProductCorpus{
			Commit:        corpus.CorpusManifest.Commit,
			DirtyManifest: corpus.CorpusManifest.DirtyManifest,
		},
		Provider: UCIProductProvider{
			Provider: corpus.ProviderProfile.Provider,
			Model:    corpus.ProviderProfile.Model,
			Profile:  corpus.ProviderProfile.Profile,
		},
		Results: results,
	}
	if _, err := validateUCIProductTaskResultInput(input, corpus, baseline); err != nil {
		t.Fatalf("validate observed UCI product recorder input before write: %v", err)
	}
	if err := uciProductMeasurementWriteAtomic(recordPath, input); err != nil {
		t.Fatalf("atomically write caller-requested UCI product evidence: %v", err)
	}
}

type uciProductMeasurementFixture struct {
	store             *gormstore.Store
	contextStore      *gormstore.UCIContextStore
	projection        *gormstore.UCIProjectionStore
	authorizer        *gormstore.UCIContextAuthorizer
	resolver          *uci.ContextResolver
	application       *worker.UCIApplication
	status            *uci.IndexStatusService
	provider          *uciProductMeasurementEmbeddingProvider
	context           context.Context
	callerContext     context.Context
	indexCaller       uci.IndexCaller
	source            *gormstore.UCISource
	primaryCheckout   *gormstore.UCICheckout
	secondaryCheckout *gormstore.UCICheckout
	profile           *gormstore.UCIAnalysisProfile
	vectorProfile     uci.VectorProfile
	token             string
	scanSequence      int64
}

func newUCIProductMeasurementFixture(t *testing.T) *uciProductMeasurementFixture {
	t.Helper()

	store := openUCIRetrievalSliceStore(t)
	provider := newUCIProductMeasurementEmbeddingProvider(t)
	t.Setenv("ENGRAM_EMBEDDING_URL", provider.server.URL)
	t.Setenv("ENGRAM_EMBEDDING_MODEL", UCIProductMeasurementModel)
	t.Setenv("ENGRAM_EMBEDDING_API_KEY", uciProductMeasurementProviderCredential)
	t.Setenv("ENGRAM_EMBEDDING_DIMENSIONS", "1536")

	ctx := context.Background()
	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	contextStore := gormstore.NewUCIContextStore(store.GetDB())
	source, err := contextStore.CreateSource(ctx, gormstore.CreateSourceInput{
		AuthRealm:   string(auth.SourceClient),
		Kind:        gormstore.UCISourceGit,
		DisplayName: "uci-product-measurement-" + token,
	})
	if err != nil {
		t.Fatalf("create product measurement source: %v", err)
	}
	principal := "uci-product-measurement-principal-" + token
	primary := uciProductMeasurementRegisterCheckout(t, contextStore, source.SourceID, principal, "primary", token)
	secondary := uciProductMeasurementRegisterCheckout(t, contextStore, source.SourceID, principal, "secondary", token)
	profile, err := contextStore.CreateProfile(ctx, gormstore.CreateProfileInput{
		ParserBundleDigest:   uciProductMeasurementDigest("uci-product-measurement-parser-bundle-v1"),
		ResolverRevision:     "uci-product-measurement-resolver-v1",
		ChunkerRevision:      "uci-product-measurement-chunker-v1",
		IgnorePolicyDigest:   uciProductMeasurementDigest("uci-product-measurement-ignore-v1"),
		BuildContextJSON:     `{"suite":"uci-product-measurement"}`,
		SecretPolicyRevision: "uci-product-measurement-secret-policy-v1",
	})
	if err != nil {
		t.Fatalf("create product measurement analysis profile: %v", err)
	}

	fixture := &uciProductMeasurementFixture{
		store:             store,
		contextStore:      contextStore,
		projection:        gormstore.NewUCIProjectionStore(store.GetDB()),
		provider:          provider,
		context:           ctx,
		source:            source,
		primaryCheckout:   primary,
		secondaryCheckout: secondary,
		profile:           profile,
		token:             token,
		vectorProfile: uci.VectorProfile{
			ProviderRef:           provider.server.URL,
			Model:                 UCIProductMeasurementModel,
			Dimension:             embedding.EmbeddingDim,
			PreprocessingRevision: UCIProductMeasurementProfile,
			IncludeRelativePath:   true,
		},
	}
	fixture.authorizer = gormstore.NewUCIContextAuthorizer(contextStore)
	fixture.indexCaller = uci.IndexCaller{
		AuthRealm:     string(auth.SourceClient),
		Principal:     principal,
		OwnerInstance: "uci-product-measurement-owner-" + token,
	}
	identity := auth.ClientWithPrincipal("read-write", primary.WorkstationID, principal, auth.PrincipalKindAgent)
	fixture.callerContext = auth.WithIdentity(mcp.ContextWithSession(ctx, "uci-product-measurement-session-"+token), identity)
	fixture.recompose(t)
	return fixture
}

func uciProductMeasurementRegisterCheckout(t *testing.T, store *gormstore.UCIContextStore, sourceID, principal, name, token string) *gormstore.UCICheckout {
	t.Helper()
	checkout, err := store.RegisterCheckout(context.Background(), gormstore.RegisterCheckoutInput{
		SourceID:       sourceID,
		WorkstationID:  "uci-product-" + name + "-workstation-" + token,
		Kind:           gormstore.UCICheckoutWorkingTree,
		OwnerPrincipal: principal,
		LocatorRef:     "file:///private/uci-product-measurement-" + name,
	})
	if err != nil {
		t.Fatalf("register %s product measurement checkout: %v", name, err)
	}
	return checkout
}

// recompose intentionally rebuilds the ordinary service graph from the same
// durable PostgreSQL stores; P12 uses that recomposed application after V2.
func (fixture *uciProductMeasurementFixture) recompose(t *testing.T) {
	t.Helper()
	embedder, err := embedding.NewClient()
	if err != nil {
		t.Fatalf("create local product measurement embedding client: %v", err)
	}
	if embedder.Model() != UCIProductMeasurementModel {
		t.Fatalf("local embedding model = %q, want fixed product model", embedder.Model())
	}
	resolver := uci.NewContextResolver(fixture.contextStore, fixture.authorizer, fixture.contextStore)
	contextApplication, err := mcp.NewUCIContextApplication(resolver, fixture.contextStore)
	if err != nil {
		t.Fatalf("compose product measurement context application: %v", err)
	}
	application, err := worker.NewUCIApplication(
		contextApplication,
		uci.NewAliasResolver(fixture.contextStore.LookupLegacyAliasRecords),
		uci.NewQueryService(fixture.projection),
		uci.NewSemanticService(fixture.vectorProfile, embedder, fixture.projection, fixture.projection),
		uci.NewGraphService(fixture.projection),
		uci.NewVersionedReadService(fixture.projection),
		uci.NewIndexStatusService(fixture.projection, nil),
	)
	if err != nil {
		t.Fatalf("compose product measurement UCI application: %v", err)
	}
	fixture.resolver = resolver
	fixture.application = application
	fixture.status = uci.NewIndexStatusService(fixture.projection, nil)
}

func (fixture *uciProductMeasurementFixture) requireDatabaseFacts(t *testing.T) {
	t.Helper()
	var facts struct {
		Version string
		Size    int64
	}
	if err := fixture.store.GetDB().Raw(`SELECT version() AS version, pg_database_size(current_database()) AS size`).Scan(&facts).Error; err != nil {
		t.Fatalf("query PostgreSQL version and data size: %v", err)
	}
	if !strings.HasPrefix(facts.Version, "PostgreSQL") || facts.Size <= 0 {
		t.Fatal("PostgreSQL version or data size is unavailable for the recorder")
	}
}

type uciProductMeasurementFile struct {
	path string
	body string
}

type uciProductMeasurementLink struct {
	fromPath  string
	fromLocal string
	toPath    string
	toLocal   string
}

type uciProductMeasurementPublication struct {
	view      uci.IndexPublishedView
	artifacts map[string]uci.IndexAdmissionArtifact
}

func uciProductMeasurementSources(vectorRoute, worktreeBody, restartBody string) []uciProductMeasurementFile {
	return []uciProductMeasurementFile{
		{path: "src/dirty_view.go", body: `package corpus

func DirtyViewStorage() string {
	return "semantic-route:dirty saved isolated worktree body baseline-p01 baselinefanout"
}
`},
		{path: "src/flows.go", body: `package corpus

// semantic-route:search binds provider-only conceptual retrieval to this source.
func SearchToExplain() string { return "semantic-route:search " + ExplainDependencies() }
func ExplainDependencies() string { return "current snapshot dependency explanation baseline-p02 baselinefanout" }
func SaveToPublish() string { return SavedUpdateBoundary() }
func SavedUpdateBoundary() string { return PublishCurrentView() }
func PublishCurrentView() string { return "saved source reaches published current view baseline-p07 baselinefanout" }
func AuthorizeToImpact() string { return ScopedAuthorizationDecision() }
func ScopedAuthorizationDecision() string { return AffectedCallers() }
func AffectedCallers() string { return "scoped authorization impact baseline-p08 baselinefanout" }
func CodeToDocs() string { return DocumentationAnchor() }
func DocumentationAnchor() string { return "documented source view contract baselinefanout" }
func CodeToOpenAPI() string { return OpenAPIAnchor() }
func OpenAPIAnchor() string { return "OpenAPI source view schema baselinefanout" }
func CodeToSQL() string { return SQLProfileAnchor() }
func SQLProfileAnchor() string { return "embedding profile schema boundary baselinefanout" }
`},
		{path: "src/vector_current.go", body: "package corpus\n\nfunc CurrentVector() string { return \"" + vectorRoute + " current generation vector boundary baseline-p03 baselinefanout\" }\n"},
		{path: "src/recovery.go", body: `package corpus

func EnglishRecoveryHandler() string {
	return "semantic-route:recovery English handler maps localized failure to index recovery code baseline-p04 baselinefanout"
}
`},
		{path: "tests/uci/acceptance/slo_runner.go", body: `package acceptance

func RunUCISLOProfile() string { return "declared UCI SLO entry point baseline-p05 baselinefanout" }
`},
		{path: "specs/010-unified-code-intelligence/acceptance/uci-acceptance.md", body: `# UCI acceptance

## Продуктовые задачи

The recorder reads this declared product-task acceptance section. baseline-p06 baselinefanout
`},
		{path: "docs/source-view.md", body: `# Source/view contract

The documented source/view contract describes the API schema boundary. baseline-p09 baselinefanout
`},
		{path: "api/source-view.openapi.json", body: `{
  "openapi": "3.0.3",
  "info": {"title": "Source View", "version": "1.0.0"},
  "paths": {"/source-views": {"get": {"operationId": "getSourceView", "responses": {"200": {"description": "source view schema baselinefanout"}}}}},
  "components": {"schemas": {"SourceView": {"type": "object"}}}
}`},
		{path: "schema/embedding_profiles.sql", body: `CREATE TABLE embedding_profiles (
  profile_id uuid PRIMARY KEY,
  model text NOT NULL,
  preprocessing_revision text NOT NULL
);
-- baseline-p10 baselinefanout
`},
		{path: "src/worktree_dirty.go", body: "package corpus\n\nfunc WorktreeSavedBody() string { return \"" + worktreeBody + " baseline-p11 baselinefanout\" }\n"},
		{path: "src/restart.go", body: "package corpus\n\nfunc RestartRecoveryFlow() string { return CurrentRecoveredView() }\nfunc CurrentRecoveredView() string { return \"" + restartBody + " baseline-p12 baselinefanout\" }\n"},
	}
}

func (fixture *uciProductMeasurementFixture) publish(t *testing.T, checkout *gormstore.UCICheckout, parent *uci.ContextRef, label string, files []uciProductMeasurementFile) uciProductMeasurementPublication {
	t.Helper()
	artifacts := make(map[string]uci.IndexAdmissionArtifact, len(files))
	artifactList := make([]uci.IndexAdmissionArtifact, 0, len(files))
	memberships := make([]uci.IndexAdmissionMembership, 0, len(files))
	for _, file := range files {
		artifact := fixture.admitArtifact(t, file)
		if artifact.Status != uci.IndexAdmissionArtifactComplete || len(artifact.Definitions) == 0 || len(artifact.Chunks) == 0 {
			t.Fatalf("synthetic corpus admission is incomplete for %q", file.path)
		}
		artifacts[file.path] = artifact
		artifactList = append(artifactList, artifact)
		artifactID := artifact.ArtifactID
		memberships = append(memberships, uci.IndexAdmissionMembership{
			PathKey: file.path, DisplayPath: file.path, Mode: "100644", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &artifactID,
		})
	}

	links := []uciProductMeasurementLink{
		{fromPath: "src/flows.go", fromLocal: "func:SearchToExplain", toPath: "src/flows.go", toLocal: "func:ExplainDependencies"},
		{fromPath: "src/flows.go", fromLocal: "func:SaveToPublish", toPath: "src/flows.go", toLocal: "func:SavedUpdateBoundary"},
		{fromPath: "src/flows.go", fromLocal: "func:SavedUpdateBoundary", toPath: "src/flows.go", toLocal: "func:PublishCurrentView"},
		{fromPath: "src/flows.go", fromLocal: "func:AuthorizeToImpact", toPath: "src/flows.go", toLocal: "func:ScopedAuthorizationDecision"},
		{fromPath: "src/flows.go", fromLocal: "func:ScopedAuthorizationDecision", toPath: "src/flows.go", toLocal: "func:AffectedCallers"},
		{fromPath: "src/flows.go", fromLocal: "func:CodeToDocs", toPath: "docs/source-view.md"},
		{fromPath: "src/flows.go", fromLocal: "func:CodeToOpenAPI", toPath: "api/source-view.openapi.json"},
		{fromPath: "src/flows.go", fromLocal: "func:CodeToSQL", toPath: "schema/embedding_profiles.sql"},
		{fromPath: "src/restart.go", fromLocal: "func:RestartRecoveryFlow", toPath: "src/restart.go", toLocal: "func:CurrentRecoveredView"},
	}
	replacements := make([]uci.IndexAdmissionEdgeReplacement, 0, len(files))
	for _, file := range files {
		edges := make([]uci.IndexAdmissionEdge, 0, 1)
		for _, link := range links {
			if link.fromPath != file.path {
				continue
			}
			fromArtifact := artifacts[link.fromPath]
			toArtifact := artifacts[link.toPath]
			fromDefinition := requireUCIRetrievalSliceDefinition(t, fromArtifact, link.fromLocal)
			toDefinition := uciProductMeasurementDefinition(t, toArtifact, link.toLocal)
			call := requireUCIRetrievalSliceCall(t, fromArtifact, fromDefinition.LocalSymbolKey)
			fromLocal := fromDefinition.LocalSymbolKey
			toLocal := toDefinition.LocalSymbolKey
			edges = append(edges, uci.IndexAdmissionEdge{
				EdgeKey:          "uci-product-" + fromLocal + "-to-" + toArtifact.ArtifactID,
				SourceArtifactID: fromArtifact.ArtifactID,
				SourceSymbolKey:  &fromLocal,
				Target: &uci.IndexAdmissionEdgeTarget{
					PathKey: link.toPath, ArtifactID: toArtifact.ArtifactID, SymbolKey: &toLocal,
				},
				Relation: "calls", EvidenceKind: "resolved", ResolutionState: "resolved", ResolverRevision: "uci-product-measurement-resolver-v1",
				Evidence: uci.IndexAdmissionEdgeEvidence{
					ReferenceSiteKey: call.SiteKey, Span: call.Span, RuleKey: "synthetic-static-call", Explanation: "source-grounded synthetic corpus relation",
				},
			})
		}
		replacements = append(replacements, uci.IndexAdmissionEdgeReplacement{SourcePath: file.path, Edges: edges})
	}

	part, err := fixture.projection.AdmitIndexFrame(fixture.context, fixture.source.SourceID, fixture.profile.ProfileID, uci.IndexAdmissionFrame{
		Version:   uci.IndexAdmissionFrameVersion,
		Profile:   uci.IndexAdmissionProfile{ID: fixture.profile.ProfileID},
		Artifacts: artifactList, Memberships: memberships, EdgeReplacements: replacements,
	})
	if err != nil {
		t.Fatalf("admit product measurement corpus %q: %v", label, err)
	}
	publisher, err := fixture.projection.Publisher(fixture.authorizer, uci.IndexPublicationConfig{
		Limits: uci.DefaultIndexPublicationLimits(), EmbeddingProfile: &fixture.vectorProfile,
	})
	if err != nil {
		t.Fatalf("create product measurement publisher: %v", err)
	}
	kind := uci.IndexJobInitial
	if parent != nil {
		kind = uci.IndexJobReconcile
	}
	build, err := publisher.Begin(fixture.context, fixture.indexCaller, uci.IndexBeginInput{
		BuildKey:  "uci-product-" + label + "-" + fixture.token,
		Scope:     uci.IndexScope{SourceID: fixture.source.SourceID, CheckoutID: checkout.CheckoutID, IncarnationID: checkout.IncarnationID},
		ProfileID: fixture.profile.ProfileID, ExpectedParent: parent, Mode: uci.IndexManifestFull, JobKind: kind,
	})
	if err != nil {
		t.Fatalf("begin product measurement publication %q: %v", label, err)
	}
	partDigest, err := uci.DigestIndexPart(part)
	if err != nil {
		t.Fatalf("digest product measurement part %q: %v", label, err)
	}
	ack, err := publisher.Stage(fixture.context, fixture.indexCaller, uci.IndexStageInput{Build: build.Build, Sequence: 0, Digest: partDigest, Part: part})
	if err != nil {
		t.Fatalf("stage product measurement publication %q: %v", label, err)
	}
	partsDigest, err := uci.DigestIndexParts([]uci.IndexPartAck{ack})
	if err != nil {
		t.Fatalf("digest product measurement staged parts %q: %v", label, err)
	}
	manifestDigest, err := uci.DigestIndexManifest(part.Memberships)
	if err != nil {
		t.Fatalf("digest product measurement manifest %q: %v", label, err)
	}
	edgesDigest, err := uci.DigestIndexEdges(part.EdgeReplacements)
	if err != nil {
		t.Fatalf("digest product measurement edges %q: %v", label, err)
	}
	fixture.scanSequence++
	scanStart := time.Date(2026, time.September, 7, 12, 0, 0, 0, time.UTC).Add(time.Duration(fixture.scanSequence) * time.Minute)
	headOID := strings.Repeat("b", 40)
	objectFormat := "sha1"
	refLabel := "refs/heads/uci-product-measurement"
	view, err := publisher.Finalize(fixture.context, fixture.indexCaller, uci.IndexFinalizeInput{
		Build: build.Build, ExpectedParent: parent,
		Manifest: uci.IndexManifestCompletion{
			PartCount: 1, PartsDigest: partsDigest, EntryCount: uint64(len(part.Memberships)), ManifestDigest: manifestDigest,
			EdgeCount: uint64(uciProductMeasurementEdgeCount(part.EdgeReplacements)), EdgesDigest: edgesDigest,
			ScanOutcome: uci.IndexScanComplete, CensusComplete: true,
			Observation: uci.IndexObservation{HeadOID: &headOID, ObjectFormat: &objectFormat, RefLabel: &refLabel, Dirty: true, ObservedFSSeq: fixture.scanSequence, ScanStart: scanStart, ScanEnd: scanStart.Add(time.Second)},
			Coverage:    uci.IndexCoverage{Structural: uci.IndexCoverageComplete, Lexical: uci.IndexCoverageComplete, Vector: uci.IndexCoverageUnavailable},
		},
	})
	if err != nil {
		t.Fatalf("finalize product measurement publication %q: %v", label, err)
	}
	return uciProductMeasurementPublication{view: view, artifacts: artifacts}
}

func (fixture *uciProductMeasurementFixture) admitArtifact(t *testing.T, file uciProductMeasurementFile) uci.IndexAdmissionArtifact {
	t.Helper()
	source := []byte(file.body)
	switch {
	case strings.HasSuffix(file.path, ".go"):
		profile := uci.GoExtractionProfile{ProfileKey: "uci-product-measurement-go", ParserKey: "go-parser-v1"}
		admissionProfile, err := uci.GoIndexAdmissionArtifactProfile(profile)
		if err != nil {
			t.Fatalf("derive Go product admission profile: %v", err)
		}
		admissionProfile.ExtractionProfileDigest = uci.IndexDigest(fixture.profile.ParserBundleDigest)
		artifact, err := uci.NewIndexAdmissionArtifactFromGo(fixture.source.SourceID, admissionProfile, source, uci.ExtractGo(source, profile))
		if err != nil {
			t.Fatalf("build Go product admission artifact: %v", err)
		}
		return artifact
	case strings.HasSuffix(file.path, ".md"):
		profile := uci.DefaultMarkdownExtractionProfile("uci-product-measurement-markdown")
		admissionProfile, err := uci.MarkdownIndexAdmissionArtifactProfile(profile)
		if err != nil {
			t.Fatalf("derive Markdown product admission profile: %v", err)
		}
		admissionProfile.ExtractionProfileDigest = uci.IndexDigest(fixture.profile.ParserBundleDigest)
		artifact, err := uci.NewIndexAdmissionArtifactFromMarkdown(fixture.source.SourceID, admissionProfile, profile, source, uci.ExtractMarkdown(source, profile))
		if err != nil {
			t.Fatalf("build Markdown product admission artifact: %v", err)
		}
		return artifact
	case strings.HasSuffix(file.path, ".openapi.json"):
		profile := uci.DefaultOpenAPIExtractionProfile("uci-product-measurement-openapi", uci.OpenAPIFormatJSON)
		admissionProfile, err := uci.OpenAPIIndexAdmissionArtifactProfile(profile)
		if err != nil {
			t.Fatalf("derive OpenAPI product admission profile: %v", err)
		}
		admissionProfile.ExtractionProfileDigest = uci.IndexDigest(fixture.profile.ParserBundleDigest)
		artifact, err := uci.NewIndexAdmissionArtifactFromOpenAPI(fixture.source.SourceID, admissionProfile, profile, source, uci.ExtractOpenAPI(source, profile))
		if err != nil {
			t.Fatalf("build OpenAPI product admission artifact: %v", err)
		}
		return artifact
	case strings.HasSuffix(file.path, ".sql"):
		profile := uci.DefaultSQLExtractionProfile("uci-product-measurement-sql")
		admissionProfile, err := uci.SQLIndexAdmissionArtifactProfile(profile)
		if err != nil {
			t.Fatalf("derive SQL product admission profile: %v", err)
		}
		admissionProfile.ExtractionProfileDigest = uci.IndexDigest(fixture.profile.ParserBundleDigest)
		artifact, err := uci.NewIndexAdmissionArtifactFromSQL(fixture.source.SourceID, admissionProfile, profile, source, uci.ExtractSQL(source, profile))
		if err != nil {
			t.Fatalf("build SQL product admission artifact: %v", err)
		}
		return artifact
	default:
		t.Fatalf("unsupported deterministic product corpus path %q", file.path)
		return uci.IndexAdmissionArtifact{}
	}
}

func uciProductMeasurementDefinition(t *testing.T, artifact uci.IndexAdmissionArtifact, local string) uci.IndexAdmissionDefinition {
	t.Helper()
	if local != "" {
		return requireUCIRetrievalSliceDefinition(t, artifact, local)
	}
	if len(artifact.Definitions) == 0 {
		t.Fatal("cross-format product corpus target has no admitted definition")
	}
	return artifact.Definitions[0]
}

func uciProductMeasurementEdgeCount(replacements []uci.IndexEdgeReplacement) int {
	count := 0
	for _, replacement := range replacements {
		count += len(replacement.Edges)
	}
	return count
}

func (fixture *uciProductMeasurementFixture) authorize(t *testing.T, resolver *uci.ContextResolver, ref uci.ContextRef) uci.AuthorizedContext {
	t.Helper()
	authorized, err := resolver.Authorize(fixture.callerContext, uci.ResolveContextInput{
		ClientSessionID: "uci-product-measurement-session-" + fixture.token,
		AuthRealm:       fixture.indexCaller.AuthRealm,
		Principal:       fixture.indexCaller.Principal,
		Ref:             &ref,
	})
	if err != nil {
		t.Fatalf("authorize product measurement view: %v", err)
	}
	if authorized.Ref() != ref {
		t.Fatal("authorized product measurement context is not the published immutable view")
	}
	return authorized
}

func (fixture *uciProductMeasurementFixture) ensureEmbeddings(t *testing.T, authorized uci.AuthorizedContext, paths []string) {
	t.Helper()
	semantic := uci.NewSemanticService(fixture.vectorProfile, mustUCIProductMeasurementEmbeddingClient(t), fixture.projection, fixture.projection)
	seen := make(map[string]struct{})
	for _, path := range paths {
		selected, err := fixture.projection.SelectCandidates(fixture.callerContext, authorized, uci.QuerySpec{
			ClientSessionID: "uci-product-measurement-session-" + fixture.token,
			Mode:            uci.QueryModeExactRelativePath, Text: path, Order: uci.QueryOrderPath, Limit: 50,
		})
		if err != nil {
			t.Fatalf("select semantic candidates for %q: %v", path, err)
		}
		for _, candidate := range selected.Candidates {
			key := candidate.Proof.ArtifactID + "\x00" + candidate.EntityKey + "\x00" + string(candidate.Proof.ContentDigest)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			if err := semantic.EnsureCandidateEmbedding(fixture.callerContext, authorized, candidate); err != nil {
				t.Fatalf("embed product measurement candidate at %q: %v", path, err)
			}
		}
	}
	if len(seen) == 0 || fixture.provider.corpusInputCount() == 0 {
		t.Fatal("product semantic corpus did not reach the local provider")
	}
}

func mustUCIProductMeasurementEmbeddingClient(t *testing.T) *embedding.Client {
	t.Helper()
	client, err := embedding.NewClient()
	if err != nil {
		t.Fatalf("create local product measurement embedding client: %v", err)
	}
	return client
}

type uciProductMeasurementObservation struct {
	tools int
	reads int
}

func (fixture *uciProductMeasurementFixture) query(t *testing.T, observation *uciProductMeasurementObservation, authorized uci.AuthorizedContext, mode uci.QueryMode, text string) uci.QueryResponse {
	t.Helper()
	observation.tools++
	result, err := uci.NewQueryService(fixture.projection).Query(fixture.callerContext, authorized, uci.QuerySpec{
		ClientSessionID: "uci-product-measurement-session-" + fixture.token, Mode: mode, Text: text, Order: uci.QueryOrderRelevance, Limit: 25,
	})
	if err != nil {
		t.Fatalf("query product measurement corpus: %v", err)
	}
	uciProductMeasurementRequireResponse(t, fmt.Sprintf("query %s %q", mode, text), result.Response, authorized.Ref())
	return result.Response
}

func (fixture *uciProductMeasurementFixture) semantic(t *testing.T, observation *uciProductMeasurementObservation, authorized uci.AuthorizedContext, text string) uci.QueryResponse {
	t.Helper()
	observation.tools++
	response, err := fixture.application.SearchCodebase(fixture.callerContext, authorized, mcp.CodebaseSearchInput{Query: text, Limit: 25})
	if err != nil {
		t.Fatalf("semantic product measurement query: %v", err)
	}
	uciProductMeasurementRequireResponse(t, "semantic query "+strconv.Quote(text), response, authorized.Ref())
	return response
}

func (fixture *uciProductMeasurementFixture) graph(t *testing.T, observation *uciProductMeasurementObservation, authorized uci.AuthorizedContext, entityKey string) uci.QueryResponse {
	t.Helper()
	observation.tools++
	response, err := fixture.application.ExploreCodebase(fixture.callerContext, authorized, mcp.CodebaseGraphInput{
		Action: uci.GraphActionNeighbors,
		Target: uci.GraphTarget{EntityKey: entityKey},
		Filter: uci.GraphFilter{Direction: uci.GraphDirectionOutgoing, Relations: []uci.IndexRelation{"calls"}},
		Budget: uci.GraphBudget{MaxDepth: 1, MaxVisited: 8, MaxNodes: 8, MaxEdges: 8},
	})
	if err != nil {
		t.Fatalf("graph product measurement corpus: %v", err)
	}
	uciProductMeasurementRequireResponse(t, "graph neighbors "+entityKey, response, authorized.Ref())
	return response
}

func (fixture *uciProductMeasurementFixture) graphPath(t *testing.T, observation *uciProductMeasurementObservation, authorized uci.AuthorizedContext, sourceEntity, destinationEntity string) uci.QueryResponse {
	t.Helper()
	observation.tools++
	destination := uci.GraphTarget{EntityKey: destinationEntity}
	response, err := fixture.application.ExploreCodebase(fixture.callerContext, authorized, mcp.CodebaseGraphInput{
		Action: uci.GraphActionPath,
		Target: uci.GraphTarget{EntityKey: sourceEntity}, Destination: &destination,
		Filter: uci.GraphFilter{Direction: uci.GraphDirectionOutgoing, Relations: []uci.IndexRelation{"calls"}},
		Budget: uci.GraphBudget{MaxDepth: 2, MaxVisited: 8, MaxNodes: 8, MaxEdges: 8},
	})
	if err != nil {
		t.Fatalf("multi-hop product measurement graph: %v", err)
	}
	uciProductMeasurementRequireResponse(t, "graph path "+sourceEntity+" -> "+destinationEntity, response, authorized.Ref())
	return response
}

func (fixture *uciProductMeasurementFixture) read(t *testing.T, observation *uciProductMeasurementObservation, authorized uci.AuthorizedContext, item uci.QueryItem) uci.QueryResponse {
	t.Helper()
	observation.reads++
	response, err := fixture.application.ReadCodebase(fixture.callerContext, authorized, mcp.CodebaseReadInput{
		Ref: item.Ref, Span: item.Span, ContentDigest: item.ContentDigest,
		MaxBytes: int(item.Span.ByteEnd - item.Span.ByteStart), VerifyWorkingCopy: true,
	})
	if err != nil {
		t.Fatalf("read exact product measurement source: %v", err)
	}
	uciProductMeasurementRequireResponse(t, "versioned read "+item.Path, response, authorized.Ref())
	return response
}

func uciProductMeasurementRequireResponse(t *testing.T, operation string, response uci.QueryResponse, ref uci.ContextRef) {
	t.Helper()
	if err := response.ValidatePreExposure(); err != nil {
		t.Fatalf("product measurement %s response is invalid before exposure: %v", operation, err)
	}
	if response.Status != uci.QueryStatusOK || response.Contexts == nil || len(*response.Contexts) != 1 {
		t.Fatalf("product measurement %s did not return one available view-bound response: status=%q contexts=%#v retrieval=%#v error=%#v", operation, response.Status, response.Contexts, response.Retrieval, response.Error)
	}
	contextRef := (*response.Contexts)[0]
	if contextRef.SourceID != ref.SourceID || contextRef.CheckoutID != ref.CheckoutID || contextRef.ViewID != ref.ViewID || contextRef.ProfileID != ref.AnalysisProfileID || contextRef.Generation != ref.Generation {
		t.Fatalf("product measurement %s response escaped its immutable selected context", operation)
	}
	if response.Items != nil {
		for _, item := range *response.Items {
			if item.Ref.SourceID != ref.SourceID || item.Ref.ViewID != ref.ViewID {
				t.Fatal("product measurement query item escaped its immutable selected context")
			}
		}
	}
	if response.Graph != nil {
		for _, edge := range response.Graph.Edges {
			if edge.From.SourceID != ref.SourceID || edge.From.ViewID != ref.ViewID || edge.To.SourceID != ref.SourceID || edge.To.ViewID != ref.ViewID {
				t.Fatal("product measurement graph edge escaped its immutable selected context")
			}
		}
	}
}

func uciProductMeasurementItem(t *testing.T, response uci.QueryResponse, path, contains string) uci.QueryItem {
	t.Helper()
	if response.Items != nil {
		for _, item := range *response.Items {
			if item.Path == path && (contains == "" || strings.Contains(item.Excerpt, contains)) {
				return item
			}
		}
	}
	t.Fatalf("observed product operation did not return required item at %q containing %q: status=%q items=%#v", path, contains, response.Status, response.Items)
	return uci.QueryItem{}
}

func uciProductMeasurementGraphEdge(t *testing.T, response uci.QueryResponse) uci.QueryGraphEdge {
	t.Helper()
	if response.Graph == nil || len(response.Graph.Edges) == 0 {
		t.Fatal("observed product graph operation did not return a relation edge")
	}
	return response.Graph.Edges[0]
}

func (fixture *uciProductMeasurementFixture) productResults(t *testing.T, corpus uciProductTaskCorpus, current, secondary uciProductMeasurementPublication, currentAuthorized, secondaryAuthorized uci.AuthorizedContext) []UCIProductTaskResult {
	t.Helper()
	tasks := make(map[string]uciProductTask, len(corpus.Tasks))
	for _, task := range corpus.Tasks {
		tasks[task.ID] = task
	}
	result := make([]UCIProductTaskResult, 0, len(corpus.Tasks))
	baseline := func(id, path, needle string) UCIProductBaselineResult {
		return fixture.baseline(t, tasks[id], currentAuthorized, path, needle)
	}

	// P01: provider-only conceptual hit for an isolated saved dirty body.
	{
		observed := uciProductMeasurementObservation{}
		response := fixture.semantic(t, &observed, currentAuthorized, uciProductMeasurementSemanticQueries[0])
		item := uciProductMeasurementItem(t, response, "src/dirty_view.go", "semantic-route:dirty")
		read := fixture.read(t, &observed, currentAuthorized, item)
		readItem := uciProductMeasurementItem(t, read, item.Path, "baseline-p01")
		semantic := fixture.semanticEvidence(t, uciProductMeasurementSemanticQueries[0])
		passed := uciProductMeasurementHasVector(item) && strings.Contains(readItem.Excerpt, "baseline-p01") && semantic.VectorDigest != ""
		result = append(result, uciProductMeasurementResult(tasks["UCI-P01"], observed, []string{"semantic_search", "bounded_read"}, false,
			[]UCIProductSourceCitation{uciProductMeasurementCitation("fixture://uci-product-corpus/dirty-view-storage", current.view.Context, item, 1)}, nil, &semantic, passed, baseline("UCI-P01", "src/dirty_view.go", "baseline-p01")))
	}

	// P02: semantic retrieval and a real source-grounded calls edge agree.
	{
		observed := uciProductMeasurementObservation{}
		response := fixture.semantic(t, &observed, currentAuthorized, uciProductMeasurementSemanticQueries[1])
		item := uciProductMeasurementItem(t, response, "src/flows.go", "semantic-route:search")
		graphItem := fixture.exactDefinition(t, &observed, current, currentAuthorized, "src/flows.go", "func:SearchToExplain")
		graph := fixture.graph(t, &observed, currentAuthorized, graphItem.Ref.EntityKey)
		edge := uciProductMeasurementGraphEdge(t, graph)
		read := fixture.read(t, &observed, currentAuthorized, item)
		readItem := uciProductMeasurementItem(t, read, item.Path, "ExplainDependencies")
		semantic := fixture.semanticEvidence(t, uciProductMeasurementSemanticQueries[1])
		citation := uciProductMeasurementCitation("fixture://uci-product-corpus/search-to-explain", current.view.Context, item, 1)
		passed := uciProductMeasurementHasVector(item) && edge.From.EntityKey == graphItem.Ref.EntityKey && strings.Contains(readItem.Excerpt, "ExplainDependencies") && semantic.VectorDigest != ""
		result = append(result, uciProductMeasurementResult(tasks["UCI-P02"], observed, []string{"semantic_search", "graph", "bounded_read"}, false,
			[]UCIProductSourceCitation{citation}, []UCIProductRelationEvidence{uciProductMeasurementRelation("fixture://uci-product-corpus/search-to-explain", citation, edge)}, &semantic, passed, baseline("UCI-P02", "src/flows.go", "baseline-p02")))
	}

	// P03: the old vector was embedded in V1; this V2 semantic result is pinned
	// to current bytes and cannot contain the old body.
	{
		observed := uciProductMeasurementObservation{}
		response := fixture.semantic(t, &observed, currentAuthorized, uciProductMeasurementSemanticQueries[2])
		item := uciProductMeasurementItem(t, response, "src/vector_current.go", "semantic-route:vector-current")
		read := fixture.read(t, &observed, currentAuthorized, item)
		readItem := uciProductMeasurementItem(t, read, item.Path, "baseline-p03")
		semantic := fixture.semanticEvidence(t, uciProductMeasurementSemanticQueries[2])
		passed := uciProductMeasurementHasVector(item) && !uciProductMeasurementResponseContains(response, "semantic-route:vector-old") && strings.Contains(readItem.Excerpt, "semantic-route:vector-current") && semantic.VectorDigest != ""
		result = append(result, uciProductMeasurementResult(tasks["UCI-P03"], observed, []string{"semantic_search", "bounded_read"}, false,
			[]UCIProductSourceCitation{uciProductMeasurementCitation("fixture://uci-product-corpus/current-version-vector", current.view.Context, item, 1)}, nil, &semantic, passed, baseline("UCI-P03", "src/vector_current.go", "baseline-p03")))
	}

	// P04: the Russian question is absent from source; only the local provider's
	// semantic route reaches the English recovery handler.
	{
		observed := uciProductMeasurementObservation{}
		response := fixture.semantic(t, &observed, currentAuthorized, uciProductMeasurementSemanticQueries[3])
		item := uciProductMeasurementItem(t, response, "src/recovery.go", "semantic-route:recovery")
		read := fixture.read(t, &observed, currentAuthorized, item)
		readItem := uciProductMeasurementItem(t, read, item.Path, "baseline-p04")
		semantic := fixture.semanticEvidence(t, uciProductMeasurementSemanticQueries[3])
		passed := uciProductMeasurementHasVector(item) && strings.Contains(readItem.Excerpt, "English handler") && semantic.VectorDigest != ""
		result = append(result, uciProductMeasurementResult(tasks["UCI-P04"], observed, []string{"semantic_search", "bounded_read"}, false,
			[]UCIProductSourceCitation{uciProductMeasurementCitation("fixture://uci-product-corpus/ru-en-recovery", current.view.Context, item, 1)}, nil, &semantic, passed, baseline("UCI-P04", "src/recovery.go", "baseline-p04")))
	}

	{
		observed := uciProductMeasurementObservation{}
		definition := uciProductMeasurementDefinition(t, current.artifacts["tests/uci/acceptance/slo_runner.go"], "func:RunUCISLOProfile")
		response := fixture.query(t, &observed, currentAuthorized, uci.QueryModeExactQualifiedSymbol, definition.SymbolKey)
		item := uciProductMeasurementItem(t, response, "tests/uci/acceptance/slo_runner.go", "RunUCISLOProfile")
		read := fixture.read(t, &observed, currentAuthorized, item)
		readItem := uciProductMeasurementItem(t, read, item.Path, "baseline-p05")
		passed := strings.Contains(readItem.Excerpt, "baseline-p05")
		result = append(result, uciProductMeasurementResult(tasks["UCI-P05"], observed, []string{"direct_read"}, true,
			[]UCIProductSourceCitation{uciProductMeasurementCitation("tests/uci/acceptance/slo_runner.go:RunUCISLOProfile", current.view.Context, item, 1)}, nil, nil, passed, baseline("UCI-P05", "tests/uci/acceptance/slo_runner.go", "baseline-p05")))
	}

	{
		observed := uciProductMeasurementObservation{}
		response := fixture.query(t, &observed, currentAuthorized, uci.QueryModeExactRelativePath, "specs/010-unified-code-intelligence/acceptance/uci-acceptance.md")
		item := uciProductMeasurementItem(t, response, "specs/010-unified-code-intelligence/acceptance/uci-acceptance.md", "Продуктовые задачи")
		read := fixture.read(t, &observed, currentAuthorized, item)
		readItem := uciProductMeasurementItem(t, read, item.Path, "Продуктовые задачи")
		passed := strings.Contains(readItem.Excerpt, "Продуктовые задачи")
		result = append(result, uciProductMeasurementResult(tasks["UCI-P06"], observed, []string{"direct_read"}, true,
			[]UCIProductSourceCitation{uciProductMeasurementCitation("specs/010-unified-code-intelligence/acceptance/uci-acceptance.md:Продуктовые задачи", current.view.Context, item, 1)}, nil, nil, passed, baseline("UCI-P06", "specs/010-unified-code-intelligence/acceptance/uci-acceptance.md", "baseline-p06")))
	}

	result = append(result, fixture.graphResult(t, tasks["UCI-P07"], current, currentAuthorized, "func:SaveToPublish", "func:PublishCurrentView", "fixture://uci-product-corpus/save-to-published-view", "src/flows.go", "SaveToPublish", baseline("UCI-P07", "src/flows.go", "baseline-p07")))
	result = append(result, fixture.graphResult(t, tasks["UCI-P08"], current, currentAuthorized, "func:AuthorizeToImpact", "func:AffectedCallers", "fixture://uci-product-corpus/authorization-impact", "src/flows.go", "AuthorizeToImpact", baseline("UCI-P08", "src/flows.go", "baseline-p08")))

	// P09 observes both documentation and OpenAPI edges from code before reading
	// the source-bound entry point, all within the registered task budget.
	{
		observed := uciProductMeasurementObservation{}
		documentation := fixture.exactDefinition(t, &observed, current, currentAuthorized, "src/flows.go", "func:CodeToDocs")
		documentationGraph := fixture.graph(t, &observed, currentAuthorized, documentation.Ref.EntityKey)
		documentationEdge := uciProductMeasurementGraphEdge(t, documentationGraph)
		openAPI := fixture.exactDefinition(t, &observed, current, currentAuthorized, "src/flows.go", "func:CodeToOpenAPI")
		openAPIGraph := fixture.graph(t, &observed, currentAuthorized, openAPI.Ref.EntityKey)
		openAPIEdge := uciProductMeasurementGraphEdge(t, openAPIGraph)
		read := fixture.read(t, &observed, currentAuthorized, documentation)
		readItem := uciProductMeasurementItem(t, read, documentation.Path, "DocumentationAnchor")
		citation := uciProductMeasurementCitation("fixture://uci-product-corpus/doc-to-source-view-schema", current.view.Context, documentation, 1)
		passed := documentationEdge.From.EntityKey == documentation.Ref.EntityKey && openAPIEdge.From.EntityKey == openAPI.Ref.EntityKey && strings.Contains(readItem.Excerpt, "DocumentationAnchor")
		result = append(result, uciProductMeasurementResult(tasks["UCI-P09"], observed, []string{"graph", "bounded_read"}, false,
			[]UCIProductSourceCitation{citation}, []UCIProductRelationEvidence{uciProductMeasurementRelation("fixture://uci-product-corpus/doc-to-source-view-schema", citation, documentationEdge)}, nil, passed, baseline("UCI-P09", "docs/source-view.md", "baseline-p09")))
	}

	{
		observed := uciProductMeasurementObservation{}
		item := fixture.exactDefinition(t, &observed, current, currentAuthorized, "src/flows.go", "func:CodeToSQL")
		graph := fixture.graph(t, &observed, currentAuthorized, item.Ref.EntityKey)
		edge := uciProductMeasurementGraphEdge(t, graph)
		read := fixture.read(t, &observed, currentAuthorized, item)
		readItem := uciProductMeasurementItem(t, read, item.Path, "SQLProfileAnchor")
		citation := uciProductMeasurementCitation("fixture://uci-product-corpus/profile-code-schema", current.view.Context, item, 1)
		passed := edge.From.EntityKey == item.Ref.EntityKey && strings.Contains(readItem.Excerpt, "SQLProfileAnchor")
		result = append(result, uciProductMeasurementResult(tasks["UCI-P10"], observed, []string{"graph", "bounded_read"}, false,
			[]UCIProductSourceCitation{citation}, []UCIProductRelationEvidence{uciProductMeasurementRelation("fixture://uci-product-corpus/profile-code-schema", citation, edge)}, nil, passed, baseline("UCI-P10", "schema/embedding_profiles.sql", "baseline-p10")))
	}

	// P11 reads the same relative path through two distinct immutable checkout
	// contexts and requires the observed persisted bodies to differ.
	{
		observed := uciProductMeasurementObservation{}
		primaryResponse := fixture.query(t, &observed, currentAuthorized, uci.QueryModeExactRelativePath, "src/worktree_dirty.go")
		primaryItem := uciProductMeasurementItem(t, primaryResponse, "src/worktree_dirty.go", "worktree-dirty-body-a")
		primaryRead := fixture.read(t, &observed, currentAuthorized, primaryItem)
		primaryReadItem := uciProductMeasurementItem(t, primaryRead, primaryItem.Path, "worktree-dirty-body-a")
		secondaryResponse := fixture.query(t, &observed, secondaryAuthorized, uci.QueryModeExactRelativePath, "src/worktree_dirty.go")
		secondaryItem := uciProductMeasurementItem(t, secondaryResponse, "src/worktree_dirty.go", "worktree-dirty-body-b")
		secondaryRead := fixture.read(t, &observed, secondaryAuthorized, secondaryItem)
		secondaryReadItem := uciProductMeasurementItem(t, secondaryRead, secondaryItem.Path, "worktree-dirty-body-b")
		primaryCitation := uciProductMeasurementCitation("fixture://uci-product-corpus/two-worktree-dirty-comparison", current.view.Context, primaryItem, 1)
		secondaryCitation := uciProductMeasurementCitation("fixture://uci-product-corpus/two-worktree-dirty-comparison", secondary.view.Context, secondaryItem, 2)
		relation := UCIProductRelationEvidence{
			EvidenceReference: "fixture://uci-product-corpus/two-worktree-dirty-comparison", CitationRank: 1, CitationContext: primaryCitation.Context,
			Path:   []string{"view:" + current.view.Context.ViewID, "view:" + secondary.view.Context.ViewID},
			Digest: uciProductMeasurementDigest(current.view.Context.ViewID + "\x00" + secondary.view.Context.ViewID),
		}
		passed := current.view.Context.CheckoutID != secondary.view.Context.CheckoutID && primaryReadItem.Excerpt != secondaryReadItem.Excerpt && strings.Contains(primaryReadItem.Excerpt, "worktree-dirty-body-a") && strings.Contains(secondaryReadItem.Excerpt, "worktree-dirty-body-b")
		result = append(result, uciProductMeasurementResult(tasks["UCI-P11"], observed, []string{"bounded_read"}, false,
			[]UCIProductSourceCitation{primaryCitation, secondaryCitation}, []UCIProductRelationEvidence{relation}, nil, passed, baseline("UCI-P11", "src/worktree_dirty.go", "baseline-p11")))
	}

	// P12 is deliberately performed only after recomposition above, with the
	// incremented durable V2 ContextRef selected again through the new resolver.
	{
		observed := uciProductMeasurementObservation{}
		item := fixture.exactDefinition(t, &observed, current, currentAuthorized, "src/restart.go", "func:RestartRecoveryFlow")
		target := fixture.exactDefinition(t, &observed, current, currentAuthorized, "src/restart.go", "func:CurrentRecoveredView")
		graph := fixture.graph(t, &observed, currentAuthorized, item.Ref.EntityKey)
		edge := uciProductMeasurementGraphEdge(t, graph)
		observed.tools++
		status, err := fixture.status.Status(fixture.callerContext, currentAuthorized, "")
		if err != nil {
			t.Fatalf("read post-recomposition current-view status: %v", err)
		}
		if err := status.Validate(); err != nil {
			t.Fatalf("validate post-recomposition current-view status: %v", err)
		}
		read := fixture.read(t, &observed, currentAuthorized, target)
		readItem := uciProductMeasurementItem(t, read, target.Path, "restart-generation-two-current")
		citation := uciProductMeasurementCitation("fixture://uci-product-corpus/increment-restart-current-view", current.view.Context, item, 1)
		passed := current.view.Context.Generation > 1 && status.Context == current.view.Context && edge.From.EntityKey == item.Ref.EntityKey && edge.To.EntityKey == target.Ref.EntityKey && strings.Contains(readItem.Excerpt, "restart-generation-two-current")
		result = append(result, uciProductMeasurementResult(tasks["UCI-P12"], observed, []string{"graph", "bounded_read"}, false,
			[]UCIProductSourceCitation{citation}, []UCIProductRelationEvidence{uciProductMeasurementRelation("fixture://uci-product-corpus/increment-restart-current-view", citation, edge)}, nil, passed, baseline("UCI-P12", "src/restart.go", "baseline-p12")))
	}
	return result
}

func (fixture *uciProductMeasurementFixture) exactDefinition(t *testing.T, observed *uciProductMeasurementObservation, publication uciProductMeasurementPublication, authorized uci.AuthorizedContext, path, local string) uci.QueryItem {
	t.Helper()
	definition := uciProductMeasurementDefinition(t, publication.artifacts[path], local)
	response := fixture.query(t, observed, authorized, uci.QueryModeExactQualifiedSymbol, definition.SymbolKey)
	return uciProductMeasurementItem(t, response, path, strings.TrimPrefix(local, "func:"))
}

func (fixture *uciProductMeasurementFixture) graphResult(t *testing.T, task uciProductTask, publication uciProductMeasurementPublication, authorized uci.AuthorizedContext, sourceLocal, destinationLocal, evidenceReference, path, itemNeedle string, baseline UCIProductBaselineResult) UCIProductTaskResult {
	t.Helper()
	observed := uciProductMeasurementObservation{}
	item := fixture.exactDefinition(t, &observed, publication, authorized, path, sourceLocal)
	destination := fixture.exactDefinition(t, &observed, publication, authorized, path, destinationLocal)
	graph := fixture.graphPath(t, &observed, authorized, item.Ref.EntityKey, destination.Ref.EntityKey)
	first, second := uciProductMeasurementTwoHop(t, graph, item.Ref.EntityKey, destination.Ref.EntityKey)
	read := fixture.read(t, &observed, authorized, item)
	readItem := uciProductMeasurementItem(t, read, item.Path, itemNeedle)
	citation := uciProductMeasurementCitation(evidenceReference, publication.view.Context, item, 1)
	passed := strings.Contains(readItem.Excerpt, itemNeedle)
	return uciProductMeasurementResult(task, observed, []string{"graph", "bounded_read"}, false,
		[]UCIProductSourceCitation{citation}, []UCIProductRelationEvidence{uciProductMeasurementTwoHopRelation(evidenceReference, citation, first, second)}, nil, passed, baseline)
}

func uciProductMeasurementTwoHop(t *testing.T, response uci.QueryResponse, sourceEntity, destinationEntity string) (uci.QueryGraphEdge, uci.QueryGraphEdge) {
	t.Helper()
	if response.Graph != nil {
		for _, first := range response.Graph.Edges {
			if first.From.EntityKey != sourceEntity {
				continue
			}
			for _, second := range response.Graph.Edges {
				if first.To.EntityKey == second.From.EntityKey && second.To.EntityKey == destinationEntity {
					return first, second
				}
			}
		}
	}
	t.Fatal("observed product graph did not retain the required two-hop relation")
	return uci.QueryGraphEdge{}, uci.QueryGraphEdge{}
}

func uciProductMeasurementTwoHopRelation(reference string, citation UCIProductSourceCitation, first, second uci.QueryGraphEdge) UCIProductRelationEvidence {
	path := []string{first.From.EntityKey, first.To.EntityKey, second.To.EntityKey}
	return UCIProductRelationEvidence{
		EvidenceReference: reference, CitationRank: citation.Rank, CitationContext: citation.Context, Path: path,
		Digest: uciProductMeasurementDigest(strings.Join(path, "\x00")),
	}
}

func (fixture *uciProductMeasurementFixture) baseline(t *testing.T, task uciProductTask, authorized uci.AuthorizedContext, path, needle string) UCIProductBaselineResult {
	t.Helper()
	observed := uciProductMeasurementObservation{}
	response := fixture.query(t, &observed, authorized, uci.QueryModeFTS, "baselinefanout")
	item := uciProductMeasurementItem(t, response, path, needle)
	read := fixture.read(t, &observed, authorized, item)
	readItem := uciProductMeasurementItem(t, read, path, needle)
	extraReads := 1
	if task.ID == "UCI-P11" {
		extraReads = 2
	}
	if response.Items != nil {
		for _, candidate := range *response.Items {
			if extraReads == 0 {
				break
			}
			if candidate.Path == item.Path {
				continue
			}
			_ = fixture.read(t, &observed, authorized, candidate)
			extraReads--
		}
	}
	if extraReads != 0 {
		t.Fatal("bounded baseline did not expose enough distinct source reads")
	}
	correct := strings.Contains(readItem.Excerpt, needle)
	return UCIProductBaselineResult{
		TaskID: task.ID, Question: task.Question, ToolCount: &observed.tools, ReadCount: &observed.reads, Correct: &correct,
	}
}

func (fixture *uciProductMeasurementFixture) semanticEvidence(t *testing.T, query string) UCIProductSemanticEvidence {
	t.Helper()
	digest, found := fixture.provider.queryVectorDigest(query)
	if !found || digest == "" || fixture.provider.queryInputCount() == 0 {
		t.Fatal("semantic task has no observed local provider vector digest")
	}
	return UCIProductSemanticEvidence{Provider: UCIProductMeasurementProvider, Model: UCIProductMeasurementModel, Profile: UCIProductMeasurementProfile, VectorDigest: digest}
}

func uciProductMeasurementResult(task uciProductTask, observed uciProductMeasurementObservation, modes []string, direct bool, citations []UCIProductSourceCitation, relations []UCIProductRelationEvidence, semantic *UCIProductSemanticEvidence, passed bool, baseline UCIProductBaselineResult) UCIProductTaskResult {
	outcome := "failure"
	reason := "observed_evidence_incomplete"
	if passed {
		outcome = "success"
		reason = ""
	}
	return UCIProductTaskResult{
		ID: task.ID, Question: task.Question, Outcome: outcome, ExecutionModes: modes,
		ToolCount: &observed.tools, ReadCount: &observed.reads,
		TopFive: citations, RelationEvidence: relations, DirectReadUsed: &direct, SemanticProviderEvidence: semantic,
		Baseline: baseline, Reason: reason,
	}
}

func uciProductMeasurementCitation(reference string, contextRef uci.ContextRef, item uci.QueryItem, rank int) UCIProductSourceCitation {
	return UCIProductSourceCitation{
		Rank: rank, EvidenceReference: reference, Context: uciProductMeasurementContext(contextRef), Path: item.Path,
		Span: UCIProductSpan{ByteStart: item.Span.ByteStart, ByteEnd: item.Span.ByteEnd, LineStart: item.Span.LineStart, LineEnd: item.Span.LineEnd}, Digest: string(item.ContentDigest),
	}
}

func uciProductMeasurementContext(ref uci.ContextRef) UCIProductContextRef {
	contextRef := UCIProductContextRef{SourceID: ref.SourceID, CheckoutID: ref.CheckoutID, ViewID: ref.ViewID, Generation: ref.Generation, AnalysisProfileID: ref.AnalysisProfileID}
	if ref.SpaceID != nil {
		contextRef.SpaceID = *ref.SpaceID
	}
	return contextRef
}

func uciProductMeasurementRelation(reference string, citation UCIProductSourceCitation, edge uci.QueryGraphEdge) UCIProductRelationEvidence {
	path := []string{edge.From.EntityKey, edge.To.EntityKey}
	return UCIProductRelationEvidence{
		EvidenceReference: reference, CitationRank: citation.Rank, CitationContext: citation.Context, Path: path,
		Digest: uciProductMeasurementDigest(strings.Join(path, "\x00")),
	}
}

func uciProductMeasurementHasVector(item uci.QueryItem) bool {
	for _, source := range item.MatchSources {
		if source == uci.QueryMatchVector {
			return true
		}
	}
	return false
}

func uciProductMeasurementResponseContains(response uci.QueryResponse, value string) bool {
	if response.Items == nil {
		return false
	}
	for _, item := range *response.Items {
		if strings.Contains(item.Excerpt, value) {
			return true
		}
	}
	return false
}

type uciProductMeasurementEmbeddingProvider struct {
	server *httptest.Server

	mu           sync.Mutex
	corpusInputs int
	queryInputs  int
	queryDigests map[string]string
}

func newUCIProductMeasurementEmbeddingProvider(t *testing.T) *uciProductMeasurementEmbeddingProvider {
	t.Helper()
	provider := &uciProductMeasurementEmbeddingProvider{queryDigests: make(map[string]string)}
	provider.server = httptest.NewServer(http.HandlerFunc(provider.serveHTTP(t)))
	t.Cleanup(provider.server.Close)
	return provider
}

func (provider *uciProductMeasurementEmbeddingProvider) serveHTTP(t *testing.T) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/embeddings" || request.Header.Get("Authorization") != "Bearer "+uciProductMeasurementProviderCredential {
			http.Error(writer, "local embedding contract rejected", http.StatusBadRequest)
			return
		}
		var payload struct {
			Model      string   `json:"model"`
			Dimensions int      `json:"dimensions"`
			Input      []string `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.Model != UCIProductMeasurementModel || payload.Dimensions != embedding.EmbeddingDim || len(payload.Input) == 0 {
			http.Error(writer, "local embedding request rejected", http.StatusBadRequest)
			return
		}

		data := make([]map[string]any, len(payload.Input))
		provider.mu.Lock()
		for index, input := range payload.Input {
			vector := uciProductMeasurementVector(input)
			digest := uciProductMeasurementVectorDigest(vector)
			if strings.Contains(input, `"content_digest"`) {
				provider.corpusInputs++
			} else {
				provider.queryInputs++
				for _, query := range uciProductMeasurementSemanticQueries {
					if strings.Contains(input, query) {
						provider.queryDigests[query] = digest
					}
				}
			}
			data[index] = map[string]any{"embedding": vector, "index": index}
		}
		provider.mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(map[string]any{"data": data}); err != nil {
			t.Errorf("encode local product embedding response: %v", err)
		}
	}
}

func uciProductMeasurementVector(input string) []float32 {
	vector := make([]float32, embedding.EmbeddingDim)
	for index, query := range uciProductMeasurementSemanticQueries {
		if strings.Contains(input, query) {
			vector[index] = 1
			return vector
		}
	}
	for index, route := range uciProductMeasurementSemanticRoutes {
		if strings.Contains(input, route) {
			vector[index] = 1
			return vector
		}
	}
	vector[len(uciProductMeasurementSemanticRoutes)] = 1
	return vector
}

func uciProductMeasurementVectorDigest(vector []float32) string {
	encoded, err := json.Marshal(vector)
	if err != nil {
		panic(fmt.Sprintf("marshal deterministic local vector: %v", err))
	}
	return uciProductMeasurementDigest(string(encoded))
}

func (provider *uciProductMeasurementEmbeddingProvider) corpusInputCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.corpusInputs
}

func (provider *uciProductMeasurementEmbeddingProvider) queryInputCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.queryInputs
}

func (provider *uciProductMeasurementEmbeddingProvider) queryVectorDigest(query string) (string, bool) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	digest, found := provider.queryDigests[query]
	return digest, found
}

type uciProductMeasurementCandidateIdentity struct {
	branch string
	commit string
	tree   string
}

func uciProductMeasurementRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve product measurement source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", "..", ".."))
}

func uciProductMeasurementCandidate(t *testing.T, repositoryRoot string) uciProductMeasurementCandidateIdentity {
	t.Helper()
	readGit := func(args ...string) string {
		command := exec.Command("git", append([]string{"-C", repositoryRoot}, args...)...)
		output, err := command.Output()
		if err != nil {
			t.Fatalf("derive candidate Git identity through %q: %v", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(string(output))
	}
	identity := uciProductMeasurementCandidateIdentity{
		branch: readGit("branch", "--show-current"),
		commit: readGit("rev-parse", "HEAD"),
		tree:   readGit("rev-parse", "HEAD^{tree}"),
	}
	if identity.branch == "" || !validUCIProductGitRevision(identity.commit) || !validUCIProductGitRevision(identity.tree) {
		t.Fatal("candidate git identity is incomplete")
	}
	return identity
}

func uciProductMeasurementBuildCandidateArtifacts(t *testing.T, repositoryRoot string) []UCIProductArtifactDigest {
	t.Helper()
	outputDirectory := t.TempDir()
	artifacts := []struct {
		name   string
		module string
	}{
		{name: "engram-server", module: "./cmd/engram-server"},
		{name: "engram-daemon", module: "./cmd/engram"},
		{name: "uci-parser", module: "./tools/uci-parser"},
	}
	result := make([]UCIProductArtifactDigest, 0, len(artifacts))
	for _, artifact := range artifacts {
		output := filepath.Join(outputDirectory, artifact.name)
		if runtime.GOOS == "windows" {
			output += ".exe"
		}
		command := exec.Command("go", "build", "-o", output, artifact.module)
		command.Dir = repositoryRoot
		command.Env = append(os.Environ(), "CGO_ENABLED=1")
		if err := command.Run(); err != nil {
			t.Fatalf("build candidate %s artifact: %v", artifact.name, err)
		}
		digest, err := uciProductMeasurementFileDigest(output)
		if err != nil {
			t.Fatalf("hash candidate %s artifact: %v", artifact.name, err)
		}
		result = append(result, UCIProductArtifactDigest{Name: artifact.name, Digest: digest})
	}
	return result
}

func uciProductMeasurementFileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func uciProductMeasurementWriteAtomic(path string, input UCIProductTaskResultInput) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode strict product input: %w", err)
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".uci-product-result-*")
	if err != nil {
		return fmt.Errorf("create caller-path temporary result: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary product result: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary product result: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary product result: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("rename temporary product result: %w", err)
	}
	return nil
}

func uciProductMeasurementDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
