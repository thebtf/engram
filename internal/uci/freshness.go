package uci

import "fmt"

// QueryFreshnessDisposition maps validated freshness evidence to its closed
// transport outcome without inspecting a retrieval response.
type QueryFreshnessDisposition string

const (
	QueryFreshnessDispositionCurrent QueryFreshnessDisposition = "current"
	QueryFreshnessDispositionStale   QueryFreshnessDisposition = "stale"
	QueryFreshnessDispositionOffline QueryFreshnessDisposition = "offline"
)

// ClassifyQueryFreshness validates freshness evidence and determines whether a
// selected View can remain current, must be surfaced as stale, or is offline.
func ClassifyQueryFreshness(freshness QueryFreshness) (QueryFreshnessDisposition, error) {
	if err := freshness.Validate(); err != nil {
		return "", err
	}

	switch freshness.State {
	case QueryFreshnessOffline:
		return QueryFreshnessDispositionOffline, nil
	case QueryFreshnessCatchingUp, QueryFreshnessUnknown:
		return QueryFreshnessDispositionStale, nil
	case QueryFreshnessObservedCurrent, QueryFreshnessHistorical:
		if freshness.Barrier != nil && freshness.Barrier.State != QueryBarrierSatisfied {
			return QueryFreshnessDispositionStale, nil
		}
		return QueryFreshnessDispositionCurrent, nil
	default:
		return "", fmt.Errorf("uci query freshness: unsupported state %q", freshness.State)
	}
}
