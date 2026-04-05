// Package vector provides common interfaces and types for vector storage implementations.
package vector

import "strings"

// DocType represents the type of document stored in the vector table.
type DocType string

const (
	DocTypeObservation    DocType = "observation"
	DocTypeSessionSummary DocType = "session_summary"
	DocTypeUserPrompt     DocType = "user_prompt"
)

// Document represents a document to store with vector embedding.
type Document struct {
	Metadata map[string]any
	ID       string
	Content  string
}

// QueryResult represents a search result from vector search.
type QueryResult struct {
	Metadata   map[string]any
	ID         string
	Distance   float64
	Similarity float64
}

// StaleVectorInfo contains information about a vector that needs rebuilding.
type StaleVectorInfo struct {
	DocID    string
	DocType  string
	SQLiteID int64
}

// HealthStats contains comprehensive health information about the vector store.
type HealthStats struct {
	TotalVectors  int64  `json:"total_vectors"`
	StaleVectors  int64  `json:"stale_vectors"`
	CurrentModel  string `json:"current_model"`
	NeedsRebuild  bool   `json:"needs_rebuild"`
	RebuildReason string `json:"rebuild_reason"`
}

// VectorMetricsSnapshot contains real query instrumentation data.
type VectorMetricsSnapshot struct {
	QueryCount   int64   `json:"query_count"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	P50LatencyMs float64 `json:"p50_latency_ms"`
	P95LatencyMs float64 `json:"p95_latency_ms"`
	P99LatencyMs float64 `json:"p99_latency_ms"`
	TotalDocs    int64   `json:"total_documents"`
}

// CacheStatsSnapshot is an exported snapshot of cache performance metrics.
// For backends without a local cache (e.g. pgvector), all counters are zero.
type CacheStatsSnapshot struct {
	EmbeddingHits   int64 `json:"embedding_hits"`
	EmbeddingMisses int64 `json:"embedding_misses"`
	ResultHits      int64 `json:"result_hits"`
	ResultMisses    int64 `json:"result_misses"`
}

// HitRate returns the overall cache hit rate as a percentage (0–100).
func (s CacheStatsSnapshot) HitRate() float64 {
	total := s.EmbeddingHits + s.EmbeddingMisses + s.ResultHits + s.ResultMisses
	if total == 0 {
		return 0
	}
	hits := s.EmbeddingHits + s.ResultHits
	return float64(hits) / float64(total) * 100
}

// DistanceToSimilarity converts cosine distance to similarity score.
// Cosine distance: 0 = identical, 2 = opposite. Similarity: 1.0 = identical, 0.0 = opposite.
func DistanceToSimilarity(distance float64) float64 {
	return 1.0 - (distance / 2.0)
}

// FilterByThreshold filters results to only include those above the similarity threshold.
// If maxResults > 0, also caps the number of results.
func FilterByThreshold(results []QueryResult, threshold float64, maxResults int) []QueryResult {
	var filtered []QueryResult
	for _, r := range results {
		if r.Similarity >= threshold {
			filtered = append(filtered, r)
			if maxResults > 0 && len(filtered) >= maxResults {
				break
			}
		}
	}
	return filtered
}

// WhereFilter represents a filter for vector queries, supporting OR conditions.
type WhereFilter struct {
	Clauses []WhereClause
}

// WhereClause is a single filter condition (equality or OR group).
type WhereClause struct {
	OrGroup []WhereClause
	Column  string
	Value   any
}

// BuildWhereFilter creates a filter for vector queries.
// When includeGlobal is true and project is non-empty, results include
// both project-specific AND global-scoped observations.
func BuildWhereFilter(docType DocType, project string, includeGlobal bool) WhereFilter {
	var filter WhereFilter
	if docType != "" {
		filter.Clauses = append(filter.Clauses, WhereClause{Column: "doc_type", Value: string(docType)})
	}
	if project != "" {
		if includeGlobal {
			filter.Clauses = append(filter.Clauses, WhereClause{
				OrGroup: []WhereClause{
					{Column: "project", Value: project},
					{Column: "scope", Value: "global"},
				},
			})
		} else {
			filter.Clauses = append(filter.Clauses, WhereClause{Column: "project", Value: project})
		}
	}
	return filter
}

// ExtractObservationIDs extracts observation database IDs from query results,
// optionally filtering by project (including global scope).
func ExtractObservationIDs(results []QueryResult, project string) []int64 {
	var ids []int64
	seen := make(map[int64]bool)
	for _, result := range results {
		id := ExtractRowID(result.Metadata)
		if id == 0 {
			continue
		}
		docType, _ := result.Metadata["doc_type"].(string)
		if docType != string(DocTypeObservation) {
			continue
		}
		if project != "" {
			proj, _ := result.Metadata["project"].(string)
			scope, _ := result.Metadata["scope"].(string)
			if proj != project && scope != "global" {
				continue
			}
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// ExtractSummaryIDs extracts session summary database IDs from query results.
func ExtractSummaryIDs(results []QueryResult, project string) []int64 {
	var ids []int64
	seen := make(map[int64]bool)
	for _, result := range results {
		id := ExtractRowID(result.Metadata)
		if id == 0 {
			continue
		}
		docType, _ := result.Metadata["doc_type"].(string)
		if docType != string(DocTypeSessionSummary) {
			continue
		}
		if project != "" {
			proj, _ := result.Metadata["project"].(string)
			if proj != project {
				continue
			}
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// ExtractPromptIDs extracts user prompt database IDs from query results.
func ExtractPromptIDs(results []QueryResult, project string) []int64 {
	var ids []int64
	seen := make(map[int64]bool)
	for _, result := range results {
		id := ExtractRowID(result.Metadata)
		if id == 0 {
			continue
		}
		docType, _ := result.Metadata["doc_type"].(string)
		if docType != string(DocTypeUserPrompt) {
			continue
		}
		if project != "" {
			proj, _ := result.Metadata["project"].(string)
			if proj != project {
				continue
			}
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// JoinStrings joins strings with a separator.
func JoinStrings(strs []string, sep string) string {
	return strings.Join(strs, sep)
}

// CopyMetadata creates a new metadata map with an additional key-value pair.
func CopyMetadata(base map[string]any, key string, value any) map[string]any {
	result := make(map[string]any, len(base)+1)
	for k, v := range base {
		result[k] = v
	}
	result[key] = value
	return result
}

// CopyMetadataMulti creates a new metadata map merging base and extra.
func CopyMetadataMulti(base map[string]any, extra map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range extra {
		result[k] = v
	}
	return result
}

// ExtractRowID safely extracts a database row ID from vector result metadata.
// The metadata key is "sqlite_id" for historical reasons (pgvector inherited the name).
func ExtractRowID(metadata map[string]any) int64 {
	if sqliteID, ok := metadata["sqlite_id"].(float64); ok {
		return int64(sqliteID)
	}
	if id, ok := metadata["sqlite_id"].(int64); ok {
		return id
	}
	return 0
}
