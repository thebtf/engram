package worker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/intervention"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

type recordedPolicyReconcileCall struct {
	limit       int
	deadline    time.Time
	hasDeadline bool
}

type recordingPolicyReconciler struct {
	calls          chan recordedPolicyReconcileCall
	errors         []error
	waitForCancel  bool
	reconcileCalls atomic.Int32
}

func newRecordingPolicyReconciler() *recordingPolicyReconciler {
	return &recordingPolicyReconciler{calls: make(chan recordedPolicyReconcileCall, 8)}
}

func (r *recordingPolicyReconciler) Reconcile(ctx context.Context, limit int) (intervention.ReconcileResult, error) {
	deadline, hasDeadline := ctx.Deadline()
	callNumber := r.reconcileCalls.Add(1)
	select {
	case r.calls <- recordedPolicyReconcileCall{limit: limit, deadline: deadline, hasDeadline: hasDeadline}:
	case <-ctx.Done():
		return intervention.ReconcileResult{}, ctx.Err()
	}

	if r.waitForCancel {
		<-ctx.Done()
		return intervention.ReconcileResult{}, ctx.Err()
	}
	if index := int(callNumber - 1); index < len(r.errors) {
		return intervention.ReconcileResult{}, r.errors[index]
	}
	return intervention.ReconcileResult{}, nil
}

type manualInterventionReconcilerTicker struct {
	ticks   chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func newManualInterventionReconcilerTicker() *manualInterventionReconcilerTicker {
	return &manualInterventionReconcilerTicker{
		ticks:   make(chan time.Time, 4),
		stopped: make(chan struct{}),
	}
}

func (t *manualInterventionReconcilerTicker) C() <-chan time.Time {
	return t.ticks
}

func (t *manualInterventionReconcilerTicker) Stop() {
	t.once.Do(func() { close(t.stopped) })
}

func awaitPolicyReconcileCall(t *testing.T, reconciler *recordingPolicyReconciler) recordedPolicyReconcileCall {
	t.Helper()
	select {
	case call := <-reconciler.calls:
		return call
	case <-time.After(time.Second):
		t.Fatal("intervention reconciler did not run")
		return recordedPolicyReconcileCall{}
	}
}

func waitForInterventionReconciler(t *testing.T, service *Service) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		service.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("intervention reconciler did not drain")
	}
}

func TestInterventionReconcilerRunsImmediateAndScheduledBoundedPasses(t *testing.T) {
	rootCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
	})

	reconciler := newRecordingPolicyReconciler()
	reconciler.errors = []error{errors.New("transient reconcile failure")}
	ticker := newManualInterventionReconcilerTicker()
	intervals := make(chan time.Duration, 1)
	service := &Service{
		ctx: rootCtx,
		interventionReconcilerTickerFactory: func(interval time.Duration) interventionReconcilerTicker {
			intervals <- interval
			return ticker
		},
	}
	service.SetInterventionReconciler(reconciler)
	startedAt := time.Now()
	service.startInterventionReconciler()

	first := awaitPolicyReconcileCall(t, reconciler)
	if first.limit != interventionReconcilerLimit {
		t.Fatalf("immediate reconciliation limit = %d, want %d", first.limit, interventionReconcilerLimit)
	}
	if !first.hasDeadline {
		t.Fatal("immediate reconciliation context has no deadline")
	}
	if got := first.deadline.Sub(startedAt); got <= 0 || got > interventionReconcilerTimeout || got < interventionReconcilerTimeout-time.Second {
		t.Fatalf("immediate reconciliation deadline offset = %s, want within one second of %s", got, interventionReconcilerTimeout)
	}

	select {
	case interval := <-intervals:
		if interval != interventionReconcilerInterval {
			t.Fatalf("reconciler interval = %s, want %s", interval, interventionReconcilerInterval)
		}
	case <-time.After(time.Second):
		t.Fatal("intervention reconciler did not create its periodic ticker")
	}

	ticker.ticks <- time.Now()
	second := awaitPolicyReconcileCall(t, reconciler)
	if second.limit != interventionReconcilerLimit {
		t.Fatalf("scheduled reconciliation limit = %d, want %d", second.limit, interventionReconcilerLimit)
	}

	cancel()
	waitForInterventionReconciler(t, service)
	select {
	case <-ticker.stopped:
	case <-time.After(time.Second):
		t.Fatal("intervention reconciler did not stop its ticker")
	}
}

func TestInterventionReconcilerCancellationDrainsWaitGroup(t *testing.T) {
	rootCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reconciler := newRecordingPolicyReconciler()
	reconciler.waitForCancel = true
	service := &Service{
		ctx: rootCtx,
		interventionReconcilerTickerFactory: func(time.Duration) interventionReconcilerTicker {
			return newManualInterventionReconcilerTicker()
		},
	}
	service.SetInterventionReconciler(reconciler)
	service.startInterventionReconciler()

	_ = awaitPolicyReconcileCall(t, reconciler)
	cancel()
	waitForInterventionReconciler(t, service)
}

func TestInitializeInterventionReconcilerMissingDependencyKeepsReadinessGreen(t *testing.T) {
	service := &Service{
		ctx:    context.Background(),
		config: config.Default(),
	}
	service.ready.Store(true)

	service.initializeInterventionReconciler(nil)

	service.initMu.RLock()
	reconciler := service.interventionReconciler
	service.initMu.RUnlock()
	if reconciler != nil {
		t.Fatal("missing policy-store dependency installed a reconciler")
	}
	if !service.ready.Load() {
		t.Fatal("missing policy-store dependency cleared readiness")
	}
	if err := service.GetInitError(); err != nil {
		t.Fatalf("missing optional policy-store dependency set init error: %v", err)
	}
}

type grpcAdvisorActivationProbe struct {
	activations atomic.Int32
}

func (*grpcAdvisorActivationProbe) GetSessionStartContext(context.Context, *pb.GetSessionStartContextRequest) (*pb.GetSessionStartContextResponse, error) {
	return nil, nil
}

func (p *grpcAdvisorActivationProbe) SetInterventionAdvisor(intervention.Advisor) {
	p.activations.Add(1)
}

func TestInterventionReconcilerLeavesGRPCAdvisorDefaultDark(t *testing.T) {
	rootCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	reconciler := newRecordingPolicyReconciler()
	probe := &grpcAdvisorActivationProbe{}
	service := &Service{
		ctx:                rootCtx,
		grpcInternalServer: probe,
		interventionReconcilerTickerFactory: func(time.Duration) interventionReconcilerTicker {
			return newManualInterventionReconcilerTicker()
		},
	}
	service.SetInterventionReconciler(reconciler)
	service.startInterventionReconciler()

	_ = awaitPolicyReconcileCall(t, reconciler)
	if got := probe.activations.Load(); got != 0 {
		t.Fatalf("policy reconciler activated gRPC advisor %d times", got)
	}

	cancel()
	waitForInterventionReconciler(t, service)
}
