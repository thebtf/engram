package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/uci"
)

type uciCodebaseExposureStoreFake struct {
	mu          sync.Mutex
	exposures   []uci.ExposureRecord
	completions []uci.CompletionEvidence
}

func newUCICodebaseExposureRecorder() (*uci.ExposureRecorder, *uciCodebaseExposureStoreFake) {
	store := &uciCodebaseExposureStoreFake{}
	return uci.NewExposureRecorder(store, uci.NewExposureHealthController(true)), store
}

func (store *uciCodebaseExposureStoreFake) AppendExposure(_ context.Context, record uci.ExposureRecord) (uci.ExposureRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, existing := range store.exposures {
		if existing.AuthRealm == record.AuthRealm && existing.ClientSessionRef == record.ClientSessionRef && existing.IdempotencyKey == record.IdempotencyKey {
			if existing.BindingDigest != record.BindingDigest {
				return uci.ExposureRecord{}, uci.ErrIdempotencyMismatch
			}
			return existing, nil
		}
	}
	record.ExposureRef = uci.NewExposureRef()
	store.exposures = append(store.exposures, record)
	return record, nil
}

func (store *uciCodebaseExposureStoreFake) AppendCompletion(_ context.Context, evidence uci.CompletionEvidence) (uci.CompletionEvidence, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, existing := range store.completions {
		if existing.ExposureRef == evidence.ExposureRef && existing.SupportedHostRef == evidence.SupportedHostRef && existing.IdempotencyKey == evidence.IdempotencyKey {
			if existing.BindingDigest != evidence.BindingDigest {
				return uci.CompletionEvidence{}, uci.ErrIdempotencyMismatch
			}
			return existing, nil
		}
	}
	store.completions = append(store.completions, evidence)
	return evidence, nil
}

func (store *uciCodebaseExposureStoreFake) exposureCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.exposures)
}

func (store *uciCodebaseExposureStoreFake) completionCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.completions)
}

func TestUCIExposureSearchAppendsOneOpaqueReceiptAndExactRetryReusesIt(t *testing.T) {
	fixture := newUCICodeIntelCompatibilityFixture(t)
	handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
	arguments := uciCodeIntelCompatibilitySearchArguments(handle, uciCodeIntelCompatibilityProject, 10)

	_, first := requireUCICodeIntelQueryResponse(t, callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", arguments), fixture.refA, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityBodyB)
	_, second := requireUCICodeIntelQueryResponse(t, callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", arguments), fixture.refA, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityBodyB)
	if first.Exposure == nil || second.Exposure == nil || first.Exposure.ExposureRef != second.Exposure.ExposureRef {
		t.Fatalf("exact retry receipts = %#v / %#v, want one original receipt", first.Exposure, second.Exposure)
	}
	if !uci.ValidExposureRef(first.Exposure.ExposureRef) {
		t.Fatalf("exposure receipt = %q, want opaque canonical reference", first.Exposure.ExposureRef)
	}
	if got := fixture.exposureStore.exposureCount(); got != 1 {
		t.Fatalf("durable exposure rows = %d, want 1", got)
	}

	mismatchedArguments := uciCodeIntelCompatibilitySearchArguments(handle, uciCodeIntelCompatibilityProject, 10)
	mismatchedArguments["query"] = "changed request body under the same JSON-RPC id"
	mismatch := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", mismatchedArguments)
	mismatchText := uciCodeIntelToolText(t, mismatch)
	var mismatchPayload uci.QueryResponse
	require.NoError(t, json.Unmarshal([]byte(mismatchText), &mismatchPayload))
	require.NoError(t, mismatchPayload.Validate())
	require.Equal(t, uci.QueryStatusUnavailable, mismatchPayload.Status)
	require.NotNil(t, mismatchPayload.Error)
	require.Equal(t, uci.QueryErrorIdempotencyMismatch, mismatchPayload.Error.Code)
	require.Nil(t, mismatchPayload.Contexts)
	require.Nil(t, mismatchPayload.Exposure)
	require.Nil(t, mismatchPayload.Items)
	require.Nil(t, mismatchPayload.Graph)
	requireUCICodeIntelNoLeaks(t, mismatchText, fixture)
	if got := fixture.exposureStore.exposureCount(); got != 1 {
		t.Fatalf("durable exposure rows after changed same-ID request = %d, want 1", got)
	}

	fixture.exposureStore.mu.Lock()
	record := fixture.exposureStore.exposures[0]
	fixture.exposureStore.mu.Unlock()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal exposure record: %v", err)
	}
	for _, forbidden := range []string{uciCodeIntelCompatibilityQuery, uciCodeIntelCompatibilityPathPrefix, uciCodeIntelCompatibilityBodyA} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("durable exposure record leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestUCICompletionAcceptsVerifiedCallbackAndKeepsMismatchClosed(t *testing.T) {
	fixture := newUCICodeIntelCompatibilityFixture(t)
	handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
	_, released := requireUCICodeIntelQueryResponse(t, callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", uciCodeIntelCompatibilitySearchArguments(handle, uciCodeIntelCompatibilityProject, 10)), fixture.refA, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityBodyB)
	if released.Exposure == nil {
		t.Fatal("released search omitted exposure receipt")
	}

	callback := uci.VerifiedSupportedHostCallback{
		ExposureRef:      released.Exposure.ExposureRef,
		SupportedHostRef: "supported-host",
		CallbackRef:      "callback",
		Outcome:          uci.CompletionPartial,
		IdempotencyKey:   "completion-idempotency",
		OccurredAt:       time.Unix(1, 0).UTC(),
	}
	if err := fixture.server.RecordUCICompletion(context.Background(), callback); err != nil {
		t.Fatalf("RecordUCICompletion() error = %v", err)
	}
	if err := fixture.server.RecordUCICompletion(context.Background(), callback); err != nil {
		t.Fatalf("RecordUCICompletion() exact retry error = %v", err)
	}
	if got := fixture.exposureStore.completionCount(); got != 1 {
		t.Fatalf("durable completion rows = %d, want 1", got)
	}

	mismatch := callback
	mismatch.Outcome = uci.CompletionFailed
	if err := fixture.server.RecordUCICompletion(context.Background(), mismatch); !errors.Is(err, uci.ErrIdempotencyMismatch) {
		t.Fatalf("RecordUCICompletion() changed callback error = %v, want %v", err, uci.ErrIdempotencyMismatch)
	}
}
