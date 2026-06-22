package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/internal/worker/sse"
)

func TestHandlePrincipalMemoryQuery_ResponseAndValidation(t *testing.T) {
	t.Run("returns attributed bounded principal memory response", func(t *testing.T) {
		querySvc := &fakePrincipalMemoryQueryService{
			result: &principalmemory.PrincipalMemoryQueryResult{
				Items: []principalmemory.PrincipalMemoryQueryItem{
					{
						ID:                 42,
						Project:            "project-a",
						Content:            "shared alice note",
						Tags:               []string{"semantic"},
						OwnerPrincipal:     "agent/alice",
						OwnerPrincipalKind: "human",
						AgentVisibility:    "shared",
						Domain:             "operator-console",
						Confidence:         0.8,
						CreatedAt:          time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC),
					},
				},
				Principal:     "agent/alice",
				PrincipalKind: "human",
				Project:       "project-a",
				Domain:        "operator-console",
				HiddenCount:   1,
				AuditStatus:   "not_required",
				Audit: principalmemory.PrincipalMemoryQueryAudit{
					Action: "principal_memory_query",
				},
			},
		}
		service := &Service{principalMemoryQueryService: querySvc}
		id := auth.ClientWithPrincipal("read-write", "keycard-bob", "agent/bob", auth.PrincipalKindAgent)
		req := httptest.NewRequest(http.MethodGet, "/api/memories/principal?project=project-a&principal=agent/alice&domain=operator-console&q=shared+alice&visibility=all&limit=2", nil).
			WithContext(auth.WithIdentity(context.Background(), id))
		w := httptest.NewRecorder()

		service.handlePrincipalMemoryQuery(w, req)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "agent/alice", body["principal"])
		require.Equal(t, "human", body["principal_kind"])
		require.Equal(t, "project-a", body["project"])
		require.Equal(t, "operator-console", body["domain"])
		require.Equal(t, float64(1), body["hidden_count"])
		require.Equal(t, "not_required", body["audit_status"])
		audit := body["audit"].(map[string]any)
		require.Equal(t, "principal_memory_query", audit["action"])
		require.Equal(t, false, audit["durable"])
		items := body["items"].([]any)
		require.Len(t, items, 1)
		first := items[0].(map[string]any)
		assert.Equal(t, float64(42), first["id"])
		assert.Equal(t, "agent/alice", first["owner_principal"])
		assert.Equal(t, "human", first["owner_principal_kind"])
		assert.Equal(t, "operator-console", first["domain"])
		assert.Equal(t, []any{"semantic"}, first["tags"])
		assert.Equal(t, 0.8, first["confidence"])
		assert.Equal(t, "2026-06-22T00:00:00Z", first["created_at"])

		assert.Equal(t, "project-a", querySvc.request.Project)
		assert.Equal(t, "agent/alice", querySvc.request.OwnerPrincipal)
		assert.Equal(t, "human", querySvc.request.OwnerPrincipalKind)
		assert.Equal(t, "operator-console", querySvc.request.Domain)
		assert.Equal(t, "shared alice", querySvc.request.Query)
		assert.Empty(t, querySvc.request.AgentVisibility)
		assert.Equal(t, 2, querySvc.request.Limit)
		assert.Equal(t, "agent/bob", querySvc.request.Caller.Principal)
	})

	t.Run("rejects invalid principal kind", func(t *testing.T) {
		service := &Service{principalMemoryQueryService: &fakePrincipalMemoryQueryService{}}
		req := httptest.NewRequest(http.MethodGet, "/api/memories/principal?principal=agent/alice&principal_kind=robot", nil)
		w := httptest.NewRecorder()

		service.handlePrincipalMemoryQuery(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("rejects non numeric limit", func(t *testing.T) {
		service := &Service{principalMemoryQueryService: &fakePrincipalMemoryQueryService{}}
		req := httptest.NewRequest(http.MethodGet, "/api/memories/principal?principal=agent/alice&principal_kind=agent&limit=nope", nil)
		w := httptest.NewRecorder()

		service.handlePrincipalMemoryQuery(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("clamps oversized limit", func(t *testing.T) {
		querySvc := &fakePrincipalMemoryQueryService{result: &principalmemory.PrincipalMemoryQueryResult{}}
		service := &Service{principalMemoryQueryService: querySvc}
		req := httptest.NewRequest(http.MethodGet, "/api/memories/principal?principal=agent/alice&principal_kind=agent&limit=9999", nil)
		w := httptest.NewRecorder()

		service.handlePrincipalMemoryQuery(w, req)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, 500, querySvc.request.Limit)
	})

	t.Run("rejects non-admin cross-principal private widening", func(t *testing.T) {
		querySvc := &fakePrincipalMemoryQueryService{err: principalmemory.ErrCrossPrincipalPrivateDenied}
		service := &Service{principalMemoryQueryService: querySvc}
		id := auth.ClientWithPrincipal("read-write", "keycard-bob", "agent/bob", auth.PrincipalKindAgent)
		req := httptest.NewRequest(http.MethodGet, "/api/memories/principal?principal=agent/alice&principal_kind=agent&include_private=true", nil).
			WithContext(auth.WithIdentity(context.Background(), id))
		w := httptest.NewRecorder()

		service.handlePrincipalMemoryQuery(w, req)

		require.Equal(t, http.StatusForbidden, w.Code)
		assert.True(t, querySvc.called)
		assert.True(t, querySvc.request.IncludePrivate)
	})
}

func TestHandlePrincipalMemoryQuery_RouteRegistered(t *testing.T) {
	t.Setenv("ENGRAM_AUTH_DISABLED", "true")
	ta, err := NewTokenAuth("")
	require.NoError(t, err)

	querySvc := &fakePrincipalMemoryQueryService{result: &principalmemory.PrincipalMemoryQueryResult{}}
	service := &Service{
		router:                      chi.NewRouter(),
		tokenAuth:                   ta,
		mcpHealth:                   mcp.NewMCPHealth(),
		sseBroadcaster:              sse.NewBroadcaster(),
		principalMemoryQueryService: querySvc,
	}
	service.ready.Store(true)
	service.setupRoutes()

	req := httptest.NewRequest(http.MethodGet, "/api/memories/principal?principal=agent/alice&principal_kind=agent", nil)
	w := httptest.NewRecorder()
	service.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.True(t, querySvc.called)
}

func TestHandlePrincipalMemoryQuery_AuditMetadataAndFailure(t *testing.T) {
	t.Run("admin private query returns audit metadata from service", func(t *testing.T) {
		querySvc := &fakePrincipalMemoryQueryService{
			result: &principalmemory.PrincipalMemoryQueryResult{
				Items: []principalmemory.PrincipalMemoryQueryItem{
					{
						ID:                 77,
						Project:            "project-a",
						Content:            "private alice audited memory",
						OwnerPrincipal:     "agent/alice",
						OwnerPrincipalKind: "agent",
						AgentVisibility:    "private",
						Domain:             "operator-console",
					},
				},
				AuditStatus: principalmemory.AuditStatusWritten,
			},
		}
		service := &Service{principalMemoryQueryService: querySvc}
		req := httptest.NewRequest(http.MethodGet, "/api/memories/principal?project=project-a&principal=agent/alice&principal_kind=agent&include_private=true&session_id=session-77", nil).
			WithContext(auth.WithIdentity(context.Background(), auth.Admin()))
		w := httptest.NewRecorder()

		service.handlePrincipalMemoryQuery(w, req)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, principalmemory.AuditStatusWritten, body["audit_status"])
		items := body["items"].([]any)
		require.Len(t, items, 1)
		first := items[0].(map[string]any)
		require.Equal(t, "private alice audited memory", first["content"])
		require.True(t, querySvc.request.CallerIsAdmin)
		require.True(t, querySvc.request.IncludePrivate)
		require.Equal(t, "session-77", querySvc.request.SourceSessionID)
	})

	t.Run("audit failure returns 500 before private data is emitted", func(t *testing.T) {
		querySvc := &fakePrincipalMemoryQueryService{
			err: errors.New("principal private widening audit: audit unavailable for private alice memory"),
		}
		service := &Service{principalMemoryQueryService: querySvc}
		req := httptest.NewRequest(http.MethodGet, "/api/memories/principal?project=project-a&principal=agent/alice&principal_kind=agent&include_private=true", nil).
			WithContext(auth.WithIdentity(context.Background(), auth.Admin()))
		w := httptest.NewRecorder()

		service.handlePrincipalMemoryQuery(w, req)

		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.NotContains(t, w.Body.String(), "private alice memory")
		require.Contains(t, w.Body.String(), "principal memory query failed")
		require.True(t, querySvc.called)
	})

	t.Run("self private query can succeed without admin audit metadata", func(t *testing.T) {
		querySvc := &fakePrincipalMemoryQueryService{
			result: &principalmemory.PrincipalMemoryQueryResult{
				Items: []principalmemory.PrincipalMemoryQueryItem{
					{
						ID:                 88,
						Project:            "project-a",
						Content:            "own private alice memory",
						OwnerPrincipal:     "agent/alice",
						OwnerPrincipalKind: "agent",
						AgentVisibility:    "private",
					},
				},
				AuditStatus: principalmemory.AuditStatusNotRequired,
			},
		}
		service := &Service{principalMemoryQueryService: querySvc}
		id := auth.ClientWithPrincipal("read-write", "keycard-alice", "agent/alice", auth.PrincipalKindAgent)
		req := httptest.NewRequest(http.MethodGet, "/api/memories/principal?project=project-a&principal=agent/alice&principal_kind=agent&include_private=true", nil).
			WithContext(auth.WithIdentity(context.Background(), id))
		w := httptest.NewRecorder()

		service.handlePrincipalMemoryQuery(w, req)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, principalmemory.AuditStatusNotRequired, body["audit_status"])
		require.False(t, querySvc.request.CallerIsAdmin)
		require.Equal(t, "agent/alice", querySvc.request.Caller.Principal)
	})
}

type fakePrincipalMemoryQueryService struct {
	result  *principalmemory.PrincipalMemoryQueryResult
	err     error
	request principalmemory.PrincipalMemoryQueryRequest
	called  bool
}

func (f *fakePrincipalMemoryQueryService) Query(ctx context.Context, req principalmemory.PrincipalMemoryQueryRequest) (*principalmemory.PrincipalMemoryQueryResult, error) {
	f.called = true
	f.request = req
	if f.err != nil {
		return nil, f.err
	}
	if f.result == nil {
		return nil, errors.New("fake result not configured")
	}
	return f.result, nil
}
