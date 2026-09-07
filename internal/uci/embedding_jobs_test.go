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
