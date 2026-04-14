package gorm

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// AuthSessionStore provides CRUD operations for dashboard authentication sessions.
type AuthSessionStore struct {
	db *gorm.DB
}

// NewAuthSessionStore creates a new AuthSessionStore.
func NewAuthSessionStore(db *gorm.DB) *AuthSessionStore {
	return &AuthSessionStore{db: db}
}

// CreateSession generates a new session for the given user with the specified lifetime.
func (s *AuthSessionStore) CreateSession(userID int64, duration time.Duration) (*AuthSession, error) {
	id, err := generateSessionID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	sess := &AuthSession{
		ID:        id,
		UserID:    userID,
		CreatedAt: now,
		ExpiresAt: now.Add(duration),
	}
	if err := s.db.Create(sess).Error; err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return sess, nil
}

// GetSession returns the session with the given ID if it exists and has not expired.
func (s *AuthSessionStore) GetSession(id string) (*AuthSession, error) {
	var sess AuthSession
	if err := s.db.Where("id = ? AND expires_at > ?", id, time.Now()).First(&sess).Error; err != nil {
		return nil, err
	}
	return &sess, nil
}

// DeleteSession removes a single session by ID.
func (s *AuthSessionStore) DeleteSession(id string) error {
	return s.db.Where("id = ?", id).Delete(&AuthSession{}).Error
}

// DeleteUserSessions removes all sessions belonging to the given user.
func (s *AuthSessionStore) DeleteUserSessions(userID int64) error {
	return s.db.Where("user_id = ?", userID).Delete(&AuthSession{}).Error
}

// CleanExpired deletes all sessions whose expiry timestamp is in the past.
// Returns the number of rows deleted.
func (s *AuthSessionStore) CleanExpired() (int64, error) {
	result := s.db.Where("expires_at < ?", time.Now()).Delete(&AuthSession{})
	return result.RowsAffected, result.Error
}

// generateSessionID produces a cryptographically random 64-character hex token.
func generateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}
