package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/pkg/cognitive"
)

type ambientCandidatesRequest struct {
	SessionID  string `json:"session_id"`
	Project    string `json:"project"`
	PromptText string `json:"prompt_text"`
	Limit      int    `json:"limit,omitempty"`
}

type ambientCandidatesResponse struct {
	Hints             []cognitive.HintProposal `json:"hints,omitempty"`
	AdditionalContext string                   `json:"additional_context,omitempty"`
	Disabled          bool                     `json:"disabled,omitempty"`
	Reason            string                   `json:"reason,omitempty"`
}

type candidateProposerStub struct {
	name      string
	proposals []cognitive.HintProposal
	err       error
	delay     time.Duration
	calls     int
	lastLimit int
	lastEvent cognitive.AttentionEvent
}

func (p *candidateProposerStub) Name() string    { return p.name }
func (p *candidateProposerStub) Version() string { return "v1.0.0" }
func (p *candidateProposerStub) Start(context.Context, cognitivecore.Dependencies) error {
	return nil
}
func (p *candidateProposerStub) Stop() error          { return nil }
func (p *candidateProposerStub) Implements() []string { return []string{"CandidateProposer"} }
func (p *candidateProposerStub) Propose(ctx context.Context, event cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error) {
	p.calls++
	p.lastLimit = limit
	p.lastEvent = event
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if p.err != nil {
		return nil, p.err
	}
	out := append([]cognitive.HintProposal(nil), p.proposals...)
	return out, nil
}

type hintEmitterStub struct {
	name        string
	calls       int
	lastSurface cognitive.HintSurface
	lastSession string
	lastHints   []cognitive.HintProposal
	err         error
}

func (e *hintEmitterStub) Name() string    { return e.name }
func (e *hintEmitterStub) Version() string { return "v1.0.0" }
func (e *hintEmitterStub) Start(context.Context, cognitivecore.Dependencies) error {
	return nil
}
func (e *hintEmitterStub) Stop() error          { return nil }
func (e *hintEmitterStub) Implements() []string { return []string{"HintEmitter"} }
func (e *hintEmitterStub) Render(_ context.Context, surface cognitive.HintSurface, sessionID string, hints []cognitive.HintProposal) (cognitive.HintDelivery, error) {
	e.calls++
	e.lastSurface = surface
	e.lastSession = sessionID
	e.lastHints = append([]cognitive.HintProposal(nil), hints...)
	if e.err != nil {
		return cognitive.HintDelivery{}, e.err
	}
	return cognitive.HintDelivery{
		Surface: surface,
		Hints:   append([]cognitive.HintProposal(nil), hints...),
	}, nil
}

func newAmbientHandlerService(t *testing.T) *Service {
	t.Helper()
	meter := cognitivecore.NewLocalMeter()
	bus := cognitivecore.NewAttentionEventBus(meter)
	queue := cognitivecore.NewHintQueue()
	return &Service{
		ctx:               context.Background(),
		cognitiveRegistry: cognitivecore.NewRegistry(),
		cognitiveMeter:    meter,
		cognitiveBus:      bus,
		cognitiveQueue:    queue,
		flagConfig:        cognitivecore.LoadFlagConfigFromEnv(),
	}
}

func postAmbientCandidates(t *testing.T, svc *Service, body ambientCandidatesRequest) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/ambient-candidates", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svc.handleAmbientCandidates(w, req)
	return w
}

func decodeAmbientResponse(t *testing.T, rec *httptest.ResponseRecorder) ambientCandidatesResponse {
	t.Helper()
	var payload ambientCandidatesResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&payload), rec.Body.String())
	return payload
}

type ambientMCPHintResult struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Reason string `json:"reason,omitempty"`
	Source string `json:"source,omitempty"`
}

type ambientMCPHintsResponse struct {
	Hints []ambientMCPHintResult `json:"hints,omitempty"`
}

func pollAmbientHintsViaMCP(t *testing.T, queue cognitivecore.HintQueue, sessionID string) ambientMCPHintsResponse {
	t.Helper()
	srv := mcp.NewServer(mcp.ServerOptions{Version: "s3-red-test"})
	srv.SetHintQueue(queue)
	paramsRaw, err := json.Marshal(map[string]any{
		"name": "get_ambient_hints",
		"arguments": map[string]any{
			"session_id": sessionID,
			"limit":      3,
		},
	})
	require.NoError(t, err)

	resp := srv.HandleRequest(context.Background(), &mcp.Request{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  json.RawMessage(paramsRaw),
	})
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)

	var payload ambientMCPHintsResponse
	text := ambientMCPResponseText(t, resp)
	require.NoError(t, json.Unmarshal([]byte(text), &payload), text)
	return payload
}

func ambientMCPResponseText(t *testing.T, resp *mcp.Response) string {
	t.Helper()
	result, ok := resp.Result.(map[string]any)
	require.True(t, ok, "tools/call must return the standard MCP content envelope")
	switch content := result["content"].(type) {
	case []map[string]any:
		require.Len(t, content, 1)
		text, ok := content[0]["text"].(string)
		require.True(t, ok)
		return text
	case []any:
		require.Len(t, content, 1)
		item, ok := content[0].(map[string]any)
		require.True(t, ok)
		text, ok := item["text"].(string)
		require.True(t, ok)
		return text
	default:
		t.Fatalf("MCP response content had unexpected type %T", result["content"])
		return ""
	}
}

func TestS3AmbientCandidates_EnabledReturnsTopThreeHints(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S3_AMBIENT", "true")

	svc := newAmbientHandlerService(t)
	proposer := &candidateProposerStub{
		name: "test.s3.proposer",
		proposals: []cognitive.HintProposal{
			{ID: "1", Title: "Release handoff checklist", Reason: "tag:handoff", Score: 0.92, Source: "s2.meta_index"},
			{ID: "2", Title: "Retry last failing command", Reason: "outcome:repair", Score: 0.88, Source: "s6.outcome_policy"},
			{ID: "3", Title: "Review PM oracle drift", Reason: "tag:oracle", Score: 0.83, Source: "s2.meta_index"},
			{ID: "4", Title: "Should be trimmed", Reason: "tag:overflow", Score: 0.81, Source: "s2.meta_index"},
		},
	}
	emitter := &hintEmitterStub{name: "test.s3.emitter"}
	require.NoError(t, svc.cognitiveRegistry.Register(proposer))
	require.NoError(t, svc.cognitiveRegistry.Register(emitter))
	require.NoError(t, svc.cognitiveRegistry.Enable(proposer.Name()))
	require.NoError(t, svc.cognitiveRegistry.Enable(emitter.Name()))

	rec := postAmbientCandidates(t, svc, ambientCandidatesRequest{
		SessionID:  "session-s3-enabled",
		Project:    "engram",
		PromptText: "Need same-turn ambient hints for the release handoff",
		Limit:      99,
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	payload := decodeAmbientResponse(t, rec)
	require.Len(t, payload.Hints, 3, "handler must cap same-turn ambient delivery at top 3 hints")
	require.Equal(t, 1, proposer.calls)
	require.Equal(t, 3, proposer.lastLimit, "handler must cap proposer fanout at 3 even if the request asks for more")
	require.Equal(t, "user_prompt_submit", proposer.lastEvent.Type)
	require.Equal(t, "session-s3-enabled", proposer.lastEvent.SessionID)
	require.Equal(t, "engram", proposer.lastEvent.Project)
	require.Equal(t, "Need same-turn ambient hints for the release handoff", proposer.lastEvent.Payload["text"])
	require.Equal(t, 1, emitter.calls, "same-turn route must go through the HintEmitter contract")
	require.Equal(t, cognitive.HintSurfaceMCPPoll, emitter.lastSurface, "hook route should receive structured hints for hook-side formatting")
	require.Len(t, emitter.lastHints, 3)
}

func TestS3AmbientCandidates_DisabledReturnsEmptyHintsWithoutDispatch(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S3_AMBIENT", "false")

	svc := newAmbientHandlerService(t)
	proposer := &candidateProposerStub{name: "test.s3.disabled.proposer"}
	require.NoError(t, svc.cognitiveRegistry.Register(proposer))
	require.NoError(t, svc.cognitiveRegistry.Enable(proposer.Name()))

	rec := postAmbientCandidates(t, svc, ambientCandidatesRequest{
		SessionID:  "session-s3-disabled",
		Project:    "engram",
		PromptText: "Ambient should fail open while disabled",
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	payload := decodeAmbientResponse(t, rec)
	require.Empty(t, payload.Hints, "disabled same-turn ambient route must emit no hints")
	require.Equal(t, 0, proposer.calls, "disabled route must gate before touching proposers")
}

func TestS3AmbientCandidates_TimeoutFailsOpenToEmptyHints(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S3_AMBIENT", "true")

	svc := newAmbientHandlerService(t)
	proposer := &candidateProposerStub{
		name:  "test.s3.slow.proposer",
		delay: 500 * time.Millisecond,
	}
	emitter := &hintEmitterStub{name: "test.s3.emitter"}
	require.NoError(t, svc.cognitiveRegistry.Register(proposer))
	require.NoError(t, svc.cognitiveRegistry.Register(emitter))
	require.NoError(t, svc.cognitiveRegistry.Enable(proposer.Name()))
	require.NoError(t, svc.cognitiveRegistry.Enable(emitter.Name()))

	rec := postAmbientCandidates(t, svc, ambientCandidatesRequest{
		SessionID:  "session-s3-timeout",
		Project:    "engram",
		PromptText: "This route must fail open under the 200 ms budget",
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	payload := decodeAmbientResponse(t, rec)
	require.Empty(t, payload.Hints, "timeout path must degrade to an empty hint payload")
}

func TestS3AmbientCandidates_DrainsQueuedFallbackHintsAfterSuccessfulHookDelivery(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S3_AMBIENT", "true")

	svc := newAmbientHandlerService(t)
	proposer := &candidateProposerStub{
		name: "test.s3.proposer.queue-drain",
		proposals: []cognitive.HintProposal{{
			ID: "hook-1", Title: "Hook delivery wins over queued fallback", Reason: "tag:drain", Score: 0.91, Source: "s2.meta_index",
		}},
	}
	emitter := &hintEmitterStub{name: "test.s3.emitter.queue-drain"}
	require.NoError(t, svc.cognitiveRegistry.Register(proposer))
	require.NoError(t, svc.cognitiveRegistry.Register(emitter))
	require.NoError(t, svc.cognitiveRegistry.Enable(proposer.Name()))
	require.NoError(t, svc.cognitiveRegistry.Enable(emitter.Name()))
	require.NoError(t, svc.cognitiveQueue.Enqueue(context.Background(), "session-s3-queue-drain", cognitivecore.HintProposalPayload{
		ID: "queued-1", Title: "Queued fallback copy", Reason: "tag:queued", Score: 0.44, Source: "s2.meta_index", CreatedAt: time.Now().UTC(),
	}))

	rec := postAmbientCandidates(t, svc, ambientCandidatesRequest{
		SessionID:  "session-s3-queue-drain",
		Project:    "engram",
		PromptText: "Deliver same-turn hints and clear the fallback queue",
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	payload := decodeAmbientResponse(t, rec)
	require.Len(t, payload.Hints, 1)
	require.Equal(t, 0, svc.cognitiveQueue.Stats("session-s3-queue-drain").QueuedNow, "same-turn hook delivery must clear queued fallback hints so MCP polling cannot replay them")
	require.Empty(t, svc.cognitiveQueue.Drain("session-s3-queue-drain", 3))
}

func TestS3AmbientCandidates_EmptyHookDeliveryPreservesQueuedFallbackHintsForMCPPoll(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S3_AMBIENT", "true")

	svc := newAmbientHandlerService(t)
	proposer := &candidateProposerStub{name: "test.s3.proposer.empty-hook"}
	emitter := &hintEmitterStub{name: "test.s3.emitter.empty-hook"}
	require.NoError(t, svc.cognitiveRegistry.Register(proposer))
	require.NoError(t, svc.cognitiveRegistry.Register(emitter))
	require.NoError(t, svc.cognitiveRegistry.Enable(proposer.Name()))
	require.NoError(t, svc.cognitiveRegistry.Enable(emitter.Name()))
	require.NoError(t, svc.cognitiveQueue.Enqueue(context.Background(), "session-s3-empty-hook", cognitivecore.HintProposalPayload{
		ID: "queued-after-empty-hook", Title: "Queued fallback survives empty hook", Reason: "tag:fallback", Score: 0.74, Source: "s2.meta_index", CreatedAt: time.Now().UTC(),
	}))

	rec := postAmbientCandidates(t, svc, ambientCandidatesRequest{
		SessionID:  "session-s3-empty-hook",
		Project:    "engram",
		PromptText: "Same-turn hook renders no ambient copy, so fallback polling still needs queued hints",
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	payload := decodeAmbientResponse(t, rec)
	require.Empty(t, payload.Hints, "empty same-turn hook delivery must not synthesize structured hints")
	require.Empty(t, payload.AdditionalContext, "empty same-turn hook delivery must not synthesize prompt context")

	fallback := pollAmbientHintsViaMCP(t, svc.cognitiveQueue, "session-s3-empty-hook")
	require.Len(t, fallback.Hints, 1, "empty same-turn hook delivery must not drain queued fallback hints before MCP polling can read them")
	require.Equal(t, "queued-after-empty-hook", fallback.Hints[0].ID)
	require.Equal(t, "Queued fallback survives empty hook", fallback.Hints[0].Title)
}
