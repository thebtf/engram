package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/pgvector/pgvector-go"
	"google.golang.org/grpc/metadata"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	engramgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/taskmemory"
	"github.com/thebtf/engram/internal/vectordim"
)

const (
	qualificationSchema = "engram.hap02a.taskmemory-qualification/v1"

	fixtureProjectKey     = "5a5a5a5a-0000-4000-8000-000000000001"
	fixtureAnchorProject  = "5a5a5a5a-0000-4000-8000-000000000002"
	fixtureOtherProject   = "5a5a5a5a-0000-4000-8000-000000000003"
	fixtureProjectName    = "hap02a-tm05"
	fixtureClientInstance = "hap02a-tm05-client"
	fixtureWorkstation    = "hap02a-tm05-workstation"
	fixturePrincipal      = "agent/hap02a-tm05"
	fixtureSession        = "hap02a-tm05-session"

	exactQuery   = "quartz exact"
	lexicalQuery = "cerulean lexical"
	hybridQuery  = "vermilion hybrid"
)

var (
	runIDPattern       = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,55}$`)
	sourceIDPattern    = regexp.MustCompile(`^[a-f0-9]{40}$`)
	databaseNamePrefix = "hap02a_tm05_"
)

type boundaryError struct {
	code string
}

func (e *boundaryError) Error() string {
	return "hap02a taskmemory qualification boundary: " + e.code
}

func boundary(code string) error {
	return &boundaryError{code: code}
}

type commandInput struct {
	RunID        string
	SourceCommit string
	SourceTree   string
	DatabaseDSN  string
}

func validateCommandInput(input commandInput) error {
	if !runIDPattern.MatchString(input.RunID) {
		return boundary("INVALID_RUN_ID")
	}
	if !sourceIDPattern.MatchString(input.SourceCommit) {
		return boundary("INVALID_SOURCE_COMMIT")
	}
	if !sourceIDPattern.MatchString(input.SourceTree) {
		return boundary("INVALID_SOURCE_TREE")
	}
	if strings.TrimSpace(input.DatabaseDSN) == "" {
		return boundary("DATABASE_DSN_REQUIRED")
	}
	return nil
}

type dedicatedDatabase struct {
	dsn      string
	database string
}

func parseDedicatedDSN(raw string) (dedicatedDatabase, error) {
	dsn := strings.TrimSpace(raw)
	if dsn == "" || strings.IndexByte(dsn, 0) >= 0 {
		return dedicatedDatabase{}, boundary("INVALID_DATABASE_DSN")
	}
	parsed, err := url.ParseRequestURI(dsn)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.Fragment != "" || parsed.RawFragment != "" || parsed.RawPath != "" {
		return dedicatedDatabase{}, boundary("INVALID_DATABASE_DSN")
	}
	if scheme := strings.ToLower(parsed.Scheme); scheme != "postgres" && scheme != "postgresql" {
		return dedicatedDatabase{}, boundary("INVALID_DATABASE_DSN_SCHEME")
	}
	if parsed.User == nil || strings.TrimSpace(parsed.User.Username()) == "" {
		return dedicatedDatabase{}, boundary("DATABASE_DSN_USER_REQUIRED")
	}
	if !isLoopbackHost(parsed.Hostname()) {
		return dedicatedDatabase{}, boundary("DATABASE_DSN_LOOPBACK_REQUIRED")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return dedicatedDatabase{}, boundary("INVALID_DATABASE_DSN_PORT")
		}
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	if parsed.Path == "/" || database == "" || strings.Contains(database, "/") || !strings.HasPrefix(database, databaseNamePrefix) || len(database) == len(databaseNamePrefix) {
		return dedicatedDatabase{}, boundary("DEDICATED_DATABASE_REQUIRED")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return dedicatedDatabase{}, boundary("INVALID_DATABASE_DSN")
	}
	for key := range query {
		switch strings.ToLower(key) {
		case "host", "hostaddr", "port", "dbname", "database", "user", "password", "passfile", "service", "servicefile", "options", "search_path":
			return dedicatedDatabase{}, boundary("DATABASE_DSN_OVERRIDE_FORBIDDEN")
		}
	}
	return dedicatedDatabase{dsn: parsed.String(), database: database}, nil
}

func verifyDedicatedDatabase(ctx context.Context, db *gormlib.DB, expected string) error {
	if db == nil || expected == "" {
		return boundary("DATABASE_UNAVAILABLE")
	}
	var row struct {
		Database string `gorm:"column:database"`
	}
	if err := db.WithContext(ctx).Raw(`SELECT current_database() AS database`).Scan(&row).Error; err != nil || row.Database != expected {
		return boundary("DEDICATED_DATABASE_MISMATCH")
	}
	return nil
}

func isLoopbackHost(raw string) bool {
	host := strings.ToLower(strings.TrimSpace(raw))
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

type qualificationReceipt struct {
	Schema                string            `json:"schema"`
	Source                sourceReceipt     `json:"source"`
	Database              databaseReceipt   `json:"database"`
	Scenarios             []scenarioReceipt `json:"scenarios"`
	Mutation              mutationReceipt   `json:"mutation"`
	CleanupRequired       bool              `json:"cleanup_required"`
	TransactionRolledBack bool              `json:"transaction_rolled_back"`
}

type sourceReceipt struct {
	Commit                  string `json:"commit"`
	Tree                    string `json:"tree"`
	PreparationRevision     string `json:"preparation_revision"`
	TaskMemorySHA256        string `json:"task_memory_sha256"`
	CandidateStoreSHA256    string `json:"candidate_store_sha256"`
	AuthorityAdapterSHA256  string `json:"authority_adapter_sha256"`
	RankfusionSHA256        string `json:"rankfusion_sha256"`
	VectorDimensionSHA256   string `json:"vector_dimension_sha256"`
	QueryProfileFingerprint string `json:"query_profile_fingerprint_sha256"`
}

type databaseReceipt struct {
	PostgreSQLVersion       string `json:"postgresql_version"`
	PGVectorVersion         string `json:"pgvector_version"`
	MigrationCurrentVersion string `json:"migration_current_version"`
	MigrationIDsSHA256      string `json:"migration_ids_sha256"`
	SchemaProjectionSHA256  string `json:"schema_projection_sha256"`
}

type referenceTriple struct {
	ID      int64  `json:"id"`
	Version int    `json:"version"`
	Tier    string `json:"tier"`
}

type deadlineReceipt struct {
	Observed       bool `json:"observed"`
	Within600Milli bool `json:"within_600ms"`
}

type scenarioReceipt struct {
	Name                   string            `json:"name"`
	ExpectedMode           string            `json:"expected_mode"`
	ObservedMode           string            `json:"observed_mode"`
	ExpectedReferences     []referenceTriple `json:"expected_references"`
	ObservedReferences     []referenceTriple `json:"observed_references"`
	ResolverCallsDelta     int               `json:"resolver_calls_delta"`
	ProviderSnapshotsDelta int               `json:"provider_snapshots_delta"`
	EmbedderCallsDelta     int               `json:"embedder_calls_delta"`
	EmbedderDeadline       deadlineReceipt   `json:"embedder_deadline"`
	Audit                  auditDeltaReceipt `json:"audit"`
}

type auditDeltaReceipt struct {
	Applicable               bool                     `json:"applicable"`
	CorrelationSHA256        string                   `json:"correlation_sha256"`
	ResolutionAttemptsBefore int                      `json:"resolution_attempts_before"`
	ResolutionAttemptsAfter  int                      `json:"resolution_attempts_after"`
	ResolutionAttemptsDelta  int                      `json:"resolution_attempts_delta"`
	ResolutionAttempts       []resolutionAuditReceipt `json:"resolution_attempts"`
	ComparisonsBefore        int                      `json:"comparisons_before"`
	ComparisonsAfter         int                      `json:"comparisons_after"`
	ComparisonsDelta         int                      `json:"comparisons_delta"`
	Comparisons              []comparisonAuditReceipt `json:"comparisons"`
}

type resolutionAuditReceipt struct {
	Intent            string `json:"intent"`
	Outcome           string `json:"outcome"`
	DescriptorVersion int    `json:"descriptor_version"`
	Provenance        string `json:"provenance"`
}

type comparisonAuditReceipt struct {
	IdempotencyKey         string `json:"idempotency_key"`
	EvidenceFingerprint    string `json:"evidence_fingerprint"`
	ClientInstanceIDSHA256 string `json:"client_instance_id_sha256"`
	V3Outcome              string `json:"v3_outcome"`
	LegacyOutcome          string `json:"legacy_outcome"`
	Classification         string `json:"classification"`
	Transport              string `json:"transport"`
	Scope                  string `json:"scope"`
	Freshness              string `json:"freshness"`
}

type rowDigestsReceipt struct {
	MemoriesBeforeSHA256      string `json:"memories_before_sha256"`
	MemoriesAfterSHA256       string `json:"memories_after_sha256"`
	ContentChunksBeforeSHA256 string `json:"content_chunks_before_sha256"`
	ContentChunksAfterSHA256  string `json:"content_chunks_after_sha256"`
}

type mutationReceipt struct {
	RowDigests                          rowDigestsReceipt `json:"row_digests"`
	ProjectBindingUnchanged             bool              `json:"project_binding_unchanged"`
	ProjectIdentifiersUnchanged         bool              `json:"project_identifiers_unchanged"`
	PublicTaskMemorySchemaBefore        int               `json:"public_task_memory_schema_before"`
	PublicTaskMemorySchemaAfter         int               `json:"public_task_memory_schema_after"`
	PublicTaskMemorySchemaUnchanged     bool              `json:"public_task_memory_schema_unchanged"`
	TemporaryResolutionAttemptsDelta    int               `json:"temporary_resolution_attempts_delta"`
	TemporaryComparisonsDelta           int               `json:"temporary_comparisons_delta"`
	CandidateDomainRowsUnchanged        bool              `json:"candidate_domain_rows_unchanged"`
	OnlyAllowedTemporaryV3AuditRowsGrew bool              `json:"only_allowed_temporary_v3_audit_rows_grew"`
	ZeroCandidateDomainMutation         bool              `json:"zero_candidate_domain_mutation"`
}

type scenarioSpec struct {
	Name                    string
	RequestIdentity         string
	Query                   string
	ExpectedMode            string
	ExpectedReferences      []referenceTriple
	UseEmbedder             bool
	ExpectedComparisonDelta int
}

var qualificationScenarios = []scenarioSpec{
	{
		Name:            "exact",
		RequestIdentity: "exact",
		Query:           exactQuery,
		ExpectedMode:    "exact",
		ExpectedReferences: []referenceTriple{
			{ID: 1001, Version: 11, Tier: "exact"},
		},
		UseEmbedder:             true,
		ExpectedComparisonDelta: 1,
	},
	{
		Name:            "fts",
		RequestIdentity: "fts",
		Query:           lexicalQuery,
		ExpectedMode:    "lexical_degraded",
		ExpectedReferences: []referenceTriple{
			{ID: 2002, Version: 22, Tier: "fts"},
			{ID: 2001, Version: 21, Tier: "fts"},
		},
		ExpectedComparisonDelta: 1,
	},
	{
		Name:            "hybrid",
		RequestIdentity: "hybrid",
		Query:           hybridQuery,
		ExpectedMode:    "hybrid",
		ExpectedReferences: []referenceTriple{
			{ID: 3001, Version: 31, Tier: "fts"},
			{ID: 3002, Version: 32, Tier: "fts"},
			{ID: 3003, Version: 33, Tier: "vector"},
		},
		UseEmbedder:             true,
		ExpectedComparisonDelta: 1,
	},
	{
		Name:         "by_id",
		ExpectedMode: "authorized_by_id",
		ExpectedReferences: []referenceTriple{
			{ID: 3003, Version: 33, Tier: "vector"},
			{ID: 3001, Version: 31, Tier: "fts"},
			{ID: 3002, Version: 32, Tier: "fts"},
		},
	},
	{
		Name:            "hybrid_replay",
		RequestIdentity: "hybrid",
		Query:           hybridQuery,
		ExpectedMode:    "hybrid",
		ExpectedReferences: []referenceTriple{
			{ID: 3001, Version: 31, Tier: "fts"},
			{ID: 3002, Version: 32, Tier: "fts"},
			{ID: 3003, Version: 33, Tier: "vector"},
		},
		UseEmbedder:             true,
		ExpectedComparisonDelta: 0,
	},
}

func marshalReceipt(receipt qualificationReceipt) ([]byte, error) {
	if receipt.Schema != qualificationSchema || !receipt.CleanupRequired || !receipt.TransactionRolledBack {
		return nil, boundary("INVALID_RECEIPT")
	}
	return json.Marshal(receipt)
}

func runQualification(parent context.Context, input commandInput) (qualificationReceipt, error) {
	if err := validateCommandInput(input); err != nil {
		return qualificationReceipt{}, err
	}
	if parent == nil {
		parent = context.Background()
	}
	connection, err := parseDedicatedDSN(input.DatabaseDSN)
	if err != nil {
		return qualificationReceipt{}, err
	}
	source, err := collectSourceReceipt(input.SourceCommit, input.SourceTree)
	if err != nil {
		return qualificationReceipt{}, err
	}
	store, err := engramgorm.NewStore(engramgorm.Config{DSN: connection.dsn, MaxConns: 1, LogLevel: logger.Silent})
	if err != nil {
		return qualificationReceipt{}, boundary("DATABASE_OPEN_FAILED")
	}
	defer func() { _ = store.Close() }()
	if err := verifyDedicatedDatabase(parent, store.GetDB(), connection.database); err != nil {
		return qualificationReceipt{}, err
	}

	database, err := collectDatabaseReceipt(parent, store)
	if err != nil {
		return qualificationReceipt{}, err
	}
	tx := store.GetDB().WithContext(parent).Begin()
	if tx.Error != nil {
		return qualificationReceipt{}, boundary("TRANSACTION_BEGIN_FAILED")
	}
	rolledBack := false
	defer func() {
		if !rolledBack {
			_ = tx.Rollback().Error
		}
	}()
	if err := createTemporaryQualificationTables(parent, tx); err != nil {
		return qualificationReceipt{}, err
	}
	fixture, err := seedQualificationFixture(parent, tx)
	if err != nil {
		return qualificationReceipt{}, err
	}
	before, err := captureMutationBaseline(parent, tx)
	if err != nil {
		return qualificationReceipt{}, err
	}

	transport, authorityServer := grpcserver.New(nil, nil)
	defer transport.Stop()
	authorityServer.SetDB(tx)
	authority := &countingAuthorityResolver{server: authorityServer}
	candidateStore := engramgorm.NewTaskMemoryCandidateStore(&engramgorm.Store{DB: tx})
	provider := &recordingCandidateProvider{provider: candidateStore}
	embedder := &deterministicEmbedder{vector: fixtureUnitVector(1)}
	evidence, err := fixtureTaskEvidence()
	if err != nil {
		return qualificationReceipt{}, err
	}

	exact, _, err := runPreparedScenario(parent, tx, qualificationScenarios[0], input.RunID, evidence, authority, provider, embedder)
	if err != nil {
		return qualificationReceipt{}, err
	}
	if err := assertHiddenReferencesAbsent(exact.ObservedReferences, fixture.hiddenIDs); err != nil {
		return qualificationReceipt{}, err
	}

	fts, _, err := runPreparedScenario(parent, tx, qualificationScenarios[1], input.RunID, evidence, authority, provider, embedder)
	if err != nil {
		return qualificationReceipt{}, err
	}
	if err := assertHiddenReferencesAbsent(fts.ObservedReferences, fixture.hiddenIDs); err != nil {
		return qualificationReceipt{}, err
	}

	hybrid, capturedHybridQuery, err := runPreparedScenario(parent, tx, qualificationScenarios[2], input.RunID, evidence, authority, provider, embedder)
	if err != nil {
		return qualificationReceipt{}, err
	}
	if err := assertHiddenReferencesAbsent(hybrid.ObservedReferences, fixture.hiddenIDs); err != nil {
		return qualificationReceipt{}, err
	}

	byID, err := runByIDScenario(parent, candidateStore, capturedHybridQuery, qualificationScenarios[3], fixture.deniedHybridID)
	if err != nil {
		return qualificationReceipt{}, err
	}
	if err := assertHiddenReferencesAbsent(byID.ObservedReferences, fixture.hiddenIDs); err != nil {
		return qualificationReceipt{}, err
	}

	replay, _, err := runPreparedScenario(parent, tx, qualificationScenarios[4], input.RunID, evidence, authority, provider, embedder)
	if err != nil {
		return qualificationReceipt{}, err
	}
	if err := assertHiddenReferencesAbsent(replay.ObservedReferences, fixture.hiddenIDs); err != nil {
		return qualificationReceipt{}, err
	}
	if !equalReferenceTriples(hybrid.ObservedReferences, replay.ObservedReferences) {
		return qualificationReceipt{}, boundary("HYBRID_REPLAY_MISMATCH")
	}

	after, err := captureMutationBaseline(parent, tx)
	if err != nil {
		return qualificationReceipt{}, err
	}
	mutation, err := buildMutationReceipt(before, after)
	if err != nil {
		return qualificationReceipt{}, err
	}
	if !mutation.ZeroCandidateDomainMutation {
		return qualificationReceipt{}, boundary("CANDIDATE_DOMAIN_MUTATION_DETECTED")
	}

	receipt := qualificationReceipt{
		Schema:          qualificationSchema,
		Source:          source,
		Database:        database,
		Scenarios:       []scenarioReceipt{exact, fts, hybrid, byID, replay},
		Mutation:        mutation,
		CleanupRequired: true,
	}
	if err := tx.Rollback().Error; err != nil {
		return qualificationReceipt{}, boundary("TRANSACTION_ROLLBACK_FAILED")
	}
	rolledBack = true
	receipt.TransactionRolledBack = true
	return receipt, nil
}

func createTemporaryQualificationTables(ctx context.Context, tx *gormlib.DB) error {
	for _, table := range qualificationTables() {
		statement := "CREATE TEMPORARY TABLE " + table + " (LIKE public." + table + " INCLUDING ALL) ON COMMIT DROP"
		if err := tx.WithContext(ctx).Exec(statement).Error; err != nil {
			return boundary("TEMPORARY_TABLE_SETUP_FAILED")
		}
	}
	if err := tx.WithContext(ctx).Exec("SET LOCAL search_path = pg_temp, public").Error; err != nil {
		return boundary("TEMPORARY_SCHEMA_SETUP_FAILED")
	}
	return nil
}

func qualificationTables() []string {
	return []string{
		"memories",
		"content_chunks",
		"projects",
		"project_identifiers",
		"project_resolution_attempts",
		"project_identity_comparisons",
	}
}

type fixtureRows struct {
	hiddenIDs      map[int64]struct{}
	deniedHybridID int64
}

type memoryFixture struct {
	ID                  int64
	Version             int
	Project             string
	Content             string
	PrivacyScope        string
	SourceWorkstationID string
	SourceSessions      []string
	OwnerPrincipal      string
	OwnerPrincipalKind  string
	AgentVisibility     string
	Domain              string
	CreatedAt           time.Time
}

type chunkFixture struct {
	ID        int64
	MemoryID  int64
	Sequence  int
	Embedding []float32
}

func seedQualificationFixture(ctx context.Context, tx *gormlib.DB) (fixtureRows, error) {
	project := engramgorm.Project{
		ID:              fixtureProjectKey,
		ProjectKey:      sql.NullString{String: fixtureProjectKey, Valid: true},
		AnchorProjectID: sql.NullString{String: fixtureAnchorProject, Valid: true},
		IdentityScope:   sql.NullString{String: "repository", Valid: true},
		IdentityStatus:  sql.NullString{String: "active", Valid: true},
		LegacyIDs:       pq.StringArray{},
	}
	if err := tx.WithContext(ctx).Create(&project).Error; err != nil {
		return fixtureRows{}, boundary("PROJECT_BINDING_SEED_FAILED")
	}

	created := func(hour int) time.Time {
		return time.Date(2025, time.January, 2, hour, 0, 0, 0, time.UTC)
	}
	visible := func(id int64, version int, content string, hour int) memoryFixture {
		return memoryFixture{
			ID:             id,
			Version:        version,
			Project:        fixtureProjectKey,
			Content:        content,
			PrivacyScope:   "project",
			SourceSessions: []string{},
			CreatedAt:      created(hour),
		}
	}
	denied := func(id int64, version int, content string, hour int, kind int) memoryFixture {
		row := visible(id, version, content, hour)
		switch kind % 5 {
		case 0:
			row.Project = fixtureOtherProject
		case 1:
			row.AgentVisibility = "private"
			row.OwnerPrincipal = "agent/other"
			row.OwnerPrincipalKind = "agent"
		case 2:
			row.AgentVisibility = "shared"
			row.Domain = "fixture-domain"
			row.OwnerPrincipal = "agent/other"
			row.OwnerPrincipalKind = "agent"
		case 3:
			row.PrivacyScope = "private"
			row.SourceWorkstationID = "other-workstation"
			row.SourceSessions = []string{fixtureSession}
		case 4:
			row.PrivacyScope = "private"
			row.SourceWorkstationID = fixtureWorkstation
			row.SourceSessions = []string{"other-session"}
		}
		return row
	}

	rows := []memoryFixture{
		visible(1001, 11, exactQuery, 1),
		denied(1101, 111, exactQuery, 9, 0),
		denied(1102, 112, exactQuery, 10, 1),
		visible(2001, 21, lexicalQuery+" retained", 2),
		visible(2002, 22, lexicalQuery+" retained", 3),
		denied(2101, 121, strings.Repeat(lexicalQuery+" ", 6)+"hidden", 11, 0),
		denied(2102, 122, strings.Repeat(lexicalQuery+" ", 6)+"hidden", 12, 1),
		denied(2103, 123, strings.Repeat(lexicalQuery+" ", 6)+"hidden", 13, 2),
		denied(2104, 124, strings.Repeat(lexicalQuery+" ", 6)+"hidden", 14, 3),
		visible(3001, 31, strings.Repeat(hybridQuery+" ", 3)+"overlap", 4),
		visible(3002, 32, hybridQuery+" fts", 3),
		visible(3003, 33, "semantic vector candidate", 2),
	}
	hiddenIDs := map[int64]struct{}{
		1101: {}, 1102: {}, 2101: {}, 2102: {}, 2103: {}, 2104: {},
	}
	for index := range 9 {
		id := int64(3101 + index)
		rows = append(rows, denied(id, 131+index, "concealed semantic candidate", 15+index, index))
		hiddenIDs[id] = struct{}{}
	}
	for _, row := range rows {
		if err := insertMemoryFixture(ctx, tx, row); err != nil {
			return fixtureRows{}, err
		}
	}

	chunks := []chunkFixture{
		{ID: 9001, MemoryID: 3001, Sequence: 0, Embedding: fixtureUnitVector(0.80)},
		{ID: 9002, MemoryID: 3001, Sequence: 1, Embedding: fixtureUnitVector(0.93)},
		{ID: 9003, MemoryID: 3003, Sequence: 0, Embedding: fixtureUnitVector(0.90)},
	}
	for index := range 9 {
		chunks = append(chunks, chunkFixture{
			ID:        int64(9101 + index),
			MemoryID:  int64(3101 + index),
			Sequence:  0,
			Embedding: fixtureUnitVector(0.99),
		})
	}
	for _, chunk := range chunks {
		if err := insertChunkFixture(ctx, tx, chunk); err != nil {
			return fixtureRows{}, err
		}
	}
	return fixtureRows{hiddenIDs: hiddenIDs, deniedHybridID: 3101}, nil
}

func insertMemoryFixture(ctx context.Context, tx *gormlib.DB, fixture memoryFixture) error {
	sessions := fixture.SourceSessions
	if sessions == nil {
		sessions = []string{}
	}
	now := time.Now().UTC()
	if err := tx.WithContext(ctx).Exec(`
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
		now.Add(-time.Hour),
		now.Add(time.Hour),
		fixture.Version,
		fixture.CreatedAt,
		fixture.CreatedAt,
	).Error; err != nil {
		return boundary("MEMORY_FIXTURE_SEED_FAILED")
	}
	return nil
}

func insertChunkFixture(ctx context.Context, tx *gormlib.DB, fixture chunkFixture) error {
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO content_chunks (id, memory_id, seq, text, embedding, model, created_at)
		VALUES (?, ?, ?, '', ?::vector, 'hap02a-tm05', ?)
	`, fixture.ID, fixture.MemoryID, fixture.Sequence, pgvector.NewVector(fixture.Embedding), time.Now().UTC()).Error; err != nil {
		return boundary("CHUNK_FIXTURE_SEED_FAILED")
	}
	return nil
}

func fixtureTaskEvidence() (taskmemory.ProjectEvidenceV3, error) {
	anchor := projectidentity.AnchorV3{
		Version:   3,
		ProjectID: fixtureAnchorProject,
		Name:      fixtureProjectName,
		Scope:     "repository",
	}
	descriptor, err := projectidentity.BuildDescriptorV3(anchor, nil, nil, fixtureClientInstance)
	if err != nil {
		return taskmemory.ProjectEvidenceV3{}, boundary("PROJECT_EVIDENCE_INVALID")
	}
	return taskmemory.ProjectEvidenceV3{Anchor: anchor, Descriptor: descriptor}, nil
}

type countingAuthorityResolver struct {
	server *grpcserver.Server
	calls  int
}

func (resolver *countingAuthorityResolver) ResolveTaskAuthority(ctx context.Context, evidence taskmemory.ProjectEvidenceV3) (taskmemory.AuthorizedTaskContext, error) {
	if resolver == nil || resolver.server == nil {
		return taskmemory.AuthorizedTaskContext{}, boundary("AUTHORITY_UNAVAILABLE")
	}
	resolver.calls++
	return resolver.server.ResolveTaskMemoryAuthority(ctx, evidence)
}

type recordingCandidateProvider struct {
	provider taskmemory.AuthorizedCandidateProvider
	calls    int
	last     taskmemory.AuthorizedCandidateQuery
	captured bool
}

func (provider *recordingCandidateProvider) Snapshot(ctx context.Context, query taskmemory.AuthorizedCandidateQuery) (taskmemory.CandidateSnapshot, error) {
	if provider == nil || provider.provider == nil {
		return taskmemory.CandidateSnapshot{}, boundary("CANDIDATE_PROVIDER_UNAVAILABLE")
	}
	provider.calls++
	provider.last = query
	provider.captured = true
	return provider.provider.Snapshot(ctx, query)
}

func (provider *recordingCandidateProvider) Last() (taskmemory.AuthorizedCandidateQuery, bool) {
	if provider == nil || !provider.captured {
		return taskmemory.AuthorizedCandidateQuery{}, false
	}
	return provider.last, true
}

type deterministicEmbedder struct {
	vector                  []float32
	calls                   int
	deadlineObserved        int
	deadlineWithin600Millis int
}

func (embedder *deterministicEmbedder) Embed(ctx context.Context, _ []string) ([][]float32, error) {
	if embedder == nil || len(embedder.vector) != vectordim.Dimension {
		return nil, boundary("EMBEDDER_UNAVAILABLE")
	}
	embedder.calls++
	if deadline, ok := ctx.Deadline(); ok {
		embedder.deadlineObserved++
		remaining := time.Until(deadline)
		if remaining > 0 && remaining <= 600*time.Millisecond {
			embedder.deadlineWithin600Millis++
		}
	}
	return [][]float32{append([]float32(nil), embedder.vector...)}, nil
}

func fixtureUnitVector(cosine float32) []float32 {
	vector := make([]float32, vectordim.Dimension)
	vector[0] = cosine
	vector[1] = float32(math.Sqrt(1 - float64(cosine)*float64(cosine)))
	return vector
}

func runPreparedScenario(
	parent context.Context,
	tx *gormlib.DB,
	specification scenarioSpec,
	runID string,
	evidence taskmemory.ProjectEvidenceV3,
	authority *countingAuthorityResolver,
	provider *recordingCandidateProvider,
	embedder *deterministicEmbedder,
) (scenarioReceipt, taskmemory.AuthorizedCandidateQuery, error) {
	requestID := fixtureRequestID(specification.RequestIdentity, runID)
	correlation, err := fixtureCorrelation(evidence, requestID)
	if err != nil {
		return scenarioReceipt{}, taskmemory.AuthorizedCandidateQuery{}, err
	}
	beforeAudit, err := captureCorrelationCounts(parent, tx, correlation)
	if err != nil {
		return scenarioReceipt{}, taskmemory.AuthorizedCandidateQuery{}, err
	}
	authorityBefore := authority.calls
	providerBefore := provider.calls
	embedderCallsBefore := embedder.calls
	deadlineObservedBefore := embedder.deadlineObserved
	deadlineWithinBefore := embedder.deadlineWithin600Millis

	facts, err := taskmemory.NewTaskFacts(specification.Query)
	if err != nil {
		return scenarioReceipt{}, taskmemory.AuthorizedCandidateQuery{}, boundary("TASK_FACTS_INVALID")
	}
	config := taskmemory.PreparerConfig{
		Authority:   authority,
		Candidates:  provider,
		MaxDuration: time.Second,
	}
	if specification.UseEmbedder {
		config.Embedder = embedder
	}
	preparer, err := taskmemory.NewPreparer(config)
	if err != nil {
		return scenarioReceipt{}, taskmemory.AuthorizedCandidateQuery{}, boundary("PREPARER_CONSTRUCTION_FAILED")
	}
	prepared, err := preparer.Prepare(fixtureScenarioContext(parent, requestID), taskmemory.PrepareRequest{
		Project: evidence,
		Task:    facts,
	})
	if err != nil {
		return scenarioReceipt{}, taskmemory.AuthorizedCandidateQuery{}, boundary("PREPARATION_FAILED")
	}
	if prepared.PreparationRevision() != taskmemory.PreparationRevision || prepared.Context().ResolutionCorrelation() != correlation {
		return scenarioReceipt{}, taskmemory.AuthorizedCandidateQuery{}, boundary("PREPARATION_AUTHORITY_MISMATCH")
	}
	query, captured := provider.Last()
	if !captured || !query.Valid() {
		return scenarioReceipt{}, taskmemory.AuthorizedCandidateQuery{}, boundary("AUTHORIZED_QUERY_NOT_CAPTURED")
	}
	afterAudit, err := collectAuditDelta(parent, tx, correlation, beforeAudit)
	if err != nil {
		return scenarioReceipt{}, taskmemory.AuthorizedCandidateQuery{}, err
	}
	observedMode, err := retrievalModeName(prepared.Mode())
	if err != nil {
		return scenarioReceipt{}, taskmemory.AuthorizedCandidateQuery{}, err
	}
	observedReferences, err := referencesFromCandidates(prepared.Candidates())
	if err != nil {
		return scenarioReceipt{}, taskmemory.AuthorizedCandidateQuery{}, err
	}
	deadlineObservedDelta := embedder.deadlineObserved - deadlineObservedBefore
	deadlineWithinDelta := embedder.deadlineWithin600Millis - deadlineWithinBefore
	receipt := scenarioReceipt{
		Name:                   specification.Name,
		ExpectedMode:           specification.ExpectedMode,
		ObservedMode:           observedMode,
		ExpectedReferences:     cloneReferenceTriples(specification.ExpectedReferences),
		ObservedReferences:     observedReferences,
		ResolverCallsDelta:     authority.calls - authorityBefore,
		ProviderSnapshotsDelta: provider.calls - providerBefore,
		EmbedderCallsDelta:     embedder.calls - embedderCallsBefore,
		EmbedderDeadline: deadlineReceipt{
			Observed:       deadlineObservedDelta > 0,
			Within600Milli: deadlineObservedDelta > 0 && deadlineWithinDelta == deadlineObservedDelta,
		},
		Audit: afterAudit,
	}
	if err := validatePreparedScenario(receipt, specification); err != nil {
		return scenarioReceipt{}, taskmemory.AuthorizedCandidateQuery{}, err
	}
	return receipt, query, nil
}

func fixtureRequestID(identity, runID string) string {
	return "hap02a-tm05-" + identity + "-" + runID
}

func fixtureScenarioContext(parent context.Context, requestID string) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	ctx := auditcontext.WithSourceSession(parent, fixtureSession)
	ctx = auth.WithIdentity(ctx, auth.ClientWithPrincipal("read-write", fixtureWorkstation, fixturePrincipal, auth.PrincipalKindAgent))
	return metadata.NewIncomingContext(ctx, metadata.Pairs(
		"x-engram-project-identity-adapter", "daemon",
		"x-request-id", requestID,
	))
}

func fixtureCorrelation(evidence taskmemory.ProjectEvidenceV3, requestID string) (projectidentity.CorrelationV3, error) {
	origin := projectidentity.NewComparisonOriginV3(projectidentity.ComparisonTransportGRPCV3, "daemon", requestID)
	references, err := origin.DeriveComparisonReferencesV3(evidence.Anchor, evidence.Descriptor, projectidentity.ReadFilterIntentV3)
	if err != nil {
		return "", boundary("CORRELATION_DERIVATION_FAILED")
	}
	return references.Correlation, nil
}

func validatePreparedScenario(receipt scenarioReceipt, specification scenarioSpec) error {
	if len(receipt.ExpectedReferences) == 0 || !equalReferenceTriples(receipt.ExpectedReferences, specification.ExpectedReferences) || receipt.ExpectedMode != specification.ExpectedMode || receipt.ObservedMode != specification.ExpectedMode || !equalReferenceTriples(receipt.ObservedReferences, specification.ExpectedReferences) {
		return boundary("SCENARIO_EXPECTATION_MISMATCH")
	}
	if receipt.ResolverCallsDelta != 1 || receipt.ProviderSnapshotsDelta != 2 {
		return boundary("PREPARATION_CALL_DELTA_MISMATCH")
	}
	if specification.UseEmbedder {
		if receipt.EmbedderCallsDelta != 1 || !receipt.EmbedderDeadline.Observed || !receipt.EmbedderDeadline.Within600Milli {
			return boundary("EMBEDDER_OBSERVATION_MISMATCH")
		}
	} else if receipt.EmbedderCallsDelta != 0 || receipt.EmbedderDeadline.Observed || receipt.EmbedderDeadline.Within600Milli {
		return boundary("LEXICAL_EMBEDDER_MISMATCH")
	}
	if err := validateAuditDelta(receipt.Audit, specification.ExpectedComparisonDelta); err != nil {
		return err
	}
	return nil
}

func runByIDScenario(ctx context.Context, store *engramgorm.TaskMemoryCandidateStore, query taskmemory.AuthorizedCandidateQuery, specification scenarioSpec, deniedID int64) (scenarioReceipt, error) {
	if store == nil || !query.Valid() {
		return scenarioReceipt{}, boundary("AUTHORIZED_BY_ID_UNAVAILABLE")
	}
	lookups, err := candidateLookups([]struct {
		id   int64
		tier taskmemory.CandidateSourceTier
	}{
		{id: deniedID, tier: taskmemory.CandidateVector},
		{id: 3003, tier: taskmemory.CandidateVector},
		{id: 999999, tier: taskmemory.CandidateFTS},
		{id: 3001, tier: taskmemory.CandidateFTS},
		{id: 3002, tier: taskmemory.CandidateFTS},
	})
	if err != nil {
		return scenarioReceipt{}, err
	}
	refs, err := store.GetAuthorizedByRefs(ctx, query, lookups)
	if err != nil {
		return scenarioReceipt{}, boundary("AUTHORIZED_BY_ID_FAILED")
	}
	deniedLookup, err := taskmemory.NewCandidateLookup(deniedID, taskmemory.CandidateVector)
	if err != nil {
		return scenarioReceipt{}, boundary("AUTHORIZED_BY_ID_LOOKUP_INVALID")
	}
	absentLookup, err := taskmemory.NewCandidateLookup(999998, taskmemory.CandidateVector)
	if err != nil {
		return scenarioReceipt{}, boundary("AUTHORIZED_BY_ID_LOOKUP_INVALID")
	}
	deniedOnly, err := store.GetAuthorizedByRefs(ctx, query, []taskmemory.CandidateLookup{deniedLookup})
	if err != nil {
		return scenarioReceipt{}, boundary("AUTHORIZED_BY_ID_FAILED")
	}
	absentOnly, err := store.GetAuthorizedByRefs(ctx, query, []taskmemory.CandidateLookup{absentLookup})
	if err != nil {
		return scenarioReceipt{}, boundary("AUTHORIZED_BY_ID_FAILED")
	}
	deniedTriples, err := referencesFromCandidates(deniedOnly)
	if err != nil {
		return scenarioReceipt{}, err
	}
	absentTriples, err := referencesFromCandidates(absentOnly)
	if err != nil {
		return scenarioReceipt{}, err
	}
	if !equalReferenceTriples(deniedTriples, absentTriples) || len(deniedTriples) != 0 {
		return scenarioReceipt{}, boundary("DENIED_ABSENT_DISTINGUISHABLE")
	}
	observed, err := referencesFromCandidates(refs)
	if err != nil {
		return scenarioReceipt{}, err
	}
	expected := cloneReferenceTriples(specification.ExpectedReferences)
	receipt := scenarioReceipt{
		Name:                   specification.Name,
		ExpectedMode:           specification.ExpectedMode,
		ObservedMode:           specification.ExpectedMode,
		ExpectedReferences:     expected,
		ObservedReferences:     observed,
		ResolverCallsDelta:     0,
		ProviderSnapshotsDelta: 0,
		EmbedderCallsDelta:     0,
		EmbedderDeadline:       deadlineReceipt{},
		Audit:                  auditDeltaReceipt{Applicable: false, ResolutionAttempts: []resolutionAuditReceipt{}, Comparisons: []comparisonAuditReceipt{}},
	}
	if len(receipt.ExpectedReferences) == 0 || specification.Name != "by_id" || specification.ExpectedMode != "authorized_by_id" || !equalReferenceTriples(receipt.ExpectedReferences, receipt.ObservedReferences) {
		return scenarioReceipt{}, boundary("AUTHORIZED_BY_ID_EXPECTATION_MISMATCH")
	}
	return receipt, nil
}

func candidateLookups(specifications []struct {
	id   int64
	tier taskmemory.CandidateSourceTier
},
) ([]taskmemory.CandidateLookup, error) {
	lookups := make([]taskmemory.CandidateLookup, 0, len(specifications))
	for _, specification := range specifications {
		lookup, err := taskmemory.NewCandidateLookup(specification.id, specification.tier)
		if err != nil {
			return nil, boundary("AUTHORIZED_BY_ID_LOOKUP_INVALID")
		}
		lookups = append(lookups, lookup)
	}
	return lookups, nil
}

func assertHiddenReferencesAbsent(references []referenceTriple, hidden map[int64]struct{}) error {
	for _, reference := range references {
		if _, found := hidden[reference.ID]; found {
			return boundary("HIDDEN_CANDIDATE_EXPOSED")
		}
	}
	return nil
}

func retrievalModeName(mode taskmemory.RetrievalMode) (string, error) {
	switch mode {
	case taskmemory.RetrievalExact:
		return "exact", nil
	case taskmemory.RetrievalLexicalDegraded:
		return "lexical_degraded", nil
	case taskmemory.RetrievalHybrid:
		return "hybrid", nil
	case taskmemory.RetrievalEmpty:
		return "empty", nil
	default:
		return "", boundary("UNKNOWN_RETRIEVAL_MODE")
	}
}

func candidateTierName(tier taskmemory.CandidateSourceTier) (string, error) {
	switch tier {
	case taskmemory.CandidateExact:
		return "exact", nil
	case taskmemory.CandidateFTS:
		return "fts", nil
	case taskmemory.CandidateVector:
		return "vector", nil
	default:
		return "", boundary("UNKNOWN_CANDIDATE_TIER")
	}
}

func referencesFromCandidates(candidates []taskmemory.AuthorizedCandidateRef) ([]referenceTriple, error) {
	references := make([]referenceTriple, 0, len(candidates))
	for _, candidate := range candidates {
		tier, err := candidateTierName(candidate.SourceTier())
		if err != nil {
			return nil, err
		}
		references = append(references, referenceTriple{ID: candidate.ID(), Version: candidate.Version(), Tier: tier})
	}
	return references, nil
}

func cloneReferenceTriples(values []referenceTriple) []referenceTriple {
	return append([]referenceTriple(nil), values...)
}

func equalReferenceTriples(left, right []referenceTriple) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type correlationCounts struct {
	resolutionAttempts int
	comparisons        int
}

type resolutionAuditDatabaseRow struct {
	Intent            string `gorm:"column:intent"`
	Outcome           string `gorm:"column:outcome"`
	DescriptorVersion int    `gorm:"column:descriptor_version"`
	Provenance        string `gorm:"column:provenance"`
}

type comparisonAuditDatabaseRow struct {
	IdempotencyKey      string `gorm:"column:idempotency_key"`
	EvidenceFingerprint string `gorm:"column:evidence_fingerprint"`
	ClientInstanceID    string `gorm:"column:client_instance_id"`
	V3Outcome           string `gorm:"column:v3_outcome"`
	LegacyOutcome       string `gorm:"column:legacy_outcome"`
	Classification      string `gorm:"column:classification"`
	Transport           string `gorm:"column:transport"`
	Scope               string `gorm:"column:scope"`
	Freshness           string `gorm:"column:freshness"`
}

func captureCorrelationCounts(ctx context.Context, tx *gormlib.DB, correlation projectidentity.CorrelationV3) (correlationCounts, error) {
	resolutionAttempts, err := countResolutionAttemptsByCorrelation(ctx, tx, correlation)
	if err != nil {
		return correlationCounts{}, err
	}
	comparisons, err := countComparisonsByCorrelation(ctx, tx, correlation)
	if err != nil {
		return correlationCounts{}, err
	}
	return correlationCounts{resolutionAttempts: resolutionAttempts, comparisons: comparisons}, nil
}

func collectAuditDelta(ctx context.Context, tx *gormlib.DB, correlation projectidentity.CorrelationV3, before correlationCounts) (auditDeltaReceipt, error) {
	after, err := captureCorrelationCounts(ctx, tx, correlation)
	if err != nil {
		return auditDeltaReceipt{}, err
	}
	var resolutionRows []resolutionAuditDatabaseRow
	if err := tx.WithContext(ctx).Raw(`
		SELECT intent, outcome, descriptor_version, provenance
		FROM project_resolution_attempts
		WHERE correlation = ?
		ORDER BY attempt_id ASC
	`, string(correlation)).Scan(&resolutionRows).Error; err != nil {
		return auditDeltaReceipt{}, boundary("RESOLUTION_AUDIT_READ_FAILED")
	}
	var comparisonRows []comparisonAuditDatabaseRow
	if err := tx.WithContext(ctx).Raw(`
		SELECT idempotency_key, evidence_fingerprint, client_instance_id,
			v3_outcome, legacy_outcome, classification, transport, scope, freshness
		FROM project_identity_comparisons
		WHERE correlation = ?
		ORDER BY comparison_id ASC
	`, string(correlation)).Scan(&comparisonRows).Error; err != nil {
		return auditDeltaReceipt{}, boundary("COMPARISON_AUDIT_READ_FAILED")
	}
	resolutionReceipts := make([]resolutionAuditReceipt, 0, len(resolutionRows))
	for _, row := range resolutionRows {
		resolutionReceipts = append(resolutionReceipts, resolutionAuditReceipt{
			Intent:            row.Intent,
			Outcome:           row.Outcome,
			DescriptorVersion: row.DescriptorVersion,
			Provenance:        row.Provenance,
		})
	}
	comparisonReceipts := make([]comparisonAuditReceipt, 0, len(comparisonRows))
	for _, row := range comparisonRows {
		comparisonReceipts = append(comparisonReceipts, comparisonAuditReceipt{
			IdempotencyKey:         row.IdempotencyKey,
			EvidenceFingerprint:    row.EvidenceFingerprint,
			ClientInstanceIDSHA256: sha256Hex([]byte(row.ClientInstanceID)),
			V3Outcome:              row.V3Outcome,
			LegacyOutcome:          row.LegacyOutcome,
			Classification:         row.Classification,
			Transport:              row.Transport,
			Scope:                  row.Scope,
			Freshness:              row.Freshness,
		})
	}
	return auditDeltaReceipt{
		Applicable:               true,
		CorrelationSHA256:        sha256Hex([]byte(string(correlation))),
		ResolutionAttemptsBefore: before.resolutionAttempts,
		ResolutionAttemptsAfter:  after.resolutionAttempts,
		ResolutionAttemptsDelta:  after.resolutionAttempts - before.resolutionAttempts,
		ResolutionAttempts:       resolutionReceipts,
		ComparisonsBefore:        before.comparisons,
		ComparisonsAfter:         after.comparisons,
		ComparisonsDelta:         after.comparisons - before.comparisons,
		Comparisons:              comparisonReceipts,
	}, nil
}

func validateAuditDelta(receipt auditDeltaReceipt, expectedComparisonDelta int) error {
	if !receipt.Applicable || receipt.CorrelationSHA256 == "" || receipt.ResolutionAttemptsDelta != 1 || receipt.ResolutionAttemptsAfter != receipt.ResolutionAttemptsBefore+1 || len(receipt.ResolutionAttempts) != receipt.ResolutionAttemptsAfter {
		return boundary("RESOLUTION_AUDIT_DELTA_MISMATCH")
	}
	for _, row := range receipt.ResolutionAttempts {
		if row.Intent != "read_filter" || row.Outcome != "PROJECT_RESOLVED" || row.DescriptorVersion != 3 || row.Provenance != "anchor_v3" {
			return boundary("RESOLUTION_AUDIT_READBACK_MISMATCH")
		}
	}
	if receipt.ComparisonsDelta != expectedComparisonDelta || receipt.ComparisonsAfter != receipt.ComparisonsBefore+expectedComparisonDelta || len(receipt.Comparisons) != receipt.ComparisonsAfter {
		return boundary("COMPARISON_AUDIT_DELTA_MISMATCH")
	}
	for _, row := range receipt.Comparisons {
		if row.IdempotencyKey == "" || row.EvidenceFingerprint == "" || row.ClientInstanceIDSHA256 == "" || row.V3Outcome != "PROJECT_RESOLVED" || row.LegacyOutcome != "unavailable" || row.Classification != "unavailable" || row.Transport != "daemon" || row.Scope != "repository" || row.Freshness != "unknown" {
			return boundary("COMPARISON_AUDIT_READBACK_MISMATCH")
		}
	}
	return nil
}

func countResolutionAttemptsByCorrelation(ctx context.Context, tx *gormlib.DB, correlation projectidentity.CorrelationV3) (int, error) {
	var row struct {
		Count int `gorm:"column:count"`
	}
	if err := tx.WithContext(ctx).Raw(`SELECT COUNT(*) AS count FROM project_resolution_attempts WHERE correlation = ?`, string(correlation)).Scan(&row).Error; err != nil {
		return 0, boundary("RESOLUTION_AUDIT_COUNT_FAILED")
	}
	return row.Count, nil
}

func countComparisonsByCorrelation(ctx context.Context, tx *gormlib.DB, correlation projectidentity.CorrelationV3) (int, error) {
	var row struct {
		Count int `gorm:"column:count"`
	}
	if err := tx.WithContext(ctx).Raw(`SELECT COUNT(*) AS count FROM project_identity_comparisons WHERE correlation = ?`, string(correlation)).Scan(&row).Error; err != nil {
		return 0, boundary("COMPARISON_AUDIT_COUNT_FAILED")
	}
	return row.Count, nil
}

type mutationBaseline struct {
	memoriesSHA256      string
	contentChunksSHA256 string
	projectsSHA256      string
	identifiersSHA256   string
	taskMemorySchema    int
	resolutionAttempts  int
	comparisons         int
}

func captureMutationBaseline(ctx context.Context, tx *gormlib.DB) (mutationBaseline, error) {
	memories, err := temporaryTableDigest(ctx, tx, "memories", "id")
	if err != nil {
		return mutationBaseline{}, err
	}
	chunks, err := temporaryTableDigest(ctx, tx, "content_chunks", "id")
	if err != nil {
		return mutationBaseline{}, err
	}
	projects, err := temporaryTableDigest(ctx, tx, "projects", "id")
	if err != nil {
		return mutationBaseline{}, err
	}
	identifiers, err := temporaryTableDigest(ctx, tx, "project_identifiers", "identifier_id")
	if err != nil {
		return mutationBaseline{}, err
	}
	schemaCount, err := publicTaskMemorySchemaCount(ctx, tx)
	if err != nil {
		return mutationBaseline{}, err
	}
	resolutionAttempts, err := temporaryTableCount(ctx, tx, "project_resolution_attempts")
	if err != nil {
		return mutationBaseline{}, err
	}
	comparisons, err := temporaryTableCount(ctx, tx, "project_identity_comparisons")
	if err != nil {
		return mutationBaseline{}, err
	}
	return mutationBaseline{
		memoriesSHA256:      memories,
		contentChunksSHA256: chunks,
		projectsSHA256:      projects,
		identifiersSHA256:   identifiers,
		taskMemorySchema:    schemaCount,
		resolutionAttempts:  resolutionAttempts,
		comparisons:         comparisons,
	}, nil
}

func buildMutationReceipt(before, after mutationBaseline) (mutationReceipt, error) {
	candidateDomainRowsUnchanged := before.memoriesSHA256 == after.memoriesSHA256 && before.contentChunksSHA256 == after.contentChunksSHA256
	projectBindingUnchanged := before.projectsSHA256 == after.projectsSHA256
	identifiersUnchanged := before.identifiersSHA256 == after.identifiersSHA256
	schemaUnchanged := before.taskMemorySchema == after.taskMemorySchema
	resolutionDelta := after.resolutionAttempts - before.resolutionAttempts
	comparisonDelta := after.comparisons - before.comparisons
	onlyAllowedAuditRowsGrew := candidateDomainRowsUnchanged && projectBindingUnchanged && identifiersUnchanged && resolutionDelta == 4 && comparisonDelta == 3
	zeroMutation := candidateDomainRowsUnchanged && schemaUnchanged && onlyAllowedAuditRowsGrew
	receipt := mutationReceipt{
		RowDigests: rowDigestsReceipt{
			MemoriesBeforeSHA256:      before.memoriesSHA256,
			MemoriesAfterSHA256:       after.memoriesSHA256,
			ContentChunksBeforeSHA256: before.contentChunksSHA256,
			ContentChunksAfterSHA256:  after.contentChunksSHA256,
		},
		ProjectBindingUnchanged:             projectBindingUnchanged,
		ProjectIdentifiersUnchanged:         identifiersUnchanged,
		PublicTaskMemorySchemaBefore:        before.taskMemorySchema,
		PublicTaskMemorySchemaAfter:         after.taskMemorySchema,
		PublicTaskMemorySchemaUnchanged:     schemaUnchanged,
		TemporaryResolutionAttemptsDelta:    resolutionDelta,
		TemporaryComparisonsDelta:           comparisonDelta,
		CandidateDomainRowsUnchanged:        candidateDomainRowsUnchanged,
		OnlyAllowedTemporaryV3AuditRowsGrew: onlyAllowedAuditRowsGrew,
		ZeroCandidateDomainMutation:         zeroMutation,
	}
	if !zeroMutation {
		return mutationReceipt{}, boundary("MUTATION_ASSERTION_FAILED")
	}
	return receipt, nil
}

func temporaryTableDigest(ctx context.Context, tx *gormlib.DB, table, orderColumn string) (string, error) {
	if !isQualificationTable(table) || !isDigestOrderColumn(table, orderColumn) {
		return "", boundary("TEMPORARY_DIGEST_TARGET_INVALID")
	}
	query := "SELECT COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY row_value." + orderColumn + ")::text, '[]') AS value FROM " + table + " AS row_value"
	var row struct {
		Value string `gorm:"column:value"`
	}
	if err := tx.WithContext(ctx).Raw(query).Scan(&row).Error; err != nil {
		return "", boundary("TEMPORARY_DIGEST_READ_FAILED")
	}
	return sha256Hex([]byte(row.Value)), nil
}

func isQualificationTable(table string) bool {
	for _, candidate := range qualificationTables() {
		if candidate == table {
			return true
		}
	}
	return false
}

func isDigestOrderColumn(table, column string) bool {
	switch table {
	case "memories", "content_chunks", "projects":
		return column == "id"
	case "project_identifiers":
		return column == "identifier_id"
	default:
		return false
	}
}

func temporaryTableCount(ctx context.Context, tx *gormlib.DB, table string) (int, error) {
	if !isQualificationTable(table) {
		return 0, boundary("TEMPORARY_COUNT_TARGET_INVALID")
	}
	var row struct {
		Count int `gorm:"column:count"`
	}
	if err := tx.WithContext(ctx).Raw("SELECT COUNT(*) AS count FROM " + table).Scan(&row).Error; err != nil {
		return 0, boundary("TEMPORARY_COUNT_FAILED")
	}
	return row.Count, nil
}

func publicTaskMemorySchemaCount(ctx context.Context, db *gormlib.DB) (int, error) {
	var row struct {
		Count int `gorm:"column:count"`
	}
	if err := db.WithContext(ctx).Raw(`
		SELECT COUNT(*) AS count
		FROM pg_catalog.pg_class AS class
		JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = class.relnamespace
		WHERE namespace.nspname = 'public'
		  AND class.relname LIKE 'task_memory%'
	`).Scan(&row).Error; err != nil {
		return 0, boundary("TASK_MEMORY_SCHEMA_COUNT_FAILED")
	}
	return row.Count, nil
}

func collectDatabaseReceipt(ctx context.Context, store *engramgorm.Store) (databaseReceipt, error) {
	if store == nil || store.GetDB() == nil {
		return databaseReceipt{}, boundary("DATABASE_UNAVAILABLE")
	}
	var serverVersion struct {
		Value string `gorm:"column:value"`
	}
	if err := store.GetDB().WithContext(ctx).Raw(`SELECT current_setting('server_version') AS value`).Scan(&serverVersion).Error; err != nil || serverVersion.Value == "" {
		return databaseReceipt{}, boundary("POSTGRES_VERSION_READ_FAILED")
	}
	var vectorVersion struct {
		Value string `gorm:"column:value"`
	}
	if err := store.GetDB().WithContext(ctx).Raw(`SELECT extversion AS value FROM pg_extension WHERE extname = 'vector'`).Scan(&vectorVersion).Error; err != nil || vectorVersion.Value == "" {
		return databaseReceipt{}, boundary("PGVECTOR_VERSION_READ_FAILED")
	}
	migrationState, err := store.GetMigrationState(ctx)
	if err != nil || migrationState == nil || migrationState.CurrentVersion == "" || len(migrationState.AppliedIDs) == 0 {
		return databaseReceipt{}, boundary("MIGRATION_STATE_READ_FAILED")
	}
	schemaProjection, err := publicSchemaProjectionFingerprint(ctx, store.GetDB())
	if err != nil {
		return databaseReceipt{}, err
	}
	return databaseReceipt{
		PostgreSQLVersion:       serverVersion.Value,
		PGVectorVersion:         vectorVersion.Value,
		MigrationCurrentVersion: migrationState.CurrentVersion,
		MigrationIDsSHA256:      hashSortedStrings(migrationState.AppliedIDs),
		SchemaProjectionSHA256:  schemaProjection,
	}, nil
}

type schemaProjectionRow struct {
	Value string `gorm:"column:value"`
}

func publicSchemaProjectionFingerprint(ctx context.Context, db *gormlib.DB) (string, error) {
	if db == nil {
		return "", boundary("DATABASE_UNAVAILABLE")
	}
	for _, table := range qualificationTables() {
		var row struct {
			Count int `gorm:"column:count"`
		}
		if err := db.WithContext(ctx).Raw(`
			SELECT COUNT(*) AS count
			FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = ?
		`, table).Scan(&row).Error; err != nil || row.Count != 1 {
			return "", boundary("REQUIRED_SCHEMA_MISSING")
		}
	}
	queries := []string{
		`SELECT concat_ws('|', 'column', table_name, ordinal_position::text, column_name, data_type, udt_name, is_nullable, COALESCE(column_default, ''), is_identity, is_generated) AS value
		 FROM information_schema.columns
		 WHERE table_schema = 'public'
		   AND table_name IN ('memories', 'content_chunks', 'projects', 'project_identifiers', 'project_resolution_attempts', 'project_identity_comparisons')
		 ORDER BY table_name ASC, ordinal_position ASC`,
		`SELECT concat_ws('|', 'constraint', relation.relname, constraint_record.conname, pg_get_constraintdef(constraint_record.oid, true)) AS value
		 FROM pg_constraint AS constraint_record
		 JOIN pg_class AS relation ON relation.oid = constraint_record.conrelid
		 JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		 WHERE namespace.nspname = 'public'
		   AND relation.relname IN ('memories', 'content_chunks', 'projects', 'project_identifiers', 'project_resolution_attempts', 'project_identity_comparisons')
		 ORDER BY relation.relname ASC, constraint_record.conname ASC`,
		`SELECT concat_ws('|', 'index', relation.relname, index_relation.relname, pg_get_indexdef(index_relation.oid)) AS value
		 FROM pg_index AS index_definition
		 JOIN pg_class AS relation ON relation.oid = index_definition.indrelid
		 JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		 JOIN pg_class AS index_relation ON index_relation.oid = index_definition.indexrelid
		 WHERE namespace.nspname = 'public'
		   AND relation.relname IN ('memories', 'content_chunks', 'projects', 'project_identifiers', 'project_resolution_attempts', 'project_identity_comparisons')
		 ORDER BY relation.relname ASC, index_relation.relname ASC`,
	}
	values := []string{"schema-projection/v1"}
	for _, query := range queries {
		var rows []schemaProjectionRow
		if err := db.WithContext(ctx).Raw(query).Scan(&rows).Error; err != nil {
			return "", boundary("SCHEMA_PROJECTION_READ_FAILED")
		}
		for _, row := range rows {
			if row.Value != "" {
				values = append(values, row.Value)
			}
		}
	}
	if len(values) == 1 {
		return "", boundary("SCHEMA_PROJECTION_EMPTY")
	}
	return hashSortedStrings(values), nil
}

func collectSourceReceipt(commit, tree string) (sourceReceipt, error) {
	root, err := sourceRoot()
	if err != nil {
		return sourceReceipt{}, err
	}
	taskMemory, err := hashSourceFile(filepath.Join(root, "internal", "taskmemory", "task_memory.go"))
	if err != nil {
		return sourceReceipt{}, err
	}
	candidateStore, err := hashSourceFile(filepath.Join(root, "internal", "db", "gorm", "task_memory_candidate_store.go"))
	if err != nil {
		return sourceReceipt{}, err
	}
	authorityAdapter, err := hashSourceFile(filepath.Join(root, "internal", "grpcserver", "task_memory_authority.go"))
	if err != nil {
		return sourceReceipt{}, err
	}
	rankfusion, err := hashSourceFile(filepath.Join(root, "internal", "rankfusion", "rrf.go"))
	if err != nil {
		return sourceReceipt{}, err
	}
	vectorDimension, err := hashSourceFile(filepath.Join(root, "internal", "vectordim", "dimension.go"))
	if err != nil {
		return sourceReceipt{}, err
	}
	return sourceReceipt{
		Commit:                  commit,
		Tree:                    tree,
		PreparationRevision:     taskmemory.PreparationRevision,
		TaskMemorySHA256:        taskMemory,
		CandidateStoreSHA256:    candidateStore,
		AuthorityAdapterSHA256:  authorityAdapter,
		RankfusionSHA256:        rankfusion,
		VectorDimensionSHA256:   vectorDimension,
		QueryProfileFingerprint: qualificationProfileFingerprint(),
	}, nil
}

func sourceRoot() (string, error) {
	starts := make([]string, 0, 2)
	if workingDirectory, err := os.Getwd(); err == nil {
		starts = append(starts, workingDirectory)
	}
	if _, sourceFile, _, ok := runtime.Caller(0); ok && sourceFile != "" {
		starts = append(starts, filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..")))
	}
	for _, start := range starts {
		for directory := filepath.Clean(start); ; directory = filepath.Dir(directory) {
			if info, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil && !info.IsDir() {
				return directory, nil
			}
			parent := filepath.Dir(directory)
			if parent == directory {
				break
			}
		}
	}
	return "", boundary("SOURCE_ROOT_UNAVAILABLE")
}

func hashSourceFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", boundary("SOURCE_HASH_FAILED")
	}
	return sha256Hex(content), nil
}

func qualificationProfileFingerprint() string {
	type profile struct {
		Name               string            `json:"name"`
		QuerySHA256        string            `json:"query_sha256"`
		ExpectedMode       string            `json:"expected_mode"`
		ExpectedReferences []referenceTriple `json:"expected_references"`
	}
	profiles := make([]profile, 0, len(qualificationScenarios))
	for _, scenario := range qualificationScenarios {
		profiles = append(profiles, profile{
			Name:               scenario.Name,
			QuerySHA256:        sha256Hex([]byte(scenario.Query)),
			ExpectedMode:       scenario.ExpectedMode,
			ExpectedReferences: cloneReferenceTriples(scenario.ExpectedReferences),
		})
	}
	encoded, _ := json.Marshal(struct {
		ProfileVersion  string    `json:"profile_version"`
		VectorDimension int       `json:"vector_dimension"`
		MaxCandidates   int       `json:"max_candidates"`
		Scenarios       []profile `json:"scenarios"`
	}{
		ProfileVersion:  "hap02a-tm05-profile/v1",
		VectorDimension: vectordim.Dimension,
		MaxCandidates:   taskmemory.MaxPreparedCandidates,
		Scenarios:       profiles,
	})
	return sha256Hex(encoded)
}

func hashSortedStrings(values []string) string {
	ordered := append([]string(nil), values...)
	sort.Strings(ordered)
	return sha256Hex([]byte(strings.Join(ordered, "\n")))
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
