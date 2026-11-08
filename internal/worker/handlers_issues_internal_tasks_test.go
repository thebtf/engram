package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/issuefixture"
	gormlib "gorm.io/gorm"
)

const (
	issue513Source      = "51300000-0000-4000-8000-000000000001"
	issue513OtherSource = "51300000-0000-4000-8000-000000000002"
	issue513Metadata    = `{"outcome":{"state":"pending","next":["read","resume"]},"unknown_extension":{"safe_integer":9007199254740991,"fraction":0.1,"enabled":true,"optional":null}}`
)

// Uses the existing migrated, schema-isolated fixture, not the shared issue HTTP
// fixture. DATABASE_DSN must name a disposable PostgreSQL/pgvector test database.
func newIssue513HTTPFixture(t *testing.T) (*Service, http.Handler) {
	t.Helper()
	store := openWorkerUCIContextCompositionStore(t)
	db := store.GetDB()
	var schema string
	require.NoError(t, db.Raw(`SELECT current_schema()`).Scan(&schema).Error)
	require.NotEqual(t, "public", schema)
	for _, table := range []string{"projects", "issues", "issue_comments"} {
		var owner string
		require.NoError(t, db.Raw(`SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = to_regclass(?)`, table).Scan(&owner).Error)
		require.Equal(t, schema, owner, "fixture table %s must not resolve to public", table)
	}
	service := &Service{
		store:        store,
		issueStore:   gormdb.NewIssueStore(db),
		sessionStore: gormdb.NewSessionStore(store),
	}
	for _, project := range []string{issue513Source, issue513OtherSource} {
		require.NoError(t, db.Create(&gormdb.Project{
			ID:             project,
			ProjectKey:     sql.NullString{String: project, Valid: true},
			DisplayName:    sql.NullString{String: "Synthetic issue 513 source", Valid: true},
			IdentityStatus: sql.NullString{String: "active", Valid: true},
			LegacyIDs:      []string{},
		}).Error)
		exists, err := service.sessionStore.CanonicalProjectExists(t.Context(), project)
		require.NoError(t, err)
		require.True(t, exists, "canonical source admission must succeed before exercising retry behavior")
		resolved, err := service.issueStore.ResolveProject(t.Context(), project)
		require.NoError(t, err)
		require.Equal(t, project, resolved)
	}
	router := chi.NewRouter()
	router.Post("/api/issues", service.handleCreateIssue)
	router.Get("/api/issues/{id}", service.handleGetIssue)
	router.Patch("/api/issues/{id}", service.handleUpdateIssue)
	return service, router
}

func issue513HTTPCall(t *testing.T, router http.Handler, actor auth.Identity, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithIdentity(req.Context(), actor))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func issue513CreateBody(source, key, title, metadata string) string {
	return fmt.Sprintf(`{"source_project":%q,"target_project":%q,"source_agent":"synthetic-task-author","created_by_session":"synthetic-task-session","request_key":%q,"title":%q,"body":"Record a resumable synthetic outcome.","type":"task","priority":"medium","labels":["internal","synthetic"],"metadata":%s}`, source, issue513Source, key, title, metadata)
}

func issue513CreatedID(t *testing.T, response *httptest.ResponseRecorder) int64 {
	t.Helper()
	require.Equal(t, http.StatusCreated, response.Code, "authorized initial create must reach persistence: %s", response.Body.String())
	var result struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.Positive(t, result.ID)
	return result.ID
}

func issue513JSON(t *testing.T, response *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var result map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	return result
}

func issue513Count(t *testing.T, service *Service) int64 {
	t.Helper()
	var count int64
	require.NoError(t, service.store.GetDB().Model(&gormdb.Issue{}).Count(&count).Error)
	return count
}

func issue513Detail(t *testing.T, router http.Handler, actor auth.Identity, id int64) map[string]json.RawMessage {
	t.Helper()
	response := issue513HTTPCall(t, router, actor, http.MethodGet, fmt.Sprintf("/api/issues/%d?detail_mode=paged", id), "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	return issue513JSON(t, response)
}

func TestIssueHTTPInternalTaskContract(t *testing.T) {
	service, router := newIssue513HTTPFixture(t)
	actor := auth.Client("read-write", "synthetic-issue-513-actor-a")
	otherActor := auth.Client("read-write", "synthetic-issue-513-actor-b")

	// Construct the pre-admission historical projection; restore the exact
	// INSERT guards before any ordinary request or conservation snapshot.
	var oldID, oldCommentID int64
	err := issuefixture.SeedHistorical(service.store.GetDB(), func(tx *gormlib.DB) error {
		store := gormdb.NewIssueStore(tx)
		var err error
		oldID, err = store.CreateIssue(t.Context(), &gormdb.Issue{
			Title: "Preexisting synthetic issue", Body: "Preserve exact old body.\nSecond line.",
			Status: "acknowledged", Type: "bug", Priority: "high",
			SourceProject: issue513Source, TargetProject: issue513Source,
			SourceAgent: "historical-synthetic-agent", CreatedBySession: "historical-synthetic-session",
			CreatorKeycardID: actor.KeycardID, Labels: []string{"retained"},
		})
		if err != nil {
			return err
		}
		oldCommentID, err = store.AddComment(t.Context(), oldID, &gormdb.IssueComment{
			AuthorProject: issue513Source, AuthorAgent: "historical-comment-agent",
			Body: "Preserve exact old comment.\nСинтетический результат.",
		})
		return err
	})
	require.NoError(t, err)
	oldIssue, oldComments, err := service.issueStore.GetIssue(t.Context(), oldID)
	require.NoError(t, err)
	require.Len(t, oldComments, 1)
	require.Equal(t, oldCommentID, oldComments[0].ID)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		current, comments, readErr := service.issueStore.GetIssue(ctx, oldID)
		require.NoError(t, readErr)
		assert.Equal(t, oldIssue, current, "task requests must preserve every preexisting issue field and attribution")
		assert.Equal(t, oldComments, comments, "task requests must preserve preexisting comment IDs, bytes and attribution")
	})

	t.Run("same_actor_source_key_reuses_card_and_roundtrips_metadata", func(t *testing.T) {
		before := issue513Count(t, service)
		body := issue513CreateBody(issue513Source, "synthetic-retry-intent", "Resume one synthetic task", issue513Metadata)
		first := issue513HTTPCall(t, router, actor, http.MethodPost, "/api/issues", body)
		id := issue513CreatedID(t, first)
		retry := issue513HTTPCall(t, router, actor, http.MethodPost, "/api/issues", body)
		var replay struct {
			ID       int64  `json:"id"`
			Revision string `json:"revision"`
			Replayed bool   `json:"replayed"`
		}
		require.NoError(t, json.Unmarshal(retry.Body.Bytes(), &replay))
		assert.Equal(t, id, replay.ID, "lost-response retry must return the original ID, not create another card")
		assert.Equal(t, before+1, issue513Count(t, service), "unchanged retry must persist exactly one card")
		assert.Equal(t, http.StatusOK, retry.Code)
		assert.True(t, replay.Replayed)
		assert.Equal(t, "1", replay.Revision)
		firstJSON := issue513JSON(t, first)
		assert.JSONEq(t, `"1"`, string(firstJSON["revision"]))
		assert.JSONEq(t, `false`, string(firstJSON["replayed"]))
		detail := issue513Detail(t, router, actor, id)
		var issue map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(detail["issue"], &issue))
		assert.JSONEq(t, issue513Metadata, string(issue["metadata"]), "retain unknown nested keys and admitted values")
		assert.JSONEq(t, `"1"`, string(issue["revision"]))
		var comments []json.RawMessage
		require.NoError(t, json.Unmarshal(detail["comments"], &comments))
		assert.Empty(t, comments, "create/retry must not invent comments")
	})

	t.Run("changed_payload_conflicts_without_disclosure_or_second_card", func(t *testing.T) {
		before := issue513Count(t, service)
		key := "synthetic-conflicting-intent"
		id := issue513CreatedID(t, issue513HTTPCall(t, router, actor, http.MethodPost, "/api/issues",
			issue513CreateBody(issue513Source, key, "Original synthetic intent", issue513Metadata)))
		original := issue513Detail(t, router, actor, id)
		conflict := issue513HTTPCall(t, router, actor, http.MethodPost, "/api/issues",
			issue513CreateBody(issue513Source, key, "Changed synthetic intent", `{"outcome":{"state":"changed"}}`))
		assert.Equal(t, http.StatusConflict, conflict.Code, "same key with changed logical payload must conflict")
		assert.Equal(t, before+1, issue513Count(t, service), "conflict must not create another card")
		var result struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(conflict.Body.Bytes(), &result))
		assert.Equal(t, "IDEMPOTENCY_CONFLICT", result.Error.Code)
		for _, forbidden := range []string{`"id"`, `"metadata"`, `"revision"`, `"digest"`, `"payload"`, "Original synthetic intent", "Changed synthetic intent", "unknown_extension"} {
			assert.NotContains(t, conflict.Body.String(), forbidden, "conflict must not disclose a receipt or either payload")
		}
		current := issue513Detail(t, router, actor, id)
		assert.JSONEq(t, string(original["issue"]), string(current["issue"]))
		assert.JSONEq(t, string(original["comments"]), string(current["comments"]))
	})

	for _, test := range []struct {
		name   string
		actor  auth.Identity
		source string
	}{
		{name: "different_existing_actor", actor: otherActor, source: issue513Source},
		{name: "different_active_canonical_source", actor: actor, source: issue513OtherSource},
	} {
		t.Run(test.name+"_cannot_replay_first_receipt", func(t *testing.T) {
			before := issue513Count(t, service)
			key := "synthetic-isolated-intent-" + test.name
			firstID := issue513CreatedID(t, issue513HTTPCall(t, router, actor, http.MethodPost, "/api/issues",
				issue513CreateBody(issue513Source, key, "First actor synthetic task", issue513Metadata)))
			body := issue513CreateBody(test.source, key, "Independent synthetic task", `{"independent":{"state":"pending"}}`)
			independent := issue513HTTPCall(t, router, test.actor, http.MethodPost, "/api/issues", body)
			secondID := issue513CreatedID(t, independent)
			assert.NotEqual(t, firstID, secondID, "another namespace must not receive the first actor/source receipt")
			assert.JSONEq(t, `false`, string(issue513JSON(t, independent)["replayed"]))
			retry := issue513HTTPCall(t, router, test.actor, http.MethodPost, "/api/issues", body)
			var replay struct {
				ID       int64 `json:"id"`
				Replayed bool  `json:"replayed"`
			}
			require.NoError(t, json.Unmarshal(retry.Body.Bytes(), &replay))
			assert.Equal(t, http.StatusOK, retry.Code)
			assert.Equal(t, secondID, replay.ID)
			assert.True(t, replay.Replayed)
			assert.Equal(t, before+2, issue513Count(t, service))
			assert.NotContains(t, independent.Body.String(), "First actor synthetic task")
			assert.NotContains(t, independent.Body.String(), "unknown_extension")
		})
	}

	t.Run("comment_metadata_roundtrips_without_changing_card_revision", func(t *testing.T) {
		id := issue513CreatedID(t, issue513HTTPCall(t, router, actor, http.MethodPost, "/api/issues",
			issue513CreateBody(issue513Source, "synthetic-comment-intent", "Record a synthetic outcome", issue513Metadata)))
		const metadata = `{"outcome":{"state":"partial","next":"read current card"},"unknown_comment_key":[true,null,1]}`
		body := fmt.Sprintf(`{"source_project":%q,"source_agent":"synthetic-outcome-author","comment":"Synthetic resumable outcome.","comment_metadata":%s}`, issue513Source, metadata)
		response := issue513HTTPCall(t, router, actor, http.MethodPatch, fmt.Sprintf("/api/issues/%d", id), body)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		detail := issue513Detail(t, router, actor, id)
		var comments []struct {
			ID       int64           `json:"id"`
			IssueID  int64           `json:"issue_id"`
			Body     string          `json:"body"`
			Metadata json.RawMessage `json:"metadata"`
		}
		require.NoError(t, json.Unmarshal(detail["comments"], &comments))
		require.Len(t, comments, 1)
		assert.Positive(t, comments[0].ID)
		assert.Equal(t, id, comments[0].IssueID)
		assert.Equal(t, "Synthetic resumable outcome.", comments[0].Body)
		assert.JSONEq(t, metadata, string(comments[0].Metadata))
		var result struct {
			CommentID int64  `json:"comment_id"`
			Revision  string `json:"revision"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
		assert.Equal(t, comments[0].ID, result.CommentID)
		assert.Equal(t, "1", result.Revision)
		var issue map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(detail["issue"], &issue))
		assert.JSONEq(t, `"1"`, string(issue["revision"]))
		assert.JSONEq(t, issue513Metadata, string(issue["metadata"]))
	})

	for _, test := range []struct {
		name     string
		metadata string
	}{
		{name: "duplicate_raw_member", metadata: `{"outcome":{"state":"pending","state":"changed"}}`},
		{name: "unsafe_integer", metadata: `{"unknown_extension":{"counter":9007199254740993}}`},
	} {
		t.Run(test.name+"_is_rejected_before_mutation", func(t *testing.T) {
			before := issue513Count(t, service)
			body := issue513CreateBody(issue513Source, "synthetic-invalid-"+test.name, "Reject invalid synthetic metadata", test.metadata)
			require.True(t, json.Valid([]byte(body)), "the discriminator must reach JSON value admission, not fail JSON syntax")
			response := issue513HTTPCall(t, router, actor, http.MethodPost, "/api/issues", body)
			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.Equal(t, before, issue513Count(t, service), "invalid metadata must not leave a card")
			var result struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			assert.Equal(t, "INVALID_ARGUMENT", result.Error.Code)
			assert.NotContains(t, response.Body.String(), `"id"`)
		})
	}

	t.Run("preexisting_issue_and_comment_remain_readable", func(t *testing.T) {
		detail := issue513Detail(t, router, actor, oldID)
		var issue gormdb.Issue
		encodedComments, err := json.Marshal(oldComments)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(detail["issue"], &issue))
		assert.Equal(t, oldID, issue.ID)
		assert.Equal(t, oldIssue.Title, issue.Title)
		assert.Equal(t, oldIssue.Body, issue.Body)
		assert.Equal(t, oldIssue.Status, issue.Status)
		assert.Equal(t, oldIssue.SourceProject, issue.SourceProject)
		assert.Equal(t, oldIssue.TargetProject, issue.TargetProject)
		assert.Equal(t, oldIssue.SourceAgent, issue.SourceAgent)
		assert.Equal(t, oldIssue.CreatedBySession, issue.CreatedBySession)
		assert.JSONEq(t, string(encodedComments), string(detail["comments"]))
	})
}
