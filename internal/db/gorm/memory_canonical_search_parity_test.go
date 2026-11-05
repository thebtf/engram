package gorm

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

// The historical selector mirrors the synthetic retained-row shape in
// TestProjectPartitionRepairOrdinaryConsumers; it is not owner data.
func TestMemoryCanonicalSearchParity(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	consolidationTestDatabaseConfig(t, dsn, "source")
	const historicalSelector = "former-alpha"

	db, _ := openInterventionReceiptMigrationTestDB(t)
	ctx := t.Context()
	selected := sessionTestProject(t, db)
	other := sessionTestProject(t, db)
	require.NotEqual(t, selected, historicalSelector)
	access := scope.MemoryReadAccess{
		Caller:     scope.KeycardContext{Principal: "agent/alice", PrincipalKind: "agent", WorkstationID: "parity-ws"},
		Visibility: scope.MemoryVisibilityOptions{ApplyPrivacyScope: true},
	}
	seed := db.Begin()
	require.NoError(t, seed.Error)
	defer seed.Rollback()
	require.NoError(t, seed.Exec("SET LOCAL session_replication_role=replica").Error)
	create := func(project, owner, status string, until *time.Time) *Memory {
		t.Helper()
		row := &Memory{
			Project: project, Content: "parityneedle synthetic memory", Status: status,
			PrivacyScope: "project", OwnerPrincipal: owner, OwnerPrincipalKind: "agent",
			AgentVisibility: "private", Version: 3, ValidUntil: until,
		}
		require.NoError(t, seed.Create(row).Error)
		return row
	}
	past := time.Now().Add(-24 * time.Hour)
	historical := create(historicalSelector, "agent/alice", "active", nil)
	native := create(selected, "agent/alice", "active", nil)
	otherRow := create(other, "agent/alice", "active", nil)
	conflict := create(selected, "agent/alice", "active", nil)
	unassigned := create(selected, "agent/alice", "active", nil)
	private := create(selected, "agent/bob", "active", nil)
	privateWorkstation := create(selected, "agent/alice", "active", nil)
	privateWorkstation.PrivacyScope = "private"
	privateWorkstation.SourceWorkstationID = "other-ws"
	require.NoError(t, seed.Model(privateWorkstation).Updates(map[string]any{"privacy_scope": "private", "source_workstation_id": "other-ws"}).Error)
	expired := create(selected, "agent/alice", "active", &past)
	for _, entry := range []struct {
		row        *Memory
		importance float64
	}{
		{historical, 0.3}, {native, 0.4}, {conflict, 0.97}, {unassigned, 0.96}, {private, 0.99}, {privateWorkstation, 0.98},
	} {
		require.NoError(t, seed.Model(entry.row).Update("importance_base", entry.importance).Error)
	}
	member := func(row *Memory, project *string, index int) {
		t.Helper()
		require.NoError(t, seed.Create(&ConsolidationMembership{
			Reference: fmt.Sprintf("urn:engram:fixture:memory-search-parity:%d", index),
			Table:     "memories", RowKey: JSONRaw(fmt.Sprintf(`{"id":%d}`, row.ID)),
			FieldPath: JSONRaw(`["project"]`), ProjectID: project, PlanFingerprint: "synthetic-memory-search-parity",
		}).Error)
	}
	member(historical, &selected, 1)
	member(conflict, &selected, 2)
	member(conflict, &other, 3)
	member(unassigned, nil, 4)
	vector := "[" + strings.Repeat("0,", 1535) + "1]"
	for _, row := range []*Memory{historical, native, otherRow, conflict, unassigned, private, privateWorkstation, expired} {
		if row.ID == historical.ID || row.ID == native.ID {
			vector = "[0.1," + strings.Repeat("0,", 1534) + "1]"
		} else {
			vector = "[" + strings.Repeat("0,", 1535) + "1]"
		}
		require.NoError(t, seed.Exec(`INSERT INTO content_chunks(memory_id, seq, text, embedding, model) VALUES (?, 0, 'synthetic', ?::vector, 'fixture')`, row.ID, vector).Error)
		if row.ID != native.ID {
			require.NoError(t, seed.Create(&graph.Edge{SourceID: &native.ID, TargetID: &row.ID, EdgeType: graph.EdgeUses, SourceType: "memory", TargetType: "memory"}).Error)
		}
	}
	require.NoError(t, seed.Commit().Error)
	store := NewMemoryStore(&Store{DB: db})

	detail, err := store.GetVisible(ctx, historical.ID, selected, access.Caller, access.Visibility)
	require.NoError(t, err, "the canonical detail reader must authorize the retained occurrence")
	require.Equal(t, historical.Content, detail.Content)
	require.Equal(t, selected, detail.Project)
	require.Equal(t, historical.Version, detail.Version)
	for _, row := range []*Memory{otherRow, conflict, unassigned, private, privateWorkstation} {
		_, err := store.GetVisible(ctx, row.ID, selected, access.Caller, access.Visibility)
		assert.Error(t, err, "detail must deny nonmember, conflicting, unassigned or private row %d", row.ID)
	}

	check := func(t *testing.T, rows []*models.Memory) {
		t.Helper()
		ids := make([]int64, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
			assert.Equal(t, selected, row.Project, "only authorized rows may be projected into the selected project")
		}
		assert.ElementsMatch(t, []int64{historical.ID, native.ID}, ids, "candidate pool must include the authorized historical row and native positive control, but not other, conflicting, unassigned, private or expired rows")
	}
	t.Run("candidate_list", func(t *testing.T) {
		rows, err := store.ListForSearch(ctx, selected, 50, access)
		require.NoError(t, err)
		check(t, rows)
	})
	t.Run("policy_before_limit", func(t *testing.T) {
		rows, err := store.ListForSearch(ctx, selected, 1, access)
		require.NoError(t, err)
		require.Len(t, rows, 1, "inaccessible and expired newer rows must not consume the limit")
		assert.Equal(t, native.ID, rows[0].ID)
		page, err := ListMemoryPageForSearch(ctx, store, selected, 1, 0, access)
		require.NoError(t, err)
		require.Len(t, page, 1)
		assert.Equal(t, native.ID, page[0].ID, "caller-aware pagination must exclude denied newer rows before LIMIT")
		page, err = store.SearchLiteralForSearch(ctx, selected, "parityneedle", 1, 1, access)
		require.NoError(t, err)
		require.Len(t, page, 1)
		assert.Equal(t, historical.ID, page[0].ID)
	})
	t.Run("literal", func(t *testing.T) {
		rows, err := store.SearchLiteralForSearch(ctx, selected, "parityneedle", 50, 0, access)
		require.NoError(t, err)
		check(t, rows)
	})
	t.Run("fts", func(t *testing.T) {
		rows, _, err := store.SearchFTSForSearch(ctx, selected, "parityneedle", 50, access)
		require.NoError(t, err)
		check(t, rows)
	})
	t.Run("exact_materialization", func(t *testing.T) {
		refs := []scope.MemoryRef{scope.MemoryReference(detail)}
		for _, row := range []*Memory{native, otherRow, conflict, unassigned, private, privateWorkstation, expired} {
			refs = append(refs, scope.MemoryReference(memoryRowToModel(row)))
		}
		rows, err := store.GetByRefsForSearch(ctx, selected, refs, access)
		require.NoError(t, err)
		check(t, rows)
		wrong := scope.MemoryReference(detail)
		wrong.Version++
		rows, err = store.GetByRefsForSearch(ctx, selected, []scope.MemoryRef{wrong}, access)
		require.NoError(t, err)
		assert.Empty(t, rows, "wrong version must not materialize")
		wrong = scope.MemoryReference(detail)
		wrong.ContentDigest = scope.MemoryContentDigest("different body")
		rows, err = store.GetByRefsForSearch(ctx, selected, []scope.MemoryRef{wrong}, access)
		require.NoError(t, err)
		assert.Empty(t, rows, "wrong content digest must not materialize")
	})
	t.Run("legacy_list_and_id_readers", func(t *testing.T) {
		want := []int64{historical.ID, native.ID, private.ID, privateWorkstation.ID}
		checkTrusted := func(rows []*models.Memory, err error) {
			t.Helper()
			require.NoError(t, err)
			ids := make([]int64, 0, len(rows))
			for _, row := range rows {
				ids = append(ids, row.ID)
				assert.Equal(t, selected, row.Project)
			}
			assert.ElementsMatch(t, want, ids)
		}
		rows, err := store.ListWithFilters(ctx, selected, ListOptions{Limit: 50})
		checkTrusted(rows, err)
		rows, err = store.ListWithOffset(ctx, selected, 50, 0)
		checkTrusted(rows, err)
		rows, err = store.SearchLiteralWithOffset(ctx, selected, "parityneedle", 50, 0)
		checkTrusted(rows, err)
		rows, err = store.SearchFTS(ctx, selected, "parityneedle", 50)
		checkTrusted(rows, err)
		rows, err = store.GetByIDs(ctx, selected, []int64{historical.ID, native.ID, otherRow.ID, conflict.ID, unassigned.ID, private.ID, privateWorkstation.ID, expired.ID})
		checkTrusted(rows, err)
		rows, err = store.ListForInjection(ctx, selected, 50)
		checkTrusted(rows, err)
		rows, err = store.ListForInjectionForSearch(ctx, selected, 1, access)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, native.ID, rows[0].ID, "private and conflicting higher-importance rows must not consume the injection limit")
		rows, err = store.ListWithFiltersForSearch(ctx, selected, ListOptions{Limit: 50}, access)
		require.NoError(t, err)
		check(t, rows)
	})
	t.Run("principal_membership_before_pagination", func(t *testing.T) {
		opts := ListOptions{OwnerPrincipal: "agent/alice", OwnerPrincipalKind: "agent", IDs: []int64{historical.ID, native.ID, otherRow.ID, conflict.ID, unassigned.ID}, Limit: 1}
		rows, err := store.ListPrincipalMemory(ctx, selected, opts)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, native.ID, rows[0].ID, "conflicting and unassigned memberships must not consume the first page")
		opts.Offset = 1
		rows, err = store.ListPrincipalMemory(ctx, selected, opts)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, historical.ID, rows[0].ID)
		assert.Equal(t, selected, rows[0].Project)
	})
	t.Run("vector", func(t *testing.T) {
		vec := make([]float32, 1536)
		vec[1535] = 1
		vectors := embedding.NewStore(db)
		legacy, err := vectors.FindSimilarForProject(ctx, selected, vec, 50, 0.5)
		require.NoError(t, err)
		var ids []int64
		for _, item := range legacy {
			ids = append(ids, item.MemoryID)
		}
		assert.ElementsMatch(t, []int64{historical.ID, native.ID, private.ID, privateWorkstation.ID}, ids)
		visible, err := vectors.FindSimilarForSearch(ctx, selected, vec, 50, 0.5, access)
		require.NoError(t, err)
		ids = nil
		for _, item := range visible {
			ids = append(ids, item.MemoryID)
		}
		assert.ElementsMatch(t, []int64{historical.ID, native.ID}, ids)
		visible, err = vectors.FindSimilarForSearch(ctx, selected, vec, 1, 0.5, access)
		require.NoError(t, err)
		require.Len(t, visible, 1, "inaccessible closer vectors must not consume top-k")
		assert.Equal(t, historical.ID, visible[0].MemoryID)
	})
	t.Run("graph", func(t *testing.T) {
		refs, err := graph.NewStore(db, nil).NeighborsForSearch(ctx, selected, []int64{native.ID}, nil, 20, access)
		require.NoError(t, err)
		require.Len(t, refs, 1)
		assert.Equal(t, scope.MemoryReference(detail), refs[0])
		refs, err = graph.NewStore(db, nil).NeighborsForSearch(ctx, selected, []int64{conflict.ID}, nil, 20, access)
		require.NoError(t, err)
		assert.Empty(t, refs)
	})
	t.Run("invalid_or_removed_project_fails_closed", func(t *testing.T) {
		for _, selector := range []string{"former-alpha", uuid.NewString()} {
			rows, err := store.ListForSearch(ctx, selector, 50, access)
			require.NoError(t, err)
			assert.Empty(t, rows)
		}
		require.NoError(t, db.Model(&Project{}).Where("id = ?", selected).Update("removed_at", time.Now()).Error)
		rows, err := store.ListForSearch(ctx, selected, 50, access)
		require.NoError(t, err)
		assert.Empty(t, rows)
		rows, err = store.GetByRefsForSearch(ctx, selected, []scope.MemoryRef{scope.MemoryReference(detail)}, access)
		require.NoError(t, err)
		assert.Empty(t, rows)
	})
}
