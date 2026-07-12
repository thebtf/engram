//go:build critical
// +build critical

// Package recovery_test contains @critical recovery tests.
package recovery_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// @critical
func TestOperatorRecoveryMissingDockerPreservesActionableDependencyError(t *testing.T) {
	if _, err := exec.LookPath("pwsh"); err != nil {
		t.Fatalf("recovery requires pwsh: %v", err)
	}

	repoRoot, script := recoveryPaths(t)
	cmd := exec.Command("pwsh", "-NoLogo", "-NoProfile", "-File", script, "-DockerCommand", "engram-deliberately-missing-docker")
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("missing Docker dependency was accepted")
	}
	if !strings.Contains(string(output), "required dependency is unavailable: engram-deliberately-missing-docker") {
		t.Fatalf("missing dependency error was masked: %v\n%s", err, output)
	}
}

// @critical
func TestOperatorCanRestorePostgresBackup_RecoversDurableEngramDataAndRejectsUnsafeRestores(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("recovery requires docker: %v", err)
	}
	if _, err := exec.LookPath("pwsh"); err != nil {
		t.Fatalf("recovery requires pwsh: %v", err)
	}

	repoRoot, script := recoveryPaths(t)
	cmd := exec.Command("pwsh", "-NoLogo", "-NoProfile", "-File", script)
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("operator recovery flow failed: %v\n%s", err, output)
	}
}

func recoveryPaths(t *testing.T) (string, string) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve recovery test path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
	script := filepath.Join(repoRoot, "scripts", "recovery", "verify-postgres-backup-restore.ps1")
	return repoRoot, script
}
