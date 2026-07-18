package obs

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	collector "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
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

func startTLSMetricReceiver(t *testing.T, receiver *metricReceiver) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	serverCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	certPath := t.TempDir() + "/collector-ca.pem"
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS12,
	})))
	collector.RegisterMetricsServiceServer(server, receiver)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return "https://localhost:" + port, certPath
}

func clearOTLPEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_HEADERS",
		"OTEL_EXPORTER_OTLP_METRICS_TIMEOUT",
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

func TestOTLPTLSWithExplicitTrustRoot(t *testing.T) {
	clearOTLPEnv(t)
	receiver := &metricReceiver{}
	endpoint, certPath := startTLSMetricReceiver(t, receiver)
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", endpoint)
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_CERTIFICATE", certPath)
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_TIMEOUT", "1000")
	runtime, err := Init(context.Background(), "vtest")
	if err != nil {
		t.Fatal(err)
	}
	RecordRuntimeEvent(context.Background(), "startup", "tls_probe")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if len(receiver.snapshot()) == 0 {
		t.Fatal("TLS collector received no metric export")
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

func TestConcurrentInitRecordShutdown(t *testing.T) {
	clearOTLPEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", startMetricReceiver(t, &metricReceiver{}))

	const callers = 4
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			runtime, err := Init(context.Background(), "concurrent")
			if err != nil {
				errs <- err
				return
			}
			RecordRuntimeEvent(context.Background(), "runtime", "concurrent")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			errs <- runtime.Shutdown(ctx)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRuntimeOwnershipIsIdempotentAndReinitializable(t *testing.T) {
	clearOTLPEnv(t)
	receiver := &metricReceiver{}
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", startMetricReceiver(t, receiver))

	previous := otel.GetMeterProvider()
	first, err := Init(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Init(context.Background(), "second")
	if err != nil {
		t.Fatal(err)
	}
	if first.state != second.state {
		t.Fatal("concurrent owners must share the process-wide metrics pipeline")
	}
	if otel.GetMeterProvider() != previous {
		t.Fatal("Engram runtime must not replace the process-global meter provider")
	}
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if otel.GetMeterProvider() != previous {
		t.Fatal("non-final owner shutdown changed the process-global provider")
	}
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := second.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if otel.GetMeterProvider() != previous {
		t.Fatal("final owner shutdown changed the process-global provider")
	}

	third, err := Init(context.Background(), "third")
	if err != nil {
		t.Fatal(err)
	}
	if third.state == first.state {
		t.Fatal("re-init after final shutdown reused a closed pipeline")
	}
	if err := third.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRepeatedLifecycleWhileRecording(t *testing.T) {
	clearOTLPEnv(t)
	receiver := &metricReceiver{}
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", startMetricReceiver(t, receiver))

	stop := make(chan struct{})
	var recorder sync.WaitGroup
	recorder.Add(1)
	go func() {
		defer recorder.Done()
		for {
			select {
			case <-stop:
				return
			default:
				RecordRuntimeEvent(context.Background(), "runtime", "repeated")
			}
		}
	}()

	for range 10 {
		runtime, err := Init(context.Background(), "repeated")
		if err != nil {
			t.Fatal(err)
		}
		RecordRuntimeEvent(context.Background(), "runtime", "owned")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = runtime.Shutdown(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	recorder.Wait()
	if len(receiver.snapshot()) == 0 {
		t.Fatal("repeated runtime ownership exported no metrics")
	}
}

// TestMeterFor proves that MeterFor routes through the Engram-owned provider
// while a Runtime is active, exports a metric created that way, and falls back
// to the global no-op provider after final shutdown.
//
// This test must not run in parallel: it initialises and shuts down the
// package-level runtime (process-global instrumentProvider) and must restore
// the global OTel provider so parallel-safe meter tests remain unaffected.
func TestMeterFor(t *testing.T) {
	clearOTLPEnv(t)

	// Save the global provider before the test and restore it unconditionally
	// on exit, including on t.Fatal paths.
	prevGlobal := otel.GetMeterProvider()
	t.Cleanup(func() {
		otel.SetMeterProvider(prevGlobal)
		// Also ensure instrumentProvider is cleared so subsequent tests see a
		// clean state.
		instrumentsMu.Lock()
		instrumentProvider = nil
		resetInstrumentsLocked()
		instrumentsMu.Unlock()
	})

	receiver := &metricReceiver{}
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", startMetricReceiver(t, receiver))
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_TIMEOUT", "1000")

	runtime, err := Init(context.Background(), "vtest")
	if err != nil {
		t.Fatal(err)
	}

	// While the runtime is active, MeterFor must use the owned provider.
	instrumentsMu.RLock()
	ownedProvider := instrumentProvider
	instrumentsMu.RUnlock()
	if ownedProvider == nil {
		t.Fatal("instrumentProvider must be set while runtime is active")
	}

	// Create a counter through MeterFor and record a value so it appears in export.
	m := MeterFor("testmodule")
	counter, err := m.Int64Counter("engram_meterfor_probe_total")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(context.Background(), 1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}

	requests := receiver.snapshot()
	if len(requests) == 0 {
		t.Fatal("collector received no metric export from MeterFor probe")
	}
	wire, err := protojson.Marshal(requests[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), "engram_meterfor_probe_total") {
		t.Fatal("MeterFor metric was not exported through the owned provider")
	}

	// After final shutdown, instrumentProvider must be nil and MeterFor must
	// fall back to the global provider without panicking.
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	instrumentsMu.RLock()
	afterProvider := instrumentProvider
	instrumentsMu.RUnlock()
	if afterProvider != nil {
		t.Fatal("instrumentProvider must be nil after final shutdown")
	}

	// MeterFor after shutdown must return a usable (no-op) meter, not panic.
	// metric.Meter is an interface; the no-op global provider never returns nil.
	mAfter := MeterFor("testmodule-after")
	// Verify it is usable — global no-op meter never errors.
	_, err = mAfter.Int64Counter("engram_meterfor_fallback_probe")
	if err != nil {
		t.Fatalf("MeterFor fallback meter counter creation failed: %v", err)
	}
}
