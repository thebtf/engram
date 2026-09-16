package uci_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/uci"
)

func TestExposureHealthControllerClosedStateMachine(t *testing.T) {
	health := uci.NewExposureHealthController(true)
	assertExposureHealthSnapshot(t, health.Snapshot(), uci.ExposureHealthHealthy, uci.ExposureHealthFailureNone)

	health.RecordCompletionFailure()
	assertExposureHealthSnapshot(t, health.Snapshot(), uci.ExposureHealthDegraded, uci.ExposureHealthFailureCompletionEvidenceUnavailable)

	health.RecordCompletionSuccess()
	assertExposureHealthSnapshot(t, health.Snapshot(), uci.ExposureHealthHealthy, uci.ExposureHealthFailureNone)

	health.RecordIntegrityFailure()
	assertExposureHealthSnapshot(t, health.Snapshot(), uci.ExposureHealthUnavailable, uci.ExposureHealthFailureExposureUnavailable)

	health.RecordCompletionFailure()
	health.RecordCompletionSuccess()
	assertExposureHealthSnapshot(t, health.Snapshot(), uci.ExposureHealthUnavailable, uci.ExposureHealthFailureExposureUnavailable)

	health.RecordInitialExposureSuccess()
	assertExposureHealthSnapshot(t, health.Snapshot(), uci.ExposureHealthHealthy, uci.ExposureHealthFailureNone)

	health.RecordInitialExposureFailure()
	assertExposureHealthSnapshot(t, health.Snapshot(), uci.ExposureHealthUnavailable, uci.ExposureHealthFailureExposureUnavailable)

	unconfigured := uci.NewExposureHealthController(false)
	assertExposureHealthSnapshot(t, unconfigured.Snapshot(), uci.ExposureHealthUnavailable, uci.ExposureHealthFailureExposureUnavailable)
}

func TestExposureHealthSnapshotIsImmutableAndSecretFree(t *testing.T) {
	health := uci.NewExposureHealthController(true)
	snapshot := health.Snapshot()
	snapshot.State = uci.ExposureHealthUnavailable
	snapshot.LastFailureCode = uci.ExposureHealthFailureExposureUnavailable
	assertExposureHealthSnapshot(t, health.Snapshot(), uci.ExposureHealthHealthy, uci.ExposureHealthFailureNone)

	encoded, err := json.Marshal(health.Snapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	const want = `{"state":"healthy","last_failure_code":"NONE"}`
	if got := string(encoded); got != want {
		t.Fatalf("snapshot JSON = %s, want %s", got, want)
	}
}

func TestExposureHealthControllerConcurrentTransitions(t *testing.T) {
	health := uci.NewExposureHealthController(true)
	start := make(chan struct{})
	invalid := make(chan uci.ExposureHealthSnapshot, 1)
	var workers sync.WaitGroup
	for worker := range 8 {
		workers.Add(1)
		go exposureHealthRunTransitions(health, start, &workers, worker)
	}
	for range 8 {
		workers.Add(1)
		go exposureHealthReadSnapshots(health, start, invalid, &workers)
	}
	close(start)
	workers.Wait()
	exposureHealthRequireClosedSnapshots(t, health, invalid)
}

func exposureHealthRunTransitions(health *uci.ExposureHealthController, start <-chan struct{}, workers *sync.WaitGroup, worker int) {
	defer workers.Done()
	<-start
	for iteration := range 500 {
		switch (worker + iteration) % 5 {
		case 0:
			health.RecordInitialExposureSuccess()
		case 1:
			health.RecordInitialExposureFailure()
		case 2:
			health.RecordCompletionSuccess()
		case 3:
			health.RecordCompletionFailure()
		case 4:
			health.RecordIntegrityFailure()
		}
	}
}

func exposureHealthReadSnapshots(health *uci.ExposureHealthController, start <-chan struct{}, invalid chan<- uci.ExposureHealthSnapshot, workers *sync.WaitGroup) {
	defer workers.Done()
	<-start
	for range 500 {
		snapshot := health.Snapshot()
		if !isClosedExposureHealthSnapshot(snapshot) {
			select {
			case invalid <- snapshot:
			default:
			}
		}
	}
}

func exposureHealthRequireClosedSnapshots(t *testing.T, health *uci.ExposureHealthController, invalid <-chan uci.ExposureHealthSnapshot) {
	t.Helper()
	select {
	case snapshot := <-invalid:
		t.Fatalf("concurrent snapshot escaped closed state machine: %#v", snapshot)
	default:
	}
	if snapshot := health.Snapshot(); !isClosedExposureHealthSnapshot(snapshot) {
		t.Fatalf("final snapshot escaped closed state machine: %#v", snapshot)
	}
}

func TestExposureRecorderReportsCompletionFailureWithoutStore(t *testing.T) {
	health := &exposureHealthControllerFake{}
	recorder := uci.NewExposureRecorder(nil, health)

	_, err := recorder.RecordCompletion(context.Background(), uci.VerifiedSupportedHostCallback{
		ExposureRef:      uci.NewExposureRef(),
		SupportedHostRef: "supported-host",
		CallbackRef:      "callback",
		Outcome:          uci.CompletionSucceeded,
		IdempotencyKey:   "callback-idempotency",
		OccurredAt:       time.Unix(1, 0).UTC(),
	})
	if err == nil {
		t.Fatal("RecordCompletion without a configured recorder store unexpectedly succeeded")
	}
	if got, want := health.events, []string{"completion-failure"}; !equalStrings(got, want) {
		t.Fatalf("health events = %#v, want %#v", got, want)
	}
}

type exposureHealthControllerFake struct {
	events []string
}

func (*exposureHealthControllerFake) Snapshot() uci.ExposureHealthSnapshot {
	return uci.ExposureHealthSnapshot{
		State:           uci.ExposureHealthHealthy,
		LastFailureCode: uci.ExposureHealthFailureNone,
	}
}

func (fake *exposureHealthControllerFake) RecordInitialExposureSuccess() {
	fake.events = append(fake.events, "initial-exposure-success")
}

func (fake *exposureHealthControllerFake) RecordInitialExposureFailure() {
	fake.events = append(fake.events, "initial-exposure-failure")
}

func (fake *exposureHealthControllerFake) RecordCompletionSuccess() {
	fake.events = append(fake.events, "completion-success")
}

func (fake *exposureHealthControllerFake) RecordCompletionFailure() {
	fake.events = append(fake.events, "completion-failure")
}

func (fake *exposureHealthControllerFake) RecordIntegrityFailure() {
	fake.events = append(fake.events, "integrity-failure")
}

func (fake *exposureHealthControllerFake) RecordIdempotencyMismatch() {
	fake.events = append(fake.events, "idempotency-mismatch")
}

func assertExposureHealthSnapshot(t *testing.T, got uci.ExposureHealthSnapshot, state uci.ExposureHealthState, failure uci.ExposureHealthFailureCode) {
	t.Helper()
	want := uci.ExposureHealthSnapshot{State: state, LastFailureCode: failure}
	if got != want {
		t.Fatalf("snapshot = %#v, want %#v", got, want)
	}
}

func isClosedExposureHealthSnapshot(snapshot uci.ExposureHealthSnapshot) bool {
	switch snapshot.State {
	case uci.ExposureHealthHealthy:
		return snapshot.LastFailureCode == uci.ExposureHealthFailureNone
	case uci.ExposureHealthDegraded:
		return snapshot.LastFailureCode == uci.ExposureHealthFailureCompletionEvidenceUnavailable
	case uci.ExposureHealthUnavailable:
		return snapshot.LastFailureCode == uci.ExposureHealthFailureExposureUnavailable
	default:
		return false
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
