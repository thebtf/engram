package gorm

import (
	"context"
	"fmt"
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

// DomainOwnerListOptions filters operator-managed domain ownership rows.
type DomainOwnerListOptions struct {
	Domain             string
	OwnerPrincipal     string
	OwnerPrincipalKind string
	Mode               string
	Limit              int
	Offset             int
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
