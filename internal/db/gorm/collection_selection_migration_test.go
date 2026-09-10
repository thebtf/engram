package gorm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"
)

const collectionSelectionMigrationID = "178_collection_selections"

type collectionSelectionMigrationColumn struct {
	Name     string `gorm:"column:column_name"`
	DataType string `gorm:"column:data_type"`
	Nullable string `gorm:"column:is_nullable"`
}

type collectionSelectionMigrationConstraint struct {
	Name       string `gorm:"column:conname"`
	Definition string `gorm:"column:definition"`
}

// TestCollectionSelectionMigration178FreshSchemaAndStoreCompatibility proves
// migration 178 owns the privacy-minimized selection schema used by the store.
func TestCollectionSelectionMigration178FreshSchemaAndStoreCompatibility(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	assertCollectionSelectionMigrationApplied(t, db, 1)
	assertCollectionSelectionMigrationSchema(t, db)
	assertCollectionSelectionMigrationConstraintFailures(t, db)

	scope := CollectionSelectionScope{
		SubjectUserID:      51,
		SessionID:          "collection-selection-migration-fresh-" + uuid.NewString(),
		Domain:             "rules",
		ContextFingerprint: collectionSelectionMigrationDigest("a"),
		AuthorizationEpoch: 3,
		CollectionVersion:  7,
	}
	expiresAt := time.Now().UTC().Add(15 * time.Minute)
	saved, err := NewCollectionSelectionStore(db).Save(context.Background(), scope, CollectionSelection{
		Kind:              CollectionSelectionFrozenFilter,
		Targets:           []CollectionSelectionTarget{{ID: "rule-1", ExpectedVersion: 2}},
		FilterFingerprint: collectionSelectionMigrationDigest("b"),
		ExcludedIDs:       []string{"rule-1"},
		ExpiresAt:         expiresAt,
	})
	require.NoError(t, err)
	require.NotEmpty(t, saved.Token)

	loaded, err := NewCollectionSelectionStore(db).Frozen(context.Background(), scope, saved.Token)
	require.NoError(t, err)
	require.Equal(t, saved.Token, loaded.Token)
	require.Equal(t, saved.FilterFingerprint, loaded.FilterFingerprint)
	require.Equal(t, saved.ExcludedIDs, loaded.ExcludedIDs)
}

// TestCollectionSelectionMigration178UpgradeRollbackAndReplay proves a schema
// at migration 177 upgrades additively and the retained selection replays safely.
func TestCollectionSelectionMigration178UpgradeRollbackAndReplay(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	legacyAudit := AuditLogEntry{
		Action: "collection-selection-migration-legacy-sentinel",
		Actor:  "collection-selection-migration-test",
		Reason: "must survive migration 178 upgrade, rollback, and replay",
	}
	require.NoError(t, db.Create(&legacyAudit).Error)

	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", collectionSelectionMigrationID).Error)
	require.NoError(t, db.Exec("DROP TABLE collection_selections").Error)
	assertCollectionSelectionMigrationApplied(t, db, 0)
	assertBrowserTabBindingMigrationPrerequisite(t, db, browserTabBindingMigrationID)
	assertCollectionSelectionMigrationTableAbsent(t, db)

	require.NoError(t, runMigrations(db), "a chain upgraded from migration 177 must register collection selections")
	assertCollectionSelectionMigrationApplied(t, db, 1)
	assertCollectionSelectionMigrationSchema(t, db)
	var retainedAudit AuditLogEntry
	require.NoError(t, db.Where("id = ?", legacyAudit.ID).First(&retainedAudit).Error)
	require.Equal(t, legacyAudit.Action, retainedAudit.Action)
	require.Equal(t, legacyAudit.Reason, retainedAudit.Reason)

	scope := CollectionSelectionScope{
		SubjectUserID:      52,
		SessionID:          "collection-selection-migration-replay-" + uuid.NewString(),
		Domain:             "rules",
		ContextFingerprint: collectionSelectionMigrationDigest("c"),
		AuthorizationEpoch: 5,
		CollectionVersion:  9,
	}
	saved, err := NewCollectionSelectionStore(db).Save(context.Background(), scope, CollectionSelection{
		Kind:              CollectionSelectionFrozenFilter,
		Targets:           []CollectionSelectionTarget{{ID: "rule-2", ExpectedVersion: 4}},
		FilterFingerprint: collectionSelectionMigrationDigest("d"),
		ExpiresAt:         time.Now().UTC().Add(15 * time.Minute),
	})
	require.NoError(t, err)

	var historyBefore int64
	require.NoError(t, db.Table("migrations").Count(&historyBefore).Error)
	require.NoError(t, rollbackCollectionSelectionsMigration178(db), "binary rollback must retain additive selection state")
	var historyAfter int64
	require.NoError(t, db.Table("migrations").Count(&historyAfter).Error)
	require.Equal(t, historyBefore, historyAfter, "the rollback boundary must retain migration history")
	assertCollectionSelectionMigrationApplied(t, db, 1)
	assertCollectionSelectionMigrationRetained(t, db, scope, saved)

	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", collectionSelectionMigrationID).Error)
	require.NoError(t, runMigrations(db), "marker-only replay must retain selections and restore migration history")
	assertCollectionSelectionMigrationApplied(t, db, 1)
	assertCollectionSelectionMigrationRetained(t, db, scope, saved)
}

func assertCollectionSelectionMigrationSchema(t *testing.T, db *gormlib.DB) {
	t.Helper()
	columns := collectionSelectionMigrationColumns(t, db)
	expected := map[string]collectionSelectionMigrationColumn{
		"selection_id":            {DataType: "uuid", Nullable: "NO"},
		"subject_user_id":         {DataType: "bigint", Nullable: "NO"},
		"session_id":              {DataType: "text", Nullable: "NO"},
		"domain":                  {DataType: "text", Nullable: "NO"},
		"kind":                    {DataType: "text", Nullable: "NO"},
		"selection_version":       {DataType: "bigint", Nullable: "NO"},
		"context_fingerprint":     {DataType: "text", Nullable: "NO"},
		"authorization_epoch":     {DataType: "bigint", Nullable: "NO"},
		"collection_version":      {DataType: "bigint", Nullable: "NO"},
		"filter_fingerprint":      {DataType: "text", Nullable: "YES"},
		"page_cursor":             {DataType: "text", Nullable: "YES"},
		"targets_json":            {DataType: "jsonb", Nullable: "NO"},
		"excluded_ids_json":       {DataType: "jsonb", Nullable: "NO"},
		"selection_token":         {DataType: "uuid", Nullable: "YES"},
		"frozen_expires_at":       {DataType: "timestamp with time zone", Nullable: "YES"},
		"reconfirmation_required": {DataType: "boolean", Nullable: "NO"},
		"reconfirmation_reason":   {DataType: "text", Nullable: "YES"},
		"created_at":              {DataType: "timestamp with time zone", Nullable: "NO"},
		"updated_at":              {DataType: "timestamp with time zone", Nullable: "NO"},
	}
	require.Equal(t, expectedColumnNames(expected), expectedColumnNames(columns), "migration 178 must persist exactly its privacy-minimized selection projection")
	for name, want := range expected {
		actual := columns[name]
		require.Equalf(t, want.DataType, actual.DataType, "collection_selections.%s type", name)
		require.Equalf(t, want.Nullable, actual.Nullable, "collection_selections.%s nullability", name)
	}
	for _, forbidden := range []string{
		"request", "request_body", "query", "raw_query", "filter", "raw_filter", "action", "authority", "secret", "credential", "authorized_ids",
	} {
		require.NotContainsf(t, columns, forbidden, "migration 178 must not persist %q", forbidden)
	}

	assertCollectionSelectionMigrationConstraint(t, db, "collection_selections_subject_user_id_chk", "subject_user_id", "> 0")
	assertCollectionSelectionMigrationConstraint(t, db, "collection_selections_session_id_chk", "btrim(session_id)", "octet_length(session_id) <= 256")
	assertCollectionSelectionMigrationConstraint(t, db, "collection_selections_domain_chk", "domain ~ '^[a-z][a-z0-9_-]{0,63}$'")
	assertCollectionSelectionMigrationConstraint(t, db, "collection_selections_kind_chk", "none", "explicit", "page", "frozen_filter")
	assertCollectionSelectionMigrationConstraint(t, db, "collection_selections_selection_version_chk", "selection_version > 0")
	assertCollectionSelectionMigrationConstraint(t, db, "collection_selections_context_fingerprint_chk", "^sha256:[0-9a-f]{64}$")
	assertCollectionSelectionMigrationConstraint(t, db, "collection_selections_authorization_epoch_chk", "authorization_epoch > 0")
	assertCollectionSelectionMigrationConstraint(t, db, "collection_selections_collection_version_chk", "collection_version > 0")
	assertCollectionSelectionMigrationConstraint(t, db, "collection_selections_filter_fingerprint_chk", "^sha256:[0-9a-f]{64}$")
	assertCollectionSelectionMigrationConstraint(t, db, "collection_selections_page_cursor_chk", "octet_length(page_cursor) <= 512")
	assertCollectionSelectionMigrationConstraint(t, db, "collection_selections_json_shape_chk", "jsonb_typeof(targets_json)", "jsonb_typeof(excluded_ids_json)", "null")
	assertCollectionSelectionMigrationConstraint(t, db, "collection_selections_selection_shape_chk", "frozen_filter", "selection_token", "frozen_expires_at")
	assertCollectionSelectionMigrationConstraint(t, db, "collection_selections_reconfirmation_chk", "filter_changed", "context_changed", "grant_changed", "collection_changed", "expired")

	assertCollectionSelectionMigrationIndex(t, db, "idx_collection_selection_scope", "unique", "subject_user_id", "session_id", "domain")
	assertCollectionSelectionMigrationIndex(t, db, "idx_collection_selection_token", "unique", "selection_token")
}

func assertCollectionSelectionMigrationConstraintFailures(t *testing.T, db *gormlib.DB) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	validContext := collectionSelectionMigrationDigest("e")
	validFilter := collectionSelectionMigrationDigest("f")
	validToken := uuid.NewString()
	otherValidToken := uuid.NewString()
	validReason := string(CollectionSelectionReconfirmFilterChanged)
	insert := func(subjectUserID int64, kind, contextFingerprint string, filterFingerprint, pageCursor, token *string, frozenExpiresAt *time.Time, targets, exclusions string, reconfirmationRequired bool, reconfirmationReason *string) error {
		return db.Exec(`
			INSERT INTO collection_selections (
				selection_id, subject_user_id, session_id, domain, kind, selection_version,
				context_fingerprint, authorization_epoch, collection_version,
				filter_fingerprint, page_cursor, targets_json, excluded_ids_json,
				selection_token, frozen_expires_at, reconfirmation_required, reconfirmation_reason,
				created_at, updated_at
			) VALUES (?, ?, ?, 'rules', ?, 1, ?, 1, 1, ?, ?, ?::jsonb, ?::jsonb, ?, ?, ?, ?, ?, ?)
		`, uuid.NewString(), subjectUserID, "collection-selection-migration-constraint-"+uuid.NewString(), kind, contextFingerprint, filterFingerprint, pageCursor, targets, exclusions, token, frozenExpiresAt, reconfirmationRequired, reconfirmationReason, now, now).Error
	}
	frozenExpiresAt := now.Add(time.Hour)
	require.NoError(t, insert(1, "frozen_filter", validContext, &validFilter, nil, &validToken, &frozenExpiresAt, `[{"id":"rule-1","expected_version":1}]`, `[]`, false, nil))
	require.Error(t, insert(0, "frozen_filter", validContext, &validFilter, nil, &validToken, &frozenExpiresAt, `[{"id":"rule-2","expected_version":1}]`, `[]`, false, nil), "selection owner must stay positive")
	require.Error(t, insert(1, "unknown", validContext, &validFilter, nil, &validToken, &frozenExpiresAt, `[{"id":"rule-3","expected_version":1}]`, `[]`, false, nil), "selection kind must stay closed")
	require.Error(t, insert(1, "frozen_filter", "sha256:"+strings.Repeat("g", 64), &validFilter, nil, &validToken, &frozenExpiresAt, `[{"id":"rule-4","expected_version":1}]`, `[]`, false, nil), "context binding must stay normalized")
	require.Error(t, insert(1, "frozen_filter", validContext, nil, nil, &validToken, &frozenExpiresAt, `[{"id":"rule-5","expected_version":1}]`, `[]`, false, nil), "frozen selections require a normalized filter fingerprint")
	require.Error(t, insert(1, "frozen_filter", validContext, &validFilter, nil, &validToken, &frozenExpiresAt, `{}`, `[]`, false, nil), "selection membership must remain a JSON array")
	require.Error(t, insert(1, "none", validContext, nil, nil, nil, nil, `[{"id":"rule-6"}]`, `[]`, false, nil), "none selections must carry no target IDs")
	require.Error(t, insert(1, "frozen_filter", validContext, &validFilter, nil, &validToken, &frozenExpiresAt, `[{"id":"rule-7","expected_version":1}]`, `[]`, true, nil), "reconfirmation status requires a closed reason")
	require.NoError(t, insert(1, "frozen_filter", validContext, &validFilter, nil, &otherValidToken, &frozenExpiresAt, `[{"id":"rule-8","expected_version":1}]`, `[]`, true, &validReason))
}

func collectionSelectionMigrationColumns(t *testing.T, db *gormlib.DB) map[string]collectionSelectionMigrationColumn {
	t.Helper()
	var rows []collectionSelectionMigrationColumn
	require.NoError(t, db.Raw(`
		SELECT column_name, data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'collection_selections'
		ORDER BY ordinal_position
	`).Scan(&rows).Error)
	columns := make(map[string]collectionSelectionMigrationColumn, len(rows))
	for _, row := range rows {
		columns[row.Name] = row
	}
	return columns
}

func expectedColumnNames[T any](columns map[string]T) map[string]struct{} {
	names := make(map[string]struct{}, len(columns))
	for name := range columns {
		names[name] = struct{}{}
	}
	return names
}

func assertCollectionSelectionMigrationConstraint(t *testing.T, db *gormlib.DB, name string, needles ...string) {
	t.Helper()
	var row collectionSelectionMigrationConstraint
	require.NoError(t, db.Raw(`
		SELECT constraint_row.conname, pg_get_constraintdef(constraint_row.oid) AS definition
		FROM pg_constraint AS constraint_row
		JOIN pg_class AS relation ON relation.oid = constraint_row.conrelid
		JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = current_schema()
			AND relation.relname = 'collection_selections'
			AND constraint_row.conname = ?
	`, name).Scan(&row).Error)
	require.Equal(t, name, row.Name, "migration 178 must retain named constraint %s", name)
	row.Definition = strings.ToLower(row.Definition)
	for _, needle := range needles {
		require.Containsf(t, row.Definition, strings.ToLower(needle), "constraint %s", name)
	}
}

func assertCollectionSelectionMigrationIndex(t *testing.T, db *gormlib.DB, name string, needles ...string) {
	t.Helper()
	var definition string
	require.NoError(t, db.Raw(`
		SELECT indexdef
		FROM pg_indexes
		WHERE schemaname = current_schema()
			AND tablename = 'collection_selections'
			AND indexname = ?
	`, name).Scan(&definition).Error)
	definition = strings.ToLower(definition)
	for _, needle := range needles {
		require.Containsf(t, definition, strings.ToLower(needle), "index %s", name)
	}
}

func assertCollectionSelectionMigrationApplied(t *testing.T, db *gormlib.DB, want int64) {
	t.Helper()
	var applied int64
	require.NoError(t, db.Table("migrations").Where("id = ?", collectionSelectionMigrationID).Count(&applied).Error)
	require.Equal(t, want, applied, "migration 178 registry entry")
}

func assertCollectionSelectionMigrationTableAbsent(t *testing.T, db *gormlib.DB) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = 'collection_selections'
	`).Scan(&count).Error)
	require.Zero(t, count, "upgrade fixture must reproduce the migration 177 schema boundary")
}

func assertCollectionSelectionMigrationRetained(t *testing.T, db *gormlib.DB, scope CollectionSelectionScope, want CollectionSelection) {
	t.Helper()
	loaded, err := NewCollectionSelectionStore(db).Frozen(context.Background(), scope, want.Token)
	require.NoError(t, err)
	require.Equal(t, want.Token, loaded.Token)
	require.Equal(t, want.Targets, loaded.Targets)
	require.Equal(t, want.FilterFingerprint, loaded.FilterFingerprint)
}

func collectionSelectionMigrationDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
