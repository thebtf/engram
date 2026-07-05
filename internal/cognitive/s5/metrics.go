package s5

const (
	MetricHintPrecision      = "hint_precision"
	MetricAcceptedHintAction = "accepted_hint_action"
	MetricMissRate           = "miss_rate"
	MetricInterruptionBurden = "interruption_burden"
	MetricStateFreshness     = "state_freshness"
)

const (
	readinessStateNoSample       = "no_sample"
	readinessStateBelowThreshold = "below_threshold"
	readinessStateReady          = "ready"
)

const (
	defaultThresholdHintPrecision      uint64 = 30
	defaultThresholdAcceptedHintAction uint64 = 20
	defaultThresholdMissRate           uint64 = 0
	defaultThresholdInterruptionBurden uint64 = 0
	defaultThresholdStateFreshness     uint64 = 0
)

func CanonicalMetricKeys() []string {
	return []string{
		MetricHintPrecision,
		MetricAcceptedHintAction,
		MetricMissRate,
		MetricInterruptionBurden,
		MetricStateFreshness,
	}
}

func emptyMetrics() map[string]float64 {
	return make(map[string]float64, len(CanonicalMetricKeys()))
}

func isCanonicalMetric(metric string) bool {
	switch metric {
	case MetricHintPrecision,
		MetricAcceptedHintAction,
		MetricMissRate,
		MetricInterruptionBurden,
		MetricStateFreshness:
		return true
	default:
		return false
	}
}

func canonicalReadinessThresholds() map[string]uint64 {
	return map[string]uint64{
		MetricHintPrecision:      defaultThresholdHintPrecision,
		MetricAcceptedHintAction: defaultThresholdAcceptedHintAction,
		MetricMissRate:           defaultThresholdMissRate,
		MetricInterruptionBurden: defaultThresholdInterruptionBurden,
		MetricStateFreshness:     defaultThresholdStateFreshness,
	}
}
