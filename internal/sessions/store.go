// Package sessions provides session indexing store operations.
// v5 (US3): IndexedSession removed; Store preserves the API surface but now
// returns explicit unsupported errors for indexed_sessions-backed operations.
// Chunk 3 will either wire a replacement or drop the store entirely.
package sessions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/gorm"
)

// Store provides session-indexing operations. In v5 the indexed_sessions
// table and its Go struct have been removed; this preserves call-sites while
// returning explicit unsupported errors for indexed_sessions-backed methods.
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

var ErrIndexedSessionsUnsupported = errors.New("indexed_sessions support removed in v5; capability is not available")

func unsupportedIndexedSessionsError(operation string) error {
	return fmt.Errorf("%s: %w", operation, ErrIndexedSessionsUnsupported)
}

// NewStore creates a new Store backed by the given gorm Store.
func NewStore(store *gormdb.Store) *Store {
	return &Store{db: store.GetDB(), rawDB: store.GetRawDB()}
}

// UpsertSession returns an explicit unsupported error so REST compatibility handlers
// can emit a deprecated/disabled response instead of pretending the session was indexed.
func (s *Store) UpsertSession(_ context.Context, _ map[string]any) error {
	return unsupportedIndexedSessionsError("upsert session")
}

// CheckSessionsExist returns the same sentinel as other indexed_sessions-backed
// operations so live worker endpoints can surface an explicit v5-disabled
// compatibility response instead of pretending every requested session is missing.
func (s *Store) CheckSessionsExist(_ context.Context, _ []string) ([]string, error) {
	return nil, unsupportedIndexedSessionsError("check sessions exist")
}

// ListSessions returns an explicit error because indexed_sessions was removed in v5.
func (s *Store) ListSessions(_ context.Context, _ ListOptions) ([]SessionSummary, error) {
	return nil, unsupportedIndexedSessionsError("list sessions")
}

// SearchSessions returns an explicit error because indexed_sessions was removed in v5.
func (s *Store) SearchSessions(_ context.Context, _ string, _ int) ([]SessionSearchResult, error) {
	return nil, unsupportedIndexedSessionsError("search sessions")
}

// GetSessionMtime returns an explicit error because indexed_sessions was removed in v5.
func (s *Store) GetSessionMtime(_ context.Context, _ string) (time.Time, bool, error) {
	return time.Time{}, false, unsupportedIndexedSessionsError("get session mtime")
}
