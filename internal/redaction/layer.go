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
// compiled regexes are cached at startup (LoadRules). Hot-reload is not
// supported in this release.
package redaction

import (
	"errors"
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

// compiledRule holds a pre-compiled Rule ready for application.
type compiledRule struct {
	Rule
	re *regexp.Regexp
}

// ErrContentFullyRedacted is returned when all content was stripped by rules.
var ErrContentFullyRedacted = errors.New("content_fully_redacted")

// Scrub applies rules to content in order, replacing all regex matches with
// the corresponding replacement string. It returns the scrubbed content, the
// IDs of rules that matched at least once, and any error.
//
// If content is empty or rules is nil/empty, content is returned unchanged
// with no matched IDs and no error (no-op path).
//
// If the scrubbed content is empty after applying rules and the original
// content was non-empty, ErrContentFullyRedacted is returned.
func Scrub(content string, rules []Rule) (string, []RuleID, error) {
	if content == "" || len(rules) == 0 {
		return content, nil, nil
	}

	compiled := make([]compiledRule, 0, len(rules))
	for _, r := range rules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			// skip invalid patterns rather than aborting; caller should
			// validate rules at load time via CompileRules.
			continue
		}
		compiled = append(compiled, compiledRule{Rule: r, re: re})
	}

	result := content
	var matched []RuleID

	for _, cr := range compiled {
		replaced := cr.re.ReplaceAllLiteralString(result, cr.Replacement)
		if replaced != result {
			matched = append(matched, cr.ID)
		}
		result = replaced
	}

	// §EC-F5: full-content match → error
	if strings.TrimSpace(result) == "" && strings.TrimSpace(content) != "" {
		return "", matched, ErrContentFullyRedacted
	}

	return result, matched, nil
}

// CompileRules validates and pre-compiles a slice of Rules. Returns an error
// if any pattern fails to compile.
// Useful at startup to surface bad configs early (EC-F9 restart-required path).
func CompileRules(rules []Rule) ([]compiledRule, error) {
	compiled := make([]compiledRule, 0, len(rules))
	for _, r := range rules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, compiledRule{Rule: r, re: re})
	}
	return compiled, nil
}
