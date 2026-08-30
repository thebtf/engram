package gorm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTokenStoreExpiryRoundTripsCreateListGetAndPrefixLookup(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := NewTokenStore(&Store{DB: db})
	name := fmt.Sprintf("zz-token-expiry-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM api_tokens WHERE name = ?`, name).Error })
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)

	created, err := store.Create(context.Background(), name, "expiry-hash", "expiry001", "read-write", &expiresAt)
	require.NoError(t, err)
	require.NotNil(t, created.ExpiresAt)
	require.WithinDuration(t, expiresAt, *created.ExpiresAt, time.Microsecond)

	loaded, err := store.GetByID(context.Background(), created.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.NotNil(t, loaded.ExpiresAt)
	require.WithinDuration(t, expiresAt, *loaded.ExpiresAt, time.Microsecond)

	listed, err := store.List(context.Background())
	require.NoError(t, err)
	var listedToken *APIToken
	for index := range listed {
		if listed[index].ID == created.ID {
			listedToken = &listed[index]
			break
		}
	}
	require.NotNil(t, listedToken)
	require.NotNil(t, listedToken.ExpiresAt)
	require.WithinDuration(t, expiresAt, *listedToken.ExpiresAt, time.Microsecond)

	candidates, err := store.FindByPrefix(context.Background(), "expiry001")
	require.NoError(t, err)
	var candidate *APIToken
	for index := range candidates {
		if candidates[index].ID == created.ID {
			candidate = &candidates[index]
			break
		}
	}
	require.NotNil(t, candidate)
	require.NotNil(t, candidate.ExpiresAt)
	require.WithinDuration(t, expiresAt, *candidate.ExpiresAt, time.Microsecond)
}
