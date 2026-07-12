//go:build critical

package recovery_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// @critical
// @category: data-consistency
// @features: [pre-v5-upgrade]
// @dev_stand: required
func TestPreV5UpgradeFixtureProvenance(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
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
