# Investigation: Lightning CSS Linux native binding missing after successful npm ci

Status: RESOLVED_IMPLEMENTATION_IMAGE_GATE_PENDING

## Problem

Observed: an exact-head Linux/amd64 Docker image gate ran `npm ci`, reported 839 packages and exit 0, then Nuxt failed with `Cannot find module '../lightningcss.linux-x64-gnu.node'`.

Expected: a successful clean lock install must either materialize every build-required native package or fail before the build consumes it.

When it started: intermittent on exact commit `9282f324`; the immediately preceding exact commit `5722882e` built from the same package manifest/lock and passed with 840 packages.

Known failed attempts:

1. Explicit exact `npm install --no-save --package-lock=false` after the injected omission failed with npm 10 `ERR_INVALID_ARG_TYPE` because Arborist consumed the inconsistent hidden lock.
2. Restoring the committed root lock and deleting the hidden lock before exact install did not complete within 300 seconds. Those attempted Dockerfile/test changes were fully reverted before the final class-level implementation.

Constraints: retain reproducible locked builds; do not make a Linux-only package a required root dependency that breaks Windows development; do not accept a blind retry as proof; no secret or deployment scope expansion.

## Contradiction

The committed lock fully contains `lightningcss-linux-x64-gnu@1.32.0`, the builder is linux/x64 with optional packages enabled, and `npm ci` exits 0; nevertheless npm can produce a 839-package tree with that exact runtime-required binding absent.

## Evidence Ledger

| Fact ID | Claim | Source | Evidence tag | Iteration |
|---|---|---|---|---|
| F1 | Exact gate `5722882e` passed with 840 packages and complete runtime/cleanup proof. | `.agent/tmp/image-gate-5722882e/final-image-set.json` and build log | DIRECT | 1 |
| F2 | Exact gate `9282f324` failed after npm installed 839 packages with exit 0; Nuxt emitted the exact missing GNU native module stack. | `.agent/tmp/image-gate-9282f324/server/build.log` | DIRECT | 1 |
| F3 | The commits have byte-identical operator package manifest/lock inputs. | `git diff --name-status 5722882e..9282f324` | DIRECT | 1 |
| F4 | The lock contains Lightning CSS 1.32.0, all eleven platform packages, and the Linux x64 GNU parent edge/package. | structural JSON query over `package-lock.json` | DIRECT | 1 |
| F5 | Two clean pinned Node 22 builder containers report linux/x64, npm 10.9.8, empty omit config, 840 packages, and successful native resolution. | disposable Docker probes | DIRECT | 1 |
| F6 | Making only the target native tarball unreachable yields 839 packages, npm exit 0, then the identical missing-module stack. | disposable target-only lock probe | DIRECT | 1 |
| F7 | npm Arborist intentionally marks failed optional platform packages inert; npm #4828/#8320 document silent platform-package omission, including npm 10/11; Lightning CSS #567 links this exact stack to the class. | Context7 `/npm/cli`; official GitHub issues via Parallel | DIRECT EXTERNAL | 1 |
| F8 | The failed gate cleaned its build context, secret files, containers, volumes, and networks. | FAIL manifest plus live prefix inventory | DIRECT | 1 |
| F9 | Full SocratiCode graph was unavailable in the bounded window; partial embedding stalled near 55%, and Engram semantic fallback returned zero results. | live tool status/results | TOOL BLOCKER | 1 |
| F10 | aimux independent reasoning was unavailable due non-retryable Loom SQLite capability mismatch. | live aimux result | TOOL BLOCKER | 1 |
| F11 | Nine locked parent packages rely on platform-specific Linux x64 optional binaries; a Lightning-only repair would leave the failure class open. | structural lock query | DIRECT | 2 |
| F12 | npm 10.9.8 officially retries idempotent registry reads on network/5xx errors; defaults are two retries, factor 10, 10s minimum and 60s maximum delay. | official npm 10 config via Parallel | DIRECT EXTERNAL | 2 |
| F13 | Node 22 diagnostic report exposes `header.glibcVersionRuntime`, enabling runtime selection of glibc versus musl lock entries. | official Node 22 docs via Context7 | DIRECT EXTERNAL | 2 |
| F14 | The final verifier rejects the injected 839-package tree with `lightningcss-linux-x64-gnu: missing (expected 1.32.0)` before Nuxt. | disposable target-only lock probe plus `verify-native-optional-deps.mjs` | DIRECT | 3 |
| F15 | A clean pinned Node 22/npm 10.9.8 Linux/glibc install materializes 840 packages and the verifier checks 12 applicable optional-native packages. | disposable Docker positive probe | DIRECT | 3 |
| F16 | Hostile self-tests cover missing package, wrong version, OS-only/CPU-only constraints, CPU exclusion, x64 and ARM glibc/musl selection, and cleanup. | `node apps/operator-console/scripts/verify-native-optional-deps.mjs --self-test` | DIRECT | 3 |
| F17 | The full critical runner passes 201/201 with zero skips, zero non-zero child commands, portable paths, and zero disposable-DB residue. | `sol-synthesis-20260713-observability-r2/summary.json` plus live residue/path scans | DIRECT | 3 |
| F18 | One absolute path remained in the nested parser stdout after the first green run; changing the parser to emit `ConvertTo-EvidencePath` and rerunning removed every drive-letter match. | first and replacement critical evidence scans | DIRECT NEGATIVE/POSITIVE | 3 |

## Mosaic Board

| Hypothesis | Prediction | Probe | Direct/Proxy | Status | Explains | Does not explain | Next gap |
|---|---|---|---|---|---|---|---|
| H1: committed lock omits the target | target key/edge absent | structural lock parse | Direct | REFUTED | none | F4 | none |
| H2: target fetch/extract can fail silently because npm treats the binding as optional | target-only failure gives npm exit 0 + 839 + same stack | injected target URL probe | Direct | CONFIRMED | F2, F4, F6, F7 | exact external cause of the original one-off fetch failure | runtime/log policy |
| H3: wrong OS/CPU or omit config | builder reports mismatch or omit=optional | two clean pinned containers | Direct | REFUTED | none | F5 | none |
| H4: advisory guard changed dependency inputs | package files differ between heads | exact git diff | Direct | REFUTED | none | F3 | none |
| H5: exact post-ci install can safely repair the missing binding in-place | injected omission is repaired and require passes | two repair probes | Direct | REFUTED for npm 10 in-place Arborist route | attempted recovery | ERR_INVALID_ARG_TYPE / timeout | choose fail-closed policy or different architecture |

## Iteration Log

| Iteration | New evidence | Decision | Next route |
|---|---|---|---|
| 1 | F1-F10; H2 confirmed, H1/H3/H4 refuted; two in-place repair routes failed | CONTINUE | Decide between bounded fetch resilience plus immediate required-binding assertion, or a larger build dependency architecture change. Do not retry the failed install route. |
| 2 | F11-F13; package-specific recovery rejected in favor of lock-driven class guard | DECIDE | Implement bounded registry retries plus a general exact platform-native tree verifier; no post-ci install or hidden-lock mutation. |
| 3 | F14-F18; injected negative, clean positive, hostile self-test, and full critical acceptance all pass | RESOLVE IMPLEMENTATION | Commit the batch, then rerun the exact-commit clean image gate that originally exposed the defect. |

## Decision

DECISION: FIX_PATH

Root cause at the project boundary is confirmed: the Docker build equates npm success with a complete platform-native tree even though npm intentionally allows an optional native fetch to fail. The exact upstream/network reason for the original single fetch failure is no longer observable because npm hid it and the failed layer is gone; that unknown does not change the reproduced boundary defect.

Implement a class-level build invariant:

1. Keep `npm ci` as the immutable lock installer, explicitly include optional packages, and use five bounded idempotent-fetch retries with factor 2 and 1s/30s delay bounds.
2. Immediately run a lock-driven verifier that selects every optional package applicable to the current OS, CPU, and Linux libc; require its installed `package.json` and exact locked version.
3. Fail with the exact missing/mismatched package list before Nuxt starts. Do not mutate the root/hidden lock and do not run a second npm install.
4. The verifier owns deterministic hostile self-tests for missing package, wrong version, platform/CPU exclusion, and glibc/musl selection. The critical Go contract executes that self-test and binds the Dockerfile invocation/retry policy.

This path does not pretend a permanently unavailable registry can produce a build. It makes transient recovery bounded and converts npm's false-success tree into an explicit, early, class-wide failure. Follow-up route: TDD implementation, exact injected omission replay, clean operator build, then the full image gate.

## Resolution

The class-level invariant is implemented and accepted at the source/test boundary. The Dockerfile uses immutable `npm ci --include=optional` with bounded fetch retries and invokes the verifier before copying the rest of the application. The verifier derives applicable packages from the committed lock instead of hard-coding Lightning CSS. The original failure shape and the clean positive shape both have direct runtime proof. Release acceptance remains deliberately open until the clean exact-commit image gate passes.
