package gorm

import (
	"fmt"
	"strings"
	"time"
)

const (
	DashboardRoleAdmin    = "admin"
	DashboardRoleOperator = "operator"
)

// DashboardRoles returns the supported dashboard-user roles in stable order.
func DashboardRoles() []string {
	return []string{DashboardRoleAdmin, DashboardRoleOperator}
}

// NormalizeDashboardRole trims and validates one dashboard-user role.
func NormalizeDashboardRole(role string) (string, error) {
	normalized := strings.TrimSpace(strings.ToLower(role))
	switch normalized {
	case "", DashboardRoleOperator:
		return DashboardRoleOperator, nil
	case DashboardRoleAdmin:
		return DashboardRoleAdmin, nil
	default:
		return "", fmt.Errorf("role %q must be one of %s", role, strings.Join(DashboardRoles(), ", "))
	}
}

// User represents a dashboard operator.
type User struct {
	ID           int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	Email        string     `gorm:"uniqueIndex;size:255;not null" json:"email"`
	PasswordHash string     `gorm:"size:255;not null;default:''" json:"-"`
	Role         string     `gorm:"size:20;not null;default:operator" json:"role"`
	Disabled     bool       `gorm:"not null;default:false" json:"disabled"`
	CreatedAt    time.Time  `gorm:"not null" json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

func (User) TableName() string { return "users" }

// Invitation is a single-use registration code.
type Invitation struct {
	ID               int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	Code             string     `gorm:"uniqueIndex;size:64;not null" json:"code"`
	Email            string     `gorm:"column:invitee_email;type:text;not null;default:''" json:"email"`
	Role             string     `gorm:"size:20;not null;default:operator" json:"role"`
	CreatedBy        int64      `gorm:"not null" json:"created_by"`
	UsedBy           *int64     `json:"used_by,omitempty"`
	UsedAt           *time.Time `json:"used_at,omitempty"`
	ExpiresAt        time.Time  `gorm:"not null" json:"expires_at"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	RevokedBy        *int64     `json:"revoked_by,omitempty"`
	RevocationReason string     `gorm:"type:text;not null;default:''" json:"revocation_reason"`
	CreatedAt        time.Time  `gorm:"not null" json:"created_at"`
}

func (Invitation) TableName() string { return "invitations" }

// AuthSession represents an authenticated dashboard session.
type AuthSession struct {
	ID               string     `gorm:"primaryKey;size:64" json:"id"`
	UserID           int64      `gorm:"not null" json:"user_id"`
	CreatedAt        time.Time  `gorm:"not null" json:"created_at"`
	ExpiresAt        time.Time  `gorm:"not null" json:"expires_at"`
	UserAgent        string     `gorm:"type:text;not null;default:''" json:"user_agent"`
	RemoteAddr       string     `gorm:"type:text;not null;default:''" json:"remote_addr"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	RevokedBy        *int64     `json:"revoked_by,omitempty"`
	RevocationReason string     `gorm:"type:text;not null;default:''" json:"revocation_reason"`
}

func (AuthSession) TableName() string { return "sessions" }
