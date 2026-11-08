package gorm

import (
	"encoding/json"
	"testing"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/google/uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"
)

func TestIssueInternalTasksLegacyConservation(t *testing.T) {
	template, templateSchema := openInterventionReceiptMigrationTestDB(t)
	schema := "issue513_legacy_" + uuid.NewString()
	require.NoError(t, template.Exec("CREATE SCHEMA "+quoted(schema)).Error)
	t.Cleanup(func() {
		require.NoError(t, template.Exec("DROP SCHEMA "+quoted(schema)+" CASCADE").Error)
	})
	withProjectIdentityMigrationSchema(t, template, quoted(schema), func(db *gormlib.DB) {
		// Reuse the migrated fixture's real column types, defaults and checks in
		// fresh tables, where LIKE does not copy triggers. Remove only post-189
		// columns before seeding; the real 186/189 guards are first installed later.
		for _, table := range []string{"projects", "issues", "issue_comments"} {
			require.NoError(t, db.Exec("CREATE TABLE "+quoted(table)+" (LIKE "+quoted(templateSchema)+"."+quoted(table)+" INCLUDING ALL)").Error)
		}
		legacy := map[string][]string{
			"issues":         {"id", "title", "body", "status", "priority", "type", "source_project", "target_project", "source_agent", "created_by_session", "creator_keycard_id", "labels", "acknowledged_at", "resolved_at", "reopened_at", "closed_at", "created_at", "updated_at"},
			"issue_comments": {"id", "issue_id", "author_project", "author_agent", "body", "created_at"},
		}
		for _, table := range []string{"issue_comments", "issues"} {
			var extra []string
			require.NoError(t, db.Raw(`SELECT column_name FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=? AND column_name NOT IN ?`, table, legacy[table]).Scan(&extra).Error)
			for _, column := range extra {
				require.NoError(t, db.Exec("ALTER TABLE "+quoted(table)+" DROP COLUMN "+quoted(column)).Error)
			}
		}
		// LIKE omits foreign keys as well as triggers; retain migration 070's
		// actual comment-parent constraint in the reconstructed historical schema.
		require.NoError(t, db.Exec(`ALTER TABLE issue_comments ADD CONSTRAINT issue_comments_issue_id_fkey
			FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE`).Error)
		var guardCount int64
		require.NoError(t, db.Raw(`SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=current_schema() AND c.relname IN ('issues','issue_comments') AND NOT t.tgisinternal`).Scan(&guardCount).Error)
		require.Zero(t, guardCount, "historical rows must predate guard installation, not bypass an installed guard")
		const target = "51300000-0000-4000-8000-000000000096"
		require.NoError(t, db.Create(&Project{ID: target}).Error)
		require.NoError(t, db.Exec(`INSERT INTO issues (id,title,body,status,priority,type,source_project,target_project,source_agent,created_by_session,creator_keycard_id,labels,created_at,updated_at)
			VALUES (8513,'Synthetic retained legacy card','Exact synthetic legacy body','reopened','high','bug','historical-unresolved-source',?,'historical-agent','historical-session','synthetic-known-creator','["old-label"]','2026-01-02T03:04:05Z','2026-01-03T03:04:05Z')`, target).Error)
		require.NoError(t, db.Exec(`INSERT INTO issue_comments (id,issue_id,author_project,author_agent,body,created_at)
			VALUES (9513,8513,'historical-unresolved-comment-source','historical-comment-agent','Exact synthetic retained comment','2026-01-03T03:04:06Z')`).Error)
		snapshot := func(table string) map[string]json.RawMessage {
			t.Helper()
			var raw string
			require.NoError(t, db.Raw("SELECT to_jsonb(t)::text FROM "+quoted(table)+" t").Row().Scan(&raw))
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(raw), &fields))
			return fields
		}
		beforeIssue, beforeComment := snapshot("issues"), snapshot("issue_comments")
		migration186 := projectConsolidationMigration186()
		migration189 := issueProjectWriteScopeMigration189()
		require.NoError(t, gormigrate.New(db, gormigrate.DefaultOptions, []*gormigrate.Migration{migration186, migration189}).Migrate(), "install the real historical write guards after the unresolved rows already exist")
		guardSnapshot := func() string {
			t.Helper()
			var value string
			require.NoError(t, db.Raw(`SELECT jsonb_build_object(
				'functions', (SELECT jsonb_agg(jsonb_build_object('oid',p.oid,'definition',pg_get_functiondef(p.oid)) ORDER BY p.proname)
				 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema()
				 AND p.proname IN ('engram_consolidation_scope','engram_consolidation_write_guard')),
				'triggers', (SELECT jsonb_agg(jsonb_build_object('oid',t.oid,'enabled',t.tgenabled,'definition',pg_get_triggerdef(t.oid)) ORDER BY c.relname)
				 FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
				 WHERE n.nspname=current_schema() AND c.relname IN ('issues','issue_comments') AND t.tgname='engram_consolidation_scope_write')
			)::text`).Row().Scan(&value))
			return value
		}
		guardsBefore := guardSnapshot()
		require.NoError(t, db.Raw(`SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=current_schema() AND c.relname IN ('issues','issue_comments') AND t.tgname='engram_consolidation_scope_write' AND t.tgenabled='O'`).Scan(&guardCount).Error)
		require.EqualValues(t, 2, guardCount, "both historical guards must now be installed in normal enabled mode")
		var applied190 int64
		require.NoError(t, db.Table("migrations").Where("id=?", issueInternalTasksMigrationID).Count(&applied190).Error)
		require.Zero(t, applied190, "the fixture must not claim the new baseline migration already ran")
		require.NoError(t, gormigrate.New(db, gormigrate.DefaultOptions, []*gormigrate.Migration{migration186, migration189, issueInternalTasksMigration193()}).Migrate(), "additive baseline must not try to re-adopt historical source/author labels")
		assert.Equal(t, guardsBefore, guardSnapshot(), "source functions and both existing write guards retain exact identity, definition and mode")
		for table, before := range map[string]map[string]json.RawMessage{"issues": beforeIssue, "issue_comments": beforeComment} {
			after := snapshot(table)
			for field, value := range before {
				assert.JSONEq(t, string(value), string(after[field]), "all original %s.%s values, timestamps, IDs and attribution survive", table, field)
			}
			assert.JSONEq(t, `{}`, string(after["metadata"]))
			assert.JSONEq(t, `"unknown"`, string(after["actor_kind"]))
			assert.JSONEq(t, `null`, string(after["actor_reference"]))
		}
		var history []IssueChange
		require.NoError(t, db.Where("issue_id=?", 8513).Find(&history).Error)
		require.Len(t, history, 1)
		assert.Equal(t, "baseline", history[0].Action)
		assert.EqualValues(t, 1, history[0].Revision)
		assert.Equal(t, "unknown", history[0].Actor.Kind)
		assert.Nil(t, history[0].Actor.Reference)
		assert.Nil(t, history[0].PreviousRevision)
		require.NotNil(t, history[0].Attribution.SourceProject)
		assert.Equal(t, "historical-unresolved-source", *history[0].Attribution.SourceProject)
		require.NotNil(t, history[0].Attribution.SourceAgent)
		assert.Equal(t, "historical-agent", *history[0].Attribution.SourceAgent)
		require.NotNil(t, history[0].Attribution.Session)
		assert.Equal(t, "historical-session", *history[0].Attribution.Session)
		assert.JSONEq(t, `{}`, string(history[0].Changes))
		assert.Empty(t, history[0].ChangedFields)
		firstBaseline, err := json.Marshal(history[0])
		require.NoError(t, err)
		upgradedIssue, upgradedComment := snapshot("issues"), snapshot("issue_comments")
		require.NoError(t, issueInternalTasksMigration193().Migrate(db))
		history = nil
		require.NoError(t, db.Where("issue_id=?", 8513).Find(&history).Error)
		require.Len(t, history, 1)
		replayedBaseline, err := json.Marshal(history[0])
		require.NoError(t, err)
		assert.JSONEq(t, string(firstBaseline), string(replayedBaseline))
		assert.Equal(t, upgradedIssue, snapshot("issues"))
		assert.Equal(t, upgradedComment, snapshot("issue_comments"))
		assert.Equal(t, guardsBefore, guardSnapshot())
		// Conservation is not a legacy write bypass: ordinary new unresolved
		// source and comment-author writes still fail through the actual guards.
		for _, statement := range []string{
			`INSERT INTO issues (title,source_project,target_project) VALUES ('Synthetic forbidden legacy create','historical-unresolved-source','` + target + `')`,
			`INSERT INTO issue_comments (issue_id,author_project,body) VALUES (8513,'historical-unresolved-comment-source','Synthetic forbidden author')`,
		} {
			err := db.Transaction(func(tx *gormlib.DB) error { return tx.Exec(statement).Error })
			require.ErrorContains(t, err, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")
		}
		assert.Equal(t, guardsBefore, guardSnapshot())
		t.Log("historical seed precedes real 186/189 guards; 190 preserves every original field and attribution; baseline/replay conserved; new unresolved writes denied with both guards enabled")
	})
}
