package gorm

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type browserReadGrantFixture struct {
	db              *gormlib.DB
	store           *BrowserReadGrantStore
	owner           *User
	target          *User
	other           *User
	source          *UCISource
	checkout        *UCICheckout
	otherCheckout   *UCICheckout
	foreignSource   *UCISource
	foreignCheckout *UCICheckout
}

func TestBrowserReadGrantStore_ExactTupleAndEnabledSubject(t *testing.T) {
	fixture := newBrowserReadGrantFixture(t)
	ctx := context.Background()

	grant, err := fixture.store.Issue(ctx, BrowserReadGrantIssue{
		IssuerUserID:    fixture.owner.ID,
		IssuerPrincipal: browserReadGrantPrincipal(fixture.owner.ID),
		TargetUserID:    fixture.target.ID,
		SourceID:        fixture.source.SourceID,
		CheckoutID:      fixture.checkout.CheckoutID,
	})
	require.NoError(t, err)
	require.Equal(t, BrowserReadGrantActive, grant.State)

	allowed, err := fixture.store.CanRead(ctx, fixture.target.ID, fixture.source.SourceID, fixture.checkout.CheckoutID)
	require.NoError(t, err)
	require.True(t, allowed, "the issued exact tuple must be readable")

	for name, tuple := range map[string]struct {
		subjectID  int64
		sourceID   string
		checkoutID string
	}{
		"different human":            {fixture.other.ID, fixture.source.SourceID, fixture.checkout.CheckoutID},
		"same source other checkout": {fixture.target.ID, fixture.source.SourceID, fixture.otherCheckout.CheckoutID},
		"different source checkout":  {fixture.target.ID, fixture.foreignSource.SourceID, fixture.foreignCheckout.CheckoutID},
	} {
		t.Run(name, func(t *testing.T) {
			canRead, readErr := fixture.store.CanRead(ctx, tuple.subjectID, tuple.sourceID, tuple.checkoutID)
			require.NoError(t, readErr)
			require.False(t, canRead)
		})
	}

	require.NoError(t, fixture.db.Model(&User{}).Where("id = ?", fixture.target.ID).Update("disabled", true).Error)
	allowed, err = fixture.store.CanRead(ctx, fixture.target.ID, fixture.source.SourceID, fixture.checkout.CheckoutID)
	require.NoError(t, err)
	require.False(t, allowed, "a disabled persisted user loses grant reads")
}

func TestBrowserReadGrantStore_RequiresExactSourceOwnerAndAuditsAtomically(t *testing.T) {
	fixture := newBrowserReadGrantFixture(t)
	ctx := context.Background()

	_, err := fixture.store.Issue(ctx, BrowserReadGrantIssue{
		IssuerUserID:    fixture.other.ID,
		IssuerPrincipal: browserReadGrantPrincipal(fixture.other.ID),
		TargetUserID:    fixture.target.ID,
		SourceID:        fixture.source.SourceID,
		CheckoutID:      fixture.checkout.CheckoutID,
	})
	require.ErrorIs(t, err, ErrBrowserReadGrantDenied, "role, Space, path, label, and legacy identity are absent from the owner predicate")

	require.NoError(t, fixture.db.Model(&User{}).Where("id = ?", fixture.owner.ID).Update("disabled", true).Error)
	_, err = fixture.store.Issue(ctx, BrowserReadGrantIssue{
		IssuerUserID:    fixture.owner.ID,
		IssuerPrincipal: browserReadGrantPrincipal(fixture.owner.ID),
		TargetUserID:    fixture.target.ID,
		SourceID:        fixture.source.SourceID,
		CheckoutID:      fixture.checkout.CheckoutID,
	})
	require.ErrorIs(t, err, ErrBrowserReadGrantDenied)
	require.NoError(t, fixture.db.Model(&User{}).Where("id = ?", fixture.owner.ID).Update("disabled", false).Error)

	grant, err := fixture.store.Issue(ctx, BrowserReadGrantIssue{
		IssuerUserID:    fixture.owner.ID,
		IssuerPrincipal: browserReadGrantPrincipal(fixture.owner.ID),
		TargetUserID:    fixture.target.ID,
		SourceID:        fixture.source.SourceID,
		CheckoutID:      fixture.checkout.CheckoutID,
	})
	require.NoError(t, err)
	assertBrowserReadGrantAuditCount(t, fixture.db, "code_grant_issued", 1)

	_, err = fixture.store.Revoke(ctx, fixture.other.ID, browserReadGrantPrincipal(fixture.other.ID), grant.GrantRef)
	require.ErrorIs(t, err, ErrBrowserReadGrantDenied)

	revoked, err := fixture.store.Revoke(ctx, fixture.owner.ID, browserReadGrantPrincipal(fixture.owner.ID), grant.GrantRef)
	require.NoError(t, err)
	require.Equal(t, BrowserReadGrantRevoked, revoked.State)
	require.NotNil(t, revoked.RevokedAt)
	assertBrowserReadGrantAuditCount(t, fixture.db, "code_grant_revoked", 1)

	canRead, err := fixture.store.CanRead(ctx, fixture.target.ID, fixture.source.SourceID, fixture.checkout.CheckoutID)
	require.NoError(t, err)
	require.False(t, canRead)
}

func TestBrowserReadGrantStore_AuditFailureRollsBackIssue(t *testing.T) {
	fixture := newBrowserReadGrantFixture(t)
	ctx := context.Background()
	require.NoError(t, fixture.db.Migrator().DropTable(&AuditLogEntry{}))

	_, err := fixture.store.Issue(ctx, BrowserReadGrantIssue{
		IssuerUserID:    fixture.owner.ID,
		IssuerPrincipal: browserReadGrantPrincipal(fixture.owner.ID),
		TargetUserID:    fixture.target.ID,
		SourceID:        fixture.source.SourceID,
		CheckoutID:      fixture.checkout.CheckoutID,
	})
	require.Error(t, err)

	var count int64
	require.NoError(t, fixture.db.Model(&BrowserReadGrant{}).Count(&count).Error)
	require.Zero(t, count, "audit failure must roll back the grant write in the same transaction")
}

func TestBrowserReadGrantStore_AuditFailureRollsBackRevoke(t *testing.T) {
	fixture := newBrowserReadGrantFixture(t)
	ctx := context.Background()
	grant, err := fixture.store.Issue(ctx, BrowserReadGrantIssue{
		IssuerUserID:    fixture.owner.ID,
		IssuerPrincipal: browserReadGrantPrincipal(fixture.owner.ID),
		TargetUserID:    fixture.target.ID,
		SourceID:        fixture.source.SourceID,
		CheckoutID:      fixture.checkout.CheckoutID,
	})
	require.NoError(t, err)
	require.NoError(t, fixture.db.Migrator().DropTable(&AuditLogEntry{}))

	_, err = fixture.store.Revoke(ctx, fixture.owner.ID, browserReadGrantPrincipal(fixture.owner.ID), grant.GrantRef)
	require.Error(t, err)

	var stored BrowserReadGrant
	require.NoError(t, fixture.db.Where("grant_ref = ?", grant.GrantRef).First(&stored).Error)
	require.Equal(t, BrowserReadGrantActive, stored.State, "audit failure must roll back the revoke transition")
	canRead, err := fixture.store.CanRead(ctx, fixture.target.ID, fixture.source.SourceID, fixture.checkout.CheckoutID)
	require.NoError(t, err)
	require.True(t, canRead)
}

func TestBrowserReadGrantStore_ExpiresOnRead(t *testing.T) {
	fixture := newBrowserReadGrantFixture(t)
	ctx := context.Background()
	expiredAt := time.Now().UTC().Add(-time.Minute)
	grant := BrowserReadGrant{
		GrantRef:        uuid.NewString(),
		AuthRealm:       fixture.source.AuthRealm,
		SubjectUserID:   fixture.target.ID,
		SourceID:        fixture.source.SourceID,
		CheckoutID:      fixture.checkout.CheckoutID,
		State:           BrowserReadGrantActive,
		IssuerPrincipal: browserReadGrantPrincipal(fixture.owner.ID),
		ExpiresAt:       &expiredAt,
		IssuedAt:        expiredAt.Add(-time.Minute),
		CreatedAt:       expiredAt.Add(-time.Minute),
		UpdatedAt:       expiredAt.Add(-time.Minute),
	}
	require.NoError(t, fixture.db.Create(&grant).Error)

	canRead, err := fixture.store.CanRead(ctx, fixture.target.ID, fixture.source.SourceID, fixture.checkout.CheckoutID)
	require.NoError(t, err)
	require.False(t, canRead)

	var stored BrowserReadGrant
	require.NoError(t, fixture.db.Where("grant_ref = ?", grant.GrantRef).First(&stored).Error)
	require.Equal(t, BrowserReadGrantExpired, stored.State)
}

func newBrowserReadGrantFixture(t *testing.T) browserReadGrantFixture {
	t.Helper()
	db := openBrowserReadGrantTestDB(t)
	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	now := time.Now().UTC()
	createUser := func(label string, disabled bool) *User {
		user := &User{
			Email:        fmt.Sprintf("browser-grant-%s-%s@example.test", label, token),
			PasswordHash: "fixture-password-hash",
			Role:         DashboardRoleOperator,
			Disabled:     disabled,
			CreatedAt:    now,
		}
		require.NoError(t, db.Create(user).Error)
		return user
	}
	owner := createUser("owner", false)
	target := createUser("target", false)
	other := createUser("other", false)

	newSource := func(label string) *UCISource {
		source := &UCISource{
			SourceID:    uuid.NewString(),
			AuthRealm:   "browser-grant-realm-" + token,
			Kind:        UCISourceGit,
			DisplayName: browserReadGrantPrincipal(other.ID),
			State:       UCISourceActive,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if label == "foreign" {
			source.DisplayName = "foreign-label-" + token
		}
		require.NoError(t, db.Create(source).Error)
		return source
	}
	newCheckout := func(source *UCISource, label string) *UCICheckout {
		checkout := &UCICheckout{
			CheckoutID:     uuid.NewString(),
			SourceID:       source.SourceID,
			WorkstationID:  "browser-grant-workstation-" + token + "-" + label,
			IncarnationID:  uuid.NewString(),
			Kind:           UCICheckoutWorkingTree,
			OwnerPrincipal: browserReadGrantPrincipal(owner.ID),
			LocatorRef:     "legacy-project/" + browserReadGrantPrincipal(other.ID) + "/" + label,
			State:          UCICheckoutRegistered,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		require.NoError(t, db.Create(checkout).Error)
		return checkout
	}
	source := newSource("main")
	space := &UCISpace{
		SpaceID:     uuid.NewString(),
		AuthRealm:   source.AuthRealm,
		DisplayName: browserReadGrantPrincipal(other.ID),
		State:       UCISpaceActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	require.NoError(t, db.Create(space).Error)
	require.NoError(t, db.Create(&UCISpaceSource{AuthRealm: source.AuthRealm, SpaceID: space.SpaceID, SourceID: source.SourceID}).Error)
	checkout := newCheckout(source, "main")
	otherCheckout := newCheckout(source, "other")
	foreignSource := newSource("foreign")
	foreignCheckout := newCheckout(foreignSource, "foreign")

	return browserReadGrantFixture{
		db:              db,
		store:           NewBrowserReadGrantStore(db),
		owner:           owner,
		target:          target,
		other:           other,
		source:          source,
		checkout:        checkout,
		otherCheckout:   otherCheckout,
		foreignSource:   foreignSource,
		foreignCheckout: foreignCheckout,
	}
}

func openBrowserReadGrantTestDB(t *testing.T) *gormlib.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping browser read grant integration test")
	}
	db, err := gormlib.Open(postgres.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Warn)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, sqlDB.Ping())

	schema := "t012_browser_grant_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, db.Exec("CREATE SCHEMA "+schema).Error)
	require.NoError(t, db.Exec("SET search_path TO "+schema).Error)
	require.NoError(t, db.AutoMigrate(&User{}, &AuditLogEntry{}, &UCISpace{}, &UCISpaceSource{}, &UCISource{}, &UCICheckout{}, &BrowserReadGrant{}))
	t.Cleanup(func() {
		_ = db.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		_ = sqlDB.Close()
	})
	return db
}

func browserReadGrantPrincipal(userID int64) string {
	return fmt.Sprintf("browser-user/%d", userID)
}

func assertBrowserReadGrantAuditCount(t *testing.T, db *gormlib.DB, action string, want int64) {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&AuditLogEntry{}).Where("action = ?", action).Count(&count).Error)
	require.Equal(t, want, count)
}
