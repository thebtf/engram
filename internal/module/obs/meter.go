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
// Passing an empty moduleName is safe: it returns a meter with an unusual scope
// name ("github.com/thebtf/engram/") but does not panic.
func MeterFor(moduleName string) metric.Meter {
	return otel.GetMeterProvider().Meter("github.com/thebtf/engram/" + moduleName)
}
