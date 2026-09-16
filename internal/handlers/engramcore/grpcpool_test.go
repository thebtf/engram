package engramcore

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestSafeRemoteURL_RemovesUserinfoAndDropsMalformedRemote(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want string
	}{
		{raw: "https://fixture-user:fixture-credential@example.invalid/acme/identity.git", want: "https://example.invalid/acme/identity.git"},
		{raw: "//fixture-user:fixture-credential@example.invalid/%zz", want: "[invalid remote URL]"},
	} {
		if got := safeRemoteURL(tt.raw); got != tt.want || strings.Contains(got, "fixture-credential") {
			t.Fatal("credential-bearing remote was not sanitized")
		}
	}
}

type grpcAuthFixtureServer interface {
	unary(context.Context, *emptypb.Empty) (*emptypb.Empty, error)
	stage(grpc.ServerStream) error
}

type grpcAuthFixture struct {
	mu       sync.Mutex
	unaryMD  metadata.MD
	streamMD metadata.MD
}

func (f *grpcAuthFixture) unary(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unaryMD, _ = metadata.FromIncomingContext(ctx)
	return &emptypb.Empty{}, nil
}

func (f *grpcAuthFixture) stage(stream grpc.ServerStream) error {
	if err := stream.RecvMsg(&emptypb.Empty{}); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamMD, _ = metadata.FromIncomingContext(stream.Context())
	return stream.SendMsg(&emptypb.Empty{})
}

func (f *grpcAuthFixture) metadata() (metadata.MD, metadata.MD) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unaryMD.Copy(), f.streamMD.Copy()
}

func TestDialGRPC_InjectsAuthorizationForUnaryAndClientStreamingRPCs(t *testing.T) {
	fixture := startGRPCAuthFixture(t)
	token := strings.Repeat("x", 32)
	conn := dialFixture(t, fixture.listener.Addr().String(), token)
	ctx := metadata.AppendToOutgoingContext(
		context.Background(),
		"x-fixture", "preserved",
		"authorization", strings.Repeat("y", 32),
	)

	if err := conn.Invoke(ctx, "/fixture.Auth/Unary", &emptypb.Empty{}, &emptypb.Empty{}); err != nil {
		t.Fatalf("unary RPC: %v", err)
	}
	stream, err := conn.NewStream(ctx, &grpc.StreamDesc{StreamName: "Stage", ClientStreams: true, ServerStreams: true}, "/fixture.Auth/Stage")
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if err := stream.SendMsg(&emptypb.Empty{}); err != nil {
		t.Fatalf("send stream message: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close stream send: %v", err)
	}
	if err := stream.RecvMsg(&emptypb.Empty{}); err != nil {
		t.Fatalf("receive stream message: %v", err)
	}

	unaryMD, streamMD := fixture.fixture.metadata()
	wantAuthorization := "Bearer " + token
	assertCredentialMetadata(t, unaryMD, wantAuthorization)
	assertCredentialMetadata(t, streamMD, wantAuthorization)
	for _, got := range []metadata.MD{unaryMD, streamMD} {
		if values := got.Get("x-fixture"); len(values) != 1 || values[0] != "preserved" {
			t.Fatal("existing outgoing metadata was not preserved")
		}
	}
}

func TestDialGRPC_LeavesUnaryAndClientStreamingRPCsUnauthenticatedWhenTokenEmpty(t *testing.T) {
	fixture := startGRPCAuthFixture(t)
	conn := dialFixture(t, fixture.listener.Addr().String(), "")

	if err := conn.Invoke(context.Background(), "/fixture.Auth/Unary", &emptypb.Empty{}, &emptypb.Empty{}); err != nil {
		t.Fatalf("unary RPC: %v", err)
	}
	stream, err := conn.NewStream(context.Background(), &grpc.StreamDesc{StreamName: "Stage", ClientStreams: true, ServerStreams: true}, "/fixture.Auth/Stage")
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if err := stream.SendMsg(&emptypb.Empty{}); err != nil {
		t.Fatalf("send stream message: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close stream send: %v", err)
	}
	if err := stream.RecvMsg(&emptypb.Empty{}); err != nil {
		t.Fatalf("receive stream message: %v", err)
	}

	unaryMD, streamMD := fixture.fixture.metadata()
	assertCredentialMetadata(t, unaryMD, "")
	assertCredentialMetadata(t, streamMD, "")
}

func assertCredentialMetadata(t *testing.T, values metadata.MD, want string) {
	t.Helper()
	got := values.Get("authorization")
	if want == "" {
		if len(got) != 0 {
			t.Fatal("unexpected authorization metadata")
		}
		return
	}
	if len(got) != 1 || got[0] != want {
		t.Fatal("authorization metadata did not contain exactly the expected bearer")
	}
}

type grpcAuthFixtureListener struct {
	listener net.Listener
	server   *grpc.Server
	fixture  *grpcAuthFixture
}

func startGRPCAuthFixture(t *testing.T) grpcAuthFixtureListener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fixture := &grpcAuthFixture{}
	server := grpc.NewServer()
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "fixture.Auth",
		HandlerType: (*grpcAuthFixtureServer)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Unary",
			Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
				request := &emptypb.Empty{}
				if err := dec(request); err != nil {
					return nil, err
				}
				handler := func(ctx context.Context, req any) (any, error) {
					return srv.(grpcAuthFixtureServer).unary(ctx, req.(*emptypb.Empty))
				}
				if interceptor == nil {
					return handler(ctx, request)
				}
				return interceptor(ctx, request, &grpc.UnaryServerInfo{Server: srv, FullMethod: "/fixture.Auth/Unary"}, handler)
			},
		}},
		Streams: []grpc.StreamDesc{{
			StreamName:    "Stage",
			Handler:       func(srv any, stream grpc.ServerStream) error { return srv.(grpcAuthFixtureServer).stage(stream) },
			ServerStreams: true,
			ClientStreams: true,
		}},
	}, fixture)
	go server.Serve(listener)
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return grpcAuthFixtureListener{listener: listener, server: server, fixture: fixture}
}

func dialFixture(t *testing.T, addr, token string) *grpc.ClientConn {
	t.Helper()
	conn, err := dialGRPC(addr, "http://"+addr, token)
	if err != nil {
		t.Fatalf("dial fixture: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
