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
| Host secrecy versus non-root container readability | Root review accepted the independent finding that file-sourced Compose secrets ignore uid/gid/mode remapping. Shared access enforcement now uses Unix 0700 roots + 0644 files and protected owner/LocalSystem Windows ACLs. A direct UID 65532 read-only bind probe passes while the host root remains private. |
| Exception during secret initialization | Pending root is recorded immediately and cleaned from the outer `finally`, including a partially initialized directory. |
| Unsafe cleanup path | Removal is limited to the OS temp root and an Engram-owned prefix; cleanup result is verified. |
| Wrong-type/false residue evidence | OTLP result contains strict checked booleans and measured deltas; hard-coded empty arrays are forbidden. |
| Absolute path hidden in nested evidence | Root review found and fixed `go-test-summary.json.input_path`; regenerated evidence contains no drive-letter paths. |
| Missing DB silently treated as success | A full run failed 196/197 without a dedicated DSN. Final acceptance reran against disposable `engram_critical_test` PostgreSQL and passed 199/199 with zero skips and zero container residue. |
| Secret-like test fixtures | Fixtures are computed and explicitly marked as non-secrets; gitleaks scans all added content with no finding. |
| Direct moderate npm advisory | GHSA-gj2h-2fpw-fhv9 is unreachable because the console is SPA-only and does not use UForm/UAuthForm. A critical guard pins the reviewed state and rejects a hostile affected component; dependency or SSR changes force reclassification. |
| npm exits zero with an incomplete optional-native tree | The original 839-package failure was reproduced by target-only fetch denial. The implementation checks every lock-applicable OS/CPU/libc optional package and exact version before Nuxt; the injected tree fails and a clean Linux tree checks 12. |
| Portable summary but absolute nested parser stdout | Root review found this on the first 201-test run, changed the parser output to the existing evidence-path normalizer, and replaced the entire evidence run. The final directory has zero drive-letter matches. |
| `_FILE` variables coexist with plaintext or conflicting duplicates | The runtime inspect helper now requires exactly one expected `_FILE`/redacted entry and rejects every legacy PostgreSQL/DSN/admin/vault plaintext name. Hostile mixed and duplicate fixtures moved RED to GREEN. |
| Fixed OTLP cleanup pause | Replaced with bounded polling over fresh process/container snapshots; Docker remains mandatory because the emitted evidence claims that container residue was measured. |
| Nuxt affected component in kebab-case | Hostile fixtures now cover both `<UAuthForm>` and `<u-auth-form>`; the source scanner recognizes both naming forms. |

## Verification reviewed

- `go test ./...`: PASS.
- Targeted critical runtime contract: PASS.
- `assert-go-test-json.ps1`, `run-critical-suite.ps1`, `run-db-suite.ps1`, and `run-dev-stand.ps1` self-tests: PASS.
- Six changed PowerShell scripts parse through the PowerShell AST parser: PASS.
- Full critical evidence after the Compose version regression: 203 tests, 203 passed, zero failed/skipped/unexpected skips.
- Critical command evidence: two records, executables `go` and `pwsh`, `working_directory` is `.`, no absolute artifact paths.
- OTLP evidence: 13 required tests, zero missing/failed, measured process/container residue both zero.
- gitleaks 8.30.0: no leaks in 149,061 UTF-8 bytes of cumulative added, untracked, and ignored content since the batch base.
- `git diff --check`: PASS.
- Native optional verifier hostile self-test: PASS; clean pinned Linux/glibc install checks 12 applicable packages; injected omission fails with the exact locked package identity.
- Independent-review follow-up: final targeted critical contract PASS (55.841s), DB and dev-stand self-tests PASS, OTLP 13/13 with one poll/no timeout/zero residue, Windows owner/LocalSystem ACL assertion PASS, and non-root UID 65532 secret bind read PASS.

## Unresolved release gates

- Clean-commit image build/scan/runtime gate: PASS at af77359d; final movement after later control-plane-only commits must remain classified.
- Clean-commit dev-stand lifecycle and anti-regression walkthrough: af77359d RED exposed missing operator-console VERSION with cleanup PASS; fix implemented, exact-commit rerun pending.
- A17 governance correction and exact direct-child B17 authority implementation.
- Independent scoped PR review and blind verdict.

These are explicit next gates, not accepted-by-assumption results.
