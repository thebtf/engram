# safety-gate.sh

Invariant checker for the engram v5 cleanup (T006 / Phase 2).

Run before and after every PR in the 14-PR v5.0.0 sequence to verify that vault
credentials and static entity counts have not drifted.

## Quick start

```
ENGRAM_API_TOKEN=<your-token> bash scripts/safety-gate.sh
```

The script exits 0 on success, 1 on any violated invariant, and 2 on a
configuration or prerequisite error (missing token, missing jq, etc.).

## Options

| Flag | Description |
|------|-------------|
| `--baseline <path>` | Override the baseline file (default: `scripts/safety-gate-baseline.json`) |
| `--phase=pre-us3` | Check `vault.credential_count` against the vault API (before US-3 migration) |
| `--phase=post-us3` | Check `entities_post_us3.credentials.exact` against the `credentials` table |
| `--skip-pg` | Skip all Postgres entity checks |
| `--help` | Print usage |

Phase is auto-detected by default: if the `credentials` table exists in Postgres
the post-US3 path is used; otherwise pre-US3.

## Environment variables

| Variable | Required | Description |
|----------|----------|-------------|
| `ENGRAM_API_TOKEN` | Yes | Bearer token for the engram API |
| `SAFETY_GATE_SKIP_PG` | No | Set to any non-empty value to skip Postgres checks |

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | All invariants satisfied |
| 1 | One or more invariants violated (diff printed to stderr) |
| 2 | Configuration or prerequisite error |

## Baseline file

`scripts/safety-gate-baseline.json` contains the captured-at snapshot:

- `vault.fingerprint` — SHA fingerprint of the 13 production vault credentials
- `vault.credential_count` — must be 13 (pre-US3)
- `vault.mismatch_count_max` — maximum tolerated vault mismatches (0)
- `entities.*` — row counts for static tables (`issues`, `api_tokens`, etc.)
- `entities_post_us3.credentials.exact` — credential count post-migration (13)

Do not edit these values without a corresponding PR to this repository.

## Prerequisites

- `jq` — JSON parsing
- `curl` — HTTP calls to the engram server
- `docker` — Postgres queries (not required when `--skip-pg` is used)

## Running tests

```
bash scripts/safety-gate_test.sh
```

All tests are self-contained and use mock binaries; no real server or Postgres
instance is required.

---

## Windows dev workstation (.ps1)

`scripts/safety-gate.ps1` is the PowerShell 7+ sibling for Windows developers.

Windows dev workstations have no Docker socket and therefore cannot run Postgres
entity checks.  The `.ps1` script only validates the vault API (Gate 1) and skips
Gate 2 entirely.  Run `safety-gate.sh` on CI or Unraid for the full entity check.

### Quick start

```powershell
$env:ENGRAM_API_TOKEN = '<your-token>'
pwsh scripts/safety-gate.ps1
```

The script exits 0 on success, 1 on any violated invariant, and 2 on a
configuration or prerequisite error (missing token, unreachable server, etc.).

### Options

| Parameter | Description |
|-----------|-------------|
| `-Baseline <path>` | Override the baseline file (default: `scripts/safety-gate-baseline.json`) |
| `-Phase auto\|pre-us3\|post-us3` | Which credentials invariant to apply. `auto` always resolves to `pre-us3` on Windows (no DB access). |
| `-Help` | Print usage and exit 0 |

### Environment variables

| Variable | Required | Description |
|----------|----------|-------------|
| `ENGRAM_API_TOKEN` | Yes | Bearer token for the engram API |

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | All invariants satisfied |
| 1 | One or more invariants violated (diff printed to stderr) |
| 2 | Configuration or prerequisite error |

### Running tests

Requires Pester v5 (`Install-Module -Name Pester -Scope CurrentUser -Force -SkipPublisherCheck`).

```powershell
pwsh -NoProfile -Command "Invoke-Pester scripts/safety-gate.Tests.ps1 -Output Detailed"
```

Tests run entirely in-process child processes with a mock `Invoke-RestMethod` module
injected per-test.  No real server or Postgres instance is required.

### PSScriptAnalyzer

```powershell
Install-Module -Name PSScriptAnalyzer -Scope CurrentUser -Force
Invoke-ScriptAnalyzer -Path scripts/safety-gate.ps1 -Severity Warning
```

Expected output: no warnings.
