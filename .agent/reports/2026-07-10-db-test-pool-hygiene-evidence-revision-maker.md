# DB-TEST-POOL-HYGIENE evidence revision 2 maker report

Status: **READY_FOR_RECHECK**

This is an evidence-only maker handoff. It is not an acceptance verdict. A
different independent checker must verify the final commit before integration.

## Immutable product boundary

- Product parent: `bd68c05baf4b7250096dd84f56bebea2aa555970`
- Exact product/evidence candidate and revision base:
  `276337b3e96aa5af6d2e7dd9a0002ff957e5ffc9`
- Branch: `work/prc-db-test-pool-hygiene-evidence-r2`
- Worktree: `D:\Dev\engram\.agent\worktrees\dbph-evidence-r2`
- Product/test paths changed by this revision: none.
- `internal/db/gorm/candidate_store_test.go` remains Git blob
  `7337f1bd8da4fb315de842eea2e3cce5476250a3`, SHA-256
  `62260c1a2e0705b065295322dd23fcf9b17fd47cb5ebc64134630788e2d23e09`.
- The exact revision commit is reported after the atomic commit because a
  commit cannot embed its own hash without changing that hash.

## Corrected inventory

The prior packet's 76-call/six-file statement was false for the exact parent.
Mechanical enumeration of every `internal/db/gorm/*_test.go` Git blob at
`bd68c05b...` proves **83 call sites across eight files**. The seven omitted
sites are one call in `temporal_truth_store_migration_test.go` and six calls in
`temporal_truth_store_test.go`.

`INVENTORY.json` stores every path and parent line number. The permanent
verifier independently regenerates the list from the exact parent and rejects
any total, file set, or line list that differs from 83/8.

## Preserved product evidence

The product evidence is unchanged:

- Exact parent broad package at `max_connections=100`: six governance/migration
  semantic failures plus six secondary pool-exhaustion failures and nine
  `too many clients` records.
- Exact candidate broad package: the same six semantic failures, zero secondary
  exhaustion failures, zero `too many clients` records, and no candidate-only
  failure.
- Focused cleanup regression `-count=20`: exit 0.
- Focused cleanup regression under `-race`: exit 0.
- Candidate/helper suite `-count=5`: exit 0.
- Candidate/helper suite under `-race`: exit 0.
- `go vet ./...` and `go build ./...`: exit 0.
- Every recorded fresh database and PostgreSQL session residue count: zero.

Revision 2 reruns a focused exact-candidate smoke plus vet/build to bind the
unchanged product blob to this handoff; it does not rewrite or reclassify the
existing broad failures.

The revision-2 commands and exits are recorded verbatim in `14-evidence-r2-focused.log`
and `15-evidence-r2-static.txt`:

- `go test -p=1 ./internal/db/gorm -run ^TestOpenCandidateTestDB_SubtestOwnerClosesPoolWithoutPrematureClose$ -count=3`: exit 0;
- `go vet ./...`: exit 0;
- `go build ./...`: exit 0.

The focused run used fresh database `engram_chk_dbph_evr2_focus_a` and ended
with zero database and PostgreSQL activity residue. Its detached temporary
worktree `D:\Dev\engram\.agent\worktrees\dbph-evidence-r2-runtime` was removed
and is no longer registered. No LF/CRLF verification worktree is used: the
adversarial harness mutates GUID-scoped ordinary files and removes them.

## Non-paradox checksum contract

The representation is machine-explicit: `git-blob-bytes-v1`.

- A manifest entry binds path, Git blob OID, SHA-256, and exact blob byte count.
- Checked-out working-tree bytes are never checksum contract bytes.
- Text blobs in this packet are clean-filtered LF Git blobs. CRLF/raw checkout
  substitution must fail.
- `MANIFEST.json` excludes itself and the outer sums file.
- Final `MANIFEST.json` is generated first.
- `SHA256SUMS.txt` is generated second, includes the final manifest, and excludes
  itself. No self-referential hash is claimed.

`Verify-DBPoolHygieneEvidence.ps1` checks the Git object type, path-to-OID
binding, SHA-256, byte count, complete outer sum set, generation-order headers,
and regenerated exact-parent inventory.

## Permanent fail-closed adversarial proof

`Test-DBPoolHygieneEvidenceAdversarial.ps1` runs one valid baseline and four
mutations. The harness succeeds only when the baseline exits zero and every
mutation exits nonzero with its expected failure class:

1. stale/mutated manifest entry;
2. raw CRLF manifest bytes under an LF/Git-blob contract;
3. unsupported raw-checkout representation contract;
4. forged 76-call/six-file inventory.

The harness uses a GUID-scoped directory under the operating-system temp root,
verifies that path before recursive deletion, and requires zero temp residue.
It creates no Git worktree and no PostgreSQL database.

The staged-index proof commands are:

- `pwsh -NoProfile -File Verify-DBPoolHygieneEvidence.ps1 -RepositoryRoot <revision-worktree> -SourceMode GitIndex -OutputPath verifier-proof.json`: exit 0, PASS, 25 manifest entries, 26 checksum entries, and regenerated 83/8 inventory;
- `pwsh -NoProfile -File Test-DBPoolHygieneEvidenceAdversarial.ps1 -RepositoryRoot <revision-worktree> -SourceMode GitIndex -OutputPath adversarial-proof.json`: exit 0, with baseline exit 0 and each of the four required mutations exit 1.

## Finish state

`review-needed`: one atomic, clean, evidence-only successor is ready for a new
independent checker. No merge, push, rebase, tag, release, integration, primary
checkout, canonical roadmap, or shared control-plane mutation was performed.
