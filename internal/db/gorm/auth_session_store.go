package gorm

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrAuthSessionExpired = errors.New("auth session expired")
	ErrAuthSessionRevoked = errors.New("auth session revoked")
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
func (s *AuthSessionStore) CreateSession(userID int64, duration time.Duration, userAgent, remoteAddr string) (*AuthSession, error) {
	id, err := generateSessionID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	sess := &AuthSession{
		ID:         id,
		UserID:     userID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(duration),
		UserAgent:  strings.TrimSpace(userAgent),
		RemoteAddr: strings.TrimSpace(remoteAddr),
	}
	if err := s.db.Create(sess).Error; err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return sess, nil
}

// GetAnySession returns the session row without lifecycle filtering.
func (s *AuthSessionStore) GetAnySession(id string) (*AuthSession, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var sess AuthSession
	if err := s.db.Where("id = ?", id).First(&sess).Error; err != nil {
		return nil, err
	}
	return &sess, nil
}

// GetSession returns the session with the given ID if it exists and remains active.
func (s *AuthSessionStore) GetSession(id string) (*AuthSession, error) {
	sess, err := s.GetAnySession(id)
	if err != nil {
		return nil, err
	}
	return validateSessionRow(sess)
}

// RevokeSession soft-revokes a single session by ID and reports whether it changed lifecycle state.
func (s *AuthSessionStore) RevokeSession(id string, revokedBy *int64, reason string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, gorm.ErrRecordNotFound
	}
	reason = strings.TrimSpace(reason)
	var changed bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var sess AuthSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&sess).Error; err != nil {
			return err
		}
		if sess.RevokedAt != nil {
			changed = false
			return nil
		}
		now := time.Now().UTC()
		updates := map[string]any{
			"revoked_at":        now,
			"revocation_reason": reason,
		}
		if revokedBy != nil {
			updates["revoked_by"] = *revokedBy
		}
		if err := tx.Model(&AuthSession{}).Where("id = ? AND revoked_at IS NULL", id).Updates(updates).Error; err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

// DeleteSession revokes a single session by ID.
func (s *AuthSessionStore) DeleteSession(id string) error {
	_, err := s.RevokeSession(id, nil, "logout")
	return err
}

// DeleteUserSessions revokes all sessions belonging to the given user.
func (s *AuthSessionStore) DeleteUserSessions(userID int64, revokedBy *int64, reason string) error {
	if userID <= 0 {
		return nil
	}
	reason = strings.TrimSpace(reason)
	now := time.Now().UTC()
	updates := map[string]any{
		"revoked_at":        now,
		"revocation_reason": reason,
	}
	if revokedBy != nil {
		updates["revoked_by"] = *revokedBy
	}
	return s.db.Model(&AuthSession{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Updates(updates).Error
}

// CleanExpired deletes all sessions whose expiry timestamp is in the past.
// Returns the number of rows deleted.
func (s *AuthSessionStore) CleanExpired() (int64, error) {
	result := s.db.Where("expires_at < ?", time.Now().UTC()).Delete(&AuthSession{})
	return result.RowsAffected, result.Error
}

func validateSessionRow(sess *AuthSession) (*AuthSession, error) {
	if sess == nil {
		return nil, gorm.ErrRecordNotFound
	}
	now := time.Now().UTC()
	if sess.RevokedAt != nil {
		return nil, ErrAuthSessionRevoked
	}
	if !sess.ExpiresAt.After(now) {
		return nil, ErrAuthSessionExpired
	}
	return sess, nil
}

// generateSessionID produces a cryptographically random 64-character hex token.
func generateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}
