package retrieval

import (
	"sort"
)

// RRF performs Reciprocal Rank Fusion on two ranked result lists.
// k is the RRF constant (typically 60).
func RRF(listA, listB []int64, k int) []int64 {
	if k <= 0 {
		k = 60
	}
	scores := make(map[int64]float64)
	for rank, id := range listA {
		scores[id] += 1.0 / float64(rank+k+1)
	}
	for rank, id := range listB {
		scores[id] += 1.0 / float64(rank+k+1)
	}

	type scored struct {
		id    int64
		score float64
	}
	var merged []scored
	for id, s := range scores {
		merged = append(merged, scored{id: id, score: s})
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].score > merged[j].score
	})

	result := make([]int64, len(merged))
	for i, s := range merged {
		result[i] = s.id
	}
	return result
}
