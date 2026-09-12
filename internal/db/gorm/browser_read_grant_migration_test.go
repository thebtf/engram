package gorm

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"
)

const browserReadGrantMigrationID = "176_browser_read_grants"

type browserReadGrantMigrationFixture struct {
	db               *gormlib.DB
	owner            *User
	subject          *User
	source           *UCISource
	checkout         *UCICheckout
	expiringCheckout *UCICheckout
	foreignCheckout  *UCICheckout
}

type browserReadGrantMigrationColumn struct {
	Name     string `gorm:"column:column_name"`
	DataType string `gorm:"column:data_type"`
	Nullable string `gorm:"column:is_nullable"`
}

type browserReadGrantMigrationConstraint struct {
	Name       string `gorm:"column:conname"`
	Definition string `gorm:"column:definition"`
}

// rewindBrowserMigrationTestBoundary removes only post-boundary test schema in
// reverse migration order. Production rollback callbacks remain untouched.
func rewindBrowserMigrationTestBoundary(t *testing.T, db *gormlib.DB, boundaryMigrationID string) {
	t.Helper()
	steps := []struct {
		migrationID string
		statements  []string
	}{
		{"181_browser_code_search_continuations", []string{"DROP TABLE browser_code_search_continuations"}},
		{"180_uci_index_intent_delivery", []string{"DROP TABLE uci_index_intent_receipts", "ALTER TABLE ci_jobs DROP COLUMN index_intent_id"}},
		{"179_uci_index_intents", []string{"DROP TABLE uci_index_intents"}},
		{"178_collection_selections", []string{"DROP TABLE collection_selections"}},
		{"177_browser_tab_bindings", []string{"DROP TABLE browser_tab_bindings"}},
		{browserReadGrantMigrationID, []string{"DROP TABLE browser_read_grants"}},
	}
	for _, step := range steps {
		if step.migrationID == boundaryMigrationID {
			return
		}
		for _, statement := range step.statements {
			require.NoErrorf(t, db.Exec(statement).Error, "unwind test schema for %s", step.migrationID)
		}
		require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", step.migrationID).Error)
	}
	require.Equal(t, "175_uci_reference_source_text", boundaryMigrationID, "unsupported browser migration test boundary")
}

// TestBrowserReadGrantMigration176FreshSchemaAndStoreLifecycle proves a fresh
// migration chain owns the grant table rather than a store AutoMigrate call.
func TestBrowserReadGrantMigration176FreshSchemaAndStoreLifecycle(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	assertBrowserReadGrantMigrationApplied(t, db, 1)
	assertBrowserReadGrantMigrationSchema(t, db)

	fixture := newBrowserReadGrantMigrationFixture(t, db)
	ctx := context.Background()
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	grant, err := NewBrowserReadGrantStore(db).Issue(ctx, BrowserReadGrantIssue{
		IssuerUserID:    fixture.owner.ID,
		IssuerPrincipal: browserReadGrantMigrationPrincipal(fixture.owner.ID),
		TargetUserID:    fixture.subject.ID,
		SourceID:        fixture.source.SourceID,
		CheckoutID:      fixture.checkout.CheckoutID,
		ExpiresAt:       &expiresAt,
	})
	require.NoError(t, err)
	require.Equal(t, BrowserReadGrantActive, grant.State)
	require.NotNil(t, grant.ExpiresAt)
	require.WithinDuration(t, expiresAt, *grant.ExpiresAt, time.Microsecond)

	allowed, err := NewBrowserReadGrantStore(db).CanRead(ctx, fixture.subject.ID, fixture.source.SourceID, fixture.checkout.CheckoutID)
	require.NoError(t, err)
	require.True(t, allowed)

	revoked, err := NewBrowserReadGrantStore(db).Revoke(ctx, fixture.owner.ID, browserReadGrantMigrationPrincipal(fixture.owner.ID), grant.GrantRef)
	require.NoError(t, err)
	require.Equal(t, BrowserReadGrantRevoked, revoked.State)
	require.NotNil(t, revoked.RevokedAt)
	assertBrowserReadGrantMigrationAuditCount(t, db, "code_grant_issued", 1)
	assertBrowserReadGrantMigrationAuditCount(t, db, "code_grant_revoked", 1)

	allowed, err = NewBrowserReadGrantStore(db).CanRead(ctx, fixture.subject.ID, fixture.source.SourceID, fixture.checkout.CheckoutID)
	require.NoError(t, err)
	require.False(t, allowed)

	expiring, err := NewBrowserReadGrantStore(db).Issue(ctx, BrowserReadGrantIssue{
		IssuerUserID:    fixture.owner.ID,
		IssuerPrincipal: browserReadGrantMigrationPrincipal(fixture.owner.ID),
		TargetUserID:    fixture.subject.ID,
		SourceID:        fixture.source.SourceID,
		CheckoutID:      fixture.expiringCheckout.CheckoutID,
		ExpiresAt:       &expiresAt,
	})
	require.NoError(t, err)
	issuedAt := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Microsecond)
	expiredAt := issuedAt.Add(time.Minute)
	require.NoError(t, db.Model(&BrowserReadGrant{}).Where("grant_ref = ?", expiring.GrantRef).Updates(map[string]any{
		"issued_at":  issuedAt,
		"expires_at": expiredAt,
		"updated_at": expiredAt,
	}).Error)

	allowed, err = NewBrowserReadGrantStore(db).CanRead(ctx, fixture.subject.ID, fixture.source.SourceID, fixture.expiringCheckout.CheckoutID)
	require.NoError(t, err)
	require.False(t, allowed)
	var expired BrowserReadGrant
	require.NoError(t, db.Where("grant_ref = ?", expiring.GrantRef).First(&expired).Error)
	require.Equal(t, BrowserReadGrantExpired, expired.State)
	require.NotNil(t, expired.ExpiresAt)

	assertBrowserReadGrantMigrationConstraintFailures(t, fixture)
}

// TestBrowserReadGrantMigration176UpgradeAndRollbackRetainSchemaHistory proves
// a database at migration 175 upgrades without rewriting legacy audit data, and
// that the additive rollback callback intentionally retains durable grants.
func TestBrowserReadGrantMigration176UpgradeAndRollbackRetainSchemaHistory(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	legacyAudit := AuditLogEntry{
		Action: "browser_grant_migration_legacy_sentinel",
		Actor:  "browser-grant-migration-test",
		Reason: "must survive migration 176 upgrade and rollback",
	}
	require.NoError(t, db.Create(&legacyAudit).Error)

	rewindBrowserMigrationTestBoundary(t, db, "175_uci_reference_source_text")
	assertBrowserReadGrantMigrationApplied(t, db, 0)
	assertBrowserReadGrantMigrationPrerequisite(t, db, "175_uci_reference_source_text")
	assertBrowserReadGrantMigrationTableAbsent(t, db)

	require.NoError(t, runMigrations(db), "a chain upgraded from migration 175 must register browser grants")
	assertBrowserReadGrantMigrationApplied(t, db, 1)
	assertBrowserReadGrantMigrationSchema(t, db)
	var retainedAudit AuditLogEntry
	require.NoError(t, db.Where("id = ?", legacyAudit.ID).First(&retainedAudit).Error)
	require.Equal(t, legacyAudit.Action, retainedAudit.Action)
	require.Equal(t, legacyAudit.Reason, retainedAudit.Reason)

	fixture := newBrowserReadGrantMigrationFixture(t, db)
	grant, err := NewBrowserReadGrantStore(db).Issue(context.Background(), BrowserReadGrantIssue{
		IssuerUserID:    fixture.owner.ID,
		IssuerPrincipal: browserReadGrantMigrationPrincipal(fixture.owner.ID),
		TargetUserID:    fixture.subject.ID,
		SourceID:        fixture.source.SourceID,
		CheckoutID:      fixture.checkout.CheckoutID,
	})
	require.NoError(t, err)

	var historyBefore int64
	require.NoError(t, db.Table("migrations").Count(&historyBefore).Error)
	require.NoError(t, rollbackBrowserReadGrantsMigration176(db), "binary rollback must retain additive grant data")
	var historyAfter int64
	require.NoError(t, db.Table("migrations").Count(&historyAfter).Error)
	require.Equal(t, historyBefore, historyAfter, "the rollback boundary must retain migration history")
	assertBrowserReadGrantMigrationApplied(t, db, 1)
	var retainedGrant BrowserReadGrant
	require.NoError(t, db.Where("grant_ref = ?", grant.GrantRef).First(&retainedGrant).Error)
	require.Equal(t, grant.GrantRef, retainedGrant.GrantRef)

	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", browserReadGrantMigrationID).Error)
	require.NoError(t, runMigrations(db), "marker-only replay must keep retained grants and restore migration history")
	assertBrowserReadGrantMigrationApplied(t, db, 1)
	require.NoError(t, db.Where("grant_ref = ?", grant.GrantRef).First(&retainedGrant).Error)
}

func newBrowserReadGrantMigrationFixture(t *testing.T, db *gormlib.DB) browserReadGrantMigrationFixture {
	t.Helper()
	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	now := time.Now().UTC().Truncate(time.Microsecond)
	newUser := func(label string) *User {
		user := &User{
			Email:        fmt.Sprintf("browser-grant-migration-%s-%s@example.test", label, token),
			PasswordHash: "browser-grant-migration-fixture",
			Role:         DashboardRoleOperator,
			CreatedAt:    now,
		}
		require.NoError(t, db.Create(user).Error)
		return user
	}
	owner := newUser("owner")
	subject := newUser("subject")
	contexts := NewUCIContextStore(db)
	realm := "browser-grant-migration-realm-" + token
	source, err := contexts.CreateSource(context.Background(), CreateSourceInput{
		AuthRealm:   realm,
		Kind:        UCISourceGit,
		DisplayName: "browser-grant-migration-source-" + token,
	})
	require.NoError(t, err)
	newCheckout := func(label string, sourceID string) *UCICheckout {
		checkout, checkoutErr := contexts.RegisterCheckout(context.Background(), RegisterCheckoutInput{
			SourceID:       sourceID,
			WorkstationID:  "browser-grant-migration-workstation-" + label + "-" + token,
			Kind:           UCICheckoutWorkingTree,
			OwnerPrincipal: browserReadGrantMigrationPrincipal(owner.ID),
			LocatorRef:     "file:///browser-grant-migration/" + label + "/" + token,
		})
		require.NoError(t, checkoutErr)
		return checkout
	}
	checkout := newCheckout("main", source.SourceID)
	expiringCheckout := newCheckout("expiring", source.SourceID)
	foreignSource, err := contexts.CreateSource(context.Background(), CreateSourceInput{
		AuthRealm:   realm,
		Kind:        UCISourceGit,
		DisplayName: "browser-grant-migration-foreign-source-" + token,
	})
	require.NoError(t, err)

	return browserReadGrantMigrationFixture{
		db:               db,
		owner:            owner,
		subject:          subject,
		source:           source,
		checkout:         checkout,
		expiringCheckout: expiringCheckout,
		foreignCheckout:  newCheckout("foreign", foreignSource.SourceID),
	}
}

func assertBrowserReadGrantMigrationSchema(t *testing.T, db *gormlib.DB) {
	t.Helper()
	columns := browserReadGrantMigrationColumns(t, db)
	for name, want := range map[string]browserReadGrantMigrationColumn{
		"grant_ref":        {DataType: "uuid", Nullable: "NO"},
		"auth_realm":       {DataType: "text", Nullable: "NO"},
		"subject_user_id":  {DataType: "bigint", Nullable: "NO"},
		"source_id":        {DataType: "uuid", Nullable: "NO"},
		"checkout_id":      {DataType: "uuid", Nullable: "NO"},
		"state":            {DataType: "text", Nullable: "NO"},
		"issuer_principal": {DataType: "text", Nullable: "NO"},
		"expires_at":       {DataType: "timestamp with time zone", Nullable: "YES"},
		"issued_at":        {DataType: "timestamp with time zone", Nullable: "NO"},
		"revoked_at":       {DataType: "timestamp with time zone", Nullable: "YES"},
		"created_at":       {DataType: "timestamp with time zone", Nullable: "NO"},
		"updated_at":       {DataType: "timestamp with time zone", Nullable: "NO"},
	} {
		actual, exists := columns[name]
		require.Truef(t, exists, "migration 176 must create browser_read_grants.%s", name)
		require.Equalf(t, want.DataType, actual.DataType, "browser_read_grants.%s type", name)
		require.Equalf(t, want.Nullable, actual.Nullable, "browser_read_grants.%s nullability", name)
	}

	assertBrowserReadGrantMigrationConstraint(t, db, "browser_read_grants_auth_realm_chk", "check", "btrim", "auth_realm")
	assertBrowserReadGrantMigrationConstraint(t, db, "browser_read_grants_subject_user_id_chk", "check", "subject_user_id", "> 0")
	assertBrowserReadGrantMigrationConstraint(t, db, "browser_read_grants_state_chk", "check", "active", "revoked", "expired")
	assertBrowserReadGrantMigrationConstraint(t, db, "browser_read_grants_issuer_principal_chk", "check", "btrim", "issuer_principal")
	assertBrowserReadGrantMigrationConstraint(t, db, "browser_read_grants_lifecycle_chk", "check", "revoked_at", "expires_at", "issued_at")
	assertBrowserReadGrantMigrationConstraint(t, db, "browser_read_grants_tuple_unique", "unique", "auth_realm", "subject_user_id", "source_id", "checkout_id")
	assertBrowserReadGrantMigrationConstraint(t, db, "browser_read_grants_subject_user_fkey", "foreign key", "references", "users", "subject_user_id")
	assertBrowserReadGrantMigrationConstraint(t, db, "browser_read_grants_source_realm_fkey", "foreign key", "references", "sources", "source_id", "auth_realm")
	assertBrowserReadGrantMigrationConstraint(t, db, "browser_read_grants_checkout_source_fkey", "foreign key", "references", "ci_checkouts", "source_id", "checkout_id")

	var activeReadIndex string
	require.NoError(t, db.Raw(`
		SELECT indexdef
		FROM pg_indexes
		WHERE schemaname = current_schema()
			AND tablename = 'browser_read_grants'
			AND indexname = 'idx_browser_read_grants_active_exact_read'
	`).Scan(&activeReadIndex).Error)
	activeReadIndex = strings.ToLower(activeReadIndex)
	for _, needle := range []string{"subject_user_id", "source_id", "checkout_id", "expires_at", "where", "active"} {
		require.Contains(t, activeReadIndex, needle, "active exact-read index must serve grant reads")
	}
}

func assertBrowserReadGrantMigrationConstraintFailures(t *testing.T, fixture browserReadGrantMigrationFixture) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	insert := func(grantRef, realm string, subjectUserID int64, sourceID, checkoutID, state, issuer string, expiresAt, revokedAt *time.Time) error {
		return fixture.db.Exec(`
			INSERT INTO browser_read_grants (
				grant_ref, auth_realm, subject_user_id, source_id, checkout_id,
				state, issuer_principal, expires_at, issued_at, revoked_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, grantRef, realm, subjectUserID, sourceID, checkoutID, state, issuer, expiresAt, now, revokedAt, now, now).Error
	}

	require.Error(t, insert(uuid.NewString(), " ", fixture.subject.ID, fixture.source.SourceID, fixture.checkout.CheckoutID, "active", browserReadGrantMigrationPrincipal(fixture.owner.ID), nil, nil), "realm must be nonblank and normalized")
	require.Error(t, insert(uuid.NewString(), fixture.source.AuthRealm, fixture.subject.ID, fixture.foreignCheckout.SourceID, fixture.foreignCheckout.CheckoutID, "pending", browserReadGrantMigrationPrincipal(fixture.owner.ID), nil, nil), "state must stay in the closed grant lifecycle")
	require.Error(t, insert(uuid.NewString(), fixture.source.AuthRealm, fixture.subject.ID, fixture.foreignCheckout.SourceID, fixture.foreignCheckout.CheckoutID, "revoked", browserReadGrantMigrationPrincipal(fixture.owner.ID), nil, nil), "revoked state requires a revoke timestamp")
	require.Error(t, insert(uuid.NewString(), fixture.source.AuthRealm, fixture.subject.ID, fixture.source.SourceID, fixture.foreignCheckout.CheckoutID, "active", browserReadGrantMigrationPrincipal(fixture.owner.ID), nil, nil), "checkout must resolve under the grant source")
	require.Error(t, insert(uuid.NewString(), fixture.source.AuthRealm, fixture.subject.ID+1000000, fixture.foreignCheckout.SourceID, fixture.foreignCheckout.CheckoutID, "active", browserReadGrantMigrationPrincipal(fixture.owner.ID), nil, nil), "grant subject must resolve to a persisted browser user")
	require.Error(t, insert(uuid.NewString(), fixture.source.AuthRealm, fixture.subject.ID, fixture.source.SourceID, fixture.checkout.CheckoutID, "revoked", browserReadGrantMigrationPrincipal(fixture.owner.ID), nil, &now), "one realm subject source checkout tuple may have only one durable grant")
}

func browserReadGrantMigrationColumns(t *testing.T, db *gormlib.DB) map[string]browserReadGrantMigrationColumn {
	t.Helper()
	var rows []browserReadGrantMigrationColumn
	require.NoError(t, db.Raw(`
		SELECT column_name, data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'browser_read_grants'
		ORDER BY ordinal_position
	`).Scan(&rows).Error)
	columns := make(map[string]browserReadGrantMigrationColumn, len(rows))
	for _, row := range rows {
		columns[row.Name] = row
	}
	return columns
}

func assertBrowserReadGrantMigrationConstraint(t *testing.T, db *gormlib.DB, name string, needles ...string) {
	t.Helper()
	var row browserReadGrantMigrationConstraint
	require.NoError(t, db.Raw(`
		SELECT constraint_row.conname, pg_get_constraintdef(constraint_row.oid) AS definition
		FROM pg_constraint AS constraint_row
		JOIN pg_class AS relation ON relation.oid = constraint_row.conrelid
		JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = current_schema()
			AND relation.relname = 'browser_read_grants'
			AND constraint_row.conname = ?
	`, name).Scan(&row).Error)
	require.Equal(t, name, row.Name, "migration 176 must retain named constraint %s", name)
	row.Definition = strings.ToLower(row.Definition)
	for _, needle := range needles {
		require.Containsf(t, row.Definition, strings.ToLower(needle), "constraint %s", name)
	}
}

func assertBrowserReadGrantMigrationApplied(t *testing.T, db *gormlib.DB, want int64) {
	t.Helper()
	var applied int64
	require.NoError(t, db.Table("migrations").Where("id = ?", browserReadGrantMigrationID).Count(&applied).Error)
	require.Equal(t, want, applied, "migration 176 registry entry")
}

func assertBrowserReadGrantMigrationPrerequisite(t *testing.T, db *gormlib.DB, migrationID string) {
	t.Helper()
	var applied int64
	require.NoError(t, db.Table("migrations").Where("id = ?", migrationID).Count(&applied).Error)
	require.Equalf(t, int64(1), applied, "upgrade fixture must retain prerequisite migration %s", migrationID)
}

func assertBrowserReadGrantMigrationTableAbsent(t *testing.T, db *gormlib.DB) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = 'browser_read_grants'
	`).Scan(&count).Error)
	require.Zero(t, count, "upgrade fixture must reproduce the migration 175 schema boundary")
}

func assertBrowserReadGrantMigrationAuditCount(t *testing.T, db *gormlib.DB, action string, want int64) {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&AuditLogEntry{}).Where("action = ?", action).Count(&count).Error)
	require.Equal(t, want, count, "grant lifecycle audit action %s", action)
}

func browserReadGrantMigrationPrincipal(userID int64) string {
	return fmt.Sprintf("browser-grant-migration-user/%d", userID)
}
