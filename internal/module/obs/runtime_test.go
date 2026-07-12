package obs

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	collector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

type metricReceiver struct {
	collector.UnimplementedMetricsServiceServer

	mu             sync.Mutex
	requests       []*collector.ExportMetricsServiceRequest
	requiredHeader string
	delay          time.Duration
	responseCode   codes.Code
	attempts       int
}

func (r *metricReceiver) Export(ctx context.Context, req *collector.ExportMetricsServiceRequest) (*collector.ExportMetricsServiceResponse, error) {
	r.mu.Lock()
	r.attempts++
	r.mu.Unlock()
	if r.delay > 0 {
		select {
		case <-time.After(r.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if r.requiredHeader != "" {
		md, _ := metadata.FromIncomingContext(ctx)
		if got := md.Get("authorization"); len(got) != 1 || got[0] != r.requiredHeader {
			return nil, status.Error(codes.Unauthenticated, "collector authentication failed")
		}
	}
	if r.responseCode != codes.OK {
		return nil, status.Error(r.responseCode, "collector rejected telemetry")
	}
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	return &collector.ExportMetricsServiceResponse{}, nil
}

func (r *metricReceiver) attemptCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts
}

func (r *metricReceiver) snapshot() []*collector.ExportMetricsServiceRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*collector.ExportMetricsServiceRequest(nil), r.requests...)
}

func startMetricReceiver(t *testing.T, receiver *metricReceiver) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	collector.RegisterMetricsServiceServer(server, receiver)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return "http://" + listener.Addr().String()
}

func clearOTLPEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_HEADERS",
		"OTEL_EXPORTER_OTLP_METRICS_HEADERS",
		"OTEL_EXPORTER_OTLP_TIMEOUT",
		"OTEL_EXPORTER_OTLP_METRICS_TIMEOUT",
		"OTEL_EXPORTER_OTLP_CERTIFICATE",
		"OTEL_EXPORTER_OTLP_METRICS_CERTIFICATE",
	} {
		t.Setenv(key, "")
	}
}

func TestInitNoEndpointIsNoop(t *testing.T) {
	clearOTLPEnv(t)
	runtime, err := Init(context.Background(), "vtest")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Enabled() {
		t.Fatal("telemetry must remain disabled without an explicit endpoint")
	}
	RecordRuntimeEvent(context.Background(), "startup", "started")
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOTLPExportsStableMetricsAndKeepsHeaderOutOfPayload(t *testing.T) {
	clearOTLPEnv(t)
	const secret = "Bearer-test-secret-should-not-be-in-payload"
	receiver := &metricReceiver{requiredHeader: secret}
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", startMetricReceiver(t, receiver))
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS", "authorization="+secret)
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_TIMEOUT", "1000")

	runtime, err := Init(context.Background(), "vtest")
	if err != nil {
		t.Fatal(err)
	}
	RecordRuntimeEvent(context.Background(), "startup", "started")
	RecordHandleTool(context.Background(), "engramcore", "recall", "ok", 7)
	RecordHandleToolError(context.Background(), "engramcore", "recall", "timeout")
	RecordModuleInit(context.Background(), "engramcore", 3)
	IncrementActiveSessions(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	requests := receiver.snapshot()
	if len(requests) == 0 {
		t.Fatal("collector received no metric export")
	}
	wire, err := protojson.Marshal(requests[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), secret) {
		t.Fatal("OTLP header secret leaked into metric payload")
	}
	for _, name := range []string{
		"engram_runtime_events_total",
		"engram_handletool_duration_ms",
		"engram_handletool_errors_total",
		"engram_module_init_duration_ms",
		"engram_active_sessions",
	} {
		if !strings.Contains(string(wire), name) {
			t.Fatalf("OTLP payload missing stable metric %q", name)
		}
	}
}

func TestCollectorAuthFailureIsBoundedAndSecretFree(t *testing.T) {
	clearOTLPEnv(t)
	const secret = "wrong-secret-value"
	receiver := &metricReceiver{requiredHeader: "Bearer-correct"}
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", startMetricReceiver(t, receiver))
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS", "authorization="+secret)
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_TIMEOUT", "1000")

	runtime, err := Init(context.Background(), "vtest")
	if err != nil {
		t.Fatal(err)
	}
	RecordRuntimeEvent(context.Background(), "auth", "failure")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = runtime.ForceFlush(ctx)
	if err == nil {
		t.Fatal("expected collector authentication failure")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("exporter error leaked authorization header")
	}
	_ = runtime.Shutdown(ctx)
}

func TestCollectorBackpressureHonorsDeadline(t *testing.T) {
	clearOTLPEnv(t)
	receiver := &metricReceiver{delay: 500 * time.Millisecond}
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", startMetricReceiver(t, receiver))
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_TIMEOUT", "50")

	runtime, err := Init(context.Background(), "vtest")
	if err != nil {
		t.Fatal(err)
	}
	RecordRuntimeEvent(context.Background(), "worker", "backpressure_probe")
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if err := runtime.ForceFlush(ctx); err == nil {
		t.Fatal("expected bounded export timeout")
	}
	if elapsed := time.Since(started); elapsed > 450*time.Millisecond {
		t.Fatalf("export backpressure exceeded deadline: %s", elapsed)
	}
	_ = runtime.Shutdown(ctx)
}

func TestTransientCollectorFailureRetriesWithinCallerDeadline(t *testing.T) {
	clearOTLPEnv(t)
	receiver := &metricReceiver{responseCode: codes.Unavailable}
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", startMetricReceiver(t, receiver))
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_TIMEOUT", "1000")
	runtime, err := Init(context.Background(), "vtest")
	if err != nil {
		t.Fatal(err)
	}
	RecordRuntimeEvent(context.Background(), "worker", "retry_probe")
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	if err := runtime.ForceFlush(ctx); err == nil {
		t.Fatal("expected transient collector failure")
	}
	if receiver.attemptCount() < 2 {
		t.Fatalf("transient collector failure was not retried: attempts=%d", receiver.attemptCount())
	}
	_ = runtime.Shutdown(ctx)
}

func TestExporterOutageAndTLSMismatchAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint func(t *testing.T) string
	}{
		{
			name: "outage",
			endpoint: func(t *testing.T) string {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				address := listener.Addr().String()
				_ = listener.Close()
				return "http://" + address
			},
		},
		{
			name: "tls_mismatch",
			endpoint: func(t *testing.T) string {
				receiver := &metricReceiver{}
				return strings.Replace(startMetricReceiver(t, receiver), "http://", "https://", 1)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearOTLPEnv(t)
			t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", tc.endpoint(t))
			t.Setenv("OTEL_EXPORTER_OTLP_METRICS_TIMEOUT", "50")
			runtime, err := Init(context.Background(), "vtest")
			if err != nil {
				t.Fatal(err)
			}
			RecordRuntimeEvent(context.Background(), "worker", "failure_probe")
			started := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
			defer cancel()
			if err := runtime.ForceFlush(ctx); err == nil {
				t.Fatal("expected exporter failure")
			}
			if elapsed := time.Since(started); elapsed > 450*time.Millisecond {
				t.Fatalf("exporter failure exceeded deadline: %s", elapsed)
			}
			_ = runtime.Shutdown(ctx)
		})
	}
}

func TestShutdownFlushesPendingMetric(t *testing.T) {
	clearOTLPEnv(t)
	receiver := &metricReceiver{}
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", startMetricReceiver(t, receiver))
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_TIMEOUT", "1000")
	runtime, err := Init(context.Background(), "vtest")
	if err != nil {
		t.Fatal(err)
	}
	RecordRuntimeEvent(context.Background(), "worker", "shutdown_probe")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if len(receiver.snapshot()) == 0 {
		t.Fatal("shutdown did not flush pending metrics")
	}
}

func TestEndpointCredentialsAreRejectedWithoutEcho(t *testing.T) {
	clearOTLPEnv(t)
	const secret = "do-not-echo"
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "https://user:"+secret+"@collector.invalid:4317")
	_, err := Init(context.Background(), "vtest")
	if err == nil {
		t.Fatal("expected endpoint credentials to be rejected")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("validation error echoed endpoint credentials")
	}
}
