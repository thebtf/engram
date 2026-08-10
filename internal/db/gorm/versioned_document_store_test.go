package gorm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVersionedDocumentStore_ListTreatsPathPrefixAsLiteralText(t *testing.T) {
	db := openCandidateTestDB(t)
	store := NewVersionedDocumentStore(&Store{DB: db})
	ctx := context.Background()
	project := fmt.Sprintf("literal-document-prefix-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = db.Exec("DELETE FROM versioned_documents WHERE project = ?", project).Error })

	paths := []string{
		`notes_1/exact.md`, `notesX1/sibling.md`,
		`notes%literal/exact.md`, `notesZliteral/sibling.md`,
		`notes\root/exact.md`, `notesXroot/sibling.md`,
		`ordinary/exact.md`, `ordinary-other/sibling.md`,
	}
	for _, path := range paths {
		_, err := store.Create(ctx, path, project, path, "markdown", "{}", "mb1-test")
		require.NoError(t, err)
	}

	for _, tc := range []struct {
		prefix string
		want   string
	}{
		{prefix: `notes_1/`, want: `notes_1/exact.md`},
		{prefix: `notes%literal/`, want: `notes%literal/exact.md`},
		{prefix: `notes\root/`, want: `notes\root/exact.md`},
		{prefix: `ordinary/`, want: `ordinary/exact.md`},
	} {
		t.Run(tc.prefix, func(t *testing.T) {
			docs, err := store.List(ctx, project, "", tc.prefix, 20)
			require.NoError(t, err)
			require.Len(t, docs, 1)
			require.Equal(t, tc.want, docs[0].Path)
		})
	}
}
