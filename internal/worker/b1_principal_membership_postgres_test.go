package worker

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/scope"
)

// Uses only the disposable schema selected by newOwnCardContextFixture.
func TestB1PrincipalMembershipBeforePaginationPostgres(t *testing.T) {
	f := newOwnCardContextFixture(t, false)
	selected, other := f.project(t), f.project(t)
	create := func(project, content string) *gormdb.Memory {
		t.Helper()
		row := &gormdb.Memory{Project: project, Content: content, Status: "active", PrivacyScope: "project", OwnerPrincipal: "agent/synthetic-owner", OwnerPrincipalKind: "agent", AgentVisibility: "shared", Version: 1}
		require.NoError(t, f.store.DB.Create(row).Error)
		return row
	}
	historical := create(other, "historical member")
	native := create(selected, "native member")
	conflict := create(selected, "conflicting member")
	unassigned := create(selected, "unassigned member")
	member := func(row *gormdb.Memory, project *string, index int) {
		t.Helper()
		require.NoError(t, f.store.DB.Create(&gormdb.ConsolidationMembership{
			Reference: fmt.Sprintf("urn:engram:b1:principal-membership:%d", index), Table: "memories",
			RowKey: gormdb.JSONRaw(fmt.Sprintf(`{"id":%d}`, row.ID)), FieldPath: gormdb.JSONRaw(`["project"]`),
			ProjectID: project, PlanFingerprint: "synthetic-b1-principal-membership",
		}).Error)
	}
	member(historical, &selected, 1)
	member(conflict, &selected, 2)
	member(conflict, &other, 3)
	member(unassigned, nil, 4)
	store := gormdb.NewMemoryStore(f.store)
	opts := gormdb.ListOptions{OwnerPrincipal: "agent/synthetic-owner", OwnerPrincipalKind: "agent", Limit: 1, ContextCaller: scope.KeycardContext{}}
	first, err := store.ListPrincipalMemory(t.Context(), selected, opts)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Equal(t, native.ID, first[0].ID)
	opts.Offset = 1
	second, err := store.ListPrincipalMemory(t.Context(), selected, opts)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, historical.ID, second[0].ID)
	require.Equal(t, selected, second[0].Project)
	var persisted gormdb.Memory
	require.NoError(t, f.store.DB.First(&persisted, historical.ID).Error)
	require.Equal(t, other, persisted.Project, "read projection must not replace native provenance")
}
