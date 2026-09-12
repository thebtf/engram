package gorm

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCollectionSelectionStore_PersistsDistinctSnapshots(t *testing.T) {
	fixture := newCollectionSelectionFixture(t)
	ctx := context.Background()
	scope := fixture.scope("rules")

	none, err := fixture.store.Save(ctx, scope, CollectionSelection{Kind: CollectionSelectionNone})
	require.NoError(t, err)
	require.Equal(t, CollectionSelectionNone, none.Kind)
	require.EqualValues(t, 1, none.Version)

	explicit, err := fixture.store.Save(ctx, scope, CollectionSelection{
		Kind: CollectionSelectionExplicit,
		Targets: []CollectionSelectionTarget{
			{ID: "rule-1", ExpectedVersion: 3},
			{ID: "rule-2", ExpectedVersion: 4},
		},
	})
	require.NoError(t, err)
	require.Equal(t, CollectionSelectionExplicit, explicit.Kind)
	require.EqualValues(t, 2, explicit.Version)
	require.Equal(t, []CollectionSelectionTarget{{ID: "rule-1", ExpectedVersion: 3}, {ID: "rule-2", ExpectedVersion: 4}}, explicit.Targets)

	page, err := fixture.store.Save(ctx, scope, CollectionSelection{
		Kind:   CollectionSelectionPage,
		Cursor: "cursor-opaque-page-2",
		Targets: []CollectionSelectionTarget{
			{ID: "rule-3", ExpectedVersion: 5},
			{ID: "rule-4", ExpectedVersion: 6},
		},
	})
	require.NoError(t, err)
	require.Equal(t, CollectionSelectionPage, page.Kind)
	require.Equal(t, "cursor-opaque-page-2", page.Cursor)
	require.EqualValues(t, 3, page.Version)

	expiresAt := time.Now().UTC().Add(20 * time.Minute)
	frozen, err := fixture.store.Save(ctx, scope, CollectionSelection{
		Kind:              CollectionSelectionFrozenFilter,
		FilterFingerprint: collectionSelectionTestDigest("f"),
		Targets: []CollectionSelectionTarget{
			{ID: "rule-1", ExpectedVersion: 3},
			{ID: "rule-2", ExpectedVersion: 4},
			{ID: "rule-3", ExpectedVersion: 5},
		},
		ExcludedIDs: []string{"rule-2"},
		ExpiresAt:   expiresAt,
	})
	require.NoError(t, err)
	require.Equal(t, CollectionSelectionFrozenFilter, frozen.Kind)
	require.NotEmpty(t, frozen.Token)
	require.Equal(t, []string{"rule-2"}, frozen.ExcludedIDs)
	require.EqualValues(t, 4, frozen.Version)

	current, err := fixture.store.Current(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, frozen.Kind, current.Kind)
	require.Equal(t, frozen.Targets, current.Targets)
	require.Equal(t, frozen.FilterFingerprint, current.FilterFingerprint)
	require.Equal(t, frozen.ExcludedIDs, current.ExcludedIDs)
	require.Equal(t, frozen.Token, current.Token)
	require.Equal(t, frozen.Version, current.Version)
	require.WithinDuration(t, frozen.ExpiresAt, current.ExpiresAt, time.Microsecond)
}

func TestCollectionSelectionStore_RejectsOversizedAndAmbiguousSnapshots(t *testing.T) {
	fixture := newCollectionSelectionFixture(t)
	ctx := context.Background()
	scope := fixture.scope("rules")

	_, err := fixture.store.Save(ctx, scope, CollectionSelection{
		Kind: CollectionSelectionExplicit,
		Targets: []CollectionSelectionTarget{
			{ID: "rule-1", ExpectedVersion: 1},
			{ID: "rule-1", ExpectedVersion: 2},
		},
	})
	require.ErrorIs(t, err, ErrCollectionSelectionInvalid)

	overflow := make([]CollectionSelectionTarget, CollectionSelectionMaxTargets+1)
	for index := range overflow {
		overflow[index] = CollectionSelectionTarget{ID: fmt.Sprintf("rule-%d", index), ExpectedVersion: 1}
	}
	_, err = fixture.store.Save(ctx, scope, CollectionSelection{Kind: CollectionSelectionExplicit, Targets: overflow})
	require.ErrorIs(t, err, ErrCollectionSelectionInvalid)

	_, err = fixture.store.Save(ctx, scope, CollectionSelection{
		Kind:              CollectionSelectionFrozenFilter,
		FilterFingerprint: collectionSelectionTestDigest("f"),
		Targets:           []CollectionSelectionTarget{{ID: "rule-1", ExpectedVersion: 1}},
		ExcludedIDs:       []string{"rule-not-frozen"},
		ExpiresAt:         time.Now().UTC().Add(time.Minute),
	})
	require.ErrorIs(t, err, ErrCollectionSelectionInvalid)
}

func TestCollectionSelectionPageRequest_RejectsUnboundedCursorAndInvalidFreezeIntent(t *testing.T) {
	filter := CollectionFilter{Fingerprint: collectionSelectionTestDigest("f"), Value: "project"}
	request := CollectionPageRequest{
		Domain: "rules",
		Filter: filter,
		Limit:  CollectionPageMaxSize,
	}
	require.True(t, request.Valid())

	request.Cursor = strings.Repeat("c", 513)
	require.False(t, request.Valid())
	request.Cursor = ""
	request.Limit = CollectionPageMaxSize + 1
	require.False(t, request.Valid())

	page := CollectionPage{
		Cursor:     "page-cursor",
		Targets:    []CollectionSelectionTarget{{ID: "rule-1", ExpectedVersion: 1}},
		NextCursor: strings.Repeat("n", 513),
	}
	require.False(t, page.ValidFor(CollectionPageRequest{
		Domain: "rules",
		Filter: filter,
		Limit:  1,
	}))
	require.ErrorIs(t, ValidateCollectionFrozenSelectionRequest(collectionSelectionTestDigest("f"), []string{"rule-1", "rule-1"}), ErrCollectionSelectionInvalid)
}

func TestCollectionSelectionStore_ChangesInvalidateAndTokenStaysOwnerBound(t *testing.T) {
	for name, change := range map[string]struct {
		apply  func(CollectionSelectionScope) CollectionSelectionScope
		reason CollectionSelectionReconfirmationReason
	}{
		"filter": {
			apply:  func(scope CollectionSelectionScope) CollectionSelectionScope { return scope },
			reason: CollectionSelectionReconfirmFilterChanged,
		},
		"context": {
			apply: func(scope CollectionSelectionScope) CollectionSelectionScope {
				scope.ContextFingerprint = collectionSelectionTestDigest("d")
				return scope
			},
			reason: CollectionSelectionReconfirmContextChanged,
		},
		"grant": {
			apply: func(scope CollectionSelectionScope) CollectionSelectionScope {
				scope.AuthorizationEpoch++
				return scope
			},
			reason: CollectionSelectionReconfirmGrantChanged,
		},
		"collection": {
			apply: func(scope CollectionSelectionScope) CollectionSelectionScope {
				scope.CollectionVersion++
				return scope
			},
			reason: CollectionSelectionReconfirmCollectionChanged,
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newCollectionSelectionFixture(t)
			ctx := context.Background()
			scope := fixture.scope("rules")
			frozen, err := fixture.store.Save(ctx, scope, CollectionSelection{
				Kind:              CollectionSelectionFrozenFilter,
				FilterFingerprint: collectionSelectionTestDigest("f"),
				Targets:           []CollectionSelectionTarget{{ID: "rule-1", ExpectedVersion: 1}},
				ExpiresAt:         time.Now().UTC().Add(time.Minute),
			})
			require.NoError(t, err)

			changed := change.apply(scope)
			var current CollectionSelection
			if change.reason == CollectionSelectionReconfirmFilterChanged {
				current, err = fixture.store.RequireReconfirmation(ctx, changed, frozen.Version, change.reason)
			} else {
				current, err = fixture.store.Current(ctx, changed)
			}
			require.NoError(t, err)
			require.True(t, current.ReconfirmationRequired)
			require.Equal(t, change.reason, current.ReconfirmationReason)
			require.Greater(t, current.Version, frozen.Version)

			_, err = fixture.store.Frozen(ctx, changed, frozen.Token)
			require.ErrorIs(t, err, ErrCollectionSelectionReconfirmationRequired)

			for name, foreignScope := range map[string]CollectionSelectionScope{
				"owner": func() CollectionSelectionScope { value := scope; value.SubjectUserID++; return value }(),
				"session": func() CollectionSelectionScope {
					value := scope
					value.SessionID = "browser-selection-session-foreign"
					return value
				}(),
				"domain": func() CollectionSelectionScope { value := scope; value.Domain = "issues"; return value }(),
			} {
				t.Run(name, func(t *testing.T) {
					_, err := fixture.store.Frozen(ctx, foreignScope, frozen.Token)
					require.ErrorIs(t, err, ErrCollectionSelectionDenied, "a valid opaque token must not select or disclose another scope's snapshot")
				})
			}
		})
	}
}

type collectionSelectionFixture struct {
	store *CollectionSelectionStore
}

func (fixture collectionSelectionFixture) scope(domain string) CollectionSelectionScope {
	return CollectionSelectionScope{
		SubjectUserID:      41,
		SessionID:          "browser-selection-session",
		Domain:             domain,
		ContextFingerprint: collectionSelectionTestDigest("c"),
		AuthorizationEpoch: 7,
		CollectionVersion:  11,
	}
}

func newCollectionSelectionFixture(t *testing.T) collectionSelectionFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN is not set; collection selection tests require a PostgreSQL database")
	}
	db, err := gormlib.Open(postgres.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Warn)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, sqlDB.Ping())

	schema := "t021_collection_selection_" + strings.ReplaceAll(fmt.Sprintf("%d", time.Now().UnixNano()), "-", "")
	require.NoError(t, db.Exec("CREATE SCHEMA "+schema).Error)
	require.NoError(t, db.Exec("SET search_path TO "+schema).Error)
	require.NoError(t, db.AutoMigrate(&CollectionSelectionRecord{}))
	t.Cleanup(func() {
		_ = db.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		_ = sqlDB.Close()
	})
	return collectionSelectionFixture{store: NewCollectionSelectionStore(db)}
}

func collectionSelectionTestDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
