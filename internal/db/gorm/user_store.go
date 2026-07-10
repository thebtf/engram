package gorm

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	initialAdminSetupLockNamespace int32 = 0x454e4752 // "ENGR"
	initialAdminSetupLockOperation int32 = 0x41444d49 // "ADMI"
)

// InitialAdminSetupAlreadyCompletedError reports that first-user setup lost
// the serialized zero-user check because another process completed it first.
type InitialAdminSetupAlreadyCompletedError struct{}

func (*InitialAdminSetupAlreadyCompletedError) Error() string {
	return "setup already completed"
}

// ErrInitialAdminSetupAlreadyCompleted is returned when an initial admin
// already exists by the time the cross-process setup lock is acquired.
var ErrInitialAdminSetupAlreadyCompleted = &InitialAdminSetupAlreadyCompletedError{}

// UserStore provides CRUD operations for dashboard users.
type UserStore struct {
	db *gorm.DB
}

// NewUserStore creates a new UserStore.
func NewUserStore(db *gorm.DB) *UserStore {
	return &UserStore{db: db}
}

// CreateUser inserts a new user record.
func (s *UserStore) CreateUser(email, passwordHash, role string) (*User, error) {
	user := &User{
		Email:        email,
		PasswordHash: passwordHash,
		Role:         role,
		CreatedAt:    time.Now(),
	}
	if err := s.db.Create(user).Error; err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	return user, nil
}

// CreateInitialAdmin atomically creates the first user as an administrator.
// PostgreSQL transaction-scoped advisory locking serializes setup across all
// server processes connected to the same database.
func (s *UserStore) CreateInitialAdmin(ctx context.Context, email, passwordHash string) (*User, error) {
	user := &User{
		Email:        email,
		PasswordHash: passwordHash,
		Role:         DashboardRoleAdmin,
		CreatedAt:    time.Now(),
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			"SELECT pg_advisory_xact_lock(?, ?)",
			initialAdminSetupLockNamespace,
			initialAdminSetupLockOperation,
		).Error; err != nil {
			return fmt.Errorf("lock initial admin setup: %w", err)
		}

		var count int64
		if err := tx.Model(&User{}).Count(&count).Error; err != nil {
			return fmt.Errorf("count users for initial admin setup: %w", err)
		}
		if count > 0 {
			return ErrInitialAdminSetupAlreadyCompleted
		}
		if err := tx.Create(user).Error; err != nil {
			return fmt.Errorf("create initial admin: %w", err)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// GetUserByEmail looks up a user by email address.
func (s *UserStore) GetUserByEmail(email string) (*User, error) {
	var user User
	if err := s.db.Where("email = ?", email).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUserByID looks up a user by primary key.
func (s *UserStore) GetUserByID(id int64) (*User, error) {
	var user User
	if err := s.db.First(&user, id).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// ListUsers returns all users ordered by creation time ascending.
func (s *UserStore) ListUsers() ([]*User, error) {
	var users []*User
	if err := s.db.Order("created_at ASC").Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

// UpdateUser applies a partial update to the user with the given ID.
func (s *UserStore) UpdateUser(id int64, updates map[string]any) error {
	result := s.db.Model(&User{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// CountUsers returns the total number of user records.
func (s *UserStore) CountUsers() (int64, error) {
	var count int64
	if err := s.db.Model(&User{}).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// CountAdmins returns the number of active (non-disabled) admin users.
func (s *UserStore) CountAdmins() (int64, error) {
	var count int64
	if err := s.db.Model(&User{}).Where("role = ? AND disabled = false", "admin").Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// UpdateUserWithLastAdminGuard applies the requested role/disabled changes while
// holding row locks on the target user and active admin set so concurrent
// demote/disable operations cannot leave the system with zero active admins.
func (s *UserStore) UpdateUserWithLastAdminGuard(id int64, role *string, disabled *bool) (*User, error) {
	if id <= 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if role == nil && disabled == nil {
		return nil, fmt.Errorf("no updates provided")
	}
	var updated User
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var adminIDs []int64
		if err := tx.Model(&User{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("role = ? AND disabled = false", DashboardRoleAdmin).
			Order("id").
			Pluck("id", &adminIDs).Error; err != nil {
			return err
		}

		var current User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, id).Error; err != nil {
			return err
		}

		revokingAdmin := !current.Disabled && current.Role == DashboardRoleAdmin && ((disabled != nil && *disabled) || (role != nil && *role != DashboardRoleAdmin))
		if revokingAdmin && len(adminIDs) <= 1 {
			if disabled != nil && *disabled {
				return fmt.Errorf("cannot disable the last admin")
			}
			return fmt.Errorf("cannot demote the last admin")
		}

		updates := map[string]any{}
		if disabled != nil {
			updates["disabled"] = *disabled
		}
		if role != nil {
			updates["role"] = *role
		}
		result := tx.Model(&User{}).Where("id = ?", id).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return tx.First(&updated, id).Error
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}
