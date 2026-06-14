package gorm

import (
	"context"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openTranscriptTestDB opens a real PostgreSQL connection for transcript
// store integration testing. Requires DATABASE_DSN env var; skips the test
// when it is not set, matching the discipline in store_stats_test.go.
func openTranscriptTestDB(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping transcript store integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	cleanup := func() { sqlDB.Close() }
	return db, cleanup
}

// openTranscriptTestTx begins a transaction on db and returns a *gorm.DB
// scoped to it plus a rollback function. All writes inside the tx are
// invisible outside it and are never committed — zero persistent side effects.
func openTranscriptTestTx(t *testing.T, db *gorm.DB) (*gorm.DB, func()) {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("begin tx: %v", tx.Error)
	}
	rollback := func() {
		if err := tx.Rollback().Error; err != nil {
			t.Logf("tx rollback: %v", err)
		}
	}
	return tx, rollback
}

// TestTranscriptStore_Lifecycle exercises the full Create → List → MarkProcessed
// → List → PruneProcessed cycle inside a single rolled-back transaction so
// zero rows ever persist to the real database.
func TestTranscriptStore_Lifecycle(t *testing.T) {
	db, closeDB := openTranscriptTestDB(t)
	defer closeDB()

	tx, rollback := openTranscriptTestTx(t, db)
	defer rollback()

	store := NewTranscriptStore(tx)
	ctx := context.Background()

	watermark := time.Now().Add(-time.Second) // before inserts

	// --- Create two transcripts ---
	t1 := &SessionTranscript{
		SessionID: "sess-alpha",
		Project:   "__test__",
		Content:   "transcript content alpha",
	}
	t2 := &SessionTranscript{
		SessionID: "sess-beta",
		Project:   "__test__",
		Content:   "transcript content beta",
	}

	if err := store.Create(ctx, t1); err != nil {
		t.Fatalf("Create t1: %v", err)
	}
	if t1.ID == 0 {
		t.Fatal("Create t1: expected non-zero ID after insert")
	}
	if t1.ByteLen != len("transcript content alpha") {
		t.Errorf("Create t1: ByteLen = %d, want %d", t1.ByteLen, len("transcript content alpha"))
	}

	if err := store.Create(ctx, t2); err != nil {
		t.Fatalf("Create t2: %v", err)
	}
	if t2.ID == 0 {
		t.Fatal("Create t2: expected non-zero ID after insert")
	}

	// --- ListUnprocessedSince should return both rows ---
	rows, err := store.ListUnprocessedSince(ctx, watermark)
	if err != nil {
		t.Fatalf("ListUnprocessedSince (initial): %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("ListUnprocessedSince (initial): got %d rows, want >= 2", len(rows))
	}

	// --- MarkProcessed on t1 only ---
	if err := store.MarkProcessed(ctx, []int64{t1.ID}); err != nil {
		t.Fatalf("MarkProcessed t1: %v", err)
	}

	// --- ListUnprocessedSince should now return only t2 ---
	rows, err = store.ListUnprocessedSince(ctx, watermark)
	if err != nil {
		t.Fatalf("ListUnprocessedSince (after mark): %v", err)
	}
	// We can only assert t1 is no longer unprocessed; t2 must remain.
	for _, r := range rows {
		if r.ID == t1.ID {
			t.Errorf("ListUnprocessedSince (after mark): t1 (id=%d) still appears as unprocessed", t1.ID)
		}
	}
	foundT2 := false
	for _, r := range rows {
		if r.ID == t2.ID {
			foundT2 = true
		}
	}
	if !foundT2 {
		t.Errorf("ListUnprocessedSince (after mark): t2 (id=%d) not found in unprocessed list", t2.ID)
	}

	// --- PruneProcessed should remove t1 (processed) and return 1 ---
	pruned, err := store.PruneProcessed(ctx)
	if err != nil {
		t.Fatalf("PruneProcessed: %v", err)
	}
	if pruned < 1 {
		t.Errorf("PruneProcessed: got %d rows affected, want >= 1", pruned)
	}

	// Confirm t1 no longer exists in the tx-scoped table.
	var check SessionTranscript
	result := tx.Where("id = ?", t1.ID).First(&check)
	if result.Error == nil {
		t.Errorf("PruneProcessed: t1 (id=%d) still present after prune", t1.ID)
	}

	// Transaction is rolled back by defer — nothing persists.
}

// TestTranscriptStore_PruneUnprocessedOlderThan_ZeroDays verifies the guard
// that prevents accidental deletion of all unprocessed rows when days == 0.
// This test does NOT require DATABASE_DSN; the guard fires before any DB call.
func TestTranscriptStore_PruneUnprocessedOlderThan_ZeroDays(t *testing.T) {
	// Pass a nil *gorm.DB — the guard must return before touching it.
	store := NewTranscriptStore(nil)
	ctx := context.Background()

	n, err := store.PruneUnprocessedOlderThan(ctx, 0)
	if err != nil {
		t.Fatalf("PruneUnprocessedOlderThan(0): unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("PruneUnprocessedOlderThan(0): got %d, want 0", n)
	}

	// Negative days also guard.
	n, err = store.PruneUnprocessedOlderThan(ctx, -5)
	if err != nil {
		t.Fatalf("PruneUnprocessedOlderThan(-5): unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("PruneUnprocessedOlderThan(-5): got %d, want 0", n)
	}
}

// TestTranscriptStore_MarkProcessed_EmptyIDs verifies no-op behaviour
// when an empty slice is passed to MarkProcessed. Guard fires before DB call,
// so this also does not require DATABASE_DSN.
func TestTranscriptStore_MarkProcessed_EmptyIDs(t *testing.T) {
	store := NewTranscriptStore(nil)
	ctx := context.Background()

	if err := store.MarkProcessed(ctx, []int64{}); err != nil {
		t.Fatalf("MarkProcessed([]): unexpected error: %v", err)
	}
	if err := store.MarkProcessed(ctx, nil); err != nil {
		t.Fatalf("MarkProcessed(nil): unexpected error: %v", err)
	}
}
