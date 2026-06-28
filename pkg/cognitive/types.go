package cognitive

import "time"

// ResolutionPolicy classifies how the SubsystemRegistry should resolve
// multiple implementations registered against the same cross-subsystem
// interface. The policy is per-interface (not per-subsystem) and is
// resolved by SubsystemRegistry.ResolvePolicy at call time.
//
// CandidateProposer uses PolicyFanOut: every enabled impl is invoked, and
// the caller (the S3 ambient handler) fuses the result lists via RRF.
// All other cross-subsystem interfaces use PolicySinglePrimary: the last
// enabled registration wins (HintEmitter applies SinglePrimary per
// HintSurface; the other writers and the distiller have a single
// legitimate owner each).
type ResolutionPolicy string

// Canonical ResolutionPolicy values.
const (
	PolicyFanOut        ResolutionPolicy = "fan_out"
	PolicySinglePrimary ResolutionPolicy = "single_primary"
)

// HintSurface is the delivery channel through which a HintEmitter renders a
// HintProposal back to the agent. Each HintSurface receives exactly one
// primary HintEmitter implementation (PolicySinglePrimary per surface).
type HintSurface string

// Canonical HintSurface values.
//
// HintSurfaceUserPromptSubmit is the primary route resolved by ADR-006
// (OQ8): the plugin UserPromptSubmit hook calls /api/hooks/ambient-candidates
// synchronously within the 200ms budget, then injects the returned
// HintDelivery.AdditionalContext into the next agent context turn.
//
// HintSurfaceMCPPoll is the fallback route: the agent calls a polling MCP
// tool (get_ambient_hints) and receives a HintDelivery whose Hints slice is
// populated. Used for agent-driven mid-session checks not bound to a
// user-prompt event.
const (
	HintSurfaceUserPromptSubmit HintSurface = "user_prompt_submit"
	HintSurfaceMCPPoll          HintSurface = "mcp_poll"
)

// AttentionEvent describes one observable boundary in the agent's runtime
// behaviour that the AttentionEventBus fan-outs to subscribers and that the
// S3 ambient candidate handler may synchronously consult. The eight
// canonical Type values are documented in the AttentionEventBus contract
// (see internal/cognitive/core/event_bus.go): user_prompt_shift,
// assistant_plan, tool_result_surprise, file_touch, segment_shift,
// failed_command, precompact, session_start.
//
// Payload carries type-specific data and is opaque to CORE — subscribers
// downcast based on Type.
type AttentionEvent struct {
	Type      string                 `json:"type"`
	SessionID string                 `json:"session_id"`
	Project   string                 `json:"project"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

// HintProposal is the index-only memory candidate produced by a
// CandidateProposer and surfaced to the agent through a HintEmitter.
// HintProposal carries no full memory content — Title is bounded at
// 80 characters by FR-3 — so that the hint cost is small enough to fit
// within an agent's attention budget. The agent decides whether to expand
// (via recall_memory), store the proposal as state (via set_session_state),
// or ignore it.
type HintProposal struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Tags      []string  `json:"tags,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Score     float32   `json:"score"`
	Source    string    `json:"source"`
	Reason    string    `json:"reason,omitempty"`
}

// HintDelivery is the rendered payload that a HintEmitter returns for a
// specific surface. Surface determines which of the two payload fields is
// populated:
//
//   - HintSurfaceUserPromptSubmit  → AdditionalContext (plain text block)
//   - HintSurfaceMCPPoll           → Hints              (JSON list)
//
// Only the field matching Surface is meaningful; the other is the
// zero value.
type HintDelivery struct {
	Surface           HintSurface    `json:"surface"`
	AdditionalContext string         `json:"additional_context,omitempty"`
	Hints             []HintProposal `json:"hints,omitempty"`
}

// SessionStateSlots is the per-session structured handoff written by S1
// through the StateWriter interface. The three nested maps hold focus,
// execution, and horizons information whose schemas are owned by S1 and
// described in the S1 feature spec — CORE does not validate their shape
// beyond JSON serializability and the NFR-8 32 KB total budget enforced
// at write time.
type SessionStateSlots struct {
	Focus     map[string]interface{} `json:"focus,omitempty"`
	Execution map[string]interface{} `json:"execution,omitempty"`
	Horizons  map[string]interface{} `json:"horizons,omitempty"`
}

// ProjectStateRecord is the cross-session canonical project state written
// through the StateWriter interface. Only the agent writes these records
// (PR-3 invariant). UpdatedBy carries the writer attribution and is
// expected to be "agent" for all legitimate writes; the field is preserved
// in the type so the audit trail survives any future override path.
//
// DeadlineDate is a pointer so the zero value distinguishes "no deadline
// set" from "deadline cleared today".
type ProjectStateRecord struct {
	Phase        string     `json:"phase,omitempty"`
	DeadlineDate *time.Time `json:"deadline_date,omitempty"`
	Pressure     string     `json:"pressure,omitempty"`
	UpdatedBy    string     `json:"updated_by"`
}

// StatePacketSource names the authority that produced a ResumePacket. It is
// deliberately explicit so filesystem fallback can never masquerade as native
// state in the deterministic resume path.
type StatePacketSource string

// Canonical StatePacketSource values.
const (
	StatePacketSourceNative             StatePacketSource = "native"
	StatePacketSourceFilesystemFallback StatePacketSource = "filesystem_fallback"
	StatePacketSourceConflict           StatePacketSource = "conflict"
)

// StateFreshness classifies whether the native packet is current enough to
// drive resume without opening narrative filesystem state.
type StateFreshness string

// Canonical StateFreshness values.
const (
	StateFreshnessFresh   StateFreshness = "fresh"
	StateFreshnessStale   StateFreshness = "stale"
	StateFreshnessUnknown StateFreshness = "unknown"
)

// StateDriftKind classifies the relationship between native state and any
// fallback/export state inspected by the read path.
type StateDriftKind string

// Canonical StateDriftKind values.
const (
	StateDriftNone          StateDriftKind = "none"
	StateDriftNativeStale   StateDriftKind = "native_stale"
	StateDriftFallbackNewer StateDriftKind = "fallback_newer"
	StateDriftConflict      StateDriftKind = "conflict"
	StateDriftUnknown       StateDriftKind = "unknown"
)

// StateScopeKind identifies which agent-owned handoff scope a state record
// describes. The first CR uses session/project state; goal/task scopes are
// contractually reserved so later CRs do not overload generic memory rows.
type StateScopeKind string

// Canonical StateScopeKind values.
const (
	StateScopeSession StateScopeKind = "session"
	StateScopeProject StateScopeKind = "project"
	StateScopeGoal    StateScopeKind = "goal"
	StateScopeTask    StateScopeKind = "task"
)

// StateActionKind classifies how an agent should execute the next action.
type StateActionKind string

// Canonical StateActionKind values.
const (
	StateActionCommand     StateActionKind = "command"
	StateActionInstruction StateActionKind = "instruction"
	StateActionReviewGate  StateActionKind = "review_gate"
)

// StateVerificationKind classifies the evidence gate that must pass before a
// resume can be considered complete.
type StateVerificationKind string

// Canonical StateVerificationKind values.
const (
	StateVerificationCommand  StateVerificationKind = "command"
	StateVerificationArtifact StateVerificationKind = "artifact"
	StateVerificationManual   StateVerificationKind = "manual"
)

// StateAction is the exact next action carried by a ResumePacket.
type StateAction struct {
	Kind        StateActionKind `json:"kind"`
	Description string          `json:"description"`
	Command     string          `json:"command,omitempty"`
}

// StateVerification is the exact evidence gate carried by a ResumePacket.
type StateVerification struct {
	Kind        StateVerificationKind `json:"kind"`
	Description string                `json:"description"`
	Command     string                `json:"command,omitempty"`
}

// StateConflict records a single native-vs-fallback disagreement without
// forcing the reader to open the larger fallback artifact.
type StateConflict struct {
	Field         string `json:"field"`
	NativeValue   string `json:"native_value,omitempty"`
	FallbackValue string `json:"fallback_value,omitempty"`
	Resolution    string `json:"resolution,omitempty"`
}

// StateDrift summarizes drift/conflict evidence for a ResumePacket.
type StateDrift struct {
	Kind      StateDriftKind  `json:"kind"`
	Conflicts []StateConflict `json:"conflicts,omitempty"`
	CheckedAt time.Time       `json:"checked_at,omitempty"`
}

// ResumePacketRequest scopes a native resume read. AllowFilesystemFallback is
// explicit: a caller that wants native-only behavior leaves it false and gets a
// non-native packet only when the implementation can prove an intentional
// conflict path.
type ResumePacketRequest struct {
	Project                 string `json:"project"`
	SessionID               string `json:"session_id,omitempty"`
	GoalID                  string `json:"goal_id,omitempty"`
	TaskID                  string `json:"task_id,omitempty"`
	AllowFilesystemFallback bool   `json:"allow_filesystem_fallback,omitempty"`
}

// ResumePacket is the bounded deterministic resume payload for the native
// state plane. The required fields are intentionally concrete: freshness,
// drift/conflict, next action, and next verification are first-class fields,
// not opaque metadata inside SessionStateSlots.
type ResumePacket struct {
	Source           StatePacketSource `json:"source"`
	Freshness        StateFreshness    `json:"freshness"`
	Drift            StateDrift        `json:"drift"`
	NextAction       StateAction       `json:"next_action"`
	NextVerification StateVerification `json:"next_verification"`
	GeneratedAt      time.Time         `json:"generated_at"`

	Project      string           `json:"project,omitempty"`
	SessionID    string           `json:"session_id,omitempty"`
	GoalID       string           `json:"goal_id,omitempty"`
	TaskID       string           `json:"task_id,omitempty"`
	FallbackPath string           `json:"fallback_path,omitempty"`
	Scopes       []StateScopeKind `json:"scopes,omitempty"`
}

// AttentionEventRecord is the durable form of an AttentionEvent persisted
// by S4a through the AttentionEventWriter interface. Unlike AttentionEvent
// (which describes a runtime signal), AttentionEventRecord captures a
// distilled directive: agent-confirmed intent derived from a user prompt,
// with the source-turn hash preserved for provenance, a horizon hint that
// bounds retrieval, and a privacy class that gates cross-session surfacing.
type AttentionEventRecord struct {
	Project        string `json:"project"`
	SessionID      string `json:"session_id"`
	SourceTurnHash string `json:"source_turn_hash"`
	DerivedIntent  string `json:"derived_intent"`
	AgentConfirmed bool   `json:"agent_confirmed"`
	Horizon        string `json:"horizon"`
	PrivacyClass   string `json:"privacy_class"`
}

// RawSignal is the input to a DirectiveDistiller: text plus a hash that
// identifies the source turn that produced it, plus optional contextual
// key/value pairs that downstream distillers may inspect. Distillation
// transforms RawSignal into a Distilled directive.
type RawSignal struct {
	Text       string            `json:"text"`
	SourceHash string            `json:"source_hash"`
	Context    map[string]string `json:"context,omitempty"`
}

// Distilled is the structured output of a DirectiveDistiller. The shape
// mirrors the durable form (AttentionEventRecord) but does not include
// project/session identifiers — those are added by S4a at write time —
// nor agent confirmation, which happens between distillation and write.
//
// Confidence is the distiller's own belief in the derived Intent; S4a may
// gate AttentionEventRecord persistence on a confidence threshold.
type Distilled struct {
	Intent     string  `json:"intent"`
	Horizon    string  `json:"horizon"`
	Privacy    string  `json:"privacy"`
	Confidence float32 `json:"confidence"`
}
