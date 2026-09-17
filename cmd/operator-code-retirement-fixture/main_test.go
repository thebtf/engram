package main

import (
	"context"
	"encoding/json"
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
		"postgres://fixture:fixture@localhost:5432/engram_test?sslmode=disable",
		"postgresql://fixture:fixture@127.0.0.1/retirement_test?sslmode=disable",
	} {
		if !validFixtureDSN(raw) {
			t.Fatalf("safe DSN rejected: %q", raw)
		}
	}
	for _, raw := range []string{
		"postgres://fixture:fixture@db.example/engram_test?sslmode=disable",
		"postgres://fixture:fixture@localhost/engram_production?sslmode=disable",
		"postgres://fixture:fixture@localhost/engram_staging?sslmode=disable",
		"not-a-dsn",
	} {
		if validFixtureDSN(raw) {
			t.Fatalf("unsafe DSN accepted: %q", raw)
		}
	}
}

func TestRetirementFixtureAgainstDisposablePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_DSN"))
	if dsn == "" {
		t.Skip("DATABASE_DSN is required for disposable PostgreSQL fixture proof")
	}
	if !validFixtureDSN(dsn) {
		t.Skip("DATABASE_DSN is not an explicit loopback test database")
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
