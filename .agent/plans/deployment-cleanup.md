# Implementation Plan: Deployment & Installation Cleanup

**Status:** COMPLETED (all 4 phases implemented, 2026-03-11)

## Summary
Fix all deployment artifacts to match current client-server architecture.
Server = Docker (worker + PostgreSQL + pgvector). Client = plugin (hooks + HTTP MCP).

**Spec:** `.agent/specs/deployment-cleanup.md` (updated: goreleaser now in scope)
**ADRs:** `.agent/arch/decisions/ADR-002-plugin-first-install.md`

## Analysis Insights

### MCP Transport Architecture
- Worker serves **both** SSE (`/sse` + `/message`) and Streamable HTTP (`/mcp`)
- Plugin `.mcp.json` uses `type: "http"` -> Streamable HTTP (correct for Claude Code)
- `mcp-stdio-proxy` converts stdio->SSE (for non-HTTP MCP clients)
- `cmd/mcp` (`engram-mcp`) is a full standalone stdio MCP server (for local-only mode)

### Goreleaser Naming vs Install Scripts
| Goreleaser builds | Install scripts expect | Actual cmd/ |
|-------------------|----------------------|-------------|
| `engram-server` | `worker` | `cmd/worker` |
| `engram-mcp` | `mcp-server` | `cmd/mcp` |
| (missing) | (not referenced) | `cmd/mcp-stdio-proxy` |

### Key Decision: Plugin `.mcp.json` URL
```
ENGRAM_URL=http://server:37777/mcp
```

### Setup Command Constraints
Claude Code plugin commands are AI instructions (markdown files), not executables.
They CANNOT modify environment variables or read them directly.
They CAN: ask user about their OS, generate platform-specific commands, call MCP tools.

## Phases

### Phase 1: Fix .env.example and DEPLOYMENT.md (docs-only) -- DONE
- [x] Task 1.1: Add missing vars to `.env.example` (EMBEDDING_TRUNCATE, FalkorDB vars)
- [x] Task 1.2: Fix DEPLOYMENT.md -- postgres user `mnemonic` -> `engram`, dimensions 1536 -> 4096
- [x] Task 1.3: Remove Unraid template reference (not in-repo)
- [x] Task 1.4: Update client setup section for plugin-first workflow
- [x] Task 1.5: Add client variables section (ENGRAM_URL, ENGRAM_API_TOKEN)
- [x] Task 1.6: Update architecture diagram to show Streamable HTTP

### Phase 2: Plugin setup command + doctor update -- DONE
- [x] Task 2.1: Create `plugin/commands/setup.md` -- `/engram:setup` interactive guide
  - Ask user for their OS (or infer from conversation context)
  - Ask for server URL (default: http://localhost:37777/mcp)
  - Ask for API token (or empty for no auth)
  - Generate platform-specific env var commands:
    - macOS/Linux: `export ENGRAM_URL=... && export ENGRAM_API_TOKEN=...`
    - Add to shell profile: `echo 'export ENGRAM_URL=...' >> ~/.zshrc`
    - Windows: `[Environment]::SetEnvironmentVariable("ENGRAM_URL", "...", "User")`
  - Instruct user to restart Claude Code
  - After restart, verify with `check_system_health()` MCP tool
- [x] Task 2.2: Update `plugin/commands/doctor.md`
  - If MCP connection fails, suggest: "Run `/engram:setup` to configure your connection"
  - Note: doctor command cannot read env vars directly -- it relies on MCP tool calls
    and user-reported error messages for diagnosis

### Phase 4: Goreleaser + release cleanup -- DONE
- [x] Task 4.1: Add `mcp-stdio-proxy` build to `.goreleaser.yaml`
  - `CGO_ENABLED=0` (VERIFIED: pure Go, stdlib-only imports in cmd/mcp-stdio-proxy/main.go)
  - All 3 platforms: darwin/arm64, linux/amd64, windows/amd64
  - Binary name: `engram-mcp-stdio-proxy`
- [x] Task 4.2: Update release header template
  - Primary: plugin marketplace (`/plugin marketplace add thebtf/engram-marketplace`)
  - Secondary: manual install script
  - Tertiary: direct download for advanced users
- [x] Task 4.3: Verify archive includes all plugin files (already confirmed in goreleaser)

### Phase 3: Refactor install scripts -- DONE
The existing scripts have solid download, registration, and error handling.
Actual work: delete dead functions, fix binary names, add env var prompting.

- [x] Task 3.1: Refactor `scripts/install.sh` (554 -> ~410 lines)
  - KEEP: `detect_platform()`, `get_latest_version()`, `download_release()`, `register_plugin()`
  - DELETE: `start_worker()`, `check_optional_deps()` (Python/uvx checks from SQLite era)
  - FIX: binary name references `worker` -> `engram-server`, `mcp-server` -> `engram-mcp`
  - ADD: prompt for `ENGRAM_URL` and `ENGRAM_API_TOKEN`, generate shell profile export lines
  - ADD: verify server health with `curl $ENGRAM_URL/../health`
  - PRESERVE: temp-file-and-rename pattern for JSON file writes (atomic, safe for concurrency)
  - REMOVE: MCP server registration in `settings.json` (plugin handles this now via .mcp.json)
- [x] Task 3.2: Refactor `scripts/install.ps1` (377 -> ~275 lines)
  - Same changes as install.sh but PowerShell native
  - FIX: `$env:MNEMONIC_VERSION` -> `$env:ENGRAM_VERSION` (line 5, old project name)
  - FIX: binary name references to match goreleaser output
  - Use `[Environment]::SetEnvironmentVariable` for persistent env vars
- Task 3.3: Edge cases both scripts must handle:
  - Upgrade path (detect existing install, preserve config)
  - Offline/corporate proxy (clear error messages)
  - Permission errors (suggest sudo / Run as Admin)
  - Download failures for CGO-dependent hooks (prebuilt binaries only, no go install fallback)

## Approach Decision
**Chosen approach:** Plugin-first with HTTP MCP (ADR-002)
**Rationale:** Claude Code natively supports `type: "http"` MCP. No proxy needed. Simplest path.
**Alternatives considered:**
- stdio proxy as primary -> rejected: adds unnecessary binary
- Full rewrite of install scripts -> rejected per critique: existing scripts are 80% correct

## Critical Decisions
- **Decision 1**: Setup command is AI-guided, not programmatic. Cannot read/set env vars.
- **Decision 2**: Install scripts are client-only. Server deployment is Docker-only.
- **Decision 3**: mcp-stdio-proxy built without CGO (VERIFIED: stdlib-only imports).
- **Decision 4**: Phase 3 is refactor (preserve working code) not rewrite (start from scratch).

## Risks & Mitigations
- **Risk 1:** `${ENGRAM_URL}` not resolving -> setup command + doctor diagnose this
- **Risk 2:** Breaking existing users -> env var names unchanged, plugin registration backward-compatible
- **Risk 3:** goreleaser archive change -> test with `goreleaser release --snapshot --skip=publish`
- **Risk 4:** Setup command UX limited (no runtime env modification) -> clear copy-paste commands
- **Risk 5:** JSON file concurrency in install scripts -> preserve atomic write pattern (temp+rename)

## Files to Modify
- `plugin/commands/setup.md` -- NEW: interactive setup guide (~50 lines)
- `plugin/commands/doctor.md` -- minor update with setup reference (~5 lines changed)
- `.goreleaser.yaml` -- add mcp-stdio-proxy build (~20 lines)
- `scripts/install.sh` -- refactor: delete ~200 lines, fix ~30 lines, add ~50 lines
- `scripts/install.ps1` -- refactor: delete ~150 lines, fix ~20 lines, add ~30 lines

## Success Criteria
- [x] `.env.example` includes every variable from docker-compose.yml
- [x] DEPLOYMENT.md has zero inconsistencies with docker-compose.yml
- [ ] `/engram:setup` generates correct platform-specific env var commands
- [ ] `/engram:doctor` suggests setup on connection failure
- [ ] Install scripts reference correct binary names (engram-server, engram-mcp)
- [ ] Install scripts don't start local worker or check Python/uvx
- [ ] goreleaser builds mcp-stdio-proxy for all platforms
- [ ] Fresh install on clean machine works end-to-end

## Implementation Order
Phase 2 -> Phase 4 -> Phase 3

Rationale: Phase 2 (setup + doctor) is highest-value -- directly solves the original ${ENGRAM_URL}
failure. Phase 4 (goreleaser) unblocks Phase 3 (scripts need correct release archives).
Phase 3 is refactoring existing working code -- lowest risk.

## Plan Validation
**Critique result:** REVISE (challenging-plans agent)
**Key findings addressed:**
1. Spec/plan conflict on goreleaser scope -> spec updated to include goreleaser
2. Phase 3 reframed from "rewrite" to "refactor" -> preserves working code
3. REMOVE list fixed to reference actual function names (check_optional_deps, start_worker)
4. Binary name fix added as explicit subtask in Phase 3
5. JSON file concurrency risk noted -> preserve atomic write pattern
6. PowerShell MNEMONIC_VERSION rename added to Task 3.2
7. Doctor's env var limitation documented in Task 2.2
