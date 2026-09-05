package uci

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// IndexAdmissionFrameVersion is the only JSON frame schema accepted by this package.
	IndexAdmissionFrameVersion = "uci-index-admission/v1"

	// IndexAdmissionMaxEncodedFrameBytes is the maximum exact encoded payload for one Stage frame.
	IndexAdmissionMaxEncodedFrameBytes = 1 << 20
	// IndexAdmissionMaxFrames is the maximum number of frames admitted in one build.
	IndexAdmissionMaxFrames = 1024
	// IndexAdmissionMaxTotalEncodedBytes is the maximum exact payload bytes admitted in one build.
	IndexAdmissionMaxTotalEncodedBytes = 16 << 20
	// IndexAdmissionMaxArtifactBodyBytes bounds the private source bytes retained for one artifact.
	IndexAdmissionMaxArtifactBodyBytes = 1 << 20

	indexAdmissionMaxArtifactsPerFrame        = 128
	indexAdmissionMaxMembershipsPerFrame      = 4 << 10
	indexAdmissionMaxDeletionsPerFrame        = 4 << 10
	indexAdmissionMaxEdgeReplacementsPerFrame = 4 << 10
	indexAdmissionMaxEdgesPerReplacement      = 4 << 10
	indexAdmissionMaxDefinitionsPerArtifact   = 2_048
	indexAdmissionMaxReferencesPerArtifact    = 8_192
	indexAdmissionMaxChunksPerArtifact        = 2_048
	indexAdmissionMaxDiagnosticsPerArtifact   = 64
	indexAdmissionMaxPathBytes                = 4 << 10
	indexAdmissionMaxKeyBytes                 = 4 << 10
	indexAdmissionMaxTextBytes                = 64 << 10
	indexAdmissionMaxProfileBytes             = 4 << 10
	indexAdmissionMaxDiagnosticBytes          = 4 << 10
)

// IndexAdmissionLanguage is the closed language vocabulary for artifacts with
// retained source bodies. More languages are admitted only when a concrete,
// source-grounded extractor is wired for them.
type IndexAdmissionLanguage string

const (
	// IndexAdmissionLanguageGo is backed by ExtractGo.
	IndexAdmissionLanguageGo IndexAdmissionLanguage = "go"
)

// IndexAdmissionArtifactStatus records the closed extraction outcome for an
// artifact carrying source bytes. Unsupported and protected files are instead
// represented by non-present memberships and never by a body-less artifact.
type IndexAdmissionArtifactStatus string

const (
	IndexAdmissionArtifactComplete IndexAdmissionArtifactStatus = "complete"
	IndexAdmissionArtifactPartial  IndexAdmissionArtifactStatus = "partial"
)

// IndexAdmissionMembershipState records a path's explicit current admission
// state. Non-present states never reference an artifact body.
type IndexAdmissionMembershipState string

const (
	IndexAdmissionMembershipPresent     IndexAdmissionMembershipState = "present"
	IndexAdmissionMembershipExcluded    IndexAdmissionMembershipState = "excluded"
	IndexAdmissionMembershipUnreadable  IndexAdmissionMembershipState = "unreadable"
	IndexAdmissionMembershipUnsupported IndexAdmissionMembershipState = "unsupported"
	IndexAdmissionMembershipProtected   IndexAdmissionMembershipState = "protected"
)

// IndexAdmissionProfile identifies the server-owned analysis profile selected
// in the frame. It deliberately carries no Source, checkout, or path authority.
type IndexAdmissionProfile struct {
	ID string `json:"id"`
}

// IndexAdmissionArtifactProfile records exactly the parser inputs used in an
// artifact identity. It is data, not an authority grant.
type IndexAdmissionArtifactProfile struct {
	Language                IndexAdmissionLanguage `json:"language"`
	ParserRevision          string                 `json:"parser_revision"`
	GrammarDigest           IndexDigest            `json:"grammar_digest"`
	ExtractionProfileDigest IndexDigest            `json:"extraction_profile_digest"`
}

// IndexAdmissionFrame is a bounded private domain payload for one or more
// source artifacts. Source and checkout authority are intentionally absent;
// callers must validate it against a server-issued IndexBinding.
type IndexAdmissionFrame struct {
	Version          string                          `json:"version"`
	Profile          IndexAdmissionProfile           `json:"profile"`
	Artifacts        []IndexAdmissionArtifact        `json:"artifacts"`
	Memberships      []IndexAdmissionMembership      `json:"memberships"`
	Deletions        []IndexAdmissionDeletion        `json:"deletions"`
	EdgeReplacements []IndexAdmissionEdgeReplacement `json:"edge_replacements"`
}

// IndexAdmissionArtifact is one source-body-bound extraction product. Body is
// the only source payload retained by this schema; it has no locator or path.
type IndexAdmissionArtifact struct {
	ArtifactID    string                        `json:"artifact_id"`
	ContentDigest IndexDigest                   `json:"content_digest"`
	FactsDigest   IndexDigest                   `json:"facts_digest"`
	Profile       IndexAdmissionArtifactProfile `json:"profile"`
	Status        IndexAdmissionArtifactStatus  `json:"status"`
	Body          []byte                        `json:"body"`
	Definitions   []IndexAdmissionDefinition    `json:"definitions"`
	References    []IndexAdmissionReference     `json:"references"`
	Chunks        []IndexAdmissionChunk         `json:"chunks"`
	Diagnostics   []IndexAdmissionDiagnostic    `json:"diagnostics"`
}

// IndexAdmissionDefinition is a normalized, source-grounded declaration.
type IndexAdmissionDefinition struct {
	LocalSymbolKey string    `json:"local_symbol_key"`
	Kind           string    `json:"kind"`
	SymbolKey      string    `json:"symbol_key"`
	Span           IndexSpan `json:"span"`
}

// IndexAdmissionReference is a normalized, source-grounded reference site.
// RawTarget must be exactly the artifact body slice named by Span.
type IndexAdmissionReference struct {
	SiteKey   string        `json:"site_key"`
	Kind      string        `json:"kind"`
	SymbolKey string        `json:"symbol_key"`
	RawTarget string        `json:"raw_target"`
	Relation  IndexRelation `json:"relation"`
	Span      IndexSpan     `json:"span"`
}

// IndexAdmissionChunk is a searchable excerpt that must exactly match Body at
// Span and carries its own content digest.
type IndexAdmissionChunk struct {
	Ordinal       int         `json:"ordinal"`
	SymbolKey     *string     `json:"symbol_key"`
	Kind          string      `json:"kind"`
	Span          IndexSpan   `json:"span"`
	ContentDigest IndexDigest `json:"content_digest"`
	Text          string      `json:"text"`
}

// IndexAdmissionDiagnostic records a bounded extraction limitation. A zero
// span is allowed only for an artifact-global diagnostic.
type IndexAdmissionDiagnostic struct {
	Code    string    `json:"code"`
	Span    IndexSpan `json:"span"`
	Message string    `json:"message"`
}

// IndexAdmissionMembership maps a relative checkout path to one admitted
// artifact or explicitly records why that path has no body.
type IndexAdmissionMembership struct {
	PathKey     string                        `json:"path_key"`
	DisplayPath string                        `json:"display_path"`
	Mode        string                        `json:"mode"`
	State       IndexAdmissionMembershipState `json:"state"`
	ArtifactID  *string                       `json:"artifact_id"`
}

// IndexAdmissionDeletion records an explicit confirmed-missing delta path.
type IndexAdmissionDeletion struct {
	PathKey          string `json:"path_key"`
	ConfirmedMissing bool   `json:"confirmed_missing"`
}

// IndexAdmissionEdgeReplacement replaces every resolved edge emitted by one
// relative source path in this frame.
type IndexAdmissionEdgeReplacement struct {
	SourcePath string               `json:"source_path"`
	Edges      []IndexAdmissionEdge `json:"edges"`
}

// IndexAdmissionEdge is a source-fact-bound directed edge. Its evidence names
// a frame-local reference-site key; the server maps that key to its durable ID.
type IndexAdmissionEdge struct {
	EdgeKey          string                     `json:"edge_key"`
	SourceArtifactID string                     `json:"source_artifact_id"`
	SourceSymbolKey  *string                    `json:"source_symbol_key"`
	Target           *IndexAdmissionEdgeTarget  `json:"target"`
	Relation         IndexRelation              `json:"relation"`
	EvidenceKind     IndexEvidenceKind          `json:"evidence_kind"`
	ResolutionState  IndexResolutionState       `json:"resolution_state"`
	ResolverRevision string                     `json:"resolver_revision"`
	Evidence         IndexAdmissionEdgeEvidence `json:"evidence"`
}

// IndexAdmissionEdgeTarget names one resolved target in the current frame.
type IndexAdmissionEdgeTarget struct {
	PathKey    string  `json:"path_key"`
	ArtifactID string  `json:"artifact_id"`
	SymbolKey  *string `json:"symbol_key"`
}

// IndexAdmissionEdgeEvidence binds an edge to a source reference site rather
// than to a client-invented durable database identifier.
type IndexAdmissionEdgeEvidence struct {
	ReferenceSiteKey string    `json:"reference_site_key"`
	Span             IndexSpan `json:"span"`
	RuleKey          string    `json:"rule_key"`
	Explanation      string    `json:"explanation"`
}

// IndexAdmissionReferenceKey identifies a frame reference site for a durable
// server reference-site ID binding.
type IndexAdmissionReferenceKey struct {
	ArtifactID string
	SiteKey    string
}

// IndexAdmissionReferenceBindings maps validated frame-local reference site
// keys to deterministic source-site UUIDs. Storage adapters may separately
// associate the same key with their internal row identity.
type IndexAdmissionReferenceBindings map[IndexAdmissionReferenceKey]string

// ReferenceBindings derives the deterministic source-site IDs used by a
// canonical publication part. The IDs are stable across daemon and server
// processes because ArtifactID is already source-scoped.
func (frame IndexAdmissionFrame) ReferenceBindings() (IndexAdmissionReferenceBindings, error) {
	canonical, err := frame.Canonicalize()
	if err != nil {
		return nil, err
	}
	return indexAdmissionReferenceBindings(canonical)
}

// DecodeIndexAdmissionFrame decodes exactly one strict JSON frame. Unknown
// fields, duplicate object keys, trailing values, structural invalidity, and a
// payload larger than the per-frame cap are rejected.
func DecodeIndexAdmissionFrame(encoded []byte) (IndexAdmissionFrame, error) {
	if len(encoded) > IndexAdmissionMaxEncodedFrameBytes {
		return IndexAdmissionFrame{}, fmt.Errorf("uci index admission: frame exceeds %d-byte cap", IndexAdmissionMaxEncodedFrameBytes)
	}
	if err := indexAdmissionRejectDuplicateJSONKeys(encoded); err != nil {
		return IndexAdmissionFrame{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var frame IndexAdmissionFrame
	if err := decoder.Decode(&frame); err != nil {
		return IndexAdmissionFrame{}, fmt.Errorf("uci index admission: decode frame: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return IndexAdmissionFrame{}, fmt.Errorf("uci index admission: frame must contain exactly one JSON value")
		}
		return IndexAdmissionFrame{}, fmt.Errorf("uci index admission: trailing frame data: %w", err)
	}
	if err := ValidateIndexAdmissionFrame(frame); err != nil {
		return IndexAdmissionFrame{}, err
	}
	return frame, nil
}

// EncodeIndexAdmissionFrame canonicalizes and encodes one strict frame. It
// rejects oversize output rather than truncating any artifact or fact.
func EncodeIndexAdmissionFrame(frame IndexAdmissionFrame) ([]byte, error) {
	canonical, err := frame.Canonicalize()
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("uci index admission: encode frame: %w", err)
	}
	if len(encoded) > IndexAdmissionMaxEncodedFrameBytes {
		return nil, fmt.Errorf("uci index admission: frame exceeds %d-byte cap", IndexAdmissionMaxEncodedFrameBytes)
	}
	return encoded, nil
}

// DigestIndexAdmissionPayload returns the SHA-256 of the exact supplied frame
// bytes. It never canonicalizes or reparses those bytes.
func DigestIndexAdmissionPayload(encoded []byte) IndexDigest {
	return indexAdmissionDigestBytes(encoded)
}

// ValidateIndexAdmissionFrame validates the strict domain shape but cannot
// verify source-scoped artifact IDs because a frame deliberately has no Source.
func ValidateIndexAdmissionFrame(frame IndexAdmissionFrame) error {
	canonical, err := frame.Canonicalize()
	if err != nil {
		return err
	}
	_, err = indexAdmissionCanonicalFrameEncodedSize(canonical)
	return err
}

// ValidateIndexAdmissionFrameForBinding validates a strict frame against the
// only authority for its Source, checkout, incarnation, and analysis profile.
func ValidateIndexAdmissionFrameForBinding(frame IndexAdmissionFrame, binding IndexBinding) error {
	canonical, err := frame.Canonicalize()
	if err != nil {
		return err
	}
	if _, err := indexAdmissionCanonicalFrameEncodedSize(canonical); err != nil {
		return err
	}
	if err := binding.Validate(); err != nil {
		return fmt.Errorf("uci index admission: invalid binding: %w", err)
	}
	if canonical.Profile.ID != binding.ProfileID {
		return fmt.Errorf("uci index admission: frame profile does not match binding")
	}
	for _, artifact := range canonical.Artifacts {
		expected, err := DeriveIndexAdmissionArtifactID(binding.Scope.SourceID, artifact.ContentDigest, artifact.Profile)
		if err != nil {
			return err
		}
		if artifact.ArtifactID != expected {
			return fmt.Errorf("uci index admission: artifact identity does not match bound source")
		}
	}
	return nil
}

// ValidateIndexAdmissionFrames validates all frames together as one bounded
// build. It resolves artifact, membership, reference, and edge endpoints from
// one global catalog so a packed frame may point at facts in another frame.
func ValidateIndexAdmissionFrames(frames []IndexAdmissionFrame) error {
	canonical, err := indexAdmissionCanonicalizeFrames(frames)
	if err != nil {
		return err
	}
	return indexAdmissionValidateFrameSet(canonical)
}

// ValidateIndexAdmissionFramesForBinding validates a complete packed build
// against the server-issued authority and its global admission catalog.
func ValidateIndexAdmissionFramesForBinding(frames []IndexAdmissionFrame, binding IndexBinding) error {
	if err := binding.Validate(); err != nil {
		return fmt.Errorf("uci index admission: invalid binding: %w", err)
	}
	canonical, err := indexAdmissionCanonicalizeFrames(frames)
	if err != nil {
		return err
	}
	for _, frame := range canonical {
		if frame.Profile.ID != binding.ProfileID {
			return fmt.Errorf("uci index admission: frame profile does not match binding")
		}
		for _, artifact := range frame.Artifacts {
			expected, err := DeriveIndexAdmissionArtifactID(binding.Scope.SourceID, artifact.ContentDigest, artifact.Profile)
			if err != nil {
				return err
			}
			if artifact.ArtifactID != expected {
				return fmt.Errorf("uci index admission: artifact identity does not match bound source")
			}
		}
	}
	return indexAdmissionValidateFrameSet(canonical)
}

// ValidateIndexAdmissionPayloads enforces exact encoded-byte limits, strictly
// decodes every frame, and validates the resulting packed build as one global
// admission catalog.
func ValidateIndexAdmissionPayloads(payloads [][]byte) error {
	if len(payloads) > IndexAdmissionMaxFrames {
		return fmt.Errorf("uci index admission: frame count exceeds %d", IndexAdmissionMaxFrames)
	}
	frames := make([]IndexAdmissionFrame, 0, len(payloads))
	total := 0
	for _, payload := range payloads {
		if len(payload) > IndexAdmissionMaxEncodedFrameBytes {
			return fmt.Errorf("uci index admission: frame exceeds %d-byte cap", IndexAdmissionMaxEncodedFrameBytes)
		}
		if len(payload) > IndexAdmissionMaxTotalEncodedBytes-total {
			return fmt.Errorf("uci index admission: total payload exceeds %d-byte cap", IndexAdmissionMaxTotalEncodedBytes)
		}
		total += len(payload)
		frame, err := DecodeIndexAdmissionFrame(payload)
		if err != nil {
			return err
		}
		frames = append(frames, frame)
	}
	return ValidateIndexAdmissionFrames(frames)
}

// Clone returns a deep copy that shares no mutable slice, byte buffer, or
// optional-string pointer with the source frame.
func (frame IndexAdmissionFrame) Clone() IndexAdmissionFrame {
	copy := frame
	copy.Artifacts = indexAdmissionCloneArtifacts(frame.Artifacts)
	copy.Memberships = indexAdmissionCloneMemberships(frame.Memberships)
	copy.Deletions = append([]IndexAdmissionDeletion(nil), frame.Deletions...)
	if frame.Deletions != nil && copy.Deletions == nil {
		copy.Deletions = []IndexAdmissionDeletion{}
	}
	copy.EdgeReplacements = indexAdmissionCloneEdgeReplacements(frame.EdgeReplacements)
	return copy
}

// Canonicalize returns a deep-cloned, validation-checked frame with every
// unordered collection in deterministic key order.
func (frame IndexAdmissionFrame) Canonicalize() (IndexAdmissionFrame, error) {
	canonical := frame.Clone()
	if err := indexAdmissionCanonicalizeFrame(&canonical); err != nil {
		return IndexAdmissionFrame{}, err
	}
	return canonical, nil
}

// PublicationPart converts a frame to the deterministic staged publication
// representation used by both daemon finalization and server staging.
func (frame IndexAdmissionFrame) PublicationPart() (IndexPart, error) {
	return frame.IndexPart(nil)
}

// IndexPart converts a validated frame into a canonical publication part. A
// nil bindings map derives stable reference-site IDs; a non-nil map permits a
// storage adapter to supply the exact same validated bindings explicitly.
// Call ValidateIndexAdmissionFrameForBinding first to verify source-scoped
// artifact identities against server authority.
func (frame IndexAdmissionFrame) IndexPart(bindings IndexAdmissionReferenceBindings) (IndexPart, error) {
	canonical, err := frame.Canonicalize()
	if err != nil {
		return IndexPart{}, err
	}
	deriveMissingBindings := bindings == nil
	if deriveMissingBindings {
		bindings, err = indexAdmissionReferenceBindings(canonical)
		if err != nil {
			return IndexPart{}, err
		}
	}

	part := IndexPart{
		Artifacts:        make([]IndexArtifactProof, 0, len(canonical.Artifacts)),
		Memberships:      make([]IndexMembership, 0, len(canonical.Memberships)),
		Deletions:        make([]IndexDeletion, 0, len(canonical.Deletions)),
		EdgeReplacements: make([]IndexEdgeReplacement, 0, len(canonical.EdgeReplacements)),
	}
	for _, artifact := range canonical.Artifacts {
		factsDigest, err := indexAdmissionArtifactFactsDigest(artifact)
		if err != nil {
			return IndexPart{}, err
		}
		part.Artifacts = append(part.Artifacts, IndexArtifactProof{
			ArtifactID:         artifact.ArtifactID,
			ContentDigest:      artifact.ContentDigest,
			FactsDigest:        factsDigest,
			DefinitionCount:    uint64(len(artifact.Definitions)),
			ReferenceSiteCount: uint64(len(artifact.References)),
			ChunkCount:         uint64(len(artifact.Chunks)),
		})
	}
	for _, membership := range canonical.Memberships {
		state, err := indexAdmissionIndexFileState(membership.State)
		if err != nil {
			return IndexPart{}, err
		}
		part.Memberships = append(part.Memberships, IndexMembership{
			PathKey:     membership.PathKey,
			DisplayPath: membership.DisplayPath,
			Mode:        membership.Mode,
			State:       state,
			ArtifactID:  indexAdmissionCopyStringPointer(membership.ArtifactID),
		})
	}
	for _, deletion := range canonical.Deletions {
		part.Deletions = append(part.Deletions, IndexDeletion{
			PathKey:          deletion.PathKey,
			ConfirmedMissing: deletion.ConfirmedMissing,
		})
	}
	for _, replacement := range canonical.EdgeReplacements {
		converted := IndexEdgeReplacement{
			SourcePath: replacement.SourcePath,
			Edges:      make([]IndexEdge, 0, len(replacement.Edges)),
		}
		for _, edge := range replacement.Edges {
			key := IndexAdmissionReferenceKey{
				ArtifactID: edge.SourceArtifactID,
				SiteKey:    edge.Evidence.ReferenceSiteKey,
			}
			referenceID, found := bindings[key]
			if !found && deriveMissingBindings {
				referenceID, err = DeriveIndexAdmissionReferenceSiteID(key.ArtifactID, key.SiteKey)
				if err != nil {
					return IndexPart{}, err
				}
				found = true
			}
			if !found || !canonicalContextUUID(referenceID) {
				return IndexPart{}, fmt.Errorf("uci index admission: missing durable reference-site binding")
			}
			convertedEdge := IndexEdge{
				EdgeKey:          edge.EdgeKey,
				SourceArtifactID: edge.SourceArtifactID,
				SourceSymbolKey:  indexAdmissionCopyStringPointer(edge.SourceSymbolKey),
				Relation:         edge.Relation,
				EvidenceKind:     edge.EvidenceKind,
				ResolutionState:  edge.ResolutionState,
				ResolverRevision: edge.ResolverRevision,
				Evidence: IndexEdgeEvidence{
					ReferenceSiteID: indexAdmissionStringPointer(referenceID),
					Span:            edge.Evidence.Span,
					RuleKey:         edge.Evidence.RuleKey,
					Explanation:     edge.Evidence.Explanation,
				},
			}
			if edge.Target != nil {
				convertedEdge.Target = &IndexEdgeTarget{
					PathKey:    edge.Target.PathKey,
					ArtifactID: edge.Target.ArtifactID,
					SymbolKey:  indexAdmissionCopyStringPointer(edge.Target.SymbolKey),
				}
			}
			converted.Edges = append(converted.Edges, convertedEdge)
		}
		part.EdgeReplacements = append(part.EdgeReplacements, converted)
	}
	return normalizeIndexPart(part)
}

// DeriveIndexAdmissionArtifactID deterministically derives the canonical UUID
// for exactly one Source and immutable content/parser profile tuple.
func DeriveIndexAdmissionArtifactID(sourceID string, contentDigest IndexDigest, profile IndexAdmissionArtifactProfile) (string, error) {
	if !canonicalContextUUID(sourceID) {
		return "", fmt.Errorf("uci index admission: invalid source ID")
	}
	if !isIndexDigest(contentDigest) {
		return "", fmt.Errorf("uci index admission: invalid content digest")
	}
	if err := indexAdmissionValidateArtifactProfile(profile); err != nil {
		return "", err
	}

	state := sha256.New()
	indexAdmissionWriteHashString(state, "uci-index-admission-artifact/v1")
	indexAdmissionWriteHashString(state, sourceID)
	indexAdmissionWriteHashString(state, string(contentDigest))
	indexAdmissionWriteHashString(state, string(profile.Language))
	indexAdmissionWriteHashString(state, profile.ParserRevision)
	indexAdmissionWriteHashString(state, string(profile.GrammarDigest))
	indexAdmissionWriteHashString(state, string(profile.ExtractionProfileDigest))

	sum := state.Sum(nil)
	var identifier [16]byte
	copy(identifier[:], sum[:len(identifier)])
	identifier[6] = identifier[6]&0x0f | 0x50
	identifier[8] = identifier[8]&0x3f | 0x80
	return indexAdmissionUUID(identifier), nil
}

// DeriveIndexAdmissionReferenceSiteID deterministically derives the canonical
// source-site UUID for one artifact-local reference key. ArtifactID is already
// source-scoped, so equal site keys in distinct Sources cannot collide.
func DeriveIndexAdmissionReferenceSiteID(artifactID, siteKey string) (string, error) {
	if !canonicalContextUUID(artifactID) || !indexAdmissionValidKey(siteKey) {
		return "", fmt.Errorf("uci index admission: invalid reference-site identity")
	}
	state := sha256.New()
	indexAdmissionWriteHashString(state, "uci-index-admission-reference-site/v1")
	indexAdmissionWriteHashString(state, artifactID)
	indexAdmissionWriteHashString(state, siteKey)
	sum := state.Sum(nil)
	var identifier [16]byte
	copy(identifier[:], sum[:len(identifier)])
	identifier[6] = identifier[6]&0x0f | 0x50
	identifier[8] = identifier[8]&0x3f | 0x80
	return indexAdmissionUUID(identifier), nil
}

// DigestIndexAdmissionArtifactFacts returns the deterministic digest over an
// artifact's canonical source-grounded facts. ArtifactID and the digest field
// itself are excluded; source bytes are bound by ContentDigest validation.
func DigestIndexAdmissionArtifactFacts(artifact IndexAdmissionArtifact) (IndexDigest, error) {
	canonical, err := indexAdmissionCanonicalizeArtifact(artifact)
	if err != nil {
		return "", err
	}
	return indexAdmissionArtifactFactsDigest(canonical)
}

// GoIndexAdmissionArtifactProfile derives the fixed parser metadata for an
// ExtractGo result from its caller-selected extraction profile.
func GoIndexAdmissionArtifactProfile(profile GoExtractionProfile) (IndexAdmissionArtifactProfile, error) {
	if !goExtractionProfileValid(profile) {
		return IndexAdmissionArtifactProfile{}, fmt.Errorf("uci index admission: invalid Go extraction profile")
	}
	profileBytes, err := json.Marshal(struct {
		Version    string `json:"version"`
		ProfileKey string `json:"profile_key"`
		ParserKey  string `json:"parser_key"`
	}{
		Version:    "uci-go-admission-profile/v1",
		ProfileKey: profile.ProfileKey,
		ParserKey:  profile.ParserKey,
	})
	if err != nil {
		return IndexAdmissionArtifactProfile{}, fmt.Errorf("uci index admission: encode Go extraction profile: %w", err)
	}
	result := IndexAdmissionArtifactProfile{
		Language:                IndexAdmissionLanguageGo,
		ParserRevision:          goExtractionParserRevision,
		GrammarDigest:           indexAdmissionDigestBytes([]byte("uci-go-grammar/v1\x00" + goExtractionParserRevision)),
		ExtractionProfileDigest: indexAdmissionDigestBytes(profileBytes),
	}
	if err := indexAdmissionValidateArtifactProfile(result); err != nil {
		return IndexAdmissionArtifactProfile{}, err
	}
	return result, nil
}

// NewIndexAdmissionArtifactFromGo converts an ExtractGo result and its exact
// caller-owned source bytes into one validated generic admission artifact.
func NewIndexAdmissionArtifactFromGo(sourceID string, profile IndexAdmissionArtifactProfile, source []byte, extracted GoArtifact) (IndexAdmissionArtifact, error) {
	if profile.Language != IndexAdmissionLanguageGo {
		return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: Go artifact requires Go profile")
	}
	if len(source) > IndexAdmissionMaxArtifactBodyBytes {
		return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: artifact body exceeds %d-byte cap", IndexAdmissionMaxArtifactBodyBytes)
	}
	contentDigest := indexAdmissionDigestBytes(source)
	if extracted.Proof.ContentDigest != contentDigest {
		return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: Go artifact source digest mismatch")
	}

	status := IndexAdmissionArtifactPartial
	switch extracted.Coverage {
	case IndexCoverageComplete:
		status = IndexAdmissionArtifactComplete
	case IndexCoveragePartial:
		status = IndexAdmissionArtifactPartial
	default:
		return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: Go extraction has unsupported coverage")
	}
	artifactID, err := DeriveIndexAdmissionArtifactID(sourceID, contentDigest, profile)
	if err != nil {
		return IndexAdmissionArtifact{}, err
	}
	artifact := IndexAdmissionArtifact{
		ArtifactID:    artifactID,
		ContentDigest: contentDigest,
		Profile:       profile,
		Status:        status,
		Body:          indexAdmissionCloneBytes(source),
		Definitions:   make([]IndexAdmissionDefinition, 0, len(extracted.Definitions)),
		References:    make([]IndexAdmissionReference, 0, len(extracted.References)),
		Chunks:        make([]IndexAdmissionChunk, 0, len(extracted.Chunks)),
		Diagnostics:   make([]IndexAdmissionDiagnostic, 0, len(extracted.Diagnostics)),
	}
	for _, definition := range extracted.Definitions {
		artifact.Definitions = append(artifact.Definitions, IndexAdmissionDefinition{
			LocalSymbolKey: definition.LocalKey,
			Kind:           definition.Kind,
			SymbolKey:      definition.SymbolKey,
			Span:           definition.Span,
		})
	}
	for _, reference := range extracted.References {
		rawTarget, err := indexAdmissionTextAtSpan(source, reference.Span)
		if err != nil {
			return IndexAdmissionArtifact{}, err
		}
		relation, err := indexAdmissionGoReferenceRelation(reference.Kind)
		if err != nil {
			return IndexAdmissionArtifact{}, err
		}
		artifact.References = append(artifact.References, IndexAdmissionReference{
			SiteKey:   indexAdmissionGoReferenceSiteKey(reference),
			Kind:      reference.Kind,
			SymbolKey: reference.SymbolKey,
			RawTarget: rawTarget,
			Relation:  relation,
			Span:      reference.Span,
		})
	}
	for ordinal, chunk := range extracted.Chunks {
		artifact.Chunks = append(artifact.Chunks, IndexAdmissionChunk{
			Ordinal:       ordinal,
			Kind:          "source",
			Span:          chunk.Span,
			ContentDigest: chunk.ContentDigest,
			Text:          chunk.Text,
		})
	}
	for _, diagnostic := range extracted.Diagnostics {
		artifact.Diagnostics = append(artifact.Diagnostics, IndexAdmissionDiagnostic{
			Code:    diagnostic.Code,
			Span:    diagnostic.Span,
			Message: diagnostic.Message,
		})
	}
	canonical, err := indexAdmissionCanonicalizeArtifact(artifact)
	if err != nil {
		return IndexAdmissionArtifact{}, err
	}
	factsDigest, err := indexAdmissionArtifactFactsDigest(canonical)
	if err != nil {
		return IndexAdmissionArtifact{}, err
	}
	canonical.FactsDigest = factsDigest
	return canonical, nil
}

func indexAdmissionCanonicalizeFrame(frame *IndexAdmissionFrame) error {
	if frame == nil || frame.Version != IndexAdmissionFrameVersion {
		return fmt.Errorf("uci index admission: unsupported frame version")
	}
	if !canonicalContextUUID(frame.Profile.ID) {
		return fmt.Errorf("uci index admission: invalid profile ID")
	}
	if len(frame.Artifacts) > indexAdmissionMaxArtifactsPerFrame ||
		len(frame.Memberships) > indexAdmissionMaxMembershipsPerFrame ||
		len(frame.Deletions) > indexAdmissionMaxDeletionsPerFrame ||
		len(frame.EdgeReplacements) > indexAdmissionMaxEdgeReplacementsPerFrame {
		return fmt.Errorf("uci index admission: frame collection exceeds limit")
	}

	if frame.Artifacts == nil {
		frame.Artifacts = []IndexAdmissionArtifact{}
	}
	for index, artifact := range frame.Artifacts {
		canonical, err := indexAdmissionCanonicalizeArtifact(artifact)
		if err != nil {
			return err
		}
		if !isIndexDigest(canonical.FactsDigest) {
			return fmt.Errorf("uci index admission: invalid facts digest")
		}
		expected, err := indexAdmissionArtifactFactsDigest(canonical)
		if err != nil {
			return err
		}
		if canonical.FactsDigest != expected {
			return fmt.Errorf("uci index admission: facts digest mismatch")
		}
		frame.Artifacts[index] = canonical
	}
	sort.Slice(frame.Artifacts, func(left, right int) bool {
		return frame.Artifacts[left].ArtifactID < frame.Artifacts[right].ArtifactID
	})
	for index, artifact := range frame.Artifacts {
		if index > 0 && frame.Artifacts[index-1].ArtifactID == artifact.ArtifactID {
			return fmt.Errorf("uci index admission: duplicate artifact ID")
		}
	}

	if frame.Memberships == nil {
		frame.Memberships = []IndexAdmissionMembership{}
	}
	sort.Slice(frame.Memberships, func(left, right int) bool {
		return frame.Memberships[left].PathKey < frame.Memberships[right].PathKey
	})
	memberships := make(map[string]IndexAdmissionMembership, len(frame.Memberships))
	for index, membership := range frame.Memberships {
		if err := indexAdmissionValidateMembership(membership); err != nil {
			return err
		}
		if index > 0 && frame.Memberships[index-1].PathKey == membership.PathKey {
			return fmt.Errorf("uci index admission: duplicate membership path")
		}
		memberships[membership.PathKey] = membership
	}

	if frame.Deletions == nil {
		frame.Deletions = []IndexAdmissionDeletion{}
	}
	sort.Slice(frame.Deletions, func(left, right int) bool {
		return frame.Deletions[left].PathKey < frame.Deletions[right].PathKey
	})
	for index, deletion := range frame.Deletions {
		if !indexAdmissionValidPath(deletion.PathKey) || !deletion.ConfirmedMissing {
			return fmt.Errorf("uci index admission: invalid deletion")
		}
		if index > 0 && frame.Deletions[index-1].PathKey == deletion.PathKey {
			return fmt.Errorf("uci index admission: duplicate deletion path")
		}
		if _, found := memberships[deletion.PathKey]; found {
			return fmt.Errorf("uci index admission: deletion conflicts with membership")
		}
	}

	if frame.EdgeReplacements == nil {
		frame.EdgeReplacements = []IndexAdmissionEdgeReplacement{}
	}
	sort.Slice(frame.EdgeReplacements, func(left, right int) bool {
		return frame.EdgeReplacements[left].SourcePath < frame.EdgeReplacements[right].SourcePath
	})
	for replacementIndex := range frame.EdgeReplacements {
		replacement := &frame.EdgeReplacements[replacementIndex]
		if !indexAdmissionValidPath(replacement.SourcePath) {
			return fmt.Errorf("uci index admission: invalid edge replacement path")
		}
		if replacementIndex > 0 && frame.EdgeReplacements[replacementIndex-1].SourcePath == replacement.SourcePath {
			return fmt.Errorf("uci index admission: duplicate edge replacement path")
		}
		if len(replacement.Edges) > indexAdmissionMaxEdgesPerReplacement {
			return fmt.Errorf("uci index admission: edge replacement exceeds limit")
		}
		if replacement.Edges == nil {
			replacement.Edges = []IndexAdmissionEdge{}
		}
		sort.Slice(replacement.Edges, func(left, right int) bool {
			return replacement.Edges[left].EdgeKey < replacement.Edges[right].EdgeKey
		})
		for edgeIndex, edge := range replacement.Edges {
			if edgeIndex > 0 && replacement.Edges[edgeIndex-1].EdgeKey == edge.EdgeKey {
				return fmt.Errorf("uci index admission: duplicate edge key")
			}
			if err := indexAdmissionValidateEdgeShape(edge); err != nil {
				return err
			}
		}
	}
	return nil
}

func indexAdmissionCanonicalizeArtifact(artifact IndexAdmissionArtifact) (IndexAdmissionArtifact, error) {
	if !canonicalContextUUID(artifact.ArtifactID) || !isIndexDigest(artifact.ContentDigest) {
		return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: invalid artifact identity")
	}
	if err := indexAdmissionValidateArtifactProfile(artifact.Profile); err != nil {
		return IndexAdmissionArtifact{}, err
	}
	if artifact.Status != IndexAdmissionArtifactComplete && artifact.Status != IndexAdmissionArtifactPartial {
		return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: unsupported artifact status")
	}
	if artifact.Body == nil || len(artifact.Body) > IndexAdmissionMaxArtifactBodyBytes {
		return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: invalid artifact body")
	}
	if artifact.ContentDigest != indexAdmissionDigestBytes(artifact.Body) {
		return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: artifact body digest mismatch")
	}
	if len(artifact.Definitions) > indexAdmissionMaxDefinitionsPerArtifact ||
		len(artifact.References) > indexAdmissionMaxReferencesPerArtifact ||
		len(artifact.Chunks) > indexAdmissionMaxChunksPerArtifact ||
		len(artifact.Diagnostics) > indexAdmissionMaxDiagnosticsPerArtifact {
		return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: artifact fact collection exceeds limit")
	}
	if artifact.Definitions == nil {
		artifact.Definitions = []IndexAdmissionDefinition{}
	}
	if artifact.References == nil {
		artifact.References = []IndexAdmissionReference{}
	}
	if artifact.Chunks == nil {
		artifact.Chunks = []IndexAdmissionChunk{}
	}
	if artifact.Diagnostics == nil {
		artifact.Diagnostics = []IndexAdmissionDiagnostic{}
	}

	lineStarts := indexAdmissionLineStarts(artifact.Body)
	sort.Slice(artifact.Definitions, func(left, right int) bool {
		return artifact.Definitions[left].LocalSymbolKey < artifact.Definitions[right].LocalSymbolKey
	})
	definitions := make(map[string]struct{}, len(artifact.Definitions))
	for index, definition := range artifact.Definitions {
		if !indexAdmissionValidKey(definition.LocalSymbolKey) || !indexAdmissionValidMetadataText(definition.Kind, indexAdmissionMaxKeyBytes) || !indexAdmissionValidKey(definition.SymbolKey) {
			return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: invalid definition")
		}
		if index > 0 && artifact.Definitions[index-1].LocalSymbolKey == definition.LocalSymbolKey {
			return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: duplicate definition key")
		}
		if err := indexAdmissionValidateSourceSpan(artifact.Body, lineStarts, definition.Span); err != nil {
			return IndexAdmissionArtifact{}, err
		}
		definitions[definition.LocalSymbolKey] = struct{}{}
	}

	sort.Slice(artifact.References, func(left, right int) bool {
		return artifact.References[left].SiteKey < artifact.References[right].SiteKey
	})
	references := make(map[string]IndexAdmissionReference, len(artifact.References))
	for index, reference := range artifact.References {
		if !indexAdmissionValidKey(reference.SiteKey) || !indexAdmissionValidMetadataText(reference.Kind, indexAdmissionMaxKeyBytes) ||
			!indexAdmissionValidKey(reference.SymbolKey) || !indexAdmissionValidSourceText(reference.RawTarget, indexAdmissionMaxTextBytes) ||
			!isIndexRelation(reference.Relation) {
			return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: invalid reference")
		}
		if index > 0 && artifact.References[index-1].SiteKey == reference.SiteKey {
			return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: duplicate reference site key")
		}
		if err := indexAdmissionValidateSourceSpan(artifact.Body, lineStarts, reference.Span); err != nil {
			return IndexAdmissionArtifact{}, err
		}
		text, err := indexAdmissionTextForValidatedSpan(artifact.Body, reference.Span)
		if err != nil || reference.RawTarget != text {
			return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: reference target does not match source body")
		}
		references[reference.SiteKey] = reference
	}

	sort.Slice(artifact.Chunks, func(left, right int) bool {
		return artifact.Chunks[left].Ordinal < artifact.Chunks[right].Ordinal
	})
	for index, chunk := range artifact.Chunks {
		if chunk.Ordinal < 0 || !indexAdmissionValidMetadataText(chunk.Kind, indexAdmissionMaxKeyBytes) || !isIndexDigest(chunk.ContentDigest) ||
			!indexAdmissionValidSourceText(chunk.Text, indexAdmissionMaxTextBytes) {
			return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: invalid chunk")
		}
		if index > 0 && artifact.Chunks[index-1].Ordinal == chunk.Ordinal {
			return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: duplicate chunk ordinal")
		}
		if chunk.SymbolKey != nil {
			if !indexAdmissionValidKey(*chunk.SymbolKey) {
				return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: invalid chunk symbol key")
			}
			if _, found := definitions[*chunk.SymbolKey]; !found {
				return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: chunk symbol is not defined by artifact")
			}
		}
		if err := indexAdmissionValidateSourceSpan(artifact.Body, lineStarts, chunk.Span); err != nil {
			return IndexAdmissionArtifact{}, err
		}
		text, err := indexAdmissionTextForValidatedSpan(artifact.Body, chunk.Span)
		if err != nil || chunk.Text != text || chunk.ContentDigest != indexAdmissionDigestBytes([]byte(text)) {
			return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: chunk does not match source body")
		}
	}

	sort.Slice(artifact.Diagnostics, func(left, right int) bool {
		if compare := indexAdmissionCompareSpans(artifact.Diagnostics[left].Span, artifact.Diagnostics[right].Span); compare != 0 {
			return compare < 0
		}
		if artifact.Diagnostics[left].Code != artifact.Diagnostics[right].Code {
			return artifact.Diagnostics[left].Code < artifact.Diagnostics[right].Code
		}
		return artifact.Diagnostics[left].Message < artifact.Diagnostics[right].Message
	})
	for _, diagnostic := range artifact.Diagnostics {
		if !indexAdmissionValidMetadataText(diagnostic.Code, indexAdmissionMaxKeyBytes) || !indexAdmissionValidMetadataText(diagnostic.Message, indexAdmissionMaxDiagnosticBytes) {
			return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: invalid diagnostic")
		}
		if !indexAdmissionZeroSpan(diagnostic.Span) {
			if err := indexAdmissionValidateSourceSpan(artifact.Body, lineStarts, diagnostic.Span); err != nil {
				return IndexAdmissionArtifact{}, err
			}
		}
	}
	if artifact.Status == IndexAdmissionArtifactComplete && len(artifact.Diagnostics) != 0 {
		return IndexAdmissionArtifact{}, fmt.Errorf("uci index admission: complete artifact has diagnostics")
	}
	return artifact, nil
}

func indexAdmissionValidateArtifactProfile(profile IndexAdmissionArtifactProfile) error {
	if profile.Language != IndexAdmissionLanguageGo ||
		!indexAdmissionValidMetadataText(profile.ParserRevision, indexAdmissionMaxProfileBytes) ||
		!isIndexDigest(profile.GrammarDigest) || !isIndexDigest(profile.ExtractionProfileDigest) {
		return fmt.Errorf("uci index admission: invalid artifact profile")
	}
	return nil
}

func indexAdmissionValidateMembership(membership IndexAdmissionMembership) error {
	if !indexAdmissionValidPath(membership.PathKey) || !indexAdmissionValidPath(membership.DisplayPath) || !indexAdmissionValidMetadataText(membership.Mode, indexAdmissionMaxKeyBytes) {
		return fmt.Errorf("uci index admission: invalid membership")
	}
	switch membership.State {
	case IndexAdmissionMembershipPresent:
		if membership.ArtifactID == nil || !canonicalContextUUID(*membership.ArtifactID) {
			return fmt.Errorf("uci index admission: present membership requires artifact")
		}
	case IndexAdmissionMembershipExcluded, IndexAdmissionMembershipUnreadable, IndexAdmissionMembershipUnsupported, IndexAdmissionMembershipProtected:
		if membership.ArtifactID != nil {
			return fmt.Errorf("uci index admission: non-present membership must not carry artifact")
		}
	default:
		return fmt.Errorf("uci index admission: unsupported membership state")
	}
	return nil
}

func indexAdmissionValidateEdgeShape(edge IndexAdmissionEdge) error {
	if !indexAdmissionValidKey(edge.EdgeKey) || !canonicalContextUUID(edge.SourceArtifactID) ||
		!indexAdmissionValidMetadataText(edge.ResolverRevision, indexAdmissionMaxProfileBytes) || !isIndexRelation(edge.Relation) ||
		!isIndexEvidenceKind(edge.EvidenceKind) || !isIndexResolutionState(edge.ResolutionState) {
		return fmt.Errorf("uci index admission: invalid edge")
	}
	if edge.SourceSymbolKey != nil && !indexAdmissionValidKey(*edge.SourceSymbolKey) {
		return fmt.Errorf("uci index admission: invalid edge source symbol")
	}
	if !indexAdmissionValidKey(edge.Evidence.ReferenceSiteKey) || !indexAdmissionValidKey(edge.Evidence.RuleKey) ||
		!indexAdmissionValidMetadataText(edge.Evidence.Explanation, indexAdmissionMaxTextBytes) || validateIndexSpan(edge.Evidence.Span) != nil {
		return fmt.Errorf("uci index admission: invalid edge evidence")
	}
	switch edge.ResolutionState {
	case IndexResolutionState("resolved"):
		if edge.Target == nil {
			return fmt.Errorf("uci index admission: resolved edge requires target")
		}
		if !indexAdmissionValidPath(edge.Target.PathKey) || !canonicalContextUUID(edge.Target.ArtifactID) {
			return fmt.Errorf("uci index admission: invalid edge target")
		}
		if edge.Target.SymbolKey != nil && !indexAdmissionValidKey(*edge.Target.SymbolKey) {
			return fmt.Errorf("uci index admission: invalid edge target symbol")
		}
	default:
		if edge.Target != nil {
			return fmt.Errorf("uci index admission: non-resolved edge must not carry target")
		}
	}
	return nil
}

func indexAdmissionCanonicalizeFrames(frames []IndexAdmissionFrame) ([]IndexAdmissionFrame, error) {
	if len(frames) > IndexAdmissionMaxFrames {
		return nil, fmt.Errorf("uci index admission: frame count exceeds %d", IndexAdmissionMaxFrames)
	}
	canonical := make([]IndexAdmissionFrame, len(frames))
	var profileID string
	total := 0
	for index, frame := range frames {
		value, err := frame.Canonicalize()
		if err != nil {
			return nil, err
		}
		frameBytes, err := indexAdmissionCanonicalFrameEncodedSize(value)
		if err != nil {
			return nil, err
		}
		if frameBytes > IndexAdmissionMaxTotalEncodedBytes-total {
			return nil, fmt.Errorf("uci index admission: total payload exceeds %d-byte cap", IndexAdmissionMaxTotalEncodedBytes)
		}
		total += frameBytes
		if index == 0 {
			profileID = value.Profile.ID
		} else if value.Profile.ID != profileID {
			return nil, fmt.Errorf("uci index admission: frames use different profiles")
		}
		canonical[index] = value
	}
	return canonical, nil
}

func indexAdmissionCanonicalFrameEncodedSize(frame IndexAdmissionFrame) (int, error) {
	encoded, err := json.Marshal(frame)
	if err != nil {
		return 0, fmt.Errorf("uci index admission: encode canonical frame: %w", err)
	}
	if len(encoded) > IndexAdmissionMaxEncodedFrameBytes {
		return 0, fmt.Errorf("uci index admission: frame exceeds %d-byte cap", IndexAdmissionMaxEncodedFrameBytes)
	}
	return len(encoded), nil
}

func indexAdmissionValidateFrameSet(frames []IndexAdmissionFrame) error {
	artifacts := make(map[string]IndexAdmissionArtifact)
	memberships := make(map[string]IndexAdmissionMembership)
	deletions := make(map[string]struct{})
	replacements := make(map[string]IndexAdmissionEdgeReplacement)
	for _, frame := range frames {
		for _, artifact := range frame.Artifacts {
			if _, duplicate := artifacts[artifact.ArtifactID]; duplicate {
				return fmt.Errorf("uci index admission: duplicate artifact ID across frames")
			}
			artifacts[artifact.ArtifactID] = artifact
		}
		for _, membership := range frame.Memberships {
			if _, duplicate := memberships[membership.PathKey]; duplicate {
				return fmt.Errorf("uci index admission: duplicate membership path across frames")
			}
			memberships[membership.PathKey] = membership
		}
		for _, deletion := range frame.Deletions {
			if _, duplicate := deletions[deletion.PathKey]; duplicate {
				return fmt.Errorf("uci index admission: duplicate deletion path across frames")
			}
			deletions[deletion.PathKey] = struct{}{}
		}
		for _, replacement := range frame.EdgeReplacements {
			if _, duplicate := replacements[replacement.SourcePath]; duplicate {
				return fmt.Errorf("uci index admission: duplicate edge replacement path across frames")
			}
			replacements[replacement.SourcePath] = replacement
		}
	}
	for pathKey, membership := range memberships {
		if _, deleted := deletions[pathKey]; deleted {
			return fmt.Errorf("uci index admission: deletion conflicts with membership across frames")
		}
		if membership.State == IndexAdmissionMembershipPresent {
			if membership.ArtifactID == nil {
				return fmt.Errorf("uci index admission: present membership requires artifact")
			}
			if _, found := artifacts[*membership.ArtifactID]; !found {
				return fmt.Errorf("uci index admission: membership artifact is absent from build")
			}
		}
	}
	for sourcePath, replacement := range replacements {
		membership, present := memberships[sourcePath]
		_, deleted := deletions[sourcePath]
		if !present && !deleted {
			return fmt.Errorf("uci index admission: edge replacement has no current path in build")
		}
		for _, edge := range replacement.Edges {
			if !present || membership.State != IndexAdmissionMembershipPresent || membership.ArtifactID == nil {
				return fmt.Errorf("uci index admission: edges require present source membership")
			}
			if err := indexAdmissionValidateEdge(edge, *membership.ArtifactID, artifacts, memberships); err != nil {
				return err
			}
		}
	}
	return nil
}

func indexAdmissionValidateEdge(edge IndexAdmissionEdge, sourceArtifactID string, artifacts map[string]IndexAdmissionArtifact, memberships map[string]IndexAdmissionMembership) error {
	if err := indexAdmissionValidateEdgeShape(edge); err != nil {
		return err
	}
	if edge.SourceArtifactID != sourceArtifactID {
		return fmt.Errorf("uci index admission: edge source artifact does not match current membership")
	}
	sourceArtifact, found := artifacts[edge.SourceArtifactID]
	if !found {
		return fmt.Errorf("uci index admission: edge source artifact is absent from build")
	}
	definitions := indexAdmissionDefinitionSet(sourceArtifact.Definitions)
	if edge.SourceSymbolKey != nil {
		if _, found := definitions[*edge.SourceSymbolKey]; !found {
			return fmt.Errorf("uci index admission: edge source symbol not defined by artifact")
		}
	}
	reference, found := indexAdmissionReferenceByKey(sourceArtifact.References, edge.Evidence.ReferenceSiteKey)
	if !found || edge.Evidence.Span != reference.Span {
		return fmt.Errorf("uci index admission: edge evidence is not bound to source reference")
	}
	if edge.ResolutionState != IndexResolutionState("resolved") {
		return nil
	}
	targetMembership, found := memberships[edge.Target.PathKey]
	if !found || targetMembership.State != IndexAdmissionMembershipPresent || targetMembership.ArtifactID == nil || *targetMembership.ArtifactID != edge.Target.ArtifactID {
		return fmt.Errorf("uci index admission: edge target does not match current membership")
	}
	targetArtifact, found := artifacts[edge.Target.ArtifactID]
	if !found {
		return fmt.Errorf("uci index admission: edge target artifact is absent from build")
	}
	if edge.Target.SymbolKey != nil {
		if _, found := indexAdmissionDefinitionSet(targetArtifact.Definitions)[*edge.Target.SymbolKey]; !found {
			return fmt.Errorf("uci index admission: edge target symbol not defined by artifact")
		}
	}
	return nil
}

func indexAdmissionIndexFileState(state IndexAdmissionMembershipState) (IndexFileState, error) {
	switch state {
	case IndexAdmissionMembershipPresent:
		return IndexFilePresent, nil
	case IndexAdmissionMembershipUnreadable:
		return IndexFileUnreadable, nil
	case IndexAdmissionMembershipExcluded, IndexAdmissionMembershipUnsupported, IndexAdmissionMembershipProtected:
		return IndexFileExcluded, nil
	default:
		return "", fmt.Errorf("uci index admission: unsupported membership state")
	}
}

func indexAdmissionArtifactFactsDigest(artifact IndexAdmissionArtifact) (IndexDigest, error) {
	encoded, err := json.Marshal(struct {
		Version     string                        `json:"version"`
		Content     IndexDigest                   `json:"content_digest"`
		Profile     IndexAdmissionArtifactProfile `json:"profile"`
		Status      IndexAdmissionArtifactStatus  `json:"status"`
		Definitions []IndexAdmissionDefinition    `json:"definitions"`
		References  []IndexAdmissionReference     `json:"references"`
		Chunks      []IndexAdmissionChunk         `json:"chunks"`
		Diagnostics []IndexAdmissionDiagnostic    `json:"diagnostics"`
	}{
		Version:     IndexAdmissionFrameVersion,
		Content:     artifact.ContentDigest,
		Profile:     artifact.Profile,
		Status:      artifact.Status,
		Definitions: artifact.Definitions,
		References:  artifact.References,
		Chunks:      artifact.Chunks,
		Diagnostics: artifact.Diagnostics,
	})
	if err != nil {
		return "", fmt.Errorf("uci index admission: encode facts: %w", err)
	}
	return indexAdmissionDigestBytes(encoded), nil
}

func indexAdmissionRejectDuplicateJSONKeys(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := indexAdmissionConsumeJSONValue(decoder); err != nil {
		return fmt.Errorf("uci index admission: invalid JSON: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("uci index admission: frame must contain exactly one JSON value")
		}
		return fmt.Errorf("uci index admission: invalid trailing JSON: %w", err)
	}
	return nil
}

func indexAdmissionConsumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if _, duplicate := keys[key]; duplicate {
				return fmt.Errorf("duplicate JSON object key")
			}
			keys[key] = struct{}{}
			if err := indexAdmissionConsumeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("unterminated JSON object")
		}
		return nil
	case '[':
		for decoder.More() {
			if err := indexAdmissionConsumeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("unterminated JSON array")
		}
		return nil
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
}

func indexAdmissionCloneArtifacts(artifacts []IndexAdmissionArtifact) []IndexAdmissionArtifact {
	if artifacts == nil {
		return nil
	}
	copy := make([]IndexAdmissionArtifact, len(artifacts))
	for index, artifact := range artifacts {
		copy[index] = artifact
		copy[index].Body = indexAdmissionCloneBytes(artifact.Body)
		copy[index].Definitions = append([]IndexAdmissionDefinition(nil), artifact.Definitions...)
		if artifact.Definitions != nil && copy[index].Definitions == nil {
			copy[index].Definitions = []IndexAdmissionDefinition{}
		}
		copy[index].References = append([]IndexAdmissionReference(nil), artifact.References...)
		if artifact.References != nil && copy[index].References == nil {
			copy[index].References = []IndexAdmissionReference{}
		}
		copy[index].Chunks = append([]IndexAdmissionChunk(nil), artifact.Chunks...)
		if artifact.Chunks != nil && copy[index].Chunks == nil {
			copy[index].Chunks = []IndexAdmissionChunk{}
		}
		for chunkIndex := range copy[index].Chunks {
			copy[index].Chunks[chunkIndex].SymbolKey = indexAdmissionCopyStringPointer(artifact.Chunks[chunkIndex].SymbolKey)
		}
		copy[index].Diagnostics = append([]IndexAdmissionDiagnostic(nil), artifact.Diagnostics...)
		if artifact.Diagnostics != nil && copy[index].Diagnostics == nil {
			copy[index].Diagnostics = []IndexAdmissionDiagnostic{}
		}
	}
	return copy
}

func indexAdmissionCloneMemberships(memberships []IndexAdmissionMembership) []IndexAdmissionMembership {
	if memberships == nil {
		return nil
	}
	copy := make([]IndexAdmissionMembership, len(memberships))
	for index, membership := range memberships {
		copy[index] = membership
		copy[index].ArtifactID = indexAdmissionCopyStringPointer(membership.ArtifactID)
	}
	return copy
}

func indexAdmissionCloneEdgeReplacements(replacements []IndexAdmissionEdgeReplacement) []IndexAdmissionEdgeReplacement {
	if replacements == nil {
		return nil
	}
	copy := make([]IndexAdmissionEdgeReplacement, len(replacements))
	for replacementIndex, replacement := range replacements {
		copy[replacementIndex] = replacement
		copy[replacementIndex].Edges = make([]IndexAdmissionEdge, len(replacement.Edges))
		for edgeIndex, edge := range replacement.Edges {
			copy[replacementIndex].Edges[edgeIndex] = edge
			copy[replacementIndex].Edges[edgeIndex].SourceSymbolKey = indexAdmissionCopyStringPointer(edge.SourceSymbolKey)
			if edge.Target != nil {
				target := *edge.Target
				target.SymbolKey = indexAdmissionCopyStringPointer(edge.Target.SymbolKey)
				copy[replacementIndex].Edges[edgeIndex].Target = &target
			}
		}
		if replacement.Edges != nil && copy[replacementIndex].Edges == nil {
			copy[replacementIndex].Edges = []IndexAdmissionEdge{}
		}
	}
	return copy
}

func indexAdmissionCloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	cloned := make([]byte, len(value))
	copy(cloned, value)
	return cloned
}

func indexAdmissionCopyStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func indexAdmissionStringPointer(value string) *string {
	copy := value
	return &copy
}

func indexAdmissionDigestBytes(value []byte) IndexDigest {
	sum := sha256.Sum256(value)
	return IndexDigest("sha256:" + hex.EncodeToString(sum[:]))
}

func indexAdmissionWriteHashString(state hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = state.Write(length[:])
	_, _ = state.Write([]byte(value))
}

func indexAdmissionUUID(value [16]byte) string {
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func indexAdmissionValidText(value string, maximum int) bool {
	return maximum > 0 && len(value) > 0 && len(value) <= maximum && utf8.ValidString(value) && strings.TrimSpace(value) == value && validIndexText(value)
}

func indexAdmissionValidSourceText(value string, maximum int) bool {
	return maximum > 0 && len(value) > 0 && len(value) <= maximum && utf8.ValidString(value)
}

func indexAdmissionValidKey(value string) bool {
	return indexAdmissionValidMetadataText(value, indexAdmissionMaxKeyBytes)
}

func indexAdmissionValidMetadataText(value string, maximum int) bool {
	return indexAdmissionValidText(value, maximum) && !indexAdmissionPrivateLocator(value)
}

func indexAdmissionPrivateLocator(value string) bool {
	normalized := strings.ReplaceAll(value, "\\", "/")
	lower := strings.ToLower(normalized)
	if strings.Contains(normalized, "://") || strings.HasPrefix(lower, "file:") || strings.HasPrefix(normalized, "~") ||
		strings.HasPrefix(normalized, "../") || normalized == ".." || path.IsAbs(normalized) {
		return true
	}
	return len(normalized) >= 2 && normalized[1] == ':' && ((normalized[0] >= 'A' && normalized[0] <= 'Z') || (normalized[0] >= 'a' && normalized[0] <= 'z'))
}

func indexAdmissionValidPath(value string) bool {
	if !indexAdmissionValidText(value, indexAdmissionMaxPathBytes) || indexAdmissionPrivateLocator(value) || value == "." {
		return false
	}
	return path.Clean(value) == value
}

func indexAdmissionLineStarts(source []byte) []int {
	starts := []int{0}
	for offset, value := range source {
		if value == '\n' {
			starts = append(starts, offset+1)
		}
	}
	return starts
}

func indexAdmissionValidateSourceSpan(source []byte, lineStarts []int, span IndexSpan) error {
	if span.ByteStart < 0 || span.ByteEnd <= span.ByteStart || span.ByteEnd > int64(len(source)) || span.LineStart < 1 || span.LineEnd < span.LineStart {
		return fmt.Errorf("uci index admission: invalid source span")
	}
	start := int(span.ByteStart)
	end := int(span.ByteEnd)
	lineStart := indexAdmissionLineForOffset(lineStarts, start)
	lineEnd := indexAdmissionLineForOffset(lineStarts, end-1)
	if span.LineStart != lineStart || span.LineEnd != lineEnd {
		return fmt.Errorf("uci index admission: source span line mismatch")
	}
	return nil
}

func indexAdmissionTextAtSpan(source []byte, span IndexSpan) (string, error) {
	if err := indexAdmissionValidateSourceSpan(source, indexAdmissionLineStarts(source), span); err != nil {
		return "", err
	}
	return indexAdmissionTextForValidatedSpan(source, span)
}

func indexAdmissionTextForValidatedSpan(source []byte, span IndexSpan) (string, error) {
	if span.ByteStart < 0 || span.ByteEnd <= span.ByteStart || span.ByteEnd > int64(len(source)) {
		return "", fmt.Errorf("uci index admission: invalid source span")
	}
	return string(source[int(span.ByteStart):int(span.ByteEnd)]), nil
}

func indexAdmissionLineForOffset(lineStarts []int, offset int) int {
	line := sort.Search(len(lineStarts), func(index int) bool {
		return lineStarts[index] > offset
	}) - 1
	if line < 0 {
		line = 0
	}
	return line + 1
}

func indexAdmissionZeroSpan(span IndexSpan) bool {
	return span.ByteStart == 0 && span.ByteEnd == 0 && span.LineStart == 0 && span.LineEnd == 0
}

func indexAdmissionCompareSpans(left, right IndexSpan) int {
	if left.ByteStart != right.ByteStart {
		if left.ByteStart < right.ByteStart {
			return -1
		}
		return 1
	}
	if left.ByteEnd != right.ByteEnd {
		if left.ByteEnd < right.ByteEnd {
			return -1
		}
		return 1
	}
	if left.LineStart != right.LineStart {
		if left.LineStart < right.LineStart {
			return -1
		}
		return 1
	}
	if left.LineEnd != right.LineEnd {
		if left.LineEnd < right.LineEnd {
			return -1
		}
		return 1
	}
	return 0
}

func indexAdmissionDefinitionSet(definitions []IndexAdmissionDefinition) map[string]struct{} {
	result := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		result[definition.LocalSymbolKey] = struct{}{}
	}
	return result
}

func indexAdmissionReferenceBindings(frame IndexAdmissionFrame) (IndexAdmissionReferenceBindings, error) {
	count := 0
	for _, artifact := range frame.Artifacts {
		count += len(artifact.References)
	}
	bindings := make(IndexAdmissionReferenceBindings, count)
	for _, artifact := range frame.Artifacts {
		for _, reference := range artifact.References {
			key := IndexAdmissionReferenceKey{ArtifactID: artifact.ArtifactID, SiteKey: reference.SiteKey}
			referenceID, err := DeriveIndexAdmissionReferenceSiteID(key.ArtifactID, key.SiteKey)
			if err != nil {
				return nil, err
			}
			bindings[key] = referenceID
		}
	}
	return bindings, nil
}

func indexAdmissionReferenceByKey(references []IndexAdmissionReference, siteKey string) (IndexAdmissionReference, bool) {
	index := sort.Search(len(references), func(index int) bool {
		return references[index].SiteKey >= siteKey
	})
	if index == len(references) || references[index].SiteKey != siteKey {
		return IndexAdmissionReference{}, false
	}
	return references[index], true
}

func indexAdmissionGoReferenceRelation(kind string) (IndexRelation, error) {
	switch kind {
	case "import":
		return IndexRelation("imports"), nil
	case "call":
		return IndexRelation("calls"), nil
	case "reference":
		return IndexRelation("references"), nil
	default:
		return "", fmt.Errorf("uci index admission: unsupported Go reference kind")
	}
}

func indexAdmissionGoReferenceSiteKey(reference GoReferenceSite) string {
	return reference.LocalKey + "@" + fmt.Sprintf("%d:%d", reference.Span.ByteStart, reference.Span.ByteEnd)
}
