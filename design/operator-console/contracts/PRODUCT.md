# PRODUCT.md — engram Operator Console

**Register:** product (admin tool; design serves the task, not the brand).

## What it is
Single-operator admin console for **engram**, persistent shared-memory infrastructure
for Claude Code workstations. Talks to one server: `http://unleashed.lan:37777`
(post "v5 demolition"). This `.od/index.html` is a self-contained HTML/CSS/vanilla-JS
prototype that a real fetch layer will later wire to the live REST/MCP surface
(see `HANDOFF-data-integration.md`, `DESIGNER-endpoints-brief.md`).

## Who uses it
One operator (the project owner), control-room context, often a wall/secondary display.
Dark theme is the default for that reason. Works in Russian; chat + UI copy Russian,
code/artifacts English.

## Primary surfaces (operational planes, not config)
Memory Lab · Candidate Queue · Noise & Usefulness · Behavior Rules · Issues ·
Vault & Docs · Projects & Sessions · Health · Settings (one tab) · Tombstones.

## Load-bearing concepts
- **Honesty contract / classification:** every surface is tagged live / dormant
  (flag-gated) / pre-demolition-stale (tombstone, never operable) / must-build.
  Color + badges must read this at a glance. Never fake a dead surface as live.
- **Feedback loop is the spine:** injection → citation → noise ratio → outcome.
  The console exists to make that loop legible and operable.
- **Secrets are write-only;** reveal is one-shot, never stored in client state.
- **Prod is read-only by default;** mutations need explicit operator grant.

## Design system (committed — preserve identity)
- Tokens: `--bg/--surface/--surface-warm`, ink ramp `--fg/--fg-2/--muted`,
  single accent (sky `#0ea5e9` light / `#4c8dff` dark), semantic
  success/warn/danger, fixed classification ramp (live/dormant/stale/must-build).
- Type: Inter (display+body), IBM Plex Mono for data. Fixed rem-ish px scale
  (`--text-xs`..`--text-4xl`). Tabular-nums everywhere numbers carry meaning.
- Radius 8/12/18; soft elevation; 120/200ms motion, ease `cubic-bezier(.2,0,0,1)`.
- A11y: WCAG AA verified (light `--muted` darkened to clear 4.5:1), reduced-motion
  reset, `pointer:coarse` 44px targets.

## Current direction (2026-06-18)
Bolder pass in **product register** = stronger hierarchy, sharper single accent,
committed density, and — the operator's actual complaint — making **function
legible**: the console currently reads as a static table of mock numbers; it must
read as a live instrument panel. No theatrics (no gradients/glass/neon).
