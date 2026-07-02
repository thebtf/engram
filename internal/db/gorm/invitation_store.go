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
	ErrInvitationUsed          = errors.New("invitation already used")
	ErrInvitationExpired       = errors.New("invitation expired")
	ErrInvitationRevoked       = errors.New("invitation revoked")
	ErrInvitationEmailMismatch = errors.New("invitation email mismatch")
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
func (s *InvitationStore) CreateInvitation(code string, createdByID int64, email, role string, expiresAt time.Time) (*Invitation, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, fmt.Errorf("create invitation: code must not be empty")
	}
	role, err := NormalizeDashboardRole(role)
	if err != nil {
		return nil, fmt.Errorf("create invitation: %w", err)
	}
	now := time.Now().UTC()
	if expiresAt.IsZero() {
		expiresAt = now.Add(7 * 24 * time.Hour)
	}
	expiresAt = expiresAt.UTC()
	if !expiresAt.After(now) {
		return nil, fmt.Errorf("create invitation: expires_at must be in the future")
	}

	inv := &Invitation{
		Code:      code,
		Email:     strings.TrimSpace(email),
		Role:      role,
		CreatedBy: createdByID,
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}
	if err := s.db.Create(inv).Error; err != nil {
		return nil, fmt.Errorf("create invitation: %w", err)
	}
	return inv, nil
}

// GetInvitationByID returns one invitation row without lifecycle filtering.
func (s *InvitationStore) GetInvitationByID(id int64) (*Invitation, error) {
	if id <= 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var inv Invitation
	if err := s.db.First(&inv, id).Error; err != nil {
		return nil, err
	}
	return &inv, nil
}

// GetValidInvitation returns a still-usable invitation matching the given code.
func (s *InvitationStore) GetValidInvitation(code string) (*Invitation, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var inv Invitation
	if err := s.db.Where("code = ?", code).First(&inv).Error; err != nil {
		return nil, err
	}
	return validateInvitationRow(&inv)
}

// ConsumeInvitation marks the invitation as used by the given user.
// Returns an error if the code does not exist or is not consumable.
func (s *InvitationStore) ConsumeInvitation(code string, usedByID int64) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return gorm.ErrRecordNotFound
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var inv Invitation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("code = ?", code).First(&inv).Error; err != nil {
			return err
		}
		if _, err := validateInvitationRow(&inv); err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := tx.Model(&Invitation{}).
			Where("id = ? AND used_by IS NULL AND revoked_at IS NULL", inv.ID).
			Updates(map[string]any{"used_by": usedByID, "used_at": now}).Error; err != nil {
			return fmt.Errorf("consume invitation: %w", err)
		}
		return nil
	})
}

// RevokeInvitation marks one invitation unusable without deleting the row.
// The returned bool reports whether the call changed lifecycle state.
func (s *InvitationStore) RevokeInvitation(id int64, revokedBy *int64, reason string) (bool, error) {
	if id <= 0 {
		return false, gorm.ErrRecordNotFound
	}
	reason = strings.TrimSpace(reason)
	var changed bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var inv Invitation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&inv, id).Error; err != nil {
			return err
		}
		if inv.UsedBy != nil || inv.UsedAt != nil {
			return ErrInvitationUsed
		}
		if inv.RevokedAt != nil {
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
		if err := tx.Model(&Invitation{}).Where("id = ? AND revoked_at IS NULL AND used_by IS NULL", id).Updates(updates).Error; err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

// ListInvitations returns all invitations ordered by creation time descending.
func (s *InvitationStore) ListInvitations() ([]*Invitation, error) {
	var invitations []*Invitation
	if err := s.db.Order("created_at DESC").Find(&invitations).Error; err != nil {
		return nil, err
	}
	return invitations, nil
}

func validateInvitationRow(inv *Invitation) (*Invitation, error) {
	if inv == nil {
		return nil, gorm.ErrRecordNotFound
	}
	now := time.Now().UTC()
	if inv.UsedBy != nil || inv.UsedAt != nil {
		return nil, ErrInvitationUsed
	}
	if inv.RevokedAt != nil {
		return nil, ErrInvitationRevoked
	}
	if !inv.ExpiresAt.After(now) {
		return nil, ErrInvitationExpired
	}
	return inv, nil
}
