package gorm

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

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
