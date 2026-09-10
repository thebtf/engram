package gorm

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"
)

const browserTabBindingMigrationID = "177_browser_tab_bindings"

type browserTabBindingMigrationColumn struct {
	Name     string `gorm:"column:column_name"`
	DataType string `gorm:"column:data_type"`
	Nullable string `gorm:"column:is_nullable"`
}

type browserTabBindingMigrationConstraint struct {
	Name       string `gorm:"column:conname"`
	Definition string `gorm:"column:definition"`
}

// TestBrowserTabBindingMigration177FreshSchemaAndStoreLifecycle proves the
// migration, rather than a store AutoMigrate call, owns digest-only bindings.
func TestBrowserTabBindingMigration177FreshSchemaAndStoreLifecycle(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	assertBrowserTabBindingMigrationApplied(t, db, 1)
	assertBrowserTabBindingMigrationSchema(t, db)
	assertBrowserTabBindingMigrationConstraintFailures(t, db)

	ctx := context.Background()
	caller := BrowserTabBindingCaller{
		SubjectUserID: 41,
		SessionID:     "browser-tab-binding-migration-" + uuid.NewString(),
	}
	input := browserTabBindingMigrationCreate(caller)
	store := NewBrowserTabBindingStore(db)
	binding, err := store.Create(ctx, input)
	require.NoError(t, err)

	pinned := browserTabBindingMigrationContext()
	require.NoError(t, store.Pin(ctx, BrowserTabBindingPin{
		BrowserTabBindingGuard: BrowserTabBindingGuard{
			Caller:              caller,
			TabBindingID:        binding.TabBindingID,
			DocumentProofDigest: input.DocumentProofDigest,
		},
		Context: pinned,
	}))
	require.NoError(t, store.Close(ctx, BrowserTabBindingLease{
		BrowserTabBindingGuard: BrowserTabBindingGuard{
			Caller:              caller,
			TabBindingID:        binding.TabBindingID,
			DocumentProofDigest: input.DocumentProofDigest,
		},
	}))

	var persisted BrowserTabBinding
	require.NoError(t, db.Where("tab_binding_id = ?", binding.TabBindingID).First(&persisted).Error)
	require.Equal(t, BrowserTabBindingLeaseClosed, persisted.DocumentLeaseState)
	require.Equal(t, pinned.SourceID, *persisted.PinnedSourceID)
	require.Equal(t, pinned.CheckoutID, *persisted.PinnedCheckoutID)
	require.Equal(t, pinned.ViewID, *persisted.PinnedViewID)
	require.Equal(t, pinned.AnalysisProfileID, *persisted.PinnedAnalysisProfileID)
	require.Equal(t, pinned.Generation, *persisted.PinnedGeneration)

	require.NoError(t, store.DestroySession(ctx, caller.SessionID))
	var remaining int64
	require.NoError(t, db.Model(&BrowserTabBinding{}).Where("session_id = ?", caller.SessionID).Count(&remaining).Error)
	require.Zero(t, remaining, "session destruction must clear migrated binding state")
}

// TestBrowserTabBindingMigration177UpgradeRollbackAndReplay proves a schema at
// migration 176 upgrades additively and the retained binding replays safely.
func TestBrowserTabBindingMigration177UpgradeRollbackAndReplay(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	legacyAudit := AuditLogEntry{
		Action: "browser-tab-binding-migration-legacy-sentinel",
		Actor:  "browser-tab-binding-migration-test",
		Reason: "must survive migration 177 upgrade, rollback, and replay",
	}
	require.NoError(t, db.Create(&legacyAudit).Error)

	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", browserTabBindingMigrationID).Error)
	require.NoError(t, db.Exec("DROP TABLE browser_tab_bindings").Error)
	assertBrowserTabBindingMigrationApplied(t, db, 0)
	assertBrowserTabBindingMigrationPrerequisite(t, db, browserReadGrantMigrationID)
	assertBrowserTabBindingMigrationTableAbsent(t, db)

	require.NoError(t, runMigrations(db), "a chain upgraded from migration 176 must register browser bindings")
	assertBrowserTabBindingMigrationApplied(t, db, 1)
	assertBrowserTabBindingMigrationSchema(t, db)
	var retainedAudit AuditLogEntry
	require.NoError(t, db.Where("id = ?", legacyAudit.ID).First(&retainedAudit).Error)
	require.Equal(t, legacyAudit.Action, retainedAudit.Action)
	require.Equal(t, legacyAudit.Reason, retainedAudit.Reason)

	caller := BrowserTabBindingCaller{
		SubjectUserID: 42,
		SessionID:     "browser-tab-binding-migration-replay-" + uuid.NewString(),
	}
	binding, err := NewBrowserTabBindingStore(db).Create(context.Background(), browserTabBindingMigrationCreate(caller))
	require.NoError(t, err)

	var historyBefore int64
	require.NoError(t, db.Table("migrations").Count(&historyBefore).Error)
	require.NoError(t, rollbackBrowserTabBindingsMigration177(db), "binary rollback must retain additive binding state")
	var historyAfter int64
	require.NoError(t, db.Table("migrations").Count(&historyAfter).Error)
	require.Equal(t, historyBefore, historyAfter, "the rollback boundary must retain migration history")
	assertBrowserTabBindingMigrationApplied(t, db, 1)
	assertBrowserTabBindingMigrationRetained(t, db, binding)

	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", browserTabBindingMigrationID).Error)
	require.NoError(t, runMigrations(db), "marker-only replay must retain bindings and restore migration history")
	assertBrowserTabBindingMigrationApplied(t, db, 1)
	assertBrowserTabBindingMigrationRetained(t, db, binding)
}

func browserTabBindingMigrationCreate(caller BrowserTabBindingCaller) BrowserTabBindingCreate {
	now := time.Now().UTC()
	return BrowserTabBindingCreate{
		Caller:                 caller,
		ResumeNonceDigest:      bytes.Repeat([]byte{0x11}, 32),
		DocumentProofDigest:    bytes.Repeat([]byte{0x22}, 32),
		ReloadTokenDigest:      bytes.Repeat([]byte{0x33}, 32),
		DocumentLeaseExpiresAt: now.Add(time.Minute),
		BindingExpiresAt:       now.Add(time.Hour),
	}
}

func browserTabBindingMigrationContext() BrowserTabBindingContext {
	return BrowserTabBindingContext{
		SourceID:          uuid.NewString(),
		CheckoutID:        uuid.NewString(),
		ViewID:            uuid.NewString(),
		AnalysisProfileID: uuid.NewString(),
		Generation:        1,
	}
}

func assertBrowserTabBindingMigrationSchema(t *testing.T, db *gormlib.DB) {
	t.Helper()
	columns := browserTabBindingMigrationColumns(t, db)
	for name, want := range map[string]browserTabBindingMigrationColumn{
		"tab_binding_id":             {DataType: "uuid", Nullable: "NO"},
		"subject_user_id":            {DataType: "bigint", Nullable: "NO"},
		"session_id":                 {DataType: "text", Nullable: "NO"},
		"resume_nonce_digest":        {DataType: "bytea", Nullable: "NO"},
		"document_proof_digest":      {DataType: "bytea", Nullable: "NO"},
		"reload_token_digest":        {DataType: "bytea", Nullable: "NO"},
		"document_lease_state":       {DataType: "text", Nullable: "NO"},
		"document_lease_expires_at":  {DataType: "timestamp with time zone", Nullable: "NO"},
		"pinned_source_id":           {DataType: "uuid", Nullable: "YES"},
		"pinned_checkout_id":         {DataType: "uuid", Nullable: "YES"},
		"pinned_view_id":             {DataType: "uuid", Nullable: "YES"},
		"pinned_analysis_profile_id": {DataType: "uuid", Nullable: "YES"},
		"pinned_generation":          {DataType: "bigint", Nullable: "YES"},
		"binding_expires_at":         {DataType: "timestamp with time zone", Nullable: "NO"},
		"created_at":                 {DataType: "timestamp with time zone", Nullable: "NO"},
		"updated_at":                 {DataType: "timestamp with time zone", Nullable: "NO"},
	} {
		actual, exists := columns[name]
		require.Truef(t, exists, "migration 177 must create browser_tab_bindings.%s", name)
		require.Equalf(t, want.DataType, actual.DataType, "browser_tab_bindings.%s type", name)
		require.Equalf(t, want.Nullable, actual.Nullable, "browser_tab_bindings.%s nullability", name)
	}
	for _, forbidden := range []string{"document_nonce", "document_proof", "resume_nonce", "reload_token"} {
		require.NotContains(t, columns, forbidden, "migration 177 must not persist raw browser material")
	}

	assertBrowserTabBindingMigrationConstraint(t, db, "browser_tab_bindings_subject_user_id_chk", "subject_user_id", "> 0")
	assertBrowserTabBindingMigrationConstraint(t, db, "browser_tab_bindings_session_id_chk", "btrim", "session_id")
	assertBrowserTabBindingMigrationConstraint(t, db, "browser_tab_bindings_digest_chk", "octet_length(resume_nonce_digest) = 32", "octet_length(document_proof_digest) = 32", "octet_length(reload_token_digest) = 32")
	assertBrowserTabBindingMigrationConstraint(t, db, "browser_tab_bindings_document_lease_state_chk", "live", "closed", "expired")
	assertBrowserTabBindingMigrationConstraint(t, db, "browser_tab_bindings_expiry_chk", "binding_expires_at", "document_lease_expires_at")
	assertBrowserTabBindingMigrationConstraint(t, db, "browser_tab_bindings_pinned_context_chk", "pinned_source_id", "pinned_checkout_id", "pinned_view_id", "pinned_analysis_profile_id", "pinned_generation > 0")

	var sessionLookupIndex string
	require.NoError(t, db.Raw(`
		SELECT indexdef
		FROM pg_indexes
		WHERE schemaname = current_schema()
			AND tablename = 'browser_tab_bindings'
			AND indexname = 'idx_browser_tab_bindings_session_lookup'
	`).Scan(&sessionLookupIndex).Error)
	sessionLookupIndex = strings.ToLower(sessionLookupIndex)
	for _, needle := range []string{"session_id", "tab_binding_id"} {
		require.Contains(t, sessionLookupIndex, needle, "session lookup index must serve session cleanup")
	}
}

func assertBrowserTabBindingMigrationConstraintFailures(t *testing.T, db *gormlib.DB) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	insert := func(subjectUserID int64, sessionID string, resumeDigest, proofDigest, reloadDigest []byte, state string, leaseExpiresAt, bindingExpiresAt time.Time, pinnedSourceID, pinnedCheckoutID, pinnedViewID, pinnedAnalysisProfileID *string, pinnedGeneration *int64) error {
		return db.Exec(`
			INSERT INTO browser_tab_bindings (
				tab_binding_id, subject_user_id, session_id,
				resume_nonce_digest, document_proof_digest, reload_token_digest,
				document_lease_state, document_lease_expires_at,
				pinned_source_id, pinned_checkout_id, pinned_view_id, pinned_analysis_profile_id, pinned_generation,
				binding_expires_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, uuid.NewString(), subjectUserID, sessionID, resumeDigest, proofDigest, reloadDigest, state, leaseExpiresAt, pinnedSourceID, pinnedCheckoutID, pinnedViewID, pinnedAnalysisProfileID, pinnedGeneration, bindingExpiresAt, now, now).Error
	}
	validDigest := bytes.Repeat([]byte{0xAA}, 32)
	leaseExpiresAt := now.Add(time.Minute)
	bindingExpiresAt := now.Add(time.Hour)
	require.Error(t, insert(0, "browser-tab-binding-migration-session", validDigest, validDigest, validDigest, "live", leaseExpiresAt, bindingExpiresAt, nil, nil, nil, nil, nil), "subject must stay positive")
	require.Error(t, insert(1, " ", validDigest, validDigest, validDigest, "live", leaseExpiresAt, bindingExpiresAt, nil, nil, nil, nil, nil), "session must stay normalized and nonblank")
	require.Error(t, insert(1, "browser-tab-binding-migration-session", validDigest[:31], validDigest, validDigest, "live", leaseExpiresAt, bindingExpiresAt, nil, nil, nil, nil, nil), "stored material must be fixed-width digests")
	require.Error(t, insert(1, "browser-tab-binding-migration-session", validDigest, validDigest, validDigest, "pending", leaseExpiresAt, bindingExpiresAt, nil, nil, nil, nil, nil), "lease state must stay closed")
	require.Error(t, insert(1, "browser-tab-binding-migration-session", validDigest, validDigest, validDigest, "live", bindingExpiresAt, leaseExpiresAt, nil, nil, nil, nil, nil), "binding expiry must outlive its document lease")
	pinnedSourceID := uuid.NewString()
	pinnedCheckoutID := uuid.NewString()
	pinnedViewID := uuid.NewString()
	pinnedAnalysisProfileID := uuid.NewString()
	require.Error(t, insert(1, "browser-tab-binding-migration-session", validDigest, validDigest, validDigest, "live", leaseExpiresAt, bindingExpiresAt, &pinnedSourceID, nil, nil, nil, nil), "pinned ContextRef must be complete")
	zeroGeneration := int64(0)
	require.Error(t, insert(1, "browser-tab-binding-migration-session", validDigest, validDigest, validDigest, "live", leaseExpiresAt, bindingExpiresAt, &pinnedSourceID, &pinnedCheckoutID, &pinnedViewID, &pinnedAnalysisProfileID, &zeroGeneration), "pinned ContextRef generation must stay positive")
}

func browserTabBindingMigrationColumns(t *testing.T, db *gormlib.DB) map[string]browserTabBindingMigrationColumn {
	t.Helper()
	var rows []browserTabBindingMigrationColumn
	require.NoError(t, db.Raw(`
		SELECT column_name, data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'browser_tab_bindings'
		ORDER BY ordinal_position
	`).Scan(&rows).Error)
	columns := make(map[string]browserTabBindingMigrationColumn, len(rows))
	for _, row := range rows {
		columns[row.Name] = row
	}
	return columns
}

func assertBrowserTabBindingMigrationConstraint(t *testing.T, db *gormlib.DB, name string, needles ...string) {
	t.Helper()
	var row browserTabBindingMigrationConstraint
	require.NoError(t, db.Raw(`
		SELECT constraint_row.conname, pg_get_constraintdef(constraint_row.oid) AS definition
		FROM pg_constraint AS constraint_row
		JOIN pg_class AS relation ON relation.oid = constraint_row.conrelid
		JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = current_schema()
			AND relation.relname = 'browser_tab_bindings'
			AND constraint_row.conname = ?
	`, name).Scan(&row).Error)
	require.Equal(t, name, row.Name, "migration 177 must retain named constraint %s", name)
	row.Definition = strings.ToLower(row.Definition)
	for _, needle := range needles {
		require.Containsf(t, row.Definition, strings.ToLower(needle), "constraint %s", name)
	}
}

func assertBrowserTabBindingMigrationApplied(t *testing.T, db *gormlib.DB, want int64) {
	t.Helper()
	var applied int64
	require.NoError(t, db.Table("migrations").Where("id = ?", browserTabBindingMigrationID).Count(&applied).Error)
	require.Equal(t, want, applied, "migration 177 registry entry")
}

func assertBrowserTabBindingMigrationPrerequisite(t *testing.T, db *gormlib.DB, migrationID string) {
	t.Helper()
	var applied int64
	require.NoError(t, db.Table("migrations").Where("id = ?", migrationID).Count(&applied).Error)
	require.Equalf(t, int64(1), applied, "upgrade fixture must retain prerequisite migration %s", migrationID)
}

func assertBrowserTabBindingMigrationTableAbsent(t *testing.T, db *gormlib.DB) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = 'browser_tab_bindings'
	`).Scan(&count).Error)
	require.Zero(t, count, "upgrade fixture must reproduce the migration 176 schema boundary")
}

func assertBrowserTabBindingMigrationRetained(t *testing.T, db *gormlib.DB, binding BrowserTabBinding) {
	t.Helper()
	var retained BrowserTabBinding
	require.NoError(t, db.Where("tab_binding_id = ?", binding.TabBindingID).First(&retained).Error)
	require.Equal(t, binding.TabBindingID, retained.TabBindingID)
	require.Equal(t, binding.SessionID, retained.SessionID)
	require.Equal(t, binding.DocumentProofDigest, retained.DocumentProofDigest)
}
