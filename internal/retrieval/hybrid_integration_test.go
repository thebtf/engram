package retrieval_test

// DSN-gated integration tests for HybridSearch.
// These tests require a live PostgreSQL database with the engram schema.
// They are skipped unless ENGRAM_TEST_DSN is set.
//
// Run with:
//   ENGRAM_TEST_DSN="host=localhost user=engram password=... dbname=engram_test" \
//   go test ./internal/retrieval/... -run Integration -v

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/retrieval"
	"github.com/thebtf/engram/pkg/models"
)

func skipIfNoDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		dsn = os.Getenv("ENGRAM_TEST_DSN")
	}
	if dsn == "" {
		t.Skip("DATABASE_DSN not set — skipping integration test")
	}
	return dsn
}

func openTestStore(t *testing.T, dsn string) *gormdb.Store {
	t.Helper()
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open test DB: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close test DB: %v", err)
		}
	})
	return store
}

// TestIntegration_FTS exercises SearchFTS against a real DB with seed data.
func TestIntegration_FTS(t *testing.T) {
	dsn := skipIfNoDSN(t)
	dbStore := openTestStore(t, dsn)
	store := gormdb.NewMemoryStore(dbStore)
	ctx := context.Background()
	project := "test-integration-" + t.Name()

	// Seed a memory.
	seed := &models.Memory{
		Project:        project,
		Content:        "integration test: the quick brown fox jumps",
		Status:         "active",
		ImportanceBase: 0.5,
		CreatedAt:      time.Now(),
	}
	created, err := store.Create(ctx, seed)
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Delete(ctx, created.ID)
	})

	rows, err := store.SearchFTS(ctx, project, "quick brown fox", 10)
	if err != nil {
		t.Fatalf("SearchFTS: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("SearchFTS did not return seeded memory (id=%d)", created.ID)
	}
}

// TestIntegration_HybridFTSOnly runs HybridSearch without embeddings to validate the FTS path.
func TestIntegration_HybridFTSOnly(t *testing.T) {
	dsn := skipIfNoDSN(t)
	dbStore := openTestStore(t, dsn)
	store := gormdb.NewMemoryStore(dbStore)
	ctx := context.Background()
	project := "test-integration-hybrid-" + t.Name()

	seed := &models.Memory{
		Project:        project,
		Content:        "hybrid search integration: persistent memory retrieval",
		Status:         "active",
		ImportanceBase: 0.6,
		CreatedAt:      time.Now(),
	}
	created, err := store.Create(ctx, seed)
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Delete(ctx, created.ID)
	})

	scored, _, err := retrieval.HybridSearch(
		ctx, project, "persistent memory retrieval", 5,
		store, nil, nil,
		retrieval.HybridOptions{Explain: true},
	)
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	found := false
	for _, sm := range scored {
		if sm.Memory.ID == created.ID {
			found = true
			if sm.Score <= 0 {
				t.Errorf("expected positive score, got %v", sm.Score)
			}
		}
	}
	if !found {
		t.Errorf("HybridSearch did not return seeded memory (id=%d); got %d results", created.ID, len(scored))
	}
}

// TestIntegration_HybridWithVector proves the vector leg independently of any
// external embedding service or lexical fallback.
func TestIntegration_HybridWithVector(t *testing.T) {
	dsn := skipIfNoDSN(t)
	const query = "orchid nebula"
	const model = "mb1-deterministic"
	vector := make([]float32, embedding.EmbeddingDim)
	vector[0] = 1
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		var request struct {
			Input      []string `json:"input"`
			Model      string   `json:"model"`
			Dimensions int      `json:"dimensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil ||
			request.Model != model || request.Dimensions != embedding.EmbeddingDim ||
			len(request.Input) != 1 || request.Input[0] != query {
			http.Error(w, fmt.Sprintf("invalid embedding request: %#v err=%v", request, err), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []any{map[string]any{"index": 0, "embedding": vector}},
		})
	}))
	t.Cleanup(endpoint.Close)
	t.Setenv("ENGRAM_EMBEDDING_URL", endpoint.URL)
	t.Setenv("ENGRAM_EMBEDDING_MODEL", model)
	t.Setenv("ENGRAM_EMBEDDING_DIMENSIONS", "")

	dbStore := openTestStore(t, dsn)
	store := gormdb.NewMemoryStore(dbStore)
	embStore := embedding.NewStore(dbStore.DB)

	embClient, err := embedding.NewClient()
	if err != nil {
		t.Fatalf("embedding client: %v", err)
	}

	ctx := context.Background()
	project := "test-integration-vector-" + t.Name()

	seed := &models.Memory{
		Project:        project,
		Content:        "vector-only seed with no lexical query terms",
		Status:         "active",
		ImportanceBase: 0.7,
		CreatedAt:      time.Now(),
	}
	created, err := store.Create(ctx, seed)
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	t.Cleanup(func() {
		_ = dbStore.DB.Exec("DELETE FROM content_chunks WHERE memory_id = ?", created.ID).Error
		_ = store.Delete(ctx, created.ID)
	})
	if err := embStore.StoreChunks(ctx, []embedding.Chunk{{
		MemoryID:  created.ID,
		Seq:       0,
		Text:      seed.Content,
		Embedding: pgvector.NewVector(vector),
		Model:     model,
	}}); err != nil {
		t.Fatalf("persist seed vector: %v", err)
	}
	ftsRows, err := store.SearchFTS(ctx, project, query, 10)
	if err != nil {
		t.Fatalf("lexical miss precondition: %v", err)
	}
	for _, row := range ftsRows {
		if row.ID == created.ID {
			t.Fatalf("lexical precondition failed: seeded memory matched FTS query")
		}
	}

	// Embed query.
	vecs, err := embClient.Embed(ctx, []string{query})
	if err != nil {
		t.Fatalf("embed query: %v", err)
	}
	queryVec := vecs[0]

	scored, explanations, err := retrieval.HybridSearch(
		ctx, project, query, 5,
		store, embStore, nil,
		retrieval.HybridOptions{QueryVec: queryVec, Explain: true},
	)
	if err != nil {
		t.Fatalf("HybridSearch with vector: %v", err)
	}
	// Verify that the seeded memory is present in results and carries a positive score.
	found := false
	for _, sm := range scored {
		if sm.Memory.ID == created.ID {
			found = true
			if sm.Relevance <= 0 || sm.Score <= 0 {
				t.Errorf("expected positive vector relevance and fused score, got relevance=%v score=%v", sm.Relevance, sm.Score)
			}
		}
	}
	if !found {
		t.Errorf("HybridSearch did not return seeded memory (id=%d); got %d results", created.ID, len(scored))
	}
	vectorExplained := false
	for _, explanation := range explanations {
		if explanation.MemoryID == created.ID {
			vectorExplained = true
			if explanation.SourceTier != "tier1_vector" {
				t.Fatalf("seeded lexical miss came from %q, want tier1_vector", explanation.SourceTier)
			}
		}
	}
	if !vectorExplained {
		t.Fatal("seeded memory lacked vector-tier explanation")
	}
}

func TestIntegration_VectorEndpointMalformedResponseFailsExplicitly(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":`))
	}))
	t.Cleanup(endpoint.Close)
	t.Setenv("ENGRAM_EMBEDDING_URL", endpoint.URL)
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "mb1-malformed")
	t.Setenv("ENGRAM_EMBEDDING_DIMENSIONS", "")
	client, err := embedding.NewClient()
	if err != nil {
		t.Fatalf("new embedding client: %v", err)
	}

	_, err = client.Embed(context.Background(), []string{"explicit endpoint failure"})
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("malformed embedding response must fail explicitly, got %v", err)
	}
}

func TestIntegration_VectorWrongDimensionCannotPersistOrProduceVectorHit(t *testing.T) {
	dsn := skipIfNoDSN(t)
	query := "orchid nebula"
	wrongVector := make([]float32, embedding.EmbeddingDim-1)
	wrongVector[0] = 1
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []any{map[string]any{"index": 0, "embedding": wrongVector}},
		})
	}))
	t.Cleanup(endpoint.Close)
	t.Setenv("ENGRAM_EMBEDDING_URL", endpoint.URL)
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "mb1-wrong-dimension")
	t.Setenv("ENGRAM_EMBEDDING_DIMENSIONS", "")

	client, err := embedding.NewClient()
	if err != nil {
		t.Fatalf("new embedding client: %v", err)
	}
	vectors, err := client.Embed(context.Background(), []string{query})
	if err != nil {
		t.Fatalf("embed wrong-dimension fixture: %v", err)
	}
	if len(vectors) != 1 || len(vectors[0]) != embedding.EmbeddingDim-1 {
		t.Fatalf("wrong-dimension fixture was not returned as designed: %d", len(vectors[0]))
	}

	dbStore := openTestStore(t, dsn)
	memoryStore := gormdb.NewMemoryStore(dbStore)
	embStore := embedding.NewStore(dbStore.DB)
	project := fmt.Sprintf("hybrid-wrong-dim-%d", time.Now().UnixNano())
	seed, err := memoryStore.Create(context.Background(), &models.Memory{
		Project: project,
		Content: "wrong sized vector seed without query terms",
		Status:  "active",
	})
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	t.Cleanup(func() {
		_ = dbStore.DB.Exec("DELETE FROM content_chunks WHERE memory_id = ?", seed.ID).Error
		_ = memoryStore.Delete(context.Background(), seed.ID)
	})

	if err := embStore.StoreChunks(context.Background(), []embedding.Chunk{{
		MemoryID:  seed.ID,
		Seq:       0,
		Text:      seed.Content,
		Embedding: pgvector.NewVector(vectors[0]),
		Model:     "mb1-wrong-dimension",
	}}); err != nil {
		t.Fatalf("dimension guard returned unexpected storage error: %v", err)
	}
	var chunks int64
	if err := dbStore.DB.Table("content_chunks").Where("memory_id = ?", seed.ID).Count(&chunks).Error; err != nil {
		t.Fatalf("count wrong-dimension chunks: %v", err)
	}
	if chunks != 0 {
		t.Fatalf("wrong-dimension endpoint vector persisted %d chunks", chunks)
	}

	results, explanations, err := retrieval.HybridSearch(
		context.Background(), project, query, 5,
		memoryStore, embStore, nil,
		retrieval.HybridOptions{QueryVec: vectors[0], Explain: true},
	)
	if err != nil {
		t.Fatalf("hybrid wrong-dimension path must degrade without false success: %v", err)
	}
	for _, result := range results {
		if result.Memory.ID == seed.ID {
			t.Fatal("wrong-dimension endpoint vector produced a false vector hit")
		}
	}
	for _, explanation := range explanations {
		if explanation.MemoryID == seed.ID && explanation.SourceTier == "tier1_vector" {
			t.Fatal("wrong-dimension endpoint vector produced a tier1_vector explanation")
		}
	}
}
