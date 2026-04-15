// Package serverevents implements the daemon-side bridge that translates
// engram-server project lifecycle events into [module.ProjectRemovalAware]
// fan-out calls on all registered daemon modules.
//
// # Architecture
//
// The bridge maintains two concurrent paths for event delivery:
//
//  1. Real-time stream (runEventStream): Opens a persistent
//     [EngramService.ProjectEvents] server-streaming gRPC call. On each
//     PROJECT_EVENT_TYPE_REMOVED event the bridge calls
//     registry.ForEachProjectRemovalAware with the affected project ID.
//     If the stream drops (network blip, server restart), the goroutine
//     reconnects with exponential backoff: 1 s → 2 s → 4 s → … → 60 s cap.
//     Under normal conditions the daemon reconnects within 30 s (NFR-3).
//
//  2. Heartbeat (runSyncTicker): Every 60 s ± 5 s jitter the bridge calls
//     [EngramService.SyncProjectState] with the set of project IDs returned
//     by a [ProjectTracker]. In production the tracker is the
//     [dispatcher.Dispatcher], which exposes its live session set via
//     ConnectedProjectIDs() — populated by OnProjectConnect /
//     OnProjectDisconnect callbacks from the muxcore lifecycle. Tests
//     inject a fake tracker to seed arbitrary project sets. Any project ID
//     that the server returns in the "removed" list triggers the same
//     fan-out as path 1. This catches events that may have been emitted while
//     the stream was down (NFR-4, eventually-consistent safety net).
//
// # Deduplication
//
// Events from both paths are deduplicated via an in-memory LRU keyed on
// (event_type, project_id) with capacity 256. A project removal that arrives
// on both the stream and the heartbeat in the same window fires
// OnProjectRemoved exactly once. The dedup state is intentionally not
// persisted — it is lost on bridge restart but the heartbeat re-seeds it
// within one tick.
//
// # Authentication
//
// The bridge reads ENGRAM_SERVER_URL and ENGRAM_API_TOKEN from the daemon
// environment (same env vars as engramcore). The token is injected into every
// outgoing gRPC call via outgoing metadata ("authorization: Bearer <token>").
// Streaming RPCs require the token in the initial call context because the
// server-side unary interceptor does not cover streaming — the bridge handles
// this by attaching metadata in the call context before opening the stream.
//
// # Design references
//
// - spec.md FR-9, FR-10, NFR-3, NFR-4
// - plan.md §"Phase 5 — serverevents bridge"
// - proto/engram/v1/engram.proto — SyncProjectState and ProjectEvents RPCs
// - proto-extensions.md — client_id format and delivery semantics
package serverevents
