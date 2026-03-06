# Specification: Deployment & Installation Cleanup

## Overview
Fix all deployment artifacts (install scripts, plugin setup, docs, env config) to match the current client-server architecture (remote Docker server + local client hooks/MCP proxy). The install scripts and docs are stuck on the old local-worker + SQLite model.

## Functional Requirements
- FR1: Install scripts (install.sh, install.ps1) download only CLIENT binaries (hooks + mcp-stdio-proxy), not the worker/server
- FR2: Install scripts prompt for server URL and API token, write them to environment/config
- FR3: Plugin has a `/engram:setup` command that interactively configures connection to remote server
- FR4: DEPLOYMENT.md accurately reflects current architecture (postgres user `engram`, dimensions 4096, no Unraid template in-repo)
- FR5: `.env.example` includes ALL variables referenced by docker-compose.yml
- FR6: Plugin `.mcp.json` works after setup (env vars are set before Claude Code reads it)
- FR7: Install scripts verify connectivity to remote server after setup

## Non-Functional Requirements
- NFR1: New user can go from zero to working engram in <5 minutes with clear error messages
- NFR2: No breaking changes for users who already have env vars set manually

## Acceptance Criteria
- [ ] AC1: Fresh `install.sh` on macOS/Linux downloads client binaries, prompts for URL/token, registers plugin, verifies health
- [ ] AC2: Fresh `install.ps1` on Windows does the same
- [ ] AC3: `/engram:setup` command works in Claude Code to configure URL/token
- [ ] AC4: DEPLOYMENT.md has zero inconsistencies with docker-compose.yml and .env.example
- [ ] AC5: `.env.example` includes every variable from docker-compose.yml (including FalkorDB, TRUNCATE)
- [ ] AC6: `plugin/.mcp.json` correctly resolves after env vars are set
- [ ] AC7: `/engram:doctor` correctly diagnoses missing env vars and suggests `/engram:setup`

## Out of Scope
- Unraid XML template (lives in user's personal `../unraid-templates/` repo)
- Changes to server-side Docker build or worker binary
- Changes to MCP protocol or tool definitions
- Goreleaser: add mcp-stdio-proxy build + update release header (required for install scripts to work)

## Acceptance Criteria (additional)
- [ ] AC8: goreleaser builds mcp-stdio-proxy for all platforms (darwin/arm64, linux/amd64, windows/amd64)
- [ ] AC9: Install scripts reference correct binary names (engram-server, engram-mcp, not worker, mcp-server)

## Dependencies
- GitHub Releases must include client-only binaries (mcp-stdio-proxy, hooks)
- Docker image on ghcr.io must be current
