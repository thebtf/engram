package feedback

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	_ "modernc.org/sqlite"
)

// The rank-6 outcome→importance_base policy lives in multiplierTable.importanceFactor.
// The SQL that applies it is covered by the gorm integration tests (DATABASE_DSN-gated);
// these unit tests pin the POLICY: the ordering and the backward-compat invariants that a
// future edit could silently break.

func TestMultiplierTable_ImportanceFactorOrdering(t *testing.T) {
	success := multiplierTable["success"].importanceFactor
	partial := multiplierTable["partial"].importanceFactor
	failure := multiplierTable["failure"].importanceFactor
	abandoned := multiplierTable["abandoned"].importanceFactor

	// A successful session must promote a cited memory for future injection at least as hard
	// as a partial one, which in turn must promote at least as hard as a failure.
	if !(success >= partial && partial > failure && failure > abandoned) {
		t.Errorf("importanceFactor must be ordered success(%v) >= partial(%v) > failure(%v) > abandoned(%v)",
			success, partial, failure, abandoned)
	}
	// Abandoned must be a no-op on importance_base.
	if abandoned != 0.0 {
		t.Errorf("abandoned importanceFactor must be 0 (no-op), got %v", abandoned)
	}
	// Success must promote strictly harder than the neutral baseline (the whole point of rank-6).
	if success <= partial {
		t.Errorf("success importanceFactor (%v) must exceed partial/neutral (%v) for outcome to matter", success, partial)
	}
}

// Backward-compat invariant: the neutral default ("") and "partial" must keep importanceFactor
// == 1.0, because the store SQL reduces to the exact historical importance_base formula only at
// factor 1.0. If either drifts off 1.0, every outcome-less session (the common case) silently
// changes its importance_base growth — a regression the SQL math test cannot catch on its own.
func TestMultiplierTable_NeutralFactorIsExactlyOne(t *testing.T) {
	if got := multiplierTable[""].importanceFactor; got != 1.0 {
		t.Errorf(`default "" importanceFactor must be exactly 1.0 (historical-formula parity), got %v`, got)
	}
	if got := multiplierTable["partial"].importanceFactor; got != 1.0 {
		t.Errorf("partial importanceFactor must be exactly 1.0, got %v", got)
	}
}

func TestUpdateWithOutcome_UnknownOutcomeUsesNeutralCitedUpdate(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DryRun: true, DisableAutomaticPing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var statements []struct {
		sql  string
		vars []any
	}
	err = db.Callback().Raw().After("gorm:raw").Register("test:capture_feedback", func(tx *gorm.DB) {
		statements = append(statements, struct {
			sql  string
			vars []any
		}{tx.Statement.SQL.String(), append([]any(nil), tx.Statement.Vars...)})
	})
	if err != nil {
		t.Fatal(err)
	}
	updater := NewUpdater(dbgorm.NewMemoryStore(&dbgorm.Store{DB: db}))
	updater.UpdateWithOutcome(context.Background(), []CitationResult{{MemoryID: 42, Cited: true}}, "totally-unknown-outcome")
	if len(statements) != 1 {
		t.Fatalf("expected one cited update, got %d", len(statements))
	}
	if !strings.Contains(statements[0].sql, "ts_alpha = ts_alpha +") || len(statements[0].vars) < 2 {
		t.Fatalf("expected cited update with multipliers, got %q %v", statements[0].sql, statements[0].vars)
	}
	if statements[0].vars[0] != float64(1) || statements[0].vars[1] != float64(1) {
		t.Errorf("unknown outcome cited alpha and importance factor = %v, %v; want 1, 1", statements[0].vars[0], statements[0].vars[1])
	}
}
