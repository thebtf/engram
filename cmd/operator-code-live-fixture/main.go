// operator-code-live-fixture provisions the disposable authenticated Code Explorer corpus.
// It receives the database DSN only through a private file and emits no credentials.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
	"github.com/thebtf/engram/internal/worker"
	"gorm.io/gorm/logger"
)

const (
	fixtureAuthRealm      = "browser"
	fixtureQuery          = "CodeExplorerFixtureEntry"
	fixtureExpectedGraph  = "CodeExplorerFixtureTarget"
	fixtureExpectedSource = "CodeExplorerFixtureEntry"
	fixtureSourcePath     = "fixture.go"
)

var fixtureSource = []byte(`package fixture

const CodeExplorerFixtureMessage = "operator-code-fixture"

func CodeExplorerFixtureTarget() string {
	return CodeExplorerFixtureMessage
}

func CodeExplorerFixtureEntry() string {
	return CodeExplorerFixtureTarget()
}
`)

type invocation struct {
	dsnFile      string
	browserEmail string
	project      string
}

type fixtureOutput struct {
	Query          string `json:"query"`
	ExpectedSearch string `json:"expectedSearch"`
	ExpectedGraph  string `json:"expectedGraph"`
	ExpectedSource string `json:"expectedSource"`
}

type commandDependencies struct {
	readFile  func(string) ([]byte, error)
	provision func(context.Context, string, invocation) (fixtureOutput, error)
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, defaultCommandDependencies()))
}

func defaultCommandDependencies() commandDependencies {
	return commandDependencies{
		readFile: os.ReadFile,
		provision: func(ctx context.Context, dsn string, in invocation) (fixtureOutput, error) {
			return provision(ctx, dsn, in.browserEmail, in.project)
		},
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, deps commandDependencies) int {
	in, err := parseInvocation(args)
	if err != nil || deps.readFile == nil || deps.provision == nil {
		fmt.Fprintln(stderr, "usage: operator-code-live-fixture --dsn-file <path> --browser-email <email> --project <fixture-id>")
		return 2
	}
	dsnBytes, err := deps.readFile(in.dsnFile)
	if err != nil {
		fmt.Fprintln(stderr, "operator-code-live-fixture: private fixture input unavailable")
		return 1
	}
	dsn := strings.TrimSpace(string(dsnBytes))
	if dsn == "" || len(dsn) > 4096 || strings.ContainsRune(dsn, '\x00') {
		fmt.Fprintln(stderr, "operator-code-live-fixture: private fixture input unavailable")
		return 1
	}
	output, err := deps.provision(ctx, dsn, in)
	if err != nil {
		fmt.Fprintln(stderr, "operator-code-live-fixture: provisioning failed")
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(output); err != nil {
		fmt.Fprintln(stderr, "operator-code-live-fixture: receipt write failed")
		return 1
	}
	return 0
}

func parseInvocation(args []string) (invocation, error) {
	if len(args) != 6 {
		return invocation{}, fmt.Errorf("expected three flags")
	}
	var in invocation
	seen := map[string]bool{}
	for index := 0; index < len(args); index += 2 {
		flag, value := args[index], args[index+1]
		if seen[flag] || value == "" || strings.Contains(flag, "=") {
			return invocation{}, fmt.Errorf("invalid fixture arguments")
		}
		seen[flag] = true
		switch flag {
		case "--dsn-file":
			in.dsnFile = value
		case "--browser-email":
			in.browserEmail = value
		case "--project":
			in.project = value
		default:
			return invocation{}, fmt.Errorf("unknown fixture argument")
		}
	}
	if in.dsnFile == "" || !validFixtureEmail(in.browserEmail) || !validFixtureID(in.project) {
		return invocation{}, fmt.Errorf("invalid fixture arguments")
	}
	return in, nil
}

func provision(ctx context.Context, dsn, browserEmail, project string) (fixtureOutput, error) {
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, MaxConns: 2, LogLevel: logger.Silent})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("open fixture store: %w", err)
	}
	defer store.Close()

	user, err := gormdb.NewUserStore(store.DB).GetUserByEmail(browserEmail)
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("load fixture browser user: %w", err)
	}
	subject := auth.BrowserSubjectForUser(user.ID)
	if !subject.Valid() {
		return fixtureOutput{}, fmt.Errorf("fixture browser subject is invalid")
	}

	contexts := gormdb.NewUCIContextStore(store.DB)
	source, err := contexts.CreateSource(ctx, gormdb.CreateSourceInput{
		AuthRealm:   fixtureAuthRealm,
		Kind:        gormdb.UCISourceGit,
		DisplayName: "Operator Code Fixture " + project,
	})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("create fixture source: %w", err)
	}
	checkout, err := contexts.RegisterCheckout(ctx, gormdb.RegisterCheckoutInput{
		SourceID:       source.SourceID,
		WorkstationID:  "operator-code-live-" + project,
		Kind:           gormdb.UCICheckoutWorkingTree,
		OwnerPrincipal: subject.Principal,
		LocatorRef:     "fixture://operator-code/" + project,
	})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("register fixture checkout: %w", err)
	}

	extraction := uci.GoExtractionProfile{ProfileKey: "operator-code-live-v1", ParserKey: "go-parser"}
	admissionProfile, err := uci.GoIndexAdmissionArtifactProfile(extraction)
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("create fixture admission profile: %w", err)
	}
	profile, err := contexts.CreateProfile(ctx, gormdb.CreateProfileInput{
		ParserBundleDigest:   string(admissionProfile.ExtractionProfileDigest),
		ResolverRevision:     "operator-code-live-resolver-v1",
		ChunkerRevision:      "operator-code-live-chunker-v1",
		IgnorePolicyDigest:   fixtureDigest("operator-code-live-ignore-v1"),
		BuildContextJSON:     `{"fixture":"operator-code-live"}`,
		SecretPolicyRevision: "operator-code-live-secret-v1",
	})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("create fixture profile: %w", err)
	}

	projection := gormdb.NewUCIProjectionStore(store.DB)
	authorizer := gormdb.NewUCIContextAuthorizer(contexts)
	publisher, err := projection.Publisher(authorizer, uci.IndexPublicationConfig{Limits: uci.DefaultIndexPublicationLimits()})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("create fixture index publisher: %w", err)
	}
	caller := uci.IndexCaller{
		AuthRealm:     source.AuthRealm,
		Principal:     subject.Principal,
		OwnerInstance: "operator-code-live-" + project,
	}
	build, err := publisher.Begin(ctx, caller, uci.IndexBeginInput{
		BuildKey: "operator-code-live-" + project,
		Scope: uci.IndexScope{
			SourceID:      source.SourceID,
			CheckoutID:    checkout.CheckoutID,
			IncarnationID: checkout.IncarnationID,
		},
		ProfileID: profile.ProfileID,
		Mode:      uci.IndexManifestFull,
		JobKind:   uci.IndexJobInitial,
	})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("begin fixture index: %w", err)
	}
	frame, err := fixtureFrame(source.SourceID, profile.ProfileID, admissionProfile, extraction)
	if err != nil {
		return fixtureOutput{}, err
	}
	part, err := projection.AdmitIndexFrame(ctx, source.SourceID, profile.ProfileID, frame)
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("admit fixture source: %w", err)
	}
	partDigest, err := uci.DigestIndexPart(part)
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("digest fixture part: %w", err)
	}
	ack, err := publisher.Stage(ctx, caller, uci.IndexStageInput{Build: build.Build, Sequence: 0, Digest: partDigest, Part: part})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("stage fixture index: %w", err)
	}
	published, err := publisher.Finalize(ctx, caller, uci.IndexFinalizeInput{
		Build:    build.Build,
		Manifest: fixtureManifest(ack, part),
	})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("publish fixture view: %w", err)
	}
	if _, err := contexts.LoadContext(ctx, published.Context); err != nil {
		return fixtureOutput{}, fmt.Errorf("read back fixture view: %w", err)
	}

	grants := worker.NewCodeGrantApplication(gormdb.NewBrowserReadGrantStore(store.DB))
	issuer := auth.SessionForBrowserUser(user.Role, user.ID)
	grant, err := grants.Issue(ctx, issuer, worker.IssueCodeGrantInput{
		Target:     subject,
		SourceID:   source.SourceID,
		CheckoutID: checkout.CheckoutID,
	})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("issue fixture grant: %w", err)
	}
	current, found, err := grants.Current(ctx, issuer)
	if err != nil || !found || current.GrantRef != grant.GrantRef || current.SourceID != source.SourceID || current.CheckoutID != checkout.CheckoutID {
		return fixtureOutput{}, fmt.Errorf("read back fixture grant")
	}
	return fixtureOutput{
		Query:          fixtureQuery,
		ExpectedSearch: fixtureQuery,
		ExpectedGraph:  fixtureExpectedGraph,
		ExpectedSource: fixtureExpectedSource,
	}, nil
}

func fixtureFrame(sourceID, profileID string, admissionProfile uci.IndexAdmissionArtifactProfile, extraction uci.GoExtractionProfile) (uci.IndexAdmissionFrame, error) {
	artifact, err := uci.NewIndexAdmissionArtifactFromGo(sourceID, admissionProfile, fixtureSource, uci.ExtractGo(fixtureSource, extraction))
	if err != nil {
		return uci.IndexAdmissionFrame{}, fmt.Errorf("build fixture artifact: %w", err)
	}
	var reference *uci.IndexAdmissionReference
	for index := range artifact.References {
		candidate := &artifact.References[index]
		if candidate.RawTarget == fixtureExpectedGraph && candidate.OwnerSymbolKey != nil && *candidate.OwnerSymbolKey == "func:"+fixtureExpectedSource {
			reference = candidate
			break
		}
	}
	if reference == nil {
		return uci.IndexAdmissionFrame{}, fmt.Errorf("fixture graph reference is unavailable")
	}
	artifactID, sourceSymbol, targetSymbol := artifact.ArtifactID, *reference.OwnerSymbolKey, "func:"+fixtureExpectedGraph
	return uci.IndexAdmissionFrame{
		Version:   uci.IndexAdmissionFrameVersion,
		Profile:   uci.IndexAdmissionProfile{ID: profileID},
		Artifacts: []uci.IndexAdmissionArtifact{artifact},
		Memberships: []uci.IndexAdmissionMembership{{
			PathKey:     fixtureSourcePath,
			DisplayPath: fixtureSourcePath,
			Mode:        "100644",
			State:       uci.IndexAdmissionMembershipPresent,
			ArtifactID:  &artifactID,
		}},
		EdgeReplacements: []uci.IndexAdmissionEdgeReplacement{{
			SourcePath: fixtureSourcePath,
			Edges: []uci.IndexAdmissionEdge{{
				EdgeKey:          "operator-code-live-entry-target",
				SourceArtifactID: artifactID,
				SourceSymbolKey:  &sourceSymbol,
				Target: &uci.IndexAdmissionEdgeTarget{
					PathKey:    fixtureSourcePath,
					ArtifactID: artifactID,
					SymbolKey:  &targetSymbol,
				},
				Relation:         reference.Relation,
				EvidenceKind:     uci.IndexEvidenceKind("extracted"),
				ResolutionState:  uci.IndexResolutionState("resolved"),
				ResolverRevision: "operator-code-live-resolver-v1",
				Evidence: uci.IndexAdmissionEdgeEvidence{
					ReferenceSiteKey: reference.SiteKey,
					Span:             reference.Span,
					RuleKey:          "operator-code-live-source-fact",
					Explanation:      "fixture source call resolved inside the published view",
				},
			}},
		}},
	}, nil
}

func fixtureManifest(ack uci.IndexPartAck, part uci.IndexPart) uci.IndexManifestCompletion {
	partsDigest, _ := uci.DigestIndexParts([]uci.IndexPartAck{ack})
	manifestDigest, _ := uci.DigestIndexManifest(part.Memberships)
	edgesDigest, _ := uci.DigestIndexEdges(part.EdgeReplacements)
	now := fixtureClock()
	headOID, objectFormat, refLabel := strings.Repeat("a", 40), "sha1", "refs/heads/operator-code-live"
	return uci.IndexManifestCompletion{
		PartCount:      1,
		PartsDigest:    partsDigest,
		EntryCount:     uint64(len(part.Memberships)),
		ManifestDigest: manifestDigest,
		EdgeCount:      uint64(len(part.EdgeReplacements[0].Edges)),
		EdgesDigest:    edgesDigest,
		ScanOutcome:    uci.IndexScanComplete,
		CensusComplete: true,
		Observation: uci.IndexObservation{
			HeadOID:       &headOID,
			ObjectFormat:  &objectFormat,
			RefLabel:      &refLabel,
			ObservedFSSeq: 1,
			ScanStart:     now,
			ScanEnd:       now.Add(time.Second),
		},
		Coverage: uci.IndexCoverage{
			Structural: uci.IndexCoverageComplete,
			Lexical:    uci.IndexCoverageComplete,
			Vector:     uci.IndexCoverageUnavailable,
		},
	}
}

func fixtureClock() time.Time { return time.Now().UTC() }

func fixtureDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validFixtureEmail(value string) bool {
	return len(value) > 3 && len(value) <= 256 && strings.TrimSpace(value) == value && strings.Count(value, "@") == 1 && !strings.ContainsFunc(value, unicode.IsControl)
}

func validFixtureID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '.') {
			return false
		}
	}
	return true
}
