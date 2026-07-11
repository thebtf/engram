# DB-TEST-POOL-HYGIENE evidence revision 4 maker report

## Scope and base

- Role: maker only; independent checker is required after this immutable handoff.
- Evidence parent: `331b5b195a967e7f27dca94038a3480c9afcc84f`.
- Product candidate: `276337b3e96aa5af6d2e7dd9a0002ff957e5ffc9`.
- Worktree: `D:\Dev\engram\.w\dbph-r4`.
- Branch: `work/prc-db-test-pool-hygiene-evidence-r4`.
- Scope: evidence and this maker report only. No product, test, spec, readiness-register, or HTML path is changed.

## Why revision 4 exists

The R3 checker proved five coherent false-green packets were accepted: unknown manifest keys, unknown inventory keys, unsupported inventory schema, stale adversarial proof, and stale verifier proof. R4 treats those as one failure class: the packet described shape but did not validate the exact schema and the truth of its committed proof objects.

R4 therefore uses one shared contract for exact object keys, schema versions, immutable product identities, and the ordered adversarial case catalog. The builder, verifier, and adversarial harness consume that contract instead of maintaining independent acceptance lists.

## TDD trace

- RED is preserved in `16-evidence-r4-red.json`: all five R3 false-green packets exited zero and reported PASS against R3.
- GREEN is proven by `adversarial-proof.json`: the original cases plus exact-key, wrong-schema, stale-proof, plausible-false-proof, ordering, diagnostics, CRLF, mixed-EOL, and duplicate-property cases all fail closed from coherent alternate Git indexes.
- REFACTOR keeps the schema and case catalog in `DBPoolHygieneEvidenceContract.ps1`; the builder and both verification paths consume it.
- The project forbade `.agent/specs/**` changes for this evidence-only revision, so the RED/GREEN/REFACTOR evidence is stored in the authorized evidence packet instead of the normal TDD spec location.

## Evidence contract

- Every JSON object and entry has an exact allowed-key set; unknown and duplicate properties fail.
- Manifest, inventory, verifier-proof, and adversarial-proof schema versions are exact values, not merely numeric values.
- Committed proofs are parsed and semantically checked for PASS status, Git-index source/revision, representation, current counts, empty-success semantics, exact case order/count, actual-versus-expected results, and required diagnostics.
- Each adversarial mutation gets a fresh repository and index, shares only immutable source objects via Git alternates, rewrites the mutated artifact blob, updates its manifest entry, and rebuilds the outer checksum. CRLF and mixed-EOL manifest mutations are likewise rehashed.
- Dynamic proofs use a deterministic valid bootstrap to break the manifest/checksum cycle. The real verifier and adversarial harness then replace the bootstrap, the packet is rebuilt, and an independent second harness run reproduces the real adversarial proof byte-for-byte.

## Verification

Final gate results and counts are recorded in `17-evidence-r4-gates.json` and the two committed proof files. The strict verifier passes with 21 changed paths, 19 directly bound paths, 35 manifest entries, 36 checksum entries, and inventory 83/8. All 35 adversarial cases pass, including the retained original 12; the independent repeat is byte-identical. Go vet/build, the focused database regression, script parsing, staged secret scan, scope checks, product-blob identity, and zero database/activity/temp residue also pass. The immutable commit SHA, tree, exact path list, and ordinal path-list digest are reported out of band because a commit cannot contain its own identity without self-reference.

## Concerns

No product-risk concern was introduced: `internal/db/gorm/candidate_store_test.go` remains byte-identical to the accepted product candidate. The remaining process concern is intentional: this maker result is not self-approved and requires the peer checker.
