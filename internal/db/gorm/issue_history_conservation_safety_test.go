package gorm

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	gormlib "gorm.io/gorm"
)

// Only normal startup installs the guard here: rerunning migration 186 would
// hide a missing forward-191 installation after adding the conservation role.
func TestIssueHistoryForward191GuardIgnoresReportedSessionMembership(t *testing.T) {
	db, schema := openInterventionReceiptMigrationTestDB(t)
	project := uuid.NewString()
	require.NoError(t, db.Create(&Project{ID: project}).Error)
	issueID, err := NewIssueStore(db).CreateIssue(context.Background(), &Issue{
		Title: "history guard parent", SourceProject: project, TargetProject: project,
	})
	require.NoError(t, err)
	const session = "01a1a369-dfce-7241-9330-2a0a3f697f9c"
	selector := "legacy-history-" + uuid.NewString()
	require.NoError(t, consolidationRedirect(db, selector, session, project, "history-session-is-not-membership"))
	var explicitSessionScope string
	require.NoError(t, db.Raw(`SELECT engram_consolidation_scope(?,?)`, selector, session).Row().Scan(&explicitSessionScope))
	require.Equal(t, project, explicitSessionScope, "the sentinel redirect must really resolve if someone wrongly uses the reported host session")

	type guard struct {
		TableSchema    string
		FunctionSchema string
		FunctionName   string
		Mode           string
		Nargs          int
		Args           string
	}
	readGuard := func() []guard {
		t.Helper()
		var rows []guard
		require.NoError(t, db.Raw(`SELECT n.nspname AS table_schema, pn.nspname AS function_schema,
			p.proname AS function_name,t.tgenabled::text AS mode,t.tgnargs AS nargs,encode(t.tgargs,'hex') AS args
			FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
			JOIN pg_proc p ON p.oid=t.tgfoid JOIN pg_namespace pn ON pn.oid=p.pronamespace
			WHERE n.nspname=current_schema() AND c.relname='issue_history_events'
			AND t.tgname='engram_consolidation_scope_write' AND NOT t.tgisinternal`).Scan(&rows).Error)
		return rows
	}
	beforeGuard := readGuard()
	require.Equal(t, []guard{{schema, schema, "engram_consolidation_write_guard", "O", 1, hex.EncodeToString([]byte("author_project\x00"))}}, beforeGuard,
		"the normal forward chain must install the owned guard on the event author")
	var columns []string
	require.NoError(t, db.Raw(`SELECT column_name FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='issue_history_events'`).Scan(&columns).Error)
	require.Contains(t, columns, "author_session")
	require.NotContains(t, columns, "session_id", "generic consolidation session_id inference must not consume reported issue provenance")

	newEvent := func(author string) IssueHistoryEvent {
		return IssueHistoryEvent{
			IssueID: issueID, Status: "resolved", AuthorProject: author,
			SessionID: session, SessionSource: "reported", Client: "omp",
			CreatedAt: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC),
		}
	}
	unresolved := newEvent(selector)
	assertConsolidationSQLRefusal(t, db.Create(&unresolved).Error, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")
	var count int64
	require.NoError(t, db.Model(&IssueHistoryEvent{}).Where("issue_id = ?", issueID).Count(&count).Error)
	require.Zero(t, count, "matching reported session must not admit a legacy author")
	for _, author := range []string{project, ""} {
		row := newEvent(author)
		require.NoError(t, db.Create(&row).Error, "canonical and unknown authors retain the existing scope owner's semantics")
		before := consolidationTestRowJSON(t, db, "issue_history_events", row.ID)
		assertConsolidationSQLRefusal(t, db.Model(&IssueHistoryEvent{}).Where("id = ?", row.ID).Updates(map[string]any{
			"author_project": selector, "author_session": session, "client": "openclaw",
		}).Error, "PROJECT_ADOPTION_UNRESOLVED_MEMBERSHIP")
		require.JSONEq(t, before, consolidationTestRowJSON(t, db, "issue_history_events", row.ID), "refused updates must retain every original value")
		var persisted IssueHistoryEvent
		require.NoError(t, db.First(&persisted, row.ID).Error)
		require.True(t, row.CreatedAt.Equal(persisted.CreatedAt), "CreatedAt must preserve the exact instant")
		expectedFields, actualFields := row, persisted
		expectedFields.CreatedAt, actualFields.CreatedAt = time.Time{}, time.Time{}
		require.Equal(t, expectedFields, actualFields)
		raw, err := json.Marshal(persisted)
		require.NoError(t, err)
		var projection map[string]any
		require.NoError(t, json.Unmarshal(raw, &projection))
		require.Equal(t, session, projection["session_id"])
		require.NotContains(t, projection, "author_session", "storage rename must not change the consumer JSON name")
	}
	valid := newEvent(project)
	require.NoError(t, db.Create(&valid).Error, "a refusal must not poison the next canonical write")
	type historyFK struct {
		ParentSchema  string
		ParentTable   string
		Columns       pq.StringArray
		ParentColumns pq.StringArray
		DeleteAction  string
		Validated     bool
	}
	var fks []historyFK
	fkRows, err := db.Raw(`SELECT pn.nspname AS parent_schema,p.relname AS parent_table,
		ARRAY(SELECT a.attname::text FROM unnest(f.conkey) WITH ORDINALITY k(attnum,ord)
		 JOIN pg_attribute a ON a.attrelid=f.conrelid AND a.attnum=k.attnum ORDER BY k.ord) AS columns,
		ARRAY(SELECT a.attname::text FROM unnest(f.confkey) WITH ORDINALITY k(attnum,ord)
		 JOIN pg_attribute a ON a.attrelid=f.confrelid AND a.attnum=k.attnum ORDER BY k.ord) AS parent_columns,
		f.confdeltype::text AS delete_action,f.convalidated AS validated
		FROM pg_constraint f JOIN pg_class c ON c.oid=f.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace
		JOIN pg_class p ON p.oid=f.confrelid JOIN pg_namespace pn ON pn.oid=p.relnamespace
		WHERE n.nspname=current_schema() AND c.relname='issue_history_events' AND f.contype='f'`).Rows()
	require.NoError(t, err)
	defer fkRows.Close()
	for fkRows.Next() {
		var fk historyFK
		require.NoError(t, fkRows.Scan(&fk.ParentSchema, &fk.ParentTable, &fk.Columns, &fk.ParentColumns, &fk.DeleteAction, &fk.Validated))
		fks = append(fks, fk)
	}
	require.NoError(t, fkRows.Err())
	require.NoError(t, fkRows.Close())
	require.Len(t, fks, 1, "only the actual issue parent FK establishes event ownership")
	require.Equal(t, schema, fks[0].ParentSchema)
	require.Equal(t, "issues", fks[0].ParentTable)
	require.Equal(t, pq.StringArray{"issue_id"}, fks[0].Columns)
	require.Equal(t, pq.StringArray{"id"}, fks[0].ParentColumns)
	require.Equal(t, "c", fks[0].DeleteAction)
	require.True(t, fks[0].Validated)
	missingParent := newEvent(project)
	missingParent.IssueID = issueID + 1000000
	var fkError *pgconn.PgError
	require.ErrorAs(t, db.Create(&missingParent).Error, &fkError)
	require.Equal(t, "23503", fkError.Code, "real FK behavior, not only a catalog/string assertion")
	require.Equal(t, beforeGuard, readGuard(), "all accepted/refused writers preserve exact guard ownership and mode")
}

func TestIssueHistoryCensusRecognizesAuthorAndDurableParent(t *testing.T) {
	db, _ := openInterventionReceiptMigrationTestDB(t)
	project := uuid.NewString()
	require.NoError(t, db.Create(&Project{ID: project}).Error)
	issueID, err := NewIssueStore(db).CreateIssue(context.Background(), &Issue{
		Title: "conservation census parent", SourceProject: project, TargetProject: project,
	})
	require.NoError(t, err)
	// Historical unresolved input is seeded only inside the existing private
	// fixture's transactional owner-guard suspension, never via a client bypass.
	rows := []IssueHistoryEvent{
		{IssueID: issueID, Status: "resolved", AuthorProject: project, SessionID: "01a1a369-dfce-7241-9330-2a0a3f697f9c", SessionSource: "reported", Client: "omp"},
		{IssueID: issueID, Status: "closed", AuthorProject: "legacy-history-author", AuthorAgent: "retained-label", SessionID: "retained-host-value", SessionSource: "reported", Client: "openclaw"},
		{IssueID: issueID, Status: "reopened", AuthorProject: ""},
	}
	for index := range rows {
		rows[index].CreatedAt = time.Date(2026, 11, 4, 0, 0, index, 0, time.UTC)
	}
	var guardBefore string
	guardSQL := `SELECT jsonb_agg(jsonb_build_object('oid',t.oid,'mode',t.tgenabled,'function',t.tgfoid,'args',encode(t.tgargs,'hex')) ORDER BY t.tgname)::text
		FROM pg_trigger t WHERE t.tgrelid='issue_history_events'::regclass AND NOT t.tgisinternal`
	require.NoError(t, db.Raw(guardSQL).Row().Scan(&guardBefore))
	require.NotEmpty(t, guardBefore)
	require.NoError(t, db.Transaction(func(tx *gormlib.DB) error {
		restore, err := consolidationSuspendWriteGuards(tx)
		if err != nil {
			return err
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
		return restore()
	}))
	var guardAfter string
	require.NoError(t, db.Raw(guardSQL).Row().Scan(&guardAfter))
	require.JSONEq(t, guardBefore, guardAfter)
	vault, err := vaultcrypto.OpenExistingVault(&config.Config{EncryptionKey: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	store := NewProjectConsolidationStore(db, vault, nil)
	census, err := store.Census(context.Background(), "issue-history-forward191-synthetic")
	require.NoError(t, err)
	var state consolidationCensusState
	require.NoError(t, store.open(census.Reference, "census", &state))
	foundTable := false
	for _, table := range state.Tables {
		if table.Name == "issue_history_events" {
			foundTable = true
			require.EqualValues(t, 3, table.Count)
			require.Equal(t, "durable_history", table.Kind)
		}
	}
	require.True(t, foundTable, "nonzero populated event table must be in the real inventory")
	parentRefs := map[string]bool{}
	for _, row := range state.Rows {
		if row.Table == "issues" && row.Column == "target_project" {
			parentRefs[row.Reference] = true
		}
	}
	require.Len(t, parentRefs, 1)
	authors, inherited := 0, 0
	for _, row := range state.Rows {
		if row.Table != "issue_history_events" {
			continue
		}
		require.NotEqual(t, "unknown", row.Kind, "a populated author field needs an owned role")
		switch row.Column {
		case "author_project":
			authors++
			require.Equal(t, "tenant", row.Kind, "reuse the existing issue-comment author scope role")
			require.Equal(t, row.Selector == "", row.Global)
		case "":
			inherited++
			require.Equal(t, "inherited", row.Kind)
			require.Len(t, row.Parents, 1)
			require.True(t, parentRefs[row.Parents[0]], "durable history inherits its actual issue FK, not session/client membership")
		default:
			t.Fatalf("unexpected project-membership field from event provenance: %s", row.Column)
		}
	}
	require.Equal(t, 3, authors)
	require.Equal(t, 3, inherited)
	plan, err := store.Preflight(context.Background(), ConsolidationPreflightRequest{CensusReference: census.Reference, CensusFingerprint: census.Fingerprint})
	require.NoError(t, err)
	mappedAuthors := 0
	for _, mapping := range plan.Mapping {
		if mapping.Family == "issue_history_events" {
			require.NotEqual(t, "unknown_field_role", mapping.Reason)
			if mapping.Role == "author_project" {
				mappedAuthors++
			}
		}
	}
	require.Equal(t, 3, mappedAuthors)
	var retained []IssueHistoryEvent
	require.NoError(t, db.Where("issue_id = ?", issueID).Order("id").Find(&retained).Error)
	require.Len(t, retained, len(rows))
	for index, row := range rows {
		require.True(t, row.CreatedAt.Equal(retained[index].CreatedAt), "event %d CreatedAt must preserve the exact instant", row.ID)
		expectedFields, actualFields := row, retained[index]
		expectedFields.CreatedAt, actualFields.CreatedAt = time.Time{}, time.Time{}
		require.Equal(t, expectedFields, actualFields, "inventory/planning must not repair, infer or rewrite historical attribution")
	}
}
