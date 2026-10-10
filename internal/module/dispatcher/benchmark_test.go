package dispatcher

// BenchmarkHandleTool_NoExporter and BenchmarkHandleTool_StdoutExporter
// measure the per-call overhead of metric emission in handleToolsCall.
//
// Design rationale:
//   - "NoExporter" runs with the OTel global no-op provider (the default when
//     no SDK is registered). This is the baseline: metric calls compile to
//     almost nothing.
//   - "WithRecorder" uses a minimal in-memory counting provider that actually
//     records each call into an atomic counter. This is intentionally NOT the
//     OTel SDK (which is not in go.mod as a direct dep) — it satisfies the
//     spec's requirement to "measure REAL recording overhead, not a no-op path"
//     with zero new external dependencies.
//
// NFR-9: metric overhead <= 5% of HandleTool p50 latency, or <= 50 µs.
// NFR-1: p99 HandleTool wall-clock < 1 000 ms.
//
// The TestBenchmarkResults_OverheadWithinBudget test enforces both NFRs.

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/module/obs"
	"github.com/thebtf/engram/internal/module/registry"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/embedded"
	"go.opentelemetry.io/otel/metric/noop"
)

// ---------------------------------------------------------------------------
// Fake module for benchmarking
// ---------------------------------------------------------------------------

// benchMod is a zero-I/O ToolProvider that returns a fixed JSON payload.
type benchMod struct{}

func (b *benchMod) Name() string                                      { return "bench" }
func (b *benchMod) Init(_ context.Context, _ module.ModuleDeps) error { return nil }
func (b *benchMod) Shutdown(_ context.Context) error                  { return nil }
func (b *benchMod) Tools() []module.ToolDef {
	return []module.ToolDef{{
		Name:        "bench.noop",
		Description: "benchmark no-op tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}}
}

func (b *benchMod) HandleTool(_ context.Context, _ muxcore.ProjectContext, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`"ok"`), nil
}

// ---------------------------------------------------------------------------
// Minimal in-memory MeterProvider for the "WithRecorder" benchmark
// ---------------------------------------------------------------------------
//
// countingProvider is a minimal metric.MeterProvider that counts every
// Int64Histogram.Record, Int64Counter.Add, and Int64UpDownCounter.Add call
// into an atomic counter. It satisfies the "REAL recording" requirement from
// the spec without pulling in the OTel SDK.

type countingProvider struct {
	embedded.MeterProvider
	calls atomic.Int64
}

func (p *countingProvider) Meter(_ string, _ ...metric.MeterOption) metric.Meter {
	// Delegate all methods to the OTel noop.Meter; we override only the three
	// instrument constructors we actually use (Int64Histogram, Int64Counter,
	// Int64UpDownCounter) so the outer interface stays forward-compatible as
	// OTel adds new instrument kinds. This is the pattern recommended in the
	// go.opentelemetry.io/otel/metric/noop package docs.
	return &countingMeter{Meter: noop.NewMeterProvider().Meter(""), provider: p}
}

// countingMeter embeds a noop.Meter so every Meter interface method has a
// default implementation. We override only the three instrument constructors
// that engram's obs package actually calls.
type countingMeter struct {
	metric.Meter
	provider *countingProvider
}

func (m *countingMeter) Int64Histogram(_ string, _ ...metric.Int64HistogramOption) (metric.Int64Histogram, error) {
	return &countingHistogram{provider: m.provider}, nil
}

func (m *countingMeter) Int64Counter(_ string, _ ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	return &countingCounter{provider: m.provider}, nil
}

func (m *countingMeter) Int64UpDownCounter(_ string, _ ...metric.Int64UpDownCounterOption) (metric.Int64UpDownCounter, error) {
	return &countingUpDown{provider: m.provider}, nil
}

// countingHistogram increments the provider call counter on each Record.
type countingHistogram struct {
	embedded.Int64Histogram
	provider *countingProvider
}

func (h *countingHistogram) Record(_ context.Context, _ int64, _ ...metric.RecordOption) {
	h.provider.calls.Add(1)
}
func (h *countingHistogram) Enabled(_ context.Context) bool { return true }

// countingCounter increments the provider call counter on each Add.
type countingCounter struct {
	embedded.Int64Counter
	provider *countingProvider
}

func (c *countingCounter) Add(_ context.Context, _ int64, _ ...metric.AddOption) {
	c.provider.calls.Add(1)
}
func (c *countingCounter) Enabled(_ context.Context) bool { return true }

// countingUpDown increments the provider call counter on each Add.
type countingUpDown struct {
	embedded.Int64UpDownCounter
	provider *countingProvider
}

func (u *countingUpDown) Add(_ context.Context, _ int64, _ ...metric.AddOption) {
	u.provider.calls.Add(1)
}
func (u *countingUpDown) Enabled(_ context.Context) bool { return true }

// ---------------------------------------------------------------------------
// Benchmark helpers
// ---------------------------------------------------------------------------

// buildBenchDispatcher creates a Dispatcher with the bench module registered.
func buildBenchDispatcher(b testing.TB) *Dispatcher {
	b.Helper()
	r := registry.New()
	if err := r.Register(&benchMod{}); err != nil {
		b.Fatalf("Register: %v", err)
	}
	r.Freeze()
	return New(r, slog.New(slog.NewTextHandler(devNull{}, nil)))
}

// devNull discards all log output in benchmarks to avoid I/O noise.
type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }

var benchRequest = jsonrpcReq(1, "tools/call", map[string]any{
	"name":      "bench.noop",
	"arguments": map[string]any{},
})

var benchProject = projectCtx("bench-project")

// ---------------------------------------------------------------------------
// BenchmarkHandleTool_NoExporter — baseline with the OTel no-op provider
// ---------------------------------------------------------------------------

// BenchmarkHandleTool_NoExporter measures HandleRequest throughput with the
// default OTel no-op provider. The no-op provider is used when no SDK is
// registered, which is the production default unless OTEL_EXPORTER_OTLP_ENDPOINT
// is set. Metric calls are effectively free.
func BenchmarkHandleTool_NoExporter(b *testing.B) {
	restoreBenchProviderAfter(b)
	otel.SetMeterProvider(noop.NewMeterProvider())
	resetInstruments()
	d := buildBenchDispatcher(b)
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp, _ := d.HandleRequest(ctx, benchProject, benchRequest)
		_ = resp
	}
	b.StopTimer()
}

// ---------------------------------------------------------------------------
// BenchmarkHandleTool_StdoutExporter — with a real counting recorder
// ---------------------------------------------------------------------------

// BenchmarkHandleTool_StdoutExporter measures HandleRequest throughput with a
// real in-memory metric provider registered. Unlike the no-op path, every
// metric.Record/Add call performs an atomic increment, simulating the
// overhead of a real recording path.
//
// Note: the name "StdoutExporter" refers to the intended comparison target
// from the spec. The actual implementation uses an in-memory counting provider
// (not the OTel stdout exporter package) per the spec's fallback clause:
// "if stdoutmetric dependency is too invasive, use a minimal in-memory reader".
// The counting provider records REAL atomic writes for each metric call,
// satisfying the "measure REAL recording overhead, not a no-op path" requirement.
func BenchmarkHandleTool_StdoutExporter(b *testing.B) {
	restoreBenchProviderAfter(b)
	cp := &countingProvider{}
	otel.SetMeterProvider(cp)
	resetInstruments()
	d := buildBenchDispatcher(b)
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp, _ := d.HandleRequest(ctx, benchProject, benchRequest)
		_ = resp
	}
	b.StopTimer()
	if calls := cp.calls.Load(); calls != int64(b.N) {
		b.Fatalf("recorder metric calls: got %d, want %d", calls, b.N)
	}
	b.ReportMetric(float64(cp.calls.Load())/float64(b.N), "metrics/op")
}

// ---------------------------------------------------------------------------
// TestBenchmarkResults_OverheadWithinBudget — NFR-1 + NFR-9 enforcement
// ---------------------------------------------------------------------------

// TestBenchmarkResults_OverheadWithinBudget measures both providers with the
// same per-call clock and alternates which provider runs first in each batch.
// All samples contribute to nearest-rank p50/p99; no millisecond truncation.
func TestBenchmarkResults_OverheadWithinBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping overhead budget test in -short mode")
	}

	const iterations = 1000
	const batchSize = 100
	restoreBenchProviderAfter(t)
	d := buildBenchDispatcher(t)
	cp := &countingProvider{}
	providers := [2]metric.MeterProvider{noop.NewMeterProvider(), cp}
	samples := [2][]time.Duration{make([]time.Duration, iterations), make([]time.Duration, iterations)}
	for batch := range iterations / batchSize {
		for offset := range providers {
			mode := (batch + offset) % len(providers)
			otel.SetMeterProvider(providers[mode])
			resetInstruments()
			runHandleRequestSamples(t, d, samples[mode][batch*batchSize:(batch+1)*batchSize])
		}
	}
	if calls := cp.calls.Load(); calls != iterations {
		t.Fatalf("recorder metric calls: got %d, want %d", calls, iterations)
	}

	zeroSamples := [2]int{}
	for mode := range samples {
		for index, duration := range samples[mode] {
			if duration < 0 {
				t.Fatalf("NFR-9: negative elapsed sample (mode=%d, index=%d, duration=%s)", mode, index, duration)
			}
			if duration == 0 {
				zeroSamples[mode]++
			}
		}
	}
	baselineP50, baselineP99 := handleToolLatencyPercentiles(samples[0])
	recorderP50, recorderP99 := handleToolLatencyPercentiles(samples[1])
	delta := recorderP50 - baselineP50
	t.Logf("baseline p50: %s; recorder p50: %s; delta: %s", baselineP50, recorderP50, delta)
	if baselineP50 == 0 {
		t.Log("overhead percentage: undefined (baseline p50=0); enforcing the unchanged 50000 ns absolute budget")
	} else {
		t.Logf("overhead: %.3f%%", float64(delta)/float64(baselineP50)*100)
	}
	t.Logf("samples per mode: %d; baseline zero samples: %d; recorder zero samples: %d", iterations, zeroSamples[0], zeroSamples[1])
	t.Logf("baseline p99: %s; recorder p99: %s; recorder metric calls: %d", baselineP99, recorderP99, cp.calls.Load())

	// Keep the existing 5% OR 50 µs budget for the zero-work tool fixture.
	if metricOverheadExceeded(baselineP50, recorderP50) {
		t.Errorf("NFR-9 FAIL: %d ns absolute delta exceeds 50000 ns and is not within 5%% of baseline (baseline p50=%d ns, recorder p50=%d ns)",
			delta.Nanoseconds(), baselineP50.Nanoseconds(), recorderP50.Nanoseconds())
	}
	if baselineP99 >= time.Second || recorderP99 >= time.Second {
		t.Errorf("NFR-1 FAIL: p99 latency exceeds 1000 ms budget (baseline=%s, recorder=%s)", baselineP99, recorderP99)
	}
}

func runHandleRequestSamples(t testing.TB, d *Dispatcher, durations []time.Duration) {
	t.Helper()
	ctx := context.Background()
	for i := range durations {
		start := time.Now()
		resp, err := d.HandleRequest(ctx, benchProject, benchRequest)
		durations[i] = time.Since(start)
		if err != nil || len(resp) == 0 {
			t.Fatalf("HandleRequest: error=%v, response=%q", err, resp)
		}
	}
}

func handleToolLatencyPercentiles(durations []time.Duration) (time.Duration, time.Duration) {
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return durations[(len(durations)+1)/2-1], durations[(99*len(durations)+99)/100-1]
}

func metricOverheadExceeded(baseline, recorder time.Duration) bool {
	if baseline < 0 || recorder < 0 {
		return true
	}
	delta := recorder - baseline
	return delta > 50*time.Microsecond && (baseline == 0 || float64(delta)/float64(baseline) > 0.05)
}

func restoreBenchProviderAfter(t testing.TB) {
	original := otel.GetMeterProvider()
	t.Cleanup(func() {
		otel.SetMeterProvider(original)
		resetInstruments()
	})
}

func TestHandleToolLatencyPercentiles(t *testing.T) {
	for _, test := range []struct {
		name     string
		samples  []time.Duration
		p50, p99 time.Duration
	}{
		{"submillisecond singleton", []time.Duration{37 * time.Nanosecond}, 37 * time.Nanosecond, 37 * time.Nanosecond},
		{"zero singleton", []time.Duration{0}, 0, 0},
		{"zero median with nonzero tail", []time.Duration{40 * time.Microsecond, 0, 0, 0, 10 * time.Microsecond}, 0, 40 * time.Microsecond},
		{"odd with outlier", []time.Duration{90, 10, 70, 30, 20, 72 * time.Millisecond, 40, 80, 50, 60, 100}, 60, 72 * time.Millisecond},
		{"even nearest rank", []time.Duration{4, 1, 3, 2}, 2, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			p50, p99 := handleToolLatencyPercentiles(test.samples)
			if p50 != test.p50 || p99 != test.p99 {
				t.Fatalf("got p50=%s p99=%s, want p50=%s p99=%s", p50, p99, test.p50, test.p99)
			}
		})
	}

	samples := make([]time.Duration, 100)
	for i := range samples {
		samples[i] = time.Duration(100 - i)
	}
	if p50, p99 := handleToolLatencyPercentiles(samples); p50 != 50 || p99 != 99 {
		t.Fatalf("100-sample nearest ranks: got p50=%s p99=%s, want 50ns/99ns", p50, p99)
	}
	zeroSamples := make([]time.Duration, 1000)
	if p50, p99 := handleToolLatencyPercentiles(zeroSamples); p50 != 0 || p99 != 0 {
		t.Fatalf("1000 zero samples: got p50=%s p99=%s, want 0/0", p50, p99)
	}
}

func TestMetricOverheadBudget(t *testing.T) {
	for _, test := range []struct {
		name               string
		baseline, recorder time.Duration
		wantExceeded       bool
	}{
		{"retained CI breach", 113528 * time.Nanosecond, 186134 * time.Nanosecond, true},
		{"both boundaries", time.Millisecond, 1050 * time.Microsecond, false},
		{"percentage boundary", 2 * time.Millisecond, 2100 * time.Microsecond, false},
		{"absolute allowance", 100 * time.Microsecond, 140 * time.Microsecond, false},
		{"one ns over both", time.Millisecond, 1050*time.Microsecond + time.Nanosecond, true},
		{"recorder faster", time.Millisecond, 900 * time.Microsecond, false},
		{"both zero", 0, 0, false},
		{"zero baseline absolute allowance", 0, 49 * time.Microsecond, false},
		{"zero baseline absolute boundary", 0, 50 * time.Microsecond, false},
		{"zero baseline absolute breach", 0, 50*time.Microsecond + time.Nanosecond, true},
		{"negative baseline", -time.Nanosecond, 0, true},
		{"negative recorder", 0, -time.Nanosecond, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := metricOverheadExceeded(test.baseline, test.recorder); got != test.wantExceeded {
				t.Fatalf("baseline=%s recorder=%s: budget exceeded=%v, want %v", test.baseline, test.recorder, got, test.wantExceeded)
			}
		})
	}
}

// resetInstruments clears the lazily-initialised instrument singletons so
// that the next metric call creates fresh instruments against the currently
// registered MeterProvider. This is needed in tests that swap the global
// provider mid-run.
//
// ResetInstrumentsForTesting clears the obs singletons before each provider
// transition and after restoring the original provider.
func resetInstruments() {
	obs.ResetInstrumentsForTesting()
}
