package gorm

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/models"
)

func TestMemoryStore_PrincipalFieldsRoundTrip(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	ctx := context.Background()
	store := NewMemoryStore(&Store{DB: db})
	project := "test-memory-principal-roundtrip"
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM memories WHERE project = ?`, project).Error
	})

	created, err := store.Create(ctx, &models.Memory{
		Project:            project,
		Content:            "principal-owned memory roundtrip",
		OwnerPrincipal:     "agent/jeeves",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityPrivate,
		Domain:             "operator-console",
	})
	require.NoError(t, err)
	require.Equal(t, "agent/jeeves", created.OwnerPrincipal)
	require.Equal(t, "agent", created.OwnerPrincipalKind)
	require.Equal(t, models.AgentVisibilityPrivate, created.AgentVisibility)
	require.Equal(t, "operator-console", created.Domain)

	got, err := store.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.OwnerPrincipal, got.OwnerPrincipal)
	require.Equal(t, created.OwnerPrincipalKind, got.OwnerPrincipalKind)
	require.Equal(t, created.AgentVisibility, got.AgentVisibility)
	require.Equal(t, created.Domain, got.Domain)
}

func TestMemoryStore_PrincipalFieldsDefaultOwnedWritesToSharedHuman(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	ctx := context.Background()
	store := NewMemoryStore(&Store{DB: db})
	project := "test-memory-principal-defaults"
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM memories WHERE project = ?`, project).Error
	})

	created, err := store.Create(ctx, &models.Memory{
		Project:        project,
		Content:        "owned memory defaults",
		OwnerPrincipal: "operator/oleg",
	})
	require.NoError(t, err)
	require.Equal(t, "operator/oleg", created.OwnerPrincipal)
	require.Equal(t, "human", created.OwnerPrincipalKind)
	require.Equal(t, models.AgentVisibilityShared, created.AgentVisibility)
}

func TestMemoryStore_RejectsVisibilityWithoutOwner(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := NewMemoryStore(&Store{DB: db})
	_, err := store.Create(context.Background(), &models.Memory{
		Project:         "test-memory-principal-invalid",
		Content:         "visibility without principal",
		AgentVisibility: models.AgentVisibilityPrivate,
	})
	require.Error(t, err)
	require.True(t, strings.HasPrefix(err.Error(), "invalid_agent_visibility:"))
}

func TestMigration149_MemoryPrincipals(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	for _, col := range []string{"owner_principal", "owner_principal_kind", "agent_visibility", "domain"} {
		var count int
		require.NoError(t, db.Raw(`
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'memories' AND column_name = ?
		`, col).Scan(&count).Error)
		require.Equal(t, 1, count, "column %q must exist in memories", col)
	}

	for _, constraint := range []string{"memories_owner_principal_kind_chk", "memories_agent_visibility_chk"} {
		var count int
		require.NoError(t, db.Raw(`
			SELECT COUNT(*) FROM pg_constraint
			WHERE conname = ?
		`, constraint).Scan(&count).Error)
		require.Equal(t, 1, count, "constraint %q must exist", constraint)
	}

	for _, index := range []string{
		"idx_memories_owner_principal_created",
		"idx_memories_agent_visibility_created",
		"idx_memories_domain_owner",
	} {
		var count int
		require.NoError(t, db.Raw(`
			SELECT COUNT(*) FROM pg_indexes
			WHERE schemaname = 'public' AND tablename = 'memories' AND indexname = ?
		`, index).Scan(&count).Error)
		require.Equal(t, 1, count, "index %q must exist", index)
	}
}
