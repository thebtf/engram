package grpcserver

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/module/obs"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

func TestGRPCObservability_EmitsTransportAuthAndVersionMetrics(t *testing.T) {
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

	listener := bufconn.Listen(1 << 20)
	grpcServer, _ := New(
		staticMCPHandler{serverName: "engram", serverVersion: "v5.0.0"},
		auth.NewValidator("master-secret", &stubReader{}),
	)
	t.Cleanup(grpcServer.Stop)
	go func() { _ = grpcServer.Serve(listener) }()

	connection, err := grpc.NewClient(
		"passthrough:///engram-observability",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	client := pb.NewEngramServiceClient(connection)

	_, err = client.NegotiateVersion(context.Background(), &pb.NegotiateVersionRequest{ClientVersion: "v4.9.9"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	authed := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer master-secret")
	response, err := client.NegotiateVersion(authed, &pb.NegotiateVersionRequest{ClientVersion: "v4.9.9"})
	require.NoError(t, err)
	require.False(t, response.GetCompatible())

	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))
	require.True(t, hasMetric(collected, "rpc.server.call.duration"), "real gRPC calls must emit the ready-made otelgrpc server metric")
	require.True(t, hasRuntimeEvent(collected, "auth", "missing_credentials"), "auth rejection must be diagnosable without recording credential data")
	require.True(t, hasRuntimeEvent(collected, "client_version", "incompatible"), "incompatible clients must be diagnosable with a bounded outcome")
}

func hasMetric(collected metricdata.ResourceMetrics, name string) bool {
	for _, scope := range collected.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name == name {
				return true
			}
		}
	}
	return false
}

func hasRuntimeEvent(collected metricdata.ResourceMetrics, component, outcome string) bool {
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
				if attributeValue(point.Attributes, "component") == component && attributeValue(point.Attributes, "outcome") == outcome {
					return true
				}
			}
		}
	}
	return false
}

func attributeValue(set attribute.Set, key string) string {
	value, ok := set.Value(attribute.Key(key))
	if !ok {
		return ""
	}
	return value.AsString()
}
