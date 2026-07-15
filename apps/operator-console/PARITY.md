# Operator-console parity ledger

There is no mockup-to-Nuxt code generator. The curated public contract is
`design/operator-console/PROMOTION-MANIFEST.json`; its hashes and design version are
the authority, not the private `.od` workspace or an old `DESIGN.md` stamp.

`npm run parity` verifies every promoted hash, the 15 route + 2 frame mapping, runtime
targets, and ledger uniqueness. A `synced` row must cite stored route-specific visual
evidence and the current manifest version. `drifted` is an honest unfinished state and
is reported without pretending the route matches; `npm run parity -- --strict` rejects
acknowledged drift for release/production-readiness gates.

G1 records every row as `drifted` from the 2026-06-21 audit. No structural code review
may change that to `synced`: only accepted Chrome evidence can do so.
