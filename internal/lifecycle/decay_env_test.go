// Package lifecycle — unit tests for env-configurable decay parameters (TG4 finding 5).
package lifecycle

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDecayBatchSize_Default verifies the default value when env var is absent.
func TestDecayBatchSize_Default(t *testing.T) {
	t.Setenv("ENGRAM_DECAY_BATCH_SIZE", "")
	assert.Equal(t, 100, DecayBatchSize(), "default batch size must be 100")
}

// TestDecayBatchSize_EnvOverride verifies the env var is respected.
func TestDecayBatchSize_EnvOverride(t *testing.T) {
	t.Setenv("ENGRAM_DECAY_BATCH_SIZE", "42")
	assert.Equal(t, 42, DecayBatchSize())
}

// TestDecayBatchSize_InvalidEnv verifies that a non-positive or non-numeric value
// falls back to the default.
func TestDecayBatchSize_InvalidEnv(t *testing.T) {
	for _, bad := range []string{"0", "-5", "notanumber", "1.5"} {
		t.Run(bad, func(t *testing.T) {
			t.Setenv("ENGRAM_DECAY_BATCH_SIZE", bad)
			assert.Equal(t, 100, DecayBatchSize(),
				"invalid value %q must fall back to default", bad)
		})
	}
}

// TestDecayRecurrenceThreshold_Default verifies the default value.
func TestDecayRecurrenceThreshold_Default(t *testing.T) {
	t.Setenv("ENGRAM_DECAY_RECURRENCE_THRESHOLD", "")
	assert.Equal(t, 3, DecayRecurrenceThreshold(), "default recurrence threshold must be 3")
}

// TestDecayRecurrenceThreshold_EnvOverride verifies the env var is respected.
func TestDecayRecurrenceThreshold_EnvOverride(t *testing.T) {
	t.Setenv("ENGRAM_DECAY_RECURRENCE_THRESHOLD", "7")
	assert.Equal(t, 7, DecayRecurrenceThreshold())
}

// TestDecayRecurrenceThreshold_InvalidEnv verifies fallback on bad values.
func TestDecayRecurrenceThreshold_InvalidEnv(t *testing.T) {
	for _, bad := range []string{"0", "-1", "abc"} {
		t.Run(bad, func(t *testing.T) {
			t.Setenv("ENGRAM_DECAY_RECURRENCE_THRESHOLD", bad)
			assert.Equal(t, 3, DecayRecurrenceThreshold(),
				"invalid value %q must fall back to default", bad)
		})
	}
}
