package graph

import (
	"context"
	"fmt"

	"github.com/lib/pq"
	"github.com/thebtf/engram/internal/scope"
)

// NeighborsForSearch selects at most twenty distinct one-hop memory neighbors
// across the authorized seeds. Both endpoints pass the same current read policy
// before ordering or limits; node endpoints cannot alias a memory ID.
func (s *Store) NeighborsForSearch(ctx context.Context, project string, seeds, exclude []int64, limit int, access scope.MemoryReadAccess) ([]scope.MemoryRef, error) {
	if project == "" {
		return nil, fmt.Errorf("memory graph search: project must not be empty")
	}
	if len(seeds) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	predicate, accessArgs := scope.MemoryReadAccessSQL(access)
	args := []any{project}
	args = append(args, accessArgs...)
	args = append(args, pq.Array(seeds), pq.Array(exclude), limit)
	var refs []scope.MemoryRef
	err := s.db.WithContext(ctx).Raw(`
		WITH authorized AS MATERIALIZED (
			SELECT m.id, m.version FROM memories m
			WHERE m.project = ? AND m.status = 'active' AND m.deleted_at IS NULL
			  AND (m.valid_from IS NULL OR m.valid_from <= NOW())
			  AND (m.valid_until IS NULL OR m.valid_until >= NOW())
			  AND (`+predicate+`)
		), seeds AS (
			SELECT id FROM authorized WHERE id = ANY(?)
		), neighbors AS (
			SELECT CASE WHEN e.source_id = seeds.id THEN e.target_id ELSE e.source_id END AS id
			FROM knowledge_edges e JOIN seeds ON e.source_id = seeds.id OR e.target_id = seeds.id
			WHERE e.superseded_at IS NULL AND e.source_type = 'memory' AND e.target_type = 'memory'
		), selected AS MATERIALIZED (
			SELECT DISTINCT a.id, a.version FROM neighbors n JOIN authorized a ON a.id = n.id
			WHERE a.id <> ALL(COALESCE(?::bigint[], '{}'::bigint[])) ORDER BY a.id ASC LIMIT ?
		)
		SELECT s.id, s.version, `+scope.MemoryContentDigestSQL+` AS content_digest
		FROM selected s JOIN memories m ON m.id = s.id AND m.version = s.version ORDER BY s.id ASC
	`, args...).Scan(&refs).Error
	if err != nil {
		return nil, fmt.Errorf("memory graph search: %w", err)
	}
	return refs, nil
}
