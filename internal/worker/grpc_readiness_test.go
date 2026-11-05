package worker

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestGRPCReadinessGateRejectsThenRecoversSameClient(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{ctx: ctx}
	done := make(chan struct{})
	go func() { service.serveGRPCWhenReady(listener); close(done) }()
	defer func() { cancel(); _ = listener.Close(); <-done }()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDisableRetry())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := healthpb.NewHealthClient(conn)
	callCtx, callCancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, err = client.Check(callCtx, &healthpb.HealthCheckRequest{})
	callCancel()
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("before readiness: code=%s, want Unavailable", status.Code(err))
	}
	real := grpc.NewServer()
	healthpb.RegisterHealthServer(real, health.NewServer())
	service.initMu.Lock()
	service.grpcServer = real
	service.initMu.Unlock()
	service.ready.Store(true)
	defer real.Stop()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		callCtx, callCancel = context.WithTimeout(context.Background(), time.Second)
		_, err = client.Check(callCtx, &healthpb.HealthCheckRequest{})
		callCancel()
		if err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("same gRPC client did not recover after readiness: %v", err)
}

func TestGRPCReadinessGateStopsBeforeInitialization(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{ctx: ctx}
	done := make(chan struct{})
	go func() { service.serveGRPCWhenReady(listener); close(done) }()
	cancel()
	_ = listener.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readiness gate did not stop after cancellation")
	}
}
