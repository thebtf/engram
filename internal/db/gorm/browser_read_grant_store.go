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

const browserReadGrantOwnerChoiceMax = 128

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

// BrowserReadGrantOwnerChoice is a server-issued, opaque checkout choice for
// owner onboarding. Labels are presentation only; ChoiceRef is resolved again
// with the exact owner predicate before every mutation.
type BrowserReadGrantOwnerChoice struct {
	ChoiceRef        string `gorm:"column:choice_ref"`
	RepositoryLabel  string `gorm:"column:repository_label"`
	WorkingCopyLabel string `gorm:"column:working_copy_label"`
}

// BrowserReadGrantTargetChoice is a persisted enabled human offered to an exact owner.
type BrowserReadGrantTargetChoice struct {
	UserID int64  `gorm:"column:id"`
	Label  string `gorm:"column:email"`
}

// BrowserReadGrantOwnerIssue creates a grant from one owner-catalog choice
// without accepting a browser-supplied Source or Checkout identifier.
type BrowserReadGrantOwnerIssue struct {
	IssuerUserID    int64
	IssuerPrincipal string
	TargetUserID    int64
	ChoiceRef       string
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
		result, err = issueBrowserReadGrant(ctx, tx, tuple, in, time.Now().UTC())
		return err
	})
	if err != nil {
		return BrowserReadGrant{}, err
	}
	return result, nil
}

// IssueOwnerChoice creates or restores a grant from an owner catalog choice.
// The choice is re-resolved and locked with the exact persisted owner before
// the grant and audit write commit together.
func (s *BrowserReadGrantStore) IssueOwnerChoice(ctx context.Context, in BrowserReadGrantOwnerIssue) (BrowserReadGrant, error) {
	if err := validateBrowserReadGrantOwnerIssue(ctx, in); err != nil {
		return BrowserReadGrant{}, err
	}
	if err := s.requireDB("issue owner choice"); err != nil {
		return BrowserReadGrant{}, err
	}

	var result BrowserReadGrant
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := loadEnabledBrowserGrantUser(ctx, tx, in.IssuerUserID); err != nil {
			return err
		}
		choice, err := loadBrowserReadGrantOwnerChoice(ctx, tx, in.IssuerPrincipal, in.ChoiceRef, true)
		if err != nil {
			return err
		}
		result, err = issueBrowserReadGrant(ctx, tx, browserReadGrantTuple{AuthRealm: choice.AuthRealm, OwnerPrincipal: in.IssuerPrincipal}, BrowserReadGrantIssue{
			IssuerUserID:    in.IssuerUserID,
			IssuerPrincipal: in.IssuerPrincipal,
			TargetUserID:    in.TargetUserID,
			SourceID:        choice.SourceID,
			CheckoutID:      choice.ChoiceRef,
			ExpiresAt:       in.ExpiresAt,
		}, time.Now().UTC())
		return err
	})
	if err != nil {
		return BrowserReadGrant{}, err
	}
	return result, nil
}

// ListOwnerChoices lists only active source/checkouts whose persisted owner is
// the exact canonical browser issuer. Returned ChoiceRef values are opaque to
// the onboarding transport and never authorize by themselves.
func (s *BrowserReadGrantStore) ListOwnerChoices(ctx context.Context, issuerUserID int64, issuerPrincipal string) ([]BrowserReadGrantOwnerChoice, error) {
	if err := validateBrowserReadGrantIssuer(ctx, issuerUserID, issuerPrincipal); err != nil {
		return nil, err
	}
	if err := s.requireDB("list owner choices"); err != nil {
		return nil, err
	}
	if err := loadEnabledBrowserGrantUser(ctx, s.db, issuerUserID); err != nil {
		return nil, err
	}

	rows := make([]BrowserReadGrantOwnerChoice, 0)
	result := s.db.WithContext(ctx).Raw(`
		SELECT
			checkout.checkout_id AS choice_ref,
			source.display_name AS repository_label,
			COALESCE(checkout.display_name, '') AS working_copy_label
		FROM ci_checkouts AS checkout
		JOIN sources AS source ON source.source_id = checkout.source_id
		WHERE checkout.owner_principal = ?
			AND source.state = ?
			AND checkout.state IN (?, ?, ?)
		ORDER BY source.display_name ASC, checkout.created_at ASC, checkout.checkout_id ASC
		LIMIT ?
	`, issuerPrincipal, UCISourceActive, UCICheckoutRegistered, UCICheckoutWatching, UCICheckoutCatchingUp, browserReadGrantOwnerChoiceMax+1).Scan(&rows)
	if result.Error != nil {
		return nil, fmt.Errorf("browser read grant owner choices: %w", result.Error)
	}
	if len(rows) > browserReadGrantOwnerChoiceMax {
		rows = rows[:browserReadGrantOwnerChoiceMax]
	}
	for _, row := range rows {
		if !validBrowserReadGrantOwnerChoice(row) {
			return nil, ErrBrowserReadGrantDenied
		}
	}
	return rows, nil
}

// ListTargetChoices offers enabled human accounts only to an owner with a grantable checkout.
// Users are global dashboard identities; the selected checkout supplies the grant realm.
func (s *BrowserReadGrantStore) ListTargetChoices(ctx context.Context, issuerUserID int64, issuerPrincipal string) ([]BrowserReadGrantTargetChoice, error) {
	choices, err := s.ListOwnerChoices(ctx, issuerUserID, issuerPrincipal)
	if err != nil {
		return nil, err
	}
	if len(choices) == 0 {
		return []BrowserReadGrantTargetChoice{}, nil
	}
	var targets []BrowserReadGrantTargetChoice
	if err := s.db.WithContext(ctx).Table("users").Select("id, email").Where("disabled = FALSE").Order("email ASC, id ASC").Find(&targets).Error; err != nil {
		return nil, fmt.Errorf("browser read grant target choices: %w", err)
	}
	return targets, nil
}

// SetOwnerChoiceLabel records validated, non-authorizing working-copy metadata
// only after the exact persisted source-owner predicate succeeds. Audit failure
// leaves the prior metadata unchanged.
func (s *BrowserReadGrantStore) SetOwnerChoiceLabel(ctx context.Context, issuerUserID int64, issuerPrincipal, choiceRef, label string) (BrowserReadGrantOwnerChoice, error) {
	if err := validateBrowserReadGrantIssuer(ctx, issuerUserID, issuerPrincipal); err != nil || !validBrowserReadGrantOwnerChoiceRef(choiceRef) || label == "" || !validBrowserCodeCheckoutDisplayLabel(label) {
		return BrowserReadGrantOwnerChoice{}, ErrBrowserReadGrantDenied
	}
	if err := s.requireDB("set owner choice label"); err != nil {
		return BrowserReadGrantOwnerChoice{}, err
	}

	var result BrowserReadGrantOwnerChoice
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := loadEnabledBrowserGrantUser(ctx, tx, issuerUserID); err != nil {
			return err
		}
		choice, err := loadBrowserReadGrantOwnerChoice(ctx, tx, issuerPrincipal, choiceRef, true)
		if err != nil {
			return err
		}
		updated := tx.WithContext(ctx).Model(&UCICheckout{}).Where("checkout_id = ? AND source_id = ? AND owner_principal = ?", choice.ChoiceRef, choice.SourceID, issuerPrincipal).Updates(map[string]any{
			"display_name": label,
			"updated_at":   time.Now().UTC(),
		})
		if updated.Error != nil {
			return fmt.Errorf("browser read grant owner label: %w", updated.Error)
		}
		if updated.RowsAffected != 1 {
			return ErrBrowserReadGrantDenied
		}
		result = BrowserReadGrantOwnerChoice{ChoiceRef: choice.ChoiceRef, RepositoryLabel: choice.RepositoryLabel, WorkingCopyLabel: label}
		if err := NewAuditStore(tx).LogTx(ctx, tx, AuditLogEntry{
			Action: "code_checkout_labeled",
			Actor:  issuerPrincipal,
			Reason: "checkout_ref=" + choice.ChoiceRef,
		}); err != nil {
			return fmt.Errorf("browser read grant owner label audit: %w", err)
		}
		return nil
	})
	if err != nil {
		return BrowserReadGrantOwnerChoice{}, err
	}
	return result, nil
}

func issueBrowserReadGrant(ctx context.Context, tx *gorm.DB, tuple browserReadGrantTuple, in BrowserReadGrantIssue, now time.Time) (BrowserReadGrant, error) {
	if err := loadEnabledBrowserGrantUser(ctx, tx, in.TargetUserID); err != nil {
		return BrowserReadGrant{}, err
	}

	var grant BrowserReadGrant
	err := tx.WithContext(ctx).
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
			return BrowserReadGrant{}, fmt.Errorf("browser read grant issue create: %w", err)
		}
	case err != nil:
		return BrowserReadGrant{}, fmt.Errorf("browser read grant issue lookup: %w", err)
	default:
		grant.State = BrowserReadGrantActive
		grant.IssuerPrincipal = in.IssuerPrincipal
		grant.ExpiresAt = copyBrowserReadGrantExpiry(in.ExpiresAt)
		grant.IssuedAt = now
		grant.RevokedAt = nil
		grant.UpdatedAt = now
		if err := tx.WithContext(ctx).Save(&grant).Error; err != nil {
			return BrowserReadGrant{}, fmt.Errorf("browser read grant issue restore: %w", err)
		}
	}

	if err := NewAuditStore(tx).LogTx(ctx, tx, AuditLogEntry{
		Action: "code_grant_issued",
		Actor:  in.IssuerPrincipal,
		Reason: browserReadGrantAuditReason(grant),
	}); err != nil {
		return BrowserReadGrant{}, fmt.Errorf("browser read grant issue audit: %w", err)
	}
	return grant, nil
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

type browserReadGrantOwnerChoiceRow struct {
	ChoiceRef        string `gorm:"column:choice_ref"`
	SourceID         string `gorm:"column:source_id"`
	AuthRealm        string `gorm:"column:auth_realm"`
	RepositoryLabel  string `gorm:"column:repository_label"`
	WorkingCopyLabel string `gorm:"column:working_copy_label"`
}

func loadBrowserReadGrantOwnerChoice(ctx context.Context, tx *gorm.DB, issuerPrincipal, choiceRef string, lock bool) (browserReadGrantOwnerChoiceRow, error) {
	query := `
		SELECT
			checkout.checkout_id AS choice_ref,
			source.source_id,
			source.auth_realm,
			source.display_name AS repository_label,
			COALESCE(checkout.display_name, '') AS working_copy_label
		FROM ci_checkouts AS checkout
		JOIN sources AS source ON source.source_id = checkout.source_id
		WHERE checkout.checkout_id = ?
			AND checkout.owner_principal = ?
			AND source.state = ?
			AND checkout.state IN (?, ?, ?)`
	if lock {
		query += " FOR UPDATE OF checkout"
	}

	var row browserReadGrantOwnerChoiceRow
	result := tx.WithContext(ctx).Raw(query, choiceRef, issuerPrincipal, UCISourceActive, UCICheckoutRegistered, UCICheckoutWatching, UCICheckoutCatchingUp).Scan(&row)
	if result.Error != nil {
		return browserReadGrantOwnerChoiceRow{}, fmt.Errorf("browser read grant owner choice: %w", result.Error)
	}
	if result.RowsAffected != 1 || validateUCIUUID("source_id", row.SourceID) != nil || !isBrowserReadGrantText(row.AuthRealm) || !validBrowserReadGrantOwnerChoice(BrowserReadGrantOwnerChoice{
		ChoiceRef:        row.ChoiceRef,
		RepositoryLabel:  row.RepositoryLabel,
		WorkingCopyLabel: row.WorkingCopyLabel,
	}) {
		return browserReadGrantOwnerChoiceRow{}, ErrBrowserReadGrantDenied
	}
	return row, nil
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

func validateBrowserReadGrantOwnerIssue(ctx context.Context, in BrowserReadGrantOwnerIssue) error {
	if err := validateBrowserReadGrantIssuer(ctx, in.IssuerUserID, in.IssuerPrincipal); err != nil {
		return err
	}
	if in.TargetUserID <= 0 || !validBrowserReadGrantOwnerChoiceRef(in.ChoiceRef) || (in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now().UTC())) {
		return ErrBrowserReadGrantDenied
	}
	return nil
}

func validateBrowserReadGrantIssuer(ctx context.Context, issuerUserID int64, issuerPrincipal string) error {
	if ctx == nil {
		return fmt.Errorf("browser read grant: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if issuerUserID <= 0 || !isBrowserReadGrantText(issuerPrincipal) {
		return ErrBrowserReadGrantDenied
	}
	return nil
}

func validateBrowserReadGrantRequest(ctx context.Context, issuerUserID int64, issuerPrincipal, opaqueRef string) error {
	if err := validateBrowserReadGrantIssuer(ctx, issuerUserID, issuerPrincipal); err != nil {
		return err
	}
	if !isBrowserReadGrantText(opaqueRef) {
		return ErrBrowserReadGrantDenied
	}
	return nil
}

func validBrowserReadGrantOwnerChoice(choice BrowserReadGrantOwnerChoice) bool {
	return validBrowserReadGrantOwnerChoiceRef(choice.ChoiceRef) && validUCIContextDisplayLabel(choice.RepositoryLabel) && validBrowserCodeCheckoutDisplayLabel(choice.WorkingCopyLabel)
}

func validBrowserReadGrantOwnerChoiceRef(value string) bool {
	return validateUCIUUID("checkout_id", value) == nil
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
