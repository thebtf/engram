package obs

import (
	"context"
	"errors"
	"net/url"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

const serviceName = "engram-server"

// Runtime owns Engram's process-wide metrics pipeline. A disabled Runtime is a
// valid no-op value when no OTLP endpoint was explicitly configured.
type Runtime struct {
	enabled  bool
	provider *sdkmetric.MeterProvider
	previous metric.MeterProvider

	shutdownOnce sync.Once
	shutdownErr  error
}

// Init installs an OTLP/gRPC metric pipeline only when an endpoint is
// explicitly configured. All exporter connection settings continue to come
// from the standard OTEL_EXPORTER_OTLP_* environment variables.
func Init(ctx context.Context, serviceVersion string) (*Runtime, error) {
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
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(serviceVersion),
	)
	reader := sdkmetric.NewPeriodicReader(exporter)
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	)
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	ResetInstrumentsForTesting()
	return &Runtime{enabled: true, provider: provider, previous: previous}, nil
}

func safeEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

// Enabled reports whether this runtime owns an active OTLP exporter.
func (r *Runtime) Enabled() bool { return r != nil && r.enabled }

// ForceFlush exports all pending metrics within the caller's deadline.
func (r *Runtime) ForceFlush(ctx context.Context) error {
	if r == nil || r.provider == nil {
		return nil
	}
	return r.provider.ForceFlush(ctx)
}

// Shutdown flushes and closes the exporter once. It restores the prior global
// provider so tests and embedded callers cannot record into a closed provider.
func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil || r.provider == nil {
		return nil
	}
	r.shutdownOnce.Do(func() {
		r.shutdownErr = r.provider.Shutdown(ctx)
		otel.SetMeterProvider(r.previous)
		ResetInstrumentsForTesting()
	})
	return r.shutdownErr
}
