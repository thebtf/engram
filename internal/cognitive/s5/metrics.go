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
