# ADR-004: Dedicated Embedding Resilience Layer

## Status

Proposed

## Context

Engram uses a single CircuitBreaker (internal/worker/sdk/processor.go) shared between LLM
and embedding operations. When either fails, both are blocked. Embedding failures cause
vector search to silently fall back to FTS, with no health monitoring or recovery probes.

Cipher implements a dedicated ResilientEmbedder with:
- Separate EmbeddingCircuitBreaker (independent from LLM CB)
- 4 status states: HEALTHY, DEGRADED, DISABLED, RECOVERING
- Health check intervals with automatic recovery probes
- Safe-operations wrapper for graceful degradation
- Max consecutive failure tracking with recovery intervals

Current engram behavior: embedding API timeout → vector search fails → FTS fallback fails →
degraded to "recent-only" results. No logging of transition, no recovery attempt, user sees
stale results without knowing why.

## Decision

Implement dedicated embedding resilience layer separate from LLM circuit breaker.

### Design

1. **Separate `EmbeddingCircuitBreaker`** in `internal/embedding/` with:
   - Independent failure/success tracking from LLM CB
   - 4 states: HEALTHY → DEGRADED (intermittent failures) → DISABLED (consistent failures) → RECOVERING
   - Health check goroutine: periodic ping to embedding API (every 30s when DEGRADED/DISABLED)
   - Automatic recovery: on successful health check, transition DISABLED → RECOVERING → HEALTHY

2. **Status endpoint**: Expose embedding health in `/api/selfcheck` and statusline

3. **Logging**: Log all state transitions (engram investigate report F-0-5 already added CB logging)

## Consequences

### Positive
- Embedding failures don't block LLM extraction
- Automatic recovery without server restart
- Observable embedding health in dashboard
- Faster detection of embedding API issues

### Negative
- Additional goroutine for health checks
- More complex failure handling logic
- Need to coordinate with existing vector search fallback

## References

- Cipher source: `src/core/brain/embedding/resilient-embedder.ts`, `circuit-breaker.ts`
- Engram investigate report F-0-6
- Production incident: embedding API timeout (2026-03-28) went undetected until user reported
