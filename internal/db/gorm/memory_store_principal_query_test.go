package gorm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/models"
)

func TestMemoryStore_ListPrincipalMemory_FiltersAndPagination(t *testing.T) {
	if testing.Short() {
		t.Skip("principal memory query requires live PostgreSQL; skipped in short mode")
	}
	db, cleanup := openTestDB(t)
	defer cleanup()

	const (
		projectA = "test-principal-query-a"
		projectB = "test-principal-query-b"
		domain   = "operator-console"
	)
	defer db.Exec(`DELETE FROM memories WHERE project IN (?, ?)`, projectA, projectB)

	ctx := context.Background()
	ms := NewMemoryStore(&Store{DB: db})
	now := time.Now().UTC()

	insertRow := func(project, content, owner, kind, visibility, rowDomain string, createdAt time.Time) int64 {
		t.Helper()
		row := &Memory{
			Project:                  project,
			Content:                  content,
			Status:                   "active",
			OwnerPrincipal:           owner,
			OwnerPrincipalKind:       kind,
			AgentVisibility:          visibility,
			Domain:                   rowDomain,
			CreatedAt:                createdAt,
			UpdatedAt:                createdAt,
			PrivacyScope:             "project",
			ImportanceBase:           0.5,
			TsAlpha:                  1.0,
			TsBeta:                   1.0,
			Version:                  1,
			Confidence:               0.8,
			Stability:                30,
			Retrievability:           1,
			InjectionCount:           0,
			AccessCount:              0,
			CitationCount:            0,
			RecurrenceCount:          0,
			PromotionTarget:          "none",
			Tier:                     "episodic",
			EpistemicType:            "observation",
			Defeasibility:            "slow",
			SourceWorkstationID:      "",
			SourceSessions:           nil,
			LastRetrievedAt:          nil,
			LastConfirmed:            nil,
			ReviewAfter:              nil,
			SupersedesID:             nil,
			SupersededBy:             nil,
			ConsecutiveCitationCount: 0,
		}
		require.NoError(t, db.Create(row).Error, "insert fixture row")
		return row.ID
	}

	hiddenWrongOwner := insertRow(projectA, "bob private newest", "agent/bob", "agent", models.AgentVisibilityPrivate, domain, now.Add(6*time.Minute))
	hiddenWrongDomain := insertRow(projectA, "alice other domain newest", "agent/alice", "agent", models.AgentVisibilityShared, "other-domain", now.Add(5*time.Minute))
	aliceNewest := insertRow(projectA, "alice visible newest", "agent/alice", "agent", models.AgentVisibilityShared, domain, now.Add(4*time.Minute))
	aliceSecond := insertRow(projectA, "alice visible second", "agent/alice", "agent", models.AgentVisibilityShared, domain, now.Add(3*time.Minute))
	legacyNoPrincipal := insertRow(projectA, "legacy no-principal memory", "", "", "", domain, now.Add(2*time.Minute))
	aliceOtherProject := insertRow(projectB, "alice visible other project", "agent/alice", "agent", models.AgentVisibilityShared, domain, now.Add(time.Minute))

	t.Run("owner domain visibility predicates run before limit", func(t *testing.T) {
		res, err := ms.ListPrincipalMemory(ctx, projectA, ListOptions{
			OwnerPrincipal:     "agent/alice",
			OwnerPrincipalKind: "agent",
			AgentVisibility:    models.AgentVisibilityShared,
			Domain:             domain,
			Limit:              2,
		})
		require.NoError(t, err)
		require.Equal(t, []int64{aliceNewest, aliceSecond}, collectIDs(res))
		assert.NotContains(t, collectIDs(res), hiddenWrongOwner)
		assert.NotContains(t, collectIDs(res), hiddenWrongDomain)
	})

	t.Run("offset is applied after principal predicates", func(t *testing.T) {
		res, err := ms.ListPrincipalMemory(ctx, projectA, ListOptions{
			OwnerPrincipal:     "agent/alice",
			OwnerPrincipalKind: "agent",
			Domain:             domain,
			Limit:              1,
			Offset:             1,
		})
		require.NoError(t, err)
		require.Equal(t, []int64{aliceSecond}, collectIDs(res))
	})

	t.Run("empty project is allowed only on principal query seam", func(t *testing.T) {
		res, err := ms.ListPrincipalMemory(ctx, "", ListOptions{
			OwnerPrincipal:     "agent/alice",
			OwnerPrincipalKind: "agent",
			Domain:             domain,
			Limit:              10,
		})
		require.NoError(t, err)
		ids := collectIDs(res)
		assert.Contains(t, ids, aliceNewest)
		assert.Contains(t, ids, aliceSecond)
		assert.Contains(t, ids, aliceOtherProject)
		assert.NotContains(t, ids, hiddenWrongOwner)
		assert.NotContains(t, ids, hiddenWrongDomain)

		_, err = ms.ListWithFilters(ctx, "", ListOptions{})
		require.Error(t, err, "ListWithFilters must keep its non-empty project contract")
	})

	t.Run("legacy no-principal rows remain queryable without owner filter", func(t *testing.T) {
		res, err := ms.ListPrincipalMemory(ctx, projectA, ListOptions{
			Domain: domain,
			Limit:  10,
		})
		require.NoError(t, err)
		assert.Contains(t, collectIDs(res), legacyNoPrincipal)
	})
}
