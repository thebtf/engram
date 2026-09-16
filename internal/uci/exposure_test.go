package uci

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestQueryResponsePreExposureStateIsNarrow(t *testing.T) {
	response := exposureTestResponse(QueryStatusOK)
	if err := response.ValidatePreExposure(); err != nil {
		t.Fatalf("ValidatePreExposure() error = %v", err)
	}
	if err := response.Validate(); err == nil {
		t.Fatal("Validate() accepted an unreleased response")
	}

	response.Exposure = &QueryExposure{ExposureRef: NewExposureRef(), CompletionState: QueryCompletionUnknown}
	if err := response.Validate(); err != nil {
		t.Fatalf("Validate() error after receipt = %v", err)
	}
	if err := response.ValidatePreExposure(); err == nil {
		t.Fatal("ValidatePreExposure() accepted an already-released response")
	}

	refusal := QueryResponse{
		Schema: QueryResponseSchema,
		Status: QueryStatusContextRequired,
		Error:  &QueryError{Code: QueryErrorContextRequired},
	}
	if err := refusal.Validate(); err != nil {
		t.Fatalf("Validate() refusal error = %v", err)
	}
	if err := refusal.ValidatePreExposure(); err == nil {
		t.Fatal("ValidatePreExposure() accepted a refusal")
	}
}

func TestExposureRecorderDerivesOpaqueIdentityAndExactRetries(t *testing.T) {
	store := &exposureStoreFake{}
	health := NewExposureHealthController(true)
	recorder := NewExposureRecorder(store, health)
	contextRef := exposureTestContextRef()
	authorized := newAuthorizedContext(contextRef)
	input := ExposureInput{
		AuthRealm:            "client",
		ClientKeycard:        "keycard-1",
		ClientSession:        "session-1",
		RequestID:            "\"request-1\"",
		RequestBindingDigest: "sha256:" + strings.Repeat("a", 64),
		Operation:            ExposureOperationCodeSearch,
		Response:             exposureTestResponse(QueryStatusOK),
		RecordedAt:           time.Unix(1, 0).UTC(),
	}

	first, err := recorder.Record(context.Background(), authorized, input)
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	second, err := recorder.Record(context.Background(), authorized, input)
	if err != nil {
		t.Fatalf("Record() exact retry error = %v", err)
	}
	if first != second || len(store.exposures) != 1 {
		t.Fatalf("exact retry = %#v / %#v with %d records, want one original receipt", first, second, len(store.exposures))
	}

	record := store.exposures[0]
	if record.ExposureRef != first.ExposureRef || first.CompletionState != QueryCompletionUnknown {
		t.Fatalf("stored receipt = %#v, want released unknown completion", record)
	}
	for _, value := range []string{record.ClientRef, record.ClientSessionRef, record.RequestRef, record.IdempotencyKey} {
		if !strings.HasPrefix(value, "sha256:") || strings.Contains(value, "keycard-1") || strings.Contains(value, "session-1") || strings.Contains(value, "request-1") {
			t.Fatalf("stored identity reference %q is not opaque", value)
		}
	}
	if record.Operation != ExposureOperationCodeSearch || record.Result != ExposureResultOK || record.Retrieval != ExposureRetrievalLexical || record.Coverage != ExposureCoverageComplete || record.Evidence != ExposureEvidenceFTS || record.Certainty != ExposureCertaintyEstablished {
		t.Fatalf("classification = %#v", record)
	}

	mismatched := input
	mismatched.RequestBindingDigest = "sha256:" + strings.Repeat("b", 64)
	if _, err := recorder.Record(context.Background(), authorized, mismatched); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("Record() changed binding error = %v, want %v", err, ErrIdempotencyMismatch)
	}
	if got := recorder.Health(); got.State != ExposureHealthHealthy || got.LastFailureCode != ExposureHealthFailureNone {
		t.Fatalf("health after mismatch = %#v, want healthy no-op", got)
	}
}

func TestExposureRecorderMapsInitialAndCompletionFailures(t *testing.T) {
	contextRef := exposureTestContextRef()
	input := ExposureInput{
		AuthRealm:            "client",
		ClientKeycard:        "keycard-2",
		ClientSession:        "session-2",
		RequestID:            "2",
		RequestBindingDigest: "sha256:" + strings.Repeat("c", 64),
		Operation:            ExposureOperationVersionedRead,
		Response:             exposureTestResponse(QueryStatusOK),
	}

	initialStore := &exposureStoreFake{exposureErr: errors.New("append failed")}
	initialRecorder := NewExposureRecorder(initialStore, NewExposureHealthController(true))
	if _, err := initialRecorder.Record(context.Background(), newAuthorizedContext(contextRef), input); !errors.Is(err, ErrExposureUnavailable) {
		t.Fatalf("Record() failure error = %v, want %v", err, ErrExposureUnavailable)
	}
	if got := initialRecorder.Health(); got.State != ExposureHealthUnavailable || got.LastFailureCode != ExposureHealthFailureExposureUnavailable {
		t.Fatalf("initial failure health = %#v", got)
	}

	completionStore := &exposureStoreFake{}
	completionRecorder := NewExposureRecorder(completionStore, NewExposureHealthController(true))
	receipt, err := completionRecorder.Record(context.Background(), newAuthorizedContext(contextRef), input)
	if err != nil {
		t.Fatalf("Record() setup error = %v", err)
	}
	callback := VerifiedSupportedHostCallback{
		ExposureRef:      receipt.ExposureRef,
		SupportedHostRef: "supported-host",
		CallbackRef:      "callback-1",
		Outcome:          CompletionPartial,
		IdempotencyKey:   "callback-idempotency",
		OccurredAt:       time.Unix(2, 0).UTC(),
	}
	if _, err := completionRecorder.RecordCompletion(context.Background(), callback); err != nil {
		t.Fatalf("RecordCompletion() error = %v", err)
	}
	if len(completionStore.completions) != 1 {
		t.Fatalf("completion rows = %d, want 1", len(completionStore.completions))
	}
	changed := callback
	changed.CallbackRef = "callback-2"
	if _, err := completionRecorder.RecordCompletion(context.Background(), changed); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("RecordCompletion() changed binding error = %v, want %v", err, ErrIdempotencyMismatch)
	}

	completionStore.completionErr = errors.New("append failed")
	callback.IdempotencyKey = "callback-idempotency-2"
	if _, err := completionRecorder.RecordCompletion(context.Background(), callback); !errors.Is(err, ErrCompletionEvidenceUnavailable) {
		t.Fatalf("RecordCompletion() failure error = %v, want %v", err, ErrCompletionEvidenceUnavailable)
	}
	if got := completionRecorder.Health(); got.State != ExposureHealthDegraded || got.LastFailureCode != ExposureHealthFailureCompletionEvidenceUnavailable {
		t.Fatalf("completion failure health = %#v", got)
	}
}

type exposureStoreFake struct {
	exposures     []ExposureRecord
	completions   []CompletionEvidence
	exposureErr   error
	completionErr error
}

func (store *exposureStoreFake) AppendExposure(_ context.Context, record ExposureRecord) (ExposureRecord, error) {
	if store.exposureErr != nil {
		return ExposureRecord{}, store.exposureErr
	}
	for _, existing := range store.exposures {
		if existing.AuthRealm == record.AuthRealm && existing.ClientSessionRef == record.ClientSessionRef && existing.IdempotencyKey == record.IdempotencyKey {
			if existing.BindingDigest != record.BindingDigest {
				return ExposureRecord{}, ErrIdempotencyMismatch
			}
			return existing, nil
		}
	}
	record.ExposureRef = NewExposureRef()
	store.exposures = append(store.exposures, record)
	return record, nil
}

func (store *exposureStoreFake) AppendCompletion(_ context.Context, evidence CompletionEvidence) (CompletionEvidence, error) {
	if store.completionErr != nil {
		return CompletionEvidence{}, store.completionErr
	}
	for _, existing := range store.completions {
		if existing.ExposureRef == evidence.ExposureRef && existing.SupportedHostRef == evidence.SupportedHostRef && existing.IdempotencyKey == evidence.IdempotencyKey {
			if existing.BindingDigest != evidence.BindingDigest {
				return CompletionEvidence{}, ErrIdempotencyMismatch
			}
			return existing, nil
		}
	}
	store.completions = append(store.completions, evidence)
	return evidence, nil
}

func exposureTestContextRef() ContextRef {
	return ContextRef{
		SourceID:          "20000000-0000-4000-8000-000000000001",
		CheckoutID:        "30000000-0000-4000-8000-000000000001",
		ViewID:            "40000000-0000-4000-8000-000000000001",
		Generation:        1,
		AnalysisProfileID: "50000000-0000-4000-8000-000000000001",
	}
}

func exposureTestResponse(status QueryResponseStatus) QueryResponse {
	ref := exposureTestContextRef()
	contexts := QueryContexts{queryContextRef(ref)}
	items := QueryItems{}
	warnings := QueryWarnings{}
	zero := int64(0)
	truncated := false
	response := QueryResponse{
		Schema:   QueryResponseSchema,
		Status:   status,
		Contexts: &contexts,
		Freshness: &QueryFreshness{
			State:          QueryFreshnessObservedCurrent,
			Method:         QueryFreshnessWatchWatermark,
			PendingChanges: &zero,
			EnrichmentWatermark: QueryEnrichmentWatermark{
				Sequence: 1,
				State:    QueryEnrichmentCurrent,
			},
		},
		Retrieval: &QueryRetrieval{
			Mode:               QueryRetrievalLexical,
			DegradationReasons: []string{},
		},
		Coverage: &QueryCoverage{
			Structural:       IndexCoverageComplete,
			UnresolvedSites:  &zero,
			UnsupportedFiles: &zero,
		},
		Items:        &items,
		Truncated:    &truncated,
		Warnings:     &warnings,
		Continuation: &QueryContinuation{},
	}
	if status == QueryStatusPartial {
		response.Coverage.Structural = IndexCoveragePartial
	}
	if err := response.ValidatePreExposure(); err != nil {
		panic(err)
	}
	return response
}
