package serverevents

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

type observableEventsServer struct {
	pb.UnimplementedEngramServiceServer
}

func (observableEventsServer) SyncProjectState(context.Context, *pb.SyncProjectStateRequest) (*pb.SyncProjectStateResponse, error) {
	return &pb.SyncProjectStateResponse{}, nil
}

func TestBridgeDialGRPC_EmitsClientTransportMetric(t *testing.T) {
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
	pb.RegisterEngramServiceServer(server, observableEventsServer{})
	t.Cleanup(server.Stop)
	go func() { _ = server.Serve(listener) }()

	bridge := &Bridge{serverURL: "http://" + listener.Addr().String()}
	connection, err := bridge.dialGRPC()
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })

	_, err = pb.NewEngramServiceClient(connection).SyncProjectState(context.Background(), &pb.SyncProjectStateRequest{})
	require.NoError(t, err)

	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))
	require.True(t, serverEventsContainsMetric(collected, "rpc.client.call.duration"), "persistent event bridge calls must emit the ready-made otelgrpc client metric")
}

func serverEventsContainsMetric(collected metricdata.ResourceMetrics, name string) bool {
	for _, scope := range collected.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name == name {
				return true
			}
		}
	}
	return false
}
