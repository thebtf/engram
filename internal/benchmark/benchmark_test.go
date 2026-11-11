//go:build benchmark

package benchmark

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	mathrand "math/rand"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func benchmarkConfig(dsn string) (*pgxpool.Config, string, error) {
	if os.Getenv("ENGRAM_BENCH_DISPOSABLE_DB") != "yes" {
		return nil, "", fmt.Errorf("ENGRAM_BENCH_DISPOSABLE_DB=yes is required for an exclusively disposable database")
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "PG") {
			return nil, "", fmt.Errorf("unset PostgreSQL environment variable %s", name)
		}
	}
	u, err := url.Parse(dsn)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil || u.User.Username() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, "", fmt.Errorf("benchmark DSN must be a PostgreSQL URL without query parameters or fragment")
	}
	password, hasPassword := u.User.Password()
	host, port, hostErr := net.SplitHostPort(u.Host)
	portNumber, portErr := strconv.Atoi(port)
	if !hasPassword || password == "" || hostErr != nil || host == "" || portErr != nil || portNumber < 1 || portNumber > 65535 || strings.ContainsAny(u.Host, ",/") || len(u.Path) < 2 || strings.Contains(u.Path[1:], "/") {
		return nil, "", fmt.Errorf("benchmark DSN must specify one host, port, user, password and database")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, "", fmt.Errorf("generate schema name: %w", err)
	}
	schema := "engram_bench_" + hex.EncodeToString(random[:])
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, "", fmt.Errorf("invalid benchmark DSN")
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	return config, schema, nil
}

func openBenchmarkFixture(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("ENGRAM_BENCH_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_BENCH_DSN not set, skipping benchmark")
	}

	config, schema, err := benchmarkConfig(dsn)
	if err != nil {
		t.Fatalf("benchmark configuration: %v", err)
	}
	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatalf("create benchmark schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("drop benchmark schema %s: %v", schema, err)
		}
	})
	if _, err := pool.Exec(ctx, "CREATE EXTENSION vector WITH SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatalf("create benchmark vector extension (requires an empty disposable database): %v", err)
	}

	if err := CreateBenchTables(ctx, pool); err != nil {
		t.Fatalf("create bench tables: %v", err)
	}
	return pool
}

func TestBenchmarkFixtureIsolation(t *testing.T) {
	pool := openBenchmarkFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, benchSQL(pool, `INSERT INTO bench_observations(id, project) VALUES (1, 'fixture')`)); err != nil {
		t.Fatalf("insert isolated benchmark row: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, benchSQL(pool, `SELECT count(*) FROM bench_observations WHERE project = 'fixture'`)).Scan(&count); err != nil || count != 1 {
		t.Fatalf("isolated benchmark row count = %d, error = %v", count, err)
	}
	relation := relationRecord{sourceID: 1, targetID: 2, relationType: "fixture", confidence: 0.5, detectionSource: "fixture", createdAt: time.Now().UTC().Format(time.RFC3339)}
	if err := sendRelationBatch(ctx, pool, []relationRecord{relation}); err != nil {
		t.Fatalf("insert isolated relation: %v", err)
	}
	vector := randomUnitVec(mathrand.New(mathrand.NewSource(1)))
	if _, err := pool.Exec(ctx, benchSQL(pool, `INSERT INTO bench_vectors(observation_id, embedding) VALUES (1, $1::vector)`), formatVector(vector)); err != nil {
		t.Fatalf("insert isolated vector: %v", err)
	}
	if err := q1Query(ctx, pool, 1); err != nil {
		t.Fatalf("one-hop query: %v", err)
	}
	if err := bfsQuery(ctx, pool, 1, 2); err != nil {
		t.Fatalf("BFS query: %v", err)
	}
	if err := hybridQuery(ctx, pool, vector); err != nil {
		t.Fatalf("hybrid query: %v", err)
	}
	if err := aggregateBatch(ctx, pool, []int64{1}); err != nil {
		t.Fatalf("aggregate with relation: %v", err)
	}
	if err := aggregateBatch(ctx, pool, []int64{999}); err != nil {
		t.Fatalf("aggregate without relation: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := q1Query(cancelled, pool, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query error = %v, want context cancellation", err)
	}
	if _, err := pool.Exec(ctx, benchSQL(pool, `DROP TABLE bench_relations`)); err != nil {
		t.Fatalf("drop owned relation table: %v", err)
	}
	if err := q1Query(ctx, pool, 1); err == nil {
		t.Fatal("one-hop query accepted a missing relation table")
	}
	if err := bfsQuery(ctx, pool, 1, 2); err == nil {
		t.Fatal("BFS query accepted a missing relation table")
	}
	if err := hybridQuery(ctx, pool, vector); err == nil {
		t.Fatal("hybrid query accepted a missing relation table")
	}
	if err := aggregateBatch(ctx, pool, []int64{1}); err == nil {
		t.Fatal("aggregate accepted a missing relation table")
	}
}

func TestBenchmarkSuite(t *testing.T) {
	pool := openBenchmarkFixture(t)
	ctx := context.Background()

	t.Log("Seeding 1M observations, 5M relations, 1M vectors (this takes a few minutes)...")
	seedStart := time.Now()
	if err := SeedData(ctx, pool); err != nil {
		t.Fatalf("seed data: %v", err)
	}
	t.Logf("Seeding complete in %v", time.Since(seedStart))

	ids := loadObsIDs(ctx, pool, t)
	if len(ids) < 500 {
		t.Fatalf("loaded %d observation ids; concurrency workload needs at least 500", len(ids))
	}

	rng := mathrand.New(mathrand.NewSource(42))

	t.Run("Q1_1hop", func(t *testing.T) {
		h := NewHistogram()
		for i := 0; i < 100; i++ {
			if err := q1Query(ctx, pool, ids[rng.Intn(len(ids))]); err != nil {
				t.Fatalf("Q1 warmup: %v", err)
			}
		}

		for i := 0; i < 1000; i++ {
			id := ids[rng.Intn(len(ids))]
			start := time.Now()
			if err := q1Query(ctx, pool, id); err != nil {
				t.Fatalf("Q1 query: %v", err)
			}
			h.Add(time.Since(start))
		}
		h.Print("Q1 1-hop (bench_relations)")
	})

	t.Run("Q2_2hop", func(t *testing.T) {
		h := NewHistogram()
		for i := 0; i < 100; i++ {
			if err := bfsQuery(ctx, pool, ids[rng.Intn(len(ids))], 2); err != nil {
				t.Fatalf("Q2 warmup: %v", err)
			}
		}

		for i := 0; i < 1000; i++ {
			id := ids[rng.Intn(len(ids))]
			start := time.Now()
			if err := bfsQuery(ctx, pool, id, 2); err != nil {
				t.Fatalf("Q2 query: %v", err)
			}
			h.Add(time.Since(start))
		}
		h.Print("Q2 2-hop BFS")
	})

	t.Run("Q3_3hop", func(t *testing.T) {
		h := NewHistogram()
		for i := 0; i < 100; i++ {
			queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := bfsQuery(queryCtx, pool, ids[rng.Intn(len(ids))], 3)
			cancel()
			if err != nil {
				t.Fatalf("Q3 warmup: %v", err)
			}
		}

		for i := 0; i < 1000; i++ {
			id := ids[rng.Intn(len(ids))]
			queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			start := time.Now()
			err := bfsQuery(queryCtx, pool, id, 3)
			elapsed := time.Since(start)
			cancel()
			if err != nil {
				t.Fatalf("Q3 query: %v", err)
			}
			h.Add(elapsed)
		}
		h.Print("Q3 3-hop BFS")
	})

	t.Run("Q4_hybrid", func(t *testing.T) {
		h := NewHistogram()
		localRng := mathrand.New(mathrand.NewSource(99))
		for i := 0; i < 100; i++ {
			if err := hybridQuery(ctx, pool, randomUnitVec(localRng)); err != nil {
				t.Fatalf("Q4 warmup: %v", err)
			}
		}

		for i := 0; i < 1000; i++ {
			qvec := randomUnitVec(localRng)
			start := time.Now()
			if err := hybridQuery(ctx, pool, qvec); err != nil {
				t.Fatalf("Q4 query: %v", err)
			}
			h.Add(time.Since(start))
		}
		h.Print("Q4 hybrid (vector+1hop)")
	})

	t.Run("Q5_aggregate_reads", func(t *testing.T) {
		start := time.Now()
		var offset, batches int64
		batchSize := int64(500)
		for offset < benchObsCount {
			batchIDs := loadBatchIDs(ctx, pool, offset, batchSize)
			if len(batchIDs) == 0 {
				t.Fatalf("aggregate scan ended after %d of %d observations", offset, benchObsCount)
			}
			if err := aggregateBatch(ctx, pool, batchIDs); err != nil {
				t.Fatalf("aggregate batch: %v", err)
			}
			offset += int64(len(batchIDs))
			batches++
		}

		total := time.Since(start)
		t.Logf("Q5 aggregate reads total: %v for %d observations in %d batches", total, offset, batches)
		fmt.Printf("%-30s total=%v observations=%d batches=%d\n", "Q5 aggregate reads", total, offset, batches)
	})

	t.Run("Concurrency", func(t *testing.T) {
		var (
			readOps, insertAttempts, inserted, conflicts, aggregateOps, interrupted int64
			mu                                                                      sync.Mutex
		)
		workCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var wg sync.WaitGroup
		recordError := func(operation string, err error) {
			if workCtx.Err() != nil && errors.Is(err, workCtx.Err()) {
				mu.Lock()
				interrupted++
				mu.Unlock()
				return
			}
			t.Errorf("concurrency %s: %v", operation, err)
			cancel()
		}

		for i := range 4 {
			workerID := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				localRng := mathrand.New(mathrand.NewSource(int64(workerID) * 1000))
				for workCtx.Err() == nil {
					id := ids[localRng.Intn(len(ids))]
					if err := q1Query(workCtx, pool, id); err != nil {
						recordError("read", err)
						return
					}
					mu.Lock()
					readOps++
					mu.Unlock()
				}
			}()
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			localRng := mathrand.New(mathrand.NewSource(9999))
			for workCtx.Err() == nil {
				src := ids[localRng.Intn(len(ids))]
				tgt := ids[localRng.Intn(len(ids))]
				if src == tgt {
					continue
				}
				mu.Lock()
				insertAttempts++
				mu.Unlock()
				tag, err := pool.Exec(workCtx,
					benchSQL(pool, `INSERT INTO bench_relations(source_id, target_id, relation_type, confidence, detection_source, created_at, created_at_epoch)
					 VALUES($1, $2, 'relates_to', 0.5, 'embedding_similarity', $3, $4)
					 ON CONFLICT ON CONSTRAINT bench_rel_unique DO NOTHING`),
					src, tgt, time.Now().UTC().Format(time.RFC3339), time.Now().UnixMilli(),
				)
				if err != nil {
					recordError("insert", err)
					return
				}
				mu.Lock()
				switch tag.RowsAffected() {
				case 1:
					inserted++
				case 0:
					conflicts++
				default:
					mu.Unlock()
					recordError("insert", fmt.Errorf("unexpected affected row count: %d", tag.RowsAffected()))
					return
				}
				mu.Unlock()
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			for workCtx.Err() == nil {
				if err := aggregateBatch(workCtx, pool, ids[:500]); err != nil {
					recordError("aggregate read", err)
					return
				}
				mu.Lock()
				aggregateOps++
				mu.Unlock()
			}
		}()
		wg.Wait()

		t.Logf("Concurrency (up to 30s): reads=%d insert_attempts=%d inserted=%d conflicts=%d aggregate_batches=%d interrupted=%d", readOps, insertAttempts, inserted, conflicts, aggregateOps, interrupted)
		fmt.Printf("%-30s reads=%d insert_attempts=%d inserted=%d conflicts=%d aggregate_batches=%d interrupted=%d\n", "Concurrency (up to 30s)", readOps, insertAttempts, inserted, conflicts, aggregateOps, interrupted)
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

func q1Query(ctx context.Context, pool *pgxpool.Pool, obsID int64) error {
	query := strings.Join([]string{
		"SELECT source_id, target_id, relation_type, confidence",
		"FROM bench_relations",
		"WHERE source_id=$1 OR target_id=$1",
	}, " ")

	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	rows, err := pool.Query(queryCtx, query, obsID)
	if err != nil {
		return fmt.Errorf("query one-hop relations: %w", err)
	}
	defer rows.Close()

	var src, tgt int64
	var relType string
	var confidence float64
	for rows.Next() {
		if err := rows.Scan(&src, &tgt, &relType, &confidence); err != nil {
			return fmt.Errorf("scan one-hop relation: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate one-hop relations: %w", err)
	}
	return nil
}

func bfsQuery(ctx context.Context, pool *pgxpool.Pool, centerID int64, maxDepth int) error {
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
				cancel()
				return fmt.Errorf("query BFS relations: %w", err)
			}

			for rows.Next() {
				var neighborID int64
				if err := rows.Scan(&neighborID); err != nil {
					rows.Close()
					cancel()
					return fmt.Errorf("scan BFS relation: %w", err)
				}
				if !visited[neighborID] {
					visited[neighborID] = true
					nextFrontier = append(nextFrontier, neighborID)
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				cancel()
				return fmt.Errorf("iterate BFS relations: %w", err)
			}
			rows.Close()
			cancel()
		}

		if len(nextFrontier) == 0 {
			return nil
		}
		frontier = nextFrontier
	}
	return nil
}

func hybridQuery(ctx context.Context, pool *pgxpool.Pool, queryVec []float32) error {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	rows, err := pool.Query(queryCtx, `SELECT observation_id FROM bench_vectors ORDER BY embedding <-> $1::vector LIMIT 20`, formatVector(queryVec))
	if err != nil {
		cancel()
		return fmt.Errorf("query nearest vectors: %w", err)
	}

	defer rows.Close()
	ids := make([]int64, 0, 20)
	for rows.Next() {
		var obsID int64
		if err := rows.Scan(&obsID); err != nil {
			cancel()
			return fmt.Errorf("scan nearest vector: %w", err)
		}
		ids = append(ids, obsID)
	}
	if err := rows.Err(); err != nil {
		cancel()
		return fmt.Errorf("iterate nearest vectors: %w", err)
	}
	rows.Close()
	cancel()

	for _, obsID := range ids {
		if err := q1Query(ctx, pool, obsID); err != nil {
			return fmt.Errorf("hybrid one-hop lookup: %w", err)
		}
	}
	return nil
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

func aggregateBatch(ctx context.Context, pool *pgxpool.Pool, obsIDs []int64) error {
	if len(obsIDs) == 0 {
		return nil
	}

	countRow := pool.QueryRow(ctx, `SELECT COUNT(*) FROM bench_relations WHERE source_id = ANY($1) OR target_id = ANY($1)`, obsIDs)
	var count int64
	if err := countRow.Scan(&count); err != nil {
		return fmt.Errorf("count relations: %w", err)
	}

	avgRow := pool.QueryRow(ctx, `SELECT AVG(confidence) FROM bench_relations WHERE source_id = ANY($1) OR target_id = ANY($1)`, obsIDs)
	var avg *float64
	if err := avgRow.Scan(&avg); err != nil {
		return fmt.Errorf("average relation confidence: %w", err)
	}
	return nil
}
