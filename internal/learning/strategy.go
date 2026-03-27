// Package learning provides strategy selection for injection A/B testing.
package learning

import (
	"sync/atomic"
)

// AvailableStrategies are the built-in injection strategies.
var AvailableStrategies = []string{
	"baseline",
	"effectiveness-weighted",
	"recency-boosted",
	"diverse",
}

// StrategySelector assigns injection strategies to sessions.
type StrategySelector struct {
	strategies []string
	mode       string // "round-robin" or "fixed"
	defaultStr string
	counter    atomic.Uint64
}

// NewStrategySelector creates a new selector.
func NewStrategySelector(strategies []string, mode, defaultStrategy string) *StrategySelector {
	if len(strategies) == 0 {
		strategies = AvailableStrategies
	}
	if mode == "" {
		mode = "round-robin"
	}
	if defaultStrategy == "" {
		defaultStrategy = "baseline"
	}
	return &StrategySelector{
		strategies: strategies,
		mode:       mode,
		defaultStr: defaultStrategy,
	}
}

// SelectStrategy returns the strategy for a session.
// In round-robin mode, cycles through available strategies using an atomic counter.
// In fixed mode, always returns the default strategy.
func (s *StrategySelector) SelectStrategy(sessionID string) string {
	if s.mode == "fixed" {
		return s.defaultStr
	}
	// Round-robin: use atomic counter for thread safety
	idx := s.counter.Add(1) - 1
	return s.strategies[idx%uint64(len(s.strategies))]
}
