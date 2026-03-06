# Phase 5 S2: Enhanced Relevance Score Formula

## Task
Implement the automem-inspired relevance score formula alongside the existing importance scoring system.

## Formula

```go
decayFactor   = math.Exp(-0.1 * ageDays)                    // base_decay_rate = 0.1/day
accessFactor  = math.Exp(-0.05 * accessRecencyDays)          // 1.0 if recently accessed
relFactor     = 1.0 + 0.3*math.Log1p(float64(relCount))     // log-scaled relationship bonus
relevance     = decayFactor * (0.3 + 0.3*accessFactor) * relFactor * (0.5 + importance) * (0.7 + 0.3*confidence)
```

Where:
- `ageDays`: days since observation created
- `accessRecencyDays`: days since last access (retrieval). If never accessed, use ageDays.
- `relCount`: number of relations this observation has (inbound + outbound)
- `importance`: observation's ImportanceScore (existing field, 0-2 range typically)
- `confidence`: average confidence of relations (default 0.5 if no relations)

## Files to Create/Modify

### 1. NEW: `internal/scoring/relevance.go`
```go
package scoring

type RelevanceCalculator struct {
    config *RelevanceConfig
}

type RelevanceConfig struct {
    BaseDecayRate    float64 // default 0.1
    AccessDecayRate  float64 // default 0.05
    RelationWeight   float64 // default 0.3
    MinRelevance     float64 // default 0.001
}

func DefaultRelevanceConfig() *RelevanceConfig

func NewRelevanceCalculator(config *RelevanceConfig) *RelevanceCalculator

// CalculateRelevance computes the relevance score for an observation.
func (r *RelevanceCalculator) CalculateRelevance(params RelevanceParams) float64

type RelevanceParams struct {
    AgeDays            float64
    AccessRecencyDays  float64
    RelationCount      int
    ImportanceScore    float64
    AvgRelConfidence   float64
}
```

### 2. MODIFY: `internal/db/gorm/migrations.go`
Add to migration 019 (same migration as S1):
```sql
ALTER TABLE observations ADD COLUMN IF NOT EXISTS relevance_score REAL DEFAULT 1.0;
ALTER TABLE observations ADD COLUMN IF NOT EXISTS last_accessed_at_epoch BIGINT;
CREATE INDEX IF NOT EXISTS idx_observations_relevance ON observations(relevance_score DESC);
```

### 3. MODIFY: `internal/db/gorm/models.go`
Add fields to Observation GORM model:
```go
RelevanceScore   float64       `gorm:"type:real;default:1.0;index:idx_observations_relevance,sort:desc"`
LastAccessedAt   sql.NullInt64 `gorm:"column:last_accessed_at_epoch"`
```

### 4. MODIFY: `pkg/models/observation.go`
Add to Observation struct:
```go
RelevanceScore   float64 `db:"relevance_score" json:"relevance_score"`
LastAccessedAt   sql.NullInt64 `db:"last_accessed_at_epoch" json:"last_accessed_at_epoch,omitempty"`
```
Add to ObservationJSON and MarshalJSON.

### 5. MODIFY: `internal/scoring/recalculator.go`
Add relevance recalculation alongside existing importance recalculation.
The Recalculator already runs in a background goroutine. Add a method `RecalculateRelevance` that:
1. Fetches observations needing relevance update
2. For each, gets relation count from RelationStore
3. Computes relevance via RelevanceCalculator
4. Updates the relevance_score column

## Constraints
- LANGUAGE: All file content MUST be English. No exceptions.
- Keep existing ImportanceScore/Calculator working unchanged. Relevance is ADDITIONAL, not replacement.
- PostgreSQL only. GORM with gorm.io/driver/postgres.
- Follow existing code patterns in internal/scoring/.
- Do NOT modify files not listed above.
