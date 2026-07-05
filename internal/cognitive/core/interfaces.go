// Package core defines the CORE-internal interfaces and DTOs that hold the
// engram v7 plug substrate together. CORE owns the lifecycle, event fan-out,
// hint buffering, and generic counter primitives that every cognitive
// subsystem (S1, S2, S3, S4a, S4b, S5, S6) depends on. The types in this file
// are intentionally NOT re-exported from pkg/cognitive: parallel Wave-2 spec
// drafting and downstream subsystem authors only ever import the cross-
// subsystem contracts under pkg/cognitive, so a public/internal split keeps
// the public surface small and lets CORE evolve internal contracts without
// rippling through every consumer.
//
// Boundary invariant — enforced by interfaces_test.go: nothing declared here
// may be referenced (as a Go identifier) from any file under pkg/cognitive.
// Comment-level mentions are fine; AST identifier references are not.
//
// Pairings with ADR-010:
//   - SubsystemRegistry, Subsystem, SubsystemInfo, SubsystemHealth
//   - AttentionEventBus, EventHandler, Unsubscribe
//   - HintQueue, QueueStats
//   - SubsystemMeter, MetricsSnapshot, HistogramSummary
//
// Pairing with ADR-008 (CORE substrate vs S5 product metrics):
//   - ProductMetricsProvider, ProductMetricsWindow, ProductMetricsSnapshot
//
// Implementations live in sibling files (registry.go, event_bus.go, hint_queue.go,
// meter.go) introduced by tasks T006-T011.
package core

import (
	"context"
	"time"

	"github.com/thebtf/engram/pkg/cognitive"
)

// --- Subsystem lifecycle ----------------------------------------------------

// SubsystemRegistry is the CORE-owned lifecycle manager. Every subsystem
// registers itself once at server boot, then transitions through the
// registered → enabled / disabled / failed states defined by ADR-009. The
// Registry is the single legitimate caller of Subsystem.Start and
// Subsystem.Stop; subsystems never directly drive each other.
type SubsystemRegistry interface {
	// Register stores s under s.Name(); duplicate names return an error.
	// Registration does not enable the subsystem — Enable is required for
	// the flag-gated transition.
	Register(s Subsystem) error

	// Enable activates a registered subsystem, calling Subsystem.Start with
	// the prepared Dependencies. A subsequent Enable on the same name is a
	// no-op when the state is already enabled.
	Enable(name string) error

	// Disable shuts a subsystem down by calling Subsystem.Stop; the
	// subsystem remains registered and may be re-enabled.
	Disable(name string) error

	// Get returns the Subsystem registered under name and a boolean
	// indicating presence. Disabled and failed subsystems are still
	// returned — the lifecycle state is queried separately via Health.
	Get(name string) (Subsystem, bool)

	// List returns a snapshot of every registered subsystem's metadata.
	// The slice is independent of any internal storage.
	List() []SubsystemInfo

	// Health returns the lifecycle health of every registered subsystem.
	// The map is independent of internal storage; absent entries mean the
	// subsystem is not registered.
	Health() map[string]SubsystemHealth

	// ResolvePolicy reports the canonical ResolutionPolicy CORE applies when
	// dispatching to the named cross-subsystem interface (per spec §FR-7 and
	// Clarify C2). Subsystems and CORE coordinate by registering against the
	// declared policy: PolicyFanOut for CandidateProposer (S2, S6, S4b);
	// PolicySinglePrimary for every other cross-subsystem contract.
	// Unknown interface names return PolicySinglePrimary as a safe default.
	ResolvePolicy(interfaceName string) cognitive.ResolutionPolicy

	// TransitionToFailed flips the named subsystem to the ADR-009 "failed"
	// state and records reason on SubsystemHealth.PanicReason. The Dispatch
	// machinery in dispatcher.go uses this after recovering a handler panic
	// (T015) so subsequent calls treat the subsystem as NoOp until the
	// operator manually re-enables it via Disable+Enable. Calling on an
	// unknown name is a no-op (returns nil to keep callers idempotent in
	// the face of concurrent unregistration).
	TransitionToFailed(name string, reason string) error
}

// Subsystem is the contract every cognitive subsystem implements. Name and
// Version identify the subsystem in stats and metric tags; Implements lists
// the cross-subsystem interface names this subsystem provides, which lets
// SubsystemRegistry resolve cross-subsystem requests against the right
// implementations.
type Subsystem interface {
	Name() string
	Version() string
	Start(ctx context.Context, deps Dependencies) error
	Stop() error
	Implements() []string
}

// Dependencies bundle the CORE handles injected into every subsystem at
// Start time. The handles let the subsystem participate in the substrate
// without owning a direct reference to the SubsystemRegistry itself, which
// keeps test doubles simple. DB and Logger are typed `any` here so this
// file does not pull *gorm.DB or zerolog.Logger into the public dependency
// graph; concrete subsystems unwrap them.
type Dependencies struct {
	Registry SubsystemRegistry
	Bus      AttentionEventBus
	Queue    HintQueue
	Meter    SubsystemMeter
	DB       any
	Logger   any
}

// SubsystemInfo is a snapshot of a registered subsystem's identity and
// declared capability set.
type SubsystemInfo struct {
	Name       string
	Version    string
	State      string
	Implements []string
}

// SubsystemHealth captures runtime lifecycle state per ADR-009. State is
// one of "registered" | "enabled" | "disabled" | "failed". LastPanic is the
// zero time when no panic has occurred.
type SubsystemHealth struct {
	State       string
	LastPanic   time.Time
	PanicReason string
	EventsSeen  uint64
	ErrorsTotal uint64
}

// --- Attention event bus ----------------------------------------------------

// AttentionEventBus is the CORE-owned in-process pub/sub. Publish is the
// fan-out path: events are dispatched to every registered handler
// concurrently inside a panic-recovery boundary (PR-5). Subscribe registers
// a handler and returns an Unsubscribe handle. The Bus is intentionally
// async — synchronous S3 candidate queries take a different path
// (`/api/hooks/ambient-candidates` directly calls CandidateProposer impls).
type AttentionEventBus interface {
	// Publish dispatches event to every subscribed handler. The supplied
	// ctx bounds the Publish call but does not bound individual handler
	// execution beyond the implementation's own per-handler deadline.
	Publish(ctx context.Context, event AttentionEventPayload) error

	// Subscribe registers handler under name (used for diagnostics and
	// deregistration) and returns an Unsubscribe function. Duplicate names
	// return an error.
	Subscribe(name string, handler EventHandler) (Unsubscribe, error)
}

// AttentionEventPayload is the in-process event shape used by the bus. It
// mirrors the cross-subsystem AttentionEvent declared in pkg/cognitive but
// lives in the CORE package because the bus contract is internal. Type
// is one of the eight canonical strings documented on
// pkg/cognitive.AttentionEvent.
type AttentionEventPayload struct {
	Type      string
	SessionID string
	Project   string
	Payload   map[string]any
	Timestamp time.Time
}

// EventHandler processes one bus delivery. Returning an error increments the
// per-handler error counter; the bus does not retry deliveries.
type EventHandler func(ctx context.Context, event AttentionEventPayload) error

// Unsubscribe is returned by AttentionEventBus.Subscribe. Calling it more
// than once is a no-op.
type Unsubscribe func()

// --- Hint queue -------------------------------------------------------------

// HintQueue is the CORE-owned bounded buffer that holds HintProposal values
// produced by CandidateProposer implementations until the next render
// opportunity. ADR-004 mandates drop-oldest semantics — recency dominates
// retention. The queue is per-session: Enqueue accepts the sessionID and
// Drain takes a sessionID + max count.
type HintQueue interface {
	// Enqueue appends hint to sessionID's buffer. Overflow drops the oldest
	// entries; counters Overflows and Evicted are incremented accordingly.
	Enqueue(ctx context.Context, sessionID string, hint HintProposalPayload) error

	// Drain removes up to max hints from sessionID's buffer and returns
	// them in queue order. The returned slice is independent of internal
	// storage and may be empty.
	Drain(sessionID string, max int) []HintProposalPayload

	// Stats reports the current depth and lifetime overflow counters for
	// sessionID. Unknown sessions return the zero QueueStats value.
	Stats(sessionID string) QueueStats
}

// HintProposalPayload mirrors pkg/cognitive.HintProposal at the substrate
// boundary so the queue contract stays decoupled from the public payload
// type. Substrate code does not interpret the fields beyond CreatedAt for
// drop-oldest ordering.
type HintProposalPayload struct {
	ID        string
	Title     string
	Tags      []string
	CreatedAt time.Time
	Score     float32
	Source    string
	Reason    string
}

// QueueStats reports per-session queue health.
type QueueStats struct {
	QueuedNow int
	Overflows uint64
	Evicted   uint64
}

// --- Subsystem meter --------------------------------------------------------

// SubsystemMeter is the CORE-owned generic counter and histogram surface.
// Per ADR-008 this interface holds substrate metrics only — calls_total,
// errors_total, latency_ns_histogram, event_emitted, event_dropped. Product
// metric semantics belong to S5 via ProductMetricsProvider; SubsystemMeter
// must never carry product metric names. The canonical S5 metric key set
// is enumerated in ADR-008 and the S5 feature spec (CORE refuses to mirror
// those names so the T009 vocabulary gate stays mechanically enforceable).
type SubsystemMeter interface {
	// IncrCounter adds delta to the named counter, partitioned by tags.
	IncrCounter(name string, delta uint64, tags map[string]string)

	// ObserveHistogram records value under name, partitioned by tags.
	ObserveHistogram(name string, value float64, tags map[string]string)

	// Snapshot returns a copy of every counter and histogram known to the
	// meter; tags are folded into the metric name by the implementation.
	Snapshot() MetricsSnapshot
}

// MetricsSnapshot is the meter's read-side projection.
type MetricsSnapshot struct {
	Counters   map[string]uint64
	Histograms map[string]HistogramSummary
}

// HistogramSummary is a compact distribution summary suitable for stats
// endpoints. P50/P95/P99 are approximated by the implementation.
type HistogramSummary struct {
	Count uint64
	P50   float64
	P95   float64
	P99   float64
}

// --- Product metrics (CORE substrate ↔ S5 boundary) -------------------------

// ProductMetricsProvider is the CORE-substrate interface that S5 implements
// per ADR-008. CORE owns the contract so the v7 stats endpoint can query
// product metrics without depending on the S5 package; S5 owns the math.
// PolicySinglePrimary applies — S5 is the single legitimate primary.
//
// The canonical S5 product-metric key set lives in ADR-008. CORE never
// references those metric names directly — that is the boundary the T009
// forbidden-vocabulary gate enforces — so this file documents the contract
// shape only.
type ProductMetricsProvider interface {
	// ProductMetrics returns the S5 product-metric snapshot covering the
	// requested window. The canonical metric key set is owned by ADR-008
	// and the S5 feature spec; CORE does not interpret the keys here.
	ProductMetrics(ctx context.Context, window ProductMetricsWindow) (ProductMetricsSnapshot, error)
}

// ProductMetricsWindow specifies a time-range filter for
// ProductMetricsProvider.ProductMetrics. Either bound may be zero: Since
// zero means "no lower bound", Until zero means "up to now".
type ProductMetricsWindow struct {
	Since time.Time
	Until time.Time
}

// ProductMetricReadiness is generic readiness evidence for one product metric.
// CORE owns only the shape; S5 owns the state vocabulary and threshold policy.
type ProductMetricReadiness struct {
	SampleN    uint64
	ThresholdN uint64
	State      string
}

// ProductMetricsSnapshot carries the aggregated product metric values for a
// window. Metrics keys follow the canonical ADR-008 names; SampleN is a
// backward-compatible summary field, while Readiness carries per-metric sample
// evidence for honest no-sample and below-threshold reporting.
type ProductMetricsSnapshot struct {
	Window    ProductMetricsWindow
	Metrics   map[string]float64
	SampleN   uint64
	Readiness map[string]ProductMetricReadiness
}
