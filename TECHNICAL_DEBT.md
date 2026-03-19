# Technical Debt

## 2026-03-19: MCP Resources/Prompts Stubs
What: MCP server returns empty lists for resources/list, prompts/list, completion/complete
Why deferred: MCP spec allows graceful empty responses for unsupported capabilities
Impact: No functional impact — clients handle empty lists

## 2026-03-19: Memory Blocks Table Unpopulated
What: migration 024 created memory_blocks table but no code populates it
Why deferred: Consolidation-driven population requires redesign of consolidation scheduler
Impact: Table exists but empty — no runtime impact

## 2026-03-19: Config Reload via os.Exit(0)
What: reloadConfig calls os.Exit(0) instead of hot-reload
Why deferred: Hot-reload requires significant refactoring of service initialization
Impact: Docker restart policy handles the restart automatically
