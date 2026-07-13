package engramcore

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/grpc"

	"github.com/thebtf/engram/internal/module/obs"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

type observablePingServer struct {
	pb.UnimplementedEngramServiceServer
}

func (observablePingServer) Ping(context.Context, *pb.PingRequest) (*pb.PingResponse, error) {
	return &pb.PingResponse{Status: "ok"}, nil
}

func TestDialGRPC_EmitsClientTransportMetric(t *testing.T) {
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

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	pb.RegisterEngramServiceServer(server, observablePingServer{})
	t.Cleanup(server.Stop)
	go func() { _ = server.Serve(listener) }()

	connection, err := dialGRPC(listener.Addr().String(), "http://"+listener.Addr().String(), "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })

	response, err := pb.NewEngramServiceClient(connection).Ping(context.Background(), &pb.PingRequest{})
	require.NoError(t, err)
	require.Equal(t, "ok", response.GetStatus())

	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))
	require.True(t, containsMetric(collected, "rpc.client.call.duration"), "production daemon gRPC calls must emit the ready-made otelgrpc client metric")
}

func containsMetric(collected metricdata.ResourceMetrics, name string) bool {
	for _, scope := range collected.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name == name {
				return true
			}
		}
	}
	return false
}
