# RELEASE-GATES R12 plan-governance / R15 closed-world authority maker report

Status: `R15_CLOSED_WORLD_AUTHORITY_READY_FOR_FRESH_MAKER_DISTINCT_CHECKER`

Successor base: `cf85a59894150bdb122067c7bb2c421b3929f1e6`

## Frozen authority

A12 is the exact child of rejected A11 `887b7c6144a09829a1a3f43ff5a19d3fb27fb7c1`. It preserves A11 and the parked B11 draft as rejected history. The committed transition manifest no longer requires its own head SHA or container blob. The trusted-base execution artifact binds the actual event head, actual manifest-container blob, canonical manifest digest, trusted validator blob, and required-status producer tuple. Strict latest-base, one-use epoch, expiry, consumed history, two-PR stale-loser behavior, unprivileged successor selftest, and PR-only recovery remain mandatory.

The plan retains exactly 69 unique slices. Forty-three existing slices are grouped into MB1 through MB4 without replacing their identities. Macro branches, dependencies, accepted predecessors, collision order, and maker-distinct checker/root-review boundaries are frozen in the active contract.

DOCUMENT R2 is rejected for concurrent duplicate removal success. Its R3 successor is immutable at `9ea95b6adc3a838a459bf78e3ba7a0292153960f`, tree `c15a731de8ca8acac09bce8b209e2a27dd6303cb`, with exactly six changed paths. R3 remains unaccepted pending a fresh checker.

## Gates

- A14's maker-distinct checker returned `REVISE` after its expanded 65-case hostile matrix exposed eleven sibling authority false-greens. R15 replaces the recurring partial-field design with one recursive closed-world comparison against the immutable A14 Git blob; it does not self-accept the successor.
- The exact protected surfaces are `authority`, `external_enforcement`, `control_plane_maintenance`, and `macro_batches`. Missing, unknown, alias, duplicate, wrong-type, wrong-value, nested drift, direct-bypass, array-cardinality, and array-order shapes fail through the same comparison path. Collision path identity also uses the existing exact-sequence helper instead of set equality.
- Raw JSON is scanned recursively before `ConvertFrom-Json`. This is required because current Microsoft PowerShell documentation states that duplicate keys otherwise collapse to the last value. Exact and case-alias duplicates are both rejected.
- Fixed-point proof hashing now canonicalizes UTF-8/LF, and an explicit CRLF inverse proves the same authority digest in a fresh `core.autocrlf=true` Windows checkout.
- Nested authority values require exact Boolean, integer, string, array, and null semantics before their values are used.
- Active epoch identity, exact authorized actor/change set, exact active window containing current UTC time, bounded TTL/lifetime, consumed/non-reopen, and two-PR stale-loser invariants are checked and hostile-flipped.
- The required-status integration ID must be a JSON integer with exact value `15368`; PowerShell coercion is not authority.
- All four macro batch identities, branches, exact maker sequences, dependencies, accepted predecessors, MB4 path count, and all seven collision orders are checked. The macro source must be the tracked `challenge-report.json` blob from immutable A13 `69b4b871`, with its canonical byte hash; coordinated path/hash drift cannot self-authorize.
- R12 plan-governance validator, recursive property/permutation selftest, and the unchanged independent checker hostile matrix: PASS, 65/65 expectations, zero false-greens.
- Prove-It: replacing `Test-StrictAuthoritySchema` with `return $true` makes the committed validator exit 1 on `unknown manifest alias was accepted`; restoring the implementation returns GREEN.
- Active-candidate and plan-path-ownership selftests: PASS.
- JSON parse, `git diff --check`, `go build ./...`, `go vet ./...`, and `go test ./... -count=1`: PASS (skips remain non-acceptance evidence).
- Legacy ownership ledger: expected RED with exactly 12 errors, all the single known class `replacement transition_kind` unsupported by the current validator; B12 owns that implementation closure.
- No GitHub ruleset, tag, release, primary checkout, product source, or foreign repository was mutated by A12.

## Commit boundary

R15 remains inside the exact 15 paths in `path-envelope.json`; it changes no product, workflow, database, MB1, or external-system path. The successor requires a fresh maker-distinct checker and root post-review before acceptance. B12 remains gated on that acceptance and may change only its declared 21 paths. Synthesis must transplant only the accepted authority delta onto `fef455bcf640f849c2d40c9bc26a459b5593e10a`, the Go 1.25.12 line, then run integrated `govulncheck`; this isolated ancestry retains the known Go 1.25.11 security baseline and is not itself releaseable.
