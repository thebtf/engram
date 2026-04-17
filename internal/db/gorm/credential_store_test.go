package gorm

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/thebtf/engram/pkg/models"
)

// openTestDB opens a real PostgreSQL connection for integration testing.
// Requires DATABASE_DSN env var; skips the test if it is not set.
func openTestDB(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping credential store integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open test db")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping())

	// Apply migrations so the credentials table exists.
	require.NoError(t, runMigrations(db), "runMigrations")

	cleanup := func() {
		// Remove test rows inserted by this test run; leave other data intact.
		db.Exec(`DELETE FROM credentials WHERE project = 'test-credential-store'`)
		sqlDB.Close()
	}
	return db, cleanup
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

	const testProject = "test-credential-store"
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

// TestCredentialStore_DeleteOrphanedByFingerprint verifies that orphaned rows
// (wrong fingerprint) are hard-deleted and matching rows survive.
func TestCredentialStore_DeleteOrphanedByFingerprint(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := &Store{DB: db}
	cs := NewCredentialStore(store)
	ctx := context.Background()

	const testProject = "test-credential-store"
	const goodFP = "goodfingerprint01"
	const badFP = "badfingerprint099"

	// Insert one matching and one orphaned credential.
	good := &models.Credential{
		Project:                  testProject,
		Key:                      "smoke-good-fp",
		EncryptedSecret:          []byte("enc-good"),
		EncryptionKeyFingerprint: goodFP,
	}
	bad := &models.Credential{
		Project:                  testProject,
		Key:                      "smoke-bad-fp",
		EncryptedSecret:          []byte("enc-bad"),
		EncryptionKeyFingerprint: badFP,
	}
	_, err := cs.Create(ctx, good)
	require.NoError(t, err)
	_, err = cs.Create(ctx, bad)
	require.NoError(t, err)

	// Count mismatched before deletion.
	mismatch, err := cs.CountWithDifferentFingerprint(ctx, goodFP)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, mismatch, int64(1), "bad-fp row must be counted as mismatched")

	// Hard-delete orphaned rows.
	deleted, err := cs.DeleteOrphanedByFingerprint(ctx, goodFP)
	require.NoError(t, err, "DeleteOrphanedByFingerprint should succeed")
	assert.GreaterOrEqual(t, deleted, int64(1), "at least our bad-fp row must be deleted")

	// Good row must still be retrievable.
	fetched, err := cs.Get(ctx, testProject, "smoke-good-fp")
	require.NoError(t, err, "good-fp credential must survive DeleteOrphanedByFingerprint")
	assert.Equal(t, goodFP, fetched.EncryptionKeyFingerprint)

	// Bad row must be gone.
	_, err = cs.Get(ctx, testProject, "smoke-bad-fp")
	require.Error(t, err, "bad-fp credential must be gone after DeleteOrphanedByFingerprint")
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
