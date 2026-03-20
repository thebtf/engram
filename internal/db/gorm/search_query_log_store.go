// Package gorm provides GORM-based database stores for engram.
package gorm

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// SearchQueryLogEntry represents a single logged search query.
type SearchQueryLogEntry struct {
	ID         int64     `gorm:"primaryKey;autoIncrement"`
	Project    string    `gorm:"type:text"`
	Query      string    `gorm:"type:text;not null"`
	SearchType string    `gorm:"type:text;not null"`
	Results    int       `gorm:"not null;default:0"`
	UsedVector bool      `gorm:"not null;default:false"`
	LatencyMs  float32   `gorm:"type:real"`
	CreatedAt  time.Time `gorm:"not null;default:NOW()"`
}

// TableName returns the table name for SearchQueryLogEntry.
func (SearchQueryLogEntry) TableName() string { return "search_query_log" }

// SearchQueryLogStore handles async logging of search queries to PostgreSQL.
type SearchQueryLogStore struct {
	db *gorm.DB
}

// NewSearchQueryLogStore creates a new SearchQueryLogStore.
func NewSearchQueryLogStore(db *gorm.DB) *SearchQueryLogStore {
	return &SearchQueryLogStore{db: db}
}

// LogQuery asynchronously inserts a search query log entry.
// Fire-and-forget: logs warning on error, never blocks caller.
func (s *SearchQueryLogStore) LogQuery(project, query, searchType string, results int, usedVector bool, latencyMs float32) {
	go func() {
		entry := SearchQueryLogEntry{
			Project:    project,
			Query:      query,
			SearchType: searchType,
			Results:    results,
			UsedVector: usedVector,
			LatencyMs:  latencyMs,
			CreatedAt:  time.Now(),
		}
		if err := s.db.Create(&entry).Error; err != nil {
			log.Warn().Err(err).Int("query_len", len(query)).Str("search_type", searchType).Msg("failed to log search query")
		}
	}()
}

// SearchAnalytics contains aggregated search analytics derived from search_query_log.
type SearchAnalytics struct {
	TotalSearches      int64   `json:"total_searches"`
	SearchesToday      int64   `json:"searches_today"`
	AvgLatencyMs       float64 `json:"avg_latency_ms"`
	ZeroResultRate     float64 `json:"zero_result_rate"`
	VectorSearches     int64   `json:"vector_searches"`
	FilterSearches     int64   `json:"filter_searches"`
	CacheHits          int64   `json:"cache_hits"`
	SearchErrors       int64   `json:"search_errors"`
	AvgVectorLatencyMs float64 `json:"avg_vector_latency_ms"`
	AvgFilterLatencyMs float64 `json:"avg_filter_latency_ms"`
	CoalescedRequests  int64   `json:"coalesced_requests"`
}

// GetAnalytics returns aggregated search analytics from the persistent log.
// If since is zero time, returns all-time stats.
func (s *SearchQueryLogStore) GetAnalytics(ctx context.Context, since time.Time) (*SearchAnalytics, error) {
	var analytics SearchAnalytics

	q := s.db.WithContext(ctx).Table("search_query_log")
	if !since.IsZero() {
		q = q.Where("created_at >= ?", since)
	}

	// Total searches
	if err := q.Count(&analytics.TotalSearches).Error; err != nil {
		return nil, err
	}

	if analytics.TotalSearches == 0 {
		return &analytics, nil
	}

	// Searches today (UTC)
	todayStart := time.Now().UTC().Truncate(24 * time.Hour)
	if err := s.db.WithContext(ctx).Table("search_query_log").
		Where("created_at >= ?", todayStart).
		Count(&analytics.SearchesToday).Error; err != nil {
		return nil, err
	}

	// Avg latency (only where latency_ms > 0, i.e. actually measured)
	baseQ := s.db.WithContext(ctx).Table("search_query_log")
	if !since.IsZero() {
		baseQ = baseQ.Where("created_at >= ?", since)
	}
	if err := baseQ.Where("latency_ms > 0").
		Select("COALESCE(AVG(latency_ms), 0)").
		Row().Scan(&analytics.AvgLatencyMs); err != nil {
		return nil, err
	}

	// Zero result rate
	var zeroCount int64
	zeroQ := s.db.WithContext(ctx).Table("search_query_log")
	if !since.IsZero() {
		zeroQ = zeroQ.Where("created_at >= ?", since)
	}
	if err := zeroQ.Where("results = 0").Count(&zeroCount).Error; err != nil {
		return nil, err
	}
	if analytics.TotalSearches > 0 {
		analytics.ZeroResultRate = float64(zeroCount) / float64(analytics.TotalSearches)
	}

	// Vector vs filter counts
	vectorQ := s.db.WithContext(ctx).Table("search_query_log")
	if !since.IsZero() {
		vectorQ = vectorQ.Where("created_at >= ?", since)
	}
	if err := vectorQ.Where("used_vector = true").Count(&analytics.VectorSearches).Error; err != nil {
		return nil, err
	}
	analytics.FilterSearches = analytics.TotalSearches - analytics.VectorSearches

	return &analytics, nil
}

// RecentQueryEntry represents a recent search query from the persistent log.
type RecentQueryEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	Query      string    `json:"query"`
	Project    string    `json:"project,omitempty"`
	SearchType string    `json:"type,omitempty"`
	Results    int       `json:"results"`
	UsedVector bool      `json:"used_vector"`
}

// GetRecent returns the most recent search queries from the persistent log.
func (s *SearchQueryLogStore) GetRecent(ctx context.Context, project string, limit int) ([]RecentQueryEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}

	var entries []SearchQueryLogEntry
	q := s.db.WithContext(ctx).
		Order("created_at DESC").
		Limit(limit)
	if project != "" {
		q = q.Where("project = ?", project)
	}
	if err := q.Find(&entries).Error; err != nil {
		return nil, err
	}

	result := make([]RecentQueryEntry, len(entries))
	for i, e := range entries {
		result[i] = RecentQueryEntry{
			Timestamp:  e.CreatedAt,
			Query:      e.Query,
			Project:    e.Project,
			SearchType: e.SearchType,
			Results:    e.Results,
			UsedVector: e.UsedVector,
		}
	}
	return result, nil
}

// Cleanup deletes entries older than the given duration.
func (s *SearchQueryLogStore) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	result := s.db.WithContext(ctx).
		Where("created_at < ?", cutoff).
		Delete(&SearchQueryLogEntry{})
	return result.RowsAffected, result.Error
}
