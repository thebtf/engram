package projectevents

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestBus_HappyPath(t *testing.T) {
	t.Parallel()
	b := &Bus{}

	var received []Event
	unsub := b.Subscribe(func(e Event) {
		received = append(received, e)
	})
	defer unsub()

	ev := Event{
		EventType: EventTypeRemoved,
		ProjectID: "proj-abc",
	}
	b.Emit(ev)

	if len(received) != 1 {
		t.Fatalf("expected 1 event, got %d", len(received))
	}
	if received[0].ProjectID != "proj-abc" {
		t.Fatalf("expected project_id proj-abc, got %q", received[0].ProjectID)
	}
	if received[0].EventType != EventTypeRemoved {
		t.Fatalf("expected event_type %q, got %q", EventTypeRemoved, received[0].EventType)
	}
}

func TestBus_MultipleSubscribers(t *testing.T) {
	t.Parallel()
	b := &Bus{}

	var mu sync.Mutex
	var order []int

	for i := 0; i < 3; i++ {
		idx := i // capture loop variable
		b.Subscribe(func(_ Event) {
			mu.Lock()
			order = append(order, idx)
			mu.Unlock()
		})
	}

	b.Emit(Event{EventType: EventTypeRemoved, ProjectID: "p1"})

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 {
		t.Fatalf("expected 3 subscribers called, got %d", len(order))
	}
	// Subscribers are called in registration order (0, 1, 2).
	for i, v := range order {
		if v != i {
			t.Fatalf("expected subscriber %d at index %d, got %d", i, i, v)
		}
	}
}

func TestBus_Unsubscribe(t *testing.T) {
	t.Parallel()
	b := &Bus{}

	var count int
	unsub := b.Subscribe(func(_ Event) {
		count++
	})

	b.Emit(Event{EventType: EventTypeRemoved, ProjectID: "p1"})
	if count != 1 {
		t.Fatalf("expected count=1 before unsub, got %d", count)
	}

	unsub()
	b.Emit(Event{EventType: EventTypeRemoved, ProjectID: "p2"})
	if count != 1 {
		t.Fatalf("expected count=1 after unsub, got %d (unsub did not work)", count)
	}

	// Idempotent second call must not panic.
	unsub()
}

func TestBus_PanicRecovered(t *testing.T) {
	t.Parallel()
	b := &Bus{}

	// Panicking subscriber.
	b.Subscribe(func(_ Event) {
		panic("deliberate panic in subscriber")
	})

	// Non-panicking subscriber that should still receive the event.
	var received bool
	b.Subscribe(func(_ Event) {
		received = true
	})

	// Emit must not panic and must deliver to the second subscriber.
	b.Emit(Event{EventType: EventTypeRemoved, ProjectID: "p1"})

	if !received {
		t.Fatal("second subscriber did not receive event after first subscriber panicked")
	}
}

func TestBus_ConcurrentEmit(t *testing.T) {
	t.Parallel()
	b := &Bus{}

	var counter atomic.Int64
	b.Subscribe(func(_ Event) {
		counter.Add(1)
	})

	const goroutines = 100
	const eventsEach = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < eventsEach; j++ {
				b.Emit(Event{EventType: EventTypeRemoved, ProjectID: "p"})
			}
		}()
	}

	wg.Wait()

	if got := counter.Load(); got != goroutines*eventsEach {
		t.Fatalf("expected %d events, got %d", goroutines*eventsEach, got)
	}
}

func TestBus_UnsubscribeIdempotent(t *testing.T) {
	t.Parallel()
	b := &Bus{}

	unsub := b.Subscribe(func(_ Event) {})

	// Call unsub multiple times — must not panic.
	for i := 0; i < 5; i++ {
		unsub()
	}
}
