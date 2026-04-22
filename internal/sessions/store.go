// Package sessions provides session indexing store operations.
// v5 (US3): IndexedSession removed; Store is a lightweight stub.
// Methods that operated on the indexed_sessions table return empty results.
// Chunk 3 will either wire a replacement or drop the store entirely.
package sessions

import (
	"context"
	"database/sql"
	"time"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/gorm"
)

// Store provides session-indexing operations. In v5 the indexed_sessions
// table and its Go struct have been removed; this stub keeps call-sites
// compiling while returning safe no-op results.
type Store struct {
	db    *gorm.DB
	rawDB *sql.DB
}

// ListOptions configures a ListSessions query.
type ListOptions struct {
	WorkstationID string
	ProjectID     string
	Limit         int
	Offset        int
}

// SessionSummary is a lightweight session record returned by list/search.
// Replaces the removed gormdb.IndexedSession type.
// Fields use sql.Null* to keep existing callers compiling without changes.
type SessionSummary struct {
	ID            string
	WorkstationID string
	ProjectID     string
	ProjectPath   sql.NullString
	GitBranch     sql.NullString
	FirstMsgAt    sql.NullTime
	LastMsgAt     sql.NullTime
	ExchangeCount int
	Content       sql.NullString
	ToolCounts    sql.NullString
	FileMtime     sql.NullTime
	IndexedAt     time.Time
}

// SessionSearchResult wraps a SessionSummary with a relevance rank.
type SessionSearchResult struct {
	Session SessionSummary
	Rank    float64
}

// NewStore creates a new Store backed by the given gorm Store.
func NewStore(store *gormdb.Store) *Store {
	return &Store{db: store.GetDB(), rawDB: store.GetRawDB()}
}

// UpsertSession is a no-op stub. The indexed_sessions table and its Go type
// were removed in US3 chunk 1. Returns nil to let callers continue normally.
func (s *Store) UpsertSession(_ context.Context, _ map[string]any) error {
	return nil
}

// CheckSessionsExist returns an empty slice (no sessions indexed in v5 stub).
func (s *Store) CheckSessionsExist(_ context.Context, _ []string) ([]string, error) {
	return nil, nil
}

// ListSessions returns an empty list (indexed_sessions table removed in v5).
func (s *Store) ListSessions(_ context.Context, _ ListOptions) ([]SessionSummary, error) {
	return nil, nil
}

// SearchSessions returns an empty list (indexed_sessions table removed in v5).
func (s *Store) SearchSessions(_ context.Context, _ string, _ int) ([]SessionSearchResult, error) {
	return nil, nil
}

// GetSessionMtime always reports the session as not found (table removed in v5).
func (s *Store) GetSessionMtime(_ context.Context, _ string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
