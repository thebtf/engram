# DB test pool hygiene evidence revision 3 — maker report

## Decision

R2 is superseded for evidence acceptance. Its product candidate remains unchanged at
`276337b3e96aa5af6d2e7dd9a0002ff957e5ffc9`, but the R2 verifier failed open for
four evidence-integrity classes: coherent omission of a changed path, non-canonical
ordering, JSON type coercion, and numeric-string coercion.

R3 repairs the evidence transport only. It does not modify product or test code.

## Immutable lineage contract

- Product candidate: `276337b3e96aa5af6d2e7dd9a0002ff957e5ffc9`
- R3 direct parent: `68242c48aaad62ec087166eeb9ea32f14d189450`
- Branch: `work/prc-db-test-pool-hygiene-evidence-r3`
- Product blob retained exactly:
  `internal/db/gorm/candidate_store_test.go` =
  `7337f1bd8da4fb315de842eea2e3cce5476250a3` /
  `62260c1a2e0705b065295322dd23fcf9b17fd47cb5ebc64134630788e2d23e09`
- Inventory retained exactly: 83 call sites in 8 files at product parent
  `bd68c05baf4b7250096dd84f56bebea2aa555970`.

The final commit SHA cannot be embedded in its own tree. It is reported out of band
with the final tree, parent, changed-path list, and gate results.

## R3 closure

1. **Whole-delta completeness.** The verifier derives the changed-path set from
   `276337b3..candidate`, not from the R2 parent. Every changed path must be a
   manifest entry unless it is one of two exact self-references. `MANIFEST.json`
   is excluded from its own entries but bound by `SHA256SUMS.txt`;
   `SHA256SUMS.txt` alone is self-excluded. Both dynamic proof JSON files are
   ordinary manifest and checksum entries.
2. **Canonical order.** Manifest and checksum paths must be unique and strictly
   increasing under `StringComparer.Ordinal`. Reversal and duplication fail even
   when all hashes are coherently regenerated.
3. **Strict raw JSON schema.** `System.Text.Json` validates raw kinds before any
   PowerShell object conversion. Required strings, arrays, booleans, and
   non-negative integer tokens reject scalar/array coercion, numeric strings,
   decimals, missing fields, duplicate properties, and `null`.
4. **Adversarial proof.** The committed harness covers baseline, coherent missing
   changed path, coherent unsorted and duplicate packets, wrong raw types, nulls,
   CRLF bytes, wrong representation, and false 76/6 inventory counts.

## Representation and generation

The contract bytes are exact LF Git blob bytes (`git cat-file blob`). The builder
selects every tracked DB-pool evidence artifact, every matching maker report, and
the accepted product test blob from the Git index. It writes the manifest first and
the outer checksum second. The final verifier then checks the index packet; the
same verifier is suitable for an independent immutable-revision replay.

## Verification

Final results are captured in:

- `DB-TEST-POOL-HYGIENE.evidence-r3.json`
- `verifier-proof.json`
- `adversarial-proof.json`

The maker handoff is not acceptance. A fresh checker and root review remain
required before integration.
