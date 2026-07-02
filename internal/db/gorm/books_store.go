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

var _ booksdomain.Store = (*BooksStore)(nil)

// NewBooksStore wraps the shared Store DB handle without opening a new pool.
func NewBooksStore(store *Store) *BooksStore {
	if store == nil {
		return &BooksStore{}
	}
	return &BooksStore{db: store.GetDB()}
}

// Create inserts a new books job in pending state.
func (s *BooksStore) Create(ctx context.Context, sourceRef string) (*booksdomain.Job, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("books_store: create: db not configured")
	}

	sourceRef = strings.TrimSpace(sourceRef)
	if sourceRef == "" {
		return nil, fmt.Errorf("books_store: create: source_ref required")
	}

	record := booksJobRecord{
		Status:    string(booksdomain.StatusPending),
		SourceRef: sourceRef,
	}
	if err := s.db.WithContext(ctx).Create(&record).Error; err != nil {
		return nil, fmt.Errorf("books_store: create: %w", err)
	}
	return booksJobFromRecord(record), nil
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

// UpdateStatus moves a books job between pending/processing/done/failed.
func (s *BooksStore) UpdateStatus(ctx context.Context, id int64, status booksdomain.Status, errorMessage string) (*booksdomain.Job, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("books_store: update status: db not configured")
	}
	if id <= 0 {
		return nil, fmt.Errorf("books_store: update status: invalid id %d", id)
	}
	if !isValidBookStatus(status) {
		return nil, fmt.Errorf("books_store: update status: invalid status %q", status)
	}

	updates := map[string]any{
		"status":     string(status),
		"updated_at": time.Now().UTC(),
	}
	if status == booksdomain.StatusFailed {
		updates["error"] = strings.TrimSpace(errorMessage)
	} else {
		updates["error"] = ""
	}

	result := s.db.WithContext(ctx).
		Model(&booksJobRecord{}).
		Where("id = ?", id).
		Updates(updates)
	if result.Error != nil {
		return nil, fmt.Errorf("books_store: update status: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, fmt.Errorf("books_store: update status: %w", gormlib.ErrRecordNotFound)
	}
	return s.GetStatus(ctx, id)
}

func isValidBookStatus(status booksdomain.Status) bool {
	switch status {
	case booksdomain.StatusPending, booksdomain.StatusProcessing, booksdomain.StatusDone, booksdomain.StatusFailed:
		return true
	default:
		return false
	}
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
