package worker

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
)

func TestServiceShutdown_PartialInitIsNilSafeAndIdempotent(t *testing.T) {
	svc := &Service{}

	contexts := []context.Context{nil, context.Background()}
	for i, ctx := range contexts {
		if err := svc.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown call %d: %v", i+1, err)
		}
	}
}

type blockingProjectReaper struct {
	stopStarted chan struct{}
	release     <-chan struct{}
	onStop      func()
	startOnce   sync.Once
	stopCalls   atomic.Int32
}

func (r *blockingProjectReaper) Stop() {
	r.stopCalls.Add(1)
	if r.onStop != nil {
		r.onStop()
	}
	if r.stopStarted != nil {
		r.startOnce.Do(func() { close(r.stopStarted) })
	}
	if r.release != nil {
		<-r.release
	}
}

func TestServiceShutdown_ConcurrentCallsStopReaperOnce(t *testing.T) {
	reaper := &blockingProjectReaper{}
	svc := &Service{
		cancel:        func() {},
		projectReaper: reaper,
	}

	start := make(chan struct{})
	results := make(chan error, 32)
	var callers sync.WaitGroup
	for range 32 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			<-start
			results <- svc.Shutdown(context.Background())
		}()
	}
	close(start)

	returned := make(chan struct{})
	go func() {
		callers.Wait()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent Shutdown calls blocked")
	}
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}
	if got := reaper.stopCalls.Load(); got != 1 {
		t.Fatalf("reaper Stop calls = %d, want 1", got)
	}
}

func TestServiceShutdown_WaitsForPartialInitializationBeforeReaperStop(t *testing.T) {
	stopStarted := make(chan struct{})
	reaper := &blockingProjectReaper{stopStarted: stopStarted}
	cancelled := make(chan struct{})
	var cancelOnce sync.Once
	svc := &Service{
		cancel: func() {
			cancelOnce.Do(func() { close(cancelled) })
		},
		projectReaper: reaper,
	}
	svc.initWG.Add(1)
	initReleased := false
	defer func() {
		if !initReleased {
			svc.initWG.Done()
		}
	}()

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- svc.Shutdown(context.Background()) }()
	<-cancelled

	select {
	case <-stopStarted:
		t.Fatal("reaper Stop ran before partial initialization joined")
	case <-time.After(50 * time.Millisecond):
	}

	svc.initWG.Done()
	initReleased = true
	select {
	case <-stopStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("reaper Stop did not run after initialization joined")
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestServiceShutdown_WaitsForReaperBeforeClosingDatabase(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping service shutdown database-order test")
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

	stopStarted := make(chan struct{})
	releaseStop := make(chan struct{})
	stopCheck := make(chan error, 1)
	reaper := &blockingProjectReaper{
		stopStarted: stopStarted,
		release:     releaseStop,
		onStop: func() {
			if err := sqlDB.PingContext(context.Background()); err != nil {
				stopCheck <- fmt.Errorf("database closed before reaper Stop: %w", err)
				return
			}
			stopCheck <- nil
		},
	}
	svc := &Service{
		cancel:        func() {},
		store:         store,
		projectReaper: reaper,
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- svc.Shutdown(context.Background()) }()

	select {
	case <-stopStarted:
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned before stopping reaper: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not stop the reaper")
	}
	if err := <-stopCheck; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned before reaper released: %v", err)
	default:
	}
	if err := sqlDB.PingContext(context.Background()); err != nil {
		t.Fatalf("database closed while reaper Stop was in progress: %v", err)
	}

	close(releaseStop)
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := sqlDB.PingContext(context.Background()); err == nil {
		t.Fatal("database remained open after Shutdown completed")
	}
}
