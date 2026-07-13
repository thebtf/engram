# Release Integration R1 Security Review

## Scope and classification

This is an S4 change because it modifies production image workflows, CI-downloaded tooling, runtime credential transport, cleanup behavior, and release evidence. This review accepts only the implementation batch; it does not assert production readiness or authorize a release. The separate A17/B17 authority batch remains mandatory.

## Assets and trust boundaries

- Runtime PostgreSQL password, admin token, vault key, and bootstrap capability.
- Trusted GitHub Actions execution and the Trivy binary downloaded during a run.
- Docker bind mounts that cross from the runner host into containers.
- Critical-suite and OTLP evidence consumed by later release decisions.
- Cleanup boundaries for runner temporary directories, processes, containers, and compose resources.

## Threat review

| Threat | Control | Evidence | Result |
|---|---|---|---|
| Trivy archive substitution | Exact v0.72.0 URL and published SHA-256; `sha256sum --check --strict` before extraction; installed binary executes `trivy --version` | Official GitHub release API reports Linux archive SHA-256 `bbb64b9695866ce4a7a8f5c9592002c5961cab378577fa3f8a040df362b9b2ea`; the downloaded Windows archive independently matched its published SHA-256 `ed3cf122060f61818fe1f735fd97557954e16e10bc8b058af9852271cf2e91b3` | PASS locally, CI replay pending |
| Scanner requires hidden credentials | The canonical scanner must analyze immutable local Docker image IDs, fetch current vulnerability data without project/package credentials, emit SARIF, and distinguish findings (exit 2) from operational failure | Hermetic Linux Scout v1.23.1 reproduced the remote Docker-ID login failure even with `DOCKER_SCOUT_OFFLINE=true`; official Trivy v0.72.0 scanned the same exact image ID through `--image-src docker` with fresh anonymous DB download, SARIF 2.1.0, zero findings, and exit 0 on both Linux and Windows | Scout rejected; Trivy PASS locally, remote replay pending |
| Credential disclosure in compose environment | Parent environment carries only secret-file paths; files are bind-mounted at `/run/secrets/*`; plaintext credential variables are explicitly rejected by self-test and runtime inspection | `run-db-suite.ps1 -SelfTest` and critical static contract | PASS |
| Weak or reused credentials | Four independent 256-bit values are generated with `RandomNumberGenerator`; default, blank, missing, and reused values are rejected | dev-stand config contract and self-test hostile cases | PASS |
| Host disclosure versus non-root container readability | Compose file secrets are bind mounts and ignore uid/gid/mode remapping. Unix roots are 0700 while files are 0644, so other host users cannot traverse the private root but UID 70/65532 can read the read-only in-container mount. Windows roots/files use protected owner + LocalSystem ACLs. | Official Docker Compose services reference; shared `compose-secret-access.ps1`; direct Windows owner/System ACL assertion and UID 65532 container read probe PASS | PASS |
| Unsafe recursive cleanup | Cleanup resolves an absolute path, requires the OS temp-root boundary and exact Engram-owned prefix before recursive removal | image and dev-stand cleanup functions | PASS |
| Partial secret initialization bypasses cleanup | The temporary root is registered immediately after creation; the outer `finally` selects the completed or pending root and verifies deletion | `PendingImageSecretRoot` and `PendingDevStandSecretRoot` are required by the critical static contract | PASS |
| False residue evidence | OTLP verification snapshots processes and Docker containers before and after the test and fails on newly created residue | live OTLP run: all 13 required tests pass, zero new process/container residue | PASS |
| Host topology disclosure or non-portable proof | Critical command records store executable leaf names, repository-relative paths, and `working_directory: "."` | critical runner self-test | PASS |
| Secrets accidentally committed in changed lines | gitleaks 8.30.0 scans added diff lines plus untracked/ignored batch artifacts with full redaction | 149,061 UTF-8 bytes scanned from batch base; no leaks found | PASS |
| Critical isolation test silently skipped or pointed at production | The test rejects missing, production, or staging DSNs; acceptance uses a disposable localhost-only PostgreSQL 17 database explicitly named `engram_critical_test` | 199/199 passed, zero skips; the disposable DB container was removed in `finally` and residue is zero | PASS |
| GHSA-gj2h-2fpw-fhv9 credential leak through pre-hydration GET | Official advisory requires SSR markup from `UForm`/`UAuthForm`; the console is explicitly `ssr: false` and has no affected component usage. A critical guard pins the reviewed lock state, scans source, and rejects a hostile UAuthForm fixture. | Official GitHub advisory via Parallel; `NUXT-UI-GHSA-gj2h-2fpw-fhv9.json`; targeted critical test PASS | NOT REACHABLE, FAIL-CLOSED |
| Silent omission of runtime-required native optional package | Immutable install explicitly includes optionals and has bounded idempotent-fetch retries; a lock-driven OS/CPU/libc verifier requires every applicable package and exact locked version before Nuxt | injected 839-package tree fails with the exact missing identity; clean Linux/glibc tree installs 840 and checks 12; hostile verifier self-test PASS | PASS, FAIL-CLOSED |
| Legacy plaintext or conflicting duplicate credentials coexist with `_FILE` variables | Runtime `docker inspect` proof requires exactly one expected `_FILE`/redacted entry and rejects duplicate values plus `POSTGRES_PASSWORD`, `DATABASE_DSN`, `ENGRAM_AUTH_ADMIN_TOKEN`, `ENGRAM_ENCRYPTION_KEY`, or `ENGRAM_VAULT_KEY` | `run-db-suite.ps1 -SelfTest` hostile mixed and duplicate environment cases | PASS, FAIL-CLOSED |
| False OTLP residue caused by a fixed cleanup delay or PID reuse | A bounded 5-second polling loop repeatedly measures process/container residue, keys processes by PID + start time, and records attempts/timeout status; absence of Docker still fails closed because evidence claims `container_residue_checked=true` | live OTLP smoke: 13/13, one poll, no timeout, zero process/container residue | PASS |

## External-source basis

- Trivy v0.72.0 release asset metadata: <https://api.github.com/repos/aquasecurity/trivy/releases/tags/v0.72.0>
- Trivy image CLI contract (`--image-src docker`, severity, exit code, SARIF): <https://trivy.dev/docs/v0.72/docs/references/configuration/cli/trivy_image/>
- Docker Scout offline-mode documentation and local-image contract, consulted to reproduce and reject the Linux authentication dependency: <https://docs.docker.com/scout/how-tos/configure-cli/> and <https://docs.docker.com/reference/cli/docker/scout/cves/>
- GitHub Actions security guidance: <https://docs.github.com/en/actions/security-for-github-actions/security-guides/security-hardening-for-github-actions>
- Nuxt UI advisory: <https://github.com/advisories/GHSA-gj2h-2fpw-fhv9>
- Context7 and Parallel were used to consult current official documentation. Tavily remains unavailable because its MCP route requires OAuth authorization; no claim was filled from model memory in its place.
- Native dependency basis: <https://github.com/npm/cli/issues/4828>, <https://github.com/npm/cli/issues/8320>, <https://github.com/npm/cli/pull/8184>, and <https://github.com/parcel-bundler/lightningcss/issues/567>.
- Compose secret bind semantics: <https://docs.docker.com/reference/compose-file/services/#secrets>.

## Residual risk and verdict

The remote Linux runner and a hermetic local Linux container both proved that Scout v1.23.1 is unsuitable for this no-package-credential lane: it stops at Docker-ID authentication even when the target is an immutable local image and offline mode is enabled. Trivy v0.72.0 is therefore the canonical scanner, not a fallback. Its official Linux and Windows archives matched the published SHA-256 values, and both platforms produced SARIF 2.1.0 for the same exact local image without package credentials. A known vulnerable exact-ID fixture produced exit 2 plus one result; a missing exact ID produced operational exit 1 without SARIF. The full critical suite and complete dev-stand lifecycle passed at `70783aeca6bda80f8740d510486a02f0ce2488c0`; the new Trivy exact-head image/runtime and remote CI replays remain mandatory before release acceptance.

Verdict: **IMPLEMENTATION REVIEW PASS; RUNTIME/CI RELEASE VERDICT PENDING.**
