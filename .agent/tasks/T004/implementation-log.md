## Task T004 — Implementation Log

### Quoted AC
> - AC: tests prove native-first read, explicit fallback marker, and drift/conflict path.
Source: `.agent/specs/memory-product-layer/changes/CR-001-initial-scope/tasks.md` | line 62

Supporting feature contract:
> The system must provide an Engram-native state plane for session, goal, task, and project handoff so resume reads native state first and filesystem state only as fallback/export.
Source: `.agent/specs/memory-product-layer/spec.md` | line 53

> The native state plane must expose a deterministic resume packet containing at least freshness marker, drift/conflict flags, exact next action, and exact next verification step.
Source: `.agent/specs/memory-product-layer/spec.md` | line 56

> Resume reads native state before filesystem fallback.
> Resume packet includes freshness, drift/conflict, next action, and next verification.
> Filesystem state remains explicit fallback/export, not silent primary truth.
Source: `.agent/specs/memory-product-layer/spec.md` | lines 129-131

### User Change Enabled
No direct user change — prerequisite for task T007 which delivers a principal-scoped brief that can be trusted after resume.

### Claim Grounding
- Claim: native-first read. Meaning here: a successful native packet is returned without consulting the filesystem fallback reader. Evidence: focused fallback service test asserts fallback is not called on native success.
- Claim: explicit fallback marker. Meaning here: when native resume is unavailable and fallback is explicitly allowed, the returned packet reports `source=filesystem_fallback` and includes a fallback path. Evidence: focused fallback service test reads a fallback JSON fixture and checks both fields.
- Claim: drift/conflict path. Meaning here: when native and fallback packets both exist and disagree on continuation-critical fields, the result is not silently native; it reports `source=conflict`, `drift.kind=conflict`, and named conflicts. Evidence: focused conflict test compares divergent next-action or next-verification fields.
- Claim: rollback stays safe. Meaning here: fallback remains an explicit read path; no native write migration is required for T004. Evidence: implementation keeps GORM store native-only and adds fallback behavior in a wrapper/use-case layer.

### Terminology Alignment
- "Native state" / "native state plane" / `StatePacketSourceNative` all mean the Engram-owned state rows introduced in T002 and surfaced in T003.
- "Filesystem fallback" / "fallback/export" / `StatePacketSourceFilesystemFallback` all mean an explicitly allowed file-backed packet source, not a silent primary read.
- "Drift/conflict" in the spec maps to `StateDrift` and `StateConflict`; T004 only proves the explicit conflict path required by the task, not later temporal truth or experience applicability.
- "current.json role from plan.md" maps to the plan's fallback note for `.agent/session-state/current.json`; the current worktree has no such file, so T004 evidence will record that absence and use a minimal temp JSON packet fixture for behavior tests.

### Implementation Decision
Add a small state-plane service package that wraps a native `cognitive.StatePlane` and an optional filesystem fallback reader. `ReadResumePacket` will return native packets first, use fallback only when `AllowFilesystemFallback` is set, and return a conflict packet when explicit fallback comparison finds continuation-critical drift. Add an opt-in fallback reader for JSON packet files, expose `allow_filesystem_fallback` through MCP and REST resume requests, and write phase-1 evidence JSON after verification. Keep `internal/db/gorm.StateStore` native-only and avoid any broad filesystem archaeology on the happy path.

### Verification Result
AC-by-AC:
  - AC 1: [PASS] — RED `go test ./internal/stateplane ./internal/mcp ./internal/worker` failed on missing `NewService`/`JSONFileFallbackReader` and missing fallback flag forwarding before implementation.
  - AC 2: [PASS] — GREEN `go test ./internal/stateplane ./internal/mcp ./internal/worker`; GREEN `go test ./pkg/cognitive ./internal/db/gorm ./internal/stateplane ./internal/mcp ./internal/worker`; GREEN `go test ./... -count=1`; REVIEW degraded native route `019f0d43-56ee-7405-a029-d8021bb884d2` plus manual lite review with no blocking findings after fallback validation hardening.

User-observable ACs:
  N/A — T004 is backend resume proof with technical acceptance criteria only.

Overall: [PASS]

### NEEDS_CLARIFICATION (if AMBIGUOUS result)
N/A
