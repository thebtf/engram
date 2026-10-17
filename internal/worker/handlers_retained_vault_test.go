package worker

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRetainedVaultMetadataHTTP(t *testing.T) {
	dsnFile, hasDSN := os.LookupEnv("ENGRAM_RETAINED_VAULT_TEST_DSN_FILE")
	rawDeadline, hasDeadline := os.LookupEnv("ENGRAM_RETAINED_VAULT_FIXTURE_DEADLINE")
	if !hasDSN && !hasDeadline {
		t.Skip("requires a new dedicated synthetic retained-vault PostgreSQL fixture; no PostgreSQL proof executed")
	}
	deadline, err := time.Parse(time.RFC3339, rawDeadline)
	if dsnFile == "" || err != nil || time.Until(deadline) < 15*time.Second || time.Until(deadline) > 30*time.Minute {
		t.Fatal("requires a current bounded retained-vault fixture admission")
	}
	rootContext, rootCancel := context.WithTimeout(t.Context(), 5*time.Second)
	commonOutput, err := exec.CommandContext(rootContext, "git", "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	rootCancel()
	require.NoError(t, err, "resolve the primary fixture owner through the worktree's native Git common directory")
	commonDirectory := filepath.Clean(strings.TrimSpace(string(commonOutput)))
	require.True(t, filepath.IsAbs(commonDirectory) && filepath.Base(commonDirectory) == ".git", "fixture requires an absolute primary repository Git directory")
	privateRoot := filepath.Join(filepath.Dir(commonDirectory), ".agent", "artifacts", "data", "pr568-stream-integrity-20261017", ".private", "retained-vault")
	if filepath.Clean(dsnFile) != filepath.Join(privateRoot, "postgres.dsn") {
		t.Fatal("refusing an unassigned private fixture input")
	}
	admissionBytes, err := os.ReadFile(filepath.Join(privateRoot, "admission.json"))
	if err != nil {
		t.Fatal("private retained-vault admission unavailable")
	}
	var admission struct {
		Owner, DSNFile, DatabaseOID, SystemIdentifier, DeadlineUTC string
		Port                                                       int
	}
	if json.Unmarshal(admissionBytes, &admission) != nil || admission.Owner != "R1RetainedVaultMaker" || admission.Port != 22417 || admission.DatabaseOID != "16392" || admission.SystemIdentifier != "7697507119866282022" || filepath.Clean(admission.DSNFile) != filepath.Clean(dsnFile) {
		t.Fatal("refusing an unassigned retained-vault admission")
	}
	admittedDeadline, err := time.Parse(time.RFC3339, admission.DeadlineUTC)
	if err != nil || !deadline.Equal(admittedDeadline) {
		t.Fatal("retained-vault deadline must match the current private admission")
	}
	raw, err := os.ReadFile(dsnFile)
	if err != nil {
		t.Fatal("private retained-vault fixture DSN unavailable")
	}
	u, err := url.Parse(strings.TrimSpace(string(raw)))
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host != "127.0.0.1:22417" || u.Path != "/retained_vault_fixture" {
		t.Fatal("refusing any target except the new dedicated retained-vault fixture")
	}
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	admin, err := gormlib.Open(postgres.Open(u.String()), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("dedicated fixture connection failed")
	}
	adminPool, err := admin.DB()
	require.NoError(t, err)
	defer adminPool.Close()
	adminPool.SetMaxOpenConns(1)
	adminPool.SetMaxIdleConns(1)
	var identity struct {
		Database         string `gorm:"column:database"`
		OID              string `gorm:"column:oid"`
		SystemIdentifier string `gorm:"column:system_identifier"`
	}
	require.NoError(t, admin.WithContext(ctx).Raw("SELECT current_database() AS database, (SELECT oid::text FROM pg_database WHERE datname=current_database()) AS oid, system_identifier::text AS system_identifier FROM pg_control_system()").Scan(&identity).Error)
	require.Equal(t, "retained_vault_fixture", identity.Database)
	require.Equal(t, "16392", identity.OID)
	require.Equal(t, "7697507119866282022", identity.SystemIdentifier)
	schema := "retained_vault_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.WithContext(ctx).Exec("CREATE SCHEMA "+schema).Error)
	defer func() {
		if time.Now().Before(deadline) {
			require.NoError(t, admin.WithContext(ctx).Exec("DROP SCHEMA "+schema+" CASCADE").Error)
		} else {
			t.Error("fixture admission expired; owned retained-vault schema preserved for fixture owner")
		}
	}()
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := gormlib.Open(postgres.Open(u.String()), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("dedicated schema connection failed")
	}
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	pool.SetMaxOpenConns(2)
	pool.SetMaxIdleConns(1)
	require.NoError(t, db.WithContext(ctx).AutoMigrate(&gormdb.Credential{}, &gormdb.ConsolidationMembership{}))
	vault := newTestVault(t)
	backing := &gormdb.Store{DB: db}
	service := &Service{store: backing, credentialStore: gormdb.NewCredentialStore(backing), config: &config.Config{EncryptionKey: testHexKey}, vault: vault}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Fixture-Actor") != "none" {
				r = r.WithContext(auth.WithIdentity(r.Context(), auth.Admin()))
			}
			next.ServeHTTP(w, r)
		})
	})
	router.Get("/api/vault/credentials", service.handleListCredentials)
	router.Get("/api/vault/credentials/{name}", service.handleGetCredential)
	router.Delete("/api/vault/credentials/{name}", service.handleDeleteCredential)
	server := httptest.NewServer(router)
	defer server.Close()
	call := func(t *testing.T, method, path, actor string, expected int, result any) {
		t.Helper()
		request, requestErr := http.NewRequestWithContext(ctx, method, server.URL+path, nil)
		require.NoError(t, requestErr)
		request.Header.Set("X-Fixture-Actor", actor)
		response, requestErr := server.Client().Do(request)
		require.NoError(t, requestErr)
		defer response.Body.Close()
		require.Equal(t, expected, response.StatusCode)
		if result != nil && response.StatusCode == http.StatusOK {
			require.NoError(t, json.NewDecoder(response.Body).Decode(result))
		} else {
			_, requestErr = io.Copy(io.Discard, response.Body)
			require.NoError(t, requestErr)
		}
		if method == http.MethodGet && strings.Contains(path, "/same-name?") && expected == http.StatusOK {
			require.Equal(t, "private, no-store", response.Header.Get("Cache-Control"))
		}
	}
	opaque := func(kind string, value any) string {
		key := vault.DeriveKey("project-consolidation-reference/v1")
		mac := hmac.New(sha256.New, key[:])
		encoded, marshalErr := json.Marshal(value)
		require.NoError(t, marshalErr)
		_, _ = mac.Write([]byte(kind + "\x00"))
		_, _ = mac.Write(encoded)
		return "urn:engram:migration:" + kind + ":" + hex.EncodeToString(mac.Sum(nil))
	}
	projects := []string{uuid.NewString(), uuid.NewString(), "", "retained-checkout-a", "retained-checkout-b"}
	credentials := make([]gormdb.Credential, len(projects))
	values := make([]string, len(projects))
	members := make([]gormdb.ConsolidationMembership, 2)
	for i, project := range projects {
		private := make([]byte, 24)
		_, err := rand.Read(private)
		require.NoError(t, err)
		values[i] = hex.EncodeToString(private)
		ciphertext, err := vault.Encrypt(values[i])
		require.NoError(t, err)
		scope := "project"
		if project == "" {
			scope = "global"
		}
		credentials[i] = gormdb.Credential{Project: project, Key: "same-name", Scope: scope, EncryptedSecret: ciphertext, EncryptionKeyFingerprint: vault.Fingerprint()}
		require.NoError(t, db.WithContext(ctx).Create(&credentials[i]).Error)
		if i < 3 {
			continue
		}
		nativeKey, err := json.Marshal(map[string]int64{"id": credentials[i].ID})
		require.NoError(t, err)
		selector := opaque("selector", project)
		reference := opaque("row", struct {
			Table, Column string
			Key           gormdb.JSONRaw
			Path          []string
		}{"credentials", "project", gormdb.JSONRaw(nativeKey), nil})
		member := gormdb.ConsolidationMembership{Reference: reference, UnresolvedReference: opaque("unresolved", selector), Table: "credentials", RowKey: gormdb.JSONRaw(nativeKey), SourceRowKey: gormdb.JSONRaw(nativeKey), FieldPath: gormdb.JSONRaw(`["project"]`), SelectorReference: selector, PlanFingerprint: "retained-vault-fixture-plan"}
		require.NoError(t, db.WithContext(ctx).Create(&member).Error)
		members[i-3] = member
	}
	retainedPath := func(id int64, reference string) string {
		return "/api/vault/credentials/same-name?" + url.Values{"id": {strconv.FormatInt(id, 10)}, "unresolved_reference": {reference}}.Encode()
	}
	metadataByID := func(t *testing.T, path string) map[int64]map[string]any {
		t.Helper()
		var metadata []map[string]any
		call(t, http.MethodGet, path, "admin", http.StatusOK, &metadata)
		byID := make(map[int64]map[string]any, len(metadata))
		for _, row := range metadata {
			require.NotContains(t, row, "value")
			require.NotContains(t, row, "encrypted_secret")
			byID[int64(row["id"].(float64))] = row
		}
		return byID
	}

	t.Run("canonical-global-same-name-behavior", func(t *testing.T) {
		var metadata []map[string]any
		call(t, http.MethodGet, "/api/vault/credentials?project="+projects[0], "admin", http.StatusOK, &metadata)
		require.Len(t, metadata, 2)
		for _, row := range metadata {
			require.NotContains(t, row, "value")
			require.True(t, row["project_id"] == projects[0] || row["project_id"] == nil)
			require.Empty(t, row["unresolved_reference"])
		}
		for i := range 3 {
			selector := "?project=" + projects[i]
			if i == 2 {
				selector = "?scope=global"
			}
			var detail map[string]any
			call(t, http.MethodGet, "/api/vault/credentials/same-name"+selector, "admin", http.StatusOK, &detail)
			require.EqualValues(t, credentials[i].ID, detail["id"])
			require.Equal(t, credentialProjectID(projects[i]), detail["project_id"])
			require.True(t, detail["value"] == values[i], "exact namespace must reveal only its own synthetic value")
		}
	})

	t.Run("list-metadata-to-actual-retained-reveal", func(t *testing.T) {
		var metadata []map[string]any
		call(t, http.MethodGet, "/api/vault/credentials", "admin", http.StatusOK, &metadata)
		require.Len(t, metadata, len(credentials))
		byID := make(map[int64]map[string]any)
		for _, row := range metadata {
			require.NotContains(t, row, "value")
			require.NotContains(t, row, "encrypted_secret")
			require.NotContains(t, row, "encryption_key_fingerprint")
			byID[int64(row["id"].(float64))] = row
		}
		for i := 3; i < len(credentials); i++ {
			row := byID[credentials[i].ID]
			require.NotNil(t, row)
			require.Nil(t, row["project_id"], "retained legacy selector must never be advertised as a canonical UUID")
			require.Equal(t, "project", row["scope"], "retained must not become global")
			reference, ok := row["unresolved_reference"].(string)
			require.True(t, ok, "list metadata must carry the existing opaque unresolved reference")
			require.Equal(t, members[i-3].UnresolvedReference, reference)
			var detail map[string]any
			call(t, http.MethodGet, retainedPath(int64(row["id"].(float64)), reference), "admin", http.StatusOK, &detail)
			require.Equal(t, row["id"], detail["id"])
			require.Equal(t, reference, detail["unresolved_reference"])
			require.Nil(t, detail["project_id"])
			require.True(t, detail["value"] == values[i], "list-selected native ID/reference must decrypt only its own synthetic value")
		}
	})

	t.Run("uuid-shaped-retained-owner-is-not-canonical", func(t *testing.T) {
		oldSelector := uuid.NewString()
		selectorReference := opaque("selector", oldSelector)
		unresolved := opaque("unresolved", selectorReference)
		require.NoError(t, db.WithContext(ctx).Model(&gormdb.Credential{}).Where("id = ?", credentials[3].ID).Update("project", oldSelector).Error)
		require.NoError(t, db.WithContext(ctx).Model(&gormdb.ConsolidationMembership{}).Where("reference = ?", members[0].Reference).Updates(map[string]any{"selector_reference": selectorReference, "unresolved_reference": unresolved}).Error)
		defer func() {
			require.NoError(t, db.WithContext(ctx).Model(&gormdb.Credential{}).Where("id = ?", credentials[3].ID).Update("project", projects[3]).Error)
			require.NoError(t, db.WithContext(ctx).Model(&gormdb.ConsolidationMembership{}).Where("reference = ?", members[0].Reference).Updates(map[string]any{"selector_reference": members[0].SelectorReference, "unresolved_reference": members[0].UnresolvedReference}).Error)
		}()
		for _, path := range []string{"/api/vault/credentials", "/api/vault/credentials?project=" + oldSelector} {
			row := metadataByID(t, path)[credentials[3].ID]
			require.NotNil(t, row)
			require.Nil(t, row["project_id"])
			require.NotContains(t, row, "project")
			require.Equal(t, unresolved, row["unresolved_reference"])
			require.Equal(t, oldSelector, row["origin_project"])
		}
		var detail map[string]any
		call(t, http.MethodGet, retainedPath(credentials[3].ID, unresolved), "admin", http.StatusOK, &detail)
		require.True(t, detail["value"] == values[3], "UUID-shaped retained origin must still use only native ID/reference")
	})

	t.Run("missing-proof-and-conflict-remain-visible-without-fallback", func(t *testing.T) {
		require.NoError(t, db.WithContext(ctx).Where("reference = ?", members[0].Reference).Delete(&gormdb.ConsolidationMembership{}).Error)
		defer func() { require.NoError(t, db.WithContext(ctx).Create(&members[0]).Error) }()
		for _, path := range []string{"/api/vault/credentials", "/api/vault/credentials?project=" + url.QueryEscape(projects[3])} {
			row := metadataByID(t, path)[credentials[3].ID]
			require.NotNil(t, row)
			require.Equal(t, "CREDENTIAL_BINDING_UNAVAILABLE", row["binding_error"])
			require.Nil(t, row["project_id"])
			require.NotContains(t, row, "project")
			require.NotContains(t, row, "unresolved_reference")
			require.Equal(t, projects[3], row["origin_project"])
		}
		var detail map[string]any
		call(t, http.MethodGet, retainedPath(credentials[4].ID, members[1].UnresolvedReference), "admin", http.StatusOK, &detail)
		require.True(t, detail["value"] == values[4], "unavailable sibling must not hide a useful retained credential")
		conflict := members[1]
		conflict.Reference = "fixture-conflicting-credential-membership"
		conflict.ProjectID = &projects[0]
		conflict.UnresolvedReference = ""
		require.NoError(t, db.WithContext(ctx).Create(&conflict).Error)
		defer func() {
			require.NoError(t, db.WithContext(ctx).Where("reference = ?", conflict.Reference).Delete(&gormdb.ConsolidationMembership{}).Error)
		}()
		metadata := metadataByID(t, "/api/vault/credentials")
		require.Len(t, metadata, len(credentials), "unavailable same-name rows must not be dropped or collapsed")
		require.Equal(t, "CREDENTIAL_BINDING_UNAVAILABLE", metadata[credentials[3].ID]["binding_error"])
		row := metadata[credentials[4].ID]
		require.Equal(t, "CREDENTIAL_BINDING_CONFLICT", row["binding_error"])
		require.Nil(t, row["project_id"])
		require.NotContains(t, row, "project")
		require.NotContains(t, row, "unresolved_reference")
		require.Equal(t, projects[4], row["origin_project"])
		call(t, http.MethodGet, "/api/vault/credentials/same-name?project="+projects[0], "admin", http.StatusOK, &detail)
		require.True(t, detail["value"] == values[0], "unavailable groups must not hide canonical credentials")
	})

	t.Run("foreign-stale-and-read-only-guards", func(t *testing.T) {
		path := retainedPath(credentials[3].ID, members[0].UnresolvedReference)
		call(t, http.MethodGet, path, "none", http.StatusForbidden, nil)
		call(t, http.MethodGet, retainedPath(credentials[4].ID, members[0].UnresolvedReference), "admin", http.StatusNotFound, nil)
		call(t, http.MethodGet, retainedPath(credentials[0].ID, members[0].UnresolvedReference), "admin", http.StatusNotFound, nil)
		call(t, http.MethodGet, retainedPath(credentials[3].ID, members[1].UnresolvedReference), "admin", http.StatusNotFound, nil)
		call(t, http.MethodGet, retainedPath(credentials[3].ID, "urn:engram:migration:unresolved:"+strings.Repeat("0", 64)), "admin", http.StatusNotFound, nil)
		call(t, http.MethodGet, strings.Replace(path, "/same-name?", "/foreign-name?", 1), "admin", http.StatusNotFound, nil)
		call(t, http.MethodGet, path+"&scope=global", "admin", http.StatusBadRequest, nil)
		call(t, http.MethodGet, path+"&project="+projects[0], "admin", http.StatusBadRequest, nil)
		call(t, http.MethodDelete, path, "admin", http.StatusBadRequest, nil)
		deleted := time.Now().UTC()
		require.NoError(t, db.WithContext(ctx).Model(&gormdb.Credential{}).Where("id = ?", credentials[3].ID).Update("deleted_at", deleted).Error)
		call(t, http.MethodGet, path, "admin", http.StatusNotFound, nil)
		require.NoError(t, db.WithContext(ctx).Model(&gormdb.Credential{}).Where("id = ?", credentials[3].ID).Update("deleted_at", nil).Error)
		require.NoError(t, db.WithContext(ctx).Model(&gormdb.Credential{}).Where("id = ?", credentials[3].ID).Update("project", "changed-retained-checkout").Error)
		call(t, http.MethodGet, path, "admin", http.StatusNotFound, nil)
		require.NoError(t, db.WithContext(ctx).Model(&gormdb.Credential{}).Where("id = ?", credentials[3].ID).Update("project", projects[3]).Error)
		require.NoError(t, db.WithContext(ctx).Model(&gormdb.Credential{}).Where("id = ?", credentials[3].ID).Update("encryption_key_fingerprint", "different-fixture-key").Error)
		call(t, http.MethodGet, path, "admin", http.StatusConflict, nil)
		require.NoError(t, db.WithContext(ctx).Model(&gormdb.Credential{}).Where("id = ?", credentials[3].ID).Update("encryption_key_fingerprint", vault.Fingerprint()).Error)
		var detail map[string]any
		call(t, http.MethodGet, path, "admin", http.StatusOK, &detail)
		require.True(t, detail["value"] == values[3], "refused retained Delete must preserve the native credential")
	})

	t.Run("canonical-global-delete-preserves-same-name-retained", func(t *testing.T) {
		call(t, http.MethodDelete, "/api/vault/credentials/same-name?project="+projects[0], "admin", http.StatusOK, nil)
		call(t, http.MethodDelete, "/api/vault/credentials/same-name?scope=global", "admin", http.StatusOK, nil)
		call(t, http.MethodGet, "/api/vault/credentials/same-name?project="+projects[0], "admin", http.StatusNotFound, nil)
		call(t, http.MethodGet, "/api/vault/credentials/same-name?scope=global", "admin", http.StatusNotFound, nil)
		var detail map[string]any
		call(t, http.MethodGet, "/api/vault/credentials/same-name?project="+projects[1], "admin", http.StatusOK, &detail)
		require.True(t, detail["value"] == values[1], "other canonical namespace must survive")
		for i := 3; i < len(credentials); i++ {
			call(t, http.MethodGet, retainedPath(credentials[i].ID, members[i-3].UnresolvedReference), "admin", http.StatusOK, &detail)
			require.True(t, detail["value"] == values[i], "same-name retained credentials must survive canonical/global deletion")
		}
	})
}
