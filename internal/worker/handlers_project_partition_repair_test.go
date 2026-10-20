package worker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestProjectPartitionRepairPublicConsumers(t *testing.T) {
	if os.Getenv("DATABASE_DSN") == "" || os.Getenv("ENGRAM_CONSOLIDATION_RESTORE_TEST_DSN") == "" {
		t.Skip("DATABASE_DSN and ENGRAM_CONSOLIDATION_RESTORE_TEST_DSN are required for separate PostgreSQL fixtures")
	}
	backing := openWorkerUCIContextCompositionStore(t)
	db := backing.DB
	vault := newTestVault(t)
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
	backing = partitionHTTPRepairFixture(t, backing, vault, a)
	db = backing.DB
	service := &Service{store: backing, sessionStore: gormdb.NewSessionStore(backing), issueStore: gormdb.NewIssueStore(db), vault: vault}
	seed := db.Begin()
	require.NoError(t, seed.Error)
	defer seed.Rollback()
	require.NoError(t, seed.Exec("SET LOCAL session_replication_role=replica").Error)
	issue := gormdb.Issue{Title: "Historical issue", Body: "preserved public issue body", SourceProject: "former-http-alpha", TargetProject: "former-http-beta", SourceAgent: "agent", Labels: []string{}}
	require.NoError(t, seed.Create(&issue).Error)
	require.NoError(t, seed.Create(&gormdb.IssueComment{IssueID: issue.ID, AuthorProject: "former-http-alpha", AuthorAgent: "agent", Body: "preserved public history"}).Error)
	for _, role := range []struct{ field, project string }{{"source_project", a}, {"target_project", b}} {
		key, err := json.Marshal(map[string]int64{"id": issue.ID})
		require.NoError(t, err)
		field, err := json.Marshal([]string{role.field})
		require.NoError(t, err)
		project := role.project
		require.NoError(t, db.Create(&gormdb.ConsolidationMembership{Reference: "urn:engram:fixture:issue:" + role.field, Table: "issues", RowKey: gormdb.JSONRaw(key), SourceRowKey: gormdb.JSONRaw(key), FieldPath: gormdb.JSONRaw(field), ProjectID: &project, PlanFingerprint: "synthetic-existing-membership"}).Error)
	}
	require.NoError(t, seed.Commit().Error)
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
			var origin issueProjectOrigin
			require.NoError(t, json.Unmarshal(result.Issues[0]["project_origin"], &origin))
			assert.Equal(t, issueProjectOrigin{"former-http-alpha", "former-http-beta"}, origin)
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
			var result struct {
				Issue issueReadItem `json:"issue"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			assert.Equal(t, &a, result.Issue.SourceProjectID)
			assert.Equal(t, &b, result.Issue.TargetProjectID)
			assert.Equal(t, issueProjectOrigin{"former-http-alpha", "former-http-beta"}, result.Issue.ProjectOrigin)
		}
	})
	t.Run("legacy_selector_still_rejected", func(t *testing.T) {
		response := request("/api/issues?project=former-http-beta")
		require.Equal(t, http.StatusBadRequest, response.Code)
	})
}

func partitionHTTPRepairFixture(t *testing.T, source *gormdb.Store, vault *vaultcrypto.Vault, canonical string) *gormdb.Store {
	t.Helper()
	ctx := context.Background()
	opaque := func(kind string, value any) string {
		key := vault.DeriveKey("project-consolidation-reference/v1")
		mac := hmac.New(sha256.New, key[:])
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		mac.Write([]byte(kind + "\x00"))
		mac.Write(raw)
		return "urn:engram:migration:" + kind + ":" + hex.EncodeToString(mac.Sum(nil))
	}
	for _, id := range []string{"former-http-alpha", "former-http-beta"} {
		key, _ := json.Marshal(map[string]string{"id": id})
		ref := opaque("row", struct {
			Table, Column string
			Key           gormdb.JSONRaw
			Path          []string
		}{"projects", "id", gormdb.JSONRaw(key), nil})
		selector := opaque("selector", id)
		require.NoError(t, source.DB.Create(&gormdb.ConsolidationMembership{Reference: ref, Table: "projects", RowKey: gormdb.JSONRaw(key), SourceRowKey: gormdb.JSONRaw(key), FieldPath: gormdb.JSONRaw(`["id"]`), SelectorReference: selector, UnresolvedReference: opaque("unresolved", selector), Reason: "missing_membership_evidence", PlanFingerprint: "synthetic-http-B7", NativeVersion: "synthetic-origin", SourceSnapshot: "synthetic-http-source"}).Error)
	}
	id := uuid.NewString()
	raw, err := json.Marshal(gormdb.ConsolidationReceipt{OperationID: id, Reference: "urn:engram:migration:receipt:" + id, ProjectIDs: []string{canonical}})
	require.NoError(t, err)
	require.NoError(t, source.DB.Exec(`INSERT INTO project_consolidation_operations(idempotency_key,operation_id,plan_id,plan_fingerprint,receipt,completed_at) VALUES(?,?,?,'synthetic-http-B7',?::jsonb,?)`, id, id, id, string(raw), time.Now().UTC()).Error)
	var digest, schema string
	require.NoError(t, source.DB.Raw(`SELECT encode(sha256(convert_to(receipt::text,'UTF8')),'hex') FROM project_consolidation_operations WHERE idempotency_key=?`, id).Scan(&digest).Error)
	require.NoError(t, source.DB.Raw("SELECT current_schema()").Scan(&schema).Error)
	dsn := os.Getenv("ENGRAM_CONSOLIDATION_RESTORE_TEST_DSN")
	admin, err := gormlib.Open(postgres.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := admin.DB()
	require.NoError(t, err)
	quote := func(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
	require.NoError(t, admin.Exec("CREATE SCHEMA "+quote(schema)).Error)
	t.Cleanup(func() {
		require.NoError(t, admin.Exec("DROP SCHEMA "+quote(schema)+" CASCADE").Error)
		require.NoError(t, pool.Close())
	})
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	query := u.Query()
	query.Set("search_path", schema+", public")
	u.RawQuery = query.Encode()
	target, err := gormdb.NewStore(gormdb.Config{DSN: u.String(), MaxConns: 2, LogLevel: logger.Silent})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, target.Close()) })
	var tables []string
	require.NoError(t, source.DB.Raw(`SELECT tablename FROM pg_tables WHERE schemaname=current_schema() ORDER BY tablename`).Scan(&tables).Error)
	require.NoError(t, target.DB.Transaction(func(tx *gormlib.DB) error {
		if err := tx.Exec("SET LOCAL session_replication_role=replica").Error; err != nil {
			return err
		}
		names := make([]string, len(tables))
		for i, table := range tables {
			names[i] = quote(table)
		}
		if err := tx.Exec("TRUNCATE " + strings.Join(names, ",")).Error; err != nil {
			return err
		}
		for _, table := range tables {
			var columns []string
			if err := source.DB.Raw(`SELECT column_name FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=? AND is_generated<>'ALWAYS' ORDER BY ordinal_position`, table).Scan(&columns).Error; err != nil {
				return err
			}
			for i := range columns {
				columns[i] = quote(columns[i])
			}
			names := strings.Join(columns, ",")
			rows, err := source.DB.Raw("SELECT to_jsonb(t)::text FROM " + quote(table) + " t").Rows()
			if err != nil {
				return err
			}
			for rows.Next() {
				var raw string
				if err := rows.Scan(&raw); err != nil {
					rows.Close()
					return err
				}
				if err := tx.Exec("INSERT INTO "+quote(table)+"("+names+") OVERRIDING SYSTEM VALUE SELECT "+names+" FROM jsonb_populate_record(NULL::"+quote(table)+",?::jsonb)", raw).Error; err != nil {
					rows.Close()
					return err
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		var sequences []string
		if err := source.DB.Raw("SELECT sequencename FROM pg_sequences WHERE schemaname=current_schema()").Scan(&sequences).Error; err != nil {
			return err
		}
		for _, sequence := range sequences {
			var value struct {
				LastValue int64
				IsCalled  bool
			}
			if err := source.DB.Raw("SELECT last_value,is_called FROM " + quote(sequence)).Scan(&value).Error; err != nil {
				return err
			}
			if err := tx.Exec("SELECT setval(?::regclass,?,?)", quote(schema)+"."+quote(sequence), value.LastValue, value.IsCalled).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	engine := gormdb.NewProjectConsolidationStore(source.DB, vault, target.DB)
	plan, err := engine.PlanProjectPartitionRepair(ctx, digest)
	require.NoError(t, err)
	result, err := engine.ApplyProjectPartitionRepair(ctx, gormdb.ProjectPartitionRepairApplyRequest{PlanReference: plan.Reference, PlanFingerprint: plan.Fingerprint, ExpectedPlanID: plan.PlanID})
	require.NoError(t, err)
	require.Equal(t, 2, result.Receipt.MembershipChanges)
	return target
}
