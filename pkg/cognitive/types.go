package cognitive

import (
	"encoding/json"
	"errors"
	"time"
)

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

// MaxSessionStatePayloadBytes is the NFR-8 upper bound for the JSON-encoded
// SessionStateSlots payload written through S1.
const MaxSessionStatePayloadBytes = 32 * 1024

// ErrSessionStatePayloadTooLarge reports that the JSON-encoded SessionStateSlots
// payload exceeds the cross-layer 32 KB write budget.
var ErrSessionStatePayloadTooLarge = errors.New("session state exceeds 32 KB budget")

// ValidateSessionStateSlotsBudget marshals state and enforces the shared 32 KB
// payload budget used by every native S1 write path.
func ValidateSessionStateSlotsBudget(state SessionStateSlots) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(payload) > MaxSessionStatePayloadBytes {
		return ErrSessionStatePayloadTooLarge
	}
	return nil
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

// Canonical StatePacketSource values. CR-006 contract-facing packets use
// native, filesystem_fallback, imported, or mixed. Conflicts are represented
// by Drift.Kind; StatePacketSourceConflict is retained only as a legacy
// compatibility value for older callers that still encoded conflict in Source.
const (
	StatePacketSourceNative             StatePacketSource = "native"
	StatePacketSourceFilesystemFallback StatePacketSource = "filesystem_fallback"
	StatePacketSourceImported           StatePacketSource = "imported"
	StatePacketSourceMixed              StatePacketSource = "mixed"
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

// StateScopeKind identifies which agent-owned handoff scope a state record or
// resume request describes. Session, project, goal, and task are explicit so
// native state cannot collapse scoped handoff data into generic memory rows.
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

// StateAction is the exact next action carried by a ResumePacket; Kind and
// Description are required.
type StateAction struct {
	Kind        StateActionKind `json:"kind"`
	Description string          `json:"description"`
	Command     string          `json:"command,omitempty"`
}

// StateVerification is the exact evidence gate carried by a ResumePacket; Kind
// and Description are required.
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

// StateDrift summarizes drift/conflict evidence for a ResumePacket. Conflicts
// is required and empty when no conflict was found.
type StateDrift struct {
	Kind      StateDriftKind  `json:"kind"`
	Conflicts []StateConflict `json:"conflicts"`
	CheckedAt time.Time       `json:"checked_at,omitempty"`
}

// ResumePacketRequest scopes a native resume read. Principal binds the read to
// the caller identity, Scopes names the handoff scope(s) being requested, and
// AllowFilesystemFallback remains explicit so fallback state can never be
// reported as native state by accident.
type ResumePacketRequest struct {
	Project                 string           `json:"project"`
	Principal               string           `json:"principal"`
	SessionID               string           `json:"session_id,omitempty"`
	GoalID                  string           `json:"goal_id,omitempty"`
	TaskID                  string           `json:"task_id,omitempty"`
	Scopes                  []StateScopeKind `json:"scopes"`
	AllowFilesystemFallback bool             `json:"allow_filesystem_fallback,omitempty"`
}

// ResumePacket is the bounded deterministic resume payload for the native
// state plane. The required fields are intentionally concrete: packet identity,
// principal/session scope, explicit state scopes, state version, source/fallback
// authority, freshness, drift/conflict, next action, next verification, and
// evidence references are first-class fields, not opaque metadata.
type ResumePacket struct {
	PacketID         string            `json:"packet_id"`
	Project          string            `json:"project"`
	Principal        string            `json:"principal"`
	SessionID        string            `json:"session_id"`
	StateVersion     string            `json:"state_version"`
	Source           StatePacketSource `json:"source"`
	FallbackUsed     bool              `json:"fallback_used"`
	Freshness        StateFreshness    `json:"freshness"`
	Drift            StateDrift        `json:"drift"`
	NextAction       StateAction       `json:"next_action"`
	NextVerification StateVerification `json:"next_verification"`
	GeneratedAt      time.Time         `json:"generated_at"`
	EvidenceRefs     []string          `json:"evidence_refs"`

	GoalID       string           `json:"goal_id,omitempty"`
	TaskID       string           `json:"task_id,omitempty"`
	FallbackPath string           `json:"fallback_path,omitempty"`
	Scopes       []StateScopeKind `json:"scopes"`
}

// ExperienceSource names the implementation shape that produced an experience
// response. CR-002 V1 starts with projection/materialization evidence and does
// not assume dedicated ExperienceRecord storage.
type ExperienceSource string

// Canonical ExperienceSource values.
const (
	ExperienceSourceProjection   ExperienceSource = "projection"
	ExperienceSourceMaterialized ExperienceSource = "materialized"
	ExperienceSourceDedicated    ExperienceSource = "dedicated"
)

// ExperienceApplicabilityState is the V1 applicability gate verdict. Blocked
// means the experience must not be silently reused for the current context.
type ExperienceApplicabilityState string

// Canonical ExperienceApplicabilityState values.
const (
	ExperienceApplicabilityApplies   ExperienceApplicabilityState = "applies"
	ExperienceApplicabilityUncertain ExperienceApplicabilityState = "uncertain"
	ExperienceApplicabilityBlocked   ExperienceApplicabilityState = "blocked"
)

// ExperienceArchiveTriggerClass identifies the finite trigger classes allowed
// to resurface archived experience. Ordinary hot-path requests leave this list
// empty so archive search cannot run implicitly.
type ExperienceArchiveTriggerClass string

// Canonical ExperienceArchiveTriggerClass values.
const (
	ExperienceArchiveTriggerHistoricalWhy        ExperienceArchiveTriggerClass = "historical_why"
	ExperienceArchiveTriggerRegressionOrRollback ExperienceArchiveTriggerClass = "regression_or_rollback"
	ExperienceArchiveTriggerRevisitOldDecision   ExperienceArchiveTriggerClass = "revisit_old_decision"
	ExperienceArchiveTriggerSimilarPriorFailure  ExperienceArchiveTriggerClass = "similar_prior_failure"
	ExperienceArchiveTriggerTemporalTruthChange  ExperienceArchiveTriggerClass = "temporal_truth_change"
	ExperienceArchiveTriggerExplicitLookup       ExperienceArchiveTriggerClass = "explicit_archive_lookup"
)

// ExperienceTimeSpan names the historical interval covered by an experience.
// Zero values mean the projection source did not expose a bound for that side.
type ExperienceTimeSpan struct {
	StartedAt time.Time `json:"started_at,omitempty,omitzero"`
	EndedAt   time.Time `json:"ended_at,omitempty,omitzero"`
}

// ExperienceApplicability carries the gate state and rationale for a returned
// experience. The envelope fields make applicability and anti-applicability
// explicit for read adapters so callers do not infer safe reuse from relevance.
type ExperienceApplicability struct {
	State            ExperienceApplicabilityState `json:"state"`
	Rationale        string                       `json:"rationale"`
	AppliesWhen      []string                     `json:"applies_when,omitempty"`
	DoesNotApplyWhen []string                     `json:"does_not_apply_when,omitempty"`
	RequiredContext  []string                     `json:"required_context,omitempty"`
	Confidence       string                       `json:"confidence,omitempty"`
	BlockReason      string                       `json:"block_reason,omitempty"`
	OverrideEvidence string                       `json:"override_evidence,omitempty"`
}

// ExperienceAntiApplicability records a condition under which a prior lesson
// should be downgraded or blocked for the current context.
type ExperienceAntiApplicability struct {
	Condition string `json:"condition"`
	Rationale string `json:"rationale"`
}

// ExperienceSourceAttribution identifies the evidence used to produce an
// experience response without forcing V1 to introduce dedicated storage.
type ExperienceSourceAttribution struct {
	Kind      string    `json:"kind"`
	ID        string    `json:"id"`
	Project   string    `json:"project,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// ExperienceQueryRequest scopes an explicit experience/history read. It is
// separate from hot-memory retrieval so causal lessons do not collapse into
// ordinary memory ranking.
type ExperienceQueryRequest struct {
	Project               string                          `json:"project"`
	Principal             string                          `json:"principal,omitempty"`
	Domain                string                          `json:"domain,omitempty"`
	Query                 string                          `json:"query"`
	CurrentContext        string                          `json:"current_context,omitempty"`
	Situation             string                          `json:"situation,omitempty"`
	TimeSpan              ExperienceTimeSpan              `json:"time_span,omitempty"`
	Decision              string                          `json:"decision,omitempty"`
	Action                string                          `json:"action,omitempty"`
	Outcome               string                          `json:"outcome,omitempty"`
	Revision              string                          `json:"revision,omitempty"`
	Reversal              string                          `json:"reversal,omitempty"`
	StorageOrigin         ExperienceSource                `json:"storage_origin,omitempty"`
	ArchiveTriggerClasses []ExperienceArchiveTriggerClass `json:"archive_trigger_classes,omitempty"`
	Limit                 int                             `json:"limit,omitempty"`
}

// ExperienceResponse is the bounded first-class payload for historical
// experience retrieval. It carries situation, decision/action, outcome,
// revision/reversal, lesson, applicability, anti-applicability, provenance,
// and storage origin before any caller may reuse the lesson.
type ExperienceResponse struct {
	Source                ExperienceSource                `json:"source"`
	StorageOrigin         ExperienceSource                `json:"storage_origin"`
	Situation             string                          `json:"situation"`
	TimeSpan              ExperienceTimeSpan              `json:"time_span"`
	Decision              string                          `json:"decision"`
	Action                string                          `json:"action"`
	Outcome               string                          `json:"outcome"`
	Revision              string                          `json:"revision"`
	Reversal              string                          `json:"reversal"`
	Lesson                string                          `json:"lesson"`
	Applicability         ExperienceApplicability         `json:"applicability"`
	AntiApplicability     []ExperienceAntiApplicability   `json:"anti_applicability"`
	Provenance            []ExperienceSourceAttribution   `json:"provenance"`
	SourceAttribution     []ExperienceSourceAttribution   `json:"source_attribution"`
	ArchiveTriggerClasses []ExperienceArchiveTriggerClass `json:"archive_trigger_classes"`
}

// ForgettingOperation names the explicit operation class for safe forgetting.
// It deliberately avoids a boolean delete shape so callers must choose the
// policy boundary and audit surface that match the memory-quality action.
type ForgettingOperation string

// Canonical ForgettingOperation values.
const (
	ForgettingOperationSuppress    ForgettingOperation = "suppress"
	ForgettingOperationExpire      ForgettingOperation = "expire"
	ForgettingOperationArchive     ForgettingOperation = "archive"
	ForgettingOperationConsolidate ForgettingOperation = "consolidate"
	ForgettingOperationDestroy     ForgettingOperation = "destroy"
)

// ForgettingReason is the classifier input signal that maps worked cases onto
// the taxonomy. It is request intent, not permission to mutate storage.
type ForgettingReason string

// Canonical ForgettingReason values.
const (
	ForgettingReasonLowValue         ForgettingReason = "low_value"
	ForgettingReasonRetentionExpired ForgettingReason = "retention_expired"
	ForgettingReasonColdStorage      ForgettingReason = "cold_storage"
	ForgettingReasonDuplicate        ForgettingReason = "duplicate"
	ForgettingReasonOperatorDestroy  ForgettingReason = "operator_destroy"
)

// ForgettingDecisionState tells the caller whether a classified action is safe
// to resolve automatically, must enter review, or is blocked.
type ForgettingDecisionState string

// Canonical ForgettingDecisionState values.
const (
	ForgettingDecisionAutoResolvable ForgettingDecisionState = "auto_resolvable"
	ForgettingDecisionReviewRequired ForgettingDecisionState = "review_required"
	ForgettingDecisionBlocked        ForgettingDecisionState = "blocked"
)

// ForgettingClassificationRequest carries enough context to classify a
// forgetting/consolidation action without mutating the underlying memory rows.
type ForgettingClassificationRequest struct {
	Reason         ForgettingReason         `json:"reason"`
	MemoryID       string                   `json:"memory_id"`
	RelatedIDs     []string                 `json:"related_ids,omitempty"`
	Evidence       []string                 `json:"evidence,omitempty"`
	Project        string                   `json:"project,omitempty"`
	PrivacyScope   string                   `json:"privacy_scope,omitempty"`
	PolicyOwner    string                   `json:"policy_owner,omitempty"`
	StructuralLoss ForgettingStructuralLoss `json:"structural_loss"`
	Risky          bool                     `json:"risky,omitempty"`
}

// ForgettingAuditSurface names the evidence required before or after a
// forgetting action. SnapshotStore and AuditStore align with existing
// candidate/snapshot/audit seams; ExportPath is populated by closeout proof.
type ForgettingAuditSurface struct {
	Required      bool     `json:"required"`
	SnapshotStore string   `json:"snapshot_store"`
	AuditStore    string   `json:"audit_store"`
	ExportPath    string   `json:"export_path"`
	Evidence      []string `json:"evidence"`
}

// ForgettingReviewPolicy tells the caller whether a bounded operator packet is
// required and which actions may be presented.
type ForgettingReviewPolicy struct {
	Required       bool                   `json:"required"`
	PacketKind     string                 `json:"packet_kind"`
	AllowedActions []string               `json:"allowed_actions"`
	Packet         ForgettingReviewPacket `json:"packet"`
}

// ForgettingStructuralLoss records whether a consolidation/destructive path
// would lose meaning, provenance, scope, or historical value. The guard is
// intentionally broader than similarity so policy cannot silently merge away
// unique context.
type ForgettingStructuralLoss struct {
	UniqueMeaning   bool   `json:"unique_meaning"`
	Provenance      bool   `json:"provenance"`
	Scope           bool   `json:"scope"`
	HistoricalValue bool   `json:"historical_value"`
	Rationale       string `json:"rationale"`
}

// ForgettingPacketScope bounds the memory ids and scope carried by a review
// packet so the operator reviews a compact exception, not raw row sludge.
type ForgettingPacketScope struct {
	Project      string   `json:"project,omitempty"`
	PrivacyScope string   `json:"privacy_scope,omitempty"`
	MemoryIDs    []string `json:"memory_ids"`
}

// ForgettingSnapshotPolicy mirrors the existing snapshot seam required before
// risky forgetting actions execute.
type ForgettingSnapshotPolicy struct {
	Store     string `json:"store"`
	Operation string `json:"operation"`
	Status    string `json:"status"`
	Required  bool   `json:"required"`
}

// ForgettingAuditPolicy mirrors the existing audit seam for risky forgetting
// review packets.
type ForgettingAuditPolicy struct {
	Store  string `json:"store"`
	Action string `json:"action"`
	Status string `json:"status"`
}

// ForgettingPacketPreview names the read-only before/after recommendation that
// a packet exposes before any mutation path is allowed to run.
type ForgettingPacketPreview struct {
	BeforeRefs        []string `json:"before_refs"`
	AfterPlan         string   `json:"after_plan"`
	Recommendation    string   `json:"recommendation"`
	Action            string   `json:"action"`
	ApprovalRequired  bool     `json:"approval_required"`
	MutationSeparated bool     `json:"mutation_separated"`
}

// ForgettingMutationRequirements defines the hard gates a mutation path must
// satisfy after a reviewed packet is approved.
type ForgettingMutationRequirements struct {
	StructuralLossCheckRequired bool `json:"structural_loss_check_required"`
	PrivacyScopeRequired        bool `json:"privacy_scope_required"`
	AuditWriteBeforeMutation    bool `json:"audit_write_before_mutation"`
	SnapshotRequired            bool `json:"snapshot_required"`
	ReviewApprovalRequired      bool `json:"review_approval_required"`
}

// ForgettingReviewPacket is the bounded exception payload emitted for risky or
// destructive forgetting/consolidation decisions.
type ForgettingReviewPacket struct {
	PacketID             string                         `json:"packet_id"`
	Kind                 string                         `json:"kind"`
	Operation            ForgettingOperation            `json:"operation"`
	State                ForgettingDecisionState        `json:"state"`
	Rationale            string                         `json:"rationale"`
	AllowedActions       []string                       `json:"allowed_actions"`
	PolicyOwner          string                         `json:"policy_owner"`
	Scope                ForgettingPacketScope          `json:"scope"`
	Evidence             []string                       `json:"evidence"`
	Preview              ForgettingPacketPreview        `json:"preview"`
	Snapshot             ForgettingSnapshotPolicy       `json:"snapshot"`
	Audit                ForgettingAuditPolicy          `json:"audit"`
	MutationRequirements ForgettingMutationRequirements `json:"mutation_requirements"`
	StructuralLoss       ForgettingStructuralLoss       `json:"structural_loss"`
	ReadOnly             bool                           `json:"read_only"`
}

// ForgettingActionPath identifies whether evidence came from automatic policy
// routing or an approved review packet.
type ForgettingActionPath string

const (
	ForgettingActionPathAutomatic ForgettingActionPath = "automatic_policy"
	ForgettingActionPathReviewed  ForgettingActionPath = "reviewed_packet"
)

// ForgettingActionResult names the lifecycle result captured by export proof.
type ForgettingActionResult string

const (
	ForgettingActionResultClassified ForgettingActionResult = "classified"
	ForgettingActionResultPreviewed  ForgettingActionResult = "previewed"
	ForgettingActionResultApplied    ForgettingActionResult = "applied"
	ForgettingActionResultBlocked    ForgettingActionResult = "blocked"
)

// ForgettingAuditExportProof is the self-describing export/readback payload for
// automatic and reviewed forgetting actions.
type ForgettingAuditExportProof struct {
	Operation                ForgettingOperation      `json:"operation"`
	Action                   string                   `json:"action"`
	State                    ForgettingDecisionState  `json:"state"`
	Path                     ForgettingActionPath     `json:"path"`
	Actor                    string                   `json:"actor"`
	Result                   ForgettingActionResult   `json:"result"`
	PolicyOwner              string                   `json:"policy_owner"`
	PolicyBoundary           string                   `json:"policy_boundary"`
	PacketID                 string                   `json:"packet_id,omitempty"`
	SnapshotID               string                   `json:"snapshot_id,omitempty"`
	AuditAction              string                   `json:"audit_action"`
	AuditRef                 string                   `json:"audit_ref,omitempty"`
	ExportRef                string                   `json:"export_ref,omitempty"`
	Evidence                 []string                 `json:"evidence"`
	DataDestructionByDefault bool                     `json:"data_destruction_by_default"`
	StructuralLoss           ForgettingStructuralLoss `json:"structural_loss"`
}

// ForgettingDecision is the bounded classifier output. It is a decision
// envelope only: DataDestructionByDefault must remain false unless a later,
// audited execution path explicitly performs an approved destructive action.
type ForgettingDecision struct {
	Operation                ForgettingOperation      `json:"operation"`
	State                    ForgettingDecisionState  `json:"state"`
	Rationale                string                   `json:"rationale"`
	PolicyOwner              string                   `json:"policy_owner"`
	PolicyBoundary           string                   `json:"policy_boundary"`
	ArchiveFirst             bool                     `json:"archive_first"`
	Audit                    ForgettingAuditSurface   `json:"audit"`
	Review                   ForgettingReviewPolicy   `json:"review"`
	StructuralLoss           ForgettingStructuralLoss `json:"structural_loss"`
	DataDestructionByDefault bool                     `json:"data_destruction_by_default"`
}

// TemporalTruthQueryState names the bounded read outcome for selected-fact
// temporal truth. NotSelected is an explicit non-graph answer: the provider
// refuses to infer truth for facts outside the selected scope.
type TemporalTruthQueryState string

// Canonical TemporalTruthQueryState values.
const (
	TemporalTruthFound       TemporalTruthQueryState = "found"
	TemporalTruthNotSelected TemporalTruthQueryState = "not_selected"
	TemporalTruthUnknown     TemporalTruthQueryState = "unknown"
)

// TemporalTruthScope identifies the selected high-value fact being queried.
type TemporalTruthScope struct {
	FactID    string `json:"fact_id"`
	FactClass string `json:"fact_class,omitempty"`
	Project   string `json:"project,omitempty"`
	Selected  bool   `json:"selected"`
	Rationale string `json:"rationale,omitempty"`
}

// TemporalTruthProvenance identifies the evidence behind a current or prior
// truth answer.
type TemporalTruthProvenance struct {
	Kind       string    `json:"kind"`
	ID         string    `json:"id"`
	Project    string    `json:"project,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}

// TemporalTruthEntry is one value for a selected evolving fact, including its
// validity window, invalidation rationale, and provenance.
type TemporalTruthEntry struct {
	Value                 string                    `json:"value"`
	ValidFrom             time.Time                 `json:"valid_from"`
	ValidUntil            *time.Time                `json:"valid_until"`
	InvalidatedAt         *time.Time                `json:"invalidated_at"`
	InvalidationRationale string                    `json:"invalidation_rationale"`
	Provenance            []TemporalTruthProvenance `json:"provenance"`
}

// TemporalTruthQueryRequest scopes a selected-fact temporal truth read.
type TemporalTruthQueryRequest struct {
	FactID    string     `json:"fact_id"`
	FactClass string     `json:"fact_class,omitempty"`
	Project   string     `json:"project,omitempty"`
	AsOf      *time.Time `json:"as_of,omitempty"`
	Limit     int        `json:"limit,omitempty"`
}

// TemporalTruthResponse answers true-now and optionally true-then for one
// selected evolving fact, with bounded history and provenance.
type TemporalTruthResponse struct {
	Scope           TemporalTruthScope        `json:"scope"`
	State           TemporalTruthQueryState   `json:"state"`
	TrueNow         *TemporalTruthEntry       `json:"true_now,omitempty"`
	TrueThen        *TemporalTruthEntry       `json:"true_then"`
	History         []TemporalTruthEntry      `json:"history"`
	ProvenanceChain []TemporalTruthProvenance `json:"provenance_chain"`
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
