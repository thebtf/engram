# Continuity State

**Last Updated:** 2026-03-01
**Session:** Plugin skill refinement + marketplace sync + release packaging

## Current Goal
Plugin v0.3.0 fully deployed to marketplace. Skill, commands, .mcp.json all synced.

## Active Work

### Plugin Refinement — DONE (this session)
- [x] Skill `plugin/skills/memory/SKILL.md` — renamed from using-engram → engram-memory → memory
- [x] Frontmatter `name: memory` → produces `/engram:memory` (not `/engram-memory`)
- [x] Added "Connection Check" section: call `check_system_health()`, never check env vars
- [x] Added common mistake: "never check ENGRAM_URL/ENGRAM_API_TOKEN env vars"
- [x] `.goreleaser.yaml` — added `plugin/skills/*/*.md` and `plugin/.mcp.json` to archives
- [x] `scripts/install.sh` — added skills/ and .mcp.json copying
- [x] Synced `doctor.md` and `restart.md` to marketplace (MCP-based doctor, improved restart)
- [x] Fixed cache `hooks/hooks/` double nesting
- [x] Marketplace version bumped: 0.1.0 → 0.2.0 → 0.3.0

### Marketplace Sync
- Marketplace repo: `thebtf/engram-marketplace` (separate from main repo)
- Main repo `plugin/` → must be manually synced to marketplace
- Marketplace commits: 18256b9 (skill), ba0295e (commands), e5755d3 (v0.2.0), d843b38 (name fix), f20516d (v0.3.0)

### MCP Config — VERIFIED
- `${VAR}` expansion: Claude Code expands from process environment at runtime
- `env` field: only for stdio servers. HTTP servers have NO env field
- Plugin `.mcp.json` creates `plugin:engram:engram` entry — requires system env vars
- Manual config in `.claude.json` works with literal values
- Two entries can coexist but plugin one fails if env vars not set

### Phase 2: Benchmark — CODE COMPLETE, NOT COMMITTED
- Task #32: 3 files by Codex: `internal/benchmark/{histogram.go, seed.go, benchmark_test.go}`
- Build tag: `//go:build benchmark`
- Verified: `go build`, `go vet` PASS

## Architecture Decision
**Chosen:** Phased C→A — PostgreSQL-only first, Apache AGE conditional on benchmarks.
**Plan:** `.agent/plans/storage-architecture-v2.md`

## Commits This Session (main repo)
- `5dad117` — fix: include skills and .mcp.json in release archives (amended with rename)
- Previous session commits: 0502eeb, 0d366df, 9860993, 70f3680

## Uncommitted Changes
- `plugin/skills/memory/SKILL.md` — renamed from engram-memory/, updated content
- Need to commit + push

## Key Files
- Plugin skill: `plugin/skills/memory/SKILL.md`
- Plugin MCP config: `plugin/.mcp.json`
- Marketplace: `thebtf/engram-marketplace` (plugins/engram/)
- Goreleaser: `.goreleaser.yaml` (archives section)
- Install script: `scripts/install.sh`

## Next Steps
1. Commit pending skill rename in main repo
2. Benchmark suite: code review, commit, run
3. Consider CI automation for marketplace sync
