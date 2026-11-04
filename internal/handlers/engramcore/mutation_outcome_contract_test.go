package engramcore

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/config"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type lostMutationResponseServer struct {
	pb.UnimplementedEngramServiceServer
	calls    atomic.Int32
	requests chan *pb.CallToolRequest
}

func (s *lostMutationResponseServer) CallTool(_ context.Context, request *pb.CallToolRequest) (*pb.CallToolResponse, error) {
	s.calls.Add(1)
	s.requests <- request
	return nil, status.Error(codes.Unavailable, "synthetic response loss after mutation")
}

func TestProxyMutationResponseLossIsNotReplayed(t *testing.T) {
	server := &lostMutationResponseServer{requests: make(chan *pb.CallToolRequest, 2)}
	_, core, project := buildContractDispatcher(t, startMockGRPC(t, server))
	project.Env[config.EnvWorkstationToken] = "synthetic-fixture-token"
	args := json.RawMessage(`{"action":"store","name":"fixture","value":"synthetic","scope":"global"}`)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := core.ProxyHandleTool(ctx, project, "vault", args)
	if err == nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("lost response error=%v, want Unavailable", err)
	}
	select {
	case request := <-server.requests:
		if request.GetToolName() != "vault" || string(request.GetArgumentsJson()) != string(args) {
			t.Fatalf("forwarded request changed: %v", request)
		}
	default:
		t.Fatal("backend did not record the mutation")
	}
	if calls := server.calls.Load(); calls != 1 {
		t.Fatalf("mutation dispatched %d times, want exactly one", calls)
	}
}
