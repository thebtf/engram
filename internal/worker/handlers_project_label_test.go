package worker

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	auth "github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/worker/sse"
	"golang.org/x/crypto/bcrypt"
)

// The fixture requires an explicitly selected disposable loopback PostgreSQL database.
// The helper creates and removes a private schema and refuses a populated public schema.
func TestProjectDisplayNamePatchHTTPPostgres(t *testing.T) {
	store := openContextSearchPrivacyStore(t)
	db := store.GetDB()
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	const master = "project-label-operator-test-only"
	tokenAuth, err := NewTokenAuth(master)
	require.NoError(t, err)
	tokens := gormdb.NewTokenStore(store)
	validator := auth.NewValidator(master, tokens)
	tokenAuth.SetValidator(validator)
	issueKeycard := func(scope string) string {
		t.Helper()
		raw := auth.TokenRawPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
		hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.MinCost)
		require.NoError(t, err)
		_, err = tokens.CreateWithPrincipal(t.Context(), gormdb.TokenCreatePrincipalInput{
			Name: "project-label-" + scope, TokenHash: string(hash),
			TokenPrefix: raw[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen],
			Scope:       scope, Principal: "agent/project-label-" + scope, PrincipalKind: "agent",
		})
		require.NoError(t, err)
		identity, err := validator.Validate(t.Context(), raw)
		require.NoError(t, err)
		require.Equal(t, auth.Role(scope), identity.Role)
		return raw
	}
	readOnly, readWrite := issueKeycard("read-only"), issueKeycard("read-write")

	service := &Service{router: chi.NewRouter(), tokenAuth: tokenAuth, store: store, vault: newTestVault(t), sessionStore: gormdb.NewSessionStore(store), mcpHealth: mcp.NewMCPHealth(), sseBroadcaster: sse.NewBroadcaster()}
	service.ready.Store(true)
	service.setupMiddleware()
	service.setupRoutes()

	t.Run("catalog_exposes_exact_nullable_value", func(t *testing.T) {
		values := []sql.NullString{{}, {String: "", Valid: true}, {String: "Registered project", Valid: true}, {String: "  ", Valid: true}}
		ids := make([]string, len(values))
		for i, value := range values {
			ids[i] = uuid.NewString()
			require.NoError(t, db.Create(&gormdb.Project{ID: ids[i], DisplayName: value}).Error)
		}
		removedID, mergedID := uuid.NewString(), uuid.NewString()
		now := time.Now().UTC()
		require.NoError(t, db.Create(&gormdb.Project{ID: removedID, RemovedAt: &now}).Error)
		require.NoError(t, db.Create(&gormdb.Project{ID: mergedID, IdentityStatus: sql.NullString{String: "merged", Valid: true}}).Error)
		req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
		req.Header.Set("Authorization", "Bearer "+master)
		rec := httptest.NewRecorder()
		service.router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var response struct {
			Items []map[string]any `json:"items"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		items := make(map[string]map[string]any)
		for _, item := range response.Items {
			id, ok := item["project_id"].(string)
			require.True(t, ok)
			items[id] = item
		}
		for i, id := range ids {
			item, found := items[id]
			require.True(t, found, "catalog missing %s", id)
			raw, present := item["display_name"]
			assert.True(t, present, "raw name must always be present")
			if values[i].Valid {
				assert.Equal(t, values[i].String, raw)
			} else {
				assert.Nil(t, raw)
			}
			assert.Equal(t, "Registered project", item["name"])
		}
		assert.NotContains(t, items, removedID)
		assert.NotContains(t, items, mergedID)
	})

	t.Run("two_writers_same_precondition", func(t *testing.T) {
		id := uuid.NewString()
		require.NoError(t, db.Create(&gormdb.Project{ID: id, DisplayName: sql.NullString{String: "original", Valid: true}}).Error)
		start := make(chan struct{})
		results := make(chan int, 2)
		var ready sync.WaitGroup
		ready.Add(2)
		for _, name := range []string{"first", "second"} {
			go func(name string) {
				req := httptest.NewRequest(http.MethodPatch, "/api/projects/"+id, strings.NewReader(fmt.Sprintf(`{"display_name":%q,"expected_display_name":"original"}`, name)))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+master)
				rec := httptest.NewRecorder()
				ready.Done()
				<-start
				service.router.ServeHTTP(rec, req)
				results <- rec.Code
			}(name)
		}
		ready.Wait()
		close(start)
		assert.ElementsMatch(t, []int{http.StatusOK, http.StatusConflict}, []int{<-results, <-results})
		var after gormdb.Project
		require.NoError(t, db.First(&after, "id = ?", id).Error)
		assert.Contains(t, []string{"first", "second"}, after.DisplayName.String)
		assert.True(t, after.DisplayName.Valid)
	})

	t.Run("auth_disabled_synthetic_admin", func(t *testing.T) {
		t.Setenv("ENGRAM_AUTH_DISABLED", "true")
		ta, err := NewTokenAuth("")
		require.NoError(t, err)
		s := &Service{router: chi.NewRouter(), tokenAuth: ta, store: store, mcpHealth: mcp.NewMCPHealth(), sseBroadcaster: sse.NewBroadcaster()}
		s.ready.Store(true)
		s.setupMiddleware()
		s.setupRoutes()
		id := uuid.NewString()
		require.NoError(t, db.Create(&gormdb.Project{ID: id}).Error)
		req := httptest.NewRequest(http.MethodPatch, "/api/projects/"+id, strings.NewReader(`{"display_name":"synthetic admin","expected_display_name":null}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var after gormdb.Project
		require.NoError(t, db.First(&after, "id = ?", id).Error)
		assert.Equal(t, sql.NullString{String: "synthetic admin", Valid: true}, after.DisplayName)
	})

	cases := []struct {
		name, initial, expected, payload, credential                   string
		initialNull, removed, merged, missing, invalidID, expectedNull bool
		status                                                         int
		changed                                                        bool
	}{
		{name: "exact_previous_string", initial: "Same label", expected: "New label", payload: `{"display_name":"New label","expected_display_name":"Same label"}`, status: http.StatusOK, changed: true},
		{name: "null_previous", initialNull: true, expected: "From null", payload: `{"display_name":"From null","expected_display_name":null}`, status: http.StatusOK, changed: true},
		{name: "empty_previous", expected: "From empty", payload: `{"display_name":"From empty","expected_display_name":""}`, status: http.StatusOK, changed: true},
		{name: "restore_null", initial: "current", expectedNull: true, payload: `{"display_name":null,"expected_display_name":"current"}`, status: http.StatusOK, changed: true},
		{name: "restore_empty", initial: "current", payload: `{"display_name":"","expected_display_name":"current"}`, status: http.StatusOK, changed: true},
		{name: "stale_null_rollback", initial: "newer", payload: `{"display_name":null,"expected_display_name":"current"}`, status: http.StatusConflict},
		{name: "empty_does_not_match_null", initialNull: true, payload: `{"display_name":"No change","expected_display_name":""}`, status: http.StatusConflict},
		{name: "null_does_not_match_empty", payload: `{"display_name":"No change","expected_display_name":null}`, status: http.StatusConflict},
		{name: "presentation_fallback_is_not_stored_value", initialNull: true, payload: `{"display_name":"No change","expected_display_name":"Registered project"}`, status: http.StatusConflict},
		{name: "stale_previous", initial: "current", payload: `{"display_name":"No change","expected_display_name":"old"}`, status: http.StatusConflict},
		{name: "missing_project", missing: true, payload: `{"display_name":"No creation","expected_display_name":null}`, status: http.StatusNotFound},
		{name: "removed_project", initial: "Same label", removed: true, payload: `{"display_name":"No resurrection","expected_display_name":"Same label"}`, status: http.StatusNotFound},
		{name: "merged_project", initial: "Same label", merged: true, payload: `{"display_name":"No resurrection","expected_display_name":"Same label"}`, status: http.StatusNotFound},
		{name: "retired_project", initial: "Same label", merged: true, payload: `{"display_name":"No resurrection","expected_display_name":"Same label"}`, status: http.StatusNotFound},
		{name: "anonymous", initial: "Same label", credential: "anonymous", payload: `{"display_name":"No change","expected_display_name":"Same label"}`, status: http.StatusUnauthorized},
		{name: "invalid_token", initial: "Same label", credential: "invalid", payload: `{"display_name":"No change","expected_display_name":"Same label"}`, status: http.StatusUnauthorized},
		{name: "read_only_keycard", initial: "Same label", credential: "read-only", payload: `{"display_name":"No change","expected_display_name":"Same label"}`, status: http.StatusForbidden},
		{name: "non_operator_read_write_keycard", initial: "Same label", credential: "read-write", payload: `{"display_name":"No change","expected_display_name":"Same label"}`, status: http.StatusForbidden},
		{name: "malformed_project_id", initial: "Same label", invalidID: true, payload: `{"display_name":"No change","expected_display_name":"Same label"}`, status: http.StatusBadRequest},
		{name: "missing_precondition", initial: "Same label", payload: `{"display_name":"No change"}`, status: http.StatusBadRequest},
		{name: "missing_name", initial: "Same label", payload: `{"expected_display_name":"Same label"}`, status: http.StatusBadRequest},
		{name: "wrong_precondition_type", initial: "Same label", payload: `{"display_name":"No change","expected_display_name":3}`, status: http.StatusBadRequest},
		{name: "unknown_field", initial: "Same label", payload: `{"display_name":"No change","expected_display_name":"Same label","id":"other"}`, status: http.StatusBadRequest},
		{name: "duplicate_field", initial: "Same label", payload: `{"display_name":"No change","display_name":"other","expected_display_name":"Same label"}`, status: http.StatusBadRequest},
		{name: "control_in_new_label", initial: "Same label", payload: `{"display_name":"bad\nlabel","expected_display_name":"Same label"}`, status: http.StatusBadRequest},
		{name: "legacy_control_label_cannot_be_restored", initial: "old\nlabel", payload: `{"display_name":"new","expected_display_name":"old\nlabel"}`, status: http.StatusBadRequest},
		{name: "malformed_json", initial: "Same label", payload: `{"display_name":`, status: http.StatusBadRequest},
		{name: "trailing_json", initial: "Same label", payload: `{"display_name":"No change","expected_display_name":"Same label"} {}`, status: http.StatusBadRequest},
		{name: "oversized_label", initial: "Same label", payload: fmt.Sprintf(`{"display_name":%q,"expected_display_name":"Same label"}`, strings.Repeat("x", 256)), status: http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, otherID := uuid.NewString(), uuid.NewString()
			row := gormdb.Project{ID: id, DisplayName: sql.NullString{String: tc.initial, Valid: !tc.initialNull}, GitRemote: sql.NullString{String: "example.test/owner/repo", Valid: true}}
			if tc.removed {
				now := time.Now().UTC().Truncate(time.Microsecond)
				row.RemovedAt = &now
			}
			if tc.merged {
				status := "merged"
				if tc.name == "retired_project" {
					status = "retired"
				}
				row.IdentityStatus = sql.NullString{String: status, Valid: true}
			}
			if !tc.missing {
				require.NoError(t, db.Create(&row).Error)
			}
			other := gormdb.Project{ID: otherID, DisplayName: sql.NullString{String: "Same label", Valid: true}}
			require.NoError(t, db.Create(&other).Error)
			documentProject := id
			if tc.missing || tc.removed || tc.merged {
				documentProject = otherID
			}
			doc := gormdb.VersionedDocument{Project: documentProject, Path: "retained.md", Version: 1, Content: "retained history sentinel", ContentHash: "fixture-hash", DocType: "markdown", Metadata: `{}`, Author: "fixture"}
			require.NoError(t, db.Create(&doc).Error)
			var beforeOther gormdb.Project
			var beforeDoc gormdb.VersionedDocument
			require.NoError(t, db.First(&beforeOther, "id = ?", otherID).Error)
			require.NoError(t, db.First(&beforeDoc, doc.ID).Error)
			var before gormdb.Project
			if !tc.missing {
				require.NoError(t, db.First(&before, "id = ?", id).Error)
			}

			requestID := id
			if tc.invalidID {
				requestID = "not-a-canonical-uuid"
			}
			req := httptest.NewRequest(http.MethodPatch, "/api/projects/"+requestID, strings.NewReader(tc.payload))
			req.Header.Set("Content-Type", "application/json")
			switch tc.credential {
			case "anonymous":
			case "invalid":
				req.Header.Set("Authorization", "Bearer invalid-credential")
			case "read-only":
				req.Header.Set("Authorization", "Bearer "+readOnly)
			case "read-write":
				req.Header.Set("Authorization", "Bearer "+readWrite)
			default:
				req.Header.Set("Authorization", "Bearer "+master)
			}
			rec := httptest.NewRecorder()
			service.router.ServeHTTP(rec, req)
			assert.Equal(t, tc.status, rec.Code, "response: %s", rec.Body.String())

			var count int64
			require.NoError(t, db.Model(&gormdb.Project{}).Where("id = ?", id).Count(&count).Error)
			if tc.missing {
				assert.Zero(t, count, "PATCH must never register an unknown project")
			} else {
				require.EqualValues(t, 1, count)
				var after gormdb.Project
				require.NoError(t, db.First(&after, "id = ?", id).Error)
				if tc.changed {
					assert.Equal(t, sql.NullString{String: tc.expected, Valid: !tc.expectedNull}, after.DisplayName)
					get := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
					get.Header.Set("Authorization", "Bearer "+master)
					catalog := httptest.NewRecorder()
					service.router.ServeHTTP(catalog, get)
					require.Equal(t, http.StatusOK, catalog.Code, catalog.Body.String())
					var page struct {
						Items []map[string]any `json:"items"`
					}
					require.NoError(t, json.Unmarshal(catalog.Body.Bytes(), &page))
					found := false
					for _, item := range page.Items {
						if item["project_id"] != id {
							continue
						}
						found = true
						raw, present := item["display_name"]
						assert.True(t, present)
						if tc.expectedNull {
							assert.Nil(t, raw)
						} else {
							assert.Equal(t, tc.expected, raw)
						}
						presentation := tc.expected
						if tc.expectedNull || strings.TrimSpace(tc.expected) == "" {
							presentation = "Registered project"
						}
						assert.Equal(t, presentation, item["name"])
					}
					assert.True(t, found)
					after.DisplayName = before.DisplayName
				}
				assert.Equal(t, before, after, "only the target display_name may change")
			}
			var afterOther gormdb.Project
			var afterDoc gormdb.VersionedDocument
			require.NoError(t, db.First(&afterOther, "id = ?", otherID).Error)
			require.NoError(t, db.First(&afterDoc, doc.ID).Error)
			assert.Equal(t, beforeOther, afterOther, "same-name project must remain unchanged")
			assert.Equal(t, beforeDoc, afterDoc, "retained document history must remain unchanged")
		})
	}
}
