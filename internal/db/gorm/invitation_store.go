package gorm

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// InvitationStore provides CRUD operations for single-use invitation codes.
type InvitationStore struct {
	db *gorm.DB
}

// NewInvitationStore creates a new InvitationStore.
func NewInvitationStore(db *gorm.DB) *InvitationStore {
	return &InvitationStore{db: db}
}

// GenerateCode produces a cryptographically random 64-character hex token.
func (s *InvitationStore) GenerateCode() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate invitation code: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// CreateInvitation inserts a new invitation record.
func (s *InvitationStore) CreateInvitation(code string, createdByID int64) (*Invitation, error) {
	inv := &Invitation{
		Code:      code,
		CreatedBy: createdByID,
		CreatedAt: time.Now(),
	}
	if err := s.db.Create(inv).Error; err != nil {
		return nil, fmt.Errorf("create invitation: %w", err)
	}
	return inv, nil
}

// GetValidInvitation returns an unused invitation matching the given code.
func (s *InvitationStore) GetValidInvitation(code string) (*Invitation, error) {
	var inv Invitation
	if err := s.db.Where("code = ? AND used_by IS NULL", code).First(&inv).Error; err != nil {
		return nil, err
	}
	return &inv, nil
}

// ConsumeInvitation marks the invitation as used by the given user.
// Returns an error if the code does not exist or was already consumed.
func (s *InvitationStore) ConsumeInvitation(code string, usedByID int64) error {
	now := time.Now()
	result := s.db.Model(&Invitation{}).
		Where("code = ? AND used_by IS NULL", code).
		Updates(map[string]any{"used_by": usedByID, "used_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("invitation already used or not found")
	}
	return nil
}

// ListInvitations returns all invitations ordered by creation time descending.
func (s *InvitationStore) ListInvitations() ([]*Invitation, error) {
	var invitations []*Invitation
	if err := s.db.Order("created_at DESC").Find(&invitations).Error; err != nil {
		return nil, err
	}
	return invitations, nil
}
