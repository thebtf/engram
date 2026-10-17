package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	gormdriver "gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestVaultGlobalCredentialLifecycleAndProjectCoexistence(t *testing.T) {
	cleanupStore, _ := openVaultCredentialTestStore(t)

	key := fmt.Sprintf("global-vault-coexist-%d", time.Now().UnixNano())
	project := "global-vault-project"
	require.NoError(t, cleanupStore.DB.Exec("DELETE FROM credentials WHERE key = ?", key).Error)
	defer cleanupStore.DB.Exec("DELETE FROM credentials WHERE key = ?", key)

	vault, err := crypto.NewVault(&config.Config{
		EncryptionKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	})
	require.NoError(t, err)

	s := NewServer(ServerOptions{Version: "global-vault-test"})
	s.vault = vault
	s.vaultOnce.Do(func() {})
	ctx := context.Background()

	call := func(args map[string]any) string {
		t.Helper()
		out, callErr := s.handleVaultConsolidated(ctx, mustJSON(t, args))
		require.NoError(t, callErr)
		return out
	}

	call(map[string]any{
		"action": "store",
		"name":   key,
		"value":  "global-secret",
		"scope":  "global",
	})
	call(map[string]any{
		"action":  "store",
		"name":    key,
		"value":   "project-secret",
		"scope":   "project",
		"project": project,
	})

	var globalGet struct {
		Name  string `json:"name"`
		Value string `json:"value"`
		Scope string `json:"scope"`
	}
	require.NoError(t, json.Unmarshal([]byte(call(map[string]any{
		"action": "get",
		"name":   key,
		"scope":  "global",
	})), &globalGet))
	assert.Equal(t, key, globalGet.Name)
	assert.True(t, globalGet.Value == "global-secret", "global credential must decrypt to the original value")
	assert.Equal(t, "global", globalGet.Scope)

	var projectGet struct {
		Value string `json:"value"`
		Scope string `json:"scope"`
	}
	require.NoError(t, json.Unmarshal([]byte(call(map[string]any{
		"action":  "get",
		"name":    key,
		"project": project,
	})), &projectGet))
	assert.True(t, projectGet.Value == "project-secret", "project credential must decrypt to the original value")
	assert.Equal(t, "project", projectGet.Scope)

	var globalListed []struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	require.NoError(t, json.Unmarshal([]byte(call(map[string]any{
		"action": "list",
		"scope":  "global",
	})), &globalListed))
	require.Len(t, globalListed, 1)
	assert.Equal(t, key, globalListed[0].Name)
	assert.Equal(t, "global", globalListed[0].Scope)

	var listed []struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	require.NoError(t, json.Unmarshal([]byte(call(map[string]any{
		"action":  "list",
		"project": project,
	})), &listed))
	require.Len(t, listed, 2)
	assert.ElementsMatch(t, []string{"global", "project"}, []string{listed[0].Scope, listed[1].Scope})
	assert.Equal(t, key, listed[0].Name)
	assert.Equal(t, key, listed[1].Name)

	call(map[string]any{
		"action": "delete",
		"name":   key,
		"scope":  "global",
	})

	projectGet = struct {
		Value string `json:"value"`
		Scope string `json:"scope"`
	}{}
	require.NoError(t, json.Unmarshal([]byte(call(map[string]any{
		"action":  "get",
		"name":    key,
		"project": project,
	})), &projectGet))
	assert.True(t, projectGet.Value == "project-secret", "deleting the global credential must preserve the project value")

	call(map[string]any{
		"action":  "delete",
		"name":    key,
		"project": project,
	})
}

func TestVaultCredentialOwnedPoolsClosed(t *testing.T) {
	observer, applicationName := openVaultCredentialTestStore(t)
	vault, err := crypto.NewVault(&config.Config{
		EncryptionKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	})
	require.NoError(t, err)
	s := NewServer(ServerOptions{Version: "vault-pool-test"})
	s.vault = vault
	s.vaultOnce.Do(func() {})
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	assertClosed := func() {
		t.Helper()
		require.Eventually(t, func() bool {
			var count int64
			err := observer.DB.Raw(`SELECT count(*) FROM pg_stat_activity
				WHERE datname = current_database() AND application_name = ?`, applicationName).Scan(&count).Error
			return err == nil && count == 0
		}, time.Second, 10*time.Millisecond, "handler-owned PostgreSQL pools must close before the next request")
		require.NoError(t, observer.Ping(), "caller-owned pool must remain open")
	}

	for range 3 {
		for _, args := range []map[string]any{
			{"action": "store", "name": "pool-lifecycle", "value": "synthetic-secret", "scope": "global"},
			{"action": "get", "name": "pool-lifecycle", "scope": "global"},
			{"action": "list", "scope": "global"},
			{"action": "status"},
			{"action": "delete", "name": "pool-lifecycle", "scope": "global"},
		} {
			_, err := s.handleVaultConsolidated(ctx, mustJSON(t, args))
			require.NoError(t, err)
			assertClosed()
		}
		for _, args := range []map[string]any{
			{"action": "store", "name": "pool-lifecycle"},
			{"action": "get", "name": "missing", "scope": "global"},
			{"action": "delete", "name": "missing", "scope": "global"},
		} {
			_, err := s.handleVaultConsolidated(ctx, mustJSON(t, args))
			require.Error(t, err)
			assertClosed()
		}
		_, err := s.handleVaultConsolidated(cancelled, mustJSON(t, map[string]any{"action": "list"}))
		require.Error(t, err)
		assertClosed()
		_, err = s.handleVaultConsolidated(cancelled, mustJSON(t, map[string]any{"action": "status"}))
		require.NoError(t, err, "status retains its existing count-error fallback")
		assertClosed()
	}

	for _, handler := range []func(context.Context, json.RawMessage) (string, error){
		s.handleStoreCredential, s.handleGetCredential, s.handleListCredentials, s.handleDeleteCredential,
	} {
		_, err := handler(ctx, json.RawMessage(`{`))
		require.Error(t, err)
		assertClosed()
	}
	s.vaultInitErr = fmt.Errorf("synthetic vault initialization failure")
	_, err = s.handleVaultConsolidated(ctx, mustJSON(t, map[string]any{
		"action": "store", "name": "pool-lifecycle", "value": "synthetic-secret", "scope": "global",
	}))
	require.Error(t, err)
	assertClosed()
}

func openVaultCredentialTestStore(t *testing.T) (*gormstore.Store, string) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping vault PostgreSQL integration test")
	}
	adminDB, err := gormlib.Open(gormdriver.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.True(t, err == nil, "open synthetic vault fixture database")
	adminSQL, err := adminDB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, adminSQL.Close()) })
	require.NoError(t, adminDB.Exec(`CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public`).Error)
	schema := "vault_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, adminDB.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() {
		require.NoError(t, adminDB.Exec("DROP SCHEMA IF EXISTS "+schema+" CASCADE").Error)
	})

	bindDSN := func(applicationName string) string {
		if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
			parsed, err := url.Parse(dsn)
			require.True(t, err == nil, "parse synthetic vault fixture DSN")
			query := parsed.Query()
			query.Set("search_path", schema+",public")
			query.Set("application_name", applicationName)
			parsed.RawQuery = query.Encode()
			return parsed.String()
		}
		return dsn + " application_name=" + applicationName + " search_path=" + schema + ",public"
	}
	t.Setenv("DATABASE_DSN", bindDSN(schema))
	store, err := gormstore.NewStore(gormstore.Config{DSN: bindDSN(schema + "_observer"), LogLevel: logger.Silent})
	require.True(t, err == nil, "open isolated vault fixture store")
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	var actualSchema string
	require.NoError(t, store.DB.Raw(`SELECT current_schema()`).Scan(&actualSchema).Error)
	require.Equal(t, schema, actualSchema)
	var isolatedCredentials int64
	require.NoError(t, store.DB.Raw(`SELECT count(*) FROM information_schema.tables
		WHERE table_schema = ? AND table_name = 'credentials'`, schema).Scan(&isolatedCredentials).Error)
	require.EqualValues(t, 1, isolatedCredentials, "vault fixture must not fall back to public credentials")
	return store, schema
}
