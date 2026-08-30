package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/hap01cfixture"
)

type fakeFixtureController struct {
	seedCalls     int
	rotationCalls int
	snapshotCalls int
	seedReceipt   hap01cfixture.SeedReceipt
	rotation      hap01cfixture.RotationReceipt
	snapshot      hap01cfixture.SnapshotReceipt
}

func (f *fakeFixtureController) Close() error { return nil }

func (f *fakeFixtureController) Seed(_ context.Context, _ string, _ string) (hap01cfixture.SeedReceipt, error) {
	f.seedCalls++
	return f.seedReceipt, nil
}

func (f *fakeFixtureController) RotateProjectKeycard(_ context.Context, _ string) (hap01cfixture.RotationReceipt, error) {
	f.rotationCalls++
	return f.rotation, nil
}

func (f *fakeFixtureController) Snapshot(_ context.Context, _ string) (hap01cfixture.SnapshotReceipt, error) {
	f.snapshotCalls++
	return f.snapshot, nil
}

func TestRunRejectsUnknownMissingAndDuplicateFlags(t *testing.T) {
	workingDirectory, root := fixtureTestRoot(t, "fixture-run")
	inside := filepath.Join(root, "dsn.txt")
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "unknown flag",
			args: []string{"seed", "--run-id", "fixture-run", "--dsn-file", inside, "--request-file", filepath.Join(root, "seed-request.json"), "--secrets-out", filepath.Join(root, "keycards.json"), "--unexpected", "value"},
		},
		{
			name: "equals syntax is rejected",
			args: []string{"seed", "--run-id=fixture-run", "--dsn-file", inside, "--request-file", filepath.Join(root, "seed-request.json"), "--secrets-out", filepath.Join(root, "keycards.json")},
		},
		{
			name: "missing value",
			args: []string{"rotate-project-keycard", "--run-id", "fixture-run", "--dsn-file", inside, "--secrets-file"},
		},
		{
			name: "duplicate flag",
			args: []string{"snapshot", "--run-id", "fixture-run", "--run-id", "fixture-run", "--dsn-file", inside, "--request-file", filepath.Join(root, "seed-request.json"), "--out", filepath.Join(root, "snapshots", "one.json")},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opened := 0
			dependencies := commandDependencies{
				open: func(context.Context, string, string) (fixtureController, error) {
					opened++
					return &fakeFixtureController{}, nil
				},
				workingDirectory: func() (string, error) { return workingDirectory, nil },
			}
			var stdout, stderr bytes.Buffer
			status := runWithDependencies(context.Background(), test.args, &stdout, &stderr, dependencies)
			if status != usageExitCode {
				t.Fatalf("status = %d, want usage exit %d", status, usageExitCode)
			}
			if opened != 0 {
				t.Fatalf("invalid arguments opened fixture %d times", opened)
			}
			if stdout.Len() != 0 {
				t.Fatalf("invalid arguments wrote stdout: %q", stdout.String())
			}
		})
	}
}

func TestRunRefusesPathEscapeWithoutOpeningFixture(t *testing.T) {
	workingDirectory, root := fixtureTestRoot(t, "fixture-run")
	escape := filepath.Join(workingDirectory, "outside-keycards.json")
	args := []string{
		"seed",
		"--run-id", "fixture-run",
		"--dsn-file", filepath.Join(root, "dsn.txt"),
		"--request-file", filepath.Join(root, "seed-request.json"),
		"--secrets-out", escape,
	}
	opened := 0
	dependencies := commandDependencies{
		open: func(context.Context, string, string) (fixtureController, error) {
			opened++
			return &fakeFixtureController{}, nil
		},
		workingDirectory: func() (string, error) { return workingDirectory, nil },
	}
	var stdout, stderr bytes.Buffer
	status := runWithDependencies(context.Background(), args, &stdout, &stderr, dependencies)
	if status != boundaryExitCode {
		t.Fatalf("status = %d, want boundary exit %d", status, boundaryExitCode)
	}
	if opened != 0 {
		t.Fatalf("escaped path opened fixture %d times", opened)
	}
	if strings.Contains(stderr.String(), escape) || strings.Contains(stderr.String(), workingDirectory) {
		t.Fatalf("boundary error leaked an absolute path: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("boundary refusal wrote stdout: %q", stdout.String())
	}
}

func TestRunRefusesLinkedFixtureRootWithoutOpeningFixture(t *testing.T) {
	workingDirectory := t.TempDir()
	foreign := t.TempDir()
	linkedParent := filepath.Join(workingDirectory, ".agent", "tmp")
	if err := os.MkdirAll(linkedParent, 0o700); err != nil {
		t.Fatalf("create linked parent: %v", err)
	}
	link := filepath.Join(linkedParent, "hap-01c")
	linkType := "dir"
	if runtime.GOOS == "windows" {
		linkType = "junction"
	}
	if err := os.Symlink(foreign, link); err != nil {
		if os.IsPermission(err) {
			t.Skip("host does not permit symlink or junction creation")
		}
		t.Fatalf("create fixture root link (%s): %v", linkType, err)
	}
	root := filepath.Join(link, "fixture-run", "fixture", "hap01c-fixture-run")
	args := []string{
		"seed",
		"--run-id", "fixture-run",
		"--dsn-file", filepath.Join(root, "dsn.txt"),
		"--request-file", filepath.Join(root, "seed-request.json"),
		"--secrets-out", filepath.Join(root, "keycards.json"),
	}
	opened := 0
	dependencies := commandDependencies{
		open: func(context.Context, string, string) (fixtureController, error) {
			opened++
			return &fakeFixtureController{}, nil
		},
		workingDirectory: func() (string, error) { return workingDirectory, nil },
	}
	var stdout, stderr bytes.Buffer
	if status := runWithDependencies(context.Background(), args, &stdout, &stderr, dependencies); status != boundaryExitCode {
		t.Fatalf("status = %d, want boundary exit %d", status, boundaryExitCode)
	}
	if opened != 0 {
		t.Fatalf("linked fixture root opened fixture %d times", opened)
	}
}

func TestRunUsesFixedCommandsAndRedactedOutputs(t *testing.T) {
	workingDirectory, root := fixtureTestRoot(t, "fixture-run")
	controller := &fakeFixtureController{
		seedReceipt: hap01cfixture.SeedReceipt{
			Schema:      "hap-01c-fixture-seed-receipt/1",
			RunIDSHA256: strings.Repeat("a", 64),
			Keycards: []hap01cfixture.KeycardReceipt{{
				Class:       "canonical_project_service",
				IDSHA256:    strings.Repeat("b", 64),
				TokenLength: 39,
			}},
		},
		rotation: hap01cfixture.RotationReceipt{
			Schema:      "hap-01c-fixture-rotation-receipt/1",
			RunIDSHA256: strings.Repeat("c", 64),
		},
		snapshot: hap01cfixture.SnapshotReceipt{
			Schema:                           "hap-01c-fixture-snapshot/1",
			RunIDSHA256:                      strings.Repeat("d", 64),
			AmbientDeliveryUnavailableReason: "NO_DURABLE_AMBIENT_ATTEMPT_COUNTER",
		},
	}
	dependencies := commandDependencies{
		open: func(context.Context, string, string) (fixtureController, error) {
			return controller, nil
		},
		workingDirectory: func() (string, error) { return workingDirectory, nil },
	}

	seedArgs := []string{
		"seed",
		"--run-id", "fixture-run",
		"--dsn-file", filepath.Join(root, "dsn.txt"),
		"--request-file", filepath.Join(root, "seed-request.json"),
		"--secrets-out", filepath.Join(root, "keycards.json"),
	}
	var seedOutput, seedError bytes.Buffer
	if status := runWithDependencies(context.Background(), seedArgs, &seedOutput, &seedError, dependencies); status != 0 {
		t.Fatalf("seed status = %d, stderr = %q", status, seedError.String())
	}
	if controller.seedCalls != 1 {
		t.Fatalf("seed calls = %d, want 1", controller.seedCalls)
	}
	assertRedactedJSON(t, seedOutput.Bytes())

	rotateArgs := []string{
		"rotate-project-keycard",
		"--run-id", "fixture-run",
		"--dsn-file", filepath.Join(root, "dsn.txt"),
		"--secrets-file", filepath.Join(root, "keycards.json"),
	}
	var rotateOutput, rotateError bytes.Buffer
	if status := runWithDependencies(context.Background(), rotateArgs, &rotateOutput, &rotateError, dependencies); status != 0 {
		t.Fatalf("rotation status = %d, stderr = %q", status, rotateError.String())
	}
	if controller.rotationCalls != 1 {
		t.Fatalf("rotation calls = %d, want 1", controller.rotationCalls)
	}
	assertRedactedJSON(t, rotateOutput.Bytes())

	snapshotPath := filepath.Join(root, "snapshots", "before.json")
	snapshotArgs := []string{
		"snapshot",
		"--run-id", "fixture-run",
		"--dsn-file", filepath.Join(root, "dsn.txt"),
		"--request-file", filepath.Join(root, "seed-request.json"),
		"--out", snapshotPath,
	}
	var snapshotOutput, snapshotError bytes.Buffer
	if status := runWithDependencies(context.Background(), snapshotArgs, &snapshotOutput, &snapshotError, dependencies); status != 0 {
		t.Fatalf("snapshot status = %d, stderr = %q", status, snapshotError.String())
	}
	if controller.snapshotCalls != 1 {
		t.Fatalf("snapshot calls = %d, want 1", controller.snapshotCalls)
	}
	if snapshotOutput.Len() != 0 {
		t.Fatalf("snapshot wrote stdout instead of --out: %q", snapshotOutput.String())
	}
	content, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read snapshot receipt: %v", err)
	}
	assertRedactedJSON(t, content)
	info, err := os.Stat(snapshotPath)
	if err != nil {
		t.Fatalf("stat snapshot receipt: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot receipt mode = %#o, want 0600", info.Mode().Perm())
	}
}

func fixtureTestRoot(t *testing.T, runID string) (string, string) {
	t.Helper()
	workingDirectory := t.TempDir()
	root := filepath.Join(workingDirectory, ".agent", "tmp", "hap-01c", runID, "fixture", "hap01c-"+runID)
	if err := os.MkdirAll(filepath.Join(root, "snapshots"), 0o700); err != nil {
		t.Fatalf("create fixture root: %v", err)
	}
	return workingDirectory, root
}

func assertRedactedJSON(t *testing.T, content []byte) {
	t.Helper()
	var decoded map[string]interface{}
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatalf("receipt is not JSON: %v", err)
	}
	for _, forbidden := range []string{"engram_", "postgres://", "password", "C:\\", "D:\\"} {
		if bytes.Contains(content, []byte(forbidden)) {
			t.Fatalf("receipt leaked %q: %s", forbidden, content)
		}
	}
}
