package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
	"github.com/thebtf/engram/pkg/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm/logger"
)

func TestParseInvocationAcceptsClosedFixtureInputs(t *testing.T) {
	got, err := parseInvocation([]string{
		"--project", "operator-code-live-1",
		"--browser-email", "fixture@example.invalid",
		"--dsn-file", "private-dsn.txt",
	})
	if err != nil {
		t.Fatalf("parseInvocation() error = %v", err)
	}
	if got != (invocation{dsnFile: "private-dsn.txt", browserEmail: "fixture@example.invalid", project: "operator-code-live-1", mode: "published"}) {
		t.Fatalf("parseInvocation() = %#v", got)
	}
}

func TestParseInvocationRequiresNoViewFixtureKeycardFile(t *testing.T) {
	base := []string{"--dsn-file", "private-dsn.txt", "--browser-email", "fixture@example.invalid", "--project", "operator-code-live-1", "--source-file", "fixture.go", "--mode", "no-view"}
	if _, err := parseInvocation(base); err == nil {
		t.Fatal("parseInvocation accepted a no-view fixture without a private keycard file")
	}
	got, err := parseInvocation(append(base, "--keycard-file", "private-keycard.txt"))
	if err != nil || got.mode != "no-view" || got.keycardFile != "private-keycard.txt" {
		t.Fatalf("parseInvocation(no-view) = %#v, %v", got, err)
	}
}

func TestFixtureSourceForReadsTheDeclaredWorktreeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), fixtureSourcePath)
	want := []byte(`package fixture

const CodeExplorerFixtureMessage = "operator-code-fixture-a"

func CodeExplorerFixtureB() string { return CodeExplorerFixtureMessage }
func CodeExplorerFixtureA() string { return CodeExplorerFixtureB() }
`)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := fixtureSourceFor(invocation{sourceFile: path})
	if err != nil || string(got) != string(want) || fixtureMarker(got) != "operator-code-fixture-a" {
		t.Fatalf("fixtureSourceFor() = %q, %v", got, err)
	}
	if _, err := fixtureSourceFor(invocation{sourceFile: filepath.Join(t.TempDir(), "outside.go")}); err == nil {
		t.Fatal("fixtureSourceFor accepted a non-fixture filename")
	}
}

func TestParseInvocationRejectsUnexpectedOrUnsafeFixtureInputs(t *testing.T) {
	valid := []string{"--dsn-file", "private-dsn.txt", "--browser-email", "fixture@example.invalid", "--project", "operator-code-live-1"}
	for name, args := range map[string][]string{
		"duplicate":   {"--dsn-file", "a", "--dsn-file", "b", "--project", "operator-code-live-1", "--browser-email", "fixture@example.invalid"},
		"unknown":     {"--dsn-file", "private-dsn.txt", "--browser-email", "fixture@example.invalid", "--unexpected", "value"},
		"bad email":   {"--dsn-file", "private-dsn.txt", "--browser-email", "fixture example.invalid", "--project", "operator-code-live-1"},
		"bad project": {"--dsn-file", "private-dsn.txt", "--browser-email", "fixture@example.invalid", "--project", "../outside"},
		"equals":      {"--dsn-file=private-dsn.txt", "ignored", "--browser-email", "fixture@example.invalid", "--project", "operator-code-live-1"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseInvocation(args); err == nil {
				t.Fatalf("parseInvocation(%q) succeeded", args)
			}
		})
	}
	if _, err := parseInvocation(valid); err != nil {
		t.Fatalf("parseInvocation(valid) error = %v", err)
	}
}

func TestRunEmitsOnlyNonSecretFixtureState(t *testing.T) {
	const dsn = "postgres://fixture:secret@127.0.0.1/engram?sslmode=disable"
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--dsn-file", "private-dsn.txt",
		"--browser-email", "fixture@example.invalid",
		"--project", "operator-code-live-1",
	}, &stdout, &stderr, commandDependencies{
		readFile: func(path string) ([]byte, error) {
			if path != "private-dsn.txt" {
				t.Fatalf("readFile path = %q", path)
			}
			return []byte(dsn), nil
		},
		provision: func(_ context.Context, gotDSN string, in invocation) (fixtureOutput, error) {
			if gotDSN != dsn {
				t.Fatalf("provision DSN = %q", gotDSN)
			}
			if in.browserEmail != "fixture@example.invalid" || in.project != "operator-code-live-1" {
				t.Fatalf("provision input = %#v", in)
			}
			return fixtureOutput{Query: fixtureQuery, ExpectedSearch: fixtureQuery, ExpectedGraph: fixtureExpectedGraph, ExpectedSource: fixtureExpectedSource}, nil
		},
	})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("run() = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), dsn) {
		t.Fatalf("run output exposed DSN: %q", stdout.String())
	}
	var output fixtureOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output.ExpectedGraph != fixtureExpectedGraph || output.ExpectedSource != fixtureExpectedSource {
		t.Fatalf("fixture output = %#v", output)
	}
}

func TestRunRedactsPrivateDSNOnProvisionFailure(t *testing.T) {
	const dsn = "postgres://fixture:private@127.0.0.1/engram?sslmode=disable"
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--dsn-file", "private-dsn.txt",
		"--browser-email", "fixture@example.invalid",
		"--project", "operator-code-live-1",
	}, &stdout, &stderr, commandDependencies{
		readFile: func(string) ([]byte, error) { return []byte(dsn), nil },
		provision: func(context.Context, string, invocation) (fixtureOutput, error) {
			return fixtureOutput{}, errors.New(dsn)
		},
	})
	if code != 1 || strings.Contains(stderr.String(), dsn) || strings.Contains(stdout.String(), dsn) {
		t.Fatalf("run() = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestFixtureFrameCarriesSearchableAAndResolvedGraphB(t *testing.T) {
	extraction := uci.GoExtractionProfile{ProfileKey: "operator-code-live-v1", ParserKey: "go-parser"}
	profile, err := uci.GoIndexAdmissionArtifactProfile(extraction)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := fixtureFrame("10000000-0000-4000-8000-000000000001", "20000000-0000-4000-8000-000000000002", profile, extraction, fixtureSource)
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Artifacts) != 1 || len(frame.Artifacts[0].Chunks) == 0 || len(frame.Memberships) != 1 || len(frame.EdgeReplacements) != 1 || len(frame.EdgeReplacements[0].Edges) != 1 {
		t.Fatalf("fixture frame shape = %#v", frame)
	}
	edge := frame.EdgeReplacements[0].Edges[0]
	if edge.Target == nil || edge.Target.PathKey != fixtureSourcePath || edge.Target.SymbolKey == nil || *edge.Target.SymbolKey != "func:"+fixtureExpectedGraph || edge.ResolutionState != uci.IndexResolutionState("resolved") {
		t.Fatalf("fixture graph edge = %#v", edge)
	}
	if fixtureQuery != "CodeExplorerFixtureA" || fixtureExpectedSource != "CodeExplorerFixtureA" || fixtureExpectedGraph != "CodeExplorerFixtureB" {
		t.Fatalf("fixture scenario = query %q, source %q, graph %q", fixtureQuery, fixtureExpectedSource, fixtureExpectedGraph)
	}
}

func TestRunProvisionsIsolatedPostgresFixture(t *testing.T) {
	fixture := newPostgresFixtureEnvironment(t)

	published := fixture.runPublished(t)
	fixture.assertPublishedDurableState(t, published)

	noView := fixture.runNoView(t)
	fixture.assertNoViewDurableState(t, noView)
}

type postgresFixtureEnvironment struct {
	adminStore       *gormdb.Store
	store            *gormdb.Store
	schema           string
	scopedDSN        string
	projectPrefix    string
	publishedProject string
	noViewProject    string
	email            string
	password         string
	root             string
	dsnFile          string
	sourceFile       string
	passwordFile     string
	keycardFile      string
}

type fixtureRunReceipt struct {
	output fixtureOutput
	stdout string
	stderr string
}

func newPostgresFixtureEnvironment(t *testing.T) *postgresFixtureEnvironment {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("DATABASE_DSN"))
	if dsn == "" {
		t.Skip("DATABASE_DSN is required for the PostgreSQL fixture integration test")
	}

	schema := fmt.Sprintf("operator_code_fixture_%x", time.Now().UnixNano())
	adminStore, err := gormdb.NewStore(gormdb.Config{DSN: dsn, MaxConns: 2, LogLevel: logger.Silent})
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	if err := adminStore.DB.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		adminStore.Close()
		t.Fatalf("create isolated fixture schema: %v", err)
	}

	fixture := &postgresFixtureEnvironment{
		adminStore: adminStore,
		schema:     schema,
		root:       t.TempDir(),
	}
	t.Cleanup(func() { fixture.cleanup(t) })

	fixture.scopedDSN, err = fixtureSchemaDSN(dsn, schema)
	if err != nil {
		t.Fatalf("scope fixture database: %v", err)
	}
	fixture.projectPrefix = fmt.Sprintf("operator-code-live-%d", time.Now().UnixNano())
	fixture.publishedProject = fixture.projectPrefix + "-published"
	fixture.noViewProject = fixture.projectPrefix + "-no-view"
	fixture.email = fixture.projectPrefix + "@fixture.invalid"
	fixture.password = "fixture-browser-password-" + fixture.projectPrefix
	fixture.dsnFile = filepath.Join(fixture.root, "private-dsn.txt")
	fixture.sourceFile = filepath.Join(fixture.root, fixtureSourcePath)
	fixture.passwordFile = filepath.Join(fixture.root, "private-password.txt")
	fixture.keycardFile = filepath.Join(fixture.root, "private-keycard.txt")
	fixture.writePrivateInputs(t)
	return fixture
}

func (fixture *postgresFixtureEnvironment) writePrivateInputs(t *testing.T) {
	t.Helper()
	fixture.writePrivateInput(t, fixture.dsnFile, []byte(fixture.scopedDSN+"\n"))
	fixture.writePrivateInput(t, fixture.sourceFile, fixtureSource)
	fixture.writePrivateInput(t, fixture.passwordFile, []byte(fixture.password+"\n"))
}

func (fixture *postgresFixtureEnvironment) writePrivateInput(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write private fixture input %q: %v", filepath.Base(path), err)
	}
}

func (fixture *postgresFixtureEnvironment) runPublished(t *testing.T) fixtureRunReceipt {
	t.Helper()
	return fixture.run(t, "published", []string{
		"--dsn-file", fixture.dsnFile,
		"--browser-email", fixture.email,
		"--project", fixture.publishedProject,
		"--source-file", fixture.sourceFile,
		"--password-file", fixture.passwordFile,
	})
}

func (fixture *postgresFixtureEnvironment) runNoView(t *testing.T) fixtureRunReceipt {
	t.Helper()
	return fixture.run(t, "no-view", []string{
		"--dsn-file", fixture.dsnFile,
		"--browser-email", fixture.email,
		"--project", fixture.noViewProject,
		"--source-file", fixture.sourceFile,
		"--mode", fixtureModeNoView,
		"--keycard-file", fixture.keycardFile,
	})
}

func (fixture *postgresFixtureEnvironment) run(t *testing.T, mode string, args []string) fixtureRunReceipt {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if status := run(context.Background(), args, &stdout, &stderr, defaultCommandDependencies()); status != 0 {
		t.Fatalf("%s fixture run = %d, stderr = %q", mode, status, stderr.String())
	}
	var output fixtureOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode %s fixture receipt: %v", mode, err)
	}
	return fixtureRunReceipt{output: output, stdout: stdout.String(), stderr: stderr.String()}
}

func (fixture *postgresFixtureEnvironment) assertPublishedDurableState(t *testing.T, receipt fixtureRunReceipt) {
	t.Helper()
	published := receipt.output
	if published.NoView != nil || published.Query != fixtureQuery || published.ExpectedSearch != fixtureQuery || published.ExpectedGraph != fixtureExpectedGraph || published.ExpectedSource != fixtureExpectedSource || published.ExpectedMarker != "operator-code-fixture" {
		t.Fatalf("published fixture receipt = %#v", published)
	}
	assertFixtureOutputRedacted(t, receipt.stdout, receipt.stderr, fixture.scopedDSN, fixture.password)

	store := fixture.verificationStore(t)
	users := gormdb.NewUserStore(store.DB)
	user, err := users.GetUserByEmail(fixture.email)
	if err != nil || user.Role != gormdb.DashboardRoleOperator {
		t.Fatalf("load provisioned browser user = %#v, %v", user, err)
	}
	var source gormdb.UCISource
	if err := store.DB.Where("auth_realm = ? AND display_name = ?", fixtureAuthRealm, "Operator Code Fixture "+fixture.publishedProject).First(&source).Error; err != nil {
		t.Fatalf("load published fixture source: %v", err)
	}
	var checkout gormdb.UCICheckout
	if err := store.DB.Where("source_id = ?", source.SourceID).First(&checkout).Error; err != nil {
		t.Fatalf("load published fixture checkout: %v", err)
	}
	contexts := gormdb.NewUCIContextStore(store.DB)
	view, err := contexts.GetCurrentView(context.Background(), checkout.CheckoutID)
	if err != nil || view.State != gormdb.UCIViewPublished || view.SourceID != source.SourceID || view.ProfileID == "" {
		t.Fatalf("published fixture view = %#v, %v", view, err)
	}
	fixture.assertPublishedIndexState(t, store, source.SourceID)
	candidates, err := gormdb.NewCandidateStore(store.DB, nil).ListByStatus(context.Background(), fixture.publishedProject, models.CandidateStatusPending, fixtureQueueSeedCount+1)
	if err != nil || len(candidates) != fixtureQueueSeedCount {
		t.Fatalf("published fixture queue candidates = %d, %v", len(candidates), err)
	}
	grants := gormdb.NewBrowserReadGrantStore(store.DB)
	if allowed, err := grants.CanRead(context.Background(), user.ID, source.SourceID, checkout.CheckoutID); err != nil || !allowed {
		t.Fatalf("published fixture grant allowed = %t, %v", allowed, err)
	}
}

func (fixture *postgresFixtureEnvironment) assertPublishedIndexState(t *testing.T, store *gormdb.Store, sourceID string) {
	t.Helper()
	var chunks, edges int64
	if err := store.DB.Model(&gormdb.UCIChunk{}).Where("source_id = ?", sourceID).Count(&chunks).Error; err != nil || chunks == 0 {
		t.Fatalf("published fixture chunks = %d, %v", chunks, err)
	}
	if err := store.DB.Model(&gormdb.UCIResolvedEdge{}).Where("source_id = ?", sourceID).Count(&edges).Error; err != nil || edges == 0 {
		t.Fatalf("published fixture edges = %d, %v", edges, err)
	}
}

func (fixture *postgresFixtureEnvironment) assertNoViewDurableState(t *testing.T, receipt fixtureRunReceipt) {
	t.Helper()
	noView := receipt.output
	if noView.NoView == nil || noView.Query != fixtureQuery || noView.ExpectedSearch != fixtureQuery || noView.ExpectedGraph != fixtureExpectedGraph || noView.ExpectedSource != fixtureExpectedSource || noView.ExpectedMarker != "operator-code-fixture" || noView.NoView.ParserBundleDigest != string(uci.TreeSitterBundleDigest()) {
		t.Fatalf("no-view fixture receipt = %#v", noView)
	}
	keycard := fixture.noViewKeycard(t)
	assertFixtureOutputRedacted(t, receipt.stdout, receipt.stderr, fixture.scopedDSN, fixture.password, keycard)

	store := fixture.verificationStore(t)
	var token gormdb.APIToken
	if err := store.DB.Where("name = ?", fixtureWorkstationPrefix+fixture.noViewProject).First(&token).Error; err != nil || bcrypt.CompareHashAndPassword([]byte(token.TokenHash), []byte(keycard)) != nil {
		t.Fatalf("no-view fixture keycard is not durably stored: %v", err)
	}
	selector, err := uci.CheckoutIndexBindingSelector(uci.RegisteredCheckoutSelector{
		Scope:     uci.IndexScope{SourceID: noView.NoView.SourceID, CheckoutID: noView.NoView.CheckoutID, IncarnationID: noView.NoView.IncarnationID},
		ProfileID: noView.NoView.AnalysisProfileID,
	})
	if err != nil {
		t.Fatalf("select no-view fixture binding: %v", err)
	}
	contexts := gormdb.NewUCIContextStore(store.DB)
	binding, err := contexts.LoadIndexBinding(context.Background(), selector)
	if err != nil || binding.Context != nil || binding.Scope.SourceID != noView.NoView.SourceID || binding.Scope.CheckoutID != noView.NoView.CheckoutID || binding.Scope.IncarnationID != noView.NoView.IncarnationID || binding.ProfileID != noView.NoView.AnalysisProfileID {
		t.Fatalf("no-view fixture binding = %#v, %v", binding, err)
	}
	user, err := gormdb.NewUserStore(store.DB).GetUserByEmail(fixture.email)
	if err != nil {
		t.Fatalf("load provisioned browser user: %v", err)
	}
	grants := gormdb.NewBrowserReadGrantStore(store.DB)
	if allowed, err := grants.CanRead(context.Background(), user.ID, noView.NoView.SourceID, noView.NoView.CheckoutID); err != nil || !allowed {
		t.Fatalf("no-view fixture grant allowed = %t, %v", allowed, err)
	}
}

func (fixture *postgresFixtureEnvironment) noViewKeycard(t *testing.T) string {
	t.Helper()
	keycardBytes, err := os.ReadFile(fixture.keycardFile)
	if err != nil {
		t.Fatalf("read private no-view keycard: %v", err)
	}
	keycard := strings.TrimSpace(string(keycardBytes))
	if !strings.HasPrefix(keycard, auth.TokenRawPrefix) {
		t.Fatal("no-view fixture keycard is unavailable")
	}
	return keycard
}

func (fixture *postgresFixtureEnvironment) verificationStore(t *testing.T) *gormdb.Store {
	t.Helper()
	if fixture.store != nil {
		return fixture.store
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: fixture.scopedDSN, MaxConns: 2, LogLevel: logger.Silent})
	if err != nil {
		t.Fatalf("open fixture verification store: %v", err)
	}
	fixture.store = store
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func (fixture *postgresFixtureEnvironment) cleanup(t *testing.T) {
	t.Helper()
	defer fixture.adminStore.Close()
	if err := fixture.adminStore.DB.Exec("DROP SCHEMA " + fixture.schema + " CASCADE").Error; err != nil {
		t.Errorf("drop isolated fixture schema: %v", err)
	}
	if err := os.RemoveAll(fixture.root); err != nil {
		t.Errorf("remove private fixture files: %v", err)
	}
	fixture.assertCleanedUp(t)
}

func (fixture *postgresFixtureEnvironment) assertCleanedUp(t *testing.T) {
	t.Helper()
	var remaining int64
	if err := fixture.adminStore.DB.Raw("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?", fixture.schema).Scan(&remaining).Error; err != nil || remaining != 0 {
		t.Errorf("isolated fixture schema remains after cleanup: %d, %v", remaining, err)
	}
	if _, err := os.Stat(fixture.root); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("private fixture root remains after cleanup: %v", err)
	}
}

func assertFixtureOutputRedacted(t *testing.T, stdout, stderr string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && (strings.Contains(stdout, secret) || strings.Contains(stderr, secret)) {
			t.Fatal("fixture command disclosed private input")
		}
	}
}

func fixtureSchemaDSN(dsn, schema string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse fixture DSN: %w", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
