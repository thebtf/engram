// Package pgvector provides PostgreSQL+pgvector based vector storage for engram.
package pgvector

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/vector"
	pgvec "github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// vectorRecord is the GORM model for the vectors table (created by migrations).
type vectorRecord struct {
	DocID        string       `gorm:"primaryKey;column:doc_id"`
	Embedding    pgvec.Vector `gorm:"column:embedding"`
	SQLiteID     int64        `gorm:"column:sqlite_id"`
	DocType      string       `gorm:"column:doc_type"`
	FieldType    string       `gorm:"column:field_type"`
	Project      string       `gorm:"column:project"`
	Scope        string       `gorm:"column:scope"`
	ModelVersion string       `gorm:"column:model_version"`
}

func (vectorRecord) TableName() string { return "vectors" }

// Config holds configuration for the pgvector client.
type Config struct {
	DB       *gorm.DB           // PostgreSQL GORM connection (required)
	EmbedSvc *embedding.Service // Embedding service (required)
}

// latencyBufSize is the number of query latency samples retained for percentile calculation.
const latencyBufSize = 1000

// Client provides vector operations via PostgreSQL+pgvector.
type Client struct {
	db             *gorm.DB
	sqlDB          *sql.DB
	embedSvc       *embedding.Service
	modelVersion   string
	mu             sync.RWMutex
	queryCount     atomic.Int64
	queryLatencyNs atomic.Int64
	latencyBuf     [latencyBufSize]int64 // ring buffer for percentile calculation
	latencyIdx     atomic.Int64
}

// NewClient creates a new pgvector client.
func NewClient(cfg Config) (*Client, error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("DB is required")
	}
	if cfg.EmbedSvc == nil {
		return nil, fmt.Errorf("EmbedSvc is required")
	}

	sqlDB, err := cfg.DB.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}

	return &Client{
		db:           cfg.DB,
		sqlDB:        sqlDB,
		embedSvc:     cfg.EmbedSvc,
		modelVersion: cfg.EmbedSvc.Version(),
	}, nil
}

// AddDocuments adds documents with their embeddings to the vector store.
func (c *Client) AddDocuments(ctx context.Context, docs []vector.Document) error {
	if len(docs) == 0 {
		return nil
	}

	texts := make([]string, len(docs))
	for i, doc := range docs {
		texts[i] = doc.Content
	}

	embeddings, err := c.embedSvc.EmbedBatch(texts)
	if err != nil {
		return fmt.Errorf("embed batch: %w", err)
	}

	records := make([]vectorRecord, 0, len(docs))
	for i, doc := range docs {
		if len(embeddings[i]) == 0 {
			continue
		}
		meta := doc.Metadata
		rec := vectorRecord{
			DocID:        doc.ID,
			Embedding:    pgvec.NewVector(embeddings[i]),
			SQLiteID:     extractInt64(meta["sqlite_id"]),
			DocType:      extractString(meta["doc_type"]),
			FieldType:    extractString(meta["field_type"]),
			Project:      extractString(meta["project"]),
			Scope:        extractString(meta["scope"]),
			ModelVersion: c.modelVersion,
		}
		records = append(records, rec)
	}

	if len(records) == 0 {
		return nil
	}

	// Upsert: INSERT ... ON CONFLICT (doc_id) DO UPDATE SET ...
	return c.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "doc_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"embedding", "sqlite_id", "doc_type", "field_type",
				"project", "scope", "model_version",
			}),
		}).
		Create(&records).Error
}

// DeleteDocuments removes documents by their IDs.
func (c *Client) DeleteDocuments(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return c.db.WithContext(ctx).
		Where("doc_id IN ?", ids).
		Delete(&vectorRecord{}).Error
}

// Query performs a vector similarity search using cosine distance.
func (c *Client) Query(ctx context.Context, query string, limit int, where vector.WhereFilter) ([]vector.QueryResult, error) {
	start := time.Now()
	defer func() {
		dur := time.Since(start).Nanoseconds()
		c.queryCount.Add(1)
		c.queryLatencyNs.Add(dur)
		idx := c.latencyIdx.Add(1) - 1
		c.latencyBuf[idx%latencyBufSize] = dur
	}()

	if limit <= 0 {
		limit = 10
	}

	embedding, err := c.embedSvc.Embed(query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(embedding) == 0 {
		return nil, nil
	}

	queryVec := pgvec.NewVector(embedding)

	// Build raw SQL for cosine distance query.
	// $1 is reserved for the query vector; additional where-clause args start at $2.
	var args []any
	args = append(args, queryVec)
	argIdx := 2

	// allowedColumns is the set of column identifiers permitted in WHERE clauses.
	// This prevents SQL injection via unvalidated column names in fmt.Sprintf.
	allowedColumns := map[string]struct{}{
		"doc_type":   {},
		"project":    {},
		"scope":      {},
		"field_type": {},
		"sqlite_id":  {},
	}

	var whereClauses []string
	for _, clause := range where.Clauses {
		if len(clause.OrGroup) > 0 {
			var orParts []string
			for _, oc := range clause.OrGroup {
				if _, ok := allowedColumns[oc.Column]; !ok {
					return nil, fmt.Errorf("unsupported where column: %s", oc.Column)
				}
				orParts = append(orParts, fmt.Sprintf("%s = $%d", oc.Column, argIdx))
				args = append(args, oc.Value)
				argIdx++
			}
			whereClauses = append(whereClauses, "("+strings.Join(orParts, " OR ")+")")
		} else {
			if _, ok := allowedColumns[clause.Column]; !ok {
				return nil, fmt.Errorf("unsupported where column: %s", clause.Column)
			}
			whereClauses = append(whereClauses, fmt.Sprintf("%s = $%d", clause.Column, argIdx))
			args = append(args, clause.Value)
			argIdx++
		}
	}
	args = append(args, limit)

	sqlStr := fmt.Sprintf(`
		SELECT doc_id, sqlite_id, doc_type, field_type, project, scope, model_version,
		       embedding <=> $1 AS distance
		FROM vectors
		%s
		ORDER BY distance
		LIMIT $%d`,
		buildWhereClause(whereClauses),
		argIdx,
	)

	rows, err := c.sqlDB.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("query vectors: %w", err)
	}
	defer rows.Close()

	var results []vector.QueryResult
	for rows.Next() {
		var (
			docID        string
			sqliteID     int64
			docType      string
			fieldType    string
			project      string
			scope        string
			modelVersion string
			distance     float64
		)
		if err := rows.Scan(
			&docID, &sqliteID, &docType, &fieldType,
			&project, &scope, &modelVersion, &distance,
		); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		results = append(results, vector.QueryResult{
			ID:         docID,
			Distance:   distance,
			Similarity: vector.DistanceToSimilarity(distance),
			Metadata: map[string]any{
				"sqlite_id":  sqliteID,
				"doc_type":   docType,
				"field_type": fieldType,
				"project":    project,
				"scope":      scope,
			},
		})
	}
	return results, rows.Err()
}

// IsConnected checks whether the PostgreSQL connection is alive.
func (c *Client) IsConnected() bool {
	return c.sqlDB.Ping() == nil
}

// Close releases the underlying sql.DB connection.
func (c *Client) Close() error {
	return c.sqlDB.Close()
}

// Count returns the total number of vectors in the store.
func (c *Client) Count(ctx context.Context) (int64, error) {
	var count int64
	err := c.db.WithContext(ctx).Model(&vectorRecord{}).Count(&count).Error
	return count, err
}

// ModelVersion returns the current embedding model version.
func (c *Client) ModelVersion() string {
	return c.modelVersion
}

// NeedsRebuild reports whether any vectors use a stale model version.
func (c *Client) NeedsRebuild(ctx context.Context) (bool, string) {
	var stale int64
	err := c.db.WithContext(ctx).Model(&vectorRecord{}).
		Where("model_version IS NULL OR model_version != ?", c.modelVersion).
		Count(&stale).Error
	if err != nil {
		log.Warn().Err(err).Msg("pgvector: failed to check stale vectors")
		return false, ""
	}
	if stale > 0 {
		return true, fmt.Sprintf("%d vectors have stale model version", stale)
	}
	return false, ""
}

// GetStaleVectors returns info about vectors with mismatched or null model versions.
func (c *Client) GetStaleVectors(ctx context.Context) ([]vector.StaleVectorInfo, error) {
	var records []vectorRecord
	err := c.db.WithContext(ctx).
		Where("model_version IS NULL OR model_version != ?", c.modelVersion).
		Find(&records).Error
	if err != nil {
		return nil, fmt.Errorf("get stale vectors: %w", err)
	}

	infos := make([]vector.StaleVectorInfo, len(records))
	for i, r := range records {
		infos[i] = vector.StaleVectorInfo{
			DocID:    r.DocID,
			DocType:  r.DocType,
			SQLiteID: r.SQLiteID,
		}
	}
	return infos, nil
}

// extractInt64 safely converts metadata values to int64.
func extractInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	case int:
		return int64(x)
	}
	return 0
}

// extractString safely converts metadata values to string.
func extractString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// buildWhereClause joins SQL WHERE conditions with AND, prefixed with WHERE.
func buildWhereClause(clauses []string) string {
	if len(clauses) == 0 {
		return ""
	}
	return "WHERE " + strings.Join(clauses, " AND ")
}

// GetHealthStats returns comprehensive health statistics using existing interface methods.
func (c *Client) GetHealthStats(ctx context.Context) (*vector.HealthStats, error) {
	total, err := c.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("count vectors: %w", err)
	}

	staleVectors, err := c.GetStaleVectors(ctx)
	if err != nil {
		return nil, fmt.Errorf("get stale vectors: %w", err)
	}

	needsRebuild, rebuildReason := c.NeedsRebuild(ctx)

	return &vector.HealthStats{
		TotalVectors:  total,
		StaleVectors:  int64(len(staleVectors)),
		CurrentModel:  c.ModelVersion(),
		NeedsRebuild:  needsRebuild,
		RebuildReason: rebuildReason,
	}, nil
}

// GetCacheStats returns cache performance statistics.
// pgvector does not maintain a local embedding result cache, so all counters are zero.
func (c *Client) GetCacheStats() vector.CacheStatsSnapshot {
	return vector.CacheStatsSnapshot{}
}

// GetMetrics returns real query instrumentation metrics.
func (c *Client) GetMetrics(ctx context.Context) vector.VectorMetricsSnapshot {
	count := c.queryCount.Load()
	totalNs := c.queryLatencyNs.Load()
	docCount, _ := c.Count(ctx)

	var avg float64
	if count > 0 {
		avg = float64(totalNs) / float64(count) / 1e6 // ns to ms
	}

	// Calculate percentiles from ring buffer
	n := count
	if n > latencyBufSize {
		n = latencyBufSize
	}

	var p50, p95, p99 float64
	if n > 0 {
		samples := make([]int64, n)
		// Read the most recent n entries from the ring buffer.
		// latencyIdx is the next-write position; the most recent n entries end at latencyIdx-1.
		startIdx := c.latencyIdx.Load() - n
		for i := int64(0); i < n; i++ {
			samples[i] = c.latencyBuf[(startIdx+i)%latencyBufSize]
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		p50 = float64(samples[n*50/100]) / 1e6
		p95 = float64(samples[n*95/100]) / 1e6
		idx99 := n * 99 / 100
		if idx99 >= n {
			idx99 = n - 1
		}
		p99 = float64(samples[idx99]) / 1e6
	}

	return vector.VectorMetricsSnapshot{
		QueryCount:   count,
		AvgLatencyMs: avg,
		P50LatencyMs: p50,
		P95LatencyMs: p95,
		P99LatencyMs: p99,
		TotalDocs:    docCount,
	}
}

// DeleteByObservationID removes all vectors associated with an observation ID.
// Doc IDs for observations follow the pattern obs_{id}_{field}.
func (c *Client) DeleteByObservationID(ctx context.Context, obsID int64) error {
	prefix := fmt.Sprintf("obs_%d_%%", obsID)
	return c.db.WithContext(ctx).
		Where("doc_id LIKE ?", prefix).
		Delete(&vectorRecord{}).Error
}

// Compile-time check: Client must satisfy vector.Client.
var _ vector.Client = (*Client)(nil)
