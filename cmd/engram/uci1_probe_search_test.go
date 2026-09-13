package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/uci"
)

const (
	uci1SearchInstalledProbeVersion = "uci1-search-installed/v1"
	uci1SearchCleanupTimeout        = 30 * time.Second
	uci1SearchCacheWait             = 10 * time.Second
)

// uci1ProbeSearchInstalled observes only installed stdio-client behavior. It
// leaves IDs without a live vector-profile denominator absent from the evidence
// map rather than converting a zero-count database observation into proof.
func uci1ProbeSearchInstalled(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (map[string]uciInstalledAcceptanceScenarioEvidence, error) {
	state, err := uci1SearchPrepareProbeState(ctx, runtime)
	if err != nil {
		return nil, err
	}
	if err := uci1SearchRunObservations(ctx, state); err != nil {
		return nil, err
	}
	return state.evidence, nil
}

func uci1SearchPrepareProbeState(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (*uci1SearchProbeState, error) {
	primaryClient, primarySelection, primaryPublication, linkedClient, linkedSelection, linkedPublication, err := uci1SearchInstalledRuntime(runtime)
	if err != nil {
		return nil, err
	}
	primaryPath := filepath.Join(runtime.Worktrees.primaryRoot, filepath.FromSlash(runtime.Request.Fixture.RelativePath))
	linkedPath := filepath.Join(runtime.Worktrees.linkedRoot, filepath.FromSlash(runtime.Request.Fixture.RelativePath))
	primarySource, err := os.ReadFile(primaryPath)
	if err != nil || len(primarySource) == 0 {
		return nil, errors.New("read installed search primary source")
	}
	linkedSource, err := os.ReadFile(linkedPath)
	if err != nil || len(linkedSource) == 0 {
		return nil, errors.New("read installed search linked source")
	}
	primaryStatus, err := uci1SearchInstalledStatusFor(ctx, primaryClient, primarySelection)
	if err != nil {
		return nil, err
	}
	linkedStatus, err := uci1SearchInstalledStatusFor(ctx, linkedClient, linkedSelection)
	if err != nil {
		return nil, err
	}
	if !uci1SearchStatusMatchesPublication(primaryStatus, primaryPublication) || !uci1SearchStatusMatchesPublication(linkedStatus, linkedPublication) {
		return nil, errors.New("installed search status escaped its selected publication")
	}
	return &uci1SearchProbeState{
		runtime:  runtime,
		primary:  uci1SearchCheckout{client: primaryClient, selection: primarySelection, publication: primaryPublication, status: primaryStatus, source: primarySource, path: primaryPath},
		linked:   uci1SearchCheckout{client: linkedClient, selection: linkedSelection, publication: linkedPublication, status: linkedStatus, source: linkedSource, path: linkedPath},
		evidence: make(map[string]uciInstalledAcceptanceScenarioEvidence),
	}, nil
}

func uci1SearchRunObservations(ctx context.Context, state *uci1SearchProbeState) error {
	unchanged, observed, err := uci1SearchObserveUnchangedCheckout(ctx, state.runtime, state.primary, state.linked)
	if err := state.record("U24", unchanged, observed, err); err != nil {
		return err
	}
	if err := state.refresh(ctx, &state.primary, "installed search primary status has no current publication"); err != nil {
		return err
	}
	saved, observed, err := uci1SearchObserveSavedBytes(ctx, state.runtime, state.primary)
	if err := state.record("U17", saved, observed, err); err != nil {
		return err
	}
	if err := state.refresh(ctx, &state.primary, "installed search primary status lost its current publication"); err != nil {
		return err
	}
	invisible, observed, err := uci1SearchObserveInvisibleCandidates(ctx, state.runtime, state.primary)
	if err := state.record("U18", invisible, observed, err); err != nil {
		return err
	}
	if err := state.refresh(ctx, &state.linked, "installed search linked status has no current publication"); err != nil {
		return err
	}
	profiles, observed, err := uci1SearchObserveSameDimensionProfiles(ctx, state.runtime, state.primary, state.linked)
	return state.record("U38", profiles, observed, err)
}

func (state *uci1SearchProbeState) refresh(ctx context.Context, checkout *uci1SearchCheckout, missingPublicationMessage string) error {
	status, err := uci1SearchInstalledStatusFor(ctx, checkout.client, checkout.selection)
	if err != nil {
		return err
	}
	publication, ok := uci1SearchPublicationFromStatus(status)
	if !ok {
		return errors.New(missingPublicationMessage)
	}
	checkout.status = status
	checkout.publication = publication
	return nil
}

func (state *uci1SearchProbeState) record(scenarioID string, observation uci1SearchObservation, observed bool, err error) error {
	if err != nil {
		return err
	}
	if observed {
		state.evidence[scenarioID] = uci1SearchInstalledEvidence(scenarioID, observation.seed())
	}
	return nil
}

type uci1SearchInstalledStatus struct {
	Status  string `json:"status"`
	RunID   string `json:"run_id"`
	Error   string `json:"error"`
	Context struct {
		SourceID   string `json:"source_id"`
		CheckoutID string `json:"checkout_id"`
		ViewID     string `json:"view_id"`
		ProfileID  string `json:"profile_id"`
		Generation int64  `json:"generation"`
	} `json:"context"`
	Embedding struct {
		EmbeddingProfileID *string `json:"embedding_profile_id"`
		Coverage           string  `json:"coverage"`
		TotalCandidates    uint64  `json:"total_candidates"`
		ReadyCandidates    uint64  `json:"ready_candidates"`
		PendingJobs        uint64  `json:"pending_jobs"`
	} `json:"embedding"`
}

type uci1SearchCheckout struct {
	client      *uciInstalledAcceptanceMCPClient
	selection   uciInstalledAcceptanceSelection
	publication uciInstalledAcceptancePublication
	status      uci1SearchInstalledStatus
	source      []byte
	path        string
}

type uci1SearchProbeState struct {
	runtime  uciInstalledAcceptanceScenarioRuntime
	primary  uci1SearchCheckout
	linked   uci1SearchCheckout
	evidence map[string]uciInstalledAcceptanceScenarioEvidence
}

type uci1SearchObservation interface {
	seed() []string
}

type uci1SearchCacheCounts struct {
	Blobs           int64
	ParseArtifacts  int64
	Embeddings      int64
	ChunkEmbeddings int64
}

type uci1SearchSavedBytesObservation struct {
	OldReady uint64
	NewReady uint64
	NewTotal uint64
	Coverage string
}

func (observation uci1SearchSavedBytesObservation) seed() []string {
	return []string{
		"saved_bytes",
		strconv.FormatUint(observation.OldReady, 10),
		strconv.FormatUint(observation.NewReady, 10),
		strconv.FormatUint(observation.NewTotal, 10),
		observation.Coverage,
	}
}

type uci1SearchUnchangedCheckoutObservation struct {
	Before uci1SearchCacheCounts
	After  uci1SearchCacheCounts
	Ready  uint64
}

func (observation uci1SearchUnchangedCheckoutObservation) seed() []string {
	return []string{
		"unchanged_checkout",
		strconv.FormatInt(observation.Before.Blobs, 10),
		strconv.FormatInt(observation.Before.ParseArtifacts, 10),
		strconv.FormatInt(observation.Before.Embeddings, 10),
		strconv.FormatInt(observation.Before.ChunkEmbeddings, 10),
		strconv.FormatInt(observation.After.Blobs, 10),
		strconv.FormatInt(observation.After.ParseArtifacts, 10),
		strconv.FormatInt(observation.After.Embeddings, 10),
		strconv.FormatInt(observation.After.ChunkEmbeddings, 10),
		strconv.FormatUint(observation.Ready, 10),
	}
}

type uci1SearchInvisibleCandidatesObservation struct {
	Visible   int64
	Invisible int64
	Status    string
}

func (observation uci1SearchInvisibleCandidatesObservation) seed() []string {
	return []string{
		"invisible_candidates",
		strconv.FormatInt(observation.Visible, 10),
		strconv.FormatInt(observation.Invisible, 10),
		observation.Status,
	}
}

type uci1SearchProfileSeparationObservation struct {
	Dimension  int
	LeftReady  uint64
	RightReady uint64
}

func (observation uci1SearchProfileSeparationObservation) seed() []string {
	return []string{
		"same_dimension_profile_separation",
		strconv.Itoa(observation.Dimension),
		strconv.FormatUint(observation.LeftReady, 10),
		strconv.FormatUint(observation.RightReady, 10),
	}
}

func uci1SearchInstalledRuntime(runtime uciInstalledAcceptanceScenarioRuntime) (
	*uciInstalledAcceptanceMCPClient,
	uciInstalledAcceptanceSelection,
	uciInstalledAcceptancePublication,
	*uciInstalledAcceptanceMCPClient,
	uciInstalledAcceptanceSelection,
	uciInstalledAcceptancePublication,
	error,
) {
	if runtime.Authority == nil || runtime.Authority.store == nil || runtime.Authority.source == nil || runtime.Authority.source.SourceID == "" || runtime.Worktrees.primaryRoot == "" || runtime.Worktrees.linkedRoot == "" {
		return nil, uciInstalledAcceptanceSelection{}, uciInstalledAcceptancePublication{}, nil, uciInstalledAcceptanceSelection{}, uciInstalledAcceptancePublication{}, errors.New("installed search probe runtime is incomplete")
	}
	primarySelection, primarySelectionFound := runtime.Selections[uciInstalledAcceptanceClientA]
	primaryPublication, primaryPublicationFound := runtime.Publications[uciInstalledAcceptanceClientA]
	linkedSelection, linkedSelectionFound := runtime.Selections[uciInstalledAcceptanceClientB]
	linkedPublication, linkedPublicationFound := runtime.Publications[uciInstalledAcceptanceClientB]
	if runtime.ClientA == nil || runtime.ClientB == nil || !primarySelectionFound || !primaryPublicationFound || !linkedSelectionFound || !linkedPublicationFound || primarySelection.contextHandle == "" || linkedSelection.contextHandle == "" || primaryPublication.runID == "" || linkedPublication.runID == "" {
		return nil, uciInstalledAcceptanceSelection{}, uciInstalledAcceptancePublication{}, nil, uciInstalledAcceptanceSelection{}, uciInstalledAcceptancePublication{}, errors.New("installed search probe selection baseline is incomplete")
	}
	return runtime.ClientA, primarySelection, primaryPublication, runtime.ClientB, linkedSelection, linkedPublication, nil
}

func uci1SearchInstalledStatusFor(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection) (uci1SearchInstalledStatus, error) {
	payload, err := uciInstalledAcceptanceStatusTool(ctx, client, map[string]any{"context_handle": selection.contextHandle})
	if err != nil {
		return uci1SearchInstalledStatus{}, err
	}
	var status uci1SearchInstalledStatus
	if err := json.Unmarshal(payload, &status); err != nil || status.Error != "" || status.Status == "" || status.RunID == "" || status.Context.SourceID == "" || status.Context.CheckoutID == "" || status.Context.ViewID == "" || status.Context.ProfileID == "" || status.Context.Generation < 1 {
		return uci1SearchInstalledStatus{}, errors.New("installed search codebase status is incomplete")
	}
	return status, nil
}

func uci1SearchPublicationFromStatus(status uci1SearchInstalledStatus) (uciInstalledAcceptancePublication, bool) {
	if status.RunID == "" || status.Context.SourceID == "" || status.Context.CheckoutID == "" || status.Context.ViewID == "" || status.Context.ProfileID == "" || status.Context.Generation < 1 {
		return uciInstalledAcceptancePublication{}, false
	}
	return uciInstalledAcceptancePublication{
		sourceID:       status.Context.SourceID,
		checkoutID:     status.Context.CheckoutID,
		viewID:         status.Context.ViewID,
		profileID:      status.Context.ProfileID,
		generation:     status.Context.Generation,
		runID:          status.RunID,
		freshnessState: "observed_current",
	}, true
}

func uci1SearchStatusMatchesPublication(status uci1SearchInstalledStatus, publication uciInstalledAcceptancePublication) bool {
	return status.RunID == publication.runID && status.Context.SourceID == publication.sourceID && status.Context.CheckoutID == publication.checkoutID && status.Context.ViewID == publication.viewID && status.Context.ProfileID == publication.profileID && status.Context.Generation == publication.generation
}

func uci1SearchComplete(status uci1SearchInstalledStatus) bool {
	return status.Status == "idle" && status.Embedding.EmbeddingProfileID != nil && *status.Embedding.EmbeddingProfileID != "" && status.Embedding.Coverage == "complete" && status.Embedding.TotalCandidates > 0 && status.Embedding.ReadyCandidates == status.Embedding.TotalCandidates && status.Embedding.PendingJobs == 0
}

func uci1SearchDegraded(status uci1SearchInstalledStatus, profileID string) bool {
	return status.Embedding.EmbeddingProfileID != nil && *status.Embedding.EmbeddingProfileID == profileID && status.Embedding.Coverage == "partial" && status.Embedding.TotalCandidates > status.Embedding.ReadyCandidates
}

func uci1SearchInstalledEvidence(scenarioID string, values []string) uciInstalledAcceptanceScenarioEvidence {
	seed := append([]string{uci1SearchInstalledProbeVersion, scenarioID}, values...)
	return uciInstalledAcceptanceScenarioEvidence{
		Code:   uciInstalledAcceptanceScenarioCodeObserved,
		Digest: uciInstalledReceiptDigestStrings(seed...),
	}
}

func uci1SearchObserveUnchangedCheckout(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, primaryCheckout, linkedCheckout uci1SearchCheckout) (observation uci1SearchUnchangedCheckoutObservation, observed bool, retErr error) {
	primaryStatus, primarySource := primaryCheckout.status, primaryCheckout.source
	linked, linkedSelection, linkedPublication, linkedStatus, linkedSource, linkedPath := linkedCheckout.client, linkedCheckout.selection, linkedCheckout.publication, linkedCheckout.status, linkedCheckout.source, linkedCheckout.path
	if !uci1SearchComplete(primaryStatus) || !uci1SearchComplete(linkedStatus) || primaryStatus.Embedding.EmbeddingProfileID == nil || linkedStatus.Embedding.EmbeddingProfileID == nil || *primaryStatus.Embedding.EmbeddingProfileID != *linkedStatus.Embedding.EmbeddingProfileID || string(primarySource) == string(linkedSource) {
		return observation, false, nil
	}

	before, err := uci1SearchCacheCountsFor(ctx, runtime.Authority)
	if err != nil || before.Embeddings == 0 || before.ChunkEmbeddings == 0 {
		return observation, false, err
	}
	previous := linkedPublication
	written := false
	defer func() {
		if !written {
			return
		}
		retErr = errors.Join(retErr, uci1SearchRestoreSource(runtime, linked, linkedSelection, previous, linkedPath, linkedSource, runtime.Request.Fixture.LinkedCallee))
	}()

	if err := os.WriteFile(linkedPath, primarySource, 0o600); err != nil {
		return observation, false, errors.New("save unchanged installed search checkout")
	}
	written = true
	publication, err := uciWaitForInstalledAcceptanceWatcherPublication(ctx, linked, linkedSelection, linkedPublication)
	if err != nil {
		return observation, false, err
	}
	previous = publication
	status, complete, err := uci1SearchWaitForCompleteEmbedding(ctx, linked, linkedSelection, publication)
	if err != nil || !complete {
		return observation, false, err
	}
	if status.Embedding.EmbeddingProfileID == nil || *status.Embedding.EmbeddingProfileID != *primaryStatus.Embedding.EmbeddingProfileID {
		return observation, false, nil
	}
	after, err := uci1SearchCacheCountsFor(ctx, runtime.Authority)
	if err != nil {
		return observation, false, err
	}
	if after != before {
		return observation, false, errors.New("installed unchanged checkout duplicated cache work")
	}
	response, err := uci1SearchQuery(ctx, linked, linkedSelection, runtime.Request.Fixture.SharedSymbol, runtime.Request.Fixture.RelativePath)
	if err != nil {
		return observation, false, err
	}
	if !uci1SearchResponseHasCurrentFunction(response, publication, runtime.Request.Fixture.SharedSymbol, runtime.Request.Fixture.RelativePath) {
		return observation, false, errors.New("installed unchanged checkout search omitted its current baseline")
	}
	return uci1SearchUnchangedCheckoutObservation{Before: before, After: after, Ready: status.Embedding.ReadyCandidates}, true, nil
}

func uci1SearchObserveSavedBytes(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, checkout uci1SearchCheckout) (observation uci1SearchSavedBytesObservation, observed bool, retErr error) {
	client, selection, baseline, before, original, path := checkout.client, checkout.selection, checkout.publication, checkout.status, checkout.source, checkout.path
	if !uci1SearchComplete(before) || before.Embedding.EmbeddingProfileID == nil || runtime.Request.Fixture.PrimaryCallee == "" {
		return observation, false, nil
	}
	oldReady, err := uci1SearchReadyCandidatesForView(ctx, runtime.Authority, baseline, *before.Embedding.EmbeddingProfileID)
	if err != nil || oldReady == 0 {
		return observation, false, err
	}
	const replacement = "UCI1SearchSavedCurrent"
	changed := strings.ReplaceAll(string(original), runtime.Request.Fixture.PrimaryCallee, replacement)
	if changed == string(original) || strings.Contains(changed, runtime.Request.Fixture.PrimaryCallee) {
		return observation, false, nil
	}
	previous := baseline
	written := false
	defer func() {
		if !written {
			return
		}
		retErr = errors.Join(retErr, uci1SearchRestoreSource(runtime, client, selection, previous, path, original, runtime.Request.Fixture.PrimaryCallee))
	}()

	if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
		return observation, false, errors.New("save installed search replacement bytes")
	}
	written = true
	publication, err := uciWaitForInstalledAcceptanceWatcherPublication(ctx, client, selection, baseline)
	if err != nil {
		return observation, false, err
	}
	previous = publication
	status, err := uci1SearchInstalledStatusFor(ctx, client, selection)
	if err != nil {
		return observation, false, err
	}
	if !uci1SearchStatusMatchesPublication(status, publication) {
		return observation, false, errors.New("installed saved-byte status escaped its replacement publication")
	}
	response, err := uci1SearchQuery(ctx, client, selection, runtime.Request.Fixture.PrimaryCallee, runtime.Request.Fixture.RelativePath)
	if err != nil {
		return observation, false, err
	}
	if !uci1SearchResponseExcludesOldBody(response, publication, uciInstalledAcceptanceStringDigest(string(original)), runtime.Request.Fixture.PrimaryCallee) {
		return observation, false, errors.New("installed saved-byte search exposed the old body")
	}
	if !uci1SearchDegraded(status, *before.Embedding.EmbeddingProfileID) {
		return observation, false, nil
	}
	return uci1SearchSavedBytesObservation{
		OldReady: before.Embedding.ReadyCandidates,
		NewReady: status.Embedding.ReadyCandidates,
		NewTotal: status.Embedding.TotalCandidates,
		Coverage: status.Embedding.Coverage,
	}, true, nil
}

func uci1SearchObserveInvisibleCandidates(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, checkout uci1SearchCheckout) (uci1SearchInvisibleCandidatesObservation, bool, error) {
	client, selection, publication, status := checkout.client, checkout.selection, checkout.publication, checkout.status
	if !uci1SearchComplete(status) || status.Embedding.EmbeddingProfileID == nil {
		return uci1SearchInvisibleCandidatesObservation{}, false, nil
	}
	visible, err := uci1SearchReadyCandidatesForView(ctx, runtime.Authority, publication, *status.Embedding.EmbeddingProfileID)
	if err != nil || visible == 0 {
		return uci1SearchInvisibleCandidatesObservation{}, false, err
	}
	invisible, err := uci1SearchReadyCandidatesOutsideView(ctx, runtime.Authority, publication, *status.Embedding.EmbeddingProfileID)
	if err != nil {
		return uci1SearchInvisibleCandidatesObservation{}, false, err
	}
	if invisible <= visible {
		return uci1SearchInvisibleCandidatesObservation{}, false, nil
	}
	response, err := uci1SearchQuery(ctx, client, selection, runtime.Request.Fixture.SharedSymbol, runtime.Request.Fixture.RelativePath)
	if err != nil {
		return uci1SearchInvisibleCandidatesObservation{}, false, err
	}
	if !uci1SearchResponseHasCurrentFunction(response, publication, runtime.Request.Fixture.SharedSymbol, runtime.Request.Fixture.RelativePath) {
		return uci1SearchInvisibleCandidatesObservation{}, false, errors.New("installed search returned false empty under invisible candidate pressure")
	}
	return uci1SearchInvisibleCandidatesObservation{Visible: visible, Invisible: invisible, Status: string(response.Status)}, true, nil
}

func uci1SearchObserveSameDimensionProfiles(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, primaryCheckout, linkedCheckout uci1SearchCheckout) (uci1SearchProfileSeparationObservation, bool, error) {
	primary, primarySelection, primaryPublication, primaryStatus := primaryCheckout.client, primaryCheckout.selection, primaryCheckout.publication, primaryCheckout.status
	linked, linkedSelection, linkedPublication, linkedStatus := linkedCheckout.client, linkedCheckout.selection, linkedCheckout.publication, linkedCheckout.status
	if !uci1SearchComplete(primaryStatus) || !uci1SearchComplete(linkedStatus) || primaryStatus.Embedding.EmbeddingProfileID == nil || linkedStatus.Embedding.EmbeddingProfileID == nil || *primaryStatus.Embedding.EmbeddingProfileID == *linkedStatus.Embedding.EmbeddingProfileID {
		return uci1SearchProfileSeparationObservation{}, false, nil
	}
	primaryProfile, linkedProfile, found, err := uci1SearchProfilePair(ctx, runtime.Authority, *primaryStatus.Embedding.EmbeddingProfileID, *linkedStatus.Embedding.EmbeddingProfileID)
	if err != nil || !found {
		return uci1SearchProfileSeparationObservation{}, false, err
	}
	if primaryProfile.Dimension != linkedProfile.Dimension || primaryProfile.Model == linkedProfile.Model {
		return uci1SearchProfileSeparationObservation{}, false, nil
	}
	mixedPrimary, err := uci1SearchMixedProfileLinks(ctx, runtime.Authority, primaryProfile.ID)
	if err != nil {
		return uci1SearchProfileSeparationObservation{}, false, err
	}
	mixedLinked, err := uci1SearchMixedProfileLinks(ctx, runtime.Authority, linkedProfile.ID)
	if err != nil {
		return uci1SearchProfileSeparationObservation{}, false, err
	}
	if mixedPrimary != 0 || mixedLinked != 0 {
		return uci1SearchProfileSeparationObservation{}, false, errors.New("installed search persisted mixed vector profile links")
	}
	primaryResponse, err := uci1SearchQuery(ctx, primary, primarySelection, runtime.Request.Fixture.SharedSymbol, runtime.Request.Fixture.RelativePath)
	if err != nil {
		return uci1SearchProfileSeparationObservation{}, false, err
	}
	linkedResponse, err := uci1SearchQuery(ctx, linked, linkedSelection, runtime.Request.Fixture.SharedSymbol, runtime.Request.Fixture.RelativePath)
	if err != nil {
		return uci1SearchProfileSeparationObservation{}, false, err
	}
	if !uci1SearchResponseHasCurrentFunction(primaryResponse, primaryPublication, runtime.Request.Fixture.SharedSymbol, runtime.Request.Fixture.RelativePath) || !uci1SearchResponseHasCurrentFunction(linkedResponse, linkedPublication, runtime.Request.Fixture.SharedSymbol, runtime.Request.Fixture.RelativePath) {
		return uci1SearchProfileSeparationObservation{}, false, errors.New("installed profile-separated search omitted a selected baseline")
	}
	return uci1SearchProfileSeparationObservation{Dimension: primaryProfile.Dimension, LeftReady: primaryStatus.Embedding.ReadyCandidates, RightReady: linkedStatus.Embedding.ReadyCandidates}, true, nil
}

func uci1SearchRestoreSource(runtime uciInstalledAcceptanceScenarioRuntime, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, previous uciInstalledAcceptancePublication, path string, source []byte, functionName string) error {
	if err := os.WriteFile(path, source, 0o600); err != nil {
		return errors.New("restore installed search source")
	}
	restored, err := os.ReadFile(path)
	if err != nil || string(restored) != string(source) {
		return errors.New("verify restored installed search source")
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), uci1SearchCleanupTimeout)
	defer cancel()
	_, err = uciWaitForInstalledAcceptanceWatcherState(cleanupCtx, client, selection, previous, functionName, runtime.Request.Fixture.RelativePath, true)
	return err
}

func uci1SearchWaitForCompleteEmbedding(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, expected uciInstalledAcceptancePublication) (uci1SearchInstalledStatus, bool, error) {
	waitCtx, cancel := context.WithTimeout(ctx, uci1SearchCacheWait)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := uci1SearchInstalledStatusFor(waitCtx, client, selection)
		if err != nil {
			if waitCtx.Err() != nil {
				return uci1SearchInstalledStatus{}, false, nil
			}
			return uci1SearchInstalledStatus{}, false, err
		}
		if !uci1SearchStatusMatchesPublication(status, expected) {
			return uci1SearchInstalledStatus{}, false, nil
		}
		if uci1SearchComplete(status) {
			return status, true, nil
		}
		select {
		case <-waitCtx.Done():
			return status, false, nil
		case <-ticker.C:
		}
	}
}

func uci1SearchQuery(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, query, relativePath string) (uci.QueryResponse, error) {
	payload, err := client.Tool(ctx, "codebase_search", map[string]any{
		"context_handle": selection.contextHandle,
		"query":          query,
		"path_prefix":    relativePath,
		"limit":          10,
	})
	if err != nil {
		return uci.QueryResponse{}, err
	}
	return uciDecodeInstalledAcceptanceQuery(payload)
}

func uci1SearchResponseHasCurrentFunction(response uci.QueryResponse, publication uciInstalledAcceptancePublication, functionName, relativePath string) bool {
	if (response.Status != uci.QueryStatusOK && response.Status != uci.QueryStatusPartial) || !uciInstalledAcceptanceQueryMatchesPublication(response, publication) || response.Items == nil || len(*response.Items) == 0 {
		return false
	}
	found := false
	for _, item := range *response.Items {
		if item.Ref.SourceID != publication.sourceID || item.Ref.ViewID != publication.viewID {
			return false
		}
		name, nameOK := uciInstalledAcceptanceGoFunctionName(item.Ref.EntityKey)
		if item.Path == relativePath && nameOK && name == functionName {
			found = true
		}
	}
	return found
}

func uci1SearchResponseExcludesOldBody(response uci.QueryResponse, publication uciInstalledAcceptancePublication, oldDigest, oldFunction string) bool {
	if (response.Status != uci.QueryStatusOK && response.Status != uci.QueryStatusPartial && response.Status != uci.QueryStatusEmpty) || !uciInstalledAcceptanceQueryMatchesPublication(response, publication) {
		return false
	}
	if response.Items == nil {
		return true
	}
	for _, item := range *response.Items {
		if item.Ref.SourceID != publication.sourceID || item.Ref.ViewID != publication.viewID || string(item.ContentDigest) == oldDigest {
			return false
		}
		name, nameOK := uciInstalledAcceptanceGoFunctionName(item.Ref.EntityKey)
		if nameOK && name == oldFunction {
			return false
		}
	}
	return true
}

func uci1SearchCacheCountsFor(ctx context.Context, authority *uciInstalledAcceptanceAuthority) (uci1SearchCacheCounts, error) {
	if authority == nil || authority.store == nil || authority.source == nil || authority.source.SourceID == "" {
		return uci1SearchCacheCounts{}, errors.New("installed search cache authority is incomplete")
	}
	counts := uci1SearchCacheCounts{}
	db := authority.store.GetDB().WithContext(ctx)
	for _, count := range []struct {
		query string
		into  *int64
	}{
		{`SELECT COUNT(*) FROM ci_blobs WHERE source_id = ?`, &counts.Blobs},
		{`SELECT COUNT(*) FROM ci_parse_artifacts WHERE source_id = ?`, &counts.ParseArtifacts},
		{`SELECT COUNT(*) FROM ci_embeddings WHERE source_id = ?`, &counts.Embeddings},
		{`SELECT COUNT(*) FROM ci_chunk_embeddings WHERE source_id = ?`, &counts.ChunkEmbeddings},
	} {
		if err := db.Raw(count.query, authority.source.SourceID).Scan(count.into).Error; err != nil {
			return uci1SearchCacheCounts{}, errors.New("count installed search cache rows")
		}
	}
	return counts, nil
}

func uci1SearchReadyCandidatesForView(ctx context.Context, authority *uciInstalledAcceptanceAuthority, publication uciInstalledAcceptancePublication, embeddingProfileID string) (int64, error) {
	if authority == nil || authority.store == nil || authority.source == nil || embeddingProfileID == "" || publication.sourceID == "" || publication.checkoutID == "" || publication.viewID == "" || publication.generation < 1 {
		return 0, errors.New("installed search ready-candidate authority is incomplete")
	}
	var count int64
	err := authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT COUNT(DISTINCT chunk_embedding.chunk_id)
		FROM ci_views AS view
		JOIN ci_memberships AS membership
		  ON membership.checkout_id = view.checkout_id
		 AND membership.valid_from_generation <= view.generation
		 AND (membership.valid_to_generation IS NULL OR membership.valid_to_generation > view.generation)
		JOIN ci_chunks AS chunk
		  ON chunk.source_id = view.source_id
		 AND chunk.artifact_id = membership.artifact_id
		JOIN ci_chunk_embeddings AS chunk_embedding
		  ON chunk_embedding.source_id = chunk.source_id
		 AND chunk_embedding.chunk_id = chunk.chunk_id
		JOIN ci_embeddings AS embedding
		  ON embedding.embedding_id = chunk_embedding.embedding_id
		 AND embedding.source_id = chunk_embedding.source_id
		WHERE view.view_id = ?
		  AND view.source_id = ?
		  AND view.checkout_id = ?
		  AND chunk_embedding.embedding_profile_id = ?
		  AND embedding.embedding_profile_id = ?
		  AND embedding.status = 'ready'`, publication.viewID, publication.sourceID, publication.checkoutID, embeddingProfileID, embeddingProfileID).Scan(&count).Error
	if err != nil {
		return 0, errors.New("count installed search ready candidates")
	}
	return count, nil
}

func uci1SearchReadyCandidatesOutsideView(ctx context.Context, authority *uciInstalledAcceptanceAuthority, publication uciInstalledAcceptancePublication, embeddingProfileID string) (int64, error) {
	if authority == nil || authority.store == nil || authority.source == nil || embeddingProfileID == "" || publication.sourceID == "" || publication.viewID == "" {
		return 0, errors.New("installed search invisible-candidate authority is incomplete")
	}
	var count int64
	err := authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT COUNT(DISTINCT chunk_embedding.chunk_id)
		FROM ci_views AS view
		JOIN ci_memberships AS membership
		  ON membership.checkout_id = view.checkout_id
		 AND membership.valid_from_generation <= view.generation
		 AND (membership.valid_to_generation IS NULL OR membership.valid_to_generation > view.generation)
		JOIN ci_chunks AS chunk
		  ON chunk.source_id = view.source_id
		 AND chunk.artifact_id = membership.artifact_id
		JOIN ci_chunk_embeddings AS chunk_embedding
		  ON chunk_embedding.source_id = chunk.source_id
		 AND chunk_embedding.chunk_id = chunk.chunk_id
		JOIN ci_embeddings AS embedding
		  ON embedding.embedding_id = chunk_embedding.embedding_id
		 AND embedding.source_id = chunk_embedding.source_id
		WHERE view.source_id = ?
		  AND view.view_id <> ?
		  AND chunk_embedding.embedding_profile_id = ?
		  AND embedding.embedding_profile_id = ?
		  AND embedding.status = 'ready'`, publication.sourceID, publication.viewID, embeddingProfileID, embeddingProfileID).Scan(&count).Error
	if err != nil {
		return 0, errors.New("count installed search invisible candidates")
	}
	return count, nil
}

type uci1SearchEmbeddingProfile struct {
	ID        string `gorm:"column:embedding_profile_id"`
	Model     string `gorm:"column:model"`
	Dimension int    `gorm:"column:dimension"`
}

func uci1SearchProfilePair(ctx context.Context, authority *uciInstalledAcceptanceAuthority, primaryID, linkedID string) (uci1SearchEmbeddingProfile, uci1SearchEmbeddingProfile, bool, error) {
	if authority == nil || authority.store == nil || primaryID == "" || linkedID == "" || primaryID == linkedID {
		return uci1SearchEmbeddingProfile{}, uci1SearchEmbeddingProfile{}, false, nil
	}
	var rows []uci1SearchEmbeddingProfile
	if err := authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT embedding_profile_id, model, dimension
		FROM ci_embedding_profiles
		WHERE embedding_profile_id IN (?, ?)`, primaryID, linkedID).Scan(&rows).Error; err != nil {
		return uci1SearchEmbeddingProfile{}, uci1SearchEmbeddingProfile{}, false, errors.New("load installed search embedding profiles")
	}
	profiles := make(map[string]uci1SearchEmbeddingProfile, len(rows))
	for _, profile := range rows {
		if profile.ID != "" && profile.Model != "" && profile.Dimension > 0 {
			profiles[profile.ID] = profile
		}
	}
	primary, primaryFound := profiles[primaryID]
	linked, linkedFound := profiles[linkedID]
	return primary, linked, primaryFound && linkedFound, nil
}

func uci1SearchMixedProfileLinks(ctx context.Context, authority *uciInstalledAcceptanceAuthority, embeddingProfileID string) (int64, error) {
	if authority == nil || authority.store == nil || embeddingProfileID == "" {
		return 0, errors.New("installed search profile-link authority is incomplete")
	}
	var count int64
	if err := authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT COUNT(*)
		FROM ci_chunk_embeddings AS chunk_embedding
		JOIN ci_embeddings AS embedding
		  ON embedding.embedding_id = chunk_embedding.embedding_id
		 AND embedding.source_id = chunk_embedding.source_id
		WHERE chunk_embedding.embedding_profile_id = ?
		  AND embedding.embedding_profile_id <> ?`, embeddingProfileID, embeddingProfileID).Scan(&count).Error; err != nil {
		return 0, errors.New("count installed search mixed profile links")
	}
	return count, nil
}
