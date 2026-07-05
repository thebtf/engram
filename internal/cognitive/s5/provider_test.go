package s5

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/cognitive/core"
)

func TestProviderNoSampleSnapshotReturnsExplicitEmptyMetrics(t *testing.T) {
	provider := NewProvider(Dependencies{})

	window := core.ProductMetricsWindow{
		Since: time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 7, 5, 10, 5, 0, 0, time.UTC),
	}
	snap, err := provider.ProductMetrics(context.Background(), window)
	if err != nil {
		t.Fatalf("ProductMetrics: %v", err)
	}

	if !snap.Window.Since.Equal(window.Since) || !snap.Window.Until.Equal(window.Until) {
		t.Fatalf("window: got %+v, want %+v", snap.Window, window)
	}
	if snap.SampleN != 0 {
		t.Fatalf("SampleN: got %d, want 0 for no-sample snapshot", snap.SampleN)
	}
	if snap.Metrics == nil {
		t.Fatalf("Metrics: got nil, want explicit empty map for no-sample snapshot")
	}
	if len(snap.Metrics) != 0 {
		t.Fatalf("Metrics: got %#v, want no product metric values without samples", snap.Metrics)
	}
}

func TestProviderSampleReadiness_NoProducerDataOmitsMetricsAndReportsNoSample(t *testing.T) {
	provider := NewProvider(Dependencies{})
	window := core.ProductMetricsWindow{
		Since: time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 7, 5, 10, 5, 0, 0, time.UTC),
	}

	snap, err := provider.ProductMetrics(context.Background(), window)
	if err != nil {
		t.Fatalf("ProductMetrics: %v", err)
	}

	if len(snap.Metrics) != 0 {
		t.Fatalf("Metrics: got %#v, want omitted metrics when producers are absent", snap.Metrics)
	}
	for metric, threshold := range canonicalReadinessThresholds() {
		assertMetricReadiness(t, snap, metric, 0, threshold, "no_sample")
	}
}

func TestProviderSampleReadiness_PartialProducerDataOnlyReportsSampledMetrics(t *testing.T) {
	source := &fixedMetricSource{samples: []MetricSample{
		{Metric: MetricHintPrecision, Value: 0.75, SampleN: 30},
	}}
	provider := NewProvider(Dependencies{MetricSources: []MetricSource{source}})

	snap, err := provider.ProductMetrics(context.Background(), core.ProductMetricsWindow{})
	if err != nil {
		t.Fatalf("ProductMetrics: %v", err)
	}

	if got := snap.Metrics[MetricHintPrecision]; got != 0.75 {
		t.Fatalf("%s: got %v, want measured value from sampled producer", MetricHintPrecision, got)
	}
	for metric, threshold := range canonicalReadinessThresholds() {
		if metric == MetricHintPrecision {
			continue
		}
		if value, ok := snap.Metrics[metric]; ok {
			t.Fatalf("%s: got measured value %v, want omitted because no producer sample exists", metric, value)
		}
		assertMetricReadiness(t, snap, metric, 0, threshold, "no_sample")
	}
	assertMetricReadiness(t, snap, MetricHintPrecision, 30, defaultThresholdHintPrecision, "ready")
}

func TestProviderSampleReadiness_DefaultHintPrecisionThresholdMakesTwentyNineBelowThreshold(t *testing.T) {
	provider := NewProvider(Dependencies{
		MetricSources: []MetricSource{&fixedMetricSource{samples: []MetricSample{
			{Metric: MetricHintPrecision, Value: 0.10, SampleN: 29},
		}}},
	})

	snap, err := provider.ProductMetrics(context.Background(), core.ProductMetricsWindow{})
	if err != nil {
		t.Fatalf("ProductMetrics: %v", err)
	}

	if value, ok := snap.Metrics[MetricHintPrecision]; ok {
		t.Fatalf("%s: got measured value %v, want omitted until sample threshold is met", MetricHintPrecision, value)
	}
	assertMetricReadiness(t, snap, MetricHintPrecision, 29, defaultThresholdHintPrecision, "below_threshold")
}

func TestProviderSampleReadiness_DefaultAcceptedHintActionThresholdMakesNineteenBelowThreshold(t *testing.T) {
	provider := NewProvider(Dependencies{
		MetricSources: []MetricSource{&fixedMetricSource{samples: []MetricSample{
			{Metric: MetricAcceptedHintAction, Value: 0.60, SampleN: 19},
		}}},
	})

	snap, err := provider.ProductMetrics(context.Background(), core.ProductMetricsWindow{})
	if err != nil {
		t.Fatalf("ProductMetrics: %v", err)
	}

	if value, ok := snap.Metrics[MetricAcceptedHintAction]; ok {
		t.Fatalf("%s: got measured value %v, want omitted until sample threshold is met", MetricAcceptedHintAction, value)
	}
	assertMetricReadiness(t, snap, MetricAcceptedHintAction, 19, defaultThresholdAcceptedHintAction, "below_threshold")
}

func TestProviderSampleReadiness_InvalidWindowRejectsBeforeSources(t *testing.T) {
	source := &fixedMetricSource{samples: []MetricSample{
		{Metric: MetricHintPrecision, Value: 0.75, SampleN: 30},
	}}
	provider := NewProvider(Dependencies{MetricSources: []MetricSource{source}})
	window := core.ProductMetricsWindow{
		Since: time.Date(2026, 7, 5, 10, 5, 0, 0, time.UTC),
		Until: time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC),
	}

	_, err := provider.ProductMetrics(context.Background(), window)
	if !errors.Is(err, ErrInvalidWindow) {
		t.Fatalf("ProductMetrics invalid window error: got %v, want ErrInvalidWindow", err)
	}
	if source.calls != 0 {
		t.Fatalf("source calls: got %d, want 0 so invalid windows fail before readiness aggregation", source.calls)
	}
}

func TestProviderSampleReadiness_AbsentStateFreshnessSourceIsOmittedHonestly(t *testing.T) {
	provider := NewProvider(Dependencies{
		MetricSources: []MetricSource{&fixedMetricSource{samples: []MetricSample{
			{Metric: MetricHintPrecision, Value: 0.80, SampleN: 30},
		}}},
	})

	snap, err := provider.ProductMetrics(context.Background(), core.ProductMetricsWindow{})
	if err != nil {
		t.Fatalf("ProductMetrics: %v", err)
	}

	if _, ok := snap.Metrics[MetricStateFreshness]; ok {
		t.Fatalf("%s: got measured value, want omitted when state freshness source is absent", MetricStateFreshness)
	}
	assertMetricReadiness(t, snap, MetricStateFreshness, 0, defaultThresholdStateFreshness, "no_sample")
}

type fixedMetricSource struct {
	samples []MetricSample
	calls   int
}

func (s *fixedMetricSource) ProductMetricSamples(_ context.Context, _ core.ProductMetricsWindow) ([]MetricSample, error) {
	s.calls++
	return s.samples, nil
}

func assertMetricReadiness(t *testing.T, snap core.ProductMetricsSnapshot, metric string, wantSampleN uint64, wantThresholdN uint64, wantState string) {
	t.Helper()

	readiness, ok := snap.Readiness[metric]
	if !ok {
		t.Fatalf("Readiness[%s]: missing; omitted metrics need explicit no-sample/below-threshold evidence", metric)
	}
	if readiness.SampleN != wantSampleN {
		t.Fatalf("Readiness[%s].SampleN: got %d, want %d", metric, readiness.SampleN, wantSampleN)
	}
	if readiness.ThresholdN != wantThresholdN {
		t.Fatalf("Readiness[%s].ThresholdN: got %d, want %d", metric, readiness.ThresholdN, wantThresholdN)
	}
	if readiness.State != wantState {
		t.Fatalf("Readiness[%s].State: got %q, want %q", metric, readiness.State, wantState)
	}
}

func TestProviderRejectsInvalidWindow(t *testing.T) {
	provider := NewProvider(Dependencies{})
	window := core.ProductMetricsWindow{
		Since: time.Date(2026, 7, 5, 10, 5, 0, 0, time.UTC),
		Until: time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC),
	}

	_, err := provider.ProductMetrics(context.Background(), window)
	if !errors.Is(err, ErrInvalidWindow) {
		t.Fatalf("ProductMetrics invalid window error: got %v, want ErrInvalidWindow", err)
	}
}

func TestProviderHonorsContextCancellation(t *testing.T) {
	provider := NewProvider(Dependencies{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := provider.ProductMetrics(ctx, core.ProductMetricsWindow{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ProductMetrics canceled context error: got %v, want context.Canceled", err)
	}
}

func TestCanonicalProductMetricKeysLiveOnlyInMetricsDeclarations(t *testing.T) {
	values := canonicalMetricValues()
	if len(values) != 5 {
		t.Fatalf("canonical metric key count: got %d, want 5", len(values))
	}

	seen := make(map[string]string, len(values))
	for constName, value := range values {
		if value == "" {
			t.Fatalf("%s: canonical metric key must not be empty", constName)
		}
		if prior, ok := seen[value]; ok {
			t.Fatalf("canonical metric key %q is shared by %s and %s", value, prior, constName)
		}
		seen[value] = constName
	}

	fset := token.NewFileSet()
	for _, name := range []string{"metrics.go", "provider.go"} {
		file := parseS5File(t, fset, name)
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			constName, isMetricKey := seen[value]
			if !isMetricKey {
				return true
			}
			if name != "metrics.go" {
				pos := fset.Position(lit.Pos())
				t.Errorf("%s:%d hard-codes canonical metric key %q (%s); S5 metric keys must come from metrics.go constants", filepath.Base(pos.Filename), pos.Line, value, constName)
			}
			return true
		})
	}
}

func TestProductMetricOutputImplementationAvoidsAdHocCanonicalMetricStrings(t *testing.T) {
	want := canonicalMetricValues()
	var adHoc []string

	fset := token.NewFileSet()
	file := parseS5File(t, fset, "provider.go")
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		for constName, metricKey := range want {
			if value == metricKey {
				pos := fset.Position(lit.Pos())
				adHoc = append(adHoc, filepath.Base(pos.Filename)+":"+strconv.Itoa(pos.Line)+" hard-codes "+metricKey+" instead of "+constName)
			}
		}
		return true
	})

	for _, violation := range adHoc {
		t.Errorf("product metric output path uses ad hoc key string: %s", violation)
	}
}

func canonicalMetricValues() map[string]string {
	return map[string]string{
		"MetricHintPrecision":      MetricHintPrecision,
		"MetricAcceptedHintAction": MetricAcceptedHintAction,
		"MetricMissRate":           MetricMissRate,
		"MetricInterruptionBurden": MetricInterruptionBurden,
		"MetricStateFreshness":     MetricStateFreshness,
	}
}

func parseS5File(t *testing.T, fset *token.FileSet, name string) *ast.File {
	t.Helper()

	_, selfFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	path := filepath.Join(filepath.Dir(selfFile), name)
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return file
}
