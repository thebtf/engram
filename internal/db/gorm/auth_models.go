package gorm

import "time"

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
	ID        int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	Code      string     `gorm:"uniqueIndex;size:64;not null" json:"code"`
	CreatedBy int64      `gorm:"not null" json:"created_by"`
	UsedBy    *int64     `json:"used_by,omitempty"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
	CreatedAt time.Time  `gorm:"not null" json:"created_at"`
}

func (Invitation) TableName() string { return "invitations" }

// AuthSession represents an authenticated dashboard session.
type AuthSession struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	UserID    int64     `gorm:"not null" json:"user_id"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	ExpiresAt time.Time `gorm:"not null" json:"expires_at"`
}

func (AuthSession) TableName() string { return "sessions" }
