package core

import (
	"context"
	"fmt"
	"sync"
)

// Compile-time assertion: *attentionEventBus must satisfy AttentionEventBus.
var _ AttentionEventBus = (*attentionEventBus)(nil)

// attentionEventBus is the concrete implementation of AttentionEventBus.
// It provides in-process buffered pub/sub for CORE substrate events per
// ADR-003 (async fan-out only; synchronous CandidateProposer queries bypass
// the bus entirely).
//
// Each subscriber gets a dedicated buffered channel (capacity 64) and a
// dedicated goroutine that reads from that channel and calls the handler.
// Publish is non-blocking: when a subscriber's buffer is full, the event is
// dropped and event_dropped_total is incremented via the injected meter.
type attentionEventBus struct {
	mu          sync.RWMutex
	subscribers map[string]*busSubscriber
	meter       SubsystemMeter
}

// busSubscriber holds the per-subscriber state. The done channel is the
// shutdown signal: Unsubscribe closes done (a synchronization primitive in
// Go's memory model) so the loop goroutine exits cleanly without anybody
// closing the data channel ch. Closing ch concurrently with a Publish send
// is a data race under -race even when the resulting panic is recovered;
// leaving ch alive and using close(done) as the wakeup avoids that entirely.
// The orphan ch buffer (≤64 events) is reclaimed by GC once the subscriber
// is delisted and any in-flight Publish snapshot drops its reference.
type busSubscriber struct {
	name   string
	ch     chan AttentionEventPayload
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once // guards close(done) + cancel() so Unsubscribe is idempotent
}

// NewAttentionEventBus creates a new attentionEventBus with the provided
// SubsystemMeter for drop-event counting. meter must be non-nil; passing a
// no-op meter is acceptable, but a nil meter will panic on the first drop.
func NewAttentionEventBus(meter SubsystemMeter) *attentionEventBus {
	return &attentionEventBus{
		subscribers: make(map[string]*busSubscriber),
		meter:       meter,
	}
}

// Publish dispatches event to every registered subscriber concurrently using
// a non-blocking send. If a subscriber's buffer is full, the event is dropped
// and event_dropped_total{subscriber=<name>} is incremented. Publish always
// returns nil; drops are signalled only through the meter.
func (b *attentionEventBus) Publish(ctx context.Context, event AttentionEventPayload) error {
	b.mu.RLock()
	// Snapshot the subscriber set under the read lock so we hold the lock as
	// briefly as possible and never call the meter while holding it.
	type entry struct {
		name string
		ch   chan AttentionEventPayload
	}
	subs := make([]entry, 0, len(b.subscribers))
	for name, sub := range b.subscribers {
		subs = append(subs, entry{name: name, ch: sub.ch})
	}
	b.mu.RUnlock()

	for _, s := range subs {
		// safeNonBlockingSend isolates the send from the Unsubscribe close(ch)
		// race: a subscriber can be unsubscribed (and its channel closed)
		// between snapshot-time and send-time. The recover boundary turns the
		// otherwise-fatal "send on closed channel" panic into a counted drop
		// scoped to the affected subscriber, leaving the Publish caller's
		// goroutine (typically an HTTP request handler) untouched.
		if dropped := safeNonBlockingSend(s.ch, event); dropped {
			b.meter.IncrCounter("event_dropped_total", 1, map[string]string{
				"subscriber": s.name,
			})
		}
	}
	return nil
}

// safeNonBlockingSend performs a non-blocking send to ch and returns true when
// the event was dropped — either because the buffer is full or because ch was
// closed concurrently by Unsubscribe. Both drop reasons fold into the same
// counter; distinguishing them at the bus level adds no operator value and
// would require deeper coupling with the subscriber lifecycle.
func safeNonBlockingSend(ch chan AttentionEventPayload, event AttentionEventPayload) (dropped bool) {
	defer func() {
		if recover() != nil {
			dropped = true
		}
	}()
	select {
	case ch <- event:
		return false
	default:
		return true
	}
}

// Subscribe registers handler under name and returns an Unsubscribe function.
// A duplicate name returns an error; subsequent calls with the same name after
// the original subscriber has been unsubscribed are allowed.
//
// The handler is called in a dedicated goroutine. The goroutine is bound to a
// child context derived internally; calling the returned Unsubscribe function
// closes the subscriber's channel and cancels its context, causing the
// goroutine to exit.
func (b *attentionEventBus) Subscribe(name string, handler EventHandler) (Unsubscribe, error) {
	b.mu.Lock()
	if _, exists := b.subscribers[name]; exists {
		b.mu.Unlock()
		return nil, fmt.Errorf("attentionEventBus: subscriber %q already registered", name)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sub := &busSubscriber{
		name:   name,
		ch:     make(chan AttentionEventPayload, 64),
		done:   make(chan struct{}),
		cancel: cancel,
	}
	b.subscribers[name] = sub
	b.mu.Unlock()

	// Start the subscriber goroutine. It exits when:
	// (a) sub.ch is closed (ok==false), or
	// (b) ctx is cancelled (ctx.Done() fires).
	go runSubscriberLoop(ctx, sub, handler, b.meter)

	// Unsubscribe removes the subscriber from the map, signals the goroutine
	// to exit via the done channel, and cancels its context. sync.Once makes
	// it idempotent. We intentionally do NOT close(sub.ch) — closing a chan
	// concurrently with a chansend is a data race under -race, and Go's
	// memory model treats it as undefined behaviour even when the panic is
	// recovered. Letting the data channel go out of scope is the race-clean
	// equivalent of closing it.
	return func() {
		sub.once.Do(func() {
			b.mu.Lock()
			delete(b.subscribers, name)
			b.mu.Unlock()

			sub.cancel()
			close(sub.done)
		})
	}, nil
}

// runSubscriberLoop is the per-subscriber goroutine body. It reads events from
// sub.ch and calls handler, exiting when the channel is closed or ctx fires.
// A recover boundary isolates handler panics per PR-5 and emits a counter so
// operator dashboards can see the failure rather than silently lose deliveries.
func runSubscriberLoop(ctx context.Context, sub *busSubscriber, handler EventHandler, meter SubsystemMeter) {
	defer func() {
		if r := recover(); r != nil {
			// PR-5: recover from handler panic so the goroutine exits cleanly
			// rather than crashing the server process. Surface the panic via
			// the meter so it does not disappear silently.
			if meter != nil {
				meter.IncrCounter("subscriber_panic_total", 1, map[string]string{
					"subscriber": sub.name,
				})
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.done:
			// Unsubscribe asked us to exit. Any remaining buffered events on
			// sub.ch are orphaned (GC reclaims when no other reference); the
			// alternative of draining-then-exit would race with new sends.
			return
		case event := <-sub.ch:
			// Handler errors are recorded by the caller's own metrics; the bus
			// does not retry deliveries.
			handler(ctx, event) //nolint:errcheck
		}
	}
}
