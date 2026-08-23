# Security and Privacy Requirements Checklist: Engram Architectural Recovery

**Purpose**: Review whether identity, evidence, retrieval, migration, and receipt requirements
protect authorization, scope, secrets, and private data.
**Created**: 2026-08-22
**Feature**: `../spec.md`, `../data-model.md`, `../contracts/`
**Review Ownership**: Requirements-quality reviewer. `[x]` means requirements quality only.

## Authorization and Scope Requirements

- [x] CHK001 Are canonical project, principal, privacy, and source validation required before each
      mutation, job claim, retrieval selection, merge, import, or export? [Completeness, FR-001]
- [x] CHK002 Are copied anchors, ambiguous legacy selectors, unresolved scope, and cross-project
      request behavior defined as fail-closed before data disclosure or mutation? [Safety, Identity
      Contract]
- [x] CHK003 Are exceptions that preserve caller-supplied project filters/admin targets explicitly
      bounded by authorization and audit requirements? [Clarity, Identity Inventory]

## Data Protection Requirements

- [x] CHK004 Are transcript redaction, credential sealing, privacy non-widening, restricted
      evidence links, and safe fingerprint requirements specified for all durable models?
      [Completeness, Data Model]
- [x] CHK005 Are raw secrets, credentials, private payloads, and credential-bearing URLs prohibited
      from contracts, logs, exports, analysis, and merge equality data? [Consistency, Constitution]
- [x] CHK006 Are outcome, exposure, query, and capability-health records required to retain only
      authorized/redacted correlation data? [Coverage, FR-017..FR-020]

## Threat and Recovery Coverage

- [x] CHK007 Are provider, cache, projection, and legacy-compatibility failures prohibited from
      bypassing privacy, authorization, identity, or reconciliation boundaries? [Consistency,
      Contracts]
- [x] CHK008 Are credential conflicts, privacy-scope conflicts, copied anchors, and unsupported
      legacy clients required to quarantine or require explicit decision rather than default merge?
      [Safety, Merge Contract]
- [x] CHK009 Are backup/export/rollback requirements constrained to same-scope, redacted, auditable
      evidence rather than unrestricted data copies? [Coverage, FR-025]

- [x] CHK010 Are S4 migration/production release review and human authorization boundaries
      specified without treating AR-0 source-only analysis as security acceptance? [Scope, Research]

## Notes

**AR-0 review result: 8/10 supported (PASS items: CHK002, CHK004-CHK010; findings: CHK001, CHK003).**

- **CHK001 remains unchecked — FAIL:** `project-identity-v3.md:59-64` and the intake/retrieval/reconciliation contracts gate canonical resolution and authorization, but `processing-job.md:36-45` defines job claims only by persisted eligibility, lease, and fencing; no AR-0 contract explicitly requires principal, privacy, and source validation before every job claim and every mutation/merge/import/export path.
- **CHK003 remains unchecked — FAIL:** `project-identity-v3.md` and `project-merge-manifest.schema.json` define canonical resolution, opaque actor references, approvals, and audit/rollback references, but no AR-0 artifact specifies bounded exceptions for caller-supplied project filters or admin targets with explicit authorization and audit semantics.

**R2 re-review result: 10/10 supported (PASS items: CHK001-CHK010; remaining unmet IDs: none).**

- **CHK001 now supported:** `plan.md` §Target Module Boundaries requires canonical project, principal, privacy, source/provenance, and authorization context before every project-scoped claim, mutation, merge, import, export, cache write, and retrieval selection; the intake, processing-job, retrieval, reconciliation, and compatibility contracts provide operation-specific validation and fail-closed behavior.
- **CHK003 now supported:** `project-identity-v3.md` §4.1 names the only caller-supplied-project exceptions (read-only filters and explicitly privileged administrative targets), requires authorization before target evaluation, preserves principal/privacy restrictions, emits redacted audit correlation, forbids enumeration, and assigns owner/operation/sunset/scan boundaries.


## R3 Final Review Notes

- Review iteration: final AR-0 requirements-quality recheck after R1 remediation and task generation, 2026-08-22.
- Result: **PASS** — 10/10 checked; no regressions identified.
- Rechecked CHK001-CHK010 against the final `plan.md`, `spec.md`, `tasks.md`, `quickstart.md`, project-identity/legacy-compatibility/processing-job/session-evidence/retrieval/knowledge/outcome contracts, migration and merge schemas, data model, and final identity/data/source/package/flag inventories. Canonical scope and authorization gates, bounded administrative/read-filter exceptions, fail-closed copied/ambiguous behavior, redaction and credential rules, privacy non-widening, quarantine, same-scope backup/export/rollback, adjacent-capability preservation, and the AR-0-only security-review boundary remain complete and consistent.
- Exact regression IDs: none.

## R4 Review Notes

- Review iteration: AR-0 R4 requirements-quality regression recheck after R2 changes, 2026-08-22.
- Result: **PASS** — 10/10 checked; no regressions identified.
- Rechecked the temporary `ENGRAM_INJECT_UNIFIED` control's canonical-project/privacy validation and explicit-empty safe branch, plus T083's added authentication compatibility scope; authorization, redaction, privacy non-widening, quarantine, and rollback boundaries remain intact.
- Exact regression IDs: none.

## AR-2 Delta Reconciliation

- [x] AR2-CHK011 Does every V3-capable adapter submit an explicit descriptor without client-selected canonical keys, credentials, raw private paths, or resolution-generated client instance IDs? [AR-2 descriptor contract]
- [x] AR2-CHK012 Are refusal-before-scoped-access, zero mutation, read-filter/admin authorization, redacted audit, and no-key-on-refusal preserved across HTTP, gRPC, MCP, and daemon paths? [AR-2 outcomes/transport contract]

- Result: **PASS** — 12/12 requirements-quality checks. The delta raises no new authorization, credential, or privacy category and preserves the existing S4 migration/production boundaries.
