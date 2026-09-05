package gorm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"
)

const uciProjectionMigrationID = "172_uci_index_projection"

var uciProjectionMigrationTableNames = [...]string{
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
	"ci_analyses",
	"uci_exposures",
	"uci_completion_evidence",
}

type uciProjectionMigrationFixture struct {
	db            *gormlib.DB
	authRealm     string
	source        *UCISource
	checkout      *UCICheckout
	profile       *UCIAnalysisProfile
	otherCheckout *UCICheckout
	view          *UCIView
	otherView     *UCIView
	stagingView   *UCIView
	projectID     string
	chunk         CodeChunk
	projectBefore uciProjectionProjectSentinel
	chunkBefore   uciProjectionCodeChunkSentinel
}

type uciProjectionProjectSentinel struct {
	GitRemote    string `gorm:"column:git_remote"`
	RelativePath string `gorm:"column:relative_path"`
	DisplayName  string `gorm:"column:display_name"`
}

type uciProjectionCodeChunkSentinel struct {
	Content        string `gorm:"column:content"`
	IndexSessionID string `gorm:"column:index_session_id"`
}

type uciProjectionMigrationColumn struct {
	Name     string `gorm:"column:column_name"`
	DataType string `gorm:"column:data_type"`
	Nullable string `gorm:"column:is_nullable"`
}

type uciProjectionMigrationConstraint struct {
	Name       string `gorm:"column:conname"`
	Definition string `gorm:"column:definition"`
}

type uciProjectionMigrationIndex struct {
	Name       string `gorm:"column:indexname"`
	Definition string `gorm:"column:indexdef"`
}

type uciProjectionMigrationTrigger struct {
	Name       string `gorm:"column:tgname"`
	Definition string `gorm:"column:definition"`
}

func TestUCIProjectionMigration172RegistersAndPreservesLegacySentinels(t *testing.T) {
	fixture := openUCIProjectionMigrationFixture(t)

	assertUCIProjectionMigrationTableSet(t, fixture.db)
	assertUCIProjectionLegacySentinels(t, fixture)
}

func TestUCIProjectionMigration172ProjectionSchemaEnforcesScopedTemporalIndexes(t *testing.T) {
	fixture := openUCIProjectionMigrationFixture(t)
	db := fixture.db

	assertUCIProjectionMigrationTableSet(t, db)
	for tableName, columns := range map[string][]string{
		"ci_blobs": {
			"blob_id", "source_id", "protection_domain", "content_digest", "byte_length", "safe_content", "encoding", "storage_state",
		},
		"ci_parse_artifacts": {
			"artifact_id", "source_id", "blob_id", "language", "parser_revision", "grammar_digest", "extraction_profile_digest", "status", "diagnostics",
		},
		"ci_definitions": {
			"artifact_id", "local_symbol_key", "kind", "name", "qualified_local_name", "signature", "byte_start", "byte_end", "line_start", "line_end",
		},
		"ci_reference_sites": {
			"artifact_id", "site_key", "owner_symbol_key", "raw_target", "relation", "syntax_span", "resolver_hints",
		},
		"ci_chunks": {
			"chunk_id", "artifact_id", "symbol_key", "chunk_kind", "ordinal", "byte_start", "byte_end", "content_digest", "text_for_search", "content_tsv",
		},
		"ci_memberships": {
			"checkout_id", "path_key", "display_path", "artifact_id", "file_state", "mode", "valid_from_generation", "valid_to_generation",
		},
		"ci_resolved_edges": {
			"checkout_id", "edge_key", "source_path", "source_artifact", "source_symbol", "target_path", "target_artifact", "target_symbol", "relation", "evidence_kind", "resolver_revision", "evidence_json", "resolution_state", "valid_from_generation", "valid_to_generation",
		},
		"ci_embedding_profiles": {
			"embedding_profile_id", "provider_ref", "model", "dimension", "preprocessing_revision", "include_relative_path",
		},
		"ci_embeddings": {
			"embedding_profile_id", "embedding_input_digest", "vector", "source_id", "protection_domain", "completion_seq", "status",
		},
		"ci_chunk_embeddings": {
			"chunk_id", "relative_path_fingerprint", "embedding_profile_id", "embedding_input_digest",
		},
		"ci_jobs": {
			"job_id", "source_id", "checkout_id", "job_kind", "input_fingerprint", "owner_epoch", "target_generation", "state", "attempt", "retry_after", "lease_owner", "lease_expiry", "error_code", "counts",
		},
		"ci_analyses": {
			"analysis_id", "view_id", "kind", "algorithm_revision", "input_digest", "artifact_refs", "result_json", "state",
		},
	} {
		assertUCIProjectionRequiredColumns(t, db, tableName, columns...)
	}

	assertUCIProjectionColumn(t, db, "ci_blobs", "blob_id", "uuid", "NO")
	assertUCIProjectionColumn(t, db, "ci_blobs", "source_id", "uuid", "NO")
	assertUCIProjectionColumn(t, db, "ci_memberships", "checkout_id", "uuid", "NO")
	assertUCIProjectionColumn(t, db, "ci_memberships", "valid_from_generation", "bigint", "NO")
	assertUCIProjectionColumn(t, db, "ci_memberships", "valid_to_generation", "bigint", "YES")
	assertUCIProjectionColumn(t, db, "ci_embedding_profiles", "embedding_profile_id", "uuid", "NO")
	assertUCIProjectionColumn(t, db, "ci_embedding_profiles", "dimension", "integer", "NO")
	assertUCIProjectionColumn(t, db, "ci_embeddings", "embedding_profile_id", "uuid", "NO")
	assertUCIProjectionColumn(t, db, "ci_embeddings", "source_id", "uuid", "NO")
	assertUCIProjectionVector1536(t, db)
	assertUCIProjectionColumn(t, db, "ci_jobs", "job_id", "uuid", "NO")
	assertUCIProjectionColumn(t, db, "ci_jobs", "source_id", "uuid", "NO")
	assertUCIProjectionColumn(t, db, "ci_jobs", "checkout_id", "uuid", "YES")
	require.Error(t, db.Exec(`UPDATE ci_checkouts SET current_view_id = ? WHERE checkout_id = ?`, fixture.stagingView.ViewID, fixture.checkout.CheckoutID).Error, "migration 172 must reject a staging view as the current view")
	current, err := NewUCIContextStore(db).GetCurrentView(context.Background(), fixture.checkout.CheckoutID)
	require.NoError(t, err)
	require.Equal(t, fixture.view.ViewID, current.ViewID, "rejected staging pointer must preserve the published current view")

	assertUCIProjectionConstraint(t, db, "ci_blobs", "unique", "source_id", "protection_domain", "content_digest")
	assertUCIProjectionConstraint(t, db, "ci_parse_artifacts", "unique", "source_id", "blob_id", "language", "parser_revision", "grammar_digest", "extraction_profile_digest")
	assertUCIProjectionConstraint(t, db, "ci_embeddings", "unique", "embedding_profile_id", "embedding_input_digest", "source_id", "protection_domain")
	assertUCIProjectionConstraint(t, db, "ci_chunk_embeddings", "unique", "chunk_id", "relative_path_fingerprint", "embedding_profile_id", "embedding_input_digest")
	assertUCIProjectionConstraint(t, db, "ci_memberships", "check", "valid_from_generation >= 1")
	assertUCIProjectionConstraint(t, db, "ci_memberships", "check", "valid_to_generation > valid_from_generation")
	assertUCIProjectionConstraint(t, db, "ci_resolved_edges", "check", "valid_from_generation >= 1")
	assertUCIProjectionConstraint(t, db, "ci_resolved_edges", "check", "valid_to_generation > valid_from_generation")

	assertUCIProjectionForeignKey(t, db, "ci_blobs", "sources", "source_id")
	assertUCIProjectionForeignKey(t, db, "ci_parse_artifacts", "ci_blobs", "source_id", "blob_id")
	assertUCIProjectionForeignKey(t, db, "ci_definitions", "ci_parse_artifacts", "artifact_id")
	assertUCIProjectionForeignKey(t, db, "ci_reference_sites", "ci_parse_artifacts", "artifact_id")
	assertUCIProjectionForeignKey(t, db, "ci_chunks", "ci_parse_artifacts", "artifact_id")
	assertUCIProjectionForeignKey(t, db, "ci_memberships", "ci_checkouts", "checkout_id")
	assertUCIProjectionForeignKey(t, db, "ci_memberships", "ci_parse_artifacts", "artifact_id")
	assertUCIProjectionForeignKey(t, db, "ci_resolved_edges", "ci_checkouts", "checkout_id")
	assertUCIProjectionForeignKey(t, db, "ci_embeddings", "ci_embedding_profiles", "embedding_profile_id")
	assertUCIProjectionForeignKey(t, db, "ci_embeddings", "sources", "source_id")
	assertUCIProjectionForeignKey(t, db, "ci_chunk_embeddings", "ci_chunks", "chunk_id")
	assertUCIProjectionForeignKey(t, db, "ci_chunk_embeddings", "ci_embedding_profiles", "embedding_profile_id")
	assertUCIProjectionForeignKey(t, db, "ci_jobs", "ci_checkouts", "source_id", "checkout_id")
	assertUCIProjectionForeignKey(t, db, "ci_analyses", "ci_views", "view_id")

	assertUCIProjectionIndex(t, db, "ci_memberships", "checkout_id", "path_key", "valid_from_generation")
	assertUCIProjectionIndex(t, db, "ci_memberships", "unique", "checkout_id", "path_key", "where", "valid_to_generation is null")
	assertUCIProjectionIndex(t, db, "ci_resolved_edges", "checkout_id", "source_artifact", "valid_from_generation")
	assertUCIProjectionIndex(t, db, "ci_resolved_edges", "checkout_id", "target_artifact", "valid_from_generation")
	assertUCIProjectionIndex(t, db, "ci_chunks", "using gin", "content_tsv")
	assertUCIProjectionIndex(t, db, "ci_definitions", "qualified_local_name")
	assertUCIProjectionIndex(t, db, "ci_jobs", "state", "retry_after")
}

func TestUCIProjectionMigration172ProjectionStoreKeepsScopeAndCallerOwnership(t *testing.T) {
	fixture := openUCIProjectionMigrationFixture(t)
	store := NewUCIProjectionStore(fixture.db)
	ctx := context.Background()

	safeContent := []byte("package fixture\n")
	blobInput := UpsertUCIBlobInput{
		SourceID:         fixture.source.SourceID,
		ProtectionDomain: "source-private",
		ContentDigest:    uciProjectionDigest("1"),
		ByteLength:       int64(len(safeContent)),
		SafeContent:      safeContent,
		Encoding:         "utf-8",
		StorageState:     UCIBlobStored,
	}
	blob, err := store.UpsertBlob(ctx, blobInput)
	require.NoError(t, err)
	safeContent[0] = 'X'
	require.Equal(t, byte('p'), blob.SafeContent[0], "blob output must not alias caller-owned content")
	blobRetryInput := blobInput
	blobRetryInput.SafeContent = []byte("package fixture\n")
	blobRetry, err := store.UpsertBlob(ctx, blobRetryInput)
	require.NoError(t, err)
	require.Equal(t, blob.BlobID, blobRetry.BlobID)
	var storedBlob UCIBlob
	require.NoError(t, fixture.db.Where("blob_id = ?", blob.BlobID).First(&storedBlob).Error)
	require.Equal(t, byte('p'), storedBlob.SafeContent[0], "stored blob must not alias caller-owned content")
	metadataOnly := blobInput
	metadataOnly.ContentDigest = uciProjectionDigest("0")
	metadataOnly.StorageState = UCIBlobMetadataOnly
	metadataOnly.SafeContent = []byte("must not persist")
	metadataOnly.ByteLength = int64(len(metadataOnly.SafeContent))
	_, err = store.UpsertBlob(ctx, metadataOnly)
	require.Error(t, err, "metadata-only blobs must reject source bytes before SQL")

	artifactInput := UpsertUCIParseArtifactInput{
		SourceID:                fixture.source.SourceID,
		BlobID:                  blob.BlobID,
		Language:                "go",
		ParserRevision:          "go-ast-v1",
		GrammarDigest:           uciProjectionDigest("2"),
		ExtractionProfileDigest: uciProjectionDigest("3"),
		Status:                  UCIParseArtifactComplete,
		Diagnostics:             `{}`,
	}
	artifact, err := store.UpsertParseArtifact(ctx, artifactInput)
	require.NoError(t, err)
	crossSourceArtifact := artifactInput
	crossSourceArtifact.SourceID = fixture.otherCheckout.SourceID
	_, err = store.UpsertParseArtifact(ctx, crossSourceArtifact)
	require.Error(t, err, "parse artifacts must not pair a blob with another Source")

	chunk, err := store.UpsertChunk(ctx, UpsertUCIChunkInput{
		SourceID:      fixture.source.SourceID,
		ArtifactID:    artifact.ArtifactID,
		ChunkKind:     "definition",
		Ordinal:       0,
		ByteStart:     0,
		ByteEnd:       int64(len(blobRetryInput.SafeContent)),
		ContentDigest: uciProjectionDigest("4"),
		TextForSearch: "func SharedTarget()",
	})
	require.NoError(t, err)

	embeddingProfile, err := store.UpsertEmbeddingProfile(ctx, UpsertUCIEmbeddingProfileInput{
		AnalysisProfileID:     fixture.profile.ProfileID,
		ProviderRef:           "provider-fixture",
		Model:                 "embedding-fixture",
		Dimension:             1536,
		PreprocessingRevision: "preprocess-v1",
		IncludeRelativePath:   true,
	})
	require.NoError(t, err)
	values := make([]float32, 1536)
	values[0] = 1
	inputVector := pgvector.NewVector(values)
	embedding, err := store.UpsertEmbedding(ctx, UpsertUCIEmbeddingInput{
		EmbeddingProfileID:   embeddingProfile.EmbeddingProfileID,
		EmbeddingInputDigest: uciProjectionDigest("5"),
		Vector:               &inputVector,
		SourceID:             fixture.source.SourceID,
		ProtectionDomain:     "source-private",
		CompletionSeq:        1,
		Status:               UCIEmbeddingReady,
	})
	require.NoError(t, err)
	inputVector.Slice()[0] = 9
	require.Equal(t, float32(1), embedding.Vector.Slice()[0], "embedding output must not alias caller-owned vector storage")
	shortVector := pgvector.NewVector([]float32{1, 2})
	_, err = store.UpsertEmbedding(ctx, UpsertUCIEmbeddingInput{
		EmbeddingProfileID:   embeddingProfile.EmbeddingProfileID,
		EmbeddingInputDigest: uciProjectionDigest("6"),
		Vector:               &shortVector,
		SourceID:             fixture.source.SourceID,
		ProtectionDomain:     "source-private",
		Status:               UCIEmbeddingReady,
	})
	require.Error(t, err, "embedding vectors must match the profile dimension before SQL")

	linked, err := store.LinkChunkEmbedding(ctx, LinkUCIChunkEmbeddingInput{
		SourceID:                fixture.source.SourceID,
		ChunkID:                 chunk.ChunkID,
		EmbeddingID:             embedding.EmbeddingID,
		RelativePathFingerprint: uciProjectionDigest("7"),
		EmbeddingProfileID:      embeddingProfile.EmbeddingProfileID,
		EmbeddingInputDigest:    embedding.EmbeddingInputDigest,
	})
	require.NoError(t, err)
	crossSourceLink := LinkUCIChunkEmbeddingInput{
		SourceID:                fixture.otherCheckout.SourceID,
		ChunkID:                 linked.ChunkID,
		EmbeddingID:             linked.EmbeddingID,
		RelativePathFingerprint: uciProjectionDigest("8"),
		EmbeddingProfileID:      linked.EmbeddingProfileID,
		EmbeddingInputDigest:    linked.EmbeddingInputDigest,
	}
	_, err = store.LinkChunkEmbedding(ctx, crossSourceLink)
	require.Error(t, err, "chunk embeddings must not cross Source scope")

	analysis, err := store.StoreAnalysis(ctx, StoreUCIAnalysisInput{
		ViewID:            fixture.view.ViewID,
		Kind:              "impact",
		AlgorithmRevision: "impact-v1",
		InputDigest:       uciProjectionDigest("9"),
		ArtifactRefs:      `{"artifact_id":"` + artifact.ArtifactID + `"}`,
		ResultJSON:        `{"bounded":true}`,
		State:             UCIAnalysisReady,
	})
	require.NoError(t, err)
	require.Equal(t, fixture.view.ViewID, analysis.ViewID)
}

func TestUCIProjectionMigration172EvidenceSchemaIsAppendOnlyAndNonContent(t *testing.T) {
	fixture := openUCIProjectionMigrationFixture(t)
	db := fixture.db

	assertUCIProjectionMigrationTableSet(t, db)
	assertUCIProjectionRequiredColumns(t, db, "uci_exposures",
		"exposure_id", "exposure_ref", "auth_realm", "source_id", "checkout_id", "view_id",
		"client_ref", "client_session_ref", "request_ref", "operation_kind", "result_state",
		"retrieval_mode", "coverage_state", "evidence_source", "certainty", "idempotency_key",
		"idempotency_binding_digest", "recorded_at",
	)
	assertUCIProjectionRequiredColumns(t, db, "uci_completion_evidence",
		"completion_evidence_id", "exposure_id", "supported_host_ref", "callback_ref", "outcome",
		"idempotency_key", "idempotency_binding_digest", "occurred_at",
	)
	assertUCIProjectionColumn(t, db, "uci_exposures", "exposure_id", "uuid", "NO")
	assertUCIProjectionColumn(t, db, "uci_exposures", "source_id", "uuid", "NO")
	assertUCIProjectionColumn(t, db, "uci_exposures", "checkout_id", "uuid", "NO")
	assertUCIProjectionColumn(t, db, "uci_exposures", "view_id", "uuid", "NO")
	assertUCIProjectionColumn(t, db, "uci_exposures", "idempotency_binding_digest", "text", "NO")
	assertUCIProjectionColumn(t, db, "uci_exposures", "recorded_at", "timestamp with time zone", "NO")
	assertUCIProjectionColumn(t, db, "uci_completion_evidence", "completion_evidence_id", "uuid", "NO")
	assertUCIProjectionColumn(t, db, "uci_completion_evidence", "exposure_id", "uuid", "NO")
	assertUCIProjectionColumn(t, db, "uci_completion_evidence", "idempotency_binding_digest", "text", "NO")
	assertUCIProjectionColumn(t, db, "uci_completion_evidence", "occurred_at", "timestamp with time zone", "NO")

	assertUCIProjectionConstraint(t, db, "uci_exposures", "unique", "exposure_ref")
	assertUCIProjectionConstraint(t, db, "uci_exposures", "unique", "auth_realm", "client_session_ref", "idempotency_key")
	assertUCIProjectionConstraint(t, db, "uci_exposures", "check", "code_search", "code_graph", "versioned_read")
	assertUCIProjectionConstraint(t, db, "uci_exposures", "check", "ok", "empty", "partial", "stale", "unavailable")
	assertUCIProjectionConstraint(t, db, "uci_exposures", "check", "exact", "lexical", "hybrid", "graph", "unavailable")
	assertUCIProjectionConstraint(t, db, "uci_exposures", "check", "complete", "partial", "unavailable")
	assertUCIProjectionConstraint(t, db, "uci_completion_evidence", "unique", "exposure_id", "supported_host_ref", "idempotency_key")
	assertUCIProjectionConstraint(t, db, "uci_completion_evidence", "check", "succeeded", "partial", "failed", "abandoned")
	assertUCIProjectionForeignKey(t, db, "uci_exposures", "ci_views", "source_id", "checkout_id", "view_id")
	assertUCIProjectionForeignKey(t, db, "uci_completion_evidence", "uci_exposures", "exposure_id")
	assertUCIProjectionIndex(t, db, "uci_exposures", "auth_realm", "client_session_ref", "idempotency_key")
	assertUCIProjectionIndex(t, db, "uci_completion_evidence", "exposure_id", "supported_host_ref", "idempotency_key")
	assertUCIProjectionEvidenceHasNoContentColumnsOrIndexes(t, db)
	assertUCIProjectionAppendOnlyTriggers(t, db, "uci_exposures")
	assertUCIProjectionAppendOnlyTriggers(t, db, "uci_completion_evidence")

	exposure := newUCIProjectionExposure(fixture)
	require.NoError(t, exposure.insert(db), "closed authorized evidence must be insertable")

	crossSource := newUCIProjectionExposure(fixture)
	crossSource.SourceID = fixture.source.SourceID
	crossSource.CheckoutID = fixture.otherCheckout.CheckoutID
	crossSource.ViewID = fixture.otherView.ViewID
	require.Error(t, crossSource.insert(db), "exposure context must reject a source paired with another checkout/view")

	duplicateRef := newUCIProjectionExposure(fixture)
	duplicateRef.ExposureRef = exposure.ExposureRef
	require.Error(t, duplicateRef.insert(db), "exposure_ref must be globally unique")

	duplicateIdempotency := newUCIProjectionExposure(fixture)
	duplicateIdempotency.ClientSessionRef = exposure.ClientSessionRef
	duplicateIdempotency.IdempotencyKey = exposure.IdempotencyKey
	require.Error(t, duplicateIdempotency.insert(db), "exposure idempotency scope must be exact")

	completion := newUCIProjectionCompletion(exposure.ExposureID)
	require.NoError(t, completion.insert(db), "verified callback evidence must be insertable")

	duplicateCompletion := newUCIProjectionCompletion(exposure.ExposureID)
	duplicateCompletion.SupportedHostRef = completion.SupportedHostRef
	duplicateCompletion.IdempotencyKey = completion.IdempotencyKey
	require.Error(t, duplicateCompletion.insert(db), "completion idempotency scope must be exact")

	require.Error(t, db.Exec(`UPDATE uci_exposures SET certainty = 'partial' WHERE exposure_id = ?`, exposure.ExposureID).Error, "retained exposure evidence must reject mutation")
	require.Error(t, db.Exec(`UPDATE uci_completion_evidence SET outcome = 'failed' WHERE completion_evidence_id = ?`, completion.CompletionEvidenceID).Error, "retained completion evidence must reject mutation")
	require.Error(t, db.Exec(`DELETE FROM uci_completion_evidence WHERE completion_evidence_id = ?`, completion.CompletionEvidenceID).Error, "retained completion evidence must reject ordinary deletion")
}

func TestUCIProjectionMigration172EvidenceStoreCanonicalRetryIntegrityAndRetention(t *testing.T) {
	fixture := openUCIProjectionMigrationFixture(t)
	store := NewUCIExposureStore(fixture.db)
	ctx := context.Background()
	recordedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)

	input := UCIExposureInput{
		AuthRealm:        fixture.authRealm,
		SourceID:         fixture.source.SourceID,
		CheckoutID:       fixture.checkout.CheckoutID,
		ViewID:           fixture.view.ViewID,
		ClientRef:        uuid.NewString(),
		ClientSessionRef: uuid.NewString(),
		RequestRef:       uuid.NewString(),
		OperationKind:    UCIExposureCodeSearch,
		ResultState:      UCIExposureResultUnavailable,
		RetrievalMode:    UCIRetrievalUnavailable,
		CoverageState:    UCICoverageUnavailable,
		EvidenceSource:   UCIEvidenceNone,
		Certainty:        UCICertaintyUnavailable,
		IdempotencyKey:   uuid.NewString(),
		RecordedAt:       recordedAt,
	}

	first, err := store.RecordExposure(ctx, input)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(first.ExposureRef, uciExposureRefPrefix))
	second, err := store.RecordExposure(ctx, input)
	require.NoError(t, err)
	require.Equal(t, first.ExposureID, second.ExposureID, "exact retry must return the original exposure")

	mismatchedExposure := input
	mismatchedExposure.RequestRef = uuid.NewString()
	staleExposure, err := store.RecordExposure(ctx, mismatchedExposure)
	require.ErrorIs(t, err, ErrUCIIdempotencyMismatch)
	require.Nil(t, staleExposure, "a mismatched retry must not disclose the prior exposure")

	_, err = store.RecordCompletion(ctx, UCICompletionInput{
		SupportedHostRef: uuid.NewString(),
		CallbackRef:      uuid.NewString(),
		Outcome:          UCICompletionPartial,
		IdempotencyKey:   uuid.NewString(),
	})
	require.Error(t, err, "completion recording must reject a missing opaque exposure reference before lookup")
	completionInput := UCICompletionInput{
		ExposureRef:      first.ExposureRef,
		SupportedHostRef: uuid.NewString(),
		CallbackRef:      uuid.NewString(),
		Outcome:          UCICompletionPartial,
		IdempotencyKey:   uuid.NewString(),
		OccurredAt:       recordedAt,
	}
	completion, err := store.RecordCompletion(ctx, completionInput)
	require.NoError(t, err)
	completionRetry, err := store.RecordCompletion(ctx, completionInput)
	require.NoError(t, err)
	require.Equal(t, completion.CompletionEvidenceID, completionRetry.CompletionEvidenceID, "exact retry must return the original completion")
	completionState, err := store.CompletionState(ctx, first.ExposureRef)
	require.NoError(t, err)
	require.Equal(t, UCICompletionState(UCICompletionPartial), completionState)

	mismatchedCompletion := completionInput
	mismatchedCompletion.CallbackRef = uuid.NewString()
	staleCompletion, err := store.RecordCompletion(ctx, mismatchedCompletion)
	require.ErrorIs(t, err, ErrUCIIdempotencyMismatch)
	require.Nil(t, staleCompletion, "a mismatched callback must not disclose the prior child")
	require.NoError(t, store.VerifyIntegrity(ctx), "normal PostgreSQL restore verification must accept canonical retained evidence")

	pruned, err := store.PruneExpired(ctx, time.Now().UTC(), 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), pruned.ExposuresDeleted)
	require.Equal(t, int64(1), pruned.CompletionsDeleted, "retention must delete the child before its parent")
	require.NoError(t, store.VerifyIntegrity(ctx), "empty retained evidence still requires append-only guards")
	tx := fixture.db.Begin()
	require.NoError(t, tx.Error)
	txStore := NewUCIExposureStore(tx)
	_, err = txStore.PruneExpired(ctx, time.Now().UTC().Add(-24*time.Hour), 1)
	require.NoError(t, err)
	var retentionMode string
	require.NoError(t, tx.Raw(`SELECT current_setting('app.uci_evidence_retention', true)`).Scan(&retentionMode).Error)
	require.Equal(t, "off", retentionMode, "retention privilege must be reset before returning to its caller transaction")
	require.NoError(t, tx.Rollback().Error)

	corrupt := UCIExposure{
		ExposureID:               uuid.NewString(),
		ExposureRef:              uciExposureRefPrefix + uuid.NewString(),
		AuthRealm:                fixture.authRealm,
		SourceID:                 fixture.source.SourceID,
		CheckoutID:               fixture.checkout.CheckoutID,
		ViewID:                   fixture.view.ViewID,
		ClientRef:                uuid.NewString(),
		ClientSessionRef:         uuid.NewString(),
		RequestRef:               uuid.NewString(),
		OperationKind:            UCIExposureCodeSearch,
		ResultState:              UCIExposureResultOK,
		RetrievalMode:            UCIRetrievalExact,
		CoverageState:            UCICoverageComplete,
		EvidenceSource:           UCIEvidenceExact,
		Certainty:                UCICertaintyEstablished,
		IdempotencyKey:           uuid.NewString(),
		IdempotencyBindingDigest: uciProjectionDigest("f"),
		RecordedAt:               time.Now().UTC(),
	}
	require.NoError(t, fixture.db.Create(&corrupt).Error, "syntactically valid restored evidence can still fail canonical verification")
	require.ErrorIs(t, store.VerifyIntegrity(ctx), ErrUCIEvidenceIntegrity)
}

func openUCIProjectionMigrationFixture(t *testing.T) *uciProjectionMigrationFixture {
	t.Helper()

	db, schema := openInterventionReceiptMigrationTestDB(t)
	assertUCIProjectionMigration171Prerequisites(t, db)

	ctx := context.Background()
	store := NewUCIContextStore(db)
	token := uuid.NewString()
	realm := "uci-projection-realm-" + token

	source, err := store.CreateSource(ctx, CreateSourceInput{
		AuthRealm:   realm,
		Kind:        UCISourceGit,
		DisplayName: "source-" + token,
	})
	require.NoError(t, err)
	otherSource, err := store.CreateSource(ctx, CreateSourceInput{
		AuthRealm:   realm,
		Kind:        UCISourceGit,
		DisplayName: "other-source-" + token,
	})
	require.NoError(t, err)

	checkout, err := store.RegisterCheckout(ctx, RegisterCheckoutInput{
		SourceID:       source.SourceID,
		WorkstationID:  "workstation-" + token,
		Kind:           UCICheckoutWorkingTree,
		OwnerPrincipal: "principal-" + token,
		LocatorRef:     "file:///uci/" + token,
	})
	require.NoError(t, err)
	otherCheckout, err := store.RegisterCheckout(ctx, RegisterCheckoutInput{
		SourceID:       otherSource.SourceID,
		WorkstationID:  "other-workstation-" + token,
		Kind:           UCICheckoutWorkingTree,
		OwnerPrincipal: "other-principal-" + token,
		LocatorRef:     "file:///uci/other-" + token,
	})
	require.NoError(t, err)

	profile, err := store.CreateProfile(ctx, CreateProfileInput{
		ParserBundleDigest:   uciProjectionDigest("a"),
		ResolverRevision:     "resolver-" + token,
		ChunkerRevision:      "chunker-" + token,
		IgnorePolicyDigest:   uciProjectionDigest("b"),
		BuildContextJSON:     `{"fixture":"` + token + `"}`,
		SecretPolicyRevision: "secret-policy-" + token,
	})
	require.NoError(t, err)

	view := newUCIProjectionView(t, store, checkout, profile, 1, token)
	otherView := newUCIProjectionView(t, store, otherCheckout, profile, 1, "other-"+token)
	stagingView := newUCIProjectionView(t, store, checkout, profile, 2, "staging-"+token)
	for _, candidate := range []*UCIView{view, otherView} {
		require.NoError(t, db.Exec(`UPDATE ci_views SET state = 'published', published_at = now() WHERE view_id = ?`, candidate.ViewID).Error)
	}
	require.NoError(t, db.Exec(`UPDATE ci_checkouts SET current_view_id = ? WHERE checkout_id = ?`, view.ViewID, checkout.CheckoutID).Error)

	current, err := store.GetCurrentView(ctx, checkout.CheckoutID)
	require.NoError(t, err, "migration 171 current-view relation must resolve the matching published view")
	require.Equal(t, view.ViewID, current.ViewID)
	require.Error(t, db.Exec(`UPDATE ci_checkouts SET current_view_id = ? WHERE checkout_id = ?`, otherView.ViewID, checkout.CheckoutID).Error, "migration 171 current-view composite foreign key must reject another checkout's view")
	current, err = store.GetCurrentView(ctx, checkout.CheckoutID)
	require.NoError(t, err)
	require.Equal(t, view.ViewID, current.ViewID, "rejected cross-checkout pointer must leave the one current view unchanged")

	projectID := "uci-projection-project-" + token
	require.NoError(t, db.Exec(`
		INSERT INTO projects (id, git_remote, relative_path, display_name)
		VALUES (?, ?, ?, ?)
	`, projectID, "https://example.invalid/"+token, "uci/"+token, "UCI "+token).Error)
	chunk := CodeChunk{
		ProjectID:      projectID,
		FilePath:       "uci/" + token + ".go",
		ByteStart:      0,
		ByteEnd:        16,
		Language:       "go",
		ChunkType:      "function",
		Content:        "package uci\n",
		ContentSHA256:  "sha256-" + token,
		IndexSessionID: "uci-projection-" + token,
	}
	require.NoError(t, NewCodeChunkStore(db).Upsert(ctx, &chunk))

	fixture := &uciProjectionMigrationFixture{
		db:            db,
		authRealm:     realm,
		source:        source,
		checkout:      checkout,
		otherCheckout: otherCheckout,
		view:          view,
		otherView:     otherView,
		profile:       profile,
		stagingView:   stagingView,
		projectID:     projectID,
		chunk:         chunk,
	}
	require.NoError(t, db.Raw(`SELECT git_remote, relative_path, display_name FROM projects WHERE id = ?`, projectID).Scan(&fixture.projectBefore).Error)
	require.NoError(t, db.Raw(`
		SELECT content, index_session_id
		FROM code_chunks
		WHERE project_id = ? AND file_path = ? AND byte_start = ? AND content_sha256 = ?
	`, chunk.ProjectID, chunk.FilePath, chunk.ByteStart, chunk.ContentSHA256).Scan(&fixture.chunkBefore).Error)

	require.NoError(t, db.Exec(`DELETE FROM migrations WHERE id = ?`, uciProjectionMigrationID).Error)
	require.Zero(t, uciProjectionMigrationAppliedCount(t, db, uciProjectionMigrationID), "the fixture must clear migration 172 before proving its idempotent reapplication")
	require.NoError(t, runMigrations(db), "migration 172 must reapply over the context registry and retained legacy rows")
	require.Equal(t, int64(1), uciProjectionMigrationAppliedCount(t, db, uciProjectionMigrationID), "migration 172 must be registered exactly once after reapplication in isolated schema %q", schema)

	assertUCIProjectionLegacySentinels(t, fixture)
	return fixture
}

func newUCIProjectionView(t *testing.T, store *UCIContextStore, checkout *UCICheckout, profile *UCIAnalysisProfile, generation int64, token string) *UCIView {
	t.Helper()

	scanStart := time.Now().UTC().Truncate(time.Microsecond)
	headOID := strings.Repeat("a", 40)
	objectFormat := "sha1"
	refLabel := "refs/heads/uci-" + token
	view, err := store.CreateView(context.Background(), CreateViewInput{
		CheckoutID:     checkout.CheckoutID,
		SourceID:       checkout.SourceID,
		IncarnationID:  checkout.IncarnationID,
		Generation:     generation,
		ProfileID:      profile.ProfileID,
		HeadOID:        &headOID,
		ObjectFormat:   &objectFormat,
		RefLabel:       &refLabel,
		ObservedFSSeq:  generation,
		ScanStart:      scanStart,
		ScanEnd:        scanStart.Add(time.Second),
		ManifestDigest: uciProjectionDigest("c"),
		CoverageJSON:   `{"fixture":"` + token + `"}`,
		State:          UCIViewStaging,
	})
	require.NoError(t, err)
	return view
}

func assertUCIProjectionMigration171Prerequisites(t *testing.T, db *gormlib.DB) {
	t.Helper()

	require.Equal(t, int64(1), uciProjectionMigrationAppliedCount(t, db, uciContextRegistryMigrationID), "migration 171 must already be applied before the projection slice")
	for _, tableName := range []string{"spaces", "sources", "space_sources", "legacy_context_aliases", "ci_profiles", "ci_checkouts", "ci_views"} {
		var count int64
		require.NoError(t, db.Raw(`
			SELECT COUNT(*)
			FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = ?
		`, tableName).Scan(&count).Error)
		require.Equalf(t, int64(1), count, "migration 171 prerequisite table %q must exist in the isolated schema", tableName)
	}
	assertUCIProjectionForeignKey(t, db, "ci_checkouts", "ci_views", "current_view_id", "checkout_id", "source_id", "incarnation_id")
	assertUCIProjectionConstraint(t, db, "ci_views", "unique", "checkout_id", "generation")
	assertUCIProjectionIndex(t, db, "ci_views", "checkout_id", "generation")
}

func uciProjectionMigrationAppliedCount(t *testing.T, db *gormlib.DB, migrationID string) int64 {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("migrations").Where("id = ?", migrationID).Count(&count).Error)
	return count
}

func assertUCIProjectionMigrationTableSet(t *testing.T, db *gormlib.DB) {
	t.Helper()

	require.Len(t, uciProjectionMigrationTableNames, 14, "migration 172 table contract must remain explicit")
	for _, tableName := range uciProjectionMigrationTableNames {
		var count int64
		require.NoError(t, db.Raw(`
			SELECT COUNT(*)
			FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = ?
		`, tableName).Scan(&count).Error)
		require.Equalf(t, int64(1), count, "migration 172 must create table %q in the isolated schema", tableName)
	}
}

func assertUCIProjectionLegacySentinels(t *testing.T, fixture *uciProjectionMigrationFixture) {
	t.Helper()

	var projectAfter uciProjectionProjectSentinel
	require.NoError(t, fixture.db.Raw(`SELECT git_remote, relative_path, display_name FROM projects WHERE id = ?`, fixture.projectID).Scan(&projectAfter).Error)
	var chunkAfter uciProjectionCodeChunkSentinel
	require.NoError(t, fixture.db.Raw(`
		SELECT content, index_session_id
		FROM code_chunks
		WHERE project_id = ? AND file_path = ? AND byte_start = ? AND content_sha256 = ?
	`, fixture.chunk.ProjectID, fixture.chunk.FilePath, fixture.chunk.ByteStart, fixture.chunk.ContentSHA256).Scan(&chunkAfter).Error)
	require.Equal(t, fixture.projectBefore, projectAfter, "migration 172 must preserve the legacy project sentinel")
	require.Equal(t, fixture.chunkBefore, chunkAfter, "migration 172 must preserve the legacy code_chunks sentinel as legacy_unscoped data")
}

func uciProjectionMigrationColumns(t *testing.T, db *gormlib.DB, tableName string) map[string]uciProjectionMigrationColumn {
	t.Helper()

	var columns []uciProjectionMigrationColumn
	require.NoError(t, db.Raw(`
		SELECT column_name, data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ?
		ORDER BY ordinal_position
	`, tableName).Scan(&columns).Error)
	actual := make(map[string]uciProjectionMigrationColumn, len(columns))
	for _, column := range columns {
		actual[column.Name] = column
	}
	return actual
}

func assertUCIProjectionRequiredColumns(t *testing.T, db *gormlib.DB, tableName string, required ...string) {
	t.Helper()

	columns := uciProjectionMigrationColumns(t, db, tableName)
	for _, column := range required {
		_, exists := columns[column]
		require.Truef(t, exists, "migration 172 table %q must contain column %q", tableName, column)
	}
}

func assertUCIProjectionColumn(t *testing.T, db *gormlib.DB, tableName, columnName, dataType, nullable string) {
	t.Helper()

	column, exists := uciProjectionMigrationColumns(t, db, tableName)[columnName]
	require.Truef(t, exists, "migration 172 table %q must contain column %q", tableName, columnName)
	require.Equalf(t, dataType, column.DataType, "migration 172 column %s.%s type", tableName, columnName)
	require.Equalf(t, nullable, column.Nullable, "migration 172 column %s.%s nullability", tableName, columnName)
}

func assertUCIProjectionVector1536(t *testing.T, db *gormlib.DB) {
	t.Helper()

	var typeName string
	require.NoError(t, db.Raw(`
		SELECT pg_catalog.format_type(attribute.atttypid, attribute.atttypmod)
		FROM pg_attribute AS attribute
		JOIN pg_class AS relation ON relation.oid = attribute.attrelid
		JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = current_schema()
		  AND relation.relname = 'ci_embeddings'
		  AND attribute.attname = 'vector'
		  AND NOT attribute.attisdropped
	`).Scan(&typeName).Error)
	require.Equal(t, "vector(1536)", typeName, "embedding vectors must retain the accepted 1536-dimension profile boundary")
}

func uciProjectionMigrationConstraints(t *testing.T, db *gormlib.DB, tableName string) []uciProjectionMigrationConstraint {
	t.Helper()

	var constraints []uciProjectionMigrationConstraint
	require.NoError(t, db.Raw(`
		SELECT constraint_row.conname, pg_get_constraintdef(constraint_row.oid) AS definition
		FROM pg_constraint AS constraint_row
		JOIN pg_class AS relation ON relation.oid = constraint_row.conrelid
		JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = current_schema() AND relation.relname = ?
		ORDER BY constraint_row.conname
	`, tableName).Scan(&constraints).Error)
	for index := range constraints {
		constraints[index].Definition = strings.ToLower(constraints[index].Definition)
	}
	return constraints
}

func assertUCIProjectionConstraint(t *testing.T, db *gormlib.DB, tableName string, needles ...string) {
	t.Helper()

	for _, constraint := range uciProjectionMigrationConstraints(t, db, tableName) {
		if uciProjectionContainsAll(constraint.Definition, needles...) {
			return
		}
	}
	t.Fatalf("migration 172 table %q has no constraint containing %q", tableName, needles)
}

func assertUCIProjectionForeignKey(t *testing.T, db *gormlib.DB, tableName, referencedTable string, columns ...string) {
	t.Helper()

	needles := append([]string{"foreign key", "references", referencedTable}, columns...)
	assertUCIProjectionConstraint(t, db, tableName, needles...)
}

func uciProjectionMigrationIndexes(t *testing.T, db *gormlib.DB, tableName string) []uciProjectionMigrationIndex {
	t.Helper()

	var indexes []uciProjectionMigrationIndex
	require.NoError(t, db.Raw(`
		SELECT indexname, indexdef
		FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = ?
		ORDER BY indexname
	`, tableName).Scan(&indexes).Error)
	for index := range indexes {
		indexes[index].Definition = strings.ToLower(indexes[index].Definition)
	}
	return indexes
}

func assertUCIProjectionIndex(t *testing.T, db *gormlib.DB, tableName string, needles ...string) {
	t.Helper()

	for _, index := range uciProjectionMigrationIndexes(t, db, tableName) {
		if uciProjectionContainsAll(index.Definition, needles...) {
			return
		}
	}
	t.Fatalf("migration 172 table %q has no index containing %q", tableName, needles)
}

func uciProjectionContainsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, strings.ToLower(needle)) {
			return false
		}
	}
	return true
}

func assertUCIProjectionEvidenceHasNoContentColumnsOrIndexes(t *testing.T, db *gormlib.DB) {
	t.Helper()

	for _, tableName := range []string{"uci_exposures", "uci_completion_evidence"} {
		for columnName := range uciProjectionMigrationColumns(t, db, tableName) {
			for _, forbidden := range []string{"content", "query", "path", "tool", "body", "prompt", "secret", "locator", "response", "output", "raw_"} {
				require.NotContainsf(t, columnName, forbidden, "UCI evidence table %q must not persist a content-bearing %q column", tableName, columnName)
			}
		}
		for _, index := range uciProjectionMigrationIndexes(t, db, tableName) {
			for _, forbidden := range []string{"content", "query", "path", "tool", "body", "prompt", "secret", "locator", "response", "output", "raw_"} {
				require.NotContainsf(t, index.Definition, forbidden, "UCI evidence table %q must not index content-bearing data through %q", tableName, index.Name)
			}
		}
	}
}

func assertUCIProjectionAppendOnlyTriggers(t *testing.T, db *gormlib.DB, tableName string) {
	t.Helper()

	var triggers []uciProjectionMigrationTrigger
	require.NoError(t, db.Raw(`
		SELECT trigger_row.tgname, pg_get_triggerdef(trigger_row.oid) AS definition
		FROM pg_trigger AS trigger_row
		JOIN pg_class AS relation ON relation.oid = trigger_row.tgrelid
		JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = current_schema()
		  AND relation.relname = ?
		  AND NOT trigger_row.tgisinternal
		ORDER BY trigger_row.tgname
	`, tableName).Scan(&triggers).Error)
	require.NotEmpty(t, triggers, "migration 172 evidence table %q must enforce append-only retention with database triggers", tableName)

	definitions := make([]string, 0, len(triggers))
	for _, trigger := range triggers {
		definitions = append(definitions, strings.ToLower(trigger.Definition))
	}
	for _, operation := range []string{"update", "delete"} {
		matched := false
		for _, definition := range definitions {
			if strings.Contains(definition, operation) {
				matched = true
				break
			}
		}
		require.Truef(t, matched, "migration 172 evidence table %q must reject ordinary %s operations", tableName, operation)
	}
}

type uciProjectionExposure struct {
	ExposureID               string
	ExposureRef              string
	AuthRealm                string
	SourceID                 string
	CheckoutID               string
	ViewID                   string
	ClientRef                string
	ClientSessionRef         string
	RequestRef               string
	IdempotencyKey           string
	IdempotencyBindingDigest string
	RecordedAt               time.Time
}

func newUCIProjectionExposure(fixture *uciProjectionMigrationFixture) uciProjectionExposure {
	return uciProjectionExposure{
		ExposureID:               uuid.NewString(),
		ExposureRef:              uuid.NewString(),
		AuthRealm:                fixture.authRealm,
		SourceID:                 fixture.source.SourceID,
		CheckoutID:               fixture.checkout.CheckoutID,
		ViewID:                   fixture.view.ViewID,
		ClientRef:                uuid.NewString(),
		ClientSessionRef:         uuid.NewString(),
		RequestRef:               uuid.NewString(),
		IdempotencyKey:           uuid.NewString(),
		IdempotencyBindingDigest: uciProjectionDigest("d"),
		RecordedAt:               time.Now().UTC(),
	}
}

func (exposure uciProjectionExposure) insert(db *gormlib.DB) error {
	return db.Exec(`
		INSERT INTO uci_exposures (
			exposure_id, exposure_ref, auth_realm, source_id, checkout_id, view_id,
			client_ref, client_session_ref, request_ref, operation_kind, result_state,
			retrieval_mode, coverage_state, evidence_source, certainty, idempotency_key,
			idempotency_binding_digest, recorded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'code_search', 'ok', 'exact', 'complete', 'exact', 'established', ?, ?, ?)
	`, exposure.ExposureID, exposure.ExposureRef, exposure.AuthRealm, exposure.SourceID, exposure.CheckoutID, exposure.ViewID,
		exposure.ClientRef, exposure.ClientSessionRef, exposure.RequestRef, exposure.IdempotencyKey, exposure.IdempotencyBindingDigest, exposure.RecordedAt).Error
}

type uciProjectionCompletion struct {
	CompletionEvidenceID     string
	ExposureID               string
	SupportedHostRef         string
	CallbackRef              string
	IdempotencyKey           string
	IdempotencyBindingDigest string
	OccurredAt               time.Time
}

func newUCIProjectionCompletion(exposureID string) uciProjectionCompletion {
	return uciProjectionCompletion{
		CompletionEvidenceID:     uuid.NewString(),
		ExposureID:               exposureID,
		SupportedHostRef:         uuid.NewString(),
		CallbackRef:              uuid.NewString(),
		IdempotencyKey:           uuid.NewString(),
		IdempotencyBindingDigest: uciProjectionDigest("e"),
		OccurredAt:               time.Now().UTC(),
	}
}

func (completion uciProjectionCompletion) insert(db *gormlib.DB) error {
	return db.Exec(`
		INSERT INTO uci_completion_evidence (
			completion_evidence_id, exposure_id, supported_host_ref, callback_ref, outcome,
			idempotency_key, idempotency_binding_digest, occurred_at
		) VALUES (?, ?, ?, ?, 'succeeded', ?, ?, ?)
	`, completion.CompletionEvidenceID, completion.ExposureID, completion.SupportedHostRef, completion.CallbackRef,
		completion.IdempotencyKey, completion.IdempotencyBindingDigest, completion.OccurredAt).Error
}

func uciProjectionDigest(hex string) string {
	return "sha256:" + strings.Repeat(hex, 64)
}
