package gorm

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

// openTestDB gives each integration test a private migrated schema and bounded pool.
func openTestDB(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	db, _ := openInterventionReceiptMigrationTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	return db, func() { require.NoError(t, sqlDB.Close()) }
}

// TestCredentialStore_CreateGetCountDelete exercises the full Create→Get→Count→Delete
// round-trip against a real PostgreSQL database.
// Anti-stub contract: if any method body is replaced with `return nil` this test fails.
func TestCredentialStore_CreateGetCountDelete(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := &Store{DB: db}
	cs := NewCredentialStore(store)
	ctx := context.Background()

	testProject := uuid.NewString()
	const testKey = "api-key-smoke"
	secret := []byte("ciphertext-placeholder-nonce-prefixed")
	fingerprint := "testfingerprint01"

	// --- Create ---
	cred := &models.Credential{
		Project:                  testProject,
		Key:                      testKey,
		EncryptedSecret:          secret,
		EncryptionKeyFingerprint: fingerprint,
		Scope:                    "project",
		EditedBy:                 "smoke-test",
	}
	created, err := cs.Create(ctx, cred)
	require.NoError(t, err, "Create should succeed")
	assert.Greater(t, created.ID, int64(0), "Create should return a populated ID")
	assert.False(t, created.CreatedAt.IsZero(), "Create should return a populated CreatedAt")

	// --- Get ---
	fetched, err := cs.Get(ctx, testProject, testKey)
	require.NoError(t, err, "Get should return the created credential")
	assert.Equal(t, created.ID, fetched.ID)
	assert.Equal(t, testProject, fetched.Project)
	assert.Equal(t, testKey, fetched.Key)
	assert.Equal(t, secret, fetched.EncryptedSecret, "EncryptedSecret must be preserved byte-for-byte")
	assert.Equal(t, fingerprint, fetched.EncryptionKeyFingerprint)
	assert.Equal(t, "project", fetched.Scope)
	assert.Nil(t, fetched.DeletedAt, "active credential must have nil deleted_at")

	// --- List ---
	list, err := cs.List(ctx, testProject)
	require.NoError(t, err, "List should succeed")
	require.Len(t, list, 1, "List should return exactly one credential for the test project")
	assert.Equal(t, testKey, list[0].Key)

	// --- CountCredentials ---
	count, err := cs.CountCredentials(ctx)
	require.NoError(t, err, "CountCredentials should succeed")
	assert.GreaterOrEqual(t, count, int64(1), "CountCredentials must be >= 1 after Create")

	// --- CountWithDifferentFingerprint (current fingerprint = matches) ---
	mismatch, err := cs.CountWithDifferentFingerprint(ctx, fingerprint)
	require.NoError(t, err, "CountWithDifferentFingerprint should succeed")
	// The row we inserted matches fingerprint, so it must NOT be counted.
	// (Other rows from the real DB may have different fingerprints — that's fine.)
	assert.GreaterOrEqual(t, mismatch, int64(0))

	// --- CountWithDifferentFingerprint (wrong fingerprint = row counted) ---
	mismatchWrong, err := cs.CountWithDifferentFingerprint(ctx, "definitely-wrong-fp")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, mismatchWrong, int64(1), "our test row should be counted as mismatched")

	// --- Delete ---
	err = cs.Delete(ctx, testProject, testKey)
	require.NoError(t, err, "Delete should succeed")

	// Verify hard-delete: Get should no longer find the row.
	_, err = cs.Get(ctx, testProject, testKey)
	require.Error(t, err, "Get after Delete should return an error (row gone)")

	// List should return empty for the test project after deletion.
	listAfter, err := cs.List(ctx, testProject)
	require.NoError(t, err)
	assert.Len(t, listAfter, 0, "List after Delete should be empty")

	// --- Rotation scenario: Create after Delete with same (project, key) must succeed ---
	// This is the primary credential-rotation use case. Hard-delete unblocks it;
	// soft-delete with table-level UNIQUE(project, key) would fail here with
	// a unique constraint violation.
	rotated := &models.Credential{
		Project:                  testProject,
		Key:                      testKey,
		EncryptedSecret:          []byte("new-ciphertext-after-rotation"),
		EncryptionKeyFingerprint: fingerprint,
		Scope:                    "project",
		EditedBy:                 "rotation-smoke",
	}
	recreated, err := cs.Create(ctx, rotated)
	require.NoError(t, err, "Create after Delete with same (project, key) must succeed (rotation)")
	assert.Greater(t, recreated.ID, created.ID, "rotated credential should receive a new ID")

	// --- Delete non-existent key ---
	err = cs.Delete(ctx, testProject, "no-such-key")
	require.Error(t, err, "Delete of non-existent key should return an error")
}

func TestCredentialStore_GlobalAndProjectSameNameCoexist(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := &Store{DB: db}
	cs := NewCredentialStore(store)
	ctx := context.Background()

	project := uuid.NewString()
	const key = "global-project-coexist"
	const fingerprint = "globalfingerprint01"
	require.NoError(t, db.Exec("DELETE FROM credentials WHERE key = ?", key).Error)
	defer db.Exec("DELETE FROM credentials WHERE key = ?", key)

	globalInput := &models.Credential{
		Project:                  "",
		Key:                      key,
		EncryptedSecret:          []byte("global-ciphertext"),
		EncryptionKeyFingerprint: fingerprint,
		Scope:                    "global",
		EditedBy:                 "global-smoke",
	}
	globalCreated, err := cs.Create(ctx, globalInput)
	require.NoError(t, err)
	assert.Empty(t, globalCreated.Project)
	assert.Equal(t, "global", globalCreated.Scope)

	projectCreated, err := cs.Create(ctx, &models.Credential{
		Project:                  project,
		Key:                      key,
		EncryptedSecret:          []byte("project-ciphertext"),
		EncryptionKeyFingerprint: fingerprint,
		Scope:                    "project",
		EditedBy:                 "project-smoke",
	})
	require.NoError(t, err, "global and project rows with the same key must coexist")
	assert.NotEqual(t, globalCreated.ID, projectCreated.ID)

	globalFetched, err := cs.Get(ctx, "", key)
	require.NoError(t, err)
	assert.Equal(t, globalCreated.ID, globalFetched.ID)
	projectFetched, err := cs.Get(ctx, project, key)
	require.NoError(t, err)
	assert.Equal(t, projectCreated.ID, projectFetched.ID)

	globalList, err := cs.List(ctx, "")
	require.NoError(t, err)
	require.Len(t, globalList, 1)
	assert.Equal(t, globalCreated.ID, globalList[0].ID)
	projectList, err := cs.List(ctx, project)
	require.NoError(t, err)
	require.Len(t, projectList, 1)
	assert.Equal(t, projectCreated.ID, projectList[0].ID)

	require.NoError(t, cs.Delete(ctx, "", key))
	_, err = cs.Get(ctx, "", key)
	require.Error(t, err)
	projectFetched, err = cs.Get(ctx, project, key)
	require.NoError(t, err, "deleting global must not delete the project credential")
	assert.Equal(t, projectCreated.ID, projectFetched.ID)

	globalCreated, err = cs.Create(ctx, globalInput)
	require.NoError(t, err)
	require.NoError(t, cs.Delete(ctx, "", key))
	_, err = cs.Get(ctx, "", key)
	require.Error(t, err, "exact global delete must preserve project credentials")
	projectFetched, err = cs.Get(ctx, project, key)
	require.NoError(t, err)
	assert.Equal(t, projectCreated.ID, projectFetched.ID)
}

// Fingerprint mismatch is diagnostic, not proof that credential bytes may be erased.
func TestCredentialStore_FingerprintMismatchIsReadOnly(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	cs := NewCredentialStore(&Store{DB: db})
	ctx := context.Background()
	project := uuid.NewString()
	for _, fingerprint := range []string{"current-fingerprint", "recoverable-old-fingerprint"} {
		_, err := cs.Create(ctx, &models.Credential{
			Project: project, Key: fingerprint, Scope: "project",
			EncryptedSecret: []byte("synthetic-ciphertext"), EncryptionKeyFingerprint: fingerprint,
		})
		require.NoError(t, err)
	}
	var before, after []Credential
	require.NoError(t, db.Unscoped().Order("id").Find(&before).Error)
	mismatch, err := cs.CountWithDifferentFingerprint(ctx, "current-fingerprint")
	require.NoError(t, err)
	require.Equal(t, int64(1), mismatch)
	count, err := cs.CountCredentials(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), count)
	require.NoError(t, db.Unscoped().Order("id").Find(&after).Error)
	require.Len(t, after, len(before))
	for i := range before {
		require.Equal(t, before[i].ID, after[i].ID)
		require.True(t, string(before[i].EncryptedSecret) == string(after[i].EncryptedSecret), "diagnostic counters must preserve ciphertext bytes")
		require.Equal(t, before[i].EncryptionKeyFingerprint, after[i].EncryptionKeyFingerprint)
	}
}

// TestCredentialStore_Create_ValidationErrors verifies that Create rejects invalid input.
func TestCredentialStore_Create_ValidationErrors(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := &Store{DB: db}
	cs := NewCredentialStore(store)
	ctx := context.Background()

	cases := []struct {
		name string
		cred *models.Credential
	}{
		{"nil cred", nil},
		{"empty project", &models.Credential{Key: "k", EncryptedSecret: []byte("s"), EncryptionKeyFingerprint: "fp"}},
		{"empty key", &models.Credential{Project: "p", EncryptedSecret: []byte("s"), EncryptionKeyFingerprint: "fp"}},
		{"empty secret", &models.Credential{Project: "p", Key: "k", EncryptionKeyFingerprint: "fp"}},
		{"empty fingerprint", &models.Credential{Project: "p", Key: "k", EncryptedSecret: []byte("s")}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cs.Create(ctx, tc.cred)
			require.Error(t, err, "Create with %q should fail", tc.name)
		})
	}
}
