package main

import (
	"context"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	collector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
)

type daemonMetricReceiver struct {
	collector.UnimplementedMetricsServiceServer

	mu       sync.Mutex
	requests []*collector.ExportMetricsServiceRequest
}

func (r *daemonMetricReceiver) Export(_ context.Context, request *collector.ExportMetricsServiceRequest) (*collector.ExportMetricsServiceResponse, error) {
	r.mu.Lock()
	r.requests = append(r.requests, request)
	r.mu.Unlock()
	return &collector.ExportMetricsServiceResponse{}, nil
}

func (r *daemonMetricReceiver) snapshot() []*collector.ExportMetricsServiceRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*collector.ExportMetricsServiceRequest(nil), r.requests...)
}

func TestInitDaemonObservability_ExportsDaemonResourceIdentity(t *testing.T) {
	previousArgs := os.Args
	os.Args = []string{"engram", muxcoreDaemonFlag}
	t.Cleanup(func() { os.Args = previousArgs })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	receiver := &daemonMetricReceiver{}
	server := grpc.NewServer()
	collector.RegisterMetricsServiceServer(server, receiver)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "http://"+listener.Addr().String())
	runtime, err := initDaemonObservability(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	requests := receiver.snapshot()
	if len(requests) == 0 {
		t.Fatal("collector received no daemon startup metric")
	}
	if !daemonRequestHasResourceAttribute(requests[0], "service.name", "engram-daemon") {
		t.Fatalf("daemon telemetry has the wrong resource identity: %v", requests[0])
	}
}

func TestInitDaemonObservability_ShimModeDoesNotStartExporter(t *testing.T) {
	previousArgs := os.Args
	os.Args = []string{"engram"}
	t.Cleanup(func() { os.Args = previousArgs })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	receiver := &daemonMetricReceiver{}
	server := grpc.NewServer()
	collector.RegisterMetricsServiceServer(server, receiver)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "http://"+listener.Addr().String())
	runtime, err := initDaemonObservability(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Enabled() {
		t.Fatal("short-lived client/shim process must not start a daemon OTLP exporter")
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(receiver.snapshot()) != 0 {
		t.Fatal("client/shim process exported daemon telemetry")
	}
}

func daemonRequestHasResourceAttribute(request *collector.ExportMetricsServiceRequest, key, want string) bool {
	for _, resourceMetrics := range request.GetResourceMetrics() {
		for _, attribute := range resourceMetrics.GetResource().GetAttributes() {
			if attribute.GetKey() == key && attribute.GetValue().GetStringValue() == want {
				return true
			}
		}
	}
	return false
}
