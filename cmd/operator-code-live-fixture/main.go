// operator-code-live-fixture provisions the disposable authenticated Code Explorer corpus.
// It receives the database DSN only through a private file and emits no credentials.
package main

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
	"github.com/thebtf/engram/internal/worker"
	"github.com/thebtf/engram/pkg/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	fixtureAuthRealm         = "browser"
	fixtureClientAuthRealm   = string(auth.SourceClient)
	fixtureQuery             = "CodeExplorerFixtureA"
	fixtureExpectedGraph     = "CodeExplorerFixtureB"
	fixtureExpectedSource    = "CodeExplorerFixtureA"
	fixtureSourcePath        = "fixture.go"
	fixtureQueueSeedCount    = 5
	fixtureModeNoView        = "no-view"
	fixtureWorkstationPrefix = "operator-code-live-"
)

var fixtureSource = []byte(`package fixture

const CodeExplorerFixtureMessage = "operator-code-fixture"

func CodeExplorerFixtureB() string {
	return CodeExplorerFixtureMessage
}

func CodeExplorerFixtureA() string {
	return CodeExplorerFixtureB()
}
`)

type invocation struct {
	dsnFile      string
	browserEmail string
	project      string
	sourceFile   string
	passwordFile string
	keycardFile  string
	mode         string
}

type fixtureOutput struct {
	Query          string               `json:"query"`
	ExpectedSearch string               `json:"expectedSearch"`
	ExpectedGraph  string               `json:"expectedGraph"`
	ExpectedSource string               `json:"expectedSource"`
	ExpectedMarker string               `json:"expectedMarker"`
	NoView         *noViewFixtureOutput `json:"noView,omitempty"`
}

type noViewFixtureOutput struct {
	SourceID           string `json:"sourceId"`
	CheckoutID         string `json:"checkoutId"`
	IncarnationID      string `json:"incarnationId"`
	AnalysisProfileID  string `json:"analysisProfileId"`
	ParserBundleDigest string `json:"parserBundleDigest"`
}

type commandDependencies struct {
	readFile  func(string) ([]byte, error)
	provision func(context.Context, string, invocation) (fixtureOutput, error)
}

type fixturePublishedProvisionInput struct {
	invocation  invocation
	sourceBytes []byte
	marker      string
	user        *gormdb.User
	subject     auth.BrowserSubject
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, defaultCommandDependencies()))
}

func defaultCommandDependencies() commandDependencies {
	return commandDependencies{
		readFile:  os.ReadFile,
		provision: provision,
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, deps commandDependencies) int {
	in, err := parseInvocation(args)
	if err != nil || deps.readFile == nil || deps.provision == nil {
		fmt.Fprintln(stderr, "usage: operator-code-live-fixture --dsn-file <path> --browser-email <email> --project <fixture-id> [--source-file <path>] [--password-file <path>] [--mode published|no-view --keycard-file <path>]")
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
	if len(args) < 6 || len(args)%2 != 0 {
		return invocation{}, fmt.Errorf("expected fixture flags")
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
		case "--source-file":
			in.sourceFile = value
		case "--password-file":
			in.passwordFile = value
		case "--keycard-file":
			in.keycardFile = value
		case "--mode":
			in.mode = value
		default:
			return invocation{}, fmt.Errorf("unknown fixture argument")
		}
	}
	if in.mode == "" {
		in.mode = "published"
	}
	if in.dsnFile == "" || !validFixtureEmail(in.browserEmail) || !validFixtureID(in.project) || (in.mode != "published" && in.mode != fixtureModeNoView) || (in.mode == fixtureModeNoView && (in.sourceFile == "" || in.keycardFile == "")) {
		return invocation{}, fmt.Errorf("invalid fixture arguments")
	}
	return in, nil
}

func provision(ctx context.Context, dsn string, in invocation) (fixtureOutput, error) {
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, MaxConns: 2, LogLevel: logger.Silent})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("open fixture store: %w", err)
	}
	defer store.Close()

	sourceBytes, err := fixtureSourceFor(in)
	if err != nil {
		return fixtureOutput{}, err
	}
	marker := fixtureMarker(sourceBytes)
	if marker == "" {
		return fixtureOutput{}, fmt.Errorf("fixture source marker is unavailable")
	}
	user, subject, err := fixtureBrowserUser(store, in)
	if err != nil {
		return fixtureOutput{}, err
	}
	if in.mode == fixtureModeNoView {
		return provisionNoView(ctx, store, user, subject, in, marker)
	}
	if err := seedFixtureQueueCandidates(ctx, store.GetDB(), in.project); err != nil {
		return fixtureOutput{}, err
	}
	return provisionPublished(ctx, store, fixturePublishedProvisionInput{invocation: in, sourceBytes: sourceBytes, marker: marker, user: user, subject: subject})
}

func fixtureBrowserUser(store *gormdb.Store, in invocation) (*gormdb.User, auth.BrowserSubject, error) {
	users := gormdb.NewUserStore(store.DB)
	user, err := users.GetUserByEmail(in.browserEmail)
	if errors.Is(err, gorm.ErrRecordNotFound) && in.passwordFile != "" {
		password, readErr := os.ReadFile(in.passwordFile)
		if readErr != nil || strings.TrimSpace(string(password)) == "" {
			return nil, auth.BrowserSubject{}, fmt.Errorf("read fixture browser password")
		}
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(strings.TrimSpace(string(password))), bcrypt.DefaultCost)
		if hashErr != nil {
			return nil, auth.BrowserSubject{}, fmt.Errorf("hash fixture browser password: %w", hashErr)
		}
		user, err = users.CreateUser(in.browserEmail, string(hash), gormdb.DashboardRoleOperator)
	}
	if err != nil {
		return nil, auth.BrowserSubject{}, fmt.Errorf("load fixture browser user: %w", err)
	}
	subject := auth.BrowserSubjectForUser(user.ID)
	if !subject.Valid() {
		return nil, auth.BrowserSubject{}, fmt.Errorf("fixture browser subject is invalid")
	}
	return user, subject, nil
}

func provisionPublished(ctx context.Context, store *gormdb.Store, input fixturePublishedProvisionInput) (fixtureOutput, error) {
	in, sourceBytes, marker, user, subject := input.invocation, input.sourceBytes, input.marker, input.user, input.subject
	project := in.project
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
		WorkstationID:  fixtureWorkstationPrefix + project,
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
		OwnerInstance: fixtureWorkstationPrefix + project,
	}
	build, err := publisher.Begin(ctx, caller, uci.IndexBeginInput{
		BuildKey: fixtureWorkstationPrefix + project,
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
	frame, err := fixtureFrame(source.SourceID, profile.ProfileID, admissionProfile, extraction, sourceBytes)
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
	published, err := publisher.Finalize(ctx, caller, uci.IndexFinalizeInput{Build: build.Build, Manifest: fixtureManifest(ack, part)})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("publish fixture view: %w", err)
	}
	if _, err := contexts.LoadContext(ctx, published.Context); err != nil {
		return fixtureOutput{}, fmt.Errorf("read back fixture view: %w", err)
	}

	grants := worker.NewCodeGrantApplication(gormdb.NewBrowserReadGrantStore(store.DB))
	issuer := auth.SessionForBrowserUser(user.Role, user.ID)
	grant, err := grants.Issue(ctx, issuer, worker.IssueCodeGrantInput{Target: subject, SourceID: source.SourceID, CheckoutID: checkout.CheckoutID})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("issue fixture grant: %w", err)
	}
	current, found, err := grants.Active(ctx, issuer, source.SourceID, checkout.CheckoutID)
	if err != nil || !found || current.GrantRef != grant.GrantRef || current.SourceID != source.SourceID || current.CheckoutID != checkout.CheckoutID {
		return fixtureOutput{}, fmt.Errorf("read back fixture grant")
	}
	return fixtureOutput{Query: fixtureQuery, ExpectedSearch: fixtureQuery, ExpectedGraph: fixtureExpectedGraph, ExpectedSource: fixtureExpectedSource, ExpectedMarker: marker}, nil
}

func provisionNoView(ctx context.Context, store *gormdb.Store, user *gormdb.User, subject auth.BrowserSubject, in invocation, marker string) (fixtureOutput, error) {
	if in.sourceFile == "" || in.keycardFile == "" {
		return fixtureOutput{}, fmt.Errorf("no-view fixture source and keycard file are required")
	}
	root, err := filepath.Abs(filepath.Dir(in.sourceFile))
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("resolve no-view fixture root: %w", err)
	}
	keycard, workstationID, err := fixtureClientKeycard(ctx, store, fixtureWorkstationPrefix+in.project, subject)
	if err != nil {
		return fixtureOutput{}, err
	}
	contexts := gormdb.NewUCIContextStore(store.DB)
	source, err := contexts.CreateSource(ctx, gormdb.CreateSourceInput{
		AuthRealm:   fixtureClientAuthRealm,
		Kind:        gormdb.UCISourceGit,
		DisplayName: "Operator Code Fixture " + in.project,
	})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("create no-view fixture source: %w", err)
	}
	locatorPath := filepath.ToSlash(root)
	if filepath.VolumeName(root) != "" {
		locatorPath = "/" + locatorPath
	}
	locator := (&url.URL{Scheme: "file", Path: locatorPath}).String()
	checkout, err := contexts.RegisterCheckout(ctx, gormdb.RegisterCheckoutInput{
		SourceID:       source.SourceID,
		WorkstationID:  workstationID,
		Kind:           gormdb.UCICheckoutWorkingTree,
		OwnerPrincipal: subject.Principal,
		LocatorRef:     locator,
	})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("register no-view fixture checkout: %w", err)
	}
	parserBundleDigest := string(uci.TreeSitterBundleDigest())
	profile, err := contexts.CreateProfile(ctx, gormdb.CreateProfileInput{
		ParserBundleDigest:   parserBundleDigest,
		ResolverRevision:     "operator-code-live-no-view-resolver-v1",
		ChunkerRevision:      "operator-code-live-no-view-chunker-v1",
		IgnorePolicyDigest:   fixtureDigest("operator-code-live-no-view-ignore-v1"),
		BuildContextJSON:     `{"fixture":"operator-code-live-no-view"}`,
		SecretPolicyRevision: "operator-code-live-no-view-secret-v1",
	})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("create no-view fixture profile: %w", err)
	}
	selector, err := uci.CheckoutIndexBindingSelector(uci.RegisteredCheckoutSelector{
		Scope:     uci.IndexScope{SourceID: source.SourceID, CheckoutID: checkout.CheckoutID, IncarnationID: checkout.IncarnationID},
		ProfileID: profile.ProfileID,
	})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("select no-view fixture checkout: %w", err)
	}
	binding, err := contexts.LoadIndexBinding(ctx, selector)
	if err != nil || binding.Context != nil || binding.Scope.SourceID != source.SourceID || binding.Scope.CheckoutID != checkout.CheckoutID || binding.Scope.IncarnationID != checkout.IncarnationID || binding.ProfileID != profile.ProfileID || binding.WorkstationID != workstationID || binding.LocalRootID != locator {
		return fixtureOutput{}, fmt.Errorf("read back no-view fixture registration")
	}
	grants := worker.NewCodeGrantApplication(gormdb.NewBrowserReadGrantStore(store.DB))
	issuer := auth.SessionForBrowserUser(user.Role, user.ID)
	grant, err := grants.Issue(ctx, issuer, worker.IssueCodeGrantInput{Target: subject, SourceID: source.SourceID, CheckoutID: checkout.CheckoutID})
	if err != nil {
		return fixtureOutput{}, fmt.Errorf("issue no-view fixture grant: %w", err)
	}
	current, found, err := grants.Active(ctx, issuer, source.SourceID, checkout.CheckoutID)
	if err != nil || !found || current.GrantRef != grant.GrantRef || current.SourceID != source.SourceID || current.CheckoutID != checkout.CheckoutID {
		return fixtureOutput{}, fmt.Errorf("read back no-view fixture grant")
	}
	if err := os.WriteFile(in.keycardFile, []byte(keycard+"\n"), 0o600); err != nil {
		return fixtureOutput{}, fmt.Errorf("write no-view fixture keycard")
	}
	return fixtureOutput{
		Query: fixtureQuery, ExpectedSearch: fixtureQuery, ExpectedGraph: fixtureExpectedGraph, ExpectedSource: fixtureExpectedSource, ExpectedMarker: marker,
		NoView: &noViewFixtureOutput{
			SourceID: source.SourceID, CheckoutID: checkout.CheckoutID, IncarnationID: checkout.IncarnationID, AnalysisProfileID: profile.ProfileID,
			ParserBundleDigest: parserBundleDigest,
		},
	}, nil
}

func fixtureClientKeycard(ctx context.Context, store *gormdb.Store, name string, subject auth.BrowserSubject) (string, string, error) {
	if store == nil || !subject.Valid() {
		return "", "", errors.New("create fixture client keycard")
	}
	randomBytes := make([]byte, auth.TokenBodyLen/2)
	if _, err := cryptorand.Read(randomBytes); err != nil {
		return "", "", fmt.Errorf("generate fixture client keycard: %w", err)
	}
	raw := auth.TokenRawPrefix + hex.EncodeToString(randomBytes)
	hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost)
	if err != nil {
		return "", "", fmt.Errorf("hash fixture client keycard: %w", err)
	}
	token, err := gormdb.NewTokenStore(store).CreateWithPrincipal(ctx, gormdb.TokenCreatePrincipalInput{
		Name:          name,
		TokenHash:     string(hash),
		TokenPrefix:   raw[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen],
		Scope:         string(auth.RoleReadOnly),
		Principal:     subject.Principal,
		PrincipalKind: string(subject.Kind),
	})
	if err != nil || token == nil || token.ID == "" {
		return "", "", fmt.Errorf("store fixture client keycard")
	}
	return raw, token.ID, nil
}

func seedFixtureQueueCandidates(ctx context.Context, db *gorm.DB, project string) error {
	candidates := gormdb.NewCandidateStore(db, nil)
	for index := range fixtureQueueSeedCount {
		candidate, err := models.NewCrystallizationCandidate(
			fmt.Sprintf("operator-code-live-queue-%s-%d", project, index),
			fmt.Sprintf("operator code live queue candidate %d", index+1),
			"semantic",
			models.CandidateOptions{
				Tier:             "semantic",
				EpistemicType:    "decision",
				PrivacyScope:     "project",
				AffectedProjects: []string{project},
				Confidence:       0.9,
				RecurrenceCount:  1,
			},
		)
		if err != nil {
			return fmt.Errorf("create fixture queue candidate: %w", err)
		}
		if _, err := candidates.Create(ctx, candidate); err != nil {
			return fmt.Errorf("seed fixture queue candidate: %w", err)
		}
	}
	return nil
}

func fixtureSourceFor(in invocation) ([]byte, error) {
	if in.sourceFile == "" {
		return append([]byte(nil), fixtureSource...), nil
	}
	if filepath.Base(in.sourceFile) != fixtureSourcePath {
		return nil, fmt.Errorf("fixture source path is invalid")
	}
	source, err := os.ReadFile(in.sourceFile)
	if err != nil || len(source) == 0 {
		return nil, fmt.Errorf("read fixture source")
	}
	return source, nil
}

func fixtureMarker(source []byte) string {
	for _, candidate := range []string{"operator-code-fixture-a", "operator-code-fixture-b", "operator-code-fixture"} {
		if strings.Contains(string(source), candidate) {
			return candidate
		}
	}
	return ""
}

func fixtureFrame(sourceID, profileID string, admissionProfile uci.IndexAdmissionArtifactProfile, extraction uci.GoExtractionProfile, source []byte) (uci.IndexAdmissionFrame, error) {
	artifact, err := uci.NewIndexAdmissionArtifactFromGo(sourceID, admissionProfile, source, uci.ExtractGo(source, extraction))
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
