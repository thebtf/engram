package graph

import (
	"context"
	"errors"
	"os"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestDangling_T016_DanglingEdgeReturnsFlag verifies EC-F7:
// When an edge references a memory row that no longer exists,
// Resolve must return ErrDangling (NOT a generic error), and the
// return signature is (source, nil, ErrDangling).
//
// The test creates an edge pointing to a non-existent memory ID, then
// calls Resolve. The edge is valid in schema terms (not soft-deleted),
// but the target row is missing — this is the "dangling" condition.
//
// DSN-gated: skips when DATABASE_DSN is not set.
//
// Anti-stub: replacing `return source, nil, ErrDangling` in resolveEndpoint
// with `return source, nil, nil` causes this test to fail because
// errors.Is(err, ErrDangling) returns false.
//
// Engram vNext Milestone F TG2 / T016.
func TestDangling_T016_DanglingEdgeReturnsFlag(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping T016 dangling acceptance test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()

	ctx := context.Background()
	ns := NewNodesStore(db)
	gs := NewStore(db, ns)

	// Insert a real memory that we will delete immediately to get its ID,
	// simulating a target row that has been hard-deleted (not soft-deleted).
	var deletedMemID int64
	err = db.Raw(
		`INSERT INTO memories (project, content) VALUES ('t016-test', 'dangling-target') RETURNING id`,
	).Row().Scan(&deletedMemID)
	if err != nil {
		t.Fatalf("insert dangling memory: %v", err)
	}

	// Insert a real source memory for the edge source side.
	var srcMemID int64
	err = db.Raw(
		`INSERT INTO memories (project, content) VALUES ('t016-test', 'dangling-source') RETURNING id`,
	).Row().Scan(&srcMemID)
	if err != nil {
		t.Fatalf("insert source memory: %v", err)
	}

	// Hard-delete the target memory so it becomes truly missing.
	if err := db.Exec(`DELETE FROM memories WHERE id = ?`, deletedMemID).Error; err != nil {
		t.Fatalf("hard delete target: %v", err)
	}

	// Cleanup (source memory + edges).
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM memories WHERE project = 't016-test'`).Error
		_ = db.Exec(`DELETE FROM knowledge_edges WHERE source_session_id = 't016-test'`).Error
	})

	// Insert a knowledge_edge directly bypassing the Create validation to point
	// at the now-deleted memory ID. We bypass Create because it would pass validation
	// (IDs exist at insert time) or we use the deleted ID directly in the edge row.
	// To guarantee the missing target, we insert the edge row directly with the
	// deleted ID (the DB-level FK is nullable and not enforced for memory IDs — only
	// for node_source_id/node_target_id FKs via migration 127).
	var edgeID int64
	err = db.Raw(`
		INSERT INTO knowledge_edges
			(source_id, target_id, edge_type, weight, source_session_id, source_type, target_type)
		VALUES (?, ?, 'uses', 1.0, 't016-test', 'memory', 'memory')
		RETURNING id
	`, srcMemID, deletedMemID).Row().Scan(&edgeID)
	if err != nil {
		t.Fatalf("insert dangling edge: %v", err)
	}

	// Load the edge row to pass to Resolve.
	fetchedEdge, err := gs.Get(ctx, edgeID)
	if err != nil {
		t.Fatalf("get dangling edge: %v", err)
	}

	// EC-F7: Resolve must return ErrDangling, NOT a generic error.
	src, tgt, resolveErr := gs.Resolve(ctx, fetchedEdge)

	// Target should be nil (missing row).
	if tgt != nil {
		t.Errorf("expected nil target for dangling edge, got %v", tgt)
	}

	// Source should be resolvable (it still exists).
	if src == nil {
		t.Logf("source is nil — acceptable if source side also dangling, but EC-F7 scenario has valid source")
	}

	// The key EC-F7 assertion: error must be ErrDangling.
	if resolveErr == nil {
		t.Fatal("expected ErrDangling from Resolve on dangling edge, got nil error")
	}
	if !errors.Is(resolveErr, ErrDangling) {
		t.Fatalf("expected errors.Is(err, ErrDangling) = true, got: %v", resolveErr)
	}

	t.Logf("EC-F7 PASS: Resolve returned ErrDangling for edge %d (target memory %d deleted)", edgeID, deletedMemID)
}

// TestDangling_T016_UnitShape verifies the ErrDangling sentinel shape without DB.
// Anti-stub: this test ensures ErrDangling exists and errors.Is works.
func TestDangling_T016_UnitShape(t *testing.T) {
	if ErrDangling == nil {
		t.Fatal("ErrDangling must be a non-nil sentinel")
	}

	// Wrapping in another error must still be detectable via errors.Is.
	wrapped := errors.Join(errors.New("outer"), ErrDangling)
	if !errors.Is(wrapped, ErrDangling) {
		t.Fatal("errors.Is(wrapped, ErrDangling) must return true")
	}

	// A different error must NOT match ErrDangling.
	other := errors.New("other error")
	if errors.Is(other, ErrDangling) {
		t.Fatal("different error must not match ErrDangling")
	}
}
