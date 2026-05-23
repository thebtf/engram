# Technical Debt

Items deferred from active work with documented impact and fix path.

## Active

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
