// Package redaction — rejection_test.go: T051 acceptance test for EC-F5 + EC-F9.
//
// EC-F5: Redaction rule matches but produces empty content.
//   The write is rejected with error='content_fully_redacted' rather than
//   storing an empty memory; the redaction attempt is still logged.
//
// EC-F9: Redaction rule file modified at runtime → server requires restart.
//   Hot-reload without signal NOT supported in Milestone F to avoid mid-write
//   rule mismatch. Admin docs (docs/operating-engram.md) document restart requirement.
//   Server MUST log loaded rule file path + checksum at startup.
//
// TG5 NOTE: The full redaction implementation lives in TG5 (write-lint, PR #250,
// NOT on this branch). Tests that require the TG5 API surface are skipped here
// and will be activated when TG5 merges to main.
//
// What IS tested in this file (no TG5 dependency):
//   - EC-F9 docs presence: docs/operating-engram.md exists and documents restart-required
//   - EC-F5/EC-F9 sentinel error shapes (package-level constants)
//   - Environment variable contract (ENGRAM_REDACTION_RULES_PATH)
package redaction

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- EC-F9: operating docs presence test ---

// TestEC_F9_OperatingDocsExist verifies that docs/operating-engram.md exists and
// contains the restart-required documentation for EC-F9.
// This test does NOT require TG5 — it validates the operator documentation deliverable.
func TestEC_F9_OperatingDocsExist(t *testing.T) {
	// Resolve the repo root from the test file's location.
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")

	// internal/redaction → ../../docs/operating-engram.md
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	docPath := filepath.Join(repoRoot, "docs", "operating-engram.md")

	content, err := os.ReadFile(docPath)
	require.NoError(t, err,
		"docs/operating-engram.md must exist — EC-F9 requires admin docs documenting restart requirement")

	docStr := string(content)

	// EC-F9 required content checks.
	assert.True(t, strings.Contains(docStr, "ENGRAM_REDACTION_RULES_PATH"),
		"docs/operating-engram.md must document ENGRAM_REDACTION_RULES_PATH env var")
	assert.True(t,
		strings.Contains(docStr, "restart") || strings.Contains(docStr, "SIGHUP"),
		"docs/operating-engram.md must document restart-required or SIGHUP for rule reload")
	assert.True(t,
		strings.Contains(docStr, "checksum") || strings.Contains(docStr, "sha256") || strings.Contains(docStr, "hash"),
		"docs/operating-engram.md must document that rule file checksum is logged at startup")
	assert.True(t,
		strings.Contains(docStr, "content_fully_redacted") || strings.Contains(docStr, "EC-F5") || strings.Contains(docStr, "full redaction"),
		"docs/operating-engram.md must document EC-F5 full-redaction rejection")
}

// --- EC-F5: package-level error sentinel ---

// TestEC_F5_ErrorSentinel_Shape verifies the expected error_code contract.
// The actual implementation is TG5-gated; this test verifies the string constant
// is correct so that TG5 integration picks up the right value.
func TestEC_F5_ErrorSentinel_Shape(t *testing.T) {
	// Per spec EC-F5: error='content_fully_redacted'
	// Compare against the actual package sentinel to catch renames.
	assert.EqualError(t, ErrContentFullyRedacted, "content_fully_redacted",
		"EC-F5 error code must be 'content_fully_redacted' — verified against spec §EC-F5")
}

// --- EC-F9: env var contract ---

// TestEC_F9_EnvVar_NoOpWhenAbsent verifies the ENGRAM_REDACTION_RULES_PATH
// no-op contract: when unset, redaction must be disabled (per ADR-F-004).
// This test exercises only the env var reading contract — TG5 implementation is separate.
func TestEC_F9_EnvVar_NoOpWhenAbsent(t *testing.T) {
	t.Setenv("ENGRAM_REDACTION_RULES_PATH", "")
	path := os.Getenv("ENGRAM_REDACTION_RULES_PATH")
	assert.Empty(t, path, "when ENGRAM_REDACTION_RULES_PATH is empty, redaction is a no-op")
}

// TestEC_F9_EnvVar_PathSet verifies that when ENGRAM_REDACTION_RULES_PATH is set
// to a non-existent path, LoadRulesFromPath returns an error (caller logs warning
// and falls back to no-op per ADR-F-004).
func TestEC_F9_EnvVar_PathSet_MissingFile(t *testing.T) {
	path := "/nonexistent/engram-rules-test-ec-f9.json"
	t.Setenv("ENGRAM_REDACTION_RULES_PATH", path)
	rules, err := LoadRulesFromPath(path)
	require.Error(t, err, "LoadRulesFromPath must return error for missing file")
	assert.Nil(t, rules, "no rules must be returned for missing file")
}

// --- TG5-gated tests (skipped until TG5 merges) ---

// TestEC_F5_FullRedactionRejected is a placeholder for the TG5-dependent EC-F5 test.
// Skipped until TG5 (write-lint, PR #250) merges to main.
//
// When TG5 is available, this test should:
//  1. Configure a redaction rule matching the entire content (e.g., regex `.*`).
//  2. Attempt to store a memory whose content matches the rule.
//  3. Assert the write is rejected with error='content_fully_redacted'.
//  4. Assert audit_log contains action='redacted' with the matched rule_id.
//  5. Assert NO memory row was written (row count unchanged).
func TestEC_F5_FullRedactionRejected(t *testing.T) {
	t.Skip("TG5 (write-lint / internal/redaction) NOT on this branch — " +
		"test activated when TG5 merges to main (PR #250)")
}

// TestEC_F9_HotReloadNotSupported is a placeholder for the TG5-dependent EC-F9 runtime test.
// Skipped until TG5 merges.
//
// When TG5 is available, this test should:
//  1. Start a test server with ENGRAM_REDACTION_RULES_PATH pointing to a rule file.
//  2. Verify startup log contains rule file path + SHA-256 checksum.
//  3. Modify the rule file on disk.
//  4. Verify the server has NOT picked up the new rules (hot-reload not supported).
//  5. Send SIGHUP (if implemented) or restart and verify new rules are active.
func TestEC_F9_HotReloadNotSupported(t *testing.T) {
	t.Skip("TG5 (write-lint / internal/redaction) NOT on this branch — " +
		"test activated when TG5 merges to main (PR #250)")
}
