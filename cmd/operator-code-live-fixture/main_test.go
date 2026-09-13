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
	scopedDSN, err := fixtureSchemaDSN(dsn, schema)
	if err != nil {
		adminStore.DB.Exec("DROP SCHEMA " + schema + " CASCADE")
		adminStore.Close()
		t.Fatalf("scope fixture database: %v", err)
	}
	projectPrefix := fmt.Sprintf("operator-code-live-%d", time.Now().UnixNano())
	publishedProject := projectPrefix + "-published"
	noViewProject := projectPrefix + "-no-view"
	email := projectPrefix + "@fixture.invalid"
	password := "fixture-browser-password-" + projectPrefix
	root := t.TempDir()
	dsnFile := filepath.Join(root, "private-dsn.txt")
	sourceFile := filepath.Join(root, fixtureSourcePath)
	passwordFile := filepath.Join(root, "private-password.txt")
	keycardFile := filepath.Join(root, "private-keycard.txt")
	for path, content := range map[string][]byte{
		dsnFile:      []byte(scopedDSN + "\n"),
		sourceFile:   fixtureSource,
		passwordFile: []byte(password + "\n"),
	} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("write private fixture input %q: %v", filepath.Base(path), err)
		}
	}
	t.Cleanup(func() {
		defer adminStore.Close()
		if err := adminStore.DB.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop isolated fixture schema: %v", err)
		}
		var remaining int64
		if err := adminStore.DB.Raw("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?", schema).Scan(&remaining).Error; err != nil || remaining != 0 {
			t.Errorf("isolated fixture schema remains after cleanup: %d, %v", remaining, err)
		}
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove private fixture files: %v", err)
		}
		if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("private fixture root remains after cleanup: %v", err)
		}
	})

	ctx := context.Background()
	var publishedStdout, publishedStderr bytes.Buffer
	if status := run(ctx, []string{
		"--dsn-file", dsnFile,
		"--browser-email", email,
		"--project", publishedProject,
		"--source-file", sourceFile,
		"--password-file", passwordFile,
	}, &publishedStdout, &publishedStderr, defaultCommandDependencies()); status != 0 {
		t.Fatalf("published fixture run = %d, stderr = %q", status, publishedStderr.String())
	}
	var published fixtureOutput
	if err := json.Unmarshal(publishedStdout.Bytes(), &published); err != nil {
		t.Fatalf("decode published fixture receipt: %v", err)
	}
	if published.NoView != nil || published.Query != fixtureQuery || published.ExpectedSearch != fixtureQuery || published.ExpectedGraph != fixtureExpectedGraph || published.ExpectedSource != fixtureExpectedSource || published.ExpectedMarker != "operator-code-fixture" {
		t.Fatalf("published fixture receipt = %#v", published)
	}
	assertFixtureOutputRedacted(t, publishedStdout.String(), publishedStderr.String(), scopedDSN, password)

	store, err := gormdb.NewStore(gormdb.Config{DSN: scopedDSN, MaxConns: 2, LogLevel: logger.Silent})
	if err != nil {
		t.Fatalf("open fixture verification store: %v", err)
	}
	defer store.Close()
	users := gormdb.NewUserStore(store.DB)
	user, err := users.GetUserByEmail(email)
	if err != nil || user.Role != gormdb.DashboardRoleOperator {
		t.Fatalf("load provisioned browser user = %#v, %v", user, err)
	}
	var publishedSource gormdb.UCISource
	if err := store.DB.Where("auth_realm = ? AND display_name = ?", fixtureAuthRealm, "Operator Code Fixture "+publishedProject).First(&publishedSource).Error; err != nil {
		t.Fatalf("load published fixture source: %v", err)
	}
	var publishedCheckout gormdb.UCICheckout
	if err := store.DB.Where("source_id = ?", publishedSource.SourceID).First(&publishedCheckout).Error; err != nil {
		t.Fatalf("load published fixture checkout: %v", err)
	}
	contexts := gormdb.NewUCIContextStore(store.DB)
	publishedView, err := contexts.GetCurrentView(ctx, publishedCheckout.CheckoutID)
	if err != nil || publishedView.State != gormdb.UCIViewPublished || publishedView.SourceID != publishedSource.SourceID || publishedView.ProfileID == "" {
		t.Fatalf("published fixture view = %#v, %v", publishedView, err)
	}
	var chunks, edges int64
	if err := store.DB.Model(&gormdb.UCIChunk{}).Where("source_id = ?", publishedSource.SourceID).Count(&chunks).Error; err != nil || chunks == 0 {
		t.Fatalf("published fixture chunks = %d, %v", chunks, err)
	}
	if err := store.DB.Model(&gormdb.UCIResolvedEdge{}).Where("source_id = ?", publishedSource.SourceID).Count(&edges).Error; err != nil || edges == 0 {
		t.Fatalf("published fixture edges = %d, %v", edges, err)
	}
	candidates, err := gormdb.NewCandidateStore(store.DB, nil).ListByStatus(ctx, publishedProject, models.CandidateStatusPending, fixtureQueueSeedCount+1)
	if err != nil || len(candidates) != fixtureQueueSeedCount {
		t.Fatalf("published fixture queue candidates = %d, %v", len(candidates), err)
	}
	grants := gormdb.NewBrowserReadGrantStore(store.DB)
	if allowed, err := grants.CanRead(ctx, user.ID, publishedSource.SourceID, publishedCheckout.CheckoutID); err != nil || !allowed {
		t.Fatalf("published fixture grant allowed = %t, %v", allowed, err)
	}

	var noViewStdout, noViewStderr bytes.Buffer
	if status := run(ctx, []string{
		"--dsn-file", dsnFile,
		"--browser-email", email,
		"--project", noViewProject,
		"--source-file", sourceFile,
		"--mode", fixtureModeNoView,
		"--keycard-file", keycardFile,
	}, &noViewStdout, &noViewStderr, defaultCommandDependencies()); status != 0 {
		t.Fatalf("no-view fixture run = %d, stderr = %q", status, noViewStderr.String())
	}
	var noView fixtureOutput
	if err := json.Unmarshal(noViewStdout.Bytes(), &noView); err != nil {
		t.Fatalf("decode no-view fixture receipt: %v", err)
	}
	if noView.NoView == nil || noView.Query != fixtureQuery || noView.ExpectedSearch != fixtureQuery || noView.ExpectedGraph != fixtureExpectedGraph || noView.ExpectedSource != fixtureExpectedSource || noView.ExpectedMarker != "operator-code-fixture" || noView.NoView.ParserBundleDigest != string(uci.TreeSitterBundleDigest()) {
		t.Fatalf("no-view fixture receipt = %#v", noView)
	}
	keycardBytes, err := os.ReadFile(keycardFile)
	if err != nil {
		t.Fatalf("read private no-view keycard: %v", err)
	}
	keycard := strings.TrimSpace(string(keycardBytes))
	if !strings.HasPrefix(keycard, auth.TokenRawPrefix) {
		t.Fatal("no-view fixture keycard is unavailable")
	}
	assertFixtureOutputRedacted(t, noViewStdout.String(), noViewStderr.String(), scopedDSN, password, keycard)
	var token gormdb.APIToken
	if err := store.DB.Where("name = ?", fixtureWorkstationPrefix+noViewProject).First(&token).Error; err != nil || bcrypt.CompareHashAndPassword([]byte(token.TokenHash), []byte(keycard)) != nil {
		t.Fatalf("no-view fixture keycard is not durably stored: %v", err)
	}
	selector, err := uci.CheckoutIndexBindingSelector(uci.RegisteredCheckoutSelector{
		Scope:     uci.IndexScope{SourceID: noView.NoView.SourceID, CheckoutID: noView.NoView.CheckoutID, IncarnationID: noView.NoView.IncarnationID},
		ProfileID: noView.NoView.AnalysisProfileID,
	})
	if err != nil {
		t.Fatalf("select no-view fixture binding: %v", err)
	}
	binding, err := contexts.LoadIndexBinding(ctx, selector)
	if err != nil || binding.Context != nil || binding.Scope.SourceID != noView.NoView.SourceID || binding.Scope.CheckoutID != noView.NoView.CheckoutID || binding.Scope.IncarnationID != noView.NoView.IncarnationID || binding.ProfileID != noView.NoView.AnalysisProfileID {
		t.Fatalf("no-view fixture binding = %#v, %v", binding, err)
	}
	if allowed, err := grants.CanRead(ctx, user.ID, noView.NoView.SourceID, noView.NoView.CheckoutID); err != nil || !allowed {
		t.Fatalf("no-view fixture grant allowed = %t, %v", allowed, err)
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
