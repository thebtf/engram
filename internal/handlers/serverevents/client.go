package serverevents

import (
	"context"

	"google.golang.org/grpc"

	pb "github.com/thebtf/engram/proto/engram/v1"
)

// EventsClient is the minimal gRPC surface required by the bridge.
// It is extracted as an interface so that tests can inject a fake
// without needing a real network connection.
//
// The production implementation wraps [pb.EngramServiceClient].
// The test implementation is an in-process bufconn-backed fake.
type EventsClient interface {
	// ProjectEvents opens a server-streaming RPC that pushes project
	// lifecycle events to the caller. The stream stays open for the
	// daemon's lifetime. ctx cancellation terminates the stream cleanly.
	ProjectEvents(ctx context.Context, req *pb.ProjectEventsRequest, opts ...grpc.CallOption) (pb.EngramService_ProjectEventsClient, error)

	// SyncProjectState performs a unary reconciliation call. The daemon
	// sends its local project ID snapshot; the server returns any IDs
	// that have been removed or are stale.
	SyncProjectState(ctx context.Context, req *pb.SyncProjectStateRequest, opts ...grpc.CallOption) (*pb.SyncProjectStateResponse, error)
}

// grpcEventsClient wraps the generated [pb.EngramServiceClient] and
// satisfies [EventsClient]. It is the production implementation used by the
// bridge when a real gRPC connection is available.
type grpcEventsClient struct {
	inner pb.EngramServiceClient
}

// newGRPCEventsClient returns an [EventsClient] backed by the given connection.
func newGRPCEventsClient(conn grpc.ClientConnInterface) EventsClient {
	return &grpcEventsClient{inner: pb.NewEngramServiceClient(conn)}
}

func (c *grpcEventsClient) ProjectEvents(ctx context.Context, req *pb.ProjectEventsRequest, opts ...grpc.CallOption) (pb.EngramService_ProjectEventsClient, error) {
	return c.inner.ProjectEvents(ctx, req, opts...)
}

func (c *grpcEventsClient) SyncProjectState(ctx context.Context, req *pb.SyncProjectStateRequest, opts ...grpc.CallOption) (*pb.SyncProjectStateResponse, error) {
	return c.inner.SyncProjectState(ctx, req, opts...)
}
