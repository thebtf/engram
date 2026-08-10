# Deployment Guide

Engram production deployment is a three-image stack: PostgreSQL, server, and
operator console. Production Compose intentionally has no moving image default.
Every deployment requires the retained post-readback `publication-result.json`
from the one release workflow execution that published the selected images. It
is deployment authority, not merely release evidence.

The retained record is an explicit local trust boundary: keep it at an
operator-owned, access-controlled path such as `/secure/evidence`. Anyone able
to modify both this record and the deployment dotenv holds deployment authority.
The wrapper validates the record and binds its read-back digests to the dotenv;
it does not claim to verify a cryptographic signature over the local file.

## Immutable image selection

Set all three identities before parsing either Compose file:

```dotenv
ENGRAM_SERVER_IMAGE=ghcr.io/thebtf/engram@sha256:<server-manifest-digest>
ENGRAM_OPERATOR_IMAGE=ghcr.io/thebtf/engram-operator-console@sha256:<operator-manifest-digest>
ENGRAM_POSTGRES_IMAGE=ghcr.io/thebtf/engram-postgres@sha256:<postgres-manifest-digest>
POSTGRES_PASSWORD=<unique-secret>
ENGRAM_AUTH_ADMIN_TOKEN=<separate-operator-secret>
```

The same release is also discoverable through exactly two tags per image:

- the full canonical release tag, for example `v6.43.0-rc.1`;
- `sha-<full-40-hex-release-commit>`.

`main`, `latest`, branch, major, and minor aliases are not release identities.
Do not replace the digest-pinned values above with moving tags.

Start the pull-only stack through the deployment wrapper. Pass both the retained
publication record and an optional dotenv snapshot (default: `.env`). The wrapper
requires `python3` for strict JSON parsing; it never evaluates JSON or dotenv content as shell code.


```bash
PUBLICATION_RESULT=/secure/evidence/publication-result.json
ENV_FILE=.env
bash deploy/image-preflight.sh --publication-result "$PUBLICATION_RESULT" --env-file "$ENV_FILE"
```

Before Docker is invoked, the wrapper parses JSON without evaluation and rejects
records unless they are schema 1, have the canonical `v*` release version and
full lowercase commit, carry the single-writer/trust-boundary and acceptance
manifest fields, and contain exactly the six canonical version/commit image
destinations. Each dotenv image must equal its canonical repository plus the
matching publication manifest digest; a mixed-release three-digest file cannot
pass.

It copies the selected dotenv file to a mode-`0600` snapshot and treats that
snapshot as the entire Compose interpolation boundary. Before any Docker call it
finds every `${VAR}` reference in `deploy/docker-compose.runtime.yml`, rejects a
different inherited value for a snapshot-defined key, and unsets every referenced
key—including keys absent from the snapshot—so the parent process cannot inject
configuration. It also rejects `COMPOSE_FILE`, `COMPOSE_PATH_SEPARATOR`,
`COMPOSE_PROJECT_NAME`, `COMPOSE_PROFILES`, `COMPOSE_ENV_FILES`,
`COMPOSE_DISABLE_ENV_FILE`, and `COMPOSE_PROJECT_DIRECTORY`; these can select a
different Compose source, project, profile, or dotenv source. Docker client
transport settings such as `DOCKER_HOST`, `DOCKER_CONTEXT`, and TLS variables
are preserved. The wrapper runs `docker compose config --quiet` to fully
interpolate, resolve, and validate without printing resolved configuration, then
runs `pull` and `up` with the same sanitized environment and frozen snapshot:

The root `docker-compose.yml` uses the same required image variables and also
contains local build definitions. The image acceptance gate sets them to exact
local image IDs before exercising the stack. A direct source build also sets
`ENGRAM_BUILD_VERSION` to the canonical release version or
`sha-<full-40-hex-commit>`; the Dockerfile rejects an absent or untrusted value.

## Release activation prerequisite

The release workflow fails before registry login unless GitHub reports exactly
one active tag-target repository ruleset with all of these properties:

- include is exactly `refs/tags/v*` and exclusions are empty;
- deletion and non-fast-forward updates are blocked;
- bypass actors are empty.

It also requires exactly one active branch-target ruleset for
`refs/heads/main` with no exclusions, deletion and non-fast-forward protection,
strict status checks, and exactly one `authority-guard` status owned by GitHub
Actions (`integration_id: 15368`). The only recovery bypass is exactly one
`User` actor, ID `7106373`, in `pull_request` mode. A missing integration ID, a
same-name status from another integration, zero/duplicate bypass actors, or an
always-bypass actor stops the release.

The repository currently needs this operator bootstrap before release
publication can activate. The workflow does not create or weaken repository
rules.

GitHub Container Registry does not provide this project with an atomic tag CAS
contract. Engram therefore uses a repository-controlled single-writer model.
The default-branch publisher uses two fresh runners:

1. `prepare-release` has only `contents: read`. Trusted default-branch code
   validates the event/tag/rulesets, checks out the exact candidate without
   persisted credentials, builds from a tracked-file-only Git archive, runs the
   full scan/runtime gate, and uploads one immutable five-file image-data
   bundle. The bundle exposes the upload artifact ID and SHA-256 digest.
2. `publish-images` alone has `actions: read` and `packages: write`. It checks
   out only trusted default-branch code, repeats the full event/API/tag/main
   provenance check, requires the current workflow run to contain exactly the
   expected artifact ID/name/digest and no other artifact, downloads by ID,
   rejects extra files, links, traversal, or checksum drift, and loads the three
   exact image archives as data without running candidate code.
3. The fresh publisher compares all six destinations before login, logs in only
   after every check passes, re-compares before the first write, publishes only
   absent exact identities, reads back all six, logs out, removes the isolated
   Docker credential directory, validates the exact evidence envelope, and
   only then uploads publication evidence.

A package administrator or external PAT can still mutate package state outside
the repository workflow; that is an explicit operational trust boundary and
must be restricted by organization policy. The repository currently lacks the
required immutable-tag and strict `authority-guard` rulesets, so release
publication remains fail-closed until an operator installs both.

## Runtime contract

- PostgreSQL: version 17.10, pgvector 0.8.1, UID/GID 70, persistent
  `/var/lib/postgresql/data`.
- Server: UID/GID 65532, read-only root filesystem, persistent
  `HOME=/var/lib/engram`, semantic health probe on `/api/ready`.
- Operator console: UID/GID 65532, read-only root filesystem,
  `NUXT_OPERATOR_API_TARGET=http://server:37777`, semantic proxied readiness.
- Every service drops all capabilities and enables `no-new-privileges`;
  bounded tmpfs mounts cover runtime-only writable paths.

The server `/health` endpoint is liveness. During failed asynchronous
initialization it can remain HTTP 200 while reporting an error. Docker health
uses `/api/ready` and accepts only the exact JSON object:

```json
{"status":"ready"}
```

Verify the running stack:

```bash
docker compose -f deploy/docker-compose.runtime.yml ps
curl --fail http://localhost:37777/health
curl --fail http://localhost:37777/api/ready
curl --fail http://localhost:3000/api/ready
```

## Backup, upgrade, and rollback

Before upgrade, create a PostgreSQL logical backup and prove it restores into a
fresh database. Keep the named data volumes when recreating containers.

### One-time migration from PostgreSQL UID 999

Volumes created by the former `pgvector/pgvector:pg17` image are owned by UID
999. The new image runs as UID/GID 70 and intentionally fails closed until the
volume is migrated. Stop the stack without deleting volumes, identify the
Compose `pgdata` volume (normally `<project>_pgdata`), then run the bounded
ownership repair with the exact new PostgreSQL image identity:

```bash
ENV_FILE=.env
POSTGRES_MIGRATION_IMAGE='ghcr.io/thebtf/engram-postgres@sha256:<postgres-manifest-digest>'
docker compose --env-file "$ENV_FILE" -f deploy/docker-compose.runtime.yml down
docker volume ls --format '{{.Name}}'
docker run --rm --user 0:0 \
  --cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE --cap-add FOWNER \
  --security-opt no-new-privileges:true --entrypoint /bin/sh \
  -v <project>_pgdata:/var/lib/postgresql/data \
  "$POSTGRES_MIGRATION_IMAGE" \
  -c 'chown -R 70:70 /var/lib/postgresql/data && chmod 0700 /var/lib/postgresql/data'
docker run --rm --user 0:0 --cap-drop ALL --entrypoint /bin/sh \
  -v <project>_pgdata:/var/lib/postgresql/data \
  "$POSTGRES_MIGRATION_IMAGE" -c "stat -c '%u:%g:%a' /var/lib/postgresql/data"
bash deploy/image-preflight.sh --publication-result "$PUBLICATION_RESULT" --env-file "$ENV_FILE"
```

The `stat` command must print `70:70:700`. Never add `-v` to the `down` command;
that would delete the database volume. The critical runtime suite proves both
the pre-migration fail-closed behavior and marker preservation after migration.

Rollback uses the three digest identities from the preceding accepted release
manifest. Change all three `ENGRAM_*_IMAGE` values as one set in a selected
dotenv file, then run `bash deploy/image-preflight.sh --publication-result "$PUBLICATION_RESULT" --env-file "$ENV_FILE"` to recreate
the stack without deleting named volumes. Verify PostgreSQL version/vector,
retained data, direct readiness, and operator-proxied readiness.

## Reproduce image acceptance

Run from a clean candidate commit on a Docker host with Trivy:

```powershell
pwsh ./scripts/production-gates/build-and-scan-images.ps1 `
  -Mode BuildAndScan `
  -ServerTag engram:r2-server `
  -OperatorTag engram:r2-operator-console `
  -PostgresTag engram:r2-postgres `
  -Platform linux/amd64 `
  -ArtifactRoot .agent/reports/evidence/production-ready/image-remediation-r2 `
  -Version sha-<full-40-hex-candidate-commit> `
  -NoAllowlist
```

This performs no-cache builds from tracked files only, exact-ID HIGH/CRITICAL
scans without allowlists, the permanent runtime/negative matrix, PostgreSQL
recreation/durability proof, and prefix-scoped cleanup verification. It does
not push a registry tag.
