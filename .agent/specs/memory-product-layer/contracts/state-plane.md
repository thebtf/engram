# State Plane Contract

Feature: ENG-MPL-1  
CR: CR-001-initial-scope  
Task: T001  
Status: draft-contract

## Purpose

The state plane is Engram-owned handoff state for session, project, goal, and task resume. Its first contract is the bounded `ResumePacket`: a deterministic payload that tells an agent where it is, what to do next, and what evidence to collect before it opens filesystem fallback artifacts.

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

Persistence, REST/MCP adapters, native-first lookup logic, filesystem fallback reading, and drift tests are implemented by T002-T004.

## Packet Authority

`ResumePacket.Source` is mandatory and must be one of:

| Source | Meaning |
| --- | --- |
| `native` | Packet came from Engram-native state and may drive the normal resume path. |
| `filesystem_fallback` | Packet came from explicit fallback/export state. This must never be reported as native state. |
| `conflict` | Native and fallback state disagreed enough that the caller must reconcile before continuing. |

## Required Packet Fields

| Field | Type | Required reason |
| --- | --- | --- |
| `source` | `StatePacketSource` | Shows native vs fallback vs conflict authority. |
| `freshness` | `StateFreshness` | Lets the caller decide whether native state is fresh, stale, or unknown. |
| `drift` | `StateDrift` | Carries drift/conflict state without broad artifact reads. |
| `next_action` | `StateAction` | Exact next action for continuation. |
| `next_verification` | `StateVerification` | Exact evidence gate for continuation. |
| `generated_at` | `time.Time` | Timestamps the packet itself. |

Optional identity fields (`project`, `session_id`, `goal_id`, `task_id`, `scopes`) bind the packet to its handoff scope.

`StateAction.Kind` is mandatory inside `next_action` and must be one of:

| Kind | Meaning |
| --- | --- |
| `command` | Execute the provided command or tool-equivalent action. |
| `instruction` | Continue with a named human-readable implementation step. |
| `review_gate` | Stop at a named review/approval gate before more work. |

`StateVerification.Kind` is mandatory inside `next_verification` and must be one of:

| Kind | Meaning |
| --- | --- |
| `command` | Run the provided command as the verification step. |
| `artifact` | Inspect or produce the named artifact as verification evidence. |
| `manual` | Perform a non-command check and record the result explicitly. |

## Drift And Conflict

`StateDrift.Kind` is mandatory inside the drift object and must be one of:

| Kind | Meaning |
| --- | --- |
| `none` | No drift was observed. |
| `native_stale` | Native state exists but is older than the required freshness boundary. |
| `fallback_newer` | Filesystem fallback/export state is newer than native state. |
| `conflict` | Native and fallback state carry incompatible values. |
| `unknown` | Drift could not be evaluated. |

When `Kind` is `conflict`, `StateDrift.Conflicts` should name the field and the native/fallback values that disagreed.

## Agent-Owned Interface

`StatePlane` is the agent-facing read/write contract:

- `WriteSessionState`
- `WriteProjectState`
- `ReadSessionState`
- `ReadProjectState`
- `ReadResumePacket`

Browser/operator UI must not receive direct write semantics from this contract. Later REST/MCP adapters may expose read or agent write paths, but T001 does not authorize operator-console mutation controls.

## Fallback Rule

Filesystem fallback is explicit and opt-in:

- callers request it with `ResumePacketRequest.AllowFilesystemFallback`;
- returned packets identify fallback via `source=filesystem_fallback`;
- conflict paths identify disagreement via `source=conflict` and `drift.kind=conflict`.

The happy path for later tasks is `source=native` with `freshness=fresh`.

## Verification

T001 contract verification is `go test ./pkg/cognitive`, specifically:

- `TestResumePacketContract_RequiredFieldsBinaryDefined`
- `TestResumePacketEnums_PinNativeFallbackAndConflict`
- `TestStatePlaneInterface_ReadWriteAgentOwned`
- `TestStatePlane_FiveMethods`
