package obs

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// MeterFor returns an OTel metric.Meter scoped to the named engram module.
// It is the single-point accessor for per-module meters: all module code that
// needs to create OTel instruments must call this function instead of calling
// otel.GetMeterProvider() directly. This enforces the framework rule that only
// the obs package reaches the global meter provider, keeping caching, label
// injection, or provider-swap logic centralised at one seam.
//
// The returned scope name is "github.com/thebtf/engram/<moduleName>", which
// follows the OTel Go conventions for instrumentation-scope naming and keeps
// per-module dashboards distinct from the framework-level metrics in metrics.go.
//
// When an Engram-owned OTLP runtime is active, MeterFor returns a meter backed
// by that owned provider so module metrics flow through the same pipeline as
// the framework wrapper metrics. After final shutdown (or when no runtime was
// ever configured) it falls back to otel.GetMeterProvider(), matching the
// no-op behaviour of the disabled path.
//
// Passing an empty moduleName is safe: it returns a meter with an unusual scope
// name ("github.com/thebtf/engram/") but does not panic.
func MeterFor(moduleName string) metric.Meter {
	const prefix = "github.com/thebtf/engram/"
	instrumentsMu.RLock()
	p := instrumentProvider
	instrumentsMu.RUnlock()
	if p != nil {
		return p.Meter(prefix + moduleName)
	}
	return otel.GetMeterProvider().Meter(prefix + moduleName)
}
