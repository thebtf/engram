// Package obs is the single-point-of-change wrapper around the OpenTelemetry
// metric API used by the engram module framework. All metric instruments used
// by the dispatcher and lifecycle pipeline are registered here and exposed via
// typed helper functions.
//
// Default behaviour: when OTEL_EXPORTER_OTLP_ENDPOINT is unset, the global
// meter provider returned by otel.GetMeterProvider() is a no-op. Recording
// metrics against it is safe and has effectively zero cost. The export
// pipeline is activated only when the standard OTel environment variables
// select a real exporter — no engram-specific configuration is required.
//
// Framework rule: no code outside this package may call otel.GetMeterProvider()
// or construct meters directly. This guarantees a single place to add caching,
// labels, or exporter hooks in the future.
package obs

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// scopeName is the OpenTelemetry instrumentation scope for every engram
// module-framework metric. Keeping it constant (rather than per-module) makes
// dashboards easier to build and matches the standard "one meter per library"
// pattern from the OTel Go conventions.
const scopeName = "github.com/thebtf/engram/internal/module"

// meter returns the process-wide engram framework meter. This is a thin
// wrapper so that later T064+ work can swap in caching or instrumentation-
// version labels at one seam instead of every call site.
func meter() metric.Meter {
	return otel.GetMeterProvider().Meter(scopeName)
}
