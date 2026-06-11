package worker

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsCrystallizationEnabled verifies the flag helper reads the env var correctly.
// These tests use t.Setenv so they are safe to run in parallel and do not pollute
// the process environment after the test returns.
func TestIsCrystallizationEnabled_Unset(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "")
	assert.False(t, isCrystallizationEnabled(), "unset flag must return false")
}

func TestIsCrystallizationEnabled_False(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "false")
	assert.False(t, isCrystallizationEnabled(), "'false' must return false")
}

func TestIsCrystallizationEnabled_True(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")
	assert.True(t, isCrystallizationEnabled(), "'true' must return true")
}

func TestIsCrystallizationEnabled_CaseSensitive(t *testing.T) {
	// The flag is intentionally case-sensitive (matches ENGRAM_LIFECYCLE_ENABLED pattern).
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "TRUE")
	assert.False(t, isCrystallizationEnabled(), "value 'TRUE' (uppercase) must return false")
}
