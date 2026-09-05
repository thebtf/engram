package uci

import (
	"context"
	"fmt"
	"time"
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

// RecoveryUpdateOutcome classifies one retained recovery attempt without
// allowing an unmeasured attempt to stand in for a healthy update sample.
type RecoveryUpdateOutcome string

const (
	RecoveryUpdateOutcomeHealthy     RecoveryUpdateOutcome = "healthy"
	RecoveryUpdateOutcomeDegraded    RecoveryUpdateOutcome = "degraded"
	RecoveryUpdateOutcomeUnavailable RecoveryUpdateOutcome = "unavailable"
	RecoveryUpdateOutcomeFailed      RecoveryUpdateOutcome = "failed"
)

// RecoveryUpdateFailureStage identifies the last stage reached by a
// non-healthy recovery accounting record. It deliberately carries no raw
// error text, paths, or source material.
type RecoveryUpdateFailureStage string

const (
	RecoveryUpdateFailureNone       RecoveryUpdateFailureStage = ""
	RecoveryUpdateFailureScan       RecoveryUpdateFailureStage = "scan"
	RecoveryUpdateFailureStructural RecoveryUpdateFailureStage = "structural_fts"
	RecoveryUpdateFailureEmbedding  RecoveryUpdateFailureStage = "embedding"
	RecoveryUpdateFailureLocalACK   RecoveryUpdateFailureStage = "local_ack"
	RecoveryUpdateFailureAccounting RecoveryUpdateFailureStage = "accounting"
)

// RecoveryUpdateTiming is an actual stage interval. Measured is false only
// when that stage was not reached; a zero/default interval is never a healthy
// latency sample.
type RecoveryUpdateTiming struct {
	Measured    bool          `json:"measured"`
	StartedAt   time.Time     `json:"started_at"`
	CompletedAt time.Time     `json:"completed_at"`
	Latency     time.Duration `json:"latency"`
}

// RecoveryProviderCallCounters is a before/after provider counter snapshot.
// Measured distinguishes an unavailable counter from an observed zero.
type RecoveryProviderCallCounters struct {
	Measured bool  `json:"measured"`
	Before   int64 `json:"before"`
	After    int64 `json:"after"`
}

// RecoveryUpdateAccounting is the runtime-owned, source-body-free portion of
// one recovery attempt. Candidate/environment identity, changed-file bounds,
// and warmth intentionally belong to the external recorder that joins this
// record with its exact measurement environment.
type RecoveryUpdateAccounting struct {
	Scope                   IndexScope                   `json:"scope"`
	ProfileID               string                       `json:"profile_id"`
	ObservedFSSeq           int64                        `json:"observed_fs_seq"`
	ScanOutcome             IndexScanOutcome             `json:"scan_outcome"`
	Coverage                IndexCoverage                `json:"coverage"`
	Outcome                 RecoveryUpdateOutcome        `json:"outcome"`
	FailureStage            RecoveryUpdateFailureStage   `json:"failure_stage,omitempty"`
	View                    *IndexPublishedView          `json:"view,omitempty"`
	Scan                    RecoveryUpdateTiming         `json:"scan"`
	StructuralFTS           RecoveryUpdateTiming         `json:"structural_fts"`
	EmbeddingReadiness      RecoveryUpdateTiming         `json:"embedding_readiness"`
	LocalACK                RecoveryUpdateTiming         `json:"local_ack"`
	ProviderCalls           RecoveryProviderCallCounters `json:"provider_calls"`
	EmbeddingCandidateCount int                          `json:"embedding_candidate_count"`
	EmbeddedCandidateCount  int                          `json:"embedded_candidate_count"`
}

// RecoveryUpdateRecorder is the optional application-owned observability
// boundary. ProviderCallCount must return measured=false when the provider
// counter cannot be observed; callers must not substitute a default zero.
// RecordRecoveryUpdate receives only a validated immutable copy and cannot
// change recovery success, ordering, or acknowledgement semantics.
type RecoveryUpdateRecorder interface {
	ProviderCallCount() (count int64, measured bool)
	RecordRecoveryUpdate(RecoveryUpdateAccounting)
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
	updates    RecoveryUpdateRecorder
	now        func() time.Time
}

// NewRecoveryService wires the local evidence, durable publication, local
// checkpoint, post-publication enrichment, and optional validated update
// accounting boundary.
func NewRecoveryService(current RecoveryCurrentByteSource, publisher RecoveryPublisher, state RecoveryLocalStatePort, embeddings RecoveryEmbeddingEnsurer, updates RecoveryUpdateRecorder) *RecoveryService {
	return &RecoveryService{
		current:    current,
		publisher:  publisher,
		state:      state,
		embeddings: embeddings,
		updates:    updates,
		now:        time.Now,
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

	update := service.beginRecoveryUpdate(canonicalRequest.Scope, canonicalRequest.ProfileID, observedHighWater)
	if update != nil {
		defer service.recordRecoveryUpdate(update)
	}

	current, err := service.current.ScanCurrent(ctx, canonicalRequest.Root)
	if update != nil {
		update.Scan = completeRecoveryUpdateTiming(update.Scan.StartedAt, service.recoveryUpdateNow())
		if isIndexScanOutcome(current.Scan.Census.Outcome) {
			update.ScanOutcome = current.Scan.Census.Outcome
			update.Coverage = canonicalRecoveryUpdateCoverage(current.Scan.Coverage)
		} else {
			update.ScanOutcome = IndexScanFailed
			update.Coverage = unavailableRecoveryUpdateCoverage()
		}
	}
	if err != nil {
		markRecoveryUpdateFailure(update, RecoveryUpdateOutcomeFailed, RecoveryUpdateFailureScan)
		return RecoveryResult{}, fmt.Errorf("uci recovery: scan current bytes: %w", err)
	}
	canonicalCurrent, currentParts, memberships, err := canonicalRecoveryCurrent(current, observedHighWater)
	if err != nil {
		markRecoveryUpdateFailure(update, RecoveryUpdateOutcomeFailed, RecoveryUpdateFailureScan)
		return RecoveryResult{}, err
	}
	if update != nil {
		update.ScanOutcome = canonicalCurrent.Scan.Census.Outcome
		update.Coverage = canonicalRecoveryUpdateCoverage(canonicalCurrent.Scan.Coverage)
	}
	if err := validateRecoveryCandidateSources(canonicalCurrent.EmbeddingCandidates, currentParts, memberships); err != nil {
		markRecoveryUpdateFailure(update, RecoveryUpdateOutcomeFailed, RecoveryUpdateFailureScan)
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
		markRecoveryUpdateFailure(update, RecoveryUpdateOutcomeUnavailable, RecoveryUpdateFailureStructural)
		return RecoveryResult{}, fmt.Errorf("uci recovery: reconcile current bytes: %w", err)
	}

	view := cloneReconcilePublishedView(published.View)
	if err := validateRecoveryPublishedView(view, canonicalRequest.Scope, canonicalRequest.ProfileID, observedHighWater); err != nil {
		markRecoveryUpdateFailure(update, RecoveryUpdateOutcomeFailed, RecoveryUpdateFailureStructural)
		return RecoveryResult{}, err
	}
	if update != nil {
		accountedView := cloneReconcilePublishedView(view)
		update.View = &accountedView
		update.StructuralFTS = completeRecoveryUpdateTiming(update.Scan.StartedAt, service.recoveryUpdateNow())
		update.Outcome = RecoveryUpdateOutcomeDegraded
		update.FailureStage = RecoveryUpdateFailureEmbedding
	}
	candidates, err := bindRecoveryEmbeddingCandidates(canonicalCurrent.EmbeddingCandidates, view.Context)
	if err != nil {
		markRecoveryUpdateFailure(update, RecoveryUpdateOutcomeDegraded, RecoveryUpdateFailureEmbedding)
		return RecoveryResult{}, err
	}
	if update != nil {
		update.EmbeddingCandidateCount = len(candidates)
	}

	authorized := newAuthorizedContext(view.Context)
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			markRecoveryUpdateFailure(update, RecoveryUpdateOutcomeUnavailable, RecoveryUpdateFailureEmbedding)
			return RecoveryResult{}, err
		}
		if err := service.embeddings.EnsureCandidateEmbedding(ctx, authorized, candidate); err != nil {
			markRecoveryUpdateFailure(update, RecoveryUpdateOutcomeDegraded, RecoveryUpdateFailureEmbedding)
			return RecoveryResult{}, fmt.Errorf("uci recovery: ensure candidate embedding: %w", err)
		}
		if update != nil {
			update.EmbeddedCandidateCount++
		}
	}
	if update != nil {
		update.EmbeddingReadiness = completeRecoveryUpdateTiming(update.Scan.StartedAt, service.recoveryUpdateNow())
		update.Outcome = RecoveryUpdateOutcomeUnavailable
		update.FailureStage = RecoveryUpdateFailureLocalACK
	}

	if err := ctx.Err(); err != nil {
		markRecoveryUpdateFailure(update, RecoveryUpdateOutcomeUnavailable, RecoveryUpdateFailureLocalACK)
		return RecoveryResult{}, err
	}
	if err := service.state.MarkReconciled(ctx, canonicalRequest.Scope.CheckoutID, view.AcceptedFSSeq); err != nil {
		markRecoveryUpdateFailure(update, RecoveryUpdateOutcomeUnavailable, RecoveryUpdateFailureLocalACK)
		return RecoveryResult{}, fmt.Errorf("uci recovery: acknowledge durable view: %w", err)
	}
	if update != nil {
		update.LocalACK = completeRecoveryUpdateTiming(update.Scan.StartedAt, service.recoveryUpdateNow())
		update.Outcome = RecoveryUpdateOutcomeHealthy
		update.FailureStage = RecoveryUpdateFailureNone
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

func (service *RecoveryService) beginRecoveryUpdate(scope IndexScope, profileID string, observedFSSeq int64) *RecoveryUpdateAccounting {
	if service == nil || semanticNil(service.updates) {
		return nil
	}

	startedAt := service.recoveryUpdateNow()
	update := &RecoveryUpdateAccounting{
		Scope:         scope,
		ProfileID:     profileID,
		ObservedFSSeq: observedFSSeq,
		ScanOutcome:   IndexScanFailed,
		Coverage:      unavailableRecoveryUpdateCoverage(),
		Outcome:       RecoveryUpdateOutcomeFailed,
		FailureStage:  RecoveryUpdateFailureScan,
		Scan: RecoveryUpdateTiming{
			Measured:  true,
			StartedAt: startedAt,
		},
	}
	if calls, measured := service.updates.ProviderCallCount(); measured {
		update.ProviderCalls = RecoveryProviderCallCounters{Measured: true, Before: calls, After: calls}
	}
	return update
}

func (service *RecoveryService) recordRecoveryUpdate(update *RecoveryUpdateAccounting) {
	if service == nil || update == nil || semanticNil(service.updates) {
		return
	}

	if calls, measured := service.updates.ProviderCallCount(); update.ProviderCalls.Measured && measured {
		update.ProviderCalls.After = calls
	} else {
		update.ProviderCalls = RecoveryProviderCallCounters{}
	}
	if update.Outcome == RecoveryUpdateOutcomeHealthy && !update.ProviderCalls.Measured {
		update.Outcome = RecoveryUpdateOutcomeUnavailable
		update.FailureStage = RecoveryUpdateFailureAccounting
	}
	if err := ValidateRecoveryUpdateAccounting(*update); err != nil {
		return
	}
	service.updates.RecordRecoveryUpdate(cloneRecoveryUpdateAccounting(*update))
}

func (service *RecoveryService) recoveryUpdateNow() time.Time {
	if service == nil || service.now == nil {
		return time.Now().UTC()
	}
	return service.now().UTC()
}

// ValidateRecoveryUpdateAccounting rejects unresolved identity, stage, timing,
// counter, and durable-view state before a recorder can expose the accounting.
func ValidateRecoveryUpdateAccounting(update RecoveryUpdateAccounting) error {
	if !validIndexScope(update.Scope) || !canonicalContextUUID(update.ProfileID) {
		return fmt.Errorf("uci recovery update: identity is invalid")
	}
	if update.ObservedFSSeq < 0 || !isIndexScanOutcome(update.ScanOutcome) || !validRecoveryUpdateCoverage(update.Coverage) {
		return fmt.Errorf("uci recovery update: observation state is invalid")
	}
	if !validRecoveryUpdateOutcome(update.Outcome) || !validRecoveryUpdateFailureStage(update.FailureStage) {
		return fmt.Errorf("uci recovery update: outcome is invalid")
	}
	if err := validateRecoveryUpdateTiming(update.Scan, true); err != nil {
		return fmt.Errorf("uci recovery update: scan timing: %w", err)
	}
	for _, stage := range []struct {
		name   string
		timing RecoveryUpdateTiming
	}{
		{name: "structural/FTS", timing: update.StructuralFTS},
		{name: "embedding readiness", timing: update.EmbeddingReadiness},
		{name: "local acknowledgement", timing: update.LocalACK},
	} {
		if err := validateRecoveryUpdateTiming(stage.timing, false); err != nil {
			return fmt.Errorf("uci recovery update: %s timing: %w", stage.name, err)
		}
	}
	if update.StructuralFTS.Measured {
		if update.View == nil || !recoveryUpdateTimingFollows(update.StructuralFTS, update.Scan) {
			return fmt.Errorf("uci recovery update: structural/FTS readiness is not bound to scan and View")
		}
	}
	if update.EmbeddingReadiness.Measured && (!update.StructuralFTS.Measured || !recoveryUpdateTimingFollows(update.EmbeddingReadiness, update.StructuralFTS)) {
		return fmt.Errorf("uci recovery update: embedding readiness precedes structural/FTS readiness")
	}
	if update.LocalACK.Measured && (!update.EmbeddingReadiness.Measured || !recoveryUpdateTimingFollows(update.LocalACK, update.EmbeddingReadiness)) {
		return fmt.Errorf("uci recovery update: local acknowledgement precedes embedding readiness")
	}
	if update.View != nil && !validRecoveryUpdateView(*update.View, update.Scope, update.ProfileID, update.ObservedFSSeq) {
		return fmt.Errorf("uci recovery update: View is not the observed durable publication")
	}
	if update.ProviderCalls.Measured {
		if update.ProviderCalls.Before < 0 || update.ProviderCalls.After < update.ProviderCalls.Before {
			return fmt.Errorf("uci recovery update: provider-call counters are invalid")
		}
	} else if update.ProviderCalls.Before != 0 || update.ProviderCalls.After != 0 {
		return fmt.Errorf("uci recovery update: unresolved provider-call counters carry values")
	}
	if update.EmbeddingCandidateCount < 0 || update.EmbeddedCandidateCount < 0 || update.EmbeddedCandidateCount > update.EmbeddingCandidateCount {
		return fmt.Errorf("uci recovery update: embedding candidate counters are invalid")
	}

	switch update.Outcome {
	case RecoveryUpdateOutcomeHealthy:
		if update.FailureStage != RecoveryUpdateFailureNone || update.ScanOutcome != IndexScanComplete || update.Coverage.Structural != IndexCoverageComplete || update.View == nil || !update.StructuralFTS.Measured || !update.EmbeddingReadiness.Measured || !update.LocalACK.Measured || !update.ProviderCalls.Measured || update.EmbeddedCandidateCount != update.EmbeddingCandidateCount || update.StructuralFTS.Latency <= 0 || update.EmbeddingReadiness.Latency <= 0 {
			return fmt.Errorf("uci recovery update: healthy outcome lacks complete measured readiness")
		}
	case RecoveryUpdateOutcomeDegraded:
		if update.FailureStage == RecoveryUpdateFailureNone || update.View == nil || !update.StructuralFTS.Measured {
			return fmt.Errorf("uci recovery update: degraded outcome lacks retained structural publication")
		}
	case RecoveryUpdateOutcomeUnavailable, RecoveryUpdateOutcomeFailed:
		if update.FailureStage == RecoveryUpdateFailureNone {
			return fmt.Errorf("uci recovery update: non-healthy outcome lacks a failure stage")
		}
	}
	return nil
}

func completeRecoveryUpdateTiming(startedAt, completedAt time.Time) RecoveryUpdateTiming {
	return RecoveryUpdateTiming{
		Measured:    true,
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		Latency:     completedAt.Sub(startedAt),
	}
}

func markRecoveryUpdateFailure(update *RecoveryUpdateAccounting, outcome RecoveryUpdateOutcome, stage RecoveryUpdateFailureStage) {
	if update == nil {
		return
	}
	update.Outcome = outcome
	update.FailureStage = stage
}

func unavailableRecoveryUpdateCoverage() IndexCoverage {
	return IndexCoverage{
		Structural: IndexCoverageUnavailable,
		Lexical:    IndexCoverageUnavailable,
		Vector:     IndexCoverageUnavailable,
	}
}

func canonicalRecoveryUpdateCoverage(coverage IndexCoverage) IndexCoverage {
	if !isIndexCoverageState(coverage.Structural) {
		coverage.Structural = IndexCoverageUnavailable
	}
	if !isIndexCoverageState(coverage.Lexical) {
		coverage.Lexical = IndexCoverageUnavailable
	}
	if !isIndexCoverageState(coverage.Vector) {
		coverage.Vector = IndexCoverageUnavailable
	}
	return coverage
}

func validRecoveryUpdateCoverage(coverage IndexCoverage) bool {
	return isIndexCoverageState(coverage.Structural) && isIndexCoverageState(coverage.Lexical) && isIndexCoverageState(coverage.Vector)
}

func validRecoveryUpdateOutcome(outcome RecoveryUpdateOutcome) bool {
	switch outcome {
	case RecoveryUpdateOutcomeHealthy, RecoveryUpdateOutcomeDegraded, RecoveryUpdateOutcomeUnavailable, RecoveryUpdateOutcomeFailed:
		return true
	default:
		return false
	}
}

func validRecoveryUpdateFailureStage(stage RecoveryUpdateFailureStage) bool {
	switch stage {
	case RecoveryUpdateFailureNone, RecoveryUpdateFailureScan, RecoveryUpdateFailureStructural, RecoveryUpdateFailureEmbedding, RecoveryUpdateFailureLocalACK, RecoveryUpdateFailureAccounting:
		return true
	default:
		return false
	}
}

func validateRecoveryUpdateTiming(timing RecoveryUpdateTiming, required bool) error {
	if !timing.Measured {
		if required || !timing.StartedAt.IsZero() || !timing.CompletedAt.IsZero() || timing.Latency != 0 {
			return fmt.Errorf("unmeasured stage carries timing")
		}
		return nil
	}
	if timing.StartedAt.IsZero() || timing.CompletedAt.IsZero() || timing.CompletedAt.Before(timing.StartedAt) || timing.Latency < 0 || timing.Latency != timing.CompletedAt.Sub(timing.StartedAt) {
		return fmt.Errorf("interval is not an actual non-negative timestamp pair")
	}
	return nil
}

func recoveryUpdateTimingFollows(next, previous RecoveryUpdateTiming) bool {
	return next.StartedAt.Equal(previous.StartedAt) && !next.CompletedAt.Before(previous.CompletedAt)
}

func validRecoveryUpdateView(view IndexPublishedView, scope IndexScope, profileID string, observedFSSeq int64) bool {
	return canonicalContextUUID(view.BuildID) &&
		view.Context.valid() &&
		view.Context.SourceID == scope.SourceID &&
		view.Context.CheckoutID == scope.CheckoutID &&
		view.Context.AnalysisProfileID == profileID &&
		isIndexDigest(view.ManifestDigest) &&
		view.AcceptedFSSeq == observedFSSeq &&
		!view.PublishedAt.IsZero()
}

func cloneRecoveryUpdateAccounting(update RecoveryUpdateAccounting) RecoveryUpdateAccounting {
	clone := update
	if update.View != nil {
		view := cloneReconcilePublishedView(*update.View)
		clone.View = &view
	}
	return clone
}
