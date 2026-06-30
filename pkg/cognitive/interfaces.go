package cognitive

import "context"

// AttentionEventSource is the declarative marker that a subsystem produces
// AttentionEvent values onto the in-process AttentionEventBus. Concrete
// implementations are registered with the SubsystemRegistry; the returned
// list of event Type values lets CORE validate that subscribers do not
// reference event types no source declares. Implementations do not push
// events through this interface — they push through the
// AttentionEventBus.Publish handle injected at Start time — so this
// interface is shape-only.
type AttentionEventSource interface {
	// EventsProduced returns the canonical AttentionEvent.Type strings this
	// source emits. The list is read once at Start time for validation.
	EventsProduced() []string
}

// CandidateProposer is the cross-subsystem proposal surface used by the S3
// ambient candidate handler. S2 (meta-memory), S6 (outcome policy), and S4b
// (creator directive surfacing) implement it; the SubsystemRegistry resolves
// CandidateProposer with PolicyFanOut, so all enabled implementations are
// invoked in parallel and their result lists fused by the caller via
// reciprocal rank fusion. Implementations must respect the limit parameter
// (it bounds output size, not just truncates returned slices) and must
// observe the caller-imposed deadline carried by ctx — the ambient
// candidate path runs inside a 200ms budget per NFR-2.
type CandidateProposer interface {
	// Propose returns up to limit HintProposal values keyed off the supplied
	// AttentionEvent. Implementations should return a typed error on context
	// cancellation rather than a partial slice plus nil.
	Propose(ctx context.Context, event AttentionEvent, limit int) ([]HintProposal, error)
}

// HintEmitter is the cross-subsystem rendering surface implemented by S3.
// The SubsystemRegistry resolves HintEmitter with PolicySinglePrimary per
// HintSurface, so each surface (UserPromptSubmit, MCPPoll) has exactly one
// primary emitter. Render owns the per-surface format contract documented
// on HintDelivery: text-block for UserPromptSubmit, structured list for
// MCPPoll. Hints is supplied pre-drained from the HintQueue by the caller
// so HintEmitter is pure rendering, not queue management.
type HintEmitter interface {
	// Render produces the HintDelivery payload for the requested surface.
	// The implementation must populate the surface-matched field on
	// HintDelivery and leave the other field zero-valued.
	Render(ctx context.Context, surface HintSurface, sessionID string, hints []HintProposal) (HintDelivery, error)
}

// StateWriter is the agent-owned state surface implemented by S1. Per ADR-010
// the interface is intentionally split from AttentionEventWriter so S1's
// implementation does not carry an attention-event method it has no
// business owning, and S4a's implementation does not carry state methods
// it has no business owning. Per PR-3 only the agent writes state — CORE
// does not invoke StateWriter; the methods are invoked from MCP tool
// handlers that the agent calls.
type StateWriter interface {
	// WriteSessionState replaces the SessionStateSlots for sessionID. NFR-8
	// limits the serialized JSON payload to 32 KB; enforcement lives in the
	// implementation, not in the interface.
	WriteSessionState(ctx context.Context, sessionID string, slots SessionStateSlots) error

	// WriteProjectState upserts the ProjectStateRecord for project. The
	// UpdatedBy field on the record is expected to be "agent" per PR-3; the
	// implementation may reject other writers.
	WriteProjectState(ctx context.Context, project string, state ProjectStateRecord) error
}

// StatePlane is the agent-owned read/write surface for native handoff state.
// StateWriter remains the narrower write-only subsystem contract used by CORE;
// StatePlane is the product-facing state-plane contract introduced by ENG-MPL-1
// so MCP/agent paths can read bounded resume packets without relying on
// filesystem archaeology.
type StatePlane interface {
	StateWriter

	// ReadSessionState returns the native session slots for sessionID.
	ReadSessionState(ctx context.Context, sessionID string) (SessionStateSlots, error)

	// ReadProjectState returns the native project record for project.
	ReadProjectState(ctx context.Context, project string) (ProjectStateRecord, error)

	// ReadResumePacket returns the bounded resume payload for request. The
	// implementation owns native-first and explicit-fallback semantics.
	ReadResumePacket(ctx context.Context, request ResumePacketRequest) (ResumePacket, error)
}

// ExperienceProvider is the product-facing read surface for first-class
// experience retrieval. It is intentionally separate from hot-memory retrieval:
// callers receive bounded historical lessons with applicability and
// anti-applicability evidence before reuse.
type ExperienceProvider interface {
	// QueryExperience returns bounded historical/causal lessons for request.
	QueryExperience(ctx context.Context, request ExperienceQueryRequest) ([]ExperienceResponse, error)
}

// ForgettingClassifier is the product-facing classification surface for safe
// forgetting/consolidation. It returns a bounded decision envelope and must not
// mutate memory storage as part of classification.
type ForgettingClassifier interface {
	// ClassifyForgetting maps a request onto the explicit forgetting taxonomy.
	ClassifyForgetting(ctx context.Context, request ForgettingClassificationRequest) (ForgettingDecision, error)
}

// TemporalTruthProvider is the product-facing read surface for selected-fact
// temporal truth. Implementations must stay bounded to selected facts and
// return provenance with current and prior truth answers.
type TemporalTruthProvider interface {
	// QueryTemporalTruth returns true-now and prior validity context for one selected fact.
	QueryTemporalTruth(ctx context.Context, request TemporalTruthQueryRequest) (TemporalTruthResponse, error)
}

// AttentionEventWriter is the agent-owned directive-capture surface
// implemented by S4a. The single-method shape mirrors the single
// responsibility per ADR-010: persist an AttentionEventRecord derived from
// a distilled directive. As with StateWriter, CORE does not invoke
// AttentionEventWriter; the method is invoked from an MCP tool handler that
// the agent calls.
type AttentionEventWriter interface {
	// WriteAttentionEvent persists event durably. Implementations may apply
	// privacy and horizon policies before write.
	WriteAttentionEvent(ctx context.Context, event AttentionEventRecord) error
}

// DirectiveDistiller is the cross-subsystem distillation surface implemented
// by S4a. SubsystemRegistry resolves DirectiveDistiller with
// PolicySinglePrimary — S4a is the single legitimate owner. Distill
// transforms a RawSignal (typically text plus source-turn metadata) into a
// Distilled directive whose Confidence field downstream callers may gate
// AttentionEventWriter persistence on.
type DirectiveDistiller interface {
	// Distill returns the structured directive derived from rawSignal. A
	// non-nil error indicates distillation failed; callers should not
	// persist the returned Distilled value when err != nil.
	Distill(ctx context.Context, rawSignal RawSignal) (Distilled, error)
}
