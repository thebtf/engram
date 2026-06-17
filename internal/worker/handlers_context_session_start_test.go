package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/thebtf/engram/internal/grpcserver"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

type stubSessionStartContextServer struct {
	grpcserver.Server
	resp *pb.GetSessionStartContextResponse
	err  error
	req  *pb.GetSessionStartContextRequest
}

func (s *stubSessionStartContextServer) GetSessionStartContext(_ context.Context, req *pb.GetSessionStartContextRequest) (*pb.GetSessionStartContextResponse, error) {
	s.req = req
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

func TestHandleSessionStartContextStatic_HappyPath(t *testing.T) {
	t.Parallel()

	generatedAt := "2026-04-22T13:00:00Z"
	server := &stubSessionStartContextServer{
		resp: &pb.GetSessionStartContextResponse{
			Issues: []*pb.SessionStartIssue{{
				Id:            1,
				Title:         "Issue title",
				Status:        "open",
				Priority:      "high",
				Type:          "bug",
				SourceProject: "source",
				TargetProject: "engram",
			}},
			Rules: []*pb.SessionStartRule{{
				Id:      2,
				Content: "Rule content",
				Project: "engram",
			}},
			Memories: []*pb.SessionStartMemory{{
				Id:      3,
				Project: "engram",
				Content: "Memory content",
			}},
			GeneratedAt: mustProtoTimestamp(t, generatedAt),
		},
	}
	service := &Service{grpcInternalServer: server}

	req := httptest.NewRequest(http.MethodGet, "/api/context/session-start?project=engram", nil)
	w := httptest.NewRecorder()

	service.handleSessionStartContextStatic(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, server.req)
	assert.Equal(t, "engram", server.req.GetProject())

	var body sessionStartCompatibilityResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Issues, 1)
	require.Len(t, body.Rules, 1)
	require.Len(t, body.Memories, 1)
	assert.Equal(t, generatedAt, body.GeneratedAt)
	assert.Equal(t, "Issue title", body.Issues[0]["title"])
	assert.Equal(t, "Rule content", body.Rules[0]["content"])
	assert.Equal(t, "Memory content", body.Memories[0]["content"])
}

func TestHandleSessionStartContextStatic_MapsGrpcErrors(t *testing.T) {
	t.Parallel()

	service := &Service{grpcInternalServer: &stubSessionStartContextServer{
		err: grpcstatus.Error(codes.Unavailable, "database not ready"),
	}}

	req := httptest.NewRequest(http.MethodGet, "/api/context/session-start?project=engram", nil)
	w := httptest.NewRecorder()

	service.handleSessionStartContextStatic(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "database not ready")
}

func mustProtoTimestamp(t *testing.T, iso string) *timestamppb.Timestamp {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, iso)
	require.NoError(t, err)
	return timestamppb.New(parsed)
}

// TestCollectSessionStartMemoryIDs is the CR-001 anti-stub proof for the injection
// recorder's ID-selection: it must dedup, drop zero/nil entries, and preserve order.
// A stub returning nil or the raw count fails these assertions.
func TestCollectSessionStartMemoryIDs(t *testing.T) {
	t.Parallel()

	t.Run("dedups, drops zero and nil, preserves order", func(t *testing.T) {
		t.Parallel()
		in := []*pb.SessionStartMemory{
			{Id: 31},
			{Id: 0}, // dropped: zero id
			nil,     // dropped: nil entry
			{Id: 42},
			{Id: 31}, // dropped: duplicate
			{Id: 7},
		}
		got := collectSessionStartMemoryIDs(in)
		assert.Equal(t, []int64{31, 42, 7}, got)
	})

	t.Run("empty input returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, collectSessionStartMemoryIDs(nil))
		assert.Nil(t, collectSessionStartMemoryIDs([]*pb.SessionStartMemory{}))
	})

	t.Run("all-zero input returns empty (no injection recorded)", func(t *testing.T) {
		t.Parallel()
		got := collectSessionStartMemoryIDs([]*pb.SessionStartMemory{{Id: 0}, nil})
		assert.Empty(t, got)
	})
}

// TestHandleSessionStartContextStatic_POSTSessionIDNoStorePanic asserts that the
// recording branch is safe when stores are not wired (nil injectionLogStore): a POST
// carrying session_id must still deliver content and must NOT panic. This guards the
// CR-001 recorder's nil-store path (delivery must never depend on telemetry).
func TestHandleSessionStartContextStatic_POSTSessionIDNoStorePanic(t *testing.T) {
	t.Parallel()

	server := &stubSessionStartContextServer{
		resp: &pb.GetSessionStartContextResponse{
			Memories: []*pb.SessionStartMemory{{Id: 3, Project: "engram", Content: "Memory content"}},
		},
	}
	// grpcInternalServer set; injectionLogStore + memoryStore deliberately nil.
	service := &Service{grpcInternalServer: server}

	body := `{"project":"engram","session_id":"sess-X"}`
	req := httptest.NewRequest(http.MethodPost, "/api/context/session-start", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	require.NotPanics(t, func() {
		service.handleSessionStartContextStatic(w, req)
	})
	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, server.req)
	assert.Equal(t, "engram", server.req.GetProject())

	var respBody sessionStartCompatibilityResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &respBody))
	require.Len(t, respBody.Memories, 1)
	assert.Equal(t, "Memory content", respBody.Memories[0]["content"])
}
