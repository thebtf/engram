// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"time"

	"github.com/lib/pq"
)

// LogInjection records that an observation was injected into a session's context.
func (s *ObservationStore) LogInjection(ctx context.Context, observationID int64, project, taskContext, sessionID string) error {
	return s.db.WithContext(ctx).Exec(
		`INSERT INTO injection_log (observation_id, project, task_context, session_id, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		observationID, project, taskContext, sessionID, time.Now(),
	).Error
}

// LogInjections records multiple observations injected into a session's context (batch).
func (s *ObservationStore) LogInjections(ctx context.Context, observationIDs []int64, project, taskContext, sessionID string) error {
	if len(observationIDs) == 0 {
		return nil
	}
	now := time.Now()
	// Build multi-row insert using PostgreSQL array unnest for efficiency.
	return s.db.WithContext(ctx).Exec(
		`INSERT INTO injection_log (observation_id, project, task_context, session_id, created_at)
		 SELECT unnest(?::bigint[]), ?, ?, ?, ?`,
		pq.Array(observationIDs), project, taskContext, sessionID, now,
	).Error
}

// injectionDiversityRow is used to scan diversity query results.
type injectionDiversityRow struct {
	ObservationID int64
	Diversity     float64
}

// GetDiversityScores returns injection diversity for observations.
// diversity = unique_projects / total_injections. Higher = more generic = should penalize.
func (s *ObservationStore) GetDiversityScores(ctx context.Context, observationIDs []int64) (map[int64]float64, error) {
	if len(observationIDs) == 0 {
		return map[int64]float64{}, nil
	}

	rows, err := s.db.WithContext(ctx).Raw(
		`SELECT observation_id, COUNT(DISTINCT project)::float / COUNT(*)::float AS diversity
		 FROM injection_log
		 WHERE observation_id = ANY(?)
		 GROUP BY observation_id`,
		pq.Array(observationIDs),
	).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int64]float64, len(observationIDs))
	for rows.Next() {
		var row injectionDiversityRow
		if err := rows.Scan(&row.ObservationID, &row.Diversity); err != nil {
			return nil, err
		}
		result[row.ObservationID] = row.Diversity
	}
	return result, rows.Err()
}

// MarkCited updates injection_log entries for a session, setting cited=true for the given observation IDs.
// Idempotent — running twice produces the same result.
func (s *ObservationStore) MarkCited(ctx context.Context, sessionID string, observationIDs []int64) (int64, error) {
	if len(observationIDs) == 0 || sessionID == "" {
		return 0, nil
	}
	result := s.db.WithContext(ctx).Exec(
		`UPDATE injection_log SET cited = true WHERE session_id = ? AND observation_id = ANY(?) AND cited = false`,
		sessionID, pq.Array(observationIDs),
	)
	return result.RowsAffected, result.Error
}

// GetCitationRate computes the citation rate for a project over the most recent sessions.
// Returns: cited_count / total_injected_count for observations in the window.
// If no data, returns 0.5 (neutral).
func (s *ObservationStore) GetCitationRate(ctx context.Context, project string, windowSize int) (float64, error) {
	var result struct {
		Total int64
		Cited int64
	}
	err := s.db.WithContext(ctx).Raw(
		`SELECT COUNT(*) AS total, COUNT(*) FILTER (WHERE cited = true) AS cited
		 FROM injection_log
		 WHERE project = ? AND session_id IN (
			 SELECT session_id FROM injection_log
			 WHERE project = ? GROUP BY session_id ORDER BY MIN(created_at) DESC LIMIT ?
		 )`,
		project, project, windowSize,
	).Scan(&result).Error
	if err != nil {
		return 0.5, err
	}
	if result.Total == 0 {
		return 0.5, nil // neutral default
	}
	return float64(result.Cited) / float64(result.Total), nil
}


// CleanupInjectionLog removes entries older than the given number of days.
func (s *ObservationStore) CleanupInjectionLog(ctx context.Context, retentionDays int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	result := s.db.WithContext(ctx).Exec(
		`DELETE FROM injection_log WHERE created_at < ?`,
		cutoff,
	)
	return result.RowsAffected, result.Error
}
