# Production Testing Playbook — Engram Images

Run this playbook before image publication. It validates the same three-image
contract customers deploy: `postgres`, `server`, and `operator-console`.
This is the image acceptance lane, not a substitute for the repository-wide
critical suite or customer-mode release emulation.

## Prerequisites

- Docker Engine and Compose v2
- Trivy
- Go 1.26.6+
- Node.js 22+
- PowerShell 7+

All disposable resources must use a unique prefix. A passing run ends with zero
matching containers, volumes, and networks.

## S1 — Build and scan the exact candidates

Run:

```powershell
pwsh ./scripts/production-gates/build-and-scan-images.ps1 `
  -Mode BuildAndScan `
  -ServerTag engram:prc-server `
  -OperatorTag engram:prc-operator-console `
  -PostgresTag engram:prc-postgres `
  -Platform linux/amd64 `
  -ArtifactRoot .agent/reports/evidence/production-ready/image-remediation-r2 `
  -Version sha-<full-40-hex-candidate-commit> `
  -NoAllowlist
```

Expected:

- every build uses `--pull --no-cache`;
- the Docker build context is produced by `git archive HEAD`, contains tracked
  files only, and contains no `.git` metadata or checkout credentials;
- the manifest records the Dockerfile hashes, pinned bases, package versions,
  exact image IDs, and SARIF hashes;
- each accepted image records OCI source, full revision, and version labels;
- the gate attempts scans for server, operator-console, and postgres before one
  aggregate HIGH/CRITICAL verdict; an accepted run has one zero-finding SARIF
  per image, while a scanner error retains its per-image log and fails closed;
- every exact-ID scan has zero HIGH and zero CRITICAL findings;
- no scanner allowlist, ignore, suppression, or exception input exists;
- the operator lock audit has no HIGH/CRITICAL or picomatch/sigstore finding.

## S2 — Positive three-image runtime

The gate and permanent critical tests must prove:

1. PostgreSQL becomes healthy as UID/GID 70 on a read-only root filesystem.
2. The server becomes healthy as UID/GID 65532 with a persistent writable
   `/var/lib/engram` volume and read-only root filesystem everywhere else.
3. The operator console becomes healthy as UID/GID 65532 and proxies to the
   exact `NUXT_OPERATOR_API_TARGET=http://server:37777` backend.
4. The PostgreSQL and server volume roots retain their required owner and mode:
   `70:70:700` and `65532:65532:700` respectively.
5. The server-created `.engram/settings.json` remains `65532:65532:600` and
   byte-identical across container restart.
6. Server `/health` is reachable as liveness.
7. Direct and proxied `/api/ready` both return exact `{"status":"ready"}`.
8. The operator root references generated Nuxt assets and at least one asset is
   retrievable.
9. Server and operator recover after restart.

Root HTTP 200 alone is never acceptance.

## S3 — PostgreSQL version, migration, and durability

Expected:

- `SHOW server_version` is exactly `17.10`;
- `CREATE EXTENSION vector` succeeds and its version is exactly `0.8.1`;
- server startup creates the application schema;
- a vector-bearing marker survives removal of the first PostgreSQL container
  and creation of a second container on the same named volume;
- an existing UID `999:999` volume fails closed before the documented bounded
  ownership migration, then starts as UID `70:70` without losing the marker;
- `pg_dump` plus restore into a fresh database preserves the marker;
- a tmpfs-only PGDATA fixture demonstrably loses the marker and is rejected as
  a deployment pattern;
- `LANG=en_US.UTF-8` never reaches ready, while the image contract remains
  `LANG=C.UTF-8` and `LC_ALL=C.UTF-8`.

## S4 — Server fail-closed matrix

Each fixture must remain non-healthy:

- missing persistent HOME volume under a read-only root filesystem;
- empty HOME override;
- root-owned mode-0500 HOME volume;
- injected database initialization failure.

For initialization failure, `/health` may intentionally remain HTTP 200 with
`status:error`; Docker health must still fail because it uses `/api/ready`.

## S5 — Operator backend fail-closed matrix

Each fixture must remain non-healthy:

- a stale legacy target variable is set but the canonical target is wrong;
- canonical target is missing/empty;
- backend is unreachable;
- backend times out;
- `/api/ready` returns malformed JSON;
- `/api/ready` returns HTTP 200 with `{"status":"error"}`;
- backend root returns HTTP 200/ready but `/api/ready` is absent.

This matrix proves that a rendered root page cannot mask a broken API target.

## S6 — Compose and immutable publication identity

Expected:
- Isolated plugin smoke starts without errors
- The assistant or `tools/list` surface lists tools beyond `loom_*` —
  e.g., `mcp__engram__store`, `mcp__engram__issues`,
  `mcp__engram__vault`, etc.
- The token field maps to the `ENGRAM_TOKEN` env var (FR-3)
Validate both compose files with explicit `ENGRAM_SERVER_IMAGE`,
`ENGRAM_OPERATOR_IMAGE`, and `ENGRAM_POSTGRES_IMAGE` values from one release
manifest. Parsing must fail when any of the three is missing. Production has no
`:main`, `latest`, branch, major, or minor default.

The release workflow must prove:

- main and manual dispatch are verification-only and have no package-write,
  registry-login, or push path;
- publication runs only for a strict canonical `vMAJOR.MINOR.PATCH` tag with an
  optional valid prerelease and never from manual dispatch;
- the tag peels to the workflow commit and exactly one active no-bypass tag
  ruleset protects `refs/tags/v*` from deletion and non-fast-forward updates;
- protected `main` has exactly one active strict ruleset requiring
  `authority-guard` from `integration_id: 15368`, plus exactly one recovery
  bypass (`User` ID `7106373`, `pull_request` mode); same-name spoof checks,
  missing integration IDs, and zero/duplicate/wrong bypass actors fail;
- raw Git/ref/context values never enter inline shell source;
- all actions are pinned to full commit SHAs and every checkout has
  `persist-credentials: false`;
- candidate build/test/runtime and package publication occur on different fresh
  runners. The prepare job is `contents: read` only; only the publisher has
  `actions: read` plus `packages: write`, and it never checks out or executes
  candidate code, tests, workflow files, scripts, actions, or containers;
- prepare uploads exactly one immutable, uniquely named five-file payload for
  the current publisher workflow run and exports its artifact ID and SHA-256
  digest. Publish REST-censuses the current run before download and rejects a
  missing, extra, duplicate, expired, wrong-run, wrong-ID, or wrong-digest
  artifact; download is by artifact ID only;
- trusted code rejects payload directories, extra files, symlink/reparse
  entries, path traversal, archive checksum/size drift, manifest drift, and any
  image identity not bound to the revalidated release commit and version;
- all six destinations (full version plus `sha-<full commit>` for each image)
  are absent or already bound to the exact scanned config digest before the
  first push; one mismatch fails the run without a write;
- after any writes, all six destinations are read back and recorded with their
  manifest digests;
- the Docker registry credential lives in a unique `RUNNER_TEMP` directory,
  is logged out and erased before the exact trusted evidence envelope is
  validated and uploaded. No post-login path is under the candidate checkout.

GitHub Container Registry is not claimed to provide atomic tag compare-and-swap.
The remaining package-admin/PAT mutation path is an explicit external
operational trust boundary.

## Evidence and verdict

Required evidence root:

```text
.agent/reports/evidence/production-ready/image-remediation-r2/
```

The run is PASS only when `final-image-set.json` reports:

- `status: PASS`;
- exact IDs for all three images;
- zero HIGH/CRITICAL results for all three SARIF files;
- every boolean runtime proof field true, migration table count at least 40,
  and all six core tables present;
- cleanup status PASS with empty container, volume, and network arrays.

Any missing artifact, unexpected skip, retained probe resource, or scanner
exception is a release blocker.

For scheduled freshness evidence, retain the post-publication rescan summary for
the latest released `server`, `operator-console`, and `postgres` digests. The
first run must start within 24 hours of publication; later evidence must be no
older than 36 hours by `started_at` and `completed_at`. A clean run retains
per-image SARIF/log outputs. A tag, scanner, or database failure retains the
affected image's error/log instead and fails the aggregate verdict. The rescan
attempts all three images and is read-only (no login, Docker pull, candidate
execution, or registry write). Missing, stale, failed, or finding-bearing
evidence blocks rollout and continued deployment, not initial publication of
the immutable digests. A new HIGH/CRITICAL CVE blocks the release owner until
the bounded maintenance-PR, rebuild/scan/runtime, and patch-release loop in the
release protocol completes.
