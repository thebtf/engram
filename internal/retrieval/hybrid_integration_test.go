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
	"os"
	"testing"
	"time"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/retrieval"
	"github.com/thebtf/engram/pkg/models"
)

func skipIfNoDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("ENGRAM_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_TEST_DSN not set — skipping integration test")
	}
	return dsn
}

func openTestStore(t *testing.T, dsn string) *gormdb.Store {
	t.Helper()
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open test DB: %v", err)
	}
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

// TestIntegration_HybridWithVector runs HybridSearch when ENGRAM_EMBEDDING_URL is set.
func TestIntegration_HybridWithVector(t *testing.T) {
	dsn := skipIfNoDSN(t)
	embURL := os.Getenv("ENGRAM_EMBEDDING_URL")
	if embURL == "" {
		t.Skip("ENGRAM_EMBEDDING_URL not set — skipping vector integration test")
	}

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
		Content:        "vector search: semantic similarity for memory retrieval systems",
		Status:         "active",
		ImportanceBase: 0.7,
		CreatedAt:      time.Now(),
	}
	created, err := store.Create(ctx, seed)
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Delete(ctx, created.ID)
	})

	// Embed query.
	vecs, err := embClient.Embed(ctx, []string{"semantic similarity retrieval"})
	if err != nil {
		t.Fatalf("embed query: %v", err)
	}
	queryVec := vecs[0]

	scored, _, err := retrieval.HybridSearch(
		ctx, project, "semantic similarity retrieval", 5,
		store, embStore, nil,
		retrieval.HybridOptions{QueryVec: queryVec},
	)
	if err != nil {
		t.Fatalf("HybridSearch with vector: %v", err)
	}
	// Verify that the seeded memory is present in results and carries a positive score.
	found := false
	for _, sm := range scored {
		if sm.Memory.ID == created.ID {
			found = true
			if sm.Score <= 0 {
				t.Errorf("expected positive score for seeded memory, got %v", sm.Score)
			}
		}
	}
	if !found {
		t.Errorf("HybridSearch did not return seeded memory (id=%d); got %d results", created.ID, len(scored))
	}
}
