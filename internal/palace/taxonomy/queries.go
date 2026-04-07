// Package taxonomy provides read-only aggregation views over existing
// observation fields (project, type, concepts) for knowledge navigation.
package taxonomy

import (
	"context"

	"gorm.io/gorm"
)

// TaxonomyNode represents one node in the project→type→concept tree.
type TaxonomyNode struct {
	Project string `json:"project"`
	Type    string `json:"type"`
	Concept string `json:"concept"`
	Count   int    `json:"count"`
}

// GetTaxonomy returns a flat list of (project, type, concept, count) nodes.
// Optional project filter narrows to a single project.
func GetTaxonomy(ctx context.Context, db *gorm.DB, project string) ([]TaxonomyNode, error) {
	var nodes []TaxonomyNode

	q := db.WithContext(ctx).Raw(`
		SELECT o.project, o.type, c.concept, COUNT(*) as count
		FROM observations o,
		     jsonb_array_elements_text(o.concepts) AS c(concept)
		WHERE o.is_superseded = 0 AND o.is_archived = 0
		  AND o.concepts IS NOT NULL AND o.concepts != '[]'::jsonb
		  `+projectFilter(project)+`
		GROUP BY o.project, o.type, c.concept
		ORDER BY o.project, o.type, count DESC
	`, filterArgs(project)...)

	if err := q.Scan(&nodes).Error; err != nil {
		return nil, err
	}

	return nodes, nil
}

// TaxonomyStats holds aggregate statistics for the taxonomy.
type TaxonomyStats struct {
	Projects          int `json:"projects"`
	Types             int `json:"types"`
	Concepts          int `json:"concepts"`
	TotalObservations int `json:"total_observations"`
}

// GetStats returns aggregate taxonomy statistics.
func GetStats(ctx context.Context, db *gorm.DB) (*TaxonomyStats, error) {
	var stats TaxonomyStats

	// Count distinct projects
	if err := db.WithContext(ctx).Raw(
		`SELECT COUNT(DISTINCT project) FROM observations WHERE is_superseded = 0 AND is_archived = 0`,
	).Scan(&stats.Projects).Error; err != nil {
		return nil, err
	}

	// Count distinct types
	if err := db.WithContext(ctx).Raw(
		`SELECT COUNT(DISTINCT type) FROM observations WHERE is_superseded = 0 AND is_archived = 0`,
	).Scan(&stats.Types).Error; err != nil {
		return nil, err
	}

	// Count distinct concepts
	if err := db.WithContext(ctx).Raw(
		`SELECT COUNT(DISTINCT c.concept) FROM observations o, jsonb_array_elements_text(o.concepts) AS c(concept) WHERE o.is_superseded = 0 AND o.is_archived = 0`,
	).Scan(&stats.Concepts).Error; err != nil {
		return nil, err
	}

	// Total active observations
	if err := db.WithContext(ctx).Raw(
		`SELECT COUNT(*) FROM observations WHERE is_superseded = 0 AND is_archived = 0`,
	).Scan(&stats.TotalObservations).Error; err != nil {
		return nil, err
	}

	return &stats, nil
}

func projectFilter(project string) string {
	if project == "" {
		return ""
	}
	return "AND o.project = ?"
}

func filterArgs(project string) []any {
	if project == "" {
		return nil
	}
	return []any{project}
}
