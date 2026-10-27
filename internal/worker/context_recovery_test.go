package worker

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/projectidentity"
)

func TestContextRecoveryLegacyBindingHoldsWithoutReplacingIdentity(t *testing.T) {
	store := openWorkerUCIContextCompositionStore(t)
	service := &Service{store: store}
	server := httptest.NewServer(http.HandlerFunc(service.handleProjectBootstrap))
	defer server.Close()
	for _, scenario := range []string{"legacy-slug", "uuid-with-different-key"} {
		t.Run(scenario, func(t *testing.T) {
			id, key := "legacy-recovery-project", uuid.NewString()
			if scenario == "uuid-with-different-key" {
				id = uuid.NewString()
			}
			locator := "example.test/Recovery/" + scenario
			project := gormdb.Project{ID: id, ProjectKey: sql.NullString{String: key, Valid: true}, GitRemote: sql.NullString{String: "https://" + locator + ".git", Valid: true}, DisplayName: sql.NullString{String: "Original owner label", Valid: true}}
			require.NoError(t, store.DB.Create(&project).Error)
			binding := gormdb.ProjectIdentifier{ProjectKey: key, Scheme: "manual_alias", NormalizedValue: locator, Source: "recovery-fixture", Provenance: `{"method":"existing_owner_mapping"}`, Status: "active"}
			require.NoError(t, store.DB.Create(&binding).Error)
			snapshot := func() string {
				t.Helper()
				var raw string
				require.NoError(t, store.DB.Raw(`SELECT json_build_object('projects', (SELECT json_agg(row_to_json(p) ORDER BY id) FROM projects p), 'bindings', (SELECT json_agg(row_to_json(i) ORDER BY project_key, normalized_value) FROM project_identifiers i), 'memories', (SELECT json_agg(row_to_json(m) ORDER BY id) FROM memories m))::text`).Scan(&raw).Error)
				return raw
			}
			// Seed a historical row before exercising recovery, with its ordinary write guard restored.
			seed := store.DB.Begin()
			require.NoError(t, seed.Error)
			defer seed.Rollback()
			require.NoError(t, seed.Exec("ALTER TABLE memories DISABLE TRIGGER engram_consolidation_scope_write").Error)
			require.NoError(t, seed.Create(&gormdb.Memory{Project: id, Content: "Original retained recovery memory", Version: 3, PrivacyScope: "project"}).Error)
			require.NoError(t, seed.Exec("ALTER TABLE memories ENABLE TRIGGER engram_consolidation_scope_write").Error)
			require.NoError(t, seed.Commit().Error)
			before := snapshot()
			root := t.TempDir()
			for _, args := range [][]string{{"init", "--quiet"}, {"remote", "add", "origin", "https://" + locator + ".git"}} {
				raw, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, args...)...).CombinedOutput()
				require.NoError(t, err, "synthetic Git fixture: %s", raw)
			}
			marker := filepath.Join(root, ".engram-project")
			cache := filepath.Join(root, ".git", "engram-project.json")
			markerBytes := []byte("{ \"name\": \"Original owner label\" }\n")
			cacheBytes := []byte("{ \"name\": \"Original cache label\" }\n")
			require.NoError(t, os.WriteFile(marker, markerBytes, 0o600))
			require.NoError(t, os.WriteFile(cache, cacheBytes, 0o600))
			resolved, err := projectidentity.ResolveLogicalProject(t.Context(), root, projectidentity.HTTPBootstrap(server.URL, ""))
			require.Equal(t, "PROJECT_MIGRATION_REQUIRED", projectidentity.ProjectErrorCode(err))
			require.Empty(t, resolved.ProjectID, "a legacy match must not mint or return replacement UUID authority")
			assert.Contains(t, err.Error(), "/api/projects/consolidation/census", "native error must carry a real owner inspection step, not require docs in the caller repository")

			// A different label is not a mapping decision or permission to create a fresh identity.
			raw, err := json.Marshal(projectidentity.BootstrapRequest{RepositoryLocator: locator, Name: "Replacement requested label"})
			require.NoError(t, err)
			response, err := http.Post(server.URL+"/api/projects/bootstrap", "application/json", bytes.NewReader(raw))
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, http.StatusConflict, response.StatusCode)
			var failure map[string]any
			require.NoError(t, json.NewDecoder(response.Body).Decode(&failure))
			require.Equal(t, "PROJECT_MIGRATION_REQUIRED", failure["code"])
			encoded, err := json.Marshal(failure)
			require.NoError(t, err)
			assert.Contains(t, string(encoded), "/api/projects/consolidation/census", "HTTP callers also need the supported next inspection action")
			for path, expected := range map[string][]byte{marker: markerBytes, cache: cacheBytes} {
				actual, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, expected, actual, "migration hold must preserve marker/cache bytes")
			}
			require.Equal(t, before, snapshot(), "legacy project and identifier rows must remain byte-equivalent; no new project or alias")
			t.Logf("%s: real native resolver -> HTTP bootstrap -> PostgreSQL migration hold; changed label cannot bypass; original project/alias/marker/cache unchanged", scenario)
		})
	}
}

func TestContextRecoveryRepositoryLocatorInspectionIsBoundedAndReadOnly(t *testing.T) {
	store := openWorkerUCIContextCompositionStore(t)
	service := &Service{store: store, vault: newTestVault(t), sessionStore: gormdb.NewSessionStore(store)}
	for _, scenario := range []string{"resolved", "migration_required", "conflict", "unbound", "active-alias-precedence", "redirected-alias-precedence", "historical-subdirectory"} {
		t.Run(scenario, func(t *testing.T) {
			locator := "example.test/Diagnostic/" + scenario
			id, key := uuid.NewString(), ""
			key = id
			if scenario == "migration_required" {
				id = "legacy-diagnostic-project"
			}
			var ids []string
			if scenario != "unbound" {
				row := gormdb.Project{ID: id, ProjectKey: sql.NullString{String: key, Valid: true}, IdentityStatus: sql.NullString{String: "active", Valid: true}, GitRemote: sql.NullString{String: "https://" + locator + ".git", Valid: true}}
				if scenario == "historical-subdirectory" {
					row.RelativePath = sql.NullString{String: "historical/", Valid: true}
				}
				require.NoError(t, store.DB.Create(&row).Error)
				ids = append(ids, id)
				if scenario == "conflict" || scenario == "active-alias-precedence" || scenario == "redirected-alias-precedence" {
					otherID := uuid.NewString()
					require.NoError(t, store.DB.Create(&gormdb.Project{ID: otherID, ProjectKey: sql.NullString{String: otherID, Valid: true}, GitRemote: row.GitRemote}).Error)
					ids = append(ids, otherID)
				}
				if scenario == "active-alias-precedence" || scenario == "redirected-alias-precedence" {
					status := "active"
					if scenario == "redirected-alias-precedence" {
						status = "redirected"
					}
					require.NoError(t, store.DB.Create(&gormdb.ProjectIdentifier{ProjectKey: key, Scheme: "manual_alias", NormalizedValue: locator, Status: status, Source: "diagnostic-fixture", Provenance: `{}`}).Error)
				}
			}
			snapshot := func() string {
				t.Helper()
				var raw string
				require.NoError(t, store.DB.Raw(`SELECT json_build_object('projects', (SELECT json_agg(row_to_json(p) ORDER BY id) FROM (SELECT *,xmin::text AS row_version FROM projects) p), 'aliases', (SELECT json_agg(row_to_json(i) ORDER BY project_key,normalized_value) FROM (SELECT *,xmin::text AS row_version FROM project_identifiers) i))::text`).Scan(&raw).Error)
				return raw
			}
			before := snapshot()
			query := url.Values{"repository_locator": {"https://" + locator + ".git"}}
			request := httptest.NewRequest(http.MethodGet, "/api/projects/resolve?"+query.Encode(), nil)
			request = request.WithContext(auth.WithIdentity(request.Context(), auth.AuthDisabled()))
			response := httptest.NewRecorder()
			service.handleResolveLogicalProject(response, request)
			t.Logf("actual existing GET resolve: scenario=%s HTTP=%d body=%s", scenario, response.Code, response.Body.String())
			assert.Equal(t, http.StatusOK, response.Code, "exclusive repository_locator must be a supported read-only selector")
			var payload map[string]any
			assert.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
			want := scenario
			if scenario == "active-alias-precedence" || scenario == "redirected-alias-precedence" {
				want = "resolved"
			} else if scenario == "historical-subdirectory" {
				want = "unbound"
			}
			assert.Equal(t, want, payload["status"])
			if want == "resolved" || want == "migration_required" {
				assert.Contains(t, response.Body.String(), id, "one exact binding needs its original ID")
				assert.Contains(t, response.Body.String(), key, "inspect the actual project_key, never a replacement UUID")
				assert.Contains(t, response.Body.String(), "active", "inspection must expose the identity state")
			}
			if want == "conflict" || scenario == "active-alias-precedence" || scenario == "redirected-alias-precedence" {
				assert.NotContains(t, response.Body.String(), ids[1], "conflicts and lower-precedence fallback rows must not be adopted or exposed as selected authority")
			}
			assert.NotContains(t, response.Body.String(), "https://", "diagnostics must not echo raw repository remotes")
			require.Equal(t, before, snapshot(), "GET must not allocate UUID/project/alias, update metadata or row versions")
		})
	}
	t.Run("existing-legacy-id-branch", func(t *testing.T) {
		var canonical gormdb.Project
		require.NoError(t, store.DB.Where("git_remote = ?", "https://example.test/Diagnostic/resolved.git").Take(&canonical).Error)
		request := httptest.NewRequest(http.MethodGet, "/api/projects/resolve?legacy_id="+url.QueryEscape(canonical.ID), nil)
		request = request.WithContext(auth.WithIdentity(request.Context(), auth.AuthDisabled()))
		response := httptest.NewRecorder()
		service.handleResolveLogicalProject(response, request)
		require.Equal(t, http.StatusOK, response.Code)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
		require.Equal(t, "resolved", payload["status"])
		require.Equal(t, canonical.ID, payload["project_id"])
	})
	for _, denied := range []struct {
		name, query string
		identity    bool
		want        int
	}{
		{"conflicting-selectors", "repository_locator=https%3A%2F%2Fexample.test%2FDiagnostic%2Fresolved.git&legacy_id=other", true, http.StatusBadRequest},
		{"unauthenticated", "repository_locator=https%3A%2F%2Fexample.test%2FDiagnostic%2Fresolved.git", false, http.StatusForbidden},
	} {
		t.Run(denied.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/projects/resolve?"+denied.query, nil)
			if denied.identity {
				request = request.WithContext(auth.WithIdentity(request.Context(), auth.AuthDisabled()))
			}
			response := httptest.NewRecorder()
			service.handleResolveLogicalProject(response, request)
			assert.Equal(t, denied.want, response.Code)
			assert.NotContains(t, response.Body.String(), "legacy-diagnostic-project")
		})
	}
}
