package uci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const indexDigestVersion = 1

// IndexDigest is a complete SHA-256 digest over a canonical publication value.
type IndexDigest string

// IndexScope identifies the checkout incarnation being published.
type IndexScope struct {
	SourceID      string
	CheckoutID    string
	IncarnationID string
}

// IndexCaller is the authenticated publishing worker identity.
type IndexCaller struct {
	AuthRealm     string
	Principal     string
	OwnerInstance string
}

// IndexManifestMode distinguishes a complete census from a delta update.
type IndexManifestMode string

const (
	IndexManifestFull  IndexManifestMode = "full"
	IndexManifestDelta IndexManifestMode = "delta"
)

// IndexJobKind is the closed publication job vocabulary.
type IndexJobKind string

const (
	IndexJobInitial   IndexJobKind = "initial_index"
	IndexJobReconcile IndexJobKind = "reconcile"
	IndexJobRecovery  IndexJobKind = "recovery"
)

// IndexScanOutcome records whether the scanner completed its authoritative census.
type IndexScanOutcome string

const (
	IndexScanComplete   IndexScanOutcome = "complete"
	IndexScanIncomplete IndexScanOutcome = "incomplete"
	IndexScanFailed     IndexScanOutcome = "failed"
)

// IndexFileState is the closed membership state vocabulary.
type IndexFileState string

const (
	IndexFilePresent    IndexFileState = "present"
	IndexFileExcluded   IndexFileState = "excluded"
	IndexFileUnreadable IndexFileState = "unreadable"
)

// IndexCoverageState is the closed coverage vocabulary.
type IndexCoverageState string

const (
	IndexCoverageComplete    IndexCoverageState = "complete"
	IndexCoveragePartial     IndexCoverageState = "partial"
	IndexCoverageUnavailable IndexCoverageState = "unavailable"
)

// IndexRelation is a declared directed graph relation.
type IndexRelation string

// IndexEvidenceKind identifies how a graph edge was produced.
type IndexEvidenceKind string

// IndexResolutionState records the closed resolution outcome.
type IndexResolutionState string

// IndexBeginInput opens or exactly replays one fenced staging build.
type IndexBeginInput struct {
	BuildKey       string
	Scope          IndexScope
	ProfileID      string
	ExpectedParent *ContextRef
	Mode           IndexManifestMode
	JobKind        IndexJobKind
}

// IndexBuildRef is the capability that fences Stage and Finalize.
type IndexBuildRef struct {
	BuildID       string
	Scope         IndexScope
	OwnerInstance string
	LeaseEpoch    int64
}

// IndexBeginResult is returned after durable build acquisition.
type IndexBeginResult struct {
	Build          IndexBuildRef
	LeaseExpiresAt time.Time
	Published      *IndexPublishedView
}

// IndexArtifactProof binds an admitted immutable artifact to its safe bytes and facts.
type IndexArtifactProof struct {
	ArtifactID         string
	ContentDigest      IndexDigest
	FactsDigest        IndexDigest
	DefinitionCount    uint64
	ReferenceSiteCount uint64
	ChunkCount         uint64
}

// IndexMembership describes one visible path in a candidate manifest.
type IndexMembership struct {
	PathKey     string
	DisplayPath string
	Mode        string
	State       IndexFileState
	ArtifactID  *string
}

// IndexDeletion records an explicit confirmed-missing path in a delta.
type IndexDeletion struct {
	PathKey          string
	ConfirmedMissing bool
}

// IndexSpan is a source-grounded byte and line span.
type IndexSpan struct {
	ByteStart int64
	ByteEnd   int64
	LineStart int
	LineEnd   int
}

// IndexEdgeTarget describes one resolved target in the same candidate membership.
type IndexEdgeTarget struct {
	PathKey    string
	ArtifactID string
	SymbolKey  *string
}

// IndexEdgeEvidence binds an edge to a source fact.
type IndexEdgeEvidence struct {
	ReferenceSiteID *string
	Span            IndexSpan
	RuleKey         string
	Explanation     string
}

// IndexEdge describes one scoped, directed resolved edge.
type IndexEdge struct {
	EdgeKey          string
	SourceArtifactID string
	SourceSymbolKey  *string
	Target           *IndexEdgeTarget
	Relation         IndexRelation
	EvidenceKind     IndexEvidenceKind
	ResolutionState  IndexResolutionState
	ResolverRevision string
	Evidence         IndexEdgeEvidence
}

// IndexEdgeReplacement replaces every current edge emitted by one source path.
type IndexEdgeReplacement struct {
	SourcePath string
	Edges      []IndexEdge
}

// IndexPart is one immutable staged frame of a candidate publication.
type IndexPart struct {
	Artifacts        []IndexArtifactProof
	Memberships      []IndexMembership
	Deletions        []IndexDeletion
	EdgeReplacements []IndexEdgeReplacement
}

// IndexStageInput is one sequenced, content-addressed staging frame.
type IndexStageInput struct {
	Build    IndexBuildRef
	Sequence uint32
	Digest   IndexDigest
	Part     IndexPart
}

// IndexPartAck proves one durable staged frame.
type IndexPartAck struct {
	BuildID  string
	Sequence uint32
	Digest   IndexDigest
}

// IndexObservation records the bounded source observation window for a View.
type IndexObservation struct {
	HeadOID       *string
	ObjectFormat  *string
	RefLabel      *string
	Dirty         bool
	ObservedFSSeq int64
	ScanStart     time.Time
	ScanEnd       time.Time
}

// IndexCoverage describes publication coverage without fabricating completeness.
type IndexCoverage struct {
	Structural           IndexCoverageState
	Lexical              IndexCoverageState
	Vector               IndexCoverageState
	ExcludedFiles        uint64
	UnreadableFiles      uint64
	UnresolvedReferences uint64
}

// IndexManifestCompletion is the explicit final census declaration.
type IndexManifestCompletion struct {
	PartCount      uint32
	PartsDigest    IndexDigest
	EntryCount     uint64
	ManifestDigest IndexDigest
	EdgeCount      uint64
	EdgesDigest    IndexDigest
	ScanOutcome    IndexScanOutcome
	CensusComplete bool
	Observation    IndexObservation
	Coverage       IndexCoverage
}

// IndexFinalizeInput is the only operation that can publish a staged build.
type IndexFinalizeInput struct {
	Build          IndexBuildRef
	ExpectedParent *ContextRef
	Manifest       IndexManifestCompletion
}

// IndexPublishedView is the durable result replayed after a lost ACK.
type IndexPublishedView struct {
	BuildID        string
	Context        ContextRef
	ManifestDigest IndexDigest
	AcceptedFSSeq  int64
	PublishedAt    time.Time
}

// IndexPublicationLimits bounds work retained by one staging build.
type IndexPublicationLimits struct {
	LeaseTTL           time.Duration
	MaxPartBytes       int64
	MaxParts           uint32
	MaxBuildBytes      int64
	MaxManifestEntries uint64
	MaxEdges           uint64
	MaxArtifactBytes   int64
}

// DefaultIndexPublicationLimits returns the first-release production bounds.
// Payload and artifact limits share the admission hard caps; manifest and edge
// counts match the private transport bounds for the documented large-repository
// profile. A fresh value prevents callers from mutating shared policy state.
func DefaultIndexPublicationLimits() IndexPublicationLimits {
	return IndexPublicationLimits{
		LeaseTTL:           5 * time.Minute,
		MaxPartBytes:       IndexAdmissionMaxEncodedFrameBytes,
		MaxParts:           IndexAdmissionMaxFrames,
		MaxBuildBytes:      IndexAdmissionMaxTotalEncodedBytes,
		MaxManifestEntries: 1_000_000,
		MaxEdges:           5_000_000,
		MaxArtifactBytes:   IndexAdmissionMaxArtifactBodyBytes,
	}
}

// IndexStore owns the fenced durable publication state machine.
type IndexStore interface {
	Begin(ctx context.Context, caller IndexCaller, input IndexBeginInput) (IndexBeginResult, error)
	Stage(ctx context.Context, caller IndexCaller, input IndexStageInput) (IndexPartAck, error)
	Finalize(ctx context.Context, caller IndexCaller, input IndexFinalizeInput) (IndexPublishedView, error)
}

// DigestIndexPart returns a stable digest for one immutable staged part.
func DigestIndexPart(part IndexPart) (IndexDigest, error) {
	normalized, err := normalizeIndexPart(part)
	if err != nil {
		return "", err
	}
	return digestIndexValue("part", normalized)
}

// DigestIndexParts returns a stable digest of the contiguous staged acknowledgements.
func DigestIndexParts(acks []IndexPartAck) (IndexDigest, error) {
	normalized := append([]IndexPartAck(nil), acks...)
	sort.Slice(normalized, func(left, right int) bool {
		return normalized[left].Sequence < normalized[right].Sequence
	})
	var buildID string
	for index, ack := range normalized {
		if !canonicalContextUUID(ack.BuildID) || !isIndexDigest(ack.Digest) {
			return "", fmt.Errorf("uci publication: invalid staged part acknowledgement")
		}
		if index > 0 && normalized[index-1].Sequence == ack.Sequence {
			return "", fmt.Errorf("uci publication: duplicate staged part sequence")
		}
		if index == 0 {
			buildID = ack.BuildID
		} else if ack.BuildID != buildID {
			return "", fmt.Errorf("uci publication: acknowledgements span multiple builds")
		}
	}
	if normalized == nil {
		normalized = []IndexPartAck{}
	}
	return digestIndexValue("parts", normalized)
}

// DigestIndexManifest returns a stable digest of the final desired membership map.
func DigestIndexManifest(memberships []IndexMembership) (IndexDigest, error) {
	normalized, err := normalizeIndexMemberships(memberships)
	if err != nil {
		return "", err
	}
	return digestIndexValue("manifest", normalized)
}

// DigestIndexEdges returns a stable digest of the final replacement map.
func DigestIndexEdges(replacements []IndexEdgeReplacement) (IndexDigest, error) {
	normalized, err := normalizeIndexEdgeReplacements(replacements)
	if err != nil {
		return "", err
	}
	return digestIndexValue("edges", normalized)
}

func digestIndexValue(kind string, value any) (IndexDigest, error) {
	encoded, err := json.Marshal(struct {
		Kind    string `json:"kind"`
		Value   any    `json:"value"`
		Version int    `json:"version"`
	}{
		Kind:    kind,
		Value:   value,
		Version: indexDigestVersion,
	})
	if err != nil {
		return "", fmt.Errorf("uci publication: encode canonical %s: %w", kind, err)
	}
	sum := sha256.Sum256(encoded)
	return IndexDigest("sha256:" + hex.EncodeToString(sum[:])), nil
}

func normalizeIndexPart(part IndexPart) (IndexPart, error) {
	artifacts := append([]IndexArtifactProof(nil), part.Artifacts...)
	sort.Slice(artifacts, func(left, right int) bool {
		return artifacts[left].ArtifactID < artifacts[right].ArtifactID
	})
	for index, artifact := range artifacts {
		if !canonicalContextUUID(artifact.ArtifactID) || !isIndexDigest(artifact.ContentDigest) || !isIndexDigest(artifact.FactsDigest) {
			return IndexPart{}, fmt.Errorf("uci publication: invalid artifact proof")
		}
		if index > 0 && artifacts[index-1].ArtifactID == artifact.ArtifactID {
			return IndexPart{}, fmt.Errorf("uci publication: duplicate artifact proof")
		}
	}
	memberships, err := normalizeIndexMemberships(part.Memberships)
	if err != nil {
		return IndexPart{}, err
	}
	deletions, err := normalizeIndexDeletions(part.Deletions)
	if err != nil {
		return IndexPart{}, err
	}
	replacements, err := normalizeIndexEdgeReplacements(part.EdgeReplacements)
	if err != nil {
		return IndexPart{}, err
	}
	if artifacts == nil {
		artifacts = []IndexArtifactProof{}
	}
	return IndexPart{
		Artifacts:        artifacts,
		Memberships:      memberships,
		Deletions:        deletions,
		EdgeReplacements: replacements,
	}, nil
}

func normalizeIndexMemberships(memberships []IndexMembership) ([]IndexMembership, error) {
	normalized := append([]IndexMembership(nil), memberships...)
	sort.Slice(normalized, func(left, right int) bool {
		return normalized[left].PathKey < normalized[right].PathKey
	})
	for index, membership := range normalized {
		if !validIndexText(membership.PathKey) || !validIndexText(membership.DisplayPath) || !validIndexText(membership.Mode) {
			return nil, fmt.Errorf("uci publication: invalid membership")
		}
		if index > 0 && normalized[index-1].PathKey == membership.PathKey {
			return nil, fmt.Errorf("uci publication: duplicate membership path")
		}
		switch membership.State {
		case IndexFilePresent:
			if membership.ArtifactID == nil || !canonicalContextUUID(*membership.ArtifactID) {
				return nil, fmt.Errorf("uci publication: present membership requires an artifact")
			}
		case IndexFileExcluded, IndexFileUnreadable:
			if membership.ArtifactID != nil {
				return nil, fmt.Errorf("uci publication: non-present membership must not carry an artifact")
			}
		default:
			return nil, fmt.Errorf("uci publication: unsupported membership state")
		}
	}
	if normalized == nil {
		normalized = []IndexMembership{}
	}
	return normalized, nil
}

func normalizeIndexDeletions(deletions []IndexDeletion) ([]IndexDeletion, error) {
	normalized := append([]IndexDeletion(nil), deletions...)
	sort.Slice(normalized, func(left, right int) bool {
		return normalized[left].PathKey < normalized[right].PathKey
	})
	for index, deletion := range normalized {
		if !validIndexText(deletion.PathKey) || !deletion.ConfirmedMissing {
			return nil, fmt.Errorf("uci publication: invalid deletion")
		}
		if index > 0 && normalized[index-1].PathKey == deletion.PathKey {
			return nil, fmt.Errorf("uci publication: duplicate deletion path")
		}
	}
	if normalized == nil {
		normalized = []IndexDeletion{}
	}
	return normalized, nil
}

func normalizeIndexEdgeReplacements(replacements []IndexEdgeReplacement) ([]IndexEdgeReplacement, error) {
	normalized := append([]IndexEdgeReplacement(nil), replacements...)
	sort.Slice(normalized, func(left, right int) bool {
		return normalized[left].SourcePath < normalized[right].SourcePath
	})
	for index := range normalized {
		replacement := &normalized[index]
		if !validIndexText(replacement.SourcePath) {
			return nil, fmt.Errorf("uci publication: invalid edge replacement path")
		}
		if index > 0 && normalized[index-1].SourcePath == replacement.SourcePath {
			return nil, fmt.Errorf("uci publication: duplicate edge replacement path")
		}
		edges := append([]IndexEdge(nil), replacement.Edges...)
		sort.Slice(edges, func(left, right int) bool {
			return edges[left].EdgeKey < edges[right].EdgeKey
		})
		for edgeIndex, edge := range edges {
			if err := validateIndexEdge(edge); err != nil {
				return nil, err
			}
			if edgeIndex > 0 && edges[edgeIndex-1].EdgeKey == edge.EdgeKey {
				return nil, fmt.Errorf("uci publication: duplicate edge key")
			}
		}
		if edges == nil {
			edges = []IndexEdge{}
		}
		replacement.Edges = edges
	}
	if normalized == nil {
		normalized = []IndexEdgeReplacement{}
	}
	return normalized, nil
}

func validateIndexEdge(edge IndexEdge) error {
	if !validIndexText(edge.EdgeKey) || !canonicalContextUUID(edge.SourceArtifactID) || !validIndexText(edge.ResolverRevision) {
		return fmt.Errorf("uci publication: invalid edge")
	}
	if edge.SourceSymbolKey != nil && !validIndexText(*edge.SourceSymbolKey) {
		return fmt.Errorf("uci publication: invalid source symbol")
	}
	if !isIndexRelation(edge.Relation) || !isIndexEvidenceKind(edge.EvidenceKind) || !isIndexResolutionState(edge.ResolutionState) {
		return fmt.Errorf("uci publication: unsupported edge vocabulary")
	}
	if edge.Target != nil {
		if !validIndexText(edge.Target.PathKey) || !canonicalContextUUID(edge.Target.ArtifactID) {
			return fmt.Errorf("uci publication: invalid edge target")
		}
		if edge.Target.SymbolKey != nil && !validIndexText(*edge.Target.SymbolKey) {
			return fmt.Errorf("uci publication: invalid target symbol")
		}
	}
	if edge.ResolutionState == IndexResolutionState("resolved") && edge.Target == nil {
		return fmt.Errorf("uci publication: resolved edge requires a target")
	}
	if edge.ResolutionState != IndexResolutionState("resolved") && edge.Target != nil {
		return fmt.Errorf("uci publication: unresolved edge must not carry a target")
	}
	if edge.Evidence.ReferenceSiteID != nil && !canonicalContextUUID(*edge.Evidence.ReferenceSiteID) {
		return fmt.Errorf("uci publication: invalid edge reference site")
	}
	if err := validateIndexSpan(edge.Evidence.Span); err != nil {
		return err
	}
	if !validIndexText(edge.Evidence.RuleKey) || !validIndexText(edge.Evidence.Explanation) {
		return fmt.Errorf("uci publication: invalid edge evidence")
	}
	return nil
}

func validateIndexSpan(span IndexSpan) error {
	if span.ByteStart < 0 || span.ByteEnd < span.ByteStart || span.LineStart < 1 || span.LineEnd < span.LineStart {
		return fmt.Errorf("uci publication: invalid edge span")
	}
	return nil
}

func validIndexScope(scope IndexScope) bool {
	return canonicalContextUUID(scope.SourceID) && canonicalContextUUID(scope.CheckoutID) && canonicalContextUUID(scope.IncarnationID)
}

func validIndexCaller(caller IndexCaller) bool {
	return validIndexText(caller.AuthRealm) && validIndexText(caller.Principal) && validIndexText(caller.OwnerInstance)
}

func validIndexText(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func isIndexDigest(value IndexDigest) bool {
	if !strings.HasPrefix(string(value), "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(string(value[len("sha256:"):]))
	return err == nil && strings.ToLower(string(value[len("sha256:"):])) == string(value[len("sha256:"):])
}

func isIndexManifestMode(mode IndexManifestMode) bool {
	return mode == IndexManifestFull || mode == IndexManifestDelta
}

func isIndexJobKind(kind IndexJobKind) bool {
	switch kind {
	case IndexJobInitial, IndexJobReconcile, IndexJobRecovery:
		return true
	default:
		return false
	}
}

func isIndexScanOutcome(outcome IndexScanOutcome) bool {
	switch outcome {
	case IndexScanComplete, IndexScanIncomplete, IndexScanFailed:
		return true
	default:
		return false
	}
}

func isIndexCoverageState(state IndexCoverageState) bool {
	switch state {
	case IndexCoverageComplete, IndexCoveragePartial, IndexCoverageUnavailable:
		return true
	default:
		return false
	}
}

func isIndexRelation(relation IndexRelation) bool {
	switch relation {
	case "contains", "imports", "exports", "references", "calls", "may_call", "inherits", "implements", "documents", "mentions", "configures", "schema_references", "tests", "depends_on":
		return true
	default:
		return false
	}
}

func isIndexEvidenceKind(kind IndexEvidenceKind) bool {
	switch kind {
	case "extracted", "resolved", "heuristic", "semantic", "unresolved":
		return true
	default:
		return false
	}
}

func isIndexResolutionState(state IndexResolutionState) bool {
	switch state {
	case "resolved", "unresolved", "ambiguous", "partial":
		return true
	default:
		return false
	}
}
