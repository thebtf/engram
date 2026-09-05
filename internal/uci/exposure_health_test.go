package uci_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/thebtf/engram/internal/db/gorm"
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

func TestExposureHealthControllerIdempotencyMismatchIsNoOp(t *testing.T) {
	for _, prepare := range []struct {
		name  string
		apply func(*uci.ExposureHealthController)
	}{
		{name: "healthy", apply: func(*uci.ExposureHealthController) {}},
		{name: "degraded", apply: func(health *uci.ExposureHealthController) { health.RecordCompletionFailure() }},
		{name: "unavailable", apply: func(health *uci.ExposureHealthController) { health.RecordInitialExposureFailure() }},
	} {
		t.Run(prepare.name, func(t *testing.T) {
			health := uci.NewExposureHealthController(true)
			prepare.apply(health)
			before := health.Snapshot()

			health.RecordIdempotencyMismatch()

			if got := health.Snapshot(); got != before {
				t.Fatalf("snapshot after idempotency mismatch = %#v, want unchanged %#v", got, before)
			}
		})
	}
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
		go func() {
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
		}()
	}
	for range 8 {
		workers.Add(1)
		go func() {
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
		}()
	}

	close(start)
	workers.Wait()

	select {
	case snapshot := <-invalid:
		t.Fatalf("concurrent snapshot escaped closed state machine: %#v", snapshot)
	default:
	}
	if snapshot := health.Snapshot(); !isClosedExposureHealthSnapshot(snapshot) {
		t.Fatalf("final snapshot escaped closed state machine: %#v", snapshot)
	}
}

func TestUCIExposureStoreReportsUnavailableAndIntegrityFailuresWithoutDB(t *testing.T) {
	health := &exposureHealthControllerFake{}
	store := gorm.NewUCIExposureStoreWithHealth(nil, health)

	if _, err := store.RecordExposure(context.Background(), gorm.UCIExposureInput{}); err == nil {
		t.Fatal("RecordExposure without a configured store unexpectedly succeeded")
	}
	if _, err := store.RecordCompletion(context.Background(), gorm.UCICompletionInput{}); err == nil {
		t.Fatal("RecordCompletion without a configured store unexpectedly succeeded")
	}
	if err := store.VerifyIntegrity(context.Background()); err == nil {
		t.Fatal("VerifyIntegrity without a configured store unexpectedly succeeded")
	}
	if got, want := health.events, []string{"initial-exposure-failure", "completion-failure", "integrity-failure"}; !equalStrings(got, want) {
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
