package gorm

// memory_store_lifecycle_test.go — unit tests for the Create / CreateWithLifecycle
// contract separation (P1-3 fix: flag-off byte-identity).
//
// These tests require DATABASE_DSN (same guard as credential_store_test.go) because
// they verify actual DB-round-trip behavior. Without a live DB we can only verify
// the struct-level logic; the authoritative check is the DB default-value behavior.
//
// Unit-only (no DB) tests verify that:
//   - Plain Create with lifecycle-populated memory fields does NOT populate the
//     Tier/EpistemicType/Defeasibility columns on the returned model — the DB
//     defaults remain authoritative (the columns stay at DB-default values).
//   - CreateWithLifecycle WITH lifecycle fields returns those fields verbatim.
//
// Integration tests (DB required) are in TestMemoryStore_CreateGetUpdateListDelete.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/models"
)

// TestMemoryStore_Create_StripsLifecycleFields verifies that plain Create ignores
// Tier, EpistemicType, and Defeasibility fields supplied by the caller (P1-3).
// The DB schema defaults stay authoritative; the caller's lifecycle values are
// not persisted by Create.
//
// This is the "byte-identity contract" test: a caller that passes lifecycle fields
// to plain Create must get back a row whose lifecycle fields reflect DB defaults,
// not the caller's values.
func TestMemoryStore_Create_StripsLifecycleFields(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	defer db.Exec(`DELETE FROM memories WHERE project = 'test-lifecycle-create-strip'`)

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	ctx := context.Background()

	mem := &models.Memory{
		Project:     "test-lifecycle-create-strip",
		Content:     "test content with lifecycle fields",
		SourceAgent: "test-strip",
		// These MUST NOT be persisted by plain Create.
		Tier:          "episodic",
		EpistemicType: "decision",
		Defeasibility: "tentative",
	}

	created, err := ms.Create(ctx, mem)
	require.NoError(t, err)
	assert.Greater(t, created.ID, int64(0))

	// Reload from DB to verify what was actually stored.
	fetched, err := ms.Get(ctx, created.ID)
	require.NoError(t, err)

	// The DB schema default for tier is "semantic"; we assert that the caller's
	// "episodic" value was NOT persisted by plain Create.
	assert.NotEqual(t, "episodic", fetched.Tier,
		"plain Create must not persist caller-supplied Tier (milestone-B byte-identity contract)")
	assert.NotEqual(t, "decision", fetched.EpistemicType,
		"plain Create must not persist caller-supplied EpistemicType")
	assert.NotEqual(t, "tentative", fetched.Defeasibility,
		"plain Create must not persist caller-supplied Defeasibility")

	// Input struct must not be mutated.
	assert.Equal(t, "episodic", mem.Tier, "caller's input struct must not be mutated")
}

// TestMemoryStore_CreateWithLifecycle_PersistsFields verifies that CreateWithLifecycle
// correctly persists Tier, EpistemicType, and Defeasibility (P1-3).
func TestMemoryStore_CreateWithLifecycle_PersistsFields(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	defer db.Exec(`DELETE FROM memories WHERE project = 'test-lifecycle-create-with'`)

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	ctx := context.Background()

	mem := &models.Memory{
		Project:       "test-lifecycle-create-with",
		Content:       "test content with lifecycle fields persisted",
		SourceAgent:   "crystallization",
		Tier:          "episodic",
		EpistemicType: "decision",
		Defeasibility: "tentative",
	}

	created, err := ms.CreateWithLifecycle(ctx, mem)
	require.NoError(t, err)
	assert.Greater(t, created.ID, int64(0))

	// Reload from DB to verify what was actually stored.
	fetched, err := ms.Get(ctx, created.ID)
	require.NoError(t, err)

	assert.Equal(t, "episodic", fetched.Tier, "CreateWithLifecycle must persist Tier")
	assert.Equal(t, "decision", fetched.EpistemicType, "CreateWithLifecycle must persist EpistemicType")
	assert.Equal(t, "tentative", fetched.Defeasibility, "CreateWithLifecycle must persist Defeasibility")

	// Input struct must not be mutated.
	assert.Equal(t, int64(0), mem.ID, "CreateWithLifecycle must not mutate caller's input ID")
}

// TestMemoryStore_ListBySourceAgentAndTag_FindsFingerprint verifies the idempotency
// query used by the crystallization pipeline (P2-5).
func TestMemoryStore_ListBySourceAgentAndTag_FindsFingerprint(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	defer db.Exec(`DELETE FROM memories WHERE project = 'test-list-by-tag'`)

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	ctx := context.Background()

	// Insert a crystallization memory with a session tag and a fingerprint tag.
	mem := &models.Memory{
		Project:       "test-list-by-tag",
		Content:       "decided to use PostgreSQL because it scales.",
		SourceAgent:   "crystallization",
		Tags:          []string{"crystallization", "session:sess-abc", "fp:deadbeef01234567"},
		Tier:          "episodic",
		EpistemicType: "decision",
	}
	created, err := ms.CreateWithLifecycle(ctx, mem)
	require.NoError(t, err)
	assert.Greater(t, created.ID, int64(0))

	// Query by source_agent + session tag — must find the row.
	found, err := ms.ListBySourceAgentAndTag(ctx, "test-list-by-tag", "crystallization", "session:sess-abc")
	require.NoError(t, err)
	require.Len(t, found, 1, "should find exactly one matching memory")
	assert.Equal(t, created.ID, found[0].ID)

	// Verify the fingerprint tag is present in the returned row's tags.
	assert.Contains(t, found[0].Tags, "fp:deadbeef01234567", "fingerprint tag must be returned")

	// Query with a different session tag — must return empty.
	notFound, err := ms.ListBySourceAgentAndTag(ctx, "test-list-by-tag", "crystallization", "session:sess-other")
	require.NoError(t, err)
	assert.Empty(t, notFound, "different session tag must not match")
}
