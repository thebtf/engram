package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/issues"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// T017 deliberately uses the migrated schema and the real authenticated router.
// Old writers must satisfy the new history contract without disabling DB guards.
func issue513T017Fixture(t *testing.T) *issue513ActorFixture {
	t.Helper()
	fixture := newIssue513ActorFixture(t)
	db := fixture.service.store.GetDB()
	var schema, role string
	require.NoError(t, db.Raw("SELECT current_schema()").Scan(&schema).Error)
	require.NotEqual(t, "public", schema)
	require.NoError(t, db.Raw("SHOW session_replication_role").Scan(&role).Error)
	require.Equal(t, "origin", role)
	for _, table := range []string{"issues", "issue_comments", "issue_changes", "issue_create_receipts", "audit_log", "collection_selections"} {
		var owner string
		require.NoError(t, db.Raw("SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=to_regclass(?)", table).Scan(&owner).Error)
		require.Equal(t, schema, owner, "all mutated tables must be schema isolated")
	}
	for _, trigger := range []string{"engram_issue_revision_monotonic", "engram_issue_receipt_immutable", "engram_issue_change_append_only"} {
		var active int64
		require.NoError(t, db.Raw("SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND t.tgname=? AND t.tgenabled IN ('O','A')", trigger).Scan(&active).Error)
		require.EqualValues(t, 1, active, "mandatory guard must remain enabled")
	}
	me := fixture.call(t, fixture.browser, http.MethodGet, "/api/auth/me?source_project="+issue513Source, "")
	_, actor := issue513ActorFromMe(t, me)
	require.Equal(t, "operator", actor.Kind)
	require.Equal(t, fmt.Sprintf("browser:%d", fixture.browserUserID), issue513ActorReference(actor.Reference))
	return fixture
}

func issue513T017Create(t *testing.T, fixture *issue513ActorFixture, credential issue513ActorCredential, label string) (int64, string) {
	t.Helper()
	body := issue513CreateBody(issue513Source, t.Name()+"/"+label, "Synthetic T017 "+label, `{"retained":{"value":1}}`)
	id := issue513CreatedID(t, fixture.call(t, credential, http.MethodPost, "/api/issues", body))
	var history []gormdb.IssueChange
	require.NoError(t, fixture.service.store.GetDB().Where("issue_id = ?", id).Find(&history).Error)
	require.Len(t, history, 1, "real create must establish history before testing an old writer")
	require.EqualValues(t, 1, history[0].Revision)
	require.Equal(t, "created", history[0].Action)
	return id, body
}

type issue513T017State struct {
	Issues   []gormdb.Issue
	Comments []gormdb.IssueComment
	History  []gormdb.IssueChange
	Audits   []gormdb.AuditLogEntry
}

func issue513T017StateOf(t *testing.T, fixture *issue513ActorFixture) issue513T017State {
	t.Helper()
	db := fixture.service.store.GetDB()
	var state issue513T017State
	require.NoError(t, db.Order("id").Find(&state.Issues).Error)
	require.NoError(t, db.Order("id").Find(&state.Comments).Error)
	require.NoError(t, db.Order("issue_id, revision").Find(&state.History).Error)
	require.NoError(t, db.Where("action LIKE 'issue.%'").Order("id").Find(&state.Audits).Error)
	return state
}

func issue513T017Unchanged(t *testing.T, fixture *issue513ActorFixture, before issue513T017State) {
	t.Helper()
	after := issue513T017StateOf(t, fixture)
	assert.Equal(t, before.Issues, after.Issues, "losing operation must preserve every issue field, revision and timestamp")
	assert.Equal(t, before.Comments, after.Comments, "losing operation must not append or delete comments")
	assert.Equal(t, before.History, after.History, "losing operation must not append or rewrite history")
	assert.Equal(t, before.Audits, after.Audits, "losing operation must not publish a success audit")
}

func issue513T017Patch(t *testing.T, fixture *issue513ActorFixture, id int64, fields string) {
	t.Helper()
	row, _, err := fixture.service.issueStore.GetIssue(t.Context(), id)
	require.NoError(t, err)
	body := fmt.Sprintf(`{"source_project":%q,"source_agent":"human","expected_revision":%q,%s}`, issue513Source, fmt.Sprint(row.Revision), fields)
	response := fixture.call(t, fixture.browser, http.MethodPatch, fmt.Sprintf("/api/issues/%d", id), body)
	require.Equal(t, http.StatusOK, response.Code, "shared-owner setup/control must succeed: %s", response.Body.String())
}

func issue513T017History(t *testing.T, fixture *issue513ActorFixture, id, previous int64, field string) {
	t.Helper()
	row, _, err := fixture.service.issueStore.GetIssue(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, previous+1, row.Revision, "material writer must advance exactly once")
	var history []gormdb.IssueChange
	require.NoError(t, fixture.service.store.GetDB().Where("issue_id = ?", id).Order("revision").Find(&history).Error)
	assert.Len(t, history, int(previous+1), "one material history row per committed revision")
	var latest *gormdb.IssueChange
	for index := range history {
		if history[index].Revision == previous+1 {
			latest = &history[index]
		}
	}
	if !assert.NotNil(t, latest, "new revision must have durable history") {
		return
	}
	assert.Equal(t, &previous, latest.PreviousRevision)
	assert.Contains(t, []string(latest.ChangedFields), field)
	assert.Equal(t, "operator", latest.Actor.Kind)
	assert.Equal(t, fmt.Sprintf("browser:%d", fixture.browserUserID), issue513ActorReference(latest.Actor.Reference))
	assert.Equal(t, issue513Source, issue513ActorReference(latest.Attribution.SourceProject))
	assert.Equal(t, "human", issue513ActorReference(latest.Attribution.SourceAgent))
	assert.JSONEq(t, `{}`, string(latest.Changes), "history must not invent private before/after snapshots")
}

func issue513T017Audit(t *testing.T, fixture *issue513ActorFixture, action string, ids []int64) {
	t.Helper()
	var entries []gormdb.AuditLogEntry
	require.NoError(t, fixture.service.store.GetDB().Where("action = ?", "issue."+action).Order("id").Find(&entries).Error)
	var matching []gormdb.AuditLogEntry
	for _, entry := range entries {
		var state struct {
			IDs    []int64 `json:"issue_ids"`
			Source string  `json:"source_project"`
			Agent  string  `json:"source_agent"`
		}
		require.NotNil(t, entry.AfterState)
		require.NoError(t, json.Unmarshal(*entry.AfterState, &state))
		if len(state.IDs) == len(ids) && fmt.Sprint(state.IDs) == fmt.Sprint(ids) {
			assert.Equal(t, issue513Source, state.Source)
			assert.Equal(t, "human", state.Agent)
			matching = append(matching, entry)
		}
	}
	require.Len(t, matching, 1, "successful complete set has exactly one actor audit")
	assert.Equal(t, fmt.Sprintf("session:human:browser-user/%d", fixture.browserUserID), matching[0].Actor)
}

func issue513T017Ack(t *testing.T, fixture *issue513ActorFixture, credential issue513ActorCredential, ids []int64) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"ids": ids, "source_project": issue513Source, "source_agent": "human"})
	require.NoError(t, err)
	return fixture.call(t, credential, http.MethodPost, "/api/issues/acknowledge", string(body))
}

func TestIssue513T017BulkAcknowledge(t *testing.T) {
	fixture := issue513T017Fixture(t)
	t.Run("control_shared_owner_and_same_result_retry", func(t *testing.T) {
		id, body := issue513T017Create(t, fixture, fixture.client, "control")
		retry := fixture.call(t, fixture.client, http.MethodPost, "/api/issues", body)
		require.Equal(t, http.StatusOK, retry.Code)
		var result struct {
			ID       int64
			Replayed bool
		}
		require.NoError(t, json.Unmarshal(retry.Body.Bytes(), &result))
		require.Equal(t, id, result.ID)
		require.True(t, result.Replayed)
		issue513T017Patch(t, fixture, id, `"status":"acknowledged"`)
		issue513T017History(t, fixture, id, 1, "status")
	})
	for _, invalid := range []string{"non_open", "missing", "denied", "unauthenticated"} {
		t.Run("control_"+invalid+"_whole_set_unchanged", func(t *testing.T) {
			first, _ := issue513T017Create(t, fixture, fixture.client, "first")
			second, _ := issue513T017Create(t, fixture, fixture.client, "second")
			credential := fixture.browser
			ids := []int64{first, second}
			want := http.StatusBadRequest // Existing bulk invalid-transition mapping; selection conflicts remain 409.
			switch invalid {
			case "non_open":
				issue513T017Patch(t, fixture, second, `"status":"resolved"`)
			case "missing":
				ids[1] = second + 1000000
			case "denied":
				credential, want = fixture.client, http.StatusForbidden
			case "unauthenticated":
				credential, want = issue513ActorCredential{}, http.StatusUnauthorized
			}
			before := issue513T017StateOf(t, fixture)
			response := issue513T017Ack(t, fixture, credential, ids)
			assert.Equal(t, want, response.Code)
			assert.NotContains(t, response.Body.String(), "Synthetic T017")
			assert.NotContains(t, response.Body.String(), "retained")
			issue513T017Unchanged(t, fixture, before)
		})
	}
	t.Run("control_audit_failure_rolls_back_whole_set", func(t *testing.T) {
		first, _ := issue513T017Create(t, fixture, fixture.client, "first")
		second, _ := issue513T017Create(t, fixture, fixture.client, "second")
		before := issue513T017StateOf(t, fixture)
		callbacks := fixture.service.store.GetDB().Callback().Create()
		name := "t017_bulk_audit_failure"
		require.NoError(t, callbacks.Before("gorm:create").Register(name, func(tx *gormlib.DB) {
			if tx.Statement.Table == "audit_log" {
				tx.AddError(errors.New("synthetic T017 audit persistence failure"))
			}
		}))
		defer func() { require.NoError(t, callbacks.Remove(name)) }()
		response := issue513T017Ack(t, fixture, fixture.browser, []int64{first, second})
		assert.Equal(t, http.StatusInternalServerError, response.Code)
		issue513T017Unchanged(t, fixture, before)
	})
	t.Run("valid_set_advances_each_once_with_authenticated_history", func(t *testing.T) {
		first, _ := issue513T017Create(t, fixture, fixture.client, "first")
		second, _ := issue513T017Create(t, fixture, fixture.client, "second")
		before := issue513T017StateOf(t, fixture)
		ids := []int64{first, second}
		response := issue513T017Ack(t, fixture, fixture.browser, ids)
		require.Equal(t, http.StatusOK, response.Code)
		var result struct {
			Acknowledged int64 `json:"acknowledged"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
		assert.EqualValues(t, 2, result.Acknowledged)
		for _, id := range ids {
			row, _, err := fixture.service.issueStore.GetIssue(t.Context(), id)
			require.NoError(t, err)
			assert.Equal(t, "acknowledged", row.Status)
			assert.NotNil(t, row.AcknowledgedAt)
			issue513T017History(t, fixture, id, 1, "status")
		}
		assert.Equal(t, before.Comments, issue513T017StateOf(t, fixture).Comments)
		issue513T017Audit(t, fixture, "acknowledge", ids)
	})
}

func issue513T017SelectionCall(t *testing.T, fixture *issue513ActorFixture, credential issue513ActorCredential, path, requestID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body["source_project"], body["source_agent"] = issue513Source, "human"
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "http://localhost:37777"+path, strings.NewReader(string(raw)))
	request.RemoteAddr = "192.0.2.51:43500"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:37777")
	request.Header.Set("X-Engram-Request-ID", requestID)
	if credential.cookie != nil {
		request.AddCookie(credential.cookie)
	}
	if credential.bearer != "" {
		request.Header.Set("Authorization", "Bearer "+credential.bearer)
	}
	response := httptest.NewRecorder()
	fixture.service.router.ServeHTTP(response, request)
	for _, value := range fixture.privateValues {
		assert.False(t, strings.Contains(response.Body.String(), value), "response must not contain credentials")
	}
	return response
}

func issue513T017Snapshot(t *testing.T, fixture *issue513ActorFixture, ids ...int64) issueSelectionSnapshot {
	t.Helper()
	targets := make([]map[string]any, len(ids))
	for index, id := range ids {
		targets[index] = map[string]any{"id": fmt.Sprint(id)}
	}
	response := issue513T017SelectionCall(t, fixture, fixture.browser, "/api/issues/selection", "t017-snapshot", map[string]any{"selection": map[string]any{"kind": "explicit", "targets": targets}})
	require.Equal(t, http.StatusOK, response.Code, "selection binding/control must reach real store: %s", response.Body.String())
	var result issueSelectionSnapshotResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.Len(t, result.Selection.Targets, len(ids))
	for _, target := range result.Selection.Targets {
		require.NotZero(t, target.ExpectedVersion)
	}
	return result.Selection
}

func issue513T017Operation(t *testing.T, fixture *issue513ActorFixture, selection issueSelectionSnapshot, action string, fields map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	fields["request_id"], fields["action"] = "t017-operation", action
	fields["selection"] = map[string]any{"kind": "explicit", "selection_version": selection.Version}
	return issue513T017SelectionCall(t, fixture, fixture.browser, "/api/issues/operations", "t017-operation", fields)
}

func issue513T017StoreTargets(t *testing.T, fixture *issue513ActorFixture, ids ...int64) []gormdb.IssueSelectionTarget {
	t.Helper()
	targets := make([]gormdb.IssueSelectionTarget, len(ids))
	for index, id := range ids {
		row, _, err := fixture.service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		targets[index] = gormdb.IssueSelectionTarget{ID: id, ExpectedUpdatedAt: row.UpdatedAt.UTC().Truncate(time.Microsecond)}
	}
	return targets
}

func issue513T017ActorContext(fixture *issue513ActorFixture) context.Context {
	return auth.WithIdentity(context.Background(), auth.SessionForBrowserUser("admin", fixture.browserUserID))
}

func TestIssue513T017Selection(t *testing.T) {
	fixture := issue513T017Fixture(t)
	for _, action := range []string{"priority", "labels", "acknowledge", "status"} {
		t.Run(action+"_advances_each_once_with_authenticated_history", func(t *testing.T) {
			first, _ := issue513T017Create(t, fixture, fixture.client, "first")
			second, _ := issue513T017Create(t, fixture, fixture.client, "second")
			selection := issue513T017Snapshot(t, fixture, first, second)
			fields := map[string]any{}
			field := action
			switch action {
			case "priority":
				fields["priority"] = "high"
			case "labels":
				fields["labels"] = []string{"selected"}
			case "status":
				fields["status"], field = "resolved", "status"
			case "acknowledge":
				field = "status"
			}
			response := issue513T017Operation(t, fixture, selection, action, fields)
			require.Equal(t, http.StatusOK, response.Code, "existing material action must reach persistence")
			for _, id := range []int64{first, second} {
				issue513T017History(t, fixture, id, 1, field)
				row, _, err := fixture.service.issueStore.GetIssue(t.Context(), id)
				require.NoError(t, err)
				switch action {
				case "priority":
					assert.Equal(t, "high", row.Priority)
				case "labels":
					assert.Equal(t, []string{"selected"}, []string(row.Labels))
				case "acknowledge":
					assert.Equal(t, "acknowledged", row.Status)
				case "status":
					assert.Equal(t, "resolved", row.Status)
				}
			}
			issue513T017Audit(t, fixture, action, []int64{first, second})
		})
	}
	t.Run("control_stale_timestamp_keeps_original_selection_and_all_rows", func(t *testing.T) {
		first, _ := issue513T017Create(t, fixture, fixture.client, "first")
		second, _ := issue513T017Create(t, fixture, fixture.client, "second")
		selection := issue513T017Snapshot(t, fixture, first, second)
		issue513T017Patch(t, fixture, second, `"priority":"critical","comment":"Synthetic competing winner"`)
		before := issue513T017StateOf(t, fixture)
		response := issue513T017Operation(t, fixture, selection, "labels", map[string]any{"labels": []string{"loser"}})
		assert.Equal(t, http.StatusConflict, response.Code)
		assert.Empty(t, response.Body.String(), "failed selection must remain body-free")
		issue513T017Unchanged(t, fixture, before)
		current := issue513T017SelectionCall(t, fixture, fixture.browser, "/api/issues/selection/current", "t017-current", map[string]any{})
		require.Equal(t, http.StatusOK, current.Code)
		var saved issueSelectionSnapshotResponse
		require.NoError(t, json.Unmarshal(current.Body.Bytes(), &saved))
		assert.Equal(t, selection.Version, saved.Selection.Version, "conflict must not refresh the selection version")
		assert.Equal(t, selection.Targets, saved.Selection.Targets, "conflict must not silently rebase targets")
	})
	t.Run("control_bound_browser_session_cannot_be_substituted", func(t *testing.T) {
		id, _ := issue513T017Create(t, fixture, fixture.client, "bound")
		selection := issue513T017Snapshot(t, fixture, id)
		before := issue513T017StateOf(t, fixture)
		response := issue513T017SelectionCall(t, fixture, fixture.browserAgain, "/api/issues/operations", "t017-operation", map[string]any{"request_id": "t017-operation", "action": "priority", "priority": "high", "selection": map[string]any{"kind": "explicit", "selection_version": selection.Version}})
		assert.Equal(t, http.StatusConflict, response.Code)
		assert.Empty(t, response.Body.String())
		issue513T017Unchanged(t, fixture, before)
	})
	t.Run("control_mixed_creator_authority_rolls_back_all_rows", func(t *testing.T) {
		first, _ := issue513T017Create(t, fixture, fixture.client, "owned")
		second, _ := issue513T017Create(t, fixture, fixture.otherClient, "not-owned")
		row, _, err := fixture.service.issueStore.GetIssue(t.Context(), first)
		require.NoError(t, err)
		require.NotEmpty(t, row.CreatorKeycardID, "creator must originate in real token middleware")
		before := issue513T017StateOf(t, fixture)
		priority := "critical"
		result, err := fixture.service.issueStore.ApplyIssueSelectionOperation(t.Context(), gormdb.IssueSelectionOperation{Action: gormdb.IssueSelectionAction{Kind: gormdb.IssueSelectionPriority, Priority: &priority}, Actor: gormdb.IssueSelectionActor{KeycardID: row.CreatorKeycardID}, Targets: issue513T017StoreTargets(t, fixture, first, second)})
		assert.ErrorIs(t, err, gormdb.ErrIssueForbidden)
		require.Len(t, result.Items, 1)
		assert.Equal(t, second, result.Items[0].TargetID)
		assert.Nil(t, result.Items[0].Readback)
		issue513T017Unchanged(t, fixture, before)
	})
	t.Run("normalized_noop_does_not_advance_or_append_history", func(t *testing.T) {
		first, _ := issue513T017Create(t, fixture, fixture.client, "first")
		second, _ := issue513T017Create(t, fixture, fixture.client, "second")
		selection := issue513T017Snapshot(t, fixture, first, second)
		before := issue513T017StateOf(t, fixture)
		response := issue513T017Operation(t, fixture, selection, "priority", map[string]any{"priority": "medium"})
		require.Equal(t, http.StatusOK, response.Code)
		after := issue513T017StateOf(t, fixture)
		assert.Equal(t, before.Issues, after.Issues, "normalized no-op must not change issue timestamps or revision")
		assert.Equal(t, before.Comments, after.Comments)
		assert.Equal(t, before.History, after.History)
	})
	t.Run("store_status_and_optional_comment_share_revision_and_actor", func(t *testing.T) {
		first, _ := issue513T017Create(t, fixture, fixture.client, "first")
		second, _ := issue513T017Create(t, fixture, fixture.client, "second")
		status, comment := "resolved", "Synthetic selected outcome"
		ctx := issue513T017ActorContext(fixture)
		ids := []int64{first, second}
		_, err := fixture.service.issueStore.ApplyIssueSelectionOperation(ctx, gormdb.IssueSelectionOperation{Action: gormdb.IssueSelectionAction{Kind: gormdb.IssueSelectionStatus, Status: &status, Comment: &comment, AuthorProject: issue513Source, AuthorAgent: "human"}, Actor: gormdb.IssueSelectionActor{IsOperator: true}, Targets: issue513T017StoreTargets(t, fixture, ids...), Audit: issueMutationAudit(ctx, "status", issue513Source, "human", ids)})
		require.NoError(t, err)
		for _, id := range ids {
			issue513T017History(t, fixture, id, 1, "status")
			_, comments, readErr := fixture.service.issueStore.GetIssue(t.Context(), id)
			require.NoError(t, readErr)
			require.Len(t, comments, 1)
			assert.Equal(t, comment, comments[0].Body)
			assert.Equal(t, "operator", comments[0].Actor.Kind)
			assert.Equal(t, fmt.Sprintf("browser:%d", fixture.browserUserID), issue513ActorReference(comments[0].Actor.Reference))
			assert.Equal(t, issue513Source, comments[0].AuthorProject)
			assert.Equal(t, "human", comments[0].AuthorAgent)
		}
	})
	for _, failedTable := range []string{"issue_comments", "issue_changes", "audit_log"} {
		t.Run("persistence_failure_"+failedTable+"_rolls_back_compound_set", func(t *testing.T) {
			first, _ := issue513T017Create(t, fixture, fixture.client, "first")
			second, _ := issue513T017Create(t, fixture, fixture.client, "second")
			targets := issue513T017StoreTargets(t, fixture, first, second)
			before := issue513T017StateOf(t, fixture)
			callbacks := fixture.service.store.GetDB().Callback().Create()
			name := "t017_selection_failure_" + failedTable
			attempts := 0
			require.NoError(t, callbacks.Before("gorm:create").Register(name, func(tx *gormlib.DB) {
				if tx.Statement.Table == failedTable {
					attempts++
					tx.AddError(errors.New("synthetic T017 persistence failure"))
				}
			}))
			defer func() { require.NoError(t, callbacks.Remove(name)) }()
			status, comment := "resolved", "Synthetic losing compound outcome"
			ctx := issue513T017ActorContext(fixture)
			_, err := fixture.service.issueStore.ApplyIssueSelectionOperation(ctx, gormdb.IssueSelectionOperation{Action: gormdb.IssueSelectionAction{Kind: gormdb.IssueSelectionStatus, Status: &status, Comment: &comment, AuthorProject: issue513Source, AuthorAgent: "human"}, Actor: gormdb.IssueSelectionActor{IsOperator: true}, Targets: targets, Audit: issueMutationAudit(ctx, "status", issue513Source, "human", []int64{first, second})})
			assert.Error(t, err, "failed persistence must abort rather than skip this dependency")
			assert.Positive(t, attempts, "the production writer must actually attempt its dependent persistence")
			issue513T017Unchanged(t, fixture, before)
		})
	}
	t.Run("control_locked_competing_revision_loser_keeps_winner_and_comments", func(t *testing.T) {
		first, _ := issue513T017Create(t, fixture, fixture.client, "first")
		second, _ := issue513T017Create(t, fixture, fixture.client, "second")
		targets := issue513T017StoreTargets(t, fixture, first, second)
		db := fixture.service.store.GetDB()
		locker := db.Begin()
		require.NoError(t, locker.Error)
		defer locker.Rollback()
		var locked gormdb.Issue
		require.NoError(t, locker.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, second).Error)
		var lockerPID int
		require.NoError(t, locker.Raw("SELECT pg_backend_pid()").Scan(&lockerPID).Error)
		ctx, cancel := context.WithTimeout(issue513T017ActorContext(fixture), 10*time.Second)
		defer cancel()
		done := make(chan error, 1)
		status, comment := "resolved", "Synthetic losing locked outcome"
		go func() {
			defer close(done)
			_, err := fixture.service.issueStore.ApplyIssueSelectionOperation(ctx, gormdb.IssueSelectionOperation{Action: gormdb.IssueSelectionAction{Kind: gormdb.IssueSelectionStatus, Status: &status, Comment: &comment, AuthorProject: issue513Source, AuthorAgent: "human"}, Actor: gormdb.IssueSelectionActor{IsOperator: true}, Targets: targets})
			done <- err
		}()
		defer func() {
			cancel()
			_ = locker.Rollback().Error
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
		}()
		blocked := false
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			// pg_stat_activity caches its session list within this holding transaction.
			// Refresh observation only; keep the real row lock and original selection.
			require.NoError(t, locker.Exec("SELECT pg_stat_clear_snapshot()").Error)
			var count int64
			require.NoError(t, locker.Raw("SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND ? = ANY(pg_blocking_pids(pid))", lockerPID).Scan(&count).Error)
			if count > 0 {
				blocked = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		require.True(t, blocked, "selection must wait on the actual PostgreSQL row lock")
		actor, err := issues.CaptureActor(ctx)
		require.NoError(t, err)
		expected, title, winnerComment := fmt.Sprint(locked.Revision), "Synthetic committed competing revision", "Synthetic retained winner comment"
		_, err = gormdb.NewIssueStore(locker).MutateTaskIssue(ctx, actor, issues.Scope{SourceProject: issue513Source}, second, issues.MutationCommand{ExpectedRevision: &expected, Title: &title, Comment: &winnerComment, SourceAgent: "human", CommentMetadata: json.RawMessage(`{}`)})
		require.NoError(t, err, "winner uses committed shared owner, not a raw revision fabrication")
		require.NoError(t, locker.Commit().Error)
		err = <-done
		assert.ErrorIs(t, err, gormdb.ErrIssueSelectionConflict)
		row, comments, err := fixture.service.issueStore.GetIssue(t.Context(), second)
		require.NoError(t, err)
		assert.EqualValues(t, 2, row.Revision)
		assert.Equal(t, title, row.Title)
		assert.Equal(t, "open", row.Status)
		require.Len(t, comments, 1)
		assert.Equal(t, winnerComment, comments[0].Body)
		firstRow, firstComments, err := fixture.service.issueStore.GetIssue(t.Context(), first)
		require.NoError(t, err)
		assert.EqualValues(t, 1, firstRow.Revision)
		assert.Equal(t, "open", firstRow.Status)
		assert.Empty(t, firstComments)
		var changes []gormdb.IssueChange
		require.NoError(t, db.Order("issue_id, revision").Where("issue_id IN ?", []int64{first, second}).Find(&changes).Error)
		assert.Len(t, changes, 3, "only initial rows and real competing winner remain")
	})
}

func TestIssue513T017Delete(t *testing.T) {
	fixture := issue513T017Fixture(t)
	for _, selectionDelete := range []bool{false, true} {
		kind := "single"
		if selectionDelete {
			kind = "selection_store"
		}
		t.Run(kind+"_retains_tombstone_history_and_current_comment_policy", func(t *testing.T) {
			first, firstBody := issue513T017Create(t, fixture, fixture.client, "first")
			second, secondBody := issue513T017Create(t, fixture, fixture.client, "second")
			ids, bodies := []int64{first}, []string{firstBody}
			if selectionDelete {
				ids, bodies = []int64{first, second}, []string{firstBody, secondBody}
			}
			for _, id := range ids {
				issue513T017Patch(t, fixture, id, `"comment":"Synthetic comment follows existing hard-delete policy"`)
			}
			before := issue513T017StateOf(t, fixture)
			var previousTombstones int64
			require.NoError(t, fixture.service.store.GetDB().Table("issue_create_receipts").Where("issue_id IS NULL").Count(&previousTombstones).Error)
			if selectionDelete {
				ctx := issue513T017ActorContext(fixture)
				_, err := fixture.service.issueStore.ApplyIssueSelectionOperation(ctx, gormdb.IssueSelectionOperation{Action: gormdb.IssueSelectionAction{Kind: gormdb.IssueSelectionDelete}, Actor: gormdb.IssueSelectionActor{IsOperator: true}, Targets: issue513T017StoreTargets(t, fixture, ids...), Audit: issueMutationAudit(ctx, "delete", issue513Source, "human", ids)})
				require.NoError(t, err)
			} else {
				response := fixture.call(t, fixture.browser, http.MethodDelete, fmt.Sprintf("/api/issues/%d?source_project=%s&source_agent=human", first, issue513Source), "")
				require.Equal(t, http.StatusNoContent, response.Code)
			}
			for index, id := range ids {
				var live, comments, tombstones int64
				db := fixture.service.store.GetDB()
				require.NoError(t, db.Model(&gormdb.Issue{}).Where("id = ?", id).Count(&live).Error)
				require.NoError(t, db.Model(&gormdb.IssueComment{}).Where("issue_id = ?", id).Count(&comments).Error)
				assert.Zero(t, live)
				assert.Zero(t, comments, "existing hard delete removes comments; feature must not invent retention/permissions")
				require.NoError(t, db.Table("issue_create_receipts").Where("issue_id IS NULL").Count(&tombstones).Error)
				assert.EqualValues(t, previousTombstones+int64(len(ids)), tombstones, "real FK must preserve immutable receipt tombstones")
				retry := fixture.call(t, fixture.client, http.MethodPost, "/api/issues", bodies[index])
				assert.Equal(t, http.StatusGone, retry.Code)
				assert.NotContains(t, retry.Body.String(), "Synthetic T017")
				assert.NotContains(t, retry.Body.String(), `"retained":`, "gone response must not expose deleted metadata")
				var result struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(retry.Body.Bytes(), &result))
				assert.Equal(t, "ISSUE_GONE", result.Error.Code)
				require.NoError(t, db.Model(&gormdb.Issue{}).Where("id = ?", id).Count(&live).Error)
				assert.Zero(t, live, "retry must never resurrect the original issue")
			}
			after := issue513T017StateOf(t, fixture)
			assert.Equal(t, before.History, after.History, "hard delete must retain exact append-only change rows")
			assert.Equal(t, len(before.Issues)-len(ids), len(after.Issues), "retry cannot allocate a replacement ID")
			issue513T017Audit(t, fixture, "delete", ids)
			for _, query := range []string{"UPDATE issue_changes SET action='synthetic overwrite' WHERE issue_id = ?", "DELETE FROM issue_changes WHERE issue_id = ?"} {
				err := fixture.service.store.GetDB().Exec(query, first).Error
				var failure *pgconn.PgError
				require.True(t, errors.As(err, &failure), "guard must reject through PostgreSQL, not fixture/auth failure")
				assert.Equal(t, "55000", failure.Code)
			}
			assert.Equal(t, after.History, issue513T017StateOf(t, fixture).History)
		})
	}
	for _, invalid := range []string{"stale", "missing"} {
		t.Run("control_selection_delete_"+invalid+"_preserves_whole_set", func(t *testing.T) {
			first, _ := issue513T017Create(t, fixture, fixture.client, "first")
			second, _ := issue513T017Create(t, fixture, fixture.client, "second")
			for _, id := range []int64{first, second} {
				issue513T017Patch(t, fixture, id, `"comment":"Synthetic comment must survive losing delete"`)
			}
			targets := issue513T017StoreTargets(t, fixture, first, second)
			if invalid == "stale" {
				issue513T017Patch(t, fixture, second, `"priority":"critical"`)
			} else {
				targets[1].ID += 1000000
			}
			before := issue513T017StateOf(t, fixture)
			ctx := issue513T017ActorContext(fixture)
			_, err := fixture.service.issueStore.ApplyIssueSelectionOperation(ctx, gormdb.IssueSelectionOperation{Action: gormdb.IssueSelectionAction{Kind: gormdb.IssueSelectionDelete}, Actor: gormdb.IssueSelectionActor{IsOperator: true}, Targets: targets, Audit: issueMutationAudit(ctx, "delete", issue513Source, "human", []int64{first, second})})
			assert.ErrorIs(t, err, gormdb.ErrIssueSelectionConflict)
			issue513T017Unchanged(t, fixture, before)
		})
	}
	t.Run("control_creator_cannot_gain_operator_delete_permission", func(t *testing.T) {
		first, _ := issue513T017Create(t, fixture, fixture.client, "first")
		second, _ := issue513T017Create(t, fixture, fixture.otherClient, "second")
		before := issue513T017StateOf(t, fixture)
		response := fixture.call(t, fixture.client, http.MethodDelete, fmt.Sprintf("/api/issues/%d?source_project=%s&source_agent=human", first, issue513Source), "")
		assert.Equal(t, http.StatusForbidden, response.Code)
		row, _, err := fixture.service.issueStore.GetIssue(t.Context(), first)
		require.NoError(t, err)
		_, err = fixture.service.issueStore.ApplyIssueSelectionOperation(t.Context(), gormdb.IssueSelectionOperation{Action: gormdb.IssueSelectionAction{Kind: gormdb.IssueSelectionDelete}, Actor: gormdb.IssueSelectionActor{KeycardID: row.CreatorKeycardID}, Targets: issue513T017StoreTargets(t, fixture, first, second)})
		assert.ErrorIs(t, err, gormdb.ErrIssueForbidden)
		issue513T017Unchanged(t, fixture, before)
	})
	for _, selectionDelete := range []bool{false, true} {
		kind := "single"
		if selectionDelete {
			kind = "selection_store"
		}
		t.Run("control_"+kind+"_audit_failure_restores_comments_and_live_receipt", func(t *testing.T) {
			first, body := issue513T017Create(t, fixture, fixture.client, "first")
			issue513T017Patch(t, fixture, first, `"comment":"Synthetic retained comment on rollback"`)
			before := issue513T017StateOf(t, fixture)
			callbacks := fixture.service.store.GetDB().Callback().Create()
			name := "t017_delete_audit_failure"
			require.NoError(t, callbacks.Before("gorm:create").Register(name, func(tx *gormlib.DB) {
				if tx.Statement.Table == "audit_log" {
					tx.AddError(errors.New("synthetic T017 delete audit failure"))
				}
			}))
			defer func() { require.NoError(t, callbacks.Remove(name)) }()
			if selectionDelete {
				ctx := issue513T017ActorContext(fixture)
				_, err := fixture.service.issueStore.ApplyIssueSelectionOperation(ctx, gormdb.IssueSelectionOperation{Action: gormdb.IssueSelectionAction{Kind: gormdb.IssueSelectionDelete}, Actor: gormdb.IssueSelectionActor{IsOperator: true}, Targets: issue513T017StoreTargets(t, fixture, first), Audit: issueMutationAudit(ctx, "delete", issue513Source, "human", []int64{first})})
				assert.Error(t, err)
			} else {
				response := fixture.call(t, fixture.browser, http.MethodDelete, fmt.Sprintf("/api/issues/%d?source_project=%s&source_agent=human", first, issue513Source), "")
				assert.Equal(t, http.StatusInternalServerError, response.Code)
			}
			issue513T017Unchanged(t, fixture, before)
			retry := fixture.call(t, fixture.client, http.MethodPost, "/api/issues", body)
			require.Equal(t, http.StatusOK, retry.Code, "rollback must keep original live receipt replayable")
			var replay struct {
				ID       int64
				Replayed bool
			}
			require.NoError(t, json.Unmarshal(retry.Body.Bytes(), &replay))
			assert.Equal(t, first, replay.ID)
			assert.True(t, replay.Replayed)
		})
	}
}
