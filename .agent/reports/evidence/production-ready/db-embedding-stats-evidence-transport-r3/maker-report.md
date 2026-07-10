# DB-EMBEDDING-EVIDENCE-TRANSPORT R3 compact handoff

Status: **READY_FOR_CHECK**

R3 is based on the immutable independent checker commit `8dac7910...` and
preserves accepted product commit `38d6a4fb...` byte-for-byte. It repairs all
four blocking checker reproductions: exact source commit, exact seven-source
set, valid-substitution rejection, and canonical-path pre-access gating.

Evidence summary:

- exact-parent RED: `18 pass / 4 fail`, exit `1`;
- GREEN and post-restore: `22/22`, exit `0`;
- Prove-It sentinels: `12` and `9` failed tests, both exit `1`;
- Windows: raw/Git/LF `0/7`, `7/7`, `7/7`; artifacts `5/5`;
- fresh LF: raw/Git/LF `7/7`; artifacts `5/5`; suite `22/22`;
- coverage repeated identically twice: aggregate line `87.27%`, branch
  `71.75%`, functions `94.87%`; verifier-only metrics are separately labeled;
- product/source/test delta, temporary worktree residue, maker Node residue,
  matching PostgreSQL databases, and matching PostgreSQL sessions are zero.

One diagnostic discrepancy was surfaced rather than hidden: the first
read-only residue query assumed a nonexistent PostgreSQL role `postgres` and
failed authentication. Container configuration identified the actual role and
database as `engram` / `engram_test`; the corrected read-only query returned
database residue `0` and session residue `0`.

The packet checksum manifest excludes itself. Final commit/tree identity and
the manifest's own hash are reported after commit. A fresh independent checker
must replay the four repaired mutations; this maker does not self-accept.
