package worker

// wiring_crystallization_test.go — wiring smoke tests for the crystallization pipeline.
//
// The per-session regex extraction path (runCrystallization) has been removed in T014.
// Crystallization now runs exclusively via the dream-cycle (runDreamCrystallization)
// on the sleep tick. These wiring tests verify the flag and transcript-store wiring
// that remains in handleSessionEnd.

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWiring_CrystallizationFlagRegistered verifies that when
// ENGRAM_CRYSTALLIZATION_ENABLED=true the helper reports enabled.
// Mirrors the "feature registered when flag on" pattern from wiring_vnext_test.go.
func TestWiring_CrystallizationFlagRegistered(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")
	assert.True(t, isCrystallizationEnabled(),
		"ENGRAM_CRYSTALLIZATION_ENABLED=true must make isCrystallizationEnabled() return true")
}

// TestWiring_CrystallizationFlagOff verifies the inverse: flag unset → disabled.
func TestWiring_CrystallizationFlagOff(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "false")
	assert.False(t, isCrystallizationEnabled(),
		"ENGRAM_CRYSTALLIZATION_ENABLED=false must make isCrystallizationEnabled() return false")
}
