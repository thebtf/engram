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

// CheckSessionsExist returns an explicit unsupported error; indexed_sessions removed in v5.
func (s *Store) CheckSessionsExist(_ context.Context, _ []string) ([]string, error) {
	return nil, unsupportedIndexedSessionsError("check sessions exist")
}

