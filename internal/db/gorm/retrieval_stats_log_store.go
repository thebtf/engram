// Package gorm provides GORM-based database stores for engram.
package gorm

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// RetrievalStatsLogEntry represents a single logged retrieval event.
type RetrievalStatsLogEntry struct {
	ID        int64     `gorm:"primaryKey;autoIncrement"`
	Project   string    `gorm:"type:text;not null"`
	EventType string    `gorm:"type:text;not null"`
	Count     int       `gorm:"not null;default:1"`
	CreatedAt time.Time `gorm:"not null;default:NOW()"`
}

// TableName returns the table name for RetrievalStatsLogEntry.
func (RetrievalStatsLogEntry) TableName() string { return "retrieval_stats_log" }

// RetrievalStatsLogStore handles batched logging of retrieval stats to PostgreSQL.
// Events are buffered in a channel and flushed periodically or when the buffer reaches a threshold.
type RetrievalStatsLogStore struct {
	db       *gorm.DB
	ch       chan RetrievalStatsLogEntry
	done     chan struct{}
	closeOnce sync.Once
}

const (
	retrievalStatsChanSize  = 1000
	retrievalStatsFlushSize = 50
	retrievalStatsFlushInterval = 5 * time.Second
)

// NewRetrievalStatsLogStore creates a new store and starts the background flusher.
func NewRetrievalStatsLogStore(db *gorm.DB) *RetrievalStatsLogStore {
	s := &RetrievalStatsLogStore{
		db:   db,
		ch:   make(chan RetrievalStatsLogEntry, retrievalStatsChanSize),
		done: make(chan struct{}),
	}
	go s.flusher()
	return s
}

// LogEvent enqueues a retrieval stats event. Non-blocking: drops if channel is full.
func (s *RetrievalStatsLogStore) LogEvent(project, eventType string, count int) {
	if count <= 0 {
		return
	}
	entry := RetrievalStatsLogEntry{
		Project:   project,
		EventType: eventType,
		Count:     count,
		CreatedAt: time.Now(),
	}
	select {
	case s.ch <- entry:
	default:
		// Channel full — drop to avoid blocking the caller.
	}
}

// flusher runs in a background goroutine, batching entries and writing to DB.
func (s *RetrievalStatsLogStore) flusher() {
	defer close(s.done)
	ticker := time.NewTicker(retrievalStatsFlushInterval)
	defer ticker.Stop()

	batch := make([]RetrievalStatsLogEntry, 0, retrievalStatsFlushSize)

	for {
		select {
		case entry, ok := <-s.ch:
			if !ok {
				// Channel closed — flush remaining and exit.
				if len(batch) > 0 {
					s.flush(batch)
				}
				return
			}
			batch = append(batch, entry)
			if len(batch) >= retrievalStatsFlushSize {
				s.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				s.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

// flush writes a batch of entries to the database.
func (s *RetrievalStatsLogStore) flush(entries []RetrievalStatsLogEntry) {
	if err := s.db.CreateInBatches(entries, len(entries)).Error; err != nil {
		log.Warn().Err(err).Int("batch_size", len(entries)).Msg("failed to flush retrieval stats batch")
	}
}

// Close drains the channel and stops the background flusher.
func (s *RetrievalStatsLogStore) Close() {
	s.closeOnce.Do(func() {
		close(s.ch)
		<-s.done
	})
}

// AggregatedRetrievalStats contains aggregated retrieval metrics from the DB.
type AggregatedRetrievalStats struct {
	TotalRequests      int64 `json:"total_requests"`
	ObservationsServed int64 `json:"observations_served"`
	SearchRequests     int64 `json:"search_requests"`
	ContextInjections  int64 `json:"context_injections"`
	StaleExcluded      int64 `json:"stale_excluded"`
	FreshCount         int64 `json:"fresh_count"`
	DuplicatesRemoved  int64 `json:"duplicates_removed"`
}

// GetStats returns aggregated retrieval stats from the persistent log.
// If project is empty, aggregates across all projects.
// If since is zero, returns all-time stats.
func (s *RetrievalStatsLogStore) GetStats(ctx context.Context, project string, since time.Time) (*AggregatedRetrievalStats, error) {
	type row struct {
		EventType string
		Total     int64
	}
	var rows []row

	q := s.db.WithContext(ctx).
		Table("retrieval_stats_log").
		Select("event_type, SUM(count) as total").
		Group("event_type")
	if project != "" {
		q = q.Where("project = ?", project)
	}
	if !since.IsZero() {
		q = q.Where("created_at >= ?", since)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}

	stats := &AggregatedRetrievalStats{}
	for _, r := range rows {
		switch r.EventType {
		case "search_request":
			stats.SearchRequests = r.Total
			stats.TotalRequests += r.Total
		case "context_injection":
			stats.ContextInjections = r.Total
			stats.TotalRequests += r.Total
		case "observations_served":
			stats.ObservationsServed = r.Total
		case "stale_excluded":
			stats.StaleExcluded = r.Total
		case "fresh_count":
			stats.FreshCount = r.Total
		case "duplicates_removed":
			stats.DuplicatesRemoved = r.Total
		}
	}
	return stats, nil
}

// Cleanup deletes entries older than the given duration.
func (s *RetrievalStatsLogStore) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	result := s.db.WithContext(ctx).
		Where("created_at < ?", cutoff).
		Delete(&RetrievalStatsLogEntry{})
	return result.RowsAffected, result.Error
}
