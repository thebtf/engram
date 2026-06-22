package gorm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/models"
)

// TestMemoryStore_ListWithFilters_T017a validates the three filter axes introduced
// by T017a (engram vNext Milestone F TG3 store-level extension):
//
//  1. Default opts behave identically to legacy List (status='active' only).
//  2. IncludeSuperseded=true returns rows with status='superseded'.
//  3. ConfidenceMin=0.7 excludes rows with confidence < 0.7 (inclusive on equality).
//
// Anti-stub: replacing the ListWithFilters body with the original List impl (which
// always filters status='active' and ignores ConfidenceMin) makes cases (2) and (3)
// fail the row-count assertions.
func TestMemoryStore_ListWithFilters_T017a(t *testing.T) {
	if testing.Short() {
		t.Skip("T017a: requires live PostgreSQL; skipped in short mode")
	}
	db, cleanup := openTestDB(t)
	defer cleanup()

	const project = "test-list-filters-t017a"
	defer db.Exec(`DELETE FROM memories WHERE project = ?`, project)

	ctx := context.Background()
	store := &Store{DB: db}
	ms := NewMemoryStore(store)

	now := time.Now().UTC()

	// Insert fixture rows at known confidence + status values.
	insertRow := func(content, status string, confidence float64) int64 {
		t.Helper()
		row := &Memory{
			Project:        project,
			Content:        content,
			Status:         status,
			Confidence:     confidence,
			ImportanceBase: 0.5,
			TsAlpha:        1.0,
			TsBeta:         1.0,
			Version:        1,
			CreatedAt:      now,
			UpdatedAt:      now,
			PrivacyScope:   "project",
		}
		require.NoError(t, db.Create(row).Error, "insert fixture row")
		return row.ID
	}

	// Row A: active, confidence 0.9
	idA := insertRow("active-high-conf", "active", 0.9)
	// Row B: active, confidence 0.4
	idB := insertRow("active-low-conf", "active", 0.4)
	// Row C: superseded, confidence 0.8
	idC := insertRow("superseded-row", "superseded", 0.8)

	t.Run("default_opts_equals_legacy_list", func(t *testing.T) {
		// Default opts: only active rows returned.
		res, err := ms.ListWithFilters(ctx, project, ListOptions{})
		require.NoError(t, err)
		ids := collectIDs(res)
		assert.Contains(t, ids, idA, "active-high-conf should be in default result")
		assert.Contains(t, ids, idB, "active-low-conf should be in default result")
		assert.NotContains(t, ids, idC, "superseded row must not appear under default opts")

		// Compare with List: should return the same ids.
		legacy, err := ms.List(ctx, project, 50)
		require.NoError(t, err)
		assert.Equal(t, collectIDs(legacy), ids, "ListWithFilters(default) must return same set as List")
	})

	t.Run("include_superseded_true", func(t *testing.T) {
		res, err := ms.ListWithFilters(ctx, project, ListOptions{IncludeSuperseded: true})
		require.NoError(t, err)
		ids := collectIDs(res)
		assert.Contains(t, ids, idA, "active row must appear")
		assert.Contains(t, ids, idB, "active-low-conf must appear")
		assert.Contains(t, ids, idC, "superseded row must appear when IncludeSuperseded=true")
	})

	t.Run("confidence_min_0_7_excludes_low", func(t *testing.T) {
		res, err := ms.ListWithFilters(ctx, project, ListOptions{ConfidenceMin: 0.7})
		require.NoError(t, err)
		ids := collectIDs(res)
		assert.Contains(t, ids, idA, "confidence=0.9 must survive ConfidenceMin=0.7")
		assert.NotContains(t, ids, idB, "confidence=0.4 must be excluded by ConfidenceMin=0.7")
		assert.NotContains(t, ids, idC, "superseded row excluded by default status filter")
	})

	t.Run("confidence_min_inclusive_on_equality", func(t *testing.T) {
		// Insert a row at exactly ConfidenceMin to verify inclusive semantics (>= not >).
		idExact := insertRow("exact-confidence", "active", 0.7)
		defer db.Exec(`DELETE FROM memories WHERE id = ?`, idExact)

		res, err := ms.ListWithFilters(ctx, project, ListOptions{ConfidenceMin: 0.7})
		require.NoError(t, err)
		assert.Contains(t, collectIDs(res), idExact, "confidence=0.7 must survive ConfidenceMin=0.7 (inclusive)")
	})

	t.Run("combined_superseded_and_confidence_min", func(t *testing.T) {
		res, err := ms.ListWithFilters(ctx, project, ListOptions{
			IncludeSuperseded: true,
			ConfidenceMin:     0.7,
		})
		require.NoError(t, err)
		ids := collectIDs(res)
		assert.Contains(t, ids, idA, "active 0.9 passes both filters")
		assert.Contains(t, ids, idC, "superseded 0.8 passes both filters")
		assert.NotContains(t, ids, idB, "active 0.4 excluded by ConfidenceMin=0.7")
	})

	t.Run("content_filter_and_offset", func(t *testing.T) {
		res, err := ms.ListWithFilters(ctx, project, ListOptions{
			ContentContains: "active",
			Limit:           1,
			Offset:          1,
		})
		require.NoError(t, err)
		require.Len(t, res, 1)
		assert.Contains(t, []int64{idA, idB}, res[0].ID, "offset must be applied after content predicate")
	})
}

// collectIDs extracts the ID list from a memory slice for assertion convenience.
func collectIDs(mems []*models.Memory) []int64 {
	ids := make([]int64, len(mems))
	for i, m := range mems {
		ids[i] = m.ID
	}
	return ids
}
