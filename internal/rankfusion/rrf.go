// Package rankfusion provides deterministic rank-only fusion primitives without
// importing retrieval providers or storage adapters.
package rankfusion

import "sort"

// RRF performs Reciprocal Rank Fusion on two ranked ID lists.
// A non-positive k uses the established default of 60. Ties resolve by best
// source rank and then ID so repeated snapshots are byte-stable.
func RRF(listA, listB []int64, k int) []int64 {
	if k <= 0 {
		k = 60
	}
	scores := make(map[int64]float64, len(listA)+len(listB))
	bestRank := make(map[int64]int, len(listA)+len(listB))
	add := func(ids []int64) {
		for rank, id := range ids {
			scores[id] += 1.0 / float64(rank+k+1)
			if best, ok := bestRank[id]; !ok || rank < best {
				bestRank[id] = rank
			}
		}
	}
	add(listA)
	add(listB)

	type scoredID struct {
		id       int64
		score    float64
		bestRank int
	}
	merged := make([]scoredID, 0, len(scores))
	for id, score := range scores {
		merged = append(merged, scoredID{id: id, score: score, bestRank: bestRank[id]})
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].score != merged[j].score {
			return merged[i].score > merged[j].score
		}
		if merged[i].bestRank != merged[j].bestRank {
			return merged[i].bestRank < merged[j].bestRank
		}
		return merged[i].id < merged[j].id
	})

	result := make([]int64, len(merged))
	for index, candidate := range merged {
		result[index] = candidate.id
	}
	return result
}
