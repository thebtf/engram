package obs

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

const serviceName = "engram-server"

// Runtime owns Engram's process-wide metrics pipeline. A disabled Runtime is a
// valid no-op value when no OTLP endpoint was explicitly configured.
type Runtime struct {
	state *runtimeState

	shutdownOnce sync.Once
	shutdownErr  error
}

type runtimeState struct {
	provider *sdkmetric.MeterProvider
	owners   int
}

var (
	runtimeMu     sync.Mutex
	activeRuntime *runtimeState
)

// Init installs one Engram-owned OTLP/gRPC metric pipeline only when an
// endpoint is explicitly configured. Concurrent callers share that pipeline;
// the first configured owner supplies its service version and exporter config.
// The process-global OpenTelemetry provider is left untouched.
func Init(ctx context.Context, serviceVersion string) (*Runtime, error) {
	return InitForService(ctx, serviceName, serviceVersion)
}

// InitForService installs the same bounded OTLP metrics runtime as Init while
// assigning the process its real resource identity. The daemon and server are
// separate executables, so collectors must not merge their transport metrics
// under one service.name.
func InitForService(ctx context.Context, resourceServiceName, serviceVersion string) (*Runtime, error) {
	resourceServiceName = strings.TrimSpace(resourceServiceName)
	if resourceServiceName == "" {
		return nil, errors.New("observability service name is required")
	}
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")
	if endpoint == "" {
		endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	if endpoint == "" {
		return &Runtime{}, nil
	}
	if !safeEndpoint(endpoint) {
		return nil, errors.New("invalid OTLP metrics endpoint: require http(s) URL without credentials, query, or fragment")
	}

	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if activeRuntime != nil {
		activeRuntime.owners++
		return &Runtime{state: activeRuntime}, nil
	}

	exporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithRetry(otlpmetricgrpc.RetryConfig{
		Enabled:         true,
		InitialInterval: 200 * time.Millisecond,
		MaxInterval:     time.Second,
		MaxElapsedTime:  5 * time.Second,
	}))
	if err != nil {
		return nil, errors.New("initialize OTLP metrics exporter: check endpoint and TLS configuration")
	}

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(resourceServiceName),
		semconv.ServiceVersion(serviceVersion),
	)
	reader := sdkmetric.NewPeriodicReader(exporter)
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	)
	instrumentsMu.Lock()
	instrumentProvider = provider
	resetInstrumentsLocked()
	instrumentsMu.Unlock()

	activeRuntime = &runtimeState{provider: provider, owners: 1}
	return &Runtime{state: activeRuntime}, nil
}

func safeEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

// Enabled reports whether this handle joined an OTLP exporter runtime.
func (r *Runtime) Enabled() bool { return r != nil && r.state != nil }

// ForceFlush exports all pending metrics within the caller's deadline.
func (r *Runtime) ForceFlush(ctx context.Context) error {
	if r == nil || r.state == nil {
		return nil
	}
	instrumentsMu.RLock()
	defer instrumentsMu.RUnlock()
	return r.state.provider.ForceFlush(ctx)
}

// Shutdown releases this handle once. The final owner flushes and closes the
// exporter, then returns Engram metrics to the untouched global provider.
func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil || r.state == nil {
		return nil
	}
	r.shutdownOnce.Do(func() {
		runtimeMu.Lock()
		defer runtimeMu.Unlock()
		if activeRuntime != r.state {
			return
		}
		r.state.owners--
		if r.state.owners != 0 {
			return
		}

		instrumentsMu.Lock()
		r.shutdownErr = r.state.provider.Shutdown(ctx)
		instrumentProvider = nil
		resetInstrumentsLocked()
		instrumentsMu.Unlock()
		activeRuntime = nil
	})
	return r.shutdownErr
}
