package gorm

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/models"
)

// TestBatchIncrementCitedN_ImportanceFactor verifies the rank-6 outcome-scaled importance_base
// bump against a real database. It pins three contracts:
//  1. factor == 1.0 reproduces the historical formula exactly: importance_base grows by
//     (importance_base*ln(2+cc) - importance_base) * 1.0, i.e. to importance_base*ln(2+cc).
//  2. factor < 1.0 yields a strictly smaller result than factor == 1.0 once the neutral bump
//     actually grows above base (failure dampens future-injection promotion).
//  3. the bump is monotonic-up: factor == 0.0 leaves importance_base unchanged.
//
// DATABASE_DSN-gated (skips without a live PostgreSQL), mirroring the other gorm store tests.
func TestBatchIncrementCitedN_ImportanceFactor(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	const proj = "test-rank6-importance"
	defer db.Exec(`DELETE FROM memories WHERE project = ?`, proj)

	ms := NewMemoryStore(&Store{DB: db})
	ctx := context.Background()

	// readImportance returns the current importance_base for a memory id.
	readImportance := func(t *testing.T, id int64) float64 {
		t.Helper()
		var got float64
		require.NoError(t, db.Raw(`SELECT importance_base FROM memories WHERE id = ?`, id).Scan(&got).Error)
		return got
	}

	// newMem creates a fresh memory with a known base importance and citation_count 0.
	newMem := func(t *testing.T, base float64) int64 {
		t.Helper()
		m, err := ms.Create(ctx, &models.Memory{
			Project:        proj,
			Content:        "rank-6 importance factor probe",
			ImportanceBase: base,
		})
		require.NoError(t, err)
		return m.ID
	}

	// citeN applies n cited increments at the given factor, compounding exactly as production
	// does (each UPDATE reads the current importance_base + citation_count), and returns the final
	// importance_base.
	citeN := func(t *testing.T, base, factor float64, n int) float64 {
		t.Helper()
		id := newMem(t, base)
		defer db.Exec(`DELETE FROM memories WHERE id = ?`, id)
		for i := 0; i < n; i++ {
			require.NoError(t, ms.BatchIncrementCitedN(ctx, []int64{id}, 1.0, factor))
		}
		return readImportance(t, id)
	}

	// importance_base is stored as PostgreSQL `real` (float32), so round-tripped values carry
	// ~1e-7 relative error — assertions use a float32-appropriate tolerance, not 1e-9.
	const eps = 1e-6

	// --- Contract 1: factor 1.0 reproduces the historical single-bump formula exactly. ---
	// One increment at cc==0: importance_base -> base + (base*ln(2+0) - base)*1.0 == base*ln(2),
	// clamped GREATEST(base, ...). base*ln(2) ≈ 0.693*base < base, so it clamps to base.
	{
		const base = 0.5
		got := citeN(t, base, 1.0, 1)
		want := base * math.Log(2.0)
		if want < base {
			want = base // GREATEST clamp
		}
		assert.InDelta(t, want, got, eps, "factor=1.0 single bump must equal the historical formula")
	}

	// --- Contract 2: failure (0.25) promotes less than neutral (1.0) in the UNCAPPED regime. ---
	// Pick base + citation count so the neutral bump grows above base but stays below the 1.0 cap;
	// otherwise LEAST(1.0, ...) saturates both factors to 1.0 and the difference vanishes.
	// base=0.5, one citation (cc=1 at UPDATE time): neutral -> 0.5*ln(3) ≈ 0.549 (>base, <1.0);
	// failure -> 0.5 + (0.549-0.5)*0.25 ≈ 0.512. Distinct and uncapped.
	{
		const base = 0.5
		neutral := citeN(t, base, 1.0, 2)
		failure := citeN(t, base, 0.25, 2)
		assert.Greater(t, neutral, float64(base), "precondition: neutral bump must grow above base")
		assert.Less(t, neutral, 1.0, "precondition: neutral bump must stay below the 1.0 cap (else both saturate)")
		assert.Greater(t, neutral, failure, "failure (0.25) must promote importance_base less than neutral (1.0)")
		assert.GreaterOrEqual(t, failure, float64(base)-eps, "failure bump must remain monotonic-up (never below base)")
	}

	// --- Contract 3: factor 0.0 is a no-op on importance_base (monotonic-up clamp to base). ---
	{
		const base = 0.6
		got := citeN(t, base, 0.0, 8)
		assert.InDelta(t, base, got, eps, "factor=0.0 must leave importance_base unchanged")
	}
}
