// Package projectevents provides an in-process synchronous event bus for
// project lifecycle events emitted by the engram-server. Subscribers receive
// events synchronously in registration order; panics in subscriber handlers
// are recovered and logged so one misbehaving subscriber cannot affect others.
package projectevents

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog/log"
)

// EventType identifies the kind of project lifecycle transition.
type EventType string

const (
	// EventTypeRemoved is emitted when a project is soft-deleted via
	// DELETE /api/projects/{id}.
	EventTypeRemoved EventType = "project_removed"
)

// Event carries data for a single project lifecycle transition.
type Event struct {
	EventType       EventType         `json:"event_type"`
	ProjectID       string            `json:"project_id"`
	TimestampUnixMs int64             `json:"timestamp_unix_ms"`
	Reason          string            `json:"reason,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

// subscription holds a registered handler and its unique ID.
type subscription struct {
	id      uint64
	handler func(Event)
}

// Bus is a synchronous fan-out event bus for project lifecycle events.
// All subscribers are called on the Emit goroutine in registration order.
// Panics in subscribers are recovered individually so one failing handler
// does not prevent delivery to subsequent subscribers.
//
// Bus is safe for concurrent use.
type Bus struct {
	mu   sync.Mutex
	subs []*subscription
	seq  atomic.Uint64
}

// Subscribe registers a handler that is called for every future event.
// The returned function, when called, removes the subscription. The
// unsubscribe function is idempotent.
func (b *Bus) Subscribe(handler func(Event)) func() {
	id := b.seq.Add(1)
	sub := &subscription{id: id, handler: handler}

	b.mu.Lock()
	b.subs = append(b.subs, sub)
	b.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			for i, s := range b.subs {
				if s.id == id {
					b.subs = append(b.subs[:i], b.subs[i+1:]...)
					return
				}
			}
		})
	}
}

// Emit delivers ev to all currently registered subscribers synchronously,
// in registration order. Panics in individual subscriber handlers are
// recovered and logged; delivery continues to remaining subscribers.
func (b *Bus) Emit(ev Event) {
	b.mu.Lock()
	// Snapshot the subscriber slice under lock to avoid holding the lock
	// while calling handlers (which could deadlock if a handler calls
	// Subscribe/Unsubscribe).
	snapshot := make([]*subscription, len(b.subs))
	copy(snapshot, b.subs)
	b.mu.Unlock()

	for _, sub := range snapshot {
		deliverSafe(sub, ev)
	}
}

// deliverSafe calls sub.handler(ev) and recovers any panic.
func deliverSafe(sub *subscription, ev Event) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Uint64("subscription_id", sub.id).
				Str("event_type", string(ev.EventType)).
				Str("project_id", ev.ProjectID).
				Str("panic", fmt.Sprintf("%v", r)).
				Msg("projectevents: panic recovered in subscriber handler")
		}
	}()
	sub.handler(ev)
}
