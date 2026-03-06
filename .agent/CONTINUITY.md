# Continuity State

**Last Updated:** 2026-03-06
**Session:** Deployment Cleanup Deep Planning

## Done
- Self-learning plan: all 3 phases complete (Phase 4 deferred to v1.1)
- RAG improvements plan: ALL 3 PHASES COMPLETE
- FalkorDB optional graph backend: ALL 6 PHASES COMPLETE
- Embedding platform split: Windows build fix COMPLETE
- **Deployment cleanup Phase 1 COMPLETE**: .env.example + DEPLOYMENT.md fixed
  - Added missing vars: EMBEDDING_TRUNCATE, FalkorDB vars
  - Fixed postgres user mnemonic -> engram, dimensions 1536 -> 4096
  - Removed Unraid template reference (lives in personal repo)
  - Updated client setup for plugin-first workflow
  - Added Streamable HTTP to architecture diagram
  - Added client variables section (ENGRAM_URL, ENGRAM_API_TOKEN)
- **ADRs written**:
  - ADR-001: Belief revision and knowledge quality assurance (preliminary)
  - ADR-002: Plugin-first installation architecture (accepted)
- **Deployment cleanup plan revised** after challenging-plans critique

## Now
Deployment cleanup Phases 2-4 — plan approved, ready for implementation.

## Next (in order)
1. **Phase 2**: Create plugin/commands/setup.md, update doctor.md
2. **Phase 4**: Add mcp-stdio-proxy to goreleaser, update release header
3. **Phase 3**: Refactor install scripts (delete dead code, fix binary names)

## Open Questions
- Gemini CLI API consistently 502 (model gemini-3.1-pro-preview-customtools not found)
- Codex plan review timed out — critique from challenging-plans agent was sufficient

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
