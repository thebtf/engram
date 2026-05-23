package core

import (
	"testing"
)

// TestMasterOff_AllOff verifies that when ENGRAM_V7_PLUG_ENABLED is unset (or
// cleared to ""), IsPlugEnabled returns false AND every IsSubsystemEnabled call
// returns false even when the per-subsystem env var is set to "true".
func TestMasterOff_AllOff(t *testing.T) {
	// Ensure master flag is absent.
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "")

	cfg := LoadFlagConfigFromEnv()

	if cfg.IsPlugEnabled() {
		t.Fatal("IsPlugEnabled: expected false when ENGRAM_V7_PLUG_ENABLED is empty")
	}

	// Set all per-sub vars to "true"; master OFF must still suppress them all.
	subsystems := []struct {
		name   string
		envVar string
	}{
		{"s1", "ENGRAM_V7_S1_STATE"},
		{"s2", "ENGRAM_V7_S2_METAMEM"},
		{"s3", "ENGRAM_V7_S3_AMBIENT"},
		{"s4a", "ENGRAM_V7_S4A_DIRECTIVES_CAPTURE"},
		{"s4b", "ENGRAM_V7_S4B_DIRECTIVES_SURFACING"},
		{"s5", "ENGRAM_V7_S5_TELEMETRY"},
		{"s6", "ENGRAM_V7_S6_OUTCOME"},
	}
	for _, sub := range subsystems {
		t.Setenv(sub.envVar, "true")
	}

	// Re-construct after setting per-sub vars; master is still empty.
	cfg2 := LoadFlagConfigFromEnv()

	if cfg2.IsPlugEnabled() {
		t.Fatal("IsPlugEnabled: expected false with per-sub set but master empty")
	}
	for _, sub := range subsystems {
		if cfg2.IsSubsystemEnabled(sub.name) {
			t.Errorf("IsSubsystemEnabled(%q): expected false when master OFF", sub.name)
		}
	}
}

// TestMasterOn_PerSubResolved verifies that when the master flag is enabled,
// IsSubsystemEnabled mirrors each per-subsystem flag independently.
// Table-driven across all 7 canonical subsystem names.
func TestMasterOn_PerSubResolved(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")

	tests := []struct {
		name   string
		envVar string
	}{
		{"s1", "ENGRAM_V7_S1_STATE"},
		{"s2", "ENGRAM_V7_S2_METAMEM"},
		{"s3", "ENGRAM_V7_S3_AMBIENT"},
		{"s4a", "ENGRAM_V7_S4A_DIRECTIVES_CAPTURE"},
		{"s4b", "ENGRAM_V7_S4B_DIRECTIVES_SURFACING"},
		{"s5", "ENGRAM_V7_S5_TELEMETRY"},
		{"s6", "ENGRAM_V7_S6_OUTCOME"},
	}

	boolCases := []struct {
		value    string
		expected bool
	}{
		{"true", true},
		{"1", true},
		{"yes", true},
		{"TRUE", true},
		{"YES", true},
		{"Yes", true},
		{"false", false},
		{"0", false},
		{"no", false},
		{"", false},
	}

	for _, sub := range tests {
		for _, bc := range boolCases {
			sub := sub
			bc := bc
			t.Run(sub.name+"/"+bc.value, func(t *testing.T) {
				t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
				t.Setenv(sub.envVar, bc.value)

				cfg := LoadFlagConfigFromEnv()

				got := cfg.IsSubsystemEnabled(sub.name)
				if got != bc.expected {
					t.Errorf("IsSubsystemEnabled(%q) with env %q=%q: got %v, want %v",
						sub.name, sub.envVar, bc.value, got, bc.expected)
				}
			})
		}
	}
}

// TestUnknownName_ReturnsFalse verifies EC-1: IsSubsystemEnabled returns false
// for any name that is not in the canonical subsystem name list, even when the
// master flag is enabled.
func TestUnknownName_ReturnsFalse(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")

	cfg := LoadFlagConfigFromEnv()

	unknownNames := []string{
		"nonexistent",
		"s99",
		"",
		"S1",    // wrong case
		"state", // wrong form
		"s1_state",
	}
	for _, name := range unknownNames {
		if cfg.IsSubsystemEnabled(name) {
			t.Errorf("IsSubsystemEnabled(%q): expected false for unknown name", name)
		}
	}
}

// TestImmutableAfterLoad verifies that a FlagConfig instance holds the env
// state captured at LoadFlagConfigFromEnv call time. Mutating env vars after
// construction must not affect the already-constructed FlagConfig value.
func TestImmutableAfterLoad(t *testing.T) {
	// Step 1: set up initial env state — master ON, s1 ON, s2 OFF.
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S1_STATE", "true")
	t.Setenv("ENGRAM_V7_S2_METAMEM", "")

	// Step 2: construct the instance under test.
	cfg := LoadFlagConfigFromEnv()

	// Verify initial state.
	if !cfg.IsPlugEnabled() {
		t.Fatal("pre-mutation: IsPlugEnabled expected true")
	}
	if !cfg.IsSubsystemEnabled("s1") {
		t.Fatal("pre-mutation: IsSubsystemEnabled(s1) expected true")
	}
	if cfg.IsSubsystemEnabled("s2") {
		t.Fatal("pre-mutation: IsSubsystemEnabled(s2) expected false")
	}

	// Step 3: mutate env after construction.
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "")   // turn master OFF
	t.Setenv("ENGRAM_V7_S1_STATE", "")       // turn s1 OFF
	t.Setenv("ENGRAM_V7_S2_METAMEM", "true") // turn s2 ON

	// Step 4: original cfg instance must still reflect load-time state.
	if !cfg.IsPlugEnabled() {
		t.Error("post-mutation: IsPlugEnabled should still be true (load-time value)")
	}
	if !cfg.IsSubsystemEnabled("s1") {
		t.Error("post-mutation: IsSubsystemEnabled(s1) should still be true (load-time value)")
	}
	if cfg.IsSubsystemEnabled("s2") {
		t.Error("post-mutation: IsSubsystemEnabled(s2) should still be false (load-time value)")
	}
}
