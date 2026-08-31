package gorm

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/thebtf/engram/internal/intervention"
	"github.com/thebtf/engram/internal/taskmemory"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

const interventionPolicyMaxCompileSources = 128

const interventionPolicySourceColumns = `
	m.id,
	m.version,
	m.project AS canonical_project,
	m.content,
	m.tags,
	m.status,
	m.deleted_at,
	m.valid_from,
	m.valid_until,
	m.superseded_by,
	m.privacy_scope,
	m.source_workstation_id,
	m.source_sessions,
	m.owner_principal,
	m.owner_principal_kind,
	m.agent_visibility,
	m.domain,
	(m.status = 'active'
	 AND m.deleted_at IS NULL
	 AND m.superseded_by IS NULL
	 AND (m.valid_from IS NULL OR m.valid_from <= NOW())
	 AND (m.valid_until IS NULL OR m.valid_until >= NOW())) AS source_current`

const interventionPolicyCompileSourcesSQL = `
	SELECT ` + interventionPolicySourceColumns + `
	FROM memories AS m
	WHERE m.project ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
	  AND m.status = 'active'
	  AND m.deleted_at IS NULL
	  AND m.superseded_by IS NULL
	  AND (m.valid_from IS NULL OR m.valid_from <= NOW())
	  AND (m.valid_until IS NULL OR m.valid_until >= NOW())
	  AND NOT EXISTS (
		SELECT 1
		FROM intervention_evidence_policies AS p
		WHERE p.memory_id = m.id
		  AND p.memory_version = m.version
		  AND p.canonical_project::text = lower(m.project)
		  AND p.compiler_version = ?
		  AND p.algorithm_version = ?
		  AND p.normalization_version = ?
		  AND p.parameter_version = ?
	)
	ORDER BY m.id ASC, m.version ASC
	LIMIT ?
`

const interventionPolicyLockedSourceSQL = `
	SELECT ` + interventionPolicySourceColumns + `
	FROM memories AS m
	WHERE m.id = ?
	  AND m.version = ?
	FOR UPDATE
`

const interventionPolicyColumns = `
	policy_id,
	policy_version,
	scope_commitment,
	descriptor_commitment,
	memory_id,
	memory_version,
	canonical_project,
	descriptor,
	descriptor_status,
	descriptor_origin,
	compiler_version,
	algorithm_version,
	normalization_version,
	parameter_version,
	created_from_event,
	created_at`

const interventionPolicyInsertSQL = `
	INSERT INTO intervention_evidence_policies (` + interventionPolicyColumns + `)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	ON CONFLICT DO NOTHING
	RETURNING ` + interventionPolicyColumns

const interventionPolicyWinnerSQL = `
	SELECT p.policy_id,
	       p.policy_version,
	       p.scope_commitment,
	       p.descriptor_commitment,
	       p.memory_id,
	       p.memory_version,
	       p.canonical_project,
	       p.descriptor,
	       p.descriptor_status,
	       p.descriptor_origin,
	       p.compiler_version,
	       p.algorithm_version,
	       p.normalization_version,
	       p.parameter_version,
	       p.created_from_event,
	       p.created_at
	FROM intervention_evidence_policies AS p
	JOIN memories AS m
	  ON m.id = p.memory_id
	 AND m.version = p.memory_version
	 AND p.canonical_project::text = lower(m.project)
	WHERE p.memory_id = ?
	  AND p.memory_version = ?
	  AND p.scope_commitment = ?
	  AND p.descriptor_commitment = ?
	  AND p.compiler_version = ?
	  AND p.algorithm_version = ?
	  AND p.normalization_version = ?
	  AND p.parameter_version = ?
`

const interventionPolicyCandidateReadSQL = `
	WITH requested AS (
		SELECT requested.memory_id, requested.memory_version, requested.ordinality
		FROM unnest(?::bigint[], ?::integer[]) WITH ORDINALITY
			AS requested(memory_id, memory_version, ordinality)
	)
	SELECT requested.ordinality,
	       m.id IS NOT NULL AS source_current,
	       p.descriptor_status,
	       p.policy_version,
	       p.scope_commitment,
	       p.descriptor_commitment,
	       m.project AS canonical_project,
	       m.privacy_scope,
	       m.source_workstation_id,
	       m.source_sessions,
	       m.owner_principal,
	       m.owner_principal_kind,
	       m.agent_visibility,
	       m.domain
	FROM requested
	LEFT JOIN memories AS m
	  ON m.id = requested.memory_id
	 AND m.version = requested.memory_version
	 AND m.superseded_by IS NULL
	 AND ` + taskMemoryCandidateAccessSQL + `
	LEFT JOIN intervention_evidence_policies AS p
	  ON p.memory_id = m.id
	 AND p.memory_version = m.version
	 AND p.canonical_project::text = lower(m.project)
	 AND p.compiler_version = ?
	 AND p.algorithm_version = ?
	 AND p.normalization_version = ?
	 AND p.parameter_version = ?
	ORDER BY requested.ordinality ASC
`

// InterventionPolicyStore persists and reads immutable policy definitions while
// enforcing the source row and task-memory authority boundaries at SQL time.
type InterventionPolicyStore struct {
	db *gormlib.DB
}

var (
	_ intervention.PolicyRepository = (*InterventionPolicyStore)(nil)
	_ intervention.PolicyReader     = (*InterventionPolicyStore)(nil)
)

// NewInterventionPolicyStore constructs the PostgreSQL policy repository and
// reader. Construction is side-effect free; calls fail when db is nil.
func NewInterventionPolicyStore(db *gormlib.DB) *InterventionPolicyStore {
	return &InterventionPolicyStore{db: db}
}

// ListCompileSources returns at most limit raw, current source versions that
// lack a policy for the exact active semantic-version tuple.
func (s *InterventionPolicyStore) ListCompileSources(
	ctx context.Context,
	versions intervention.PolicySemanticVersions,
	limit int,
) ([]intervention.SourceMemoryVersion, error) {
	if err := interventionPolicyContext(ctx); err != nil {
		return nil, err
	}
	if !versions.Valid() {
		return nil, fmt.Errorf("list intervention policy sources: invalid semantic versions")
	}
	if limit < 1 || limit > interventionPolicyMaxCompileSources {
		return nil, fmt.Errorf("list intervention policy sources: limit must be between 1 and %d", interventionPolicyMaxCompileSources)
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}

	var rows []interventionPolicySourceRow
	if err := db.WithContext(ctx).Raw(
		interventionPolicyCompileSourcesSQL,
		versions.Compiler,
		versions.Algorithm,
		versions.Normalization,
		versions.Parameter,
		limit,
	).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list intervention policy sources: %w", err)
	}

	sources := make([]intervention.SourceMemoryVersion, 0, len(rows))
	for _, row := range rows {
		source, err := row.source()
		if err != nil {
			return nil, fmt.Errorf("restore intervention policy source memory id=%d version=%d: %w", row.ID, row.Version, err)
		}
		sources = append(sources, source)
	}
	return sources, nil
}

// CommitPolicy locks the exact source version before checking its deterministic
// fingerprint and atomically inserts one immutable policy definition. A stale
// source returns the zero definition without inserting any policy row.
func (s *InterventionPolicyStore) CommitPolicy(
	ctx context.Context,
	definition intervention.PolicyDefinition,
) (intervention.PolicyDefinition, bool, error) {
	if err := interventionPolicyContext(ctx); err != nil {
		return intervention.PolicyDefinition{}, false, err
	}
	if !definition.CanCommit() {
		return intervention.PolicyDefinition{}, false, fmt.Errorf("commit intervention policy: definition is not trusted and authorable")
	}
	db, err := s.database()
	if err != nil {
		return intervention.PolicyDefinition{}, false, err
	}

	candidateSource := definition.Source()
	candidateRow := interventionPolicyRowFromRecord(definition.PersistenceRecord())
	var committed intervention.PolicyDefinition
	inserted := false

	err = db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		currentSource, found, err := readLockedInterventionPolicySource(ctx, tx, candidateRow.MemoryID, candidateRow.MemoryVersion)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		currentVersion, err := currentSource.source()
		if err != nil {
			return fmt.Errorf("restore locked intervention policy source: %w", err)
		}
		if currentVersion.SourceFingerprint() != candidateSource.SourceFingerprint() || !currentSource.SourceCurrent {
			return nil
		}

		insertedRow, didInsert, err := insertInterventionPolicyRow(ctx, tx, candidateRow)
		if err != nil {
			return err
		}
		if didInsert {
			committed, err = insertedRow.definition()
			if err != nil {
				return fmt.Errorf("restore inserted intervention policy: %w", err)
			}
			inserted = true
			return nil
		}

		winner, found, err := readInterventionPolicyWinner(ctx, tx, candidateRow)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("intervention policy conflict did not expose an immutable winner")
		}
		committed, err = winner.definition()
		if err != nil {
			return fmt.Errorf("restore committed intervention policy winner: %w", err)
		}
		return nil
	})
	if err != nil {
		return intervention.PolicyDefinition{}, false, fmt.Errorf("commit immutable intervention policy: %w", err)
	}
	return committed, inserted, nil
}

// ReadCandidatePolicies returns one content-free current policy state for each
// requested candidate reference in caller order. The access predicate is the
// same one that bounded candidate preparation already applies before ranking.
func (s *InterventionPolicyStore) ReadCandidatePolicies(
	ctx context.Context,
	authority taskmemory.AuthorizedTaskContext,
	refs []taskmemory.AuthorizedCandidateRef,
	versions intervention.PolicySemanticVersions,
) ([]intervention.CandidatePolicy, error) {
	if err := interventionPolicyContext(ctx); err != nil {
		return nil, err
	}
	if !versions.Valid() {
		return nil, fmt.Errorf("read intervention candidate policies: invalid semantic versions")
	}
	if len(refs) == 0 {
		return []intervention.CandidatePolicy{}, nil
	}
	for _, ref := range refs {
		if ref.ID() <= 0 || ref.Version() <= 0 {
			return nil, fmt.Errorf("read intervention candidate policies: invalid candidate reference")
		}
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(refs))
	memoryVersions := make([]int64, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ID())
		memoryVersions = append(memoryVersions, int64(ref.Version()))
	}
	access := taskMemoryCandidateAccessFromContext(authority)
	args := make([]any, 0, 2+len(access.args)+4)
	args = append(args, pq.Array(ids), pq.Array(memoryVersions))
	args = append(args, access.args...)
	args = append(args,
		versions.Compiler,
		versions.Algorithm,
		versions.Normalization,
		versions.Parameter,
	)

	var rows []interventionPolicyCandidateRow
	if err := db.WithContext(ctx).Raw(interventionPolicyCandidateReadSQL, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("read intervention candidate policies: %w", err)
	}
	if len(rows) != len(refs) {
		return nil, fmt.Errorf("read intervention candidate policies: returned %d rows for %d references", len(rows), len(refs))
	}

	policies := make([]intervention.CandidatePolicy, len(refs))
	seen := make([]bool, len(refs))
	for _, row := range rows {
		index := int(row.Ordinal) - 1
		if index < 0 || index >= len(refs) || seen[index] {
			return nil, fmt.Errorf("read intervention candidate policies: malformed requested order")
		}
		candidate, err := row.candidatePolicy(refs[index])
		if err != nil {
			return nil, fmt.Errorf("restore intervention candidate policy at index %d: %w", index, err)
		}
		policies[index] = candidate
		seen[index] = true
	}
	for index, returned := range seen {
		if !returned {
			return nil, fmt.Errorf("read intervention candidate policies: missing requested result at index %d", index)
		}
	}
	return policies, nil
}

func (s *InterventionPolicyStore) database() (*gormlib.DB, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("intervention policy store has nil database")
	}
	return s.db, nil
}

func interventionPolicyContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("intervention policy store received nil context")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("intervention policy store context: %w", err)
	}
	return nil
}

type interventionPolicySourceRow struct {
	ID                  int64                  `gorm:"column:id"`
	Version             int                    `gorm:"column:version"`
	CanonicalProject    string                 `gorm:"column:canonical_project"`
	Content             string                 `gorm:"column:content"`
	Tags                models.JSONStringArray `gorm:"column:tags"`
	Status              string                 `gorm:"column:status"`
	DeletedAt           *time.Time             `gorm:"column:deleted_at"`
	ValidFrom           *time.Time             `gorm:"column:valid_from"`
	ValidUntil          *time.Time             `gorm:"column:valid_until"`
	SupersededBy        *int64                 `gorm:"column:superseded_by"`
	PrivacyScope        string                 `gorm:"column:privacy_scope"`
	SourceWorkstationID string                 `gorm:"column:source_workstation_id"`
	SourceSessions      pq.StringArray         `gorm:"column:source_sessions;type:text[]"`
	OwnerPrincipal      string                 `gorm:"column:owner_principal"`
	SourceCurrent       bool                   `gorm:"column:source_current"`
	OwnerPrincipalKind  string                 `gorm:"column:owner_principal_kind"`
	AgentVisibility     string                 `gorm:"column:agent_visibility"`
	Domain              string                 `gorm:"column:domain"`
}

func (row interventionPolicySourceRow) source() (intervention.SourceMemoryVersion, error) {
	return intervention.NewSourceMemoryVersion(intervention.SourceMemoryInput{
		ID:                  row.ID,
		Version:             row.Version,
		CanonicalProject:    row.CanonicalProject,
		Content:             row.Content,
		Tags:                []string(row.Tags),
		Status:              row.Status,
		DeletedAt:           row.DeletedAt,
		ValidFrom:           row.ValidFrom,
		ValidUntil:          row.ValidUntil,
		SupersededBy:        row.SupersededBy,
		PrivacyScope:        row.PrivacyScope,
		SourceWorkstationID: row.SourceWorkstationID,
		SourceSessions:      []string(row.SourceSessions),
		OwnerPrincipal:      row.OwnerPrincipal,
		OwnerPrincipalKind:  row.OwnerPrincipalKind,
		AgentVisibility:     row.AgentVisibility,
		Domain:              row.Domain,
	})
}

func readLockedInterventionPolicySource(
	ctx context.Context,
	db *gormlib.DB,
	memoryID int64,
	memoryVersion int,
) (interventionPolicySourceRow, bool, error) {
	rows, err := db.WithContext(ctx).Raw(interventionPolicyLockedSourceSQL, memoryID, memoryVersion).Rows()
	if err != nil {
		return interventionPolicySourceRow{}, false, fmt.Errorf("lock intervention policy source memory id=%d version=%d: %w", memoryID, memoryVersion, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return interventionPolicySourceRow{}, false, fmt.Errorf("iterate locked intervention policy source: %w", err)
		}
		return interventionPolicySourceRow{}, false, nil
	}
	var source interventionPolicySourceRow
	if err := db.ScanRows(rows, &source); err != nil {
		return interventionPolicySourceRow{}, false, fmt.Errorf("scan locked intervention policy source: %w", err)
	}
	if rows.Next() {
		return interventionPolicySourceRow{}, false, fmt.Errorf("lock intervention policy source returned multiple exact rows")
	}
	if err := rows.Err(); err != nil {
		return interventionPolicySourceRow{}, false, fmt.Errorf("iterate locked intervention policy source: %w", err)
	}
	return source, true, nil
}

type interventionPolicyRow struct {
	PolicyID             []byte    `gorm:"column:policy_id"`
	PolicyVersion        []byte    `gorm:"column:policy_version"`
	ScopeCommitment      []byte    `gorm:"column:scope_commitment"`
	DescriptorCommitment []byte    `gorm:"column:descriptor_commitment"`
	MemoryID             int64     `gorm:"column:memory_id"`
	MemoryVersion        int       `gorm:"column:memory_version"`
	CanonicalProject     string    `gorm:"column:canonical_project"`
	DescriptorJSON       []byte    `gorm:"column:descriptor"`
	DescriptorStatus     string    `gorm:"column:descriptor_status"`
	DescriptorOrigin     string    `gorm:"column:descriptor_origin"`
	CompilerVersion      string    `gorm:"column:compiler_version"`
	AlgorithmVersion     string    `gorm:"column:algorithm_version"`
	NormalizationVersion string    `gorm:"column:normalization_version"`
	ParameterVersion     string    `gorm:"column:parameter_version"`
	CreatedFromEvent     []byte    `gorm:"column:created_from_event"`
	CreatedAt            time.Time `gorm:"column:created_at"`
}

func interventionPolicyRowFromRecord(record intervention.PolicyPersistenceRecord) interventionPolicyRow {
	return interventionPolicyRow{
		PolicyID:             interventionPolicyDigestBytes(record.PolicyID),
		PolicyVersion:        interventionPolicyDigestBytes(record.PolicyVersion),
		ScopeCommitment:      interventionPolicyDigestBytes(record.ScopeCommitment),
		DescriptorCommitment: interventionPolicyDigestBytes(record.DescriptorCommitment),
		MemoryID:             record.MemoryID,
		MemoryVersion:        record.MemoryVersion,
		CanonicalProject:     record.CanonicalProject,
		DescriptorJSON:       append([]byte(nil), record.DescriptorJSON...),
		DescriptorStatus:     string(record.DescriptorStatus),
		DescriptorOrigin:     string(record.DescriptorOrigin),
		CompilerVersion:      record.CompilerVersion,
		AlgorithmVersion:     record.AlgorithmVersion,
		NormalizationVersion: record.NormalizationVersion,
		ParameterVersion:     record.ParameterVersion,
		CreatedFromEvent:     interventionPolicyDigestBytes(record.CreatedFromEvent),
		CreatedAt:            record.CreatedAt,
	}
}

func (row interventionPolicyRow) definition() (intervention.PolicyDefinition, error) {
	policyID, err := interventionPolicyDigestFromBytes("policy_id", row.PolicyID)
	if err != nil {
		return intervention.PolicyDefinition{}, err
	}
	policyVersion, err := interventionPolicyDigestFromBytes("policy_version", row.PolicyVersion)
	if err != nil {
		return intervention.PolicyDefinition{}, err
	}
	scopeCommitment, err := interventionPolicyDigestFromBytes("scope_commitment", row.ScopeCommitment)
	if err != nil {
		return intervention.PolicyDefinition{}, err
	}
	descriptorCommitment, err := interventionPolicyDigestFromBytes("descriptor_commitment", row.DescriptorCommitment)
	if err != nil {
		return intervention.PolicyDefinition{}, err
	}
	createdFromEvent, err := interventionPolicyDigestFromBytes("created_from_event", row.CreatedFromEvent)
	if err != nil {
		return intervention.PolicyDefinition{}, err
	}
	return intervention.RestoreUnverifiedPolicy(intervention.PolicyPersistenceRecord{
		PolicyID:             [32]byte(policyID),
		PolicyVersion:        [32]byte(policyVersion),
		ScopeCommitment:      [32]byte(scopeCommitment),
		DescriptorCommitment: [32]byte(descriptorCommitment),
		MemoryID:             row.MemoryID,
		MemoryVersion:        row.MemoryVersion,
		CanonicalProject:     row.CanonicalProject,
		DescriptorJSON:       append([]byte(nil), row.DescriptorJSON...),
		DescriptorStatus:     intervention.PolicyDescriptorStatus(row.DescriptorStatus),
		DescriptorOrigin:     intervention.PolicyDescriptorOrigin(row.DescriptorOrigin),
		CompilerVersion:      row.CompilerVersion,
		AlgorithmVersion:     row.AlgorithmVersion,
		NormalizationVersion: row.NormalizationVersion,
		ParameterVersion:     row.ParameterVersion,
		CreatedFromEvent:     [32]byte(createdFromEvent),
		CreatedAt:            row.CreatedAt,
	})
}

func (row interventionPolicyRow) insertArguments() []any {
	return []any{
		row.PolicyID,
		row.PolicyVersion,
		row.ScopeCommitment,
		row.DescriptorCommitment,
		row.MemoryID,
		row.MemoryVersion,
		row.CanonicalProject,
		string(row.DescriptorJSON),
		row.DescriptorStatus,
		row.DescriptorOrigin,
		row.CompilerVersion,
		row.AlgorithmVersion,
		row.NormalizationVersion,
		row.ParameterVersion,
		row.CreatedFromEvent,
		row.CreatedAt,
	}
}

func insertInterventionPolicyRow(ctx context.Context, db *gormlib.DB, row interventionPolicyRow) (interventionPolicyRow, bool, error) {
	rows, err := db.WithContext(ctx).Raw(interventionPolicyInsertSQL, row.insertArguments()...).Rows()
	if err != nil {
		return interventionPolicyRow{}, false, fmt.Errorf("insert immutable intervention policy: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return interventionPolicyRow{}, false, fmt.Errorf("iterate intervention policy insert: %w", err)
		}
		return interventionPolicyRow{}, false, nil
	}
	var inserted interventionPolicyRow
	if err := db.ScanRows(rows, &inserted); err != nil {
		return interventionPolicyRow{}, false, fmt.Errorf("scan inserted intervention policy: %w", err)
	}
	if rows.Next() {
		return interventionPolicyRow{}, false, fmt.Errorf("intervention policy insert returned multiple rows")
	}
	if err := rows.Err(); err != nil {
		return interventionPolicyRow{}, false, fmt.Errorf("iterate intervention policy insert: %w", err)
	}
	return inserted, true, nil
}

func readInterventionPolicyWinner(
	ctx context.Context,
	db *gormlib.DB,
	candidate interventionPolicyRow,
) (interventionPolicyRow, bool, error) {
	rows, err := db.WithContext(ctx).Raw(
		interventionPolicyWinnerSQL,
		candidate.MemoryID,
		candidate.MemoryVersion,
		candidate.ScopeCommitment,
		candidate.DescriptorCommitment,
		candidate.CompilerVersion,
		candidate.AlgorithmVersion,
		candidate.NormalizationVersion,
		candidate.ParameterVersion,
	).Rows()
	if err != nil {
		return interventionPolicyRow{}, false, fmt.Errorf("read immutable intervention policy winner: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return interventionPolicyRow{}, false, fmt.Errorf("iterate immutable intervention policy winner: %w", err)
		}
		return interventionPolicyRow{}, false, nil
	}
	var winner interventionPolicyRow
	if err := db.ScanRows(rows, &winner); err != nil {
		return interventionPolicyRow{}, false, fmt.Errorf("scan immutable intervention policy winner: %w", err)
	}
	if rows.Next() {
		return interventionPolicyRow{}, false, fmt.Errorf("immutable intervention policy winner lookup returned multiple rows")
	}
	if err := rows.Err(); err != nil {
		return interventionPolicyRow{}, false, fmt.Errorf("iterate immutable intervention policy winner: %w", err)
	}
	return winner, true, nil
}

func interventionPolicyDigestBytes(value [32]byte) []byte {
	return append([]byte(nil), value[:]...)
}

func interventionPolicyDigestFromBytes(field string, value []byte) (intervention.Digest, error) {
	if len(value) != len(intervention.Digest{}) {
		return intervention.Digest{}, fmt.Errorf("persisted intervention policy %s must be exactly 32 bytes", field)
	}
	var digest intervention.Digest
	copy(digest[:], value)
	return digest, nil
}

type interventionPolicyCandidateRow struct {
	Ordinal              int64          `gorm:"column:ordinality"`
	SourceCurrent        bool           `gorm:"column:source_current"`
	DescriptorStatus     sql.NullString `gorm:"column:descriptor_status"`
	PolicyVersion        []byte         `gorm:"column:policy_version"`
	ScopeCommitment      []byte         `gorm:"column:scope_commitment"`
	DescriptorCommitment []byte         `gorm:"column:descriptor_commitment"`
	CanonicalProject     sql.NullString `gorm:"column:canonical_project"`
	PrivacyScope         sql.NullString `gorm:"column:privacy_scope"`
	SourceWorkstationID  sql.NullString `gorm:"column:source_workstation_id"`
	SourceSessions       pq.StringArray `gorm:"column:source_sessions;type:text[]"`
	OwnerPrincipal       sql.NullString `gorm:"column:owner_principal"`
	OwnerPrincipalKind   sql.NullString `gorm:"column:owner_principal_kind"`
	AgentVisibility      sql.NullString `gorm:"column:agent_visibility"`
	Domain               sql.NullString `gorm:"column:domain"`
}

func (row interventionPolicyCandidateRow) candidatePolicy(ref taskmemory.AuthorizedCandidateRef) (intervention.CandidatePolicy, error) {
	if !row.SourceCurrent {
		return intervention.NewCandidatePolicy(ref, intervention.CandidatePolicySourceStale, intervention.Digest{}, intervention.Digest{}, intervention.Digest{}, intervention.SourceMemoryScope{})
	}
	if !row.DescriptorStatus.Valid {
		return intervention.NewCandidatePolicy(ref, intervention.CandidatePolicyMissing, intervention.Digest{}, intervention.Digest{}, intervention.Digest{}, intervention.SourceMemoryScope{})
	}
	var state intervention.CandidatePolicyState
	switch row.DescriptorStatus.String {
	case string(intervention.PolicyDescriptorValid):
		state = intervention.CandidatePolicyValid
	case string(intervention.PolicyDescriptorInsufficient):
		state = intervention.CandidatePolicyInsufficient
	default:
		return intervention.CandidatePolicy{}, fmt.Errorf("invalid persisted descriptor status %q", row.DescriptorStatus.String)
	}
	policyVersion, err := interventionPolicyDigestFromBytes("policy_version", row.PolicyVersion)
	if err != nil {
		return intervention.CandidatePolicy{}, err
	}
	scopeCommitment, err := interventionPolicyDigestFromBytes("scope_commitment", row.ScopeCommitment)
	if err != nil {
		return intervention.CandidatePolicy{}, err
	}
	descriptorCommitment, err := interventionPolicyDigestFromBytes("descriptor_commitment", row.DescriptorCommitment)
	if err != nil {
		return intervention.CandidatePolicy{}, err
	}
	currentScope, err := intervention.NewSourceMemoryScope(intervention.SourceMemoryScopeInput{
		CanonicalProject:    row.CanonicalProject.String,
		PrivacyScope:        row.PrivacyScope.String,
		SourceWorkstationID: row.SourceWorkstationID.String,
		SourceSessions:      []string(row.SourceSessions),
		OwnerPrincipal:      row.OwnerPrincipal.String,
		OwnerPrincipalKind:  row.OwnerPrincipalKind.String,
		AgentVisibility:     row.AgentVisibility.String,
		Domain:              row.Domain.String,
	})
	if err != nil {
		return intervention.CandidatePolicy{}, err
	}
	return intervention.NewCandidatePolicy(ref, state, policyVersion, scopeCommitment, descriptorCommitment, currentScope)
}
