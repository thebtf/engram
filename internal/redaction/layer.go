// Package redaction — T036 (engram vNext Milestone F TG5).
// layer.go implements the operator-configured regex scrub layer that runs
// BEFORE write-lint per ADR-F-004.
//
// Design:
//   - Rules are loaded from ENGRAM_REDACTION_RULES_PATH (JSON file).
//     If the env var is unset the layer is a no-op; content passes unchanged.
//   - Each Rule has an ID (string), a regex Pattern, and a Replacement string.
//   - Rules are applied in order; all matches in the content are replaced.
//   - Matched rule IDs are accumulated in the returned []RuleID slice.
//   - If the scrubbed result is empty (full-content match), Scrub returns
//     the sentinel error "content_fully_redacted" per spec §EC-F5.
//
// EC-F9 restart-required: Rule changes require a process restart because
// compiled regexes are cached at startup (LoadRulesFromPath). Hot-reload is not
// supported in this release.
//
// Finding 7 fix (TG5 review): Scrub now accepts pre-compiled []CompiledRule to
// avoid per-call regexp.Compile. Callers compile rules once at startup via
// CompileRules (or load via LoadRulesFromPath) and pass the result to ScrubCompiled.
// The original Scrub(content, []Rule) signature is preserved for tests and
// one-off callers; it compiles internally but is NOT on the hot path.
package redaction

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// RuleID is the string identifier of a redaction rule.
type RuleID = string

// Rule is a single regex-based redaction rule.
type Rule struct {
	ID          RuleID // unique identifier, used in matched[] output
	Pattern     string // Go regex pattern applied to content
	Replacement string // literal replacement string (not a template)
}

// CompiledRule holds a pre-compiled Rule ready for application.
// Export to allow callers (service.go wiring) to hold the compiled slice.
type CompiledRule struct {
	rule Rule
	re   *regexp.Regexp
}

// ErrContentFullyRedacted is returned when all content was stripped by rules.
var ErrContentFullyRedacted = errors.New("content_fully_redacted")

// CompileRules validates and pre-compiles a slice of Rules. Returns an error
// if any pattern fails to compile.
// Useful at startup to surface bad configs early (EC-F9 restart-required path).
func CompileRules(rules []Rule) ([]CompiledRule, error) {
	compiled := make([]CompiledRule, 0, len(rules))
	for _, r := range rules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("redaction: rule %q: invalid pattern %q: %w", r.ID, r.Pattern, err)
		}
		compiled = append(compiled, CompiledRule{rule: r, re: re})
	}
	return compiled, nil
}

// LoadRulesFromPath reads a JSON file at path and returns compiled rules.
// The JSON file must be an array of Rule objects:
//
//	[{"id":"rule1","pattern":"AKIA[0-9A-Z]{16}","replacement":"[REDACTED]"}]
//
// Returns an empty slice (no-op) when path is empty.
// Returns an error when the file cannot be read or contains invalid patterns.
func LoadRulesFromPath(path string) ([]CompiledRule, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("redaction: read rules file %q: %w", path, err)
	}
	var rules []Rule
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("redaction: parse rules file %q: %w", path, err)
	}
	return CompileRules(rules)
}

// ScrubCompiled applies pre-compiled rules to content in order, replacing all
// regex matches with the corresponding replacement string. It returns the
// scrubbed content, the IDs of rules that matched at least once, and any error.
//
// If content is empty or rules is nil/empty, content is returned unchanged
// with no matched IDs and no error (no-op path).
//
// If the scrubbed content is empty after applying rules and the original
// content was non-empty, ErrContentFullyRedacted is returned.
//
// This is the hot-path function: callers compile rules once at startup and
// pass the []CompiledRule on every call — no per-call regexp.Compile.
func ScrubCompiled(content string, rules []CompiledRule) (string, []RuleID, error) {
	if content == "" || len(rules) == 0 {
		return content, nil, nil
	}

	result := content
	var matched []RuleID

	for _, cr := range rules {
		replaced := cr.re.ReplaceAllLiteralString(result, cr.rule.Replacement)
		if replaced != result {
			matched = append(matched, cr.rule.ID)
		}
		result = replaced
	}

	// §EC-F5: full-content match → error
	if strings.TrimSpace(result) == "" && strings.TrimSpace(content) != "" {
		return "", matched, ErrContentFullyRedacted
	}

	return result, matched, nil
}

// Scrub applies rules to content in order, replacing all regex matches with
// the corresponding replacement string. It returns the scrubbed content, the
// IDs of rules that matched at least once, and any error.
//
// If content is empty or rules is nil/empty, content is returned unchanged
// with no matched IDs and no error (no-op path).
//
// If the scrubbed content is empty after applying rules and the original
// content was non-empty, ErrContentFullyRedacted is returned.
//
// NOTE: This function compiles rules internally on each call. Prefer
// CompileRules + ScrubCompiled for the production hot path to avoid
// per-call regexp.Compile overhead (finding 7 fix, TG5 review).
func Scrub(content string, rules []Rule) (string, []RuleID, error) {
	if content == "" || len(rules) == 0 {
		return content, nil, nil
	}

	compiled := make([]CompiledRule, 0, len(rules))
	for _, r := range rules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			// skip invalid patterns rather than aborting; caller should
			// validate rules at load time via CompileRules.
			continue
		}
		compiled = append(compiled, CompiledRule{rule: r, re: re})
	}

	return ScrubCompiled(content, compiled)
}
