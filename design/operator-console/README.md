# operator-console public design snapshot

This directory is the public tracked contract snapshot for the Engram operator console.
It is not the live OpenDesign workspace.

`PROMOTION-MANIFEST.json` is the reproducible snapshot: it lists every curated file
and SHA-256, so a clean checkout can verify the contract without `.od/`.

See [PROMOTION-CONTRACT.md](./PROMOTION-CONTRACT.md) for the boundary between `.od/`
authoring, this curated snapshot, and `apps/operator-console/` runtime integration.
