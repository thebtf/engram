# Continuity State

**Last Updated:** 2026-03-06
**Session:** Deployment Cleanup Implementation

## Done
- Self-learning plan: all 3 phases complete (Phase 4 deferred to v1.1)
- RAG improvements plan: ALL 3 PHASES COMPLETE
- FalkorDB optional graph backend: ALL 6 PHASES COMPLETE
- Embedding platform split: Windows build fix COMPLETE
- **Deployment cleanup: ALL 4 PHASES COMPLETE** (6 commits)
  - Phase 1: .env.example + DEPLOYMENT.md fixed (dimensions, postgres user, FalkorDB vars, client setup)
  - Phase 2: Created `/engram:setup` command, updated doctor.md with setup reference
  - Phase 4: Added mcp-stdio-proxy to goreleaser, updated release header
  - Phase 3: Refactored install.sh and install.ps1 (deleted dead code, fixed binary names, added env var setup)
- **ADRs written**:
  - ADR-001: Belief revision and knowledge quality assurance (preliminary, deferred to v1.1)
  - ADR-002: Plugin-first installation architecture (accepted)

## Now
Deployment cleanup plan fully implemented. Ready for push/PR.

## Next
1. Push deployment cleanup commits and create PR
2. Test goreleaser snapshot: `goreleaser release --snapshot --skip=publish`
3. RAG improvements Phase 1 (from `.agent/plans/rag-improvements.md`)

## Open Questions
- Gemini CLI API consistently 502 (model gemini-3.1-pro-preview-customtools not found)

## Known Pre-existing Test Failures (Windows)
- `TestSafeResolvePath` — Windows path separator mismatch
- `TestConfigSuite/TestLoad_TableDriven` — env var isolation issue
- `TestKillProcessOnPort_NoProcess` — `lsof` not available on Windows
- `go-tree-sitter` — CGO build constraints exclude Windows

## Key Files
- Deployment plan: `.agent/plans/deployment-cleanup.md`
- Deployment spec: `.agent/specs/deployment-cleanup.md`
- ADRs: `.agent/arch/decisions/ADR-001-belief-revision.md`, `ADR-002-plugin-first-install.md`
- Plugin MCP config: `plugin/.mcp.json`
- Install scripts: `scripts/install.sh`, `scripts/install.ps1`
- Goreleaser: `.goreleaser.yaml`

## Plan Documents
- Global Roadmap: `.agent/plans/global-roadmap.md`
- Deployment Cleanup: `.agent/plans/deployment-cleanup.md`
- FalkorDB Graph: `.agent/plans/falkordb-optional-graph.md`
- RAG Improvements: `.agent/plans/rag-improvements.md`
