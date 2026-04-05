//go:build benchmark

package benchmark

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBenchmarkSuite(t *testing.T) {
	dsn := os.Getenv("ENGRAM_BENCH_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_BENCH_DSN not set, skipping benchmark")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}

	t.Cleanup(func() {
		_ = DropBenchTables(ctx, pool)
		pool.Close()
	})

	if err := CreateBenchTables(ctx, pool); err != nil {
		t.Fatalf("create bench tables: %v", err)
	}

	t.Log("Seeding 1M observations, 5M relations, 1M vectors (this takes a few minutes)...")
	seedStart := time.Now()
	if err := SeedData(ctx, pool); err != nil {
		t.Fatalf("seed data: %v", err)
	}
	t.Logf("Seeding complete in %v", time.Since(seedStart))

	ids := loadObsIDs(ctx, pool, t)
	if len(ids) == 0 {
		t.Fatalf("no observation ids loaded")
	}

	rng := rand.New(rand.NewSource(42))

	t.Run("Q1_1hop", func(t *testing.T) {
		h := NewHistogram()
		for i := 0; i < 100; i++ {
			q1Query(ctx, pool, ids[rng.Intn(len(ids))])
		}

		for i := 0; i < 1000; i++ {
			id := ids[rng.Intn(len(ids))]
			start := time.Now()
			q1Query(ctx, pool, id)
			h.Add(time.Since(start))
		}
		h.Print("Q1 1-hop (bench_relations)")
	})

	t.Run("Q2_2hop", func(t *testing.T) {
		h := NewHistogram()
		for i := 0; i < 100; i++ {
			bfsQuery(ctx, pool, ids[rng.Intn(len(ids))], 2)
		}

		for i := 0; i < 1000; i++ {
			id := ids[rng.Intn(len(ids))]
			start := time.Now()
			bfsQuery(ctx, pool, id, 2)
			h.Add(time.Since(start))
		}
		h.Print("Q2 2-hop BFS")
	})

	t.Run("Q3_3hop", func(t *testing.T) {
		h := NewHistogram()
		for i := 0; i < 100; i++ {
			queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			bfsQuery(queryCtx, pool, ids[rng.Intn(len(ids))], 3)
			cancel()
		}

		for i := 0; i < 1000; i++ {
			id := ids[rng.Intn(len(ids))]
			queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			start := time.Now()
			bfsQuery(queryCtx, pool, id, 3)
			h.Add(time.Since(start))
			cancel()
		}
		h.Print("Q3 3-hop BFS")
	})

	t.Run("Q4_hybrid", func(t *testing.T) {
		h := NewHistogram()
		localRng := rand.New(rand.NewSource(99))
		for i := 0; i < 100; i++ {
			hybridQuery(ctx, pool, randomUnitVec(localRng))
		}

		for i := 0; i < 1000; i++ {
			qvec := randomUnitVec(localRng)
			start := time.Now()
			hybridQuery(ctx, pool, qvec)
			h.Add(time.Since(start))
		}
		h.Print("Q4 hybrid (vector+1hop)")
	})

	t.Run("Q5_decay", func(t *testing.T) {
		start := time.Now()
		var offset int64
		batchSize := int64(500)
		for offset < benchObsCount {
			batchIDs := loadBatchIDs(ctx, pool, offset, batchSize)
			if len(batchIDs) == 0 {
				break
			}
			decayBatch(ctx, pool, batchIDs)
			offset += batchSize
		}

		total := time.Since(start)
		t.Logf("Q5 decay cycle total: %v for %d obs in batches of %d", total, benchObsCount, batchSize)
		fmt.Printf("%-30s total=%v batches=%d\n", "Q5 decay cycle", total, benchObsCount/int(batchSize))
	})

	t.Run("Concurrency", func(t *testing.T) {
		var (
			readOps  int64
			writeOps int64
			decayOps int64
			mu       sync.Mutex
		)
		done := make(chan struct{})
		var wg sync.WaitGroup

		for i := 0; i < 4; i++ {
			workerID := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				localRng := rand.New(rand.NewSource(int64(workerID) * 1000))
				for {
					select {
					case <-done:
						return
					default:
						id := ids[localRng.Intn(len(ids))]
						q1Query(ctx, pool, id)
						mu.Lock()
						readOps++
						mu.Unlock()
					}
				}
			}()
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			localRng := rand.New(rand.NewSource(9999))
			for {
				select {
				case <-done:
					return
				default:
					src := ids[localRng.Intn(len(ids))]
					tgt := ids[localRng.Intn(len(ids))]
					if src == tgt {
						continue
					}
					_, _ = pool.Exec(ctx,
						`INSERT INTO bench_relations(source_id, target_id, relation_type, confidence, detection_source, created_at, created_at_epoch)
						 VALUES($1, $2, 'relates_to', 0.5, 'embedding_similarity', $3, $4)
						 ON CONFLICT ON CONSTRAINT bench_rel_unique DO NOTHING`,
						src,
						tgt,
						time.Now().UTC().Format(time.RFC3339),
						time.Now().UnixMilli(),
					)
					mu.Lock()
					writeOps++
					mu.Unlock()
				}
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					batch := ids[:500]
					decayBatch(ctx, pool, batch)
					mu.Lock()
					decayOps++
					mu.Unlock()
				}
			}
		}()

		time.Sleep(30 * time.Second)
		close(done)
		wg.Wait()

		mu.Lock()
		r, w, d := readOps, writeOps, decayOps
		mu.Unlock()

		t.Logf("Concurrency (30s): reads=%d writes=%d decay_batches=%d", r, w, d)
		fmt.Printf("%-30s reads=%d writes=%d decay_batches=%d\n", "Concurrency (30s)", r, w, d)
	})

	fmt.Println("\n=== Benchmark Summary ===")
}

func loadObsIDs(ctx context.Context, pool *pgxpool.Pool, t *testing.T) []int64 {
	rows, err := pool.Query(ctx, `SELECT id FROM bench_observations ORDER BY random() LIMIT 10000`)
	if err != nil {
		t.Fatalf("load observation ids: %v", err)
	}
	defer rows.Close()

	ids := make([]int64, 0, 10000)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan observation id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate observation ids: %v", err)
	}
	return ids
}

func q1Query(ctx context.Context, pool *pgxpool.Pool, obsID int64) {
	query := strings.Join([]string{
		"SELECT source_id, target_id, relation_type, confidence",
		"FROM bench_relations",
		"WHERE source_id=$1 OR target_id=$1",
	}, " ")

	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	rows, err := pool.Query(queryCtx, query, obsID)
	if err != nil {
		if queryCtx.Err() != nil && queryCtx.Err() != context.Canceled && queryCtx.Err() != context.DeadlineExceeded {
			panic(err)
		}
		return
	}
	defer rows.Close()

	var src, tgt int64
	var relType string
	var confidence float64
	for rows.Next() {
		if err := rows.Scan(&src, &tgt, &relType, &confidence); err != nil {
			panic(err)
		}
	}
	if err := rows.Err(); err != nil {
		if queryCtx.Err() != nil && queryCtx.Err() != context.Canceled && queryCtx.Err() != context.DeadlineExceeded {
			panic(err)
		}
	}
}

func bfsQuery(ctx context.Context, pool *pgxpool.Pool, centerID int64, maxDepth int) {
	query := strings.Join([]string{
		"SELECT CASE WHEN source_id=$1 THEN target_id ELSE source_id END AS neighbor_id",
		"FROM bench_relations",
		"WHERE source_id=$1 OR target_id=$1",
	}, " ")

	visited := map[int64]bool{centerID: true}
	frontier := []int64{centerID}

	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		nextFrontier := make([]int64, 0)
		for _, obsID := range frontier {
			queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			rows, err := pool.Query(queryCtx, query, obsID)
			if err != nil {
				if queryCtx.Err() == nil {
					panic(err)
				}
				cancel()
				continue
			}

			for rows.Next() {
				var neighborID int64
				if err := rows.Scan(&neighborID); err != nil {
					rows.Close()
					cancel()
					panic(err)
				}
				if !visited[neighborID] {
					visited[neighborID] = true
					nextFrontier = append(nextFrontier, neighborID)
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				cancel()
				if queryCtx.Err() == nil {
					panic(err)
				}
				continue
			}
			rows.Close()
			cancel()
		}

		if len(nextFrontier) == 0 {
			return
		}
		frontier = nextFrontier
	}
}

func hybridQuery(ctx context.Context, pool *pgxpool.Pool, queryVec []float32) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	rows, err := pool.Query(queryCtx, `SELECT observation_id FROM bench_vectors ORDER BY embedding <-> $1::vector LIMIT 20`, formatVector(queryVec))
	if err != nil {
		cancel()
		if queryCtx.Err() == nil {
			panic(err)
		}
		return
	}

	defer rows.Close()
	ids := make([]int64, 0, 20)
	for rows.Next() {
		var obsID int64
		if err := rows.Scan(&obsID); err != nil {
			panic(err)
		}
		ids = append(ids, obsID)
	}
	if err := rows.Err(); err != nil {
		if queryCtx.Err() == nil {
			panic(err)
		}
	}
	cancel()

	for _, obsID := range ids {
		q1Query(ctx, pool, obsID)
	}
}

func loadBatchIDs(ctx context.Context, pool *pgxpool.Pool, offset, limit int64) []int64 {
	rows, err := pool.Query(ctx, `SELECT id FROM bench_observations ORDER BY id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		panic(err)
	}
	defer rows.Close()

	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			panic(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		panic(err)
	}
	return ids
}

func decayBatch(ctx context.Context, pool *pgxpool.Pool, obsIDs []int64) {
	if len(obsIDs) == 0 {
		return
	}

	countRow := pool.QueryRow(ctx, `SELECT COUNT(*) FROM bench_relations WHERE source_id = ANY($1) OR target_id = ANY($1)`, obsIDs)
	var count int64
	if err := countRow.Scan(&count); err != nil {
		panic(err)
	}

	avgRow := pool.QueryRow(ctx, `SELECT AVG(confidence) FROM bench_relations WHERE source_id = ANY($1) OR target_id = ANY($1)`, obsIDs)
	var avg float64
	if err := avgRow.Scan(&avg); err != nil {
		_ = count
		panic(err)
	}
	_ = avg
	_ = count
}
