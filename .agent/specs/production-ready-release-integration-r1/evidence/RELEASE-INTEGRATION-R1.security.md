# Release Integration R1 Security Review

## Scope and classification

This is an S4 change because it modifies production image workflows, CI-downloaded tooling, runtime credential transport, cleanup behavior, and release evidence. This review accepts only the implementation batch; it does not assert production readiness or authorize a release. The separate A17/B17 authority batch remains mandatory.

## Assets and trust boundaries

- Runtime PostgreSQL password, admin token, vault key, and bootstrap capability.
- Trusted GitHub Actions execution and the Docker Scout binary downloaded during a run.
- Docker bind mounts that cross from the runner host into containers.
- Critical-suite and OTLP evidence consumed by later release decisions.
- Cleanup boundaries for runner temporary directories, processes, containers, and compose resources.

## Threat review

| Threat | Control | Evidence | Result |
|---|---|---|---|
| Scout archive substitution | Exact v1.23.1 URL and published SHA-256; `sha256sum --check --strict` before extraction; installed binary executes `docker scout version` | Official GitHub release API reports asset size 62,062,436 and SHA-256 `0f778f9d833f28bc6cccff95e33039849c0afcecafa38d9f46fe74bfd0915714` | PASS, CI execution pending |
| Credential disclosure in compose environment | Parent environment carries only secret-file paths; files are bind-mounted at `/run/secrets/*`; plaintext credential variables are explicitly rejected by self-test and runtime inspection | `run-db-suite.ps1 -SelfTest` and critical static contract | PASS |
| Weak or reused credentials | Four independent 256-bit values are generated with `RandomNumberGenerator`; default, blank, missing, and reused values are rejected | dev-stand config contract and self-test hostile cases | PASS |
| Secret files readable by other Unix users | Temporary directory mode 0700 and secret file mode 0600 on non-Windows runners | critical static contract requires `SetUnixFileMode`; PowerShell AST parse passes | PASS |
| Unsafe recursive cleanup | Cleanup resolves an absolute path, requires the OS temp-root boundary and exact Engram-owned prefix before recursive removal | image and dev-stand cleanup functions | PASS |
| Partial secret initialization bypasses cleanup | The temporary root is registered immediately after creation; the outer `finally` selects the completed or pending root and verifies deletion | `PendingImageSecretRoot` and `PendingDevStandSecretRoot` are required by the critical static contract | PASS |
| False residue evidence | OTLP verification snapshots processes and Docker containers before and after the test and fails on newly created residue | live OTLP run: all 13 required tests pass, zero new process/container residue | PASS |
| Host topology disclosure or non-portable proof | Critical command records store executable leaf names, repository-relative paths, and `working_directory: "."` | critical runner self-test | PASS |
| Secrets accidentally committed in changed lines | gitleaks 8.30.0 scans only added diff lines with full redaction | 30.85 KB scanned; no leaks found | PASS |
| Critical isolation test silently skipped or pointed at production | The test rejects missing, production, or staging DSNs; acceptance uses a disposable localhost-only PostgreSQL 17 database explicitly named `engram_critical_test` | 199/199 passed, zero skips; the disposable DB container was removed in `finally` and residue is zero | PASS |
| GHSA-gj2h-2fpw-fhv9 credential leak through pre-hydration GET | Official advisory requires SSR markup from `UForm`/`UAuthForm`; the console is explicitly `ssr: false` and has no affected component usage. A critical guard pins the reviewed lock state, scans source, and rejects a hostile UAuthForm fixture. | Official GitHub advisory via Parallel; `NUXT-UI-GHSA-gj2h-2fpw-fhv9.json`; targeted critical test PASS | NOT REACHABLE, FAIL-CLOSED |

## External-source basis

- Docker Scout release asset metadata: <https://api.github.com/repos/docker/scout-cli/releases/tags/v1.23.1>
- Docker Scout installation documentation: <https://docs.docker.com/scout/install/>
- GitHub Actions security guidance: <https://docs.github.com/en/actions/security-for-github-actions/security-guides/security-hardening-for-github-actions>
- Nuxt UI advisory: <https://github.com/advisories/GHSA-gj2h-2fpw-fhv9>
- Context7 and Parallel were used to consult current official documentation. Tavily remains unavailable because its MCP route requires OAuth authorization; no claim was filled from model memory in its place.

## Residual risk and verdict

The local network did not complete the 62 MB Scout asset download within two command timeouts, so a local byte-for-byte hash is not claimed. The official release API metadata is recorded, and both workflows fail closed on the exact digest before installation. The full critical suite passed against a disposable dedicated test database with 199/199 tests, zero skips, portable evidence, and zero database-container residue. The image gate reports one direct moderate npm advisory; it is accepted only as demonstrably unreachable under the explicit SPA/no-UForm guard above, not silently waived. A clean-commit image/dev-stand run and the successor CI workflow remain required.

Verdict: **IMPLEMENTATION REVIEW PASS; RUNTIME/CI RELEASE VERDICT PENDING.**
