package reaper

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
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

func TestParseRetentionConfig_ExplicitInvalidAndLargeBehavior(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		wantDays    int64
		wantSkip    bool
		wantDefault bool
	}{
		{name: "unset uses default", value: "", wantDays: defaultRetentionDays, wantDefault: true},
		{name: "configured positive", value: "7", wantDays: 7},
		{name: "malformed uses default", value: "not-a-number", wantDays: defaultRetentionDays, wantDefault: true},
		{name: "zero uses default", value: "0", wantDays: defaultRetentionDays, wantDefault: true},
		{name: "negative uses default", value: "-9", wantDays: defaultRetentionDays, wantDefault: true},
		{name: "largest duration-safe value", value: strconv.FormatInt(maxRetentionDays, 10), wantDays: maxRetentionDays},
		{name: "one day beyond duration range skips purge", value: strconv.FormatInt(maxRetentionDays+1, 10), wantSkip: true},
		{name: "arbitrarily large positive skips purge", value: "999999999999999999999999999999999999999999", wantSkip: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRetentionConfig(tt.value)
			if got.days != tt.wantDays {
				t.Fatalf("days = %d, want %d", got.days, tt.wantDays)
			}
			if got.skipPurge != tt.wantSkip {
				t.Fatalf("skipPurge = %v, want %v", got.skipPurge, tt.wantSkip)
			}
			if got.usedDefault != tt.wantDefault {
				t.Fatalf("usedDefault = %v, want %v", got.usedDefault, tt.wantDefault)
			}
		})
	}
}

func TestReaper_InvalidRetentionFallsBack(t *testing.T) {
	db, cleanup := testReaperDB(t)
	defer cleanup()

	t.Setenv("ENGRAM_PROJECT_RETENTION_DAYS", "not-a-number")

	expiredID := "reaper-invalid-expired-" + t.Name()
	recentID := "reaper-invalid-recent-" + t.Name()
	if err := db.Exec(
		"INSERT INTO projects (id, removed_at) VALUES (?, ?), (?, ?)",
		expiredID, time.Now().UTC().Add(-60*24*time.Hour),
		recentID, time.Now().UTC().Add(-24*time.Hour),
	).Error; err != nil {
		t.Fatalf("insert invalid-retention fixtures: %v", err)
	}
	defer db.Exec("DELETE FROM projects WHERE id IN (?, ?)", expiredID, recentID)

	if err := New(db).PurgeOnce(context.Background()); err != nil {
		t.Fatalf("PurgeOnce: %v", err)
	}

	var expiredCount, recentCount int64
	db.Raw("SELECT COUNT(*) FROM projects WHERE id = ?", expiredID).Scan(&expiredCount)
	db.Raw("SELECT COUNT(*) FROM projects WHERE id = ?", recentID).Scan(&recentCount)
	if expiredCount != 0 || recentCount != 1 {
		t.Fatalf("default fallback counts = expired:%d recent:%d, want expired:0 recent:1", expiredCount, recentCount)
	}
}

func TestReaper_LargeRetentionDoesNotWrapOrPurgeNewerRows(t *testing.T) {
	db, cleanup := testReaperDB(t)
	defer cleanup()

	t.Setenv("ENGRAM_PROJECT_RETENTION_DAYS", strconv.FormatInt(maxRetentionDays+1, 10))

	id := "reaper-large-retention-" + t.Name()
	removedAt := time.Now().UTC().Add(-60 * 24 * time.Hour)
	if err := db.Exec(
		"INSERT INTO projects (id, removed_at) VALUES (?, ?)",
		id, removedAt,
	).Error; err != nil {
		t.Fatalf("insert large-retention fixture: %v", err)
	}
	defer db.Exec("DELETE FROM projects WHERE id = ?", id)

	if err := New(db).PurgeOnce(context.Background()); err != nil {
		t.Fatalf("PurgeOnce: %v", err)
	}

	var count int64
	db.Raw("SELECT COUNT(*) FROM projects WHERE id = ?", id).Scan(&count)
	if count != 1 {
		t.Fatalf("large retention removed a row newer than requested; count = %d, want 1", count)
	}
}

func TestReaper_PurgeOnceReturnsQueryError(t *testing.T) {
	db, cleanup := testReaperDB(t)
	defer cleanup()

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql DB: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close sql DB: %v", err)
	}

	err = New(db).PurgeOnce(context.Background())
	if err == nil {
		t.Fatal("PurgeOnce returned nil after the database was closed")
	}
	if !strings.Contains(err.Error(), "purge query failed") {
		t.Fatalf("PurgeOnce error = %q, want purge query context", err)
	}
}

func TestReaper_StopBeforeStartReturns(t *testing.T) {
	r := New(nil)

	returned := make(chan struct{})
	go func() {
		r.Stop()
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop blocked before Start")
	}
}

type trackingReaperTicker struct {
	ticks    chan time.Time
	stopOnce sync.Once
	onStop   func()
}

func (t *trackingReaperTicker) Chan() <-chan time.Time { return t.ticks }

func (t *trackingReaperTicker) Stop() {
	t.stopOnce.Do(t.onStop)
}

type tickerLifecycleTracker struct {
	mu        sync.Mutex
	started   int
	stopped   int
	active    int
	maxActive int
}

func (t *tickerLifecycleTracker) New(time.Duration) reaperTicker {
	t.mu.Lock()
	t.started++
	t.active++
	if t.active > t.maxActive {
		t.maxActive = t.active
	}
	t.mu.Unlock()

	return &trackingReaperTicker{
		ticks: make(chan time.Time),
		onStop: func() {
			t.mu.Lock()
			t.stopped++
			t.active--
			t.mu.Unlock()
		},
	}
}

func (t *tickerLifecycleTracker) snapshot() (started, stopped, active, maxActive int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.started, t.stopped, t.active, t.maxActive
}

func (t *tickerLifecycleTracker) waitForStarts(tb testing.TB, want int) {
	tb.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		started, _, _, _ := t.snapshot()
		if started >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	started, _, _, _ := t.snapshot()
	tb.Fatalf("ticker starts = %d, want at least %d", started, want)
}

func TestReaper_ConcurrentStartIsIdempotent(t *testing.T) {
	r := New(nil)
	tracker := &tickerLifecycleTracker{}
	r.newTicker = tracker.New

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := make(chan struct{})
	var callers sync.WaitGroup
	for range 32 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			<-start
			r.Start(ctx)
		}()
	}
	close(start)
	callers.Wait()
	tracker.waitForStarts(t, 1)

	started, _, _, maxActive := tracker.snapshot()
	if started != 1 || maxActive != 1 {
		t.Fatalf("concurrent Start created %d loops with max active %d, want exactly one", started, maxActive)
	}
	r.Stop()
}

func TestReaper_ConcurrentStopJoinsSingleLoop(t *testing.T) {
	r := New(nil)
	tracker := &tickerLifecycleTracker{}
	r.newTicker = tracker.New
	r.Start(context.Background())
	tracker.waitForStarts(t, 1)

	start := make(chan struct{})
	var callers sync.WaitGroup
	for range 32 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			<-start
			r.Stop()
		}()
	}
	close(start)

	allReturned := make(chan struct{})
	go func() {
		callers.Wait()
		close(allReturned)
	}()
	select {
	case <-allReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent Stop calls blocked")
	}

	started, stopped, active, _ := tracker.snapshot()
	if started != 1 || stopped != 1 || active != 0 {
		t.Fatalf("ticker lifecycle = started:%d stopped:%d active:%d, want 1/1/0", started, stopped, active)
	}
}

func TestReaper_StopWaitsForTickerCleanup(t *testing.T) {
	stopEntered := make(chan struct{})
	releaseStop := make(chan struct{})
	ticker := &trackingReaperTicker{
		ticks: make(chan time.Time),
		onStop: func() {
			close(stopEntered)
			<-releaseStop
		},
	}
	created := make(chan struct{})

	r := New(nil)
	r.newTicker = func(time.Duration) reaperTicker {
		close(created)
		return ticker
	}
	r.Start(context.Background())
	<-created

	stopReturned := make(chan struct{})
	go func() {
		r.Stop()
		close(stopReturned)
	}()

	select {
	case <-stopEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("ticker cleanup did not start")
	}
	select {
	case <-stopReturned:
		t.Fatal("Stop returned before ticker cleanup completed")
	default:
	}
	close(releaseStop)
	select {
	case <-stopReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not join the reaper goroutine")
	}
}

func TestReaper_ConcurrentStartStopLeavesNoLoopOrTicker(t *testing.T) {
	r := New(nil)
	tracker := &tickerLifecycleTracker{}
	r.newTicker = tracker.New

	for round := 0; round < 25; round++ {
		startedBefore, _, _, _ := tracker.snapshot()
		r.Start(context.Background())
		tracker.waitForStarts(t, startedBefore+1)

		start := make(chan struct{})
		var callers sync.WaitGroup
		for i := 0; i < 16; i++ {
			callers.Add(1)
			go func(stop bool) {
				defer callers.Done()
				<-start
				if stop {
					r.Stop()
					return
				}
				r.Start(context.Background())
			}(i%2 == 0)
		}
		close(start)
		callers.Wait()
		r.Stop()
	}

	started, stopped, active, maxActive := tracker.snapshot()
	if started != stopped || active != 0 || maxActive > 1 {
		t.Fatalf("ticker lifecycle = started:%d stopped:%d active:%d max_active:%d, want balanced with max_active <= 1", started, stopped, active, maxActive)
	}
}

func TestReaper_StopsOnContextCancel(t *testing.T) {
	t.Parallel()

	r := New(nil)
	tracker := &tickerLifecycleTracker{}
	r.newTicker = tracker.New

	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	tracker.waitForStarts(t, 1)
	cancel()

	stopped := make(chan struct{})
	go func() {
		r.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("reaper goroutine did not stop after context cancel")
	}

	started, tickerStops, active, _ := tracker.snapshot()
	if started != 1 || tickerStops != 1 || active != 0 {
		t.Fatalf("context cancellation lifecycle = started:%d stopped:%d active:%d, want 1/1/0", started, tickerStops, active)
	}
}
