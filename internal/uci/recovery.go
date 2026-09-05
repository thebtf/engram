package uci

import (
	"context"
	"fmt"
)

// RecoveryCurrentByteSource reads one complete, current-byte observation from
// an already-authorized local root. It has no authority to select or publish a
// checkout.
type RecoveryCurrentByteSource interface {
	ScanCurrent(context.Context, AuthorizedRootEvidence) (RecoveryCurrentBytes, error)
}

// RecoveryPublisher durably reconciles one complete current-byte observation.
type RecoveryPublisher interface {
	Reconcile(context.Context, IndexCaller, ReconcileRequest) (ReconcileResult, error)
}

// RecoveryLocalStatePort supplies only local recovery triggering and durable
// acknowledgement state. It does not select, authorize, or publish a View.
type RecoveryLocalStatePort interface {
	Snapshot(context.Context, string) (RecoveryLocalState, bool, error)
	MarkReconciled(context.Context, string, int64) error
}

// RecoveryEmbeddingEnsurer enriches a candidate already bound to a published
// immutable View.
type RecoveryEmbeddingEnsurer interface {
	EnsureCandidateEmbedding(context.Context, AuthorizedContext, QueryCandidate) error
}

// RecoveryLocalState is the bounded local checkpoint used to trigger a safe
// current-byte recovery. It intentionally contains no source bodies or server
// selection authority.
type RecoveryLocalState struct {
	CheckoutID             string
	IncarnationID          string
	DirtySequence          int64
	RescanRequired         bool
	LastReconciledSequence int64
}

// RecoveryCurrentBytes is one complete scanner observation and the immutable
// publication material derived from those current bytes.
type RecoveryCurrentBytes struct {
	Scan                ScannerResult
	Parts               []IndexPart
	EmbeddingCandidates []QueryCandidate
}

// RecoveryRequest names the already-authorized checkout and parent expected by
// the fenced durable reconciliation protocol.
type RecoveryRequest struct {
	Caller         IndexCaller
	Root           AuthorizedRootEvidence
	Scope          IndexScope
	ProfileID      string
	BuildKey       string
	ExpectedParent *ContextRef
}

// RecoveryResult contains the durable View after its current candidates have
// been idempotently enriched and the local high-water has been acknowledged.
type RecoveryResult struct {
	View IndexPublishedView
}

// RecoveryService recovers one checkout from a local rescan trigger. It keeps
// no local authority or replay cache: the publisher's fenced durable result is
// the source of truth for crash and lost-ack replays.
type RecoveryService struct {
	current    RecoveryCurrentByteSource
	publisher  RecoveryPublisher
	state      RecoveryLocalStatePort
	embeddings RecoveryEmbeddingEnsurer
}

// NewRecoveryService wires the local evidence, durable publication, local
// checkpoint, and post-publication enrichment boundaries.
func NewRecoveryService(current RecoveryCurrentByteSource, publisher RecoveryPublisher, state RecoveryLocalStatePort, embeddings RecoveryEmbeddingEnsurer) *RecoveryService {
	return &RecoveryService{
		current:    current,
		publisher:  publisher,
		state:      state,
		embeddings: embeddings,
	}
}

// Recover scans current bytes exactly once after binding the local dirty
// high-water, then acknowledges that high-water only after durable publication
// and idempotent enrichment succeed.
func (service *RecoveryService) Recover(ctx context.Context, request RecoveryRequest) (RecoveryResult, error) {
	if service == nil || semanticNil(service.current) || semanticNil(service.publisher) || semanticNil(service.state) || semanticNil(service.embeddings) {
		return RecoveryResult{}, fmt.Errorf("uci recovery: service is not configured")
	}
	if semanticNil(ctx) {
		return RecoveryResult{}, fmt.Errorf("uci recovery: nil context")
	}
	if err := ctx.Err(); err != nil {
		return RecoveryResult{}, err
	}

	canonicalRequest, err := canonicalRecoveryRequest(request)
	if err != nil {
		return RecoveryResult{}, err
	}

	local, found, err := service.state.Snapshot(ctx, canonicalRequest.Scope.CheckoutID)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("uci recovery: load local state: %w", err)
	}
	if !found {
		return RecoveryResult{}, fmt.Errorf("uci recovery: local checkout state is missing")
	}
	if err := validateRecoveryLocalState(local, canonicalRequest.Scope); err != nil {
		return RecoveryResult{}, err
	}
	observedHighWater := local.DirtySequence

	current, err := service.current.ScanCurrent(ctx, canonicalRequest.Root)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("uci recovery: scan current bytes: %w", err)
	}
	canonicalCurrent, currentParts, memberships, err := canonicalRecoveryCurrent(current, observedHighWater)
	if err != nil {
		return RecoveryResult{}, err
	}
	if err := validateRecoveryCandidateSources(canonicalCurrent.EmbeddingCandidates, currentParts, memberships); err != nil {
		return RecoveryResult{}, err
	}

	published, err := service.publisher.Reconcile(ctx, canonicalRequest.Caller, ReconcileRequest{
		BuildKey:       canonicalRequest.BuildKey,
		Scope:          canonicalRequest.Scope,
		ProfileID:      canonicalRequest.ProfileID,
		ExpectedParent: cloneReconcileContextRef(canonicalRequest.ExpectedParent),
		Scan:           canonicalCurrent.Scan,
		Parts:          canonicalCurrent.Parts,
	})
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("uci recovery: reconcile current bytes: %w", err)
	}

	view := cloneReconcilePublishedView(published.View)
	if err := validateRecoveryPublishedView(view, canonicalRequest.Scope, canonicalRequest.ProfileID, observedHighWater); err != nil {
		return RecoveryResult{}, err
	}
	candidates, err := bindRecoveryEmbeddingCandidates(canonicalCurrent.EmbeddingCandidates, view.Context)
	if err != nil {
		return RecoveryResult{}, err
	}

	authorized := newAuthorizedContext(view.Context)
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return RecoveryResult{}, err
		}
		if err := service.embeddings.EnsureCandidateEmbedding(ctx, authorized, candidate); err != nil {
			return RecoveryResult{}, fmt.Errorf("uci recovery: ensure candidate embedding: %w", err)
		}
	}

	if err := ctx.Err(); err != nil {
		return RecoveryResult{}, err
	}
	if err := service.state.MarkReconciled(ctx, canonicalRequest.Scope.CheckoutID, view.AcceptedFSSeq); err != nil {
		return RecoveryResult{}, fmt.Errorf("uci recovery: acknowledge durable view: %w", err)
	}

	return RecoveryResult{View: cloneReconcilePublishedView(view)}, nil
}

func canonicalRecoveryRequest(request RecoveryRequest) (RecoveryRequest, error) {
	canonical := cloneRecoveryRequest(request)
	if !validIndexCaller(canonical.Caller) {
		return RecoveryRequest{}, fmt.Errorf("uci recovery: invalid caller")
	}
	if _, err := scannerAuthorizedRoot(canonical.Root.RootPath); err != nil {
		return RecoveryRequest{}, fmt.Errorf("uci recovery: invalid authorized root: %w", err)
	}
	if !validIndexScope(canonical.Scope) {
		return RecoveryRequest{}, fmt.Errorf("uci recovery: invalid scope")
	}
	if !canonicalContextUUID(canonical.ProfileID) {
		return RecoveryRequest{}, fmt.Errorf("uci recovery: invalid profile")
	}
	if !validIndexText(canonical.BuildKey) {
		return RecoveryRequest{}, fmt.Errorf("uci recovery: invalid build key")
	}

	parent, err := canonicalReconcileParent(canonical.ExpectedParent, canonical.Scope, canonical.ProfileID)
	if err != nil {
		return RecoveryRequest{}, fmt.Errorf("uci recovery: invalid expected parent: %w", err)
	}
	canonical.ExpectedParent = parent
	return canonical, nil
}

func validateRecoveryLocalState(local RecoveryLocalState, scope IndexScope) error {
	if local.CheckoutID != scope.CheckoutID {
		return fmt.Errorf("uci recovery: local checkout does not match request")
	}
	if local.IncarnationID != scope.IncarnationID {
		return fmt.Errorf("uci recovery: local incarnation does not match request")
	}
	if local.DirtySequence < 0 || local.LastReconciledSequence < 0 || local.LastReconciledSequence > local.DirtySequence {
		return fmt.Errorf("uci recovery: local sequence state is invalid")
	}
	if !local.RescanRequired {
		return fmt.Errorf("uci recovery: local state does not require recovery")
	}
	return nil
}

func canonicalRecoveryCurrent(current RecoveryCurrentBytes, observedHighWater int64) (RecoveryCurrentBytes, []IndexPart, []IndexMembership, error) {
	if observedHighWater < 0 {
		return RecoveryCurrentBytes{}, nil, nil, fmt.Errorf("uci recovery: observed high-water is invalid")
	}

	canonical := cloneRecoveryCurrentBytes(current)
	if canonical.Scan.Observation.ObservedFSSeq != 0 && canonical.Scan.Observation.ObservedFSSeq != observedHighWater {
		return RecoveryCurrentBytes{}, nil, nil, fmt.Errorf("uci recovery: scan observation does not match local high-water")
	}
	canonical.Scan.Observation.ObservedFSSeq = observedHighWater

	scan, err := canonicalReconcileScan(canonical.Scan)
	if err != nil {
		return RecoveryCurrentBytes{}, nil, nil, fmt.Errorf("uci recovery: current scan is not complete: %w", err)
	}
	parts, _, memberships, _, _, err := canonicalReconcileParts(canonical.Parts, scan)
	if err != nil {
		return RecoveryCurrentBytes{}, nil, nil, fmt.Errorf("uci recovery: current publication material is invalid: %w", err)
	}
	return canonical, parts, memberships, nil
}

func validateRecoveryCandidateSources(candidates []QueryCandidate, parts []IndexPart, memberships []IndexMembership) error {
	artifacts := make(map[string]IndexArtifactProof)
	for _, part := range parts {
		for _, artifact := range part.Artifacts {
			artifacts[artifact.ArtifactID] = artifact
		}
	}
	membershipsByPath := make(map[string]IndexMembership, len(memberships))
	for _, membership := range memberships {
		membershipsByPath[membership.PathKey] = membership
	}

	for index, candidate := range candidates {
		artifact, found := artifacts[candidate.Proof.ArtifactID]
		if !found || artifact != candidate.Proof {
			return fmt.Errorf("uci recovery: embedding candidate %d is not an exact current artifact", index)
		}
		membership, found := membershipsByPath[candidate.RelativePath]
		if !found || membership.State != IndexFilePresent || membership.ArtifactID == nil || *membership.ArtifactID != candidate.Proof.ArtifactID {
			return fmt.Errorf("uci recovery: embedding candidate %d is outside the current manifest", index)
		}
	}
	return nil
}

func bindRecoveryEmbeddingCandidates(candidates []QueryCandidate, view ContextRef) ([]QueryCandidate, error) {
	bound := make([]QueryCandidate, 0, len(candidates))
	for index, candidate := range candidates {
		candidate = cloneRecoveryCandidate(candidate)
		candidate.Context = view.clone()
		if !validQueryCandidate(candidate) {
			return nil, fmt.Errorf("uci recovery: embedding candidate %d is invalid", index)
		}
		bound = append(bound, candidate)
	}
	return bound, nil
}

func validateRecoveryPublishedView(view IndexPublishedView, scope IndexScope, profileID string, observedHighWater int64) error {
	if !canonicalContextUUID(view.BuildID) ||
		!view.Context.valid() ||
		view.Context.SourceID != scope.SourceID ||
		view.Context.CheckoutID != scope.CheckoutID ||
		view.Context.AnalysisProfileID != profileID ||
		!isIndexDigest(view.ManifestDigest) ||
		view.AcceptedFSSeq != observedHighWater ||
		view.PublishedAt.IsZero() {
		return fmt.Errorf("uci recovery: publisher returned an invalid durable view")
	}
	return nil
}

func cloneRecoveryRequest(request RecoveryRequest) RecoveryRequest {
	clone := request
	clone.ExpectedParent = cloneReconcileContextRef(request.ExpectedParent)
	return clone
}

func cloneRecoveryCurrentBytes(current RecoveryCurrentBytes) RecoveryCurrentBytes {
	clone := RecoveryCurrentBytes{
		Scan: cloneRecoveryScannerResult(current.Scan),
	}
	if current.Parts != nil {
		clone.Parts = make([]IndexPart, len(current.Parts))
		for index, part := range current.Parts {
			clone.Parts[index] = cloneReconcilePart(part)
		}
	}
	if current.EmbeddingCandidates != nil {
		clone.EmbeddingCandidates = make([]QueryCandidate, len(current.EmbeddingCandidates))
		for index, candidate := range current.EmbeddingCandidates {
			clone.EmbeddingCandidates[index] = cloneRecoveryCandidate(candidate)
		}
	}
	return clone
}

func cloneRecoveryScannerResult(scan ScannerResult) ScannerResult {
	clone := scan
	clone.Observation = cloneReconcileObservation(scan.Observation)
	if scan.Files != nil {
		clone.Files = make([]ScannerFile, len(scan.Files))
		for index, file := range scan.Files {
			clone.Files[index] = file
			if file.Body != nil {
				clone.Files[index].Body = append([]byte(nil), file.Body...)
			}
		}
	}
	return clone
}

func cloneRecoveryCandidate(candidate QueryCandidate) QueryCandidate {
	clone := candidate
	clone.Context = candidate.Context.clone()
	return clone
}
