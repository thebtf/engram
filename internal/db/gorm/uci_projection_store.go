package gorm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	ucidomain "github.com/thebtf/engram/internal/uci"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	errUCIProjectionStoreNotConfigured = errors.New("uci projection store not configured")
	errUCIProjectionImmutable          = errors.New("UCI_PROJECTION_IMMUTABLE")
)

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
		if digestUCIBytes(in.SafeContent) != in.ContentDigest {
			return nil, fmt.Errorf("uci projection blob: content_digest does not match stored bytes")
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
	if !sameUCIBlobPayload(existing, row) {
		return nil, errUCIProjectionImmutable
	}
	return &existing, nil
}

// UpsertUCIParseArtifactInput names a complete immutable parser cache key.
type UpsertUCIParseArtifactInput struct {
	// ArtifactID optionally pins a deterministic caller-derived UUID. When empty,
	// the store preserves its historical random-ID behavior.
	ArtifactID              string
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
	if in.ArtifactID != "" {
		if err := validateUCIUUID("artifact_id", in.ArtifactID); err != nil {
			return nil, err
		}
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

	artifactID := in.ArtifactID
	if artifactID == "" {
		artifactID = uuid.NewString()
	}

	row := &UCIParseArtifact{
		ArtifactID:              artifactID,
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
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row)
	if result.Error != nil {
		return nil, fmt.Errorf("uci projection upsert parse artifact: %w", result.Error)
	}
	if result.RowsAffected != 0 {
		return row, nil
	}
	var existing UCIParseArtifact
	err = s.db.WithContext(ctx).Where(
		"source_id = ? AND blob_id = ? AND language = ? AND parser_revision = ? AND grammar_digest = ? AND extraction_profile_digest = ?",
		in.SourceID, in.BlobID, in.Language, in.ParserRevision, in.GrammarDigest, in.ExtractionProfileDigest,
	).First(&existing).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) && in.ArtifactID != "" {
			var occupied UCIParseArtifact
			occupiedErr := s.db.WithContext(ctx).Where("artifact_id = ?", in.ArtifactID).First(&occupied).Error
			if occupiedErr == nil {
				return nil, errUCIProjectionImmutable
			}
			if !errors.Is(occupiedErr, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("uci projection load requested parse artifact: %w", occupiedErr)
			}
		}
		return nil, fmt.Errorf("uci projection load existing parse artifact: %w", err)
	}
	existingDiagnostics, err := normalizeUCIJSONObject("diagnostics", existing.Diagnostics)
	if err != nil {
		return nil, errUCIProjectionImmutable
	}
	if (in.ArtifactID != "" && existing.ArtifactID != in.ArtifactID) ||
		existing.Status != row.Status || existingDiagnostics != row.Diagnostics {
		return nil, errUCIProjectionImmutable
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
	if _, err := s.loadMutableUCIArtifact(ctx, in.ArtifactID); err != nil {
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
	if existing.Kind != row.Kind || existing.Name != row.Name || existing.QualifiedLocalName != row.QualifiedLocalName ||
		existing.Signature != row.Signature || existing.ByteStart != row.ByteStart || existing.ByteEnd != row.ByteEnd ||
		existing.LineStart != row.LineStart || existing.LineEnd != row.LineEnd {
		return nil, errUCIProjectionImmutable
	}
	return &existing, nil
}

// UpsertUCIReferenceSiteInput describes an immutable observed reference site.
type UpsertUCIReferenceSiteInput struct {
	// ReferenceSiteID optionally pins a deterministic caller-derived UUID. When
	// empty, the store preserves its historical random-ID behavior.
	ReferenceSiteID string
	ArtifactID      string
	SiteKey         string
	OwnerSymbolKey  *string
	RawTarget       string
	Relation        string
	SyntaxSpan      string
	ResolverHints   string
}

func (s *UCIProjectionStore) UpsertReferenceSite(ctx context.Context, in UpsertUCIReferenceSiteInput) (*UCIReferenceSite, error) {
	if err := s.requireDB("upsert reference site"); err != nil {
		return nil, err
	}
	if err := validateUCIUUID("artifact_id", in.ArtifactID); err != nil {
		return nil, err
	}
	if in.ReferenceSiteID != "" {
		if err := validateUCIUUID("reference_site_id", in.ReferenceSiteID); err != nil {
			return nil, err
		}
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
	if _, err := s.loadMutableUCIArtifact(ctx, in.ArtifactID); err != nil {
		return nil, err
	}

	referenceSiteID := in.ReferenceSiteID
	if referenceSiteID == "" {
		referenceSiteID = uuid.NewString()
	}

	row := &UCIReferenceSite{
		ReferenceSiteID: referenceSiteID,
		ArtifactID:      in.ArtifactID,
		SiteKey:         in.SiteKey,
		OwnerSymbolKey:  ownerSymbolKey,
		RawTarget:       in.RawTarget,
		Relation:        in.Relation,
		SyntaxSpan:      syntaxSpan,
		ResolverHints:   resolverHints,
		CreatedAt:       time.Now().UTC(),
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row)
	if result.Error != nil {
		return nil, fmt.Errorf("uci projection upsert reference site: %w", result.Error)
	}
	if result.RowsAffected != 0 {
		return row, nil
	}
	var existing UCIReferenceSite
	err = s.db.WithContext(ctx).Where("artifact_id = ? AND site_key = ?", in.ArtifactID, in.SiteKey).First(&existing).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) && in.ReferenceSiteID != "" {
			var occupied UCIReferenceSite
			occupiedErr := s.db.WithContext(ctx).Where("reference_site_id = ?", in.ReferenceSiteID).First(&occupied).Error
			if occupiedErr == nil {
				return nil, errUCIProjectionImmutable
			}
			if !errors.Is(occupiedErr, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("uci projection load requested reference site: %w", occupiedErr)
			}
		}
		return nil, fmt.Errorf("uci projection load existing reference site: %w", err)
	}
	existingSyntaxSpan, err := normalizeUCIJSONObject("syntax_span", existing.SyntaxSpan)
	if err != nil {
		return nil, errUCIProjectionImmutable
	}
	existingResolverHints, err := normalizeUCIJSONObject("resolver_hints", existing.ResolverHints)
	if err != nil {
		return nil, errUCIProjectionImmutable
	}
	if (in.ReferenceSiteID != "" && existing.ReferenceSiteID != in.ReferenceSiteID) ||
		!sameUCIOptionalString(existing.OwnerSymbolKey, row.OwnerSymbolKey) || existing.RawTarget != row.RawTarget ||
		existing.Relation != row.Relation || existingSyntaxSpan != row.SyntaxSpan || existingResolverHints != row.ResolverHints {
		return nil, errUCIProjectionImmutable
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
	artifact, err := s.loadMutableUCIArtifact(ctx, in.ArtifactID)
	if err != nil {
		return nil, err
	}
	if artifact.SourceID != in.SourceID {
		return nil, fmt.Errorf("uci projection chunk: artifact source mismatch")
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
	var existing UCIChunk
	err = s.db.WithContext(ctx).Where("artifact_id = ? AND ordinal = ?", in.ArtifactID, in.Ordinal).First(&existing).Error
	if err == nil {
		if !sameUCIOptionalString(existing.SymbolKey, row.SymbolKey) || existing.ChunkKind != row.ChunkKind ||
			existing.ByteStart != row.ByteStart || existing.ByteEnd != row.ByteEnd || existing.ContentDigest != row.ContentDigest ||
			existing.TextForSearch != row.TextForSearch {
			return nil, errUCIProjectionImmutable
		}
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("uci projection load existing chunk: %w", err)
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
	if err := s.db.WithContext(ctx).Where("artifact_id = ? AND ordinal = ?", in.ArtifactID, in.Ordinal).First(&existing).Error; err != nil {
		return nil, fmt.Errorf("uci projection load existing chunk: %w", err)
	}
	if !sameUCIOptionalString(existing.SymbolKey, row.SymbolKey) || existing.ChunkKind != row.ChunkKind ||
		existing.ByteStart != row.ByteStart || existing.ByteEnd != row.ByteEnd || existing.ContentDigest != row.ContentDigest ||
		existing.TextForSearch != row.TextForSearch {
		return nil, errUCIProjectionImmutable
	}
	return &existing, nil
}

const uciIndexAdmissionProtectionDomain = "source-private"

// AdmitIndexFrame admits one frame through the packed admission transaction.
func (s *UCIProjectionStore) AdmitIndexFrame(ctx context.Context, sourceID, profileID string, frame ucidomain.IndexAdmissionFrame) (ucidomain.IndexPart, error) {
	parts, err := s.AdmitIndexFrames(ctx, sourceID, profileID, []ucidomain.IndexAdmissionFrame{frame})
	if err != nil {
		return ucidomain.IndexPart{}, err
	}
	if len(parts) != 1 {
		return ucidomain.IndexPart{}, fmt.Errorf("uci index admission: expected exactly one admitted part")
	}
	return parts[0], nil
}

// AdmitIndexFrames validates a complete packed build before any durable write,
// then atomically records every artifact and fact before returning canonical
// parts for staging. Cross-frame edges therefore never observe a partially
// admitted target catalog.
func (s *UCIProjectionStore) AdmitIndexFrames(ctx context.Context, sourceID, profileID string, frames []ucidomain.IndexAdmissionFrame) ([]ucidomain.IndexPart, error) {
	if err := s.requireDB("admit index frames"); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, fmt.Errorf("uci index admission: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateUCIUUID("source_id", sourceID); err != nil {
		return nil, err
	}
	if err := validateUCIUUID("profile_id", profileID); err != nil {
		return nil, err
	}
	if len(frames) == 0 || len(frames) > ucidomain.IndexAdmissionMaxFrames {
		return nil, fmt.Errorf("uci index admission: invalid packed frame count")
	}

	canonicalFrames := make([]ucidomain.IndexAdmissionFrame, len(frames))
	for index, frame := range frames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		canonical, err := frame.Canonicalize()
		if err != nil {
			return nil, fmt.Errorf("uci index admission: validate frame %d: %w", index, err)
		}
		if canonical.Profile.ID != profileID {
			return nil, fmt.Errorf("uci index admission: frame profile does not match authorized profile")
		}
		for _, artifact := range canonical.Artifacts {
			expectedID, err := ucidomain.DeriveIndexAdmissionArtifactID(sourceID, artifact.ContentDigest, artifact.Profile)
			if err != nil {
				return nil, fmt.Errorf("uci index admission: derive artifact ID: %w", err)
			}
			if artifact.ArtifactID != expectedID {
				return nil, fmt.Errorf("uci index admission: artifact ID does not match authorized source")
			}
			for _, definition := range artifact.Definitions {
				if _, err := uciIndexAdmissionDefinitionName(artifact.Profile.Language, definition); err != nil {
					return nil, err
				}
			}
		}
		canonicalFrames[index] = canonical
	}
	if err := ucidomain.ValidateIndexAdmissionFrames(canonicalFrames); err != nil {
		return nil, fmt.Errorf("uci index admission: validate packed build: %w", err)
	}

	var admitted []ucidomain.IndexPart
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		store := &UCIProjectionStore{db: tx}
		bindings := make(ucidomain.IndexAdmissionReferenceBindings)
		for _, frame := range canonicalFrames {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := store.validateUCIIndexAdmissionProfile(ctx, profileID, frame); err != nil {
				return err
			}
			frameBindings, err := frame.ReferenceBindings()
			if err != nil {
				return fmt.Errorf("uci index admission: derive reference bindings: %w", err)
			}
			for key, referenceSiteID := range frameBindings {
				if existing, found := bindings[key]; found && existing != referenceSiteID {
					return errUCIProjectionImmutable
				}
				bindings[key] = referenceSiteID
			}
		}

		proofs := make(map[string]ucidomain.IndexArtifactProof)
		for _, frame := range canonicalFrames {
			for _, artifact := range frame.Artifacts {
				if err := ctx.Err(); err != nil {
					return err
				}
				proof, err := store.admitUCIIndexAdmissionArtifact(ctx, sourceID, artifact, bindings)
				if err != nil {
					return err
				}
				proofs[artifact.ArtifactID] = proof
			}
		}

		admitted = make([]ucidomain.IndexPart, len(canonicalFrames))
		for frameIndex, frame := range canonicalFrames {
			part, err := frame.IndexPart(bindings)
			if err != nil {
				return fmt.Errorf("uci index admission: canonical publication part: %w", err)
			}
			for artifactIndex := range part.Artifacts {
				proof, found := proofs[part.Artifacts[artifactIndex].ArtifactID]
				if !found {
					return fmt.Errorf("uci index admission: missing stored artifact proof")
				}
				part.Artifacts[artifactIndex] = proof
			}
			if _, err := ucidomain.DigestIndexPart(part); err != nil {
				return fmt.Errorf("uci index admission: validate stored publication part: %w", err)
			}
			admitted[frameIndex] = part
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return admitted, nil
}

func (s *UCIProjectionStore) validateUCIIndexAdmissionProfile(ctx context.Context, profileID string, frame ucidomain.IndexAdmissionFrame) error {
	var profile UCIAnalysisProfile
	if err := s.db.WithContext(ctx).Where("profile_id = ?", profileID).First(&profile).Error; err != nil {
		return fmt.Errorf("uci index admission: load profile: %w", err)
	}
	for _, artifact := range frame.Artifacts {
		if string(artifact.Profile.ExtractionProfileDigest) != profile.ParserBundleDigest {
			return fmt.Errorf("uci index admission: artifact profile does not match authorized profile")
		}
	}
	return nil
}

func (s *UCIProjectionStore) admitUCIIndexAdmissionArtifact(ctx context.Context, sourceID string, artifact ucidomain.IndexAdmissionArtifact, bindings ucidomain.IndexAdmissionReferenceBindings) (ucidomain.IndexArtifactProof, error) {
	status, err := uciIndexAdmissionArtifactStatus(artifact.Status)
	if err != nil {
		return ucidomain.IndexArtifactProof{}, err
	}
	diagnostics, err := marshalUCIIndexAdmissionDiagnostics(artifact.Diagnostics)
	if err != nil {
		return ucidomain.IndexArtifactProof{}, err
	}
	blob, err := s.UpsertBlob(ctx, UpsertUCIBlobInput{
		SourceID:         sourceID,
		ProtectionDomain: uciIndexAdmissionProtectionDomain,
		ContentDigest:    string(artifact.ContentDigest),
		ByteLength:       int64(len(artifact.Body)),
		SafeContent:      artifact.Body,
		Encoding:         "utf-8",
		StorageState:     UCIBlobStored,
	})
	if err != nil {
		return ucidomain.IndexArtifactProof{}, fmt.Errorf("uci index admission: upsert blob: %w", err)
	}
	stored, err := s.UpsertParseArtifact(ctx, UpsertUCIParseArtifactInput{
		ArtifactID:              artifact.ArtifactID,
		SourceID:                sourceID,
		BlobID:                  blob.BlobID,
		Language:                string(artifact.Profile.Language),
		ParserRevision:          artifact.Profile.ParserRevision,
		GrammarDigest:           string(artifact.Profile.GrammarDigest),
		ExtractionProfileDigest: string(artifact.Profile.ExtractionProfileDigest),
		Status:                  status,
		Diagnostics:             diagnostics,
	})
	if err != nil {
		return ucidomain.IndexArtifactProof{}, fmt.Errorf("uci index admission: upsert artifact: %w", err)
	}
	if stored.ArtifactID != artifact.ArtifactID || stored.SourceID != sourceID {
		return ucidomain.IndexArtifactProof{}, errUCIProjectionImmutable
	}

	if stored.SealedAt == nil {
		if err := s.storeUCIIndexAdmissionFacts(ctx, sourceID, artifact, bindings); err != nil {
			return ucidomain.IndexArtifactProof{}, err
		}
	}
	if err := s.verifyUCIIndexAdmissionFacts(ctx, artifact, bindings); err != nil {
		return ucidomain.IndexArtifactProof{}, err
	}
	proof, err := s.DescribeIndexArtifact(ctx, sourceID, artifact.ArtifactID)
	if err != nil {
		return ucidomain.IndexArtifactProof{}, fmt.Errorf("uci index admission: describe stored artifact: %w", err)
	}
	return proof, nil
}

func (s *UCIProjectionStore) storeUCIIndexAdmissionFacts(ctx context.Context, sourceID string, artifact ucidomain.IndexAdmissionArtifact, bindings ucidomain.IndexAdmissionReferenceBindings) error {
	for _, definition := range artifact.Definitions {
		name, err := uciIndexAdmissionDefinitionName(artifact.Profile.Language, definition)
		if err != nil {
			return err
		}
		if _, err := s.UpsertDefinition(ctx, UpsertUCIDefinitionInput{
			ArtifactID:         artifact.ArtifactID,
			LocalSymbolKey:     definition.LocalSymbolKey,
			Kind:               definition.Kind,
			Name:               name,
			QualifiedLocalName: definition.SymbolKey,
			Signature:          "",
			ByteStart:          definition.Span.ByteStart,
			ByteEnd:            definition.Span.ByteEnd,
			LineStart:          definition.Span.LineStart,
			LineEnd:            definition.Span.LineEnd,
		}); err != nil {
			return fmt.Errorf("uci index admission: upsert definition: %w", err)
		}
	}
	for _, reference := range artifact.References {
		binding := ucidomain.IndexAdmissionReferenceKey{ArtifactID: artifact.ArtifactID, SiteKey: reference.SiteKey}
		referenceSiteID, found := bindings[binding]
		if !found {
			return fmt.Errorf("uci index admission: missing reference binding")
		}
		syntaxSpan, err := marshalUCIIndexAdmissionSpan(reference.Span)
		if err != nil {
			return err
		}
		resolverHints, err := marshalUCIIndexAdmissionReferenceHints(reference)
		if err != nil {
			return err
		}
		if _, err := s.UpsertReferenceSite(ctx, UpsertUCIReferenceSiteInput{
			ReferenceSiteID: referenceSiteID,
			ArtifactID:      artifact.ArtifactID,
			SiteKey:         reference.SiteKey,
			OwnerSymbolKey:  reference.OwnerSymbolKey,
			RawTarget:       reference.RawTarget,
			Relation:        string(reference.Relation),
			SyntaxSpan:      syntaxSpan,
			ResolverHints:   resolverHints,
		}); err != nil {
			return fmt.Errorf("uci index admission: upsert reference site: %w", err)
		}
	}
	for _, chunk := range artifact.Chunks {
		if _, err := s.UpsertChunk(ctx, UpsertUCIChunkInput{
			SourceID:      sourceID,
			ArtifactID:    artifact.ArtifactID,
			SymbolKey:     chunk.SymbolKey,
			ChunkKind:     chunk.Kind,
			Ordinal:       chunk.Ordinal,
			ByteStart:     chunk.Span.ByteStart,
			ByteEnd:       chunk.Span.ByteEnd,
			ContentDigest: string(chunk.ContentDigest),
			TextForSearch: chunk.Text,
		}); err != nil {
			return fmt.Errorf("uci index admission: upsert chunk: %w", err)
		}
	}
	return nil
}

func uciIndexAdmissionDefinitionName(language ucidomain.IndexAdmissionLanguage, definition ucidomain.IndexAdmissionDefinition) (string, error) {
	switch language {
	case ucidomain.IndexAdmissionLanguageGo:
		return uciIndexAdmissionGoDefinitionName(definition)
	case ucidomain.IndexAdmissionLanguageJavaScript, ucidomain.IndexAdmissionLanguageTypeScript, ucidomain.IndexAdmissionLanguageTSX:
		return uciIndexAdmissionTreeSitterDefinitionName(definition)
	default:
		return "", fmt.Errorf("uci index admission: unsupported artifact language %q", language)
	}
}

func uciIndexAdmissionGoDefinitionName(definition ucidomain.IndexAdmissionDefinition) (string, error) {
	localKey := definition.LocalSymbolKey
	var expectedKind, name string
	switch {
	case strings.HasPrefix(localKey, "pkg:"):
		expectedKind = "package"
		name = strings.TrimPrefix(localKey, "pkg:")
	case strings.HasPrefix(localKey, "type:"):
		expectedKind = "type"
		name = strings.TrimPrefix(localKey, "type:")
	case strings.HasPrefix(localKey, "func:"):
		expectedKind = "function"
		name = strings.TrimPrefix(localKey, "func:")
	case strings.HasPrefix(localKey, "method:"):
		expectedKind = "method"
		receiverAndName := strings.TrimPrefix(localKey, "method:")
		separator := strings.LastIndex(receiverAndName, ".")
		if separator <= 0 || separator == len(receiverAndName)-1 || !uciIndexAdmissionGoReceiver(receiverAndName[:separator]) {
			return "", fmt.Errorf("uci index admission: unparseable Go definition key %q", localKey)
		}
		name = receiverAndName[separator+1:]
	default:
		return "", fmt.Errorf("uci index admission: unparseable Go definition key %q", localKey)
	}
	if definition.Kind != expectedKind || !token.IsIdentifier(name) {
		return "", fmt.Errorf("uci index admission: unparseable Go definition key %q", localKey)
	}
	return name, nil
}

func uciIndexAdmissionGoReceiver(receiver string) bool {
	for _, segment := range strings.Split(receiver, ".") {
		if !token.IsIdentifier(segment) {
			return false
		}
	}
	return true
}

func uciIndexAdmissionTreeSitterDefinitionName(definition ucidomain.IndexAdmissionDefinition) (string, error) {
	switch definition.Kind {
	case "function", "method", "class", "interface", "type", "enum", "namespace", "const", "let", "var":
	default:
		return "", fmt.Errorf("uci index admission: unparseable Tree-sitter definition key %q", definition.LocalSymbolKey)
	}
	prefix := definition.Kind + ":"
	if !strings.HasPrefix(definition.LocalSymbolKey, prefix) {
		return "", fmt.Errorf("uci index admission: unparseable Tree-sitter definition key %q", definition.LocalSymbolKey)
	}
	name, valid := uciIndexAdmissionTreeSitterQualifiedName(strings.TrimPrefix(definition.LocalSymbolKey, prefix))
	if !valid {
		return "", fmt.Errorf("uci index admission: unparseable Tree-sitter definition key %q", definition.LocalSymbolKey)
	}
	return name, nil
}

func uciIndexAdmissionTreeSitterQualifiedName(value string) (string, bool) {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", false
	}
	segmentStart := 0
	for index, character := range value {
		if character != '.' {
			continue
		}
		segment := value[segmentStart:index]
		if segment == "" || strings.TrimSpace(segment) != segment {
			return "", false
		}
		segmentStart = index + 1
	}
	name := value[segmentStart:]
	if name == "" || strings.TrimSpace(name) != name {
		return "", false
	}
	return name, true
}

func (s *UCIProjectionStore) verifyUCIIndexAdmissionFacts(ctx context.Context, artifact ucidomain.IndexAdmissionArtifact, bindings ucidomain.IndexAdmissionReferenceBindings) error {
	var definitions []UCIDefinition
	if err := s.db.WithContext(ctx).Where("artifact_id = ?", artifact.ArtifactID).Order("local_symbol_key ASC").Find(&definitions).Error; err != nil {
		return fmt.Errorf("uci index admission: load definitions: %w", err)
	}
	if len(definitions) != len(artifact.Definitions) {
		return fmt.Errorf("uci index admission: stored definition count differs: %w", errUCIProjectionImmutable)
	}
	for index, expected := range artifact.Definitions {
		name, err := uciIndexAdmissionDefinitionName(artifact.Profile.Language, expected)
		if err != nil {
			return err
		}
		actual := definitions[index]
		if actual.LocalSymbolKey != expected.LocalSymbolKey || actual.Kind != expected.Kind ||
			actual.Name != name || actual.QualifiedLocalName != expected.SymbolKey || actual.Signature != "" ||
			actual.ByteStart != expected.Span.ByteStart || actual.ByteEnd != expected.Span.ByteEnd ||
			actual.LineStart != expected.Span.LineStart || actual.LineEnd != expected.Span.LineEnd {
			return fmt.Errorf("uci index admission: stored definition differs: %w", errUCIProjectionImmutable)
		}
	}

	var references []UCIReferenceSite
	if err := s.db.WithContext(ctx).Where("artifact_id = ?", artifact.ArtifactID).Order("site_key ASC").Find(&references).Error; err != nil {
		return fmt.Errorf("uci index admission: load reference sites: %w", err)
	}
	if len(references) != len(artifact.References) {
		return fmt.Errorf("uci index admission: stored reference count differs: %w", errUCIProjectionImmutable)
	}
	for index, expected := range artifact.References {
		binding := ucidomain.IndexAdmissionReferenceKey{ArtifactID: artifact.ArtifactID, SiteKey: expected.SiteKey}
		referenceSiteID, found := bindings[binding]
		if !found {
			return fmt.Errorf("uci index admission: stored reference has no deterministic binding: %w", errUCIProjectionImmutable)
		}
		syntaxSpan, err := marshalUCIIndexAdmissionSpan(expected.Span)
		if err != nil {
			return err
		}
		resolverHints, err := marshalUCIIndexAdmissionReferenceHints(expected)
		if err != nil {
			return err
		}
		actual := references[index]
		actualSyntaxSpan, err := normalizeUCIJSONObject("syntax_span", actual.SyntaxSpan)
		if err != nil {
			return errUCIProjectionImmutable
		}
		actualResolverHints, err := normalizeUCIJSONObject("resolver_hints", actual.ResolverHints)
		if err != nil {
			return errUCIProjectionImmutable
		}
		if actual.ReferenceSiteID != referenceSiteID || actual.SiteKey != expected.SiteKey ||
			!sameUCIOptionalString(actual.OwnerSymbolKey, expected.OwnerSymbolKey) ||
			actual.RawTarget != expected.RawTarget || actual.Relation != string(expected.Relation) ||
			actualSyntaxSpan != syntaxSpan || actualResolverHints != resolverHints {
			return fmt.Errorf("uci index admission: stored reference differs: %w", errUCIProjectionImmutable)
		}
	}

	var chunks []UCIChunk
	if err := s.db.WithContext(ctx).Where("artifact_id = ?", artifact.ArtifactID).Order("ordinal ASC").Find(&chunks).Error; err != nil {
		return fmt.Errorf("uci index admission: load chunks: %w", err)
	}
	if len(chunks) != len(artifact.Chunks) {
		return fmt.Errorf("uci index admission: stored chunk count differs: %w", errUCIProjectionImmutable)
	}
	for index, expected := range artifact.Chunks {
		actual := chunks[index]
		if !sameUCIOptionalString(actual.SymbolKey, expected.SymbolKey) || actual.ChunkKind != expected.Kind || actual.Ordinal != expected.Ordinal ||
			actual.ByteStart != expected.Span.ByteStart || actual.ByteEnd != expected.Span.ByteEnd ||
			actual.ContentDigest != string(expected.ContentDigest) || actual.TextForSearch != expected.Text {
			return fmt.Errorf("uci index admission: stored chunk differs: %w", errUCIProjectionImmutable)
		}
	}
	return nil
}

func uciIndexAdmissionArtifactStatus(status ucidomain.IndexAdmissionArtifactStatus) (UCIParseArtifactStatus, error) {
	switch status {
	case ucidomain.IndexAdmissionArtifactComplete:
		return UCIParseArtifactComplete, nil
	case ucidomain.IndexAdmissionArtifactPartial:
		return UCIParseArtifactPartial, nil
	default:
		return "", fmt.Errorf("uci index admission: unsupported artifact status")
	}
}

func marshalUCIIndexAdmissionDiagnostics(diagnostics []ucidomain.IndexAdmissionDiagnostic) (string, error) {
	encoded, err := json.Marshal(struct {
		Items []ucidomain.IndexAdmissionDiagnostic `json:"items"`
	}{Items: diagnostics})
	if err != nil {
		return "", fmt.Errorf("uci index admission: encode diagnostics: %w", err)
	}
	return string(encoded), nil
}

func marshalUCIIndexAdmissionSpan(span ucidomain.IndexSpan) (string, error) {
	encoded, err := json.Marshal(struct {
		ByteEnd   int64 `json:"byte_end"`
		ByteStart int64 `json:"byte_start"`
		LineEnd   int   `json:"line_end"`
		LineStart int   `json:"line_start"`
	}{
		ByteEnd:   span.ByteEnd,
		ByteStart: span.ByteStart,
		LineEnd:   span.LineEnd,
		LineStart: span.LineStart,
	})
	if err != nil {
		return "", fmt.Errorf("uci index admission: encode source span: %w", err)
	}
	return string(encoded), nil
}

func marshalUCIIndexAdmissionReferenceHints(reference ucidomain.IndexAdmissionReference) (string, error) {
	encoded, err := json.Marshal(struct {
		Kind      string `json:"kind"`
		SymbolKey string `json:"symbol_key"`
	}{
		Kind:      reference.Kind,
		SymbolKey: reference.SymbolKey,
	})
	if err != nil {
		return "", fmt.Errorf("uci index admission: encode reference hints: %w", err)
	}
	return string(encoded), nil
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

const uciSemanticPathIndependentFingerprint = "path-independent"

var _ ucidomain.SemanticStore = (*UCIProjectionStore)(nil)

// LookupCandidateEmbedding returns only an exact, current candidate embedding.
// The selected_candidate CTE binds Source, Checkout, View, generation, profile,
// artifact bytes, and membership before it joins any reusable vector rows.
func (s *UCIProjectionStore) LookupCandidateEmbedding(ctx context.Context, authorized ucidomain.AuthorizedContext, profile ucidomain.VectorProfile, candidate ucidomain.QueryCandidate) ([]float32, bool, error) {
	if err := s.requireDB("lookup semantic candidate embedding"); err != nil {
		return nil, false, err
	}
	ref := authorized.Ref()
	if err := validateUCISemanticCandidate(ref, profile, candidate); err != nil {
		return nil, false, err
	}
	_, inputDigest, err := ucidomain.SemanticEmbeddingInput(profile, candidate)
	if err != nil {
		return nil, false, fmt.Errorf("uci projection semantic input: %w", err)
	}
	query, arguments := buildUCISemanticLookupSQL(ref, profile, candidate, inputDigest)
	var row uciSemanticVectorRow
	result := s.db.WithContext(ctx).Raw(query, arguments...).Scan(&row)
	if result.Error != nil {
		return nil, false, fmt.Errorf("uci projection lookup semantic candidate embedding: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, false, nil
	}
	vector := row.Vector.Slice()
	if err := validateUCISemanticVector(vector, profile.Dimension); err != nil {
		return nil, false, fmt.Errorf("uci projection lookup semantic candidate embedding: stored vector is invalid: %w", err)
	}
	return append([]float32(nil), vector...), true, nil
}

// StoreCandidateEmbedding persists one provider-validated vector and links it
// only to a candidate still visible in the exact authorized View.
func (s *UCIProjectionStore) StoreCandidateEmbedding(ctx context.Context, authorized ucidomain.AuthorizedContext, profile ucidomain.VectorProfile, candidate ucidomain.QueryCandidate, vector []float32) error {
	if err := s.requireDB("store semantic candidate embedding"); err != nil {
		return err
	}
	ref := authorized.Ref()
	if err := validateUCISemanticCandidate(ref, profile, candidate); err != nil {
		return err
	}
	if err := validateUCISemanticVector(vector, profile.Dimension); err != nil {
		return fmt.Errorf("uci projection store semantic candidate embedding: %w", err)
	}
	_, inputDigest, err := ucidomain.SemanticEmbeddingInput(profile, candidate)
	if err != nil {
		return fmt.Errorf("uci projection semantic input: %w", err)
	}
	candidateRow, found, err := s.loadUCISemanticCurrentCandidate(ctx, ref, candidate)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("uci projection store semantic candidate embedding: candidate is not current in the authorized view")
	}

	embeddingProfile, err := s.UpsertEmbeddingProfile(ctx, UpsertUCIEmbeddingProfileInput{
		AnalysisProfileID:     ref.AnalysisProfileID,
		ProviderRef:           profile.ProviderRef,
		Model:                 profile.Model,
		Dimension:             profile.Dimension,
		PreprocessingRevision: profile.PreprocessingRevision,
		IncludeRelativePath:   profile.IncludeRelativePath,
	})
	if err != nil {
		return fmt.Errorf("uci projection store semantic embedding profile: %w", err)
	}
	copyVector := pgvector.NewVector(append([]float32(nil), vector...))
	embedding, err := s.UpsertEmbedding(ctx, UpsertUCIEmbeddingInput{
		EmbeddingProfileID:   embeddingProfile.EmbeddingProfileID,
		EmbeddingInputDigest: string(inputDigest),
		Vector:               &copyVector,
		SourceID:             ref.SourceID,
		ProtectionDomain:     candidateRow.ProtectionDomain,
		CompletionSeq:        ref.Generation,
		Status:               UCIEmbeddingReady,
	})
	if err != nil {
		return fmt.Errorf("uci projection store semantic embedding: %w", err)
	}
	if embedding.Status != UCIEmbeddingReady || embedding.Vector == nil {
		return fmt.Errorf("uci projection store semantic embedding: existing embedding is not ready")
	}
	if _, err := s.LinkChunkEmbedding(ctx, LinkUCIChunkEmbeddingInput{
		SourceID:                ref.SourceID,
		ChunkID:                 candidateRow.ChunkID,
		EmbeddingID:             embedding.EmbeddingID,
		RelativePathFingerprint: uciSemanticRelativePathFingerprint(profile, candidate.RelativePath),
		EmbeddingProfileID:      embeddingProfile.EmbeddingProfileID,
		EmbeddingInputDigest:    string(inputDigest),
	}); err != nil {
		return fmt.Errorf("uci projection link semantic candidate embedding: %w", err)
	}
	return nil
}

// SelectSemanticCandidates ranks vectors inside the authorized current View.
// It never ranks globally and filters afterward: the selected_view CTE is the
// first relation in both coverage and pgvector-distance queries.
func (s *UCIProjectionStore) SelectSemanticCandidates(ctx context.Context, authorized ucidomain.AuthorizedContext, profile ucidomain.VectorProfile, vector []float32, spec ucidomain.QuerySpec) (ucidomain.SemanticStoreResult, error) {
	if err := s.requireDB("select semantic candidates"); err != nil {
		return ucidomain.SemanticStoreResult{}, err
	}
	ref := authorized.Ref()
	if err := validateUCIQueryContext(ref); err != nil {
		return ucidomain.SemanticStoreResult{}, err
	}
	if err := validateUCISemanticProfile(profile); err != nil {
		return ucidomain.SemanticStoreResult{}, err
	}
	if err := validateUCISemanticVector(vector, profile.Dimension); err != nil {
		return ucidomain.SemanticStoreResult{}, err
	}
	if err := validateUCIQuerySpec(spec); err != nil {
		return ucidomain.SemanticStoreResult{}, err
	}

	metadata, found, err := s.loadUCIQueryViewMetadata(ctx, ref)
	if err != nil {
		return ucidomain.SemanticStoreResult{}, err
	}
	if !found || metadata.State == UCIViewRetired || metadata.State == UCIViewStaging {
		return ucidomain.SemanticStoreResult{Coverage: ucidomain.IndexCoverageUnavailable}, nil
	}
	coverage, coverageOK := parseUCIQueryCoverage(metadata.Structural)
	if !coverageOK || coverage == ucidomain.IndexCoverageUnavailable {
		return ucidomain.SemanticStoreResult{Coverage: ucidomain.IndexCoverageUnavailable}, nil
	}
	if coverage != ucidomain.IndexCoverageComplete {
		return ucidomain.SemanticStoreResult{Coverage: coverage, VectorCoverage: 0}, nil
	}
	coverageQuery, coverageArguments, err := buildUCISemanticCoverageSQL(ref, profile, spec)
	if err != nil {
		return ucidomain.SemanticStoreResult{}, err
	}
	var coverageRow uciSemanticCoverageRow
	if err := s.db.WithContext(ctx).Raw(coverageQuery, coverageArguments...).Scan(&coverageRow).Error; err != nil {
		return ucidomain.SemanticStoreResult{}, fmt.Errorf("uci projection select semantic coverage: %w", err)
	}
	if coverageRow.TotalCandidates < 0 || coverageRow.CompatibleCandidates < 0 || coverageRow.CompatibleCandidates > coverageRow.TotalCandidates {
		return ucidomain.SemanticStoreResult{}, fmt.Errorf("uci projection select semantic coverage: invalid counts")
	}
	vectorCoverage := float64(1)
	if coverageRow.TotalCandidates != 0 {
		vectorCoverage = float64(coverageRow.CompatibleCandidates) / float64(coverageRow.TotalCandidates)
	}
	result := ucidomain.SemanticStoreResult{
		Coverage:       coverage,
		VectorCoverage: vectorCoverage,
	}
	if vectorCoverage < 1 {
		return result, nil
	}

	query, arguments, err := buildUCISemanticCandidatesSQL(ref, profile, vector, spec)
	if err != nil {
		return ucidomain.SemanticStoreResult{}, err
	}
	var rows []uciQueryCandidateRow
	if err := s.db.WithContext(ctx).Raw(query, arguments...).Scan(&rows).Error; err != nil {
		return ucidomain.SemanticStoreResult{}, fmt.Errorf("uci projection select semantic candidates: %w", err)
	}
	result.Candidates = make([]ucidomain.QueryCandidate, 0, len(rows))
	for _, row := range rows {
		candidate, ok := row.queryCandidate(ref)
		if ok {
			result.Candidates = append(result.Candidates, candidate)
		}
	}
	return result, nil
}

type uciSemanticVectorRow struct {
	Vector pgvector.Vector `gorm:"column:vector"`
}

type uciSemanticCurrentCandidateRow struct {
	ChunkID          string `gorm:"column:chunk_id"`
	ProtectionDomain string `gorm:"column:protection_domain"`
}

type uciSemanticCoverageRow struct {
	TotalCandidates      int64 `gorm:"column:total_candidates"`
	CompatibleCandidates int64 `gorm:"column:compatible_candidates"`
}

func (s *UCIProjectionStore) loadUCISemanticCurrentCandidate(ctx context.Context, ref ucidomain.ContextRef, candidate ucidomain.QueryCandidate) (uciSemanticCurrentCandidateRow, bool, error) {
	cte, arguments := buildUCISemanticCurrentCandidateCTE(ref, candidate)
	var row uciSemanticCurrentCandidateRow
	result := s.db.WithContext(ctx).Raw(cte+`
		SELECT chunk_id, protection_domain
		FROM scoped_candidate
		LIMIT 1`, arguments...).Scan(&row)
	if result.Error != nil {
		return uciSemanticCurrentCandidateRow{}, false, fmt.Errorf("uci projection load semantic current candidate: %w", result.Error)
	}
	return row, result.RowsAffected != 0, nil
}

func buildUCISemanticLookupSQL(ref ucidomain.ContextRef, profile ucidomain.VectorProfile, candidate ucidomain.QueryCandidate, inputDigest ucidomain.IndexDigest) (string, []any) {
	cte, arguments := buildUCISemanticCurrentCandidateCTE(ref, candidate)
	arguments = append(arguments,
		profile.ProviderRef,
		profile.Model,
		profile.Dimension,
		profile.PreprocessingRevision,
		profile.IncludeRelativePath,
		uciSemanticPathIndependentFingerprint,
		string(inputDigest),
		string(inputDigest),
		UCIEmbeddingReady,
	)
	return cte + `
		SELECT embedding.vector
		FROM scoped_candidate AS candidate
		JOIN ci_embedding_profiles AS profile_row
			ON profile_row.analysis_profile_id = candidate.analysis_profile_id
			AND profile_row.provider_ref = ?
			AND profile_row.model = ?
			AND profile_row.dimension = ?
			AND profile_row.preprocessing_revision = ?
			AND profile_row.include_relative_path = ?
		JOIN ci_chunk_embeddings AS chunk_embedding
			ON chunk_embedding.source_id = candidate.source_id
			AND chunk_embedding.chunk_id = candidate.chunk_id
			AND chunk_embedding.relative_path_fingerprint = CASE WHEN profile_row.include_relative_path THEN candidate.relative_path ELSE ? END
			AND chunk_embedding.embedding_profile_id = profile_row.embedding_profile_id
			AND chunk_embedding.embedding_input_digest = ?
		JOIN ci_embeddings AS embedding
			ON embedding.source_id = candidate.source_id
			AND embedding.embedding_id = chunk_embedding.embedding_id
			AND embedding.embedding_profile_id = profile_row.embedding_profile_id
			AND embedding.embedding_input_digest = ?
			AND embedding.protection_domain = candidate.protection_domain
		WHERE embedding.status = ?
			AND embedding.vector IS NOT NULL
		ORDER BY embedding.embedding_id ASC
		LIMIT 1`, arguments
}

func buildUCISemanticCurrentCandidateCTE(ref ucidomain.ContextRef, candidate ucidomain.QueryCandidate) (string, []any) {
	arguments := []any{
		ref.ViewID,
		ref.SourceID,
		ref.CheckoutID,
		ref.AnalysisProfileID,
		ref.Generation,
		UCIViewPublished,
		UCIViewSuperseded,
		UCIBlobStored,
		UCIFilePresent,
		UCIParseArtifactComplete,
		UCIParseArtifactPartial,
		candidate.Proof.ArtifactID,
		string(candidate.Proof.ContentDigest),
		string(candidate.Proof.FactsDigest),
		candidate.RelativePath,
		candidate.Span.ByteStart,
		candidate.Span.ByteEnd,
		candidate.EntityKey,
		candidate.Language,
		candidate.Text,
	}
	return `
		WITH selected_view AS (
			SELECT
				view_row.source_id,
				view_row.checkout_id,
				view_row.generation,
				view_row.profile_id AS analysis_profile_id,
				profile.parser_bundle_digest
			FROM ci_views AS view_row
			JOIN ci_profiles AS profile ON profile.profile_id = view_row.profile_id
			WHERE view_row.view_id = ?
				AND view_row.source_id = ?
				AND view_row.checkout_id = ?
				AND view_row.profile_id = ?
				AND view_row.generation = ?
				AND view_row.state IN (?, ?)
		),
		scoped_candidate AS (
			SELECT
				view_row.source_id,
				view_row.analysis_profile_id,
				chunk.chunk_id,
				membership.display_path AS relative_path,
				blob.protection_domain
			FROM selected_view AS view_row
			JOIN ci_memberships AS membership
				ON membership.source_id = view_row.source_id
				AND membership.checkout_id = view_row.checkout_id
				AND membership.valid_from_generation <= view_row.generation
				AND (membership.valid_to_generation IS NULL OR membership.valid_to_generation > view_row.generation)
			JOIN ci_parse_artifacts AS artifact
				ON artifact.source_id = membership.source_id
				AND artifact.artifact_id = membership.artifact_id
				AND artifact.extraction_profile_digest = view_row.parser_bundle_digest
			JOIN ci_blobs AS blob
				ON blob.source_id = artifact.source_id
				AND blob.blob_id = artifact.blob_id
			JOIN ci_chunks AS chunk
				ON chunk.source_id = artifact.source_id
				AND chunk.artifact_id = artifact.artifact_id
			LEFT JOIN ci_definitions AS definition
				ON definition.artifact_id = chunk.artifact_id
				AND definition.local_symbol_key = chunk.symbol_key
			WHERE blob.storage_state = ?
				AND membership.file_state = ?
				AND artifact.status IN (?, ?)
				AND artifact.sealed_at IS NOT NULL
				AND artifact.facts_digest IS NOT NULL
				AND artifact.artifact_id = ?
				AND blob.content_digest = ?
				AND artifact.facts_digest = ?
				AND membership.display_path = ?
				AND chunk.byte_start = ?
				AND chunk.byte_end = ?
				AND COALESCE(NULLIF(definition.qualified_local_name, ''), NULLIF(chunk.symbol_key, ''), membership.path_key || ':' || chunk.ordinal::text) = ?
				AND artifact.language = ?
				AND chunk.text_for_search = ?
		)
`, arguments
}

func buildUCISemanticScopedCandidatesSQL(ref ucidomain.ContextRef, spec ucidomain.QuerySpec) (string, []any, error) {
	conditions := []string{
		"blob.storage_state = ?",
		"membership.file_state = ?",
		"artifact.status IN (?, ?)",
		"artifact.sealed_at IS NOT NULL",
		"artifact.facts_digest IS NOT NULL",
	}
	predicateArguments := []any{UCIBlobStored, UCIFilePresent, UCIParseArtifactComplete, UCIParseArtifactPartial}
	if len(spec.Filter.Languages) != 0 {
		placeholders := make([]string, len(spec.Filter.Languages))
		for index, language := range spec.Filter.Languages {
			placeholders[index] = "?"
			predicateArguments = append(predicateArguments, language)
		}
		conditions = append(conditions, "artifact.language IN ("+strings.Join(placeholders, ", ")+")")
	}
	if prefix := spec.Filter.PathPrefix; prefix != "" {
		conditions = append(conditions, `(membership.display_path = ? OR membership.display_path LIKE ? ESCAPE '\')`)
		predicateArguments = append(predicateArguments, prefix, escapeUCIQueryLike(prefix)+"/%")
	}
	arguments := []any{
		ref.ViewID,
		ref.SourceID,
		ref.CheckoutID,
		ref.AnalysisProfileID,
		ref.Generation,
		UCIViewPublished,
		UCIViewSuperseded,
		uciQueryStoreMaxExcerptBytes,
	}
	arguments = append(arguments, predicateArguments...)
	return `
		WITH selected_view AS (
			SELECT
				view_row.source_id,
				view_row.checkout_id,
				view_row.generation,
				view_row.profile_id AS analysis_profile_id,
				profile.parser_bundle_digest
			FROM ci_views AS view_row
			JOIN ci_profiles AS profile ON profile.profile_id = view_row.profile_id
			WHERE view_row.view_id = ?
				AND view_row.source_id = ?
				AND view_row.checkout_id = ?
				AND view_row.profile_id = ?
				AND view_row.generation = ?
				AND view_row.state IN (?, ?)
		),
		scoped_candidates AS (
			SELECT
				view_row.source_id,
				view_row.checkout_id,
				view_row.analysis_profile_id,
				artifact.artifact_id,
				blob.content_digest AS chunk_content_digest,
				artifact.facts_digest,
				(SELECT COUNT(*) FROM ci_definitions AS proof_definition WHERE proof_definition.artifact_id = artifact.artifact_id) AS definition_count,
				(SELECT COUNT(*) FROM ci_reference_sites AS proof_reference WHERE proof_reference.artifact_id = artifact.artifact_id) AS reference_site_count,
				(SELECT COUNT(*) FROM ci_chunks AS proof_chunk WHERE proof_chunk.artifact_id = artifact.artifact_id) AS chunk_count,
				COALESCE(NULLIF(definition.qualified_local_name, ''), NULLIF(chunk.symbol_key, ''), membership.path_key || ':' || chunk.ordinal::text) AS entity_key,
				COALESCE(definition.name, '') AS local_name,
				COALESCE(definition.qualified_local_name, '') AS qualified_symbol,
				membership.display_path AS relative_path,
				chunk.byte_start,
				chunk.byte_end,
				array_length(regexp_split_to_array(
					convert_from(substring(blob.safe_content FROM 1 FOR chunk.byte_start::integer), replace(upper(blob.encoding), '-', '')),
					E'\n'
				), 1) AS line_start,
				array_length(regexp_split_to_array(
					convert_from(substring(blob.safe_content FROM 1 FOR chunk.byte_end::integer), replace(upper(blob.encoding), '-', '')),
					E'\n'
				), 1) AS line_end,
				CASE WHEN octet_length(chunk.text_for_search) <= ? THEN chunk.text_for_search ELSE '' END AS text,
				chunk.chunk_kind,
				artifact.language,
				chunk.chunk_id,
				blob.protection_domain
			FROM selected_view AS view_row
			JOIN ci_memberships AS membership
				ON membership.source_id = view_row.source_id
				AND membership.checkout_id = view_row.checkout_id
				AND membership.valid_from_generation <= view_row.generation
				AND (membership.valid_to_generation IS NULL OR membership.valid_to_generation > view_row.generation)
			JOIN ci_parse_artifacts AS artifact
				ON artifact.source_id = membership.source_id
				AND artifact.artifact_id = membership.artifact_id
				AND artifact.extraction_profile_digest = view_row.parser_bundle_digest
			JOIN ci_blobs AS blob
				ON blob.source_id = artifact.source_id
				AND blob.blob_id = artifact.blob_id
			JOIN ci_chunks AS chunk
				ON chunk.source_id = artifact.source_id
				AND chunk.artifact_id = artifact.artifact_id
			LEFT JOIN ci_definitions AS definition
				ON definition.artifact_id = chunk.artifact_id
				AND definition.local_symbol_key = chunk.symbol_key
			WHERE ` + strings.Join(conditions, "\n\t\t\t\tAND ") + `
		)
`, arguments, nil
}

func buildUCISemanticCoverageSQL(ref ucidomain.ContextRef, profile ucidomain.VectorProfile, spec ucidomain.QuerySpec) (string, []any, error) {
	cte, arguments, err := buildUCISemanticScopedCandidatesSQL(ref, spec)
	if err != nil {
		return "", nil, err
	}
	arguments = append(arguments,
		profile.ProviderRef,
		profile.Model,
		profile.Dimension,
		profile.PreprocessingRevision,
		profile.IncludeRelativePath,
		uciSemanticPathIndependentFingerprint,
		UCIEmbeddingReady,
	)
	return cte + `
		SELECT
			COUNT(*) AS total_candidates,
			COUNT(*) FILTER (WHERE EXISTS (
				SELECT 1
				FROM ci_embedding_profiles AS profile_row
				JOIN ci_chunk_embeddings AS chunk_embedding
					ON chunk_embedding.source_id = candidate.source_id
					AND chunk_embedding.chunk_id = candidate.chunk_id
					AND chunk_embedding.relative_path_fingerprint = CASE WHEN profile_row.include_relative_path THEN candidate.relative_path ELSE ? END
					AND chunk_embedding.embedding_profile_id = profile_row.embedding_profile_id
				JOIN ci_embeddings AS embedding
					ON embedding.source_id = candidate.source_id
					AND embedding.embedding_id = chunk_embedding.embedding_id
					AND embedding.embedding_profile_id = profile_row.embedding_profile_id
					AND embedding.embedding_input_digest = chunk_embedding.embedding_input_digest
					AND embedding.protection_domain = candidate.protection_domain
					AND embedding.status = ?
					AND embedding.vector IS NOT NULL
				WHERE profile_row.analysis_profile_id = candidate.analysis_profile_id
					AND profile_row.provider_ref = ?
					AND profile_row.model = ?
					AND profile_row.dimension = ?
					AND profile_row.preprocessing_revision = ?
					AND profile_row.include_relative_path = ?
			)) AS compatible_candidates
		FROM scoped_candidates AS candidate`, append(arguments[:len(arguments)-7],
			uciSemanticPathIndependentFingerprint,
			UCIEmbeddingReady,
			profile.ProviderRef,
			profile.Model,
			profile.Dimension,
			profile.PreprocessingRevision,
			profile.IncludeRelativePath,
		), nil
}

func buildUCISemanticCandidatesSQL(ref ucidomain.ContextRef, profile ucidomain.VectorProfile, vector []float32, spec ucidomain.QuerySpec) (string, []any, error) {
	cte, scopeArguments, err := buildUCISemanticScopedCandidatesSQL(ref, spec)
	if err != nil {
		return "", nil, err
	}
	arguments := make([]any, 0, len(scopeArguments)+10)
	arguments = append(arguments, pgvector.NewVector(vector))
	arguments = append(arguments, scopeArguments...)
	arguments = append(arguments,
		profile.ProviderRef,
		profile.Model,
		profile.Dimension,
		profile.PreprocessingRevision,
		profile.IncludeRelativePath,
		uciSemanticPathIndependentFingerprint,
		UCIEmbeddingReady,
		spec.Limit+1,
		spec.Offset,
	)
	scopedCTE := strings.TrimPrefix(strings.TrimSpace(cte), "WITH ")
	return `
		WITH query_vector AS (SELECT ?::vector AS vector), ` + scopedCTE + `
		SELECT
			candidate.artifact_id,
			candidate.chunk_content_digest,
			candidate.facts_digest,
			candidate.definition_count,
			candidate.reference_site_count,
			candidate.chunk_count,
			candidate.entity_key,
			candidate.local_name,
			candidate.qualified_symbol,
			candidate.relative_path,
			candidate.byte_start,
			candidate.byte_end,
			candidate.line_start,
			candidate.line_end,
			candidate.text,
			candidate.chunk_kind,
			candidate.language,
			1 - (embedding.vector <=> query_vector.vector) AS score
		FROM scoped_candidates AS candidate
		JOIN ci_embedding_profiles AS profile_row
			ON profile_row.analysis_profile_id = candidate.analysis_profile_id
			AND profile_row.provider_ref = ?
			AND profile_row.model = ?
			AND profile_row.dimension = ?
			AND profile_row.preprocessing_revision = ?
			AND profile_row.include_relative_path = ?
		JOIN ci_chunk_embeddings AS chunk_embedding
			ON chunk_embedding.source_id = candidate.source_id
			AND chunk_embedding.chunk_id = candidate.chunk_id
			AND chunk_embedding.relative_path_fingerprint = CASE WHEN profile_row.include_relative_path THEN candidate.relative_path ELSE ? END
			AND chunk_embedding.embedding_profile_id = profile_row.embedding_profile_id
		JOIN ci_embeddings AS embedding
			ON embedding.source_id = candidate.source_id
			AND embedding.embedding_id = chunk_embedding.embedding_id
			AND embedding.embedding_profile_id = profile_row.embedding_profile_id
			AND embedding.embedding_input_digest = chunk_embedding.embedding_input_digest
			AND embedding.protection_domain = candidate.protection_domain
			AND embedding.status = ?
			AND embedding.vector IS NOT NULL
		CROSS JOIN query_vector
		ORDER BY
			embedding.vector <=> query_vector.vector ASC,
			candidate.relative_path ASC,
			candidate.byte_start ASC,
			candidate.entity_key ASC,
			candidate.chunk_id ASC
		LIMIT ? OFFSET ?`, arguments, nil
}

func validateUCISemanticCandidate(ref ucidomain.ContextRef, profile ucidomain.VectorProfile, candidate ucidomain.QueryCandidate) error {
	if err := validateUCIQueryContext(ref); err != nil {
		return err
	}
	if err := validateUCISemanticProfile(profile); err != nil {
		return err
	}
	if !sameUCISemanticContext(ref, candidate.Context) {
		return fmt.Errorf("uci projection semantic candidate: candidate context does not match authorized context")
	}
	if _, _, err := ucidomain.SemanticEmbeddingInput(profile, candidate); err != nil {
		return fmt.Errorf("uci projection semantic candidate: %w", err)
	}
	return nil
}

func validateUCISemanticProfile(profile ucidomain.VectorProfile) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"provider_ref", profile.ProviderRef},
		{"model", profile.Model},
		{"preprocessing_revision", profile.PreprocessingRevision},
	} {
		if err := validateUCIRequiredText(field.name, field.value); err != nil {
			return err
		}
	}
	if profile.Dimension != 1536 {
		return fmt.Errorf("uci projection semantic profile: dimension must be 1536")
	}
	return nil
}

func validateUCISemanticVector(vector []float32, dimension int) error {
	if len(vector) != dimension {
		return fmt.Errorf("vector dimension = %d, want %d", len(vector), dimension)
	}
	for index, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("vector contains non-finite value at index %d", index)
		}
	}
	return nil
}

func sameUCISemanticContext(left, right ucidomain.ContextRef) bool {
	return sameUCIOptionalString(left.SpaceID, right.SpaceID) &&
		left.SourceID == right.SourceID &&
		left.CheckoutID == right.CheckoutID &&
		left.ViewID == right.ViewID &&
		left.AnalysisProfileID == right.AnalysisProfileID &&
		left.Generation == right.Generation
}

func uciSemanticRelativePathFingerprint(profile ucidomain.VectorProfile, relativePath string) string {
	if !profile.IncludeRelativePath {
		return uciSemanticPathIndependentFingerprint
	}
	return relativePath
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

const uciQueryStoreMaxExcerptBytes = 8 << 10

// SelectCandidates implements the UCI query store port. The selected_view CTE
// binds every candidate predicate to the exact authorized source, checkout,
// view, generation, and analysis profile before any ranking or limiting occurs.
func (s *UCIProjectionStore) SelectCandidates(ctx context.Context, authorized ucidomain.AuthorizedContext, spec ucidomain.QuerySpec) (ucidomain.QueryStoreResult, error) {
	if err := s.requireDB("select query candidates"); err != nil {
		return ucidomain.QueryStoreResult{}, err
	}
	ref := authorized.Ref()
	if err := validateUCIQueryContext(ref); err != nil {
		return ucidomain.QueryStoreResult{}, err
	}
	if err := validateUCIQuerySpec(spec); err != nil {
		return ucidomain.QueryStoreResult{}, err
	}

	metadata, found, err := s.loadUCIQueryViewMetadata(ctx, ref)
	if err != nil {
		return ucidomain.QueryStoreResult{}, err
	}
	if !found || metadata.State == UCIViewRetired || metadata.State == UCIViewStaging {
		return uciUnavailableQueryResult(ucidomain.QueryErrorBuildIncomplete), nil
	}
	coverage, ok := parseUCIQueryCoverage(metadata.Structural)
	if !ok || coverage == ucidomain.IndexCoverageUnavailable {
		return uciUnavailableQueryResult(ucidomain.QueryErrorBuildIncomplete), nil
	}
	if spec.Mode == ucidomain.QueryModeFTS {
		lexical, lexicalOK := parseUCIQueryCoverage(metadata.Lexical)
		if !lexicalOK || lexical == ucidomain.IndexCoverageUnavailable {
			return uciUnavailableQueryResult(ucidomain.QueryErrorParserUnsupported), nil
		}
		if lexical == ucidomain.IndexCoveragePartial && coverage == ucidomain.IndexCoverageComplete {
			coverage = ucidomain.IndexCoveragePartial
		}
	}

	query, arguments, err := buildUCIQueryCandidatesSQL(ref, spec)
	if err != nil {
		return ucidomain.QueryStoreResult{}, err
	}
	var rows []uciQueryCandidateRow
	if err := s.db.WithContext(ctx).Raw(query, arguments...).Scan(&rows).Error; err != nil {
		return ucidomain.QueryStoreResult{}, fmt.Errorf("uci projection select query candidates: %w", err)
	}

	candidates := make([]ucidomain.QueryCandidate, 0, len(rows))
	for _, row := range rows {
		candidate, ok := row.queryCandidate(ref)
		if !ok {
			continue
		}
		candidates = append(candidates, candidate)
	}
	return ucidomain.QueryStoreResult{
		Candidates: candidates,
		Coverage:   coverage,
	}, nil
}

type uciQueryViewMetadata struct {
	State      UCIViewState `gorm:"column:state"`
	Structural string       `gorm:"column:structural"`
	Lexical    string       `gorm:"column:lexical"`
	Vector     string       `gorm:"column:vector"`
}

func (s *UCIProjectionStore) loadUCIQueryViewMetadata(ctx context.Context, ref ucidomain.ContextRef) (uciQueryViewMetadata, bool, error) {
	var metadata uciQueryViewMetadata
	result := s.db.WithContext(ctx).Raw(`
		SELECT
			view_row.state,
			COALESCE(view_row.coverage_json->>'structural', '') AS structural,
			COALESCE(view_row.coverage_json->>'lexical', '') AS lexical,
			COALESCE(view_row.coverage_json->>'vector', '') AS vector
		FROM ci_views AS view_row
		WHERE view_row.view_id = ?
			AND view_row.source_id = ?
			AND view_row.checkout_id = ?
			AND view_row.profile_id = ?
			AND view_row.generation = ?
	`, ref.ViewID, ref.SourceID, ref.CheckoutID, ref.AnalysisProfileID, ref.Generation).Scan(&metadata)
	if result.Error != nil {
		return uciQueryViewMetadata{}, false, fmt.Errorf("uci projection load query view: %w", result.Error)
	}
	return metadata, result.RowsAffected != 0, nil
}

type uciQueryCandidateRow struct {
	ArtifactID         string  `gorm:"column:artifact_id"`
	ChunkContentDigest string  `gorm:"column:chunk_content_digest"`
	FactsDigest        string  `gorm:"column:facts_digest"`
	DefinitionCount    int64   `gorm:"column:definition_count"`
	ReferenceSiteCount int64   `gorm:"column:reference_site_count"`
	ChunkCount         int64   `gorm:"column:chunk_count"`
	EntityKey          string  `gorm:"column:entity_key"`
	LocalName          string  `gorm:"column:local_name"`
	QualifiedSymbol    string  `gorm:"column:qualified_symbol"`
	RelativePath       string  `gorm:"column:relative_path"`
	ByteStart          int64   `gorm:"column:byte_start"`
	ByteEnd            int64   `gorm:"column:byte_end"`
	LineStart          int     `gorm:"column:line_start"`
	LineEnd            int     `gorm:"column:line_end"`
	Text               string  `gorm:"column:text"`
	ChunkKind          string  `gorm:"column:chunk_kind"`
	Language           string  `gorm:"column:language"`
	Score              float64 `gorm:"column:score"`
}

func (row uciQueryCandidateRow) queryCandidate(ref ucidomain.ContextRef) (ucidomain.QueryCandidate, bool) {
	if validateUCIUUID("artifact_id", row.ArtifactID) != nil || row.ByteStart < 0 || row.ByteEnd <= row.ByteStart || row.LineStart < 1 || row.LineEnd < row.LineStart ||
		row.EntityKey == "" || row.RelativePath == "" || row.Language == "" || !isUCIDigest(row.ChunkContentDigest) {
		return ucidomain.QueryCandidate{}, false
	}
	if row.DefinitionCount < 0 || row.ReferenceSiteCount < 0 || row.ChunkCount < 0 {
		return ucidomain.QueryCandidate{}, false
	}
	return ucidomain.QueryCandidate{
		Context: ref,
		Proof: ucidomain.IndexArtifactProof{
			ArtifactID:         row.ArtifactID,
			ContentDigest:      ucidomain.IndexDigest(row.ChunkContentDigest),
			FactsDigest:        ucidomain.IndexDigest(row.FactsDigest),
			DefinitionCount:    uint64(row.DefinitionCount),
			ReferenceSiteCount: uint64(row.ReferenceSiteCount),
			ChunkCount:         uint64(row.ChunkCount),
		},
		EntityKey:       row.EntityKey,
		LocalName:       row.LocalName,
		QualifiedSymbol: row.QualifiedSymbol,
		RelativePath:    row.RelativePath,
		Span: ucidomain.IndexSpan{
			ByteStart: row.ByteStart,
			ByteEnd:   row.ByteEnd,
			LineStart: row.LineStart,
			LineEnd:   row.LineEnd,
		},
		Text:     row.Text,
		Kind:     uciQueryItemKind(row.ChunkKind),
		Language: row.Language,
		Score:    row.Score,
	}, true
}

func buildUCIQueryCandidatesSQL(ref ucidomain.ContextRef, spec ucidomain.QuerySpec) (string, []any, error) {
	cteArguments := []any{
		ref.ViewID,
		ref.SourceID,
		ref.CheckoutID,
		ref.AnalysisProfileID,
		ref.Generation,
		UCIViewPublished,
		UCIViewSuperseded,
	}
	scoreArguments := []any(nil)
	excerptArguments := []any{uciQueryStoreMaxExcerptBytes}
	predicateArguments := []any{UCIBlobStored, UCIFilePresent, UCIParseArtifactComplete, UCIParseArtifactPartial}
	score := "0::double precision AS score"
	conditions := []string{
		"blob.storage_state = ?",
		"membership.file_state = ?",
		"artifact.status IN (?, ?)",
		"artifact.sealed_at IS NOT NULL",
		"artifact.facts_digest IS NOT NULL",
	}
	if prefix := spec.Filter.PathPrefix; prefix != "" {
		conditions = append(conditions, `(membership.display_path = ? OR membership.display_path LIKE ? ESCAPE '\')`)
		predicateArguments = append(predicateArguments, prefix, escapeUCIQueryLike(prefix)+"/%")
	}

	if len(spec.Filter.Languages) != 0 {
		placeholders := make([]string, len(spec.Filter.Languages))
		for index, language := range spec.Filter.Languages {
			placeholders[index] = "?"
			predicateArguments = append(predicateArguments, language)
		}
		conditions = append(conditions, "artifact.language IN ("+strings.Join(placeholders, ", ")+")")
	}

	switch spec.Mode {
	case ucidomain.QueryModeExactLocalName:
		conditions = append(conditions, "definition.name = ?")
		predicateArguments = append(predicateArguments, spec.Text)
	case ucidomain.QueryModeExactQualifiedSymbol:
		conditions = append(conditions, "definition.qualified_local_name = ?")
		predicateArguments = append(predicateArguments, spec.Text)
	case ucidomain.QueryModeExactRelativePath:
		conditions = append(conditions, "membership.display_path = ?")
		predicateArguments = append(predicateArguments, spec.Text)
	case ucidomain.QueryModeFTS:
		terms := spec.FTSTerms()
		if len(terms) == 0 {
			return "", nil, fmt.Errorf("uci projection query FTS: no searchable terms")
		}
		score = "ts_rank_cd(chunk.content_tsv, plainto_tsquery('simple', ?)) AS score"
		scoreArguments = append(scoreArguments, spec.Text)
		for _, term := range terms {
			pattern := "%" + escapeUCIQueryLike(term) + "%"
			conditions = append(conditions, `(
				chunk.content_tsv @@ plainto_tsquery('simple', ?)
				OR chunk.text_for_search ILIKE ? ESCAPE '\'
				OR COALESCE(definition.name, '') ILIKE ? ESCAPE '\'
				OR COALESCE(definition.qualified_local_name, '') ILIKE ? ESCAPE '\'
				OR membership.display_path ILIKE ? ESCAPE '\'
			)`)
			predicateArguments = append(predicateArguments, term, pattern, pattern, pattern, pattern)
		}
	default:
		return "", nil, fmt.Errorf("uci projection query: unsupported mode %q", spec.Mode)
	}

	order := "membership.display_path ASC, chunk.byte_start ASC, entity_key ASC, chunk.chunk_id ASC"
	if spec.Order == ucidomain.QueryOrderRelevance {
		order = "score DESC, membership.display_path ASC, chunk.byte_start ASC, entity_key ASC, chunk.chunk_id ASC"
	}
	arguments := append(cteArguments, excerptArguments...)
	arguments = append(arguments, scoreArguments...)
	arguments = append(arguments, predicateArguments...)
	arguments = append(arguments, spec.Limit+1, spec.Offset)
	query := `
		WITH selected_view AS (
			SELECT
				view_row.view_id,
				view_row.source_id,
				view_row.checkout_id,
				view_row.generation,
				view_row.profile_id,
				profile.parser_bundle_digest
			FROM ci_views AS view_row
			JOIN ci_profiles AS profile ON profile.profile_id = view_row.profile_id
			WHERE view_row.view_id = ?
				AND view_row.source_id = ?
				AND view_row.checkout_id = ?
				AND view_row.profile_id = ?
				AND view_row.generation = ?
				AND view_row.state IN (?, ?)
		)
		SELECT
			artifact.artifact_id,
			blob.content_digest AS chunk_content_digest,
			artifact.facts_digest,
			(SELECT COUNT(*) FROM ci_definitions AS proof_definition WHERE proof_definition.artifact_id = artifact.artifact_id) AS definition_count,
			(SELECT COUNT(*) FROM ci_reference_sites AS proof_reference WHERE proof_reference.artifact_id = artifact.artifact_id) AS reference_site_count,
			(SELECT COUNT(*) FROM ci_chunks AS proof_chunk WHERE proof_chunk.artifact_id = artifact.artifact_id) AS chunk_count,
			COALESCE(NULLIF(definition.qualified_local_name, ''), NULLIF(chunk.symbol_key, ''), membership.path_key || ':' || chunk.ordinal::text) AS entity_key,
			COALESCE(definition.name, '') AS local_name,
			COALESCE(definition.qualified_local_name, '') AS qualified_symbol,
			membership.display_path AS relative_path,
			chunk.byte_start,
			chunk.byte_end,
			array_length(regexp_split_to_array(
				convert_from(substring(blob.safe_content FROM 1 FOR chunk.byte_start::integer), replace(upper(blob.encoding), '-', '')),
				E'\n'
			), 1) AS line_start,
			array_length(regexp_split_to_array(
				convert_from(substring(blob.safe_content FROM 1 FOR chunk.byte_end::integer), replace(upper(blob.encoding), '-', '')),
				E'\n'
			), 1) AS line_end,
			CASE WHEN octet_length(chunk.text_for_search) <= ? THEN chunk.text_for_search ELSE '' END AS text,
			chunk.chunk_kind,
			artifact.language,
			` + score + `
		FROM selected_view AS view_row
		JOIN ci_memberships AS membership
			ON membership.source_id = view_row.source_id
			AND membership.checkout_id = view_row.checkout_id
			AND membership.valid_from_generation <= view_row.generation
			AND (membership.valid_to_generation IS NULL OR membership.valid_to_generation > view_row.generation)
		JOIN ci_parse_artifacts AS artifact
			ON artifact.source_id = membership.source_id
			AND artifact.artifact_id = membership.artifact_id
			AND artifact.extraction_profile_digest = view_row.parser_bundle_digest
		JOIN ci_blobs AS blob
			ON blob.source_id = artifact.source_id
			AND blob.blob_id = artifact.blob_id
		JOIN ci_chunks AS chunk
			ON chunk.source_id = artifact.source_id
			AND chunk.artifact_id = artifact.artifact_id
		LEFT JOIN ci_definitions AS definition
			ON definition.artifact_id = chunk.artifact_id
			AND definition.local_symbol_key = chunk.symbol_key
		WHERE ` + strings.Join(conditions, "\n\t\t\tAND ") + `
		ORDER BY ` + order + `
		LIMIT ? OFFSET ?`
	return query, arguments, nil
}

func validateUCIQueryContext(ref ucidomain.ContextRef) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"source_id", ref.SourceID},
		{"checkout_id", ref.CheckoutID},
		{"view_id", ref.ViewID},
		{"analysis_profile_id", ref.AnalysisProfileID},
	} {
		if err := validateUCIUUID(field.name, field.value); err != nil {
			return err
		}
	}
	if ref.SpaceID != nil {
		if err := validateUCIUUID("space_id", *ref.SpaceID); err != nil {
			return err
		}
	}
	if ref.Generation < 1 {
		return fmt.Errorf("uci projection query: generation must be positive")
	}
	return nil
}

func validateUCIQuerySpec(spec ucidomain.QuerySpec) error {
	if spec.Limit < 1 || spec.Limit > 50 || spec.Offset < 0 || strings.TrimSpace(spec.Text) == "" || strings.TrimSpace(spec.Text) != spec.Text {
		return fmt.Errorf("uci projection query: invalid request")
	}
	switch spec.Mode {
	case ucidomain.QueryModeExactLocalName, ucidomain.QueryModeExactQualifiedSymbol, ucidomain.QueryModeExactRelativePath, ucidomain.QueryModeFTS:
	default:
		return fmt.Errorf("uci projection query: unsupported mode %q", spec.Mode)
	}
	switch spec.Order {
	case ucidomain.QueryOrderPath, ucidomain.QueryOrderRelevance:
	default:
		return fmt.Errorf("uci projection query: unsupported order %q", spec.Order)
	}
	if len(spec.Filter.Languages) > 32 {
		return fmt.Errorf("uci projection query: language filter exceeds limit")
	}
	for _, language := range spec.Filter.Languages {
		if strings.TrimSpace(language) == "" || strings.TrimSpace(language) != language {
			return fmt.Errorf("uci projection query: invalid language filter")
		}
	}
	normalizedPathPrefix, err := ucidomain.NormalizeQueryPathPrefix(spec.Filter.PathPrefix)
	if err != nil || normalizedPathPrefix != spec.Filter.PathPrefix {
		return fmt.Errorf("uci projection query: invalid path prefix")
	}
	return nil
}

func parseUCIQueryCoverage(value string) (ucidomain.IndexCoverageState, bool) {
	switch ucidomain.IndexCoverageState(value) {
	case ucidomain.IndexCoverageComplete:
		return ucidomain.IndexCoverageComplete, true
	case ucidomain.IndexCoveragePartial:
		return ucidomain.IndexCoveragePartial, true
	case ucidomain.IndexCoverageUnavailable:
		return ucidomain.IndexCoverageUnavailable, true
	default:
		return "", false
	}
}

func uciUnavailableQueryResult(code ucidomain.QueryErrorCode) ucidomain.QueryStoreResult {
	return ucidomain.QueryStoreResult{
		Coverage:    ucidomain.IndexCoverageUnavailable,
		Unavailable: &ucidomain.QueryError{Code: code},
	}
}

func uciQueryItemKind(chunkKind string) ucidomain.QueryItemKind {
	switch strings.ToLower(chunkKind) {
	case "document", "doc":
		return ucidomain.QueryItemDocument
	case "schema":
		return ucidomain.QueryItemSchema
	case "config", "configuration":
		return ucidomain.QueryItemConfig
	default:
		return ucidomain.QueryItemCode
	}
}

func escapeUCIQueryLike(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return replacer.Replace(value)
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

func digestUCIBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func sameUCIBlobPayload(existing UCIBlob, candidate *UCIBlob) bool {
	return existing.SourceID == candidate.SourceID &&
		existing.ProtectionDomain == candidate.ProtectionDomain &&
		existing.ContentDigest == candidate.ContentDigest &&
		existing.ByteLength == candidate.ByteLength &&
		existing.Encoding == candidate.Encoding &&
		existing.StorageState == candidate.StorageState &&
		bytes.Equal(existing.SafeContent, candidate.SafeContent)
}

func (s *UCIProjectionStore) loadMutableUCIArtifact(ctx context.Context, artifactID string) (*UCIParseArtifact, error) {
	var artifact UCIParseArtifact
	if err := s.db.WithContext(ctx).Where("artifact_id = ?", artifactID).First(&artifact).Error; err != nil {
		return nil, fmt.Errorf("uci projection load artifact: %w", err)
	}
	if artifact.SealedAt != nil {
		return nil, errUCIProjectionImmutable
	}
	return &artifact, nil
}

// DescribeIndexArtifact recomputes the source-scoped proof for one admitted artifact.
// It is read-only and does not seal the artifact.
func (s *UCIProjectionStore) DescribeIndexArtifact(ctx context.Context, sourceID, artifactID string) (ucidomain.IndexArtifactProof, error) {
	if err := s.requireDB("describe index artifact"); err != nil {
		return ucidomain.IndexArtifactProof{}, err
	}
	if err := validateUCIUUID("source_id", sourceID); err != nil {
		return ucidomain.IndexArtifactProof{}, err
	}
	if err := validateUCIUUID("artifact_id", artifactID); err != nil {
		return ucidomain.IndexArtifactProof{}, err
	}

	var artifact UCIParseArtifact
	if err := s.db.WithContext(ctx).Where("source_id = ? AND artifact_id = ?", sourceID, artifactID).First(&artifact).Error; err != nil {
		return ucidomain.IndexArtifactProof{}, fmt.Errorf("uci projection describe artifact: %w", err)
	}
	var blob UCIBlob
	if err := s.db.WithContext(ctx).Where("source_id = ? AND blob_id = ?", sourceID, artifact.BlobID).First(&blob).Error; err != nil {
		return ucidomain.IndexArtifactProof{}, fmt.Errorf("uci projection describe blob: %w", err)
	}
	if blob.StorageState != UCIBlobStored || blob.SafeContent == nil || int64(len(blob.SafeContent)) != blob.ByteLength || digestUCIBytes(blob.SafeContent) != blob.ContentDigest {
		return ucidomain.IndexArtifactProof{}, errUCIProjectionImmutable
	}

	hash := sha256.New()
	header, err := json.Marshal(struct {
		Version                 int                    `json:"version"`
		Kind                    string                 `json:"kind"`
		SourceID                string                 `json:"source_id"`
		ArtifactID              string                 `json:"artifact_id"`
		BlobID                  string                 `json:"blob_id"`
		ContentDigest           string                 `json:"content_digest"`
		ByteLength              int64                  `json:"byte_length"`
		Language                string                 `json:"language"`
		ParserRevision          string                 `json:"parser_revision"`
		GrammarDigest           string                 `json:"grammar_digest"`
		ExtractionProfileDigest string                 `json:"extraction_profile_digest"`
		Status                  UCIParseArtifactStatus `json:"status"`
		Diagnostics             json.RawMessage        `json:"diagnostics"`
	}{
		Version:                 1,
		Kind:                    "artifact_facts",
		SourceID:                sourceID,
		ArtifactID:              artifact.ArtifactID,
		BlobID:                  artifact.BlobID,
		ContentDigest:           blob.ContentDigest,
		ByteLength:              blob.ByteLength,
		Language:                artifact.Language,
		ParserRevision:          artifact.ParserRevision,
		GrammarDigest:           artifact.GrammarDigest,
		ExtractionProfileDigest: artifact.ExtractionProfileDigest,
		Status:                  artifact.Status,
		Diagnostics:             json.RawMessage(artifact.Diagnostics),
	})
	if err != nil {
		return ucidomain.IndexArtifactProof{}, fmt.Errorf("uci projection encode artifact facts: %w", err)
	}
	_, _ = hash.Write(header)

	definitions, err := s.hashUCIDefinitions(ctx, hash, artifact.ArtifactID, blob.ByteLength)
	if err != nil {
		return ucidomain.IndexArtifactProof{}, err
	}
	references, err := s.hashUCIReferenceSites(ctx, hash, artifact.ArtifactID, blob.ByteLength)
	if err != nil {
		return ucidomain.IndexArtifactProof{}, err
	}
	chunks, err := s.hashUCIChunks(ctx, hash, artifact.ArtifactID, blob)
	if err != nil {
		return ucidomain.IndexArtifactProof{}, err
	}

	factsDigest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if artifact.SealedAt != nil && (artifact.FactsDigest == nil || *artifact.FactsDigest != factsDigest) {
		return ucidomain.IndexArtifactProof{}, errUCIProjectionImmutable
	}
	return ucidomain.IndexArtifactProof{
		ArtifactID:         artifact.ArtifactID,
		ContentDigest:      ucidomain.IndexDigest(blob.ContentDigest),
		FactsDigest:        ucidomain.IndexDigest(factsDigest),
		DefinitionCount:    definitions,
		ReferenceSiteCount: references,
		ChunkCount:         chunks,
	}, nil
}

func (s *UCIProjectionStore) hashUCIDefinitions(ctx context.Context, hash interface{ Write([]byte) (int, error) }, artifactID string, byteLength int64) (uint64, error) {
	if _, err := hash.Write([]byte("|definitions|")); err != nil {
		return 0, err
	}
	rows, err := s.db.WithContext(ctx).Where("artifact_id = ?", artifactID).Order("local_symbol_key ASC, definition_id ASC").Model(&UCIDefinition{}).Rows()
	if err != nil {
		return 0, fmt.Errorf("uci projection describe definitions: %w", err)
	}
	defer rows.Close()
	var count uint64
	for rows.Next() {
		var definition UCIDefinition
		if err := s.db.ScanRows(rows, &definition); err != nil {
			return 0, fmt.Errorf("uci projection scan definition: %w", err)
		}
		if err := validateUCIProjectionSpan(definition.ByteStart, definition.ByteEnd, definition.LineStart, definition.LineEnd); err != nil || definition.ByteEnd > byteLength {
			return 0, errUCIProjectionImmutable
		}
		encoded, err := json.Marshal(struct {
			LocalSymbolKey     string `json:"local_symbol_key"`
			Kind               string `json:"kind"`
			Name               string `json:"name"`
			QualifiedLocalName string `json:"qualified_local_name"`
			Signature          string `json:"signature"`
			ByteStart          int64  `json:"byte_start"`
			ByteEnd            int64  `json:"byte_end"`
			LineStart          int    `json:"line_start"`
			LineEnd            int    `json:"line_end"`
		}{
			LocalSymbolKey:     definition.LocalSymbolKey,
			Kind:               definition.Kind,
			Name:               definition.Name,
			QualifiedLocalName: definition.QualifiedLocalName,
			Signature:          definition.Signature,
			ByteStart:          definition.ByteStart,
			ByteEnd:            definition.ByteEnd,
			LineStart:          definition.LineStart,
			LineEnd:            definition.LineEnd,
		})
		if err != nil {
			return 0, err
		}
		_, _ = hash.Write(encoded)
		_, _ = hash.Write([]byte{'\n'})
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("uci projection iterate definitions: %w", err)
	}
	return count, nil
}

func (s *UCIProjectionStore) hashUCIReferenceSites(ctx context.Context, hash interface{ Write([]byte) (int, error) }, artifactID string, byteLength int64) (uint64, error) {
	if _, err := hash.Write([]byte("|reference_sites|")); err != nil {
		return 0, err
	}
	rows, err := s.db.WithContext(ctx).Where("artifact_id = ?", artifactID).Order("site_key ASC, reference_site_id ASC").Model(&UCIReferenceSite{}).Rows()
	if err != nil {
		return 0, fmt.Errorf("uci projection describe reference sites: %w", err)
	}
	defer rows.Close()
	var count uint64
	for rows.Next() {
		var reference UCIReferenceSite
		if err := s.db.ScanRows(rows, &reference); err != nil {
			return 0, fmt.Errorf("uci projection scan reference site: %w", err)
		}
		syntaxSpan, err := normalizeUCIArtifactJSONObject(reference.SyntaxSpan, byteLength)
		if err != nil {
			return 0, errUCIProjectionImmutable
		}
		resolverHints, err := normalizeUCIArtifactJSONObject(reference.ResolverHints, -1)
		if err != nil {
			return 0, errUCIProjectionImmutable
		}
		encoded, err := json.Marshal(struct {
			SiteKey        string  `json:"site_key"`
			OwnerSymbolKey *string `json:"owner_symbol_key"`
			RawTarget      string  `json:"raw_target"`
			Relation       string  `json:"relation"`
			SyntaxSpan     string  `json:"syntax_span"`
			ResolverHints  string  `json:"resolver_hints"`
		}{
			SiteKey:        reference.SiteKey,
			OwnerSymbolKey: reference.OwnerSymbolKey,
			RawTarget:      reference.RawTarget,
			Relation:       reference.Relation,
			SyntaxSpan:     syntaxSpan,
			ResolverHints:  resolverHints,
		})
		if err != nil {
			return 0, err
		}
		_, _ = hash.Write(encoded)
		_, _ = hash.Write([]byte{'\n'})
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("uci projection iterate reference sites: %w", err)
	}
	return count, nil
}

func (s *UCIProjectionStore) hashUCIChunks(ctx context.Context, hash interface{ Write([]byte) (int, error) }, artifactID string, blob UCIBlob) (uint64, error) {
	if _, err := hash.Write([]byte("|chunks|")); err != nil {
		return 0, err
	}
	rows, err := s.db.WithContext(ctx).Where("artifact_id = ?", artifactID).Order("ordinal ASC, chunk_id ASC").Model(&UCIChunk{}).Rows()
	if err != nil {
		return 0, fmt.Errorf("uci projection describe chunks: %w", err)
	}
	defer rows.Close()
	var count uint64
	for rows.Next() {
		var chunk UCIChunk
		if err := s.db.ScanRows(rows, &chunk); err != nil {
			return 0, fmt.Errorf("uci projection scan chunk: %w", err)
		}
		if chunk.ByteStart < 0 || chunk.ByteEnd < chunk.ByteStart || chunk.ByteEnd > blob.ByteLength {
			return 0, errUCIProjectionImmutable
		}
		if digestUCIBytes(blob.SafeContent[chunk.ByteStart:chunk.ByteEnd]) != chunk.ContentDigest {
			return 0, errUCIProjectionImmutable
		}
		encoded, err := json.Marshal(struct {
			SymbolKey     *string `json:"symbol_key"`
			ChunkKind     string  `json:"chunk_kind"`
			Ordinal       int     `json:"ordinal"`
			ByteStart     int64   `json:"byte_start"`
			ByteEnd       int64   `json:"byte_end"`
			ContentDigest string  `json:"content_digest"`
			TextForSearch string  `json:"text_for_search"`
		}{
			SymbolKey:     chunk.SymbolKey,
			ChunkKind:     chunk.ChunkKind,
			Ordinal:       chunk.Ordinal,
			ByteStart:     chunk.ByteStart,
			ByteEnd:       chunk.ByteEnd,
			ContentDigest: chunk.ContentDigest,
			TextForSearch: chunk.TextForSearch,
		})
		if err != nil {
			return 0, err
		}
		_, _ = hash.Write(encoded)
		_, _ = hash.Write([]byte{'\n'})
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("uci projection iterate chunks: %w", err)
	}
	return count, nil
}

func normalizeUCIArtifactJSONObject(value string, byteLength int64) (string, error) {
	var object map[string]any
	if err := json.Unmarshal([]byte(value), &object); err != nil || object == nil {
		return "", errUCIProjectionImmutable
	}
	if byteLength >= 0 {
		byteStart, hasByteStart := object["byte_start"].(float64)
		byteEnd, hasByteEnd := object["byte_end"].(float64)
		lineStart, hasLineStart := object["line_start"].(float64)
		lineEnd, hasLineEnd := object["line_end"].(float64)
		if hasByteStart || hasByteEnd || hasLineStart || hasLineEnd {
			if !hasByteStart || !hasByteEnd || !hasLineStart || !hasLineEnd ||
				byteStart < 0 || byteEnd < byteStart || byteEnd > float64(byteLength) ||
				lineStart < 1 || lineEnd < lineStart ||
				byteStart != float64(int64(byteStart)) || byteEnd != float64(int64(byteEnd)) ||
				lineStart != float64(int64(lineStart)) || lineEnd != float64(int64(lineEnd)) {
				return "", errUCIProjectionImmutable
			}
		}
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return "", err
	}
	return string(normalized), nil
}

var (
	errUCIPublicationRejected            = errors.New("UCI_PUBLICATION_REJECTED")
	errUCIPublicationIdempotencyMismatch = errors.New("IDEMPOTENCY_MISMATCH")
	errUCIPublicationLeaseStale          = errors.New("LEASE_STALE")
	errUCIPublicationBuildIncomplete     = errors.New("BUILD_INCOMPLETE")
)

type uciPublisher struct {
	store            *UCIProjectionStore
	authorizer       ucidomain.ContextAuthorizer
	limits           ucidomain.IndexPublicationLimits
	embeddingProfile *ucidomain.VectorProfile
}

// Publisher creates the server-owned implementation of the fenced publication API.
func (s *UCIProjectionStore) Publisher(authorizer ucidomain.ContextAuthorizer, config ucidomain.IndexPublicationConfig) (ucidomain.IndexStore, error) {
	if err := s.requireDB("create publisher"); err != nil {
		return nil, err
	}
	if authorizer == nil || !validUCIPublicationLimits(config.Limits) {
		return nil, errUCIPublicationRejected
	}
	publisher := &uciPublisher{store: s, authorizer: authorizer, limits: config.Limits}
	if config.EmbeddingProfile != nil {
		if err := validateUCISemanticProfile(*config.EmbeddingProfile); err != nil {
			return nil, errUCIPublicationRejected
		}
		profileCopy := *config.EmbeddingProfile
		publisher.embeddingProfile = &profileCopy
	}
	return publisher, nil
}

func (publisher *uciPublisher) Begin(ctx context.Context, caller ucidomain.IndexCaller, input ucidomain.IndexBeginInput) (ucidomain.IndexBeginResult, error) {
	if publisher == nil || ctx == nil || !validUCIPublicationCaller(caller) || !validUCIPublicationBegin(input) {
		return ucidomain.IndexBeginResult{}, errUCIPublicationRejected
	}
	if err := publisher.authorize(ctx, caller, input.Scope); err != nil {
		return ucidomain.IndexBeginResult{}, errUCIPublicationRejected
	}
	bindingDigest, err := canonicalUCIPublicationBeginDigest(caller, input)
	if err != nil {
		return ucidomain.IndexBeginResult{}, errUCIPublicationRejected
	}

	var result ucidomain.IndexBeginResult
	err = publisher.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		checkout, err := lockUCIPublicationCheckout(ctx, tx, input.Scope)
		if err != nil {
			return err
		}

		job, found, err := lockUCIPublicationJobByKey(ctx, tx, input.Scope, input.BuildKey)
		if err != nil {
			return err
		}
		if found {
			if !sameUCIPublicationBegin(*job, caller, input, bindingDigest) {
				return errUCIPublicationIdempotencyMismatch
			}
			build, err := indexBuildRefFromJob(*job, input.Scope)
			if err != nil {
				return errUCIPublicationRejected
			}
			result.Build = build
			if job.LeaseExpiry != nil {
				result.LeaseExpiresAt = job.LeaseExpiry.UTC()
			}
			if job.ResultViewID != nil {
				published, err := loadUCIPublishedViewForJob(ctx, tx, *job)
				if err != nil {
					return err
				}
				result.Published = &published
			}
			return nil
		}

		var profile UCIAnalysisProfile
		if err := tx.WithContext(ctx).Where("profile_id = ?", input.ProfileID).First(&profile).Error; err != nil {
			return errUCIPublicationRejected
		}
		current, err := loadUCICurrentViewForCheckout(ctx, tx, checkout)
		if err != nil {
			return err
		}
		if !matchesUCIPublicationParent(input.ExpectedParent, current, input.Scope, input.ProfileID) {
			return errUCIPublicationRejected
		}
		now, err := uciDatabaseClock(ctx, tx)
		if err != nil {
			return err
		}
		if checkout.LeaseExpiresAt != nil && checkout.LeaseExpiresAt.After(now) {
			return errUCIPublicationLeaseStale
		}
		nextEpoch := checkout.LeaseEpoch + 1
		leaseExpiry := now.Add(publisher.limits.LeaseTTL)
		if err := tx.WithContext(ctx).Model(&UCICheckout{}).Where("checkout_id = ?", checkout.CheckoutID).Updates(map[string]any{
			"lease_epoch":      nextEpoch,
			"owner_instance":   caller.OwnerInstance,
			"lease_expires_at": leaseExpiry,
			"updated_at":       now,
		}).Error; err != nil {
			return fmt.Errorf("uci publication acquire checkout lease: %w", err)
		}
		expectedParentID := uciPublicationParentID(input.ExpectedParent)
		checkoutID := input.Scope.CheckoutID
		incarnationID := input.Scope.IncarnationID
		profileID := input.ProfileID
		requestedBy := caller.Principal
		publicationKey := input.BuildKey
		manifestMode := string(input.Mode)
		leaseOwner := caller.OwnerInstance
		epoch := nextEpoch
		newJob := UCIJob{
			JobID:                uuid.NewString(),
			SourceID:             input.Scope.SourceID,
			CheckoutID:           &checkoutID,
			JobKind:              string(input.JobKind),
			InputFingerprint:     bindingDigest,
			OwnerEpoch:           &epoch,
			State:                UCIJobRunning,
			Attempt:              1,
			LeaseOwner:           &leaseOwner,
			LeaseExpiry:          &leaseExpiry,
			Counts:               `{}`,
			PublicationKey:       &publicationKey,
			RequestedBy:          &requestedBy,
			IncarnationID:        &incarnationID,
			ProfileID:            &profileID,
			ExpectedParentViewID: expectedParentID,
			ManifestMode:         &manifestMode,
			CreatedAt:            now,
			UpdatedAt:            now,
		}
		if err := tx.WithContext(ctx).Create(&newJob).Error; err != nil {
			return fmt.Errorf("uci publication create build: %w", err)
		}
		result = ucidomain.IndexBeginResult{
			Build: ucidomain.IndexBuildRef{
				BuildID:       newJob.JobID,
				Scope:         input.Scope,
				OwnerInstance: caller.OwnerInstance,
				LeaseEpoch:    nextEpoch,
			},
			LeaseExpiresAt: leaseExpiry,
		}
		return nil
	})
	if err != nil {
		return ucidomain.IndexBeginResult{}, err
	}
	return result, nil
}

func (publisher *uciPublisher) authorize(ctx context.Context, caller ucidomain.IndexCaller, scope ucidomain.IndexScope) error {
	if publisher.authorizer == nil {
		return errUCIPublicationRejected
	}
	if err := publisher.authorizer.AuthorizeContext(ctx, ucidomain.ContextAccess{
		AuthRealm:  caller.AuthRealm,
		Principal:  caller.Principal,
		SourceID:   scope.SourceID,
		CheckoutID: scope.CheckoutID,
	}); err != nil {
		return errUCIPublicationRejected
	}
	return nil
}

func validUCIPublicationLimits(limits ucidomain.IndexPublicationLimits) bool {
	return limits.LeaseTTL > 0 && limits.MaxPartBytes > 0 && limits.MaxParts > 0 &&
		limits.MaxBuildBytes >= limits.MaxPartBytes && limits.MaxManifestEntries > 0 &&
		limits.MaxEdges > 0 && limits.MaxArtifactBytes > 0
}

func validUCIPublicationCaller(caller ucidomain.IndexCaller) bool {
	return validUCIPublicationText(caller.AuthRealm) && validUCIPublicationText(caller.Principal) && validUCIPublicationText(caller.OwnerInstance)
}

func validUCIPublicationBegin(input ucidomain.IndexBeginInput) bool {
	if !validUCIPublicationText(input.BuildKey) || !validUCIPublicationScope(input.Scope) || validateUCIUUID("profile_id", input.ProfileID) != nil {
		return false
	}
	if input.Mode != ucidomain.IndexManifestFull && input.Mode != ucidomain.IndexManifestDelta {
		return false
	}
	if input.JobKind != ucidomain.IndexJobInitial && input.JobKind != ucidomain.IndexJobReconcile && input.JobKind != ucidomain.IndexJobRecovery {
		return false
	}
	return validUCIPublicationParent(input.ExpectedParent, input.Scope, input.ProfileID)
}

func validUCIPublicationScope(scope ucidomain.IndexScope) bool {
	return validateUCIUUID("source_id", scope.SourceID) == nil && validateUCIUUID("checkout_id", scope.CheckoutID) == nil && validateUCIUUID("incarnation_id", scope.IncarnationID) == nil
}

func validUCIPublicationParent(parent *ucidomain.ContextRef, scope ucidomain.IndexScope, profileID string) bool {
	if parent == nil {
		return true
	}
	return validateUCIUUID("parent_source_id", parent.SourceID) == nil &&
		validateUCIUUID("parent_checkout_id", parent.CheckoutID) == nil &&
		validateUCIUUID("parent_view_id", parent.ViewID) == nil &&
		validateUCIUUID("parent_profile_id", parent.AnalysisProfileID) == nil &&
		parent.Generation > 0 && parent.SourceID == scope.SourceID && parent.CheckoutID == scope.CheckoutID &&
		(profileID == "" || parent.AnalysisProfileID == profileID)
}

func validUCIPublicationText(value string) bool {
	return validateUCIRequiredText("publication_value", value) == nil
}

func lockUCIPublicationCheckout(ctx context.Context, tx *gorm.DB, scope ucidomain.IndexScope) (*UCICheckout, error) {
	var checkout UCICheckout
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("checkout_id = ? AND source_id = ?", scope.CheckoutID, scope.SourceID).First(&checkout).Error; err != nil {
		return nil, errUCIPublicationRejected
	}
	if checkout.IncarnationID != scope.IncarnationID || checkout.Kind != UCICheckoutWorkingTree {
		return nil, errUCIPublicationRejected
	}
	return &checkout, nil
}

func lockUCIPublicationJobByKey(ctx context.Context, tx *gorm.DB, scope ucidomain.IndexScope, key string) (*UCIJob, bool, error) {
	var job UCIJob
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where(
		"source_id = ? AND checkout_id = ? AND publication_key = ?", scope.SourceID, scope.CheckoutID, key,
	).First(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("uci publication lock build by key: %w", err)
	}
	return &job, true, nil
}

func loadUCICurrentViewForCheckout(ctx context.Context, tx *gorm.DB, checkout *UCICheckout) (*UCIView, error) {
	if checkout.CurrentViewID == nil {
		return nil, nil
	}
	var view UCIView
	if err := tx.WithContext(ctx).Where("view_id = ?", *checkout.CurrentViewID).First(&view).Error; err != nil {
		return nil, fmt.Errorf("uci publication load current view: %w", err)
	}
	return &view, nil
}

func matchesUCIPublicationParent(parent *ucidomain.ContextRef, current *UCIView, scope ucidomain.IndexScope, profileID string) bool {
	if parent == nil {
		return current == nil
	}
	if current == nil || !validUCIPublicationParent(parent, scope, profileID) {
		return false
	}
	return current.ViewID == parent.ViewID && current.SourceID == parent.SourceID && current.CheckoutID == parent.CheckoutID &&
		current.ProfileID == parent.AnalysisProfileID && current.Generation == parent.Generation && current.IncarnationID == scope.IncarnationID
}

func uciPublicationParentID(parent *ucidomain.ContextRef) *string {
	if parent == nil {
		return nil
	}
	value := parent.ViewID
	return &value
}

func sameUCIPublicationBegin(job UCIJob, caller ucidomain.IndexCaller, input ucidomain.IndexBeginInput, bindingDigest string) bool {
	return job.PublicationKey != nil && *job.PublicationKey == input.BuildKey &&
		job.RequestedBy != nil && *job.RequestedBy == caller.Principal &&
		job.CheckoutID != nil && *job.CheckoutID == input.Scope.CheckoutID && job.SourceID == input.Scope.SourceID &&
		job.IncarnationID != nil && *job.IncarnationID == input.Scope.IncarnationID &&
		job.ProfileID != nil && *job.ProfileID == input.ProfileID && job.JobKind == string(input.JobKind) &&
		job.ManifestMode != nil && *job.ManifestMode == string(input.Mode) && job.InputFingerprint == bindingDigest &&
		sameUCIOptionalString(job.ExpectedParentViewID, uciPublicationParentID(input.ExpectedParent))
}

func indexBuildRefFromJob(job UCIJob, scope ucidomain.IndexScope) (ucidomain.IndexBuildRef, error) {
	if job.OwnerEpoch == nil || job.LeaseOwner == nil || !validUCIPublicationScope(scope) {
		return ucidomain.IndexBuildRef{}, errUCIPublicationRejected
	}
	return ucidomain.IndexBuildRef{
		BuildID:       job.JobID,
		Scope:         scope,
		OwnerInstance: *job.LeaseOwner,
		LeaseEpoch:    *job.OwnerEpoch,
	}, nil
}

// LoadIndexBuildRef returns the server-recorded lease capability for an exact
// publication build. Stage and Finalize use it to recover the durable owner
// because their transport requests deliberately carry no owner instance.
func (s *UCIProjectionStore) LoadIndexBuildRef(ctx context.Context, scope ucidomain.IndexScope, profileID, buildID string, leaseEpoch int64) (ucidomain.IndexBuildRef, error) {
	if err := s.requireDB("load index build"); err != nil {
		return ucidomain.IndexBuildRef{}, err
	}
	if ctx == nil {
		return ucidomain.IndexBuildRef{}, errUCIPublicationRejected
	}
	if err := ctx.Err(); err != nil {
		return ucidomain.IndexBuildRef{}, err
	}
	if !validUCIPublicationScope(scope) || validateUCIUUID("profile_id", profileID) != nil ||
		validateUCIUUID("build_id", buildID) != nil || leaseEpoch <= 0 {
		return ucidomain.IndexBuildRef{}, errUCIPublicationRejected
	}

	var job UCIJob
	if err := s.db.WithContext(ctx).Where(
		"job_id = ? AND source_id = ? AND checkout_id = ? AND incarnation_id = ? AND profile_id = ? AND owner_epoch = ?",
		buildID, scope.SourceID, scope.CheckoutID, scope.IncarnationID, profileID, leaseEpoch,
	).First(&job).Error; err != nil {
		return ucidomain.IndexBuildRef{}, fmt.Errorf("uci projection load index build: %w", err)
	}
	build, err := indexBuildRefFromJob(job, scope)
	if err != nil || build.BuildID != buildID || build.LeaseEpoch != leaseEpoch {
		return ucidomain.IndexBuildRef{}, errUCIPublicationRejected
	}
	return build, nil
}

func loadUCIPublishedViewForJob(ctx context.Context, tx *gorm.DB, job UCIJob) (ucidomain.IndexPublishedView, error) {
	if job.ResultViewID == nil {
		return ucidomain.IndexPublishedView{}, errUCIPublicationRejected
	}
	var view UCIView
	if err := tx.WithContext(ctx).Where("view_id = ?", *job.ResultViewID).First(&view).Error; err != nil {
		return ucidomain.IndexPublishedView{}, fmt.Errorf("uci publication load durable result: %w", err)
	}
	if view.PublishedAt == nil {
		return ucidomain.IndexPublishedView{}, errUCIPublicationRejected
	}
	return ucidomain.IndexPublishedView{
		BuildID:        job.JobID,
		Context:        uciContextRefFromView(view),
		ManifestDigest: ucidomain.IndexDigest(view.ManifestDigest),
		AcceptedFSSeq:  view.ObservedFSSeq,
		PublishedAt:    view.PublishedAt.UTC(),
	}, nil
}

func uciContextRefFromView(view UCIView) ucidomain.ContextRef {
	return ucidomain.ContextRef{
		SourceID:          view.SourceID,
		CheckoutID:        view.CheckoutID,
		ViewID:            view.ViewID,
		AnalysisProfileID: view.ProfileID,
		Generation:        view.Generation,
	}
}

func uciDatabaseClock(ctx context.Context, db *gorm.DB) (time.Time, error) {
	var now time.Time
	if err := db.WithContext(ctx).Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return time.Time{}, fmt.Errorf("uci publication database clock: %w", err)
	}
	return now.UTC(), nil
}

func canonicalUCIPublicationBeginDigest(caller ucidomain.IndexCaller, input ucidomain.IndexBeginInput) (string, error) {
	return canonicalUCIPublicationDigest("begin", struct {
		Caller         ucidomain.IndexCaller       `json:"caller"`
		BuildKey       string                      `json:"build_key"`
		Scope          ucidomain.IndexScope        `json:"scope"`
		ProfileID      string                      `json:"profile_id"`
		ExpectedParent *ucidomain.ContextRef       `json:"expected_parent"`
		Mode           ucidomain.IndexManifestMode `json:"mode"`
		JobKind        ucidomain.IndexJobKind      `json:"job_kind"`
	}{
		Caller:         caller,
		BuildKey:       input.BuildKey,
		Scope:          input.Scope,
		ProfileID:      input.ProfileID,
		ExpectedParent: input.ExpectedParent,
		Mode:           input.Mode,
		JobKind:        input.JobKind,
	})
}

func canonicalUCIPublicationDigest(kind string, value any) (string, error) {
	encoded, err := json.Marshal(struct {
		Kind    string `json:"kind"`
		Value   any    `json:"value"`
		Version int    `json:"version"`
	}{Kind: kind, Value: value, Version: 1})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (publisher *uciPublisher) Stage(ctx context.Context, caller ucidomain.IndexCaller, input ucidomain.IndexStageInput) (ucidomain.IndexPartAck, error) {
	if publisher == nil || ctx == nil || !validUCIPublicationCaller(caller) || !validUCIPublicationBuild(input.Build) {
		return ucidomain.IndexPartAck{}, errUCIPublicationRejected
	}
	if err := publisher.authorize(ctx, caller, input.Build.Scope); err != nil {
		return ucidomain.IndexPartAck{}, errUCIPublicationRejected
	}
	computedDigest, err := ucidomain.DigestIndexPart(input.Part)
	if err != nil || computedDigest != input.Digest {
		return ucidomain.IndexPartAck{}, errUCIPublicationRejected
	}
	payload, err := json.Marshal(input.Part)
	if err != nil || int64(len(payload)) > publisher.limits.MaxPartBytes {
		return ucidomain.IndexPartAck{}, errUCIPublicationRejected
	}

	var preflight UCIJob
	if err := publisher.store.db.WithContext(ctx).Where(
		"job_id = ? AND source_id = ? AND checkout_id = ?", input.Build.BuildID, input.Build.Scope.SourceID, input.Build.Scope.CheckoutID,
	).First(&preflight).Error; err != nil || preflight.ProfileID == nil {
		return ucidomain.IndexPartAck{}, errUCIPublicationRejected
	}
	if err := publisher.validateStagedPart(ctx, input.Build.Scope, *preflight.ProfileID, input.Part); err != nil {
		return ucidomain.IndexPartAck{}, errUCIPublicationRejected
	}

	var acknowledgement ucidomain.IndexPartAck
	err = publisher.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		checkout, err := lockUCIPublicationCheckout(ctx, tx, input.Build.Scope)
		if err != nil {
			return err
		}
		job, err := lockUCIPublicationJobByID(ctx, tx, input.Build.BuildID)
		if err != nil {
			return err
		}
		if !matchesUCIPublicationBuild(*job, checkout, caller, input.Build) {
			return errUCIPublicationRejected
		}
		var existing UCIIndexBuildPart
		err = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"build_id = ? AND sequence = ?", input.Build.BuildID, input.Sequence,
		).First(&existing).Error
		if err == nil {
			if existing.PartDigest != string(input.Digest) {
				return errUCIPublicationIdempotencyMismatch
			}
			acknowledgement = ucidomain.IndexPartAck{BuildID: input.Build.BuildID, Sequence: input.Sequence, Digest: input.Digest}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("uci publication load staged part: %w", err)
		}
		now, err := uciDatabaseClock(ctx, tx)
		if err != nil {
			return err
		}
		if !activeUCIPublicationLease(*job, checkout, caller, input.Build, now) {
			return errUCIPublicationLeaseStale
		}
		var partCount int64
		if err := tx.WithContext(ctx).Model(&UCIIndexBuildPart{}).Where("build_id = ?", input.Build.BuildID).Count(&partCount).Error; err != nil {
			return fmt.Errorf("uci publication count staged parts: %w", err)
		}
		if partCount >= int64(publisher.limits.MaxParts) || input.Sequence != uint32(partCount) {
			return errUCIPublicationBuildIncomplete
		}
		var totalBytes int64
		if err := tx.WithContext(ctx).Model(&UCIIndexBuildPart{}).Where("build_id = ?", input.Build.BuildID).Select("COALESCE(SUM(payload_bytes), 0)").Scan(&totalBytes).Error; err != nil {
			return fmt.Errorf("uci publication total staged bytes: %w", err)
		}
		if totalBytes > publisher.limits.MaxBuildBytes-int64(len(payload)) {
			return errUCIPublicationBuildIncomplete
		}
		row := UCIIndexBuildPart{
			BuildID:      input.Build.BuildID,
			Sequence:     input.Sequence,
			PartDigest:   string(input.Digest),
			Payload:      string(payload),
			PayloadBytes: int64(len(payload)),
			CreatedAt:    now,
		}
		if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
			return fmt.Errorf("uci publication store staged part: %w", err)
		}
		acknowledgement = ucidomain.IndexPartAck{BuildID: input.Build.BuildID, Sequence: input.Sequence, Digest: input.Digest}
		return nil
	})
	if err != nil {
		return ucidomain.IndexPartAck{}, err
	}
	return acknowledgement, nil
}

func validUCIPublicationBuild(build ucidomain.IndexBuildRef) bool {
	return validateUCIUUID("build_id", build.BuildID) == nil && validUCIPublicationScope(build.Scope) &&
		validUCIPublicationText(build.OwnerInstance) && build.LeaseEpoch > 0
}

func lockUCIPublicationJobByID(ctx context.Context, tx *gorm.DB, buildID string) (*UCIJob, error) {
	var job UCIJob
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id = ?", buildID).First(&job).Error; err != nil {
		return nil, errUCIPublicationRejected
	}
	return &job, nil
}

func matchesUCIPublicationBuild(job UCIJob, checkout *UCICheckout, caller ucidomain.IndexCaller, build ucidomain.IndexBuildRef) bool {
	return job.SourceID == build.Scope.SourceID && job.CheckoutID != nil && *job.CheckoutID == build.Scope.CheckoutID &&
		job.IncarnationID != nil && *job.IncarnationID == build.Scope.IncarnationID &&
		job.OwnerEpoch != nil && *job.OwnerEpoch == build.LeaseEpoch && job.LeaseOwner != nil &&
		*job.LeaseOwner == build.OwnerInstance && caller.OwnerInstance == build.OwnerInstance && checkout.LeaseEpoch == build.LeaseEpoch
}

func activeUCIPublicationLease(job UCIJob, checkout *UCICheckout, caller ucidomain.IndexCaller, build ucidomain.IndexBuildRef, now time.Time) bool {
	return job.State == UCIJobRunning && job.LeaseExpiry != nil && job.LeaseExpiry.After(now) &&
		checkout.LeaseExpiresAt != nil && checkout.LeaseExpiresAt.After(now) &&
		matchesUCIPublicationBuild(job, checkout, caller, build)
}

func (publisher *uciPublisher) validateStagedPart(ctx context.Context, scope ucidomain.IndexScope, profileID string, part ucidomain.IndexPart) error {
	proofs := make(map[string]ucidomain.IndexArtifactProof, len(part.Artifacts))
	for _, expected := range part.Artifacts {
		actual, err := publisher.store.DescribeIndexArtifact(ctx, scope.SourceID, expected.ArtifactID)
		if err != nil || !sameUCIIndexArtifactProof(actual, expected) {
			return errUCIPublicationRejected
		}
		if err := publisher.validateArtifactProfile(ctx, scope.SourceID, expected.ArtifactID, profileID); err != nil {
			return err
		}
		if err := publisher.validateArtifactSize(ctx, scope.SourceID, expected.ArtifactID); err != nil {
			return err
		}
		proofs[expected.ArtifactID] = expected
	}
	for _, replacement := range part.EdgeReplacements {
		for _, edge := range replacement.Edges {
			if err := publisher.validateStagedEdge(ctx, scope.SourceID, profileID, edge); err != nil {
				return err
			}
		}
	}
	return nil
}

func sameUCIIndexArtifactProof(left, right ucidomain.IndexArtifactProof) bool {
	return left.ArtifactID == right.ArtifactID && left.ContentDigest == right.ContentDigest && left.FactsDigest == right.FactsDigest &&
		left.DefinitionCount == right.DefinitionCount && left.ReferenceSiteCount == right.ReferenceSiteCount && left.ChunkCount == right.ChunkCount
}

func (publisher *uciPublisher) validateArtifactProfile(ctx context.Context, sourceID, artifactID, profileID string) error {
	var row UCIParseArtifact
	if err := publisher.store.db.WithContext(ctx).Where("source_id = ? AND artifact_id = ?", sourceID, artifactID).First(&row).Error; err != nil {
		return errUCIPublicationRejected
	}
	var profile UCIAnalysisProfile
	if err := publisher.store.db.WithContext(ctx).Where("profile_id = ?", profileID).First(&profile).Error; err != nil {
		return errUCIPublicationRejected
	}
	if row.ExtractionProfileDigest != profile.ParserBundleDigest {
		return errUCIPublicationRejected
	}
	return nil
}

func (publisher *uciPublisher) validateArtifactSize(ctx context.Context, sourceID, artifactID string) error {
	var row struct {
		ByteLength int64 `gorm:"column:byte_length"`
	}
	if err := publisher.store.db.WithContext(ctx).Raw(`
		SELECT blob.byte_length
		FROM ci_parse_artifacts AS artifact
		JOIN ci_blobs AS blob ON blob.source_id = artifact.source_id AND blob.blob_id = artifact.blob_id
		WHERE artifact.source_id = ? AND artifact.artifact_id = ?
	`, sourceID, artifactID).Scan(&row).Error; err != nil || row.ByteLength > publisher.limits.MaxArtifactBytes {
		return errUCIPublicationRejected
	}
	return nil
}

func (publisher *uciPublisher) validateStagedEdge(ctx context.Context, sourceID, profileID string, edge ucidomain.IndexEdge) error {
	if err := publisher.validateArtifactProfile(ctx, sourceID, edge.SourceArtifactID, profileID); err != nil {
		return err
	}
	if edge.SourceSymbolKey != nil {
		if err := publisher.validateArtifactSymbol(ctx, edge.SourceArtifactID, *edge.SourceSymbolKey); err != nil {
			return err
		}
	}
	var source struct {
		ByteLength int64 `gorm:"column:byte_length"`
	}
	if err := publisher.store.db.WithContext(ctx).Raw(`
		SELECT blob.byte_length
		FROM ci_parse_artifacts AS artifact
		JOIN ci_blobs AS blob ON blob.source_id = artifact.source_id AND blob.blob_id = artifact.blob_id
		WHERE artifact.source_id = ? AND artifact.artifact_id = ?
	`, sourceID, edge.SourceArtifactID).Scan(&source).Error; err != nil || edge.Evidence.Span.ByteEnd > source.ByteLength {
		return errUCIPublicationRejected
	}
	if edge.Target != nil {
		if err := publisher.validateArtifactProfile(ctx, sourceID, edge.Target.ArtifactID, profileID); err != nil {
			return err
		}
		if edge.Target.SymbolKey != nil {
			if err := publisher.validateArtifactSymbol(ctx, edge.Target.ArtifactID, *edge.Target.SymbolKey); err != nil {
				return err
			}
		}
	}
	if edge.Evidence.ReferenceSiteID != nil {
		var reference UCIReferenceSite
		if err := publisher.store.db.WithContext(ctx).Where("reference_site_id = ? AND artifact_id = ?", *edge.Evidence.ReferenceSiteID, edge.SourceArtifactID).First(&reference).Error; err != nil {
			return errUCIPublicationRejected
		}
		if reference.Relation != string(edge.Relation) || !sameUCIOptionalString(reference.OwnerSymbolKey, edge.SourceSymbolKey) {
			return errUCIPublicationRejected
		}
	}
	return nil
}

func (publisher *uciPublisher) validateArtifactSymbol(ctx context.Context, artifactID, symbolKey string) error {
	var count int64
	if err := publisher.store.db.WithContext(ctx).Model(&UCIDefinition{}).
		Where("artifact_id = ? AND local_symbol_key = ?", artifactID, symbolKey).Count(&count).Error; err != nil || count != 1 {
		return errUCIPublicationRejected
	}
	return nil
}

func (publisher *uciPublisher) Finalize(ctx context.Context, caller ucidomain.IndexCaller, input ucidomain.IndexFinalizeInput) (ucidomain.IndexPublishedView, error) {
	if publisher == nil || ctx == nil || !validUCIPublicationCaller(caller) || !validUCIPublicationBuild(input.Build) ||
		!validUCIPublicationManifest(input.Manifest) || !validUCIPublicationParent(input.ExpectedParent, input.Build.Scope, "") {
		return ucidomain.IndexPublishedView{}, errUCIPublicationRejected
	}
	if err := publisher.authorize(ctx, caller, input.Build.Scope); err != nil {
		return ucidomain.IndexPublishedView{}, errUCIPublicationRejected
	}
	finalizeDigest, err := canonicalUCIPublicationFinalizeDigest(caller, input)
	if err != nil {
		return ucidomain.IndexPublishedView{}, errUCIPublicationRejected
	}

	var preflight UCIJob
	if err := publisher.store.db.WithContext(ctx).Where(
		"job_id = ? AND source_id = ? AND checkout_id = ?", input.Build.BuildID, input.Build.Scope.SourceID, input.Build.Scope.CheckoutID,
	).First(&preflight).Error; err != nil {
		return ucidomain.IndexPublishedView{}, errUCIPublicationRejected
	}
	if preflight.ResultViewID != nil {
		if preflight.FinalizeBindingDigest == nil || *preflight.FinalizeBindingDigest != finalizeDigest {
			return ucidomain.IndexPublishedView{}, errUCIPublicationIdempotencyMismatch
		}
		return publisher.loadPublishedResult(ctx, preflight)
	}
	if preflight.ProfileID == nil || preflight.ManifestMode == nil || !sameUCIOptionalString(preflight.ExpectedParentViewID, uciPublicationParentID(input.ExpectedParent)) {
		return ucidomain.IndexPublishedView{}, errUCIPublicationRejected
	}
	candidate, err := publisher.prepareUCIPublicationCandidate(ctx, preflight, input)
	if err != nil {
		return ucidomain.IndexPublishedView{}, err
	}

	var published ucidomain.IndexPublishedView
	err = publisher.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		checkout, err := lockUCIPublicationCheckout(ctx, tx, input.Build.Scope)
		if err != nil {
			return err
		}
		job, err := lockUCIPublicationJobByID(ctx, tx, input.Build.BuildID)
		if err != nil {
			return err
		}
		// A durable result is replayed before evaluating lease freshness so a lost ACK cannot rewind current.
		if job.ResultViewID != nil {
			if job.FinalizeBindingDigest == nil || *job.FinalizeBindingDigest != finalizeDigest {
				return errUCIPublicationIdempotencyMismatch
			}
			published, err = loadUCIPublishedViewForJob(ctx, tx, *job)
			return err
		}
		if !matchesUCIPublicationBuild(*job, checkout, caller, input.Build) ||
			!sameUCIOptionalString(job.ExpectedParentViewID, uciPublicationParentID(input.ExpectedParent)) ||
			job.ProfileID == nil {
			return errUCIPublicationRejected
		}
		current, err := loadUCICurrentViewForCheckout(ctx, tx, checkout)
		if err != nil {
			return err
		}
		if !matchesUCIPublicationParent(input.ExpectedParent, current, input.Build.Scope, *job.ProfileID) {
			return errUCIPublicationRejected
		}
		now, err := uciDatabaseClock(ctx, tx)
		if err != nil {
			return err
		}
		if !activeUCIPublicationLease(*job, checkout, caller, input.Build, now) {
			return errUCIPublicationLeaseStale
		}
		if current != nil && canReuseUCIPublishedView(ctx, tx, *job, *current, input, candidate) {
			sealedManifest, err := json.Marshal(input.Manifest)
			if err != nil {
				return err
			}
			resultViewID := current.ViewID
			if err := tx.WithContext(ctx).Model(&UCIJob{}).Where("job_id = ?", job.JobID).Updates(map[string]any{
				"state":                   UCIJobSucceeded,
				"result_view_id":          resultViewID,
				"sealed_manifest":         string(sealedManifest),
				"finalize_binding_digest": finalizeDigest,
				"updated_at":              now,
			}).Error; err != nil {
				return fmt.Errorf("uci publication store no-op result: %w", err)
			}
			if err := tx.WithContext(ctx).Model(&UCICheckout{}).Where("checkout_id = ?", checkout.CheckoutID).Updates(map[string]any{
				"owner_instance":   nil,
				"lease_expires_at": nil,
				"updated_at":       now,
			}).Error; err != nil {
				return fmt.Errorf("uci publication release checkout lease: %w", err)
			}
			published = ucidomain.IndexPublishedView{
				BuildID:        job.JobID,
				Context:        uciContextRefFromView(*current),
				ManifestDigest: ucidomain.IndexDigest(current.ManifestDigest),
				AcceptedFSSeq:  current.ObservedFSSeq,
				PublishedAt:    current.PublishedAt.UTC(),
			}
			return nil
		}

		nextGeneration := int64(1)
		if current != nil {
			nextGeneration = current.Generation + 1
		}
		if err := publisher.sealUCIPublicationArtifacts(ctx, tx, input.Build.Scope.SourceID, candidate.ArtifactProofs, now); err != nil {
			return err
		}
		if err := applyUCIPublicationMemberships(ctx, tx, input.Build.Scope, nextGeneration, candidate.CurrentMemberships, candidate.Memberships); err != nil {
			return err
		}
		if err := applyUCIPublicationEdges(ctx, tx, input.Build.Scope, nextGeneration, candidate.CurrentEdges, candidate.EdgeReplacements); err != nil {
			return err
		}
		coverageJSON, err := marshalUCIPublicationCoverage(input.Manifest.Coverage)
		if err != nil {
			return err
		}
		publishedAt := now
		view := UCIView{
			ViewID:         uuid.NewString(),
			CheckoutID:     input.Build.Scope.CheckoutID,
			SourceID:       input.Build.Scope.SourceID,
			IncarnationID:  input.Build.Scope.IncarnationID,
			Generation:     nextGeneration,
			ProfileID:      *job.ProfileID,
			HeadOID:        input.Manifest.Observation.HeadOID,
			ObjectFormat:   input.Manifest.Observation.ObjectFormat,
			RefLabel:       input.Manifest.Observation.RefLabel,
			Dirty:          input.Manifest.Observation.Dirty,
			ObservedFSSeq:  input.Manifest.Observation.ObservedFSSeq,
			ScanStart:      input.Manifest.Observation.ScanStart.UTC(),
			ScanEnd:        input.Manifest.Observation.ScanEnd.UTC(),
			ManifestDigest: string(input.Manifest.ManifestDigest),
			State:          UCIViewPublished,
			CoverageJSON:   coverageJSON,
			PublishedAt:    &publishedAt,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := tx.WithContext(ctx).Create(&view).Error; err != nil {
			return fmt.Errorf("uci publication create view: %w", err)
		}
		if err := tx.WithContext(ctx).Model(&UCICheckout{}).Where("checkout_id = ?", checkout.CheckoutID).Updates(map[string]any{
			"current_view_id":  view.ViewID,
			"owner_instance":   nil,
			"lease_expires_at": nil,
			"updated_at":       now,
		}).Error; err != nil {
			return fmt.Errorf("uci publication switch current pointer: %w", err)
		}
		if publisher.embeddingProfile != nil {
			if err := enqueueUCIEmbeddingJob(ctx, tx, caller.AuthRealm, caller.Principal, view, *publisher.embeddingProfile); err != nil {
				return fmt.Errorf("uci publication enqueue embedding: %w", err)
			}
		}
		if current != nil {
			if err := tx.WithContext(ctx).Model(&UCIView{}).Where("view_id = ?", current.ViewID).Updates(map[string]any{
				"state":      UCIViewSuperseded,
				"updated_at": now,
			}).Error; err != nil {
				return fmt.Errorf("uci publication supersede parent: %w", err)
			}
		}
		sealedManifest, err := json.Marshal(input.Manifest)
		if err != nil {
			return err
		}
		resultViewID := view.ViewID
		if err := tx.WithContext(ctx).Model(&UCIJob{}).Where("job_id = ?", job.JobID).Updates(map[string]any{
			"state":                   UCIJobSucceeded,
			"result_view_id":          resultViewID,
			"sealed_manifest":         string(sealedManifest),
			"finalize_binding_digest": finalizeDigest,
			"updated_at":              now,
		}).Error; err != nil {
			return fmt.Errorf("uci publication store result: %w", err)
		}
		published = ucidomain.IndexPublishedView{
			BuildID:        job.JobID,
			Context:        uciContextRefFromView(view),
			ManifestDigest: input.Manifest.ManifestDigest,
			AcceptedFSSeq:  input.Manifest.Observation.ObservedFSSeq,
			PublishedAt:    publishedAt,
		}
		return nil
	})
	if err != nil {
		return ucidomain.IndexPublishedView{}, err
	}
	return published, nil
}

func (publisher *uciPublisher) loadPublishedResult(ctx context.Context, job UCIJob) (ucidomain.IndexPublishedView, error) {
	var published ucidomain.IndexPublishedView
	err := publisher.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		published, err = loadUCIPublishedViewForJob(ctx, tx, job)
		return err
	})
	if err != nil {
		return ucidomain.IndexPublishedView{}, err
	}
	return published, nil
}

type uciPublicationCandidate struct {
	ArtifactProofs     map[string]ucidomain.IndexArtifactProof
	CurrentMemberships map[string]UCIMembership
	Memberships        map[string]ucidomain.IndexMembership
	CurrentEdges       map[string][]UCIResolvedEdge
	EdgeReplacements   map[string]ucidomain.IndexEdgeReplacement
	DeletedPaths       map[string]struct{}
}

// canReuseUCIPublishedView accepts only a fully proven semantic no-op. Any missing,
// malformed, or divergent prior publication deliberately falls through to normal
// publication rather than converting uncertainty into a successful reuse.
func canReuseUCIPublishedView(ctx context.Context, tx *gorm.DB, job UCIJob, current UCIView, input ucidomain.IndexFinalizeInput, candidate *uciPublicationCandidate) bool {
	if candidate == nil || job.ProfileID == nil || current.ProfileID != *job.ProfileID ||
		!matchesUCIPublicationParent(input.ExpectedParent, &current, input.Build.Scope, *job.ProfileID) ||
		!sameUCIPublicationProjection(candidate.CurrentMemberships, candidate.Memberships, candidate.CurrentEdges, candidate.EdgeReplacements) {
		return false
	}

	priorJob, priorManifest, found := loadUCIPublishedManifestForView(ctx, tx, current)
	if !found || !matchesUCIPublishedViewManifest(current, priorJob, priorManifest) {
		return false
	}
	return sameUCIPublicationSemanticManifest(priorManifest, input.Manifest)
}

// loadUCIPublishedManifestForView finds the successful job written with the View.
// Normal publication stamps its result record and View with the same database clock.
func loadUCIPublishedManifestForView(ctx context.Context, tx *gorm.DB, view UCIView) (UCIJob, ucidomain.IndexManifestCompletion, bool) {
	if view.PublishedAt == nil {
		return UCIJob{}, ucidomain.IndexManifestCompletion{}, false
	}
	var job UCIJob
	if err := tx.WithContext(ctx).Where(
		"result_view_id = ? AND state = ? AND updated_at = ?", view.ViewID, UCIJobSucceeded, *view.PublishedAt,
	).Order("job_id ASC").First(&job).Error; err != nil || job.SealedManifest == nil {
		return UCIJob{}, ucidomain.IndexManifestCompletion{}, false
	}
	var manifest ucidomain.IndexManifestCompletion
	if err := json.Unmarshal([]byte(*job.SealedManifest), &manifest); err != nil || !validUCIPublicationManifest(manifest) {
		return UCIJob{}, ucidomain.IndexManifestCompletion{}, false
	}
	return job, manifest, true
}

// matchesUCIPublishedViewManifest verifies that the sealed manifest is provenance
// for this exact current View. PostgreSQL stores timestamptz at microsecond
// precision, so its reloaded scan window is compared within that storage unit.
func matchesUCIPublishedViewManifest(view UCIView, job UCIJob, manifest ucidomain.IndexManifestCompletion) bool {
	if !sameUCIPublicationStoredCoverage(view.CoverageJSON, manifest.Coverage) {
		return false
	}
	return view.State == UCIViewPublished && view.PublishedAt != nil && job.State == UCIJobSucceeded &&
		job.ResultViewID != nil && *job.ResultViewID == view.ViewID && job.SourceID == view.SourceID &&
		job.CheckoutID != nil && *job.CheckoutID == view.CheckoutID && job.IncarnationID != nil && *job.IncarnationID == view.IncarnationID &&
		job.ProfileID != nil && *job.ProfileID == view.ProfileID && view.ManifestDigest == string(manifest.ManifestDigest) &&
		view.ObservedFSSeq == manifest.Observation.ObservedFSSeq && view.Dirty == manifest.Observation.Dirty &&
		sameUCIOptionalString(view.HeadOID, manifest.Observation.HeadOID) &&
		sameUCIOptionalString(view.ObjectFormat, manifest.Observation.ObjectFormat) &&
		sameUCIOptionalString(view.RefLabel, manifest.Observation.RefLabel) &&
		sameUCIPublicationStoredTimestamp(view.ScanStart, manifest.Observation.ScanStart) &&
		sameUCIPublicationStoredTimestamp(view.ScanEnd, manifest.Observation.ScanEnd)
}

func sameUCIPublicationStoredTimestamp(stored, input time.Time) bool {
	delta := stored.UTC().Sub(input.UTC())
	if delta < 0 {
		delta = -delta
	}
	return delta < time.Microsecond
}

// sameUCIPublicationSemanticManifest excludes only the volatile scan window and
// build-scoped staging identity. DigestIndexParts and its frame count intentionally
// bind staging to a BuildID; prepareUCIPublicationCandidate authenticated that binding
// for each job before this logical final-state comparison.
func sameUCIPublicationSemanticManifest(left, right ucidomain.IndexManifestCompletion) bool {
	return left.EntryCount == right.EntryCount && left.ManifestDigest == right.ManifestDigest &&
		left.EdgeCount == right.EdgeCount && left.EdgesDigest == right.EdgesDigest &&
		left.ScanOutcome == right.ScanOutcome && left.CensusComplete == right.CensusComplete &&
		sameUCIPublicationObservation(left.Observation, right.Observation) &&
		sameUCIPublicationCoverage(left.Coverage, right.Coverage)
}

func sameUCIPublicationObservation(left, right ucidomain.IndexObservation) bool {
	return sameUCIOptionalString(left.HeadOID, right.HeadOID) &&
		sameUCIOptionalString(left.ObjectFormat, right.ObjectFormat) &&
		sameUCIOptionalString(left.RefLabel, right.RefLabel) &&
		left.Dirty == right.Dirty && left.ObservedFSSeq == right.ObservedFSSeq
}

func sameUCIPublicationCoverage(left, right ucidomain.IndexCoverage) bool {
	return left.Structural == right.Structural && left.Lexical == right.Lexical && left.Vector == right.Vector &&
		left.ExcludedFiles == right.ExcludedFiles && left.UnreadableFiles == right.UnreadableFiles &&
		left.UnresolvedReferences == right.UnresolvedReferences
}

func sameUCIPublicationStoredCoverage(encoded string, expected ucidomain.IndexCoverage) bool {
	var actual struct {
		Structural           *string `json:"structural"`
		Lexical              *string `json:"lexical"`
		Vector               *string `json:"vector"`
		ExcludedFiles        *uint64 `json:"excluded_files"`
		UnreadableFiles      *uint64 `json:"unreadable_files"`
		UnresolvedReferences *uint64 `json:"unresolved_references"`
	}
	if err := json.Unmarshal([]byte(encoded), &actual); err != nil || actual.Structural == nil || actual.Lexical == nil ||
		actual.Vector == nil || actual.ExcludedFiles == nil || actual.UnreadableFiles == nil || actual.UnresolvedReferences == nil {
		return false
	}
	return *actual.Structural == string(expected.Structural) && *actual.Lexical == string(expected.Lexical) &&
		*actual.Vector == string(expected.Vector) && *actual.ExcludedFiles == expected.ExcludedFiles &&
		*actual.UnreadableFiles == expected.UnreadableFiles && *actual.UnresolvedReferences == expected.UnresolvedReferences
}

func sameUCIPublicationProjection(currentMemberships map[string]UCIMembership, memberships map[string]ucidomain.IndexMembership, currentEdges map[string][]UCIResolvedEdge, replacements map[string]ucidomain.IndexEdgeReplacement) bool {
	if len(currentMemberships) != len(memberships) {
		return false
	}
	for path, membership := range memberships {
		current, found := currentMemberships[path]
		if !found || !sameUCIPublicationMembership(current, membership) {
			return false
		}
	}
	for sourcePath, replacement := range replacements {
		if !sameUCIPublicationEdgeReplacement(currentEdges[sourcePath], replacement) {
			return false
		}
	}
	for sourcePath := range currentEdges {
		if _, found := replacements[sourcePath]; !found {
			return false
		}
	}
	return true
}

func validUCIPublicationManifest(manifest ucidomain.IndexManifestCompletion) bool {
	if validateUCIDigest("parts_digest", string(manifest.PartsDigest)) != nil ||
		validateUCIDigest("manifest_digest", string(manifest.ManifestDigest)) != nil ||
		validateUCIDigest("edges_digest", string(manifest.EdgesDigest)) != nil ||
		manifest.ScanOutcome != ucidomain.IndexScanComplete || !manifest.CensusComplete ||
		manifest.Observation.ObservedFSSeq < 0 || manifest.Observation.ScanStart.IsZero() || manifest.Observation.ScanEnd.IsZero() ||
		manifest.Observation.ScanEnd.Before(manifest.Observation.ScanStart) {
		return false
	}
	if manifest.Observation.HeadOID != nil {
		if manifest.Observation.ObjectFormat == nil || (*manifest.Observation.ObjectFormat != "sha1" && *manifest.Observation.ObjectFormat != "sha256") {
			return false
		}
		length := 40
		if *manifest.Observation.ObjectFormat == "sha256" {
			length = 64
		}
		if !isUCILowerHex(*manifest.Observation.HeadOID, length) {
			return false
		}
	}
	if manifest.Observation.RefLabel != nil && !validUCIPublicationText(*manifest.Observation.RefLabel) {
		return false
	}
	return validUCIPublicationCoverage(manifest.Coverage)
}

func validUCIPublicationCoverage(coverage ucidomain.IndexCoverage) bool {
	valid := func(value ucidomain.IndexCoverageState) bool {
		return value == ucidomain.IndexCoverageComplete || value == ucidomain.IndexCoveragePartial || value == ucidomain.IndexCoverageUnavailable
	}
	return valid(coverage.Structural) && valid(coverage.Lexical) && valid(coverage.Vector)
}

func canonicalUCIPublicationFinalizeDigest(caller ucidomain.IndexCaller, input ucidomain.IndexFinalizeInput) (string, error) {
	return canonicalUCIPublicationDigest("finalize", struct {
		Caller         ucidomain.IndexCaller             `json:"caller"`
		Build          ucidomain.IndexBuildRef           `json:"build"`
		ExpectedParent *ucidomain.ContextRef             `json:"expected_parent"`
		Manifest       ucidomain.IndexManifestCompletion `json:"manifest"`
	}{Caller: caller, Build: input.Build, ExpectedParent: input.ExpectedParent, Manifest: input.Manifest})
}

func (publisher *uciPublisher) prepareUCIPublicationCandidate(ctx context.Context, job UCIJob, input ucidomain.IndexFinalizeInput) (*uciPublicationCandidate, error) {
	if job.ProfileID == nil || job.ManifestMode == nil {
		return nil, errUCIPublicationRejected
	}
	parts, acknowledgements, err := loadUCIPublicationParts(ctx, publisher.store.db, job.JobID)
	if err != nil {
		return nil, err
	}
	if len(parts) != int(input.Manifest.PartCount) || len(parts) > int(publisher.limits.MaxParts) {
		return nil, errUCIPublicationBuildIncomplete
	}
	partsDigest, err := ucidomain.DigestIndexParts(acknowledgements)
	if err != nil || partsDigest != input.Manifest.PartsDigest {
		return nil, errUCIPublicationBuildIncomplete
	}
	currentMemberships, currentEdges, err := loadUCIPublicationCurrentProjection(ctx, publisher.store.db, input.Build.Scope)
	if err != nil {
		return nil, err
	}
	mode := ucidomain.IndexManifestMode(*job.ManifestMode)
	if mode != ucidomain.IndexManifestFull && mode != ucidomain.IndexManifestDelta {
		return nil, errUCIPublicationRejected
	}
	candidate := &uciPublicationCandidate{
		ArtifactProofs:     make(map[string]ucidomain.IndexArtifactProof),
		CurrentMemberships: currentMemberships,
		Memberships:        make(map[string]ucidomain.IndexMembership),
		CurrentEdges:       currentEdges,
		EdgeReplacements:   make(map[string]ucidomain.IndexEdgeReplacement),
		DeletedPaths:       make(map[string]struct{}),
	}
	if mode == ucidomain.IndexManifestDelta {
		for path, membership := range currentMemberships {
			candidate.Memberships[path] = indexMembershipFromRow(membership)
		}
		for sourcePath, rows := range currentEdges {
			replacement, err := indexEdgeReplacementFromRows(sourcePath, rows)
			if err != nil {
				return nil, errUCIPublicationRejected
			}
			candidate.EdgeReplacements[sourcePath] = replacement
		}
	}

	seenMemberships := make(map[string]struct{})
	seenDeletions := make(map[string]struct{})
	seenReplacements := make(map[string]struct{})
	for _, part := range parts {
		if err := publisher.validateStagedPart(ctx, input.Build.Scope, *job.ProfileID, part); err != nil {
			return nil, err
		}
		for _, proof := range part.Artifacts {
			if existing, exists := candidate.ArtifactProofs[proof.ArtifactID]; exists && !sameUCIIndexArtifactProof(existing, proof) {
				return nil, errUCIPublicationRejected
			}
			candidate.ArtifactProofs[proof.ArtifactID] = proof
		}
		if mode == ucidomain.IndexManifestFull && len(part.Deletions) != 0 {
			return nil, errUCIPublicationRejected
		}
		for _, membership := range part.Memberships {
			if _, exists := seenMemberships[membership.PathKey]; exists {
				return nil, errUCIPublicationRejected
			}
			if _, deleted := seenDeletions[membership.PathKey]; deleted {
				return nil, errUCIPublicationRejected
			}
			seenMemberships[membership.PathKey] = struct{}{}
			candidate.Memberships[membership.PathKey] = membership
		}
		for _, deletion := range part.Deletions {
			if !deletion.ConfirmedMissing {
				return nil, errUCIPublicationRejected
			}
			if _, exists := seenDeletions[deletion.PathKey]; exists {
				return nil, errUCIPublicationRejected
			}
			if _, membership := seenMemberships[deletion.PathKey]; membership {
				return nil, errUCIPublicationRejected
			}
			seenDeletions[deletion.PathKey] = struct{}{}
			delete(candidate.Memberships, deletion.PathKey)
			candidate.DeletedPaths[deletion.PathKey] = struct{}{}
		}
		for _, replacement := range part.EdgeReplacements {
			if _, exists := seenReplacements[replacement.SourcePath]; exists {
				return nil, errUCIPublicationRejected
			}
			seenReplacements[replacement.SourcePath] = struct{}{}
			candidate.EdgeReplacements[replacement.SourcePath] = replacement
		}
	}
	if uint64(len(candidate.Memberships)) > publisher.limits.MaxManifestEntries {
		return nil, errUCIPublicationBuildIncomplete
	}
	if len(candidate.Memberships) == 0 && mode != ucidomain.IndexManifestFull {
		return nil, errUCIPublicationRejected
	}
	if err := validateUCIPublicationCandidateShape(candidate, mode); err != nil {
		return nil, err
	}
	if err := publisher.validateUCIPublicationCandidateArtifacts(ctx, input.Build.Scope, *job.ProfileID, candidate); err != nil {
		return nil, err
	}
	if err := validateUCIPublicationCandidateEdges(candidate); err != nil {
		return nil, err
	}
	memberships := sortedUCIPublicationMemberships(candidate.Memberships)
	replacements := sortedUCIPublicationReplacements(candidate.EdgeReplacements)
	manifestDigest, err := ucidomain.DigestIndexManifest(memberships)
	if err != nil || uint64(len(memberships)) != input.Manifest.EntryCount || manifestDigest != input.Manifest.ManifestDigest {
		return nil, errUCIPublicationBuildIncomplete
	}
	edgeCount := uint64(0)
	for _, replacement := range replacements {
		edgeCount += uint64(len(replacement.Edges))
	}
	edgesDigest, err := ucidomain.DigestIndexEdges(replacements)
	if err != nil || edgeCount != input.Manifest.EdgeCount || edgeCount > publisher.limits.MaxEdges || edgesDigest != input.Manifest.EdgesDigest {
		return nil, errUCIPublicationBuildIncomplete
	}
	return candidate, nil
}

func loadUCIPublicationParts(ctx context.Context, db *gorm.DB, buildID string) ([]ucidomain.IndexPart, []ucidomain.IndexPartAck, error) {
	var rows []UCIIndexBuildPart
	if err := db.WithContext(ctx).Where("build_id = ?", buildID).Order("sequence ASC").Find(&rows).Error; err != nil {
		return nil, nil, fmt.Errorf("uci publication load staged parts: %w", err)
	}
	parts := make([]ucidomain.IndexPart, 0, len(rows))
	acks := make([]ucidomain.IndexPartAck, 0, len(rows))
	for sequence, row := range rows {
		if row.Sequence != uint32(sequence) || validateUCIDigest("part_digest", row.PartDigest) != nil {
			return nil, nil, errUCIPublicationBuildIncomplete
		}
		var part ucidomain.IndexPart
		if err := json.Unmarshal([]byte(row.Payload), &part); err != nil {
			return nil, nil, errUCIPublicationBuildIncomplete
		}
		digest, err := ucidomain.DigestIndexPart(part)
		if err != nil || string(digest) != row.PartDigest {
			return nil, nil, errUCIPublicationBuildIncomplete
		}
		parts = append(parts, part)
		acks = append(acks, ucidomain.IndexPartAck{BuildID: buildID, Sequence: row.Sequence, Digest: digest})
	}
	return parts, acks, nil
}

func loadUCIPublicationCurrentProjection(ctx context.Context, db *gorm.DB, scope ucidomain.IndexScope) (map[string]UCIMembership, map[string][]UCIResolvedEdge, error) {
	var membershipRows []UCIMembership
	if err := db.WithContext(ctx).Where("source_id = ? AND checkout_id = ? AND valid_to_generation IS NULL", scope.SourceID, scope.CheckoutID).Find(&membershipRows).Error; err != nil {
		return nil, nil, fmt.Errorf("uci publication load current memberships: %w", err)
	}
	memberships := make(map[string]UCIMembership, len(membershipRows))
	for _, membership := range membershipRows {
		if _, duplicate := memberships[membership.PathKey]; duplicate {
			return nil, nil, errUCIPublicationRejected
		}
		memberships[membership.PathKey] = membership
	}
	var edgeRows []UCIResolvedEdge
	if err := db.WithContext(ctx).Where("source_id = ? AND checkout_id = ? AND valid_to_generation IS NULL", scope.SourceID, scope.CheckoutID).Order("source_path ASC, edge_key ASC").Find(&edgeRows).Error; err != nil {
		return nil, nil, fmt.Errorf("uci publication load current edges: %w", err)
	}
	edges := make(map[string][]UCIResolvedEdge)
	for _, edge := range edgeRows {
		edges[edge.SourcePath] = append(edges[edge.SourcePath], edge)
	}
	return memberships, edges, nil
}

func indexMembershipFromRow(row UCIMembership) ucidomain.IndexMembership {
	return ucidomain.IndexMembership{
		PathKey:     row.PathKey,
		DisplayPath: row.DisplayPath,
		Mode:        row.Mode,
		State:       ucidomain.IndexFileState(row.FileState),
		ArtifactID:  row.ArtifactID,
	}
}

func indexEdgeReplacementFromRows(sourcePath string, rows []UCIResolvedEdge) (ucidomain.IndexEdgeReplacement, error) {
	edges := make([]ucidomain.IndexEdge, 0, len(rows))
	for _, row := range rows {
		var evidence ucidomain.IndexEdgeEvidence
		if err := json.Unmarshal([]byte(row.EvidenceJSON), &evidence); err != nil {
			return ucidomain.IndexEdgeReplacement{}, err
		}
		var target *ucidomain.IndexEdgeTarget
		if row.TargetPath != nil && row.TargetArtifact != nil {
			target = &ucidomain.IndexEdgeTarget{PathKey: *row.TargetPath, ArtifactID: *row.TargetArtifact, SymbolKey: row.TargetSymbol}
		}
		edges = append(edges, ucidomain.IndexEdge{
			EdgeKey:          row.EdgeKey,
			SourceArtifactID: row.SourceArtifact,
			SourceSymbolKey:  row.SourceSymbol,
			Target:           target,
			Relation:         ucidomain.IndexRelation(row.Relation),
			EvidenceKind:     ucidomain.IndexEvidenceKind(row.EvidenceKind),
			ResolutionState:  ucidomain.IndexResolutionState(row.ResolutionState),
			ResolverRevision: row.ResolverRevision,
			Evidence:         evidence,
		})
	}
	return ucidomain.IndexEdgeReplacement{SourcePath: sourcePath, Edges: edges}, nil
}

func validateUCIPublicationCandidateShape(candidate *uciPublicationCandidate, mode ucidomain.IndexManifestMode) error {
	for sourcePath, replacement := range candidate.EdgeReplacements {
		_, member := candidate.Memberships[sourcePath]
		_, deleted := candidate.DeletedPaths[sourcePath]
		if mode == ucidomain.IndexManifestFull && !member {
			return errUCIPublicationRejected
		}
		if mode == ucidomain.IndexManifestDelta && !member && !deleted {
			return errUCIPublicationRejected
		}
		if !member && len(replacement.Edges) != 0 {
			return errUCIPublicationRejected
		}
	}
	if mode == ucidomain.IndexManifestFull {
		if len(candidate.EdgeReplacements) != len(candidate.Memberships) {
			return errUCIPublicationRejected
		}
		for path := range candidate.Memberships {
			if _, exists := candidate.EdgeReplacements[path]; !exists {
				return errUCIPublicationRejected
			}
		}
	}
	return nil
}

func (publisher *uciPublisher) validateUCIPublicationCandidateArtifacts(ctx context.Context, scope ucidomain.IndexScope, profileID string, candidate *uciPublicationCandidate) error {
	var profile UCIAnalysisProfile
	if err := publisher.store.db.WithContext(ctx).Where("profile_id = ?", profileID).First(&profile).Error; err != nil {
		return errUCIPublicationRejected
	}
	referenced := make(map[string]struct{})
	for _, membership := range candidate.Memberships {
		if membership.State != ucidomain.IndexFilePresent || membership.ArtifactID == nil {
			continue
		}
		artifactID := *membership.ArtifactID
		if _, exists := referenced[artifactID]; exists {
			continue
		}
		referenced[artifactID] = struct{}{}
		var artifact UCIParseArtifact
		if err := publisher.store.db.WithContext(ctx).Where("source_id = ? AND artifact_id = ?", scope.SourceID, artifactID).First(&artifact).Error; err != nil {
			return errUCIPublicationRejected
		}
		if artifact.ExtractionProfileDigest != profile.ParserBundleDigest ||
			(artifact.Status != UCIParseArtifactComplete && artifact.Status != UCIParseArtifactPartial) {
			return errUCIPublicationRejected
		}
		if artifact.SealedAt == nil {
			if _, proven := candidate.ArtifactProofs[artifactID]; !proven {
				return errUCIPublicationRejected
			}
		} else if artifact.FactsDigest == nil {
			return errUCIPublicationRejected
		}
		if err := publisher.validateArtifactSize(ctx, scope.SourceID, artifactID); err != nil {
			return err
		}
	}
	for artifactID := range candidate.ArtifactProofs {
		if _, used := referenced[artifactID]; !used {
			return errUCIPublicationRejected
		}
	}
	return nil
}

func validateUCIPublicationCandidateEdges(candidate *uciPublicationCandidate) error {
	seenEdgeKeys := make(map[string]struct{})
	for sourcePath, replacement := range candidate.EdgeReplacements {
		sourceMembership, sourceExists := candidate.Memberships[sourcePath]
		for _, edge := range replacement.Edges {
			if _, duplicate := seenEdgeKeys[edge.EdgeKey]; duplicate {
				return errUCIPublicationRejected
			}
			seenEdgeKeys[edge.EdgeKey] = struct{}{}
			if !sourceExists || sourceMembership.State != ucidomain.IndexFilePresent || sourceMembership.ArtifactID == nil || *sourceMembership.ArtifactID != edge.SourceArtifactID {
				return errUCIPublicationRejected
			}
			if edge.Target == nil {
				continue
			}
			targetMembership, targetExists := candidate.Memberships[edge.Target.PathKey]
			if !targetExists || targetMembership.State != ucidomain.IndexFilePresent || targetMembership.ArtifactID == nil || *targetMembership.ArtifactID != edge.Target.ArtifactID {
				return errUCIPublicationRejected
			}
		}
	}
	return nil
}

func sortedUCIPublicationMemberships(memberships map[string]ucidomain.IndexMembership) []ucidomain.IndexMembership {
	paths := make([]string, 0, len(memberships))
	for path := range memberships {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := make([]ucidomain.IndexMembership, 0, len(paths))
	for _, path := range paths {
		result = append(result, memberships[path])
	}
	return result
}

func sortedUCIPublicationReplacements(replacements map[string]ucidomain.IndexEdgeReplacement) []ucidomain.IndexEdgeReplacement {
	paths := make([]string, 0, len(replacements))
	for path := range replacements {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := make([]ucidomain.IndexEdgeReplacement, 0, len(paths))
	for _, path := range paths {
		result = append(result, replacements[path])
	}
	return result
}

func (publisher *uciPublisher) sealUCIPublicationArtifacts(ctx context.Context, tx *gorm.DB, sourceID string, proofs map[string]ucidomain.IndexArtifactProof, now time.Time) error {
	artifactIDs := make([]string, 0, len(proofs))
	for artifactID := range proofs {
		artifactIDs = append(artifactIDs, artifactID)
	}
	sort.Strings(artifactIDs)
	transactionalStore := &UCIProjectionStore{db: tx}
	for _, artifactID := range artifactIDs {
		proof := proofs[artifactID]
		var artifact UCIParseArtifact
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("source_id = ? AND artifact_id = ?", sourceID, artifactID).First(&artifact).Error; err != nil {
			return errUCIPublicationRejected
		}
		if (artifact.SealedAt == nil) != (artifact.FactsDigest == nil) {
			return errUCIProjectionImmutable
		}
		actual, err := transactionalStore.DescribeIndexArtifact(ctx, sourceID, artifactID)
		if err != nil || !sameUCIIndexArtifactProof(actual, proof) {
			return errUCIPublicationRejected
		}
		if artifact.SealedAt != nil {
			continue
		}
		factsDigest := string(proof.FactsDigest)
		result := tx.WithContext(ctx).Model(&UCIParseArtifact{}).Where("artifact_id = ? AND sealed_at IS NULL", artifactID).Updates(map[string]any{
			"facts_digest": factsDigest,
			"sealed_at":    now,
		})
		if result.Error != nil {
			return fmt.Errorf("uci publication seal artifact: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return errUCIProjectionImmutable
		}
	}
	return nil
}

func applyUCIPublicationMemberships(ctx context.Context, tx *gorm.DB, scope ucidomain.IndexScope, generation int64, current map[string]UCIMembership, candidate map[string]ucidomain.IndexMembership) error {
	paths := unionUCIPublicationPaths(current, candidate)
	now, err := uciDatabaseClock(ctx, tx)
	if err != nil {
		return err
	}
	for _, path := range paths {
		existing, hasExisting := current[path]
		next, hasNext := candidate[path]
		if hasExisting && (!hasNext || !sameUCIPublicationMembership(existing, next)) {
			if err := tx.WithContext(ctx).Model(&UCIMembership{}).Where("membership_id = ? AND valid_to_generation IS NULL", existing.MembershipID).Updates(map[string]any{
				"valid_to_generation": generation,
			}).Error; err != nil {
				return fmt.Errorf("uci publication close membership: %w", err)
			}
		}
		if hasNext && (!hasExisting || !sameUCIPublicationMembership(existing, next)) {
			row := UCIMembership{
				MembershipID:        uuid.NewString(),
				SourceID:            scope.SourceID,
				CheckoutID:          scope.CheckoutID,
				PathKey:             next.PathKey,
				DisplayPath:         next.DisplayPath,
				ArtifactID:          cloneUCIOptionalString(next.ArtifactID),
				FileState:           UCIFileState(next.State),
				Mode:                next.Mode,
				ValidFromGeneration: generation,
				CreatedAt:           now,
			}
			if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
				return fmt.Errorf("uci publication insert membership: %w", err)
			}
		}
	}
	return nil
}

func applyUCIPublicationEdges(ctx context.Context, tx *gorm.DB, scope ucidomain.IndexScope, generation int64, current map[string][]UCIResolvedEdge, candidate map[string]ucidomain.IndexEdgeReplacement) error {
	paths := make(map[string]struct{}, len(current)+len(candidate))
	for path := range current {
		paths[path] = struct{}{}
	}
	for path := range candidate {
		paths[path] = struct{}{}
	}
	orderedPaths := make([]string, 0, len(paths))
	for path := range paths {
		orderedPaths = append(orderedPaths, path)
	}
	sort.Strings(orderedPaths)
	now, err := uciDatabaseClock(ctx, tx)
	if err != nil {
		return err
	}
	for _, path := range orderedPaths {
		existing := current[path]
		next, hasNext := candidate[path]
		unchanged := hasNext && sameUCIPublicationEdgeReplacement(existing, next)
		if unchanged {
			continue
		}
		if len(existing) != 0 {
			if err := tx.WithContext(ctx).Model(&UCIResolvedEdge{}).Where(
				"checkout_id = ? AND source_path = ? AND valid_to_generation IS NULL", scope.CheckoutID, path,
			).Updates(map[string]any{"valid_to_generation": generation}).Error; err != nil {
				return fmt.Errorf("uci publication close edges: %w", err)
			}
		}
		if !hasNext {
			continue
		}
		for _, edge := range next.Edges {
			evidenceJSON, err := json.Marshal(edge.Evidence)
			if err != nil {
				return err
			}
			var targetPath, targetArtifact, targetSymbol *string
			if edge.Target != nil {
				targetPath = cloneUCIOptionalString(&edge.Target.PathKey)
				targetArtifact = cloneUCIOptionalString(&edge.Target.ArtifactID)
				targetSymbol = cloneUCIOptionalString(edge.Target.SymbolKey)
			}
			row := UCIResolvedEdge{
				ResolvedEdgeID:      uuid.NewString(),
				SourceID:            scope.SourceID,
				CheckoutID:          scope.CheckoutID,
				EdgeKey:             edge.EdgeKey,
				SourcePath:          next.SourcePath,
				SourceArtifact:      edge.SourceArtifactID,
				SourceSymbol:        cloneUCIOptionalString(edge.SourceSymbolKey),
				TargetPath:          targetPath,
				TargetArtifact:      targetArtifact,
				TargetSymbol:        targetSymbol,
				Relation:            string(edge.Relation),
				EvidenceKind:        UCIResolvedEdgeEvidenceKind(edge.EvidenceKind),
				ResolverRevision:    edge.ResolverRevision,
				EvidenceJSON:        string(evidenceJSON),
				ResolutionState:     UCIResolvedEdgeState(edge.ResolutionState),
				ValidFromGeneration: generation,
				CreatedAt:           now,
			}
			if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
				return fmt.Errorf("uci publication insert edge: %w", err)
			}
		}
	}
	return nil
}

func unionUCIPublicationPaths(current map[string]UCIMembership, candidate map[string]ucidomain.IndexMembership) []string {
	paths := make(map[string]struct{}, len(current)+len(candidate))
	for path := range current {
		paths[path] = struct{}{}
	}
	for path := range candidate {
		paths[path] = struct{}{}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	return ordered
}

func sameUCIPublicationMembership(existing UCIMembership, next ucidomain.IndexMembership) bool {
	return existing.PathKey == next.PathKey && existing.DisplayPath == next.DisplayPath && existing.Mode == next.Mode &&
		UCIFileState(next.State) == existing.FileState && sameUCIOptionalString(existing.ArtifactID, next.ArtifactID)
}

func sameUCIPublicationEdgeReplacement(existing []UCIResolvedEdge, next ucidomain.IndexEdgeReplacement) bool {
	current, err := indexEdgeReplacementFromRows(next.SourcePath, existing)
	if err != nil {
		return false
	}
	currentDigest, err := ucidomain.DigestIndexEdges([]ucidomain.IndexEdgeReplacement{current})
	if err != nil {
		return false
	}
	nextDigest, err := ucidomain.DigestIndexEdges([]ucidomain.IndexEdgeReplacement{next})
	return err == nil && currentDigest == nextDigest
}

func cloneUCIOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func marshalUCIPublicationCoverage(coverage ucidomain.IndexCoverage) (string, error) {
	encoded, err := json.Marshal(map[string]any{
		"structural":            string(coverage.Structural),
		"lexical":               string(coverage.Lexical),
		"vector":                string(coverage.Vector),
		"excluded_files":        coverage.ExcludedFiles,
		"unreadable_files":      coverage.UnreadableFiles,
		"unresolved_references": coverage.UnresolvedReferences,
	})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
