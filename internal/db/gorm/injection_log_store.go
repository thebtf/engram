// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"fmt"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

// InjectionLogStore handles append-only recording of memory injection events.
// The injection_log table is created by migration 106.
type InjectionLogStore struct {
	db *gorm.DB
}

// NewInjectionLogStore creates a new InjectionLogStore backed by the given Store.
func NewInjectionLogStore(store *Store) *InjectionLogStore {
	return &InjectionLogStore{db: store.DB}
}

// Record inserts a single injection event row for the given session, project,
// and the slice of memory IDs that were injected. memoryIDs must not be empty.
func (s *InjectionLogStore) Record(ctx context.Context, sessionID, project string, memoryIDs []int64) error {
	if sessionID == "" {
		return fmt.Errorf("injection_log: sessionID must not be empty")
	}
	if project == "" {
		return fmt.Errorf("injection_log: project must not be empty")
	}
	if len(memoryIDs) == 0 {
		return fmt.Errorf("injection_log: memoryIDs must not be empty")
	}

	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("injection_log: get raw db: %w", err)
	}

	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO injection_log (session_id, project, memory_ids) VALUES ($1, $2, $3)`,
		sessionID, project, pq.Array(memoryIDs),
	)
	if err != nil {
		return fmt.Errorf("injection_log: record session=%q project=%q: %w", sessionID, project, err)
	}
	return nil
}

// GetBySession returns the slice of memory IDs injected in every row matching
// the given sessionID. Rows are returned in insertion order (injected_at ASC).
// Returns an empty (non-nil) slice when no rows exist.
func (s *InjectionLogStore) GetBySession(ctx context.Context, sessionID string) ([]int64, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("injection_log: sessionID must not be empty")
	}

	sqlDB, err := s.db.DB()
	if err != nil {
		return nil, fmt.Errorf("injection_log: get raw db: %w", err)
	}

	rows, err := sqlDB.QueryContext(ctx,
		`SELECT memory_ids FROM injection_log WHERE session_id = $1 ORDER BY injected_at ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("injection_log: query session=%q: %w", sessionID, err)
	}
	defer rows.Close()

	var result []int64
	for rows.Next() {
		var ids pq.Int64Array
		if err := rows.Scan(&ids); err != nil {
			return nil, fmt.Errorf("injection_log: scan session=%q: %w", sessionID, err)
		}
		result = append(result, []int64(ids)...)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("injection_log: rows error session=%q: %w", sessionID, err)
	}
	if result == nil {
		result = []int64{}
	}
	return result, nil
}

// DeleteOlderThan removes all injection_log rows whose injected_at timestamp
// is strictly before olderThan. Returns the number of rows deleted.
func (s *InjectionLogStore) DeleteOlderThan(ctx context.Context, olderThan time.Time) (int64, error) {
	sqlDB, err := s.db.DB()
	if err != nil {
		return 0, fmt.Errorf("injection_log: get raw db: %w", err)
	}

	result, err := sqlDB.ExecContext(ctx,
		`DELETE FROM injection_log WHERE injected_at < $1`,
		olderThan,
	)
	if err != nil {
		return 0, fmt.Errorf("injection_log: delete older than %v: %w", olderThan, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("injection_log: rows affected: %w", err)
	}
	return n, nil
}
