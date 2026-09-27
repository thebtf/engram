package gorm

import (
	"context"
	"fmt"
	"strings"
	"time"

	booksdomain "github.com/thebtf/engram/internal/books"
	gormlib "gorm.io/gorm"
)

type booksJobRecord struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement"`
	Status    string    `gorm:"column:status;not null"`
	SourceRef string    `gorm:"column:source_ref;not null"`
	Error     string    `gorm:"column:error"`
	CreatedAt time.Time `gorm:"column:created_at;not null"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null"`
}

func (booksJobRecord) TableName() string { return "books_jobs" }

// BooksStore persists the books bounded-context job lifecycle defined by the
// T016 books_jobs table.
type BooksStore struct {
	db *gormlib.DB
}

var (
	_ booksdomain.Store            = (*BooksStore)(nil)
	_ booksdomain.ResidualJobStore = (*BooksStore)(nil)
)

// NewBooksStore wraps the shared Store DB handle without opening a new pool.
func NewBooksStore(store *Store) *BooksStore {
	if store == nil {
		return &BooksStore{}
	}
	return &BooksStore{db: store.GetDB()}
}

// GetStatus reads one books job by id.
func (s *BooksStore) GetStatus(ctx context.Context, id int64) (*booksdomain.Job, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("books_store: get status: db not configured")
	}
	if id <= 0 {
		return nil, fmt.Errorf("books_store: get status: invalid id %d", id)
	}

	var record booksJobRecord
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&record).Error; err != nil {
		return nil, fmt.Errorf("books_store: get status: %w", err)
	}
	return booksJobFromRecord(record), nil
}

// RetireNonterminal preserves historical rows while marking only unfinished
// legacy work failed. Repeating it leaves terminal rows unchanged.
func (s *BooksStore) RetireNonterminal(ctx context.Context, reason string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("books_store: retire nonterminal: db not configured")
	}
	reason = strings.TrimSpace(reason)
	if reason != booksdomain.RetirementFailureReason {
		return 0, fmt.Errorf("books_store: retire nonterminal: unexpected reason %q", reason)
	}
	result := s.db.WithContext(ctx).
		Model(&booksJobRecord{}).
		Where("status IN ?", []string{string(booksdomain.StatusPending), string(booksdomain.StatusProcessing)}).
		Updates(map[string]any{
			"status":     string(booksdomain.StatusFailed),
			"error":      reason,
			"updated_at": time.Now().UTC(),
		})
	if result.Error != nil {
		return 0, fmt.Errorf("books_store: retire nonterminal: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func booksJobFromRecord(record booksJobRecord) *booksdomain.Job {
	return &booksdomain.Job{
		ID:        record.ID,
		Status:    booksdomain.Status(record.Status),
		SourceRef: record.SourceRef,
		Error:     record.Error,
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	}
}
