# Phase 5 S3: Creative Association Discovery Engine

## Task
Implement an association discovery engine that finds non-obvious relationships between observations using type-pair rules and embedding similarity.

## Architecture
New package: `internal/consolidation/` with file `associations.go`.

## Type-Pair Rules (from automem)

Given two observations A and B with computed embedding similarity:

| Condition | Relation Type | Confidence |
|-----------|--------------|------------|
| Both type=Decision + similarity < 0.3 | contradicts | 0.6 |
| Either {Insight,Pattern} + similarity > 0.5 | explains | 0.7 |
| Any types + similarity > 0.7 | shares_theme | similarity value |
| Within 7 days + similarity < 0.4 | parallel_context | 0.5 |
| Type A=bugfix, B=decision + similarity > 0.4 | leads_to | 0.6 |
| Memory type=Preference + same concept overlap | prefers_over | 0.65 |

## Files to Create

### 1. NEW: `internal/consolidation/associations.go`

```go
package consolidation

import (
    "context"
    "github.com/thebtf/engram/internal/vector"
    "github.com/thebtf/engram/pkg/models"
)

// AssociationEngine discovers creative associations between observations.
type AssociationEngine struct {
    vectorClient vector.Client
    config       AssociationConfig
}

type AssociationConfig struct {
    SampleSize         int     // default 20
    MinSimilarity      float64 // default 0.3
    ThemeSimilarity    float64 // default 0.7
    ExplainSimilarity  float64 // default 0.5
    ParallelMaxDays    int     // default 7
    ParallelMaxSim     float64 // default 0.4
}

func DefaultAssociationConfig() AssociationConfig

func NewAssociationEngine(vectorClient vector.Client, config AssociationConfig) *AssociationEngine

// DiscoverAssociations takes a sample of observations and finds creative associations.
// Returns a list of RelationDetectionResults for new associations found.
func (e *AssociationEngine) DiscoverAssociations(ctx context.Context, observations []*models.Observation) ([]*models.RelationDetectionResult, error)

// computeSimilarity computes cosine similarity between two observations using vector client.
func (e *AssociationEngine) computeSimilarity(ctx context.Context, a, b *models.Observation) (float64, error)
```

For computing similarity: use the vector client's existing embedding infrastructure.
The approach: build text from observation (title + narrative + facts), then use vector search to find similarity.

Actually, a simpler approach: for each pair in the sample, concatenate title+narrative, call the embedding service, compute cosine similarity between the two embedding vectors.

BUT we don't have direct access to the embedding service from consolidation package. Instead:
- Use `vector.Client.Search()` with observation A's text and check if B appears in results with a score.
- OR accept an `embedding.Service` dependency and compute embeddings directly.

Best approach: Accept `embedding.Service` as dependency. Call `embedSvc.Embed(text)` for both observations, compute cosine similarity directly.

```go
type AssociationEngine struct {
    embedSvc embedding.Service  // for computing similarity
    config   AssociationConfig
}
```

Add a `CosineSimilarity(a, b []float32) float64` helper function.

### 2. NEW: `internal/consolidation/similarity.go`

```go
package consolidation

import "math"

// CosineSimilarity computes cosine similarity between two vectors.
func CosineSimilarity(a, b []float32) float64 {
    // standard dot product / (norm_a * norm_b)
}
```

## Constraints
- LANGUAGE: All file content MUST be English. No exceptions.
- Use existing embedding.Service interface for embeddings (internal/embedding/service.go).
- Use existing models.RelationDetectionResult for return values.
- Use existing detection source: DetectionSourceEmbeddingSimilarity.
- Add new detection source "creative_association" to pkg/models/relation.go.
- Do NOT modify any files outside of internal/consolidation/ and the new detection source constant.
- Keep it simple: no external dependencies, no FalkorDB.
