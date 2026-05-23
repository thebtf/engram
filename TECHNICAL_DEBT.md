# Technical Debt

Items deferred from active work with documented impact and fix path.

## Active

### TD-008 — GoReleaser `Release` workflow broken since 2026-04-26
**Branch:** `main`
**Severity:** LOW (no user-facing impact; pre-existing flake on 8 consecutive releases)
**Source:** Discovered post v6.4.0 release verification (2026-05-23)

**What:** `.github/workflows/release.yaml` runs `goreleaser release --clean`
which invokes `scripts/generate-plugin-config.sh`. The script tries to copy
`plugin/.claude-plugin/marketplace.json` to the goreleaser output dir, but
that file was DELETED in commit `653fabb` (2026-04-something — "chore:
deps update, migration fix, marketplace cleanup"). The script was never
updated. As a result the `Release` workflow has failed on EVERY release
since v5.2.5 (verified via `gh run list`):
v5.2.5 / v6.0.0 / v6.0.1 / v6.1.0 / v6.2.0 / v6.2.1 / v6.3.0 / v6.4.0.

**Why this hasn't blocked anything:** User-facing release artefacts
(binaries, Docker images, plugin marketplace sync) are produced by SEPARATE
workflows that all succeed:
- `release-binary.yml` → uploads engram-{linux,darwin,windows} assets
- `docker-publish.yml` / `docker.yaml` → push ghcr.io images
- `sync-marketplace.yml` → mirror plugin/engram/ into engram-marketplace repo

GoReleaser produces additional archives + cosign signatures that no
downstream consumer requires. The failing workflow is dead weight.

**Why deferred:** Pre-existing breakage older than CR-001. v6.4.0 release
itself is functionally complete (verified via /api/version=v6.4.0 on the
live server post-Watchtower). Fix can land in a separate small CR without
blocking any in-flight work.

**Upgrade path:** Two options:
1. Remove the broken `cp ... marketplace.json` line from
   `scripts/generate-plugin-config.sh` (the file lives in the separate
   `thebtf/engram-marketplace` repo per MEMORY.md infrastructure note).
2. Delete `.github/workflows/release.yaml` + `scripts/generate-plugin-config.sh`
   entirely if GoReleaser is no longer needed (release-binary.yml +
   docker-publish.yml + sync-marketplace.yml cover all user paths).

Option 1 is the minimal fix; Option 2 is the clean-shop. Decide when
opening the CR.

**Files:** `.github/workflows/release.yaml`,
`scripts/generate-plugin-config.sh`, `.goreleaser.yaml`.

---

### TD-007 — Concurrent Enable/Disable race for same subsystem name
**Branch:** `feat/v7-core`
**Severity:** LOW (no current code path exercises it; advisory only)
**Source:** PR #218 Gemini review findings (CodeRabbit did NOT flag — duplicate-call risk only)

**What:** `registry.Enable(name)` and `registry.Disable(name)` snapshot impl+deps
under `r.mu`, then RELEASE the lock around `Subsystem.Start/Stop` (the deadlock fix
in c2a8fdc). Two concurrent goroutines calling `Enable("x")` for the same name
can BOTH pass the state check and BOTH call `Start` before either commits the
state change — `Start` would be invoked twice.

**Why deferred:** Current code paths exercising Enable are sequential:
- `NewService` activates NoOps in a single for-loop
- No operator API exposes Enable concurrently

The race is reachable only via future operator-restart endpoints calling
Enable from concurrent HTTP handlers for the same subsystem name — a path
that does not yet exist.

**Upgrade path:** Add per-entry mutex OR intermediate "enabling"/"disabling"
states to the ADR-009 state machine, with concurrent callers short-circuiting
on detecting the in-progress state. Regression test:
two goroutines call `Enable("x")` simultaneously — Start counter must equal 1.

**Files:** `internal/cognitive/core/registry.go` Enable/Disable.

---

### TD-006 — `foldKey` tag value escaping
**Branch:** `feat/v7-core`
**Severity:** LOW (internal-only tag sources)
**Source:** PR #218 Gemini review finding

**What:** `internal/cognitive/core/meter.go::foldKey(name, tags)` joins tag
key/value pairs as `name{k=v,k=v}` without escaping `=`, `,`, or `}` in
values. A maliciously-crafted tag value containing `,subsystem=X` could
produce a fold-key that the `filterSnapshotBySubsystem` parser would
misinterpret as belonging to a different subsystem.

**Why deferred:** Tag sources are all internal Go code paths (event names,
subsystem names from RegisterNoOps, hardcoded counter names). No external
input reaches `foldKey` in current v7 scope. The threat surface only opens
if S5 product-metric tags accept user-supplied values.

**Upgrade path:** When S5 lands, add `url.QueryEscape` or a custom escaper
on values + matching unescaping in `foldKeyContainsTag`. Regression test:
tag value containing `,evil=true` must NOT match filter for "evil" tag.

**Files:** `internal/cognitive/core/meter.go::foldKey`,
`internal/worker/handlers_stats_v7.go::foldKeyContainsTag`.

---

### TD-005 — `ObserveHistogram` O(n) eviction on overflow
**Branch:** `feat/v7-core`
**Severity:** LOW (eviction fires only at 10k-observation cap; not a v7 hot path)
**Source:** PR #218 Gemini review finding

**What:** When a histogram reaches `maxHistogramObservations` (10,000),
`ObserveHistogram` uses `copy(obs, obs[1:])` + `obs[:n-1]` to drop the oldest
observation — O(n) memcopy on every overflow write. At sustained overflow
this is wasteful relative to a ring buffer with constant-time write.

**Why deferred:** NoOps do not call ObserveHistogram; v7-core ships only
NoOps as concrete subsystems. The eviction path is unreachable in current
scope. When real subsystems start emitting histograms, refactor BEFORE the
performance becomes a hot path.

**Upgrade path:** Replace flat `[]float64` with a ring-buffer struct
(`buf [N]float64; head int; size int`). Snapshot() materialises in
chronological order. Regression test: existing
`TestObserveHistogram_PercentilesCorrect` remains green; add
`TestObserveHistogram_RingEviction_O1` measuring per-write ns.

**Files:** `internal/cognitive/core/meter.go::ObserveHistogram`.

---

### TD-004 — T021 capture-baseline.sh + real v6.3.0 fixtures
**Branch:** `feat/v7-core`
**Severity:** MEDIUM (release-blocker once v6.3.0 → v7.0.0 cutover ships; not blocking SG-3/SG-4 internal gates)
**Source:** Task T021 of CR-001-initial-scope (engram-v7-core)

**What:** Two pieces of the FR-9 byte-identity baseline pipeline are scaffolded
but not yet executable end-to-end:

1. **`scripts/capture-baseline.sh`** — referenced by `make rebaseline-v6` but
   does not yet exist. The script must (a) spin a clean PostgreSQL test stand,
   (b) launch the supplied v6.3.0 binary with ENGRAM_AUTH_DISABLED=true,
   (c) seed the curated test session described in
   `internal/cognitive/core/testdata/v6_3_0_baseline/README.md`, (d) call
   GetSessionStartContext via grpcurl + MCP `tools/list`, (e) pipe each payload
   through a small Go helper that applies `pkg/cognitive.NormalizeForDiff`,
   (f) emit a tarball with the two normalized JSONs.

2. **Real captured fixtures** — current fixtures under
   `internal/cognitive/core/testdata/v6_3_0_baseline/` are SYNTHETIC. They
   enforce the FR-9 gate machinery (TestNormalizedByteIdentity_v6_3_0_Baseline
   verifies (a) non-empty + opens with '{', (b) zero VolatileFields, (c)
   NormalizeForDiff idempotence, (d) ≤ 50 KB) but do not yet prove
   v7-master-off equals the real v6.3.0 binary output.

**Why deferred:** Running v6.3.0 + PostgreSQL inline in this session would
require CI infrastructure not available locally. The synthetic fixtures
already exercise the gate logic; promoting to real captures is mechanical
once the CI step exists.

**Upgrade path:**
1. Add `scripts/capture-baseline.sh` following the README capture procedure.
2. Run `make rebaseline-v6` in CI (or a release-prep workstation).
3. Commit the resulting real fixtures with explicit ADR amendment + PR review
   (per Clarify C3).
4. TestNormalizedByteIdentity_v6_3_0_Baseline does not change — it is fixture-
   agnostic at the byte level.

**Files:**
- `Makefile` rebaseline-v6 target (present, references scripts/capture-baseline.sh)
- `internal/cognitive/core/testdata/v6_3_0_baseline/{session_start_response.json, tools_list_response.json, README.md}` (synthetic now)

---

### TD-003 — T017 Variant A (pre-plug build) benchmark gap
**Branch:** `feat/v7-core`
**Severity:** LOW (does not block release; NFR-2 verified via Variant B vs C delta)
**Source:** Task T017 of CR-001-initial-scope (engram-v7-core)

**What:** The post-PM-Fix-6 plan defined three benchmark variants:
- Variant A: pre-plug build (no plug machinery linked) — selected via `//go:build !plugv7`
- Variant B: plug linked, master flag OFF
- Variant C: plug active, all subsystem flags OFF, 5 NoOps registered+enabled

Variants B + C are implemented in `internal/cognitive/core/bench_test.go`. Variant A is
NOT implemented inline because the binary unconditionally links the plug machinery (T014).
The task AC anticipated this: "if не feasible via tags, use git-cherry-pick approach in CI".

**Why deferred:** Adding a real `//go:build !plugv7` tag would require duplicating large
parts of T014 wiring behind conditional compilation, increasing complexity without
proportional value. The Variant B numbers (0.2 ns/op for master-off) already prove the
dead-code path is essentially free; Variant A would add nothing actionable.

**Upgrade path:** When the FR-9 byte-identity gate (SG-4 T020) compares v6.3.0 fixtures to
plug-linked-master-off runtime, that comparison fulfills the Variant A role at the
release-blocker layer (a 0-byte diff proves the plug-linked binary produces identical
output to the pre-plug v6.3.0 binary). If finer profiling becomes necessary, the
canonical capture procedure in T021's `rebaseline-v6` Makefile target can be repurposed
to also produce a Variant A benchmark binary.

**Files:** `internal/cognitive/core/bench_test.go` (B + C variants present)
