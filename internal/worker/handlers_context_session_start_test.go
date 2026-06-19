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

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/pkg/models"
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

func TestHandleSessionStartContextStatic_MapsRuleRouterSidecar(t *testing.T) {
	t.Parallel()

	server := &stubSessionStartContextServer{
		resp: &pb.GetSessionStartContextResponse{
			Rules: []*pb.SessionStartRule{{
				Id:      2,
				Content: "Kernel rule content",
			}},
			RuleRouter: &pb.SessionStartRuleRouter{
				Enabled:         true,
				Mode:            "router",
				KernelCount:     1,
				ContextualCount: 1,
				SuppressedCount: 1,
				BudgetOutcome:   "within_budget",
				Kernel: []*pb.SessionStartRulePacket{{
					RuleVersionId: 10,
					Bucket:        "kernel",
					Content:       "Kernel rule content",
					State:         "kernel",
				}},
				Contextual: []*pb.SessionStartRulePacket{{
					RuleVersionId: 11,
					Bucket:        "contextual",
					Content:       "Contextual rule content",
					State:         "active_project",
				}},
				Suppressed: []*pb.SessionStartRulePacket{{
					RuleVersionId:     12,
					Bucket:            "suppressed",
					SuppressionReason: "suppressed_predicate",
				}},
			},
		},
	}
	service := &Service{grpcInternalServer: server}

	req := httptest.NewRequest(http.MethodGet, "/api/context/session-start?project=engram", nil)
	w := httptest.NewRecorder()

	service.handleSessionStartContextStatic(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	router, ok := body["rule_router"].(map[string]any)
	require.True(t, ok, "rule_router sidecar must be present when gRPC response carries it")
	assert.Equal(t, true, router["enabled"])
	assert.Equal(t, "router", router["mode"])
	assert.Equal(t, float64(1), router["kernel_count"])
	assert.Equal(t, float64(1), router["contextual_count"])
	assert.Equal(t, float64(1), router["suppressed_count"])
	assert.Equal(t, "within_budget", router["budget_outcome"])

	kernel, ok := router["kernel"].([]any)
	require.True(t, ok)
	require.Len(t, kernel, 1)
	kernelPacket, ok := kernel[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(10), kernelPacket["rule_version_id"])
	assert.Equal(t, "kernel", kernelPacket["bucket"])

	suppressed, ok := router["suppressed"].([]any)
	require.True(t, ok)
	require.Len(t, suppressed, 1)
	suppressedPacket, ok := suppressed[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "suppressed_predicate", suppressedPacket["suppression_reason"])
	assert.Empty(t, suppressedPacket["content"], "suppressed packets must not expose rule text")
}

func TestContextRuleRouterSidecarMapHonorsRouterFlag(t *testing.T) {
	t.Parallel()

	server := &stubSessionStartContextServer{
		resp: &pb.GetSessionStartContextResponse{
			RuleRouter: &pb.SessionStartRuleRouter{
				Enabled:         true,
				Mode:            "router",
				KernelCount:     1,
				ContextualCount: 0,
				SuppressedCount: 0,
				BudgetOutcome:   "within_budget",
				Kernel: []*pb.SessionStartRulePacket{{
					RuleVersionId: 44,
					Bucket:        "kernel",
					Content:       "kernel rule",
				}},
			},
		},
	}
	cfg := config.Default()
	cfg.RuleRouterEnabled = true
	service := &Service{config: cfg, grpcInternalServer: server}

	router := service.contextRuleRouterSidecarMap(context.Background(), "engram")
	require.NotNil(t, router, "router-enabled context endpoints must be able to attach rule_router sidecar")
	assert.Equal(t, "engram", server.req.GetProject())
	assert.Equal(t, true, router["enabled"])
	assert.Equal(t, "router", router["mode"])
	kernel, ok := router["kernel"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, kernel, 1)
	assert.Equal(t, int64(44), kernel[0]["rule_version_id"])

	cfg.RuleRouterEnabled = false
	assert.Nil(t, service.contextRuleRouterSidecarMap(context.Background(), "engram"))
}

func TestWithRuleRouterSidecarAddsOnlyWhenPresent(t *testing.T) {
	t.Parallel()

	response := withRuleRouterSidecar(map[string]any{"observations": []any{}}, nil)
	assert.NotContains(t, response, "rule_router")

	response = withRuleRouterSidecar(response, map[string]any{"enabled": true})
	assert.Equal(t, map[string]any{"enabled": true}, response["rule_router"])
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

func TestCollectSessionStartRuleEvents(t *testing.T) {
	t.Parallel()

	events := collectSessionStartRuleEvents("sess-1", "engram", "session-start", &pb.SessionStartRuleRouter{
		Enabled: true,
		Mode:    "router",
		Kernel: []*pb.SessionStartRulePacket{{
			RuleVersionId: 10,
			Bucket:        "kernel",
		}},
		Contextual: []*pb.SessionStartRulePacket{
			{
				RuleVersionId: 11,
				Bucket:        "contextual",
			},
			{
				LegacyBehavioralRuleId: 22,
				Bucket:                 "contextual",
				BudgetClass:            "legacy",
			},
		},
		Suppressed: []*pb.SessionStartRulePacket{
			{
				RuleVersionId:     12,
				Bucket:            "suppressed",
				SuppressionReason: "deferred_budget",
			},
			{
				RuleVersionId:     13,
				Bucket:            "suppressed",
				SuppressionReason: "suppressed_state",
			},
			{
				RuleVersionId:     14,
				Bucket:            "suppressed",
				SuppressionReason: "unknown",
			},
		},
		FallbackReason: "router store unavailable",
	})

	require.Len(t, events, 7)
	assert.Equal(t, models.RuleInjectionEmittedKernel, events[0].EventType)
	require.NotNil(t, events[0].RuleVersionID)
	assert.Equal(t, int64(10), *events[0].RuleVersionID)
	assert.Equal(t, 1, events[0].BudgetPosition)

	assert.Equal(t, models.RuleInjectionEmittedContextual, events[1].EventType)
	assert.Equal(t, models.RuleInjectionFallbackLegacy, events[2].EventType)
	require.NotNil(t, events[2].LegacyBehavioralRuleID)
	assert.Equal(t, int64(22), *events[2].LegacyBehavioralRuleID)
	assert.Equal(t, "legacy_behavioral_rule_fallback", events[2].Reason)

	assert.Equal(t, models.RuleInjectionDeferredBudget, events[3].EventType)
	assert.Equal(t, models.RuleInjectionSuppressedState, events[4].EventType)
	assert.Equal(t, models.RuleInjectionSuppressedPredicate, events[5].EventType)
	assert.Equal(t, models.RuleInjectionRouterError, events[6].EventType)
	assert.Equal(t, "router store unavailable", events[6].Reason)
}

func TestCollectSessionStartRuleEventsSkipsWithoutRouterOrSession(t *testing.T) {
	t.Parallel()

	router := &pb.SessionStartRuleRouter{Enabled: true, Mode: "router", Kernel: []*pb.SessionStartRulePacket{{RuleVersionId: 1}}}
	assert.Nil(t, collectSessionStartRuleEvents("", "engram", "session-start", router))
	assert.Nil(t, collectSessionStartRuleEvents("sess", "", "session-start", router))
	assert.Nil(t, collectSessionStartRuleEvents("sess", "engram", "session-start", nil))
	assert.Nil(t, collectSessionStartRuleEvents("sess", "engram", "session-start", &pb.SessionStartRuleRouter{Enabled: false}))
}

// TestDetachedContext_NilServiceCtxFallsBackToBackground proves the CR-001 review
// fix (gemini): a fire-and-forget recorder must never panic when s.ctx is nil (a
// half-initialized Service, common in tests / early init). context.WithTimeout(nil,…)
// panics; detachedContext must fall back to context.Background() instead.
func TestDetachedContext_NilServiceCtxFallsBackToBackground(t *testing.T) {
	t.Parallel()

	svc := &Service{} // s.ctx is nil
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	require.NotPanics(t, func() {
		ctx, cancel = svc.detachedContext(30 * time.Second)
	})
	defer cancel()
	require.NotNil(t, ctx)
	_, hasDeadline := ctx.Deadline()
	assert.True(t, hasDeadline, "detachedContext must apply the timeout deadline")
	assert.NoError(t, ctx.Err(), "fresh context must not be already cancelled")

	// With a real parent ctx, the deadline is still applied and not panicking.
	svc2 := &Service{ctx: context.Background()}
	ctx2, cancel2 := svc2.detachedContext(30 * time.Second)
	defer cancel2()
	_, hasDeadline2 := ctx2.Deadline()
	assert.True(t, hasDeadline2)
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
