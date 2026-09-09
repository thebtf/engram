package uci

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEmbeddingJobTimingObserverIsDisabledWithoutLoopbackEndpoint(t *testing.T) {
	t.Setenv(EnvUCIWatcherSLOEmbeddingTimingObserver, "")
	if observer := newEmbeddingJobTimingObserverFromEnvironment(); observer != nil {
		t.Fatal("unset embedding timing observer was enabled")
	}
	t.Setenv(EnvUCIWatcherSLOEmbeddingTimingObserver, "http://example.test/observer")
	if observer := newEmbeddingJobTimingObserverFromEnvironment(); observer != nil {
		t.Fatal("non-loopback embedding timing observer was enabled")
	}
}

func TestEmbeddingJobTimingObserverPostsOnlyRedactedMetadata(t *testing.T) {
	claim := embeddingJobTimingTestClaim(1)
	origin := time.Now()
	batch := EmbeddingBatch{Candidates: make([]EmbeddingCandidate, 7), MissingInputIndexes: make([]int, 3)}
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("observer method = %s, want POST", request.Method)
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var err error
		received, err = io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read observer payload: %v", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	t.Setenv(EnvUCIWatcherSLOEmbeddingTimingObserver, server.URL)
	observer := newEmbeddingJobTimingObserverFromEnvironment()
	if observer == nil {
		t.Fatal("loopback embedding timing observer was not enabled")
	}
	scope := newEmbeddingJobTimingScope(observer, claim, origin)
	scope.observe(embeddingJobTimingStagePrepare, origin.Add(time.Millisecond), origin.Add(3*time.Millisecond), &batch, "ok")
	var event embeddingJobTimingEvent
	if err := json.Unmarshal(received, &event); err != nil {
		t.Fatalf("decode timing event: %v", err)
	}
	if !event.valid() || event.JobDigest == claim.Ref.JobID || event.ProfileDigest != embeddingJobTimingDigest(claim.Ref.Context.AnalysisProfileID) || event.CandidateCount != 7 || event.MissingCount != 3 || event.Stage != embeddingJobTimingStagePrepare || event.ElapsedNS != (2*time.Millisecond).Nanoseconds() {
		t.Fatalf("timing event = %#v", event)
	}
	payload := string(received)
	for _, forbidden := range []string{claim.Ref.JobID, claim.Ref.EmbeddingProfileID, claim.Ref.Context.AnalysisProfileID, "source text", "vectors", "https://", "api_key", "DATABASE_DSN"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("timing event retained %q: %s", forbidden, payload)
		}
	}
}

func TestEmbeddingWorkerTimingRecordsConcurrentProviderSpans(t *testing.T) {
	profile := semanticTestProfile("concurrent-timing")
	claim, batch := embeddingJobTimingTestClaimAndBatch(t, profile, 8)
	provider := &concurrentEmbeddingProbe{model: profile.Model, started: make(chan struct{}, 8), release: make(chan struct{})}
	limits := DefaultEmbeddingWorkerLimits()
	limits.CandidatePageSize = len(batch.Candidates)
	limits.ProviderBatchSize = 2
	limits.ProviderConcurrency = 4
	worker := &EmbeddingWorker{profile: profile, embedder: provider, limits: limits}
	var eventsMu sync.Mutex
	events := make([]embeddingJobTimingEvent, 0, limits.ProviderConcurrency)
	timing := newEmbeddingJobTimingScope(func(event embeddingJobTimingEvent) {
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	}, claim, time.Now())
	type result struct {
		vectors [][]float32
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		vectors, err := worker.embedMissingWithTiming(context.Background(), claim, batch, timing)
		resultCh <- result{vectors: vectors, err: err}
	}()
	for range limits.ProviderConcurrency {
		select {
		case <-provider.started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed provider batches did not overlap")
		}
	}
	time.Sleep(20 * time.Millisecond)
	close(provider.release)
	outcome := <-resultCh
	if outcome.err != nil || len(outcome.vectors) != len(batch.Candidates) {
		t.Fatalf("timed provider result = vectors:%d err:%v", len(outcome.vectors), outcome.err)
	}
	eventsMu.Lock()
	observed := append([]embeddingJobTimingEvent(nil), events...)
	eventsMu.Unlock()
	if len(observed) != limits.ProviderConcurrency {
		t.Fatalf("provider timing spans = %d, want %d", len(observed), limits.ProviderConcurrency)
	}
	sort.Slice(observed, func(left, right int) bool { return observed[left].StartedElapsedNS < observed[right].StartedElapsedNS })
	individual := int64(0)
	latestStart, earliestReturn := observed[0].StartedElapsedNS, observed[0].ReturnedElapsedNS
	for _, event := range observed {
		if !event.valid() || event.Stage != embeddingJobTimingStageEmbed || event.CandidateCount != len(batch.Candidates) || event.MissingCount != len(batch.MissingInputIndexes) || event.ResultCode != "ok" {
			t.Fatalf("provider timing event = %#v", event)
		}
		individual += event.ElapsedNS
		if event.StartedElapsedNS > latestStart {
			latestStart = event.StartedElapsedNS
		}
		if event.ReturnedElapsedNS < earliestReturn {
			earliestReturn = event.ReturnedElapsedNS
		}
	}
	if earliestReturn < latestStart || individual <= observed[len(observed)-1].ReturnedElapsedNS-observed[0].StartedElapsedNS {
		t.Fatalf("provider spans were not concurrent: spans=%#v", observed)
	}
}

func embeddingJobTimingTestClaim(attempt int) EmbeddingJobClaim {
	contextRef := semanticTestContextRef(
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
		1,
	)
	return EmbeddingJobClaim{
		Ref: EmbeddingJobRef{
			JobID:              "55555555-5555-4555-8555-555555555555",
			Scope:              IndexScope{SourceID: contextRef.SourceID, CheckoutID: contextRef.CheckoutID, IncarnationID: "66666666-6666-4666-8666-666666666666"},
			Context:            contextRef,
			EmbeddingProfileID: "77777777-7777-4777-8777-777777777777",
			InputFingerprint:   semanticTestDigest("timing-job"),
			LeaseOwner:         "timing-owner",
			LeaseEpoch:         1,
		},
		Access:         ContextAccess{AuthRealm: "client", Principal: "agent/timing", SourceID: contextRef.SourceID, CheckoutID: contextRef.CheckoutID},
		Profile:        semanticTestProfile("timing-profile"),
		Attempt:        attempt,
		LeaseExpiresAt: time.Now().UTC().Add(time.Minute),
	}
}

func embeddingJobTimingTestClaimAndBatch(t *testing.T, profile VectorProfile, count int) (EmbeddingJobClaim, EmbeddingBatch) {
	t.Helper()
	claim := embeddingJobTimingTestClaim(1)
	claim.Profile = profile
	candidates := make([]EmbeddingCandidate, count)
	missing := make([]int, count)
	for index := range candidates {
		queryCandidate := semanticTestCandidate(
			claim.Ref.Context,
			fmt.Sprintf("8%07d-0000-4000-8000-%012d", index, index+1),
			fmt.Sprintf("entity:%d", index),
			fmt.Sprintf("Local%d", index),
			fmt.Sprintf("fixture.Local%d", index),
			fmt.Sprintf("file-%d.go", index),
			fmt.Sprintf("source text %d", index),
		)
		input, digest, err := SemanticEmbeddingInput(profile, queryCandidate)
		if err != nil {
			t.Fatal(err)
		}
		candidates[index] = EmbeddingCandidate{
			Key:              EmbeddingCandidateKey{MembershipID: fmt.Sprintf("9%07d-0000-4000-8000-%012d", index, index+1), ChunkID: fmt.Sprintf("a%07d-0000-4000-8000-%012d", index, index+1)},
			Candidate:        queryCandidate,
			ProtectionDomain: "source-private",
			Input:            input,
			InputDigest:      digest,
		}
		missing[index] = index
	}
	last := candidates[len(candidates)-1].Key
	return claim, EmbeddingBatch{Job: claim.Ref, BatchDigest: semanticTestDigest("timing-batch"), NextAfter: &last, Candidates: candidates, MissingInputIndexes: missing}
}

func TestEmbeddingWorkerTimingObserverPreservesDisabledPathAndCapturesWorkerStages(t *testing.T) {
	disabled := embeddingJobTimingRunWorker(t, nil)
	var eventsMu sync.Mutex
	var events []embeddingJobTimingEvent
	enabled := embeddingJobTimingRunWorker(t, func(event embeddingJobTimingEvent) {
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	})
	if disabled.claims != enabled.claims || disabled.prepares != enabled.prepares || disabled.commits != enabled.commits || disabled.completed != enabled.completed || disabled.providerCalls != enabled.providerCalls {
		t.Fatalf("disabled and enabled worker paths diverged: disabled=%#v enabled=%#v", disabled, enabled)
	}
	if disabled.claims != 1 || disabled.prepares != 2 || disabled.commits != 1 || disabled.completed != 1 || disabled.providerCalls != 1 {
		t.Fatalf("worker stages = %#v", disabled)
	}
	eventsMu.Lock()
	observed := append([]embeddingJobTimingEvent(nil), events...)
	eventsMu.Unlock()
	if len(observed) != 7 {
		t.Fatalf("worker timing event count = %d, want 7: %#v", len(observed), observed)
	}
	stages := make(map[embeddingJobTimingStage]int)
	for _, event := range observed {
		if !event.valid() || event.ResultCode != "ok" || event.ProfileDigest != embeddingJobTimingDigest(enabled.claim.Ref.Context.AnalysisProfileID) || event.JobDigest == enabled.claim.Ref.JobID {
			t.Fatalf("worker timing event = %#v", event)
		}
		stages[event.Stage]++
	}
	for stage, count := range map[embeddingJobTimingStage]int{
		embeddingJobTimingStageClaim: 1, embeddingJobTimingStageAuthorize: 1, embeddingJobTimingStagePrepare: 2,
		embeddingJobTimingStageEmbed: 1, embeddingJobTimingStageCommit: 1, embeddingJobTimingStageComplete: 1,
	} {
		if stages[stage] != count {
			t.Fatalf("%s timing spans = %d, want %d: %#v", stage, stages[stage], count, observed)
		}
	}
}

type embeddingJobTimingWorkerRun struct {
	claim         EmbeddingJobClaim
	claims        int
	prepares      int
	commits       int
	completed     int
	providerCalls int
}

func embeddingJobTimingRunWorker(t *testing.T, observer embeddingJobTimingObserver) embeddingJobTimingWorkerRun {
	t.Helper()
	profile := semanticTestProfile("worker-stage-timing")
	claim, batch := embeddingJobTimingTestClaimAndBatch(t, profile, 1)
	result := embeddingJobTimingWorkerRun{claim: claim}
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &embeddingJobTimingWorkerStore{
		claim: claim,
		batches: []EmbeddingBatch{
			batch,
			{Job: claim.Ref, BatchDigest: semanticTestDigest("worker-stage-exhausted"), Exhausted: true},
		},
		cancel: cancel,
		result: &result,
	}
	resolver := NewContextResolver(&contextResolverCatalogFake{records: []contextResolverCatalogRecord{{lookup: claim.Ref.Context, record: ContextRecord{Ref: claim.Ref.Context, AuthRealm: claim.Access.AuthRealm}}}}, &contextResolverAuthorizerFake{}, nil)
	worker := &EmbeddingWorker{profile: profile, embedder: &embeddingJobTimingWorkerEmbedder{model: profile.Model, result: &result}, store: store, resolver: resolver, limits: DefaultEmbeddingWorkerLimits(), timingObserver: observer}
	if err := worker.Run(root, claim.Ref.LeaseOwner); err != nil {
		t.Fatalf("run timed embedding worker: %v", err)
	}
	return result
}

type embeddingJobTimingWorkerStore struct {
	claim    EmbeddingJobClaim
	batches  []EmbeddingBatch
	cancel   context.CancelFunc
	result   *embeddingJobTimingWorkerRun
	prepared int
}

func (store *embeddingJobTimingWorkerStore) EnsureCurrentEmbeddingJobs(context.Context, VectorProfile, string, int) (string, bool, error) {
	return "", true, nil
}

func (store *embeddingJobTimingWorkerStore) ClaimEmbeddingJob(context.Context, VectorProfile, string, time.Duration) (EmbeddingJobClaim, bool, error) {
	if store.result.claims > 0 {
		return EmbeddingJobClaim{}, false, nil
	}
	store.result.claims++
	return store.claim, true, nil
}

func (store *embeddingJobTimingWorkerStore) RenewEmbeddingJob(context.Context, EmbeddingJobRef, time.Duration) error {
	return nil
}

func (store *embeddingJobTimingWorkerStore) PrepareEmbeddingBatch(context.Context, EmbeddingJobClaim, AuthorizedContext, int) (EmbeddingBatch, error) {
	batch := store.batches[store.prepared]
	store.prepared++
	store.result.prepares++
	return batch, nil
}

func (store *embeddingJobTimingWorkerStore) CommitEmbeddingBatch(context.Context, EmbeddingJobClaim, AuthorizedContext, EmbeddingBatch, [][]float32) error {
	store.result.commits++
	return nil
}

func (store *embeddingJobTimingWorkerStore) CompleteEmbeddingJob(context.Context, EmbeddingJobClaim, AuthorizedContext) error {
	store.result.completed++
	store.cancel()
	return nil
}

func (store *embeddingJobTimingWorkerStore) FailEmbeddingJob(context.Context, EmbeddingJobRef, EmbeddingFailure) error {
	return nil
}

type embeddingJobTimingWorkerEmbedder struct {
	model  string
	result *embeddingJobTimingWorkerRun
}

func (embedder *embeddingJobTimingWorkerEmbedder) Model() string {
	return embedder.model
}

func (embedder *embeddingJobTimingWorkerEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	embedder.result.providerCalls++
	vectors := make([][]float32, len(inputs))
	for index := range vectors {
		vectors[index] = semanticTestVector(float32(index + 1))
	}
	return vectors, nil
}
