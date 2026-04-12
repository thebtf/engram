package search

// SearchLaneConfig defines retrieval policy for a logical observation lane.
// Different observation types can use different score thresholds, top-k limits,
// and reranker weights when F5 typed lanes are enabled.
type SearchLaneConfig struct {
	MinScore       float64
	TopK           int
	RerankerWeight float64
}

// DefaultSearchLanes is the canonical default lane map for typed retrieval.
// These values come from learning-memory-v4 spec.md FR-8.
var DefaultSearchLanes = map[string]SearchLaneConfig{
	"guidance": {
		MinScore:       0.20,
		TopK:           5,
		RerankerWeight: 1.5,
	},
	"pitfall": {
		MinScore:       0.20,
		TopK:           5,
		RerankerWeight: 1.5,
	},
	"decision": {
		MinScore:       0.55,
		TopK:           3,
		RerankerWeight: 1.0,
	},
	"wiki": {
		MinScore:       0.65,
		TopK:           2,
		RerankerWeight: 0.8,
	},
	"entity": {
		MinScore:       0.65,
		TopK:           2,
		RerankerWeight: 0.8,
	},
	"pattern": {
		MinScore:       0.40,
		TopK:           5,
		RerankerWeight: 1.2,
	},
	"bugfix": {
		MinScore:       0.40,
		TopK:           5,
		RerankerWeight: 1.2,
	},
	"feature": {
		MinScore:       0.40,
		TopK:           5,
		RerankerWeight: 1.2,
	},
	"default": {
		MinScore:       0.35,
		TopK:           10,
		RerankerWeight: 1.0,
	},
}
