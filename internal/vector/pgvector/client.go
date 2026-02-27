// Package pgvector provides PostgreSQL+pgvector based vector storage for claude-mnemonic.
package pgvector

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"github.com/thebtf/claude-mnemonic-plus/internal/embedding"
	"github.com/thebtf/claude-mnemonic-plus/internal/vector"
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

// Client provides vector operations via PostgreSQL+pgvector.
type Client struct {
	db           *gorm.DB
	sqlDB        *sql.DB
	embedSvc     *embedding.Service
	modelVersion string
	mu           sync.RWMutex
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
func (c *Client) Query(ctx context.Context, query string, limit int, where map[string]any) ([]vector.QueryResult, error) {
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

	var whereClauses []string
	for k, v := range where {
		whereClauses = append(whereClauses, fmt.Sprintf("%s = $%d", k, argIdx))
		args = append(args, v)
		argIdx++
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
			DocID:     r.DocID,
			DocType:   r.DocType,
			FieldType: r.FieldType,
			Project:   r.Project,
			Scope:     r.Scope,
			SQLiteID:  r.SQLiteID,
		}
	}
	return infos, nil
}

// DeleteVectorsByDocIDs removes vectors by their doc_ids.
// Used for granular rebuild — delete stale vectors before re-adding.
func (c *Client) DeleteVectorsByDocIDs(ctx context.Context, docIDs []string) error {
	if len(docIDs) == 0 {
		return nil
	}
	return c.db.WithContext(ctx).
		Where("doc_id IN ?", docIDs).
		Delete(&vectorRecord{}).Error
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

// DeleteByObservationID removes all vectors associated with an observation ID.
// Doc IDs for observations follow the pattern obs_{id}_{field}.
func (c *Client) DeleteByObservationID(ctx context.Context, obsID int64) error {
	prefix := fmt.Sprintf("obs_%d_%%", obsID)
	return c.db.WithContext(ctx).
		Where("doc_id LIKE ?", prefix).
		Delete(&vectorRecord{}).Error
}

// CacheStats returns basic cache size info for backward compatibility.
// pgvector has no local embedding cache; returns zeros.
//
// Deprecated: Use GetCacheStats for comprehensive statistics.
func (c *Client) CacheStats() (size int, maxSize int) {
	return 0, 0
}

// Compile-time check: Client must satisfy vector.Client.
var _ vector.Client = (*Client)(nil)
