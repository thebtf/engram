// Package redaction — rejection_test.go: T051 acceptance test for EC-F5 + EC-F9.
//
// EC-F5: Redaction rule matches but produces empty content.
//
//	The write is rejected with error='content_fully_redacted' rather than
//	storing an empty memory; the redaction attempt is still logged.
//
// EC-F9: Redaction rule file modified at runtime → server requires restart.
//
//	Hot-reload without signal NOT supported in Milestone F to avoid mid-write
//	rule mismatch. Admin docs (docs/operating-engram.md) document restart requirement.
//	Server MUST log loaded rule file path + checksum at startup.
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
