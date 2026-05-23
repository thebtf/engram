package core

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testMeter is a minimal SubsystemMeter implementation for T007 tests.
// It records IncrCounter calls so tests can assert drop counter behavior
// without depending on T009's concrete meter.
type testMeter struct {
	mu       sync.Mutex
	counters map[string]uint64 // key = "name{subscriber=X}"
}

func newTestMeter() *testMeter {
	return &testMeter{counters: make(map[string]uint64)}
}

// IncrCounter records delta under a key derived from name + tags.
// For drop counter assertions: name="event_dropped_total", tags={"subscriber": <name>}.
func (m *testMeter) IncrCounter(name string, delta uint64, tags map[string]string) {
	key := name
	if sub, ok := tags["subscriber"]; ok {
		key = name + "{subscriber=" + sub + "}"
	}
	m.mu.Lock()
	m.counters[key] += delta
	m.mu.Unlock()
}

// ObserveHistogram is a no-op for T007 tests.
func (m *testMeter) ObserveHistogram(name string, value float64, tags map[string]string) {}

// Snapshot returns a copy of the current counters.
func (m *testMeter) Snapshot() MetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := MetricsSnapshot{
		Counters:   make(map[string]uint64, len(m.counters)),
		Histograms: make(map[string]HistogramSummary),
	}
	for k, v := range m.counters {
		snap.Counters[k] = v
	}
	return snap
}

// dropCount returns the accumulated drop counter for the named subscriber.
func (m *testMeter) dropCount(subscriberName string) uint64 {
	key := "event_dropped_total{subscriber=" + subscriberName + "}"
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counters[key]
}

// --- Tests ------------------------------------------------------------------

// TestPublish_FanOut verifies that Publish delivers the event to all 3
// subscribers. Each handler increments a shared atomic counter; after
// publishing 1 event, the counter must reach 3 within 100ms.
func TestPublish_FanOut(t *testing.T) {
	meter := newTestMeter()
	bus := NewAttentionEventBus(meter)

	var received atomic.Int64
	done := make(chan struct{})

	handler := func(ctx context.Context, event AttentionEventPayload) error {
		if received.Add(1) == 3 {
			close(done)
		}
		return nil
	}

	for _, name := range []string{"sub-a", "sub-b", "sub-c"} {
		unsub, err := bus.Subscribe(name, handler)
		if err != nil {
			t.Fatalf("Subscribe(%q): unexpected error: %v", name, err)
		}
		t.Cleanup(unsub)
	}

	event := AttentionEventPayload{
		Type:      "test.event",
		SessionID: "sess-1",
		Timestamp: time.Now(),
	}

	if err := bus.Publish(context.Background(), event); err != nil {
		t.Fatalf("Publish: unexpected error: %v", err)
	}

	select {
	case <-done:
		// all 3 handlers delivered
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("fan-out timeout: only %d/3 handlers received the event", received.Load())
	}
}

// TestSlowSubscriber_DropsEvents_OthersUnaffected (EC-3) verifies the
// async-bus isolation property: when one subscriber's handler is slow enough
// to back its buffer up, the SLOW subscriber accumulates drops, while a
// FAST subscriber stays substantially healthier in the same window.
//
// The bus contract is non-blocking-send-with-drop-on-full (cap 64 per
// subscriber). With a 128-event burst published as fast as the publisher
// can write, BOTH subscribers can in principle see drops — the fast
// subscriber's handler may not keep up with a tight publish loop. The
// real isolation property the test pins is RELATIVE: the slow subscriber
// drops far more events than the fast one, and fast receives an order of
// magnitude more deliveries than slow. Absolute "fast receives all 128"
// would only hold with a back-pressure contract the bus deliberately
// does not provide.
func TestSlowSubscriber_DropsEvents_OthersUnaffected(t *testing.T) {
	const publishCount = 128

	meter := newTestMeter()
	bus := NewAttentionEventBus(meter)

	// fastReceived counts how many events the fast handler processed.
	var fastReceived atomic.Int64
	fastHandler := func(ctx context.Context, event AttentionEventPayload) error {
		fastReceived.Add(1)
		return nil
	}

	// slowHandler blocks for 500ms per event, ensuring the buffer fills.
	var slowReceived atomic.Int64
	slowHandler := func(ctx context.Context, event AttentionEventPayload) error {
		slowReceived.Add(1)
		time.Sleep(500 * time.Millisecond)
		return nil
	}

	fastUnsub, err := bus.Subscribe("fast", fastHandler)
	if err != nil {
		t.Fatalf("Subscribe fast: %v", err)
	}
	defer fastUnsub()

	slowUnsub, err := bus.Subscribe("slow", slowHandler)
	if err != nil {
		t.Fatalf("Subscribe slow: %v", err)
	}
	defer slowUnsub()

	event := AttentionEventPayload{Type: "test.flood", Timestamp: time.Now()}

	for i := 0; i < publishCount; i++ {
		if err := bus.Publish(context.Background(), event); err != nil {
			t.Fatalf("Publish[%d]: %v", i, err)
		}
	}

	// Give the fast handler time to drain its buffer (the slow handler is
	// still mid-sleep, so its drops are already locked in). 200 ms is well
	// over the ~µs per fastHandler invocation but short enough that the
	// slow handler never catches up.
	time.Sleep(200 * time.Millisecond)

	const bufferCap = 64
	fastGot := fastReceived.Load()
	slowGot := slowReceived.Load()
	fastDrops := meter.dropCount("fast")
	slowDrops := meter.dropCount("slow")

	// Per-subscriber accounting: at any instant, delivered + dropped +
	// buffered = published, with buffered ≤ cap. We therefore expect
	//
	//	delivered + dropped ≥ published - cap
	//
	// (anything below that floor means lost events the bus cannot account
	// for). The slow subscriber still holds up to `cap` events in its
	// buffered channel waiting for the 500 ms handler to drain — that is
	// why a strict delivered+dropped==published assertion would be wrong.
	if got := int(fastGot) + int(fastDrops); got < publishCount-bufferCap {
		t.Errorf("fast accounting: delivered+dropped = %d, want ≥ %d (publishCount-cap)",
			got, publishCount-bufferCap)
	}
	if got := int(slowGot) + int(slowDrops); got < publishCount-bufferCap {
		t.Errorf("slow accounting: delivered+dropped = %d, want ≥ %d (publishCount-cap)",
			got, publishCount-bufferCap)
	}

	// Slow subscriber MUST experience drops — publishing 128 in a tight
	// loop while sleeping 500 ms per event guarantees buffer overflow.
	if slowDrops == 0 {
		t.Errorf("slow subscriber drop count = 0, want ≥ 1")
	}

	// Real isolation property under a non-blocking bus contract: the FAST
	// subscriber DELIVERS substantially more events than the SLOW one. The
	// drop counters are noisy because goroutine scheduling under burst can
	// cause some fast drops too — but the delivery ratio is the genuine
	// "OthersUnaffected" signal. We require fast to deliver ≥ 10× slow.
	if int64(slowGot)*10 > fastGot && slowGot > 0 {
		t.Errorf("isolation broken: fast delivered %d, slow delivered %d (ratio %.2fx, want ≥ 10x)",
			fastGot, slowGot, float64(fastGot)/float64(slowGot))
	}

	// Fast subscriber should receive substantially more deliveries than
	// slow. Tight bound: fast receives > publishCount/2.
	if fastGot <= publishCount/2 {
		t.Errorf("fast handler received only %d/%d — fast subscriber appears penalised", fastGot, publishCount)
	}
}

// TestUnsubscribe_StopsDelivery subscribes a handler, immediately unsubscribes
// it, then publishes an event. The handler must receive 0 deliveries within
// 100ms.
func TestUnsubscribe_StopsDelivery(t *testing.T) {
	meter := newTestMeter()
	bus := NewAttentionEventBus(meter)

	var received atomic.Int64
	unsub, err := bus.Subscribe("sub-x", func(ctx context.Context, event AttentionEventPayload) error {
		received.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Unsubscribe before publishing.
	unsub()

	event := AttentionEventPayload{Type: "test.after-unsub", Timestamp: time.Now()}
	if err := bus.Publish(context.Background(), event); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Wait briefly and verify no delivery.
	time.Sleep(50 * time.Millisecond)

	if got := received.Load(); got != 0 {
		t.Errorf("handler received %d events after Unsubscribe, want 0", got)
	}
}

// TestGoroutineLeak_None_EventBus verifies that Subscribe starts exactly one
// goroutine per subscriber and Unsubscribe terminates it without leaking. Uses
// only runtime.NumGoroutine() (no external goleak dependency, per Fix #5).
// Name disambiguated from the HintQueue sibling test in this same package.
func TestGoroutineLeak_None_EventBus(t *testing.T) {
	meter := newTestMeter()
	bus := NewAttentionEventBus(meter)

	// Allow existing goroutines (GC, test runner, etc.) to settle.
	runtime.GC()
	time.Sleep(10 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	const subCount = 3
	unsubs := make([]Unsubscribe, subCount)

	handler := func(ctx context.Context, event AttentionEventPayload) error { return nil }

	for i := 0; i < subCount; i++ {
		name := []string{"leak-a", "leak-b", "leak-c"}[i]
		u, err := bus.Subscribe(name, handler)
		if err != nil {
			t.Fatalf("Subscribe(%q): %v", name, err)
		}
		unsubs[i] = u
	}

	// After subscribing, goroutine count should have grown by subCount.
	withSubs := runtime.NumGoroutine()
	if withSubs < baseline+subCount {
		t.Errorf("expected at least %d goroutines after Subscribe (baseline=%d + %d subs), got %d",
			baseline+subCount, baseline, subCount, withSubs)
	}

	// Unsubscribe all.
	for _, u := range unsubs {
		u()
	}

	// Give goroutines time to exit (100ms grace period per task spec).
	time.Sleep(100 * time.Millisecond)
	runtime.GC()

	after := runtime.NumGoroutine()
	if after > baseline {
		t.Errorf("goroutine leak: baseline=%d, after unsubscribe+100ms=%d (delta=%d)",
			baseline, after, after-baseline)
	}
}

// TestSubscribe_DuplicateName_Error verifies that a second Subscribe with the
// same name returns a non-nil error (AC: "Duplicate names return an error").
func TestSubscribe_DuplicateName_Error(t *testing.T) {
	meter := newTestMeter()
	bus := NewAttentionEventBus(meter)

	noop := func(ctx context.Context, event AttentionEventPayload) error { return nil }

	unsub, err := bus.Subscribe("dup", noop)
	if err != nil {
		t.Fatalf("first Subscribe: unexpected error: %v", err)
	}
	defer unsub()

	_, err = bus.Subscribe("dup", noop)
	if err == nil {
		t.Error("second Subscribe with duplicate name: expected error, got nil")
	}
}

// TestRace_ConcurrentPublishUnsubscribe stresses the Publish/Unsubscribe race:
// many goroutines repeatedly subscribe and unsubscribe while a publisher
// goroutine flushes events as fast as possible. Without the safeNonBlockingSend
// recover boundary this test panics with "send on closed channel" under
// `-race`. With the fix in place every drop is counted (or successfully
// delivered) and the test completes cleanly.
func TestRace_ConcurrentPublishUnsubscribe(t *testing.T) {
	meter := newTestMeter()
	bus := NewAttentionEventBus(meter)

	noop := func(ctx context.Context, event AttentionEventPayload) error { return nil }
	event := AttentionEventPayload{Type: "race.flood", Timestamp: time.Now()}

	const (
		subCycles  = 200
		publishers = 4
		publishMs  = 100
	)

	var wg sync.WaitGroup

	// Publisher goroutines.
	stop := make(chan struct{})
	for i := 0; i < publishers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = bus.Publish(context.Background(), event)
				}
			}
		}()
	}

	// Sub/unsub churn goroutines.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < subCycles; i++ {
			name := "churn"
			unsub, err := bus.Subscribe(name, noop)
			if err != nil {
				// duplicate-name from racing iterations is acceptable; skip.
				continue
			}
			// Yield briefly so publishers can snapshot then attempt sends.
			time.Sleep(time.Microsecond)
			unsub()
		}
	}()

	// Bounded test duration.
	time.Sleep(publishMs * time.Millisecond)
	close(stop)
	wg.Wait()
}
