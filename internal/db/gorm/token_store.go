// Package gorm provides GORM-based database operations for engram.
package gorm

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// TokenStore provides API token database operations using GORM.
type TokenStore struct {
	db *gorm.DB
}

// NewTokenStore creates a new token store.
func NewTokenStore(store *Store) *TokenStore {
	return &TokenStore{db: store.DB}
}

// Create stores a new API token record.
func (s *TokenStore) Create(ctx context.Context, name, tokenHash, tokenPrefix, scope string) (*APIToken, error) {
	token := &APIToken{
		Name:        name,
		TokenHash:   tokenHash,
		TokenPrefix: tokenPrefix,
		Scope:       scope,
	}

	if err := s.db.WithContext(ctx).Create(token).Error; err != nil {
		return nil, err
	}

	return token, nil
}

// List returns all tokens (including revoked, for audit trail).
// Token hashes are included in the DB model but callers should
// exclude them from API responses.
func (s *TokenStore) List(ctx context.Context) ([]APIToken, error) {
	var tokens []APIToken
	err := s.db.WithContext(ctx).
		Order("created_at DESC").
		Find(&tokens).Error
	return tokens, err
}

// FindByPrefix looks up a non-revoked token by its prefix for auth middleware.
func (s *TokenStore) FindByPrefix(ctx context.Context, prefix string) (*APIToken, error) {
	var token APIToken
	err := s.db.WithContext(ctx).
		Where("token_prefix = ? AND NOT revoked", prefix).
		First(&token).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &token, nil
}

// Revoke marks a token as revoked.
func (s *TokenStore) Revoke(ctx context.Context, id string) error {
	now := time.Now()
	result := s.db.WithContext(ctx).
		Model(&APIToken{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"revoked":    true,
			"revoked_at": &now,
		})
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return result.Error
}

// IncrementStats increments request_count and updates last_used_at for a token.
func (s *TokenStore) IncrementStats(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).
		Model(&APIToken{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"request_count": gorm.Expr("request_count + 1"),
			"last_used_at":  time.Now(),
		}).Error
}

// IncrementErrorCount increments the error_count for a token.
func (s *TokenStore) IncrementErrorCount(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).
		Model(&APIToken{}).
		Where("id = ?", id).
		Update("error_count", gorm.Expr("error_count + 1")).Error
}

// GetByID retrieves a token by ID with full stats.
func (s *TokenStore) GetByID(ctx context.Context, id string) (*APIToken, error) {
	var token APIToken
	err := s.db.WithContext(ctx).First(&token, "id = ?", id).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &token, nil
}

// BatchIncrementStats increments request_count and updates last_used_at
// for multiple tokens in a single UPDATE statement. Used by the buffered
// stats flusher to reduce DB round-trips.
func (s *TokenStore) BatchIncrementStats(ctx context.Context, counts map[string]int) error {
	if len(counts) == 0 {
		return nil
	}

	// Process each token — GORM does not support CASE-based batch updates
	// cleanly, but since flush intervals are 5s with typically few distinct
	// tokens, individual UPDATEs within a transaction are acceptable.
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		for id, count := range counts {
			if err := tx.Model(&APIToken{}).
				Where("id = ?", id).
				Updates(map[string]interface{}{
					"request_count": gorm.Expr("request_count + ?", count),
					"last_used_at":  now,
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
