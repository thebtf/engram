package codeintel_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/thebtf/engram/internal/module/obs"
	"github.com/thebtf/engram/internal/moduletest"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

func TestCodebaseIndex_RecordsBackgroundRunFailure(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	previousProvider := otel.GetMeterProvider()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	obs.ResetInstrumentsForTesting()
	t.Cleanup(func() {
		obs.ResetInstrumentsForTesting()
		otel.SetMeterProvider(previousProvider)
		_ = provider.Shutdown(context.Background())
	})

	core := &fakeCore{indexErr: errors.New("synthetic index failure")}
	mod := newTestModule(core)
	h := moduletest.New(t)
	require.NoError(t, h.Register(mod))
	h.Freeze()

	project := muxcore.ProjectContext{ID: "proj-observability", Cwd: t.TempDir()}
	args, err := json.Marshal(map[string]any{"root": project.Cwd})
	require.NoError(t, err)
	_, err = h.CallToolWithProject(context.Background(), project, "codebase_index", args)
	require.NoError(t, err)
	drainIndex(t, h, project)

	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))
	require.True(t, codeintelHasRuntimeEvent(collected, "index", "run_error"), "background index failures must emit a bounded diagnostic metric")
}

func codeintelHasRuntimeEvent(collected metricdata.ResourceMetrics, component, outcome string) bool {
	for _, scope := range collected.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != "engram_runtime_events_total" {
				continue
			}
			sum, ok := metric.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, point := range sum.DataPoints {
				if codeintelAttributeValue(point.Attributes, "component") == component && codeintelAttributeValue(point.Attributes, "outcome") == outcome {
					return true
				}
			}
		}
	}
	return false
}

func codeintelAttributeValue(set attribute.Set, key string) string {
	value, ok := set.Value(attribute.Key(key))
	if !ok {
		return ""
	}
	return value.AsString()
}
