package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
)

const (
	uciSLORecordPathEnv       = "ENGRAM_UCI_SLO_RECORD_PATH"
	uciSLORecordProfileID     = "uci-slo-local-profile-v1"
	uciSLORecordCorpusID      = "uci-slo-bounded-corpus-v1"
	uciSLORecordProviderID    = "uci-local-http-embedding-v1"
	uciSLORecordProviderModel = "uci-retrieval-local-model"

	uciSLORecordMinimumHealthySamples = uci.UCISLOMinimumHealthySamples
	uciSLORecordMaximumAttempts       = uciSLORecordMinimumHealthySamples + 20

	uciSLORecordStructuralWaitBound = 4 * time.Second
	uciSLORecordEmbeddingWaitBound  = 12 * time.Second
	uciSLORecordSearchWaitBound     = 2 * time.Second
	uciSLORecordQueryWaitBound      = 4 * time.Second
	uciSLORecordGraphWaitBound      = 3 * time.Second
)

type uciSLORecordedAttempt struct {
	outcome  string
	status   string
	coverage string
	reason   string
	latency  time.Duration
}

type uciSLOProviderCallState struct {
	requests int
	corpus   int
	query    int
	failed   int
}

type uciSLORecordCorpus struct {
	fixture        *uciRetrievalSliceFixture
	publisher      uci.IndexStore
	current        uci.IndexPublishedView
	manifestDigest uci.IndexDigest
	graphEntry     string
	memberships    map[string]uci.IndexMembership
	replacements   map[string]uci.IndexEdgeReplacement
	markdownBytes  []byte
	maxChanged     int64
}

func TestUCIRecordSLOMeasurement(t *testing.T) {
	recordPath := strings.TrimSpace(os.Getenv(uciSLORecordPathEnv))
	if recordPath == "" {
		t.Skipf("%s not set; skipping UCI SLO measurement recording", uciSLORecordPathEnv)
	}
	if strings.TrimSpace(os.Getenv("DATABASE_DSN")) == "" {
		t.Skip("DATABASE_DSN not set; UCI SLO measurement recording requires a caller-supplied disposable PostgreSQL database")
	}

	candidate, err := uciSLORecordCandidate(t)
	if err != nil {
		t.Fatalf("build exact candidate artifacts: %v", err)
	}

	fixture := newUCIRetrievalSliceFixture(t)
	activeCheckouts, inactiveRegistrations, err := uciSLORegisterCheckoutCoverage(fixture)
	if err != nil {
		t.Fatalf("register isolated SLO checkout coverage: %v", err)
	}

	corpus, err := newUCISLORecordCorpus(t, fixture)
	if err != nil {
		t.Fatalf("publish deterministic bounded SLO corpus: %v", err)
	}
	contextID := uciSLORecordContextID(fixture)

	coldAuthorization, err := corpus.authorize(fixture.callerContext)
	if err != nil {
		t.Fatalf("authorize cold SLO context: %v", err)
	}
	coldSearch := uciSLORecordServerSearch(fixture, coldAuthorization, "ucislobasefts")

	baseAuthorization, err := corpus.authorize(fixture.callerContext)
	if err != nil {
		t.Fatalf("authorize SLO warm-up context: %v", err)
	}
	_ = uciSLOEnsurePathEmbedding(fixture.callerContext, fixture, baseAuthorization, "src/slo_measurement.go")
	_ = uciSLOEnsurePathEmbedding(fixture.callerContext, fixture, baseAuthorization, "docs/slo_update.md")

	localRTT, localRTTErr := uciSLOMeasureLocalProviderRTT(fixture)

	structuralAttempts := make([]uciSLORecordedAttempt, 0, uciSLORecordMaximumAttempts)
	embeddingAttempts := make([]uciSLORecordedAttempt, 0, uciSLORecordMaximumAttempts)
	for sequence := 1; sequence <= uciSLORecordMaximumAttempts &&
		(uciSLOHealthyAttemptCount(structuralAttempts) < uciSLORecordMinimumHealthySamples || uciSLOHealthyAttemptCount(embeddingAttempts) < uciSLORecordMinimumHealthySamples); sequence++ {
		structural, embeddingAttempt := corpus.updateAttempt(sequence)
		structuralAttempts = append(structuralAttempts, structural)
		embeddingAttempts = append(embeddingAttempts, embeddingAttempt)
	}

	currentAuthorization, err := corpus.authorize(fixture.callerContext)
	if err != nil {
		t.Fatalf("authorize warmed SLO context: %v", err)
	}
	serverSearchAttempts := uciSLOCollectWarmAttempts(uciSLORecordMaximumAttempts, func() uciSLORecordedAttempt {
		return uciSLORecordServerSearch(fixture, currentAuthorization, "ucislobasefts")
	})
	queryEmbeddingAttempts := uciSLOCollectWarmAttempts(uciSLORecordMaximumAttempts, func() uciSLORecordedAttempt {
		return uciSLORecordQueryEmbedding(fixture, currentAuthorization)
	})
	graphAttempts := uciSLOCollectWarmAttempts(uciSLORecordMaximumAttempts, func() uciSLORecordedAttempt {
		return uciSLORecordGraph(fixture, currentAuthorization, corpus.graphEntrySymbol())
	})

	textFiles, linesOfCode, err := uciSLOCorpusCounts(fixture)
	if err != nil {
		t.Fatalf("measure actual SLO corpus size: %v", err)
	}
	databaseVersion, databaseSize, err := uciSLODatabaseMeasurement(fixture)
	if err != nil {
		t.Fatalf("measure PostgreSQL version and data size: %v", err)
	}
	providerCalls := uciSLOProviderCalls(fixture.provider)
	providerStatus := "healthy"
	if localRTTErr != nil || providerCalls.requests == 0 || providerCalls.corpus == 0 || providerCalls.query == 0 || providerCalls.failed != 0 {
		providerStatus = "unavailable"
	}

	environment := uci.UCISLOEnvironment{
		Host: uci.UCISLOHost{ID: "local-uci-slo-recorder"},
		Database: uci.UCISLODatabase{
			ID:            "postgresql-isolated-schema",
			Version:       databaseVersion,
			DataSizeBytes: databaseSize,
		},
		Corpus: uci.UCISLOCorpus{
			ID:             uciSLORecordCorpusID,
			ManifestDigest: string(corpus.manifestDigest),
		},
		Provider: uci.UCISLOProvider{
			ID:     uciSLORecordProviderID,
			Model:  uciSLORecordProviderModel,
			Status: providerStatus,
		},
	}
	profile := uci.UCISLOProfile{
		ID:                        uciSLORecordProfileID,
		TextFileCount:             textFiles,
		LinesOfCode:               linesOfCode,
		ActiveWorktreeCount:       activeCheckouts,
		InactiveRegistrationCount: inactiveRegistrations,
		LANRTT:                    localRTT,
		ChangedFileCount:          1,
		ChangedBytes:              corpus.maxChanged,
	}
	identity := uci.UCISLOSampleIdentity{Candidate: candidate, Environment: environment}
	input := uci.UCISLOMeasurementInput{
		SchemaVersion: uci.UCISLOReportSchemaVersion,
		Candidate:     candidate,
		Environment:   environment,
		Profile:       profile,
		Gates: []uci.UCISLOGateInput{
			uciSLORecordGate("update.structural_fts", "update", uciSLORecordStructuralWaitBound, contextID, identity, structuralAttempts, 0, nil),
			uciSLORecordGate("update.embedding_readiness", "update", uciSLORecordEmbeddingWaitBound, contextID, identity, embeddingAttempts, 0, nil),
			uciSLORecordGate("search.server_retrieval", "search", uciSLORecordSearchWaitBound, contextID, identity, serverSearchAttempts, 0, nil),
			uciSLORecordGate("search.query_embedding", "search", uciSLORecordQueryWaitBound, contextID, identity, queryEmbeddingAttempts, 0, nil),
			uciSLORecordGate("graph.small_explain_neighbors_impact", "graph", uciSLORecordGraphWaitBound, contextID, identity, graphAttempts, 4, map[string]int{"max_visited": 16, "max_nodes": 16, "max_edges": 16}),
		},
		ColdObservations: []uci.UCISLOColdObservation{{
			Operation:   "search",
			Warmth:      "cold",
			ProfileID:   profile.ID,
			ContextID:   contextID,
			WaitBound:   uciSLORecordSearchWaitBound,
			SampleCount: 1,
			Samples:     []uci.UCISLOSample{uciSLORecordSample("cold.search-001", "cold", profile.ID, contextID, identity, coldSearch)},
		}},
	}

	report, calculateErr := uci.CalculateUCISLOReport(input)
	violations := uci.ValidateUCISLOReport(report)
	if err := uciSLOWriteInputAtomically(recordPath, input); err != nil {
		t.Fatalf("write UCI SLO measurement input atomically: %v", err)
	}
	if calculateErr != nil {
		t.Fatalf("calculate recorded UCI SLO report: %v", calculateErr)
	}
	if len(violations) != 0 {
		t.Fatalf("recorded UCI SLO report violations = %#v", violations)
	}
	if !report.Passed {
		t.Fatal("recorded UCI SLO report is not accepted")
	}
}

func uciSLORecordCandidate(t *testing.T) (uci.UCISLOCandidate, error) {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		return uci.UCISLOCandidate{}, fmt.Errorf("resolve SLO measurement source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", "..", ".."))
	branch, err := uciSLOGit(root, "branch", "--show-current")
	if err != nil {
		return uci.UCISLOCandidate{}, err
	}
	if branch == "" {
		return uci.UCISLOCandidate{}, fmt.Errorf("candidate branch is empty")
	}
	commit, err := uciSLOGit(root, "rev-parse", "HEAD")
	if err != nil {
		return uci.UCISLOCandidate{}, err
	}
	tree, err := uciSLOGit(root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return uci.UCISLOCandidate{}, err
	}

	outputDirectory := t.TempDir()
	artifacts := []struct {
		name        string
		packagePath string
		cgo         bool
	}{
		{name: "engram-daemon", packagePath: "./cmd/engram"},
		{name: "engram-server", packagePath: "./cmd/engram-server"},
		{name: "uci-parser", packagePath: "./tools/uci-parser", cgo: true},
	}
	digests := make([]uci.UCISLOArtifactDigest, 0, len(artifacts))
	for _, artifact := range artifacts {
		outputPath := filepath.Join(outputDirectory, artifact.name)
		if runtime.GOOS == "windows" {
			outputPath += ".exe"
		}
		command := exec.Command("go", "build", "-o", outputPath, artifact.packagePath)
		command.Dir = root
		if artifact.cgo {
			command.Env = append(os.Environ(), "CGO_ENABLED=1")
		}
		if err := command.Run(); err != nil {
			return uci.UCISLOCandidate{}, fmt.Errorf("build %s", artifact.name)
		}
		digest, err := uciSLOSHA256File(outputPath)
		if err != nil {
			return uci.UCISLOCandidate{}, fmt.Errorf("hash %s: %w", artifact.name, err)
		}
		digests = append(digests, uci.UCISLOArtifactDigest{Name: artifact.name, Digest: digest})
	}
	return uci.UCISLOCandidate{Branch: branch, Commit: commit, Tree: tree, ArtifactDigests: digests}, nil
}

func uciSLOGit(root string, arguments ...string) (string, error) {
	commandArguments := append([]string{"-C", root}, arguments...)
	output, err := exec.Command("git", commandArguments...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s failed", strings.Join(arguments, " "))
	}
	return strings.TrimSpace(string(output)), nil
}

func uciSLOSHA256File(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func uciSLORegisterCheckoutCoverage(fixture *uciRetrievalSliceFixture) (int, int, error) {
	for index := 1; index <= 5; index++ {
		_, err := fixture.contextStore.RegisterCheckout(fixture.context, gormstore.RegisterCheckoutInput{
			SourceID:       fixture.source.SourceID,
			WorkstationID:  fmt.Sprintf("00000000-0000-4000-8000-%012d", index),
			Kind:           gormstore.UCICheckoutWorkingTree,
			OwnerPrincipal: fmt.Sprintf("uci-slo-active-%d", index),
			LocatorRef:     fmt.Sprintf("fixture://uci-slo/active/%d", index),
		})
		if err != nil {
			return 0, 0, fmt.Errorf("register active checkout %d: %w", index, err)
		}
	}
	inactive, err := fixture.contextStore.RegisterCheckout(fixture.context, gormstore.RegisterCheckoutInput{
		SourceID:       fixture.source.SourceID,
		WorkstationID:  "00000000-0000-4000-8000-000000000099",
		Kind:           gormstore.UCICheckoutWorkingTree,
		OwnerPrincipal: "uci-slo-inactive",
		LocatorRef:     "fixture://uci-slo/inactive",
	})
	if err != nil {
		return 0, 0, fmt.Errorf("register inactive checkout: %w", err)
	}
	result := fixture.store.GetDB().WithContext(fixture.context).
		Model(&gormstore.UCICheckout{}).
		Where("checkout_id = ?", inactive.CheckoutID).
		Update("state", gormstore.UCICheckoutUnregistered)
	if result.Error != nil || result.RowsAffected != 1 {
		return 0, 0, fmt.Errorf("mark inactive checkout unregistered")
	}

	var activeCount, inactiveCount int64
	db := fixture.store.GetDB().WithContext(fixture.context)
	if err := db.Model(&gormstore.UCICheckout{}).
		Where("source_id = ? AND state IN ?", fixture.source.SourceID, []gormstore.UCICheckoutState{gormstore.UCICheckoutRegistered, gormstore.UCICheckoutWatching, gormstore.UCICheckoutCatchingUp}).
		Count(&activeCount).Error; err != nil {
		return 0, 0, fmt.Errorf("count active checkouts: %w", err)
	}
	if err := db.Model(&gormstore.UCICheckout{}).
		Where("source_id = ? AND state = ?", fixture.source.SourceID, gormstore.UCICheckoutUnregistered).
		Count(&inactiveCount).Error; err != nil {
		return 0, 0, fmt.Errorf("count inactive registrations: %w", err)
	}
	if activeCount < 5 || inactiveCount < 1 {
		return 0, 0, fmt.Errorf("incomplete checkout coverage")
	}
	return int(activeCount), int(inactiveCount), nil
}

func newUCISLORecordCorpus(t *testing.T, fixture *uciRetrievalSliceFixture) (*uciSLORecordCorpus, error) {
	t.Helper()
	goArtifact := fixture.goArtifact(t, []byte(`package slomeasurement

func SLOMeasurementEntry() string {
	return SLOMeasurementTarget()
}

func SLOMeasurementTarget() string {
	return "ucislobasefts semanticvectortarget"
}
`))
	markdownBytes := uciSLOMarkdownSource(0)
	markdownArtifact := fixture.markdownArtifact(t, markdownBytes)
	entry := requireUCIRetrievalSliceDefinition(t, goArtifact, "func:SLOMeasurementEntry")
	target := requireUCIRetrievalSliceDefinition(t, goArtifact, "func:SLOMeasurementTarget")
	call := requireUCIRetrievalSliceCall(t, goArtifact, entry.LocalSymbolKey)
	goArtifactID := goArtifact.ArtifactID
	markdownArtifactID := markdownArtifact.ArtifactID
	entrySymbol := entry.LocalSymbolKey
	targetSymbol := target.LocalSymbolKey
	memberships := []uci.IndexAdmissionMembership{
		{PathKey: "src/slo_measurement.go", DisplayPath: "src/slo_measurement.go", Mode: "100644", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &goArtifactID},
		{PathKey: "docs/slo_update.md", DisplayPath: "docs/slo_update.md", Mode: "100644", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &markdownArtifactID},
	}
	replacements := []uci.IndexAdmissionEdgeReplacement{
		{
			SourcePath: "src/slo_measurement.go",
			Edges: []uci.IndexAdmissionEdge{{
				EdgeKey:          "slo-entry-calls-target",
				SourceArtifactID: goArtifactID,
				SourceSymbolKey:  &entrySymbol,
				Target: &uci.IndexAdmissionEdgeTarget{
					PathKey:    "src/slo_measurement.go",
					ArtifactID: goArtifactID,
					SymbolKey:  &targetSymbol,
				},
				Relation:         "calls",
				EvidenceKind:     "resolved",
				ResolutionState:  "resolved",
				ResolverRevision: "uci-slo-measurement-resolver-v1",
				Evidence: uci.IndexAdmissionEdgeEvidence{
					ReferenceSiteKey: call.SiteKey,
					Span:             call.Span,
					RuleKey:          "go-static-call",
					Explanation:      "same-artifact static call",
				},
			}},
		},
		{SourcePath: "docs/slo_update.md", Edges: []uci.IndexAdmissionEdge{}},
	}
	part, err := fixture.projection.AdmitIndexFrame(fixture.context, fixture.source.SourceID, fixture.profile.ProfileID, uci.IndexAdmissionFrame{
		Version:          uci.IndexAdmissionFrameVersion,
		Profile:          uci.IndexAdmissionProfile{ID: fixture.profile.ProfileID},
		Artifacts:        []uci.IndexAdmissionArtifact{goArtifact, markdownArtifact},
		Memberships:      memberships,
		EdgeReplacements: replacements,
	})
	if err != nil {
		return nil, fmt.Errorf("admit bounded SLO corpus: %w", err)
	}
	publisher, err := fixture.projection.Publisher(fixture.authorizer, uci.IndexPublicationConfig{
		Limits:           uci.DefaultIndexPublicationLimits(),
		EmbeddingProfile: &fixture.vectorProfile,
	})
	if err != nil {
		return nil, fmt.Errorf("create SLO publisher: %w", err)
	}
	corpus := &uciSLORecordCorpus{
		fixture:       fixture,
		publisher:     publisher,
		graphEntry:    entry.SymbolKey,
		memberships:   uciSLOMembershipMap(part.Memberships),
		replacements:  uciSLOReplacementMap(part.EdgeReplacements),
		markdownBytes: markdownBytes,
	}
	published, err := corpus.publish(fixture.context, "uci-slo-initial", uci.IndexManifestFull, uci.IndexJobInitial, nil, part, 1)
	if err != nil {
		return nil, fmt.Errorf("publish bounded SLO corpus: %w", err)
	}
	corpus.current = published
	corpus.manifestDigest = published.ManifestDigest
	return corpus, nil
}

func (corpus *uciSLORecordCorpus) updateAttempt(sequence int) (uciSLORecordedAttempt, uciSLORecordedAttempt) {
	started := time.Now()
	structuralContext, cancelStructural := context.WithTimeout(corpus.fixture.callerContext, uciSLORecordStructuralWaitBound)
	published, err := corpus.publishUpdate(structuralContext, sequence)
	if err != nil {
		cancelStructural()
		latency := time.Since(started)
		return uciSLOFailedAttempt(latency), uciSLOUnavailableAttempt(latency, "structural_update_failed")
	}
	corpus.current = published
	authorized, err := corpus.authorize(structuralContext)
	if err != nil {
		cancelStructural()
		latency := time.Since(started)
		return uciSLOFailedAttempt(latency), uciSLOUnavailableAttempt(latency, "context_authorization_failed")
	}
	structuralResponse, err := corpus.fixture.queryService.Query(structuralContext, authorized, uci.QuerySpec{
		ClientSessionID: corpus.fixture.clientSessionID,
		Mode:            uci.QueryModeFTS,
		Text:            uciSLOUpdateMarker(sequence),
		Order:           uci.QueryOrderRelevance,
		Limit:           10,
	})
	structural := uciSLOClassifyQueryAttempt(time.Since(started), structuralResponse.Response, err, uci.QueryRetrievalLexical)
	cancelStructural()

	embeddingContext, cancelEmbedding := context.WithTimeout(corpus.fixture.callerContext, uciSLORecordEmbeddingWaitBound)
	before := uciSLOProviderCalls(corpus.fixture.provider)
	err = uciSLOEnsurePathEmbedding(embeddingContext, corpus.fixture, authorized, "docs/slo_update.md")
	after := uciSLOProviderCalls(corpus.fixture.provider)
	embedding := uciSLOClassifyEmbeddingAttempt(time.Since(started), structural, err, before, after)
	cancelEmbedding()
	corpus.current = published
	return structural, embedding
}

func (corpus *uciSLORecordCorpus) publishUpdate(ctx context.Context, sequence int) (uci.IndexPublishedView, error) {
	next := uciSLOMarkdownSource(sequence)
	changed := uciSLOChangedBytes(corpus.markdownBytes, next)
	if changed > corpus.maxChanged {
		corpus.maxChanged = changed
	}
	artifact, err := corpus.fixture.markdownArtifactForRecord(next)
	if err != nil {
		return uci.IndexPublishedView{}, err
	}
	artifactID := artifact.ArtifactID
	part, err := corpus.fixture.projection.AdmitIndexFrame(ctx, corpus.fixture.source.SourceID, corpus.fixture.profile.ProfileID, uci.IndexAdmissionFrame{
		Version:   uci.IndexAdmissionFrameVersion,
		Profile:   uci.IndexAdmissionProfile{ID: corpus.fixture.profile.ProfileID},
		Artifacts: []uci.IndexAdmissionArtifact{artifact},
		Memberships: []uci.IndexAdmissionMembership{{
			PathKey:     "docs/slo_update.md",
			DisplayPath: "docs/slo_update.md",
			Mode:        "100644",
			State:       uci.IndexAdmissionMembershipPresent,
			ArtifactID:  &artifactID,
		}},
		EdgeReplacements: []uci.IndexAdmissionEdgeReplacement{{SourcePath: "docs/slo_update.md", Edges: []uci.IndexAdmissionEdge{}}},
	})
	if err != nil {
		return uci.IndexPublishedView{}, err
	}
	previousMemberships := corpus.memberships
	previousReplacements := corpus.replacements
	corpus.memberships = uciSLOMembershipMapMerged(previousMemberships, part.Memberships)
	corpus.replacements = uciSLOReplacementMapMerged(previousReplacements, part.EdgeReplacements)
	published, err := corpus.publish(ctx, fmt.Sprintf("uci-slo-update-%03d", sequence), uci.IndexManifestDelta, uci.IndexJobReconcile, &corpus.current.Context, part, int64(sequence+1))
	if err != nil {
		corpus.memberships = previousMemberships
		corpus.replacements = previousReplacements
		return uci.IndexPublishedView{}, err
	}
	corpus.markdownBytes = next
	return published, nil
}

func (fixture *uciRetrievalSliceFixture) markdownArtifactForRecord(source []byte) (uci.IndexAdmissionArtifact, error) {
	profile := uci.DefaultMarkdownExtractionProfile("uci-slo-record-markdown")
	admissionProfile, err := uci.MarkdownIndexAdmissionArtifactProfile(profile)
	if err != nil {
		return uci.IndexAdmissionArtifact{}, err
	}
	admissionProfile.ExtractionProfileDigest = uci.IndexDigest(fixture.profile.ParserBundleDigest)
	artifact, err := uci.NewIndexAdmissionArtifactFromMarkdown(fixture.source.SourceID, admissionProfile, profile, source, uci.ExtractMarkdown(source, profile))
	if err != nil {
		return uci.IndexAdmissionArtifact{}, err
	}
	return artifact, nil
}

func (corpus *uciSLORecordCorpus) publish(ctx context.Context, buildKey string, mode uci.IndexManifestMode, jobKind uci.IndexJobKind, parent *uci.ContextRef, part uci.IndexPart, sequence int64) (uci.IndexPublishedView, error) {
	build, err := corpus.publisher.Begin(ctx, corpus.fixture.indexCaller, uci.IndexBeginInput{
		BuildKey:       buildKey,
		Scope:          uci.IndexScope{SourceID: corpus.fixture.source.SourceID, CheckoutID: corpus.fixture.checkout.CheckoutID, IncarnationID: corpus.fixture.checkout.IncarnationID},
		ProfileID:      corpus.fixture.profile.ProfileID,
		ExpectedParent: parent,
		Mode:           mode,
		JobKind:        jobKind,
	})
	if err != nil {
		return uci.IndexPublishedView{}, err
	}
	partDigest, err := uci.DigestIndexPart(part)
	if err != nil {
		return uci.IndexPublishedView{}, err
	}
	ack, err := corpus.publisher.Stage(ctx, corpus.fixture.indexCaller, uci.IndexStageInput{Build: build.Build, Sequence: 0, Digest: partDigest, Part: part})
	if err != nil {
		return uci.IndexPublishedView{}, err
	}
	manifest, err := uciSLORecordManifest([]uci.IndexPartAck{ack}, corpus.memberships, corpus.replacements, sequence)
	if err != nil {
		return uci.IndexPublishedView{}, err
	}
	return corpus.publisher.Finalize(ctx, corpus.fixture.indexCaller, uci.IndexFinalizeInput{Build: build.Build, ExpectedParent: parent, Manifest: manifest})
}

func (corpus *uciSLORecordCorpus) authorize(ctx context.Context) (uci.AuthorizedContext, error) {
	return corpus.fixture.resolver.Authorize(ctx, uci.ResolveContextInput{
		ClientSessionID: corpus.fixture.clientSessionID,
		AuthRealm:       corpus.fixture.indexCaller.AuthRealm,
		Principal:       corpus.fixture.indexCaller.Principal,
		Ref:             &corpus.current.Context,
	})
}

func (corpus *uciSLORecordCorpus) graphEntrySymbol() string {
	return corpus.graphEntry
}

func uciSLORecordManifest(acks []uci.IndexPartAck, memberships map[string]uci.IndexMembership, replacements map[string]uci.IndexEdgeReplacement, sequence int64) (uci.IndexManifestCompletion, error) {
	membershipValues := uciSLOSortedMemberships(memberships)
	replacementValues := uciSLOSortedReplacements(replacements)
	partsDigest, err := uci.DigestIndexParts(acks)
	if err != nil {
		return uci.IndexManifestCompletion{}, err
	}
	manifestDigest, err := uci.DigestIndexManifest(membershipValues)
	if err != nil {
		return uci.IndexManifestCompletion{}, err
	}
	edgesDigest, err := uci.DigestIndexEdges(replacementValues)
	if err != nil {
		return uci.IndexManifestCompletion{}, err
	}
	now := time.Now().UTC()
	refLabel := "refs/heads/uci-slo-measurement"
	return uci.IndexManifestCompletion{
		PartCount:      uint32(len(acks)),
		PartsDigest:    partsDigest,
		EntryCount:     uint64(len(membershipValues)),
		ManifestDigest: manifestDigest,
		EdgeCount:      uint64(uciSLOEdgeCount(replacementValues)),
		EdgesDigest:    edgesDigest,
		ScanOutcome:    uci.IndexScanComplete,
		CensusComplete: true,
		Observation: uci.IndexObservation{
			RefLabel:      &refLabel,
			ObservedFSSeq: sequence,
			ScanStart:     now,
			ScanEnd:       now,
		},
		Coverage: uci.IndexCoverage{Structural: uci.IndexCoverageComplete, Lexical: uci.IndexCoverageComplete, Vector: uci.IndexCoverageUnavailable},
	}, nil
}

func uciSLORecordServerSearch(fixture *uciRetrievalSliceFixture, authorized uci.AuthorizedContext, text string) uciSLORecordedAttempt {
	started := time.Now()
	ctx, cancel := context.WithTimeout(fixture.callerContext, uciSLORecordSearchWaitBound)
	defer cancel()
	result, err := fixture.queryService.Query(ctx, authorized, uci.QuerySpec{
		ClientSessionID: fixture.clientSessionID,
		Mode:            uci.QueryModeFTS,
		Text:            text,
		Order:           uci.QueryOrderRelevance,
		Limit:           10,
	})
	return uciSLOClassifyQueryAttempt(time.Since(started), result.Response, err, uci.QueryRetrievalLexical)
}

func uciSLORecordQueryEmbedding(fixture *uciRetrievalSliceFixture, authorized uci.AuthorizedContext) uciSLORecordedAttempt {
	started := time.Now()
	ctx, cancel := context.WithTimeout(fixture.callerContext, uciSLORecordQueryWaitBound)
	defer cancel()
	before := uciSLOProviderCalls(fixture.provider)
	response, err := fixture.application.SearchCodebase(ctx, authorized, mcp.CodebaseSearchInput{Query: uciRetrievalSliceSemanticQuery, Limit: 10})
	after := uciSLOProviderCalls(fixture.provider)
	attempt := uciSLOClassifyQueryAttempt(time.Since(started), response, err, uci.QueryRetrievalHybrid)
	if attempt.outcome == "healthy" && (response.Retrieval.VectorCoverage == nil || *response.Retrieval.VectorCoverage != 1) {
		attempt = uciSLODegradedAttempt(attempt.latency, "vector_coverage_incomplete", string(response.Status), uciSLOCoverage(response))
	}
	if attempt.outcome == "healthy" && after.query <= before.query {
		attempt = uciSLODegradedAttempt(attempt.latency, "provider_not_called", string(response.Status), uciSLOCoverage(response))
	}
	return attempt
}

func uciSLORecordGraph(fixture *uciRetrievalSliceFixture, authorized uci.AuthorizedContext, entrySymbol string) uciSLORecordedAttempt {
	started := time.Now()
	ctx, cancel := context.WithTimeout(fixture.callerContext, uciSLORecordGraphWaitBound)
	defer cancel()
	for _, action := range []uci.GraphAction{uci.GraphActionExplain, uci.GraphActionNeighbors, uci.GraphActionImpact} {
		response, err := fixture.application.ExploreCodebase(ctx, authorized, mcp.CodebaseGraphInput{
			Action: uci.GraphAction(action),
			Target: uci.GraphTarget{EntityKey: entrySymbol},
			Filter: uci.GraphFilter{
				Direction: uci.GraphDirectionOutgoing,
				Relations: []uci.IndexRelation{"calls"},
			},
			Budget: uci.GraphBudget{
				MaxDepth:   4,
				MaxVisited: 16,
				MaxNodes:   16,
				MaxEdges:   16,
				Deadline:   time.Now().Add(uciSLORecordGraphWaitBound),
			},
		})
		attempt := uciSLOClassifyQueryAttempt(time.Since(started), response, err, uci.QueryRetrievalGraph)
		if attempt.outcome != "healthy" {
			return attempt
		}
	}
	return uciSLOHealthyAttempt(time.Since(started), "ok", "complete")
}

func uciSLOEnsurePathEmbedding(ctx context.Context, fixture *uciRetrievalSliceFixture, authorized uci.AuthorizedContext, path string) error {
	selected, err := fixture.projection.SelectCandidates(ctx, authorized, uci.QuerySpec{
		ClientSessionID: fixture.clientSessionID,
		Mode:            uci.QueryModeExactRelativePath,
		Text:            path,
		Order:           uci.QueryOrderPath,
		Limit:           50,
	})
	if err != nil {
		return err
	}
	if selected.Unavailable != nil || selected.Coverage != uci.IndexCoverageComplete || len(selected.Candidates) == 0 {
		return fmt.Errorf("current candidate selection is not complete")
	}
	for _, candidate := range selected.Candidates {
		if err := fixture.semanticService.EnsureCandidateEmbedding(ctx, authorized, candidate); err != nil {
			return err
		}
	}
	return nil
}

func uciSLOClassifyEmbeddingAttempt(latency time.Duration, structural uciSLORecordedAttempt, err error, before, after uciSLOProviderCallState) uciSLORecordedAttempt {
	if err != nil {
		return uciSLOUnavailableAttempt(latency, "provider_embedding_unavailable")
	}
	if structural.outcome != "healthy" {
		return uciSLODegradedAttempt(latency, "structural_update_not_healthy", structural.status, structural.coverage)
	}
	if after.corpus <= before.corpus {
		return uciSLODegradedAttempt(latency, "provider_not_called", "ok", "complete")
	}
	return uciSLOHealthyAttempt(latency, "ok", "complete")
}

func uciSLOClassifyQueryAttempt(latency time.Duration, response uci.QueryResponse, err error, expected uci.QueryRetrievalMode) uciSLORecordedAttempt {
	if err != nil {
		return uciSLOFailedAttempt(latency)
	}
	status := string(response.Status)
	coverage := uciSLOCoverage(response)
	if response.ValidatePreExposure() != nil {
		return uciSLOFailedAttempt(latency)
	}
	if response.Status == uci.QueryStatusUnavailable {
		return uciSLOUnavailableAttempt(latency, "query_unavailable")
	}
	if response.Status != uci.QueryStatusOK || coverage != string(uci.IndexCoverageComplete) || response.Retrieval == nil || response.Retrieval.Mode != expected || len(response.Retrieval.DegradationReasons) != 0 {
		return uciSLODegradedAttempt(latency, "query_not_healthy", status, coverage)
	}
	return uciSLOHealthyAttempt(latency, status, coverage)
}

func uciSLOCoverage(response uci.QueryResponse) string {
	if response.Coverage == nil {
		return "unknown"
	}
	return string(response.Coverage.Structural)
}

func uciSLOHealthyAttempt(latency time.Duration, status, coverage string) uciSLORecordedAttempt {
	if latency <= 0 {
		return uciSLOFailedAttempt(latency)
	}
	return uciSLORecordedAttempt{outcome: "healthy", status: status, coverage: coverage, latency: latency}
}

func uciSLODegradedAttempt(latency time.Duration, reason, status, coverage string) uciSLORecordedAttempt {
	return uciSLORecordedAttempt{outcome: "degraded", status: status, coverage: coverage, reason: reason, latency: latency}
}

func uciSLOUnavailableAttempt(latency time.Duration, reason string) uciSLORecordedAttempt {
	return uciSLORecordedAttempt{outcome: "unavailable", status: "unavailable", coverage: "unavailable", reason: reason, latency: latency}
}

func uciSLOFailedAttempt(latency time.Duration) uciSLORecordedAttempt {
	return uciSLORecordedAttempt{outcome: "failed", status: "error", coverage: "unknown", reason: "operation_failed", latency: latency}
}

func uciSLOCollectWarmAttempts(maximum int, run func() uciSLORecordedAttempt) []uciSLORecordedAttempt {
	attempts := make([]uciSLORecordedAttempt, 0, maximum)
	for len(attempts) < maximum && uciSLOHealthyAttemptCount(attempts) < uciSLORecordMinimumHealthySamples {
		attempts = append(attempts, run())
	}
	return attempts
}

func uciSLOHealthyAttemptCount(attempts []uciSLORecordedAttempt) int {
	count := 0
	for _, attempt := range attempts {
		if attempt.outcome == "healthy" {
			count++
		}
	}
	return count
}

func uciSLORecordGate(name, operation string, waitBound time.Duration, contextID string, identity uci.UCISLOSampleIdentity, attempts []uciSLORecordedAttempt, maxDepth int, caps map[string]int) uci.UCISLOGateInput {
	samples := make([]uci.UCISLOSample, 0, len(attempts))
	for index, attempt := range attempts {
		samples = append(samples, uciSLORecordSample(fmt.Sprintf("%s-%03d", name, index+1), "warm", uciSLORecordProfileID, contextID, identity, attempt))
	}
	return uci.UCISLOGateInput{
		Name:          name,
		Operation:     operation,
		Warmth:        "warm",
		WaitBound:     waitBound,
		ProfileID:     uciSLORecordProfileID,
		ContextID:     contextID,
		MaxGraphDepth: maxDepth,
		QueryCaps:     caps,
		Samples:       samples,
	}
}

func uciSLORecordSample(id, warmth, profileID, contextID string, identity uci.UCISLOSampleIdentity, attempt uciSLORecordedAttempt) uci.UCISLOSample {
	return uci.UCISLOSample{
		ID:           id,
		Identity:     identity,
		Outcome:      attempt.outcome,
		Warmth:       warmth,
		ProfileID:    profileID,
		ContextID:    contextID,
		ResultStatus: attempt.status,
		Coverage:     attempt.coverage,
		Reason:       attempt.reason,
		Waited:       0,
		Latency:      attempt.latency,
	}
}

func uciSLOMeasureLocalProviderRTT(fixture *uciRetrievalSliceFixture) (time.Duration, error) {
	payload, err := json.Marshal(struct {
		Model      string   `json:"model"`
		Dimensions int      `json:"dimensions"`
		Input      []string `json:"input"`
	}{
		Model:      uciSLORecordProviderModel,
		Dimensions: embedding.EmbeddingDim,
		Input:      []string{"uci-slo-local-rtt"},
	})
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, fixture.providerURL+"/v1/embeddings", bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+uciRetrievalSliceProviderKey)
	request.Header.Set("Content-Type", "application/json")
	started := time.Now()
	response, err := fixture.provider.server.Client().Do(request)
	latency := time.Since(started)
	if err != nil {
		return latency, err
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		return latency, err
	}
	if response.StatusCode != http.StatusOK {
		return latency, fmt.Errorf("local provider returned non-healthy status")
	}
	return latency, nil
}

func uciSLOProviderCalls(provider *uciRetrievalSliceEmbeddingProvider) uciSLOProviderCallState {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return uciSLOProviderCallState{requests: provider.requestCount, corpus: provider.corpus, query: provider.query, failed: provider.failed}
}

func uciSLOCorpusCounts(fixture *uciRetrievalSliceFixture) (int, int, error) {
	var counts struct {
		Files int
		Lines int
	}
	err := fixture.store.GetDB().WithContext(fixture.context).Raw(`
		SELECT
			COUNT(*) AS files,
			COALESCE(SUM(cardinality(regexp_split_to_array(convert_from(blob.safe_content, blob.encoding), chr(10))), 0) AS lines
		FROM ci_memberships AS membership
		JOIN ci_parse_artifacts AS artifact
			ON artifact.source_id = membership.source_id
			AND artifact.artifact_id = membership.artifact_id
		JOIN ci_blobs AS blob
			ON blob.source_id = artifact.source_id
			AND blob.blob_id = artifact.blob_id
		WHERE membership.source_id = ?
			AND membership.checkout_id = ?
			AND membership.valid_to_generation IS NULL
			AND membership.file_state = 'present'
	`, fixture.source.SourceID, fixture.checkout.CheckoutID).Scan(&counts).Error
	if err != nil {
		return 0, 0, err
	}
	return counts.Files, counts.Lines, nil
}

func uciSLODatabaseMeasurement(fixture *uciRetrievalSliceFixture) (string, int64, error) {
	var version string
	if err := fixture.store.GetDB().WithContext(fixture.context).Raw(`SELECT current_setting('server_version')`).Scan(&version).Error; err != nil {
		return "", 0, err
	}
	var size int64
	if err := fixture.store.GetDB().WithContext(fixture.context).Raw(`
		SELECT COALESCE(SUM(pg_total_relation_size((quote_ident(schemaname) || '.' || quote_ident(tablename))::regclass)), 0)::bigint
		FROM pg_tables
		WHERE schemaname = current_schema()
	`).Scan(&size).Error; err != nil {
		return "", 0, err
	}
	return version, size, nil
}

func uciSLORecordContextID(fixture *uciRetrievalSliceFixture) string {
	return "uci-scope-" + fixture.source.SourceID + "-" + fixture.checkout.CheckoutID + "-" + fixture.profile.ProfileID
}

func uciSLOMarkdownSource(sequence int) []byte {
	return []byte("# UCI SLO measurement\n\n" + uciSLOUpdateMarker(sequence) + "\n")
}

func uciSLOUpdateMarker(sequence int) string {
	value := sequence
	if value < 0 {
		value = 0
	}
	letters := make([]byte, 0, 3)
	for {
		letters = append([]byte{byte('a' + value%26)}, letters...)
		value = value/26 - 1
		if value < 0 {
			break
		}
	}
	return "ucislochange" + string(letters)
}

func uciSLOChangedBytes(previous, next []byte) int64 {
	limit := len(previous)
	if len(next) < limit {
		limit = len(next)
	}
	changed := 0
	for index := range limit {
		if previous[index] != next[index] {
			changed++
		}
	}
	changed += len(previous) - limit
	changed += len(next) - limit
	return int64(changed)
}

func uciSLOMembershipMap(values []uci.IndexMembership) map[string]uci.IndexMembership {
	mapped := make(map[string]uci.IndexMembership, len(values))
	for _, value := range values {
		mapped[value.PathKey] = value
	}
	return mapped
}

func uciSLOMembershipMapMerged(existing map[string]uci.IndexMembership, updates []uci.IndexMembership) map[string]uci.IndexMembership {
	merged := make(map[string]uci.IndexMembership, len(existing)+len(updates))
	for key, value := range existing {
		merged[key] = value
	}
	for _, value := range updates {
		merged[value.PathKey] = value
	}
	return merged
}

func uciSLOReplacementMap(values []uci.IndexEdgeReplacement) map[string]uci.IndexEdgeReplacement {
	mapped := make(map[string]uci.IndexEdgeReplacement, len(values))
	for _, value := range values {
		mapped[value.SourcePath] = value
	}
	return mapped
}

func uciSLOReplacementMapMerged(existing map[string]uci.IndexEdgeReplacement, updates []uci.IndexEdgeReplacement) map[string]uci.IndexEdgeReplacement {
	merged := make(map[string]uci.IndexEdgeReplacement, len(existing)+len(updates))
	for key, value := range existing {
		merged[key] = value
	}
	for _, value := range updates {
		merged[value.SourcePath] = value
	}
	return merged
}

func uciSLOSortedMemberships(values map[string]uci.IndexMembership) []uci.IndexMembership {
	ordered := make([]uci.IndexMembership, 0, len(values))
	for _, value := range values {
		ordered = append(ordered, value)
	}
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].PathKey < ordered[right].PathKey })
	return ordered
}

func uciSLOSortedReplacements(values map[string]uci.IndexEdgeReplacement) []uci.IndexEdgeReplacement {
	ordered := make([]uci.IndexEdgeReplacement, 0, len(values))
	for _, value := range values {
		ordered = append(ordered, value)
	}
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].SourcePath < ordered[right].SourcePath })
	return ordered
}

func uciSLOEdgeCount(replacements []uci.IndexEdgeReplacement) int {
	count := 0
	for _, replacement := range replacements {
		count += len(replacement.Edges)
	}
	return count
}

func uciSLOWriteInputAtomically(path string, input uci.UCISLOMeasurementInput) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var decoded uci.UCISLOMeasurementInput
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("encoded input contains more than one JSON value")
		}
		return err
	}

	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".uci-slo-measurement-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}
