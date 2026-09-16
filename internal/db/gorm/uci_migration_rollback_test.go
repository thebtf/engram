package gorm

import (
	"bytes"
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	ucidomain "github.com/thebtf/engram/internal/uci"
	gormlib "gorm.io/gorm"
)

var uciMigrationRollbackMigrationIDs = [...]string{
	uciContextRegistryMigrationID,
	uciProjectionMigrationID,
	uciPublicationMigrationID,
	"174_uci_embedding_jobs",
	"175_uci_reference_source_text",
}

type uciMigrationRollbackStep struct {
	migrationID string
	rollback    func(*gormlib.DB) error
}

var uciMigrationRollbackSteps = [...]uciMigrationRollbackStep{
	{
		migrationID: "175_uci_reference_source_text",
		rollback:    rollbackUCIReferenceSourceTextMigration175,
	},
	{
		migrationID: "174_uci_embedding_jobs",
		rollback:    rollbackUCIEmbeddingJobsMigration174,
	},
	{
		migrationID: uciPublicationMigrationID,
		rollback:    rollbackUCIFencedPublicationMigration173,
	},
	{
		migrationID: uciProjectionMigrationID,
		rollback:    rollbackUCIIndexProjectionMigration172,
	},
	{
		migrationID: uciContextRegistryMigrationID,
		rollback:    rollbackUCIContextRegistryMigration171,
	},
}

var uciMigrationRollbackSnapshotTables = [...]string{
	"spaces",
	"sources",
	"space_sources",
	"legacy_context_aliases",
	"ci_profiles",
	"ci_checkouts",
	"ci_views",
	"ci_blobs",
	"ci_parse_artifacts",
	"ci_definitions",
	"ci_reference_sites",
	"ci_chunks",
	"ci_memberships",
	"ci_resolved_edges",
	"ci_embedding_profiles",
	"ci_embeddings",
	"ci_chunk_embeddings",
	"ci_jobs",
	"ci_index_build_parts",
	"ci_analyses",
	"uci_exposures",
	"uci_completion_evidence",
	"projects",
	"code_chunks",
	"issues",
	"credentials",
	"task_memory_intervention_receipts",
	"migrations",
}

type uciMigrationRollbackFixture struct {
	db              *gormlib.DB
	legacyProjectID string
	legacyChunk     CodeChunk
	issueID         int64
	credentialID    int64
	receiptID       string
	referenceID     string
}

type uciMigrationRollbackSchemaEntry struct {
	Kind       string `gorm:"column:kind"`
	TableName  string `gorm:"column:table_name"`
	Name       string `gorm:"column:name"`
	Definition string `gorm:"column:definition"`
}

type uciMigrationRollbackLegacySentinel struct {
	GitRemote      string `gorm:"column:git_remote"`
	RelativePath   string `gorm:"column:relative_path"`
	DisplayName    string `gorm:"column:display_name"`
	FilePath       string `gorm:"column:file_path"`
	ByteStart      int64  `gorm:"column:byte_start"`
	ByteEnd        int64  `gorm:"column:byte_end"`
	Content        string `gorm:"column:content"`
	ContentSHA256  string `gorm:"column:content_sha256"`
	IndexSessionID string `gorm:"column:index_session_id"`
}

type uciMigrationRollbackProductSentinel struct {
	Title            string `gorm:"column:title"`
	Body             string `gorm:"column:body"`
	Status           string `gorm:"column:status"`
	Priority         string `gorm:"column:priority"`
	Type             string `gorm:"column:type"`
	SourceProject    string `gorm:"column:source_project"`
	TargetProject    string `gorm:"column:target_project"`
	SourceAgent      string `gorm:"column:source_agent"`
	CreatedBySession string `gorm:"column:created_by_session"`
	CreatorKeycardID string `gorm:"column:creator_keycard_id"`
}

type uciMigrationRollbackCryptoSentinel struct {
	Ciphertext  []byte `gorm:"column:encrypted_secret"`
	Fingerprint string `gorm:"column:encryption_key_fingerprint"`
	Scope       string `gorm:"column:scope"`
	EditedBy    string `gorm:"column:edited_by"`
	Version     int    `gorm:"column:version"`
}

type uciMigrationRollbackReceiptSentinel struct {
	KeyEpochCommitment           []byte `gorm:"column:key_epoch_commitment"`
	IntegrityDigest              []byte `gorm:"column:integrity_digest"`
	ChannelKey                   []byte `gorm:"column:channel_key"`
	SessionKey                   []byte `gorm:"column:session_key"`
	OccurrenceKey                []byte `gorm:"column:occurrence_key"`
	ContentCommitment            []byte `gorm:"column:content_commitment"`
	CapabilitySnapshotCommitment []byte `gorm:"column:capability_snapshot_commitment"`
}

type uciMigrationRollbackSnapshot struct {
	schema    []uciMigrationRollbackSchemaEntry
	counts    map[string]int64
	rows      map[string]string
	history   []string
	legacy    uciMigrationRollbackLegacySentinel
	product   uciMigrationRollbackProductSentinel
	crypto    uciMigrationRollbackCryptoSentinel
	receipt   uciMigrationRollbackReceiptSentinel
	reference string
}

// TestUCIMigrationRollback175Through171RetainsSchemaDataAndHistory exercises the
// expand-only rollback representation: all five UCI migration callbacks retain the
// complete durable state, and marker-only replay is idempotent in a private schema.
func TestUCIMigrationRollback175Through171RetainsSchemaDataAndHistory(t *testing.T) {
	fixture := openUCIMigrationRollbackFixture(t)
	before := uciMigrationRollbackSnapshotFor(t, fixture)

	require.Equal(t, []string{
		"171_uci_context_registry",
		"172_uci_index_projection",
		"173_uci_fenced_publication",
		"174_uci_embedding_jobs",
		"175_uci_reference_source_text",
	}, uciMigrationRollbackMigrationIDs[:], "the rollback contract owns exactly migrations 171 through 175")
	require.Len(t, uciMigrationRollbackSteps, len(uciMigrationRollbackMigrationIDs))
	for index, step := range uciMigrationRollbackSteps {
		require.Equal(t, uciMigrationRollbackMigrationIDs[len(uciMigrationRollbackMigrationIDs)-1-index], step.migrationID, "rollback helpers must run from 175 down through 171")
	}
	uciMigrationRollbackRequireSeededRows(t, before)
	uciMigrationRollbackRequireHistory(t, before.history)
	require.NoError(t, NewUCIExposureStore(fixture.db).VerifyIntegrity(context.Background()), "the seeded UCI evidence must satisfy the backup/restore integrity verifier")

	for _, step := range uciMigrationRollbackSteps {
		require.NoError(t, step.rollback(fixture.db), "expand-only rollback %s must retain durable UCI state", step.migrationID)
		afterRollback := uciMigrationRollbackSnapshotFor(t, fixture)
		uciMigrationRollbackRequireStable(t, before, afterRollback, step.migrationID)
		require.NoError(t, NewUCIExposureStore(fixture.db).VerifyIntegrity(context.Background()), "rollback %s must retain verifiable UCI evidence", step.migrationID)
	}

	for _, migrationID := range uciMigrationRollbackMigrationIDs {
		require.NoError(t, fixture.db.Exec("DELETE FROM migrations WHERE id = ?", migrationID).Error, "only the isolated migration marker may be removed for %s", migrationID)
	}
	var removedMarkers int64
	require.NoError(t, fixture.db.Raw(`
		SELECT COUNT(*)
		FROM migrations
		WHERE id IN (?, ?, ?, ?, ?)
	`,
		uciMigrationRollbackMigrationIDs[0],
		uciMigrationRollbackMigrationIDs[1],
		uciMigrationRollbackMigrationIDs[2],
		uciMigrationRollbackMigrationIDs[3],
		uciMigrationRollbackMigrationIDs[4],
	).Scan(&removedMarkers).Error)
	require.Zero(t, removedMarkers, "marker-only replay must not delete UCI, product, or crypto rows")

	require.NoError(t, runMigrations(fixture.db), "the complete chain must reapply after isolated marker removal")
	afterReapply := uciMigrationRollbackSnapshotFor(t, fixture)
	uciMigrationRollbackRequireStable(t, before, afterReapply, "marker-only reapply")
	require.NoError(t, NewUCIExposureStore(fixture.db).VerifyIntegrity(context.Background()), "reapplied schema must retain backup/restore-verifiable UCI evidence")
}

func openUCIMigrationRollbackFixture(t *testing.T) *uciMigrationRollbackFixture {
	t.Helper()

	ctx := context.Background()
	embedding := openUCIEmbeddingJobsFixture(t)
	publication := embedding.publication
	db := publication.db
	token := uuid.NewString()

	legacyProjectID := "uci-migration-rollback-project-" + token
	require.NoError(t, db.Exec(`
		INSERT INTO projects (id, git_remote, relative_path, display_name)
		VALUES (?, ?, ?, ?)
	`, legacyProjectID, "https://example.invalid/uci/"+token, "uci/rollback/"+token, "UCI rollback "+token).Error)
	legacyChunk := CodeChunk{
		ProjectID:      legacyProjectID,
		FilePath:       "uci/rollback/" + token + ".go",
		ByteStart:      0,
		ByteEnd:        32,
		Language:       "go",
		ChunkType:      "function",
		Content:        "package rollback\nfunc Sentinel() {}\n",
		ContentSHA256:  "sha256-rollback-" + token,
		IndexSessionID: "uci-migration-rollback-" + token,
	}
	require.NoError(t, NewCodeChunkStore(db).Upsert(ctx, &legacyChunk))

	issue := Issue{
		Title:            "migration rollback product sentinel " + token,
		Body:             "product-domain fixture",
		Status:           "open",
		Priority:         "high",
		Type:             "feature",
		SourceProject:    "source-product-" + token,
		TargetProject:    "target-product-" + token,
		SourceAgent:      "migration-rollback-fixture",
		CreatedBySession: "migration-rollback-session-" + token,
		CreatorKeycardID: "migration-rollback-keycard-" + token,
	}
	require.NoError(t, db.Create(&issue).Error)

	credential := Credential{
		Project:                  "crypto-product-" + token,
		Key:                      "migration-rollback-sentinel",
		EncryptedSecret:          []byte{0x00, 0x01, 0x02, 0xff, 0x10, 0x20},
		EncryptionKeyFingerprint: uciPublicationDigest("migration-rollback-crypto-" + token),
		Scope:                    "project",
		EditedBy:                 "migration-rollback-fixture",
		Version:                  1,
	}
	require.NoError(t, db.Create(&credential).Error)

	receipt := newInterventionReceiptFixture()
	require.NoError(t, receipt.insert(db))

	referenceArtifact := publication.insertArtifact(t, publication.source.SourceID, "rollback-reference", "func RollbackReference() {}\n", UCIParseArtifactComplete)
	reference, err := publication.projection.UpsertReferenceSite(ctx, UpsertUCIReferenceSiteInput{
		ReferenceSiteID: uuid.NewString(),
		ArtifactID:      referenceArtifact.Artifact.ArtifactID,
		SiteKey:         "rollback-multiline-reference-" + publication.token,
		RawTarget:       "source\n  .RollbackReference",
		Relation:        "calls",
		SyntaxSpan:      `{"byte_start":15,"byte_end":41,"line_start":1,"line_end":2}`,
		ResolverHints:   `{"kind":"call"}`,
	})
	require.NoError(t, err)

	embeddingArtifact := publication.admitArtifact(t, publication.source.SourceID, "rollback-embedding", "func RollbackEmbedding() {}\n", UCIParseArtifactComplete)
	published := embedding.publish(t, "rollback-embedding", publication.checkout, nil, ucidomain.IndexJobInitial,
		[]uciPublicationArtifact{embeddingArtifact},
		[]ucidomain.IndexMembership{uciPublicationPresentMembership("rollback.go", embeddingArtifact)},
	)
	authorized := uciEmbeddingJobsAuthorize(t, publication, published.Context)
	claim, claimed, err := embedding.store.ClaimEmbeddingJob(ctx, embedding.profile, "migration-rollback-embedding-worker-"+publication.token, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed, "the publication must leave a durable embedding obligation")
	batch, err := embedding.store.PrepareEmbeddingBatch(ctx, claim, authorized, 16)
	require.NoError(t, err)
	require.NotEmpty(t, batch.Candidates, "the published fixture must expose an embedding candidate")
	require.NotEmpty(t, batch.MissingInputIndexes, "the published fixture must require a persisted vector")
	vectors := make([][]float32, len(batch.MissingInputIndexes))
	for vectorIndex := range batch.MissingInputIndexes {
		vectors[vectorIndex] = uciEmbeddingJobsVector(float32(vectorIndex + 1))
	}
	require.NoError(t, embedding.store.CommitEmbeddingBatch(ctx, claim, authorized, batch, vectors))

	var publishedView UCIView
	require.NoError(t, db.Where("view_id = ?", published.Context.ViewID).First(&publishedView).Error)
	evidenceFixture := &uciProjectionMigrationFixture{
		db:        db,
		authRealm: publication.realm,
		source:    publication.source,
		checkout:  publication.checkout,
		view:      &publishedView,
	}
	evidenceStore := NewUCIExposureStore(db)
	exposure, err := evidenceStore.AppendExposure(ctx, uciProjectionExposureRecord(t, evidenceFixture, time.Now().UTC().Truncate(time.Microsecond)))
	require.NoError(t, err)
	_, err = evidenceStore.AppendCompletion(ctx, uciProjectionCompletionEvidence(t, exposure.ExposureRef, time.Now().UTC().Truncate(time.Microsecond)))
	require.NoError(t, err)
	require.NoError(t, evidenceStore.VerifyIntegrity(ctx))

	return &uciMigrationRollbackFixture{
		db:              db,
		legacyProjectID: legacyProjectID,
		legacyChunk:     legacyChunk,
		issueID:         issue.ID,
		credentialID:    credential.ID,
		receiptID:       receipt.ReceiptID,
		referenceID:     reference.ReferenceSiteID,
	}
}

func uciMigrationRollbackSnapshotFor(t *testing.T, fixture *uciMigrationRollbackFixture) uciMigrationRollbackSnapshot {
	t.Helper()

	snapshot := uciMigrationRollbackSnapshot{
		schema: uciMigrationRollbackSchema(t, fixture.db),
		counts: make(map[string]int64, len(uciMigrationRollbackSnapshotTables)),
		rows:   make(map[string]string, len(uciMigrationRollbackSnapshotTables)),
	}
	for _, tableName := range uciMigrationRollbackSnapshotTables {
		var count int64
		require.NoError(t, fixture.db.Table(tableName).Count(&count).Error)
		snapshot.counts[tableName] = count
		snapshot.rows[tableName] = uciMigrationRollbackTableRows(t, fixture.db, tableName)
	}

	var markerRows []uciAppliedMigration
	require.NoError(t, fixture.db.Raw(`
		SELECT id
		FROM migrations
		WHERE id IN (?, ?, ?, ?, ?)
		ORDER BY id ASC
	`,
		uciMigrationRollbackMigrationIDs[0],
		uciMigrationRollbackMigrationIDs[1],
		uciMigrationRollbackMigrationIDs[2],
		uciMigrationRollbackMigrationIDs[3],
		uciMigrationRollbackMigrationIDs[4],
	).Scan(&markerRows).Error)
	snapshot.history = make([]string, 0, len(markerRows))
	for _, marker := range markerRows {
		snapshot.history = append(snapshot.history, marker.ID)
	}

	require.NoError(t, fixture.db.Raw(`
		SELECT
			project.git_remote,
			project.relative_path,
			project.display_name,
			chunk.file_path,
			chunk.byte_start,
			chunk.byte_end,
			chunk.content,
			chunk.content_sha256,
			chunk.index_session_id
		FROM projects AS project
		JOIN code_chunks AS chunk ON chunk.project_id = project.id
		WHERE project.id = ?
			AND chunk.file_path = ?
			AND chunk.byte_start = ?
			AND chunk.content_sha256 = ?
	`, fixture.legacyProjectID, fixture.legacyChunk.FilePath, fixture.legacyChunk.ByteStart, fixture.legacyChunk.ContentSHA256).Scan(&snapshot.legacy).Error)
	require.NotEmpty(t, snapshot.legacy.Content, "the legacy code-chunk sentinel must remain queryable")

	require.NoError(t, fixture.db.Raw(`
		SELECT title, body, status, priority, type, source_project, target_project, source_agent, created_by_session, creator_keycard_id
		FROM issues
		WHERE id = ?
	`, fixture.issueID).Scan(&snapshot.product).Error)
	require.NotEmpty(t, snapshot.product.Title, "the product-domain sentinel must remain queryable")

	require.NoError(t, fixture.db.Raw(`
		SELECT encrypted_secret, encryption_key_fingerprint, scope, edited_by, version
		FROM credentials
		WHERE id = ?
	`, fixture.credentialID).Scan(&snapshot.crypto).Error)
	require.NotEmpty(t, snapshot.crypto.Ciphertext, "the crypto-bound sentinel must retain opaque ciphertext bytes")

	require.NoError(t, fixture.db.Raw(`
		SELECT
			key_epoch_commitment,
			integrity_digest,
			channel_key,
			session_key,
			occurrence_key,
			content_commitment,
			capability_snapshot_commitment
		FROM task_memory_intervention_receipts
		WHERE receipt_id = ?
	`, fixture.receiptID).Scan(&snapshot.receipt).Error)
	require.NotEmpty(t, snapshot.receipt.KeyEpochCommitment, "the receipt crypto sentinel must remain queryable")

	require.NoError(t, fixture.db.Raw(`SELECT raw_target FROM ci_reference_sites WHERE reference_site_id = ?`, fixture.referenceID).Scan(&snapshot.reference).Error)
	require.Equal(t, "source\n  .RollbackReference", snapshot.reference, "migration 175 fixture must retain multiline source text")

	return snapshot
}

func uciMigrationRollbackRequireSeededRows(t *testing.T, snapshot uciMigrationRollbackSnapshot) {
	t.Helper()
	for _, tableName := range []string{
		"sources",
		"ci_checkouts",
		"ci_profiles",
		"ci_views",
		"ci_blobs",
		"ci_parse_artifacts",
		"ci_definitions",
		"ci_reference_sites",
		"ci_chunks",
		"ci_memberships",
		"ci_jobs",
		"ci_index_build_parts",
		"ci_embedding_profiles",
		"ci_embeddings",
		"ci_chunk_embeddings",
		"uci_exposures",
		"uci_completion_evidence",
		"projects",
		"code_chunks",
		"issues",
		"credentials",
		"task_memory_intervention_receipts",
	} {
		require.Positive(t, snapshot.counts[tableName], "fixture must seed representative rows in %s", tableName)
	}
}

func uciMigrationRollbackRequireHistory(t *testing.T, history []string) {
	t.Helper()
	require.True(t, reflect.DeepEqual(uciMigrationRollbackMigrationIDs[:], history), "migrations 171 through 175 must remain recorded exactly once")
}

func uciMigrationRollbackRequireStable(t *testing.T, before, after uciMigrationRollbackSnapshot, phase string) {
	t.Helper()
	require.True(t, reflect.DeepEqual(before.schema, after.schema), "%s must not contract or rewrite the UCI/product schema", phase)
	require.True(t, reflect.DeepEqual(before.counts, after.counts), "%s must not delete or add fixture rows", phase)
	require.True(t, reflect.DeepEqual(before.rows, after.rows), "%s must retain exact representative context, projection, publication, embedding, reference, and evidence rows", phase)
	require.True(t, reflect.DeepEqual(before.history, after.history), "%s must retain migration history", phase)
	require.True(t, before.legacy == after.legacy, "%s must not rewrite legacy projects or code_chunks", phase)
	require.True(t, before.product == after.product, "%s must not rewrite product-domain rows", phase)
	require.True(t, before.reference == after.reference, "%s must preserve multiline reference source text", phase)
	uciMigrationRollbackRequireCryptoStable(t, before.crypto, after.crypto, phase)
	uciMigrationRollbackRequireReceiptStable(t, before.receipt, after.receipt, phase)
}

func uciMigrationRollbackRequireCryptoStable(t *testing.T, before, after uciMigrationRollbackCryptoSentinel, phase string) {
	t.Helper()
	require.True(t, bytes.Equal(before.Ciphertext, after.Ciphertext), "%s must preserve ciphertext bytes without decrypting them", phase)
	require.True(t, before.Fingerprint == after.Fingerprint, "%s must preserve the encryption-key fingerprint", phase)
	require.True(t, before.Scope == after.Scope, "%s must preserve credential scope", phase)
	require.True(t, before.EditedBy == after.EditedBy, "%s must preserve credential provenance", phase)
	require.True(t, before.Version == after.Version, "%s must preserve credential version", phase)
}

func uciMigrationRollbackRequireReceiptStable(t *testing.T, before, after uciMigrationRollbackReceiptSentinel, phase string) {
	t.Helper()
	for _, value := range []struct {
		name   string
		before []byte
		after  []byte
	}{
		{name: "key epoch commitment", before: before.KeyEpochCommitment, after: after.KeyEpochCommitment},
		{name: "integrity digest", before: before.IntegrityDigest, after: after.IntegrityDigest},
		{name: "channel key", before: before.ChannelKey, after: after.ChannelKey},
		{name: "session key", before: before.SessionKey, after: after.SessionKey},
		{name: "occurrence key", before: before.OccurrenceKey, after: after.OccurrenceKey},
		{name: "content commitment", before: before.ContentCommitment, after: after.ContentCommitment},
		{name: "capability snapshot commitment", before: before.CapabilitySnapshotCommitment, after: after.CapabilitySnapshotCommitment},
	} {
		require.True(t, bytes.Equal(value.before, value.after), "%s must preserve receipt %s bytes", phase, value.name)
	}
}

func uciMigrationRollbackTableRows(t *testing.T, db *gormlib.DB, tableName string) string {
	t.Helper()

	var rows string
	require.NoError(t, db.Raw(`
		SELECT COALESCE(
			string_agg(row_to_json(snapshot_row)::text, E'\n' ORDER BY row_to_json(snapshot_row)::text),
			''
		)
		FROM `+tableName+` AS snapshot_row
	`).Scan(&rows).Error)
	return rows
}

func uciMigrationRollbackSchema(t *testing.T, db *gormlib.DB) []uciMigrationRollbackSchemaEntry {
	t.Helper()

	entries := make([]uciMigrationRollbackSchemaEntry, 0)
	load := func(query string) {
		var batch []uciMigrationRollbackSchemaEntry
		require.NoError(t, db.Raw(query).Scan(&batch).Error)
		entries = append(entries, batch...)
	}

	load(`
		SELECT
			'column' AS kind,
			table_name,
			column_name AS name,
			COALESCE(data_type, '') || E'\x1f' ||
			COALESCE(udt_name, '') || E'\x1f' ||
			COALESCE(is_nullable, '') || E'\x1f' ||
			COALESCE(column_default, '') || E'\x1f' ||
			COALESCE(character_maximum_length::text, '') || E'\x1f' ||
			COALESCE(numeric_precision::text, '') || E'\x1f' ||
			COALESCE(numeric_scale::text, '') || E'\x1f' ||
			COALESCE(datetime_precision::text, '') || E'\x1f' ||
			COALESCE(collation_name, '') AS definition
		FROM information_schema.columns
		WHERE table_schema = current_schema()
			AND ` + uciMigrationRollbackSchemaTablePredicate("table_name"))
	load(`
		SELECT
			'constraint' AS kind,
			relation.relname AS table_name,
			constraint_row.conname AS name,
			pg_get_constraintdef(constraint_row.oid, true) AS definition
		FROM pg_constraint AS constraint_row
		JOIN pg_class AS relation ON relation.oid = constraint_row.conrelid
		JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = current_schema()
			AND ` + uciMigrationRollbackSchemaTablePredicate("relation.relname"))
	load(`
		SELECT
			'index' AS kind,
			tablename AS table_name,
			indexname AS name,
			indexdef AS definition
		FROM pg_indexes
		WHERE schemaname = current_schema()
			AND ` + uciMigrationRollbackSchemaTablePredicate("tablename"))
	load(`
		SELECT
			'trigger' AS kind,
			relation.relname AS table_name,
			trigger_row.tgname AS name,
			pg_get_triggerdef(trigger_row.oid, true) AS definition
		FROM pg_trigger AS trigger_row
		JOIN pg_class AS relation ON relation.oid = trigger_row.tgrelid
		JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = current_schema()
			AND NOT trigger_row.tgisinternal
			AND ` + uciMigrationRollbackSchemaTablePredicate("relation.relname"))
	load(`
		SELECT
			'function' AS kind,
			'' AS table_name,
			procedure.proname || '(' || pg_get_function_identity_arguments(procedure.oid) || ')' AS name,
			pg_get_functiondef(procedure.oid) AS definition
		FROM pg_proc AS procedure
		JOIN pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
		WHERE namespace.nspname = current_schema()
			AND procedure.proname LIKE 'uci\_%' ESCAPE '\'
	`)

	sort.Slice(entries, func(left, right int) bool {
		if entries[left].Kind != entries[right].Kind {
			return entries[left].Kind < entries[right].Kind
		}
		if entries[left].TableName != entries[right].TableName {
			return entries[left].TableName < entries[right].TableName
		}
		return entries[left].Name < entries[right].Name
	})
	return entries
}

func uciMigrationRollbackSchemaTablePredicate(column string) string {
	return strings.ReplaceAll(`(
		__TABLE__ LIKE 'ci\_%' ESCAPE '\'
		OR __TABLE__ IN (
			'spaces',
			'sources',
			'space_sources',
			'legacy_context_aliases',
			'uci_exposures',
			'uci_completion_evidence',
			'projects',
			'code_chunks',
			'issues',
			'credentials',
			'task_memory_intervention_receipts'
		)
	)`, "__TABLE__", column)
}
