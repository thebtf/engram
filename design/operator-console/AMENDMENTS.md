# Accepted design amendments

## 2026-09-17 — Feature011 D-A operator-workspace contract amendment

The accepted Feature011 D-A contract supersedes the promoted snapshot's `search → bounded graph → Source` discovery order and any instruction that freezes its pages, navigation, copy, or workflow as scaffold fidelity. The D-A source journey is **Home → Workspace → Repository → Working copy → Indexed snapshot → structure/search → source → direct or reverse relation → evidence**. `Source`/`Checkout`/`View` remain secondary authority evidence, not primary operator task labels.

Manual knowledge-graph construction and plaintext book intake are retired workflows, not stale panels to preserve. D-A removes their UI and executable HTTP/MCP/flag/worker admission while retaining approved historical readers, versioned Documents, provenance, and UCI code graph boundaries. A visual relation aid remains bounded (depth `2`, maximum `24` nodes) and always has an accessible relation-list equivalent; it does not replace evidence or reintroduce a manual editor.

The exact private `.od` authoring tree is not present in this candidate and must not be invented or reconstructed in the tracked promotion snapshot. Consequently this amendment does **not** alter `contracts/DESIGN.md`, `PROMOTION-MANIFEST.json`, mockups, or `apps/operator-console/PARITY.json`, and it makes no promotion/parity claim. T001 must update private `.od/DESIGN.md` first, bump its design version, and perform the reviewed curated promotion described by `PROMOTION-CONTRACT.md`. Until then, the 2026.09.10 snapshot is historical design input, not D-A runtime acceptance authority.

`PRODUCT.md` and `specs/011-operator-code-console/spec.md` are the authoritative D-A outcome contract. The following snapshot instructions are superseded for D-A implementation: `contracts/DESIGN.md` §1.1's old discovery order; `contracts/DEVELOPER-PLAYBOOK.md` and `contracts/INTEGRATION-AGENT-PROMPT.md` language that limits the developer to swapping data or forbids page/product changes; and `contracts/HANDOFF-data-integration.md` language that preserves old page/classification behavior. Their retained security, token, and promotion-boundary material still applies unless it conflicts with the D-A contract.

The supported secure-origin/authenticated-browser versus deliberately supported single-user HTTP/no-auth deployment choice remains install-bound. This amendment preserves grants and release authority and does not manufacture an onboarding bypass or installed-success claim.

## 2026-09-10 — Feature011 Code-flow snapshot promoted

Authoring `DESIGN.md` now defines the Operate-mode `Source → Checkout → View` flow,
including the `search → bounded graph → Source` return path (depth `2`, maximum `24`
nodes), explicit `must-build`/blocked/empty states, and the boundary that legacy
collections are stale read-only listings rather than a code source or checkout.

`PROMOTION-MANIFEST.json` records design version `2026.09.10`, deterministic provenance
`2026-09-10T00:00:00Z`, snapshot SHA-256
`356563b1674beda461230e50af1d9afc29e28e197fcb64e18db83a592bf35ee9`, and the curated
13-file allowlist. The manifest proves the source snapshot only. It does not assert
runtime or visual parity, and this promotion made no `apps/operator-console/` write.

The focused `npm run test:parity` check verified all 13 promoted hashes and preserved
the acknowledged route drift, then correctly exited nonzero because the runtime-owned
`apps/operator-console/PARITY.json` still declares `2026.07.14` and the prior snapshot
hash. T001 deliberately does not change that application ledger. Its owner may update it
only with the runtime slice's route-specific browser evidence; until then, this is a
recorded expected drift, not a design or runtime parity claim.


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
