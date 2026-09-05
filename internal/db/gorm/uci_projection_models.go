package gorm

import (
	"time"

	"github.com/pgvector/pgvector-go"
)

// UCIBlobStorageState describes whether an admitted source blob carries bytes or metadata only.
type UCIBlobStorageState string

const (
	UCIBlobStored       UCIBlobStorageState = "stored"
	UCIBlobMetadataOnly UCIBlobStorageState = "metadata_only"
	UCIBlobExcluded     UCIBlobStorageState = "excluded"
)

// UCIParseArtifactStatus is the closed parser/extractor outcome vocabulary.
type UCIParseArtifactStatus string

const (
	UCIParseArtifactComplete    UCIParseArtifactStatus = "complete"
	UCIParseArtifactPartial     UCIParseArtifactStatus = "partial"
	UCIParseArtifactUnsupported UCIParseArtifactStatus = "unsupported"
	UCIParseArtifactExcluded    UCIParseArtifactStatus = "excluded"
)

// UCIFileState describes one checkout-scoped path membership.
type UCIFileState string

const (
	UCIFilePresent    UCIFileState = "present"
	UCIFileExcluded   UCIFileState = "excluded"
	UCIFileUnreadable UCIFileState = "unreadable"
)

// UCIResolvedEdgeEvidenceKind identifies the evidence that produced a graph edge.
type UCIResolvedEdgeEvidenceKind string

const (
	UCIResolvedEdgeEvidenceExtracted  UCIResolvedEdgeEvidenceKind = "extracted"
	UCIResolvedEdgeEvidenceResolved   UCIResolvedEdgeEvidenceKind = "resolved"
	UCIResolvedEdgeEvidenceHeuristic  UCIResolvedEdgeEvidenceKind = "heuristic"
	UCIResolvedEdgeEvidenceSemantic   UCIResolvedEdgeEvidenceKind = "semantic"
	UCIResolvedEdgeEvidenceUnresolved UCIResolvedEdgeEvidenceKind = "unresolved"
)

// UCIResolvedEdgeState is the closed resolution outcome vocabulary.
type UCIResolvedEdgeState string

const (
	UCIResolvedEdgeResolved   UCIResolvedEdgeState = "resolved"
	UCIResolvedEdgeUnresolved UCIResolvedEdgeState = "unresolved"
	UCIResolvedEdgeAmbiguous  UCIResolvedEdgeState = "ambiguous"
	UCIResolvedEdgePartial    UCIResolvedEdgeState = "partial"
)

// UCIEmbeddingStatus describes a reusable embedding's durable readiness.
type UCIEmbeddingStatus string

const (
	UCIEmbeddingPending UCIEmbeddingStatus = "pending"
	UCIEmbeddingReady   UCIEmbeddingStatus = "ready"
	UCIEmbeddingFailed  UCIEmbeddingStatus = "failed"
)

// UCIJobState is the persisted lifecycle state for later fenced publication work.
type UCIJobState string

const (
	UCIJobQueued         UCIJobState = "queued"
	UCIJobRunning        UCIJobState = "running"
	UCIJobRetryScheduled UCIJobState = "retry_scheduled"
	UCIJobSucceeded      UCIJobState = "succeeded"
	UCIJobFailedTerminal UCIJobState = "failed_terminal"
	UCIJobCancelled      UCIJobState = "cancelled"
	UCIJobObsolete       UCIJobState = "obsolete"
)

// UCIAnalysisState is the lifecycle state for a rebuildable view-pinned analysis.
type UCIAnalysisState string

const (
	UCIAnalysisPending UCIAnalysisState = "pending"
	UCIAnalysisReady   UCIAnalysisState = "ready"
	UCIAnalysisFailed  UCIAnalysisState = "failed"
)

// UCIBlob is an immutable source-byte unit, scoped to one Source and protection domain.
type UCIBlob struct {
	BlobID           string              `gorm:"column:blob_id;type:uuid;primaryKey"`
	SourceID         string              `gorm:"column:source_id;type:uuid;not null"`
	ProtectionDomain string              `gorm:"column:protection_domain;type:text;not null"`
	ContentDigest    string              `gorm:"column:content_digest;type:text;not null"`
	ByteLength       int64               `gorm:"column:byte_length;not null"`
	SafeContent      []byte              `gorm:"column:safe_content;type:bytea"`
	Encoding         string              `gorm:"column:encoding;type:text;not null"`
	StorageState     UCIBlobStorageState `gorm:"column:storage_state;type:text;not null"`
	CreatedAt        time.Time           `gorm:"column:created_at;type:timestamptz;not null"`
}

func (UCIBlob) TableName() string { return "ci_blobs" }

// UCIParseArtifact is an immutable parser or structured-text extraction product.
type UCIParseArtifact struct {
	ArtifactID              string                 `gorm:"column:artifact_id;type:uuid;primaryKey"`
	SourceID                string                 `gorm:"column:source_id;type:uuid;not null"`
	BlobID                  string                 `gorm:"column:blob_id;type:uuid;not null"`
	Language                string                 `gorm:"column:language;type:text;not null"`
	ParserRevision          string                 `gorm:"column:parser_revision;type:text;not null"`
	GrammarDigest           string                 `gorm:"column:grammar_digest;type:text;not null"`
	ExtractionProfileDigest string                 `gorm:"column:extraction_profile_digest;type:text;not null"`
	Status                  UCIParseArtifactStatus `gorm:"column:status;type:text;not null"`
	Diagnostics             string                 `gorm:"column:diagnostics;type:jsonb;not null"`
	FactsDigest             *string                `gorm:"column:facts_digest;type:text"`
	SealedAt                *time.Time             `gorm:"column:sealed_at;type:timestamptz"`
	CreatedAt               time.Time              `gorm:"column:created_at;type:timestamptz;not null"`
}

func (UCIParseArtifact) TableName() string { return "ci_parse_artifacts" }

// UCIDefinition is one source-grounded declaration within an artifact.
type UCIDefinition struct {
	DefinitionID       string    `gorm:"column:definition_id;type:uuid;primaryKey"`
	ArtifactID         string    `gorm:"column:artifact_id;type:uuid;not null"`
	LocalSymbolKey     string    `gorm:"column:local_symbol_key;type:text;not null"`
	Kind               string    `gorm:"column:kind;type:text;not null"`
	Name               string    `gorm:"column:name;type:text;not null"`
	QualifiedLocalName string    `gorm:"column:qualified_local_name;type:text;not null"`
	Signature          string    `gorm:"column:signature;type:text;not null"`
	ByteStart          int64     `gorm:"column:byte_start;not null"`
	ByteEnd            int64     `gorm:"column:byte_end;not null"`
	LineStart          int       `gorm:"column:line_start;not null"`
	LineEnd            int       `gorm:"column:line_end;not null"`
	CreatedAt          time.Time `gorm:"column:created_at;type:timestamptz;not null"`
}

func (UCIDefinition) TableName() string { return "ci_definitions" }

// UCIReferenceSite is an unresolved or resolved-in-later-stage observed reference fact.
type UCIReferenceSite struct {
	ReferenceSiteID string    `gorm:"column:reference_site_id;type:uuid;primaryKey"`
	ArtifactID      string    `gorm:"column:artifact_id;type:uuid;not null"`
	SiteKey         string    `gorm:"column:site_key;type:text;not null"`
	OwnerSymbolKey  *string   `gorm:"column:owner_symbol_key;type:text"`
	RawTarget       string    `gorm:"column:raw_target;type:text;not null"`
	Relation        string    `gorm:"column:relation;type:text;not null"`
	SyntaxSpan      string    `gorm:"column:syntax_span;type:jsonb;not null"`
	ResolverHints   string    `gorm:"column:resolver_hints;type:jsonb;not null"`
	CreatedAt       time.Time `gorm:"column:created_at;type:timestamptz;not null"`
}

func (UCIReferenceSite) TableName() string { return "ci_reference_sites" }

// UCIChunk is one searchable source-grounded excerpt of an immutable artifact.
type UCIChunk struct {
	ChunkID       string    `gorm:"column:chunk_id;type:uuid;primaryKey"`
	SourceID      string    `gorm:"column:source_id;type:uuid;not null"`
	ArtifactID    string    `gorm:"column:artifact_id;type:uuid;not null"`
	SymbolKey     *string   `gorm:"column:symbol_key;type:text"`
	ChunkKind     string    `gorm:"column:chunk_kind;type:text;not null"`
	Ordinal       int       `gorm:"column:ordinal;not null"`
	ByteStart     int64     `gorm:"column:byte_start;not null"`
	ByteEnd       int64     `gorm:"column:byte_end;not null"`
	ContentDigest string    `gorm:"column:content_digest;type:text;not null"`
	TextForSearch string    `gorm:"column:text_for_search;type:text;not null"`
	ContentTSV    string    `gorm:"column:content_tsv;->"`
	CreatedAt     time.Time `gorm:"column:created_at;type:timestamptz;not null"`
}

func (UCIChunk) TableName() string { return "ci_chunks" }

// UCIMembership maps one checkout's path to an artifact for a half-open generation interval.
type UCIMembership struct {
	MembershipID        string       `gorm:"column:membership_id;type:uuid;primaryKey"`
	SourceID            string       `gorm:"column:source_id;type:uuid;not null"`
	CheckoutID          string       `gorm:"column:checkout_id;type:uuid;not null"`
	PathKey             string       `gorm:"column:path_key;type:text;not null"`
	DisplayPath         string       `gorm:"column:display_path;type:text;not null"`
	ArtifactID          *string      `gorm:"column:artifact_id;type:uuid"`
	FileState           UCIFileState `gorm:"column:file_state;type:text;not null"`
	Mode                string       `gorm:"column:mode;type:text;not null"`
	ValidFromGeneration int64        `gorm:"column:valid_from_generation;not null"`
	ValidToGeneration   *int64       `gorm:"column:valid_to_generation"`
	CreatedAt           time.Time    `gorm:"column:created_at;type:timestamptz;not null"`
}

func (UCIMembership) TableName() string { return "ci_memberships" }

// UCIResolvedEdge is a checkout-scoped temporal graph edge.
type UCIResolvedEdge struct {
	ResolvedEdgeID      string                      `gorm:"column:resolved_edge_id;type:uuid;primaryKey"`
	SourceID            string                      `gorm:"column:source_id;type:uuid;not null"`
	CheckoutID          string                      `gorm:"column:checkout_id;type:uuid;not null"`
	EdgeKey             string                      `gorm:"column:edge_key;type:text;not null"`
	SourcePath          string                      `gorm:"column:source_path;type:text;not null"`
	SourceArtifact      string                      `gorm:"column:source_artifact;type:uuid;not null"`
	SourceSymbol        *string                     `gorm:"column:source_symbol;type:text"`
	TargetPath          *string                     `gorm:"column:target_path;type:text"`
	TargetArtifact      *string                     `gorm:"column:target_artifact;type:uuid"`
	TargetSymbol        *string                     `gorm:"column:target_symbol;type:text"`
	Relation            string                      `gorm:"column:relation;type:text;not null"`
	EvidenceKind        UCIResolvedEdgeEvidenceKind `gorm:"column:evidence_kind;type:text;not null"`
	ResolverRevision    string                      `gorm:"column:resolver_revision;type:text;not null"`
	EvidenceJSON        string                      `gorm:"column:evidence_json;type:jsonb;not null"`
	ResolutionState     UCIResolvedEdgeState        `gorm:"column:resolution_state;type:text;not null"`
	ValidFromGeneration int64                       `gorm:"column:valid_from_generation;not null"`
	ValidToGeneration   *int64                      `gorm:"column:valid_to_generation"`
	CreatedAt           time.Time                   `gorm:"column:created_at;type:timestamptz;not null"`
}

func (UCIResolvedEdge) TableName() string { return "ci_resolved_edges" }

// UCIEmbeddingProfile names one compatible semantic space for a UCI analysis profile.
type UCIEmbeddingProfile struct {
	EmbeddingProfileID    string    `gorm:"column:embedding_profile_id;type:uuid;primaryKey"`
	AnalysisProfileID     string    `gorm:"column:analysis_profile_id;type:uuid;not null"`
	ProviderRef           string    `gorm:"column:provider_ref;type:text;not null"`
	Model                 string    `gorm:"column:model;type:text;not null"`
	Dimension             int       `gorm:"column:dimension;not null"`
	PreprocessingRevision string    `gorm:"column:preprocessing_revision;type:text;not null"`
	IncludeRelativePath   bool      `gorm:"column:include_relative_path;not null"`
	CreatedAt             time.Time `gorm:"column:created_at;type:timestamptz;not null"`
}

func (UCIEmbeddingProfile) TableName() string { return "ci_embedding_profiles" }

// UCIEmbedding is a rebuildable source/protection-domain-scoped semantic vector.
type UCIEmbedding struct {
	EmbeddingID          string             `gorm:"column:embedding_id;type:uuid;primaryKey"`
	EmbeddingProfileID   string             `gorm:"column:embedding_profile_id;type:uuid;not null"`
	EmbeddingInputDigest string             `gorm:"column:embedding_input_digest;type:text;not null"`
	Vector               *pgvector.Vector   `gorm:"column:vector;type:vector(1536)"`
	SourceID             string             `gorm:"column:source_id;type:uuid;not null"`
	ProtectionDomain     string             `gorm:"column:protection_domain;type:text;not null"`
	CompletionSeq        int64              `gorm:"column:completion_seq;not null"`
	Status               UCIEmbeddingStatus `gorm:"column:status;type:text;not null"`
	CreatedAt            time.Time          `gorm:"column:created_at;type:timestamptz;not null"`
}

func (UCIEmbedding) TableName() string { return "ci_embeddings" }

// UCIChunkEmbedding links a source chunk to one compatible reusable embedding.
type UCIChunkEmbedding struct {
	ChunkEmbeddingID        string    `gorm:"column:chunk_embedding_id;type:uuid;primaryKey"`
	SourceID                string    `gorm:"column:source_id;type:uuid;not null"`
	ChunkID                 string    `gorm:"column:chunk_id;type:uuid;not null"`
	EmbeddingID             string    `gorm:"column:embedding_id;type:uuid;not null"`
	RelativePathFingerprint string    `gorm:"column:relative_path_fingerprint;type:text;not null"`
	EmbeddingProfileID      string    `gorm:"column:embedding_profile_id;type:uuid;not null"`
	EmbeddingInputDigest    string    `gorm:"column:embedding_input_digest;type:text;not null"`
	CreatedAt               time.Time `gorm:"column:created_at;type:timestamptz;not null"`
}

func (UCIChunkEmbedding) TableName() string { return "ci_chunk_embeddings" }

// UCIJob is persisted workflow state, including a fenced publication build when publication_key is set.
type UCIJob struct {
	JobID                 string      `gorm:"column:job_id;type:uuid;primaryKey"`
	SourceID              string      `gorm:"column:source_id;type:uuid;not null"`
	CheckoutID            *string     `gorm:"column:checkout_id;type:uuid"`
	JobKind               string      `gorm:"column:job_kind;type:text;not null"`
	InputFingerprint      string      `gorm:"column:input_fingerprint;type:text;not null"`
	OwnerEpoch            *int64      `gorm:"column:owner_epoch"`
	TargetGeneration      *int64      `gorm:"column:target_generation"`
	State                 UCIJobState `gorm:"column:state;type:text;not null"`
	Attempt               int         `gorm:"column:attempt;not null"`
	RetryAfter            *time.Time  `gorm:"column:retry_after;type:timestamptz"`
	LeaseOwner            *string     `gorm:"column:lease_owner;type:text"`
	LeaseExpiry           *time.Time  `gorm:"column:lease_expiry;type:timestamptz"`
	ErrorCode             *string     `gorm:"column:error_code;type:text"`
	Counts                string      `gorm:"column:counts;type:jsonb;not null"`
	PublicationKey        *string     `gorm:"column:publication_key;type:text"`
	RequestedBy           *string     `gorm:"column:requested_by;type:text"`
	IncarnationID         *string     `gorm:"column:incarnation_id;type:uuid"`
	ProfileID             *string     `gorm:"column:profile_id;type:uuid"`
	ExpectedParentViewID  *string     `gorm:"column:expected_parent_view_id;type:uuid"`
	ManifestMode          *string     `gorm:"column:manifest_mode;type:text"`
	SealedManifest        *string     `gorm:"column:sealed_manifest;type:jsonb"`
	FinalizeBindingDigest *string     `gorm:"column:finalize_binding_digest;type:text"`
	ResultViewID          *string     `gorm:"column:result_view_id;type:uuid"`
	CreatedAt             time.Time   `gorm:"column:created_at;type:timestamptz;not null"`
	UpdatedAt             time.Time   `gorm:"column:updated_at;type:timestamptz;not null"`
}

func (UCIJob) TableName() string { return "ci_jobs" }

// UCIIndexBuildPart is one immutable, sequenced frame belonging to a publication build.
type UCIIndexBuildPart struct {
	BuildID      string    `gorm:"column:build_id;type:uuid;primaryKey"`
	Sequence     uint32    `gorm:"column:sequence;primaryKey"`
	PartDigest   string    `gorm:"column:part_digest;type:text;not null"`
	Payload      string    `gorm:"column:payload;type:jsonb;not null"`
	PayloadBytes int64     `gorm:"column:payload_bytes;not null"`
	CreatedAt    time.Time `gorm:"column:created_at;type:timestamptz;not null"`
}

func (UCIIndexBuildPart) TableName() string { return "ci_index_build_parts" }

// UCIAnalysis is a rebuildable result pinned to a specific View.
type UCIAnalysis struct {
	AnalysisID        string           `gorm:"column:analysis_id;type:uuid;primaryKey"`
	ViewID            string           `gorm:"column:view_id;type:uuid;not null"`
	Kind              string           `gorm:"column:kind;type:text;not null"`
	AlgorithmRevision string           `gorm:"column:algorithm_revision;type:text;not null"`
	InputDigest       string           `gorm:"column:input_digest;type:text;not null"`
	ArtifactRefs      string           `gorm:"column:artifact_refs;type:jsonb;not null"`
	ResultJSON        string           `gorm:"column:result_json;type:jsonb;not null"`
	State             UCIAnalysisState `gorm:"column:state;type:text;not null"`
	CreatedAt         time.Time        `gorm:"column:created_at;type:timestamptz;not null"`
	UpdatedAt         time.Time        `gorm:"column:updated_at;type:timestamptz;not null"`
}

func (UCIAnalysis) TableName() string { return "ci_analyses" }

// UCIExposureOperationKind identifies the closed UCI boundary operation that produced evidence.
type UCIExposureOperationKind string

const (
	UCIExposureCodeSearch    UCIExposureOperationKind = "code_search"
	UCIExposureCodeGraph     UCIExposureOperationKind = "code_graph"
	UCIExposureVersionedRead UCIExposureOperationKind = "versioned_read"
)

// UCIExposureResultState describes the closed, already-authorized result decision.
type UCIExposureResultState string

const (
	UCIExposureResultOK          UCIExposureResultState = "ok"
	UCIExposureResultEmpty       UCIExposureResultState = "empty"
	UCIExposureResultPartial     UCIExposureResultState = "partial"
	UCIExposureResultStale       UCIExposureResultState = "stale"
	UCIExposureResultUnavailable UCIExposureResultState = "unavailable"
)

// UCIRetrievalMode describes how an authorized result was retrieved.
type UCIRetrievalMode string

const (
	UCIRetrievalExact       UCIRetrievalMode = "exact"
	UCIRetrievalLexical     UCIRetrievalMode = "lexical"
	UCIRetrievalHybrid      UCIRetrievalMode = "hybrid"
	UCIRetrievalGraph       UCIRetrievalMode = "graph"
	UCIRetrievalUnavailable UCIRetrievalMode = "unavailable"
)

// UCICoverageState describes the closed coverage state without creating authority.
type UCICoverageState string

const (
	UCICoverageComplete    UCICoverageState = "complete"
	UCICoveragePartial     UCICoverageState = "partial"
	UCICoverageUnavailable UCICoverageState = "unavailable"
)

// UCIEvidenceSource labels the evidence mechanism behind a closed result.
type UCIEvidenceSource string

const (
	UCIEvidenceExact  UCIEvidenceSource = "exact"
	UCIEvidenceFTS    UCIEvidenceSource = "fts"
	UCIEvidenceVector UCIEvidenceSource = "vector"
	UCIEvidenceGraph  UCIEvidenceSource = "graph"
	UCIEvidenceMixed  UCIEvidenceSource = "mixed"
	UCIEvidenceNone   UCIEvidenceSource = "none"
)

// UCICertainty is the closed confidence label for UCI evidence.
type UCICertainty string

const (
	UCICertaintyEstablished UCICertainty = "established"
	UCICertaintyPartial     UCICertainty = "partial"
	UCICertaintyUnavailable UCICertainty = "unavailable"
)

// UCICompletionOutcome is written only by a verified supported-host callback.
type UCICompletionOutcome string

const (
	UCICompletionSucceeded UCICompletionOutcome = "succeeded"
	UCICompletionPartial   UCICompletionOutcome = "partial"
	UCICompletionFailed    UCICompletionOutcome = "failed"
	UCICompletionAbandoned UCICompletionOutcome = "abandoned"
)

// UCICompletionState is the receipt state; unknown is represented by no completion child row.
type UCICompletionState string

const (
	UCICompletionUnknown UCICompletionState = "unknown"
)

// UCIExposure is durable, append-only, non-content evidence for an authorized closed result.
type UCIExposure struct {
	ExposureID               string                   `gorm:"column:exposure_id;type:uuid;primaryKey"`
	ExposureRef              string                   `gorm:"column:exposure_ref;type:text;not null"`
	AuthRealm                string                   `gorm:"column:auth_realm;type:text;not null"`
	SourceID                 string                   `gorm:"column:source_id;type:uuid;not null"`
	CheckoutID               string                   `gorm:"column:checkout_id;type:uuid;not null"`
	ViewID                   string                   `gorm:"column:view_id;type:uuid;not null"`
	ClientRef                string                   `gorm:"column:client_ref;type:text;not null"`
	ClientSessionRef         string                   `gorm:"column:client_session_ref;type:text;not null"`
	RequestRef               string                   `gorm:"column:request_ref;type:text;not null"`
	OperationKind            UCIExposureOperationKind `gorm:"column:operation_kind;type:text;not null"`
	ResultState              UCIExposureResultState   `gorm:"column:result_state;type:text;not null"`
	RetrievalMode            UCIRetrievalMode         `gorm:"column:retrieval_mode;type:text;not null"`
	CoverageState            UCICoverageState         `gorm:"column:coverage_state;type:text;not null"`
	EvidenceSource           UCIEvidenceSource        `gorm:"column:evidence_source;type:text;not null"`
	Certainty                UCICertainty             `gorm:"column:certainty;type:text;not null"`
	IdempotencyKey           string                   `gorm:"column:idempotency_key;type:text;not null"`
	IdempotencyBindingDigest string                   `gorm:"column:idempotency_binding_digest;type:text;not null"`
	RecordedAt               time.Time                `gorm:"column:recorded_at;type:timestamptz;not null"`
}

func (UCIExposure) TableName() string { return "uci_exposures" }

// UCICompletionEvidence is a durable append-only child of one UCI exposure.
type UCICompletionEvidence struct {
	CompletionEvidenceID     string               `gorm:"column:completion_evidence_id;type:uuid;primaryKey"`
	ExposureID               string               `gorm:"column:exposure_id;type:uuid;not null"`
	SupportedHostRef         string               `gorm:"column:supported_host_ref;type:text;not null"`
	CallbackRef              string               `gorm:"column:callback_ref;type:text;not null"`
	Outcome                  UCICompletionOutcome `gorm:"column:outcome;type:text;not null"`
	IdempotencyKey           string               `gorm:"column:idempotency_key;type:text;not null"`
	IdempotencyBindingDigest string               `gorm:"column:idempotency_binding_digest;type:text;not null"`
	OccurredAt               time.Time            `gorm:"column:occurred_at;type:timestamptz;not null"`
}

func (UCICompletionEvidence) TableName() string { return "uci_completion_evidence" }

func isUCIBlobStorageState(value UCIBlobStorageState) bool {
	switch value {
	case UCIBlobStored, UCIBlobMetadataOnly, UCIBlobExcluded:
		return true
	default:
		return false
	}
}

func isUCIParseArtifactStatus(value UCIParseArtifactStatus) bool {
	switch value {
	case UCIParseArtifactComplete, UCIParseArtifactPartial, UCIParseArtifactUnsupported, UCIParseArtifactExcluded:
		return true
	default:
		return false
	}
}

func isUCIEmbeddingStatus(value UCIEmbeddingStatus) bool {
	switch value {
	case UCIEmbeddingPending, UCIEmbeddingReady, UCIEmbeddingFailed:
		return true
	default:
		return false
	}
}

func isUCIAnalysisState(value UCIAnalysisState) bool {
	switch value {
	case UCIAnalysisPending, UCIAnalysisReady, UCIAnalysisFailed:
		return true
	default:
		return false
	}
}

func isUCIExposureOperationKind(value UCIExposureOperationKind) bool {
	switch value {
	case UCIExposureCodeSearch, UCIExposureCodeGraph, UCIExposureVersionedRead:
		return true
	default:
		return false
	}
}

func isUCIExposureResultState(value UCIExposureResultState) bool {
	switch value {
	case UCIExposureResultOK, UCIExposureResultEmpty, UCIExposureResultPartial, UCIExposureResultStale, UCIExposureResultUnavailable:
		return true
	default:
		return false
	}
}

func isUCIRetrievalMode(value UCIRetrievalMode) bool {
	switch value {
	case UCIRetrievalExact, UCIRetrievalLexical, UCIRetrievalHybrid, UCIRetrievalGraph, UCIRetrievalUnavailable:
		return true
	default:
		return false
	}
}

func isUCICoverageState(value UCICoverageState) bool {
	switch value {
	case UCICoverageComplete, UCICoveragePartial, UCICoverageUnavailable:
		return true
	default:
		return false
	}
}

func isUCIEvidenceSource(value UCIEvidenceSource) bool {
	switch value {
	case UCIEvidenceExact, UCIEvidenceFTS, UCIEvidenceVector, UCIEvidenceGraph, UCIEvidenceMixed, UCIEvidenceNone:
		return true
	default:
		return false
	}
}

func isUCICertainty(value UCICertainty) bool {
	switch value {
	case UCICertaintyEstablished, UCICertaintyPartial, UCICertaintyUnavailable:
		return true
	default:
		return false
	}
}

func isUCICompletionOutcome(value UCICompletionOutcome) bool {
	switch value {
	case UCICompletionSucceeded, UCICompletionPartial, UCICompletionFailed, UCICompletionAbandoned:
		return true
	default:
		return false
	}
}
