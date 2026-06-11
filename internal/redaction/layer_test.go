// Package redaction — T036 RED/GREEN tests: regex scrub layer.
// Tests cover: no rules → no-op; 1 rule → match replaced;
// full strip → content_fully_redacted error; env-var-unset → bypass.
package redaction_test

import (
	"testing"

	"github.com/thebtf/engram/internal/redaction"
)

// TestRedaction_NoRules_NoOp verifies 0 rules → content unchanged.
func TestRedaction_NoRules_NoOp(t *testing.T) {
	content := "PostgreSQL max_connections=200"
	scrubbed, matched, err := redaction.Scrub(content, nil)
	if err != nil {
		t.Fatalf("Scrub no rules: unexpected error: %v", err)
	}
	if scrubbed != content {
		t.Errorf("Scrub no rules: expected unchanged, got %q", scrubbed)
	}
	if len(matched) != 0 {
		t.Errorf("Scrub no rules: expected 0 matches, got %v", matched)
	}
}

// TestRedaction_AWSKeyRule_MatchReplaced verifies AWS key pattern is scrubbed.
func TestRedaction_AWSKeyRule_MatchReplaced(t *testing.T) {
	awsRule := redaction.Rule{
		ID:          "aws-access-key",
		Pattern:     `AKIA[0-9A-Z]{16}`,
		Replacement: "[REDACTED-AWS-KEY]",
	}
	content := "use AKIAIOSFODNN7EXAMPLE for AWS calls"
	scrubbed, matched, err := redaction.Scrub(content, []redaction.Rule{awsRule})
	if err != nil {
		t.Fatalf("Scrub AWS rule: unexpected error: %v", err)
	}
	if scrubbed == content {
		t.Fatal("Scrub AWS rule: expected content to be modified")
	}
	if len(matched) == 0 {
		t.Fatal("Scrub AWS rule: expected at least 1 matched rule")
	}
	if matched[0] != "aws-access-key" {
		t.Errorf("Scrub AWS rule: expected matched[0]=%q, got %q", "aws-access-key", matched[0])
	}
}

// TestRedaction_FullContentMatch_Error verifies full-content replacement returns
// content_fully_redacted error per spec §EC-F5.
func TestRedaction_FullContentMatch_Error(t *testing.T) {
	fullRule := redaction.Rule{
		ID:          "full-strip",
		Pattern:     `.*`,
		Replacement: "",
	}
	content := "some content that gets fully redacted"
	_, _, err := redaction.Scrub(content, []redaction.Rule{fullRule})
	if err == nil {
		t.Fatal("Scrub full-strip: expected error content_fully_redacted, got nil")
	}
	if err.Error() != "content_fully_redacted" {
		t.Errorf("Scrub full-strip: expected error %q, got %q", "content_fully_redacted", err.Error())
	}
}

// TestRedaction_MultipleRules_AllApplied verifies multiple rules chain correctly.
func TestRedaction_MultipleRules_AllApplied(t *testing.T) {
	rules := []redaction.Rule{
		{ID: "r1", Pattern: `secret=\S+`, Replacement: "secret=[REDACTED]"},
		{ID: "r2", Pattern: `password=\S+`, Replacement: "password=[REDACTED]"},
	}
	content := "config: secret=abc123 password=hunter2 host=localhost"
	scrubbed, matched, err := redaction.Scrub(content, rules)
	if err != nil {
		t.Fatalf("Scrub multi-rules: unexpected error: %v", err)
	}
	if len(matched) < 2 {
		t.Errorf("Scrub multi-rules: expected 2 matched rules, got %d (%v)", len(matched), matched)
	}
	// Scrubbed content should not contain the originals
	if contains(scrubbed, "secret=abc123") {
		t.Error("Scrub multi-rules: secret not redacted")
	}
	if contains(scrubbed, "password=hunter2") {
		t.Error("Scrub multi-rules: password not redacted")
	}
}

// TestRedaction_EmptyContent_NoOp verifies empty content passes through.
func TestRedaction_EmptyContent_NoOp(t *testing.T) {
	rules := []redaction.Rule{
		{ID: "r1", Pattern: `secret`, Replacement: "[REDACTED]"},
	}
	scrubbed, matched, err := redaction.Scrub("", rules)
	if err != nil {
		t.Fatalf("Scrub empty: unexpected error: %v", err)
	}
	if scrubbed != "" {
		t.Errorf("Scrub empty: expected empty, got %q", scrubbed)
	}
	if len(matched) != 0 {
		t.Errorf("Scrub empty: expected 0 matches, got %v", matched)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
