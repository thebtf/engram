package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseInvocationRequiresClosedCandidateInputs(t *testing.T) {
	server := filepath.Join(t.TempDir(), "engram-server")
	if runtime.GOOS == "windows" {
		server += ".exe"
	}
	if err := os.WriteFile(server, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, err := parseInvocation([]string{"--dsn-file", "private.txt", "--server", server})
	if err != nil || parsed.dsnFile != "private.txt" || parsed.server != server {
		t.Fatalf("parse valid invocation = %#v, %v", parsed, err)
	}
	for _, args := range [][]string{
		{"--dsn-file", "private.txt", "--server", "relative-server"},
		{"--dsn-file", "private.txt", "--unknown", server},
		{"--dsn-file", "private.txt"},
	} {
		if _, err := parseInvocation(args); err == nil {
			t.Fatalf("parse unsafe invocation %q succeeded", args)
		}
	}
}

func TestValidFixtureDSNRejectsNonDisposableTargets(t *testing.T) {
	for _, raw := range []string{
		"postgres://fixture:fixture@127.0.0.1:5432/engram_test?sslmode=disable",
		"postgresql://fixture:fixture@127.0.0.1:5432/retirement_test?sslmode=disable",
	} {
		if !validFixtureDSN(raw) {
			t.Fatalf("safe DSN rejected: %q", raw)
		}
	}
	for _, raw := range []string{
		"postgres://fixture:fixture@db.example:5432/engram_test?sslmode=disable",
		"postgres://fixture:fixture@127.0.0.1:5432/engram_production?sslmode=disable",
		"postgres://user:pass@127.0.0.1:5432/engram",
		"postgres://user:pass_test@127.0.0.1:5432/engram?sslmode=disable",
		"postgres://user:pass@127.0.0.1:5432/engram?application_name=fixture_test",
		"postgres://fixture:fixture@127.0.0.1:5432/engram_staging_test?sslmode=disable",
		"postgres://fixture:fixture@127.0.0.1:5432/latest?sslmode=disable",
		"postgres://fixture:fixture@127.0.0.1:5432/engram_test?host=remote",
		"postgres://fixture:fixture@127.0.0.1:5432/engram_test?dbname=production",
		"postgres://fixture@127.0.0.1:5432/engram_test",
		"postgres://fixture:fixture@127.0.0.1/engram_test",
		"postgres://fixture:fixture@localhost:5432/engram_test",
		"not-a-dsn",
	} {
		if validFixtureDSN(raw) {
			t.Fatalf("unsafe DSN accepted: %q", raw)
		}
	}
}

func TestRetirementFixtureRejectsInheritedPostgresSettings(t *testing.T) {
	t.Setenv("PGOPTIONS", "-c search_path=public")
	_, err := execute(context.Background(), "postgres://fixture:fixture@127.0.0.1:5432/engram_test", "unused")
	if err == nil || !strings.Contains(err.Error(), "refuses inherited PostgreSQL settings") {
		t.Fatalf("inherited PostgreSQL settings error = %v", err)
	}
}

func TestRecordRetirementCleanupPreservesFailure(t *testing.T) {
	cleanupErr := errors.New("synthetic cleanup failure")
	completed := true
	var resultErr error
	recordRetirementCleanup(&completed, &resultErr, "remove fixture", cleanupErr)
	if completed || !errors.Is(resultErr, cleanupErr) {
		t.Fatalf("failed cleanup: completed=%t error=%v", completed, resultErr)
	}
	recordRetirementCleanup(&completed, &resultErr, "remove fixture", nil)
	if !completed || !errors.Is(resultErr, cleanupErr) {
		t.Fatalf("cleanup error was lost: completed=%t error=%v", completed, resultErr)
	}
}

func TestRetirementFixtureAgainstDisposablePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("ENGRAM_OPERATOR_RETIREMENT_FIXTURE_TEST_DSN"))
	if dsn == "" {
		t.Skip("ENGRAM_OPERATOR_RETIREMENT_FIXTURE_TEST_DSN must explicitly name a disposable loopback test database")
	}
	if !validFixtureDSN(dsn) {
		t.Fatal("retirement fixture requires an explicitly designated loopback database ending in _test")
	}
	root := filepath.Clean(filepath.Join(mustGetwd(t), "..", ".."))
	binary := filepath.Join(t.TempDir(), "engram-server")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-tags", "fts5", "-o", binary, "./cmd/engram-server")
	build.Dir = root
	build.Env = os.Environ()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build exact candidate server: %v\n%s", err, output)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	receipt, err := execute(ctx, dsn, binary)
	if err != nil {
		t.Fatalf("execute retirement fixture: %v", err)
	}
	if receipt.Schema != retirementReceiptSchema || receipt.Status != "PASS" || receipt.WriterDenominator.Total != 10 || !receipt.Cleanup.ServerProcessesStopped || !receipt.Cleanup.SchemaRemoved || !receipt.Cleanup.TemporaryRootRemoved {
		t.Fatalf("retirement receipt=%#v", receipt)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("encode retirement receipt: %v", err)
	}
	t.Logf("DA03 source receipt: %s", encoded)
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return workingDirectory
}

func TestCleanEnvironmentRemovesInheritedServerConfiguration(t *testing.T) {
	env := cleanEnvironment([]string{"PATH=tools", "ENGRAM_VNEXT_ENABLED=false", "ENGRAM_VNEXT_F_ENABLED=false", "ENGRAM_LIFECYCLE_ENABLED=false", "ENGRAM_CONTINUITY_SLOT_ENABLED=false", "ENGRAM_ADAPTIVE_ENABLED=false", "ENGRAM_CRYSTALLIZATION_ENABLED=false", "ENGRAM_GRAPH_ENABLED=true", "ENGRAM_EMBEDDING_URL=foreign-provider", "DATABASE_DSN=foreign", "PGOPTIONS=-c search_path=public", "WORKSTATION_ID=foreign"}, "WORKSTATION_ID")
	if len(env) != 1 || env[0] != "PATH=tools" {
		t.Fatalf("inherited server configuration was not removed")
	}
}
