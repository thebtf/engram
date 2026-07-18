package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	collector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
)

type fatalFlushMetricReceiver struct {
	collector.UnimplementedMetricsServiceServer
	mu       sync.Mutex
	requests []*collector.ExportMetricsServiceRequest
}

func (r *fatalFlushMetricReceiver) Export(_ context.Context, req *collector.ExportMetricsServiceRequest) (*collector.ExportMetricsServiceResponse, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	return &collector.ExportMetricsServiceResponse{}, nil
}

// TestFatalStartupFlush proves that a fatal startup caused by missing auth
// configuration:
//   - exits non-zero within a bounded time window
//   - emits and flushes the "initialization_error" metric to the collector
//   - does not leak authorization metadata in log output or metric payload
func TestFatalStartupFlush(t *testing.T) {
	if os.Getenv("ENGRAM_FATAL_FLUSH_CHILD") == "1" {
		main()
		return
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	receiver := &fatalFlushMetricReceiver{}
	server := grpc.NewServer()
	collector.RegisterMetricsServiceServer(server, receiver)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestFatalStartupFlush$")
	cmd.Env = append(os.Environ(),
		"ENGRAM_FATAL_FLUSH_CHILD=1",
		"ENGRAM_AUTH_ADMIN_TOKEN=",
		"ENGRAM_AUTH_DISABLED=",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=http://"+listener.Addr().String(),
		"OTEL_EXPORTER_OTLP_METRICS_TIMEOUT=1000",
		"USERPROFILE="+home,
		"HOME="+home,
		"APPDATA="+home,
		"LOCALAPPDATA="+home,
	)
	started := time.Now()
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("fatal startup child unexpectedly exited successfully")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("fatal startup flush exceeded bound: %s", elapsed)
	}
	logText := string(output)
	if !strings.Contains(logText, "ENGRAM_AUTH_ADMIN_TOKEN is not set") {
		t.Fatalf("fatal startup output was not actionable: %s", logText)
	}

	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	var wire strings.Builder
	for _, request := range receiver.requests {
		wire.WriteString(request.String())
	}
	if !strings.Contains(wire.String(), "initialization_error") {
		t.Fatal("fatal startup did not flush the worker initialization diagnostic")
	}
	if strings.Contains(logText, "authorization=") || strings.Contains(wire.String(), "authorization=") {
		t.Fatal("fatal startup evidence leaked OTLP authorization metadata")
	}
}
