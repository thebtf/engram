# Temporal Truth Contract

ENG-MPL-1 CR-004 makes temporal truth queryable only for selected high-value
evolving facts. It is not a broad graph projection and does not infer truth
across all memories.

## Bounded Scope

Each temporal truth query names one selected fact by `fact_id`, optionally
scoped by `fact_class` and `project`. A fact outside the selected set returns
`not_selected` instead of triggering general memory search or graph traversal.

## Response Shape

The response carries:

- `scope`: selected fact metadata and rationale
- `state`: `found`, `not_selected`, or `unknown`
- `true_now`: the current value with validity and provenance
- `true_then`: the value valid at the requested `as_of` instant, when supplied
- `history`: bounded prior/current entries
- `provenance_chain`: ordered provenance from the bounded history

Each history entry carries:

- `value`
- `valid_from`
- `valid_until`
- `invalidated_at`
- `invalidation_rationale`
- `provenance`

## Out Of Scope

- broad graphification of all memories
- cross-domain truth inference
- learned applicability model
- broad operator-console redesign
- new external graph database or microservice
