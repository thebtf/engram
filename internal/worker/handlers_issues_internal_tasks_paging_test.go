package worker

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/issues"
)

const issue513PageTarget = 4 * 1024 * 1024

// Keep assertions body-free: a failed read/privacy oracle must not print the
// synthetic private content or any real fixture credential into retained output.
func issue513PagingCall(t *testing.T, f *issue513ActorFixture, credential issue513ActorCredential, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://localhost:37777"+path, nil)
	request.RemoteAddr = "192.0.2.51:43500"
	if credential.bearer != "" {
		request.Header.Set("Authorization", "Bearer "+credential.bearer)
	}
	if credential.cookie != nil {
		request.AddCookie(credential.cookie)
	}
	response := httptest.NewRecorder()
	f.service.router.ServeHTTP(response, request)
	for _, value := range f.privateValues {
		assert.False(t, strings.Contains(response.Body.String(), value), "response must not disclose fixture credentials")
	}
	return response
}

func issue513PagingObject(t *testing.T, response *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	require.Equal(t, http.StatusOK, response.Code, "authenticated read must reach the real handler")
	var result map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result), "handler response must be JSON")
	return result
}

func issue513PagingInt(t *testing.T, object map[string]json.RawMessage, key string, expected int) {
	t.Helper()
	raw, present := object[key]
	if !assert.True(t, present, "response must include %s", key) {
		return
	}
	var value int
	require.NoError(t, json.Unmarshal(raw, &value))
	assert.Equal(t, expected, value, "%s must report the admitted value/authorized total", key)
}

func issue513PagingBool(t *testing.T, object map[string]json.RawMessage, key string, expected bool) {
	t.Helper()
	raw, present := object[key]
	if !assert.True(t, present, "response must include %s", key) {
		return
	}
	var value bool
	require.NoError(t, json.Unmarshal(raw, &value))
	assert.Equal(t, expected, value, "%s must describe this collection only", key)
}

func issue513PagingNext(t *testing.T, object map[string]json.RawMessage, key string, next *int) {
	t.Helper()
	raw, present := object[key]
	if !assert.True(t, present, "response must include %s, including explicit null at the end", key) {
		return
	}
	var value *int
	require.NoError(t, json.Unmarshal(raw, &value))
	assert.Equal(t, next, value, "%s is the first unreturned record, or null", key)
}

func issue513PagingRecords(t *testing.T, object map[string]json.RawMessage, key string) []map[string]json.RawMessage {
	t.Helper()
	raw, present := object[key]
	if !assert.True(t, present, "response must include %s", key) {
		return nil
	}
	var records []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &records))
	return records
}

func issue513PagingIDs(t *testing.T, records []map[string]json.RawMessage) []int64 {
	t.Helper()
	ids := make([]int64, len(records))
	for index, record := range records {
		require.NoError(t, json.Unmarshal(record["id"], &ids[index]))
	}
	return ids
}

func issue513PagingRevisions(t *testing.T, records []map[string]json.RawMessage) []string {
	t.Helper()
	revisions := make([]string, len(records))
	for index, record := range records {
		require.NoError(t, json.Unmarshal(record["revision"], &revisions[index]), "history revisions are canonical decimal strings")
	}
	return revisions
}

func issue513PagingError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) map[string]json.RawMessage {
	t.Helper()
	assert.Equal(t, status, response.Code)
	var result map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	var failure struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if assert.True(t, len(result["error"]) > 0 && result["error"][0] == '{', "error must be a typed, body-free object") {
		require.NoError(t, json.Unmarshal(result["error"], &failure))
		assert.Equal(t, code, failure.Code)
	}
	return result
}

func TestIssueHTTPInternalTaskPagingContract(t *testing.T) {
	// Reuse the dedicated migration186 fixture admission policy from
	// consolidationTestDatabaseConfig; its run name and loopback port are dynamic.
	// The existing worker fixture then owns schema creation, migration and cleanup.
	rawDSN := os.Getenv("DATABASE_DSN")
	if rawDSN == "" {
		t.Skip("DATABASE_DSN not set; PostgreSQL paging proof not exercised")
	}
	target, err := url.Parse(rawDSN)
	require.True(t, err == nil && (target.Scheme == "postgres" || target.Scheme == "postgresql"), "paging proof requires an explicit PostgreSQL URL for the disposable test database")
	dsn, err := pgx.ParseConfig(rawDSN)
	require.True(t, err == nil, "invalid disposable test database DSN")
	require.True(t, dsn.Host == "127.0.0.1" || dsn.Host == "localhost" || dsn.Host == "::1", "paging fixture requires an admitted loopback PostgreSQL node")
	const prefix, suffix = "engram_migration186_scope_test_", "_source"
	require.True(t, strings.HasPrefix(dsn.Database, prefix) && strings.HasSuffix(dsn.Database, suffix) && len(dsn.Database) > len(prefix)+len(suffix), "refusing a database outside the existing dedicated test namespace")
	require.True(t, dsn.RuntimeParams["search_path"] == "" || dsn.RuntimeParams["search_path"] == "public", "paging fixture requires the initial public schema before owned schema isolation")
	require.Zero(t, len(dsn.Fallbacks), "fixture DSN must not fall back to another PostgreSQL target")
	f := newIssue513ActorFixture(t)
	service := f.service
	db := service.store.GetDB()
	var schema string
	require.NoError(t, db.Raw("SELECT current_schema()").Scan(&schema).Error)
	require.True(t, strings.HasPrefix(schema, "worker_uci_context_"))
	for _, table := range []string{"issues", "issue_comments", "issue_changes", "issue_create_receipts", "api_tokens", "users", "sessions"} {
		var owner string
		require.NoError(t, db.Raw("SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = to_regclass(?)", table).Scan(&owner).Error)
		require.Equal(t, schema, owner, "all issue/auth fixtures must belong to the isolated schema: %s", table)
	}
	t.Logf("fixture_schema=%s; routes=Service.setupMiddleware/setupRoutes; auth=real master and persisted keycards; seed=IssueStore and issues.Application", schema)

	projectC := "51300000-0000-4000-8000-000000000071"
	projectD := "51300000-0000-4000-8000-000000000072"
	for _, project := range []string{projectC, projectD} {
		require.NoError(t, db.Create(&gormdb.Project{ID: project, IdentityStatus: sql.NullString{String: "active", Valid: true}, DisplayName: sql.NullString{String: "Synthetic paging project", Valid: true}}).Error)
		exists, err := service.sessionStore.CanonicalProjectExists(t.Context(), project)
		require.NoError(t, err)
		require.True(t, exists)
		resolved, err := service.issueStore.ResolveProject(t.Context(), project)
		require.NoError(t, err)
		require.Equal(t, project, resolved)
	}
	application := issues.New(service.issueStore, nil)
	ctx := auth.WithIdentity(t.Context(), auth.Admin())
	seed := func(t *testing.T, source, target, title, body, priority string) int64 {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"target_project": target, "source_agent": "synthetic-paging-agent", "title": title, "body": body, "priority": priority, "type": "task", "labels": []string{"synthetic"},
		})
		require.NoError(t, err)
		created, err := application.Create(ctx, issues.Scope{SourceProject: source}, raw)
		require.NoError(t, err)
		return created.ID
	}
	comment := func(t *testing.T, id int64, body string) int64 {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"comment": body, "source_agent": "synthetic-paging-commenter"})
		require.NoError(t, err)
		commented, err := application.Mutate(ctx, issues.Scope{SourceProject: issue513Source}, id, raw)
		require.NoError(t, err)
		require.NotNil(t, commented.CommentID)
		return *commented.CommentID
	}
	create := func(t *testing.T, source string, fields map[string]any) issues.Result {
		t.Helper()
		raw, err := json.Marshal(fields)
		require.NoError(t, err)
		result, err := application.Create(ctx, issues.Scope{SourceProject: source}, raw)
		require.NoError(t, err, "shared-owner seed admission must succeed before the HTTP read")
		require.Equal(t, "1", result.Revision)
		return result
	}
	mutate := func(t *testing.T, source string, id int64, fields map[string]any) issues.Result {
		t.Helper()
		raw, err := json.Marshal(fields)
		require.NoError(t, err)
		result, err := application.Mutate(ctx, issues.Scope{SourceProject: source}, id, raw)
		require.NoError(t, err, "real CAS seed must commit before the HTTP read")
		return result
	}
	list := func(t *testing.T, query url.Values) map[string]json.RawMessage {
		t.Helper()
		return issue513PagingObject(t, issue513PagingCall(t, f, f.master, "/api/issues?"+query.Encode()))
	}
	detailPath := func(id int64, query url.Values) string { return fmt.Sprintf("/api/issues/%d?%s", id, query.Encode()) }
	detail := func(t *testing.T, id int64, query url.Values) map[string]json.RawMessage {
		t.Helper()
		return issue513PagingObject(t, issue513PagingCall(t, f, f.master, detailPath(id, query)))
	}

	t.Run("static_205_cards", func(t *testing.T) {
		priorities := []string{"critical", "high", "medium", "low"}
		ranks := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
		rows := make([]gormdb.Issue, 0, 205)
		for index := range 205 {
			id := seed(t, issue513Source, issue513Source, fmt.Sprintf("Synthetic static card %03d", index), "Synthetic private static detail", priorities[index%4])
			row, _, err := service.issueStore.GetIssue(t.Context(), id)
			require.NoError(t, err)
			rows = append(rows, *row)
		}
		require.EqualValues(t, 205, issue513Count(t, service), "static-corpus denominator must be exact")
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Priority != rows[j].Priority {
				return ranks[rows[i].Priority] < ranks[rows[j].Priority]
			}
			if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
				return rows[i].CreatedAt.After(rows[j].CreatedAt)
			}
			return rows[i].ID > rows[j].ID
		})
		expected := make([]int64, len(rows))
		for index, row := range rows {
			expected[index] = row.ID
		}
		for _, size := range []int{50, 200} {
			t.Run(fmt.Sprintf("all_rows_limit_%d", size), func(t *testing.T) {
				var accumulated []int64
				for offset := 0; offset < 205; offset += size {
					query := url.Values{"limit": {strconv.Itoa(size)}, "offset": {strconv.Itoa(offset)}}
					page := list(t, query)
					end := min(offset+size, 205)
					ids := issue513PagingIDs(t, issue513PagingRecords(t, page, "issues"))
					assert.Equal(t, expected[offset:end], ids, "priority/created_at/id order must remain stable across every page")
					accumulated = append(accumulated, ids...)
					issue513PagingInt(t, page, "total", 205)
					issue513PagingInt(t, page, "limit", size)
					issue513PagingInt(t, page, "offset", offset)
					var next *int
					if end < 205 {
						next = &end
					}
					issue513PagingNext(t, page, "next_offset", next)
				}
				assert.Equal(t, expected, accumulated, "all 205 unique static IDs must be reachable, including later pages")
			})
		}
		t.Run("default_and_empty_tail", func(t *testing.T) {
			page := list(t, nil)
			assert.Equal(t, expected[:50], issue513PagingIDs(t, issue513PagingRecords(t, page, "issues")))
			issue513PagingInt(t, page, "limit", 50)
			issue513PagingInt(t, page, "offset", 0)
			next := 50
			issue513PagingNext(t, page, "next_offset", &next)
			tail := list(t, url.Values{"offset": {"205"}, "limit": {"200"}})
			assert.Zero(t, len(issue513PagingRecords(t, tail, "issues")))
			issue513PagingInt(t, tail, "total", 205)
			issue513PagingInt(t, tail, "limit", 200)
			issue513PagingInt(t, tail, "offset", 205)
			issue513PagingNext(t, tail, "next_offset", nil)
		})
		t.Run("summary_excludes_detail_payloads", func(t *testing.T) {
			page := list(t, url.Values{"limit": {"1"}})
			records := issue513PagingRecords(t, page, "issues")
			require.Len(t, records, 1)
			for _, field := range []string{"body", "metadata", "history", "comments", "creator_keycard_id"} {
				_, exposed := records[0][field]
				assert.False(t, exposed, "list summary must not carry %s detail data", field)
			}
			for _, field := range []string{"id", "revision", "title", "status", "priority", "type", "source_project", "target_project", "source_agent", "owner_label", "recipient_label", "labels", "created_at", "updated_at", "comment_count", "project_origin"} {
				_, present := records[0][field]
				assert.True(t, present, "summary must preserve %s", field)
			}
		})
	})

	t.Run("independent_source_target_filters", func(t *testing.T) {
		ab := seed(t, projectC, projectD, "Synthetic C to D", "Synthetic role detail", "medium")
		ba := seed(t, projectD, projectC, "Synthetic D to C", "Synthetic role detail", "medium")
		aa := seed(t, projectC, projectC, "Synthetic C to C", "Synthetic role detail", "medium")
		for _, test := range []struct {
			name     string
			query    url.Values
			ids      []int64
			projects []string
		}{
			{"target_only", url.Values{"project": {projectC}}, []int64{ba, aa}, []string{projectC, projectD}},
			{"source_only", url.Values{"source_project": {projectC}}, []int64{ab, aa}, []string{projectC, projectD}},
			{"source_and_target", url.Values{"source_project": {projectC}, "project": {projectD}}, []int64{ab}, []string{projectC, projectD}},
			{"same_project_both_roles", url.Values{"source_project": {projectC}, "project": {projectC}}, []int64{aa}, []string{projectC}},
		} {
			t.Run(test.name, func(t *testing.T) {
				page := list(t, test.query)
				assert.ElementsMatch(t, test.ids, issue513PagingIDs(t, issue513PagingRecords(t, page, "issues")))
				issue513PagingInt(t, page, "total", len(test.ids))
				var names map[string]string
				require.NoError(t, json.Unmarshal(page["project_names"], &names))
				keys := make([]string, 0, len(names))
				for project := range names {
					keys = append(keys, project)
				}
				assert.ElementsMatch(t, test.projects, keys, "display names must be limited to result projects")
			})
		}
	})

	t.Run("literal_title_body_query_and_combined_filters", func(t *testing.T) {
		literal := `Needle%_\Suffix`
		matching := create(t, issue513OtherSource, map[string]any{"target_project": issue513OtherSource, "title": "Synthetic " + literal + " title", "body": "Synthetic private title-match detail", "owner_label": "docs", "recipient_label": "next", "source_agent": "synthetic-query-agent", "type": "task", "priority": "high", "metadata": map[string]any{"only_metadata_marker": "metadata-only-query-token"}})
		bodyID := seed(t, issue513OtherSource, issue513OtherSource, "Synthetic body-only match", "Synthetic "+strings.ToUpper(literal)+" detail", "medium")
		seed(t, issue513OtherSource, issue513OtherSource, "Synthetic NeedleABCXSuffix decoy", "Synthetic wildcard decoy", "medium")
		comment(t, matching.ID, "comment-only-query-token")
		mutate(t, issue513OtherSource, matching.ID, map[string]any{"expected_revision": "1", "body": "Synthetic private current detail", "source_agent": "history-only-query-token"})
		var storedHistory []gormdb.IssueChange
		require.NoError(t, db.Where("issue_id = ?", matching.ID).Order("revision").Find(&storedHistory).Error)
		require.Len(t, storedHistory, 2)
		require.NotNil(t, storedHistory[1].Attribution.SourceAgent)
		require.True(t, *storedHistory[1].Attribution.SourceAgent == "history-only-query-token")
		for _, test := range []struct {
			name, query string
			expected    []int64
		}{
			{"case_insensitive_literal_wildcards", strings.ToLower(literal), []int64{matching.ID, bodyID}},
			{"ordinary_substring", "bOdY-OnLy", []int64{bodyID}},
			{"does_not_search_metadata", "metadata-only-query-token", []int64{}},
			{"does_not_search_comments", "comment-only-query-token", []int64{}},
			{"does_not_search_history", "history-only-query-token", []int64{}},
		} {
			t.Run(test.name, func(t *testing.T) {
				page := list(t, url.Values{"project": {issue513OtherSource}, "q": {test.query}})
				assert.ElementsMatch(t, test.expected, issue513PagingIDs(t, issue513PagingRecords(t, page, "issues")))
				issue513PagingInt(t, page, "total", len(test.expected))
			})
		}
		t.Run("all_filters_are_anded", func(t *testing.T) {
			query := url.Values{"project": {issue513OtherSource}, "source_project": {issue513OtherSource}, "source_agent": {"synthetic-query-agent"}, "status": {"open,reopened"}, "type": {"task"}, "owner_label": {"docs"}, "recipient_label": {"next"}, "q": {strings.ToLower(literal)}}
			page := list(t, query)
			assert.Equal(t, []int64{matching.ID}, issue513PagingIDs(t, issue513PagingRecords(t, page, "issues")))
			issue513PagingInt(t, page, "total", 1)
			query.Set("owner_label", "Docs")
			page = list(t, query)
			assert.Zero(t, len(issue513PagingRecords(t, page, "issues")), "labels remain exact case-sensitive attribution, not principals")
			issue513PagingInt(t, page, "total", 0)
		})
	})

	t.Run("comments_and_history_have_independent_pages", func(t *testing.T) {
		card := create(t, issue513Source, map[string]any{"target_project": issue513Source, "title": "Synthetic history paging card", "body": "Synthetic private history detail", "metadata": map[string]any{"private_marker": "synthetic-metadata-retained-in-detail"}})
		for revision := 1; revision < 205; revision++ {
			result := mutate(t, issue513Source, card.ID, map[string]any{"expected_revision": strconv.Itoa(revision), "title": fmt.Sprintf("Synthetic history step %03d", revision)})
			require.Equal(t, strconv.Itoa(revision+1), result.Revision)
			require.True(t, result.Changed)
		}
		var history []gormdb.IssueChange
		require.NoError(t, db.Where("issue_id = ?", card.ID).Order("revision").Find(&history).Error)
		require.Len(t, history, 205, "history denominator must come from real accepted CAS transactions")
		commentIDs := make([]int64, 0, 205)
		for index := range 200 {
			commentIDs = append(commentIDs, comment(t, card.ID, fmt.Sprintf("Synthetic private comment %03d", index)))
		}
		t.Run("legacy_200_small_comments_remain_complete", func(t *testing.T) {
			page := detail(t, card.ID, nil)
			assert.Equal(t, commentIDs, issue513PagingIDs(t, issue513PagingRecords(t, page, "comments")))
			issue513PagingInt(t, page, "comment_count", 200)
		})
		for index := 200; index < 205; index++ {
			commentIDs = append(commentIDs, comment(t, card.ID, fmt.Sprintf("Synthetic private comment %03d", index)))
		}
		_, persisted, err := service.issueStore.GetIssue(t.Context(), card.ID)
		require.NoError(t, err)
		require.Equal(t, commentIDs, func() []int64 {
			ids := make([]int64, len(persisted))
			for index, row := range persisted {
				ids[index] = row.ID
			}
			return ids
		}(), "legal store seed preserves ascending comment order")
		for _, test := range []struct {
			name           string
			co, cl, ho, hl int
			defaults       bool
		}{
			{"defaults_50", 0, 50, 0, 50, true},
			{"independent_offsets_49_199", 49, 3, 199, 2, false},
			{"independent_offsets_199_49", 199, 2, 49, 3, false},
			{"maximum_200", 0, 200, 0, 200, false},
			{"final_five", 200, 200, 200, 200, false},
			{"empty_at_nonzero_offset", 205, 50, 205, 50, false},
		} {
			t.Run(test.name, func(t *testing.T) {
				query := url.Values{"detail_mode": {"paged"}}
				if !test.defaults {
					query.Set("comments_offset", strconv.Itoa(test.co))
					query.Set("comments_limit", strconv.Itoa(test.cl))
					query.Set("history_offset", strconv.Itoa(test.ho))
					query.Set("history_limit", strconv.Itoa(test.hl))
				}
				page := detail(t, card.ID, query)
				comments := issue513PagingRecords(t, page, "comments")
				history := issue513PagingRecords(t, page, "history")
				ce, he := min(test.co+test.cl, 205), min(test.ho+test.hl, 205)
				assert.Equal(t, commentIDs[test.co:ce], issue513PagingIDs(t, comments), "comments must use their own offset and limit")
				expectedHistory := make([]string, 0, he-test.ho)
				for index := test.ho; index < he; index++ {
					expectedHistory = append(expectedHistory, strconv.Itoa(index+1))
				}
				assert.Equal(t, expectedHistory, issue513PagingRevisions(t, history), "history must use its own ascending revision page")
				for _, field := range []struct {
					key   string
					value int
				}{{"comment_count", 205}, {"history_count", 205}, {"comments_offset", test.co}, {"comments_limit", test.cl}, {"history_offset", test.ho}, {"history_limit", test.hl}} {
					issue513PagingInt(t, page, field.key, field.value)
				}
				var cn, hn *int
				if ce < 205 {
					cn = &ce
				}
				if he < 205 {
					hn = &he
				}
				issue513PagingNext(t, page, "comments_next_offset", cn)
				issue513PagingNext(t, page, "history_next_offset", hn)
				issue513PagingBool(t, page, "comments_complete", cn == nil)
				issue513PagingBool(t, page, "history_complete", hn == nil)
				issue513PagingBool(t, page, "comments_oversized", false)
				issue513PagingBool(t, page, "history_oversized", false)
				serialized, err := json.Marshal(comments)
				require.NoError(t, err)
				assert.LessOrEqual(t, len(serialized), issue513PageTarget)
				serialized, err = json.Marshal(history)
				require.NoError(t, err)
				assert.LessOrEqual(t, len(serialized), issue513PageTarget)
			})
		}
		t.Run("legacy_205_refuses_without_partial_data", func(t *testing.T) {
			response := issue513PagingCall(t, f, f.master, detailPath(card.ID, nil))
			result := issue513PagingError(t, response, http.StatusConflict, "DETAIL_PAGING_REQUIRED")
			for _, field := range []string{"issue", "comments", "history", "metadata"} {
				_, present := result[field]
				assert.False(t, present, "paging-required must not return partial %s", field)
			}
			assert.True(t, strings.Contains(response.Body.String(), "detail_mode=paged"), "refusal must name executable paged recovery")
		})
		t.Run("unrelated_canonical_project_is_body_free_not_found", func(t *testing.T) {
			response := issue513PagingCall(t, f, f.master, detailPath(card.ID, url.Values{"project": {projectD}, "detail_mode": {"paged"}, "comments_offset": {"50"}, "history_offset": {"50"}}))
			assert.Equal(t, http.StatusNotFound, response.Code)
			missing := issue513PagingCall(t, f, f.master, detailPath(9223372036854775807, url.Values{"project": {projectD}, "detail_mode": {"paged"}}))
			assert.Equal(t, missing.Code, response.Code)
			assert.True(t, response.Body.String() == missing.Body.String(), "unreadable and missing must have the same body-free result")
			for _, forbidden := range []string{"Synthetic private history detail", "Synthetic private comment", "synthetic-metadata-retained-in-detail", `"issue"`, `"comments"`, `"history"`, `"metadata"`, `"actor"`, `"comment_count"`, `"history_count"`, `"revision"`, `"receipt"`} {
				assert.False(t, strings.Contains(response.Body.String(), forbidden), "not-found must suppress every protected detail/count/actor/receipt projection")
			}
		})
	})

	t.Run("comment_byte_target_and_oversized_singleton", func(t *testing.T) {
		id := seed(t, issue513Source, issue513Source, "Synthetic byte-target card", "Synthetic private byte-target detail", "medium")
		medium := strings.Repeat("s", 1536*1024)
		ids := []int64{comment(t, id, medium), comment(t, id, medium), comment(t, id, medium)}
		t.Run("stops_before_target_and_continues_without_skips", func(t *testing.T) {
			next := 2
			for _, test := range []struct {
				offset, count int
				next          *int
			}{{0, 2, &next}, {2, 1, nil}} {
				page := detail(t, id, url.Values{"detail_mode": {"paged"}, "comments_offset": {strconv.Itoa(test.offset)}, "comments_limit": {"200"}, "history_limit": {"1"}})
				records := issue513PagingRecords(t, page, "comments")
				assert.Equal(t, ids[test.offset:test.offset+test.count], issue513PagingIDs(t, records))
				serialized, err := json.Marshal(records)
				require.NoError(t, err)
				assert.LessOrEqual(t, len(serialized), issue513PageTarget)
				issue513PagingInt(t, page, "comment_count", 3)
				issue513PagingInt(t, page, "comments_offset", test.offset)
				issue513PagingInt(t, page, "comments_limit", 200)
				issue513PagingNext(t, page, "comments_next_offset", test.next)
				issue513PagingBool(t, page, "comments_complete", test.next == nil)
				issue513PagingBool(t, page, "comments_oversized", false)
			}
		})
		largeID := seed(t, issue513Source, issue513Source, "Synthetic oversized history-compatible card", "Synthetic private singleton detail", "medium")
		large := strings.Repeat("h", issue513PageTarget+257)
		largeComment := comment(t, largeID, large)
		tailComment := comment(t, largeID, "Synthetic private singleton continuation")
		_, stored, err := service.issueStore.GetIssue(t.Context(), largeID)
		require.NoError(t, err)
		require.Len(t, stored, 2)
		require.True(t, stored[0].Body == large, "legal historical seed must retain exact oversized bytes")
		serialized, err := json.Marshal(stored[0])
		require.NoError(t, err)
		require.Greater(t, len(serialized), issue513PageTarget)
		t.Run("intact_oversized_singleton_then_continuation", func(t *testing.T) {
			first := detail(t, largeID, url.Values{"detail_mode": {"paged"}, "comments_limit": {"200"}})
			records := issue513PagingRecords(t, first, "comments")
			assert.Equal(t, []int64{largeComment}, issue513PagingIDs(t, records), "oversized record must be isolated, never skipped or combined")
			if len(records) > 0 {
				var body string
				require.NoError(t, json.Unmarshal(records[0]["body"], &body))
				assert.True(t, body == large, "oversized comment must be returned byte-for-byte intact")
			}
			next := 1
			issue513PagingNext(t, first, "comments_next_offset", &next)
			issue513PagingInt(t, first, "comment_count", 2)
			issue513PagingBool(t, first, "comments_oversized", true)
			issue513PagingBool(t, first, "comments_complete", false)
			tail := detail(t, largeID, url.Values{"detail_mode": {"paged"}, "comments_offset": {"1"}, "comments_limit": {"200"}})
			assert.Equal(t, []int64{tailComment}, issue513PagingIDs(t, issue513PagingRecords(t, tail, "comments")))
			issue513PagingInt(t, tail, "comments_offset", 1)
			issue513PagingNext(t, tail, "comments_next_offset", nil)
			issue513PagingBool(t, tail, "comments_oversized", false)
			issue513PagingBool(t, tail, "comments_complete", true)
		})
		t.Run("legacy_over_4MiB_refuses_without_partial_data", func(t *testing.T) {
			response := issue513PagingCall(t, f, f.master, detailPath(largeID, nil))
			result := issue513PagingError(t, response, http.StatusConflict, "DETAIL_PAGING_REQUIRED")
			for _, field := range []string{"issue", "comments", "history"} {
				_, present := result[field]
				assert.False(t, present, "legacy refusal must not expose partial %s", field)
			}
			assert.False(t, strings.Contains(response.Body.String(), large), "legacy refusal must not release oversized content")
			assert.True(t, strings.Contains(response.Body.String(), "detail_mode=paged"), "refusal must name executable paged recovery")
		})
	})

	t.Run("read_authentication_and_existing_selector_controls", func(t *testing.T) {
		for _, test := range []struct {
			name, path string
			credential issue513ActorCredential
			status     int
		}{
			{"anonymous_list_denied", "/api/issues", issue513ActorCredential{}, http.StatusUnauthorized},
			{"anonymous_detail_denied", "/api/issues/1?detail_mode=paged", issue513ActorCredential{}, http.StatusUnauthorized},
			{"invalid_bearer_list_denied", "/api/issues", issue513ActorCredential{bearer: "synthetic-invalid-credential"}, http.StatusUnauthorized},
			{"persisted_readonly_keycard_can_list", "/api/issues?limit=1", f.readOnly, http.StatusOK},
			{"invalid_canonical_selector_denied", "/api/issues?project=not-a-canonical-project", f.master, http.StatusBadRequest},
			{"duplicate_detail_selector_denied", "/api/issues/1?project=" + issue513Source + "&project=" + projectD, f.master, http.StatusBadRequest},
			{"malformed_issue_id_denied", "/api/issues/not-an-id", f.master, http.StatusBadRequest},
		} {
			t.Run(test.name, func(t *testing.T) {
				response := issue513PagingCall(t, f, test.credential, test.path)
				assert.Equal(t, test.status, response.Code)
			})
		}
	})

	t.Run("invalid_paging_arguments_are_not_silent_defaults", func(t *testing.T) {
		for _, test := range []struct {
			name, key, value string
			detail           bool
		}{
			{"list_zero_limit", "limit", "0", false},
			{"list_over_limit", "limit", "201", false},
			{"list_negative_offset", "offset", "-1", false},
			{"list_fraction_offset", "offset", "1.5", false},
			{"list_overflow_offset", "offset", "9223372036854775808", false},
			{"comments_zero_limit", "comments_limit", "0", true},
			{"comments_over_limit", "comments_limit", "201", true},
			{"comments_negative_offset", "comments_offset", "-1", true},
			{"comments_fraction_offset", "comments_offset", "1.5", true},
			{"history_zero_limit", "history_limit", "0", true},
			{"history_over_limit", "history_limit", "201", true},
			{"history_negative_offset", "history_offset", "-1", true},
			{"history_noninteger_limit", "history_limit", "no", true},
		} {
			t.Run(test.name, func(t *testing.T) {
				query := url.Values{test.key: {test.value}}
				path := "/api/issues?" + query.Encode()
				if test.detail {
					query.Set("detail_mode", "paged")
					path = detailPath(1, query)
				}
				response := issue513PagingCall(t, f, f.master, path)
				issue513PagingError(t, response, http.StatusBadRequest, "INVALID_ARGUMENT")
			})
		}
	})
}
