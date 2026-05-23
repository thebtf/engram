# Technical Debt

Items deferred from active work with documented impact and fix path.

## Active

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

