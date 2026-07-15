# Accepted design amendments

## 2026-07-14 — G1-R3 real version bump promoted

Authoring `DESIGN.md` bumped `design_version` to `2026.07.14` for the candidate-queue
merge into the memory screen (queue route now renders memory lab in review mode).
This is a real content-driven version change, not a re-stamp of `2026.06.21`. The
promoted snapshot, `PROMOTION-MANIFEST.json`, and `apps/operator-console/PARITY.json`
all carry this one version authority plus the matching `content_sha256`.
`promoted_at_utc` is derived deterministically from the version date
(`2026-07-14T00:00:00Z`), not from wall-clock promotion time, so two identical
promotions stay byte-identical.

## 2026-07-14 — G1 promoted authoring snapshot

The curated allowlist was promoted from private authoring and is identified by
`PROMOTION-MANIFEST.json`; `.od` has no usable commit identity. Authoring
`DESIGN.md` is the single design-version authority: this snapshot and the parity
ledger use its stamp. This dated amendment is history, not a
competing version source. `content_sha256` identifies the promoted snapshot when
authoring content changes without a new version stamp.

All fifteen visible routes plus shell and settings-modal frames are `drifted` in
`apps/operator-console/PARITY.json`. No route is visually synced until route-specific
Chrome evidence is stored and re-audited.

Known contradictions remain: visible disabled must-build controls conflict with design
guidance, and phone layouts are incomplete (including navigation and settings).
Route-specific accepted amendments override older generic prose. This promotion changes
no runtime layout, page, component, or integration code.
