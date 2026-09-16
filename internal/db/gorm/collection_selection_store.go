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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	CollectionSelectionMaxTargets   = 1_000
	CollectionPageMaxSize           = 200
	collectionSelectionMaxCursor    = 512
	collectionSelectionMaxTTL       = time.Hour
	collectionSelectionScopeWhere   = "subject_user_id = ? AND session_id = ? AND domain = ?"
	collectionSelectionDigestPrefix = "sha256:"
)

var (
	ErrCollectionSelectionInvalid                = errors.New("collection selection is invalid")
	ErrCollectionSelectionDenied                 = errors.New("collection selection denied")
	ErrCollectionSelectionReconfirmationRequired = errors.New("collection selection reconfirmation required")
	errCollectionSelectionStoreNotConfigured     = errors.New("collection selection store is not configured")
)

// CollectionSelectionKind names the only collection membership forms. It is
// selection intent only: no kind grants permission to mutate a domain.
type CollectionSelectionKind string

const (
	CollectionSelectionNone         CollectionSelectionKind = "none"
	CollectionSelectionExplicit     CollectionSelectionKind = "explicit"
	CollectionSelectionPage         CollectionSelectionKind = "page"
	CollectionSelectionFrozenFilter CollectionSelectionKind = "frozen_filter"
)

// CollectionSelectionReconfirmationReason is retained so a browser can explain
// why a formerly valid selection cannot be used without a fresh preview.
type CollectionSelectionReconfirmationReason string

const (
	CollectionSelectionReconfirmFilterChanged     CollectionSelectionReconfirmationReason = "filter_changed"
	CollectionSelectionReconfirmContextChanged    CollectionSelectionReconfirmationReason = "context_changed"
	CollectionSelectionReconfirmGrantChanged      CollectionSelectionReconfirmationReason = "grant_changed"
	CollectionSelectionReconfirmCollectionChanged CollectionSelectionReconfirmationReason = "collection_changed"
	CollectionSelectionReconfirmExpired           CollectionSelectionReconfirmationReason = "expired"
)

// CollectionSelectionScope is server-derived request state. The opaque
// fingerprints contain no query text and must be recomputed before any use.
type CollectionSelectionScope struct {
	SubjectUserID      int64
	SessionID          string
	Domain             string
	ContextFingerprint string
	AuthorizationEpoch int64
	CollectionVersion  int64
}

// CollectionSelectionTarget is a domain-owned immutable identifier paired with
// the version observed for the selection. A zero ExpectedVersion is permitted
// only for explicit and page intent, whose domain may not expose versions yet.
type CollectionSelectionTarget struct {
	ID              string `json:"id"`
	ExpectedVersion uint64 `json:"expected_version,omitempty"`
}

// CollectionSelection is the portable selection snapshot returned to a browser
// or later domain action. Token lookup is intentionally scope-bound and never
// substitutes for the domain's authorization check.
type CollectionSelection struct {
	Kind                   CollectionSelectionKind
	Targets                []CollectionSelectionTarget
	Cursor                 string
	FilterFingerprint      string
	ExcludedIDs            []string
	Token                  string
	ExpiresAt              time.Time
	Version                int64
	ReconfirmationRequired bool
	ReconfirmationReason   CollectionSelectionReconfirmationReason
}

// CollectionFrozenSelection is the authorized server result used to create an
// all-filter snapshot. Browser input never supplies its targets, token, or TTL.
type CollectionFrozenSelection struct {
	Targets   []CollectionSelectionTarget
	ExpiresAt time.Time
}

// CollectionFilter is domain-normalized server state. Its fingerprint is
// derived from Value; browser JSON never supplies either as authority.
type CollectionFilter struct {
	Fingerprint string
	Value       string
}

func (filter CollectionFilter) Valid() bool {
	return validCollectionSelectionFingerprint(filter.Fingerprint) && validCollectionSelectionText(filter.Value, 256)
}

// CollectionPageRequest is a bounded, non-authorizing request for one page.
type CollectionPageRequest struct {
	Domain string
	Filter CollectionFilter
	Cursor string
	Limit  int
}

// CollectionPage is a bounded domain-owned page. It carries no selection token.
type CollectionPage struct {
	Cursor     string
	Targets    []CollectionSelectionTarget
	NextCursor string
	Total      *int64
}

// CollectionSelectionRecord is the migration-owned durable projection of one
// subject/session/domain intent. It stores fingerprints, never raw filter text.
type CollectionSelectionRecord struct {
	SelectionID            string                                  `gorm:"column:selection_id;type:uuid;primaryKey"`
	SubjectUserID          int64                                   `gorm:"column:subject_user_id;not null;uniqueIndex:idx_collection_selection_scope,priority:1"`
	SessionID              string                                  `gorm:"column:session_id;type:text;not null;uniqueIndex:idx_collection_selection_scope,priority:2"`
	Domain                 string                                  `gorm:"column:domain;type:text;not null;uniqueIndex:idx_collection_selection_scope,priority:3"`
	Kind                   CollectionSelectionKind                 `gorm:"column:kind;type:text;not null"`
	SelectionVersion       int64                                   `gorm:"column:selection_version;not null"`
	ContextFingerprint     string                                  `gorm:"column:context_fingerprint;type:text;not null"`
	AuthorizationEpoch     int64                                   `gorm:"column:authorization_epoch;not null"`
	CollectionVersion      int64                                   `gorm:"column:collection_version;not null"`
	FilterFingerprint      *string                                 `gorm:"column:filter_fingerprint;type:text"`
	PageCursor             *string                                 `gorm:"column:page_cursor;type:text"`
	TargetsJSON            []byte                                  `gorm:"column:targets_json;type:jsonb;not null"`
	ExcludedIDsJSON        []byte                                  `gorm:"column:excluded_ids_json;type:jsonb;not null"`
	SelectionToken         *string                                 `gorm:"column:selection_token;type:uuid;uniqueIndex:idx_collection_selection_token"`
	FrozenExpiresAt        *time.Time                              `gorm:"column:frozen_expires_at;type:timestamptz"`
	ReconfirmationRequired bool                                    `gorm:"column:reconfirmation_required;not null"`
	ReconfirmationReason   CollectionSelectionReconfirmationReason `gorm:"column:reconfirmation_reason;type:text"`
	CreatedAt              time.Time                               `gorm:"column:created_at;not null"`
	UpdatedAt              time.Time                               `gorm:"column:updated_at;not null"`
}

func (CollectionSelectionRecord) TableName() string { return "collection_selections" }

// CollectionSelectionStore is the sole persistence owner for collection
// selection intent. It deliberately has no domain action or authorization API.
type CollectionSelectionStore struct {
	db *gorm.DB
}

func NewCollectionSelectionStore(db *gorm.DB) *CollectionSelectionStore {
	return &CollectionSelectionStore{db: db}
}

// Save replaces the current selection for one browser owner/session/domain and
// generates a fresh opaque token only for a server-frozen filter snapshot.
func (store *CollectionSelectionStore) Save(ctx context.Context, scope CollectionSelectionScope, selection CollectionSelection) (CollectionSelection, error) {
	if err := validateCollectionSelectionScope(scope); err != nil {
		return CollectionSelection{}, err
	}
	if err := store.requireDB("save"); err != nil {
		return CollectionSelection{}, err
	}
	now := time.Now().UTC()
	if err := validateCollectionSelection(selection, now, true); err != nil {
		return CollectionSelection{}, err
	}

	var saved CollectionSelection
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current CollectionSelectionRecord
		lookup := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			collectionSelectionScopeWhere, scope.SubjectUserID, scope.SessionID, scope.Domain,
		).First(&current)
		if lookup.Error != nil && !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return fmt.Errorf("collection selection lookup: %w", lookup.Error)
		}

		version := int64(1)
		selectionID := uuid.NewString()
		createdAt := now
		if lookup.Error == nil {
			version = current.SelectionVersion + 1
			selectionID = current.SelectionID
			createdAt = current.CreatedAt
		}
		record, recordErr := newCollectionSelectionRecord(scope, selection, selectionID, version, createdAt, now)
		if recordErr != nil {
			return recordErr
		}
		if lookup.Error == nil {
			if err := tx.Save(&record).Error; err != nil {
				return fmt.Errorf("collection selection update: %w", err)
			}
		} else if err := tx.Create(&record).Error; err != nil {
			return fmt.Errorf("collection selection create: %w", err)
		}
		parsed, parseErr := collectionSelectionFromRecord(record)
		if parseErr != nil {
			return parseErr
		}
		saved = parsed
		return nil
	})
	if err != nil {
		return CollectionSelection{}, err
	}
	return saved, nil
}

// Current returns the selection only for its exact browser owner/session/domain.
// A newer domain/context/grant revision makes it require reconfirmation before
// later consumers can use it.
func (store *CollectionSelectionStore) Current(ctx context.Context, scope CollectionSelectionScope) (CollectionSelection, error) {
	return store.load(ctx, scope, "", false)
}

// Frozen returns a frozen snapshot only when both its opaque token and the
// current browser scope match. It never treats the token as action authority.
func (store *CollectionSelectionStore) Frozen(ctx context.Context, scope CollectionSelectionScope, token string) (CollectionSelection, error) {
	if !validCollectionSelectionToken(token) {
		return CollectionSelection{}, ErrCollectionSelectionDenied
	}
	return store.load(ctx, scope, token, true)
}

// RequireReconfirmation records a locally observed filter change without
// accepting a new filter or target set. A fresh server freeze is still required.
func (store *CollectionSelectionStore) RequireReconfirmation(ctx context.Context, scope CollectionSelectionScope, version int64, reason CollectionSelectionReconfirmationReason) (CollectionSelection, error) {
	if err := validateCollectionSelectionScope(scope); err != nil || version < 1 || !validCollectionSelectionReconfirmationReason(reason) {
		return CollectionSelection{}, ErrCollectionSelectionInvalid
	}
	if err := store.requireDB("require reconfirmation"); err != nil {
		return CollectionSelection{}, err
	}
	var updated CollectionSelection
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record CollectionSelectionRecord
		lookup := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			collectionSelectionScopeWhere, scope.SubjectUserID, scope.SessionID, scope.Domain,
		).First(&record)
		if errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return ErrCollectionSelectionDenied
		}
		if lookup.Error != nil {
			return fmt.Errorf("collection selection reconfirmation lookup: %w", lookup.Error)
		}
		if record.SelectionVersion != version {
			return ErrCollectionSelectionReconfirmationRequired
		}
		if !record.ReconfirmationRequired {
			record.ReconfirmationRequired = true
			record.ReconfirmationReason = reason
			record.SelectionVersion++
			record.UpdatedAt = time.Now().UTC()
			if err := tx.Save(&record).Error; err != nil {
				return fmt.Errorf("collection selection reconfirmation update: %w", err)
			}
		}
		parsed, parseErr := collectionSelectionFromRecord(record)
		if parseErr != nil {
			return parseErr
		}
		updated = parsed
		return nil
	})
	if err != nil {
		return CollectionSelection{}, err
	}
	return updated, nil
}

func (store *CollectionSelectionStore) load(ctx context.Context, scope CollectionSelectionScope, token string, frozenOnly bool) (CollectionSelection, error) {
	if err := validateCollectionSelectionScope(scope); err != nil {
		return CollectionSelection{}, err
	}
	if err := store.requireDB("load"); err != nil {
		return CollectionSelection{}, err
	}
	return store.loadCurrent(ctx, scope, token, frozenOnly)
}

func (store *CollectionSelectionStore) loadCurrent(ctx context.Context, scope CollectionSelectionScope, token string, frozenOnly bool) (CollectionSelection, error) {
	var current CollectionSelection
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		record, found, err := loadCollectionSelectionRecord(tx, scope, token, frozenOnly)
		if err != nil {
			return err
		}
		if !found {
			if frozenOnly {
				return ErrCollectionSelectionDenied
			}
			current = CollectionSelection{Kind: CollectionSelectionNone}
			return nil
		}
		if err := reconfirmCollectionSelection(tx, &record, scope); err != nil {
			return err
		}
		parsed, err := collectionSelectionFromRecord(record)
		if err != nil {
			return err
		}
		if frozenOnly && parsed.ReconfirmationRequired {
			return ErrCollectionSelectionReconfirmationRequired
		}
		current = parsed
		return nil
	})
	if err != nil {
		return CollectionSelection{}, err
	}
	return current, nil
}

func loadCollectionSelectionRecord(tx *gorm.DB, scope CollectionSelectionScope, token string, frozenOnly bool) (CollectionSelectionRecord, bool, error) {
	query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(collectionSelectionScopeWhere, scope.SubjectUserID, scope.SessionID, scope.Domain)
	if frozenOnly {
		query = query.Where("selection_token = ?", token)
	}
	var record CollectionSelectionRecord
	lookup := query.First(&record)
	if errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
		return CollectionSelectionRecord{}, false, nil
	}
	if lookup.Error != nil {
		return CollectionSelectionRecord{}, false, fmt.Errorf("collection selection load: %w", lookup.Error)
	}
	if frozenOnly && record.Kind != CollectionSelectionFrozenFilter {
		return CollectionSelectionRecord{}, false, ErrCollectionSelectionDenied
	}
	return record, true, nil
}

func reconfirmCollectionSelection(tx *gorm.DB, record *CollectionSelectionRecord, scope CollectionSelectionScope) error {
	reason, changed := collectionSelectionChangeReason(*record, scope, time.Now().UTC())
	if !changed || record.ReconfirmationRequired {
		return nil
	}
	record.ReconfirmationRequired = true
	record.ReconfirmationReason = reason
	record.SelectionVersion++
	record.UpdatedAt = time.Now().UTC()
	if err := tx.Save(record).Error; err != nil {
		return fmt.Errorf("collection selection invalidate: %w", err)
	}
	return nil
}

func newCollectionSelectionRecord(scope CollectionSelectionScope, selection CollectionSelection, selectionID string, version int64, createdAt, now time.Time) (CollectionSelectionRecord, error) {
	targetsJSON, err := json.Marshal(selection.Targets)
	if err != nil {
		return CollectionSelectionRecord{}, fmt.Errorf("marshal collection selection targets: %w", err)
	}
	exclusionsJSON, err := json.Marshal(selection.ExcludedIDs)
	if err != nil {
		return CollectionSelectionRecord{}, fmt.Errorf("marshal collection selection exclusions: %w", err)
	}
	record := CollectionSelectionRecord{
		SelectionID:        selectionID,
		SubjectUserID:      scope.SubjectUserID,
		SessionID:          scope.SessionID,
		Domain:             scope.Domain,
		Kind:               selection.Kind,
		SelectionVersion:   version,
		ContextFingerprint: scope.ContextFingerprint,
		AuthorizationEpoch: scope.AuthorizationEpoch,
		CollectionVersion:  scope.CollectionVersion,
		TargetsJSON:        targetsJSON,
		ExcludedIDsJSON:    exclusionsJSON,
		CreatedAt:          createdAt,
		UpdatedAt:          now,
	}
	switch selection.Kind {
	case CollectionSelectionPage:
		record.PageCursor = collectionSelectionStringPointer(selection.Cursor)
	case CollectionSelectionFrozenFilter:
		record.FilterFingerprint = collectionSelectionStringPointer(selection.FilterFingerprint)
		token := uuid.NewString()
		record.SelectionToken = &token
		expiresAt := selection.ExpiresAt.UTC()
		record.FrozenExpiresAt = &expiresAt
	}
	return record, nil
}

func collectionSelectionFromRecord(record CollectionSelectionRecord) (CollectionSelection, error) {
	var targets []CollectionSelectionTarget
	if err := json.Unmarshal(record.TargetsJSON, &targets); err != nil {
		return CollectionSelection{}, fmt.Errorf("decode collection selection targets: %w", err)
	}
	var exclusions []string
	if err := json.Unmarshal(record.ExcludedIDsJSON, &exclusions); err != nil {
		return CollectionSelection{}, fmt.Errorf("decode collection selection exclusions: %w", err)
	}
	selection := CollectionSelection{
		Kind:                   record.Kind,
		Targets:                cloneCollectionSelectionTargets(targets),
		ExcludedIDs:            append([]string(nil), exclusions...),
		Version:                record.SelectionVersion,
		ReconfirmationRequired: record.ReconfirmationRequired,
		ReconfirmationReason:   record.ReconfirmationReason,
	}
	if record.PageCursor != nil {
		selection.Cursor = *record.PageCursor
	}
	if record.FilterFingerprint != nil {
		selection.FilterFingerprint = *record.FilterFingerprint
	}
	if record.SelectionToken != nil {
		selection.Token = *record.SelectionToken
	}
	if record.FrozenExpiresAt != nil {
		selection.ExpiresAt = record.FrozenExpiresAt.UTC()
	}
	if err := validateCollectionSelection(selection, time.Now().UTC(), false); err != nil {
		return CollectionSelection{}, fmt.Errorf("stored collection selection is invalid: %w", err)
	}
	return selection, nil
}

func collectionSelectionChangeReason(record CollectionSelectionRecord, scope CollectionSelectionScope, now time.Time) (CollectionSelectionReconfirmationReason, bool) {
	if record.ContextFingerprint != scope.ContextFingerprint {
		return CollectionSelectionReconfirmContextChanged, true
	}
	if record.AuthorizationEpoch != scope.AuthorizationEpoch {
		return CollectionSelectionReconfirmGrantChanged, true
	}
	if record.CollectionVersion != scope.CollectionVersion {
		return CollectionSelectionReconfirmCollectionChanged, true
	}
	if record.Kind == CollectionSelectionFrozenFilter && (record.FrozenExpiresAt == nil || !record.FrozenExpiresAt.After(now)) {
		return CollectionSelectionReconfirmExpired, true
	}
	return "", false
}

func validateCollectionSelectionScope(scope CollectionSelectionScope) error {
	if scope.SubjectUserID < 1 || !validCollectionSelectionText(scope.SessionID, 256) || !validCollectionSelectionDomain(scope.Domain) ||
		!validCollectionSelectionFingerprint(scope.ContextFingerprint) || scope.AuthorizationEpoch < 1 || scope.CollectionVersion < 1 {
		return ErrCollectionSelectionInvalid
	}
	return nil
}

func validateCollectionSelection(selection CollectionSelection, now time.Time, input bool) error {
	if err := validateCollectionSelectionState(selection, input); err != nil {
		return err
	}
	switch selection.Kind {
	case CollectionSelectionNone:
		return validateEmptyCollectionSelection(selection)
	case CollectionSelectionExplicit:
		return validateExplicitCollectionSelection(selection)
	case CollectionSelectionPage:
		return validatePagedCollectionSelection(selection)
	case CollectionSelectionFrozenFilter:
		return validateFrozenCollectionSelection(selection, now, input)
	default:
		return ErrCollectionSelectionInvalid
	}
}

func validateCollectionSelectionState(selection CollectionSelection, input bool) error {
	if input {
		if selection.Version != 0 || selection.ReconfirmationRequired || selection.ReconfirmationReason != "" || selection.Token != "" {
			return ErrCollectionSelectionInvalid
		}
		return nil
	}
	if selection.Version < 1 || (selection.ReconfirmationRequired && !validCollectionSelectionReconfirmationReason(selection.ReconfirmationReason)) || (!selection.ReconfirmationRequired && selection.ReconfirmationReason != "") {
		return ErrCollectionSelectionInvalid
	}
	return nil
}

func validateEmptyCollectionSelection(selection CollectionSelection) error {
	if len(selection.Targets) != 0 || selection.Cursor != "" || selection.FilterFingerprint != "" || len(selection.ExcludedIDs) != 0 || !selection.ExpiresAt.IsZero() || selection.Token != "" {
		return ErrCollectionSelectionInvalid
	}
	return nil
}

func validateExplicitCollectionSelection(selection CollectionSelection) error {
	if selection.Cursor != "" || selection.FilterFingerprint != "" || len(selection.ExcludedIDs) != 0 || !selection.ExpiresAt.IsZero() || selection.Token != "" || !validCollectionSelectionTargets(selection.Targets, false, CollectionSelectionMaxTargets) {
		return ErrCollectionSelectionInvalid
	}
	return nil
}

func validatePagedCollectionSelection(selection CollectionSelection) error {
	if !validCollectionSelectionCursor(selection.Cursor) || selection.FilterFingerprint != "" || len(selection.ExcludedIDs) != 0 || !selection.ExpiresAt.IsZero() || selection.Token != "" || !validCollectionSelectionTargets(selection.Targets, false, CollectionPageMaxSize) {
		return ErrCollectionSelectionInvalid
	}
	return nil
}

func validateFrozenCollectionSelection(selection CollectionSelection, now time.Time, input bool) error {
	if selection.Cursor != "" || !validCollectionSelectionFingerprint(selection.FilterFingerprint) || !validCollectionSelectionTargets(selection.Targets, true, CollectionSelectionMaxTargets) || !validCollectionSelectionExclusions(selection.ExcludedIDs, selection.Targets) {
		return ErrCollectionSelectionInvalid
	}
	if input && selection.Token != "" {
		return ErrCollectionSelectionInvalid
	}
	if !input && !validCollectionSelectionToken(selection.Token) {
		return ErrCollectionSelectionInvalid
	}
	if selection.ExpiresAt.IsZero() || (input && (!selection.ExpiresAt.After(now) || selection.ExpiresAt.After(now.Add(collectionSelectionMaxTTL)))) {
		return ErrCollectionSelectionInvalid
	}
	return nil
}

// ValidateCollectionSelectionInput validates only browser-supplied selection
// intent after a frozen selection has received server-owned membership and TTL.
// Frozen targets and tokens remain server-generated.
func ValidateCollectionSelectionInput(selection CollectionSelection) error {
	return validateCollectionSelection(selection, time.Now().UTC(), true)
}

// ValidateCollectionFrozenSelectionRequest validates the part of a frozen
// selection request that a browser may supply before domain freezing occurs.
// It deliberately cannot validate membership, which only the freezer knows.
func ValidateCollectionFrozenSelectionRequest(filterFingerprint string, excludedIDs []string) error {
	if !validCollectionSelectionFingerprint(filterFingerprint) || len(excludedIDs) > CollectionSelectionMaxTargets {
		return ErrCollectionSelectionInvalid
	}
	seen := make(map[string]struct{}, len(excludedIDs))
	for _, id := range excludedIDs {
		if !validCollectionSelectionText(id, 256) {
			return ErrCollectionSelectionInvalid
		}
		if _, found := seen[id]; found {
			return ErrCollectionSelectionInvalid
		}
		seen[id] = struct{}{}
	}
	return nil
}

// Valid reports whether this is a bounded generic page request. The domain
// pager remains responsible for authorization and interpreting the filter.
func (request CollectionPageRequest) Valid() bool {
	return validCollectionSelectionDomain(request.Domain) && request.Filter.Valid() &&
		(request.Cursor == "" || validCollectionSelectionCursor(request.Cursor)) && request.Limit >= 1 && request.Limit <= CollectionPageMaxSize
}

// ValidFor reports whether a domain page honored the request's bounds without
// treating its returned targets as an authorization decision.
func (page CollectionPage) ValidFor(request CollectionPageRequest) bool {
	if !request.Valid() || !validCollectionSelectionCursor(page.Cursor) || len(page.Targets) > request.Limit || (page.NextCursor != "" && !validCollectionSelectionCursor(page.NextCursor)) {
		return false
	}
	if page.Total != nil && (*page.Total < 0 || *page.Total < int64(len(page.Targets))) {
		return false
	}
	if len(page.Targets) == 0 {
		return true
	}
	return validCollectionSelectionTargets(page.Targets, false, request.Limit)
}

func validCollectionSelectionTargets(targets []CollectionSelectionTarget, requireVersion bool, limit int) bool {
	if len(targets) == 0 || len(targets) > limit {
		return false
	}
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if !validCollectionSelectionText(target.ID, 256) || (requireVersion && target.ExpectedVersion == 0) {
			return false
		}
		if _, found := seen[target.ID]; found {
			return false
		}
		seen[target.ID] = struct{}{}
	}
	return true
}

func validCollectionSelectionExclusions(exclusions []string, targets []CollectionSelectionTarget) bool {
	if len(exclusions) > len(targets) {
		return false
	}
	targetIDs := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		targetIDs[target.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(exclusions))
	for _, id := range exclusions {
		if !validCollectionSelectionText(id, 256) {
			return false
		}
		if _, found := targetIDs[id]; !found {
			return false
		}
		if _, found := seen[id]; found {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

// ValidCollectionSelectionDomain reports whether a browser collection domain
// has the bounded canonical form shared by every selection request.
func ValidCollectionSelectionDomain(domain string) bool {
	return validCollectionSelectionDomain(domain)
}

func validCollectionSelectionDomain(domain string) bool {
	if len(domain) == 0 || len(domain) > 64 {
		return false
	}
	for index, character := range domain {
		if (character < 'a' || character > 'z') && (index == 0 || (character < '0' || character > '9') && character != '_' && character != '-') {
			return false
		}
	}
	return true
}

func validCollectionSelectionFingerprint(value string) bool {
	if len(value) != len(collectionSelectionDigestPrefix)+64 || !strings.HasPrefix(value, collectionSelectionDigestPrefix) {
		return false
	}
	for _, character := range value[len(collectionSelectionDigestPrefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validCollectionSelectionCursor(value string) bool {
	return validCollectionSelectionText(value, collectionSelectionMaxCursor)
}

func validCollectionSelectionToken(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func validCollectionSelectionReconfirmationReason(reason CollectionSelectionReconfirmationReason) bool {
	switch reason {
	case CollectionSelectionReconfirmFilterChanged, CollectionSelectionReconfirmContextChanged, CollectionSelectionReconfirmGrantChanged, CollectionSelectionReconfirmCollectionChanged, CollectionSelectionReconfirmExpired:
		return true
	default:
		return false
	}
}

func validCollectionSelectionText(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func cloneCollectionSelectionTargets(targets []CollectionSelectionTarget) []CollectionSelectionTarget {
	return append([]CollectionSelectionTarget(nil), targets...)
}

func collectionSelectionStringPointer(value string) *string {
	copy := value
	return &copy
}

func (store *CollectionSelectionStore) requireDB(operation string) error {
	if store == nil || store.db == nil {
		return fmt.Errorf("collection selection %s: %w", operation, errCollectionSelectionStoreNotConfigured)
	}
	return nil
}
