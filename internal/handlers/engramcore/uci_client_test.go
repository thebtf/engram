package engramcore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auditcontext"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	uciClientTestSpaceID       = "11111111-1111-4111-8111-111111111111"
	uciClientTestSourceID      = "22222222-2222-4222-8222-222222222222"
	uciClientTestCheckoutAID   = "33333333-3333-4333-8333-333333333333"
	uciClientTestCheckoutBID   = "77777777-7777-4777-8777-777777777777"
	uciClientTestViewAID       = "44444444-4444-4444-8444-444444444444"
	uciClientTestViewBID       = "88888888-8888-4888-8888-888888888888"
	uciClientTestProfileID     = "55555555-5555-4555-8555-555555555555"
	uciClientTestIncarnationA  = "66666666-6666-4666-8666-666666666666"
	uciClientTestIncarnationB  = "99999999-9999-4999-8999-999999999999"
	uciClientTestDigest        = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	uciClientTestServerBuildID = "server-build"
	uciClientTestLeaseEpoch    = uint64(7)
)

func TestUCIClientForwardsBoundScopeAndBuild(t *testing.T) {
	rpc := &uciClientRPCFake{stageStream: &uciClientStageStream{}}
	client := newUCIClient(rpc)
	ctx := context.Background()
	reference := uciClientTestContextA()

	binding, err := client.Bind(ctx, &pb.BindCodeContextRequest{
		ClientSessionId:  "client-a",
		RequestedContext: reference,
	})
	require.NoError(t, err)
	require.Equal(t, "context-handle-client-a", binding.GetContextHandle())
	requireUCIClientContextEqual(t, reference, binding.GetContext())

	scope := uciClientTestScopeA()
	begin, err := client.Begin(ctx, uciClientTestBeginRequest(scope, "daemon-a", "build-key-a"))
	require.NoError(t, err)
	requireUCIClientScopeEqual(t, scope, begin.GetScope())
	require.Equal(t, uciClientTestServerBuildID, begin.GetBuildId())
	require.Equal(t, uciClientTestLeaseEpoch, begin.GetLeaseEpoch())

	frames := []*pb.StageCodeIndexFrame{
		uciClientTestStageFrame(scope, begin.GetBuildId(), begin.GetLeaseEpoch(), 0),
		uciClientTestStageFrame(scope, begin.GetBuildId(), begin.GetLeaseEpoch(), 1),
	}
	staged, err := client.Stage(ctx, frames)
	require.NoError(t, err)
	require.Equal(t, begin.GetBuildId(), staged.GetBuildId())
	require.Equal(t, uint64(1), staged.GetAcceptedSequence())
	require.Equal(t, uint64(2), staged.GetAcceptedPartCount())
	require.Equal(t, uciClientTestDigest, staged.GetPartDigest())

	finalize := uciClientTestFinalizeRequest(scope, begin.GetBuildId(), begin.GetLeaseEpoch(), reference)
	published, err := client.Finalize(ctx, finalize)
	require.NoError(t, err)
	requireUCIClientContextEqual(t, reference, published.GetPublishedContext())
	require.Equal(t, begin.GetBuildId(), published.GetBuildId())
	require.Equal(t, begin.GetLeaseEpoch(), published.GetLeaseEpoch())

	queried, err := client.Query(ctx, uciClientTestQueryRequest(reference))
	require.NoError(t, err)
	requireUCIClientContextEqual(t, reference, queried.GetContext())
	require.JSONEq(t, `{"status":"ok"}`, string(queried.GetResponseJson()))

	explored, err := client.Explore(ctx, uciClientTestExploreRequest(reference))
	require.NoError(t, err)
	requireUCIClientContextEqual(t, reference, explored.GetContext())
	require.JSONEq(t, `{"status":"ok"}`, string(explored.GetResponseJson()))

	require.Len(t, rpc.bindRequests, 1)
	require.Equal(t, "client-a", rpc.bindRequests[0].GetClientSessionId())
	requireUCIClientContextEqual(t, reference, rpc.bindRequests[0].GetRequestedContext())
	require.Len(t, rpc.beginRequests, 1)
	requireUCIClientScopeEqual(t, scope, rpc.beginRequests[0].GetScope())
	require.Equal(t, "daemon-a", rpc.beginRequests[0].GetOwnerInstance())
	require.Len(t, rpc.stageStream.sent, len(frames))
	for index, frame := range rpc.stageStream.sent {
		requireUCIClientScopeEqual(t, scope, frame.GetScope())
		require.Equal(t, begin.GetBuildId(), frame.GetBuildId(), "frame %d build", index)
		require.Equal(t, begin.GetLeaseEpoch(), frame.GetLeaseEpoch(), "frame %d lease", index)
		require.Equal(t, uint64(index), frame.GetSequence())
	}
	require.Len(t, rpc.finalizeRequests, 1)
	requireUCIClientScopeEqual(t, scope, rpc.finalizeRequests[0].GetScope())
	require.Equal(t, begin.GetBuildId(), rpc.finalizeRequests[0].GetBuildId())
	require.Equal(t, begin.GetLeaseEpoch(), rpc.finalizeRequests[0].GetLeaseEpoch())
	require.Len(t, rpc.queryRequests, 1)
	requireUCIClientContextEqual(t, reference, rpc.queryRequests[0].GetContext())
	require.Len(t, rpc.exploreRequests, 1)
	requireUCIClientContextEqual(t, reference, rpc.exploreRequests[0].GetContext())
}

func TestUCIClientPropagatesSourceSessionMetadata(t *testing.T) {
	rpc := &uciClientRPCFake{bind: func(ctx context.Context, request *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
		outgoing, ok := metadata.FromOutgoingContext(ctx)
		require.True(t, ok)
		require.Equal(t, []string{"client-a"}, outgoing.Get(auditcontext.SourceSessionMetadataKey))
		return &pb.BindCodeContextResponse{
			ContextHandle: "context-handle-client-a",
			Context:       request.GetRequestedContext(),
		}, nil
	}}
	ctx := auditcontext.WithSourceSession(context.Background(), "client-a")
	_, err := newUCIClient(rpc).Bind(ctx, &pb.BindCodeContextRequest{
		ClientSessionId:  "client-a",
		RequestedContext: uciClientTestContextA(),
	})
	require.NoError(t, err)
}

func TestUCIClientKeepsSessionsAndWorktreesIndependent(t *testing.T) {
	rpc := &uciClientRPCFake{stageStream: &uciClientStageStream{}}
	client := newUCIClient(rpc)
	ctx := context.Background()
	contextA := uciClientTestContextA()
	contextB := uciClientTestContextB()
	scopeA := uciClientTestScopeA()
	scopeB := uciClientTestScopeB()

	_, err := client.Bind(ctx, &pb.BindCodeContextRequest{ClientSessionId: "client-a", RequestedContext: contextA})
	require.NoError(t, err)
	_, err = client.Bind(ctx, &pb.BindCodeContextRequest{ClientSessionId: "client-b", RequestedContext: contextB})
	require.NoError(t, err)

	_, err = client.Begin(ctx, uciClientTestBeginRequest(scopeA, "daemon-a", "build-key-a"))
	require.NoError(t, err)
	_, err = client.Begin(ctx, uciClientTestBeginRequest(scopeB, "daemon-b", "build-key-b"))
	require.NoError(t, err)

	queryA, err := client.Query(ctx, uciClientTestQueryRequest(contextA))
	require.NoError(t, err)
	requireUCIClientContextEqual(t, contextA, queryA.GetContext())
	exploreB, err := client.Explore(ctx, uciClientTestExploreRequest(contextB))
	require.NoError(t, err)
	requireUCIClientContextEqual(t, contextB, exploreB.GetContext())

	require.Len(t, rpc.bindRequests, 2)
	require.Equal(t, "client-a", rpc.bindRequests[0].GetClientSessionId())
	requireUCIClientContextEqual(t, contextA, rpc.bindRequests[0].GetRequestedContext())
	require.Equal(t, "client-b", rpc.bindRequests[1].GetClientSessionId())
	requireUCIClientContextEqual(t, contextB, rpc.bindRequests[1].GetRequestedContext())
	require.Len(t, rpc.beginRequests, 2)
	requireUCIClientScopeEqual(t, scopeA, rpc.beginRequests[0].GetScope())
	requireUCIClientScopeEqual(t, scopeB, rpc.beginRequests[1].GetScope())
	require.Len(t, rpc.queryRequests, 1)
	requireUCIClientContextEqual(t, contextA, rpc.queryRequests[0].GetContext())
	require.Len(t, rpc.exploreRequests, 1)
	requireUCIClientContextEqual(t, contextB, rpc.exploreRequests[0].GetContext())
}

func TestUCIClientRejectsInconsistentStageFrames(t *testing.T) {
	for _, test := range []struct {
		name   string
		frames func() []*pb.StageCodeIndexFrame
	}{
		{name: "empty", frames: func() []*pb.StageCodeIndexFrame { return nil }},
		{name: "nil frame", frames: func() []*pb.StageCodeIndexFrame { return []*pb.StageCodeIndexFrame{nil} }},
		{name: "first sequence is not zero", frames: func() []*pb.StageCodeIndexFrame {
			return []*pb.StageCodeIndexFrame{uciClientTestStageFrame(uciClientTestScopeA(), uciClientTestServerBuildID, uciClientTestLeaseEpoch, 1)}
		}},
		{name: "sequence gap", frames: func() []*pb.StageCodeIndexFrame {
			scope := uciClientTestScopeA()
			return []*pb.StageCodeIndexFrame{
				uciClientTestStageFrame(scope, uciClientTestServerBuildID, uciClientTestLeaseEpoch, 0),
				uciClientTestStageFrame(scope, uciClientTestServerBuildID, uciClientTestLeaseEpoch, 2),
			}
		}},
		{name: "scope changes", frames: func() []*pb.StageCodeIndexFrame {
			return []*pb.StageCodeIndexFrame{
				uciClientTestStageFrame(uciClientTestScopeA(), uciClientTestServerBuildID, uciClientTestLeaseEpoch, 0),
				uciClientTestStageFrame(uciClientTestScopeB(), uciClientTestServerBuildID, uciClientTestLeaseEpoch, 1),
			}
		}},
		{name: "build changes", frames: func() []*pb.StageCodeIndexFrame {
			scope := uciClientTestScopeA()
			return []*pb.StageCodeIndexFrame{
				uciClientTestStageFrame(scope, uciClientTestServerBuildID, uciClientTestLeaseEpoch, 0),
				uciClientTestStageFrame(scope, "other-build", uciClientTestLeaseEpoch, 1),
			}
		}},
		{name: "lease changes", frames: func() []*pb.StageCodeIndexFrame {
			scope := uciClientTestScopeA()
			return []*pb.StageCodeIndexFrame{
				uciClientTestStageFrame(scope, uciClientTestServerBuildID, uciClientTestLeaseEpoch, 0),
				uciClientTestStageFrame(scope, uciClientTestServerBuildID, uciClientTestLeaseEpoch+1, 1),
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rpc := &uciClientRPCFake{stageStream: &uciClientStageStream{}}

			_, err := newUCIClient(rpc).Stage(context.Background(), test.frames())
			require.Error(t, err)
			require.Zero(t, rpc.stageOpenCalls, "invalid frames must not open the generated client stream")
		})
	}
}

func TestUCIClientPropagatesCancellation(t *testing.T) {
	for _, test := range []struct {
		name   string
		invoke func(*uciClient, context.Context) error
	}{
		{name: "Bind", invoke: func(client *uciClient, ctx context.Context) error {
			_, err := client.Bind(ctx, &pb.BindCodeContextRequest{ClientSessionId: "client-a", RequestedContext: uciClientTestContextA()})
			return err
		}},
		{name: "Begin", invoke: func(client *uciClient, ctx context.Context) error {
			_, err := client.Begin(ctx, uciClientTestBeginRequest(uciClientTestScopeA(), "daemon-a", "build-key-a"))
			return err
		}},
		{name: "Stage", invoke: func(client *uciClient, ctx context.Context) error {
			_, err := client.Stage(ctx, []*pb.StageCodeIndexFrame{uciClientTestStageFrame(uciClientTestScopeA(), uciClientTestServerBuildID, uciClientTestLeaseEpoch, 0)})
			return err
		}},
		{name: "Finalize", invoke: func(client *uciClient, ctx context.Context) error {
			_, err := client.Finalize(ctx, uciClientTestFinalizeRequest(uciClientTestScopeA(), uciClientTestServerBuildID, uciClientTestLeaseEpoch, uciClientTestContextA()))
			return err
		}},
		{name: "Query", invoke: func(client *uciClient, ctx context.Context) error {
			_, err := client.Query(ctx, uciClientTestQueryRequest(uciClientTestContextA()))
			return err
		}},
		{name: "Explore", invoke: func(client *uciClient, ctx context.Context) error {
			_, err := client.Explore(ctx, uciClientTestExploreRequest(uciClientTestContextA()))
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			rpc := &uciClientRPCFake{stageStream: &uciClientStageStream{}}

			err := test.invoke(newUCIClient(rpc), ctx)
			require.ErrorIs(t, err, context.Canceled)
			require.Zero(t, rpc.callCount(), "a canceled request must not reach the generated client")
		})
	}
}

func TestUCIClientRejectsNilOrMalformedResponses(t *testing.T) {
	t.Run("Bind", func(t *testing.T) {
		rpc := &uciClientRPCFake{bind: func(context.Context, *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
			return nil, nil
		}}
		_, err := newUCIClient(rpc).Bind(context.Background(), &pb.BindCodeContextRequest{ClientSessionId: "client-a", RequestedContext: uciClientTestContextA()})
		require.Error(t, err)
	})

	t.Run("Begin", func(t *testing.T) {
		rpc := &uciClientRPCFake{begin: func(_ context.Context, request *pb.BeginCodeIndexRequest) (*pb.BeginCodeIndexResponse, error) {
			return &pb.BeginCodeIndexResponse{Scope: request.GetScope(), BuildId: uciClientTestServerBuildID}, nil
		}}
		_, err := newUCIClient(rpc).Begin(context.Background(), uciClientTestBeginRequest(uciClientTestScopeA(), "daemon-a", "build-key-a"))
		require.Error(t, err)
	})

	t.Run("Stage", func(t *testing.T) {
		rpc := &uciClientRPCFake{stageStream: &uciClientStageStream{close: func([]*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error) {
			return nil, nil
		}}}
		_, err := newUCIClient(rpc).Stage(context.Background(), []*pb.StageCodeIndexFrame{uciClientTestStageFrame(uciClientTestScopeA(), uciClientTestServerBuildID, uciClientTestLeaseEpoch, 0)})
		require.Error(t, err)
	})

	t.Run("Finalize", func(t *testing.T) {
		rpc := &uciClientRPCFake{finalize: func(_ context.Context, request *pb.FinalizeCodeIndexRequest) (*pb.FinalizeCodeIndexResponse, error) {
			return &pb.FinalizeCodeIndexResponse{
				BuildId:                    request.GetBuildId(),
				LeaseEpoch:                 request.GetLeaseEpoch(),
				AcceptedFilesystemSequence: request.GetObservedFilesystemSequence(),
			}, nil
		}}
		_, err := newUCIClient(rpc).Finalize(context.Background(), uciClientTestFinalizeRequest(uciClientTestScopeA(), uciClientTestServerBuildID, uciClientTestLeaseEpoch, uciClientTestContextA()))
		require.Error(t, err)
	})

	t.Run("Query", func(t *testing.T) {
		rpc := &uciClientRPCFake{query: func(_ context.Context, request *pb.QueryCodeRequest) (*pb.QueryCodeResponse, error) {
			return &pb.QueryCodeResponse{Context: request.GetContext(), ResponseJson: []byte("[]")}, nil
		}}
		_, err := newUCIClient(rpc).Query(context.Background(), uciClientTestQueryRequest(uciClientTestContextA()))
		require.Error(t, err)
	})

	t.Run("Explore", func(t *testing.T) {
		rpc := &uciClientRPCFake{explore: func(_ context.Context, _ *pb.ExploreCodeRequest) (*pb.ExploreCodeResponse, error) {
			return &pb.ExploreCodeResponse{ResponseJson: []byte(`{"status":"ok"}`)}, nil
		}}
		_, err := newUCIClient(rpc).Explore(context.Background(), uciClientTestExploreRequest(uciClientTestContextA()))
		require.Error(t, err)
	})
}

func TestUCIClientRejectsMismatchedResponseBindings(t *testing.T) {
	t.Run("Bind", func(t *testing.T) {
		rpc := &uciClientRPCFake{bind: func(_ context.Context, request *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
			responseContext := proto.Clone(request.GetRequestedContext()).(*pb.ContextRef)
			responseContext.Generation++
			return &pb.BindCodeContextResponse{ContextHandle: "context-handle-client-a", Context: responseContext}, nil
		}}
		_, err := newUCIClient(rpc).Bind(context.Background(), &pb.BindCodeContextRequest{ClientSessionId: "client-a", RequestedContext: uciClientTestContextA()})
		require.Error(t, err)
	})

	t.Run("Begin", func(t *testing.T) {
		rpc := &uciClientRPCFake{begin: func(context.Context, *pb.BeginCodeIndexRequest) (*pb.BeginCodeIndexResponse, error) {
			return &pb.BeginCodeIndexResponse{
				Scope:          uciClientTestScopeB(),
				BuildId:        uciClientTestServerBuildID,
				LeaseEpoch:     uciClientTestLeaseEpoch,
				LeaseExpiresAt: timestamppb.New(time.Now().Add(time.Hour)),
			}, nil
		}}
		_, err := newUCIClient(rpc).Begin(context.Background(), uciClientTestBeginRequest(uciClientTestScopeA(), "daemon-a", "build-key-a"))
		require.Error(t, err)
	})

	t.Run("Stage", func(t *testing.T) {
		rpc := &uciClientRPCFake{stageStream: &uciClientStageStream{close: func(frames []*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error) {
			return &pb.StageCodeIndexResponse{
				BuildId:           "other-build",
				AcceptedSequence:  frames[len(frames)-1].GetSequence(),
				AcceptedPartCount: uint64(len(frames)),
				PartDigest:        uciClientTestDigest,
			}, nil
		}}}
		_, err := newUCIClient(rpc).Stage(context.Background(), []*pb.StageCodeIndexFrame{uciClientTestStageFrame(uciClientTestScopeA(), uciClientTestServerBuildID, uciClientTestLeaseEpoch, 0)})
		require.Error(t, err)
	})

	t.Run("Finalize", func(t *testing.T) {
		rpc := &uciClientRPCFake{finalize: func(_ context.Context, request *pb.FinalizeCodeIndexRequest) (*pb.FinalizeCodeIndexResponse, error) {
			return &pb.FinalizeCodeIndexResponse{
				PublishedContext:           request.GetExpectedParent(),
				BuildId:                    request.GetBuildId(),
				LeaseEpoch:                 request.GetLeaseEpoch() + 1,
				AcceptedFilesystemSequence: request.GetObservedFilesystemSequence(),
			}, nil
		}}
		_, err := newUCIClient(rpc).Finalize(context.Background(), uciClientTestFinalizeRequest(uciClientTestScopeA(), uciClientTestServerBuildID, uciClientTestLeaseEpoch, uciClientTestContextA()))
		require.Error(t, err)
	})

	t.Run("Query", func(t *testing.T) {
		rpc := &uciClientRPCFake{query: func(context.Context, *pb.QueryCodeRequest) (*pb.QueryCodeResponse, error) {
			return &pb.QueryCodeResponse{Context: uciClientTestContextB(), ResponseJson: []byte(`{"status":"ok"}`)}, nil
		}}
		_, err := newUCIClient(rpc).Query(context.Background(), uciClientTestQueryRequest(uciClientTestContextA()))
		require.Error(t, err)
	})

	t.Run("Explore", func(t *testing.T) {
		rpc := &uciClientRPCFake{explore: func(context.Context, *pb.ExploreCodeRequest) (*pb.ExploreCodeResponse, error) {
			return &pb.ExploreCodeResponse{Context: uciClientTestContextB(), ResponseJson: []byte(`{"status":"ok"}`)}, nil
		}}
		_, err := newUCIClient(rpc).Explore(context.Background(), uciClientTestExploreRequest(uciClientTestContextA()))
		require.Error(t, err)
	})
}

type uciClientRPCFake struct {
	bindRequests     []*pb.BindCodeContextRequest
	beginRequests    []*pb.BeginCodeIndexRequest
	stageOpenCalls   int
	stageStream      *uciClientStageStream
	finalizeRequests []*pb.FinalizeCodeIndexRequest
	queryRequests    []*pb.QueryCodeRequest
	exploreRequests  []*pb.ExploreCodeRequest

	bind     func(context.Context, *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error)
	begin    func(context.Context, *pb.BeginCodeIndexRequest) (*pb.BeginCodeIndexResponse, error)
	finalize func(context.Context, *pb.FinalizeCodeIndexRequest) (*pb.FinalizeCodeIndexResponse, error)
	query    func(context.Context, *pb.QueryCodeRequest) (*pb.QueryCodeResponse, error)
	explore  func(context.Context, *pb.ExploreCodeRequest) (*pb.ExploreCodeResponse, error)
}

func (fake *uciClientRPCFake) BindCodeContext(ctx context.Context, request *pb.BindCodeContextRequest, _ ...grpc.CallOption) (*pb.BindCodeContextResponse, error) {
	fake.bindRequests = append(fake.bindRequests, request)
	if fake.bind != nil {
		return fake.bind(ctx, request)
	}
	return &pb.BindCodeContextResponse{
		ContextHandle: "context-handle-" + request.GetClientSessionId(),
		Context:       request.GetRequestedContext(),
	}, nil
}

func (fake *uciClientRPCFake) BeginCodeIndex(ctx context.Context, request *pb.BeginCodeIndexRequest, _ ...grpc.CallOption) (*pb.BeginCodeIndexResponse, error) {
	fake.beginRequests = append(fake.beginRequests, request)
	if fake.begin != nil {
		return fake.begin(ctx, request)
	}
	return &pb.BeginCodeIndexResponse{
		Scope:          request.GetScope(),
		BuildId:        uciClientTestServerBuildID,
		LeaseEpoch:     uciClientTestLeaseEpoch,
		LeaseExpiresAt: timestamppb.New(time.Now().Add(time.Hour)),
	}, nil
}

func (fake *uciClientRPCFake) StageCodeIndex(_ context.Context, _ ...grpc.CallOption) (grpc.ClientStreamingClient[pb.StageCodeIndexFrame, pb.StageCodeIndexResponse], error) {
	fake.stageOpenCalls++
	if fake.stageStream == nil {
		fake.stageStream = &uciClientStageStream{}
	}
	return fake.stageStream, nil
}

func (fake *uciClientRPCFake) FinalizeCodeIndex(ctx context.Context, request *pb.FinalizeCodeIndexRequest, _ ...grpc.CallOption) (*pb.FinalizeCodeIndexResponse, error) {
	fake.finalizeRequests = append(fake.finalizeRequests, request)
	if fake.finalize != nil {
		return fake.finalize(ctx, request)
	}
	return &pb.FinalizeCodeIndexResponse{
		PublishedContext:           request.GetExpectedParent(),
		BuildId:                    request.GetBuildId(),
		LeaseEpoch:                 request.GetLeaseEpoch(),
		AcceptedFilesystemSequence: request.GetObservedFilesystemSequence(),
	}, nil
}

func (fake *uciClientRPCFake) QueryCode(ctx context.Context, request *pb.QueryCodeRequest, _ ...grpc.CallOption) (*pb.QueryCodeResponse, error) {
	fake.queryRequests = append(fake.queryRequests, request)
	if fake.query != nil {
		return fake.query(ctx, request)
	}
	return &pb.QueryCodeResponse{Context: request.GetContext(), ResponseJson: []byte(`{"status":"ok"}`)}, nil
}

func (fake *uciClientRPCFake) ExploreCode(ctx context.Context, request *pb.ExploreCodeRequest, _ ...grpc.CallOption) (*pb.ExploreCodeResponse, error) {
	fake.exploreRequests = append(fake.exploreRequests, request)
	if fake.explore != nil {
		return fake.explore(ctx, request)
	}
	return &pb.ExploreCodeResponse{Context: request.GetContext(), ResponseJson: []byte(`{"status":"ok"}`)}, nil
}

func (fake *uciClientRPCFake) callCount() int {
	return len(fake.bindRequests) + len(fake.beginRequests) + fake.stageOpenCalls + len(fake.finalizeRequests) + len(fake.queryRequests) + len(fake.exploreRequests)
}

var _ uciClientRPC = (*uciClientRPCFake)(nil)

type uciClientStageStream struct {
	grpc.ClientStream
	sent  []*pb.StageCodeIndexFrame
	close func([]*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error)
}

func (stream *uciClientStageStream) Send(frame *pb.StageCodeIndexFrame) error {
	stream.sent = append(stream.sent, frame)
	return nil
}

func (stream *uciClientStageStream) CloseAndRecv() (*pb.StageCodeIndexResponse, error) {
	if stream.close != nil {
		return stream.close(stream.sent)
	}
	if len(stream.sent) == 0 {
		return nil, nil
	}
	return &pb.StageCodeIndexResponse{
		BuildId:           stream.sent[0].GetBuildId(),
		AcceptedSequence:  stream.sent[len(stream.sent)-1].GetSequence(),
		AcceptedPartCount: uint64(len(stream.sent)),
		PartDigest:        uciClientTestDigest,
	}, nil
}

func uciClientTestContextA() *pb.ContextRef {
	return uciClientTestContext(uciClientTestCheckoutAID, uciClientTestViewAID)
}

func uciClientTestContextB() *pb.ContextRef {
	return uciClientTestContext(uciClientTestCheckoutBID, uciClientTestViewBID)
}

func uciClientTestContext(checkoutID, viewID string) *pb.ContextRef {
	spaceID := uciClientTestSpaceID
	return &pb.ContextRef{
		SpaceId:           &spaceID,
		SourceId:          uciClientTestSourceID,
		CheckoutId:        checkoutID,
		ViewId:            viewID,
		Generation:        1,
		AnalysisProfileId: uciClientTestProfileID,
	}
}

func uciClientTestScopeA() *pb.CodeIndexScope {
	return uciClientTestScope(uciClientTestCheckoutAID, uciClientTestIncarnationA)
}

func uciClientTestScopeB() *pb.CodeIndexScope {
	return uciClientTestScope(uciClientTestCheckoutBID, uciClientTestIncarnationB)
}

func uciClientTestScope(checkoutID, incarnationID string) *pb.CodeIndexScope {
	return &pb.CodeIndexScope{
		SourceId:          uciClientTestSourceID,
		CheckoutId:        checkoutID,
		IncarnationId:     incarnationID,
		AnalysisProfileId: uciClientTestProfileID,
	}
}

func uciClientTestBeginRequest(scope *pb.CodeIndexScope, ownerInstance, buildKey string) *pb.BeginCodeIndexRequest {
	return &pb.BeginCodeIndexRequest{
		Scope:         scope,
		OwnerInstance: ownerInstance,
		BuildKey:      buildKey,
		ManifestMode:  "full",
		JobKind:       "initial_index",
	}
}

func uciClientTestStageFrame(scope *pb.CodeIndexScope, buildID string, leaseEpoch, sequence uint64) *pb.StageCodeIndexFrame {
	return &pb.StageCodeIndexFrame{
		Scope:         scope,
		BuildId:       buildID,
		LeaseEpoch:    leaseEpoch,
		Sequence:      sequence,
		PayloadDigest: uciClientTestDigest,
		Payload:       []byte("payload"),
	}
}

func uciClientTestFinalizeRequest(scope *pb.CodeIndexScope, buildID string, leaseEpoch uint64, expectedParent *pb.ContextRef) *pb.FinalizeCodeIndexRequest {
	startedAt := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	return &pb.FinalizeCodeIndexRequest{
		Scope:                      scope,
		BuildId:                    buildID,
		LeaseEpoch:                 leaseEpoch,
		ExpectedParent:             expectedParent,
		ManifestPartCount:          2,
		PartsDigest:                uciClientTestDigest,
		ManifestEntryCount:         3,
		ManifestDigest:             uciClientTestDigest,
		EdgeCount:                  4,
		EdgesDigest:                uciClientTestDigest,
		ObservedFilesystemSequence: 9,
		ScanStartedAt:              timestamppb.New(startedAt),
		ScanCompletedAt:            timestamppb.New(startedAt.Add(time.Second)),
		ScanOutcome:                "complete",
		CompleteCensus:             true,
		CoverageJson:               []byte(`{"structural":"complete"}`),
	}
}

func uciClientTestQueryRequest(reference *pb.ContextRef) *pb.QueryCodeRequest {
	return &pb.QueryCodeRequest{
		Context:    reference,
		Query:      "needle",
		MaxResults: 10,
		MaxBytes:   1024,
		DeadlineMs: 1_000,
	}
}

func uciClientTestExploreRequest(reference *pb.ContextRef) *pb.ExploreCodeRequest {
	return &pb.ExploreCodeRequest{
		Context:         reference,
		Operation:       "impact",
		Subject:         "needle",
		MaxDepth:        4,
		MaxVisitedNodes: 100,
		MaxResultNodes:  10,
		MaxResultEdges:  20,
		DeadlineMs:      1_000,
	}
}

func requireUCIClientContextEqual(t *testing.T, want, got *pb.ContextRef) {
	t.Helper()
	require.True(t, proto.Equal(want, got), "context got=%v want=%v", got, want)
}

func requireUCIClientScopeEqual(t *testing.T, want, got *pb.CodeIndexScope) {
	t.Helper()
	require.True(t, proto.Equal(want, got), "scope got=%v want=%v", got, want)
}
