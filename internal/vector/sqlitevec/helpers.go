// Package sqlitevec provides sqlite-vec based vector database integration for claude-mnemonic.
package sqlitevec

import "github.com/thebtf/claude-mnemonic-plus/internal/vector"

// Type aliases for backward compatibility — canonical types are now in the vector package.
type (
	DocType         = vector.DocType
	Document        = vector.Document
	QueryResult     = vector.QueryResult
	StaleVectorInfo = vector.StaleVectorInfo
	ExtractedIDs    = vector.ExtractedIDs
)

// DocType constant re-declarations for backward compatibility.
// Since DocType is an alias for vector.DocType, these are identical values.
const (
	DocTypeObservation    DocType = vector.DocTypeObservation
	DocTypeSessionSummary DocType = vector.DocTypeSessionSummary
	DocTypeUserPrompt     DocType = vector.DocTypeUserPrompt
)

// DistanceToSimilarity converts sqlite-vec cosine distance to similarity score.
// Delegates to the canonical implementation in the vector package.
func DistanceToSimilarity(distance float64) float64 {
	return vector.DistanceToSimilarity(distance)
}

// FilterByThreshold filters results to only include those above the similarity threshold.
// If maxResults > 0, also caps the number of results.
func FilterByThreshold(results []QueryResult, threshold float64, maxResults int) []QueryResult {
	return vector.FilterByThreshold(results, threshold, maxResults)
}

// BuildWhereFilter creates a where filter map for vector queries.
func BuildWhereFilter(docType DocType, project string) map[string]interface{} {
	return vector.BuildWhereFilter(docType, project)
}

// ExtractIDsByDocType extracts database IDs from query results, grouped by document type.
func ExtractIDsByDocType(results []QueryResult) *ExtractedIDs {
	return vector.ExtractIDsByDocType(results)
}

// ExtractObservationIDs extracts observation database IDs from query results,
// optionally filtering by project (including global scope).
func ExtractObservationIDs(results []QueryResult, project string) []int64 {
	return vector.ExtractObservationIDs(results, project)
}

// ExtractSummaryIDs extracts session summary database IDs from query results.
func ExtractSummaryIDs(results []QueryResult, project string) []int64 {
	return vector.ExtractSummaryIDs(results, project)
}

// ExtractPromptIDs extracts user prompt database IDs from query results.
func ExtractPromptIDs(results []QueryResult, project string) []int64 {
	return vector.ExtractPromptIDs(results, project)
}

// copyMetadata creates a new metadata map with an additional key-value pair (internal use).
func copyMetadata(base map[string]any, key string, value any) map[string]any {
	return vector.CopyMetadata(base, key, value)
}

// copyMetadataMulti creates a new metadata map merging base and extra (internal use).
func copyMetadataMulti(base map[string]any, extra map[string]any) map[string]any {
	return vector.CopyMetadataMulti(base, extra)
}

// joinStrings joins strings with separator (internal use).
func joinStrings(strs []string, sep string) string {
	return vector.JoinStrings(strs, sep)
}
