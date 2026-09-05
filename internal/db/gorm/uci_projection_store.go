package gorm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errUCIProjectionStoreNotConfigured = errors.New("uci projection store not configured")

// UCIProjectionStore persists rebuildable shared code artifacts. It deliberately
// does not expose a current-view setter or publication lifecycle methods: those
// operations require the later fenced publication transaction.
type UCIProjectionStore struct {
	db *gorm.DB
}

func NewUCIProjectionStore(db *gorm.DB) *UCIProjectionStore {
	return &UCIProjectionStore{db: db}
}

// UpsertUCIBlobInput names one immutable, source-scoped blob cache entry.
type UpsertUCIBlobInput struct {
	SourceID         string
	ProtectionDomain string
	ContentDigest    string
	ByteLength       int64
	SafeContent      []byte
	Encoding         string
	StorageState     UCIBlobStorageState
}

func (s *UCIProjectionStore) UpsertBlob(ctx context.Context, in UpsertUCIBlobInput) (*UCIBlob, error) {
	if err := s.requireDB("upsert blob"); err != nil {
		return nil, err
	}
	if err := validateUCIUUID("source_id", in.SourceID); err != nil {
		return nil, err
	}
	if err := validateUCIRequiredText("protection_domain", in.ProtectionDomain); err != nil {
		return nil, err
	}
	if err := validateUCIDigest("content_digest", in.ContentDigest); err != nil {
		return nil, err
	}
	if in.ByteLength < 0 {
		return nil, fmt.Errorf("uci projection blob: byte_length must be non-negative")
	}
	if err := validateUCIRequiredText("encoding", in.Encoding); err != nil {
		return nil, err
	}
	if !isUCIBlobStorageState(in.StorageState) {
		return nil, fmt.Errorf("uci projection blob: unsupported storage_state %q", in.StorageState)
	}
	if in.StorageState == UCIBlobStored {
		if in.SafeContent == nil || int64(len(in.SafeContent)) != in.ByteLength {
			return nil, fmt.Errorf("uci projection blob: stored content must match byte_length")
		}
	} else if in.SafeContent != nil {
		return nil, fmt.Errorf("uci projection blob: metadata-only or excluded content must not carry source bytes")
	}

	row := &UCIBlob{
		BlobID:           uuid.NewString(),
		SourceID:         in.SourceID,
		ProtectionDomain: in.ProtectionDomain,
		ContentDigest:    in.ContentDigest,
		ByteLength:       in.ByteLength,
		SafeContent:      append([]byte(nil), in.SafeContent...),
		Encoding:         in.Encoding,
		StorageState:     in.StorageState,
		CreatedAt:        time.Now().UTC(),
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_id"}, {Name: "protection_domain"}, {Name: "content_digest"}},
		DoNothing: true,
	}).Create(row)
	if result.Error != nil {
		return nil, fmt.Errorf("uci projection upsert blob: %w", result.Error)
	}
	if result.RowsAffected != 0 {
		return row, nil
	}
	var existing UCIBlob
	if err := s.db.WithContext(ctx).
		Where("source_id = ? AND protection_domain = ? AND content_digest = ?", in.SourceID, in.ProtectionDomain, in.ContentDigest).
		First(&existing).Error; err != nil {
		return nil, fmt.Errorf("uci projection load existing blob: %w", err)
	}
	return &existing, nil
}

// UpsertUCIParseArtifactInput names a complete immutable parser cache key.
type UpsertUCIParseArtifactInput struct {
	SourceID                string
	BlobID                  string
	Language                string
	ParserRevision          string
	GrammarDigest           string
	ExtractionProfileDigest string
	Status                  UCIParseArtifactStatus
	Diagnostics             string
}

func (s *UCIProjectionStore) UpsertParseArtifact(ctx context.Context, in UpsertUCIParseArtifactInput) (*UCIParseArtifact, error) {
	if err := s.requireDB("upsert parse artifact"); err != nil {
		return nil, err
	}
	if err := validateUCIUUID("source_id", in.SourceID); err != nil {
		return nil, err
	}
	if err := validateUCIUUID("blob_id", in.BlobID); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"language", in.Language},
		{"parser_revision", in.ParserRevision},
	} {
		if err := validateUCIRequiredText(field.name, field.value); err != nil {
			return nil, err
		}
	}
	if err := validateUCIDigest("grammar_digest", in.GrammarDigest); err != nil {
		return nil, err
	}
	if err := validateUCIDigest("extraction_profile_digest", in.ExtractionProfileDigest); err != nil {
		return nil, err
	}
	if !isUCIParseArtifactStatus(in.Status) {
		return nil, fmt.Errorf("uci projection parse artifact: unsupported status %q", in.Status)
	}
	diagnostics, err := normalizeUCIJSONObject("diagnostics", in.Diagnostics)
	if err != nil {
		return nil, err
	}

	row := &UCIParseArtifact{
		ArtifactID:              uuid.NewString(),
		SourceID:                in.SourceID,
		BlobID:                  in.BlobID,
		Language:                in.Language,
		ParserRevision:          in.ParserRevision,
		GrammarDigest:           in.GrammarDigest,
		ExtractionProfileDigest: in.ExtractionProfileDigest,
		Status:                  in.Status,
		Diagnostics:             diagnostics,
		CreatedAt:               time.Now().UTC(),
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "source_id"},
			{Name: "blob_id"},
			{Name: "language"},
			{Name: "parser_revision"},
			{Name: "grammar_digest"},
			{Name: "extraction_profile_digest"},
		},
		DoNothing: true,
	}).Create(row)
	if result.Error != nil {
		return nil, fmt.Errorf("uci projection upsert parse artifact: %w", result.Error)
	}
	if result.RowsAffected != 0 {
		return row, nil
	}
	var existing UCIParseArtifact
	if err := s.db.WithContext(ctx).Where(
		"source_id = ? AND blob_id = ? AND language = ? AND parser_revision = ? AND grammar_digest = ? AND extraction_profile_digest = ?",
		in.SourceID, in.BlobID, in.Language, in.ParserRevision, in.GrammarDigest, in.ExtractionProfileDigest,
	).First(&existing).Error; err != nil {
		return nil, fmt.Errorf("uci projection load existing parse artifact: %w", err)
	}
	return &existing, nil
}

// UpsertUCIDefinitionInput describes one artifact-local symbol declaration.
type UpsertUCIDefinitionInput struct {
	ArtifactID         string
	LocalSymbolKey     string
	Kind               string
	Name               string
	QualifiedLocalName string
	Signature          string
	ByteStart          int64
	ByteEnd            int64
	LineStart          int
	LineEnd            int
}

func (s *UCIProjectionStore) UpsertDefinition(ctx context.Context, in UpsertUCIDefinitionInput) (*UCIDefinition, error) {
	if err := s.requireDB("upsert definition"); err != nil {
		return nil, err
	}
	if err := validateUCIUUID("artifact_id", in.ArtifactID); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"local_symbol_key", in.LocalSymbolKey},
		{"kind", in.Kind},
		{"name", in.Name},
		{"qualified_local_name", in.QualifiedLocalName},
	} {
		if err := validateUCIRequiredText(field.name, field.value); err != nil {
			return nil, err
		}
	}
	if err := validateUCIProjectionSpan(in.ByteStart, in.ByteEnd, in.LineStart, in.LineEnd); err != nil {
		return nil, err
	}

	row := &UCIDefinition{
		DefinitionID:       uuid.NewString(),
		ArtifactID:         in.ArtifactID,
		LocalSymbolKey:     in.LocalSymbolKey,
		Kind:               in.Kind,
		Name:               in.Name,
		QualifiedLocalName: in.QualifiedLocalName,
		Signature:          in.Signature,
		ByteStart:          in.ByteStart,
		ByteEnd:            in.ByteEnd,
		LineStart:          in.LineStart,
		LineEnd:            in.LineEnd,
		CreatedAt:          time.Now().UTC(),
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "artifact_id"}, {Name: "local_symbol_key"}},
		DoNothing: true,
	}).Create(row)
	if result.Error != nil {
		return nil, fmt.Errorf("uci projection upsert definition: %w", result.Error)
	}
	if result.RowsAffected != 0 {
		return row, nil
	}
	var existing UCIDefinition
	if err := s.db.WithContext(ctx).Where("artifact_id = ? AND local_symbol_key = ?", in.ArtifactID, in.LocalSymbolKey).First(&existing).Error; err != nil {
		return nil, fmt.Errorf("uci projection load existing definition: %w", err)
	}
	return &existing, nil
}

// UpsertUCIReferenceSiteInput describes an immutable observed reference site.
type UpsertUCIReferenceSiteInput struct {
	ArtifactID     string
	SiteKey        string
	OwnerSymbolKey *string
	RawTarget      string
	Relation       string
	SyntaxSpan     string
	ResolverHints  string
}

func (s *UCIProjectionStore) UpsertReferenceSite(ctx context.Context, in UpsertUCIReferenceSiteInput) (*UCIReferenceSite, error) {
	if err := s.requireDB("upsert reference site"); err != nil {
		return nil, err
	}
	if err := validateUCIUUID("artifact_id", in.ArtifactID); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"site_key", in.SiteKey},
		{"raw_target", in.RawTarget},
		{"relation", in.Relation},
	} {
		if err := validateUCIRequiredText(field.name, field.value); err != nil {
			return nil, err
		}
	}
	ownerSymbolKey, err := copyUCIOptionalTextPointer("owner_symbol_key", in.OwnerSymbolKey)
	if err != nil {
		return nil, err
	}
	syntaxSpan, err := normalizeUCIJSONObject("syntax_span", in.SyntaxSpan)
	if err != nil {
		return nil, err
	}
	resolverHints, err := normalizeUCIJSONObject("resolver_hints", in.ResolverHints)
	if err != nil {
		return nil, err
	}

	row := &UCIReferenceSite{
		ReferenceSiteID: uuid.NewString(),
		ArtifactID:      in.ArtifactID,
		SiteKey:         in.SiteKey,
		OwnerSymbolKey:  ownerSymbolKey,
		RawTarget:       in.RawTarget,
		Relation:        in.Relation,
		SyntaxSpan:      syntaxSpan,
		ResolverHints:   resolverHints,
		CreatedAt:       time.Now().UTC(),
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "artifact_id"}, {Name: "site_key"}},
		DoNothing: true,
	}).Create(row)
	if result.Error != nil {
		return nil, fmt.Errorf("uci projection upsert reference site: %w", result.Error)
	}
	if result.RowsAffected != 0 {
		return row, nil
	}
	var existing UCIReferenceSite
	if err := s.db.WithContext(ctx).Where("artifact_id = ? AND site_key = ?", in.ArtifactID, in.SiteKey).First(&existing).Error; err != nil {
		return nil, fmt.Errorf("uci projection load existing reference site: %w", err)
	}
	return &existing, nil
}

// UpsertUCIChunkInput describes one artifact-local searchable excerpt.
type UpsertUCIChunkInput struct {
	SourceID      string
	ArtifactID    string
	SymbolKey     *string
	ChunkKind     string
	Ordinal       int
	ByteStart     int64
	ByteEnd       int64
	ContentDigest string
	TextForSearch string
}

func (s *UCIProjectionStore) UpsertChunk(ctx context.Context, in UpsertUCIChunkInput) (*UCIChunk, error) {
	if err := s.requireDB("upsert chunk"); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"source_id", in.SourceID},
		{"artifact_id", in.ArtifactID},
	} {
		if err := validateUCIUUID(field.name, field.value); err != nil {
			return nil, err
		}
	}
	if err := validateUCIRequiredText("chunk_kind", in.ChunkKind); err != nil {
		return nil, err
	}
	if in.Ordinal < 0 {
		return nil, fmt.Errorf("uci projection chunk: ordinal must be non-negative")
	}
	if in.ByteStart < 0 || in.ByteEnd < in.ByteStart {
		return nil, fmt.Errorf("uci projection chunk: invalid byte span")
	}
	if err := validateUCIDigest("content_digest", in.ContentDigest); err != nil {
		return nil, err
	}
	symbolKey, err := copyUCIOptionalTextPointer("symbol_key", in.SymbolKey)
	if err != nil {
		return nil, err
	}

	row := &UCIChunk{
		ChunkID:       uuid.NewString(),
		SourceID:      in.SourceID,
		ArtifactID:    in.ArtifactID,
		SymbolKey:     symbolKey,
		ChunkKind:     in.ChunkKind,
		Ordinal:       in.Ordinal,
		ByteStart:     in.ByteStart,
		ByteEnd:       in.ByteEnd,
		ContentDigest: in.ContentDigest,
		TextForSearch: in.TextForSearch,
		CreatedAt:     time.Now().UTC(),
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "artifact_id"}, {Name: "ordinal"}, {Name: "content_digest"}},
		DoNothing: true,
	}).Create(row)
	if result.Error != nil {
		return nil, fmt.Errorf("uci projection upsert chunk: %w", result.Error)
	}
	if result.RowsAffected != 0 {
		return row, nil
	}
	var existing UCIChunk
	if err := s.db.WithContext(ctx).Where("artifact_id = ? AND ordinal = ? AND content_digest = ?", in.ArtifactID, in.Ordinal, in.ContentDigest).First(&existing).Error; err != nil {
		return nil, fmt.Errorf("uci projection load existing chunk: %w", err)
	}
	return &existing, nil
}

// UpsertUCIEmbeddingProfileInput names a versioned semantic space for an analysis profile.
type UpsertUCIEmbeddingProfileInput struct {
	AnalysisProfileID     string
	ProviderRef           string
	Model                 string
	Dimension             int
	PreprocessingRevision string
	IncludeRelativePath   bool
}

func (s *UCIProjectionStore) UpsertEmbeddingProfile(ctx context.Context, in UpsertUCIEmbeddingProfileInput) (*UCIEmbeddingProfile, error) {
	if err := s.requireDB("upsert embedding profile"); err != nil {
		return nil, err
	}
	if err := validateUCIUUID("analysis_profile_id", in.AnalysisProfileID); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"provider_ref", in.ProviderRef},
		{"model", in.Model},
		{"preprocessing_revision", in.PreprocessingRevision},
	} {
		if err := validateUCIRequiredText(field.name, field.value); err != nil {
			return nil, err
		}
	}
	if in.Dimension != 1536 {
		return nil, fmt.Errorf("uci projection embedding profile: dimension must be 1536")
	}

	row := &UCIEmbeddingProfile{
		EmbeddingProfileID:    uuid.NewString(),
		AnalysisProfileID:     in.AnalysisProfileID,
		ProviderRef:           in.ProviderRef,
		Model:                 in.Model,
		Dimension:             in.Dimension,
		PreprocessingRevision: in.PreprocessingRevision,
		IncludeRelativePath:   in.IncludeRelativePath,
		CreatedAt:             time.Now().UTC(),
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "analysis_profile_id"},
			{Name: "provider_ref"},
			{Name: "model"},
			{Name: "dimension"},
			{Name: "preprocessing_revision"},
			{Name: "include_relative_path"},
		},
		DoNothing: true,
	}).Create(row)
	if result.Error != nil {
		return nil, fmt.Errorf("uci projection upsert embedding profile: %w", result.Error)
	}
	if result.RowsAffected != 0 {
		return row, nil
	}
	var existing UCIEmbeddingProfile
	if err := s.db.WithContext(ctx).Where(
		"analysis_profile_id = ? AND provider_ref = ? AND model = ? AND dimension = ? AND preprocessing_revision = ? AND include_relative_path = ?",
		in.AnalysisProfileID, in.ProviderRef, in.Model, in.Dimension, in.PreprocessingRevision, in.IncludeRelativePath,
	).First(&existing).Error; err != nil {
		return nil, fmt.Errorf("uci projection load existing embedding profile: %w", err)
	}
	return &existing, nil
}

// UpsertUCIEmbeddingInput persists a reusable profile/source/protection-domain vector.
type UpsertUCIEmbeddingInput struct {
	EmbeddingProfileID   string
	EmbeddingInputDigest string
	Vector               *pgvector.Vector
	SourceID             string
	ProtectionDomain     string
	CompletionSeq        int64
	Status               UCIEmbeddingStatus
}

func (s *UCIProjectionStore) UpsertEmbedding(ctx context.Context, in UpsertUCIEmbeddingInput) (*UCIEmbedding, error) {
	if err := s.requireDB("upsert embedding"); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"embedding_profile_id", in.EmbeddingProfileID},
		{"source_id", in.SourceID},
	} {
		if err := validateUCIUUID(field.name, field.value); err != nil {
			return nil, err
		}
	}
	if err := validateUCIDigest("embedding_input_digest", in.EmbeddingInputDigest); err != nil {
		return nil, err
	}
	if err := validateUCIRequiredText("protection_domain", in.ProtectionDomain); err != nil {
		return nil, err
	}
	if in.CompletionSeq < 0 {
		return nil, fmt.Errorf("uci projection embedding: completion_seq must be non-negative")
	}
	if !isUCIEmbeddingStatus(in.Status) {
		return nil, fmt.Errorf("uci projection embedding: unsupported status %q", in.Status)
	}
	var vector *pgvector.Vector
	if in.Vector != nil {
		values := in.Vector.Slice()
		if len(values) != 1536 {
			return nil, fmt.Errorf("uci projection embedding: vector dimension must be 1536")
		}
		copyValues := append([]float32(nil), values...)
		copyVector := pgvector.NewVector(copyValues)
		vector = &copyVector
	}
	if in.Status == UCIEmbeddingReady && vector == nil {
		return nil, fmt.Errorf("uci projection embedding: ready embeddings require a vector")
	}
	if in.Status != UCIEmbeddingReady && vector != nil {
		return nil, fmt.Errorf("uci projection embedding: only ready embeddings may carry a vector")
	}

	row := &UCIEmbedding{
		EmbeddingID:          uuid.NewString(),
		EmbeddingProfileID:   in.EmbeddingProfileID,
		EmbeddingInputDigest: in.EmbeddingInputDigest,
		Vector:               vector,
		SourceID:             in.SourceID,
		ProtectionDomain:     in.ProtectionDomain,
		CompletionSeq:        in.CompletionSeq,
		Status:               in.Status,
		CreatedAt:            time.Now().UTC(),
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "embedding_profile_id"}, {Name: "embedding_input_digest"}, {Name: "source_id"}, {Name: "protection_domain"}},
		DoNothing: true,
	}).Create(row)
	if result.Error != nil {
		return nil, fmt.Errorf("uci projection upsert embedding: %w", result.Error)
	}
	if result.RowsAffected != 0 {
		return row, nil
	}
	var existing UCIEmbedding
	if err := s.db.WithContext(ctx).Where(
		"embedding_profile_id = ? AND embedding_input_digest = ? AND source_id = ? AND protection_domain = ?",
		in.EmbeddingProfileID, in.EmbeddingInputDigest, in.SourceID, in.ProtectionDomain,
	).First(&existing).Error; err != nil {
		return nil, fmt.Errorf("uci projection load existing embedding: %w", err)
	}
	return &existing, nil
}

// LinkUCIChunkEmbeddingInput connects a chunk to one matching immutable embedding.
type LinkUCIChunkEmbeddingInput struct {
	SourceID                string
	ChunkID                 string
	EmbeddingID             string
	RelativePathFingerprint string
	EmbeddingProfileID      string
	EmbeddingInputDigest    string
}

func (s *UCIProjectionStore) LinkChunkEmbedding(ctx context.Context, in LinkUCIChunkEmbeddingInput) (*UCIChunkEmbedding, error) {
	if err := s.requireDB("link chunk embedding"); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"source_id", in.SourceID},
		{"chunk_id", in.ChunkID},
		{"embedding_id", in.EmbeddingID},
		{"embedding_profile_id", in.EmbeddingProfileID},
	} {
		if err := validateUCIUUID(field.name, field.value); err != nil {
			return nil, err
		}
	}
	if err := validateUCIRequiredText("relative_path_fingerprint", in.RelativePathFingerprint); err != nil {
		return nil, err
	}
	if err := validateUCIDigest("embedding_input_digest", in.EmbeddingInputDigest); err != nil {
		return nil, err
	}

	row := &UCIChunkEmbedding{
		ChunkEmbeddingID:        uuid.NewString(),
		SourceID:                in.SourceID,
		ChunkID:                 in.ChunkID,
		EmbeddingID:             in.EmbeddingID,
		RelativePathFingerprint: in.RelativePathFingerprint,
		EmbeddingProfileID:      in.EmbeddingProfileID,
		EmbeddingInputDigest:    in.EmbeddingInputDigest,
		CreatedAt:               time.Now().UTC(),
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "chunk_id"},
			{Name: "relative_path_fingerprint"},
			{Name: "embedding_profile_id"},
			{Name: "embedding_input_digest"},
		},
		DoNothing: true,
	}).Create(row)
	if result.Error != nil {
		return nil, fmt.Errorf("uci projection link chunk embedding: %w", result.Error)
	}
	if result.RowsAffected != 0 {
		return row, nil
	}
	var existing UCIChunkEmbedding
	if err := s.db.WithContext(ctx).Where(
		"source_id = ? AND chunk_id = ? AND relative_path_fingerprint = ? AND embedding_profile_id = ? AND embedding_input_digest = ?",
		in.SourceID, in.ChunkID, in.RelativePathFingerprint, in.EmbeddingProfileID, in.EmbeddingInputDigest,
	).First(&existing).Error; err != nil {
		return nil, fmt.Errorf("uci projection load existing chunk embedding: %w", err)
	}
	return &existing, nil
}

// StoreUCIAnalysisInput persists a rebuildable analysis pinned to a View.
type StoreUCIAnalysisInput struct {
	ViewID            string
	Kind              string
	AlgorithmRevision string
	InputDigest       string
	ArtifactRefs      string
	ResultJSON        string
	State             UCIAnalysisState
}

func (s *UCIProjectionStore) StoreAnalysis(ctx context.Context, in StoreUCIAnalysisInput) (*UCIAnalysis, error) {
	if err := s.requireDB("store analysis"); err != nil {
		return nil, err
	}
	if err := validateUCIUUID("view_id", in.ViewID); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"kind", in.Kind},
		{"algorithm_revision", in.AlgorithmRevision},
	} {
		if err := validateUCIRequiredText(field.name, field.value); err != nil {
			return nil, err
		}
	}
	if err := validateUCIDigest("input_digest", in.InputDigest); err != nil {
		return nil, err
	}
	if !isUCIAnalysisState(in.State) {
		return nil, fmt.Errorf("uci projection analysis: unsupported state %q", in.State)
	}
	artifactRefs, err := normalizeUCIJSONObject("artifact_refs", in.ArtifactRefs)
	if err != nil {
		return nil, err
	}
	resultJSON, err := normalizeUCIJSONObject("result_json", in.ResultJSON)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	row := &UCIAnalysis{
		AnalysisID:        uuid.NewString(),
		ViewID:            in.ViewID,
		Kind:              in.Kind,
		AlgorithmRevision: in.AlgorithmRevision,
		InputDigest:       in.InputDigest,
		ArtifactRefs:      artifactRefs,
		ResultJSON:        resultJSON,
		State:             in.State,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "view_id"}, {Name: "kind"}, {Name: "algorithm_revision"}, {Name: "input_digest"}},
		DoNothing: true,
	}).Create(row)
	if result.Error != nil {
		return nil, fmt.Errorf("uci projection store analysis: %w", result.Error)
	}
	if result.RowsAffected != 0 {
		return row, nil
	}
	var existing UCIAnalysis
	if err := s.db.WithContext(ctx).Where(
		"view_id = ? AND kind = ? AND algorithm_revision = ? AND input_digest = ?",
		in.ViewID, in.Kind, in.AlgorithmRevision, in.InputDigest,
	).First(&existing).Error; err != nil {
		return nil, fmt.Errorf("uci projection load existing analysis: %w", err)
	}
	return &existing, nil
}

func (s *UCIProjectionStore) requireDB(operation string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("uci projection %s: %w", operation, errUCIProjectionStoreNotConfigured)
	}
	return nil
}

func validateUCIProjectionSpan(byteStart, byteEnd int64, lineStart, lineEnd int) error {
	if byteStart < 0 || byteEnd < byteStart {
		return fmt.Errorf("uci projection: invalid byte span")
	}
	if lineStart < 1 || lineEnd < lineStart {
		return fmt.Errorf("uci projection: invalid line span")
	}
	return nil
}
