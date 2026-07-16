# T007 compatibility demolition classification — maker report

## Outcome

`TestEC_F1_TagDerivedBackfill_T007` is a **CURRENT_CONTRACT_TEST_CORRECTION**. No production change is authorized or required.

The raw SQL half of the test already proves that the `scope:global` fixture is backfilled to `privacy_scope='global'`. The failing List assertion then inspected `models.Memory.PrivacyScope` while the vNext-F flag was off. The live `MemoryStore.List -> memoryRowToModel` path intentionally leaves that field empty under flag-off to preserve the v6.4 response contract.

The corrected test explicitly fixes flag-off state, reads the fixture's durable `id` alongside the existing raw SQL proof, and requires `MemoryStore.List` to return that exact ID with exact content. It no longer depends on metadata intentionally hidden by the compatibility projection.

## TDD and verification

- Valid RED: old assertion failed on fresh PostgreSQL 17.10; one failed test, zero skips, zero sessions before drop, zero database residue.
- GREEN: focused fresh-database gate passed 3/3 with all child/parser/coverage/cleanup exits zero.
- Prove-It: temporarily restoring the old assertion failed exactly the T007 test; cleanup passed. The intended test file was restored byte-for-byte (`SHA256 B223366204385AFC4D4F6A4899777C2D6530A5B6420D6869683B6CBF65F4EC21`) and post-restore GREEN passed.
- Race: focused fresh-database race gate passed.
- Static gates: `go vet ./...`, `go build ./...`, and `git diff --check` passed.
- Full fresh-database `./internal/mcp`: 488 tests, 487 passed, 1 failed, 0 skipped. Both T007 tests passed. The sole failure is `TestHybridTG3_ConfidenceMin_FloorEnforced_T022`, which A10 assigns to `DEMOLITION-SKIP-CLASSIFICATION`; T007 has no authority to alter it. Cleanup recorded zero remaining sessions and zero database residue.

Machine evidence is under `.agent/reports/evidence/production-ready/t007-compat/`.

## Scope discipline

Production/test change: `internal/mcp/store_memory_compat_t007_test.go` only. No production source, workflow, plan, role state, release state, main checkout, tag, push, merge, or browser/report surface was changed.

The operator-requested `.w/t007-current-contract` location is not ignored by the current repository (`.w/` appears as untracked in the preserve-only primary checkout). The path was explicit and already hosts isolated worktrees; `.gitignore` is outside this slice, so no ignore rule was changed.

Post-commit checks—exact-commit gitleaks, synthesis-preview merge-tree compatibility, and immutable SHA/parent/tree/path handoff—are intentionally performed after the commit exists and are reported by the maker handoff rather than self-referenced inside the commit.
