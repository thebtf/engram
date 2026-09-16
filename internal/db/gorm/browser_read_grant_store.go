package gorm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BrowserReadGrantState is the lifecycle state for one exact browser code-read grant.
type BrowserReadGrantState string

const (
	BrowserReadGrantActive  BrowserReadGrantState = "active"
	BrowserReadGrantRevoked BrowserReadGrantState = "revoked"
	BrowserReadGrantExpired BrowserReadGrantState = "expired"
)

var (
	// ErrBrowserReadGrantDenied intentionally does not distinguish absent, foreign,
	// disabled, or expired grant state.
	ErrBrowserReadGrantDenied             = errors.New("browser read grant denied")
	errBrowserReadGrantStoreNotConfigured = errors.New("browser read grant store is not configured")
)

// BrowserReadGrant persists the minimum authority needed to read one exact source checkout.
type BrowserReadGrant struct {
	GrantRef        string                `gorm:"column:grant_ref;type:uuid;primaryKey" json:"grant_ref"`
	AuthRealm       string                `gorm:"column:auth_realm;type:text;not null;uniqueIndex:idx_browser_read_grant_tuple,priority:1" json:"auth_realm"`
	SubjectUserID   int64                 `gorm:"column:subject_user_id;not null;uniqueIndex:idx_browser_read_grant_tuple,priority:2" json:"subject_user_id"`
	SourceID        string                `gorm:"column:source_id;type:uuid;not null;uniqueIndex:idx_browser_read_grant_tuple,priority:3" json:"source_id"`
	CheckoutID      string                `gorm:"column:checkout_id;type:uuid;not null;uniqueIndex:idx_browser_read_grant_tuple,priority:4" json:"checkout_id"`
	State           BrowserReadGrantState `gorm:"column:state;type:text;not null" json:"state"`
	IssuerPrincipal string                `gorm:"column:issuer_principal;type:text;not null" json:"issuer_principal"`
	ExpiresAt       *time.Time            `gorm:"column:expires_at;type:timestamptz" json:"expires_at,omitempty"`
	IssuedAt        time.Time             `gorm:"column:issued_at;type:timestamptz;not null" json:"issued_at"`
	RevokedAt       *time.Time            `gorm:"column:revoked_at;type:timestamptz" json:"revoked_at,omitempty"`
	CreatedAt       time.Time             `gorm:"column:created_at;type:timestamptz;not null" json:"created_at"`
	UpdatedAt       time.Time             `gorm:"column:updated_at;type:timestamptz;not null" json:"updated_at"`
}

func (BrowserReadGrant) TableName() string { return "browser_read_grants" }

// BrowserReadGrantIssue holds the already-authenticated request boundary for grant issuance.
type BrowserReadGrantIssue struct {
	IssuerUserID    int64
	IssuerPrincipal string
	TargetUserID    int64
	SourceID        string
	CheckoutID      string
	ExpiresAt       *time.Time
}

// BrowserReadGrantStore is the only persistence owner for browser code-read grants.
type BrowserReadGrantStore struct {
	db *gorm.DB
}

// NewBrowserReadGrantStore creates a store backed by db.
func NewBrowserReadGrantStore(db *gorm.DB) *BrowserReadGrantStore {
	return &BrowserReadGrantStore{db: db}
}

// Issue creates or restores exactly one grant after rechecking the source-owner tuple,
// enabled target user, and audit append in one transaction.
func (s *BrowserReadGrantStore) Issue(ctx context.Context, in BrowserReadGrantIssue) (BrowserReadGrant, error) {
	if err := validateBrowserReadGrantIssue(ctx, in); err != nil {
		return BrowserReadGrant{}, err
	}
	if err := s.requireDB("issue"); err != nil {
		return BrowserReadGrant{}, err
	}

	now := time.Now().UTC()
	var result BrowserReadGrant
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := loadEnabledBrowserGrantUser(ctx, tx, in.IssuerUserID); err != nil {
			return err
		}

		tuple, err := loadBrowserReadGrantTuple(ctx, tx, in.SourceID, in.CheckoutID)
		if err != nil {
			return err
		}
		if tuple.OwnerPrincipal != in.IssuerPrincipal {
			return ErrBrowserReadGrantDenied
		}
		if err := loadEnabledBrowserGrantUser(ctx, tx, in.TargetUserID); err != nil {
			return err
		}

		var grant BrowserReadGrant
		err = tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("auth_realm = ? AND subject_user_id = ? AND source_id = ? AND checkout_id = ?", tuple.AuthRealm, in.TargetUserID, in.SourceID, in.CheckoutID).
			First(&grant).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			grant = BrowserReadGrant{
				GrantRef:        uuid.NewString(),
				AuthRealm:       tuple.AuthRealm,
				SubjectUserID:   in.TargetUserID,
				SourceID:        in.SourceID,
				CheckoutID:      in.CheckoutID,
				State:           BrowserReadGrantActive,
				IssuerPrincipal: in.IssuerPrincipal,
				ExpiresAt:       copyBrowserReadGrantExpiry(in.ExpiresAt),
				IssuedAt:        now,
				CreatedAt:       now,
				UpdatedAt:       now,
			}
			if err := tx.WithContext(ctx).Create(&grant).Error; err != nil {
				return fmt.Errorf("browser read grant issue create: %w", err)
			}
		case err != nil:
			return fmt.Errorf("browser read grant issue lookup: %w", err)
		default:
			grant.State = BrowserReadGrantActive
			grant.IssuerPrincipal = in.IssuerPrincipal
			grant.ExpiresAt = copyBrowserReadGrantExpiry(in.ExpiresAt)
			grant.IssuedAt = now
			grant.RevokedAt = nil
			grant.UpdatedAt = now
			if err := tx.WithContext(ctx).Save(&grant).Error; err != nil {
				return fmt.Errorf("browser read grant issue restore: %w", err)
			}
		}

		if err := NewAuditStore(tx).LogTx(ctx, tx, AuditLogEntry{
			Action: "code_grant_issued",
			Actor:  in.IssuerPrincipal,
			Reason: browserReadGrantAuditReason(grant),
		}); err != nil {
			return fmt.Errorf("browser read grant issue audit: %w", err)
		}
		result = grant
		return nil
	})
	if err != nil {
		return BrowserReadGrant{}, err
	}
	return result, nil
}

// Revoke transitions one grant after rechecking that the requester remains the exact source owner.
func (s *BrowserReadGrantStore) Revoke(ctx context.Context, issuerUserID int64, issuerPrincipal, grantRef string) (BrowserReadGrant, error) {
	if err := validateBrowserReadGrantRequest(ctx, issuerUserID, issuerPrincipal, grantRef); err != nil {
		return BrowserReadGrant{}, err
	}
	if err := s.requireDB("revoke"); err != nil {
		return BrowserReadGrant{}, err
	}

	now := time.Now().UTC()
	var result BrowserReadGrant
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := loadEnabledBrowserGrantUser(ctx, tx, issuerUserID); err != nil {
			return err
		}

		var grant BrowserReadGrant
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("grant_ref = ?", grantRef).First(&grant).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBrowserReadGrantDenied
			}
			return fmt.Errorf("browser read grant revoke lookup: %w", err)
		}
		tuple, err := loadBrowserReadGrantTuple(ctx, tx, grant.SourceID, grant.CheckoutID)
		if err != nil {
			return err
		}
		if tuple.AuthRealm != grant.AuthRealm || tuple.OwnerPrincipal != issuerPrincipal {
			return ErrBrowserReadGrantDenied
		}
		if grant.State != BrowserReadGrantRevoked {
			grant.State = BrowserReadGrantRevoked
			grant.RevokedAt = &now
			grant.UpdatedAt = now
			if err := tx.WithContext(ctx).Save(&grant).Error; err != nil {
				return fmt.Errorf("browser read grant revoke update: %w", err)
			}
			if err := NewAuditStore(tx).LogTx(ctx, tx, AuditLogEntry{
				Action: "code_grant_revoked",
				Actor:  issuerPrincipal,
				Reason: browserReadGrantAuditReason(grant),
			}); err != nil {
				return fmt.Errorf("browser read grant revoke audit: %w", err)
			}
		}
		result = grant
		return nil
	})
	if err != nil {
		return BrowserReadGrant{}, err
	}
	return result, nil
}

// Current returns the subject's one active, unexpired grant. It intentionally
// treats zero and multiple grants as the same unselected result.
func (s *BrowserReadGrantStore) Current(ctx context.Context, subjectUserID int64) (BrowserReadGrant, bool, error) {
	if ctx == nil {
		return BrowserReadGrant{}, false, fmt.Errorf("browser read grant current: context is required")
	}
	if err := ctx.Err(); err != nil {
		return BrowserReadGrant{}, false, err
	}
	if subjectUserID <= 0 {
		return BrowserReadGrant{}, false, nil
	}
	if err := s.requireDB("current"); err != nil {
		return BrowserReadGrant{}, false, err
	}

	now := time.Now().UTC()
	if err := s.expireDue(ctx, subjectUserID, now); err != nil {
		return BrowserReadGrant{}, false, err
	}
	var grants []BrowserReadGrant
	err := s.db.WithContext(ctx).Table("browser_read_grants AS browser_grant").
		Select("browser_grant.*").
		Joins("JOIN sources AS source ON source.source_id = browser_grant.source_id AND source.auth_realm = browser_grant.auth_realm").
		Joins("JOIN ci_checkouts AS checkout ON checkout.checkout_id = browser_grant.checkout_id AND checkout.source_id = browser_grant.source_id").
		Joins("JOIN users AS subject ON subject.id = browser_grant.subject_user_id").
		Where("browser_grant.subject_user_id = ? AND browser_grant.state = ? AND (browser_grant.expires_at IS NULL OR browser_grant.expires_at > ?) AND subject.disabled = FALSE", subjectUserID, BrowserReadGrantActive, now).
		Limit(2).
		Find(&grants).Error
	if err != nil {
		return BrowserReadGrant{}, false, fmt.Errorf("browser read grant current: %w", err)
	}
	if len(grants) != 1 {
		return BrowserReadGrant{}, false, nil
	}
	return grants[0], true, nil
}

// Active returns one exact active grant, including its issuance epoch. Unlike
// Current, it remains well-defined when the subject holds grants for several
// checkouts and is therefore the only grant lookup suitable for a pinned tab.
func (s *BrowserReadGrantStore) Active(ctx context.Context, subjectUserID int64, sourceID, checkoutID string) (BrowserReadGrant, bool, error) {
	if ctx == nil {
		return BrowserReadGrant{}, false, fmt.Errorf("browser read grant active: context is required")
	}
	if err := ctx.Err(); err != nil {
		return BrowserReadGrant{}, false, err
	}
	if subjectUserID <= 0 || validateUCIUUID("source_id", sourceID) != nil || validateUCIUUID("checkout_id", checkoutID) != nil {
		return BrowserReadGrant{}, false, nil
	}
	if err := s.requireDB("active"); err != nil {
		return BrowserReadGrant{}, false, err
	}

	now := time.Now().UTC()
	var grant BrowserReadGrant
	result := s.db.WithContext(ctx).Table("browser_read_grants AS browser_grant").
		Select("browser_grant.*").
		Joins("JOIN sources AS source ON source.source_id = browser_grant.source_id AND source.auth_realm = browser_grant.auth_realm").
		Joins("JOIN ci_checkouts AS checkout ON checkout.checkout_id = browser_grant.checkout_id AND checkout.source_id = browser_grant.source_id").
		Joins("JOIN users AS subject ON subject.id = browser_grant.subject_user_id").
		Where("browser_grant.subject_user_id = ? AND browser_grant.source_id = ? AND browser_grant.checkout_id = ?", subjectUserID, sourceID, checkoutID).
		Where("browser_grant.state = ? AND (browser_grant.expires_at IS NULL OR browser_grant.expires_at > ?) AND subject.disabled = FALSE", BrowserReadGrantActive, now).
		First(&grant)
	if result.Error == nil {
		return grant, true, nil
	}
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return BrowserReadGrant{}, false, fmt.Errorf("browser read grant active: %w", result.Error)
	}
	if err := s.db.WithContext(ctx).Model(&BrowserReadGrant{}).
		Where("subject_user_id = ? AND source_id = ? AND checkout_id = ? AND state = ? AND expires_at IS NOT NULL AND expires_at <= ?", subjectUserID, sourceID, checkoutID, BrowserReadGrantActive, now).
		Updates(map[string]any{"state": BrowserReadGrantExpired, "updated_at": now}).Error; err != nil {
		return BrowserReadGrant{}, false, fmt.Errorf("browser read grant expire: %w", err)
	}
	return BrowserReadGrant{}, false, nil
}

// CanRead returns true only while the exact enabled user grant remains active and unexpired.
func (s *BrowserReadGrantStore) CanRead(ctx context.Context, subjectUserID int64, sourceID, checkoutID string) (bool, error) {
	_, active, err := s.Active(ctx, subjectUserID, sourceID, checkoutID)
	return active, err
}

func (s *BrowserReadGrantStore) expireDue(ctx context.Context, subjectUserID int64, now time.Time) error {
	if err := s.db.WithContext(ctx).Model(&BrowserReadGrant{}).
		Where("subject_user_id = ? AND state = ? AND expires_at IS NOT NULL AND expires_at <= ?", subjectUserID, BrowserReadGrantActive, now).
		Updates(map[string]any{"state": BrowserReadGrantExpired, "updated_at": now}).Error; err != nil {
		return fmt.Errorf("browser read grant expire: %w", err)
	}
	return nil
}

type browserReadGrantTuple struct {
	AuthRealm      string `gorm:"column:auth_realm"`
	OwnerPrincipal string `gorm:"column:owner_principal"`
}

func loadBrowserReadGrantTuple(ctx context.Context, tx *gorm.DB, sourceID, checkoutID string) (browserReadGrantTuple, error) {
	var tuple browserReadGrantTuple
	result := tx.WithContext(ctx).Table("ci_checkouts AS checkout").
		Select("source.auth_realm, checkout.owner_principal").
		Joins("JOIN sources AS source ON source.source_id = checkout.source_id").
		Where("checkout.checkout_id = ? AND checkout.source_id = ?", checkoutID, sourceID).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Scan(&tuple)
	if result.Error != nil {
		return browserReadGrantTuple{}, fmt.Errorf("browser read grant source checkout: %w", result.Error)
	}
	if result.RowsAffected != 1 || !isBrowserReadGrantText(tuple.AuthRealm) || !isBrowserReadGrantText(tuple.OwnerPrincipal) {
		return browserReadGrantTuple{}, ErrBrowserReadGrantDenied
	}
	return tuple, nil
}

func loadEnabledBrowserGrantUser(ctx context.Context, tx *gorm.DB, userID int64) error {
	var user User
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND disabled = FALSE", userID).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrBrowserReadGrantDenied
		}
		return fmt.Errorf("browser read grant target user: %w", err)
	}
	return nil
}

func (s *BrowserReadGrantStore) requireDB(operation string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("browser read grant %s: %w", operation, errBrowserReadGrantStoreNotConfigured)
	}
	return nil
}

func validateBrowserReadGrantIssue(ctx context.Context, in BrowserReadGrantIssue) error {
	if err := validateBrowserReadGrantRequest(ctx, in.IssuerUserID, in.IssuerPrincipal, in.SourceID); err != nil {
		return err
	}
	if in.TargetUserID <= 0 || validateUCIUUID("source_id", in.SourceID) != nil || validateUCIUUID("checkout_id", in.CheckoutID) != nil {
		return ErrBrowserReadGrantDenied
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now().UTC()) {
		return ErrBrowserReadGrantDenied
	}
	return nil
}

func validateBrowserReadGrantRequest(ctx context.Context, issuerUserID int64, issuerPrincipal, opaqueRef string) error {
	if ctx == nil {
		return fmt.Errorf("browser read grant: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if issuerUserID <= 0 || !isBrowserReadGrantText(issuerPrincipal) || !isBrowserReadGrantText(opaqueRef) {
		return ErrBrowserReadGrantDenied
	}
	return nil
}

func isBrowserReadGrantText(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func copyBrowserReadGrantExpiry(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func browserReadGrantAuditReason(grant BrowserReadGrant) string {
	return fmt.Sprintf("grant_ref=%s tuple=%s:%s:%s target_user=%d", grant.GrantRef, grant.AuthRealm, grant.SourceID, grant.CheckoutID, grant.SubjectUserID)
}
