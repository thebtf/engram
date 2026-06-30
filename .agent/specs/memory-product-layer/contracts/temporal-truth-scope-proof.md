# Temporal Truth Scope Proof

ENG-MPL-1 CR-004 implements selective temporal truth for a narrow chosen fact
set. It does not implement broad graphification.

## Included Fact Classes

| Fact class | Fixture / fact id | Why included |
| --- | --- | --- |
| `deployment_setting` | `deploy.primary_region` | High-value operational setting that can change over time and needs `true now vs then` answers with provenance. |
| `release_policy` | `release.supported_version` | High-value product/release policy fact where old/current answers and invalidation rationale matter. |

These classes are represented only by explicit selected records in
`internal/temporaltruth` tests. A fact outside the selected set returns
`not_selected`.

## Evidence

| Evidence | Path | Proof |
| --- | --- | --- |
| Temporal contract and selected query TDD | `.agent/specs/memory-product-layer/evidence/T001-T002-temporal.tdd.json` | RED before contract/service existed; GREEN after selected-scope implementation; Prove-It failed on `not_selected` broadening and disabled selected filter. |
| G001 temporal gate | `.agent/specs/memory-product-layer/evidence/phase-6-temporal-gate.json` | `go test ./...` passed and recorded explicit/provenance-first/bounded checks. |
| Validity/provenance history TDD | `.agent/specs/memory-product-layer/evidence/T003-temporal-history.tdd.json` | RED before `provenance_chain`; GREEN after chain construction; Prove-It failed when the chain was removed. |
| G002 history gate | `.agent/specs/memory-product-layer/evidence/phase-6-history-gate.json` | `go test ./...` passed and recorded truth evolution/provenance checks. |
| Contract | `.agent/specs/memory-product-layer/contracts/temporal-truth.md` | Names selected fact scope, response shape, provenance chain, and exclusions. |

## Excluded Graph Work

CR-004 does not add:

- graph-wide memory projection
- graph traversal API
- cross-domain truth inference
- learned applicability model
- broad operator-console redesign
- new external graph database
- new microservice or queue broker
- full company-brain ingestion platform

## Why The CR Stayed Narrow

The implementation is an in-process selected-record service. The query request
names one fact id, optional class, and optional project. The service filters by
that selected fact id and returns `not_selected` when the fact is outside the
chosen set. Current and prior answers are shaped from explicit records and
bounded history; they are not inferred by searching or traversing all memories.

The Prove-It evidence deliberately broke the narrowness guard by changing
`not_selected` into `graph_search` and disabling the selected-fact filter; tests
failed. That is the load-bearing proof that CR-004 stayed a selected-fact slice.
