package gorm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/uci"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	browserCodeCatalogMaxEntries       = 128
	browserCodeContinuationMaxLength   = 2_048
	browserCodeContinuationPageMaxSize = 50
	browserCodeContinuationTTL         = 10 * time.Minute
)

var (
	ErrBrowserCodeContextDenied      = errors.New("browser code context denied")
	ErrBrowserCodeContinuationDenied = errors.New("browser code continuation denied")
)

// BrowserCodeContextCatalogEntry is one grant-authorized Source → Checkout →
// published View choice. A nil Context is intentionally the only shape for a
// registered checkout without a published View; it exposes an index-intent
// affordance without inventing a ContextRef.
type BrowserCodeContextCatalogEntry struct {
	SourceID             string
	SourceLabel          string
	CheckoutID           string
	CheckoutLabel        string
	Context              *uci.ContextRef
	ViewLabel            string
	IndexIntentAvailable bool
}

// BrowserCodeContextPin binds a live browser document to one exact canonical
// ContextRef. The store rechecks the active grant, registry relation, and audit
// append in the same transaction as the durable tab pin.
type BrowserCodeContextPin struct {
	Caller              BrowserTabBindingCaller
	TabBindingID        string
	DocumentProofDigest []byte
	Context             uci.ContextRef
}

// BrowserCodeSearchContinuation is an internal server-owned cursor. It stores
// only normalized request digests and the application continuation, never raw
// query text, source content, or a browser document proof.
type BrowserCodeSearchContinuation struct {
	CursorRef     string     `gorm:"column:cursor_ref;type:uuid;primaryKey"`
	SubjectUserID int64      `gorm:"column:subject_user_id;not null"`
	AuthRealm     string     `gorm:"column:auth_realm;type:text;not null"`
	GrantRef      string     `gorm:"column:grant_ref;type:uuid;not null"`
	GrantIssuedAt time.Time  `gorm:"column:grant_issued_at;type:timestamptz;not null"`
	TabBindingID  string     `gorm:"column:tab_binding_id;type:uuid;not null"`
	SourceID      string     `gorm:"column:source_id;type:uuid;not null"`
	CheckoutID    string     `gorm:"column:checkout_id;type:uuid;not null"`
	ViewID        string     `gorm:"column:view_id;type:uuid;not null"`
	ProfileID     string     `gorm:"column:profile_id;type:uuid;not null"`
	Generation    int64      `gorm:"column:generation;not null"`
	QueryDigest   string     `gorm:"column:query_digest;type:text;not null"`
	FilterDigest  string     `gorm:"column:filter_digest;type:text;not null"`
	PageSize      int        `gorm:"column:page_size;not null"`
	ServiceCursor string     `gorm:"column:service_cursor;type:text;not null"`
	ExpiresAt     time.Time  `gorm:"column:expires_at;type:timestamptz;not null"`
	ConsumedAt    *time.Time `gorm:"column:consumed_at;type:timestamptz"`
	CreatedAt     time.Time  `gorm:"column:created_at;type:timestamptz;not null"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;type:timestamptz;not null"`
}

func (BrowserCodeSearchContinuation) TableName() string { return "browser_code_search_continuations" }

// BrowserCodeContinuationBinding is the complete server-derived binding for a
// continuation. A browser provides only its opaque CursorRef; callers derive
// every other member from the guarded tab, exact grant, and request.
type BrowserCodeContinuationBinding struct {
	SubjectUserID int64
	AuthRealm     string
	GrantRef      string
	GrantIssuedAt time.Time
	TabBindingID  string
	Context       uci.ContextRef
	QueryDigest   string
	FilterDigest  string
	PageSize      int
}

// BrowserCodeContextStore owns cross-table browser code catalog, exact pin,
// and continuation state. It deliberately reuses grants, bindings, registry,
// publication, audit, and source-text projections instead of creating another
// authorization or indexing subsystem.
type BrowserCodeContextStore struct {
	db              *gorm.DB
	bindings        *BrowserTabBindingStore
	continuationTTL time.Duration
}

func NewBrowserCodeContextStore(db *gorm.DB) *BrowserCodeContextStore {
	return &BrowserCodeContextStore{
		db:              db,
		bindings:        NewBrowserTabBindingStore(db),
		continuationTTL: browserCodeContinuationTTL,
	}
}

// ListCatalog returns each active grant's exact published Views in stable
// Source, Checkout, generation, and View order. A checkout without a View is
// returned once with Context nil and IndexIntentAvailable true.
func (s *BrowserCodeContextStore) ListCatalog(ctx context.Context, subjectUserID int64) ([]BrowserCodeContextCatalogEntry, error) {
	if err := s.requireDB("list catalog"); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, fmt.Errorf("browser code context catalog: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if subjectUserID < 1 {
		return nil, ErrBrowserCodeContextDenied
	}

	now, err := browserTabBindingDatabaseClock(ctx, s.db)
	if err != nil {
		return nil, fmt.Errorf("browser code context catalog clock: %w", err)
	}
	if err := s.expireDue(ctx, subjectUserID, now); err != nil {
		return nil, err
	}

	var rows []browserCodeContextCatalogRow
	result := s.db.WithContext(ctx).Raw(`
		SELECT
			browser_grant.source_id,
			source.display_name AS source_label,
			browser_grant.checkout_id,
			checkout.kind AS checkout_kind,
			checkout.kind || ' · ' || checkout.checkout_id::text AS checkout_label,
			view_row.view_id,
			view_row.profile_id,
			view_row.generation,
			CASE
				WHEN view_row.view_id IS NULL THEN NULL
				WHEN view_row.ref_label IS NOT NULL THEN view_row.ref_label
				WHEN view_row.head_oid IS NULL THEN 'unborn'
				ELSE 'detached'
			END AS view_label
		FROM browser_read_grants AS browser_grant
		JOIN users AS subject ON subject.id = browser_grant.subject_user_id
		JOIN sources AS source
			ON source.source_id = browser_grant.source_id
			AND source.auth_realm = browser_grant.auth_realm
		JOIN ci_checkouts AS checkout
			ON checkout.checkout_id = browser_grant.checkout_id
			AND checkout.source_id = browser_grant.source_id
		LEFT JOIN ci_views AS view_row
			ON view_row.checkout_id = checkout.checkout_id
			AND view_row.source_id = checkout.source_id
			AND view_row.incarnation_id = checkout.incarnation_id
			AND view_row.state IN (?, ?)
		WHERE browser_grant.subject_user_id = ?
			AND browser_grant.state = ?
			AND (browser_grant.expires_at IS NULL OR browser_grant.expires_at > ?)
			AND subject.disabled = FALSE
			AND source.state = ?
			AND checkout.state IN (?, ?, ?)
		ORDER BY browser_grant.source_id ASC, browser_grant.checkout_id ASC, view_row.generation DESC NULLS LAST, view_row.view_id ASC
		LIMIT ?
	`, UCIViewPublished, UCIViewSuperseded, subjectUserID, BrowserReadGrantActive, now,
		UCISourceActive, UCICheckoutRegistered, UCICheckoutWatching, UCICheckoutCatchingUp,
		browserCodeCatalogMaxEntries+1).Scan(&rows)
	if result.Error != nil {
		return nil, fmt.Errorf("browser code context catalog: %w", result.Error)
	}
	if len(rows) > browserCodeCatalogMaxEntries {
		return nil, fmt.Errorf("browser code context catalog exceeds %d entries", browserCodeCatalogMaxEntries)
	}

	entries := make([]BrowserCodeContextCatalogEntry, 0, len(rows))
	for _, row := range rows {
		entry, err := row.catalogEntry()
		if err != nil {
			return nil, fmt.Errorf("browser code context catalog stored row: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// Pin atomically proves the live tab, exact active grant, exact published View
// tuple, and audit append before writing the pin. Any refusal writes neither a
// pin nor an audit row.
func (s *BrowserCodeContextStore) Pin(ctx context.Context, in BrowserCodeContextPin) error {
	if err := s.requireDB("pin"); err != nil {
		return err
	}
	if err := validateBrowserCodeContextPin(ctx, in); err != nil {
		return err
	}
	if s.bindings == nil {
		return errBrowserTabBindingStoreNotConfigured
	}

	guard := BrowserTabBindingGuard{
		Caller:              in.Caller,
		TabBindingID:        in.TabBindingID,
		DocumentProofDigest: append([]byte(nil), in.DocumentProofDigest...),
	}
	return s.bindings.mutateLiveLease(ctx, guard, func(tx *gorm.DB, binding BrowserTabBinding, now time.Time) error {
		grant, err := loadBrowserCodeActiveGrant(ctx, tx, in.Caller.SubjectUserID, in.Context.SourceID, in.Context.CheckoutID, now)
		if err != nil {
			return err
		}
		if err := browserCodePublishedContextExists(ctx, tx, in.Context, grant.AuthRealm); err != nil {
			return err
		}
		result := tx.WithContext(ctx).Model(&BrowserTabBinding{}).Where("tab_binding_id = ?", binding.TabBindingID).Updates(map[string]any{
			"pinned_source_id":           in.Context.SourceID,
			"pinned_checkout_id":         in.Context.CheckoutID,
			"pinned_view_id":             in.Context.ViewID,
			"pinned_analysis_profile_id": in.Context.AnalysisProfileID,
			"pinned_generation":          in.Context.Generation,
			"updated_at":                 now,
		})
		if result.Error != nil {
			return fmt.Errorf("browser code context pin update: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrBrowserCodeContextDenied
		}
		if err := NewAuditStore(tx).LogTx(ctx, tx, AuditLogEntry{
			Action: "code_context_pinned",
			Actor:  fmt.Sprintf("browser-user/%d", in.Caller.SubjectUserID),
			Reason: fmt.Sprintf("tab_binding_id=%s grant_ref=%s source_id=%s checkout_id=%s view_id=%s generation=%d", binding.TabBindingID, grant.GrantRef, in.Context.SourceID, in.Context.CheckoutID, in.Context.ViewID, in.Context.Generation),
		}); err != nil {
			return fmt.Errorf("browser code context pin audit: %w", err)
		}
		return nil
	})
}

// LoadContinuation returns the private application cursor only while the
// opaque browser cursor remains unused, unexpired, and exactly bound to the
// current subject, grant epoch, tab, View, query, filter, and page size.
func (s *BrowserCodeContextStore) LoadContinuation(ctx context.Context, cursorRef string, binding BrowserCodeContinuationBinding) (string, error) {
	if err := s.requireDB("load continuation"); err != nil {
		return "", err
	}
	if err := validateBrowserCodeContinuationBinding(ctx, binding); err != nil || !validBrowserCodeCursorRef(cursorRef) {
		return "", ErrBrowserCodeContinuationDenied
	}
	now, err := browserTabBindingDatabaseClock(ctx, s.db)
	if err != nil {
		return "", fmt.Errorf("browser code continuation clock: %w", err)
	}
	var row BrowserCodeSearchContinuation
	result := s.db.WithContext(ctx).Where("cursor_ref = ?", cursorRef).First(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return "", ErrBrowserCodeContinuationDenied
	}
	if result.Error != nil {
		return "", fmt.Errorf("browser code continuation load: %w", result.Error)
	}
	if !browserCodeContinuationMatches(row, binding) || row.ConsumedAt != nil || !row.ExpiresAt.After(now) {
		return "", ErrBrowserCodeContinuationDenied
	}
	return row.ServiceCursor, nil
}

// CreateContinuation turns an internal UCI continuation into a browser-safe
// opaque cursor. The cursor is not issued if there is no next internal page.
func (s *BrowserCodeContextStore) CreateContinuation(ctx context.Context, binding BrowserCodeContinuationBinding, serviceCursor string) (string, error) {
	if serviceCursor == "" {
		return "", nil
	}
	if err := s.requireDB("create continuation"); err != nil {
		return "", err
	}
	if err := validateBrowserCodeContinuationBinding(ctx, binding); err != nil || !validBrowserCodeServiceCursor(serviceCursor) {
		return "", ErrBrowserCodeContinuationDenied
	}
	var cursorRef string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now, err := browserTabBindingDatabaseClock(ctx, tx)
		if err != nil {
			return err
		}
		row := newBrowserCodeContinuation(binding, serviceCursor, now, now.Add(s.continuationDuration()))
		if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
			return fmt.Errorf("browser code continuation create: %w", err)
		}
		cursorRef = row.CursorRef
		return nil
	})
	if err != nil {
		return "", err
	}
	return cursorRef, nil
}

// AdvanceContinuation consumes one opaque browser cursor exactly once and,
// when the UCI service supplied another page, creates its successor in the same
// transaction. A concurrent or replayed advance fails closed.
func (s *BrowserCodeContextStore) AdvanceContinuation(ctx context.Context, cursorRef string, binding BrowserCodeContinuationBinding, nextServiceCursor string) (string, error) {
	if err := s.requireDB("advance continuation"); err != nil {
		return "", err
	}
	if err := validateBrowserCodeContinuationBinding(ctx, binding); err != nil || !validBrowserCodeCursorRef(cursorRef) || (nextServiceCursor != "" && !validBrowserCodeServiceCursor(nextServiceCursor)) {
		return "", ErrBrowserCodeContinuationDenied
	}

	var nextCursorRef string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now, err := browserTabBindingDatabaseClock(ctx, tx)
		if err != nil {
			return err
		}
		var row BrowserCodeSearchContinuation
		result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("cursor_ref = ?", cursorRef).First(&row)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return ErrBrowserCodeContinuationDenied
		}
		if result.Error != nil {
			return fmt.Errorf("browser code continuation lock: %w", result.Error)
		}
		if !browserCodeContinuationMatches(row, binding) || row.ConsumedAt != nil || !row.ExpiresAt.After(now) {
			return ErrBrowserCodeContinuationDenied
		}
		if err := tx.WithContext(ctx).Model(&BrowserCodeSearchContinuation{}).Where("cursor_ref = ?", row.CursorRef).Updates(map[string]any{
			"consumed_at": now,
			"updated_at":  now,
		}).Error; err != nil {
			return fmt.Errorf("browser code continuation consume: %w", err)
		}
		if nextServiceCursor == "" {
			return nil
		}
		next := newBrowserCodeContinuation(binding, nextServiceCursor, now, row.ExpiresAt)
		if err := tx.WithContext(ctx).Create(&next).Error; err != nil {
			return fmt.Errorf("browser code continuation successor: %w", err)
		}
		nextCursorRef = next.CursorRef
		return nil
	})
	if err != nil {
		return "", err
	}
	return nextCursorRef, nil
}

type browserCodeContextCatalogRow struct {
	SourceID      string  `gorm:"column:source_id"`
	SourceLabel   string  `gorm:"column:source_label"`
	CheckoutID    string  `gorm:"column:checkout_id"`
	CheckoutKind  string  `gorm:"column:checkout_kind"`
	CheckoutLabel string  `gorm:"column:checkout_label"`
	ViewID        *string `gorm:"column:view_id"`
	ProfileID     *string `gorm:"column:profile_id"`
	Generation    *int64  `gorm:"column:generation"`
	ViewLabel     *string `gorm:"column:view_label"`
}

func (row browserCodeContextCatalogRow) catalogEntry() (BrowserCodeContextCatalogEntry, error) {
	if validateUCIUUID("source_id", row.SourceID) != nil || validateUCIUUID("checkout_id", row.CheckoutID) != nil || !validUCIContextDisplayLabel(row.SourceLabel) || !isUCICheckoutKind(UCICheckoutKind(row.CheckoutKind)) || !validUCIContextDisplayLabel(row.CheckoutLabel) {
		return BrowserCodeContextCatalogEntry{}, ErrBrowserCodeContextDenied
	}
	entry := BrowserCodeContextCatalogEntry{
		SourceID:             row.SourceID,
		SourceLabel:          row.SourceLabel,
		CheckoutID:           row.CheckoutID,
		CheckoutLabel:        row.CheckoutLabel,
		IndexIntentAvailable: row.ViewID == nil,
	}
	if row.ViewID == nil && row.ProfileID == nil && row.Generation == nil && row.ViewLabel == nil {
		return entry, nil
	}
	if row.ViewID == nil || row.ProfileID == nil || row.Generation == nil || row.ViewLabel == nil || validateUCIUUID("view_id", *row.ViewID) != nil || validateUCIUUID("profile_id", *row.ProfileID) != nil || *row.Generation < 1 || !validUCIContextDisplayLabel(*row.ViewLabel) {
		return BrowserCodeContextCatalogEntry{}, ErrBrowserCodeContextDenied
	}
	entry.Context = &uci.ContextRef{
		SourceID:          row.SourceID,
		CheckoutID:        row.CheckoutID,
		ViewID:            *row.ViewID,
		AnalysisProfileID: *row.ProfileID,
		Generation:        *row.Generation,
	}
	entry.ViewLabel = *row.ViewLabel
	return entry, nil
}

func loadBrowserCodeActiveGrant(ctx context.Context, tx *gorm.DB, subjectUserID int64, sourceID, checkoutID string, now time.Time) (BrowserReadGrant, error) {
	var grant BrowserReadGrant
	result := tx.WithContext(ctx).Table("browser_read_grants AS browser_grant").
		Select("browser_grant.*").
		Joins("JOIN users AS subject ON subject.id = browser_grant.subject_user_id").
		Joins("JOIN sources AS source ON source.source_id = browser_grant.source_id AND source.auth_realm = browser_grant.auth_realm").
		Joins("JOIN ci_checkouts AS checkout ON checkout.checkout_id = browser_grant.checkout_id AND checkout.source_id = browser_grant.source_id").
		Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "browser_grant"}}).
		Where("browser_grant.subject_user_id = ? AND browser_grant.source_id = ? AND browser_grant.checkout_id = ?", subjectUserID, sourceID, checkoutID).
		Where("browser_grant.state = ? AND (browser_grant.expires_at IS NULL OR browser_grant.expires_at > ?) AND subject.disabled = FALSE", BrowserReadGrantActive, now).
		Where("source.state = ? AND checkout.state IN (?, ?, ?)", UCISourceActive, UCICheckoutRegistered, UCICheckoutWatching, UCICheckoutCatchingUp).
		First(&grant)
	if result.Error == nil {
		return grant, nil
	}
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return BrowserReadGrant{}, fmt.Errorf("browser code context grant: %w", result.Error)
	}
	if err := tx.WithContext(ctx).Model(&BrowserReadGrant{}).
		Where("subject_user_id = ? AND source_id = ? AND checkout_id = ? AND state = ? AND expires_at IS NOT NULL AND expires_at <= ?", subjectUserID, sourceID, checkoutID, BrowserReadGrantActive, now).
		Updates(map[string]any{"state": BrowserReadGrantExpired, "updated_at": now}).Error; err != nil {
		return BrowserReadGrant{}, fmt.Errorf("browser code context expire grant: %w", err)
	}
	return BrowserReadGrant{}, ErrBrowserCodeContextDenied
}

func browserCodePublishedContextExists(ctx context.Context, tx *gorm.DB, ref uci.ContextRef, authRealm string) error {
	var found int
	result := tx.WithContext(ctx).Raw(`
		SELECT 1
		FROM ci_views AS view_row
		JOIN ci_checkouts AS checkout
			ON checkout.checkout_id = view_row.checkout_id
			AND checkout.source_id = view_row.source_id
			AND checkout.incarnation_id = view_row.incarnation_id
		JOIN sources AS source ON source.source_id = checkout.source_id
		WHERE view_row.view_id = ?
			AND view_row.source_id = ?
			AND view_row.checkout_id = ?
			AND view_row.profile_id = ?
			AND view_row.generation = ?
			AND view_row.state IN (?, ?)
			AND source.auth_realm = ?
			AND source.state = ?
			AND checkout.state IN (?, ?, ?)
		LIMIT 1
	`, ref.ViewID, ref.SourceID, ref.CheckoutID, ref.AnalysisProfileID, ref.Generation,
		UCIViewPublished, UCIViewSuperseded, authRealm, UCISourceActive,
		UCICheckoutRegistered, UCICheckoutWatching, UCICheckoutCatchingUp).Scan(&found)
	if result.Error != nil {
		return fmt.Errorf("browser code context view: %w", result.Error)
	}
	if result.RowsAffected != 1 || found != 1 {
		return ErrBrowserCodeContextDenied
	}
	return nil
}

func validateBrowserCodeContextPin(ctx context.Context, in BrowserCodeContextPin) error {
	if ctx == nil {
		return ErrBrowserCodeContextDenied
	}
	if err := ctx.Err(); err != nil || !validBrowserTabBindingCaller(in.Caller) || !validBrowserTabBindingID(in.TabBindingID) || !validBrowserTabBindingDigest(in.DocumentProofDigest) || in.Context.SpaceID != nil {
		return ErrBrowserCodeContextDenied
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"source_id", in.Context.SourceID},
		{"checkout_id", in.Context.CheckoutID},
		{"view_id", in.Context.ViewID},
		{"analysis_profile_id", in.Context.AnalysisProfileID},
	} {
		if validateUCIUUID(field.name, field.value) != nil {
			return ErrBrowserCodeContextDenied
		}
	}
	if in.Context.Generation < 1 {
		return ErrBrowserCodeContextDenied
	}
	return nil
}

func (s *BrowserCodeContextStore) expireDue(ctx context.Context, subjectUserID int64, now time.Time) error {
	if err := s.db.WithContext(ctx).Model(&BrowserReadGrant{}).
		Where("subject_user_id = ? AND state = ? AND expires_at IS NOT NULL AND expires_at <= ?", subjectUserID, BrowserReadGrantActive, now).
		Updates(map[string]any{"state": BrowserReadGrantExpired, "updated_at": now}).Error; err != nil {
		return fmt.Errorf("browser code context expire grants: %w", err)
	}
	return nil
}

func (s *BrowserCodeContextStore) continuationDuration() time.Duration {
	if s != nil && s.continuationTTL > 0 {
		return s.continuationTTL
	}
	return browserCodeContinuationTTL
}

func (s *BrowserCodeContextStore) requireDB(operation string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("browser code context %s: %w", operation, errBrowserTabBindingStoreNotConfigured)
	}
	return nil
}

func newBrowserCodeContinuation(binding BrowserCodeContinuationBinding, serviceCursor string, now, expiresAt time.Time) BrowserCodeSearchContinuation {
	return BrowserCodeSearchContinuation{
		CursorRef:     uuid.NewString(),
		SubjectUserID: binding.SubjectUserID,
		AuthRealm:     binding.AuthRealm,
		GrantRef:      binding.GrantRef,
		GrantIssuedAt: binding.GrantIssuedAt.UTC(),
		TabBindingID:  binding.TabBindingID,
		SourceID:      binding.Context.SourceID,
		CheckoutID:    binding.Context.CheckoutID,
		ViewID:        binding.Context.ViewID,
		ProfileID:     binding.Context.AnalysisProfileID,
		Generation:    binding.Context.Generation,
		QueryDigest:   binding.QueryDigest,
		FilterDigest:  binding.FilterDigest,
		PageSize:      binding.PageSize,
		ServiceCursor: serviceCursor,
		ExpiresAt:     expiresAt.UTC(),
		CreatedAt:     now.UTC(),
		UpdatedAt:     now.UTC(),
	}
}

func browserCodeContinuationMatches(row BrowserCodeSearchContinuation, binding BrowserCodeContinuationBinding) bool {
	return row.SubjectUserID == binding.SubjectUserID &&
		row.AuthRealm == binding.AuthRealm &&
		row.GrantRef == binding.GrantRef &&
		row.GrantIssuedAt.Equal(binding.GrantIssuedAt.UTC()) &&
		row.TabBindingID == binding.TabBindingID &&
		row.SourceID == binding.Context.SourceID &&
		row.CheckoutID == binding.Context.CheckoutID &&
		row.ViewID == binding.Context.ViewID &&
		row.ProfileID == binding.Context.AnalysisProfileID &&
		row.Generation == binding.Context.Generation &&
		row.QueryDigest == binding.QueryDigest &&
		row.FilterDigest == binding.FilterDigest &&
		row.PageSize == binding.PageSize
}

func validateBrowserCodeContinuationBinding(ctx context.Context, binding BrowserCodeContinuationBinding) error {
	if ctx == nil || ctx.Err() != nil || binding.SubjectUserID < 1 || binding.Context.SpaceID != nil || !validBrowserCodeText(binding.AuthRealm, 256) || !validBrowserCodeCursorRef(binding.GrantRef) || !validBrowserCodeCursorRef(binding.TabBindingID) || binding.GrantIssuedAt.IsZero() || !validBrowserCodeDigest(binding.QueryDigest) || !validBrowserCodeDigest(binding.FilterDigest) || binding.PageSize < 1 || binding.PageSize > browserCodeContinuationPageMaxSize {
		return ErrBrowserCodeContinuationDenied
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"source_id", binding.Context.SourceID},
		{"checkout_id", binding.Context.CheckoutID},
		{"view_id", binding.Context.ViewID},
		{"analysis_profile_id", binding.Context.AnalysisProfileID},
	} {
		if validateUCIUUID(field.name, field.value) != nil {
			return ErrBrowserCodeContinuationDenied
		}
	}
	if binding.Context.Generation < 1 {
		return ErrBrowserCodeContinuationDenied
	}
	return nil
}

func validBrowserCodeCursorRef(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func validBrowserCodeServiceCursor(value string) bool {
	return validBrowserCodeText(value, browserCodeContinuationMaxLength)
}

func validBrowserCodeDigest(value string) bool {
	if len(value) != len("sha256:")+64 || value[:len("sha256:")] != "sha256:" {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validBrowserCodeText(value string, maximum int) bool {
	if maximum < 1 || len(value) < 1 || len(value) > maximum || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
