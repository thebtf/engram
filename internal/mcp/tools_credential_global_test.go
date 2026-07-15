package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
)

func TestVaultGlobalCredentialLifecycleAndProjectCoexistence(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping global vault integration test")
	}

	cleanupStore, err := gormstore.NewStore(gormstore.Config{DSN: dsn})
	require.NoError(t, err)
	defer cleanupStore.Close()

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
	assert.Equal(t, "global-secret", globalGet.Value)
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
	assert.Equal(t, "project-secret", projectGet.Value)
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
	assert.Equal(t, "project-secret", projectGet.Value)

	call(map[string]any{
		"action":  "delete",
		"name":    key,
		"project": project,
	})
}
