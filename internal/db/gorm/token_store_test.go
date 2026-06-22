package gorm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTokenStore_CreateWithPrincipalRoundTrip(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	ctx := context.Background()
	store := NewTokenStore(&Store{DB: db})
	name := fmt.Sprintf("zz-test-principal-token-%d", time.Now().UnixNano())
	prefix := "pim2abcd"
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM api_tokens WHERE name = ?`, name).Error
	})

	created, err := store.CreateWithPrincipal(ctx, name, "hash-principal", prefix, "read-write", "agent/codex", "agent")
	require.NoError(t, err)
	require.Equal(t, "agent/codex", created.Principal)
	require.Equal(t, "agent", created.PrincipalKind)

	got, err := store.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "agent/codex", got.Principal)
	require.Equal(t, "agent", got.PrincipalKind)

	candidates, err := store.FindByPrefix(ctx, prefix)
	require.NoError(t, err)
	require.NotEmpty(t, candidates)
	var matched bool
	for _, candidate := range candidates {
		if candidate.ID == created.ID {
			require.Equal(t, "agent/codex", candidate.Principal)
			require.Equal(t, "agent", candidate.PrincipalKind)
			matched = true
		}
	}
	require.True(t, matched, "created token must be returned by prefix lookup")
}
