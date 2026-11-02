package worker

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

func TestVaultAggregateCleanupCannotEraseRecoverableCredentials(t *testing.T) {
	backing := openWorkerUCIContextCompositionStore(t)
	store := gormdb.NewCredentialStore(backing)

	key := func() string {
		private := make([]byte, 32)
		_, err := rand.Read(private)
		require.NoError(t, err)
		return hex.EncodeToString(private)
	}
	keyA, keyB := key(), key()
	cfgA, cfgB := &config.Config{EncryptionKey: keyA}, &config.Config{EncryptionKey: keyB}
	vaultA, err := vaultcrypto.NewVault(cfgA)
	require.NoError(t, err)
	vaultB, err := vaultcrypto.NewVault(cfgB)
	require.NoError(t, err)
	require.True(t, vaultA.Fingerprint() != vaultB.Fingerprint(), "synthetic keys must differ")
	// Restore the test process configuration after the scoped environment cleanup.
	t.Cleanup(func() { _, _, err := config.Reload(); require.NoError(t, err) })
	t.Setenv("ENGRAM_ENCRYPTION_KEY", keyB)
	_, _, err = config.Reload()
	require.NoError(t, err)

	fixture := newOpenClawLifecycleFixture(t, backing.DB)
	fixture.service.config = cfgB
	fixture.service.credentialStore = store
	require.NoError(t, chi.Walk(fixture.service.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		require.False(t, method == http.MethodDelete && route == "/api/vault/orphaned-credentials", "the aggregate writer must not be registered")
		return nil
	}))
	projects := []string{fixture.project, fixture.other, ""}
	values := []string{key(), key(), key()}
	ids := make([]int64, 0, len(projects))
	for i, project := range projects {
		ciphertext, err := vaultA.Encrypt(values[i])
		require.NoError(t, err)
		plain, err := vaultA.Decrypt(ciphertext)
		require.NoError(t, err)
		require.True(t, plain == values[i], "key A must recover its synthetic value before any request")
		_, err = vaultB.Decrypt(ciphertext)
		require.Error(t, err, "a temporary key mismatch is not proof the ciphertext is irrecoverable")
		scope := "project"
		if project == "" {
			scope = "global"
		}
		created, err := store.Create(t.Context(), &models.Credential{Project: project, Key: "recoverable", Scope: scope, EncryptedSecret: ciphertext, EncryptionKeyFingerprint: vaultA.Fingerprint()})
		require.NoError(t, err)
		ids = append(ids, created.ID)
	}
	currentValue := key()
	currentCiphertext, err := vaultB.Encrypt(currentValue)
	require.NoError(t, err)
	_, err = store.Create(t.Context(), &models.Credential{Project: fixture.project, Key: "current-key", Scope: "project", EncryptedSecret: currentCiphertext, EncryptionKeyFingerprint: vaultB.Fingerprint()})
	require.NoError(t, err)

	snapshot := func() []gormdb.Credential {
		var rows []gormdb.Credential
		require.NoError(t, backing.DB.Unscoped().Order("id").Find(&rows).Error)
		return rows
	}
	call := func(f *openClawLifecycleFixture, method, route string, expected int, result any) {
		t.Helper()
		status, payload := f.call(t, method, route, f.tokenA, nil)
		require.Equal(t, expected, status, "synthetic Vault route status; private response omitted")
		if result != nil {
			require.NoError(t, json.Unmarshal([]byte(payload), result))
		}
	}
	var status struct {
		Count    int    `json:"credential_count"`
		Mismatch int    `json:"mismatch_count"`
		Warning  string `json:"mismatch_warning"`
	}
	before := snapshot()
	call(fixture, http.MethodGet, "/api/vault/status", http.StatusOK, &status)
	require.Equal(t, 4, status.Count)
	require.Equal(t, 3, status.Mismatch)
	require.NotEmpty(t, status.Warning)
	var current struct {
		Value string `json:"value"`
	}
	call(fixture, http.MethodGet, "/api/vault/credentials/current-key?project="+fixture.project, http.StatusOK, &current)
	require.True(t, current.Value == currentValue, "the current-key credential must still be readable")
	require.True(t, reflect.DeepEqual(before, snapshot()), "status and reveal must not mutate credential bytes")

	response, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, fixture.server.URL+"/api/vault/orphaned-credentials", nil)
	require.NoError(t, err)
	response.Header.Set("Authorization", "Bearer "+fixture.tokenA)
	reply, err := fixture.client.Do(response)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, reply.Body)
	require.NoError(t, err)
	require.NoError(t, reply.Body.Close())
	after := snapshot()
	oldKeyRows := 0
	for _, row := range after {
		if row.EncryptionKeyFingerprint == vaultA.Fingerprint() {
			oldKeyRows++
		}
	}
	t.Logf("synthetic aggregate request: HTTP%d; rows before=%d after=%d; recoverable old-key rows after=%d across two projects and global scope", reply.StatusCode, len(before), len(after), oldKeyRows)
	// The registered SPA GET wildcard makes an absent DELETE return chi's native 405.
	assert.Equal(t, http.StatusMethodNotAllowed, reply.StatusCode, "the retired convenience writer must have no route or fallback alias")
	if !assert.True(t, reflect.DeepEqual(before, after), "the aggregate request must preserve every credential row and ciphertext byte") {
		return
	}

	restored := newOpenClawLifecycleFixture(t, backing.DB)
	restored.service.config = cfgA
	restored.service.credentialStore = store
	selector := func(project string) string {
		if project == "" {
			return "?scope=global"
		}
		return "?project=" + project
	}
	for i, project := range projects {
		var read struct {
			ID    int64  `json:"id"`
			Value string `json:"value"`
		}
		call(restored, http.MethodGet, "/api/vault/credentials/recoverable"+selector(project), http.StatusOK, &read)
		require.Equal(t, ids[i], read.ID)
		require.True(t, read.Value == values[i], "restoring A must recover the exact retained synthetic value in every scope")
	}
	call(fixture, http.MethodGet, "/api/vault/status", http.StatusOK, &status)
	require.Equal(t, 4, status.Count)
	require.Equal(t, 3, status.Mismatch)
	call(restored, http.MethodDelete, "/api/vault/credentials/recoverable?project="+fixture.project, http.StatusOK, nil)
	call(restored, http.MethodGet, "/api/vault/credentials/recoverable?project="+fixture.project, http.StatusNotFound, nil)
	remaining := snapshot()
	require.Len(t, remaining, 3)
	for _, row := range remaining {
		for _, original := range before {
			if row.ID == original.ID {
				require.True(t, reflect.DeepEqual(row, original) && bytes.Equal(row.EncryptedSecret, original.EncryptedSecret), "explicit per-record delete must preserve every sibling field and ciphertext byte")
			}
		}
	}
	t.Log("PASS synthetic real-router/AES-GCM/PostgreSQL: bulk route retired; recoverable A ciphertext preserved under temporary B; read-only diagnostics and exact per-record deletion retained")
}
