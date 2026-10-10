package uci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/thebtf/engram/internal/embedding"
)

func TestEmbeddingWorkerProviderPaddedCostBound(t *testing.T) {
	t.Run("packing", func(t *testing.T) {
		cases := []struct {
			name  string
			sizes []int
			want  []int
		}{
			{name: "empty"},
			{name: "small", sizes: []int{100, 200, 300}, want: []int{3}},
			{name: "exact-bound", sizes: []int{32768, 32768}, want: []int{2}},
			{name: "heterogeneous", sizes: []int{100, 100, 20000, 100, 100}, want: []int{3, 2}},
			{name: "oversized-alone", sizes: []int{100, 68628, 100}, want: []int{1, 1, 1}},
			{name: "count-bound", sizes: make([]int, 129), want: []int{128, 1}},
		}
		worker := &EmbeddingWorker{limits: DefaultEmbeddingWorkerLimits()}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				inputs := make([]embeddingProviderInput, len(test.sizes))
				for index, size := range test.sizes {
					inputs[index].input = strings.Repeat("x", size)
				}
				batches, err := worker.embeddingProviderBatches(inputs)
				if err != nil {
					t.Fatal(err)
				}
				var counts []int
				position := 0
				for _, batch := range batches {
					counts = append(counts, len(batch.texts))
					if batch.start != position {
						t.Fatalf("batch start = %d, want %d", batch.start, position)
					}
					for _, text := range batch.texts {
						if text != inputs[position].input {
							t.Fatal("packing changed input bytes or order")
						}
						position++
					}
				}
				if !reflect.DeepEqual(counts, test.want) || position != len(inputs) {
					t.Fatalf("batch counts = %v, want %v; preserved = %d/%d", counts, test.want, position, len(inputs))
				}
			})
		}
		worker.limits.MaxProviderBatchBytes = 10
		_, err := worker.embeddingProviderBatches([]embeddingProviderInput{{input: "12345678901"}})
		if !errors.Is(err, ErrEmbeddingInputCapacity) {
			t.Fatalf("existing input capacity error = %v", err)
		}
	})

	t.Run("real-client-full-text-mapping-and-cache-scope", func(t *testing.T) {
		profile := semanticTestProfile("cost-bound-provider")
		claim, batch := embeddingJobTimingTestClaimAndBatch(t, profile, 20)
		sizes := []int{400, 512, 800, 1024, 2048, 400, 750, 4096, 68628, 400, 600, 8000, 2000, 950, 14000, 1200, 5000, 2500, 16384, 1000}
		for index, size := range sizes {
			candidate := &batch.Candidates[index]
			candidate.Candidate.Text = fmt.Sprintf("fixture-%d-界\n", index)
			if size > 65536 {
				candidate.Candidate.Text += strings.Repeat("<", 11000)
			}
			input, _, err := SemanticEmbeddingInput(profile, candidate.Candidate)
			if err != nil || len(input) > size {
				t.Fatalf("fixture size %d: %v", size, err)
			}
			candidate.Candidate.Text += strings.Repeat("x", size-len(input))
			candidate.Input, candidate.InputDigest, err = SemanticEmbeddingInput(profile, candidate.Candidate)
			if err != nil || len(candidate.Input) != size {
				t.Fatalf("canonical fixture bytes = %d, want %d: %v", len(candidate.Input), size, err)
			}
		}
		for _, index := range []int{18, 19} {
			batch.Candidates[index].Candidate.Text = batch.Candidates[2].Candidate.Text
			batch.Candidates[index].Candidate.RelativePath = batch.Candidates[2].Candidate.RelativePath
			batch.Candidates[index].Input = batch.Candidates[2].Input
			batch.Candidates[index].InputDigest = batch.Candidates[2].InputDigest
		}
		batch.Candidates[18].ProtectionDomain = "second-private-domain"
		batch.MissingInputIndexes = batch.MissingInputIndexes[1:]
		before, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		expected := make(map[string]int)
		seeds := make(map[string]float32)
		groups := make(map[string]bool)
		for _, index := range batch.MissingInputIndexes {
			candidate := batch.Candidates[index]
			key := candidate.ProtectionDomain + "\x00" + string(candidate.InputDigest)
			if !groups[key] {
				groups[key] = true
				expected[candidate.Input]++
			}
			if _, found := seeds[candidate.Input]; !found {
				seeds[candidate.Input] = float32(index + 1)
			}
		}
		var mu sync.Mutex
		seen := make(map[string]int)
		calls, oversized := 0, 0
		provider := newEmbeddingCostTestClient(t, profile.Model, func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Input      []string `json:"input"`
				Model      string   `json:"model"`
				Dimensions int      `json:"dimensions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("actual SDK request decode: %v", err)
				return
			}
			if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" || request.Model != profile.Model || request.Dimensions != 1536 {
				t.Error("SDK request contract changed")
			}
			maxBytes := 0
			mu.Lock()
			calls++
			for _, input := range request.Input {
				if _, found := expected[input]; !found {
					t.Error("SDK received changed, cached, or unknown full input")
				}
				seen[input]++
				maxBytes = max(maxBytes, len(input))
			}
			if maxBytes > 65536 && len(request.Input) == 1 {
				oversized++
			}
			mu.Unlock()
			if len(request.Input) > 128 || (len(request.Input) > 1 && maxBytes*len(request.Input) > 65536) {
				t.Errorf("provider padded UTF-8 cost = %d*%d, exceeds 65536", maxBytes, len(request.Input))
			}
			type item struct {
				Embedding []float32 `json:"embedding"`
				Index     int       `json:"index"`
			}
			data := make([]item, len(request.Input))
			for index, input := range request.Input {
				data[len(data)-1-index] = item{Embedding: semanticTestVector(seeds[input]), Index: index}
			}
			if err := json.NewEncoder(w).Encode(struct {
				Data []item `json:"data"`
			}{Data: data}); err != nil {
				t.Errorf("fixture response: %v", err)
			}
		})
		worker := &EmbeddingWorker{profile: profile, embedder: provider, limits: DefaultEmbeddingWorkerLimits()}
		vectors, err := worker.embedMissing(context.Background(), claim, batch)
		if err != nil {
			t.Fatal(err)
		}
		if len(vectors) != len(batch.MissingInputIndexes) {
			t.Fatal("missing-candidate vector count changed")
		}
		for position, index := range batch.MissingInputIndexes {
			if !reflect.DeepEqual(vectors[position], semanticTestVector(seeds[batch.Candidates[index].Input])) {
				t.Fatalf("decoded response index mapped to wrong missing candidate %d", index)
			}
		}
		mu.Lock()
		if !reflect.DeepEqual(seen, expected) || calls < 2 || oversized != 1 {
			t.Errorf("full-input/domain multiplicity differs: seen=%d expected=%d calls=%d oversized=%d", len(seen), len(expected), calls, oversized)
		}
		mu.Unlock()
		after, err := json.Marshal(batch)
		if err != nil || string(after) != string(before) {
			t.Fatal("worker changed job identity, cursor, profile, cache input keys, or full candidates")
		}
	})

	for _, mode := range []string{"cancel", "missing-index", "wrong-dimension", "wrong-source", "provider-unavailable"} {
		t.Run(mode, func(t *testing.T) {
			profile := semanticTestProfile("cost-bound-errors")
			claim, batch := embeddingJobTimingTestClaimAndBatch(t, profile, 2)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var mu sync.Mutex
			calls := 0
			provider := newEmbeddingCostTestClient(t, profile.Model, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls++
				mu.Unlock()
				if mode == "cancel" {
					cancel()
					return
				}
				if mode == "provider-unavailable" {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error":{"code":"server_error"}}`))
					return
				}
				vector := semanticTestVector(1)
				if mode == "wrong-dimension" {
					vector = vector[:len(vector)-1]
				}
				data := []map[string]any{{"index": 0, "embedding": vector}}
				if mode != "missing-index" {
					data = append(data, map[string]any{"index": 1, "embedding": vector})
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"data": data}); err != nil {
					t.Errorf("fixture response: %v", err)
				}
			})
			if mode == "wrong-source" {
				batch.Job.Context.SourceID = "99999999-9999-4999-8999-999999999999"
			}
			worker := &EmbeddingWorker{profile: profile, embedder: provider, limits: DefaultEmbeddingWorkerLimits()}
			_, err := worker.embedMissing(ctx, claim, batch)
			mu.Lock()
			observedCalls := calls
			mu.Unlock()
			if mode == "cancel" {
				if !errors.Is(err, context.Canceled) || observedCalls != 1 {
					t.Fatalf("caller cancellation = %v, HTTP calls=%d, want1", err, observedCalls)
				}
				return
			}
			if mode == "provider-unavailable" {
				if classifyEmbeddingFailure(err) != (EmbeddingFailure{Code: EmbeddingFailureProviderUnavailable, Disposition: EmbeddingFailureRetry}) || observedCalls != 3 {
					t.Fatalf("typed503 semantics = %v, HTTP calls=%d, want existing3", err, observedCalls)
				}
				return
			}
			if !errors.Is(err, errEmbeddingProviderReply) || classifyEmbeddingFailure(err) != (EmbeddingFailure{Code: EmbeddingFailureMalformedResponse, Disposition: EmbeddingFailureTerminal}) {
				t.Fatalf("existing malformed/authority refusal semantics = %v", err)
			}
			wantCalls := 1
			if mode == "wrong-source" {
				wantCalls = 0
			}
			if observedCalls != wantCalls {
				t.Fatalf("HTTP calls=%d, want%d; no new retries", observedCalls, wantCalls)
			}
		})
	}
}

func newEmbeddingCostTestClient(t *testing.T, model string, handler http.HandlerFunc) *embedding.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Setenv("ENGRAM_EMBEDDING_URL", server.URL+"/v1")
	t.Setenv("ENGRAM_EMBEDDING_MODEL", model)
	t.Setenv("ENGRAM_EMBEDDING_DIMENSIONS", "1536")
	t.Setenv("ENGRAM_EMBEDDING_API_KEY", "")
	client, err := embedding.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	return client
}
