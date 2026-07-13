//go:build critical

package recovery_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// @critical
// @category: data-consistency
// @features: [pre-v5-upgrade]
// @dev_stand: required
func TestPreV5UpgradeFixtureProvenance(t *testing.T) {
	repo := criticalRepoRoot(t)
	type fixtureManifest struct {
		SourceTag      string `json:"source_tag"`
		SourceCommit   string `json:"source_commit"`
		LastMigration  string `json:"last_migration"`
		MigrationCount int    `json:"migration_count"`
		Fixture        string `json:"fixture"`
		FixtureSHA256  string `json:"fixture_sha256"`
	}
	manifestBytes, err := os.ReadFile(filepath.Join(repo, "tests", "fixtures", "pre-v5", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest fixtureManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SourceTag != "v4.5.0" || manifest.SourceCommit == "" || manifest.LastMigration != "082_projects_lifecycle" || manifest.MigrationCount != 82 {
		t.Fatalf("invalid pre-v5 provenance: %#v", manifest)
	}
	fixture, err := os.ReadFile(filepath.Join(repo, "tests", "fixtures", "pre-v5", manifest.Fixture))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(fixture)
	if got := hex.EncodeToString(digest[:]); got != manifest.FixtureSHA256 {
		t.Fatalf("pre-v5 fixture checksum = %s, want %s", got, manifest.FixtureSHA256)
	}
}

// @critical
// @category: data-consistency
// @features: [pre-v5-upgrade]
// @dev_stand: required
func TestPreV5UpgradeBehavior(t *testing.T) {
	repo := criticalRepoRoot(t)
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Fatalf("pwsh is required for the pre-v5 behavior gate: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, pwsh, "-NoProfile", "-File", filepath.Join(repo, "scripts", "production-smoke", "customer", "run-pre-v5-upgrade.ps1"))
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pre-v5 behavior gate failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "PRE_V5_UPGRADE_PASS") {
		t.Fatalf("pre-v5 behavior gate did not emit its success marker:\n%s", output)
	}
}

func criticalRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
