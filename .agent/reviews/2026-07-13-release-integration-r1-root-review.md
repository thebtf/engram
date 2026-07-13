# Release Integration R1 Root Review

## Decision

**ACCEPT FOR COMMIT; clean-source image/dev-stand runtime gates and independent PR review remain mandatory before integration.**

## Reviewed scope

- Docker verification and publisher workflow Scout provisioning.
- Image and dev-stand credential generation, file transport, mount proof, and cleanup.
- Critical-suite and nested Go JSON evidence portability.
- OTLP process/container residue measurement.
- Critical contract tests and refreshed tracked evidence.

## Behavioral-edge review

| Edge | Review result |
|---|---|
| Missing Scout on a stock runner | Both workflows install exact v1.23.1, verify the official SHA-256 before extraction, and execute `docker scout version`. |
| Partial archive download | Strict SHA check fails closed; the local partial transfers were rejected and removed. |
| Plaintext credential leakage through compose environment | Compose receives secret-file paths; runtime inspection requires exact `_FILE` variables and bind destinations. Bootstrap capability remains intentionally ephemeral for the one-time bootstrap path and is redacted from evidence. |
| Blank/default/reused/missing credential | Hostile self-test cases are rejected; four generated values are distinct. |
| Unix runner cross-user readability | Secret root is 0700 and files are 0600. |
| Exception during secret initialization | Pending root is recorded immediately and cleaned from the outer `finally`, including a partially initialized directory. |
| Unsafe cleanup path | Removal is limited to the OS temp root and an Engram-owned prefix; cleanup result is verified. |
| Wrong-type/false residue evidence | OTLP result contains strict checked booleans and measured deltas; hard-coded empty arrays are forbidden. |
| Absolute path hidden in nested evidence | Root review found and fixed `go-test-summary.json.input_path`; regenerated evidence contains no drive-letter paths. |
| Missing DB silently treated as success | A full run failed 196/197 without a dedicated DSN. Acceptance reran against disposable `engram_critical_test` PostgreSQL and passed 197/197 with zero skips and zero container residue. |
| Secret-like test fixtures | Fixtures are computed and explicitly marked as non-secrets; gitleaks scans all added content with no finding. |

## Verification reviewed

- `go test ./...`: PASS.
- Targeted critical runtime contract: PASS.
- `assert-go-test-json.ps1`, `run-critical-suite.ps1`, `run-db-suite.ps1`, and `run-dev-stand.ps1` self-tests: PASS.
- Six changed PowerShell scripts parse through the PowerShell AST parser: PASS.
- Full critical evidence: 197 tests, 197 passed, zero failed/skipped/unexpected skips.
- Critical command evidence: two records, executables `go` and `pwsh`, `working_directory` is `.`, no absolute artifact paths.
- OTLP evidence: 13 required tests, zero missing/failed, measured process/container residue both zero.
- gitleaks 8.30.0: no leaks in 73.15 KB of changed and new evidence content.
- `git diff --check`: PASS.

## Unresolved release gates

- Clean-commit image build/scan/runtime gate.
- Clean-commit dev-stand lifecycle and anti-regression walkthrough.
- A17 governance correction and exact direct-child B17 authority implementation.
- Independent scoped PR review and blind verdict.

These are explicit next gates, not accepted-by-assumption results.
