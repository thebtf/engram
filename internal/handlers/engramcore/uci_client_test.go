package engramcore

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	uciClientTestSpaceID              = "11111111-1111-4111-8111-111111111111"
	uciClientTestSourceID             = "22222222-2222-4222-8222-222222222222"
	uciClientTestCheckoutAID          = "33333333-3333-4333-8333-333333333333"
	uciClientTestCheckoutBID          = "77777777-7777-4777-8777-777777777777"
	uciClientTestViewAID              = "44444444-4444-4444-8444-444444444444"
	uciClientTestViewBID              = "88888888-8888-4888-8888-888888888888"
	uciClientTestProfileID            = "55555555-5555-4555-8555-555555555555"
	uciClientTestIncarnationA         = "66666666-6666-4666-8666-666666666666"
	uciClientTestIncarnationB         = "99999999-9999-4999-8999-999999999999"
	uciClientTestFrameDigest          = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	uciClientTestAggregatePartsDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	uciClientTestHeadOID              = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	uciClientTestObjectFormat         = "sha1"
	uciClientTestRefLabel             = "main"
	uciClientTestServerBuildID        = "server-build"
	uciClientTestLocalRootID          = "local-root-a"
	uciClientTestWorkstationID        = "workstation-a"
	uciClientTestLeaseEpoch           = uint64(7)
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
	require.Equal(t, uciClientTestAggregatePartsDigest, staged.GetPartDigest())
	require.NotEqual(t, frames[0].GetPayloadDigest(), staged.GetPartDigest())

	finalize := uciClientTestFinalizeRequest(scope, begin.GetBuildId(), begin.GetLeaseEpoch(), reference, staged.GetPartDigest())
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
	require.Equal(t, staged.GetPartDigest(), rpc.finalizeRequests[0].GetPartsDigest())
	require.Equal(t, uciClientTestHeadOID, rpc.finalizeRequests[0].GetHeadOid())
	require.Equal(t, uciClientTestObjectFormat, rpc.finalizeRequests[0].GetObjectFormat())
	require.Equal(t, uciClientTestRefLabel, rpc.finalizeRequests[0].GetRefLabel())
	require.NotNil(t, rpc.finalizeRequests[0].Dirty)
	require.False(t, rpc.finalizeRequests[0].GetDirty())
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
		return uciClientTestBindResponse(request), nil
	}}
	ctx := auditcontext.WithUCITransportSession(context.Background(), "client-a")
	_, err := newUCIClient(rpc).Bind(ctx, &pb.BindCodeContextRequest{
		ClientSessionId:  "client-a",
		RequestedContext: uciClientTestContextA(),
	})
	require.NoError(t, err)
}

func TestUCIClientAcceptsCompleteNoViewHandleBinding(t *testing.T) {
	const contextHandle = "opaque-context-handle"
	rpc := &uciClientRPCFake{}

	bound, err := newUCIClient(rpc).Bind(context.Background(), &pb.BindCodeContextRequest{
		ClientSessionId: "client-a",
		ContextHandle:   contextHandle,
	})
	require.NoError(t, err)
	require.Equal(t, contextHandle, bound.GetContextHandle())
	require.Nil(t, bound.GetContext())
	requireUCIClientScopeEqual(t, uciClientTestScopeA(), bound.GetIndexScope())
	require.Equal(t, uciClientTestLocalRootID, bound.GetLocalRootId())
	require.Equal(t, uciClientTestWorkstationID, bound.GetWorkstationId())
}

func TestUCIClientForwardsUnboundBindRequest(t *testing.T) {
	rpc := &uciClientRPCFake{bind: func(_ context.Context, request *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
		require.Equal(t, "client-a", request.GetClientSessionId())
		require.Empty(t, request.GetContextHandle())
		require.Nil(t, request.GetRequestedContext())
		response := uciClientTestBindResponse(request)
		response.Context = uciClientTestContextA()
		response.IndexScope = uciClientTestScopeA()
		return response, nil
	}}

	bound, err := newUCIClient(rpc).Bind(context.Background(), &pb.BindCodeContextRequest{ClientSessionId: "client-a"})
	require.NoError(t, err)
	require.Equal(t, "context-handle-client-a", bound.GetContextHandle())
	requireUCIClientContextEqual(t, uciClientTestContextA(), bound.GetContext())
	require.Len(t, rpc.bindRequests, 1)
}

func TestUCIClientRejectsIncompleteOrMismatchedHandleBinding(t *testing.T) {
	const contextHandle = "opaque-context-handle"
	for _, test := range []struct {
		name   string
		mutate func(*pb.BindCodeContextResponse)
	}{
		{name: "different handle", mutate: func(response *pb.BindCodeContextResponse) { response.ContextHandle = "other-handle" }},
		{name: "missing scope", mutate: func(response *pb.BindCodeContextResponse) { response.IndexScope = nil }},
		{name: "missing local root", mutate: func(response *pb.BindCodeContextResponse) { response.LocalRootId = "" }},
		{name: "missing workstation", mutate: func(response *pb.BindCodeContextResponse) { response.WorkstationId = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			rpc := &uciClientRPCFake{bind: func(_ context.Context, request *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
				response := uciClientTestBindResponse(request)
				test.mutate(response)
				return response, nil
			}}

			_, err := newUCIClient(rpc).Bind(context.Background(), &pb.BindCodeContextRequest{
				ClientSessionId: "client-a",
				ContextHandle:   contextHandle,
			})
			require.Error(t, err)
		})
	}
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

func TestUCIClientValidatesFinalizeObservation(t *testing.T) {
	newRequest := func() *pb.FinalizeCodeIndexRequest {
		return uciClientTestFinalizeRequest(uciClientTestScopeA(), uciClientTestServerBuildID, uciClientTestLeaseEpoch, uciClientTestContextA(), uciClientTestAggregatePartsDigest)
	}

	for _, test := range []struct {
		name   string
		mutate func(*pb.FinalizeCodeIndexRequest)
	}{
		{name: "missing dirty presence", mutate: func(request *pb.FinalizeCodeIndexRequest) { request.Dirty = nil }},
		{name: "head without object format", mutate: func(request *pb.FinalizeCodeIndexRequest) { request.ObjectFormat = nil }},
		{name: "unsupported object format", mutate: func(request *pb.FinalizeCodeIndexRequest) {
			objectFormat := "sha512"
			request.ObjectFormat = &objectFormat
		}},
		{name: "sha256 head with sha1 length", mutate: func(request *pb.FinalizeCodeIndexRequest) {
			objectFormat := "sha256"
			request.ObjectFormat = &objectFormat
		}},
		{name: "invalid head object id", mutate: func(request *pb.FinalizeCodeIndexRequest) {
			headOID := "not-a-head"
			request.HeadOid = &headOID
		}},
		{name: "invalid ref label", mutate: func(request *pb.FinalizeCodeIndexRequest) {
			refLabel := ""
			request.RefLabel = &refLabel
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rpc := &uciClientRPCFake{}
			request := newRequest()
			test.mutate(request)
			_, err := newUCIClient(rpc).Finalize(context.Background(), request)
			require.Error(t, err)
			require.Empty(t, rpc.finalizeRequests)
		})
	}

	t.Run("accepts omitted optional observations", func(t *testing.T) {
		request := newRequest()
		request.HeadOid = nil
		request.ObjectFormat = nil
		request.RefLabel = nil
		rpc := &uciClientRPCFake{}
		_, err := newUCIClient(rpc).Finalize(context.Background(), request)
		require.NoError(t, err)
		require.Len(t, rpc.finalizeRequests, 1)
	})
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
			_, err := client.Finalize(ctx, uciClientTestFinalizeRequest(uciClientTestScopeA(), uciClientTestServerBuildID, uciClientTestLeaseEpoch, uciClientTestContextA(), uciClientTestAggregatePartsDigest))
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
		_, err := newUCIClient(rpc).Finalize(context.Background(), uciClientTestFinalizeRequest(uciClientTestScopeA(), uciClientTestServerBuildID, uciClientTestLeaseEpoch, uciClientTestContextA(), uciClientTestAggregatePartsDigest))
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
			response := uciClientTestBindResponse(request)
			response.Context = responseContext
			return response, nil
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
				PartDigest:        uciClientTestAggregatePartsDigest,
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
		_, err := newUCIClient(rpc).Finalize(context.Background(), uciClientTestFinalizeRequest(uciClientTestScopeA(), uciClientTestServerBuildID, uciClientTestLeaseEpoch, uciClientTestContextA(), uciClientTestAggregatePartsDigest))
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

func TestUCIIndexAdapterBindsBeforeCollaboratorWithServerBinding(t *testing.T) {
	const (
		clientSessionID = "client-a"
		contextHandle   = "opaque-context-handle"
		rootHint        = "server-authorized-root-hint"
	)
	boundContext := uciClientTestContextA()
	server := &uciIndexAdapterGRPCServer{
		bind: func(_ context.Context, request *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
			response := uciClientTestBindResponse(request)
			response.Context = proto.Clone(boundContext).(*pb.ContextRef)
			response.IndexScope = uciClientTestScopeA()
			return response, nil
		},
	}
	serverURL := startUCIIndexAdapterGRPC(t, server)
	published := uciClientTestPublishedContext()
	collaborator := &uciIndexCollaboratorFake{
		result:        &IndexResult{Context: published, Embedded: 3, Deleted: 1, Uploaded: 4},
		mutateBinding: true,
	}
	mod := NewModuleWithPreparedIndexCollaborator("", collaborator)
	t.Cleanup(mod.pool.closeAll)
	adapter := NewUCIIndexAdapter(mod)
	project := uciClientTestProject(serverURL)
	ctx := auditcontext.WithUCITransportSession(context.Background(), clientSessionID)

	target, err := adapter.ResolveIndexTarget(ctx, project, contextHandle)
	require.NoError(t, err)
	require.Equal(t, uciClientTestIndexBinding(boundContext), target.Binding)
	requests := server.bindRequestsSnapshot()
	require.Len(t, requests, 1)
	require.Equal(t, clientSessionID, requests[0].GetClientSessionId())
	require.Equal(t, contextHandle, requests[0].GetContextHandle())
	require.Nil(t, requests[0].GetRequestedContext(), "project and CWD must not become binding authority")
	calls, _, _, _ := collaborator.snapshot()
	require.Zero(t, calls, "the collaborator cannot prepare authority before server Bind")

	result, err := adapter.IndexCodebase(ctx, target, rootHint)
	require.NoError(t, err)
	require.Equal(t, &IndexResult{Context: published, Embedded: 3, Deleted: 1, Uploaded: 4}, result)
	calls, received, receivedRoot, receivedClient := collaborator.snapshot()
	require.Equal(t, 1, calls)
	require.Equal(t, uciClientTestIndexBinding(boundContext), received.Binding)
	require.Equal(t, rootHint, receivedRoot)
	require.NotNil(t, receivedClient)
	require.NotNil(t, target.Binding.Context)
	require.Equal(t, int64(1), target.Binding.Context.Generation, "collaborator mutation must not alter the resolved binding")
}

func TestUCIIndexAdapterForwardsUnboundBindAndRetainsIssuedHandle(t *testing.T) {
	const (
		clientSessionID = "client-a"
		issuedHandle    = "server-issued-context-handle"
	)
	boundContext := uciClientTestContextA()
	server := &uciIndexAdapterGRPCServer{
		bind: func(_ context.Context, request *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
			response := uciClientTestBindResponse(request)
			response.ContextHandle = issuedHandle
			response.Context = proto.Clone(boundContext).(*pb.ContextRef)
			response.IndexScope = uciClientTestScopeA()
			return response, nil
		},
	}
	serverURL := startUCIIndexAdapterGRPC(t, server)
	mod := NewModuleWithClientInstanceID("")
	t.Cleanup(mod.pool.closeAll)
	adapter := NewUCIIndexAdapter(mod)
	ctx := auditcontext.WithUCITransportSession(context.Background(), clientSessionID)

	target, err := adapter.ResolveIndexTarget(ctx, uciClientTestProject(serverURL), "")
	require.NoError(t, err)
	require.Equal(t, issuedHandle, target.ContextHandle)
	require.Equal(t, uciClientTestIndexBinding(boundContext), target.Binding)

	rebound, err := adapter.RebindIndexTarget(ctx, target)
	require.NoError(t, err)
	require.Equal(t, issuedHandle, rebound.ContextHandle)
	require.Equal(t, target.Binding, rebound.Binding)

	requests := server.bindRequestsSnapshot()
	require.Len(t, requests, 2)
	require.Equal(t, clientSessionID, requests[0].GetClientSessionId())
	require.Empty(t, requests[0].GetContextHandle())
	require.Nil(t, requests[0].GetRequestedContext(), "project and CWD must not become binding authority")
	require.Equal(t, issuedHandle, requests[1].GetContextHandle())
	require.Nil(t, requests[1].GetRequestedContext())
}

func TestUCIIndexAdapterMapsUnboundContextRequiredToModuleError(t *testing.T) {
	server := &uciIndexAdapterGRPCServer{
		bind: func(context.Context, *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
			return nil, status.Error(codes.FailedPrecondition, string(uci.ContextRequired))
		},
	}
	serverURL := startUCIIndexAdapterGRPC(t, server)
	mod := NewModuleWithClientInstanceID("")
	t.Cleanup(mod.pool.closeAll)
	ctx := auditcontext.WithUCITransportSession(context.Background(), "client-a")

	_, err := NewUCIIndexAdapter(mod).ResolveIndexTarget(ctx, uciClientTestProject(serverURL), "")
	var moduleErr *module.ModuleError
	require.True(t, errors.As(err, &moduleErr))
	require.Equal(t, string(uci.ContextRequired), moduleErr.Code)
	require.Equal(t, "context is required", moduleErr.Message)
	requests := server.bindRequestsSnapshot()
	require.Len(t, requests, 1)
	require.Empty(t, requests[0].GetContextHandle())
	require.Nil(t, requests[0].GetRequestedContext())
}

func TestUCIIndexAdapterResolvesNoViewAndProxiesWithoutCollaborator(t *testing.T) {
	const (
		contextHandle   = "registered-no-view"
		clientSessionID = "client-a"
	)
	server := &uciIndexAdapterGRPCServer{
		call: func(context.Context, *pb.CallToolRequest) (*pb.CallToolResponse, error) {
			return &pb.CallToolResponse{IsError: true, ContentJson: []byte("server error text")}, nil
		},
	}
	serverURL := startUCIIndexAdapterGRPC(t, server)
	mod := NewModuleWithClientInstanceID("")
	t.Cleanup(mod.pool.closeAll)
	adapter := NewUCIIndexAdapter(mod)
	ctx := auditcontext.WithUCITransportSession(context.Background(), clientSessionID)

	target, err := adapter.ResolveIndexTarget(ctx, uciClientTestProject(serverURL), contextHandle)
	require.NoError(t, err)
	require.Nil(t, target.Binding.Context)
	require.NoError(t, target.Binding.Validate())

	_, err = adapter.IndexCodebase(ctx, target, "server-authorized-root-hint")
	require.Error(t, err, "indexing still requires a prepared-index collaborator")
	block, err := adapter.ProxyHandleTool(ctx, target, "codebase_status", json.RawMessage(`{}`))
	require.Nil(t, block)
	var proxyErr *module.ProxyIsError
	require.True(t, errors.As(err, &proxyErr))
	require.JSONEq(t, `{"type":"text","text":"server error text"}`, string(proxyErr.RawContent))
	require.Len(t, server.callRequestsSnapshot(), 1)
}

func TestUCIIndexAdapterForwardsTransportTagAcrossBindAndProxy(t *testing.T) {
	const (
		transportTag  = "transport-tag-a"
		contextHandle = "transport-context-handle"
	)
	server := &uciIndexAdapterGRPCServer{}
	serverURL := startUCIIndexAdapterGRPC(t, server)
	mod := NewModuleWithClientInstanceID("")
	t.Cleanup(mod.pool.closeAll)
	adapter := NewUCIIndexAdapter(mod)
	ctx := auditcontext.WithUCITransportSession(context.Background(), transportTag)

	target, err := adapter.ResolveIndexTarget(ctx, uciClientTestProject(serverURL), contextHandle)
	require.NoError(t, err)
	_, err = adapter.ProxyHandleTool(ctx, target, "codebase_status", json.RawMessage(`{}`))
	require.NoError(t, err)

	bindRequests := server.bindRequestsSnapshot()
	callRequests := server.callRequestsSnapshot()
	require.Len(t, bindRequests, 1)
	require.Len(t, callRequests, 1)
	require.Equal(t, transportTag, bindRequests[0].GetClientSessionId())
	require.Equal(t, transportTag, callRequests[0].GetSessionId())
	bindMetadata, callMetadata := server.metadataSnapshot()
	require.Equal(t, []string{transportTag}, bindMetadata.Get(auditcontext.SourceSessionMetadataKey))
	require.Equal(t, []string{transportTag}, callMetadata.Get(auditcontext.SourceSessionMetadataKey))
}

func TestUCIIndexAdapterRejectsMissingTransportTagBeforeBind(t *testing.T) {
	server := &uciIndexAdapterGRPCServer{}
	serverURL := startUCIIndexAdapterGRPC(t, server)
	mod := NewModuleWithClientInstanceID("")
	t.Cleanup(mod.pool.closeAll)
	_, err := NewUCIIndexAdapter(mod).ResolveIndexTarget(context.Background(), uciClientTestProject(serverURL), "context-handle")
	require.Error(t, err)
	require.Empty(t, server.bindRequestsSnapshot())
}

func TestUCIIndexAdapterRejectsMissingTransportTagForBoundTarget(t *testing.T) {
	const transportTag = "transport-tag-a"
	server := &uciIndexAdapterGRPCServer{}
	serverURL := startUCIIndexAdapterGRPC(t, server)
	mod := NewModuleWithClientInstanceID("")
	t.Cleanup(mod.pool.closeAll)
	adapter := NewUCIIndexAdapter(mod)
	project := uciClientTestProject(serverURL)
	project.Env[config.EnvClaudeSessionID] = "host-session-must-not-be-used"
	tagged := auditcontext.WithUCITransportSession(context.Background(), transportTag)

	target, err := adapter.ResolveIndexTarget(tagged, project, "context-handle")
	require.NoError(t, err)
	result, err := adapter.IndexCodebase(context.Background(), target, "server-authorized-root-hint")
	require.Nil(t, result)
	require.ErrorContains(t, err, "UCI transport session")
	block, err := adapter.ProxyHandleTool(context.Background(), target, "codebase_status", json.RawMessage(`{}`))
	require.Nil(t, block)
	require.ErrorContains(t, err, "UCI transport session")
	require.Empty(t, server.callRequestsSnapshot(), "missing transport context must fail before CallTool")
}

func TestUCIIndexAdapterRejectsForeignTransportForResolvedTarget(t *testing.T) {
	server := &uciIndexAdapterGRPCServer{}
	serverURL := startUCIIndexAdapterGRPC(t, server)
	mod := NewModuleWithClientInstanceID("")
	t.Cleanup(mod.pool.closeAll)
	adapter := NewUCIIndexAdapter(mod)
	project := uciClientTestProject(serverURL)
	target, err := adapter.ResolveIndexTarget(auditcontext.WithUCITransportSession(context.Background(), "client-a"), project, "context-handle")
	require.NoError(t, err)

	block, err := adapter.ProxyHandleTool(auditcontext.WithUCITransportSession(context.Background(), "client-b"), target, "codebase_status", json.RawMessage(`{}`))
	require.Nil(t, block)
	require.ErrorContains(t, err, "resolved target is unavailable")
	require.Empty(t, server.callRequestsSnapshot(), "a target resolved for one transport must not be reusable by another")
}

func TestUCIClientDiscardsResponseWhenCallCancelsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rpc := &uciClientRPCFake{query: func(_ context.Context, request *pb.QueryCodeRequest) (*pb.QueryCodeResponse, error) {
		cancel()
		return &pb.QueryCodeResponse{Context: request.GetContext(), ResponseJson: []byte(`{"status":"ok"}`)}, nil
	}}

	response, err := newUCIClient(rpc).Query(ctx, uciClientTestQueryRequest(uciClientTestContextA()))
	require.Nil(t, response)
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, rpc.queryRequests, 1)
}

func TestUCIClientRejectsUnknownQueryResponseWithoutReturningPrivatePayload(t *testing.T) {
	const privatePayload = `{"access_token":"must-not-be-returned"}`
	rpc := &uciClientRPCFake{query: func(_ context.Context, request *pb.QueryCodeRequest) (*pb.QueryCodeResponse, error) {
		response := &pb.QueryCodeResponse{Context: request.GetContext(), ResponseJson: []byte(privatePayload)}
		response.ProtoReflect().SetUnknown([]byte{0x98, 0x06, 0x01})
		return response, nil
	}}

	response, err := newUCIClient(rpc).Query(context.Background(), uciClientTestQueryRequest(uciClientTestContextA()))
	require.Nil(t, response)
	require.ErrorIs(t, err, errUCIClientInvalidResponse)
	require.NotContains(t, err.Error(), privatePayload)
}

func TestUCIClientReplacesSpoofedProvenanceMetadata(t *testing.T) {
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		auditcontext.SourceSessionMetadataKey, "untrusted-session",
		auditcontext.UCIRequestCorrelationMetadataKey, "untrusted-correlation",
	))
	ctx = auditcontext.WithUCITransportSession(ctx, "client-a")
	correlation, ok := auditcontext.NewUCIRequestCorrelation(json.RawMessage(`"client-operation-42"`))
	require.True(t, ok)
	ctx = auditcontext.WithUCIRequestCorrelation(ctx, correlation)
	rpc := &uciClientRPCFake{bind: func(callCtx context.Context, request *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
		outgoing, ok := metadata.FromOutgoingContext(callCtx)
		require.True(t, ok)
		require.Equal(t, []string{"client-a"}, outgoing.Get(auditcontext.SourceSessionMetadataKey))
		require.Equal(t, []string{correlation.MetadataValue()}, outgoing.Get(auditcontext.UCIRequestCorrelationMetadataKey))
		return uciClientTestBindResponse(request), nil
	}}

	_, err := newUCIClient(rpc).Bind(ctx, &pb.BindCodeContextRequest{ClientSessionId: "client-a", RequestedContext: uciClientTestContextA()})
	require.NoError(t, err)
}

func TestUCIClientMapsStageTransportFailurePhase(t *testing.T) {
	frames := []*pb.StageCodeIndexFrame{uciClientTestStageFrame(uciClientTestScopeA(), uciClientTestServerBuildID, uciClientTestLeaseEpoch, 0)}
	t.Run("opening stream", func(t *testing.T) {
		cause := errors.New("open failed")
		rpc := &uciClientRPCFake{stageOpen: func(context.Context) (grpc.ClientStreamingClient[pb.StageCodeIndexFrame, pb.StageCodeIndexResponse], error) {
			return nil, cause
		}}
		response, err := newUCIClient(rpc).Stage(context.Background(), frames)
		require.Nil(t, response)
		require.ErrorIs(t, err, cause)
		require.ErrorContains(t, err, "UCI Stage open")
	})
	t.Run("sending frame", func(t *testing.T) {
		cause := errors.New("send failed")
		rpc := &uciClientRPCFake{stageStream: &uciClientStageStream{send: func(*pb.StageCodeIndexFrame) error { return cause }}}
		response, err := newUCIClient(rpc).Stage(context.Background(), frames)
		require.Nil(t, response)
		require.ErrorIs(t, err, cause)
		require.ErrorContains(t, err, "UCI Stage send")
	})
	t.Run("closing stream", func(t *testing.T) {
		cause := errors.New("close failed")
		rpc := &uciClientRPCFake{stageStream: &uciClientStageStream{close: func([]*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error) {
			return nil, cause
		}}}
		response, err := newUCIClient(rpc).Stage(context.Background(), frames)
		require.Nil(t, response)
		require.ErrorIs(t, err, cause)
		require.ErrorContains(t, err, "UCI Stage close")
	})
}

type uciIndexAdapterGRPCServer struct {
	pb.UnimplementedEngramServiceServer
	mu sync.Mutex

	bind func(context.Context, *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error)
	call func(context.Context, *pb.CallToolRequest) (*pb.CallToolResponse, error)

	bindRequests []*pb.BindCodeContextRequest
	callRequests []*pb.CallToolRequest
	bindMetadata metadata.MD
	callMetadata metadata.MD
}

func (server *uciIndexAdapterGRPCServer) BindCodeContext(ctx context.Context, request *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error) {
	server.mu.Lock()
	server.bindRequests = append(server.bindRequests, proto.Clone(request).(*pb.BindCodeContextRequest))
	server.bindMetadata, _ = metadata.FromIncomingContext(ctx)
	server.bindMetadata = server.bindMetadata.Copy()
	bind := server.bind
	server.mu.Unlock()
	if bind != nil {
		return bind(ctx, request)
	}
	return uciClientTestBindResponse(request), nil
}

func (server *uciIndexAdapterGRPCServer) CallTool(ctx context.Context, request *pb.CallToolRequest) (*pb.CallToolResponse, error) {
	server.mu.Lock()
	server.callRequests = append(server.callRequests, proto.Clone(request).(*pb.CallToolRequest))
	server.callMetadata, _ = metadata.FromIncomingContext(ctx)
	server.callMetadata = server.callMetadata.Copy()
	call := server.call
	server.mu.Unlock()
	if call != nil {
		return call(ctx, request)
	}
	return &pb.CallToolResponse{}, nil
}

func (server *uciIndexAdapterGRPCServer) bindRequestsSnapshot() []*pb.BindCodeContextRequest {
	server.mu.Lock()
	defer server.mu.Unlock()
	requests := make([]*pb.BindCodeContextRequest, len(server.bindRequests))
	for index, request := range server.bindRequests {
		requests[index] = proto.Clone(request).(*pb.BindCodeContextRequest)
	}
	return requests
}

func (server *uciIndexAdapterGRPCServer) callRequestsSnapshot() []*pb.CallToolRequest {
	server.mu.Lock()
	defer server.mu.Unlock()
	requests := make([]*pb.CallToolRequest, len(server.callRequests))
	for index, request := range server.callRequests {
		requests[index] = proto.Clone(request).(*pb.CallToolRequest)
	}
	return requests
}

func (server *uciIndexAdapterGRPCServer) metadataSnapshot() (metadata.MD, metadata.MD) {
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.bindMetadata.Copy(), server.callMetadata.Copy()
}

func startUCIIndexAdapterGRPC(t *testing.T, server *uciIndexAdapterGRPCServer) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcServer := grpc.NewServer()
	pb.RegisterEngramServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.GracefulStop)
	return listener.Addr().String()
}

type uciIndexCollaboratorFake struct {
	mu sync.Mutex

	result        *IndexResult
	mutateBinding bool
	calls         int
	target        ResolvedIndexTarget
	rootHint      string
	client        UCIIndexClient
}

func (fake *uciIndexCollaboratorFake) IndexPreparedCodebase(_ context.Context, target ResolvedIndexTarget, rootHint string, client UCIIndexClient) (*IndexResult, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls++
	fake.target = target.Clone()
	fake.rootHint = rootHint
	fake.client = client
	if fake.mutateBinding && target.Binding.Context != nil {
		target.Binding.Context.Generation++
	}
	return fake.result, nil
}

func (fake *uciIndexCollaboratorFake) snapshot() (int, ResolvedIndexTarget, string, UCIIndexClient) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.calls, fake.target.Clone(), fake.rootHint, fake.client
}

type uciClientRPCFake struct {
	bindRequests   []*pb.BindCodeContextRequest
	pollRequests   []*pb.PollCodeIndexIntentsRequest
	updateRequests []*pb.UpdateCodeIndexIntentRequest
	beginRequests  []*pb.BeginCodeIndexRequest
	stageOpenCalls int
	stageOpen      func(context.Context) (grpc.ClientStreamingClient[pb.StageCodeIndexFrame, pb.StageCodeIndexResponse], error)
	stageStream    *uciClientStageStream

	finalizeRequests []*pb.FinalizeCodeIndexRequest
	queryRequests    []*pb.QueryCodeRequest
	exploreRequests  []*pb.ExploreCodeRequest

	bind     func(context.Context, *pb.BindCodeContextRequest) (*pb.BindCodeContextResponse, error)
	poll     func(context.Context, *pb.PollCodeIndexIntentsRequest) (*pb.PollCodeIndexIntentsResponse, error)
	update   func(context.Context, *pb.UpdateCodeIndexIntentRequest) (*pb.UpdateCodeIndexIntentResponse, error)
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
	return uciClientTestBindResponse(request), nil
}

func (fake *uciClientRPCFake) PollCodeIndexIntents(ctx context.Context, request *pb.PollCodeIndexIntentsRequest, _ ...grpc.CallOption) (*pb.PollCodeIndexIntentsResponse, error) {
	fake.pollRequests = append(fake.pollRequests, request)
	if fake.poll != nil {
		return fake.poll(ctx, request)
	}
	return &pb.PollCodeIndexIntentsResponse{}, nil
}

func (fake *uciClientRPCFake) UpdateCodeIndexIntent(ctx context.Context, request *pb.UpdateCodeIndexIntentRequest, _ ...grpc.CallOption) (*pb.UpdateCodeIndexIntentResponse, error) {
	fake.updateRequests = append(fake.updateRequests, request)
	if fake.update != nil {
		return fake.update(ctx, request)
	}
	response := &pb.UpdateCodeIndexIntentResponse{
		IntentRef: request.GetIntentRef(),
		State:     string(uci.IndexIntentAcknowledged),
		Attempt:   1,
	}
	if request.GetOwnerEpoch() > 0 {
		response.OwnerEpoch = request.GetOwnerEpoch()
		response.LeaseExpiresAt = timestamppb.Now()
	}
	return response, nil
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

func (fake *uciClientRPCFake) StageCodeIndex(ctx context.Context, _ ...grpc.CallOption) (grpc.ClientStreamingClient[pb.StageCodeIndexFrame, pb.StageCodeIndexResponse], error) {
	fake.stageOpenCalls++
	if fake.stageOpen != nil {
		return fake.stageOpen(ctx)
	}
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
	return len(fake.bindRequests) + len(fake.pollRequests) + len(fake.updateRequests) + len(fake.beginRequests) + fake.stageOpenCalls + len(fake.finalizeRequests) + len(fake.queryRequests) + len(fake.exploreRequests)
}

var _ uciClientRPC = (*uciClientRPCFake)(nil)

type uciClientStageStream struct {
	grpc.ClientStream
	send  func(*pb.StageCodeIndexFrame) error
	sent  []*pb.StageCodeIndexFrame
	close func([]*pb.StageCodeIndexFrame) (*pb.StageCodeIndexResponse, error)
}

func (stream *uciClientStageStream) Send(frame *pb.StageCodeIndexFrame) error {
	if stream.send != nil {
		return stream.send(frame)
	}
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
		PartDigest:        uciClientTestAggregatePartsDigest,
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

func uciClientTestBindResponse(request *pb.BindCodeContextRequest) *pb.BindCodeContextResponse {
	response := &pb.BindCodeContextResponse{
		ContextHandle: "context-handle-" + request.GetClientSessionId(),
		Context:       request.GetRequestedContext(),
		IndexScope:    uciClientTestScopeA(),
		LocalRootId:   uciClientTestLocalRootID,
		WorkstationId: uciClientTestWorkstationID,
	}
	if request.GetContextHandle() != "" {
		response.ContextHandle = request.GetContextHandle()
		return response
	}
	response.IndexScope = uciClientTestScopeForContext(request.GetRequestedContext())
	return response
}

func uciClientTestScopeForContext(reference *pb.ContextRef) *pb.CodeIndexScope {
	scope := uciClientTestScopeA()
	if reference == nil {
		return scope
	}
	if reference.GetCheckoutId() == uciClientTestCheckoutBID {
		scope.IncarnationId = uciClientTestIncarnationB
	}
	scope.SourceId = reference.GetSourceId()
	scope.CheckoutId = reference.GetCheckoutId()
	scope.AnalysisProfileId = reference.GetAnalysisProfileId()
	return scope
}

func uciClientTestIndexBinding(reference *pb.ContextRef) uci.IndexBinding {
	scope := uciClientTestScopeForContext(reference)
	binding := uci.IndexBinding{
		Scope: uci.IndexScope{
			SourceID:      scope.GetSourceId(),
			CheckoutID:    scope.GetCheckoutId(),
			IncarnationID: scope.GetIncarnationId(),
		},
		ProfileID:     scope.GetAnalysisProfileId(),
		LocalRootID:   uciClientTestLocalRootID,
		WorkstationID: uciClientTestWorkstationID,
	}
	if reference != nil {
		contextRef := uciClientTestDomainContext(reference)
		binding.Context = &contextRef
	}
	return binding
}

func uciClientTestDomainContext(reference *pb.ContextRef) uci.ContextRef {
	contextRef := uci.ContextRef{
		SourceID:          reference.GetSourceId(),
		CheckoutID:        reference.GetCheckoutId(),
		ViewID:            reference.GetViewId(),
		AnalysisProfileID: reference.GetAnalysisProfileId(),
		Generation:        reference.GetGeneration(),
	}
	if reference.SpaceId != nil {
		spaceID := reference.GetSpaceId()
		contextRef.SpaceID = &spaceID
	}
	return contextRef
}

func uciClientTestPublishedContext() uci.ContextRef {
	spaceID := uciClientTestSpaceID
	return uci.ContextRef{
		SpaceID:           &spaceID,
		SourceID:          uciClientTestSourceID,
		CheckoutID:        uciClientTestCheckoutAID,
		ViewID:            uciClientTestViewBID,
		AnalysisProfileID: uciClientTestProfileID,
		Generation:        2,
	}
}

func uciClientTestProject(serverURL string) muxcore.ProjectContext {
	return muxcore.ProjectContext{
		ID:  "untrusted-project-selector",
		Cwd: "untrusted-cwd",
		Env: map[string]string{
			config.EnvServerURL:        "http://" + serverURL,
			config.EnvWorkstationToken: "fixture-token",
		},
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
		PayloadDigest: uciClientTestFrameDigest,
		Payload:       []byte("payload"),
	}
}

func uciClientTestFinalizeRequest(scope *pb.CodeIndexScope, buildID string, leaseEpoch uint64, expectedParent *pb.ContextRef, partsDigest string) *pb.FinalizeCodeIndexRequest {
	startedAt := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	headOID := uciClientTestHeadOID
	objectFormat := uciClientTestObjectFormat
	refLabel := uciClientTestRefLabel
	dirty := false
	return &pb.FinalizeCodeIndexRequest{
		Scope:                      scope,
		BuildId:                    buildID,
		LeaseEpoch:                 leaseEpoch,
		ExpectedParent:             expectedParent,
		ManifestPartCount:          2,
		PartsDigest:                partsDigest,
		ManifestEntryCount:         3,
		ManifestDigest:             uciClientTestFrameDigest,
		EdgeCount:                  4,
		EdgesDigest:                uciClientTestFrameDigest,
		ObservedFilesystemSequence: 9,
		ScanStartedAt:              timestamppb.New(startedAt),
		ScanCompletedAt:            timestamppb.New(startedAt.Add(time.Second)),
		ScanOutcome:                "complete",
		CompleteCensus:             true,
		CoverageJson:               []byte(`{"structural":"complete"}`),
		HeadOid:                    &headOID,
		ObjectFormat:               &objectFormat,
		RefLabel:                   &refLabel,
		Dirty:                      &dirty,
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
