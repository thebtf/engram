package gorm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/uci"
	"gorm.io/gorm"
)

var (
	ErrUCIAliasConflict              = errors.New("uci context alias conflict")
	errUCIContextStoreNotConfigured  = errors.New("uci context store not configured")
	errUCIContextCatalogNotFound     = errors.New("uci context catalog entry not found")
	errUCIContextAuthorizationDenied = uci.NewContextError(uci.PermissionDenied, errors.New("uci context authorization denied"))
)

const uciContextListMax = 64

type UCIContextStore struct {
	db *gorm.DB
}

// UCIContextAuthorizer authorizes only the recorded owner of an active checkout.
// Broader Source grants intentionally remain unavailable until a source-grant
// authority is modeled in PostgreSQL.
type UCIContextAuthorizer struct {
	contexts *UCIContextStore
}

func NewUCIContextStore(db *gorm.DB) *UCIContextStore {
	return &UCIContextStore{db: db}
}

// NewUCIContextAuthorizer binds owner-only context authorization to one
// authoritative context store.
func NewUCIContextAuthorizer(contexts *UCIContextStore) *UCIContextAuthorizer {
	return &UCIContextAuthorizer{contexts: contexts}
}

var (
	_ uci.ContextCatalog    = (*UCIContextStore)(nil)
	_ uci.ContextDirectory  = (*UCIContextStore)(nil)
	_ uci.ContextAuthorizer = (*UCIContextAuthorizer)(nil)
)

// LoadContext resolves an exact published or historical-published View tuple
// from the authoritative registry. A ContextRef is never inferred from a
// locator, path, or current checkout pointer.
func (s *UCIContextStore) LoadContext(ctx context.Context, ref uci.ContextRef) (uci.ContextRecord, error) {
	if err := s.requireDB("load context"); err != nil {
		return uci.ContextRecord{}, err
	}
	if err := requireUCIContextRequest(ctx, "load context"); err != nil {
		return uci.ContextRecord{}, err
	}
	if err := validateUCIContextRef(ref); err != nil {
		return uci.ContextRecord{}, fmt.Errorf("uci context load context: %w", err)
	}

	row, err := s.loadUCIContextCatalogRow(ctx, ref)
	if err != nil {
		return uci.ContextRecord{}, err
	}
	if err := validateUCIContextCatalogRow(row); err != nil {
		return uci.ContextRecord{}, fmt.Errorf("uci context load context: stored context is invalid: %w", err)
	}
	return uci.ContextRecord{Ref: cloneUCIContextRef(ref), AuthRealm: row.AuthRealm}, nil
}

// ListAuthorizedContexts returns current published ContextRefs for exactly one
// active realm/principal owner in stable source, checkout, then view ID order.
// Limits must be within [1, uciContextListMax] and are rejected rather than clamped.
func (s *UCIContextStore) ListAuthorizedContexts(ctx context.Context, authRealm, principal string, limit int) ([]uci.ContextRef, error) {
	if err := s.requireDB("list authorized contexts"); err != nil {
		return nil, err
	}
	if err := requireUCIContextRequest(ctx, "list authorized contexts"); err != nil {
		return nil, err
	}
	if err := validateUCIContextOwner(authRealm, principal); err != nil {
		return nil, errUCIContextAuthorizationDenied
	}
	if limit < 1 || limit > uciContextListMax {
		return nil, fmt.Errorf("uci context list authorized contexts: limit must be between 1 and %d", uciContextListMax)
	}

	var rows []uciAuthorizedContextRow
	result := s.db.WithContext(ctx).Raw(`
		SELECT
			source.source_id,
			checkout.checkout_id,
			view_row.view_id,
			view_row.profile_id,
			view_row.generation,
			source.auth_realm,
			checkout.owner_principal,
			checkout.workstation_id
		FROM sources AS source
		JOIN ci_checkouts AS checkout
			ON checkout.source_id = source.source_id
		JOIN ci_views AS view_row
			ON view_row.view_id = checkout.current_view_id
			AND view_row.checkout_id = checkout.checkout_id
			AND view_row.source_id = checkout.source_id
			AND view_row.incarnation_id = checkout.incarnation_id
		JOIN ci_profiles AS profile
			ON profile.profile_id = view_row.profile_id
		WHERE source.auth_realm = ?
			AND source.state = ?
			AND checkout.owner_principal = ?
			AND checkout.state IN (?, ?, ?)
			AND view_row.state = ?
		ORDER BY source.source_id ASC, checkout.checkout_id ASC, view_row.view_id ASC
		LIMIT ?
	`, authRealm, UCISourceActive, principal,
		UCICheckoutRegistered, UCICheckoutWatching, UCICheckoutCatchingUp,
		UCIViewPublished, limit).Scan(&rows)
	if result.Error != nil {
		return nil, fmt.Errorf("uci context list authorized contexts: %w", result.Error)
	}

	refs := make([]uci.ContextRef, 0, len(rows))
	for _, row := range rows {
		if err := validateUCIAuthorizedContextRow(row, authRealm, principal); err != nil {
			return nil, fmt.Errorf("uci context list authorized contexts: stored context is invalid: %w", err)
		}
		ref := uci.ContextRef{
			SourceID:          row.SourceID,
			CheckoutID:        row.CheckoutID,
			ViewID:            row.ViewID,
			AnalysisProfileID: row.ProfileID,
			Generation:        row.Generation,
		}
		if err := validateUCIContextRef(ref); err != nil {
			return nil, fmt.Errorf("uci context list authorized contexts: stored reference is invalid: %w", err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// LoadContextMetadata returns only safe display labels for an already-resolved
// context. It never returns a locator, workstation, owner, or other authority
// evidence, and it does not use labels as an authority selector.
func (s *UCIContextStore) LoadContextMetadata(ctx context.Context, ref uci.ContextRef) (uci.ContextMetadata, error) {
	if err := s.requireDB("load context metadata"); err != nil {
		return uci.ContextMetadata{}, err
	}
	if err := requireUCIContextRequest(ctx, "load context metadata"); err != nil {
		return uci.ContextMetadata{}, err
	}
	if err := validateUCIContextRef(ref); err != nil {
		return uci.ContextMetadata{}, fmt.Errorf("uci context load context metadata: %w", err)
	}

	row, err := s.loadUCIContextCatalogRow(ctx, ref)
	if err != nil {
		return uci.ContextMetadata{}, err
	}
	if err := validateUCIContextCatalogRow(row); err != nil {
		return uci.ContextMetadata{}, fmt.Errorf("uci context load context metadata: stored context is invalid: %w", err)
	}
	metadata, err := uciContextMetadataFromRow(row)
	if err != nil {
		return uci.ContextMetadata{}, fmt.Errorf("uci context load context metadata: stored labels are invalid: %w", err)
	}
	return metadata, nil
}

// AuthorizeContext allows only the stored owner of an active checkout in the
// source's exact realm. The lack of a source-grant table is intentional: this
// method must remain restrictive until that authority exists.
func (authorizer *UCIContextAuthorizer) AuthorizeContext(ctx context.Context, access uci.ContextAccess) error {
	if authorizer == nil || authorizer.contexts == nil {
		return errUCIContextAuthorizationDenied
	}
	if err := authorizer.contexts.requireDB("authorize context"); err != nil {
		return err
	}
	if err := requireUCIContextRequest(ctx, "authorize context"); err != nil {
		return err
	}
	if err := validateUCIContextAccess(access); err != nil {
		return errUCIContextAuthorizationDenied
	}

	var row uciContextAuthorizationRow
	result := authorizer.contexts.db.WithContext(ctx).Raw(`
		SELECT
			source.auth_realm,
			checkout.owner_principal,
			checkout.workstation_id
		FROM sources AS source
		JOIN ci_checkouts AS checkout
			ON checkout.source_id = source.source_id
		WHERE source.source_id = ?
			AND checkout.checkout_id = ?
			AND source.auth_realm = ?
			AND checkout.owner_principal = ?
			AND source.state = ?
			AND checkout.state IN (?, ?, ?)
		LIMIT 1
	`, access.SourceID, access.CheckoutID, access.AuthRealm, access.Principal,
		UCISourceActive, UCICheckoutRegistered, UCICheckoutWatching, UCICheckoutCatchingUp).Scan(&row)
	if result.Error != nil {
		return fmt.Errorf("uci context authorize context: %w", result.Error)
	}
	if result.RowsAffected != 1 ||
		row.AuthRealm != access.AuthRealm ||
		row.OwnerPrincipal != access.Principal ||
		validateUCIContextOwner(row.AuthRealm, row.OwnerPrincipal) != nil ||
		validateUCIRequiredText("workstation_id", row.WorkstationID) != nil {
		return errUCIContextAuthorizationDenied
	}
	return nil
}

type uciContextCatalogRow struct {
	AuthRealm      string          `gorm:"column:auth_realm"`
	SourceLabel    string          `gorm:"column:source_label"`
	OwnerPrincipal string          `gorm:"column:owner_principal"`
	WorkstationID  string          `gorm:"column:workstation_id"`
	CheckoutKind   UCICheckoutKind `gorm:"column:checkout_kind"`
	ViewLabel      string          `gorm:"column:view_label"`
}

type uciAuthorizedContextRow struct {
	SourceID       string `gorm:"column:source_id"`
	CheckoutID     string `gorm:"column:checkout_id"`
	ViewID         string `gorm:"column:view_id"`
	ProfileID      string `gorm:"column:profile_id"`
	Generation     int64  `gorm:"column:generation"`
	AuthRealm      string `gorm:"column:auth_realm"`
	OwnerPrincipal string `gorm:"column:owner_principal"`
	WorkstationID  string `gorm:"column:workstation_id"`
}

type uciContextAuthorizationRow struct {
	AuthRealm      string `gorm:"column:auth_realm"`
	OwnerPrincipal string `gorm:"column:owner_principal"`
	WorkstationID  string `gorm:"column:workstation_id"`
}

func (s *UCIContextStore) loadUCIContextCatalogRow(ctx context.Context, ref uci.ContextRef) (uciContextCatalogRow, error) {
	query := `
		SELECT
			source.auth_realm,
			source.display_name AS source_label,
			checkout.owner_principal,
			checkout.workstation_id,
			checkout.kind AS checkout_kind,
			CASE
				WHEN view_row.ref_label IS NOT NULL THEN view_row.ref_label
				WHEN view_row.head_oid IS NULL THEN 'unborn'
				ELSE 'detached'
			END AS view_label
		FROM ci_views AS view_row
		JOIN ci_checkouts AS checkout
			ON checkout.checkout_id = view_row.checkout_id
			AND checkout.source_id = view_row.source_id
			AND checkout.incarnation_id = view_row.incarnation_id
		JOIN sources AS source
			ON source.source_id = checkout.source_id
		JOIN ci_profiles AS profile
			ON profile.profile_id = view_row.profile_id
		WHERE view_row.view_id = ?
			AND view_row.source_id = ?
			AND view_row.checkout_id = ?
			AND view_row.profile_id = ?
			AND view_row.generation = ?
			AND source.state = ?
			AND checkout.state IN (?, ?, ?)
			AND view_row.state IN (?, ?)
	`
	arguments := []any{
		ref.ViewID,
		ref.SourceID,
		ref.CheckoutID,
		ref.AnalysisProfileID,
		ref.Generation,
		UCISourceActive,
		UCICheckoutRegistered,
		UCICheckoutWatching,
		UCICheckoutCatchingUp,
		UCIViewPublished,
		UCIViewSuperseded,
	}
	if ref.SpaceID != nil {
		query += `
			AND EXISTS (
				SELECT 1
				FROM spaces AS space
				JOIN space_sources AS membership
					ON membership.space_id = space.space_id
					AND membership.source_id = source.source_id
					AND membership.auth_realm = source.auth_realm
				WHERE space.space_id = ?
					AND space.auth_realm = source.auth_realm
					AND space.state = ?
			)
		`
		arguments = append(arguments, *ref.SpaceID, UCISpaceActive)
	}
	query += " LIMIT 1"

	var row uciContextCatalogRow
	result := s.db.WithContext(ctx).Raw(query, arguments...).Scan(&row)
	if result.Error != nil {
		return uciContextCatalogRow{}, fmt.Errorf("uci context load catalog row: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return uciContextCatalogRow{}, errUCIContextCatalogNotFound
	}
	return row, nil
}

func uciContextMetadataFromRow(row uciContextCatalogRow) (uci.ContextMetadata, error) {
	if !validUCIContextDisplayLabel(row.SourceLabel) ||
		!validUCIContextDisplayLabel(row.ViewLabel) ||
		!isUCICheckoutKind(row.CheckoutKind) {
		return uci.ContextMetadata{}, errors.New("invalid context display metadata")
	}
	return uci.ContextMetadata{
		Source:   row.SourceLabel,
		Checkout: string(row.CheckoutKind),
		View:     row.ViewLabel,
	}, nil
}

func validateUCIContextCatalogRow(row uciContextCatalogRow) error {
	if err := validateUCIContextOwner(row.AuthRealm, row.OwnerPrincipal); err != nil {
		return err
	}
	if err := validateUCIRequiredText("workstation_id", row.WorkstationID); err != nil {
		return err
	}
	if !isUCICheckoutKind(row.CheckoutKind) {
		return errors.New("checkout kind is invalid")
	}
	return nil
}

func validateUCIAuthorizedContextRow(row uciAuthorizedContextRow, authRealm, principal string) error {
	if row.AuthRealm != authRealm || row.OwnerPrincipal != principal {
		return errors.New("owner scope does not match")
	}
	if err := validateUCIContextOwner(row.AuthRealm, row.OwnerPrincipal); err != nil {
		return err
	}
	return validateUCIRequiredText("workstation_id", row.WorkstationID)
}

func validateUCIContextAccess(access uci.ContextAccess) error {
	if err := validateUCIContextOwner(access.AuthRealm, access.Principal); err != nil {
		return err
	}
	if err := validateUCIUUID("source_id", access.SourceID); err != nil {
		return err
	}
	return validateUCIUUID("checkout_id", access.CheckoutID)
}

func validateUCIContextOwner(authRealm, principal string) error {
	if err := validateUCIRequiredText("auth_realm", authRealm); err != nil {
		return err
	}
	// Master, browser-session, and authentication-disabled identities have no
	// single workstation-bound principal and therefore cannot use owner-only UCI.
	switch authRealm {
	case "master", "session", "auth-disabled":
		return errors.New("auth realm has no owner-only workstation identity")
	}
	return validateUCIRequiredText("principal", principal)
}

func validateUCIContextRef(ref uci.ContextRef) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"source_id", ref.SourceID},
		{"checkout_id", ref.CheckoutID},
		{"view_id", ref.ViewID},
		{"analysis_profile_id", ref.AnalysisProfileID},
	} {
		if err := validateUCIUUID(field.name, field.value); err != nil {
			return err
		}
	}
	if ref.SpaceID != nil {
		if err := validateUCIUUID("space_id", *ref.SpaceID); err != nil {
			return err
		}
	}
	if ref.Generation <= 0 {
		return errors.New("generation must be positive")
	}
	return nil
}

func cloneUCIContextRef(ref uci.ContextRef) uci.ContextRef {
	copy := ref
	if ref.SpaceID != nil {
		spaceID := *ref.SpaceID
		copy.SpaceID = &spaceID
	}
	return copy
}

func validUCIContextDisplayLabel(value string) bool {
	return len(value) <= 256 && utf8.ValidString(value) && validateUCIRequiredText("display_label", value) == nil
}

func requireUCIContextRequest(ctx context.Context, operation string) error {
	if ctx == nil {
		return fmt.Errorf("uci context %s: context is required", operation)
	}
	return ctx.Err()
}

func (s *UCIContextStore) CreateSpace(ctx context.Context, in CreateSpaceInput) (*UCISpace, error) {
	if err := s.requireDB("create space"); err != nil {
		return nil, err
	}
	return createUCISpace(ctx, s.db, in)
}

func (s *UCIContextStore) CreateSource(ctx context.Context, in CreateSourceInput) (*UCISource, error) {
	if err := s.requireDB("create source"); err != nil {
		return nil, err
	}
	if err := validateCreateSourceInput(in); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	row := &UCISource{
		SourceID:    uuid.NewString(),
		AuthRealm:   in.AuthRealm,
		Kind:        in.Kind,
		DisplayName: in.DisplayName,
		State:       UCISourceActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("uci context create source: %w", err)
	}
	return row, nil
}

func (s *UCIContextStore) LinkSpaceSource(ctx context.Context, in LinkSpaceSourceInput) error {
	if err := s.requireDB("link space source"); err != nil {
		return err
	}
	if err := validateUCIUUID("space_id", in.SpaceID); err != nil {
		return err
	}
	if err := validateUCIUUID("source_id", in.SourceID); err != nil {
		return err
	}
	if in.DisplayOrder < 0 {
		return fmt.Errorf("uci context link space source: display_order must be non-negative")
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var space UCISpace
		if err := tx.WithContext(ctx).
			Where("space_id = ? AND state = ?", in.SpaceID, UCISpaceActive).
			First(&space).Error; err != nil {
			return fmt.Errorf("uci context link space source: space %q: %w", in.SpaceID, err)
		}

		var source UCISource
		if err := tx.WithContext(ctx).
			Where("source_id = ? AND state = ?", in.SourceID, UCISourceActive).
			First(&source).Error; err != nil {
			return fmt.Errorf("uci context link space source: source %q: %w", in.SourceID, err)
		}
		if space.AuthRealm != source.AuthRealm {
			return fmt.Errorf("uci context link space source: space and source realms differ")
		}

		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO space_sources (auth_realm, space_id, source_id, display_order)
			VALUES (?, ?, ?, ?)
			ON CONFLICT (space_id, source_id)
			DO UPDATE SET display_order = EXCLUDED.display_order
		`, space.AuthRealm, space.SpaceID, source.SourceID, in.DisplayOrder).Error; err != nil {
			return fmt.Errorf("uci context link space source: %w", err)
		}
		return nil
	})
}

func (s *UCIContextStore) RegisterCheckout(ctx context.Context, in RegisterCheckoutInput) (*UCICheckout, error) {
	if err := s.requireDB("register checkout"); err != nil {
		return nil, err
	}
	if err := validateRegisterCheckoutInput(in); err != nil {
		return nil, err
	}

	var source UCISource
	if err := s.db.WithContext(ctx).
		Where("source_id = ? AND state = ?", in.SourceID, UCISourceActive).
		First(&source).Error; err != nil {
		return nil, fmt.Errorf("uci context register checkout: source %q: %w", in.SourceID, err)
	}

	ownerInstance, err := copyUCIOptionalText("owner_instance", in.OwnerInstance)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	row := &UCICheckout{
		CheckoutID:     uuid.NewString(),
		SourceID:       source.SourceID,
		WorkstationID:  in.WorkstationID,
		IncarnationID:  uuid.NewString(),
		Kind:           in.Kind,
		OwnerPrincipal: in.OwnerPrincipal,
		LocatorRef:     in.LocatorRef,
		State:          UCICheckoutRegistered,
		LeaseEpoch:     0,
		OwnerInstance:  ownerInstance,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("uci context register checkout: %w", err)
	}
	return row, nil
}

func (s *UCIContextStore) CreateProfile(ctx context.Context, in CreateProfileInput) (*UCIAnalysisProfile, error) {
	if err := s.requireDB("create profile"); err != nil {
		return nil, err
	}
	buildContext, err := validateCreateProfileInput(in)
	if err != nil {
		return nil, err
	}

	row := &UCIAnalysisProfile{
		ProfileID:            uuid.NewString(),
		ParserBundleDigest:   in.ParserBundleDigest,
		ResolverRevision:     in.ResolverRevision,
		ChunkerRevision:      in.ChunkerRevision,
		IgnorePolicyDigest:   in.IgnorePolicyDigest,
		BuildContextJSON:     buildContext,
		SecretPolicyRevision: in.SecretPolicyRevision,
		CreatedAt:            time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("uci context create profile: %w", err)
	}
	return row, nil
}

func (s *UCIContextStore) CreateView(ctx context.Context, in CreateViewInput) (*UCIView, error) {
	if err := s.requireDB("create view"); err != nil {
		return nil, err
	}
	row, err := uciViewFromInput(in)
	if err != nil {
		return nil, err
	}

	var checkout UCICheckout
	if err := s.db.WithContext(ctx).
		Where("checkout_id = ? AND source_id = ? AND incarnation_id = ?", row.CheckoutID, row.SourceID, row.IncarnationID).
		First(&checkout).Error; err != nil {
		return nil, fmt.Errorf("uci context create view: checkout/source/incarnation tuple: %w", err)
	}

	var profile UCIAnalysisProfile
	if err := s.db.WithContext(ctx).Where("profile_id = ?", row.ProfileID).First(&profile).Error; err != nil {
		return nil, fmt.Errorf("uci context create view: profile %q: %w", row.ProfileID, err)
	}

	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("uci context create view: %w", err)
	}
	return row, nil
}

func (s *UCIContextStore) GetSpace(ctx context.Context, spaceID string) (*UCISpace, error) {
	if err := s.requireDB("get space"); err != nil {
		return nil, err
	}
	if err := validateUCIUUID("space_id", spaceID); err != nil {
		return nil, err
	}

	var row UCISpace
	if err := s.db.WithContext(ctx).Where("space_id = ?", spaceID).First(&row).Error; err != nil {
		return nil, fmt.Errorf("uci context get space %q: %w", spaceID, err)
	}
	return &row, nil
}

func (s *UCIContextStore) GetSource(ctx context.Context, sourceID string) (*UCISource, error) {
	if err := s.requireDB("get source"); err != nil {
		return nil, err
	}
	if err := validateUCIUUID("source_id", sourceID); err != nil {
		return nil, err
	}

	var row UCISource
	if err := s.db.WithContext(ctx).Where("source_id = ?", sourceID).First(&row).Error; err != nil {
		return nil, fmt.Errorf("uci context get source %q: %w", sourceID, err)
	}
	return &row, nil
}

func (s *UCIContextStore) GetProfile(ctx context.Context, profileID string) (*UCIAnalysisProfile, error) {
	if err := s.requireDB("get profile"); err != nil {
		return nil, err
	}
	if err := validateUCIUUID("profile_id", profileID); err != nil {
		return nil, err
	}

	var row UCIAnalysisProfile
	if err := s.db.WithContext(ctx).Where("profile_id = ?", profileID).First(&row).Error; err != nil {
		return nil, fmt.Errorf("uci context get profile %q: %w", profileID, err)
	}
	return &row, nil
}

func (s *UCIContextStore) GetCheckout(ctx context.Context, checkoutID string) (*UCICheckout, error) {
	if err := s.requireDB("get checkout"); err != nil {
		return nil, err
	}
	if err := validateUCIUUID("checkout_id", checkoutID); err != nil {
		return nil, err
	}

	var row UCICheckout
	if err := s.db.WithContext(ctx).Where("checkout_id = ?", checkoutID).First(&row).Error; err != nil {
		return nil, fmt.Errorf("uci context get checkout %q: %w", checkoutID, err)
	}
	return &row, nil
}

func (s *UCIContextStore) GetView(ctx context.Context, viewID string) (*UCIView, error) {
	if err := s.requireDB("get view"); err != nil {
		return nil, err
	}
	if err := validateUCIUUID("view_id", viewID); err != nil {
		return nil, err
	}

	var row UCIView
	if err := s.db.WithContext(ctx).Where("view_id = ?", viewID).First(&row).Error; err != nil {
		return nil, fmt.Errorf("uci context get view %q: %w", viewID, err)
	}
	return &row, nil
}

func (s *UCIContextStore) GetCurrentView(ctx context.Context, checkoutID string) (*UCIView, error) {
	checkout, err := s.GetCheckout(ctx, checkoutID)
	if err != nil {
		return nil, err
	}
	if checkout.CurrentViewID == nil {
		return nil, fmt.Errorf("uci context get current view %q: %w", checkoutID, gorm.ErrRecordNotFound)
	}

	var row UCIView
	if err := s.db.WithContext(ctx).
		Where("view_id = ? AND checkout_id = ? AND source_id = ? AND incarnation_id = ? AND state = ?", *checkout.CurrentViewID, checkout.CheckoutID, checkout.SourceID, checkout.IncarnationID, UCIViewPublished).
		First(&row).Error; err != nil {
		return nil, fmt.Errorf("uci context get current view %q: %w", checkoutID, err)
	}
	return &row, nil
}

// LoadIndexBinding reloads an already-authorized View or registered checkout
// selector from the server-owned registry without deriving authority from paths.
func (s *UCIContextStore) LoadIndexBinding(ctx context.Context, selector uci.IndexBindingSelector) (uci.IndexBinding, error) {
	if err := s.requireDB("load index binding"); err != nil {
		return uci.IndexBinding{}, err
	}
	selector = selector.Clone()
	if err := selector.Validate(); err != nil {
		return uci.IndexBinding{}, fmt.Errorf("uci context load index binding: %w", err)
	}
	if ref, pinned := selector.Context(); pinned {
		return s.loadUCIIndexBindingForContext(ctx, ref)
	}
	checkout, _ := selector.Checkout()
	return s.loadUCIIndexBindingForCheckout(ctx, checkout.Scope, checkout.ProfileID)
}

func (s *UCIContextStore) ListCheckoutsBySource(ctx context.Context, sourceID string) ([]UCICheckout, error) {
	if err := s.requireDB("list checkouts"); err != nil {
		return nil, err
	}
	if err := validateUCIUUID("source_id", sourceID); err != nil {
		return nil, err
	}

	rows := make([]UCICheckout, 0)
	if err := s.db.WithContext(ctx).
		Where("source_id = ?", sourceID).
		Order("created_at ASC, checkout_id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("uci context list checkouts for source %q: %w", sourceID, err)
	}
	return rows, nil
}

func (s *UCIContextStore) UpsertLegacyContextAlias(ctx context.Context, in LegacyContextAliasInput) (*UCILegacyContextAlias, error) {
	if err := s.requireDB("upsert legacy context alias"); err != nil {
		return nil, err
	}

	var alias *UCILegacyContextAlias
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		alias, err = upsertUCILegacyContextAlias(ctx, tx, in)
		return err
	})
	if err != nil {
		return nil, err
	}
	return alias, nil
}

func (s *UCIContextStore) LookupLegacyContextAliases(ctx context.Context, key LegacyContextAliasKey) ([]UCILegacyContextAlias, error) {
	if err := s.requireDB("lookup legacy context aliases"); err != nil {
		return nil, err
	}
	if err := validateUCILegacyContextAliasKey(key); err != nil {
		return nil, err
	}

	rows := make([]UCILegacyContextAlias, 0)
	if err := s.db.WithContext(ctx).
		Where("auth_realm = ? AND legacy_domain = ? AND scheme = ? AND value = ? AND client_namespace = ?", key.AuthRealm, key.LegacyDomain, key.Scheme, key.Value, key.ClientNamespace).
		Order("revision ASC, alias_id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("uci context lookup legacy context aliases: %w", err)
	}
	return rows, nil
}

// LookupLegacyAliasRecords adapts stored aliases to the typed compatibility resolver contract.
func (s *UCIContextStore) LookupLegacyAliasRecords(ctx context.Context, key uci.LegacyAliasKey) ([]uci.LegacyAliasRecord, error) {
	rows, err := s.LookupLegacyContextAliases(ctx, LegacyContextAliasKey{
		AuthRealm:       key.AuthRealm,
		LegacyDomain:    key.LegacyDomain,
		Scheme:          key.Scheme,
		Value:           key.Value,
		ClientNamespace: key.ClientNamespace,
	})
	if err != nil {
		return nil, err
	}

	records := make([]uci.LegacyAliasRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, uci.LegacyAliasRecord{
			Key: uci.LegacyAliasKey{
				AuthRealm:       row.AuthRealm,
				LegacyDomain:    row.LegacyDomain,
				Scheme:          row.Scheme,
				Value:           row.Value,
				ClientNamespace: row.ClientNamespace,
			},
			State:    string(row.MappingState),
			SpaceID:  copyUCIAliasTarget(row.SpaceID),
			SourceID: copyUCIAliasTarget(row.SourceID),
		})
	}
	return records, nil
}

func copyUCIAliasTarget(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (s *UCIContextStore) CreateSpaceWithLegacyAlias(ctx context.Context, spaceIn CreateSpaceInput, aliasIn LegacyContextAliasInput) (*UCISpace, *UCILegacyContextAlias, error) {
	if err := s.requireDB("create space with legacy alias"); err != nil {
		return nil, nil, err
	}
	if err := validateCreateSpaceInput(spaceIn); err != nil {
		return nil, nil, err
	}
	if aliasIn.SpaceID != nil {
		return nil, nil, fmt.Errorf("uci context create space with legacy alias: space_id is assigned by this operation")
	}
	if aliasIn.AuthRealm != spaceIn.AuthRealm {
		return nil, nil, fmt.Errorf("uci context create space with legacy alias: space and alias realms differ")
	}
	if aliasIn.MappingState != UCIAliasResolved {
		return nil, nil, fmt.Errorf("uci context create space with legacy alias: mapping_state must be %q", UCIAliasResolved)
	}

	var space *UCISpace
	var alias *UCILegacyContextAlias
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		space, err = createUCISpace(ctx, tx, spaceIn)
		if err != nil {
			return err
		}
		spaceID := space.SpaceID
		aliasIn.SpaceID = &spaceID
		alias, err = upsertUCILegacyContextAlias(ctx, tx, aliasIn)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return space, alias, nil
}

func createUCISpace(ctx context.Context, db *gorm.DB, in CreateSpaceInput) (*UCISpace, error) {
	if err := validateCreateSpaceInput(in); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	row := &UCISpace{
		SpaceID:     uuid.NewString(),
		AuthRealm:   in.AuthRealm,
		DisplayName: in.DisplayName,
		State:       UCISpaceActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("uci context create space: %w", err)
	}
	return row, nil
}

func upsertUCILegacyContextAlias(ctx context.Context, tx *gorm.DB, in LegacyContextAliasInput) (*UCILegacyContextAlias, error) {
	candidate, err := uciLegacyContextAliasFromInput(in)
	if err != nil {
		return nil, err
	}
	if err := tx.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, uciLegacyContextAliasLockKey(candidate)).Error; err != nil {
		return nil, fmt.Errorf("uci context upsert legacy context alias: acquire key lock: %w", err)
	}
	if err := validateUCILegacyContextAliasTargets(ctx, tx, candidate); err != nil {
		return nil, err
	}

	var existing UCILegacyContextAlias
	err = tx.WithContext(ctx).
		Where("auth_realm = ? AND legacy_domain = ? AND scheme = ? AND value = ? AND client_namespace = ?", candidate.AuthRealm, candidate.LegacyDomain, candidate.Scheme, candidate.Value, candidate.ClientNamespace).
		First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := tx.WithContext(ctx).Create(candidate).Error; err != nil {
			return nil, fmt.Errorf("uci context upsert legacy context alias: %w", err)
		}
		return candidate, nil
	}
	if err != nil {
		return nil, fmt.Errorf("uci context upsert legacy context alias: %w", err)
	}

	same, err := sameUCILegacyContextAliasPayload(&existing, candidate)
	if err != nil {
		return nil, fmt.Errorf("uci context upsert legacy context alias: %w", err)
	}
	if same {
		return &existing, nil
	}
	return nil, fmt.Errorf("%w: key %q/%q/%q/%q", ErrUCIAliasConflict, candidate.AuthRealm, candidate.LegacyDomain, candidate.Scheme, candidate.Value)
}

func uciLegacyContextAliasFromInput(in LegacyContextAliasInput) (*UCILegacyContextAlias, error) {
	key := LegacyContextAliasKey{
		AuthRealm:       in.AuthRealm,
		LegacyDomain:    in.LegacyDomain,
		Scheme:          in.Scheme,
		Value:           in.Value,
		ClientNamespace: in.ClientNamespace,
	}
	if err := validateUCILegacyContextAliasKey(key); err != nil {
		return nil, err
	}
	if !isUCIAliasMappingState(in.MappingState) {
		return nil, fmt.Errorf("uci context alias: unsupported mapping_state %q", in.MappingState)
	}
	if in.Revision <= 0 {
		return nil, fmt.Errorf("uci context alias: revision must be positive")
	}
	provenance, err := normalizeUCIJSONObject("provenance", in.Provenance)
	if err != nil {
		return nil, err
	}
	spaceID, err := copyUCIOptionalUUID("space_id", in.SpaceID)
	if err != nil {
		return nil, err
	}
	sourceID, err := copyUCIOptionalUUID("source_id", in.SourceID)
	if err != nil {
		return nil, err
	}
	if in.MappingState == UCIAliasResolved && spaceID == nil && sourceID == nil {
		return nil, fmt.Errorf("uci context alias: resolved mapping requires a space_id or source_id")
	}
	if in.MappingState == UCIAliasUnmapped && (spaceID != nil || sourceID != nil) {
		return nil, fmt.Errorf("uci context alias: unmapped mapping cannot target a space or source")
	}

	now := time.Now().UTC()
	return &UCILegacyContextAlias{
		AliasID:         uuid.NewString(),
		AuthRealm:       in.AuthRealm,
		LegacyDomain:    in.LegacyDomain,
		Scheme:          in.Scheme,
		Value:           in.Value,
		ClientNamespace: in.ClientNamespace,
		SpaceID:         spaceID,
		SourceID:        sourceID,
		MappingState:    in.MappingState,
		Revision:        in.Revision,
		Provenance:      provenance,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

func validateUCILegacyContextAliasTargets(ctx context.Context, db *gorm.DB, alias *UCILegacyContextAlias) error {
	if alias.SpaceID != nil {
		var space UCISpace
		if err := db.WithContext(ctx).
			Where("space_id = ? AND auth_realm = ?", *alias.SpaceID, alias.AuthRealm).
			First(&space).Error; err != nil {
			return fmt.Errorf("uci context alias: space target in auth_realm %q: %w", alias.AuthRealm, err)
		}
	}
	if alias.SourceID != nil {
		var source UCISource
		if err := db.WithContext(ctx).
			Where("source_id = ? AND auth_realm = ?", *alias.SourceID, alias.AuthRealm).
			First(&source).Error; err != nil {
			return fmt.Errorf("uci context alias: source target in auth_realm %q: %w", alias.AuthRealm, err)
		}
	}
	return nil
}

func sameUCILegacyContextAliasPayload(existing, candidate *UCILegacyContextAlias) (bool, error) {
	provenance, err := normalizeUCIJSONObject("stored provenance", existing.Provenance)
	if err != nil {
		return false, err
	}
	return existing.MappingState == candidate.MappingState &&
		existing.Revision == candidate.Revision &&
		provenance == candidate.Provenance &&
		sameUCIOptionalString(existing.SpaceID, candidate.SpaceID) &&
		sameUCIOptionalString(existing.SourceID, candidate.SourceID), nil
}

func (s *UCIContextStore) loadUCIIndexBindingForContext(ctx context.Context, ref uci.ContextRef) (uci.IndexBinding, error) {
	source, checkout, err := s.loadUCIIndexBindingCheckout(ctx, ref.SourceID, ref.CheckoutID, "")
	if err != nil {
		return uci.IndexBinding{}, err
	}
	if err := s.validateUCIIndexBindingSpace(ctx, source, ref.SpaceID); err != nil {
		return uci.IndexBinding{}, err
	}
	if _, err := s.loadUCIIndexBindingProfile(ctx, ref.AnalysisProfileID); err != nil {
		return uci.IndexBinding{}, err
	}

	var view UCIView
	if err := s.db.WithContext(ctx).Where("view_id = ?", ref.ViewID).First(&view).Error; err != nil {
		return uci.IndexBinding{}, fmt.Errorf("uci context load index binding: view %q: %w", ref.ViewID, err)
	}
	if view.CheckoutID != checkout.CheckoutID ||
		view.SourceID != source.SourceID ||
		view.IncarnationID != checkout.IncarnationID ||
		view.ProfileID != ref.AnalysisProfileID ||
		view.Generation != ref.Generation {
		return uci.IndexBinding{}, fmt.Errorf("uci context load index binding: view does not match authorized context")
	}
	if view.State != UCIViewPublished {
		return uci.IndexBinding{}, fmt.Errorf("uci context load index binding: view state %q is not published", view.State)
	}
	return uciIndexBindingFromRows(checkout, ref.AnalysisProfileID, &view, ref.SpaceID)
}

func (s *UCIContextStore) loadUCIIndexBindingForCheckout(ctx context.Context, scope uci.IndexScope, profileID string) (uci.IndexBinding, error) {
	_, checkout, err := s.loadUCIIndexBindingCheckout(ctx, scope.SourceID, scope.CheckoutID, scope.IncarnationID)
	if err != nil {
		return uci.IndexBinding{}, err
	}
	if _, err := s.loadUCIIndexBindingProfile(ctx, profileID); err != nil {
		return uci.IndexBinding{}, err
	}

	view, err := s.loadUCICurrentIndexBindingView(ctx, checkout)
	if err != nil {
		return uci.IndexBinding{}, err
	}
	if view != nil && view.ProfileID != profileID {
		return uci.IndexBinding{}, fmt.Errorf("uci context load index binding: current view profile %q does not match selector profile %q", view.ProfileID, profileID)
	}
	return uciIndexBindingFromRows(checkout, profileID, view, nil)
}

func (s *UCIContextStore) loadUCIIndexBindingCheckout(ctx context.Context, sourceID, checkoutID, incarnationID string) (*UCISource, *UCICheckout, error) {
	var source UCISource
	if err := s.db.WithContext(ctx).Where("source_id = ?", sourceID).First(&source).Error; err != nil {
		return nil, nil, fmt.Errorf("uci context load index binding: source %q: %w", sourceID, err)
	}
	if source.State != UCISourceActive {
		return nil, nil, fmt.Errorf("uci context load index binding: source state %q is not active", source.State)
	}
	if err := validateUCIRequiredText("auth_realm", source.AuthRealm); err != nil {
		return nil, nil, fmt.Errorf("uci context load index binding: source realm: %w", err)
	}

	var checkout UCICheckout
	if err := s.db.WithContext(ctx).Where("checkout_id = ?", checkoutID).First(&checkout).Error; err != nil {
		return nil, nil, fmt.Errorf("uci context load index binding: checkout %q: %w", checkoutID, err)
	}
	if checkout.SourceID != source.SourceID {
		return nil, nil, fmt.Errorf("uci context load index binding: checkout source does not match selector source")
	}
	if incarnationID != "" && checkout.IncarnationID != incarnationID {
		return nil, nil, fmt.Errorf("uci context load index binding: checkout incarnation does not match selector incarnation")
	}
	if !isUCIIndexBindingCheckoutState(checkout.State) {
		return nil, nil, fmt.Errorf("uci context load index binding: checkout state %q is not registered", checkout.State)
	}
	return &source, &checkout, nil
}

func (s *UCIContextStore) loadUCIIndexBindingProfile(ctx context.Context, profileID string) (*UCIAnalysisProfile, error) {
	var profile UCIAnalysisProfile
	if err := s.db.WithContext(ctx).Where("profile_id = ?", profileID).First(&profile).Error; err != nil {
		return nil, fmt.Errorf("uci context load index binding: profile %q: %w", profileID, err)
	}
	return &profile, nil
}

func (s *UCIContextStore) loadUCICurrentIndexBindingView(ctx context.Context, checkout *UCICheckout) (*UCIView, error) {
	if checkout.CurrentViewID == nil {
		return nil, nil
	}
	var view UCIView
	if err := s.db.WithContext(ctx).Where("view_id = ?", *checkout.CurrentViewID).First(&view).Error; err != nil {
		return nil, fmt.Errorf("uci context load index binding: current view %q: %w", *checkout.CurrentViewID, err)
	}
	if view.CheckoutID != checkout.CheckoutID || view.SourceID != checkout.SourceID || view.IncarnationID != checkout.IncarnationID {
		return nil, fmt.Errorf("uci context load index binding: current view does not match checkout")
	}
	if view.State != UCIViewPublished {
		return nil, fmt.Errorf("uci context load index binding: current view state %q is not published", view.State)
	}
	return &view, nil
}

func (s *UCIContextStore) validateUCIIndexBindingSpace(ctx context.Context, source *UCISource, spaceID *string) error {
	if spaceID == nil {
		return nil
	}
	var space UCISpace
	if err := s.db.WithContext(ctx).Where("space_id = ?", *spaceID).First(&space).Error; err != nil {
		return fmt.Errorf("uci context load index binding: space %q: %w", *spaceID, err)
	}
	if space.State != UCISpaceActive {
		return fmt.Errorf("uci context load index binding: space state %q is not active", space.State)
	}
	if space.AuthRealm != source.AuthRealm {
		return fmt.Errorf("uci context load index binding: space and source realms differ")
	}
	var link UCISpaceSource
	if err := s.db.WithContext(ctx).
		Where("space_id = ? AND source_id = ? AND auth_realm = ?", space.SpaceID, source.SourceID, source.AuthRealm).
		First(&link).Error; err != nil {
		return fmt.Errorf("uci context load index binding: space source relationship: %w", err)
	}
	return nil
}

func uciIndexBindingFromRows(checkout *UCICheckout, profileID string, view *UCIView, spaceID *string) (uci.IndexBinding, error) {
	binding := uci.IndexBinding{
		Scope: uci.IndexScope{
			SourceID:      checkout.SourceID,
			CheckoutID:    checkout.CheckoutID,
			IncarnationID: checkout.IncarnationID,
		},
		ProfileID:     profileID,
		LocalRootID:   checkout.LocatorRef,
		WorkstationID: checkout.WorkstationID,
	}
	if view != nil {
		contextRef := uci.ContextRef{
			SpaceID:           cloneUCIIndexBindingSpaceID(spaceID),
			SourceID:          view.SourceID,
			CheckoutID:        view.CheckoutID,
			ViewID:            view.ViewID,
			AnalysisProfileID: view.ProfileID,
			Generation:        view.Generation,
		}
		binding.Context = &contextRef
	}
	if err := binding.Validate(); err != nil {
		return uci.IndexBinding{}, fmt.Errorf("uci context load index binding: invalid stored binding: %w", err)
	}
	return binding.Clone(), nil
}

func cloneUCIIndexBindingSpaceID(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func isUCIIndexBindingCheckoutState(state UCICheckoutState) bool {
	switch state {
	case UCICheckoutRegistered, UCICheckoutWatching, UCICheckoutCatchingUp:
		return true
	default:
		return false
	}
}

func uciViewFromInput(in CreateViewInput) (*UCIView, error) {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"checkout_id", in.CheckoutID},
		{"source_id", in.SourceID},
		{"incarnation_id", in.IncarnationID},
		{"profile_id", in.ProfileID},
	} {
		if err := validateUCIUUID(field.name, field.value); err != nil {
			return nil, err
		}
	}
	if in.Generation <= 0 {
		return nil, fmt.Errorf("uci context create view: generation must be positive")
	}
	if in.ObservedFSSeq < 0 {
		return nil, fmt.Errorf("uci context create view: observed_fs_seq must be non-negative")
	}
	if in.ScanStart.IsZero() || in.ScanEnd.IsZero() || in.ScanEnd.Before(in.ScanStart) {
		return nil, fmt.Errorf("uci context create view: scan window is invalid")
	}
	if err := validateUCIDigest("manifest_digest", in.ManifestDigest); err != nil {
		return nil, err
	}
	coverage, err := normalizeUCIJSONObject("coverage_json", in.CoverageJSON)
	if err != nil {
		return nil, err
	}
	state := in.State
	if state == "" {
		state = UCIViewStaging
	}
	if state != UCIViewStaging {
		return nil, fmt.Errorf("uci context create view: only staging views may be created before publication")
	}
	headOID, err := copyUCIOptionalTextPointer("head_oid", in.HeadOID)
	if err != nil {
		return nil, err
	}
	objectFormat, err := copyUCIOptionalTextPointer("object_format", in.ObjectFormat)
	if err != nil {
		return nil, err
	}
	refLabel, err := copyUCIOptionalTextPointer("ref_label", in.RefLabel)
	if err != nil {
		return nil, err
	}
	if objectFormat != nil && *objectFormat != "sha1" && *objectFormat != "sha256" {
		return nil, fmt.Errorf("uci context create view: unsupported object_format %q", *objectFormat)
	}
	if headOID != nil {
		if objectFormat == nil {
			return nil, fmt.Errorf("uci context create view: object_format is required with head_oid")
		}
		length := 40
		if *objectFormat == "sha256" {
			length = 64
		}
		if !isUCILowerHex(*headOID, length) {
			return nil, fmt.Errorf("uci context create view: invalid head_oid for object_format %q", *objectFormat)
		}
	}

	now := time.Now().UTC()
	return &UCIView{
		ViewID:         uuid.NewString(),
		CheckoutID:     in.CheckoutID,
		SourceID:       in.SourceID,
		IncarnationID:  in.IncarnationID,
		Generation:     in.Generation,
		ProfileID:      in.ProfileID,
		HeadOID:        headOID,
		ObjectFormat:   objectFormat,
		RefLabel:       refLabel,
		Dirty:          in.Dirty,
		ObservedFSSeq:  in.ObservedFSSeq,
		ScanStart:      in.ScanStart.UTC(),
		ScanEnd:        in.ScanEnd.UTC(),
		ManifestDigest: in.ManifestDigest,
		State:          UCIViewStaging,
		CoverageJSON:   coverage,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

func validateCreateSpaceInput(in CreateSpaceInput) error {
	if err := validateUCIRequiredText("auth_realm", in.AuthRealm); err != nil {
		return err
	}
	return validateUCIRequiredText("display_name", in.DisplayName)
}

func validateCreateSourceInput(in CreateSourceInput) error {
	if err := validateUCIRequiredText("auth_realm", in.AuthRealm); err != nil {
		return err
	}
	if !isUCISourceKind(in.Kind) {
		return fmt.Errorf("uci context create source: unsupported kind %q", in.Kind)
	}
	return validateUCIRequiredText("display_name", in.DisplayName)
}

func validateRegisterCheckoutInput(in RegisterCheckoutInput) error {
	if err := validateUCIUUID("source_id", in.SourceID); err != nil {
		return err
	}
	if err := validateUCIRequiredText("workstation_id", in.WorkstationID); err != nil {
		return err
	}
	if !isUCICheckoutKind(in.Kind) {
		return fmt.Errorf("uci context register checkout: unsupported kind %q", in.Kind)
	}
	if err := validateUCIRequiredText("owner_principal", in.OwnerPrincipal); err != nil {
		return err
	}
	if err := validateUCIRequiredText("locator_ref", in.LocatorRef); err != nil {
		return err
	}
	if in.OwnerInstance != "" {
		return validateUCIRequiredText("owner_instance", in.OwnerInstance)
	}
	return nil
}

func validateCreateProfileInput(in CreateProfileInput) (string, error) {
	if err := validateUCIDigest("parser_bundle_digest", in.ParserBundleDigest); err != nil {
		return "", err
	}
	if err := validateUCIRequiredText("resolver_revision", in.ResolverRevision); err != nil {
		return "", err
	}
	if err := validateUCIRequiredText("chunker_revision", in.ChunkerRevision); err != nil {
		return "", err
	}
	if err := validateUCIDigest("ignore_policy_digest", in.IgnorePolicyDigest); err != nil {
		return "", err
	}
	if err := validateUCIRequiredText("secret_policy_revision", in.SecretPolicyRevision); err != nil {
		return "", err
	}
	return normalizeUCIJSONObject("build_context_json", in.BuildContextJSON)
}

func validateUCILegacyContextAliasKey(key LegacyContextAliasKey) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"auth_realm", key.AuthRealm},
		{"legacy_domain", key.LegacyDomain},
		{"scheme", key.Scheme},
		{"value", key.Value},
	} {
		if err := validateUCIRequiredText(field.name, field.value); err != nil {
			return err
		}
	}
	if key.ClientNamespace != "" {
		return validateUCIRequiredText("client_namespace", key.ClientNamespace)
	}
	return nil
}

func (s *UCIContextStore) requireDB(operation string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("uci context %s: %w", operation, errUCIContextStoreNotConfigured)
	}
	return nil
}

func validateUCIRequiredText(name, value string) error {
	if value == "" || strings.TrimSpace(value) != value || strings.TrimSpace(value) == "" || containsUCIControl(value) {
		return fmt.Errorf("uci context: %s must be non-blank, trimmed, and control-free", name)
	}
	return nil
}

func validateUCIUUID(name, value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return fmt.Errorf("uci context: %s must be a canonical non-nil UUID", name)
	}
	return nil
}

func validateUCIDigest(name, value string) error {
	if !isUCIDigest(value) {
		return fmt.Errorf("uci context: %s must be a complete sha256 digest", name)
	}
	return nil
}

func normalizeUCIJSONObject(name, value string) (string, error) {
	var object map[string]any
	if err := json.Unmarshal([]byte(value), &object); err != nil || object == nil {
		return "", fmt.Errorf("uci context: %s must be a JSON object", name)
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return "", fmt.Errorf("uci context: normalize %s: %w", name, err)
	}
	return string(normalized), nil
}

func copyUCIOptionalText(name, value string) (*string, error) {
	if value == "" {
		return nil, nil
	}
	if err := validateUCIRequiredText(name, value); err != nil {
		return nil, err
	}
	copy := value
	return &copy, nil
}

func copyUCIOptionalTextPointer(name string, value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if err := validateUCIRequiredText(name, *value); err != nil {
		return nil, err
	}
	copy := *value
	return &copy, nil
}

func copyUCIOptionalUUID(name string, value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if err := validateUCIUUID(name, *value); err != nil {
		return nil, err
	}
	copy := *value
	return &copy, nil
}

func sameUCIOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func containsUCIControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func isUCIDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && isUCILowerHex(value[len("sha256:"):], 64)
}

func isUCILowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for i := range value {
		if (value[i] < '0' || value[i] > '9') && (value[i] < 'a' || value[i] > 'f') {
			return false
		}
	}
	return true
}

func isUCISourceKind(kind UCISourceKind) bool {
	switch kind {
	case UCISourceGit, UCISourceDirectory, UCISourceDocumentSet:
		return true
	default:
		return false
	}
}

func isUCICheckoutKind(kind UCICheckoutKind) bool {
	switch kind {
	case UCICheckoutWorkingTree, UCICheckoutCommitReader:
		return true
	default:
		return false
	}
}

func isUCIAliasMappingState(state UCIAliasMappingState) bool {
	switch state {
	case UCIAliasResolved, UCIAliasAmbiguous, UCIAliasUnmapped, UCIAliasRetired:
		return true
	default:
		return false
	}
}

func uciLegacyContextAliasLockKey(alias *UCILegacyContextAlias) string {
	return strings.Join([]string{alias.AuthRealm, alias.LegacyDomain, alias.Scheme, alias.Value, alias.ClientNamespace}, "\x1f")
}
