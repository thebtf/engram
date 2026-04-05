// Package scoring provides importance score calculation for observations.
package scoring

// EffectivenessResult contains effectiveness data for an observation.
type EffectivenessResult struct {
	ObservationID int64   `json:"observation_id"`
	Injections    int     `json:"injections"`
	Successes     int     `json:"successes"`
	Effectiveness float64 `json:"effectiveness"`
	MinData       bool    `json:"min_data"` // true when injections >= 10
}

// minDataThreshold is the minimum number of injections required for effectiveness data
// to be considered statistically meaningful.
const minDataThreshold = 10

// ComputeEffectiveness calculates effectiveness from stored counters.
// When injections is 0, effectiveness is 0 and MinData is false.
func ComputeEffectiveness(obsID int64, injections, successes int) EffectivenessResult {
	var effectiveness float64
	if injections > 0 {
		effectiveness = float64(successes) / float64(injections)
	}
	return EffectivenessResult{
		ObservationID: obsID,
		Injections:    injections,
		Successes:     successes,
		Effectiveness: effectiveness,
		MinData:       injections >= minDataThreshold,
	}
}
