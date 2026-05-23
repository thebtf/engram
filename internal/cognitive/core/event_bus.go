package core

import (
	"context"
	"fmt"
	"sync"
)

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

// busSubscriber holds the per-subscriber state.
type busSubscriber struct {
	name   string
	ch     chan AttentionEventPayload
	cancel context.CancelFunc
	once   sync.Once // guards close(ch) + cancel() so Unsubscribe is idempotent
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
		select {
		case s.ch <- event:
			// delivered
		default:
			// buffer full — drop and count
			b.meter.IncrCounter("event_dropped_total", 1, map[string]string{
				"subscriber": s.name,
			})
		}
	}
	return nil
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
		cancel: cancel,
	}
	b.subscribers[name] = sub
	b.mu.Unlock()

	// Start the subscriber goroutine. It exits when:
	// (a) sub.ch is closed (ok==false), or
	// (b) ctx is cancelled (ctx.Done() fires).
	go runSubscriberLoop(ctx, sub, handler)

	// Unsubscribe removes the subscriber from the map, closes its channel, and
	// cancels its context. sync.Once ensures it is idempotent.
	return func() {
		sub.once.Do(func() {
			b.mu.Lock()
			delete(b.subscribers, name)
			b.mu.Unlock()

			sub.cancel()
			close(sub.ch)
		})
	}, nil
}

// runSubscriberLoop is the per-subscriber goroutine body. It reads events from
// sub.ch and calls handler, exiting when the channel is closed or ctx fires.
// A recover boundary isolates handler panics per PR-5.
func runSubscriberLoop(ctx context.Context, sub *busSubscriber, handler EventHandler) {
	defer func() {
		// PR-5: recover any panic from handler so the goroutine exits cleanly
		// rather than crashing the server process.
		recover() //nolint:errcheck
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-sub.ch:
			if !ok {
				// channel closed by Unsubscribe
				return
			}
			// Handler errors are recorded by the caller's own metrics; the bus
			// does not retry deliveries.
			handler(ctx, event) //nolint:errcheck
		}
	}
}
