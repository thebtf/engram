package gorm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	ucidomain "github.com/thebtf/engram/internal/uci"
	gormlib "gorm.io/gorm"
)

const (
	uciIndexIntentMigrationID         = "179_uci_index_intents"
	uciIndexIntentDeliveryMigrationID = "180_uci_index_intent_delivery"
)

type uciIndexIntentMigrationColumn struct {
	Name     string `gorm:"column:column_name"`
	DataType string `gorm:"column:data_type"`
	Nullable string `gorm:"column:is_nullable"`
}

type uciIndexIntentMigrationConstraint struct {
	Name       string `gorm:"column:conname"`
	Definition string `gorm:"column:definition"`
}

// TestUCIIndexIntentMigration179FreshSchemaAndStoreLifecycle proves migrations
// 179 and 180 own the durable intent schema and preserve the store lifecycle.
func TestUCIIndexIntentMigration179FreshSchemaAndStoreLifecycle(t *testing.T) {
	fixture := openUCIProjectionMigrationFixture(t)
	db := fixture.db
	assertUCIIndexIntentMigrationApplied(t, db, 1)
	assertUCIIndexIntentDeliveryMigrationApplied(t, db, 1)
	assertUCIIndexIntentMigrationSchema(t, db)
	assertUCIIndexIntentMigrationConstraintFailures(t, fixture)

	store := NewUCIIndexIntentStore(db)
	input, previous, result := newUCIIndexIntentMigrationInput(fixture)
	submitted, err := store.SubmitIndexIntent(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentSubmitted, submitted.State)
	require.Equal(t, previous, *submitted.PreviousView)

	replayed, err := store.SubmitIndexIntent(context.Background(), input.Clone())
	require.NoError(t, err)
	require.Equal(t, submitted.ID, replayed.ID, "the request reference must bind idempotently")
	changed := input.Clone()
	changed.Kind = ucidomain.IndexIntentReconcile
	_, err = store.SubmitIndexIntent(context.Background(), changed)
	require.ErrorIs(t, err, ucidomain.ErrIndexIntentBindingMismatch)

	_, err = store.QueueIndexIntent(context.Background(), submitted.ID)
	require.NoError(t, err)
	claim, err := store.AcknowledgeIndexIntent(context.Background(), submitted.ID, "index-intent-migration-owner")
	require.NoError(t, err)
	require.Equal(t, int64(1), claim.Epoch)
	require.False(t, claim.AcknowledgedAt.IsZero())
	_, err = store.StartIndexIntent(context.Background(), claim)
	require.NoError(t, err)
	completed, err := store.CompleteIndexIntent(context.Background(), claim, result, indexIntentReadableViews{result.ViewID: true})
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexIntentCompleted, completed.State)
	require.Equal(t, result, *completed.ResultView)
}

// TestUCIIndexIntentMigration179UpgradeRollbackAndReplay proves migrations 179
// and 180 upgrade the migration-178 boundary additively and retain durable intents.
func TestUCIIndexIntentMigration179UpgradeRollbackAndReplay(t *testing.T) {
	fixture := openUCIProjectionMigrationFixture(t)
	db := fixture.db
	legacyAudit := AuditLogEntry{
		Action: "uci-index-intent-migration-legacy-sentinel",
		Actor:  "uci-index-intent-migration-test",
		Reason: "must survive migration 179 upgrade, rollback, and replay",
	}
	require.NoError(t, db.Create(&legacyAudit).Error)

	var sourceBefore UCISource
	require.NoError(t, db.Where("source_id = ?", fixture.source.SourceID).First(&sourceBefore).Error)
	var checkoutBefore UCICheckout
	require.NoError(t, db.Where("checkout_id = ?", fixture.checkout.CheckoutID).First(&checkoutBefore).Error)
	var viewBefore UCIView
	require.NoError(t, db.Where("view_id = ?", fixture.view.ViewID).First(&viewBefore).Error)
	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id IN ?", []string{uciIndexIntentMigrationID, uciIndexIntentDeliveryMigrationID}).Error)
	require.NoError(t, db.Exec("DROP TABLE uci_index_intent_receipts").Error)
	require.NoError(t, db.Exec("DROP TABLE uci_index_intents").Error)
	require.NoError(t, db.Exec("ALTER TABLE ci_jobs DROP COLUMN index_intent_id").Error)
	assertUCIIndexIntentMigrationApplied(t, db, 0)
	assertUCIIndexIntentDeliveryMigrationApplied(t, db, 0)
	var prerequisite int64
	require.NoError(t, db.Table("migrations").Where("id = ?", "178_collection_selections").Count(&prerequisite).Error)
	require.Equal(t, int64(1), prerequisite, "upgrade fixture must retain migration 178")
	assertUCIIndexIntentMigrationTableAbsent(t, db)

	require.NoError(t, runMigrations(db), "a chain upgraded from migration 178 must register index intent delivery")
	assertUCIIndexIntentMigrationApplied(t, db, 1)
	assertUCIIndexIntentDeliveryMigrationApplied(t, db, 1)
	assertUCIIndexIntentMigrationSchema(t, db)
	var retainedAudit AuditLogEntry
	require.NoError(t, db.Where("id = ?", legacyAudit.ID).First(&retainedAudit).Error)
	require.Equal(t, legacyAudit.Action, retainedAudit.Action)
	require.Equal(t, legacyAudit.Reason, retainedAudit.Reason)
	var retainedSource UCISource
	require.NoError(t, db.Where("source_id = ?", sourceBefore.SourceID).First(&retainedSource).Error)
	require.Equal(t, sourceBefore, retainedSource)
	var retainedCheckout UCICheckout
	require.NoError(t, db.Where("checkout_id = ?", checkoutBefore.CheckoutID).First(&retainedCheckout).Error)
	require.Equal(t, checkoutBefore, retainedCheckout)
	var retainedView UCIView
	require.NoError(t, db.Where("view_id = ?", viewBefore.ViewID).First(&retainedView).Error)
	require.Equal(t, viewBefore, retainedView)

	store := NewUCIIndexIntentStore(db)
	input, _, _ := newUCIIndexIntentMigrationInput(fixture)
	saved, err := store.SubmitIndexIntent(context.Background(), input)
	require.NoError(t, err)

	var historyBefore int64
	require.NoError(t, db.Table("migrations").Count(&historyBefore).Error)
	require.NoError(t, rollbackUCIIndexIntentsMigration179(db), "binary rollback must retain durable index intents")
	var historyAfter int64
	require.NoError(t, db.Table("migrations").Count(&historyAfter).Error)
	require.Equal(t, historyBefore, historyAfter, "the rollback boundary must retain migration history")
	assertUCIIndexIntentMigrationApplied(t, db, 1)
	retained, err := store.GetIndexIntent(context.Background(), saved.ID)
	require.NoError(t, err)
	require.Equal(t, saved.ID, retained.ID)

	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", uciIndexIntentMigrationID).Error)
	require.NoError(t, runMigrations(db), "marker-only replay must retain intents and restore migration history")
	assertUCIIndexIntentMigrationApplied(t, db, 1)
	replayed, err := store.GetIndexIntent(context.Background(), saved.ID)
	require.NoError(t, err)
	require.Equal(t, saved.ID, replayed.ID)
}

func TestUCIIndexIntentMigration180BackfillsLegacyClaimsAsExpired(t *testing.T) {
	fixture := openUCIProjectionMigrationFixture(t)
	db := fixture.db
	legacy := newUCIIndexIntentMigrationRow(fixture)
	acknowledgedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	legacy.CreatedAt = acknowledgedAt.Add(-time.Minute)
	legacy.UpdatedAt = acknowledgedAt
	legacy.State = string(ucidomain.IndexIntentAcknowledged)
	legacy.Attempt = 1
	legacy.AcknowledgedOwner = indexIntentString("legacy-index-owner")
	legacy.AcknowledgementEpoch = 1
	legacy.AcknowledgedAt = indexIntentTime(acknowledgedAt)
	legacy.ClaimExpiresAt = indexIntentTime(acknowledgedAt.Add(time.Minute))
	require.NoError(t, db.Create(&legacy).Error)

	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", uciIndexIntentDeliveryMigrationID).Error)
	require.NoError(t, db.Exec("DROP TABLE uci_index_intent_receipts").Error)
	require.NoError(t, db.Exec("ALTER TABLE uci_index_intents DROP CONSTRAINT uci_index_intents_checkout_source_incarnation_fkey").Error)
	require.NoError(t, db.Exec("ALTER TABLE uci_index_intents DROP CONSTRAINT uci_index_intents_profile_fkey").Error)
	require.NoError(t, db.Exec("ALTER TABLE uci_index_intents DROP CONSTRAINT uci_index_intents_acknowledgement_shape_chk").Error)
	require.NoError(t, db.Exec("ALTER TABLE uci_index_intents DROP COLUMN claim_expires_at").Error)
	require.NoError(t, db.Exec("ALTER TABLE uci_index_intents DROP COLUMN publication_build_id").Error)
	require.NoError(t, db.Exec("ALTER TABLE ci_jobs DROP COLUMN index_intent_id").Error)
	assertUCIIndexIntentDeliveryMigrationApplied(t, db, 0)

	require.NoError(t, runMigrations(db), "migration 180 must upgrade pre-delivery acknowledged records")
	assertUCIIndexIntentDeliveryMigrationApplied(t, db, 1)
	assertUCIIndexIntentMigrationSchema(t, db)
	var restored indexIntentRow
	require.NoError(t, db.Where("intent_id = ?", legacy.IntentID).First(&restored).Error)
	require.NotNil(t, restored.AcknowledgedAt)
	require.NotNil(t, restored.ClaimExpiresAt)
	require.True(t, restored.ClaimExpiresAt.After(*restored.AcknowledgedAt), "backfill must satisfy the fenced claim shape without granting a fresh lease")
	require.True(t, restored.ClaimExpiresAt.Before(time.Now().UTC()), "legacy backfill must not grant a new execution lease")
}

func assertUCIIndexIntentMigrationSchema(t *testing.T, db *gormlib.DB) {
	t.Helper()
	columns := uciIndexIntentMigrationColumns(t, db)
	expected := map[string]uciIndexIntentMigrationColumn{
		"intent_id":             {DataType: "uuid", Nullable: "NO"},
		"request_ref":           {DataType: "text", Nullable: "NO"},
		"kind":                  {DataType: "text", Nullable: "NO"},
		"source_id":             {DataType: "uuid", Nullable: "NO"},
		"checkout_id":           {DataType: "uuid", Nullable: "NO"},
		"incarnation_id":        {DataType: "uuid", Nullable: "NO"},
		"profile_id":            {DataType: "uuid", Nullable: "NO"},
		"previous_space_id":     {DataType: "uuid", Nullable: "YES"},
		"previous_view_id":      {DataType: "uuid", Nullable: "YES"},
		"previous_generation":   {DataType: "bigint", Nullable: "YES"},
		"state":                 {DataType: "text", Nullable: "NO"},
		"attempt":               {DataType: "integer", Nullable: "NO"},
		"acknowledged_owner":    {DataType: "text", Nullable: "YES"},
		"acknowledgement_epoch": {DataType: "bigint", Nullable: "NO"},
		"acknowledged_at":       {DataType: "timestamp with time zone", Nullable: "YES"},
		"claim_expires_at":      {DataType: "timestamp with time zone", Nullable: "YES"},
		"publication_build_id":  {DataType: "uuid", Nullable: "YES"},
		"result_space_id":       {DataType: "uuid", Nullable: "YES"},
		"result_view_id":        {DataType: "uuid", Nullable: "YES"},
		"result_generation":     {DataType: "bigint", Nullable: "YES"},
		"created_at":            {DataType: "timestamp with time zone", Nullable: "NO"},
		"updated_at":            {DataType: "timestamp with time zone", Nullable: "NO"},
	}
	expectedNames := make(map[string]struct{}, len(expected))
	for name := range expected {
		expectedNames[name] = struct{}{}
	}
	actualNames := make(map[string]struct{}, len(columns))
	for name := range columns {
		actualNames[name] = struct{}{}
	}
	require.Equal(t, expectedNames, actualNames, "migrations 179 and 180 must persist exactly the durable intent row")
	for name, want := range expected {
		actual := columns[name]
		require.Equalf(t, want.DataType, actual.DataType, "uci_index_intents.%s type", name)
		require.Equalf(t, want.Nullable, actual.Nullable, "uci_index_intents.%s nullability", name)
	}
	for _, forbidden := range []string{
		"host", "path", "secret", "credential", "environment", "source_body", "provider", "transport", "job_lease",
	} {
		require.NotContainsf(t, columns, forbidden, "durable intent migrations must not persist %q", forbidden)
	}
	require.True(t, db.Migrator().HasTable(&indexIntentReceiptRow{}), "migration 180 must persist exact update receipts")
	require.True(t, db.Migrator().HasColumn(&UCIJob{}, "index_intent_id"), "migration 180 must link publication builds")

	assertUCIIndexIntentMigrationConstraint(t, db, "uci_index_intents_request_ref_chk", "btrim(request_ref)", "octet_length(request_ref) <= 256")
	assertUCIIndexIntentMigrationConstraint(t, db, "uci_index_intents_kind_chk", "reindex", "reconcile")
	assertUCIIndexIntentMigrationConstraint(t, db, "uci_index_intents_previous_view_shape_chk", "previous_view_id", "previous_generation")
	assertUCIIndexIntentMigrationConstraint(t, db, "uci_index_intents_state_chk", "submitted", "queued", "acknowledged", "running", "completed", "unavailable", "failed")
	assertUCIIndexIntentMigrationConstraint(t, db, "uci_index_intents_attempt_epoch_chk", "attempt >= 0", "acknowledgement_epoch = attempt")
	assertUCIIndexIntentMigrationConstraint(t, db, "uci_index_intents_acknowledgement_shape_chk", "acknowledged_owner", "octet_length(acknowledged_owner) <= 256", "acknowledged_at", "claim_expires_at")
	assertUCIIndexIntentMigrationConstraint(t, db, "uci_index_intents_result_view_shape_chk", "result_view_id", "result_generation")
	assertUCIIndexIntentMigrationConstraint(t, db, "uci_index_intents_completed_result_chk", "completed", "result_view_id", "result_generation")
	assertUCIIndexIntentMigrationConstraint(t, db, "uci_index_intents_timestamps_chk", "updated_at >= created_at")
	assertUCIIndexIntentMigrationConstraint(t, db, "uci_index_intents_checkout_source_incarnation_fkey", "foreign key", "references ci_checkouts", "checkout_id", "source_id", "incarnation_id")
	assertUCIIndexIntentMigrationConstraint(t, db, "uci_index_intents_profile_fkey", "foreign key", "references ci_profiles", "profile_id")

	assertUCIIndexIntentMigrationIndex(t, db, "uci_index_intents_request_ref", "unique", "request_ref")
	assertUCIIndexIntentMigrationIndex(t, db, "idx_uci_index_intents_owner_state", "acknowledged_owner", "state", "updated_at", "where")
	assertUCIIndexIntentMigrationIndex(t, db, "idx_uci_index_intents_scope_status", "source_id", "checkout_id", "incarnation_id", "profile_id", "state", "updated_at")
	assertUCIIndexIntentMigrationIndex(t, db, "uci_index_intents_publication_build", "unique", "publication_build_id", "where")
}

func assertUCIIndexIntentMigrationConstraintFailures(t *testing.T, fixture *uciProjectionMigrationFixture) {
	t.Helper()
	valid := newUCIIndexIntentMigrationRow(fixture)
	require.NoError(t, fixture.db.Create(&valid).Error)

	invalid := newUCIIndexIntentMigrationRow(fixture)
	invalid.SourceID = uuid.NewString()
	require.Error(t, fixture.db.Create(&invalid).Error, "intent scope must match an existing checkout identity")

	invalid = newUCIIndexIntentMigrationRow(fixture)
	invalid.ProfileID = uuid.NewString()
	require.Error(t, fixture.db.Create(&invalid).Error, "intent profile must exist")

	invalid = newUCIIndexIntentMigrationRow(fixture)
	invalid.RequestRef = valid.RequestRef
	require.Error(t, fixture.db.Create(&invalid).Error, "request references must remain unique")

	invalid = newUCIIndexIntentMigrationRow(fixture)
	invalid.RequestRef = strings.Repeat("r", 257)
	require.Error(t, fixture.db.Create(&invalid).Error, "request references must be bounded opaque text")

	invalid = newUCIIndexIntentMigrationRow(fixture)
	invalid.Kind = "unknown"
	require.Error(t, fixture.db.Create(&invalid).Error, "intent kinds must stay closed")

	invalid = newUCIIndexIntentMigrationRow(fixture)
	invalid.PreviousGeneration = nil
	require.Error(t, fixture.db.Create(&invalid).Error, "previous View tuples must be complete")

	invalid = newUCIIndexIntentMigrationRow(fixture)
	invalid.ResultViewID = indexIntentString(fixture.stagingView.ViewID)
	require.Error(t, fixture.db.Create(&invalid).Error, "result View tuples must be complete")

	invalid = newUCIIndexIntentMigrationRow(fixture)
	invalid.State = string(ucidomain.IndexIntentAcknowledged)
	invalid.Attempt = 1
	invalid.AcknowledgementEpoch = 1
	require.Error(t, fixture.db.Create(&invalid).Error, "acknowledged states require complete owner metadata")

	invalid = newUCIIndexIntentMigrationRow(fixture)
	acknowledgedAt := invalid.CreatedAt
	invalid.State = string(ucidomain.IndexIntentAcknowledged)
	invalid.Attempt = 1
	invalid.AcknowledgementEpoch = 1
	invalid.AcknowledgedOwner = indexIntentString(strings.Repeat("o", 257))
	invalid.AcknowledgedAt = indexIntentTime(acknowledgedAt)
	require.Error(t, fixture.db.Create(&invalid).Error, "acknowledgement owners must be bounded opaque text")

	invalid = newUCIIndexIntentMigrationRow(fixture)
	invalid.State = string(ucidomain.IndexIntentCompleted)
	invalid.Attempt = 1
	invalid.AcknowledgementEpoch = 1
	invalid.AcknowledgedOwner = indexIntentString("index-intent-migration-owner")
	invalid.AcknowledgedAt = indexIntentTime(invalid.CreatedAt)
	require.Error(t, fixture.db.Create(&invalid).Error, "completed intents require a result View")

	invalid = newUCIIndexIntentMigrationRow(fixture)
	invalid.State = string(ucidomain.IndexIntentRunning)
	invalid.Attempt = 1
	invalid.AcknowledgementEpoch = 1
	invalid.AcknowledgedOwner = indexIntentString("index-intent-migration-owner")
	invalid.AcknowledgedAt = indexIntentTime(invalid.CreatedAt)
	invalid.ResultViewID = indexIntentString(fixture.stagingView.ViewID)
	invalid.ResultGeneration = indexIntentInt64(fixture.stagingView.Generation)
	require.Error(t, fixture.db.Create(&invalid).Error, "only completed intents may carry a result View")

	invalid = newUCIIndexIntentMigrationRow(fixture)
	invalid.UpdatedAt = invalid.CreatedAt.Add(-time.Second)
	require.Error(t, fixture.db.Create(&invalid).Error, "intent timestamps must be monotonic")
}

func newUCIIndexIntentMigrationInput(fixture *uciProjectionMigrationFixture) (ucidomain.IndexIntentInput, ucidomain.ContextRef, ucidomain.ContextRef) {
	previous := uciContextRefFromView(*fixture.view)
	result := uciContextRefFromView(*fixture.stagingView)
	return ucidomain.IndexIntentInput{
		RequestRef: "uci-index-intent-migration-request-" + uuid.NewString(),
		Kind:       ucidomain.IndexIntentReindex,
		Scope: ucidomain.IndexScope{
			SourceID:      fixture.source.SourceID,
			CheckoutID:    fixture.checkout.CheckoutID,
			IncarnationID: fixture.checkout.IncarnationID,
		},
		ProfileID:    fixture.profile.ProfileID,
		PreviousView: &previous,
	}, previous, result
}

func newUCIIndexIntentMigrationRow(fixture *uciProjectionMigrationFixture) indexIntentRow {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return indexIntentRow{
		IntentID:           uuid.NewString(),
		RequestRef:         "uci-index-intent-migration-row-" + uuid.NewString(),
		Kind:               string(ucidomain.IndexIntentReindex),
		SourceID:           fixture.source.SourceID,
		CheckoutID:         fixture.checkout.CheckoutID,
		IncarnationID:      fixture.checkout.IncarnationID,
		ProfileID:          fixture.profile.ProfileID,
		PreviousViewID:     indexIntentString(fixture.view.ViewID),
		PreviousGeneration: indexIntentInt64(fixture.view.Generation),
		State:              string(ucidomain.IndexIntentSubmitted),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
}

func uciIndexIntentMigrationColumns(t *testing.T, db *gormlib.DB) map[string]uciIndexIntentMigrationColumn {
	t.Helper()
	var rows []uciIndexIntentMigrationColumn
	require.NoError(t, db.Raw(`
		SELECT column_name, data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'uci_index_intents'
		ORDER BY ordinal_position
	`).Scan(&rows).Error)
	columns := make(map[string]uciIndexIntentMigrationColumn, len(rows))
	for _, row := range rows {
		columns[row.Name] = row
	}
	return columns
}

func assertUCIIndexIntentMigrationConstraint(t *testing.T, db *gormlib.DB, name string, needles ...string) {
	t.Helper()
	var row uciIndexIntentMigrationConstraint
	require.NoError(t, db.Raw(`
		SELECT constraint_row.conname, pg_get_constraintdef(constraint_row.oid) AS definition
		FROM pg_constraint AS constraint_row
		JOIN pg_class AS relation ON relation.oid = constraint_row.conrelid
		JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = current_schema()
			AND relation.relname = 'uci_index_intents'
			AND constraint_row.conname = ?
	`, name).Scan(&row).Error)
	require.Equal(t, name, row.Name, "migration 179 must retain named constraint %s", name)
	row.Definition = strings.ToLower(row.Definition)
	for _, needle := range needles {
		require.Containsf(t, row.Definition, strings.ToLower(needle), "constraint %s", name)
	}
}

func assertUCIIndexIntentMigrationIndex(t *testing.T, db *gormlib.DB, name string, needles ...string) {
	t.Helper()
	var definition string
	require.NoError(t, db.Raw(`
		SELECT indexdef
		FROM pg_indexes
		WHERE schemaname = current_schema()
			AND tablename = 'uci_index_intents'
			AND indexname = ?
	`, name).Scan(&definition).Error)
	definition = strings.ToLower(definition)
	for _, needle := range needles {
		require.Containsf(t, definition, strings.ToLower(needle), "index %s", name)
	}
}

func assertUCIIndexIntentMigrationApplied(t *testing.T, db *gormlib.DB, want int64) {
	t.Helper()
	var applied int64
	require.NoError(t, db.Table("migrations").Where("id = ?", uciIndexIntentMigrationID).Count(&applied).Error)
	require.Equal(t, want, applied, "migration 179 registry entry")
}

func assertUCIIndexIntentDeliveryMigrationApplied(t *testing.T, db *gormlib.DB, want int64) {
	t.Helper()
	var got int64
	require.NoError(t, db.Table("migrations").Where("id = ?", uciIndexIntentDeliveryMigrationID).Count(&got).Error)
	require.Equal(t, want, got, "migration 180 delivery history")
}

func assertUCIIndexIntentMigrationTableAbsent(t *testing.T, db *gormlib.DB) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = 'uci_index_intents'
	`).Scan(&count).Error)
	require.Zero(t, count, "upgrade fixture must reproduce the migration 178 schema boundary")
}
