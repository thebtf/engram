# Promotion Contract — operator-console

## Boundary

- `.od/` is private OpenDesign authoring.
- `design/operator-console/` is the tracked curated public snapshot.
- `apps/operator-console/` is developer-owned runtime integration.

Promotion is one-way and is neither code generation nor bidirectional sync. The
public snapshot is identified by `PROMOTION-MANIFEST.json`, not a fictitious `.od`
commit. The manifest records its explicit docs/mockups allowlist, canonical SHA-256
values, deterministic snapshot SHA-256, route/frame map, tool version, and private-material exclusions. The source
`DESIGN.md` stamp is the single design-version authority; the manifest copies it.
The snapshot SHA-256 distinguishes content changes that retain the same source stamp.
Canonical promoted text uses UTF-8 bytes with LF line endings, so CRLF/LF authoring
differences do not create a new snapshot.

`promoted_at_utc` is a deterministic provenance stamp derived from the accepted
`design_version` date (`YYYY.MM.DD` -> `YYYY-MM-DDT00:00:00Z`), never wall-clock time.
A no-op promotion (unchanged `design_version`, unchanged content) preserves the
existing `promoted_at_utc` from the prior manifest, so two identical promotions
remain byte-identical.

Promotion refuses to write when allowlisted content changed but `design_version`
is unchanged from the existing manifest: bump `design_version` in `DESIGN.md`
first.

## Never promote

Do not promote the full `.od` tree, nested `.git`, images, prompts, agent state,
artifact metadata, critique/intermediate files, `.od-skills`, `.nuxt`, `.output`, or
`node_modules`. Do not make `.od` a submodule.

## Workflow

Inspect the exact allowlisted diff without writing:

```powershell
pwsh -NoProfile -File scripts/promote-od-operator-console.ps1 -SourceRoot D:\path\to\.od
```

Promote only the reviewed public allowlist:

```powershell
pwsh -NoProfile -File scripts/promote-od-operator-console.ps1 -SourceRoot D:\path\to\.od -DesignOnly
```

Then run `npm run parity` in `apps/operator-console/`. It verifies the public
manifest without reading `.od/`; explicit drift is reported, while false sync claims
fail closed.

## Runtime safety

Routine design promotion never overwrites `apps/operator-console/`. Passing
`-AllowAppWrite` produces a complete conflict report and stops in G1; a separately
reviewed runtime-promotion slice is required before any app write exists.
