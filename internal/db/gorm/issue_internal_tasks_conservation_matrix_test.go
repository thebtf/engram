package gorm

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This is post-hoc conservation coverage, not historical RED or a binary
// downgrade rehearsal. Migration 193 refuses down-migration; its retained
// readers can be exercised without claiming an old unconditional writer is safe.
func TestIssueInternalTasksConservationMatrix(t *testing.T) {
	template, templateSchema := openInterventionReceiptMigrationTestDB(t)
	schema := "issue513_matrix_" + uuid.NewString()
	require.NoError(t, template.Exec("CREATE SCHEMA "+quoted(schema)).Error)
	t.Cleanup(func() {
		require.NoError(t, template.Exec("DROP SCHEMA "+quoted(schema)+" CASCADE").Error)
	})
	// Match ordinary startup (store.go): migrate a new pool before exposing
	// its readers. Keep normal statement caching; never reuse a pre-ALTER
	// SELECT * plan as though it belonged to a restarted application.
	openSession := func(name string) *gormlib.DB {
		t.Helper()
		config, err := pgx.ParseConfig(os.Getenv("DATABASE_DSN"))
		require.NoError(t, err)
		if config.RuntimeParams == nil {
			config.RuntimeParams = make(map[string]string)
		}
		config.RuntimeParams["search_path"] = quoted(schema) + ", public"
		config.RuntimeParams["application_name"] = "engram_issue513_matrix_" + name
		pool := stdlib.OpenDB(*config)
		pool.SetMaxOpenConns(1)
		pool.SetMaxIdleConns(1)
		t.Cleanup(func() { require.NoError(t, pool.Close()) })
		db, err := gormlib.Open(postgres.New(postgres.Config{Conn: pool}), &gormlib.Config{
			Logger: logger.Default.LogMode(logger.Silent), PrepareStmt: true,
		})
		require.NoError(t, err)
		var actualSchema string
		require.NoError(t, db.Raw("SELECT current_schema()").Scan(&actualSchema).Error)
		require.Equal(t, schema, actualSchema)
		return db
	}
	func() {
		db := openSession("migration")
		// Match the existing historical fixture: LIKE retains actual column
		// types/defaults/checks, but not triggers or foreign keys. The projection
		// is migrations 070/071/075/160; no guard is suspended to seed old rows.
		legacy := map[string][]string{
			"issues":         {"id", "title", "body", "status", "priority", "type", "source_project", "target_project", "source_agent", "created_by_session", "creator_keycard_id", "labels", "acknowledged_at", "resolved_at", "reopened_at", "closed_at", "created_at", "updated_at"},
			"issue_comments": {"id", "issue_id", "author_project", "author_agent", "body", "created_at"},
		}
		for _, table := range []string{"projects", "issues", "issue_comments"} {
			require.NoError(t, db.Exec("CREATE TABLE "+quoted(table)+" (LIKE "+quoted(templateSchema)+"."+quoted(table)+" INCLUDING ALL)").Error)
		}
		for _, table := range []string{"issue_comments", "issues"} {
			var extra, columns []string
			require.NoError(t, db.Raw(`SELECT column_name FROM information_schema.columns
				WHERE table_schema=current_schema() AND table_name=? AND column_name NOT IN ?`, table, legacy[table]).Scan(&extra).Error)
			for _, column := range extra {
				require.NoError(t, db.Exec("ALTER TABLE "+quoted(table)+" DROP COLUMN "+quoted(column)).Error)
			}
			require.NoError(t, db.Raw(`SELECT column_name FROM information_schema.columns
				WHERE table_schema=current_schema() AND table_name=? ORDER BY ordinal_position`, table).Scan(&columns).Error)
			require.ElementsMatch(t, legacy[table], columns, "exact pre-190 column set: %s", table)
		}
		require.NoError(t, db.Exec(`ALTER TABLE issue_comments ADD CONSTRAINT issue_comments_issue_id_fkey
			FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE`).Error)
		var guards, featureTables int64
		require.NoError(t, db.Raw(`SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid
			JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema()
			AND c.relname IN ('issues','issue_comments') AND NOT t.tgisinternal`).Scan(&guards).Error)
		require.Zero(t, guards, "historical seeding precedes guard installation")
		require.NoError(t, db.Raw(`SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()
			AND table_name IN ('issue_changes','issue_create_receipts')`).Scan(&featureTables).Error)
		require.Zero(t, featureTables)

		const source = "51300000-0000-4000-8000-000000000091"
		const target = "51300000-0000-4000-8000-000000000092"
		for _, id := range []string{source, target} {
			require.NoError(t, db.Create(&Project{ID: id}).Error)
		}
		provenance := []struct {
			name, source, creator, authorProject string
			agent, session, authorAgent          any
		}{
			{"known", source, "synthetic-known-keycard", source, "synthetic-known-agent", "synthetic-known-session", "synthetic-known-comment-agent"},
			{"historical_unresolved", "synthetic-retained-source", "synthetic-retained-keycard", "synthetic-retained-author", "synthetic-retained-agent", "synthetic-retained-session", "synthetic-retained-comment-agent"},
			{"absent_null", "", "", "", nil, nil, nil},
			{"absent_empty", "", "", "", "", "", ""},
		}
		// These are the six statuses admitted by migration 071 and the shared
		// application's validStatus. There is no rejected_at column to invent.
		statuses := []string{"open", "acknowledged", "resolved", "reopened", "closed", "rejected"}
		priorities := []string{"critical", "high", "medium", "low"}
		types := []string{"bug", "feature", "improvement", "task"}
		base := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
		var issueIDs, commentIDs, knownSourceIDs []int64
		expectedComments := make(map[int64][]int64)
		caseNames := make(map[int64]string)
		for stateIndex, status := range statuses {
			for provenanceIndex, evidence := range provenance {
				id := int64(85130 + stateIndex*101 + provenanceIndex*7)
				created := base.Add(time.Duration(len(issueIDs)) * 24 * time.Hour)
				var acknowledged, resolved, reopened, closed *time.Time
				at := func(hours int) *time.Time {
					value := created.Add(time.Duration(hours) * time.Hour)
					return &value
				}
				if status != "open" {
					acknowledged = at(1)
				}
				if status == "resolved" || status == "reopened" || status == "closed" {
					resolved = at(2)
				}
				if status == "reopened" || status == "closed" {
					reopened = at(3)
				}
				if status == "closed" {
					resolved, closed = at(4), at(5)
				}
				if status == "rejected" {
					closed = at(5)
				}
				var body any = fmt.Sprintf("Exact synthetic body %d: строка\nsecond line", id)
				var labels any = `["retained","Юникод","retained"]`
				if evidence.name == "absent_null" {
					body, labels = nil, nil
				} else if evidence.name == "absent_empty" {
					body, labels = "", `[]`
				}
				require.NoError(t, db.Exec(`INSERT INTO issues
					(id,title,body,status,priority,type,source_project,target_project,source_agent,created_by_session,
					 creator_keycard_id,labels,acknowledged_at,resolved_at,reopened_at,closed_at,created_at,updated_at)
					VALUES (?,?,?,?,?,?,?,?,?,?,?,?::jsonb,?,?,?,?,?,?)`,
					id, fmt.Sprintf("Synthetic retained %s %s", status, evidence.name), body, status,
					priorities[provenanceIndex], types[provenanceIndex], evidence.source, target, evidence.agent,
					evidence.session, evidence.creator, labels, acknowledged, resolved, reopened, closed, created, *at(6)).Error)
				issueIDs = append(issueIDs, id)
				caseNames[id] = status + "/" + evidence.name
				if evidence.source == source {
					knownSourceIDs = append(knownSourceIDs, id)
				}
				// IDs run opposite to time order, so the reader must preserve the
				// exact parent and chronological order, not sort comments by ID.
				for ordinal := range 2 {
					commentID := id*10 + int64(2-ordinal)
					require.NoError(t, db.Exec(`INSERT INTO issue_comments
						(id,issue_id,author_project,author_agent,body,created_at) VALUES (?,?,?,?,?,?)`,
						commentID, id, evidence.authorProject, evidence.authorAgent,
						fmt.Sprintf("Exact synthetic comment %d\nСохранить", commentID), *at(7 + ordinal)).Error)
					commentIDs = append(commentIDs, commentID)
					expectedComments[id] = append(expectedComments[id], commentID)
				}
			}
		}

		snapshot := func(table, key string) map[int64]map[string]json.RawMessage {
			t.Helper()
			rows, err := db.Raw("SELECT to_jsonb(t)::text FROM " + quoted(table) + " t ORDER BY " + quoted(key)).Rows()
			require.NoError(t, err)
			defer rows.Close()
			result := make(map[int64]map[string]json.RawMessage)
			for rows.Next() {
				var raw string
				require.NoError(t, rows.Scan(&raw))
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal([]byte(raw), &fields))
				var id int64
				require.NoError(t, json.Unmarshal(fields[key], &id))
				_, duplicate := result[id]
				require.False(t, duplicate, "unexpected duplicate %s.%s=%d", table, key, id)
				result[id] = fields
			}
			require.NoError(t, rows.Err())
			return result
		}
		beforeIssues, beforeComments := snapshot("issues", "id"), snapshot("issue_comments", "id")
		ids := func(rows map[int64]map[string]json.RawMessage) []int64 {
			result := make([]int64, 0, len(rows))
			for id := range rows {
				result = append(result, id)
			}
			return result
		}
		require.ElementsMatch(t, issueIDs, ids(beforeIssues))
		require.ElementsMatch(t, commentIDs, ids(beforeComments))
		migrations := []*gormigrate.Migration{projectConsolidationMigration186(), issueProjectWriteScopeMigration189()}
		require.NoError(t, gormigrate.New(db, gormigrate.DefaultOptions, migrations).Migrate())
		require.NoError(t, db.Raw(`SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid
			JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema()
			AND c.relname IN ('issues','issue_comments') AND t.tgname='engram_consolidation_scope_write' AND t.tgenabled='O'`).Scan(&guards).Error)
		require.EqualValues(t, 2, guards, "both real historical guards remain enabled during the upgrade")
		preReaderDB := openSession("pre190_reader")
		var migrationPID, preReaderPID int
		require.NoError(t, db.Raw("SELECT pg_backend_pid()").Scan(&migrationPID).Error)
		require.NoError(t, preReaderDB.Raw("SELECT pg_backend_pid()").Scan(&preReaderPID).Error)
		require.NotEqual(t, migrationPID, preReaderPID, "pre-feature reader owns an independent connection")
		store := NewIssueStore(preReaderDB)
		type retainedRead struct {
			issue    Issue
			comments []IssueComment
		}
		readsBefore := make(map[int64]retainedRead)
		for _, id := range issueIDs {
			issue, comments, err := store.GetIssue(t.Context(), id)
			require.NoError(t, err, "retained reader on genuine pre-190 columns")
			readsBefore[id] = retainedRead{*issue, comments}
		}
		readList := func(params IssueListParams, expected []int64) {
			t.Helper()
			rows, total, err := store.ListIssuesEx(t.Context(), params)
			require.NoError(t, err)
			require.EqualValues(t, len(expected), total)
			var got []int64
			for _, row := range rows {
				got = append(got, row.ID)
				assert.EqualValues(t, len(expectedComments[row.ID]), row.CommentCount)
			}
			require.ElementsMatch(t, expected, got, "exact reader ID set, not counts alone")
		}
		for _, params := range []IssueListParams{{Limit: 200}, {TargetProject: target, Limit: 200}} {
			readList(params, issueIDs)
		}
		readList(IssueListParams{SourceProject: source, Limit: 200}, knownSourceIDs)
		preReaderPool, err := preReaderDB.DB()
		require.NoError(t, err)
		require.NoError(t, preReaderPool.Close(), "quiesce the old reader before migration; no old process is resumed")

		migrations = append(migrations, issueInternalTasksMigration193())
		require.NoError(t, gormigrate.New(db, gormigrate.DefaultOptions, migrations).Migrate())
		postReaderDB := openSession("expanded_reader")
		var postReaderPID int
		require.NoError(t, postReaderDB.Raw("SELECT pg_backend_pid()").Scan(&postReaderPID).Error)
		require.NotEqual(t, migrationPID, postReaderPID)
		require.NotEqual(t, preReaderPID, postReaderPID)
		var applied190 int64
		require.NoError(t, postReaderDB.Table("migrations").Where("id=?", issueInternalTasksMigrationID).Count(&applied190).Error)
		require.EqualValues(t, 1, applied190, "an independent reader observes the committed migration, not uncommitted fixture DDL")
		store = NewIssueStore(postReaderDB)
		afterIssues, afterComments := snapshot("issues", "id"), snapshot("issue_comments", "id")
		require.ElementsMatch(t, issueIDs, ids(afterIssues))
		require.ElementsMatch(t, commentIDs, ids(afterComments))
		assertOriginal := func(table string, before, after map[int64]map[string]json.RawMessage) {
			t.Helper()
			for id, fields := range before {
				for field, value := range fields {
					assert.JSONEq(t, string(value), string(after[id][field]), "exact old %s[%d].%s", table, id, field)
				}
				for field, value := range map[string]string{"metadata": `{}`, "actor_kind": `"unknown"`, "actor_reference": `null`} {
					assert.JSONEq(t, value, string(after[id][field]), "%s[%d].%s", table, id, field)
				}
			}
		}
		assertOriginal("issues", beforeIssues, afterIssues)
		assertOriginal("issue_comments", beforeComments, afterComments)
		baselines := snapshot("issue_changes", "issue_id")
		require.ElementsMatch(t, issueIDs, ids(baselines), "one and only one baseline for every exact old issue")
		require.Empty(t, snapshot("issue_create_receipts", "issue_id"), "no fabricated historical request keys/receipts")
		for _, id := range issueIDs {
			t.Run(caseNames[id], func(t *testing.T) {
				for field, value := range map[string]string{"revision": `1`, "owner_label": `null`, "recipient_label": `null`} {
					assert.JSONEq(t, value, string(afterIssues[id][field]), "issue %d %s", id, field)
				}
				for field, value := range map[string]string{
					"issue_id": fmt.Sprint(id), "revision": `1`, "previous_revision": `null`, "action": `"baseline"`,
					"actor_kind": `"unknown"`, "actor_reference": `null`, "changed_fields": `[]`, "changes": `{}`,
				} {
					assert.JSONEq(t, value, string(baselines[id][field]), "truthful baseline %d %s", id, field)
				}
				for field, oldField := range map[string]string{
					"attribution_source_project": "source_project", "attribution_source_agent": "source_agent", "attribution_session": "created_by_session",
				} {
					value := beforeIssues[id][oldField]
					if string(value) == `""` {
						value = json.RawMessage(`null`)
					}
					assert.JSONEq(t, string(value), string(baselines[id][field]), "known attribution retained; absence not invented")
				}
				var baselineTime time.Time
				require.NoError(t, json.Unmarshal(baselines[id]["created_at"], &baselineTime))
				require.False(t, baselineTime.IsZero())
				issue, comments, err := store.GetIssue(t.Context(), id)
				require.NoError(t, err)
				var gotComments []int64
				for _, comment := range comments {
					assert.Equal(t, id, comment.IssueID)
					gotComments = append(gotComments, comment.ID)
				}
				assert.Equal(t, expectedComments[id], gotComments, "exact comment parents and chronological order")
				// Strip only newly added fields from the current retained reader's
				// local result, then compare every pre-feature model field.
				issue.Metadata, issue.Revision, issue.OwnerLabel, issue.RecipientLabel, issue.Actor = nil, 0, nil, nil, IssueActor{}
				for index := range comments {
					comments[index].Metadata, comments[index].Actor = nil, IssueActor{}
				}
				assert.Equal(t, readsBefore[id], retainedRead{*issue, comments})
			})
		}
		for _, params := range []IssueListParams{{Limit: 200}, {TargetProject: target, Limit: 200}} {
			readList(params, issueIDs)
		}
		readList(IssueListParams{SourceProject: source, Limit: 200}, knownSourceIDs)

		// Exercise both tracked startup replay and direct idempotent migration.
		require.NoError(t, gormigrate.New(db, gormigrate.DefaultOptions, migrations).Migrate())
		require.NoError(t, issueInternalTasksMigration193().Migrate(db))
		assert.Equal(t, afterIssues, snapshot("issues", "id"))
		assert.Equal(t, afterComments, snapshot("issue_comments", "id"))
		assert.Equal(t, baselines, snapshot("issue_changes", "issue_id"))
		require.Empty(t, snapshot("issue_create_receipts", "issue_id"))
		// The actual rollback owner refuses contraction. This proves preserved
		// data and read access after refusal, not a supported prior app version.
		require.ErrorContains(t, issueInternalTasksMigration193().Rollback(db), "rollback refused")
		assert.Equal(t, afterIssues, snapshot("issues", "id"))
		assert.Equal(t, afterComments, snapshot("issue_comments", "id"))
		assert.Equal(t, baselines, snapshot("issue_changes", "issue_id"))
		require.Empty(t, snapshot("issue_create_receipts", "issue_id"))
		readList(IssueListParams{TargetProject: target, Limit: 200}, issueIDs)
		t.Logf("synthetic conservation: states=%d provenance_variants=%d issues=%d comments=%d; exact old fields, unknown baselines, no receipts, replay and retained readers preserved; down-migration refused; no prior binary or universal SQL old-writer rejection claimed", len(statuses), len(provenance), len(issueIDs), len(commentIDs))
	}()
}
