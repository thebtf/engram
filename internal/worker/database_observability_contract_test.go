package worker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/module/obs"
)

func TestOpenObservedStore_RecordsInitializationFailure(t *testing.T) {
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

	store, err := openObservedStore(context.Background(), dbgorm.Config{DSN: "://invalid-observability-contract"})
	require.Error(t, err)
	require.Nil(t, store)

	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))
	require.True(t, workerHasRuntimeEvent(collected, "database", "initialization_error"), "database startup failures must emit a bounded diagnostic metric")
}

func workerHasRuntimeEvent(collected metricdata.ResourceMetrics, component, outcome string) bool {
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
				if workerAttributeValue(point.Attributes, "component") == component && workerAttributeValue(point.Attributes, "outcome") == outcome {
					return true
				}
			}
		}
	}
	return false
}

func workerAttributeValue(set attribute.Set, key string) string {
	value, ok := set.Value(attribute.Key(key))
	if !ok {
		return ""
	}
	return value.AsString()
}
