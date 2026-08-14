# AGENTS.md

## STACKS

```yaml
STACKS: [GO]
```

## PROJECT OVERVIEW

Persistent shared memory infrastructure for Claude Code workstations.
Single server (Docker on Unraid/NAS) stores memories, behavioral rules, credentials,
issues, and documents in PostgreSQL 17. MCP tools are exposed via the `engram` stdio
client proxy (server-side HTTP MCP transports removed in v5 — permanent architectural shift to the
stdio daemon + gRPC model); REST API + gRPC on port 37777 (cmux multiplexed).

## RULES

| Rule | Description |
|------|-------------|
| **No stubs** | Complete, working implementations only |
| **No guessing** | Verify with tools before using |
| **Reasoning first** | Document WHY before implementing |
| **No silent patching** | Report every discrepancy found |
| **No time estimates** | Prioritize by value/risk/dependencies, not phantom duration |
| **No resurrecting demolished code** | A symbol/field/env-var/doc EXISTING ≠ it is wired or correct. Classify before building on it (see V5 DEMOLITION GUARD). |

## V5 DEMOLITION GUARD (anti-resurrection — read before extending any existing scaffold)

engram underwent a **v5 demolition**: graph stage, cross-encoder rerank, `internal/search` scoring passes (ApplyCompositeScoring/LaneWeights/DiversityPenalty/SessionBoost), SDK observation extraction, and server-side MCP HTTP transports were all **removed**. Leftover references survive in docs, Swagger `@Description` strings, `.gitattributes` LFS rules, CHANGELOG, model fields, and env-var templates. **"I found something that looks like it implements X" ≠ "X is designed, wired, and works."** Building on a stale remnant restores demolished (incorrect) behavior.

Before building on ANY existing scaffold, classify it against the CURRENT code (not memory, not docs, not its mere existence):

- **live** — runs on prod today. Verify: trace the call path AND the flag state (e.g. `ENGRAM_VNEXT_ENABLED`).
- **pre-demolition-stale** — leftover remnant. IGNORE or cleanly delete; never extend, never use as a build target.
- **dormant-flag-gated** — exists but only active behind an unset flag (e.g. `ENGRAM_LIFECYCLE_ENABLED`). A serve-time feature built on it yields empty/null output in prod.
- **must-build** — absent; build new.

**Tombstone tells** (signals a thing is stale, not a contract): a model field the write-path never populates (grep the write handler, not the struct); an env var with **zero `os.Getenv` reads** in `.go` (grep the reads, not the template/docs); a doc-comment whose handler body returns 501 or is FTS-only; a comment literally saying "removed in v5". When in doubt, grep the write-path and the actual `os.Getenv`/call sites — never trust the declaration alone.

## DUAL-ROLE PM/DEVELOPER GOVERNANCE (read before running a PM or Developer session on this repo)

This repo is developed under an NVMD dual-role loop: one session is PM (governance, review, release), a peer session is Developer (code). Role truth lives in `.agent/session-state/roles/_assignment.json`. Three failures recurred within a single 2026-07 session; each is now a durable guard so the next session inherits the lesson instead of repeating it.

### CODE-REVIEW BEHAVIORAL-EDGE guard

A PM code-review that passes because "the seams exist and tests are green" is **not** a real review. Structure-pass ≠ edge-pass. CR-008's PM review APPROVED a handback that AI-reviewers then flagged with 4 valid behavioral defects the structural pass missed: an audit-snapshot accepted with the **wrong type** (mutation proceeded audit-less), a switch on the **raw** request field instead of the **normalized** one, a **silent limit clamp** where the sibling path returned an error, and metrics computed over a **filtered subset** but presented as the whole. Before APPROVE, for EACH validation/mutation/filter/adapter path, check the edge — wrong type accepted, raw-vs-normalized value, silent clamp vs explicit error, filtered-vs-full counts, ordinary request must-NOT trigger a gated path — not merely that the seam is present. `existing-design-is-a-hypothesis` applies to per-path validation details, not just to the shape.

### RELEASE-RACE guard (shared `.git` object store)

Worktrees share one `.git` object store, so a local release commit is visible to the peer session the instant it exists. During v6.32.0 the peer pushed PM's release commit + created the tag before PM's own `git tag` ran (`fatal: tag already exists`). Release tagging is a **coordination point**, not a solo act. Before cutting a release: announce the exact release commit + tag intent in the PM oracle first, OR on `tag already exists` / `main already at my commit` treat it as a likely peer-push and **verify** consistency (local tag object == remote tag object, deref commit == the intended commit, parent chain correct) rather than force-moving or re-cutting. Never `--force` or re-cut a pushed release tag. `calibrated-doubt-for-irreversible-actions` governs the "tag exists" branch.

### ROLE-STATE GOVERNANCE guard

Role/assignment state is CONTROL-PLANE, not a last-writer-wins free-for-all. A Developer session writing `claude=<role>` into `_assignment.json`, or flipping the shared front-door `current.json` to its own session `mode`, is a **hallucinated self-promotion**, not a legitimate coordination write. Role truth comes ONLY from `_assignment.json` as maintained by the operator / `mode` skill; a subordinate Developer never authors it. The correct PM response is NOT capitulation ("don't war over the front-door") — it is: (1) hold the pm role from `_assignment.json` regardless of front-door churn; (2) restore the shared governance surface (front-door `mode=dual-role`) as PM-owned hygiene; (3) surface repeat role-state hallucination to the operator. Distinction: **content** oracles (`roles/developer/current.json` handoff detail) are peer-owned and fine to let the peer own; **role/assignment/front-door-mode** is control-plane and must not be self-authored by a subordinate role. Meta-lesson: do NOT enshrine an observed-but-illegitimate peer behavior as a rule just because it happened — that is how a hallucination becomes durable policy.

## CONVENTIONS

- Language: Go 1.26.6+
- Build: `make build`
- Test: `go test ./...`
- Database: PostgreSQL 17
- Server: `cmd/engram-server/main.go` — HTTP API + gRPC + dashboard on :37777 (cmux)
- Client: `cmd/engram/main.go` — stdio MCP proxy with git-derived project identity
- Hooks: `plugin/engram/hooks/` — JavaScript hooks for Claude Code lifecycle
- Plugin: `plugin/` — Claude Code plugin definition + marketplace

## KEY DIRECTORIES

```
cmd/engram-server/   — server entry point
cmd/engram/          — local client (stdio MCP proxy)
internal/mcp/        — MCP protocol, tool handlers (tools_*.go)
internal/grpcserver/ — gRPC service implementations
internal/worker/     — HTTP handlers, retrieval, session management
internal/db/gorm/    — GORM models + stores (memories, behavioral_rules, credentials, issues, documents)
internal/crypto/     — AES-256-GCM vault for credential encryption
plugin/engram/hooks/ — JS hooks (SessionStart, UserPromptSubmit, SubagentStop, PreToolUse, PreCompact, Stop, SessionEnd)
```

## CODE INTELLIGENCE (SocratiCode replacement, v6.13.0+)

Set `ENGRAM_CODE_INTEL_ENABLED=true` to activate three drop-in SocratiCode replacement tools:

| Tool | Side | Description |
|------|------|-------------|
| `codebase_index` | daemon | Triggers async code index for the current project root. Returns `{status:"started",run_id}` immediately. |
| `codebase_status` | daemon+server | Reports index liveness, chunk counts, and last-indexed timestamp. |
| `codebase_search` | server | Hybrid FTS+vector search over indexed code chunks. V1: FTS-only (no embedding required). |

Flag-off: all three tools are absent from `tools/list` (byte-identical to pre-v6.13.0 surface).

Key directories:
- `internal/handlers/codeintel/` — daemon-side module (codebase_index, codebase_status liveness)
- `internal/mcp/tools_code_intel.go` — server-side codebase_search + codebase_status counts
- `internal/db/gorm/code_chunk_store.go` — CountEmbeddedByProject, MaxUpdatedAtByProject

## INSTRUCTION HIERARCHY

```
System prompts > Task/delegation > Global rules > Project rules > Defaults
```

## SKILL LOADING

1. Project skills (`.agent/skills/`) override global skills
2. Same-name project skill completely replaces global
3. Skills are loaded by semantic description matching
