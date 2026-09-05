package uci

import "sync"

// ExposureHealthState is the closed availability state of one scoped exposure recorder.
type ExposureHealthState string

const (
	ExposureHealthHealthy     ExposureHealthState = "healthy"
	ExposureHealthDegraded    ExposureHealthState = "degraded"
	ExposureHealthUnavailable ExposureHealthState = "unavailable"
)

// ExposureHealthFailureCode is the closed, non-diagnostic reason for recorder health.
type ExposureHealthFailureCode string

const (
	ExposureHealthFailureNone                          ExposureHealthFailureCode = "NONE"
	ExposureHealthFailureExposureUnavailable           ExposureHealthFailureCode = "EXPOSURE_UNAVAILABLE"
	ExposureHealthFailureCompletionEvidenceUnavailable ExposureHealthFailureCode = "COMPLETION_EVIDENCE_UNAVAILABLE"
)

// ExposureHealthSnapshot is a secret-free value copy of one recorder's current health.
type ExposureHealthSnapshot struct {
	State           ExposureHealthState       `json:"state"`
	LastFailureCode ExposureHealthFailureCode `json:"last_failure_code"`
}

// ExposureHealthTracker receives closed recorder lifecycle outcomes. A tracker is owned
// by one scoped ExposureRecorder; it never aggregates daemon or indexer state.
type ExposureHealthTracker interface {
	Snapshot() ExposureHealthSnapshot
	RecordInitialExposureSuccess()
	RecordInitialExposureFailure()
	RecordCompletionSuccess()
	RecordCompletionFailure()
	RecordIntegrityFailure()
	RecordIdempotencyMismatch()
}

// ExposureHealthController is an in-memory, concurrency-safe tracker for one recorder.
type ExposureHealthController struct {
	mu       sync.RWMutex
	snapshot ExposureHealthSnapshot
}

// NewExposureHealthController starts healthy only when its recorder is configured.
func NewExposureHealthController(configured bool) *ExposureHealthController {
	snapshot := unavailableExposureHealthSnapshot()
	if configured {
		snapshot = healthyExposureHealthSnapshot()
	}
	return &ExposureHealthController{snapshot: snapshot}
}

// Snapshot returns an immutable value copy containing only the closed health fields.
func (controller *ExposureHealthController) Snapshot() ExposureHealthSnapshot {
	if controller == nil {
		return unavailableExposureHealthSnapshot()
	}
	controller.mu.RLock()
	defer controller.mu.RUnlock()
	return controller.snapshot
}

// RecordInitialExposureSuccess restores recorder health after a successful initial append.
func (controller *ExposureHealthController) RecordInitialExposureSuccess() {
	controller.set(healthyExposureHealthSnapshot())
}

// RecordInitialExposureFailure records an initial exposure append failure.
func (controller *ExposureHealthController) RecordInitialExposureFailure() {
	controller.set(unavailableExposureHealthSnapshot())
}

// RecordCompletionSuccess clears only a completion-evidence degradation.
func (controller *ExposureHealthController) RecordCompletionSuccess() {
	if controller == nil {
		return
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.snapshot == completionDegradedExposureHealthSnapshot() {
		controller.snapshot = healthyExposureHealthSnapshot()
	}
}

// RecordCompletionFailure records a completion append failure without masking unavailability.
func (controller *ExposureHealthController) RecordCompletionFailure() {
	if controller == nil {
		return
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.snapshot.State != ExposureHealthUnavailable {
		controller.snapshot = completionDegradedExposureHealthSnapshot()
	}
}

// RecordIntegrityFailure records a durable-evidence integrity failure as unavailable.
func (controller *ExposureHealthController) RecordIntegrityFailure() {
	controller.set(unavailableExposureHealthSnapshot())
}

// RecordIdempotencyMismatch deliberately leaves prior recorder health unchanged.
func (*ExposureHealthController) RecordIdempotencyMismatch() {}

func (controller *ExposureHealthController) set(snapshot ExposureHealthSnapshot) {
	if controller == nil {
		return
	}
	controller.mu.Lock()
	controller.snapshot = snapshot
	controller.mu.Unlock()
}

func healthyExposureHealthSnapshot() ExposureHealthSnapshot {
	return ExposureHealthSnapshot{
		State:           ExposureHealthHealthy,
		LastFailureCode: ExposureHealthFailureNone,
	}
}

func completionDegradedExposureHealthSnapshot() ExposureHealthSnapshot {
	return ExposureHealthSnapshot{
		State:           ExposureHealthDegraded,
		LastFailureCode: ExposureHealthFailureCompletionEvidenceUnavailable,
	}
}

func unavailableExposureHealthSnapshot() ExposureHealthSnapshot {
	return ExposureHealthSnapshot{
		State:           ExposureHealthUnavailable,
		LastFailureCode: ExposureHealthFailureExposureUnavailable,
	}
}
