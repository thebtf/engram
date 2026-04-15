package reaper

import (
	"context"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// testReaperDB opens a postgres test DB and creates the minimal projects schema.
// Tests skip when DATABASE_DSN is not set.
func testReaperDB(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping reaper integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}

	ddl := `CREATE TABLE IF NOT EXISTS projects (
		id             TEXT PRIMARY KEY,
		git_remote     TEXT,
		relative_path  TEXT,
		display_name   TEXT,
		legacy_ids     TEXT[],
		created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		removed_at     TIMESTAMPTZ NULL,
		last_heartbeat TIMESTAMPTZ DEFAULT NOW()
	)`
	if err := db.Exec(ddl).Error; err != nil {
		sqlDB, _ := db.DB()
		sqlDB.Close()
		t.Fatalf("create schema: %v", err)
	}

	sqlDB, _ := db.DB()
	return db, func() { sqlDB.Close() }
}

func TestReaper_PurgesExpired(t *testing.T) {
	t.Parallel()

	db, cleanup := testReaperDB(t)
	defer cleanup()

	// Insert a project that was soft-deleted 60 days ago (past default 30d retention).
	id := "reaper-expired-" + t.Name()
	removedAt := time.Now().UTC().Add(-60 * 24 * time.Hour)
	if err := db.Exec(
		"INSERT INTO projects (id, removed_at) VALUES (?, ?) ON CONFLICT DO NOTHING",
		id, removedAt,
	).Error; err != nil {
		t.Fatalf("insert expired project: %v", err)
	}
	defer db.Exec("DELETE FROM projects WHERE id = ?", id)

	r := New(db)
	if err := r.PurgeOnce(context.Background()); err != nil {
		t.Fatalf("PurgeOnce: %v", err)
	}

	// Project row must be gone.
	var count int64
	db.Raw("SELECT COUNT(*) FROM projects WHERE id = ?", id).Scan(&count)
	if count != 0 {
		t.Errorf("expected expired project to be hard-deleted, but row still exists")
	}
}

func TestReaper_PreservesUnexpired(t *testing.T) {
	t.Parallel()

	db, cleanup := testReaperDB(t)
	defer cleanup()

	// Insert a recently soft-deleted project (1 day ago, within default 30d window).
	id := "reaper-recent-" + t.Name()
	removedAt := time.Now().UTC().Add(-1 * 24 * time.Hour)
	if err := db.Exec(
		"INSERT INTO projects (id, removed_at) VALUES (?, ?) ON CONFLICT DO NOTHING",
		id, removedAt,
	).Error; err != nil {
		t.Fatalf("insert recent project: %v", err)
	}
	defer db.Exec("DELETE FROM projects WHERE id = ?", id)

	r := New(db)
	if err := r.PurgeOnce(context.Background()); err != nil {
		t.Fatalf("PurgeOnce: %v", err)
	}

	// Project row must still be present.
	var count int64
	db.Raw("SELECT COUNT(*) FROM projects WHERE id = ?", id).Scan(&count)
	if count != 1 {
		t.Errorf("expected recently-deleted project to be preserved, but row is gone")
	}
}

func TestReaper_RespectsRetentionEnvVar(t *testing.T) {
	t.Parallel()

	db, cleanup := testReaperDB(t)
	defer cleanup()

	// Set retention to 1 day.
	t.Setenv("ENGRAM_PROJECT_RETENTION_DAYS", "1")

	// Insert a project soft-deleted 2 days ago — should be purged with 1-day retention.
	id := "reaper-envvar-" + t.Name()
	removedAt := time.Now().UTC().Add(-2 * 24 * time.Hour)
	if err := db.Exec(
		"INSERT INTO projects (id, removed_at) VALUES (?, ?) ON CONFLICT DO NOTHING",
		id, removedAt,
	).Error; err != nil {
		t.Fatalf("insert project: %v", err)
	}
	defer db.Exec("DELETE FROM projects WHERE id = ?", id)

	r := New(db)
	if err := r.PurgeOnce(context.Background()); err != nil {
		t.Fatalf("PurgeOnce: %v", err)
	}

	var count int64
	db.Raw("SELECT COUNT(*) FROM projects WHERE id = ?", id).Scan(&count)
	if count != 0 {
		t.Errorf("expected project purged with 1-day retention, but row still exists")
	}
}

func TestReaper_StopsOnContextCancel(t *testing.T) {
	t.Parallel()

	db, cleanup := testReaperDB(t)
	defer cleanup()

	r := New(db)

	ctx, cancel := context.WithCancel(context.Background())

	// Use a very short ticker for this test. We override via a minimal loop.
	// Start the reaper.
	r.Start(ctx)

	// Cancel context almost immediately — reaper should stop cleanly.
	cancel()

	// Stop() waits for the goroutine — if it doesn't exit, the test will timeout.
	done := make(chan struct{})
	go func() {
		// Wait for the done channel which is closed when the goroutine exits.
		<-r.done
		close(done)
	}()

	select {
	case <-done:
		// Clean exit.
	case <-time.After(5 * time.Second):
		t.Fatal("reaper goroutine did not stop within 5s after context cancel")
	}
}
