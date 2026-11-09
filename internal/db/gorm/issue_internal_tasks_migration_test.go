package gorm

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	issues "github.com/thebtf/engram/internal/issues/contract"
	gormlib "gorm.io/gorm"
)

func TestIssueInternalTasksHistoricalMigration(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	// Reconstruct the real pre-feature column projection in this private schema.
	// Future additive columns are discovered rather than required by this RED.
	legacy := map[string][]string{
		"issues":         {"id", "title", "body", "status", "priority", "type", "source_project", "target_project", "source_agent", "created_by_session", "creator_keycard_id", "labels", "acknowledged_at", "resolved_at", "reopened_at", "closed_at", "created_at", "updated_at"},
		"issue_comments": {"id", "issue_id", "author_project", "author_agent", "body", "created_at"},
	}
	for _, table := range []string{"issues", "issue_comments"} {
		require.NoError(t, db.Exec(`DROP TRIGGER engram_issue_insert_actor_required ON `+pq.QuoteIdentifier(table)).Error)
	}
	for _, table := range []string{"issue_comments", "issues"} {
		var extra []string
		require.NoError(t, db.Raw(`SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ? AND column_name NOT IN ?`, table, legacy[table]).Scan(&extra).Error)
		for _, column := range extra {
			require.NoError(t, db.Exec(`ALTER TABLE `+pq.QuoteIdentifier(table)+` DROP COLUMN `+pq.QuoteIdentifier(column)).Error)
		}
	}
	require.NoError(t, db.Exec(`DELETE FROM migrations WHERE id > '189_issue_project_write_scope'`).Error)
	const source = "51300000-0000-4000-8000-000000000099"
	require.NoError(t, db.Create(&Project{ID: source}).Error)
	require.NoError(t, db.Exec(`INSERT INTO issues (id,title,body,status,priority,type,source_project,target_project,source_agent,created_by_session,creator_keycard_id,labels,created_at,updated_at)
		VALUES (7513,'Historical synthetic task','Exact historical body','acknowledged','high','bug',?,?,'historical-agent','historical-session','synthetic-historical-actor','["retained"]','2026-01-02T03:04:05Z','2026-01-02T03:04:06Z')`, source, source).Error)
	require.NoError(t, db.Exec(`INSERT INTO issue_comments (id,issue_id,author_project,author_agent,body,created_at)
		VALUES (9513,7513,?,'historical-comment-agent','Exact historical comment','2026-01-02T03:04:07Z')`, source).Error)
	snapshot := func(table string) map[string]json.RawMessage {
		t.Helper()
		var raw string
		require.NoError(t, db.Raw(`SELECT to_jsonb(t)::text FROM `+pq.QuoteIdentifier(table)+` t`).Row().Scan(&raw))
		var result map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(raw), &result))
		return result
	}
	beforeIssue, beforeComment := snapshot("issues"), snapshot("issue_comments")
	require.NoError(t, runMigrations(db), "ordinary startup must add the baseline to genuinely pre-feature rows")
	for table, before := range map[string]map[string]json.RawMessage{"issues": beforeIssue, "issue_comments": beforeComment} {
		after := snapshot(table)
		for field, value := range before {
			assert.JSONEq(t, string(value), string(after[field]), "%s.%s must survive the additive upgrade", table, field)
		}
		assert.JSONEq(t, `{}`, string(after["metadata"]), "historical %s metadata must have an honest empty baseline", table)
		assert.JSONEq(t, `"unknown"`, string(after["actor_kind"]))
		assert.JSONEq(t, `null`, string(after["actor_reference"]))
		if table == "issues" {
			assert.JSONEq(t, `1`, string(after["revision"]), "historical issue revision must begin at one")
			assert.JSONEq(t, `null`, string(after["owner_label"]))
			assert.JSONEq(t, `null`, string(after["recipient_label"]))
		}
	}
	var changeCount, receiptCount int64
	require.NoError(t, db.Table("issue_changes").Count(&changeCount).Error)
	require.NoError(t, db.Table("issue_create_receipts").Count(&receiptCount).Error)
	assert.EqualValues(t, 1, changeCount, "exactly one historical baseline, not reconstructed edit history")
	assert.Zero(t, receiptCount, "migration must not invent request keys or receipts for historical cards")
	baseline := snapshot("issue_changes")
	for field, expected := range map[string]string{
		"issue_id": "7513", "revision": "1", "previous_revision": "null", "action": `"baseline"`,
		"actor_kind": `"unknown"`, "actor_reference": "null", "changed_fields": `[]`, "changes": `{}`,
		"attribution_source_project": `"` + source + `"`, "attribution_source_agent": `"historical-agent"`,
		"attribution_session": `"historical-session"`,
	} {
		assert.JSONEq(t, expected, string(baseline[field]), "truthful historical baseline: %s", field)
	}
	upgradedIssue, upgradedComment := snapshot("issues"), snapshot("issue_comments")
	require.NoError(t, runMigrations(db), "startup replay must not invent another baseline or rewrite history")
	for table, before := range map[string]map[string]json.RawMessage{"issues": upgradedIssue, "issue_comments": upgradedComment} {
		after := snapshot(table)
		assert.Equal(t, before, after, "%s must remain byte-equivalent after startup replay", table)
	}
	require.NoError(t, issueInternalTasksMigration193().Migrate(db), "direct migration replay must also preserve its baseline")
	assert.Equal(t, baseline, snapshot("issue_changes"))
	require.NoError(t, db.Table("issue_changes").Count(&changeCount).Error)
	assert.EqualValues(t, 1, changeCount)
	assert.Equal(t, upgradedIssue, snapshot("issues"))
	assert.Equal(t, upgradedComment, snapshot("issue_comments"))
}

func TestIssueTargetSessionGuardMigration196Upgrade(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN is required for the isolated migration fixture")
	}
	schema := "issue196_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	db := openMigratedTestDBInSchemaTo(t, dsn, schema, "engram_issue196", "195_memory_links")
	const source = "51300000-0000-4000-8000-000000000196"
	require.NoError(t, db.Create(&Project{ID: source}).Error)
	actor := issueSelectionTestActor{keycard: "migration196"}
	store := NewIssueStore(db)
	requestKey := "synthetic-196-retained"
	created, err := store.CreateTaskIssue(t.Context(), actor, issues.Scope{SourceProject: source}, issues.CreateCommand{Title: "retained guard", TargetProject: source, Type: "task", Priority: "medium", Labels: []string{}, Metadata: json.RawMessage(`{}`), RequestKey: &requestKey})
	require.NoError(t, err)
	comment := "retained comment"
	_, err = store.MutateTaskIssue(t.Context(), actor, issues.Scope{SourceProject: source}, created.ID, issues.MutationCommand{Comment: &comment, CommentMetadata: json.RawMessage(`{}`)})
	require.NoError(t, err)
	ledger := func() []string {
		var ids []string
		require.NoError(t, db.Table("migrations").Order("id").Pluck("id", &ids).Error)
		return ids
	}
	snapshot := func() []string {
		var rows []string
		for _, table := range []string{"issues", "issue_comments", "issue_changes", "issue_create_receipts"} {
			var value string
			require.NoError(t, db.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+pq.QuoteIdentifier(table)+` t`).Scan(&value).Error)
			rows = append(rows, value)
		}
		return rows
	}
	beforeLedger, beforeRows := ledger(), snapshot()
	var receiptCount int64
	require.NoError(t, db.Table("issue_create_receipts").Where("issue_id=?", created.ID).Count(&receiptCount).Error)
	require.EqualValues(t, 1, receiptCount)
	require.Contains(t, beforeLedger, "193_issue_internal_tasks")
	require.Contains(t, beforeLedger, "195_memory_links")
	require.NotContains(t, beforeLedger, "196_issue_target_session_revision_guard")
	rollback := errors.New("rollback probe")
	err = db.Transaction(func(tx *gormlib.DB) error {
		if err := tx.Exec(`UPDATE issues SET target_session='old-unguarded' WHERE id=?`, created.ID).Error; err != nil {
			return err
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback, "retained 193 must expose the missing field guard")
	require.Equal(t, beforeRows, snapshot())
	require.NoError(t, runMigrations(db))
	afterLedger := ledger()
	require.Len(t, afterLedger, len(beforeLedger)+1)
	require.Equal(t, append(beforeLedger, "196_issue_target_session_revision_guard"), afterLedger)
	require.Equal(t, beforeRows, snapshot())
	inserted := false
	err = db.Transaction(func(tx *gormlib.DB) error {
		if err := tx.Exec(`INSERT INTO issue_comments(issue_id,author_project,body,actor_kind,actor_reference) VALUES (?,?,'companion','client','client:migration196')`, created.ID, source).Error; err != nil {
			return err
		}
		inserted = true
		return tx.Exec(`UPDATE issues SET target_session='forbidden' WHERE id=?`, created.ID).Error
	})
	var pgErr *pgconn.PgError
	require.True(t, inserted)
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23514", pgErr.Code)
	require.Equal(t, beforeRows, snapshot())
	revision := "1"
	result, err := store.MutateTaskIssue(t.Context(), actor, issues.Scope{SourceProject: source}, created.ID, issues.MutationCommand{ExpectedRevision: &revision, TargetSession: json.RawMessage(`"accepted"`)})
	require.NoError(t, err)
	require.Equal(t, "2", result.Revision)
	var changes int64
	require.NoError(t, db.Table("issue_changes").Where("issue_id=? AND revision=2", created.ID).Count(&changes).Error)
	require.EqualValues(t, 1, changes)
	require.NoError(t, issueTargetSessionRevisionGuardMigration196().Migrate(db))
	require.NoError(t, runMigrations(db))
	require.Equal(t, afterLedger, ledger())
	require.ErrorContains(t, issueTargetSessionRevisionGuardMigration196().Rollback(db), "rollback refused")
}

func TestIssueTargetSessionGuardMigration196RefusesUntrustedGuards(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN is required for the isolated migration fixture")
	}
	for _, test := range []struct{ name, ddl string }{
		{"missing old", `DROP TRIGGER engram_issue_revision_monotonic ON issues`},
		{"disabled old", `ALTER TABLE issues DISABLE TRIGGER engram_issue_revision_monotonic`},
		{"modified old", `CREATE OR REPLACE FUNCTION engram_issue_revision_monotonic() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`},
		{"conflicting new", `CREATE FUNCTION engram_issue_target_session_revision_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema := "issue196_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			db := openMigratedTestDBInSchemaTo(t, dsn, schema, "engram_issue196", "195_memory_links")
			require.NoError(t, db.Exec(test.ddl).Error)
			var before, after int64
			require.NoError(t, db.Table("migrations").Count(&before).Error)
			require.Error(t, runMigrations(db))
			require.NoError(t, db.Table("migrations").Count(&after).Error)
			require.Equal(t, before, after)
			var installed int64
			require.NoError(t, db.Table("migrations").Where("id=?", "196_issue_target_session_revision_guard").Count(&installed).Error)
			require.Zero(t, installed)
		})
	}
}
