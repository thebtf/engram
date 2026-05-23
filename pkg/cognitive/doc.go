// Package cognitive defines the public plug-interface surface and shared
// payload types of the Engram v7.0 Plug Platform. Downstream cognitive
// subsystems import this package to receive the canonical interfaces they
// implement and the data types they exchange through CORE.
//
// # Public / internal split
//
// pkg/cognitive (this package) exports exactly six cross-subsystem
// interfaces, all consumed via dependency injection at registration time:
//
//   - AttentionEventSource — subsystems declaring event types they emit
//   - CandidateProposer    — synchronously invoked by S3 ambient handler
//   - HintEmitter          — surface delivery (UserPromptSubmit / MCPPoll)
//   - StateWriter          — S1 owns session_state + project_state writes
//   - AttentionEventWriter — S4a owns attention_events writes
//   - DirectiveDistiller   — S4a transforms RawSignal into Distilled directive
//
// CORE-internal substrate (registry, event bus, hint queue, meter and the
// CORE-only DTOs) lives in internal/cognitive/core/interfaces.go. Downstream
// subsystems never import internal/cognitive/core directly; they receive
// concrete handles through the Dependencies struct passed at Subsystem.Start
// time. This split is a boundary invariant — leakage of CORE-internal types
// into pkg/cognitive is a release blocker.
//
// # Subsystem naming
//
// Every Subsystem registers under a name following the convention
// "{owner-prefix}.{component-name}". The owner-prefix MUST be one of the
// eight reserved prefixes mapped to the v7-plug milestone roster:
//
//   - core  — CORE plug platform itself (e.g. core.noop.candidate_proposer)
//   - s1    — Engram v7 State subsystem
//   - s2    — Engram v7 Meta-memory subsystem
//   - s3    — Engram v7 Ambient Attention subsystem
//   - s4a   — Engram v7 Creator Directive Capture subsystem
//   - s4b   — Engram v7 Creator Directive Surfacing subsystem
//   - s5    — Engram v7 Telemetry product-metrics subsystem
//   - s6    — Engram v7 Outcome Policy subsystem
//
// The component-name is the subsystem author's choice (lowercase identifier,
// dots permitted for sub-component disambiguation, no spaces). Examples:
// "s2.metaindex", "s3.ambient.user_prompt_route", "core.noop.hint_emitter".
//
// # Registration lifecycle
//
// Subsystems are registered at server startup, before the first request is
// accepted. The CORE SubsystemRegistry hands each registered subsystem a
// Dependencies value carrying the registry, event bus, hint queue, meter, db,
// and logger handles via dependency injection — subsystems never reach into
// CORE internals at package level. Mid-process registration is not supported
// in v7.0; flag toggling requires a server restart.
//
// # Logging field schema versioning
//
// Subsystems that emit structured log entries via the meter (per NFR-6) MUST
// version their field schema with the subsystem's own semantic version:
//
//   - Adding a new field         → minor bump (vX.(Y+1).0)
//   - Removing or renaming field → major bump (v(X+1).0.0)
//   - Changing a field's type    → major bump
//
// Stats consumers (the eventual S5 product-metrics dashboard) pin against a
// subsystem version and expect this discipline.
//
// # Reading the rest of the package
//
//   - types.go        — cross-subsystem payload types (AttentionEvent,
//     HintProposal, ResolutionPolicy, HintSurface,
//     HintDelivery, SessionStateSlots, ProjectStateRecord,
//     AttentionEventRecord, RawSignal, Distilled)
//   - interfaces.go   — the six cross-subsystem interfaces listed above
//   - normalize.go    — NormalizeForDiff + VolatileFields + MemorySortKey
//     used by the FR-9 byte-identity gate
//
// CORE-internal interfaces live alongside this package in
// internal/cognitive/core/interfaces.go and are intentionally not re-exported
// here.
package cognitive
