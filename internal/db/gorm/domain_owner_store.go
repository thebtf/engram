package gorm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	gormlib "gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	DomainOwnerPrincipalKindHuman   = "human"
	DomainOwnerPrincipalKindAgent   = "agent"
	DomainOwnerPrincipalKindService = "service"

	DomainOwnerModeOff    = "off"
	DomainOwnerModeWarn   = "warn"
	DomainOwnerModeReject = "reject"
)

var ErrDomainOwnerConflict = errors.New("domain owner conflict")

// DomainOwnerListOptions filters operator-managed domain ownership rows.
type DomainOwnerListOptions struct {
	Domain             string
	OwnerPrincipal     string
	OwnerPrincipalKind string
	Mode               string
	Limit              int
	Offset             int
}

// AccessRoleSummary is the operator-console read shape for dashboard-user roles.
type AccessRoleSummary struct {
	Role      string `json:"role"`
	UserCount int64  `json:"user_count"`
}

// AccessInvitationView is the operator-console read shape for one invitation.
type AccessInvitationView struct {
	ID               int64      `json:"id"`
	Code             string     `json:"code"`
	Email            string     `json:"email"`
	Role             string     `json:"role"`
	CreatedBy        int64      `json:"created_by"`
	CreatedByEmail   string     `json:"created_by_email"`
	UsedBy           *int64     `json:"used_by,omitempty"`
	UsedByEmail      string     `json:"used_by_email,omitempty"`
	UsedAt           *time.Time `json:"used_at,omitempty"`
	ExpiresAt        time.Time  `json:"expires_at"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	RevokedBy        *int64     `json:"revoked_by,omitempty"`
	RevocationReason string     `json:"revocation_reason"`
	CreatedAt        time.Time  `json:"created_at"`
	Status           string     `json:"status"`
}

// AccessSessionView is the operator-console read shape for one dashboard session.
type AccessSessionView struct {
	ID               string     `json:"id"`
	UserID           int64      `json:"user_id"`
	UserEmail        string     `json:"user_email"`
	UserRole         string     `json:"user_role"`
	UserDisabled     bool       `json:"user_disabled"`
	CreatedAt        time.Time  `json:"created_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	UserAgent        string     `json:"user_agent"`
	RemoteAddr       string     `json:"remote_addr"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	RevokedBy        *int64     `json:"revoked_by,omitempty"`
	RevocationReason string     `json:"revocation_reason"`
	Status           string     `json:"status"`
}

// AccessAuditEntry is the operator-console read shape for auth/access audit rows.
type AccessAuditEntry struct {
	ID          int64          `json:"id"`
	Action      string         `json:"action"`
	Actor       string         `json:"actor"`
	Reason      string         `json:"reason"`
	CreatedAt   time.Time      `json:"created_at"`
	BeforeState map[string]any `json:"before_state,omitempty"`
	AfterState  map[string]any `json:"after_state,omitempty"`
}

// AccessUserDrilldown bundles the user-detail side panel data for the access page.
type AccessUserDrilldown struct {
	User               *User                  `json:"user"`
	Sessions           []AccessSessionView    `json:"sessions"`
	InvitationsCreated []AccessInvitationView `json:"invitations_created"`
	InvitationsUsed    []AccessInvitationView `json:"invitations_used"`
	Audit              []AccessAuditEntry     `json:"audit"`
}

// AccessAuditRecord is one auth/access audit write request.
type AccessAuditRecord struct {
	Action      string
	Actor       string
	Reason      string
	BeforeState any
	AfterState  any
	CreatedAt   time.Time
}

// InvitationRegistrationRequest is the single-use invite consumption contract.
type InvitationRegistrationRequest struct {
	Code         string
	Email        string
	PasswordHash string
}

// DomainOwnerStore persists explicit memory-domain ownership decisions.
type DomainOwnerStore struct {
	db *gormlib.DB
}

// NewDomainOwnerStore creates a DomainOwnerStore backed by the given Store.
func NewDomainOwnerStore(store *Store) *DomainOwnerStore {
	return &DomainOwnerStore{db: store.DB}
}

// Upsert inserts or updates the owner/mode for one domain and returns the
// stored row. The caller's input is copied and never mutated.
func (s *DomainOwnerStore) Upsert(ctx context.Context, in *DomainOwner) (*DomainOwner, error) {
	row, err := normalizeDomainOwner(in)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	row.CreatedAt = now
	row.UpdatedAt = now

	err = s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "domain"}},
		DoUpdates: clause.Assignments(map[string]any{
			"owner_principal":      row.OwnerPrincipal,
			"owner_principal_kind": row.OwnerPrincipalKind,
			"mode":                 row.Mode,
			"updated_at":           now,
		}),
	}).Create(&row).Error
	if err != nil {
		return nil, fmt.Errorf("upsert domain owner %q: %w", row.Domain, err)
	}
	return s.Get(ctx, row.Domain)
}

// UpdateIfUnchanged updates one domain row only when the caller's expected
// updated_at still matches the database row. It is the narrow compare/update
// seam used by operator surfaces that need deterministic conflict reporting.
func (s *DomainOwnerStore) UpdateIfUnchanged(ctx context.Context, in *DomainOwner, expectedUpdatedAt time.Time) (*DomainOwner, error) {
	row, err := normalizeDomainOwner(in)
	if err != nil {
		return nil, err
	}
	if expectedUpdatedAt.IsZero() {
		return nil, fmt.Errorf("expected_updated_at must not be zero")
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	res := s.db.WithContext(ctx).Model(&DomainOwner{}).
		Where("domain = ? AND updated_at = ?", row.Domain, expectedUpdatedAt.UTC().Truncate(time.Microsecond)).
		Updates(map[string]any{
			"owner_principal":      row.OwnerPrincipal,
			"owner_principal_kind": row.OwnerPrincipalKind,
			"mode":                 row.Mode,
			"updated_at":           now,
		})
	if res.Error != nil {
		return nil, fmt.Errorf("update domain owner %q: %w", row.Domain, res.Error)
	}
	if res.RowsAffected == 0 {
		if _, getErr := s.Get(ctx, row.Domain); getErr != nil {
			return nil, getErr
		}
		return nil, fmt.Errorf("update domain owner %q: %w", row.Domain, ErrDomainOwnerConflict)
	}
	return s.Get(ctx, row.Domain)
}

// Get returns one domain owner row or a wrapped gorm.ErrRecordNotFound.
func (s *DomainOwnerStore) Get(ctx context.Context, domain string) (*DomainOwner, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return nil, fmt.Errorf("domain must not be empty")
	}
	var row DomainOwner
	if err := s.db.WithContext(ctx).First(&row, "domain = ?", domain).Error; err != nil {
		return nil, fmt.Errorf("get domain owner %q: %w", domain, err)
	}
	return &row, nil
}

// List returns domain owner rows ordered by domain for deterministic operator
// surfaces and tests.
func (s *DomainOwnerStore) List(ctx context.Context, opts DomainOwnerListOptions) ([]*DomainOwner, error) {
	normalized, err := normalizeDomainOwnerListOptions(opts)
	if err != nil {
		return nil, err
	}

	query := s.db.WithContext(ctx).Model(&DomainOwner{})
	if normalized.Domain != "" {
		query = query.Where("domain = ?", normalized.Domain)
	}
	if normalized.OwnerPrincipal != "" {
		query = query.Where("owner_principal = ?", normalized.OwnerPrincipal)
	}
	if normalized.OwnerPrincipalKind != "" {
		query = query.Where("owner_principal_kind = ?", normalized.OwnerPrincipalKind)
	}
	if normalized.Mode != "" {
		query = query.Where("mode = ?", normalized.Mode)
	}
	query = query.Order("domain ASC").Limit(normalized.Limit)
	if normalized.Offset > 0 {
		query = query.Offset(normalized.Offset)
	}

	var rows []DomainOwner
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list domain owners: %w", err)
	}
	result := make([]*DomainOwner, len(rows))
	for i := range rows {
		result[i] = &rows[i]
	}
	return result, nil
}

// Delete removes one explicit domain-owner row. There is no soft-delete column
// on memory_domain_owners, so removal means "return this domain to implicit
// legacy policy" rather than hiding an inactive row.
func (s *DomainOwnerStore) Delete(ctx context.Context, domain string) error {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return fmt.Errorf("domain must not be empty")
	}
	res := s.db.WithContext(ctx).Delete(&DomainOwner{}, "domain = ?", domain)
	if res.Error != nil {
		return fmt.Errorf("delete domain owner %q: %w", domain, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("delete domain owner %q: %w", domain, gormlib.ErrRecordNotFound)
	}
	return nil
}

// RegisterUserFromInvitation consumes one invitation and creates the invited
// user inside the same DB transaction so exactly one concurrent consumer wins.
func (s *DomainOwnerStore) RegisterUserFromInvitation(ctx context.Context, req InvitationRegistrationRequest) (*User, error) {
	code := strings.TrimSpace(req.Code)
	email := strings.TrimSpace(req.Email)
	passwordHash := req.PasswordHash
	if code == "" {
		return nil, gormlib.ErrRecordNotFound
	}
	if email == "" {
		return nil, fmt.Errorf("register user: email must not be empty")
	}
	if strings.TrimSpace(passwordHash) == "" {
		return nil, fmt.Errorf("register user: password_hash must not be empty")
	}

	var created *User
	err := s.db.WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		var inv Invitation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("code = ?", code).First(&inv).Error; err != nil {
			return err
		}
		if _, err := validateInvitationRow(&inv); err != nil {
			return err
		}
		if wanted := strings.TrimSpace(inv.Email); wanted != "" && !strings.EqualFold(wanted, email) {
			return ErrInvitationEmailMismatch
		}
		role, err := NormalizeDashboardRole(inv.Role)
		if err != nil {
			return fmt.Errorf("register user: %w", err)
		}
		now := time.Now().UTC()
		user := &User{
			Email:        email,
			PasswordHash: passwordHash,
			Role:         role,
			CreatedAt:    now,
		}
		if err := tx.Create(user).Error; err != nil {
			return fmt.Errorf("register user: %w", err)
		}
		if err := tx.Model(&Invitation{}).
			Where("id = ? AND used_by IS NULL AND revoked_at IS NULL", inv.ID).
			Updates(map[string]any{"used_by": user.ID, "used_at": now}).Error; err != nil {
			return fmt.Errorf("register user consume invite: %w", err)
		}
		created = user
		return s.logAccessEventTx(ctx, tx, AccessAuditRecord{
			Action:     "auth_invitation_consumed",
			Actor:      user.Email,
			Reason:     "invitation accepted",
			AfterState: map[string]any{"invite_id": inv.ID, "user_id": user.ID, "email": user.Email, "role": user.Role},
			CreatedAt:  now,
		})
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// ListAccessRoles returns the supported dashboard roles with live member counts.
func (s *DomainOwnerStore) ListAccessRoles(ctx context.Context) ([]AccessRoleSummary, error) {
	type roleCountRow struct {
		Role      string `gorm:"column:role"`
		UserCount int64  `gorm:"column:user_count"`
	}
	var rows []roleCountRow
	if err := s.db.WithContext(ctx).
		Model(&User{}).
		Select("role, COUNT(*) AS user_count").
		Group("role").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list access roles: %w", err)
	}
	counts := map[string]int64{}
	for _, row := range rows {
		counts[strings.TrimSpace(strings.ToLower(row.Role))] = row.UserCount
	}
	result := []AccessRoleSummary{
		{Role: DashboardRoleAdmin, UserCount: counts[DashboardRoleAdmin]},
		{Role: DashboardRoleOperator, UserCount: counts[DashboardRoleOperator]},
	}
	for role, count := range counts {
		if role != DashboardRoleAdmin && role != DashboardRoleOperator {
			result = append(result, AccessRoleSummary{Role: role, UserCount: count})
		}
	}
	return result, nil
}

// ListAccessInvitations returns invitation rows enriched with actor emails.
func (s *DomainOwnerStore) ListAccessInvitations(ctx context.Context, limit int) ([]AccessInvitationView, error) {
	return s.listAccessInvitations(ctx, limit, nil, nil)
}

// ListAccessSessions returns session rows enriched with user metadata.
func (s *DomainOwnerStore) ListAccessSessions(ctx context.Context, limit int, includeRevoked bool) ([]AccessSessionView, error) {
	return s.listAccessSessions(ctx, limit, nil, includeRevoked)
}

// ListAccessAudit returns the latest auth/access audit events.
func (s *DomainOwnerStore) ListAccessAudit(ctx context.Context, limit int) ([]AccessAuditEntry, error) {
	return s.queryAccessAudit(ctx, limit, nil, "")
}

// GetAccessUserDrilldown returns the side-panel data for one access user.
func (s *DomainOwnerStore) GetAccessUserDrilldown(ctx context.Context, userID int64, limit int) (*AccessUserDrilldown, error) {
	if userID <= 0 {
		return nil, gormlib.ErrRecordNotFound
	}
	if limit <= 0 {
		limit = 20
	}
	var user User
	if err := s.db.WithContext(ctx).First(&user, userID).Error; err != nil {
		return nil, fmt.Errorf("get access user detail %d: %w", userID, err)
	}
	sessions, err := s.listAccessSessions(ctx, limit, &userID, true)
	if err != nil {
		return nil, err
	}
	created, err := s.listAccessInvitations(ctx, limit, &userID, nil)
	if err != nil {
		return nil, err
	}
	used, err := s.listAccessInvitations(ctx, limit, nil, &userID)
	if err != nil {
		return nil, err
	}
	audit, err := s.queryAccessAudit(ctx, limit, &userID, user.Email)
	if err != nil {
		return nil, err
	}
	return &AccessUserDrilldown{
		User:               &user,
		Sessions:           sessions,
		InvitationsCreated: created,
		InvitationsUsed:    used,
		Audit:              audit,
	}, nil
}

// LogAccessEvent writes one access/auth audit row.
func (s *DomainOwnerStore) LogAccessEvent(ctx context.Context, record AccessAuditRecord) error {
	return s.logAccessEventTx(ctx, s.db, record)
}

func (s *DomainOwnerStore) logAccessEventTx(ctx context.Context, tx *gormlib.DB, record AccessAuditRecord) error {
	action := strings.TrimSpace(record.Action)
	actor := strings.TrimSpace(record.Actor)
	if action == "" {
		return fmt.Errorf("access audit action must not be empty")
	}
	if actor == "" {
		actor = "system"
	}
	beforeState, err := marshalAuditState(record.BeforeState)
	if err != nil {
		return fmt.Errorf("marshal access audit before_state: %w", err)
	}
	afterState, err := marshalAuditState(record.AfterState)
	if err != nil {
		return fmt.Errorf("marshal access audit after_state: %w", err)
	}
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	entry := AuditLogEntry{
		Action:      action,
		Actor:       actor,
		Reason:      strings.TrimSpace(record.Reason),
		BeforeState: beforeState,
		AfterState:  afterState,
		CreatedAt:   createdAt,
	}
	return tx.WithContext(ctx).Create(&entry).Error
}

func normalizeDomainOwner(in *DomainOwner) (DomainOwner, error) {
	if in == nil {
		return DomainOwner{}, fmt.Errorf("domain owner must not be nil")
	}
	row := DomainOwner{
		Domain:             strings.TrimSpace(in.Domain),
		OwnerPrincipal:     strings.TrimSpace(in.OwnerPrincipal),
		OwnerPrincipalKind: strings.TrimSpace(in.OwnerPrincipalKind),
		Mode:               strings.TrimSpace(in.Mode),
	}
	if row.Domain == "" {
		return DomainOwner{}, fmt.Errorf("domain must not be empty")
	}
	if row.OwnerPrincipal == "" {
		return DomainOwner{}, fmt.Errorf("owner_principal must not be empty")
	}
	if !validDomainOwnerPrincipalKind(row.OwnerPrincipalKind) {
		return DomainOwner{}, fmt.Errorf("owner_principal_kind %q must be one of human, agent, service", row.OwnerPrincipalKind)
	}
	if !validDomainOwnerMode(row.Mode) {
		return DomainOwner{}, fmt.Errorf("mode %q must be one of off, warn, reject", row.Mode)
	}
	return row, nil
}

func normalizeDomainOwnerListOptions(opts DomainOwnerListOptions) (DomainOwnerListOptions, error) {
	opts.Domain = strings.TrimSpace(opts.Domain)
	opts.OwnerPrincipal = strings.TrimSpace(opts.OwnerPrincipal)
	opts.OwnerPrincipalKind = strings.TrimSpace(opts.OwnerPrincipalKind)
	opts.Mode = strings.TrimSpace(opts.Mode)
	if opts.OwnerPrincipalKind != "" && !validDomainOwnerPrincipalKind(opts.OwnerPrincipalKind) {
		return DomainOwnerListOptions{}, fmt.Errorf("owner_principal_kind %q must be one of human, agent, service", opts.OwnerPrincipalKind)
	}
	if opts.Mode != "" && !validDomainOwnerMode(opts.Mode) {
		return DomainOwnerListOptions{}, fmt.Errorf("mode %q must be one of off, warn, reject", opts.Mode)
	}
	if opts.Limit <= 0 {
		opts.Limit = 100
	}
	if opts.Limit > 500 {
		opts.Limit = 500
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	return opts, nil
}

func validDomainOwnerPrincipalKind(kind string) bool {
	switch kind {
	case DomainOwnerPrincipalKindHuman, DomainOwnerPrincipalKindAgent, DomainOwnerPrincipalKindService:
		return true
	default:
		return false
	}
}

func validDomainOwnerMode(mode string) bool {
	switch mode {
	case DomainOwnerModeOff, DomainOwnerModeWarn, DomainOwnerModeReject:
		return true
	default:
		return false
	}
}

type accessInvitationRow struct {
	ID               int64      `gorm:"column:id"`
	Code             string     `gorm:"column:code"`
	Email            string     `gorm:"column:email"`
	Role             string     `gorm:"column:role"`
	CreatedBy        int64      `gorm:"column:created_by"`
	CreatedByEmail   string     `gorm:"column:created_by_email"`
	UsedBy           *int64     `gorm:"column:used_by"`
	UsedByEmail      string     `gorm:"column:used_by_email"`
	UsedAt           *time.Time `gorm:"column:used_at"`
	ExpiresAt        time.Time  `gorm:"column:expires_at"`
	RevokedAt        *time.Time `gorm:"column:revoked_at"`
	RevokedBy        *int64     `gorm:"column:revoked_by"`
	RevocationReason string     `gorm:"column:revocation_reason"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
}

func (s *DomainOwnerStore) listAccessInvitations(ctx context.Context, limit int, createdBy *int64, usedBy *int64) ([]AccessInvitationView, error) {
	if limit <= 0 {
		limit = 100
	}
	q := s.db.WithContext(ctx).
		Table("invitations AS i").
		Select(`
			i.id,
			i.code,
			i.invitee_email AS email,
			i.role,
			i.created_by,
			creator.email AS created_by_email,
			i.used_by,
			used.email AS used_by_email,
			i.used_at,
			i.expires_at,
			i.revoked_at,
			i.revoked_by,
			i.revocation_reason,
			i.created_at`).
		Joins("LEFT JOIN users AS creator ON creator.id = i.created_by").
		Joins("LEFT JOIN users AS used ON used.id = i.used_by")
	if createdBy != nil {
		q = q.Where("i.created_by = ?", *createdBy)
	}
	if usedBy != nil {
		q = q.Where("i.used_by = ?", *usedBy)
	}
	q = q.Order("i.created_at DESC").Limit(limit)
	var rows []accessInvitationRow
	if err := q.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list access invitations: %w", err)
	}
	result := make([]AccessInvitationView, 0, len(rows))
	for _, row := range rows {
		result = append(result, AccessInvitationView{
			ID:               row.ID,
			Code:             row.Code,
			Email:            row.Email,
			Role:             row.Role,
			CreatedBy:        row.CreatedBy,
			CreatedByEmail:   row.CreatedByEmail,
			UsedBy:           row.UsedBy,
			UsedByEmail:      row.UsedByEmail,
			UsedAt:           row.UsedAt,
			ExpiresAt:        row.ExpiresAt,
			RevokedAt:        row.RevokedAt,
			RevokedBy:        row.RevokedBy,
			RevocationReason: row.RevocationReason,
			CreatedAt:        row.CreatedAt,
			Status:           invitationStatusFromFields(row.UsedAt, row.RevokedAt, row.ExpiresAt),
		})
	}
	return result, nil
}

type accessSessionRow struct {
	ID               string     `gorm:"column:id"`
	UserID           int64      `gorm:"column:user_id"`
	UserEmail        string     `gorm:"column:user_email"`
	UserRole         string     `gorm:"column:user_role"`
	UserDisabled     bool       `gorm:"column:user_disabled"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
	ExpiresAt        time.Time  `gorm:"column:expires_at"`
	UserAgent        string     `gorm:"column:user_agent"`
	RemoteAddr       string     `gorm:"column:remote_addr"`
	RevokedAt        *time.Time `gorm:"column:revoked_at"`
	RevokedBy        *int64     `gorm:"column:revoked_by"`
	RevocationReason string     `gorm:"column:revocation_reason"`
}

func (s *DomainOwnerStore) listAccessSessions(ctx context.Context, limit int, userID *int64, includeRevoked bool) ([]AccessSessionView, error) {
	if limit <= 0 {
		limit = 100
	}
	now := time.Now().UTC()
	q := s.db.WithContext(ctx).
		Table("sessions AS s").
		Select(`
			s.id,
			s.user_id,
			u.email AS user_email,
			u.role AS user_role,
			u.disabled AS user_disabled,
			s.created_at,
			s.expires_at,
			s.user_agent,
			s.remote_addr,
			s.revoked_at,
			s.revoked_by,
			s.revocation_reason`).
		Joins("JOIN users AS u ON u.id = s.user_id")
	if userID != nil {
		q = q.Where("s.user_id = ?", *userID)
	}
	if !includeRevoked {
		q = q.Where("s.revoked_at IS NULL AND s.expires_at > ?", now)
	}
	q = q.Order("s.created_at DESC").Limit(limit)
	var rows []accessSessionRow
	if err := q.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list access sessions: %w", err)
	}
	result := make([]AccessSessionView, 0, len(rows))
	for _, row := range rows {
		result = append(result, AccessSessionView{
			ID:               row.ID,
			UserID:           row.UserID,
			UserEmail:        row.UserEmail,
			UserRole:         row.UserRole,
			UserDisabled:     row.UserDisabled,
			CreatedAt:        row.CreatedAt,
			ExpiresAt:        row.ExpiresAt,
			UserAgent:        row.UserAgent,
			RemoteAddr:       row.RemoteAddr,
			RevokedAt:        row.RevokedAt,
			RevokedBy:        row.RevokedBy,
			RevocationReason: row.RevocationReason,
			Status:           sessionStatusFromFields(row.RevokedAt, row.ExpiresAt),
		})
	}
	return result, nil
}

func (s *DomainOwnerStore) queryAccessAudit(ctx context.Context, limit int, targetUserID *int64, actorEmail string) ([]AccessAuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	q := s.db.WithContext(ctx).Model(&AuditLogEntry{}).Where("action LIKE ?", "auth_%")
	if targetUserID != nil {
		idText := strconv.FormatInt(*targetUserID, 10)
		if actorEmail != "" {
			q = q.Where("(after_state->>'user_id') = ? OR (before_state->>'user_id') = ? OR actor = ?", idText, idText, actorEmail)
		} else {
			q = q.Where("(after_state->>'user_id') = ? OR (before_state->>'user_id') = ?", idText, idText)
		}
	} else if actorEmail != "" {
		q = q.Where("actor = ?", actorEmail)
	}
	var rows []AuditLogEntry
	if err := q.Order("created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list access audit: %w", err)
	}
	result := make([]AccessAuditEntry, 0, len(rows))
	for _, row := range rows {
		result = append(result, AccessAuditEntry{
			ID:          row.ID,
			Action:      row.Action,
			Actor:       row.Actor,
			Reason:      row.Reason,
			CreatedAt:   row.CreatedAt,
			BeforeState: decodeAuditJSON(row.BeforeState),
			AfterState:  decodeAuditJSON(row.AfterState),
		})
	}
	return result, nil
}

func marshalAuditState(v any) (*json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	buf, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	raw := json.RawMessage(buf)
	return &raw, nil
}

func decodeAuditJSON(raw *json.RawMessage) map[string]any {
	if raw == nil || len(*raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(*raw, &out); err != nil {
		return map[string]any{"raw": string(*raw), "decode_error": err.Error()}
	}
	return out
}

func invitationStatusFromFields(usedAt, revokedAt *time.Time, expiresAt time.Time) string {
	if usedAt != nil {
		return "used"
	}
	if revokedAt != nil {
		return "revoked"
	}
	if !expiresAt.After(time.Now().UTC()) {
		return "expired"
	}
	return "pending"
}

func sessionStatusFromFields(revokedAt *time.Time, expiresAt time.Time) string {
	if revokedAt != nil {
		return "revoked"
	}
	if !expiresAt.After(time.Now().UTC()) {
		return "expired"
	}
	return "active"
}
