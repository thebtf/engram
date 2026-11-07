package gorm

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTaskContextDocumentSearchCurrentScopedBody(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewVersionedDocumentStore(&Store{DB: db})
	ctx := t.Context()
	project, other := uuid.NewString(), uuid.NewString()
	_, err := store.Create(ctx, "current.md", project, "obsolete hedgehog passage", "markdown", "{}", "fixture")
	require.NoError(t, err)
	body := strings.Repeat("meaningful hedgehog body ", 800)
	id, err := store.Create(ctx, "current.md", project, body, "markdown", "{}", "fixture")
	require.NoError(t, err)
	_, err = store.Create(ctx, "secret.md", other, "hedgehog confidential", "markdown", "{}", "fixture")
	require.NoError(t, err)
	rows, err := store.SearchTaskContext(ctx, project, "hedgehog", 3)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, id, rows[0].ID)
	require.Equal(t, 2, rows[0].Version)
	require.Equal(t, body, rows[0].Content)
	digest := sha256.Sum256([]byte(body))
	require.Equal(t, hex.EncodeToString(digest[:]), rows[0].ContentHash)
	rows, err = store.SearchTaskContext(ctx, project, "hedgehog absentuniquetoken", 3)
	require.NoError(t, err)
	require.Empty(t, rows, "query must not be widened into an arbitrary OR")
	rows, err = store.SearchTaskContext(ctx, project, "obsolete", 3)
	require.NoError(t, err)
	require.Empty(t, rows)
	rows, err = store.SearchTaskContext(ctx, other, "meaningful", 3)
	require.NoError(t, err)
	require.Empty(t, rows)
	_, err = store.Create(ctx, "rank-low.md", project, "rankneedle", "markdown", "{}", "fixture")
	require.NoError(t, err)
	highID, err := store.Create(ctx, "rank-high.md", project, "rankneedle rankneedle rankneedle rankneedle", "markdown", "{}", "fixture")
	require.NoError(t, err)
	_, err = store.Create(ctx, "rank-private.md", other, strings.Repeat("rankneedle ", 30), "markdown", "{}", "fixture")
	require.NoError(t, err)
	rows, err = store.SearchTaskContext(ctx, project, "rankneedle", 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, highID, rows[0].ID, "ranking and project scope must precede the limit")
}
