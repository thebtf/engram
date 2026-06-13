// Package worker provides the main worker service for engram.
package worker

import (
	"github.com/thebtf/engram/pkg/models"
	"github.com/thebtf/engram/pkg/similarity"
)

// clusterObservations deduplicates a result set by grouping observations that
// share high Jaccard similarity across their title, narrative, and fact terms.
// Only one representative per cluster is returned. Clustering logic is owned
// by pkg/similarity; this wrapper keeps the worker API stable if the
// implementation moves.
func clusterObservations(observations []*models.Observation, similarityThreshold float64) []*models.Observation {
	return similarity.ClusterObservations(observations, similarityThreshold)
}
