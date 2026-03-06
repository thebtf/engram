# Phase 5 S1: Extended Relation Types + Memory Type Classification

## Task
Extend the relation type system and add memory type classification for observations.

## Files to Modify

### 1. `pkg/models/relation.go`
Add 11 new RelationType constants after existing 6:

```go
// New Phase 5 relation types for memory graph
RelationLeadsTo        RelationType = "leads_to"
RelationSimilarTo      RelationType = "similar_to"
RelationContradicts    RelationType = "contradicts"
RelationReinforces     RelationType = "reinforces"
RelationInvalidatedBy  RelationType = "invalidated_by"
RelationExplains       RelationType = "explains"
RelationSharesTheme   RelationType = "shares_theme"
RelationParallelCtx   RelationType = "parallel_context"
RelationSummarizes     RelationType = "summarizes"
RelationPartOf         RelationType = "part_of"
RelationPrefersOver    RelationType = "prefers_over"
```

Add them to `AllRelationTypes` slice.

### 2. `pkg/models/observation.go`
Add MemoryType enum after ObservationType:

```go
type MemoryType string

const (
    MemTypeDecision   MemoryType = "decision"
    MemTypePattern    MemoryType = "pattern"
    MemTypePreference MemoryType = "preference"
    MemTypeStyle      MemoryType = "style"
    MemTypeHabit      MemoryType = "habit"
    MemTypeInsight    MemoryType = "insight"
    MemTypeContext    MemoryType = "context"
)

var AllMemoryTypes = []MemoryType{...}
```

Add `MemoryType MemoryType` field to Observation struct (after Type field).
Add to ObservationJSON, MarshalJSON, NewObservation, ToStoredObservation, ParsedObservation.

Add `ClassifyMemoryType(obs *Observation) MemoryType` function with regex-based heuristics:
- concepts contain "architecture"|"design"|"choice" → Decision
- concepts contain "pattern"|"best-practice"|"anti-pattern" → Pattern
- concepts contain "preference"|"config"|"setting" → Preference
- concepts contain "style"|"naming"|"format" → Style
- concepts contain "workflow"|"habit"|"routine" → Habit
- concepts contain "insight"|"discovery"|"gotcha" → Insight
- default → Context

### 3. `internal/db/gorm/models.go`
Add `MemoryType` field to Observation GORM model:
```go
MemoryType  models.MemoryType `gorm:"type:text;index:idx_observations_memory_type"`
```

Update ObservationRelation CHECK constraint in the RelationType tag to include all 17 types.

### 4. `internal/db/gorm/migrations.go`
Add migration `019_extended_relation_types`:
- ALTER observation_relations DROP CONSTRAINT and add new CHECK for all 17 relation types
  (PostgreSQL: use raw SQL to drop old check and add new one)
- ALTER observations ADD COLUMN memory_type TEXT
- CREATE INDEX idx_observations_memory_type ON observations(memory_type)
- Backfill: UPDATE observations SET memory_type = (CASE logic based on type and concepts)

## Constraints
- LANGUAGE: All file content MUST be English. No exceptions.
- PostgreSQL (not SQLite). GORM with `gorm.io/driver/postgres`.
- Keep existing 6 relation types working. Only ADD new ones.
- Migration must be idempotent (check before alter).
- Follow existing migration pattern in migrations.go (numbered functions).
- Do NOT modify any files not listed above.
