package uci

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

type concurrentEmbeddingProbe struct {
	model   string
	started chan struct{}
	release chan struct{}

	mu     sync.Mutex
	active int
	peak   int
}

func (probe *concurrentEmbeddingProbe) Model() string { return probe.model }

func (probe *concurrentEmbeddingProbe) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	probe.mu.Lock()
	probe.active++
	if probe.active > probe.peak {
		probe.peak = probe.active
	}
	probe.mu.Unlock()
	probe.started <- struct{}{}
	defer func() {
		probe.mu.Lock()
		probe.active--
		probe.mu.Unlock()
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-probe.release:
	}
	vectors := make([][]float32, len(inputs))
	for index := range vectors {
		vectors[index] = semanticTestVector(float32(index + 1))
	}
	return vectors, nil
}

func TestEmbeddingWorkerRunsBoundedProviderBatchesConcurrently(t *testing.T) {
	profile := semanticTestProfile("concurrent-provider")
	contextRef := semanticTestContextRef(
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
		1,
	)
	claim := EmbeddingJobClaim{
		Ref: EmbeddingJobRef{
			JobID:              "55555555-5555-4555-8555-555555555555",
			Scope:              IndexScope{SourceID: contextRef.SourceID, CheckoutID: contextRef.CheckoutID, IncarnationID: "66666666-6666-4666-8666-666666666666"},
			Context:            contextRef,
			EmbeddingProfileID: "77777777-7777-4777-8777-777777777777",
			InputFingerprint:   semanticTestDigest("concurrent-job"),
			LeaseOwner:         "concurrent-owner",
			LeaseEpoch:         1,
		},
		Access:         ContextAccess{AuthRealm: "client", Principal: "agent/concurrent", SourceID: contextRef.SourceID, CheckoutID: contextRef.CheckoutID},
		Profile:        profile,
		Attempt:        1,
		LeaseExpiresAt: time.Now().UTC().Add(time.Minute),
	}
	candidates := make([]EmbeddingCandidate, 8)
	missing := make([]int, len(candidates))
	for index := range candidates {
		queryCandidate := semanticTestCandidate(
			contextRef,
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
			Key: EmbeddingCandidateKey{
				MembershipID: fmt.Sprintf("9%07d-0000-4000-8000-%012d", index, index+1),
				ChunkID:      fmt.Sprintf("a%07d-0000-4000-8000-%012d", index, index+1),
			},
			Candidate:        queryCandidate,
			ProtectionDomain: "source-private",
			Input:            input,
			InputDigest:      digest,
		}
		missing[index] = index
	}
	last := candidates[len(candidates)-1].Key
	batch := EmbeddingBatch{
		Job:                 claim.Ref,
		BatchDigest:         semanticTestDigest("concurrent-batch"),
		NextAfter:           &last,
		Candidates:          candidates,
		MissingInputIndexes: missing,
	}
	provider := &concurrentEmbeddingProbe{
		model:   profile.Model,
		started: make(chan struct{}, len(candidates)),
		release: make(chan struct{}),
	}
	limits := DefaultEmbeddingWorkerLimits()
	limits.CandidatePageSize = len(candidates)
	limits.ProviderBatchSize = 2
	limits.ProviderConcurrency = 4
	worker := &EmbeddingWorker{profile: profile, embedder: provider, limits: limits}

	type result struct {
		vectors [][]float32
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		vectors, err := worker.embedMissing(context.Background(), claim, batch)
		resultCh <- result{vectors: vectors, err: err}
	}()
	released := false
	defer func() {
		if !released {
			close(provider.release)
		}
	}()
	for range limits.ProviderConcurrency {
		select {
		case <-provider.started:
		case <-time.After(2 * time.Second):
			t.Fatal("provider batches did not overlap up to the configured concurrency")
		}
	}
	close(provider.release)
	released = true
	outcome := <-resultCh
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if len(outcome.vectors) != len(candidates) {
		t.Fatalf("vectors = %d, want %d", len(outcome.vectors), len(candidates))
	}
	provider.mu.Lock()
	peak := provider.peak
	provider.mu.Unlock()
	if peak != limits.ProviderConcurrency {
		t.Fatalf("peak provider concurrency = %d, want %d", peak, limits.ProviderConcurrency)
	}
}

func TestEmbeddingWorkerPollsAtConfiguredCadenceAndProcessesOneQueuedClaim(t *testing.T) {
	limits := DefaultEmbeddingWorkerLimits()
	if limits.PollInterval != 100*time.Millisecond {
		t.Fatalf("default poll interval = %s, want 100ms", limits.PollInterval)
	}
	profile := semanticTestProfile("worker-polling")
	claim, batch := embeddingJobTimingTestClaimAndBatch(t, profile, 1)
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &embeddingWorkerPollingStore{
		claim:       claim,
		batches:     []EmbeddingBatch{batch, {Job: claim.Ref, BatchDigest: semanticTestDigest("polling-exhausted"), Exhausted: true}},
		emptyLimit:  3,
		idleReached: make(chan time.Time, 1),
		queued:      make(chan struct{}),
		claimed:     make(chan time.Time, 1),
		cancel:      cancel,
	}
	resolver := NewContextResolver(&contextResolverCatalogFake{records: []contextResolverCatalogRecord{{lookup: claim.Ref.Context, record: ContextRecord{Ref: claim.Ref.Context, AuthRealm: claim.Access.AuthRealm}}}}, &contextResolverAuthorizerFake{}, nil)
	worker := &EmbeddingWorker{profile: profile, embedder: &embeddingWorkerPollingEmbedder{model: profile.Model}, store: store, resolver: resolver, limits: limits}
	run := make(chan error, 1)
	go func() { run <- worker.Run(root, claim.Ref.LeaseOwner) }()

	embeddingWorkerPollingWaitForIdle(t, store)
	queuedAt, claimedAt := embeddingWorkerPollingQueueAndWait(t, store)
	embeddingWorkerPollingWaitForStop(t, run)
	claimAttempts, claims, prepares, commits, completes := store.snapshot()
	embeddingWorkerPollingRequireCadence(t, claimAttempts, limits, queuedAt, claimedAt)
	embeddingWorkerPollingRequireOutcome(t, root, claims, prepares, commits, completes)
}

func embeddingWorkerPollingWaitForIdle(t *testing.T, store *embeddingWorkerPollingStore) {
	t.Helper()
	select {
	case <-store.idleReached:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not make the expected idle claim attempts")
	}
}

func embeddingWorkerPollingQueueAndWait(t *testing.T, store *embeddingWorkerPollingStore) (time.Time, time.Time) {
	t.Helper()
	queuedAt := time.Now()
	close(store.queued)
	select {
	case claimedAt := <-store.claimed:
		return queuedAt, claimedAt
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not claim queued work promptly")
		return time.Time{}, time.Time{}
	}
}

func embeddingWorkerPollingWaitForStop(t *testing.T, run <-chan error) {
	t.Helper()
	select {
	case err := <-run:
		if err != nil {
			t.Fatalf("run embedding worker: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

func embeddingWorkerPollingRequireCadence(t *testing.T, claimAttempts []embeddingWorkerPollingClaimAttempt, limits EmbeddingWorkerLimits, queuedAt, claimedAt time.Time) {
	t.Helper()
	if claimedAt.Sub(queuedAt) > 4*limits.PollInterval {
		t.Fatalf("queued claim latency = %s, want at most %s", claimedAt.Sub(queuedAt), 4*limits.PollInterval)
	}
	claimedAttempt := embeddingWorkerPollingClaimedAttempt(t, claimAttempts, limits)
	if claimedAttempt < 0 {
		t.Fatal("worker did not claim queued work")
	}
	embeddingWorkerPollingRequireContinuation(t, claimAttempts, claimedAttempt, limits)
}

func embeddingWorkerPollingClaimedAttempt(t *testing.T, claimAttempts []embeddingWorkerPollingClaimAttempt, limits EmbeddingWorkerLimits) int {
	t.Helper()
	claimedAttempt := -1
	for index, attempt := range claimAttempts {
		if attempt.claimed {
			if claimedAttempt >= 0 {
				t.Fatalf("successful claim attempts = %d and %d, want exactly one", claimedAttempt, index)
			}
			claimedAttempt = index
		}
		if index > 0 {
			previous := claimAttempts[index-1]
			if !previous.claimed && !attempt.claimed && attempt.at.Sub(previous.at) < limits.PollInterval {
				t.Fatalf("idle claim interval %d = %s, want at least %s", index, attempt.at.Sub(previous.at), limits.PollInterval)
			}
		}
	}
	return claimedAttempt
}

func embeddingWorkerPollingRequireContinuation(t *testing.T, claimAttempts []embeddingWorkerPollingClaimAttempt, claimedAttempt int, limits EmbeddingWorkerLimits) {
	t.Helper()
	if claimedAttempt+1 >= len(claimAttempts) {
		t.Fatal("worker did not continue immediately after the successful claim")
	}
	continuation := claimAttempts[claimedAttempt+1]
	if continuation.claimed {
		t.Fatal("worker duplicated the successful claim during its immediate continuation")
	}
	if interval := continuation.at.Sub(claimAttempts[claimedAttempt].at); interval >= limits.PollInterval {
		t.Fatalf("post-claim continuation interval = %s, want below %s", interval, limits.PollInterval)
	}
	if len(claimAttempts) != claimedAttempt+2 {
		t.Fatalf("claim attempts after immediate continuation = %d, want 1", len(claimAttempts)-claimedAttempt-1)
	}
}

func embeddingWorkerPollingRequireOutcome(t *testing.T, root context.Context, claims, prepares, commits, completes int) {
	t.Helper()
	if root.Err() != context.Canceled {
		t.Fatalf("worker cancellation = %v, want %v", root.Err(), context.Canceled)
	}
	if claims != 1 || prepares != 2 || commits != 1 || completes != 1 {
		t.Fatalf("claimed/processed work = claims:%d prepares:%d commits:%d completes:%d, want 1:2:1:1", claims, prepares, commits, completes)
	}
}

type embeddingWorkerPollingClaimAttempt struct {
	at      time.Time
	claimed bool
}

type embeddingWorkerPollingStore struct {
	claim       EmbeddingJobClaim
	batches     []EmbeddingBatch
	emptyLimit  int
	idleReached chan time.Time
	queued      chan struct{}
	claimed     chan time.Time
	cancel      context.CancelFunc

	mu            sync.Mutex
	claimAttempts []embeddingWorkerPollingClaimAttempt
	emptyCalls    int
	claims        int
	prepares      int
	commits       int
	completes     int
}

func (store *embeddingWorkerPollingStore) EnsureCurrentEmbeddingJobs(context.Context, VectorProfile, string, int) (string, bool, error) {
	return "", true, nil
}

func (store *embeddingWorkerPollingStore) ClaimEmbeddingJob(_ context.Context, _ VectorProfile, _ string, _ time.Duration) (EmbeddingJobClaim, bool, error) {
	now := time.Now()
	store.mu.Lock()
	store.claimAttempts = append(store.claimAttempts, embeddingWorkerPollingClaimAttempt{at: now})
	attempt := len(store.claimAttempts) - 1
	select {
	case <-store.queued:
		if store.claims == 0 {
			store.claims++
			store.claimAttempts[attempt].claimed = true
			store.mu.Unlock()
			store.claimed <- now
			return store.claim, true, nil
		}
		store.mu.Unlock()
		store.cancel()
		return EmbeddingJobClaim{}, false, nil
	default:
		store.emptyCalls++
		if store.emptyCalls == store.emptyLimit {
			store.mu.Unlock()
			store.idleReached <- now
			return EmbeddingJobClaim{}, false, nil
		}
	}
	store.mu.Unlock()
	return EmbeddingJobClaim{}, false, nil
}

func (store *embeddingWorkerPollingStore) RenewEmbeddingJob(context.Context, EmbeddingJobRef, time.Duration) error {
	return nil
}

func (store *embeddingWorkerPollingStore) PrepareEmbeddingBatch(context.Context, EmbeddingJobClaim, AuthorizedContext, int) (EmbeddingBatch, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	batch := store.batches[store.prepares]
	store.prepares++
	return batch, nil
}

func (store *embeddingWorkerPollingStore) CommitEmbeddingBatch(context.Context, EmbeddingJobClaim, AuthorizedContext, EmbeddingBatch, [][]float32) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.commits++
	return nil
}

func (store *embeddingWorkerPollingStore) CompleteEmbeddingJob(context.Context, EmbeddingJobClaim, AuthorizedContext) error {
	store.mu.Lock()
	store.completes++
	store.mu.Unlock()
	return nil
}

func (store *embeddingWorkerPollingStore) FailEmbeddingJob(context.Context, EmbeddingJobRef, EmbeddingFailure) error {
	return nil
}

func (store *embeddingWorkerPollingStore) snapshot() ([]embeddingWorkerPollingClaimAttempt, int, int, int, int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]embeddingWorkerPollingClaimAttempt(nil), store.claimAttempts...), store.claims, store.prepares, store.commits, store.completes
}

type embeddingWorkerPollingEmbedder struct {
	model string
}

func (embedder *embeddingWorkerPollingEmbedder) Model() string {
	return embedder.model
}

func (embedder *embeddingWorkerPollingEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	vectors := make([][]float32, len(inputs))
	for index := range vectors {
		vectors[index] = semanticTestVector(float32(index + 1))
	}
	return vectors, nil
}
