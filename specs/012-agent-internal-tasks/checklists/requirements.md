# Specification Quality Checklist: Agent internal tasks

**Purpose**: Main's design review before accepting the issue #513 specify→plan→tasks package.
**Created**: 2026-10-30
**Feature**: [spec.md](../spec.md)
**Review Ownership**: Main owns this review. Every unchecked item is an actionable future design-review task, not a claim of execution. A later [x] means requirements quality reviewed, never implementation complete.

## Content Quality

- [ ] CHK001 Future design review: confirm spec.md states the useful fresh-consumer journey and one existing tracker, without implementation machinery in its product requirements.
- [ ] CHK002 Future design review: confirm all mandatory spec-template sections are complete and the lone P1 story is independently testable end to end; no sample stories or placeholders remain.
- [ ] CHK003 Future design review: compare the package to Main-supplied issue #513 comments 1730 and 1739, including narrow retry authority, actor+source key isolation, labels versus authorization and CAS loser no-op; reject narrowed acceptance promises.

## Requirement Completeness

- [ ] CHK004 Future design review: check contracts/issues.md for exact input/default/omitted/null/empty semantics, typed success/errors and compatible text/JSON projections for every caller family.
- [ ] CHK005 Future design review: verify existing authenticated-actor + canonical-source + request-key namespace, current auth/visibility before original-payload equality/replay, same-actor equal/mismatch outcomes and different-actor no-disclosure/isolation. Existing create authority, not a new ACL, governs independent creation.
- [ ] CHK006 Future review: preserve accepted retry-only privacy and internal v1 SHA-256 receipt binding, with no private payload/digest exports or secrets/vault permission. Record actual issue-route protection gap versus memory-only helpers/rules; require applicable recognized-class safe refusal without silent metadata mutation, a scanner/framework or comprehensive detection claim. Targeted create-title log uses synthetic RED/GREEN without content evidence.
- [ ] CHK007 Future review: confirm recorded actor mapping (client KeycardID/validated persisted browser user/source-qualified operator singleton), CanonicalProjectExists and no ownership/ACL claim; exact 512-byte UTF-8 key, 10 MiB HTTP/16 MiB native, 1 MiB/depth 256 metadata and consumer-preservable raw-JSON numeric/duplicate domain have no provisional contradiction.
- [ ] CHK008 Future design review: verify monotonic revision, actor-versus-attribution, atomic fields/state/history/comment, no-op semantics and losing UI draft preservation; the winner remains committed and both conflicting field proposals are not promised durable.
- [ ] CHK009 Future review: list 1–200/defaults 50/20; comment/history 50/max 200 with 4 MiB target/continuation/intact oversized singleton/native-envelope safe refusal plus REST access; legacy complete≤200/4 MiB or paging-required, no silent trim/loss or invented HTTP response cap. Current text/both UI cutover exposes completeness.
- [ ] CHK010 Future design review: check old-row ID/comment/provenance conservation, truthful unknown baselines, all-writer activation and non-destructive compatible rollback against data-model.md.

## Feature Readiness

- [ ] CHK011 Future design review: reconcile plan.md's source-backed HTTP/native/OpenClaw/both-UI and bulk/internal-writer map with actual callsites before implementation; no unconditional edit bypass or rollout flag may survive.
- [ ] CHK012 Future design review: trace FR-001–FR-010 and SC-001–SC-005 to tasks.md/quickstart.md, including fresh relevant consumer resume, authorized completion and material privacy/race/migration negatives; no scenario is claimed run.
- [ ] CHK013 Future design review: confirm 012/path resolution and exact original feature-pointer preservation from candidate-bound setup/readback evidence; only specs/012-agent-internal-tasks/** belongs in the final diff.
- [ ] CHK014 Future review: distinguish Main’s P1/delegated-choice authorization (message 39669b21-48b8-43c1-9544-c0cd0a7c94be/comment 1742) from Engram session 01a18a3b’s bounded T001/T002 engineering choices and Node 24.22.1 observations. [x] means design recorded only. T003 is next within #513, not a displacement of in-progress priority #514/#515 or a new approval gate. Lane owns candidate commits; Main owns integration/default/product acceptance/release. No runtime effects in this correction.
- [ ] CHK015 Future design review: confirm every relevant production task follows a focused meaningful synthetic/non-private RED at a named real seam, then GREEN; missing symbols/dependencies/fixture failures are not RED, already-green behavior is not artificially broken, and accepted real-consumer/conservation proof remains required.

## Notes

This reviewer checklist stays open; no runtime/test/product acceptance is claimed. Main comments 1730/1739 own accepted refinements; comment 1742/message 39669b21-48b8-43c1-9544-c0cd0a7c94be accepted P1 and authorized the lane to choose T001/T002. Engram session 01a18a3b made those engineering choices and Node 24.22.1 observations. Their documentation—not implementation/GREEN—is recorded; priority #514/#515 remains undisturbed.
