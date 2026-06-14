package gorm

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// SessionTranscript is the GORM model for the session_transcripts table.
// Each row holds the raw content of one completed session, pending async
// LLM analysis. Once a worker processes it, processed_at is stamped.
type SessionTranscript struct {
	ID          int64      `gorm:"column:id;primaryKey;autoIncrement"`
	SessionID   string     `gorm:"column:session_id;not null"`
	Project     string     `gorm:"column:project;not null"`
	Content     string     `gorm:"column:content;not null"`
	ByteLen     int        `gorm:"column:byte_len;not null"`
	CreatedAt   time.Time  `gorm:"column:created_at;not null;autoCreateTime:false"`
	ProcessedAt *time.Time `gorm:"column:processed_at"`
}

// TableName satisfies the gorm.Tabler interface.
func (SessionTranscript) TableName() string { return "session_transcripts" }

// TranscriptStore handles persistence for the session_transcripts table.
type TranscriptStore struct {
	db *gorm.DB
}

// NewTranscriptStore creates a TranscriptStore backed by db.
func NewTranscriptStore(db *gorm.DB) *TranscriptStore {
	return &TranscriptStore{db: db}
}

// Create inserts t into the database. If t.ByteLen is 0 it is computed
// from len(t.Content) so callers do not have to set it explicitly.
// CreatedAt is set to now() by the database DEFAULT; the Go-side value
// is left as-is so tests can supply an explicit timestamp when needed.
func (s *TranscriptStore) Create(ctx context.Context, t *SessionTranscript) error {
	if t.ByteLen == 0 {
		t.ByteLen = len(t.Content)
	}
	// Use Omit("ProcessedAt") so a nil ProcessedAt is stored as NULL rather
	// than the zero time that GORM would otherwise write.
	return s.db.WithContext(ctx).Omit("ProcessedAt").Create(t).Error
}

// ListUnprocessedSince returns all rows with processed_at IS NULL and
// created_at >= watermark, ordered by created_at ascending.
func (s *TranscriptStore) ListUnprocessedSince(ctx context.Context, watermark time.Time) ([]SessionTranscript, error) {
	var rows []SessionTranscript
	err := s.db.WithContext(ctx).
		Where("processed_at IS NULL AND created_at >= ?", watermark).
		Order("created_at ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("transcript store list unprocessed: %w", err)
	}
	return rows, nil
}

// MarkProcessed stamps processed_at = now() for the given ids.
// It is a no-op when ids is empty.
func (s *TranscriptStore) MarkProcessed(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	err := s.db.WithContext(ctx).
		Model(&SessionTranscript{}).
		Where("id IN ?", ids).
		Update("processed_at", gorm.Expr("now()")).Error
	if err != nil {
		return fmt.Errorf("transcript store mark processed: %w", err)
	}
	return nil
}

// PruneProcessed deletes all rows where processed_at IS NOT NULL and
// returns the number of rows deleted.
func (s *TranscriptStore) PruneProcessed(ctx context.Context) (int64, error) {
	result := s.db.WithContext(ctx).
		Where("processed_at IS NOT NULL").
		Delete(&SessionTranscript{})
	if result.Error != nil {
		return 0, fmt.Errorf("transcript store prune processed: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// PruneUnprocessedOlderThan deletes rows where processed_at IS NULL and
// created_at is older than days days. Returns (0, nil) immediately when
// days <= 0 — a zero value means "no prune of unprocessed", not "prune all".
func (s *TranscriptStore) PruneUnprocessedOlderThan(ctx context.Context, days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	// days is an int validated above (> 0) — not user-controlled string input —
	// so embedding it directly in the interval literal is injection-safe and
	// matches the established codebase pattern (session_store.go uses the same
	// 'N days'::interval form). Avoids relying on driver-specific quoting of a
	// parameterised ?::interval, which can vary between pgx/lib/pq.
	intervalClause := fmt.Sprintf("processed_at IS NULL AND created_at < now() - '%d days'::interval", days)
	result := s.db.WithContext(ctx).
		Where(intervalClause).
		Delete(&SessionTranscript{})
	if result.Error != nil {
		return 0, fmt.Errorf("transcript store prune unprocessed older than %d days: %w", days, result.Error)
	}
	return result.RowsAffected, nil
}
