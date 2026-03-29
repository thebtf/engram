//go:build benchmark

package benchmark

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	benchObsCount = 1_000_000
	benchRelCount = 5_000_000
	benchVecCount = 1_000_000
	seedBatchSize = 10_000
	relBatchSize  = 1_000
	vecDims       = 384

	observationCount = benchObsCount
	relationCount    = benchRelCount
	vectorCount      = benchVecCount
	relationSendSize = relBatchSize
	vectorDimensions = vecDims
)

var (
	obsTypes       = []string{"feature", "bugfix", "discovery", "decision", "refactor", "change"}
	obsTypeWeights = []float64{0.40, 0.20, 0.15, 0.10, 0.10, 0.05}
	relTypes       = []string{"similar_to", "leads_to", "contradicts", "reinforces", "relates_to"}
	relTypeWeights = []float64{0.40, 0.25, 0.10, 0.15, 0.10}
	memTypes       = []string{"decision", "pattern", "insight", "context"}
	daysInMillis   = int64(365 * 24 * 3600 * 1000)
)

type relationRecord struct {
	sourceID        int64
	targetID        int64
	relationType    string
	confidence      float64
	detectionSource string
	createdAt       string
	createdAtEpoch  int64
}

func weightedPick(weights []float64, rng *rand.Rand) int {
	if len(weights) == 0 {
		return 0
	}

	total := 0.0
	for _, weight := range weights {
		total += weight
	}
	if total <= 0 {
		return 0
	}

	target := rng.Float64() * total
	cumulative := 0.0
	for index, weight := range weights {
		cumulative += weight
		if target < cumulative {
			return index
		}
	}

	return len(weights) - 1
}

func normalClamp(mean, stddev float64, rng *rand.Rand) float64 {
	u1 := rng.Float64()
	if u1 == 0 {
		u1 = 1e-12
	}
	z := math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*rng.Float64())
	value := mean + stddev*z
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func randomUnitVec(rng *rand.Rand) []float32 {
	vec := make([]float32, vecDims)
	var normSq float64
	for i := 0; i < vecDims; i++ {
		u1 := rng.Float64()
		if u1 == 0 {
			u1 = 1e-12
		}
		value := math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*rng.Float64())
		vec[i] = float32(value)
		normSq += value * value
	}

	if normSq == 0 {
		vec[0] = 1
		normSq = 1
	}

	normalizer := 1.0 / math.Sqrt(normSq)
	for i := range vec {
		vec[i] = float32(float64(vec[i]) * normalizer)
	}
	return vec
}

func formatVector(v []float32) string {
	builder := strings.Builder{}
	builder.WriteByte('[')
	for i, value := range v {
		if i > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(fmt.Sprintf("%f", value))
	}
	builder.WriteByte(']')
	return builder.String()
}

func CreateBenchTables(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		return err
	}

	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS bench_observations(
		id BIGINT PRIMARY KEY,
		project TEXT,
		type TEXT,
		importance_score REAL,
		created_at_epoch BIGINT,
		sdk_session_id TEXT,
		scope TEXT DEFAULT 'project',
		title TEXT,
		narrative TEXT,
		concepts TEXT,
		is_archived INT DEFAULT 0,
		is_superseded INT DEFAULT 0,
		memory_type TEXT,
		created_at TEXT
	)`); err != nil {
		return err
	}

	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS bench_relations(
		id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		source_id BIGINT NOT NULL,
		target_id BIGINT NOT NULL,
		relation_type TEXT NOT NULL,
		confidence REAL NOT NULL,
		detection_source TEXT NOT NULL,
		created_at TEXT NOT NULL,
		created_at_epoch BIGINT NOT NULL,
		CONSTRAINT bench_rel_unique UNIQUE(source_id,target_id,relation_type)
	)`); err != nil {
		return err
	}

	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS bench_vectors(
		observation_id BIGINT PRIMARY KEY,
		embedding vector(384) NOT NULL
	)`); err != nil {
		return err
	}

	if _, err := pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_bench_rel_source ON bench_relations(source_id)`); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_bench_rel_target ON bench_relations(target_id)`); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_bench_vec_hnsw ON bench_vectors USING hnsw(embedding vector_cosine_ops) WITH (m=16, ef_construction=128)`); err != nil {
		return err
	}

	return nil
}

func DropBenchTables(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS bench_vectors CASCADE`); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS bench_relations CASCADE`); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS bench_observations CASCADE`); err != nil {
		return err
	}
	return nil
}

func SeedData(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `TRUNCATE bench_observations, bench_relations, bench_vectors RESTART IDENTITY CASCADE`); err != nil {
		return err
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	baseEpoch := time.Now().UnixMilli()

	if err := seedObservations(ctx, pool, rng, baseEpoch); err != nil {
		return err
	}
	if err := seedRelations(ctx, pool, rng); err != nil {
		return err
	}
	if err := seedVectors(ctx, pool, rng); err != nil {
		return err
	}

	return nil
}

func seedObservations(ctx context.Context, pool *pgxpool.Pool, rng *rand.Rand, baseEpoch int64) error {
	rows := make([][]any, 0, seedBatchSize)
	columns := []string{"id", "project", "type", "importance_score", "created_at_epoch", "sdk_session_id", "scope", "title", "narrative", "concepts", "is_archived", "is_superseded", "memory_type", "created_at"}

	for id := int64(1); id <= benchObsCount; id++ {
		project := fmt.Sprintf("project_%02d", (id-1)%50)
		importanceScore := normalClamp(0.5, 0.2, rng)
		createdAtEpoch := baseEpoch - rng.Int63n(daysInMillis)
		createdAt := time.Unix(createdAtEpoch/1000, 0).UTC().Format(time.RFC3339)

		rows = append(rows, []any{
			id,
			project,
			obsTypes[weightedPick(obsTypeWeights, rng)],
			importanceScore,
			createdAtEpoch,
			fmt.Sprintf("sess_%d", id/100),
			"project",
			fmt.Sprintf("Observation %d", id),
			fmt.Sprintf("Narrative text for observation %d in project %s", id, project),
			fmt.Sprintf("[\"concept_%d\",\"concept_%d\"]", id%200, (id+7)%200),
			0,
			0,
			memTypes[int(id%int64(len(memTypes)))],
			createdAt,
		})

		if len(rows) == seedBatchSize {
			if err := copyFromRows(ctx, pool, "bench_observations", columns, rows); err != nil {
				return err
			}
			rows = rows[:0]
		}
	}

	if len(rows) > 0 {
		if err := copyFromRows(ctx, pool, "bench_observations", columns, rows); err != nil {
			return err
		}
	}
	return nil
}

func seedRelations(ctx context.Context, pool *pgxpool.Pool, rng *rand.Rand) error {
	batch := make([]relationRecord, 0, relBatchSize)
	createdAt := time.Now().UTC()
	createdAtEpoch := createdAt.UnixMilli()
	createdAtText := createdAt.Format(time.RFC3339)
	generatedPairs := int64(0)

	for sourceID := int64(1); sourceID <= benchObsCount && generatedPairs < benchRelCount; sourceID++ {
		fanout := 2 + int(rng.ExpFloat64()*2)
		if rng.Float64() < 0.01 {
			fanout += 13
		}
		if fanout > 50 {
			fanout = 50
		}

		for i := 0; i < fanout && generatedPairs < benchRelCount; i++ {
			targetID := int64(rng.Int63n(benchObsCount) + 1)
			if targetID == sourceID {
				targetID = (sourceID % benchObsCount) + 1
			}

			rel := relationRecord{
				sourceID:        sourceID,
				targetID:        targetID,
				relationType:    relTypes[weightedPick(relTypeWeights, rng)],
				confidence:      normalClamp(0.5, 0.2, rng),
				detectionSource: "embedding_similarity",
				createdAt:       createdAtText,
				createdAtEpoch:  createdAtEpoch,
			}
			batch = append(batch, rel)
			generatedPairs++

			if len(batch) >= relBatchSize {
				if err := sendRelationBatch(ctx, pool, batch); err != nil {
					return err
				}
				batch = batch[:0]
			}
		}
	}

	if len(batch) > 0 {
		if err := sendRelationBatch(ctx, pool, batch); err != nil {
			return err
		}
	}

	return nil
}

func sendRelationBatch(ctx context.Context, pool *pgxpool.Pool, rows []relationRecord) error {
	insertStmt := `INSERT INTO bench_relations(source_id,target_id,relation_type,confidence,detection_source,created_at,created_at_epoch)
	VALUES($1,$2,$3,$4,$5,$6,$7)
	ON CONFLICT ON CONSTRAINT bench_rel_unique DO NOTHING`

	batch := &pgx.Batch{}
	for _, row := range rows {
		batch.Queue(
			insertStmt,
			row.sourceID,
			row.targetID,
			row.relationType,
			row.confidence,
			row.detectionSource,
			row.createdAt,
			row.createdAtEpoch,
		)
	}

	br := pool.SendBatch(ctx, batch)
	for i := 0; i < len(rows); i++ {
		if _, err := br.Exec(); err != nil && !isUniqueViolation(err) {
			_ = br.Close()
			return err
		}
	}

	if err := br.Close(); err != nil {
		if !isUniqueViolation(err) {
			return err
		}
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func seedVectors(ctx context.Context, pool *pgxpool.Pool, rng *rand.Rand) error {
	rows := make([][]any, 0, seedBatchSize)
	columns := []string{"observation_id", "embedding"}

	for id := int64(1); id <= benchVecCount; id++ {
		rows = append(rows, []any{id, formatVector(randomUnitVec(rng))})

		if len(rows) == seedBatchSize {
			if err := copyFromRows(ctx, pool, "bench_vectors", columns, rows); err != nil {
				return err
			}
			rows = rows[:0]
		}
	}

	if len(rows) > 0 {
		if err := copyFromRows(ctx, pool, "bench_vectors", columns, rows); err != nil {
			return err
		}
	}
	return nil
}

func copyFromRows(ctx context.Context, pool *pgxpool.Pool, table string, columns []string, rows [][]any) error {
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{table}, columns, pgx.CopyFromRows(rows)); err != nil {
		return err
	}
	return nil
}
