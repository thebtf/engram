package taxonomy

import (
	"context"

	"gorm.io/gorm"
)

// Tunnel represents a concept shared between two projects.
type Tunnel struct {
	Concept string `json:"concept"`
	CountA  int    `json:"count_a"`
	CountB  int    `json:"count_b"`
}

// GetTunnels finds concepts shared between two projects with occurrence counts.
func GetTunnels(ctx context.Context, db *gorm.DB, projectA, projectB string) ([]Tunnel, error) {
	var tunnels []Tunnel

	err := db.WithContext(ctx).Raw(`
		WITH concepts_a AS (
			SELECT c.concept, COUNT(*) as cnt
			FROM observations o,
			     jsonb_array_elements_text(o.concepts) AS c(concept)
			WHERE o.project = ? AND o.is_superseded = 0 AND o.is_archived = 0
			GROUP BY c.concept
		),
		concepts_b AS (
			SELECT c.concept, COUNT(*) as cnt
			FROM observations o,
			     jsonb_array_elements_text(o.concepts) AS c(concept)
			WHERE o.project = ? AND o.is_superseded = 0 AND o.is_archived = 0
			GROUP BY c.concept
		)
		SELECT a.concept, a.cnt as count_a, b.cnt as count_b
		FROM concepts_a a
		INNER JOIN concepts_b b ON a.concept = b.concept
		ORDER BY (a.cnt + b.cnt) DESC
	`, projectA, projectB).Scan(&tunnels).Error

	return tunnels, err
}
