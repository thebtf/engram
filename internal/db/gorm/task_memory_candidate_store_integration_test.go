package gorm

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"

	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/taskmemory"
)

const (
	taskMemoryTestProject     = "11111111-1111-4111-8111-111111111111"
	taskMemoryOtherProject    = "22222222-2222-4222-8222-222222222222"
	taskMemoryTestWorkstation = "task-memory-workstation"
	taskMemoryTestSession     = "task-memory-session"
	taskMemoryTestPrincipal   = "agent/alice"
)

type taskMemoryFixture struct {
	ID                  int64
	Name                string
	Project             string
	Content             string
	PrivacyScope        string
	SourceWorkstationID string
	SourceSessions      []string
	OwnerPrincipal      string
	OwnerPrincipalKind  string
	AgentVisibility     string
	Domain              string
	Version             int
	CreatedAt           time.Time
}

type taskMemoryTestAuthority struct {
	authority taskmemory.AuthorizedTaskContext
}

func (a taskMemoryTestAuthority) ResolveTaskAuthority(_ context.Context, _ taskmemory.ProjectEvidenceV3) (taskmemory.AuthorizedTaskContext, error) {
	return a.authority, nil
}

type taskMemoryCapturingProvider struct {
	store *TaskMemoryCandidateStore
	query taskmemory.AuthorizedCandidateQuery
	calls int
}

func (p *taskMemoryCapturingProvider) Snapshot(ctx context.Context, query taskmemory.AuthorizedCandidateQuery) (taskmemory.CandidateSnapshot, error) {
	p.query = query
	p.calls++
	return p.store.Snapshot(ctx, query)
}

func taskMemoryFixtureDB(t *testing.T) (*gormlib.DB, func()) {
	t.Helper()
	db, closeDB := openTestDB(t)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, tx.Exec(`CREATE TEMPORARY TABLE memories (LIKE public.memories INCLUDING DEFAULTS INCLUDING GENERATED) ON COMMIT DROP`).Error)
	return tx, func() {
		_ = tx.Rollback().Error
		closeDB()
	}
}

func taskMemoryTestContext(t *testing.T) taskmemory.AuthorizedTaskContext {
	t.Helper()
	caller, err := taskmemory.NewAuthenticatedCaller(
		"client",
		"read-write",
		taskMemoryTestWorkstation,
		taskMemoryTestPrincipal,
		"agent",
		nil,
	)
	require.NoError(t, err)

	project, err := projectidentity.NewProjectKeyV3(taskMemoryTestProject)
	require.NoError(t, err)
	correlation, err := projectidentity.NewCorrelationV3("task-memory-test-correlation")
	require.NoError(t, err)
	resolution, err := projectidentity.NewSuccessResultV3(
		projectidentity.ReadFilterIntentV3,
		projectidentity.ProjectResolvedOutcomeV3,
		project,
		projectidentity.RepositoryResolvedScopeV3,
		"",
		correlation,
	)
	require.NoError(t, err)
	authority, err := taskmemory.NewAuthorizedTaskContext(caller, resolution, taskMemoryTestSession)
	require.NoError(t, err)
	return authority
}

func taskMemoryCapturedQuery(t *testing.T, db *gormlib.DB, queryText string) taskmemory.AuthorizedCandidateQuery {
	t.Helper()
	store := NewTaskMemoryCandidateStore(&Store{DB: db})
	provider := &taskMemoryCapturingProvider{store: store}
	preparer, err := taskmemory.NewPreparer(taskmemory.PreparerConfig{
		Authority:  taskMemoryTestAuthority{authority: taskMemoryTestContext(t)},
		Candidates: provider,
	})
	require.NoError(t, err)

	facts, err := taskmemory.NewTaskFacts(queryText)
	require.NoError(t, err)
	anchor := projectidentity.AnchorV3{
		Version:   3,
		ProjectID: taskMemoryTestProject,
		Name:      "task-memory-fixture",
		Scope:     "repository",
	}
	descriptor, err := projectidentity.BuildDescriptorV3(anchor, nil, nil, "task-memory-fixture-client")
	require.NoError(t, err)
	_, err = preparer.Prepare(context.Background(), taskmemory.PrepareRequest{
		Project: taskmemory.ProjectEvidenceV3{Anchor: anchor, Descriptor: descriptor},
		Task:    facts,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, provider.calls, 2)
	return provider.query
}

func taskMemoryAllowedFixtures(prefix, content string, startID int64, count int, base time.Time) []taskMemoryFixture {
	fixtures := make([]taskMemoryFixture, count)
	for index := range fixtures {
		fixtures[index] = taskMemoryFixture{
			ID:             startID + int64(index),
			Name:           fmt.Sprintf("%s-allowed-%d", prefix, index+1),
			Project:        taskMemoryTestProject,
			Content:        content,
			PrivacyScope:   "project",
			SourceSessions: []string{},
			Version:        index + 1,
			CreatedAt:      base.Add(time.Duration(index) * time.Minute),
		}
	}
	return fixtures
}

func taskMemoryDeniedFixtures(prefix, content string, startID int64, base time.Time) []taskMemoryFixture {
	fixture := func(index int, name string) taskMemoryFixture {
		return taskMemoryFixture{
			ID:             startID + int64(index),
			Name:           prefix + "-" + name,
			Project:        taskMemoryTestProject,
			Content:        content,
			PrivacyScope:   "project",
			SourceSessions: []string{},
			Version:        100 + index,
			CreatedAt:      base.Add(time.Duration(index) * time.Minute),
		}
	}

	crossProject := fixture(0, "denied-cross-project")
	crossProject.Project = taskMemoryOtherProject
	wrongPrincipal := fixture(1, "denied-private-principal")
	wrongPrincipal.AgentVisibility = "private"
	wrongPrincipal.OwnerPrincipal = "agent/bob"
	wrongPrincipal.OwnerPrincipalKind = "agent"
	wrongDomain := fixture(2, "denied-domain-owner")
	wrongDomain.AgentVisibility = "shared"
	wrongDomain.Domain = "task-memory-domain"
	wrongDomain.OwnerPrincipal = "agent/bob"
	wrongDomain.OwnerPrincipalKind = "agent"
	wrongWorkstation := fixture(3, "denied-private-workstation")
	wrongWorkstation.PrivacyScope = "private"
	wrongWorkstation.SourceWorkstationID = "another-workstation"
	wrongWorkstation.SourceSessions = []string{taskMemoryTestSession}
	wrongSession := fixture(4, "denied-private-session")
	wrongSession.PrivacyScope = "private"
	wrongSession.SourceWorkstationID = taskMemoryTestWorkstation
	wrongSession.SourceSessions = []string{"another-session"}
	invalidScope := fixture(5, "denied-invalid-scope")
	invalidScope.PrivacyScope = "unknown-scope"
	invalidVisibility := fixture(6, "denied-invalid-visibility")
	invalidVisibility.AgentVisibility = "unknown-visibility"
	secondPrincipal := fixture(7, "denied-private-principal-second")
	secondPrincipal.AgentVisibility = "private"
	secondPrincipal.OwnerPrincipal = "agent/eve"
	secondPrincipal.OwnerPrincipalKind = "agent"
	secondDomain := fixture(8, "denied-domain-owner-second")
	secondDomain.AgentVisibility = "shared"
	secondDomain.Domain = "task-memory-domain"
	secondDomain.OwnerPrincipal = "agent/eve"
	secondDomain.OwnerPrincipalKind = "agent"

	return []taskMemoryFixture{
		crossProject,
		wrongPrincipal,
		wrongDomain,
		wrongWorkstation,
		wrongSession,
		invalidScope,
		invalidVisibility,
		secondPrincipal,
		secondDomain,
	}
}

func taskMemorySeedFixtures(t *testing.T, db *gormlib.DB, fixtures []taskMemoryFixture) {
	t.Helper()
	validFrom := time.Now().UTC().Add(-time.Hour)
	validUntil := time.Now().UTC().Add(time.Hour)
	for _, fixture := range fixtures {
		sessions := fixture.SourceSessions
		if sessions == nil {
			sessions = []string{}
		}
		require.NoError(t, db.Exec(`
			INSERT INTO memories (
				id, project, content, status, privacy_scope, source_workstation_id,
				source_sessions, owner_principal, owner_principal_kind, agent_visibility,
				domain, valid_from, valid_until, version, created_at, updated_at
			) VALUES (?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			fixture.ID,
			fixture.Project,
			fixture.Content,
			fixture.PrivacyScope,
			fixture.SourceWorkstationID,
			pq.Array(sessions),
			fixture.OwnerPrincipal,
			fixture.OwnerPrincipalKind,
			fixture.AgentVisibility,
			fixture.Domain,
			validFrom,
			validUntil,
			fixture.Version,
			fixture.CreatedAt,
			fixture.CreatedAt,
		).Error, "insert fixture %s", fixture.Name)
	}
}

func taskMemoryOracleAllows(query taskmemory.AuthorizedCandidateQuery, row *Memory) bool {
	if row.Project != query.CanonicalProject() || row.Status != "active" || row.DeletedAt != nil {
		return false
	}
	now := time.Now().UTC()
	if row.ValidFrom != nil && row.ValidFrom.After(now) {
		return false
	}
	if row.ValidUntil != nil && row.ValidUntil.Before(now) {
		return false
	}
	return query.AccessPolicy().Allows(memoryRowToSnapshotModel(row))
}

func taskMemoryOracleIDSet(query taskmemory.AuthorizedCandidateQuery, rows []Memory) map[int64]struct{} {
	ids := make(map[int64]struct{}, len(rows))
	for index := range rows {
		if taskMemoryOracleAllows(query, &rows[index]) {
			ids[rows[index].ID] = struct{}{}
		}
	}
	return ids
}

func taskMemoryIDSet(rows []Memory) map[int64]struct{} {
	ids := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		ids[row.ID] = struct{}{}
	}
	return ids
}

func taskMemoryAssertOracleEquivalent(t *testing.T, query taskmemory.AuthorizedCandidateQuery, all, sqlVisible []Memory) {
	t.Helper()
	want := taskMemoryOracleIDSet(query, all)
	require.NotEmpty(t, want, "the fixture must retain a nonzero oracle-allowed denominator")
	require.Equal(t, want, taskMemoryIDSet(sqlVisible), "SQL access projection must match the existing scope/domain oracle")
}

func taskMemoryAssertExactOracleEquivalent(t *testing.T, db *gormlib.DB, query taskmemory.AuthorizedCandidateQuery, content string) {
	t.Helper()
	access := taskMemoryCandidateAccess(query)
	visibleArgs := append([]any{}, access.args...)
	visibleArgs = append(visibleArgs, content)
	var sqlVisible []Memory
	require.NoError(t, db.Raw(`SELECT m.* FROM memories m WHERE `+access.sql+` AND m.content = ?`, visibleArgs...).Scan(&sqlVisible).Error)
	var all []Memory
	require.NoError(t, db.Raw(`SELECT m.* FROM memories m WHERE m.content = ?`, content).Scan(&all).Error)
	taskMemoryAssertOracleEquivalent(t, query, all, sqlVisible)
}

func taskMemoryAssertFTSOracleEquivalent(t *testing.T, db *gormlib.DB, query taskmemory.AuthorizedCandidateQuery) {
	t.Helper()
	access := taskMemoryCandidateAccess(query)
	visibleArgs := []any{query.Query(), query.Query()}
	visibleArgs = append(visibleArgs, access.args...)
	var sqlVisible []Memory
	require.NoError(t, db.Raw(`
		WITH parsed AS (
			SELECT websearch_to_tsquery('english', ?) AS wsq,
			       plainto_tsquery('english', ?) AS ptq
		)
		SELECT m.*
		FROM memories m, parsed
		WHERE `+access.sql+`
		  AND m.search_vector @@ COALESCE(NULLIF(parsed.wsq, ''::tsquery), parsed.ptq)
	`, visibleArgs...).Scan(&sqlVisible).Error)
	var all []Memory
	require.NoError(t, db.Raw(`
		WITH parsed AS (
			SELECT websearch_to_tsquery('english', ?) AS wsq,
			       plainto_tsquery('english', ?) AS ptq
		)
		SELECT m.*
		FROM memories m, parsed
		WHERE m.search_vector @@ COALESCE(NULLIF(parsed.wsq, ''::tsquery), parsed.ptq)
	`, query.Query(), query.Query()).Scan(&all).Error)
	taskMemoryAssertOracleEquivalent(t, query, all, sqlVisible)
}

func taskMemoryAssertByRefOracleEquivalent(t *testing.T, db *gormlib.DB, query taskmemory.AuthorizedCandidateQuery, lookups []taskmemory.CandidateLookup) {
	t.Helper()
	ids := make([]int64, len(lookups))
	tiers := make([]int64, len(lookups))
	for index, lookup := range lookups {
		ids[index] = lookup.ID()
		tiers[index] = int64(lookup.SourceTier())
	}
	access := taskMemoryCandidateAccess(query)
	visibleArgs := []any{pq.Array(ids), pq.Array(tiers)}
	visibleArgs = append(visibleArgs, access.args...)
	var sqlVisible []Memory
	require.NoError(t, db.Raw(`
		WITH requested AS (
			SELECT requested_refs.id, requested_refs.tier, requested_refs.ordinality
			FROM unnest(?::bigint[], ?::smallint[]) WITH ORDINALITY AS requested_refs(id, tier, ordinality)
		)
		SELECT m.*
		FROM requested
		JOIN memories m ON m.id = requested.id
		WHERE `+access.sql+`
	`, visibleArgs...).Scan(&sqlVisible).Error)
	var all []Memory
	require.NoError(t, db.Raw(`SELECT m.* FROM memories m WHERE m.id = ANY(?)`, pq.Array(ids)).Scan(&all).Error)
	taskMemoryAssertOracleEquivalent(t, query, all, sqlVisible)
}

func taskMemoryNewest(fixtures []taskMemoryFixture, limit int) []taskMemoryFixture {
	ordered := append([]taskMemoryFixture(nil), fixtures...)
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].CreatedAt.Equal(ordered[right].CreatedAt) {
			return ordered[left].ID > ordered[right].ID
		}
		return ordered[left].CreatedAt.After(ordered[right].CreatedAt)
	})
	return append([]taskMemoryFixture(nil), ordered[:limit]...)
}

func taskMemoryAssertFixtureRefs(t *testing.T, refs []taskmemory.AuthorizedCandidateRef, expected []taskMemoryFixture, tier taskmemory.CandidateSourceTier) {
	t.Helper()
	require.Len(t, refs, len(expected))
	for index, fixture := range expected {
		require.Equal(t, fixture.ID, refs[index].ID(), "candidate %d", index)
		require.Equal(t, fixture.Version, refs[index].Version(), "candidate %d", index)
		require.Equal(t, tier, refs[index].SourceTier(), "candidate %d", index)
	}
}

func taskMemoryLookup(t *testing.T, id int64, tier taskmemory.CandidateSourceTier) taskmemory.CandidateLookup {
	t.Helper()
	lookup, err := taskmemory.NewCandidateLookup(id, tier)
	require.NoError(t, err)
	return lookup
}

func TestTaskMemoryCandidateStore_ExactScopeBeforeRankingLimitAndOrder(t *testing.T) {
	db, cleanup := taskMemoryFixtureDB(t)
	defer cleanup()

	const queryText = "exact task memory needle"
	base := time.Now().UTC().Add(-30 * time.Minute)
	expectedAllowed := taskMemoryAllowedFixtures("exact", queryText, 1001, taskmemory.MaxPreparedCandidates, base)
	expectedAllowed[5].AgentVisibility = "private"
	expectedAllowed[5].OwnerPrincipal = taskMemoryTestPrincipal
	expectedAllowed[5].OwnerPrincipalKind = "agent"
	expectedAllowed[6].PrivacyScope = "private"
	expectedAllowed[6].SourceWorkstationID = taskMemoryTestWorkstation
	expectedAllowed[6].SourceSessions = []string{taskMemoryTestSession}
	expectedAllowed[7].AgentVisibility = "shared"
	expectedAllowed[7].Domain = "task-memory-domain"
	expectedAllowed[7].OwnerPrincipal = taskMemoryTestPrincipal
	expectedAllowed[7].OwnerPrincipalKind = "agent"
	overflowAllowed := taskMemoryAllowedFixtures("exact-overflow", queryText, 1009, 1, base.Add(-time.Minute))
	allowed := append(append([]taskMemoryFixture{}, expectedAllowed...), overflowAllowed...)
	denied := taskMemoryDeniedFixtures("exact", queryText, 1101, base.Add(20*time.Minute))
	taskMemorySeedFixtures(t, db, append(append([]taskMemoryFixture{}, allowed...), denied...))

	query := taskMemoryCapturedQuery(t, db, queryText)
	taskMemoryAssertExactOracleEquivalent(t, db, query, queryText)

	store := NewTaskMemoryCandidateStore(&Store{DB: db})
	first, err := store.Snapshot(context.Background(), query)
	require.NoError(t, err)
	second, err := store.Snapshot(context.Background(), query)
	require.NoError(t, err)
	require.Equal(t, first.Candidates(), second.Candidates(), "exact ordering must be deterministic")
	require.Equal(t, taskmemory.RetrievalExact, first.Mode())
	taskMemoryAssertFixtureRefs(t, first.Candidates(), taskMemoryNewest(expectedAllowed, taskmemory.MaxPreparedCandidates), taskmemory.CandidateExact)
}

func TestTaskMemoryCandidateStore_FTSScopeBeforeRankingAndOrder(t *testing.T) {
	db, cleanup := taskMemoryFixtureDB(t)
	defer cleanup()

	const queryText = "lexical needle"
	base := time.Now().UTC().Add(-30 * time.Minute)
	allowed := taskMemoryAllowedFixtures("fts", "lexical needle allowed", 2001, 2, base)
	allowed[0].PrivacyScope = "private"
	allowed[0].SourceWorkstationID = taskMemoryTestWorkstation
	allowed[0].SourceSessions = []string{taskMemoryTestSession}
	allowed[1].AgentVisibility = "shared"
	allowed[1].Domain = "task-memory-domain"
	allowed[1].OwnerPrincipal = taskMemoryTestPrincipal
	allowed[1].OwnerPrincipalKind = "agent"
	denied := taskMemoryDeniedFixtures("fts", "lexical needle lexical needle lexical needle lexical needle lexical needle hidden", 2101, base.Add(20*time.Minute))
	taskMemorySeedFixtures(t, db, append(append([]taskMemoryFixture{}, allowed...), denied...))

	query := taskMemoryCapturedQuery(t, db, queryText)
	taskMemoryAssertFTSOracleEquivalent(t, db, query)

	store := NewTaskMemoryCandidateStore(&Store{DB: db})
	first, err := store.Snapshot(context.Background(), query)
	require.NoError(t, err)
	second, err := store.Snapshot(context.Background(), query)
	require.NoError(t, err)
	require.Equal(t, first.Candidates(), second.Candidates(), "FTS ordering must be deterministic")
	require.Equal(t, taskmemory.RetrievalLexicalDegraded, first.Mode())
	taskMemoryAssertFixtureRefs(t, first.Candidates(), taskMemoryNewest(allowed, len(allowed)), taskmemory.CandidateFTS)
}

func TestTaskMemoryCandidateStore_ByRefScopeBeforeOrderLimitAndIndistinguishability(t *testing.T) {
	db, cleanup := taskMemoryFixtureDB(t)
	defer cleanup()

	base := time.Now().UTC().Add(-30 * time.Minute)
	allowed := taskMemoryAllowedFixtures("by-ref", "by reference materialization", 3001, 2, base)
	allowed[0].PrivacyScope = "private"
	allowed[0].SourceWorkstationID = taskMemoryTestWorkstation
	allowed[0].SourceSessions = []string{taskMemoryTestSession}
	allowed[1].AgentVisibility = "shared"
	allowed[1].Domain = "task-memory-domain"
	allowed[1].OwnerPrincipal = taskMemoryTestPrincipal
	allowed[1].OwnerPrincipalKind = "agent"
	denied := taskMemoryDeniedFixtures("by-ref", "by reference materialization", 3101, base.Add(20*time.Minute))
	taskMemorySeedFixtures(t, db, append(append([]taskMemoryFixture{}, allowed...), denied...))

	query := taskMemoryCapturedQuery(t, db, "no matching preparation query")
	lookups := make([]taskmemory.CandidateLookup, 0, len(denied)+3)
	for _, fixture := range denied {
		lookups = append(lookups, taskMemoryLookup(t, fixture.ID, taskmemory.CandidateFTS))
	}
	lookups = append(lookups,
		taskMemoryLookup(t, allowed[1].ID, taskmemory.CandidateExact),
		taskMemoryLookup(t, 999999, taskmemory.CandidateVector),
		taskMemoryLookup(t, allowed[0].ID, taskmemory.CandidateVector),
	)
	taskMemoryAssertByRefOracleEquivalent(t, db, query, lookups)

	store := NewTaskMemoryCandidateStore(&Store{DB: db})
	refs, err := store.GetAuthorizedByRefs(context.Background(), query, lookups)
	require.NoError(t, err)
	require.Len(t, refs, 2, "denied leading references must not consume the preparation bound")
	require.Equal(t, allowed[1].ID, refs[0].ID())
	require.Equal(t, allowed[1].Version, refs[0].Version())
	require.Equal(t, taskmemory.CandidateExact, refs[0].SourceTier())
	require.Equal(t, allowed[0].ID, refs[1].ID())
	require.Equal(t, allowed[0].Version, refs[1].Version())
	require.Equal(t, taskmemory.CandidateVector, refs[1].SourceTier())

	deniedOnly, err := store.GetAuthorizedByRefs(context.Background(), query, []taskmemory.CandidateLookup{taskMemoryLookup(t, denied[0].ID, taskmemory.CandidateVector)})
	require.NoError(t, err)
	absentOnly, err := store.GetAuthorizedByRefs(context.Background(), query, []taskmemory.CandidateLookup{taskMemoryLookup(t, 999998, taskmemory.CandidateVector)})
	require.NoError(t, err)
	require.Equal(t, absentOnly, deniedOnly, "absent and denied IDs must have the same observable result")
	require.Empty(t, deniedOnly)

	_, err = store.GetAuthorizedByRefs(context.Background(), query, []taskmemory.CandidateLookup{
		taskMemoryLookup(t, allowed[0].ID, taskmemory.CandidateFTS),
		taskMemoryLookup(t, allowed[0].ID, taskmemory.CandidateVector),
	})
	require.ErrorIs(t, err, taskmemory.ErrInvalidRequest, "duplicate IDs must fail before SQL")

	overLimit := make([]taskmemory.CandidateLookup, taskmemory.MaxCandidateLookups+1)
	for index := range overLimit {
		overLimit[index] = taskMemoryLookup(t, 5000+int64(index), taskmemory.CandidateFTS)
	}
	_, err = store.GetAuthorizedByRefs(context.Background(), query, overLimit)
	require.ErrorIs(t, err, taskmemory.ErrInvalidRequest, "by-reference inputs must be bounded")

	_, err = store.GetAuthorizedByRefs(context.Background(), query, []taskmemory.CandidateLookup{{}})
	require.ErrorIs(t, err, taskmemory.ErrInvalidRequest, "zero-value lookups must fail closed")
}

type taskMemoryReadState struct {
	ID        int64
	Version   int
	UpdatedAt time.Time
}

func taskMemoryReadStateOf(t *testing.T, db *gormlib.DB) []taskMemoryReadState {
	t.Helper()
	var state []taskMemoryReadState
	require.NoError(t, db.Raw(`SELECT id, version, updated_at FROM memories ORDER BY id`).Scan(&state).Error)
	return state
}

func taskMemoryPublicSchemaCount(t *testing.T, db *gormlib.DB) int {
	t.Helper()
	var count int
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND c.relname LIKE 'task_memory%'
	`).Scan(&count).Error)
	return count
}

func TestTaskMemoryCandidateStore_ReadsCreateNoTaskMemorySchemaOrWrites(t *testing.T) {
	db, cleanup := taskMemoryFixtureDB(t)
	defer cleanup()

	fixture := taskMemoryAllowedFixtures("read-only", "read only task memory", 4001, 1, time.Now().UTC().Add(-time.Minute))
	taskMemorySeedFixtures(t, db, fixture)
	beforeRows := taskMemoryReadStateOf(t, db)
	beforeSchema := taskMemoryPublicSchemaCount(t, db)

	_ = taskMemoryCapturedQuery(t, db, fixture[0].Content)

	require.Equal(t, beforeRows, taskMemoryReadStateOf(t, db), "candidate reads must not mutate memory rows")
	require.Equal(t, beforeSchema, taskMemoryPublicSchemaCount(t, db), "candidate reads must not create TaskMemory schema")
}

func TestTaskMemoryCandidateStoreRejectsInvalidConstructionAndQuery(t *testing.T) {
	store := NewTaskMemoryCandidateStore(nil)
	_, err := store.Snapshot(context.Background(), taskmemory.AuthorizedCandidateQuery{})
	require.ErrorIs(t, err, taskmemory.ErrInvalidRequest)
	_, err = store.GetAuthorizedByRefs(context.Background(), taskmemory.AuthorizedCandidateQuery{}, nil)
	require.ErrorIs(t, err, taskmemory.ErrInvalidRequest)
}
