# Phase 5 S4: Consolidation Scheduler

## Task
Implement a background consolidation scheduler that runs lifecycle tasks on a schedule.

## Schedule

| Cycle | Interval | Task |
|-------|----------|------|
| Decay | Daily (24h) | Recalculate relevance scores for all non-archived observations |
| Associations | Weekly (168h) | Run creative association discovery on random sample of 20 observations |
| Clustering | Monthly (720h) | Find connected components in relation graph, create summary observations for clusters >= 5 |
| Forgetting | Quarterly (2160h) | Archive observations below relevance threshold (disabled by default) |

## Protection Rules (never auto-archive)
- `importance_score >= 0.7`
- Age < 90 days (grace period)
- ObservationType in {decision, discovery}
- MemoryType in {Decision, Insight}

## Files to Create

### 1. NEW: `internal/consolidation/scheduler.go`

```go
package consolidation

import (
    "context"
    "time"
    "github.com/rs/zerolog"
)

type Scheduler struct {
    relevanceCalc  *scoring.RelevanceCalculator
    assocEngine    *AssociationEngine
    obsStore       ObservationProvider  // interface for fetching observations
    relStore       RelationProvider     // interface for relation operations
    config         SchedulerConfig
    logger         zerolog.Logger
}

// ObservationProvider is the subset of observation store methods needed.
type ObservationProvider interface {
    GetAllObservations(project string) ([]*models.Observation, error)
    GetRecentObservations(project string, limit int) ([]*models.Observation, error)
    UpdateObservation(obs *models.Observation) error
    ArchiveObservation(id int64, reason string) error
}

// RelationProvider is the subset of relation store methods needed.
type RelationProvider interface {
    GetRelationsByObservationID(id int64) ([]*models.ObservationRelation, error)
    StoreRelations(relations []*models.ObservationRelation) error
    GetRelationCount(id int64) (int, error)
    GetRelationGraph(centerID int64, maxHops int) (*models.RelationGraph, error)
}

type SchedulerConfig struct {
    DecayInterval       time.Duration // default 24h
    AssociationInterval time.Duration // default 168h
    ClusterInterval     time.Duration // default 720h
    ForgetInterval      time.Duration // default 2160h
    ForgetEnabled       bool          // default false
    ForgetThreshold     float64       // default 0.01
    SampleSize          int           // default 20
    MinClusterSize      int           // default 5
}

func DefaultSchedulerConfig() SchedulerConfig

func NewScheduler(relevanceCalc, assocEngine, obsStore, relStore, config, logger) *Scheduler

// Start begins the scheduler's background loops. Call from a goroutine.
func (s *Scheduler) Start(ctx context.Context)

// Stop signals the scheduler to shut down gracefully.
func (s *Scheduler) Stop()

// RunDecay recalculates relevance scores for all observations.
func (s *Scheduler) RunDecay(ctx context.Context) error

// RunAssociations discovers creative associations.
func (s *Scheduler) RunAssociations(ctx context.Context) error

// RunClustering finds connected components and creates summaries.
func (s *Scheduler) RunClustering(ctx context.Context) error

// RunForgetting archives low-relevance observations.
func (s *Scheduler) RunForgetting(ctx context.Context) error

// RunAll triggers all consolidation tasks manually.
func (s *Scheduler) RunAll(ctx context.Context) error
```

The Start method uses time.Ticker for each cycle:
```go
func (s *Scheduler) Start(ctx context.Context) {
    decayTicker := time.NewTicker(s.config.DecayInterval)
    assocTicker := time.NewTicker(s.config.AssociationInterval)
    // ... etc
    for {
        select {
        case <-ctx.Done():
            return
        case <-decayTicker.C:
            s.RunDecay(ctx)
        case <-assocTicker.C:
            s.RunAssociations(ctx)
        // ...
        }
    }
}
```

### 2. MODIFY: Ensure interfaces are satisfied

The ObservationProvider and RelationProvider interfaces must be satisfied by existing stores.
Check that gorm.ObservationStore and gorm.RelationStore have the needed methods.

If any methods are missing (like ArchiveObservation), add them to the relevant store.

## Constraints
- LANGUAGE: All file content MUST be English. No exceptions.
- Use existing stores via interfaces (no direct GORM dependency in consolidation package).
- The scheduler runs in a goroutine, must be context-cancellable.
- Log all operations via zerolog.
- Clustering uses existing GetRelationGraph for finding connected components.
- Do NOT add FalkorDB. All graph operations via PostgreSQL.
- Do NOT modify files outside internal/consolidation/ except for missing interface methods.
