package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/uci"
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

func CodeExplorerFixtureTarget() string { return CodeExplorerFixtureMessage }
func CodeExplorerFixtureEntry() string { return CodeExplorerFixtureTarget() }
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

func TestFixtureFrameCarriesSearchableSourceAndResolvedGraphFact(t *testing.T) {
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
}
