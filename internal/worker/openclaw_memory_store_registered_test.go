package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/worker/sse"
	"github.com/thebtf/engram/pkg/models"
	"golang.org/x/crypto/bcrypt"
)

func TestOpenClawRegisteredMemoryStoreRealRouter(t *testing.T) {
	host, compiled := os.Getenv("ENGRAM_OPENCLAW_HOST_FIXTURE"), os.Getenv("ENGRAM_OPENCLAW_TEST_COMPILED")
	if host == "" || compiled == "" {
		t.Skip("requires official openclaw@2026.10.7 and freshly compiled candidate adapter; set ENGRAM_OPENCLAW_HOST_FIXTURE and ENGRAM_OPENCLAW_TEST_COMPILED")
	}
	node, err := exec.LookPath("node")
	require.NoError(t, err, "Node is a fixture prerequisite, not the regression oracle")
	for _, file := range []string{filepath.Join(host, "package.json"), filepath.Join(compiled, "index.js")} {
		_, err := os.Stat(file)
		require.NoError(t, err, "missing fixture prerequisite: %s", file)
	}
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	fixture := filepath.Join(filepath.Dir(source), "..", "..", "plugin", "openclaw-engram", "test", "registered-memory-store.fixture.mjs")

	// No Store constructor, inherited DSN, service startup, migration or live target.
	t.Setenv("DATABASE_DSN", "")
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	db := knowledgeSQLiteDB(t)
	store := &gormdb.Store{DB: db}
	const project = "11111111-1111-4111-8111-111111111111"
	const otherProject = "22222222-2222-4222-8222-222222222222"
	const agentID = "synthetic-openclaw-agent-a"
	const principal = "agent/" + agentID
	const workstation = "synthetic-openclaw-card-a"
	const sessionID = "synthetic-openclaw-conversation"
	for _, id := range []string{project, otherProject} {
		require.NoError(t, db.Create(&gormdb.Project{ID: id}).Error)
	}

	// Only the token lookup is synthetic. Bearer parsing, bcrypt validation and
	// effective auth.Identity derivation use the real HTTP middleware/validator.
	tokens := knowledgeFixtureTokens{}
	newToken := func(card, owner string) string {
		var secret [16]byte
		_, err := rand.Read(secret[:])
		require.NoError(t, err)
		raw := auth.TokenRawPrefix + hex.EncodeToString(secret[:])
		hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.MinCost)
		require.NoError(t, err)
		tokens = append(tokens, gormdb.APIToken{
			ID: card, TokenPrefix: raw[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen], TokenHash: string(hash),
			Scope: "read-write", Principal: owner, PrincipalKind: "agent",
		})
		return raw
	}
	tokenA := newToken(workstation, principal)
	tokenSameAgent := newToken("synthetic-openclaw-card-a-second", principal)
	tokenB := newToken("synthetic-openclaw-card-b", "agent/synthetic-openclaw-agent-b")
	tokenNoPrincipal := newToken("synthetic-openclaw-card-no-principal", "")
	tokenAuth, err := NewTokenAuth(rand.Text())
	require.NoError(t, err)
	require.True(t, tokenAuth.IsEnabled())
	tokenAuth.SetValidator(auth.NewValidator("", tokens))
	service := &Service{
		router: chi.NewRouter(), store: store,
		memoryStore: gormdb.NewMemoryStore(store), sessionStore: gormdb.NewSessionStore(store),
		mcpHealth: mcp.NewMCPHealth(), sseBroadcaster: sse.NewBroadcaster(),
	}
	service.router.Use(tokenAuth.Middleware)
	service.ready.Store(true)
	service.setupRoutes()

	var traceMu sync.Mutex
	var trace []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := httptest.NewRecorder()
		service.router.ServeHTTP(response, r)
		traceMu.Lock()
		trace = append(trace, fmt.Sprintf("%s %s -> HTTP %d", r.Method, r.URL.Path, response.Code))
		traceMu.Unlock()
		for key, values := range response.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	}))
	t.Cleanup(server.Close)
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request := func(t *testing.T, method, route, token string, body any, status int, result any) string {
		t.Helper()
		var encoded []byte
		if body != nil {
			var err error
			encoded, err = json.Marshal(body)
			require.NoError(t, err)
		}
		req, err := http.NewRequest(method, server.URL+route, bytes.NewReader(encoded))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Equal(t, status, response.StatusCode, "%s %s: %s", method, route, payload)
		if result != nil {
			require.NoError(t, json.Unmarshal(payload, result))
		}
		return string(payload)
	}
	read := func(t *testing.T, id int64, project, token string, status int) string {
		t.Helper()
		return request(t, http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", id, project), token, nil, status, nil)
	}

	// One positive control proves real routing, readiness, creation, persistence,
	// metadata and caller-aware reads before any registered-tool RED is accepted.
	const controlContent = "Synthetic canonical handler positive control"
	var control models.Memory
	request(t, http.MethodPost, "/api/memories", tokenA, storeMemoryRequest{
		Project: project, Content: controlContent, Tags: []string{"canonical-control"},
		SourceAgent: agentID, SessionID: sessionID, PrivacyScope: "project", AgentVisibility: "private",
	}, http.StatusCreated, &control)
	var persistedControl gormdb.Memory
	require.NoError(t, db.First(&persistedControl, control.ID).Error)
	require.Equal(t, controlContent, persistedControl.Content)
	require.Equal(t, principal, persistedControl.OwnerPrincipal)
	require.Equal(t, workstation, persistedControl.SourceWorkstationID)
	require.Equal(t, agentID, persistedControl.SourceAgent)
	require.Equal(t, []string{sessionID}, []string(persistedControl.SourceSessions))
	require.Contains(t, read(t, control.ID, project, tokenA, http.StatusOK), controlContent)
	require.Contains(t, read(t, control.ID, project, tokenSameAgent, http.StatusOK), controlContent)
	require.NotContains(t, read(t, control.ID, project, tokenB, http.StatusNotFound), controlContent)
	require.NotContains(t, read(t, control.ID, otherProject, tokenA, http.StatusNotFound), controlContent)
	t.Log("positive control: real canonical memory handler persisted caller metadata and enforced principal/project visibility")

	for _, scenario := range []struct {
		tool    string
		content string
		params  map[string]any
		private bool
		refuse  bool
	}{
		{
			tool: "memory_store", content: "Synthetic project memory from registered memory_store",
			params: map[string]any{"text": "Synthetic project memory from registered memory_store", "title": "Synthetic project note", "category": "decision", "tags": []string{"registered-store"}},
		},
		{
			tool: "memory_store", content: "Synthetic short memory without a supplied title",
			params: map[string]any{"text": "Synthetic short memory without a supplied title", "category": "decision", "tags": []string{"registered-store"}},
		},
		{
			tool: "engram_remember", content: "Synthetic agent-private memory from registered engram_remember", private: true,
			params: map[string]any{"content": "Synthetic agent-private memory from registered engram_remember", "title": "Synthetic private note", "type": "decision", "scope": "agent", "tags": []string{"registered-store"}},
		},
		{
			tool: "engram_remember", content: "Synthetic private memory without authenticated principal", private: true, refuse: true,
			params: map[string]any{"content": "Synthetic private memory without authenticated principal", "title": "Synthetic refused note", "type": "decision", "scope": "agent", "tags": []string{"registered-store"}},
		},
	} {
		name := scenario.tool
		if scenario.refuse {
			name += "_without_principal"
		}
		t.Run(name, func(t *testing.T) {
			var before int64
			require.NoError(t, db.Model(&gormdb.Memory{}).Count(&before).Error)
			callerToken := tokenA
			if scenario.refuse {
				callerToken = tokenNoPrincipal
			}
			home, err := os.MkdirTemp(filepath.Dir(compiled), ".registered-memory-store-home-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(home)) })
			input, err := json.Marshal(map[string]any{
				"url": server.URL, "token": callerToken, "project": project, "agentId": agentID,
				"sessionId": sessionID, "tool": scenario.tool, "params": scenario.params, "expectRefusal": scenario.refuse,
			})
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, node, fixture)
			cmd.Dir = home
			cmd.Stdin = bytes.NewReader(input)
			// Do not inherit NODE_OPTIONS, proxy, DB, Engram/profile credentials or
			// home/config paths. No installed host or existing user profile is used.
			for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "PATHEXT", "COMSPEC"} {
				if value := os.Getenv(key); value != "" {
					cmd.Env = append(cmd.Env, key+"="+value)
				}
			}
			for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "TMP", "TEMP", "TMPDIR"} {
				cmd.Env = append(cmd.Env, key+"="+home)
			}
			cmd.Env = append(cmd.Env,
				"ENGRAM_OPENCLAW_HOST_FIXTURE="+host, "ENGRAM_OPENCLAW_TEST_COMPILED="+compiled,
				"OPENCLAW_STATE_DIR="+filepath.Join(home, "state"), "OPENCLAW_CONFIG_PATH="+filepath.Join(home, "openclaw.json"),
				"OPENCLAW_LOAD_SHELL_ENV=0", "OPENCLAW_NO_RESPAWN=1",
			)
			traceMu.Lock()
			traceStart := len(trace)
			traceMu.Unlock()
			output, runErr := cmd.CombinedOutput()
			traceMu.Lock()
			requests := strings.Join(trace[traceStart:], "\n")
			traceMu.Unlock()
			require.NoError(t, runErr, "registered tool result:\n%s\nreal Go router requests:\n%s", output, requests)
			if scenario.refuse {
				var after int64
				require.NoError(t, db.Model(&gormdb.Memory{}).Count(&after).Error)
				require.Equal(t, before, after, "refused agent-private writes must not persist any row")
				require.Contains(t, requests, "POST /api/memories -> HTTP 400", "the actual handler must reject the missing principal")
				body := request(t, http.MethodGet, "/api/memories?project="+project, tokenB, nil, http.StatusOK, nil)
				require.NotContains(t, body, scenario.content)
				return
			}

			var rows []gormdb.Memory
			require.NoError(t, db.Where("project = ? AND content LIKE ?", project, "%"+scenario.content+"%").Find(&rows).Error)
			require.Len(t, rows, 1, "success text alone is not a persisted memory")
			memory := rows[0]
			require.Equal(t, "active", memory.Status)
			require.Contains(t, []string(memory.Tags), "registered-store")
			require.Contains(t, []string(memory.Tags), "type:decision")
			if title, supplied := scenario.params["title"]; supplied {
				require.Contains(t, memory.Content, title)
			} else {
				require.Equal(t, scenario.content, memory.Content, "an automatic title must not duplicate the body")
			}
			require.Equal(t, principal, memory.OwnerPrincipal, "effective caller must own the memory")
			require.Equal(t, "agent", memory.OwnerPrincipalKind)
			require.Equal(t, workstation, memory.SourceWorkstationID, "keycard attribution must survive the registered tool")
			require.Equal(t, agentID, memory.SourceAgent, "host agent attribution is distinct from authenticated owner")
			require.Equal(t, []string{sessionID}, []string(memory.SourceSessions), "reuse the actual host conversation")
			require.Contains(t, read(t, memory.ID, project, tokenA, http.StatusOK), scenario.content)
			require.Contains(t, read(t, memory.ID, project, tokenSameAgent, http.StatusOK), scenario.content, "agent-private does not mean one-workstation-only")
			if scenario.private {
				require.NotContains(t, read(t, memory.ID, project, tokenB, http.StatusNotFound), scenario.content, "scope=agent must never widen to project/global visibility")
			} else {
				require.Equal(t, "project", memory.PrivacyScope)
				require.Contains(t, read(t, memory.ID, project, tokenB, http.StatusOK), scenario.content, "memory_store promises project scope")
			}
			require.NotContains(t, read(t, memory.ID, otherProject, tokenA, http.StatusNotFound), scenario.content)
		})
	}
}
