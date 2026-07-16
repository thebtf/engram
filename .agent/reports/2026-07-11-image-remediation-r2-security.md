# IMAGE-REMEDIATION-R2 security review

Classification: **S4 / Critical** because the change controls production image
publication, receives `packages: write`, processes cross-run artifacts, and
crosses GitHub Actions, Docker, and GHCR trust boundaries.

Verdict: **PASS WITH A BLOCKING EXTERNAL PRECONDITION for code review; production
publication remains prohibited until the required live GitHub rulesets exist
and an independent checker accepts the implementation**.

## Attack-surface map

| Surface | Untrusted or external input | Authority at the boundary |
| --- | --- | --- |
| `docker.yaml` | repository source from main/PR/manual | `contents: read` only |
| `workflow_run` bridge | triggering run metadata, candidate SHA, tag/ref, artifact metadata | trusted default-branch script; prepare job has `contents: read` |
| release payload | current-run artifact plus three Docker archives | publisher has no candidate checkout/execution; validates as data before login |
| GHCR registry | six existing remote refs and possible external package-admin writes | isolated short-lived `GITHUB_TOKEN` with `packages: write` only in publisher |
| runtime images | source tree, package lock, pinned bases/packages, database volume | non-root server/operator; explicit PostgreSQL ownership transition |
| GitHub rulesets | current repository control-plane state | external operator/admin boundary; validation is read-only and fail-closed |

Attack-surface confidence: **95%**. The review covers every changed workflow,
publication helper, Dockerfile, Compose runtime, and executable critical test.

## Security invariants and evidence

- **Least privilege:** verification and preparation cannot publish. Only the
  fresh publisher job has `actions: read` plus `packages: write`.
- **No privileged candidate execution:** the publisher checks out trusted main
  only. Candidate source is executed solely on the unprivileged prepare runner;
  persisted checkout credentials are disabled.
- **Injection resistance:** refs, SHAs, versions, repository identity, IDs, and
  digests are passed as typed script arguments and validated; raw event/context
  values are not interpolated into shell programs.
- **Artifact integrity:** a fresh REST census requires exactly one non-expired
  current-run artifact with the expected ID, name, and SHA-256 digest. Download
  is by numeric artifact ID, not name.
- **Path/deserialization safety:** the payload envelope is exactly five regular
  files under a trusted no-link output tree. Bundle names are basenames; outer
  tar entries must be regular files/directories; absolute/traversal paths and
  links are rejected; every byte count and SHA-256 is re-derived.
- **Registry race handling:** all six destinations are compared before login and
  again before the first push, then read back. The implementation explicitly
  does not claim atomic compare-and-swap semantics from GHCR and records external
  package administrators as a residual trust boundary.
- **Credential containment:** `DOCKER_CONFIG` is isolated, logout is mandatory,
  and the credential directory is removed before publication evidence upload.
- **Supply-chain pinning:** workflow actions and image sources are digest-pinned;
  PostgreSQL/pgvector package versions are exact; the build context comes from
  tracked files only and excludes Git metadata/credentials.
- **Container hardening:** server and operator use minimal distroless runtimes
  and non-root users. Dedicated compiled health checks avoid adding shell/curl
  attack surface. Runtime tests verify user, health, persistence, schema,
  restart/recreation, and version contracts.
- **Vulnerability evidence:** `govulncheck ./...` reports zero reachable Go
  vulnerabilities. Docker Scout reports zero HIGH/CRITICAL findings in every
  provisional image. Secret-pattern review found only empty environment
  pass-through variables and a per-run random PostgreSQL password, not embedded
  credentials.

## STRIDE review

| Threat | Control |
| --- | --- |
| Spoofing | Exact repository, workflow, run, SHA, tag, artifact ID/name/digest, and ruleset actor IDs are independently revalidated. |
| Tampering | Tracked-only build context, same-run immutable artifact census, byte/hash/size checks, exact image IDs, and post-push readback. |
| Repudiation | Acceptance manifest, release bundle, artifact census, pre-login plan, publication result, scanner SARIF, runtime JSONL, and cleanup census form an audit trail. |
| Information disclosure | Candidate code cannot observe publisher credentials; Docker credentials are isolated and erased; build context excludes `.git` and credentials. |
| Denial of service | Malformed/extra/expired artifacts and conflicting tags fail before login/write; cleanup is prefix-bounded and proves zero residue. |
| Elevation of privilege | Package authority exists in one fresh trusted-code job only and is conditional on exact protected-main/tag-ruleset policy. |

## Findings and residual risk

| # | Severity | Finding | Disposition |
| --- | --- | --- | --- |
| 1 | BLOCKER / external | Live repository rulesets do not provide the required exact protected-main authority guard, recovery bypass, or no-bypass `refs/tags/v*` protection. | Publication remains fail-closed before login. Repository administrators must configure and independently verify the rulesets. |
| 2 | MEDIUM / accepted scope | GHCR does not expose an atomic immutable-tag compare-and-swap primitive; an external package administrator can race repository workflow publication. | Explicit trust boundary; compare-before-write plus readback detects conflicts but cannot remove external administrator authority. Restrict and audit package-admin membership. |
| 3 | MODERATE / dependency | `npm audit` reports GHSA-gj2h-2fpw-fhv9 in direct `@nuxt/ui` 3.3.7; the affected `UAuthForm`/`UForm` components have no source usage in `apps/operator-console`. The available fix is a major upgrade. | Not reachable in the shipped console by current-source search. Track a separately tested Nuxt UI upgrade; do not introduce those components before upgrade. |
| 4 | LOW / hardening | The exact A11 payload carries hashes, scanner output, and an acceptance manifest but no signed OCI provenance/SBOM attestation. | Preserve as explicit future supply-chain hardening; immutable refs and byte-bound local evidence are the current contract. |

## Rollback and approval boundary

- Before package login there is no external side effect; any failed validation
  leaves only bounded runner-local artifacts, which cleanup removes.
- Once a previously absent immutable ref is pushed, rollback means retaining the
  immutable ref and publishing a new canonical version; force-moving or deleting
  a release tag/image is not an allowed rollback.
- The S4 human/control-plane gate is intentionally outside this maker: the user
  authorized implementation and verification, but live ruleset mutation and
  production publication were not performed.

Security finish state: **implementation may enter independent checker review;
publication may not proceed while Finding 1 remains open**.
