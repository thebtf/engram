package retrieval

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	gormliblogger "gorm.io/gorm/logger"

	engramgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// openIntegrationDB opens a PostgreSQL connection for T020 integration tests.
// Requires DATABASE_DSN env var (same convention as internal/db/gorm tests).
// Skips the test if the env var is unset or if running in short mode.
func openIntegrationDB(tb testing.TB) (*engramgorm.Store, func()) {
	tb.Helper()
	if testing.Short() {
		tb.Skip("T020: requires live PostgreSQL; skipped in short mode")
	}
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		tb.Skip("T020: DATABASE_DSN not set; skipped")
	}
	rawDB, err := gormlib.Open(postgres.Open(dsn), &gormlib.Config{
		Logger: gormliblogger.Default.LogMode(gormliblogger.Silent),
	})
	require.NoError(tb, err, "open integration test DB")
	sqlDB, err := rawDB.DB()
	require.NoError(tb, err)
	require.NoError(tb, sqlDB.Ping(), "ping integration test DB")

	store := &engramgorm.Store{DB: rawDB}
	return store, func() { sqlDB.Close() }
}

// TestTG3IntegrationCombinations_T020 exercises all 6 param combinations of
// the T018 TG3 fetch path using real SQL via ListWithFilters + AssembleRationale:
//
//	rationale ON/OFF × confidence_min 0.0/0.7 × include_superseded true/false
//
// Anti-stub guarantees:
//   - confidence_min=0.7 exclusion of low-confidence row: fails if ListWithFilters
//     omits the WHERE clause
//   - include_superseded=true inclusion of superseded row: fails if status filter
//     is always status='active'
//   - include_rationale=true RankingRationale: fails if AssembleRationale returns
//     zero struct (FiltersApplied would be nil/empty, but we require ≥1 descriptor)
func TestTG3IntegrationCombinations_T020(t *testing.T) {
	store, cleanup := openIntegrationDB(t)
	defer cleanup()

	const project = "test-tg3-integration-t020"
	db := store.DB
	defer db.Exec(`DELETE FROM memories WHERE project = ?`, project)

	ctx := context.Background()
	ms := engramgorm.NewMemoryStore(store)
	now := time.Now().UTC()

	insertRow := func(content, status string, confidence float64) int64 {
		t.Helper()
		row := &engramgorm.Memory{
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
			Tier:           "semantic",
		}
		require.NoError(t, db.Create(row).Error, "insert fixture row")
		return row.ID
	}

	// Fixture: three rows with distinct confidence + status values.
	idActiveHigh := insertRow("active-high-confidence", "active", 0.9)        // passes all filters
	idActiveLow := insertRow("active-low-confidence", "active", 0.3)          // excluded by confidence_min=0.7
	idSuperseded := insertRow("superseded-high-confidence", "superseded", 0.8) // excluded unless include_superseded

	type combo struct {
		name              string
		confidenceMin     float64
		includeSuperseded bool
		includeRationale  bool
		wantContains      []int64
		wantExcludes      []int64
	}

	combos := []combo{
		// Combination 1: rationale OFF, confidence 0.0, superseded false
		{
			name:              "rationale_OFF_confidence_0_superseded_false",
			confidenceMin:     0.0,
			includeSuperseded: false,
			includeRationale:  false,
			wantContains:      []int64{idActiveHigh, idActiveLow},
			wantExcludes:      []int64{idSuperseded},
		},
		// Combination 2: rationale OFF, confidence 0.0, superseded true
		{
			name:              "rationale_OFF_confidence_0_superseded_true",
			confidenceMin:     0.0,
			includeSuperseded: true,
			includeRationale:  false,
			wantContains:      []int64{idActiveHigh, idActiveLow, idSuperseded},
			wantExcludes:      nil,
		},
		// Combination 3: rationale OFF, confidence 0.7, superseded false
		{
			name:              "rationale_OFF_confidence_0_7_superseded_false",
			confidenceMin:     0.7,
			includeSuperseded: false,
			includeRationale:  false,
			wantContains:      []int64{idActiveHigh},
			wantExcludes:      []int64{idActiveLow, idSuperseded},
		},
		// Combination 4: rationale OFF, confidence 0.7, superseded true
		{
			name:              "rationale_OFF_confidence_0_7_superseded_true",
			confidenceMin:     0.7,
			includeSuperseded: true,
			includeRationale:  false,
			wantContains:      []int64{idActiveHigh, idSuperseded},
			wantExcludes:      []int64{idActiveLow},
		},
		// Combination 5: rationale ON, confidence 0.0, superseded false
		{
			name:              "rationale_ON_confidence_0_superseded_false",
			confidenceMin:     0.0,
			includeSuperseded: false,
			includeRationale:  true,
			wantContains:      []int64{idActiveHigh, idActiveLow},
			wantExcludes:      []int64{idSuperseded},
		},
		// Combination 6: rationale ON, confidence 0.7, superseded true
		{
			name:              "rationale_ON_confidence_0_7_superseded_true",
			confidenceMin:     0.7,
			includeSuperseded: true,
			includeRationale:  true,
			wantContains:      []int64{idActiveHigh, idSuperseded},
			wantExcludes:      []int64{idActiveLow},
		},
	}

	for _, c := range combos {
		c := c // capture range variable
		t.Run(c.name, func(t *testing.T) {
			opts := engramgorm.ListOptions{
				ConfidenceMin:     c.confidenceMin,
				IncludeSuperseded: c.includeSuperseded,
				Limit:             100,
			}
			mems, err := ms.ListWithFilters(ctx, project, opts)
			require.NoError(t, err)

			ids := make([]int64, len(mems))
			for i, m := range mems {
				ids[i] = m.ID
			}

			for _, want := range c.wantContains {
				assert.Contains(t, ids, want, fmt.Sprintf("expected ID %d in result", want))
			}
			for _, excl := range c.wantExcludes {
				assert.NotContains(t, ids, excl, fmt.Sprintf("ID %d must not appear in result", excl))
			}

			if !c.includeRationale {
				return
			}

			// include_rationale=true: build filter descriptors mirroring the
			// handler and call AssembleRationale for every returned memory.
			// Assert the 6 fields are populated consistently with fixture data.
			var filterDescs []string
			filterDescs = append(filterDescs, "project="+project)
			if c.confidenceMin > 0 {
				filterDescs = append(filterDescs, fmt.Sprintf("confidence_min=%.4g", c.confidenceMin))
			}
			if c.includeSuperseded {
				filterDescs = append(filterDescs, "include_superseded=true")
			}

			for _, mem := range mems {
				queryText := "confidence"
				contentMatched := true // "confidence" substring in all fixture content strings
				rat := AssembleRationale(mem, queryText, contentMatched, filterDescs)

				// recency_days: fixture rows inserted in this test, so < 1 day
				assert.GreaterOrEqual(t, rat.RecencyDays, 0.0, "recency_days non-negative")
				assert.Less(t, rat.RecencyDays, 1.0, "recency_days < 1 for just-inserted rows")

				// confidence echoes the stored value
				assert.Equal(t, mem.Confidence, rat.Confidence, "confidence must echo memory.Confidence")

				// tier echoes the stored value ("semantic" on all fixture rows)
				assert.Equal(t, "semantic", rat.Tier, "tier must echo memory.Tier")

				// substring_match: true because queryText is set and matched=true
				assert.True(t, rat.SubstringMatch, "substring_match must be true when queryText non-empty and matched=true")

				// filters_applied: must be non-nil slice with at least project= descriptor
				assert.NotNil(t, rat.FiltersApplied, "filters_applied must never be nil")
				assert.GreaterOrEqual(t, len(rat.FiltersApplied), 1, "filters_applied must have ≥1 descriptor")
				assert.Contains(t, rat.FiltersApplied, "project="+project, "project descriptor must be present")

				// models.Memory is the correct type (confirming ListWithFilters returns domain type)
				assert.IsType(t, &models.Memory{}, mem, "ListWithFilters must return *models.Memory")
			}
		})
	}
}
