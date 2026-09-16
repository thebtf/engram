package gorm

import (
	"context"
	"fmt"

	"github.com/lib/pq"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/thebtf/engram/internal/rankfusion"
	"github.com/thebtf/engram/internal/taskmemory"
)

// TaskMemoryCandidateStore reads the existing memories table through the
// TaskMemory scope-before-ranking boundary. It owns no schema or write path.
type TaskMemoryCandidateStore struct {
	db *gorm.DB
}

// NewTaskMemoryCandidateStore creates the bounded TaskMemory candidate reader.
func NewTaskMemoryCandidateStore(store *Store) *TaskMemoryCandidateStore {
	if store == nil {
		return &TaskMemoryCandidateStore{}
	}
	return &TaskMemoryCandidateStore{db: store.DB}
}

type taskMemoryCandidateRow struct {
	ID      int64 `gorm:"column:id"`
	Version int   `gorm:"column:version"`
}

type taskMemoryMaterializedRow struct {
	ID      int64  `gorm:"column:id"`
	Version int    `gorm:"column:version"`
	Project string `gorm:"column:project"`
	Content string `gorm:"column:content"`
}

type taskMemoryAccessPredicate struct {
	sql  string
	args []any
}

func (s *TaskMemoryCandidateStore) validateQuery(ctx context.Context, query taskmemory.AuthorizedCandidateQuery) error {
	if s == nil || s.db == nil || ctx == nil || !query.Valid() {
		return taskmemory.ErrInvalidRequest
	}
	return nil
}

// taskMemoryCandidateAccessSQL is the SQL projection of the existing
// scope.ResolveMemory and DomainOwnershipPolicy read rules. Every retrieval
// leg uses this predicate before its relevance ordering and limit.
const taskMemoryCandidateAccessSQL = `
	m.project = ?
	AND m.status = 'active'
	AND m.deleted_at IS NULL
	AND (m.valid_from IS NULL OR m.valid_from <= NOW())
	AND (m.valid_until IS NULL OR m.valid_until >= NOW())
	AND (
		CASE COALESCE(NULLIF(m.privacy_scope, ''), 'project')
			WHEN 'project' THEN TRUE
			WHEN 'shared' THEN TRUE
			WHEN 'global' THEN TRUE
			WHEN 'private' THEN
				? <> ''
				AND m.source_workstation_id <> ''
				AND m.source_workstation_id = ?
				AND (? = '' OR ? = ANY(m.source_sessions))
			ELSE FALSE
		END
	)
	AND (
		btrim(m.agent_visibility) = ''
		OR btrim(m.agent_visibility) = 'shared'
		OR (
			btrim(m.agent_visibility) = 'private'
			AND btrim(m.owner_principal) <> ''
			AND btrim(m.owner_principal_kind) <> ''
			AND ? <> ''
			AND ? <> ''
			AND btrim(m.owner_principal) = ?
			AND btrim(m.owner_principal_kind) = ?
		)
	)
	AND (
		btrim(m.domain) = ''
		OR (
			btrim(m.owner_principal) <> ''
			AND btrim(m.owner_principal_kind) <> ''
			AND ? <> ''
			AND ? IN ('human', 'agent', 'service')
			AND btrim(m.owner_principal) = ?
			AND btrim(m.owner_principal_kind) = ?
		)
	)
`

// taskMemoryCandidateAccessFromContext shares the retrieval access predicate
// with policy reads while keeping the authorized task context as the authority
// source instead of rebuilding an untrusted query.
func taskMemoryCandidateAccessFromContext(authority taskmemory.AuthorizedTaskContext) taskMemoryAccessPredicate {
	caller := authority.KeycardContext()
	return taskMemoryCandidateAccessWithKeycard(
		string(authority.CanonicalProject()),
		caller.WorkstationID,
		caller.SessionID,
		caller.Principal,
		caller.PrincipalKind,
	)
}

func taskMemoryCandidateAccess(query taskmemory.AuthorizedCandidateQuery) taskMemoryAccessPredicate {
	caller := query.AccessPolicy().KeycardContext()
	return taskMemoryCandidateAccessWithKeycard(
		query.CanonicalProject(),
		caller.WorkstationID,
		caller.SessionID,
		caller.Principal,
		caller.PrincipalKind,
	)
}

func taskMemoryCandidateAccessWithKeycard(
	canonicalProject string,
	workstationID string,
	sessionID string,
	principal string,
	principalKind string,
) taskMemoryAccessPredicate {
	return taskMemoryAccessPredicate{
		sql: taskMemoryCandidateAccessSQL,
		args: []any{
			canonicalProject,
			workstationID,
			workstationID,
			sessionID,
			sessionID,
			principal,
			principalKind,
			principal,
			principalKind,
			principal,
			principalKind,
			principal,
			principalKind,
		},
	}
}

func taskMemoryCandidateRefs(rows []taskMemoryCandidateRow, tier taskmemory.CandidateSourceTier) ([]taskmemory.AuthorizedCandidateRef, error) {
	refs := make([]taskmemory.AuthorizedCandidateRef, 0, len(rows))
	for _, row := range rows {
		ref, err := taskmemory.NewAuthorizedCandidateRef(row.ID, row.Version, tier)
		if err != nil {
			return nil, fmt.Errorf("new authorized task memory candidate reference: %w", err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

type taskMemoryCandidateLookupRow struct {
	ID      int64 `gorm:"column:id"`
	Version int   `gorm:"column:version"`
	Tier    int   `gorm:"column:tier"`
}

func taskMemoryCandidateLookupRefs(rows []taskMemoryCandidateLookupRow) ([]taskmemory.AuthorizedCandidateRef, error) {
	refs := make([]taskmemory.AuthorizedCandidateRef, 0, len(rows))
	for _, row := range rows {
		ref, err := taskmemory.NewAuthorizedCandidateRef(row.ID, row.Version, taskmemory.CandidateSourceTier(row.Tier))
		if err != nil {
			return nil, fmt.Errorf("new authorized task memory candidate reference: %w", err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func (s *TaskMemoryCandidateStore) loadCandidateRefs(
	ctx context.Context,
	querySQL string,
	args []any,
	tier taskmemory.CandidateSourceTier,
) ([]taskmemory.AuthorizedCandidateRef, error) {
	var rows []taskMemoryCandidateRow
	if err := s.db.WithContext(ctx).Raw(querySQL, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return taskMemoryCandidateRefs(rows, tier)
}

const taskMemoryExactSQL = `
	SELECT m.id, m.version
	FROM memories m
	WHERE ` + taskMemoryCandidateAccessSQL + `
	  AND m.content = ?
	ORDER BY m.created_at DESC, m.id DESC
	LIMIT ?
`

const taskMemoryFTSSQL = `
	WITH parsed AS (
		SELECT websearch_to_tsquery('english', ?) AS wsq,
		       plainto_tsquery('english', ?) AS ptq
	)
	SELECT m.id, m.version
	FROM memories m, parsed
	WHERE ` + taskMemoryCandidateAccessSQL + `
	  AND m.search_vector @@ COALESCE(NULLIF(parsed.wsq, ''::tsquery), parsed.ptq)
	ORDER BY ts_rank_cd(m.search_vector, COALESCE(NULLIF(parsed.wsq, ''::tsquery), parsed.ptq)) DESC,
	         m.created_at DESC,
	         m.id DESC
	LIMIT ?
`

const taskMemoryVectorSimilarityThreshold = 0.7

const taskMemoryVectorSQL = `
	WITH authorized_chunks AS (
		SELECT m.id, m.version, c.embedding <=> ?::vector AS distance
		FROM content_chunks c
		JOIN memories m ON m.id = c.memory_id
		WHERE ` + taskMemoryCandidateAccessSQL + `
		  AND c.embedding IS NOT NULL
	),
	ranked AS (
		SELECT id, version, MIN(distance) AS distance
		FROM authorized_chunks
		GROUP BY id, version
	)
	SELECT id, version
	FROM ranked
	WHERE 1 - distance >= ?
	ORDER BY distance ASC, id ASC
	LIMIT ?
`

func taskMemoryLexicalSnapshot(fts []taskmemory.AuthorizedCandidateRef) (taskmemory.CandidateSnapshot, error) {
	if len(fts) == 0 {
		snapshot, err := taskmemory.NewCandidateSnapshot(taskmemory.RetrievalEmpty, nil)
		if err != nil {
			return taskmemory.CandidateSnapshot{}, fmt.Errorf("new empty task memory snapshot: %w", err)
		}
		return snapshot, nil
	}
	snapshot, err := taskmemory.NewCandidateSnapshot(taskmemory.RetrievalLexicalDegraded, fts)
	if err != nil {
		return taskmemory.CandidateSnapshot{}, fmt.Errorf("new FTS task memory snapshot: %w", err)
	}
	return snapshot, nil
}

func taskMemoryHybridCandidateRefs(
	fts []taskmemory.AuthorizedCandidateRef,
	vectors []taskmemory.AuthorizedCandidateRef,
	limit int,
) ([]taskmemory.AuthorizedCandidateRef, error) {
	ftsIDs := make([]int64, 0, len(fts))
	ftsRanks := make(map[int64]int, len(fts))
	ftsRefs := make(map[int64]taskmemory.AuthorizedCandidateRef, len(fts))
	for rank, ref := range fts {
		id := ref.ID()
		ftsIDs = append(ftsIDs, id)
		ftsRanks[id] = rank
		ftsRefs[id] = ref
	}

	vectorIDs := make([]int64, 0, len(vectors))
	vectorRanks := make(map[int64]int, len(vectors))
	vectorRefs := make(map[int64]taskmemory.AuthorizedCandidateRef, len(vectors))
	for rank, ref := range vectors {
		id := ref.ID()
		vectorIDs = append(vectorIDs, id)
		vectorRanks[id] = rank
		vectorRefs[id] = ref
	}

	fusedIDs := rankfusion.RRF(ftsIDs, vectorIDs, 60)
	if len(fusedIDs) > limit {
		fusedIDs = fusedIDs[:limit]
	}
	refs := make([]taskmemory.AuthorizedCandidateRef, 0, len(fusedIDs))
	for _, id := range fusedIDs {
		ftsRank, inFTS := ftsRanks[id]
		vectorRank, inVector := vectorRanks[id]
		if !inFTS && !inVector {
			return nil, fmt.Errorf("fused task memory candidate %d has no source", id)
		}
		source := vectorRefs[id]
		tier := taskmemory.CandidateVector
		if inFTS && (!inVector || ftsRank <= vectorRank) {
			source = ftsRefs[id]
			tier = taskmemory.CandidateFTS
		}
		ref, err := taskmemory.NewAuthorizedCandidateRef(source.ID(), source.Version(), tier)
		if err != nil {
			return nil, fmt.Errorf("new fused task memory candidate reference: %w", err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// Snapshot returns exact matches when present; otherwise it returns bounded lexical or hybrid candidate references.
func (s *TaskMemoryCandidateStore) Snapshot(
	ctx context.Context,
	query taskmemory.AuthorizedCandidateQuery,
) (taskmemory.CandidateSnapshot, error) {
	if err := s.validateQuery(ctx, query); err != nil {
		return taskmemory.CandidateSnapshot{}, err
	}
	access := taskMemoryCandidateAccess(query)

	exactArgs := make([]any, 0, len(access.args)+2)
	exactArgs = append(exactArgs, access.args...)
	exactArgs = append(exactArgs, query.Query(), query.Limit())
	exact, err := s.loadCandidateRefs(ctx, taskMemoryExactSQL, exactArgs, taskmemory.CandidateExact)
	if err != nil {
		return taskmemory.CandidateSnapshot{}, fmt.Errorf("task memory exact candidates: %w", err)
	}
	if len(exact) != 0 {
		snapshot, err := taskmemory.NewCandidateSnapshot(taskmemory.RetrievalExact, exact)
		if err != nil {
			return taskmemory.CandidateSnapshot{}, fmt.Errorf("new exact task memory snapshot: %w", err)
		}
		return snapshot, nil
	}

	ftsArgs := make([]any, 0, len(access.args)+3)
	ftsArgs = append(ftsArgs, query.Query(), query.Query())
	ftsArgs = append(ftsArgs, access.args...)
	ftsArgs = append(ftsArgs, query.Limit())
	fts, err := s.loadCandidateRefs(ctx, taskMemoryFTSSQL, ftsArgs, taskmemory.CandidateFTS)
	if err != nil {
		return taskmemory.CandidateSnapshot{}, fmt.Errorf("task memory FTS candidates: %w", err)
	}
	if !query.VectorEnabled() {
		return taskMemoryLexicalSnapshot(fts)
	}

	vector := pgvector.NewVector(query.Vector())
	vectorArgs := make([]any, 0, len(access.args)+3)
	vectorArgs = append(vectorArgs, vector)
	vectorArgs = append(vectorArgs, access.args...)
	vectorArgs = append(vectorArgs, taskMemoryVectorSimilarityThreshold, query.Limit())
	vectors, err := s.loadCandidateRefs(ctx, taskMemoryVectorSQL, vectorArgs, taskmemory.CandidateVector)
	if err != nil {
		// Vector lookup is best-effort after FTS so store failure preserves lexical retrieval.
		return taskMemoryLexicalSnapshot(fts)
	}

	hybrid, err := taskMemoryHybridCandidateRefs(fts, vectors, query.Limit())
	if err != nil {
		return taskmemory.CandidateSnapshot{}, fmt.Errorf("fuse task memory candidates: %w", err)
	}
	if len(hybrid) == 0 {
		snapshot, err := taskmemory.NewCandidateSnapshot(taskmemory.RetrievalEmpty, nil)
		if err != nil {
			return taskmemory.CandidateSnapshot{}, fmt.Errorf("new empty task memory snapshot: %w", err)
		}
		return snapshot, nil
	}
	snapshot, err := taskmemory.NewCandidateSnapshot(taskmemory.RetrievalHybrid, hybrid)
	if err != nil {
		return taskmemory.CandidateSnapshot{}, fmt.Errorf("new hybrid task memory snapshot: %w", err)
	}
	return snapshot, nil
}

const taskMemoryByRefsSQL = `
	WITH requested AS (
		SELECT requested_refs.id, requested_refs.tier, requested_refs.ordinality
		FROM unnest(?::bigint[], ?::smallint[]) WITH ORDINALITY AS requested_refs(id, tier, ordinality)
	)
	SELECT m.id, m.version, requested.tier
	FROM requested
	JOIN memories m ON m.id = requested.id
	WHERE ` + taskMemoryCandidateAccessSQL + `
	ORDER BY requested.ordinality ASC
	LIMIT ?
`

// GetAuthorizedByRefs returns visible requested references in caller order.
// Missing and unauthorized IDs are deliberately indistinguishable because both
// are omitted before the ordered result is formed.
func (s *TaskMemoryCandidateStore) GetAuthorizedByRefs(
	ctx context.Context,
	query taskmemory.AuthorizedCandidateQuery,
	lookups []taskmemory.CandidateLookup,
) ([]taskmemory.AuthorizedCandidateRef, error) {
	if err := s.validateQuery(ctx, query); err != nil {
		return nil, err
	}
	if len(lookups) == 0 {
		return []taskmemory.AuthorizedCandidateRef{}, nil
	}
	if len(lookups) > taskmemory.MaxCandidateLookups {
		return nil, taskmemory.ErrInvalidRequest
	}
	seen := make(map[int64]struct{}, len(lookups))
	for _, lookup := range lookups {
		if !lookup.Valid() {
			return nil, taskmemory.ErrInvalidRequest
		}
		if _, duplicate := seen[lookup.ID()]; duplicate {
			return nil, taskmemory.ErrInvalidRequest
		}
		seen[lookup.ID()] = struct{}{}
	}

	ids := make([]int64, len(lookups))
	tiers := make([]int64, len(lookups))
	for index, lookup := range lookups {
		ids[index] = lookup.ID()
		tiers[index] = int64(lookup.SourceTier())
	}

	access := taskMemoryCandidateAccess(query)
	args := make([]any, 0, len(access.args)+3)
	args = append(args, pq.Array(ids), pq.Array(tiers))
	args = append(args, access.args...)
	args = append(args, query.Limit())

	var rows []taskMemoryCandidateLookupRow
	if err := s.db.WithContext(ctx).Raw(taskMemoryByRefsSQL, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("task memory authorized candidates by reference: %w", err)
	}
	refs, err := taskMemoryCandidateLookupRefs(rows)
	if err != nil {
		return nil, fmt.Errorf("task memory authorized candidates by reference: %w", err)
	}
	return refs, nil
}

const taskMemoryMaterializeSQL = `
	SELECT m.id, m.version, m.project, m.content
	FROM memories m
	WHERE m.id = ?
	  AND m.version = ?
	  AND ` + taskMemoryCandidateAccessSQL + `
	LIMIT 1
`

// Materialize repeats the exact ID, version, active-status, and scope checks in
// the same SQL statement that reads the source body. Missing, denied, and
// version-drifted references all return found=false without source detail.
func (s *TaskMemoryCandidateStore) Materialize(
	ctx context.Context,
	authority taskmemory.AuthorizedTaskContext,
	reference taskmemory.AuthorizedCandidateRef,
) (taskmemory.MaterializedCandidate, bool, error) {
	if s == nil || s.db == nil || ctx == nil || !authority.Valid() || !reference.Valid() {
		return taskmemory.MaterializedCandidate{}, false, taskmemory.ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return taskmemory.MaterializedCandidate{}, false, err
	}

	access := taskMemoryCandidateAccessFromContext(authority)
	args := make([]any, 0, len(access.args)+2)
	args = append(args, reference.ID(), reference.Version())
	args = append(args, access.args...)

	var rows []taskMemoryMaterializedRow
	if err := s.db.WithContext(ctx).Raw(taskMemoryMaterializeSQL, args...).Scan(&rows).Error; err != nil {
		return taskmemory.MaterializedCandidate{}, false, fmt.Errorf("materialize authorized task memory candidate: %w", err)
	}
	if len(rows) == 0 {
		return taskmemory.MaterializedCandidate{}, false, nil
	}
	if len(rows) != 1 || rows[0].ID != reference.ID() || rows[0].Version != reference.Version() {
		return taskmemory.MaterializedCandidate{}, false, fmt.Errorf("materialize authorized task memory candidate: exact reference mismatch")
	}

	materialized, err := taskmemory.NewMaterializedCandidate(reference, rows[0].Project, rows[0].Content)
	if err != nil {
		return taskmemory.MaterializedCandidate{}, false, nil
	}
	return materialized, true, nil
}

var _ taskmemory.Materializer = (*TaskMemoryCandidateStore)(nil)
