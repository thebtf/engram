# T007 R1 independent checker report

## Verdict

**ACCEPT_WITH_MEDIUM_AUDIT_CORRECTION.** The T007 change is a test-only correction to the current flag-off compatibility contract. There are no CRITICAL or HIGH findings, no production-code changes, and no v5-demolished behavior was restored. The single MEDIUM finding corrects the maker handoff's path-digest serialization; it does not change the validated path set or immutable maker tree.

## Immutable maker and scope

- Maker: `1418796e55e8b5bfbb216ffbf5a3fba9fa620922`
- Required/direct parent: `af1ed63536829916e0477be719a30a57a8d9227a`
- Maker tree: `a08b92b88d5ac5570e25f3b8ef19ad0acf1727b6`
- Maker worktree was clean before checker work began.
- Exact diff: 331 paths — one test path, 329 paths under `.agent/reports/evidence/production-ready/t007-compat/**`, and one maker report under `.agent/reports/production-ready/t007-compat-demolition-classification/**`.
- The only product/test path is `internal/mcp/store_memory_compat_t007_test.go`. There is no production-code diff.

The maker's `5B4915A5...` digest is reproducible only with culture-aware PowerShell `Sort-Object`. The active platform digest contract uses ordinal normalized repository paths with one LF per entry. The corrected digest for the same 331-path set is:

`1d545a9ff8ff89dcd4a7a363ab621b3e7c28d9bc67c235ba715ec30cd98c294b`

## Semantic review

The changed test fixes `ENGRAM_VNEXT_F_ENABLED` to off before constructing the store. The raw SQL projection independently proves that the global-tagged fixture has a non-zero durable ID and persisted `privacy_scope='global'`. `MemoryStore.List` then must return that exact row by ID and exact content. This is the correct flag-off contract because `memoryRowToModel` intentionally omits `Memory.PrivacyScope` unless `ENGRAM_VNEXT_F_ENABLED == "true"` to preserve the v6.4 response shape.

The three test rows use a UUID-scoped project and distinct content, and are inserted in deterministic ID order. Matching `rows[0].ID` plus `rows[0].Content` cannot be satisfied by the project-tagged or untagged fixture.

## Independent adversarial evidence

- Original parent with flag explicitly off: expected RED, exactly one failed T007 test, zero skips, clean database teardown.
- Maker with the old `PrivacyScope` assertion restored: expected RED, exactly the T007 test failed.
- Wrong fixture-ID mutation: expected RED at the exact-content assertion.
- Raw backfill corruption (`SET privacy_scope='project'`): expected RED at the SQL `global` assertion.
- Parent with ambient flag `true`: false green reproduced, proving the old test was environment/order dependent.
- Maker reset with ambient flag `true` plus a temporary checker guard: PASS, proving the committed `t.Setenv(..., "")` overrides ambient state.
- Removing the reset while retaining the temporary checker guard: expected RED.
- Every temporary test mutation was restored from the immutable maker commit before final gates.

## Runtime and static gates

- Fresh PostgreSQL 17.10 focused repeat: 3/3 PASS, zero skips, zero non-zero child commands.
- Fresh PostgreSQL 17.10 race run: 1/1 PASS, zero skips, zero non-zero child commands.
- Full fresh-DB `./internal/mcp`: 488 tests, 487 PASS, one FAIL, zero skips. Both T007 tests passed. The sole failure was the already-owned `TestHybridTG3_ConfidenceMin_FloorEnforced_T022`.
- All 12 checker-run disposable databases were absent after cleanup; their session residue was also empty.
- `go build ./...`: PASS.
- `go vet ./...`: PASS.
- Maker-range and worktree `git diff --check`: PASS.
- Exact-maker-commit gitleaks: one commit scanned, zero findings.
- Synthesis-preview merge-tree with `0c6269908aa810a2248f2bfaf3fca4f9f5791359`: PASS; result tree `9421a8eae8c9579dfe6800e6d16f26840269823a`.
- Maker JSON evidence: 40/40 files parsed.

## Findings

### T007-R1-AUDIT-001 — MEDIUM, non-blocking

The maker path digest used culture-aware sorting instead of ordinal sorting. This is an audit-presentation defect, not a path-set or code defect. The corrected active-contract digest is recorded above and in `verification-summary.json`.

## Reusability candidates

- none — evaluated; the change is a single compatibility regression-test correction, not a reusable component boundary.

## Cleanup and handoff

The temporary detached parent worktree was clean and removed. The checker branch contains only artifacts under `.agent/reviews/t007-r1-fresh-checker/**`; it must remain a direct child of the maker commit and is not integrated, pushed, tagged, or merged by this checker.
