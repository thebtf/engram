package gorm

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDocumentStoreDeactivateDocumentReportsOnlyActiveTransition(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping document store integration test")
	}

	store, err := NewStore(Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	documents := NewDocumentStore(store)
	collection := "document-store-deactivate-" + uuid.NewString()
	path := "guide.md"
	body := "body " + uuid.NewString()
	ctx := context.Background()
	doc, err := documents.UpsertDocument(ctx, collection, path, "Guide", body)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.DB.WithContext(ctx).Exec(`DELETE FROM documents WHERE collection = ?`, collection).Error)
		require.NoError(t, store.DB.WithContext(ctx).Exec(`DELETE FROM content WHERE hash = ?`, doc.Hash.String).Error)
	})

	deactivated, err := documents.DeactivateDocument(ctx, collection, path)
	require.NoError(t, err)
	require.True(t, deactivated)
	deactivated, err = documents.DeactivateDocument(ctx, collection, path)
	require.NoError(t, err)
	require.False(t, deactivated)
	deactivated, err = documents.DeactivateDocument(ctx, collection, "missing.md")
	require.NoError(t, err)
	require.False(t, deactivated)

	active, err := documents.GetDocument(ctx, collection, path)
	require.NoError(t, err)
	require.Nil(t, active)
	content, err := documents.GetContent(ctx, doc.Hash.String)
	require.NoError(t, err)
	require.NotNil(t, content)
	require.Equal(t, body, content.Doc)
}
