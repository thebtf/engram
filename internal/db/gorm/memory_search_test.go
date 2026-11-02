package gorm

import (
	"testing"

	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"

	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

func TestMemorySearchMaterialization_DigestAndACL(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := NewMemoryStore(&Store{DB: db})
	project := sessionTestProject(t, db)
	access := scope.MemoryReadAccess{
		Caller:     scope.KeycardContext{Principal: "agent/alice", PrincipalKind: "agent", WorkstationID: "search-ws"},
		Visibility: scope.MemoryVisibilityOptions{ApplyPrivacyScope: true},
	}
	memory, err := store.Create(t.Context(), &models.Memory{
		Project: project, Content: "initial body", PrivacyScope: "project",
		OwnerPrincipal: "agent/alice", OwnerPrincipalKind: "agent", AgentVisibility: "private",
		Tags: []string{"digest-regression"},
	})
	require.NoError(t, err)
	edit := func(current *models.Memory, content string) *models.Memory {
		t.Helper()
		next := *current
		next.Content = content
		updated, updateErr := store.Update(t.Context(), &next)
		require.NoError(t, updateErr)
		return updated
	}
	memory = edit(memory, "snapshot version two")
	snapshot, err := store.GetForSnapshot(t.Context(), memory.ID)
	require.NoError(t, err)
	require.Equal(t, 2, snapshot.Version)
	memory = edit(memory, "selected version three body A — память\n")
	selected, err := store.ListForSearch(t.Context(), project, 10, access)
	require.NoError(t, err)
	require.Len(t, selected, 1)
	selectedRef := scope.MemoryReference(selected[0])
	require.Equal(t, 3, selectedRef.Version)
	rows, err := store.GetByRefsForSearch(t.Context(), project, []scope.MemoryRef{selectedRef}, access)
	require.NoError(t, err)
	require.Len(t, rows, 1, "unchanged UTF-8 bodies must survive SQL/Go digest comparison")

	// Use the actual rollback writer and ordinary edit writer, in deterministic
	// serial order: the version number is reused without preserving body identity.
	require.NoError(t, db.Transaction(func(tx *gormlib.DB) error {
		return store.RestoreRawTx(t.Context(), tx, snapshot)
	}))
	replacement := edit(snapshot, "replacement version three body B — память\n")
	require.Equal(t, selectedRef.Version, replacement.Version)
	require.NotEqual(t, selectedRef.ContentDigest, scope.MemoryContentDigest(replacement.Content))
	rows, err = store.GetByRefsForSearch(t.Context(), project, []scope.MemoryRef{selectedRef}, access)
	require.NoError(t, err)
	require.Empty(t, rows, "a rollback/edit ABA must not substitute body B for selected body A")
	replacementRef := scope.MemoryReference(replacement)
	rows, err = store.GetByRefsForSearch(t.Context(), project, []scope.MemoryRef{replacementRef}, access)
	require.NoError(t, err)
	require.Len(t, rows, 1, "a newly selected replacement remains useful")
	require.Equal(t, replacement.Content, rows[0].Content)
	rows, err = store.GetByRefsForSearch(t.Context(), project, []scope.MemoryRef{selectedRef, replacementRef}, access)
	require.NoError(t, err)
	require.Len(t, rows, 1, "competing leg references retain only the exact current identity")
	require.Equal(t, replacement.Content, rows[0].Content)

	for _, test := range []struct {
		name    string
		changes map[string]any
	}{
		{"principal", map[string]any{"owner_principal": "agent/bob"}},
		{"domain", map[string]any{"owner_principal": "agent/bob", "agent_visibility": "shared", "domain": "private-domain"}},
		{"workstation", map[string]any{"privacy_scope": "private", "source_workstation_id": "other-ws"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, db.Model(&Memory{}).Where("id = ?", replacement.ID).Updates(map[string]any{
				"owner_principal": "agent/alice", "owner_principal_kind": "agent", "agent_visibility": "private",
				"domain": "", "privacy_scope": "project", "source_workstation_id": "",
			}).Error)
			require.NoError(t, db.Model(&Memory{}).Where("id = ?", replacement.ID).Updates(test.changes).Error)
			current, currentErr := store.GetForSnapshot(t.Context(), replacement.ID)
			require.NoError(t, currentErr)
			require.True(t, replacementRef.Matches(current), "ACL mutation deliberately preserves version and body digest")
			denied, loadErr := store.GetByRefsForSearch(t.Context(), project, []scope.MemoryRef{replacementRef}, access)
			require.NoError(t, loadErr)
			require.Empty(t, denied, "matching body identity never replaces current ACL authorization")
		})
	}
}

func TestMemorySearchFTSFallback_TracksAuthorizedEvidence(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := NewMemoryStore(&Store{DB: db})
	project := sessionTestProject(t, db)
	access := scope.MemoryReadAccess{
		Caller:     scope.KeycardContext{Principal: "agent/alice", PrincipalKind: "agent", WorkstationID: "search-ws"},
		Visibility: scope.MemoryVisibilityOptions{ApplyPrivacyScope: true},
	}
	create := func(body, principal, privacy, workstation string) *models.Memory {
		t.Helper()
		memory, err := store.Create(t.Context(), &models.Memory{
			Project: project, Content: body, Tags: []string{"fallback-regression"},
			OwnerPrincipal: principal, OwnerPrincipalKind: "agent", AgentVisibility: "private",
			PrivacyScope: privacy, SourceWorkstationID: workstation,
		})
		require.NoError(t, err)
		return memory
	}
	semantic := create("ClaudeReset restores an exhausted assistant conversation.", "agent/alice", "project", "")
	lexical := create("ownerworkstation confidential synthetic notes.", "agent/alice", "private", "search-ws")
	hidden := create("ClaudeReset principalonly confidential synthetic notes.", "agent/bob", "project", "")
	rows, partial, err := store.SearchFTSForSearch(t.Context(), project, hidden.Content, 50, access)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Len(t, partial, 2)
	for _, memory := range rows {
		require.NotEqual(t, hidden.ID, memory.ID)
		require.True(t, partial[memory.ID], "authorized OR-recovery evidence must retain its weaker match class")
	}
	require.Equal(t, lexical.ID, rows[0].ID, "the actual FTS rank puts the multi-lexeme partial hit first")
	strict, strictPartial, err := store.SearchFTSForSearch(t.Context(), project, "ClaudeReset", 50, access)
	require.NoError(t, err)
	require.Len(t, strict, 1)
	require.Equal(t, semantic.ID, strict[0].ID)
	require.Empty(t, strictPartial, "strict matches must keep their existing relevance class")
}
