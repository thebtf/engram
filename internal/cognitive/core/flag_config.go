package core

import (
	"os"
	"strings"
)

// FlagConfig holds the resolved boolean state of all ENGRAM_V7_* feature flags
// at the moment LoadFlagConfigFromEnv was called. It is a plain value struct
// with no package-level cache; constructing a new instance always re-reads
// os.Getenv, which makes t.Setenv-based tests order-independent.
//
// ADR-005: env-var gating is the canonical toggle mechanism for v7.0–v7.4.
// All flags default to false (disabled) when the corresponding env var is absent
// or contains any value other than "true", "1", or "yes" (case-insensitive).
//
// Master guard: IsSubsystemEnabled always returns false when IsPlugEnabled is
// false, regardless of the individual subsystem flag values.
type FlagConfig struct {
	plugEnabled bool
	s1          bool
	s2          bool
	s3          bool
	s4a         bool
	s4b         bool
	s5          bool
	s6          bool
}

// LoadFlagConfigFromEnv constructs a FlagConfig by reading the canonical
// ENGRAM_V7_* env vars at call time. The returned value is immutable — it holds
// the bool snapshot from the moment of construction. Callers that need a fresh
// read must call LoadFlagConfigFromEnv again.
//
// subsystemEnvVarName is used to look up each subsystem's env var by name so
// the mapping stays in one place (REFACTOR: single SSOT per AC).
func LoadFlagConfigFromEnv() FlagConfig {
	sub := func(name string) bool {
		return parseBool(os.Getenv(subsystemEnvVarName(name)))
	}
	return FlagConfig{
		plugEnabled: parseBool(os.Getenv("ENGRAM_V7_PLUG_ENABLED")),
		s1:          sub("s1"),
		s2:          sub("s2"),
		s3:          sub("s3"),
		s4a:         sub("s4a"),
		s4b:         sub("s4b"),
		s5:          sub("s5"),
		s6:          sub("s6"),
	}
}

// IsPlugEnabled reports whether the master v7 plug flag is enabled.
// Corresponds to env var ENGRAM_V7_PLUG_ENABLED.
func (c FlagConfig) IsPlugEnabled() bool {
	return c.plugEnabled
}

// IsSubsystemEnabled reports whether the named subsystem is enabled.
// Returns false when:
//   - the master plug flag is disabled (regardless of the per-subsystem flag);
//   - the name is not a registered subsystem name (EC-1);
//   - the per-subsystem env var is absent or not a truthy value.
//
// Canonical subsystem names: "s1", "s2", "s3", "s4a", "s4b", "s5", "s6".
func (c FlagConfig) IsSubsystemEnabled(name string) bool {
	if !c.plugEnabled {
		return false
	}
	switch name {
	case "s1":
		return c.s1
	case "s2":
		return c.s2
	case "s3":
		return c.s3
	case "s4a":
		return c.s4a
	case "s4b":
		return c.s4b
	case "s5":
		return c.s5
	case "s6":
		return c.s6
	default:
		return false
	}
}

// subsystemEnvVarName returns the canonical env var name for the given
// subsystem name. Returns an empty string for unknown names.
// This is the single SSOT for the subsystem-name → env-var mapping.
func subsystemEnvVarName(name string) string {
	switch name {
	case "s1":
		return "ENGRAM_V7_S1_STATE"
	case "s2":
		return "ENGRAM_V7_S2_METAMEM"
	case "s3":
		return "ENGRAM_V7_S3_AMBIENT"
	case "s4a":
		return "ENGRAM_V7_S4A_DIRECTIVES_CAPTURE"
	case "s4b":
		return "ENGRAM_V7_S4B_DIRECTIVES_SURFACING"
	case "s5":
		return "ENGRAM_V7_S5_TELEMETRY"
	case "s6":
		return "ENGRAM_V7_S6_OUTCOME"
	default:
		return ""
	}
}

// parseBool returns true if s (after trimming whitespace and lowercasing)
// equals "true", "1", or "yes". All other values — including the empty string
// produced by os.Getenv for an unset var — return false.
//
// strconv.ParseBool is intentionally NOT used: it does not accept "yes" and it
// returns an error for unknown strings, neither of which matches the task spec.
func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes":
		return true
	}
	return false
}
