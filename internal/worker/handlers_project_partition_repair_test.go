package worker

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestProjectPartitionRepairPublicConsumers(t *testing.T) {
	db, closeDB := setupProjectTestDB(t)
	defer closeDB()
	require.NoError(t, db.AutoMigrate(&gormdb.Issue{}, &gormdb.IssueComment{}, &gormdb.ConsolidationMembership{}))
	backing := &gormdb.Store{DB: db}
	service := &Service{store: backing, sessionStore: gormdb.NewSessionStore(backing), issueStore: gormdb.NewIssueStore(db), vault: newTestVault(t)}
	a, b, foreign := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, project := range []gormdb.Project{
		{ID: a, DisplayName: sql.NullString{String: "Existing Alpha", Valid: true}},
		{ID: b, DisplayName: sql.NullString{String: "Existing Beta", Valid: true}},
		{ID: foreign, DisplayName: sql.NullString{String: "Unrelated", Valid: true}},
		{ID: "former-http-alpha", DisplayName: sql.NullString{String: "Former HTTP Alpha", Valid: true}, GitRemote: sql.NullString{String: "https://example.invalid/same.git", Valid: true}},
		{ID: "former-http-beta", DisplayName: sql.NullString{String: "Former HTTP Beta", Valid: true}, GitRemote: sql.NullString{String: "https://example.invalid/same.git", Valid: true}},
	} {
		require.NoError(t, db.Create(&project).Error)
	}
	issue := gormdb.Issue{Title: "Historical issue", Body: "preserved public issue body", SourceProject: "former-http-alpha", TargetProject: "former-http-beta", SourceAgent: "agent", Labels: []string{}}
	require.NoError(t, db.Create(&issue).Error)
	require.NoError(t, db.Create(&gormdb.IssueComment{IssueID: issue.ID, AuthorProject: "former-http-alpha", AuthorAgent: "agent", Body: "preserved public history"}).Error)
	for _, role := range []struct{ field, project string }{{"source_project", a}, {"target_project", b}} {
		key, err := json.Marshal(map[string]int64{"id": issue.ID})
		require.NoError(t, err)
		field, err := json.Marshal([]string{role.field})
		require.NoError(t, err)
		project := role.project
		require.NoError(t, db.Create(&gormdb.ConsolidationMembership{Reference: "urn:engram:fixture:issue:" + role.field, Table: "issues", RowKey: gormdb.JSONRaw(key), SourceRowKey: gormdb.JSONRaw(key), FieldPath: gormdb.JSONRaw(field), ProjectID: &project, PlanFingerprint: "synthetic-existing-membership"}).Error)
	}
	router := chi.NewRouter()
	router.Get("/api/projects", service.handleGetProjects)
	router.Get("/api/issues", service.handleListIssues)
	router.Get("/api/issues/{id}", service.handleGetIssue)
	request := func(target string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req = req.WithContext(auth.WithIdentity(req.Context(), auth.AuthDisabled()))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	t.Run("ordinary_Projects_entry_discovers_both_former_names", func(t *testing.T) {
		response := request("/api/projects")
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var catalogue projectListResponse
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &catalogue))
		labels := map[string]bool{}
		for _, item := range catalogue.Items {
			labels[item.DisplayName] = true
		}
		assert.True(t, labels["Former HTTP Alpha"], "ordinary entry has no supplied hidden project UUID")
		assert.True(t, labels["Former HTTP Beta"], "same remote does not merge distinct historical partitions")
	})
	t.Run("Incoming_and_Outgoing_return_canonical_role_DTOs", func(t *testing.T) {
		for _, selector := range []string{"project=" + b, "source_project=" + a} {
			response := request("/api/issues?" + selector)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var result struct {
				Issues []map[string]json.RawMessage `json:"issues"`
				Total  int64                        `json:"total"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			require.EqualValues(t, 1, result.Total, "agent-created retained issue belongs in both independently filtered lists")
			require.Len(t, result.Issues, 1)
			for field, expected := range map[string]string{"source_project_id": a, "target_project_id": b} {
				var actual *string
				raw, exists := result.Issues[0][field]
				if assert.True(t, exists, "canonical role DTO must not make the client compare a raw legacy label with a selected UUID") {
					require.NoError(t, json.Unmarshal(raw, &actual))
					assert.Equal(t, &expected, actual)
				}
			}
		}
	})
	t.Run("scoped_issue_detail_refuses_unrelated_project", func(t *testing.T) {
		response := request(fmt.Sprintf("/api/issues/%d?project=%s", issue.ID, foreign))
		assert.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, response.Code, "an unrelated canonical selection must not disclose a historical issue body")
		assert.NotContains(t, response.Body.String(), issue.Body)
		assert.NotContains(t, response.Body.String(), "preserved public history")
	})
	t.Run("either_actual_role_can_read_body_and_history", func(t *testing.T) {
		for _, project := range []string{a, b} {
			response := request(fmt.Sprintf("/api/issues/%d?project=%s", issue.ID, project))
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), issue.Body)
			require.Contains(t, response.Body.String(), "preserved public history")
		}
	})
	t.Run("legacy_selector_still_rejected", func(t *testing.T) {
		response := request("/api/issues?project=former-http-beta")
		require.Equal(t, http.StatusBadRequest, response.Code)
	})
}
