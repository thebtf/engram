# AGENTS.md

## PRODUCT AND STARTUP

Engram's accepted scope includes code intelligence and knowledge workflows for supported coding agents. PostgreSQL is authoritative; the local stdio MCP daemon talks to the server over gRPC. Do not restore server-side HTTP MCP or embed upstream products as a shortcut.

Read this file, the current run/checkpoint, and the active feature contract; for releases, read `docs/RELEASE-PROTOCOL.md`. Reconcile the actual checkout, dirty work, installed version, assignments and next user-visible outcome. Do not load every historical intake or repeat completed Spec Kit stages after compaction.

Follow the host's actual instruction hierarchy. Within project artifacts, `.specify/memory/constitution.md` governs, then accepted feature decisions/contracts. Explicit operator amendments use that governance. A delegated task cannot remove required functionality or waive safety/release gates. Ask about missing product decisions, not implementation details an engineer can resolve.

## ROOT SOLO

One Root Solo session decides, delegates, controls drift and accepts independently checked results. Native Task-subagents author architecture, technical contracts, code, tests, scripts, conflict resolutions, builds, diagnostics and reviews. Root does not do technical patches through shell/eval or become a second implementer. Do not recreate the historical peer PM/Developer model.

Use actual operator-managed assignments; subagents never self-promote or rewrite role/control-plane state. One authorized integrator owns the candidate; one authorized release owner coordinates publication. Check existing tags and remote state for consistency, never force-move a published tag. This file grants no extra production or credential access.

Delegate coherent outcomes with context, write boundaries and acceptance. Run ready independent tasks before waiting; shared files have one writer. Choose available models by task difficulty/risk. Avoid duplicate assignments and competing heavy runs on shared resources. An author is not their sole verifier.

## DEFINE THE FINISH

Before implementation, select one independently useful installable slice, non-goals, acceptance and migration/rollback boundary in the existing feature/run. Do not regroup small tasks into one long-lived mega-release. New features use Spec Kit; corrections revalidate the affected contract/delta, not the whole history.

For an authorized delivery goal, done means implemented, accepted, released, installed and exercised through the ordinary consumer. A unit PASS, fixture UI, commit, accepted feature or tag is not delivery. Research-only tasks may end with their requested artifact; do not invent deployment authority.

Preserve the promised outcome when fixing defects. A page bound must not disable semantic search on a normal corpus; a fake daemon or manually seeded fixture cannot prove ordinary indexing. Check authorization, actual downloads, first startup, UI navigation and worktree isolation during implementation, not for the first time after declaring readiness.

## REVIEW TO A DECISION

Review changed behavior and affected dependencies for correctness and maintainability, not perfection. Explain a meaningful decision once at its natural location; trivial fixes do not each need a new ADR/report.

Give every reported finding one evidenced disposition in existing review/task state: **fix now**, **not applicable**, or **maintainer-accepted follow-up**. Blocking means a failed mandatory gate, violated accepted behavior/compatibility, or credible security/data-integrity risk. A severity label alone is not the reasoning; credible unresolved high-impact risks remain blocking during investigation.

Do not defer real blockers to ship. Nonblocking optional findings do not automatically require release code. Required approvals/thread resolution still apply; disputes and scanner findings cannot be suppressed or unilaterally marked resolved. Never lower thresholds or hide failures to pass.

Finish required review before expensive final validation. Recheck fixes and affected invariants; reopen unchanged accepted areas only for new evidence or affected dependencies. A new reviewer/model or changelog edit alone does not justify another whole-product audit. No fixed review-count permits unsafe release; every additional round needs a concrete unresolved question.

## VALIDATION WITHOUT RESET LOOPS

Finish known blocker repairs, version metadata and generated inputs before freezing a candidate. Use its own runner and one QA owner. Do not mutate/rebase the candidate during its gate or launch final validation while same-candidate repairs are outstanding. A new blocker requires an explicit candidate/evidence-impact decision.

Reuse evidence only within validated input/environment/provenance contracts. Never relabel an old PASS. Docs-only changes do not justify a new product investigation, but cannot bypass the current tool's exact-input rules. SonarQube is a mandatory agent-executed local gate: the root agent runs `node tools/quality/run-sonarqube.mjs` from the clean, frozen exact candidate, retains `.agent/e/sonarqube/<HEAD>.json`, and requires `OK`, except for the single operator-approved v6.49.4 exception dated 2026-09-25 in `docs/RELEASE-PROTOCOL.md`; no GitHub Actions workflow performs this gate. All enforced checks and security rules remain.

Long checks have an owned durable job, actual command/path, selected tests, prerequisites, budget and observable result. A short Task wait must not kill a progressing gate. Follow the same job; do not start a replacement or clear a lock without checking ownership. A bad progress counter alone is not cause to cancel useful work.

Classify product, test, evidence-parser, environment and gate failures separately. Reproduce the smallest relevant case; replay saved events to debug classification. A repeated unchanged failure without new information requires a different diagnosis, not another identical full run. Do not fix flakiness by unsupported retries, assertion weakening or timeout inflation.

When the same cause invalidates two candidate attempts, Root makes a short recovery decision in the existing run: causal repair, valid-baseline restoration, operator-approved scope change or a specific external decision. This is an escalation trigger, not a waiver or a new QC project. Independent safe work continues.

## CI COMPUTE POLICY

GitHub-hosted compute is a scarce release resource. The 2026-09-19 incident exhausted the included 2,000 minutes and attributed $93.07 gross to Engram after 902 September runs, with no product result; macOS runners cost about 10.33x Linux. Do not spend remote CI merely to probe an unavailable external service or to rerun unchanged bytes.

- **Local proof:** run the focused local proof first. It precedes, rather than substitutes for, remote release gates.
- **Ordinary development:** each PR or `main` candidate gets one cancelable Ubuntu validation lane and cheap authority checks; it does not build Docker images. Configure events so the same candidate does not receive duplicate push and PR runs, and cancel superseded non-manual runs. If branch protection is enabled later, require only checks emitted on ordinary PRs—never dispatch-only `test / windows-latest`, `test / macos-14`, or `migrations / clean-db chain`.
- **Frozen candidate:** an explicit manual frozen-candidate validation runs clean-DB coverage and the Ubuntu/Windows/macOS matrix once for those immutable bytes. Do not begin another heavy run unless candidate bytes changed or proven infrastructure recovery makes one retry necessary.
- **Release boundary:** Docker image acceptance runs only for a `v*` tag or explicit manual frozen-candidate acceptance. The root agent runs SonarQube outside GitHub Actions with `node tools/quality/run-sonarqube.mjs` from the clean, frozen exact candidate before publication, retains `.agent/e/sonarqube/<HEAD>.json`, and requires `OK`, except for the single operator-approved v6.49.4 exception dated 2026-09-25 in `docs/RELEASE-PROTOCOL.md`. Human action is required only at an unavailable infrastructure, credential, or irreversible-effect authority boundary. Publication/release jobs are never cancelled mid-flight. A Sonar outage otherwise holds the release—repair or prove service recovery before one same-candidate rerun, rather than creating a no-op/amended commit or repeatedly consuming remote runs.
- **Post-publication:** the daily Docker schedule performs only the published-image rescan. It does not rebuild or accept images; the existing freshness and remediation rules still govern rollout.

Read `docs/RELEASE-PROTOCOL.md` before a frozen-candidate, tag, publication, Sonar, or published-image action.

## DELIVERY AND CONTINUATION

Once required gates pass and effects are authorized, proceed to release, rollout and ordinary-consumer verification; do not wait for another user "continue" or discretionary audit. A real external block needs one concrete decision request with impact/rollback, not repeated approval of an already authorized action.

Current recovery order: usable code search/graph/worktrees and Code Explorer, then Book Context, then cognitive memory unless the operator changes it. Preserve queued work; no old-PR cleanup or new capabilities inside release closeout. Do not dismantle an assembled candidate merely to obey a new small-batch slogan.

Keep one continuation pointer: candidate, installed state, causal blockers/owners and next action. Report actual user availability separately from checks. Forecast from evidence/dependencies with uncertainty, not invented dates or blanket bans on estimates. Root owns delivery progress, not just subagent activity.

Replace conflicting policy at its source; do not accumulate per-incident instructions, new ledgers, schedulers or report schemas. Land adopted rules in main through existing review so later sessions inherit them. Never silently update a running frozen checkout.

## ARCHITECTURE AND NAVIGATION

Verify call paths and consumers; distinguish deployed, implemented-but-unshipped, dormant, obsolete and absent. Historical v5 removal rejects obsolete implementations, not accepted outcomes (constitution XIV). Names, flags, docs and green mocks alone prove neither current architecture nor deployment.

Code queries/traversal/source reads use authorized Source/Checkout/pinned View. Labels, paths, legacy project aliases and equal vector dimensions are not authority or compatibility. Preserve privacy, migrations, bounded resources and independent worktrees. PostgreSQL projections are rebuildable, not a second write authority.

SocratiCode/Graphify are references for researched native mechanism adaptation, not embedded products or proof of parity. Reuse accepted research; state actual language/corpus coverage. Test doubles are allowed for isolated tests, never as proof of a real provider, daemon or installed user path.

Entry points: `cmd/engram-server/`, `cmd/engram/`. Current code intelligence: `internal/handlers/codeintel/`, `internal/mcp/tools_code_intel.go`, `internal/db/gorm/code_chunk_store.go`. UCI-native paths are present only when they exist in the current checkout; never direct current sessions to absent paths such as `internal/uci/` or `internal/db/gorm/uci_*`. Console: `apps/operator-console/`; HTTP/storage: `internal/worker/`, `internal/db/gorm/`; integrations: `plugin/`.

Use current `go.mod`, CI and lockfiles for toolchain versions. Base commands: `make build`, `go test ./...`; release commands/environments: `docs/RELEASE-PROTOCOL.md`. QA verifies real flags and selected tests; unexpected SKIP or zero selection is not PASS. Load only relevant available skills.

Never reset, clean, stage or overwrite unrelated user work, including inherited main-checkout `AGENTS.md` changes. Check actual owners and shared `.agent`/`.specify` paths before writing. Leave details of safe implementation to the responsible subagent.
