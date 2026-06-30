# State Plane Contract

Feature: ENG-MPL-1
CR: CR-006-native-state-plane
Task: T001
Status: executable-contract

## Purpose

The state plane is Engram-owned handoff state for session, project, goal, and task resume. Its first executable contract is the bounded `ResumePacket`: a deterministic payload that tells an agent which state source was used, whether fallback contributed, how fresh the state is, where drift/conflict exists, what exact action comes next, and what exact verification proves continuation before any filesystem fallback artifact is opened.

## Scope In T001

T001 defines the packet and interface contract only:

- `pkg/cognitive.ResumePacket`
- `pkg/cognitive.ResumePacketRequest`
- `pkg/cognitive.StatePacketSource`
- `pkg/cognitive.StateFreshness`
- `pkg/cognitive.StateDriftKind`
- `pkg/cognitive.StateScopeKind`
- `pkg/cognitive.StateActionKind`
- `pkg/cognitive.StateVerificationKind`
- `pkg/cognitive.StatePlane`

Persistence internals, REST/MCP adapters, native-first lookup behavior, audit wiring, and filesystem fallback reads are implemented by later CR-006 tasks. T001 only makes those later tasks unambiguous.

## Packet Authority

`ResumePacket.Source` is mandatory and must be one of:

| Source | Meaning |
| --- | --- |
| `native` | Packet came from Engram-native state and may drive the normal resume path. |
| `filesystem_fallback` | Packet came from explicit fallback/export state. This must never be reported as native state. |
| `imported` | Packet came from an explicitly imported/exported state payload rather than a live native read. |
| `mixed` | Native and fallback/export state were both inspected; the packet includes drift/conflict evidence. |
| `conflict` | Legacy compatibility value only; new conflict packets use `source=mixed` with `drift.kind=conflict`. |

`ResumePacket.FallbackUsed` is mandatory. It is `false` for a pure native packet and `true` whenever filesystem fallback/export contributed to the packet or drift comparison.

## Required Packet Fields

| Field | Type | Required reason |
| --- | --- | --- |
| `packet_id` | `string` | Stable deterministic identity for this emitted packet. |
| `project` | `string` | Project/repo scope used for lookup. |
| `principal` | `string` | Requesting principal or agent identity; prevents unscoped resume. |
| `session_id` | `string` | Active/source session identity when known; unknown must be explicit in the packet content. |
| `state_version` | `string` | Version/freshness marker used for deterministic identity and stale/conflict comparison. |
| `source` | `StatePacketSource` | Shows native vs fallback/export vs imported vs mixed authority. |
| `fallback_used` | `bool` | Shows whether fallback/export contributed; fallback can never be silent primary truth. |
| `freshness` | `StateFreshness` | Lets the caller decide whether state is fresh, stale, or unknown. |
| `drift` | `StateDrift` | Carries drift/conflict state without broad artifact reads. |
| `next_action` | `StateAction` | Exact next action for continuation. |
| `next_verification` | `StateVerification` | Exact evidence gate for continuation. |
| `generated_at` | `time.Time` | Timestamps the packet itself. |
| `evidence_refs` | `[]string` | Source/evidence handles supporting packet truth. |
| `scopes` | `[]StateScopeKind` | Explicit state scopes represented by the packet. |

Optional fields `goal_id`, `task_id`, and `fallback_path` add precision when that scope or fallback/export path exists. They do not replace the required `scopes`, `source`, `fallback_used`, or `evidence_refs` fields.

## Required Request Fields

`ResumePacketRequest` scopes a native resume read:

| Field | Type | Required reason |
| --- | --- | --- |
| `project` | `string` | Project/repo scope for native lookup. |
| `principal` | `string` | Caller identity; implementations must reject missing principals. |
| `session_id` | `string` | Session scope when reading session state. |
| `goal_id` | `string` | Goal scope when reading goal state. |
| `task_id` | `string` | Task scope when reading task state. |
| `scopes` | `[]StateScopeKind` | Explicit requested state scopes: `session`, `project`, `goal`, `task`. |
| `allow_filesystem_fallback` | `bool` | Opt-in flag; fallback/export reads are never implicit. |

## Exact Next Action

`StateAction.Kind` is mandatory inside `next_action` and must be one of:

| Kind | Meaning |
| --- | --- |
| `command` | Execute the provided command or tool-equivalent action. |
| `instruction` | Continue with a named human-readable implementation step. |
| `review_gate` | Stop at a named review/approval gate before more work. |

`StateAction.Description` is mandatory. `StateAction.Command` is required when `kind=command` and omitted otherwise.

## Exact Next Verification

`StateVerification.Kind` is mandatory inside `next_verification` and must be one of:

| Kind | Meaning |
| --- | --- |
| `command` | Run the provided command as the verification step. |
| `artifact` | Inspect or produce the named artifact as verification evidence. |
| `manual` | Perform a non-command check and record the result explicitly. |

`StateVerification.Description` is mandatory. `StateVerification.Command` is required when `kind=command` and omitted otherwise.

## Drift And Conflict

`StateDrift.Kind` is mandatory inside the drift object and must be one of:

| Kind | Meaning |
| --- | --- |
| `none` | No drift was observed. |
| `native_stale` | Native state exists but is older than the required freshness boundary. |
| `fallback_newer` | Filesystem fallback/export state is newer than native state. |
| `conflict` | Native and fallback/export state carry incompatible values. |
| `unknown` | Drift could not be evaluated. |

`StateDrift.Conflicts` is mandatory. It is an empty list when there is no conflict. When `Kind` is `conflict`, each `StateConflict` names the field and the native/fallback values that disagreed; `Resolution` records the deterministic recovery stance.

## Agent-Owned Interface

`StatePlane` is the agent-facing read/write contract:

- `WriteSessionState`
- `WriteProjectState`
- `ReadSessionState`
- `ReadProjectState`
- `ReadResumePacket(context.Context, ResumePacketRequest) (ResumePacket, error)`

Browser/operator UI must not receive direct write semantics from this contract. Later REST/MCP adapters may expose read or agent write paths, but T001 does not authorize operator-console mutation controls.

## Fallback/Export Rule

Filesystem fallback/export is explicit and opt-in:

- callers request it with `ResumePacketRequest.AllowFilesystemFallback`;
- fallback/export adapters must force `source=filesystem_fallback`, even if the serialized file claims another source;
- returned fallback packets identify fallback via `source=filesystem_fallback`, `fallback_used=true`, non-empty `fallback_path`, and an `evidence_refs` entry for `filesystem_fallback:<fallback_path>`;
- mixed native/fallback comparison packets use `source=mixed`, `fallback_used=true`, non-empty `fallback_path`, and combined native + fallback evidence refs;
- conflict paths identify disagreement via `source=mixed`, `drift.kind=conflict`, and non-empty `drift.conflicts`;
- pure native happy-path packets use `source=native`, `fallback_used=false`, empty `fallback_path`, no filesystem fallback evidence refs, `freshness=fresh`, and `drift.kind=none`.

Filesystem fallback/export may support recovery or parity proof, but it is never silent primary truth once native state exists. A native read that returns fallback markers is invalid; a fallback read that lacks path or evidence markers is invalid.

## CR-006 Closeout Boundary

CR-006 closes MPL-1 only: native state plane primary read/write, explicit fallback/export, bounded drift/conflict markers, exact next action/verification, and audit evidence for mutations plus exceptional read-source choices.

The following remain outside this contract and require later CRs: principal explorer / briefs, review-loop UX, experience redesign, forgetting and consolidation, temporal truth expansion, operator-console mutation UI, and broad graph/rerank resurrection.


## Verification

T001 contract verification is `go test ./pkg/cognitive`, specifically:

- `TestResumePacketContract_RequiredFieldsBinaryDefined`
- `TestResumePacketRequestContract_ExplicitScopeAndFallbackFields`
- `TestResumePacketNestedContract_DriftConflictActionVerification`
- `TestResumePacketEnums_PinNativeFallbackAndContractSourceTaxonomy`
- `TestStatePlaneInterface_ReadWriteAgentOwned`
- `TestStatePlane_FiveMethods`
- `TestStatePlane_ReadResumePacketSignature`
