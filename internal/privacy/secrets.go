// Package privacy provides utilities for protecting sensitive data.
package privacy

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// secretPatterns contains compiled regular expressions for detecting secrets.
// These patterns are designed to catch common secret formats with minimal false positives.
var secretPatterns = []*regexp.Regexp{
	// API keys with common prefixes
	regexp.MustCompile(`(?i)(api[_-]?key|apikey)\s*[:=]\s*['"]?[a-zA-Z0-9_-]{20,}['"]?`),

	// Passwords in configuration
	regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[:=]\s*['"][^'"]{8,}['"]`),

	// Secret tokens
	regexp.MustCompile(`(?i)(secret[_-]?key|secret[_-]?token|auth[_-]?token)\s*[:=]\s*['"]?[a-zA-Z0-9_-]{20,}['"]?`),

	// OpenAI API keys
	regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`),

	// Anthropic API keys
	regexp.MustCompile(`sk-ant-[a-zA-Z0-9-]{20,}`),

	// GitHub tokens
	regexp.MustCompile(`gh[pous]_[a-zA-Z0-9]{36,}`),
	regexp.MustCompile(`github_pat_[a-zA-Z0-9_]{22,}`),

	// AWS keys
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`(?i)aws[_-]?secret[_-]?access[_-]?key\s*[:=]\s*['"]?[a-zA-Z0-9/+=]{40}['"]?`),

	// Private keys — header-only pattern for detection (a BEGIN line alone signals a secret).
	regexp.MustCompile(`-----BEGIN (RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----`),
	// Private keys — full PEM block (BEGIN...END) to capture the entire key material when
	// both delimiters are present. (?s) enables DOTALL so '.' matches newlines.
	regexp.MustCompile(`(?s)-----BEGIN (RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----.+?-----END (RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----`),

	// JWT tokens (base64.base64.base64 format)
	regexp.MustCompile(`eyJ[a-zA-Z0-9_-]+\.eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+`),

	// Generic secret assignment patterns
	regexp.MustCompile(`(?i)bearer\s+[a-zA-Z0-9_-]{20,}`),
}

// DetectedSecret represents a secret value found in text, with a deterministic name
// derived from the full SHA-256 hex of the value (64 hex chars) to avoid collisions
// when used as a vault deduplication key.
type DetectedSecret struct {
	Name  string // deterministic: "auto:{sha256hex}" (64 hex chars)
	Value string // the raw secret value
}

// deriveHashPrefix computes the 8-character SHA-256 hex prefix for a secret value.
// Both ExtractSecrets and RedactSecrets use the same derivation so that hash
// prefixes are consistent and allow cross-referencing between the two outputs.
func deriveHashPrefix(match string) string {
	value := extractSecretValue(match)
	if value == "" {
		value = match
	}
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])[:8]
}

// ExtractSecrets scans text for secret patterns and returns all unique matches.
// Each secret gets a deterministic name based on the SHA-256 hash of its value,
// ensuring idempotent vault storage (same secret = same name = one entry).
func ExtractSecrets(text string) []DetectedSecret {
	if text == "" {
		return nil
	}

	seen := make(map[string]struct{})
	var results []DetectedSecret

	for _, pattern := range secretPatterns {
		matches := pattern.FindAllString(text, -1)
		for _, match := range matches {
			hashPrefix := deriveHashPrefix(match)
			name := "auto:" + hashPrefix

			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}

			// Extract just the secret value (strip key name prefix) for vault storage.
			value := extractSecretValue(match)
			if value == "" {
				value = match
			}
			results = append(results, DetectedSecret{Name: name, Value: value})
		}
	}

	return results
}

// extractSecretValue strips key= or key: prefixes from a matched secret string,
// returning just the sensitive value portion.
func extractSecretValue(match string) string {
	for _, sep := range []string{"=", ":"} {
		if idx := strings.Index(match, sep); idx != -1 {
			val := strings.TrimSpace(match[idx+1:])
			val = strings.Trim(val, `'"`)
			if val != "" {
				return val
			}
		}
	}
	return ""
}

// ContainsSecrets checks if the given text contains any patterns that look like secrets.
// Returns true if potential secrets are detected.
func ContainsSecrets(text string) bool {
	if text == "" {
		return false
	}

	for _, pattern := range secretPatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

// RedactSecrets replaces detected secrets with a redaction marker that includes
// a SHA-256 hash prefix for cross-referencing with ExtractSecrets output.
// The hash allows correlating redacted values with their vault entries without
// exposing the secret itself.
func RedactSecrets(text string) string {
	if text == "" {
		return text
	}

	result := text
	for _, pattern := range secretPatterns {
		result = pattern.ReplaceAllStringFunc(result, func(match string) string {
			hashPrefix := deriveHashPrefix(match)
			redacted := "[REDACTED:" + hashPrefix + "]"

			// Preserve the key name by cutting at the earliest key/value separator.
			// Checking "=" before ":" is wrong when the value itself contains "=" (e.g.
			// password: "a=b"), which would expose part of the secret. Instead, pick the
			// separator that appears first in the string.
			idxEq := strings.Index(match, "=")
			idxColon := strings.Index(match, ":")
			var sepIdx int
			switch {
			case idxEq == -1:
				sepIdx = idxColon
			case idxColon == -1:
				sepIdx = idxEq
			case idxEq < idxColon:
				sepIdx = idxEq
			default:
				sepIdx = idxColon
			}
			if sepIdx != -1 {
				return match[:sepIdx+1] + redacted
			}
			// For standalone secrets (like sk-xxx, ghp_xxx), show prefix + hash
			if len(match) > 8 {
				return match[:4] + "..." + redacted
			}
			return redacted
		})
	}
	return result
}

