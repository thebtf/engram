package uci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// ReconcileService turns one complete scanner census and its immutable facts
// into a single fenced IndexStore publication.
type ReconcileService struct {
	store IndexStore
}

// ReconcileRequest is the complete current-state candidate for one checkout.
type ReconcileRequest struct {
	BuildKey       string
	Scope          IndexScope
	ProfileID      string
	ExpectedParent *ContextRef
	Scan           ScannerResult
	Parts          []IndexPart
}

// ReconcileResult contains only the durable View returned by IndexStore.
type ReconcileResult struct {
	View IndexPublishedView
}

// NewReconcileService constructs a reconciliation service over the canonical
// fenced publication protocol.
func NewReconcileService(store IndexStore) *ReconcileService {
	return &ReconcileService{store: store}
}

// Reconcile validates a complete scanner census, stages every canonical part,
// and publishes it through the IndexStore fence. It never advances a local
// acknowledgement; AcceptedFSSeq remains an outer durable-publication token.
func (service *ReconcileService) Reconcile(ctx context.Context, caller IndexCaller, request ReconcileRequest) (ReconcileResult, error) {
	if service == nil || service.store == nil {
		return ReconcileResult{}, fmt.Errorf("uci reconcile: missing index store")
	}
	if ctx == nil {
		return ReconcileResult{}, fmt.Errorf("uci reconcile: nil context")
	}
	if err := ctx.Err(); err != nil {
		return ReconcileResult{}, err
	}

	plan, err := prepareReconcilePlan(caller, request)
	if err != nil {
		return ReconcileResult{}, err
	}

	begun, err := service.store.Begin(ctx, caller, plan.begin)
	if err != nil {
		return ReconcileResult{}, err
	}
	if err := validateReconcileBuild(begun.Build, caller, request.Scope); err != nil {
		return ReconcileResult{}, err
	}
	if begun.Published != nil {
		published := cloneReconcilePublishedView(*begun.Published)
		if err := validateReconcilePublishedView(published, begun.Build, request.Scope, request.ProfileID); err != nil {
			return ReconcileResult{}, err
		}
		if published.ManifestDigest != plan.manifestDigest || published.AcceptedFSSeq != plan.observation.ObservedFSSeq {
			return ReconcileResult{}, fmt.Errorf("uci reconcile: durable replay does not match the requested payload")
		}
		return ReconcileResult{View: published}, nil
	}

	acks := make([]IndexPartAck, 0, len(plan.parts))
	for sequence, part := range plan.parts {
		ack, err := service.store.Stage(ctx, caller, IndexStageInput{
			Build:    begun.Build,
			Sequence: uint32(sequence),
			Digest:   plan.partDigests[sequence],
			Part:     part,
		})
		if err != nil {
			return ReconcileResult{}, err
		}
		if ack.BuildID != begun.Build.BuildID || ack.Sequence != uint32(sequence) || ack.Digest != plan.partDigests[sequence] {
			return ReconcileResult{}, fmt.Errorf("uci reconcile: invalid stage acknowledgement")
		}
		acks = append(acks, ack)
	}

	partsDigest, err := DigestIndexParts(acks)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("uci reconcile: digest staged parts: %w", err)
	}
	published, err := service.store.Finalize(ctx, caller, IndexFinalizeInput{
		Build:          begun.Build,
		ExpectedParent: cloneReconcileContextRef(plan.expectedParent),
		Manifest: IndexManifestCompletion{
			PartCount:      uint32(len(plan.parts)),
			PartsDigest:    partsDigest,
			EntryCount:     uint64(len(plan.memberships)),
			ManifestDigest: plan.manifestDigest,
			EdgeCount:      plan.edgeCount,
			EdgesDigest:    plan.edgesDigest,
			ScanOutcome:    IndexScanComplete,
			CensusComplete: true,
			Observation:    plan.observation,
			Coverage:       plan.coverage,
		},
	})
	if err != nil {
		return ReconcileResult{}, err
	}
	published = cloneReconcilePublishedView(published)
	if err := validateReconcilePublishedView(published, begun.Build, request.Scope, request.ProfileID); err != nil {
		return ReconcileResult{}, err
	}
	if published.ManifestDigest != plan.manifestDigest || published.AcceptedFSSeq != plan.observation.ObservedFSSeq {
		return ReconcileResult{}, fmt.Errorf("uci reconcile: published view does not match the requested payload")
	}
	return ReconcileResult{View: published}, nil
}

type reconcilePlan struct {
	begin          IndexBeginInput
	expectedParent *ContextRef
	parts          []IndexPart
	partDigests    []IndexDigest
	memberships    []IndexMembership
	manifestDigest IndexDigest
	edgeCount      uint64
	edgesDigest    IndexDigest
	observation    IndexObservation
	coverage       IndexCoverage
}

type reconcileScanFile struct {
	state         IndexFileState
	contentDigest IndexDigest
}

type reconcileScan struct {
	files       map[string]reconcileScanFile
	observation IndexObservation
	coverage    IndexCoverage
}

type reconcilePartFrame struct {
	part   IndexPart
	digest IndexDigest
}

func prepareReconcilePlan(caller IndexCaller, request ReconcileRequest) (reconcilePlan, error) {
	if !validIndexCaller(caller) {
		return reconcilePlan{}, fmt.Errorf("uci reconcile: invalid caller")
	}
	if !validIndexText(request.BuildKey) {
		return reconcilePlan{}, fmt.Errorf("uci reconcile: invalid build key")
	}
	if !validIndexScope(request.Scope) {
		return reconcilePlan{}, fmt.Errorf("uci reconcile: invalid scope")
	}
	if !canonicalContextUUID(request.ProfileID) {
		return reconcilePlan{}, fmt.Errorf("uci reconcile: invalid profile")
	}

	parent, err := canonicalReconcileParent(request.ExpectedParent, request.Scope, request.ProfileID)
	if err != nil {
		return reconcilePlan{}, err
	}
	scan, err := canonicalReconcileScan(request.Scan)
	if err != nil {
		return reconcilePlan{}, err
	}
	parts, partDigests, memberships, replacements, edgeCount, err := canonicalReconcileParts(request.Parts, scan)
	if err != nil {
		return reconcilePlan{}, err
	}
	manifestDigest, err := DigestIndexManifest(memberships)
	if err != nil {
		return reconcilePlan{}, fmt.Errorf("uci reconcile: digest manifest: %w", err)
	}
	edgesDigest, err := DigestIndexEdges(replacements)
	if err != nil {
		return reconcilePlan{}, fmt.Errorf("uci reconcile: digest edges: %w", err)
	}
	publicationKey, err := reconcilePublicationKey(request.BuildKey, partDigests, manifestDigest, edgesDigest, edgeCount, scan.observation, scan.coverage)
	if err != nil {
		return reconcilePlan{}, err
	}

	return reconcilePlan{
		begin: IndexBeginInput{
			BuildKey:       publicationKey,
			Scope:          request.Scope,
			ProfileID:      request.ProfileID,
			ExpectedParent: cloneReconcileContextRef(parent),
			Mode:           IndexManifestFull,
			JobKind:        IndexJobReconcile,
		},
		expectedParent: parent,
		parts:          parts,
		partDigests:    partDigests,
		memberships:    memberships,
		manifestDigest: manifestDigest,
		edgeCount:      edgeCount,
		edgesDigest:    edgesDigest,
		observation:    scan.observation,
		coverage:       scan.coverage,
	}, nil
}

func reconcilePublicationKey(requestKey string, partDigests []IndexDigest, manifestDigest, edgesDigest IndexDigest, edgeCount uint64, observation IndexObservation, coverage IndexCoverage) (string, error) {
	digest, err := digestIndexValue("reconcile-build", struct {
		RequestKey     string           `json:"request_key"`
		PartDigests    []IndexDigest    `json:"part_digests"`
		ManifestDigest IndexDigest      `json:"manifest_digest"`
		EdgesDigest    IndexDigest      `json:"edges_digest"`
		EdgeCount      uint64           `json:"edge_count"`
		Observation    IndexObservation `json:"observation"`
		Coverage       IndexCoverage    `json:"coverage"`
	}{
		RequestKey:     requestKey,
		PartDigests:    append([]IndexDigest(nil), partDigests...),
		ManifestDigest: manifestDigest,
		EdgesDigest:    edgesDigest,
		EdgeCount:      edgeCount,
		Observation:    cloneReconcileObservation(observation),
		Coverage:       coverage,
	})
	if err != nil {
		return "", fmt.Errorf("uci reconcile: digest publication binding: %w", err)
	}
	return string(digest), nil
}

func canonicalReconcileParent(parent *ContextRef, scope IndexScope, profileID string) (*ContextRef, error) {
	if parent == nil {
		return nil, nil
	}
	canonical := parent.clone()
	if !canonical.valid() || canonical.SourceID != scope.SourceID || canonical.CheckoutID != scope.CheckoutID || canonical.AnalysisProfileID != profileID {
		return nil, fmt.Errorf("uci reconcile: invalid expected parent")
	}
	return &canonical, nil
}

func canonicalReconcileScan(scan ScannerResult) (reconcileScan, error) {
	if scan.Census.Outcome != IndexScanComplete || !scan.Census.Complete || !scan.Census.CanDeleteAll {
		return reconcileScan{}, fmt.Errorf("uci reconcile: scan is not a complete delete-all census")
	}
	if err := validateReconcileObservation(scan.Observation); err != nil {
		return reconcileScan{}, err
	}
	if scan.Coverage.Structural != IndexCoverageComplete || !isIndexCoverageState(scan.Coverage.Lexical) || !isIndexCoverageState(scan.Coverage.Vector) {
		return reconcileScan{}, fmt.Errorf("uci reconcile: invalid complete scan coverage")
	}

	files := make(map[string]reconcileScanFile, len(scan.Files))
	var excluded uint64
	var unreadable uint64
	for _, file := range scan.Files {
		if err := scannerValidateGitPath(file.Path); err != nil {
			return reconcileScan{}, fmt.Errorf("uci reconcile: invalid scan path %q: %w", file.Path, err)
		}
		if _, exists := files[file.Path]; exists {
			return reconcileScan{}, fmt.Errorf("uci reconcile: duplicate scan path %q", file.Path)
		}

		fact := reconcileScanFile{state: file.State}
		switch file.State {
		case IndexFilePresent:
			if file.Exclusion != ScannerExclusionNone {
				return reconcileScan{}, fmt.Errorf("uci reconcile: present scan file %q has an exclusion", file.Path)
			}
			sum := sha256.Sum256(file.Body)
			fact.contentDigest = IndexDigest("sha256:" + hex.EncodeToString(sum[:]))
		case IndexFileExcluded:
			if len(file.Body) != 0 || !validReconcileExclusion(file.Exclusion) || file.Exclusion == ScannerExclusionNone {
				return reconcileScan{}, fmt.Errorf("uci reconcile: invalid excluded scan file %q", file.Path)
			}
			excluded++
		case IndexFileUnreadable:
			if len(file.Body) != 0 || (file.Exclusion != ScannerExclusionNone && file.Exclusion != ScannerExclusionChanging) {
				return reconcileScan{}, fmt.Errorf("uci reconcile: invalid unreadable scan file %q", file.Path)
			}
			unreadable++
		default:
			return reconcileScan{}, fmt.Errorf("uci reconcile: unsupported scan file state %q", file.State)
		}
		files[file.Path] = fact
	}
	if scan.Coverage.ExcludedFiles != excluded || scan.Coverage.UnreadableFiles != unreadable {
		return reconcileScan{}, fmt.Errorf("uci reconcile: scan coverage does not match scan files")
	}

	return reconcileScan{
		files:       files,
		observation: cloneReconcileObservation(scan.Observation),
		coverage:    scan.Coverage,
	}, nil
}

func validateReconcileObservation(observation IndexObservation) error {
	if observation.ObservedFSSeq < 0 || observation.ScanStart.IsZero() || observation.ScanEnd.IsZero() || observation.ScanEnd.Before(observation.ScanStart) {
		return fmt.Errorf("uci reconcile: invalid scan observation")
	}
	if observation.ObjectFormat != nil && *observation.ObjectFormat != "sha1" && *observation.ObjectFormat != "sha256" {
		return fmt.Errorf("uci reconcile: unsupported object format")
	}
	if observation.HeadOID != nil {
		if observation.ObjectFormat == nil {
			return fmt.Errorf("uci reconcile: head has no object format")
		}
		length := 40
		if *observation.ObjectFormat == "sha256" {
			length = 64
		}
		if !validReconcileLowerHex(*observation.HeadOID, length) {
			return fmt.Errorf("uci reconcile: invalid head object id")
		}
	}
	if observation.RefLabel != nil && !validIndexText(*observation.RefLabel) {
		return fmt.Errorf("uci reconcile: invalid ref label")
	}
	return nil
}

func validReconcileLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validReconcileExclusion(exclusion ScannerExclusion) bool {
	switch exclusion {
	case ScannerExclusionNone,
		ScannerExclusionProtected,
		ScannerExclusionSecret,
		ScannerExclusionTooLarge,
		ScannerExclusionUnsupportedType,
		ScannerExclusionBinary,
		ScannerExclusionNestedRepository,
		ScannerExclusionSubmodule,
		ScannerExclusionReparseEscape,
		ScannerExclusionChanging:
		return true
	default:
		return false
	}
}

func canonicalReconcileParts(parts []IndexPart, scan reconcileScan) ([]IndexPart, []IndexDigest, []IndexMembership, []IndexEdgeReplacement, uint64, error) {
	if uint64(len(parts)) > uint64(^uint32(0)) {
		return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: too many parts")
	}

	frames := make([]reconcilePartFrame, 0, len(parts))
	for index, part := range parts {
		canonical, err := normalizeIndexPart(cloneReconcilePart(part))
		if err != nil {
			return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: invalid part %d: %w", index, err)
		}
		if len(canonical.Deletions) != 0 {
			return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: full reconciliation must not stage deletions")
		}
		digest, err := DigestIndexPart(canonical)
		if err != nil {
			return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: digest part %d: %w", index, err)
		}
		frames = append(frames, reconcilePartFrame{part: canonical, digest: digest})
	}
	sort.SliceStable(frames, func(left, right int) bool {
		return frames[left].digest < frames[right].digest
	})

	artifacts := make(map[string]IndexArtifactProof)
	membershipsByPath := make(map[string]IndexMembership)
	replacementsByPath := make(map[string]IndexEdgeReplacement)
	partsOut := make([]IndexPart, 0, len(frames))
	partDigests := make([]IndexDigest, 0, len(frames))
	memberships := make([]IndexMembership, 0)
	replacements := make([]IndexEdgeReplacement, 0)
	var edgeCount uint64

	for _, frame := range frames {
		partsOut = append(partsOut, frame.part)
		partDigests = append(partDigests, frame.digest)
		for _, artifact := range frame.part.Artifacts {
			if existing, found := artifacts[artifact.ArtifactID]; found && existing != artifact {
				return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: conflicting artifact proof %q", artifact.ArtifactID)
			}
			artifacts[artifact.ArtifactID] = artifact
		}
		for _, membership := range frame.part.Memberships {
			if _, found := membershipsByPath[membership.PathKey]; found {
				return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: duplicate membership path %q", membership.PathKey)
			}
			membershipsByPath[membership.PathKey] = membership
			memberships = append(memberships, membership)
		}
		for _, replacement := range frame.part.EdgeReplacements {
			if _, found := replacementsByPath[replacement.SourcePath]; found {
				return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: duplicate edge replacement path %q", replacement.SourcePath)
			}
			edges := uint64(len(replacement.Edges))
			if ^uint64(0)-edgeCount < edges {
				return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: edge count overflow")
			}
			edgeCount += edges
			replacementsByPath[replacement.SourcePath] = replacement
			replacements = append(replacements, replacement)
		}
	}

	usedArtifacts := make(map[string]struct{})
	for path, membership := range membershipsByPath {
		file, found := scan.files[path]
		if !found || membership.DisplayPath != path || membership.State != file.state {
			return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: membership %q does not represent the complete scan", path)
		}
		if membership.State != IndexFilePresent {
			continue
		}
		proof, found := artifacts[*membership.ArtifactID]
		if !found || proof.ContentDigest != file.contentDigest {
			return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: membership %q has no matching immutable artifact", path)
		}
		usedArtifacts[*membership.ArtifactID] = struct{}{}
	}
	for path := range scan.files {
		if _, found := membershipsByPath[path]; !found {
			return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: scan path %q has no membership", path)
		}
	}
	for artifactID := range artifacts {
		if _, found := usedArtifacts[artifactID]; !found {
			return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: artifact proof %q is not current", artifactID)
		}
	}

	for path := range membershipsByPath {
		if _, found := replacementsByPath[path]; !found {
			return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: membership %q has no edge replacement", path)
		}
	}
	seenEdgeKeys := make(map[string]struct{})
	for path, replacement := range replacementsByPath {
		membership, found := membershipsByPath[path]
		if !found {
			return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: edge replacement %q is outside the scan", path)
		}
		for _, edge := range replacement.Edges {
			if _, found := seenEdgeKeys[edge.EdgeKey]; found {
				return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: duplicate edge key %q", edge.EdgeKey)
			}
			seenEdgeKeys[edge.EdgeKey] = struct{}{}
			if membership.State != IndexFilePresent || membership.ArtifactID == nil || *membership.ArtifactID != edge.SourceArtifactID {
				return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: edge %q does not match its source", edge.EdgeKey)
			}
			if edge.Target == nil {
				continue
			}
			target, found := membershipsByPath[edge.Target.PathKey]
			if !found || target.State != IndexFilePresent || target.ArtifactID == nil || *target.ArtifactID != edge.Target.ArtifactID {
				return nil, nil, nil, nil, 0, fmt.Errorf("uci reconcile: edge %q does not match its target", edge.EdgeKey)
			}
		}
	}

	return partsOut, partDigests, memberships, replacements, edgeCount, nil
}

func validateReconcileBuild(build IndexBuildRef, caller IndexCaller, scope IndexScope) error {
	if !canonicalContextUUID(build.BuildID) || build.Scope != scope || build.OwnerInstance != caller.OwnerInstance || !validIndexText(build.OwnerInstance) || build.LeaseEpoch <= 0 {
		return fmt.Errorf("uci reconcile: invalid store build reference")
	}
	return nil
}

func validateReconcilePublishedView(view IndexPublishedView, build IndexBuildRef, scope IndexScope, profileID string) error {
	if view.BuildID != build.BuildID || !view.Context.valid() || view.Context.SourceID != scope.SourceID || view.Context.CheckoutID != scope.CheckoutID || view.Context.AnalysisProfileID != profileID || !isIndexDigest(view.ManifestDigest) || view.AcceptedFSSeq < 0 || view.PublishedAt.IsZero() {
		return fmt.Errorf("uci reconcile: invalid published view")
	}
	return nil
}

func cloneReconcileContextRef(ref *ContextRef) *ContextRef {
	if ref == nil {
		return nil
	}
	clone := ref.clone()
	return &clone
}

func cloneReconcilePublishedView(view IndexPublishedView) IndexPublishedView {
	view.Context = view.Context.clone()
	return view
}

func cloneReconcileObservation(observation IndexObservation) IndexObservation {
	clone := observation
	clone.HeadOID = cloneReconcileString(observation.HeadOID)
	clone.ObjectFormat = cloneReconcileString(observation.ObjectFormat)
	clone.RefLabel = cloneReconcileString(observation.RefLabel)
	return clone
}

func cloneReconcileString(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneReconcilePart(part IndexPart) IndexPart {
	clone := IndexPart{
		Artifacts: append([]IndexArtifactProof(nil), part.Artifacts...),
		Deletions: append([]IndexDeletion(nil), part.Deletions...),
	}
	if part.Memberships != nil {
		clone.Memberships = make([]IndexMembership, len(part.Memberships))
		for index, membership := range part.Memberships {
			clone.Memberships[index] = membership
			clone.Memberships[index].ArtifactID = cloneReconcileString(membership.ArtifactID)
		}
	}
	if part.EdgeReplacements != nil {
		clone.EdgeReplacements = make([]IndexEdgeReplacement, len(part.EdgeReplacements))
		for index, replacement := range part.EdgeReplacements {
			clone.EdgeReplacements[index].SourcePath = replacement.SourcePath
			if replacement.Edges == nil {
				continue
			}
			clone.EdgeReplacements[index].Edges = make([]IndexEdge, len(replacement.Edges))
			for edgeIndex, edge := range replacement.Edges {
				clone.EdgeReplacements[index].Edges[edgeIndex] = cloneReconcileEdge(edge)
			}
		}
	}
	return clone
}

func cloneReconcileEdge(edge IndexEdge) IndexEdge {
	clone := edge
	clone.SourceSymbolKey = cloneReconcileString(edge.SourceSymbolKey)
	clone.Evidence.ReferenceSiteID = cloneReconcileString(edge.Evidence.ReferenceSiteID)
	if edge.Target == nil {
		clone.Target = nil
		return clone
	}
	target := *edge.Target
	target.SymbolKey = cloneReconcileString(edge.Target.SymbolKey)
	clone.Target = &target
	return clone
}
