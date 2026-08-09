package worker

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
)

type blockingProjectReaper struct {
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (r *blockingProjectReaper) Stop() {
	r.calls.Add(1)
	r.once.Do(func() { close(r.started) })
	if r.release != nil {
		<-r.release
	}
}

func TestServiceShutdownWaitsForReaper(t *testing.T) {
	release := make(chan struct{})
	r := &blockingProjectReaper{started: make(chan struct{}), release: release}
	svc := &Service{cancel: func() {}, projectReaper: r}

	shutdown := make(chan error, 1)
	go func() { shutdown <- svc.Shutdown(context.Background()) }()

	select {
	case <-r.started:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not stop the started reaper")
	}
	select {
	case err := <-shutdown:
		t.Fatalf("Shutdown returned before reaper stopped: %v", err)
	default:
	}
	results := make(chan error, 8)
	for range 8 {
		go func() { results <- svc.Shutdown(context.Background()) }()
	}

	close(release)
	for range 8 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent Shutdown: %v", err)
		}
	}
	if err := <-shutdown; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatalf("repeated Shutdown: %v", err)
	}
	if got := r.calls.Load(); got != 1 {
		t.Fatalf("reaper Stop calls = %d, want 1", got)
	}
}

func TestServiceShutdownBoundsReaperWait(t *testing.T) {
	release := make(chan struct{})
	r := &blockingProjectReaper{started: make(chan struct{}), release: release}
	svc := &Service{cancel: func() {}, projectReaper: r}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := svc.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want deadline exceeded", err)
	}
	select {
	case <-r.started:
	default:
		t.Fatal("Shutdown returned without stopping the reaper")
	}
	close(release)
	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatalf("coordinated Shutdown: %v", err)
	}
	if got := r.calls.Load(); got != 1 {
		t.Fatalf("reaper Stop calls = %d, want 1", got)
	}

}
func TestServiceShutdownStopsReaperBeforeClosingDatabase(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping shutdown close-order test")
	}

	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sqlDB, err := store.DB.DB()
	if err != nil {
		t.Fatalf("get sql DB: %v", err)
	}

	release := make(chan struct{})
	stopStarted := make(chan struct{})
	stopChecked := make(chan error, 1)
	svc := &Service{
		cancel: func() {},
		store:  store,
		projectReaper: projectReaperFunc(func() {
			stopChecked <- sqlDB.PingContext(context.Background())
			close(stopStarted)
			<-release
		}),
	}

	shutdown := make(chan error, 1)
	go func() { shutdown <- svc.Shutdown(context.Background()) }()
	<-stopStarted
	if err := <-stopChecked; err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.PingContext(context.Background()); err != nil {
		t.Fatalf("database closed while reaper was stopping: %v", err)
	}
	close(release)
	if err := <-shutdown; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := sqlDB.PingContext(context.Background()); err == nil {
		t.Fatal("database remained open after Shutdown")
	}
}

func TestNewServiceShutdownJoinsInitialization(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "true")
	t.Setenv("ENGRAM_AUTH_ADMIN_TOKEN", "")
	svc, err := NewService("reaper-shutdown-test", nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	joined := make(chan struct{})
	go func() {
		svc.initWG.Wait()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("Shutdown returned before initialization joined")
	}
}

type projectReaperFunc func()

func (f projectReaperFunc) Stop() { f() }
