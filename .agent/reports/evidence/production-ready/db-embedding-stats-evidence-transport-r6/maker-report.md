# DB-EMBEDDING-EVIDENCE-TRANSPORT R6 maker report

R6 closes ET-R5-001, ET-R5-002, and ET-R5-003 without changing product code or
the accepted seven-source product manifest. The covered product verifier remains
Git blob `75bec9c41eb5abc435f13d90848074f6608f7fce`; the final portable test blob is
`caca5ec75f47c03698ac270b549a432b0de04cfe`.

## Process evidence

`capture-coverage-run.cjs` materializes the exact candidate index tree in a
temporary independent Git clone with a real `.git` directory,
`core.autocrlf=false`, and exact LF index bytes. It launches the exact Node argv
through `spawnSync` there and captures the operating-system status, signal,
start/finish timestamps, and raw stdout/stderr. The host checkout is not mutated.
Each run
has a separate raw stdout file, raw stderr file, canonical TAP transcript, and
process envelope. The envelope is written even when the child is nonzero or TAP
parsing fails; a successful packet additionally requires `parse_error=null`.

The two real runs both exited 0, emitted empty stderr, and recorded 24/24. Their
metrics are identical: aggregate `89.96 / 75.86 / 95.8`, verifier
`80.08 / 55.91 / 81.82`, and test harness `99.72 / 94.78 / 100`. Aggregate line
coverage is `89.96% >= 80%`.

The packet binds all nine measured dimensions individually. The inherited
normative contract has exactly one floor: `aggregate.line_percent >= 80`; its
observed value is `89.96` and margin is `+9.96`. Aggregate branch/functions,
verifier line/branch/functions, and test-harness line/branch/functions remain
explicit observations with `normative=false`, `floor=null`, and `margin=null`.
In particular, verifier branch `55.91` is not misreported as satisfying an
invented 80% branch floor.

`verify-evidence.cjs` independently re-hashes every raw stream, transcript, and
envelope; regenerates the transcript from raw stdout; reparses all counts and
metrics; and refuses nonzero process status, signal, spawn/parse error, nonempty
stderr, missing files, stale source blobs, or hand-entered results. A parseable
TAP file alone can no longer establish success.

## Representation portability

Checkout representation and canonical execution bytes are separate contracts.
The verifier classifies LF, CRLF-equivalent, and mixed-equivalent checkout bytes
without calling any of them LF. Canonical execution always uses an independent
`.git`-directory clone of the exact tree with exact LF Git blobs, so coverage no
longer changes merely because the host is a linked worktree or ordinary clone.
The permanent 24-case suite now runs the strict R5 materialization check
inside an isolated `core.autocrlf=false` Git fixture built from the canonical
index blobs. It still rejects a real mixed-EOL mutation; there is no skip or
conditional pass.

The final atomic commit must be checked in both fresh `core.autocrlf=false` and
fresh `core.autocrlf=true` checkouts. In each, the same committed test blob must
report 24/24. `verify-final-commit-replay.cjs` additionally requires the final
commit to be a direct child of `a538f6224ef31f612152470a4ecd45e78ff9d0f2`,
compares its committed verifier/test/wrapper blobs to the capture, materializes
the final tree in the same canonical clone topology, replays the coverage command
from those final blobs, and emits the exact commit, tree, and
changed-path digest. Those identifiers cannot be embedded in their own commit
without a self-reference, so the immutable replay output is part of the final
handoff and checker command, not a guessed field in this file.

## RED, GREEN, REFACTOR, Prove-It

RED was recorded before implementation:

- an exit-7 process emitted the exact committed R5 TAP bytes while the R5 parser
  synthesized `exit_code=0` and returned PASS;
- the unchanged R5 suite in `core.autocrlf=true` returned 23/24 exit 1;
- the exact rejected R5 test blob produced 8/16 for the schema sentinel, proving
  its 9/15 claim stale;
- the first R6 checksum packet passed while omitting the changed R5 verifier and
  counting an unchanged load-bearing verifier instead;
- the first final replay inherited linked-worktree versus ordinary-clone branch
  variance and therefore failed in a legitimate fresh clone despite 24/24.

GREEN is 24/24 for the base suite and 12/12 for the R6 tamper suite. Prove-It
mutations against the exact final blobs returned 9/15 for schema release,
15/9 for forced artifact PASS, and 0/12 for a forced R6 status PASS. Both suites
returned fully green after byte-for-byte restoration.

The tamper suite refreshes local envelope hashes while attacking nonzero status,
stale source identity, hand-entered metrics, stdout, stderr, transcript, missing
files, and unknown fields. All semantic attacks fail. Only additional trailing
padding in a coverage-table row is accepted, because the independent canonical
transform removes and exactly counts those bytes while leaving the transcript
unchanged.

## Changed-path checksum completeness

The checksum verifier derives the full path delta from the exact target base to
the candidate Git index/final tree. Every changed path must be present directly
in one checksum layer except the checksum manifest and `R6-SHA256SUMS.txt`, the
only two declared self-exclusions. The unchanged covered verifier remains a
separately labelled `load-bearing-unchanged` entry. The changed R5
`verify-coverage-capture.cjs` is now directly listed. A permanent mutation removes
that path and refreshes the attacker's local manifest, path digest, and sums; the
verifier still returns nonzero FAIL and names the uncovered changed path.

## Historical R5 correction and scope

R5 remains a rejected historical snapshot. Its TDD artifact now records the
actual 8/16 result for the exact R5 test blob and its report/summary/matrix state
that the old hashes and 89.28% coverage belong only to R5. R6 current claims use
the final R6 blobs and the two real R6 envelopes above. No PostgreSQL service,
schema, data, product candidate, or product source blob was modified.

## Residual risk

The remaining risk is operational, not hidden in the packet: the post-commit
fresh-LF/fresh-CRLF and final-commit replay gates must pass on the immutable maker
SHA. Failure of any one keeps the handoff at REVISE. No push, merge, tag, release,
browser report opening, or PostgreSQL mutation is part of this maker branch.
