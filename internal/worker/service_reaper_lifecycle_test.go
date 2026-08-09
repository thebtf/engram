package worker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

	close(release)
	if err := <-shutdown; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatalf("repeated Shutdown: %v", err)
	}
	if got := r.calls.Load(); got != 2 {
		t.Fatalf("reaper Stop calls = %d, want 2", got)
	}
}

func TestServiceShutdownBoundsReaperWait(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
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
}
