# Investigation: intermittent missing Lightning CSS Linux native package

Status: RESOLVED_IN_BATCH

## 1. Symptom

Verbatim error from the exact-head clean Docker image gate:

```text
ERROR Cannot find module '../lightningcss.linux-x64-gnu.node'
Require stack:
- /workspace/apps/operator-console/node_modules/lightningcss/node/index.js
```

The failure occurs during `npm run build` inside the Linux/amd64 Docker operator-console build stage after `npm ci` exits successfully.

## 2. Reproduction

Status: CONFIRMED INTERMITTENT

- `build-and-scan-images.ps1` on commit `5722882e` completed PASS; its clean build installed 840 packages.
- The same no-cache gate on commit `9282f324` failed during the server target's shared operator-console build; `npm ci` installed 839 packages and the Lightning CSS Linux native module was absent.
- The two commits do not change `package.json` or `package-lock.json`; the second commit adds only a Go critical guard and evidence.
- Target environment: Docker Desktop Linux/amd64 builder, Node 22 bookworm-slim, npm 10.9.8.

## 3. Competing hypotheses

| ID | Claim | Prediction | Disproving test | Disproving test result | Status |
|---|---|---|---|---|---|
| H1 | The local lock graph omits the platform-specific Lightning CSS package. | `package-lock.json` lacks `lightningcss-linux-x64-gnu` or its parent optional edge. | Parse the lock structurally. | REFUTED: lock contains Lightning CSS 1.32.0, all eleven platform variants, and the exact Linux x64 GNU edge/package. | REFUTED |
| H2 | A fetch/extract failure silently omitted the target because npm models the native package as optional. | Complete lock plus successful `npm ci` exit can coexist with 839 packages and a later native require failure; deliberately making only the target tarball unavailable should reproduce this shape. | Repeat clean installs, then mutate only the disposable target `resolved` URL to an unreachable endpoint and observe npm exit plus native require. | CONFIRMED: target-only unreachable URL produced 839 packages, `npm ci` exit 0, and the identical `Cannot find module '../lightningcss.linux-x64-gnu.node'` stack. | CONFIRMED |
| H3 | Dockerfile or environment selects the wrong platform/architecture or strips optional dependencies. | `process.platform/arch`, npm config, or Dockerfile flags show non-linux/non-x64 or `omit=optional`. | Capture platform/arch/npm config in the same builder image. | REFUTED: two pinned builder runs report linux/x64 and empty omit config, and resolve the GNU native package. | REFUTED |
| H4 | The Nuxt advisory guard commit changed the operator dependency graph. | Diff `5722882e..9282f324` includes package manifest/lock changes. | Inspect exact git diff names. | OBSERVED: diff contains no operator package manifest or lock change. | REFUTED |

## 4. Evidence classification

| Fact | Class | Source |
|---|---|---|
| Exact missing-module stack and Linux build stage | OBSERVED | `.agent/tmp/image-gate-9282f324/server/build.log` and failed gate output |
| Prior exact gate passed from the same dependency lock | OBSERVED | `.agent/tmp/image-gate-5722882e/final-image-set.json` status PASS |
| Successful run installed 840 packages; failed run installed 839 | OBSERVED | the two image-gate build transcripts |
| Package manifests were unchanged between exact commits | OBSERVED | scoped commit contents and prior PR diff |
| npm optional-dependency lock behavior may be causal | INFERRED | requires current official/upstream source confirmation |
| npm treats unsupported optional platform packages as inert rather than fatal | OBSERVED | Context7 current `/npm/cli` source excerpt from Arborist `build-ideal-tree.js` |
| npm/cli #4828 describes inconsistent package locks that silently omit another platform's optional native package | OBSERVED | Parallel fetch of official npm/cli issue #4828, updated 2026-06-30 |
| npm/cli #8320 reproduces the class on npm 10.9.2 and 11.4.1 with Node 22 | OBSERVED | Parallel fetch of official npm/cli issue #8320 |
| Lightning CSS maintainers linked the exact missing native-module error to npm/cli #4828 | OBSERVED | Parallel fetch of official parcel-bundler/lightningcss issue #567 |
| Engram memory contains prior project guidance for this failure | OBSERVED FALSE | live Engram recall returned zero memories |
| A fresh external structured-reasoning pass is available through aimux | OBSERVED FALSE | aimux returned non-retryable `CapabilityMismatch`: Loom SQLite session store unavailable |
| Clean Linux install environment is accidentally non-x64 or omits optional packages | OBSERVED FALSE | two disposable Node 22 bookworm-slim runs reported linux/x64, npm 10.9.8, empty omit config, 840 packages, and successful native package resolution |
| Failed image gate leaked build context, secret files, containers, volumes, or networks | OBSERVED FALSE | FAIL manifest records build-context cleanup true, cleanup PASS, secret files cleaned true; live prefixed residue query returned zero rows |
| npm/cli PR #8184 addresses the ideal/hidden-lock cause and shipped in npm 11.3.0 | OBSERVED | Parallel fetch of official merged PR #8184 and linked 11.3.0 release event |
| Upgrading npm alone is proven sufficient | OBSERVED FALSE | official npm/cli #8320 reports the related cross-platform lock failure on npm 11.4.1; an upgrade remains a hypothesis, not a verified fix |
| npm's successful exit proves the Linux native binding exists | OBSERVED FALSE | target-only failure injection completed `npm ci` successfully with 839 packages, then failed the native require with the original stack |

## 5. Hypothesis status log

- H4 REFUTED: the only change between the successful and failed exact heads is the critical advisory guard/evidence; operator dependency inputs are byte-identical.
- H2 strengthened by three upstream issue records, including npm 10/11 reproduction and the exact Lightning CSS error. H3 remained active pending lock inspection and a minimal repeated Linux install.
- H3 REFUTED: two clean pinned builder-container diagnostics prove linux/x64 selection, optional dependencies enabled, and successful native resolution. H2 remains possible because the exact full build produced 839 packages once while two subsequent minimal installs produced 840.
- H1 REFUTED for this repository: structural JSON inspection shows Lightning CSS 1.32.0 plus all platform packages, including `lightningcss-linux-x64-gnu`, are present in the committed lock.
- SocratiCode semantic index did not reach its graph phase within the bounded debug window (55%, batch 13); Engram semantic fallback returned zero results. The fallback was a narrow structural JSON query, not broad text search.
- H2 CONFIRMED: a disposable lock mutation changed only the target native package URL. npm exited 0 with 839 packages, and `require('lightningcss')` produced the same missing GNU binding stack. This directly reproduces the product failure class without changing the repository.
- A package-specific in-place repair was rejected after two failed probes (`ERR_INVALID_ARG_TYPE`, then a 300-second timeout) and a structural lock query found nine native-parent families. The repair target was widened to the whole applicable optional-native class.
- The final verifier selects every optional lock entry applicable to the running OS, CPU, and Linux libc, requires the exact installed version, and fails before Nuxt. A clean pinned Linux/glibc install checked 12 packages; the injected 839-package tree failed with the exact missing `lightningcss-linux-x64-gnu@1.32.0` identity.
- Full acceptance passed 201/201 critical tests with zero skips and repository-relative evidence after the parser stdout portability discrepancy was found and fixed.

## 6. Root cause

The Linux builder treats `lightningcss-linux-x64-gnu@1.32.0` as transitively optional, so npm 10.9.8 can exit successfully after failing to materialize that runtime-required binding; the Docker build trusts the exit code and discovers the absence only when Nuxt loads Lightning CSS. Evidence chain: complete target lock entry + correct linux/x64/optional-enabled environment + target-only failure injection -> 839 packages/npm exit 0 -> identical native-module stack. H1, H3, and H4 are refuted; no assumed link remains.

Implemented class-level fix: retain immutable `npm ci`, explicitly include optional packages, add bounded registry retries, then run the lock-driven OS/CPU/libc verifier before Nuxt. No second install and no lock mutation are allowed. The exact-commit image gate remains the next acceptance boundary; this investigation is resolved at the reproduced project-boundary defect and implementation level, not yet at release level.
