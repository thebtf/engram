package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
	requestID := "same-json-rpc-request"

	_, first := requireUCICodeIntelQueryResponse(t, callUCICodeIntelWithID(t, fixture.server, fixture.clientA, requestID, "codebase_search", arguments), fixture.refA, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityBodyB)
	_, second := requireUCICodeIntelQueryResponse(t, callUCICodeIntelWithID(t, fixture.server, fixture.clientA, requestID, "codebase_search", arguments), fixture.refA, uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityBodyB)
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
	mismatch := callUCICodeIntelWithID(t, fixture.server, fixture.clientA, requestID, "codebase_search", mismatchedArguments)
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

func TestUCINilExposureRecorderSuppressesAuthorizedResultAndReportsUnavailable(t *testing.T) {
	fixture := newUCICodeIntelCompatibilityFixture(t)
	handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
	fixture.server.SetUCIExposureRecorder(nil)

	response := callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_search", uciCodeIntelCompatibilitySearchArguments(handle, uciCodeIntelCompatibilityProject, 10))
	text := uciCodeIntelToolText(t, response)
	var payload uci.QueryResponse
	require.NoError(t, json.Unmarshal([]byte(text), &payload))
	require.NoError(t, payload.Validate())
	require.Equal(t, uci.QueryStatusUnavailable, payload.Status)
	require.NotNil(t, payload.Error)
	require.Equal(t, uci.QueryErrorExposureUnavailable, payload.Error.Code)
	require.Nil(t, payload.Exposure)
	require.Nil(t, payload.Contexts)
	require.Nil(t, payload.Freshness)
	require.Nil(t, payload.Retrieval)
	require.Nil(t, payload.Coverage)
	require.Nil(t, payload.Items)
	require.Nil(t, payload.Graph)
	require.Nil(t, payload.Truncated)
	require.Nil(t, payload.Warnings)
	require.Nil(t, payload.Continuation)
	requireUCICodeIntelNoLeaks(t, text, fixture)
	require.Len(t, fixture.application.searchCalls, 1, "the authorized application result must not be released without durable exposure evidence")
	require.Zero(t, fixture.exposureStore.exposureCount())

	status := requireUCICodeIntelStatus(t, callUCICodeIntel(t, fixture.server, fixture.clientA, "codebase_status", map[string]any{
		"context_handle": handle,
	}), fixture.refA, 17, 11, "unavailable", "EXPOSURE_UNAVAILABLE")
	encodedStatus, err := json.Marshal(status)
	require.NoError(t, err)
	for _, forbidden := range []string{uciCodeIntelCompatibilityBodyA, uciCodeIntelCompatibilityBodyB, "private_locator"} {
		assert.NotContains(t, string(encodedStatus), forbidden)
	}
}

func TestRunPreservesJSONRPCIDsForUCIIdempotency(t *testing.T) {
	fixture := newUCICodeIntelCompatibilityFixture(t)
	handle := fixture.selectContext(t, fixture.clientA, fixture.refA)
	params, err := json.Marshal(map[string]any{
		"name": "codebase_search",
		"arguments": uciCodeIntelCompatibilitySearchArguments(
			handle,
			uciCodeIntelCompatibilityProject,
			10,
		),
	})
	require.NoError(t, err)

	request := func(id json.RawMessage) string {
		body, err := json.Marshal(struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}{"2.0", id, "tools/call", params})
		require.NoError(t, err)
		return string(body)
	}

	ids := []json.RawMessage{
		json.RawMessage(`42`),
		json.RawMessage(`9007199254740992`),
		json.RawMessage(`9007199254740993`),
		json.RawMessage(`"ordinary-id"`),
	}
	var output bytes.Buffer
	fixture.server.stdin = strings.NewReader(strings.Join([]string{
		request(ids[0]),
		request(ids[1]),
		request(ids[2]),
		request(ids[3]),
	}, "\n"))
	fixture.server.stdout = &output
	require.NoError(t, fixture.server.Run(fixture.clientA))

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	require.Len(t, lines, len(ids))
	for index, line := range lines {
		var response struct {
			ID json.RawMessage `json:"id"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &response))
		require.Equal(t, string(ids[index]), string(response.ID))
	}

	fixture.exposureStore.mu.Lock()
	records := append([]uci.ExposureRecord(nil), fixture.exposureStore.exposures...)
	fixture.exposureStore.mu.Unlock()
	require.Len(t, records, len(ids))
	assert.NotEqual(t, records[1].RequestRef, records[2].RequestRef)
	assert.NotEqual(t, records[1].IdempotencyKey, records[2].IdempotencyKey)
}
