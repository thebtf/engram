# RELEASE-GATES Foundation Revision 3 — Maker Report

Date: 2026-07-10

Role: maker only; no independent checker verdict, post-review verdict, integration verdict, production-readiness verdict, or GO/NO-GO claim

Worktree: `D:\Dev\engram\.agent\worktrees\prc-release-gates`

Branch: `work/prc-release-gates`
Starting head: `2b3ef3e33bd19e630f8f67d07a9e2521cb98537f`

Plan-governance commit:
`a1653abf5a1088f45df2c58487a74a886666adf1`

## Outcome

Revision 3 implements the release-gate foundation corrections required by the
independent revision-2 `REVISE` report. It provides durable plan authority,
ordered ownership-state enforcement, fail-closed dev-stand liveness/readiness and
credential proof, and a clean-checkout OpenClaw release matrix. The implemented
gates correctly expose two current product/release blockers rather than masking
them:

1. the exact current three-image dev stand contains HIGH/CRITICAL findings
   (`5` operator-console, `38` PostgreSQL, `13` server); and
2. `plugin/openclaw-engram/package-lock.json` is absent, so no npm release command
   is allowed to run.

The DB bulk-operations ownership state remains truthfully held at
`DB-BULKOPS-BEHAVIORAL-EDGE-REWORK`. Rejected head
`68b2ce5835c7c6efdf1c68da9eedcb8d9c3837ef` is recorded as rejected, with no
integration SHA. No later DB candidate is represented as accepted in these
artifacts.

## Exact source locks

| Artifact | SHA256 |
| --- | --- |
| revision-2 checker report | `C7A96460C34951C0876F3F87F3B404E037D246A974D9AC491D5A9C2FF455FCCB` |
| revision-3 master plan | `D371E94DFF1EA12767B9D0832240CB6CAF52C6C3BBE2209FE4280159C4F03C52` |
| revision-3 ownership state | `1419E2F7E5236E21DD9A2D8C3271CED2DEF16DC0A798435AD5A9401FE522D55B` |
| rejected DB bulk-ops checker | `EB9EB227363A27EA058C6654BD7E38EED1088252F79F837E377B2A3CBC1FAFB7` |
| OpenClaw/ingest classification | `A095E9A30D6F04D8918B96D41D790D1F8CA1FB42671FBCF57BD0E29609E0AC2A` |
| MCP structured-input classification | `3356F3AE6073F95E701707FCF451D63809AC186ED1DEA7321A7027C4C3122E7A` |

The exact plan hash is embedded in the tracked ownership state and the executable
CI Ledger step. A different plan byte sequence fails closed.

## Changed implementation surface

- `.agent/dev-stand.config.yaml`
- `.github/workflows/test.yml`
- `scripts/production-gates/assert-plan-path-ownership.ps1`
- `scripts/production-gates/run-db-suite.ps1`
- `scripts/production-gates/run-dev-stand.ps1`
- new `scripts/production-gates/run-node-matrix.ps1`
- exact ignored governance artifacts:
  `.agent/plans/2026-07-10-engram-production-ready-master-plan.md` and
  `.agent/plans/2026-07-10-engram-production-ready-ownership-state.json`
- exact maker/evidence namespace under
  `.agent/reports/evidence/production-ready/release-gates-foundation-revision-3/**`

No DB bulk-ops product source, canonical root evidence register, rendered Markdown,
or HTML dashboard was edited by this slice.

## Revision-2 F-1 through F-7 disposition

| Finding | Revision-3 maker disposition |
| --- | --- |
| F-1 live DB-BULKOPS head unauthorized | Closed at authority level without accepting the defective head. The exact sibling/rework paths and ordered epochs exist; four overlapping paths are assigned to `DB-BULKOPS-BEHAVIORAL-EDGE-REWORK`; rejected head/checker/hash are pinned; integration is empty. The historical head produces zero path violations and exactly four current-owner errors. |
| F-2 owner sets accepted reversed order | Closed in the gate. Epoch owner sequences are compared exactly; current owner, predecessor evidence, required successor base, and Git ancestry are enforced. Reversed order, missing evidence, wrong base, and rejected-head mismatch all have negative fixtures. |
| F-3 plan authority ignored/unpinned | Closed in this branch's governance surface. The exact plan and ownership-state files are force-added, state is bound to the challenged plan SHA256, and CI invokes Ledger with `-ExpectedPlanSha256`. |
| F-4 OpenClaw release has no repair owner | Closed in the plan/gate design. `OPENCLAW-RELEASE` owns the manifest/lock release surface. The new runner requires clean pre/post state, a tracked non-ignored lockfile, four-way version parity, exact `npm ci -> typecheck -> test -> audit(high) -> pack --dry-run --json`, required package contents, and cleanup. Current expected-negative evidence stops before npm because the lockfile is absent. |
| F-5 `ingest_doc` unclassified | Closed in revision-3 plan authority using the source-backed classifier: `SnapshotOpIngestDoc/executeIngestDoc` is pre-demolition-stale/unwired for this release, cannot count as durable-audit proof, and has explicit demolition/public-truth owners. |
| F-6 current RELEASE-GATES liveness/credential defects | Closed in implementation, pending independent acceptance. Liveness is HTTP 200 plus exact `starting|ready|error`; readiness is HTTP 200 plus exact `ready`. Up generates three independent cryptographic 256-bit values, rejects blank/default/reused values, injects them at runtime, persists only redacted proof, and validates direct plus operator-proxied endpoints. |
| F-7 missing root register rows | Root-owned and still open at this maker snapshot. The canonical register has 54 unique rows, but comparison with the 47 revision-3 plan slices finds two missing rows: `DOCUMENT-INGEST-PUBLIC-TRUTH` and `INGEST-DOC-SNAPSHOT-DEMOLITION`. This discrepancy was sent to root; this slice did not silently patch root-owned JSON/Markdown/HTML. |

## TDD and deterministic verification

RED evidence is preserved at:

- `tdd/RG3-OWNERSHIP.red.json` —
  `051CC783F978E65B777A96D3897C1F2CB5CB4F29824C86506BCB939870919309`
- `tdd/RG3-DEVSTAND.red.json` —
  `DA1948A48ADE2C830C8EEE570F4BA3C14D5B8020D7C4402B2C588CF8C0DF8622`
- `tdd/RG3-NODE.red.json` —
  `C9E044E2D4560ECFC19F8E6828DA4C5BD1F1E3DC1B0AB7ABD974EF66E727797B`

GREEN/fail-closed verification:

| Proof | Result |
| --- | --- |
| PowerShell AST parse | PASS, 8 scripts, 0 errors |
| deterministic script self-tests | PASS, 8/8 |
| revision-3 ownership Ledger | PASS, 47 slices, 318 declarations, 32 repeated exact paths, 2 declared prefix intersections, 32 state epochs, 0 errors |
| rejected DB head Diff | expected FAIL, exit 1, 22 changed paths, 0 path violations, exactly 4 current-owner errors |
| workflow/config/runner conformance | PASS, 26 semantic mutations rejected |
| `actionlint` | PASS, v1.7.12 |
| `git diff --check` | PASS |
| evidence secret-pattern scan | PASS, 0 matching files |
| post-run Docker residue | PASS, 0 containers, 0 networks, 0 volumes |

Machine summary:
`.agent/reports/evidence/production-ready/release-gates-foundation-revision-3/verification-summary.json`.

## Actual dev-stand runtime proof

The real lifecycle used compose project `engram-critical-stand` and exact images
declared by `.agent/dev-stand.config.yaml`.

- Up: PASS. PostgreSQL/admin/bootstrap values were independently generated,
  distinct, non-default, runtime-injected, and not persisted. Direct and
  operator-proxied liveness/readiness endpoints all returned HTTP 200 and passed
  their distinct semantic contracts.
- Ready: PASS.
- Scan: FAIL as required by policy. Findings were `5`, `38`, and `13` for the exact
  operator-console, PostgreSQL, and server images respectively.
- Down: PASS. Containers, networks, and volumes owned by the project were zero.
- Wrapper: expected FAIL because Scan failed; cleanup remained PASS.

The wrapper summary SHA256 is
`2E8386AAC779F1EA3E68EDE005BF643B8EBCD37824101B8E7B80070AB9B311CF`.

### Bootstrap capability claim boundary

The runner proves that `ENGRAM_AUTH_BOOTSTRAP_CAPABILITY` reaches the server
container environment through an ephemeral override. Current Go configuration has
no live consumer for that variable. Therefore this report claims only generation,
non-default/distinct policy, runtime injection, redaction, and non-persistence. It
does **not** claim functional bootstrap authorization behavior.

## OpenClaw release-matrix proof

The current clean-surface run is an expected negative:

- pre-surface clean: true;
- post-surface clean: true;
- release commands executed: 0;
- package dry-run: false;
- blocker: missing tracked `plugin/openclaw-engram/package-lock.json`.

This is owned by `OPENCLAW-RELEASE`; RELEASE-GATES does not generate or repair the
manifest. Evidence SHA256:
`408424249909005FEC919E1E5E00C73596FCDB4FAACDBDC69295E3EBBC860472`.

## S4 threat model

### Assets

- PostgreSQL credential, admin token, and bootstrap capability;
- exact challenged plan bytes and ordered ownership authority;
- npm lock/package/plugin release identity and packed artifact contents;
- cleanup ownership boundaries for Docker and Node artifacts.

### Threats and controls

| Threat | Control |
| --- | --- |
| blank/default/reused credentials | cryptographic 256-bit generation plus nonblank, non-default, pairwise-distinct assertions and negative fixtures |
| secrets in command arguments, raw logs, summaries, or config | environment-only process injection, redaction before persistence, machine-evidence scans, and no caller-environment export |
| shell-dependent runtime proof failing on distroless images | container IDs plus `docker inspect --format '{{json .Config.Env}}'`; no in-container `sh` execution |
| HTTP 200 false-green | separate liveness and readiness parsers with exact allowed status sets |
| locally altered/ignored plan authorizing work | tracked plan/state, exact expected SHA256, canonical state binding, CI Ledger |
| reversed ownership or successor omitting predecessor | exact sequence comparison, current-owner enforcement, predecessor evidence, exact required base, ancestor proof |
| lockfile ignored, stale, or version-drifted | presence/tracking/non-ignore proof plus package/lock-root/plugin version and dependency parity |
| source/tests or `node_modules` leaking into package | dry-run package allow/deny checks and unconditional exact-surface cleanup |
| cleanup escaping its owned scope | compose-label inventory and exact OpenClaw `node_modules`/`dist` removal with outside-sentinel self-test |

### Residual risks

- Current release images fail the zero HIGH/CRITICAL policy and require the separate
  image-remediation lane.
- OpenClaw clean-install/package proof cannot start until the release owner commits
  a valid tracked lockfile.
- Functional bootstrap capability remains a separate product/security contract.
- DB bulk-ops successor acceptance, root register parity/render, independent
  revision-3 checker, post-review, integration, full project gates, and customer-mode
  proof remain mandatory.

## Handoff contract

This maker handoff is suitable only for a fresh independent checker. The checker
must bind to the exact commits and hashes supplied after commit, rerun the
deterministic and runtime-relevant gates from a clean worktree, and preserve the two
expected-negative product blockers. No evidence in this report authorizes
integration or a production-ready claim.
