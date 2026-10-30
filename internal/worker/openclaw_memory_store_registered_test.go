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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/worker/sse"
	"github.com/thebtf/engram/pkg/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
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

	// No inherited DSN or service startup. PostgreSQL is used only through the
	// explicit disposable-fixture opt-in, never an ordinary DATABASE_DSN.
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	db := openClawMemoryFixtureDB(t)
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

// Allocate at the top-level test so PostgreSQL migrations run once, not once
// per scenario. Without the explicit disposable DSN, retain the SQLite suite.
func openClawMemoryFixtureDB(t *testing.T) *gorm.DB {
	t.Helper()
	if os.Getenv("ENGRAM_MEMORY_MUTATION_TEST_DSN") != "" {
		return memoryMutationPostgresDB(t)
	}
	t.Setenv("DATABASE_DSN", "")
	return memoryMutationSQLiteDB(t)
}

type openClawLifecycleFixture struct {
	*memoryMutationSecurityFixture
	server  *httptest.Server
	client  *http.Client
	traceMu sync.Mutex
	trace   []string
}

func newOpenClawLifecycleFixture(t *testing.T, db *gorm.DB) *openClawLifecycleFixture {
	t.Helper()
	// The selected fixture executes real authorization, routing and storage.
	// Only a PostgreSQL run provides PostgreSQL operator and locking evidence.
	f := &openClawLifecycleFixture{
		memoryMutationSecurityFixture: newMemoryMutationSecurityFixture(t, db),
	}
	// The context search route uses real config and memory storage, without
	// retrieval hooks, fake search results or a running background service.
	f.service.config = &config.Config{ContextMaxPromptResults: 20}
	f.service.retrievalStats = make(map[string]*RetrievalStats)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := httptest.NewRecorder()
		f.service.router.ServeHTTP(response, r)
		f.traceMu.Lock()
		f.trace = append(f.trace, fmt.Sprintf("%s %s -> HTTP %d", r.Method, r.URL.Path, response.Code))
		f.traceMu.Unlock()
		for key, values := range response.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	}))
	t.Cleanup(f.server.Close)
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	f.client = &http.Client{
		Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return f
}

func (f *openClawLifecycleFixture) call(t *testing.T, method, route, token string, body any) (int, string) {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		require.NoError(t, err)
	}
	req, err := http.NewRequest(method, f.server.URL+route, bytes.NewReader(encoded))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Engram-Project-Identity-Adapter", "openclaw")
	response, err := f.client.Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(payload)
}

func (f *openClawLifecycleFixture) rawRow(t *testing.T, id int64) gormdb.Memory {
	t.Helper()
	var rows []gormdb.Memory
	require.NoError(t, f.db.Unscoped().Where("id = ?", id).Find(&rows).Error)
	require.Len(t, rows, 1, "Lifecycle operations must retain the memory row, not hard-delete it")
	return rows[0]
}

func (f *openClawLifecycleFixture) activeList(t *testing.T, project, token string) string {
	t.Helper()
	status, body := f.call(t, http.MethodGet, "/api/memories?project="+project+"&limit=500", token, nil)
	require.Equal(t, http.StatusOK, status, body)
	return body
}

func (f *openClawLifecycleFixture) logOwnerListMiss(t *testing.T, seed gormdb.Memory) {
	t.Helper()
	defer t.Logf("owner-list-miss original seed: id=%d status=%q not_deleted=%t valid_from=%v valid_until=%v",
		seed.ID, seed.Status, seed.DeletedAt == nil, seed.ValidFrom, seed.ValidUntil)
	if os.Getenv("ENGRAM_MEMORY_MUTATION_TEST_DSN") == "" {
		t.Log("owner-list-miss: PostgreSQL connection/clocks unavailable in the SQLite fixture")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// This single later read is not the failed list's connection or clock.
	// It never repeats the list, reauthenticates or changes the seed.
	var snapshot string
	err := f.db.WithContext(ctx).Raw(`SELECT json_build_object(
		'phase', 'later_read_after_owner_list_miss',
		'schema', current_schema(), 'backend_pid', pg_backend_pid(),
		'id', id, 'status', status, 'not_deleted', deleted_at IS NULL,
		'valid_from', valid_from, 'valid_until', valid_until,
		'transaction_clock', transaction_timestamp(),
		'statement_clock', statement_timestamp(), 'wall_clock', clock_timestamp()
	)::text FROM memories WHERE id = ?`, seed.ID).Scan(&snapshot).Error
	// Only the error's type is logged, never its potentially sensitive text.
	t.Logf("owner-list-miss later PostgreSQL snapshot: %s; error_type=%T", snapshot, err)
}

func (f *openClawLifecycleFixture) searchIDs(t *testing.T, project, token, query string) []int64 {
	t.Helper()
	status, body := f.call(t, http.MethodPost, "/api/context/search", token, map[string]any{
		"project": project, "query": query,
	})
	require.Equal(t, http.StatusOK, status, body)
	var result struct {
		Observations []struct {
			ID int64 `json:"id"`
		} `json:"observations"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &result))
	ids := make([]int64, 0, len(result.Observations))
	for _, observation := range result.Observations {
		ids = append(ids, observation.ID)
	}
	return ids
}

func (f *openClawLifecycleFixture) seedPrivate(t *testing.T, token string) gormdb.Memory {
	t.Helper()
	memory := f.seed(t, token, "project", "private", "")
	require.Equal(t, "active", memory.Status)
	require.Nil(t, memory.DeletedAt)
	require.Empty(t, memory.Domain)
	status, body := f.call(t, http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", memory.ID, f.project), token, nil)
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, memory.Content)
	list := f.activeList(t, f.project, token)
	if !strings.Contains(list, memory.Content) {
		f.logOwnerListMiss(t, memory)
	}
	require.Contains(t, list, memory.Content, "The owner must see the seed before mutation")
	require.Contains(t, f.searchIDs(t, f.project, token, memory.Content), memory.ID, "Real context search must find the unique seed before mutation")
	return memory
}

func (f *openClawLifecycleFixture) assertOwnerState(t *testing.T, before gormdb.Memory, status string, increments int) {
	t.Helper()
	after := f.rawRow(t, before.ID)
	assert.Equal(t, status, after.Status)
	assert.Equal(t, before.DeletedAt, after.DeletedAt, "Suppress, restore and archive must preserve deleted_at")
	assert.Equal(t, before.Version+increments, after.Version, "Each successful transition must increment the version once")
	assert.Equal(t, before.Content, after.Content)
	assert.Equal(t, before.OwnerPrincipal, after.OwnerPrincipal)
	assert.Equal(t, before.Project, after.Project)
	detailStatus, detail := f.call(t, http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", before.ID, f.project), f.tokenA, nil)
	assert.Equal(t, http.StatusOK, detailStatus, "All nondeleted statuses retain owner detail access: %s", detail)
	assert.Contains(t, detail, before.Content)
	list := f.activeList(t, f.project, f.tokenA)
	search := f.searchIDs(t, f.project, f.tokenA, before.Content)
	if status == "active" {
		assert.Contains(t, list, before.Content, "Restoration must return the memory to the active list")
		assert.Contains(t, search, before.ID, "Restoration must return the target to actual context search")
	} else {
		assert.NotContains(t, list, before.Content, "Flagged and archived memories must leave the active list")
		assert.NotContains(t, search, before.ID, "Flagged and archived targets must be absent from actual context search")
	}
}

func (f *openClawLifecycleFixture) runRegisteredLifecycleTool(t *testing.T, tool, operation, project, token string, params map[string]any, refusal bool, conflict ...bool) string {
	t.Helper()
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
	home, err := os.MkdirTemp(filepath.Dir(compiled), ".registered-memory-lifecycle-home-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(home)) })
	agent := "synthetic-security-a"
	if token == f.tokenB {
		agent = "synthetic-security-b"
	}
	input, err := json.Marshal(map[string]any{
		"url": f.server.URL, "token": token, "project": project, "agentId": agent,
		"sessionId": "synthetic-security-session", "tool": tool, "params": params,
		"operation": operation, "expectRefusal": refusal,
		"expectConflict": len(conflict) > 0 && conflict[0],
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, fixture)
	cmd.Dir = home
	cmd.Stdin = bytes.NewReader(input)
	// No inherited profile, credentials, proxy, DB configuration or NODE_OPTIONS.
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
	f.traceMu.Lock()
	start := len(f.trace)
	f.traceMu.Unlock()
	output, runErr := cmd.CombinedOutput()
	f.traceMu.Lock()
	trace := strings.Join(f.trace[start:], "\n")
	f.traceMu.Unlock()
	t.Logf("registered %s %s result:\n%s\nreal Go router requests:\n%s", tool, operation, output, trace)
	assert.NoError(t, runErr, "The registered tool must report the expected lifecycle outcome")
	assert.NotEmpty(t, trace, "The registered call must reach the actual HTTP router")
	return string(output)
}

func TestOpenClawMemoryTransitionRealRouter(t *testing.T) {
	db := openClawMemoryFixtureDB(t)
	t.Run("owner_without_project", func(t *testing.T) {
		f := newOpenClawLifecycleFixture(t, db)
		target := f.seedPrivate(t, f.tokenA)
		witness := f.seedPrivate(t, f.tokenB)
		// The seed's owner detail/list/search controls succeed before exercising
		// the ID-only route. A project query must not be required for this caller.
		for i, transition := range []struct{ action, status string }{
			{"suppress", "flagged"},
			{"unsuppress", "active"},
			{"archive", "archived"},
		} {
			status, body := f.call(t, http.MethodPost, fmt.Sprintf("/api/memories/%d/transition", target.ID), f.tokenA, map[string]any{"action": transition.action})
			require.Equal(t, http.StatusOK, status, "Authorized ID-only %s must resolve canonical membership: %s", transition.action, body)
			f.assertOwnerState(t, target, transition.status, i+1)
			assert.Equal(t, witness, f.rawRow(t, witness.ID), "ID-only transitions must preserve the foreign-principal witness")
		}
	})

	for _, selector := range []string{"not-a-canonical-project", "global", "self"} {
		t.Run("invalid_explicit_project/"+selector, func(t *testing.T) {
			f := newOpenClawLifecycleFixture(t, db)
			target := f.seedPrivate(t, f.tokenA)
			witness := f.seedPrivate(t, f.tokenB)
			status, body := f.call(t, http.MethodPost, fmt.Sprintf("/api/memories/%d/transition?project=%s", target.ID, selector), f.tokenA, map[string]any{"action": "suppress"})
			assert.Contains(t, []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound}, status, "An invalid explicit selector must refuse rather than fall back to global/self: %s", body)
			assert.NotContains(t, body, target.Content)
			assert.NotContains(t, body, target.OwnerPrincipal)
			assert.Equal(t, target, f.rawRow(t, target.ID), "Invalid explicit selectors must preserve every target field")
			assert.Equal(t, witness, f.rawRow(t, witness.ID), "Invalid explicit selectors must preserve the witness")
			f.assertOwnerState(t, target, "active", 0)
		})
	}

	t.Run("revoked_key", func(t *testing.T) {
		f := newOpenClawLifecycleFixture(t, db)
		target := f.seedPrivate(t, f.tokenA)
		witness := f.seedPrivate(t, f.tokenB)
		// Bind the existing validator to the real token store. The ordinary
		// knowledgeFixtureTokens lookup does not filter revoked tokens, so merely
		// setting Revoked on that synthetic lookup would not prove revocation.
		require.NoError(t, f.db.Exec(`CREATE TABLE IF NOT EXISTS api_tokens (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, token_hash TEXT NOT NULL,
			token_prefix TEXT NOT NULL, scope TEXT NOT NULL, principal TEXT NOT NULL,
			principal_kind TEXT NOT NULL, expires_at TIMESTAMP, created_at TIMESTAMP,
			last_used_at TIMESTAMP, request_count INTEGER, error_count INTEGER,
			revoked BOOLEAN NOT NULL, revoked_at TIMESTAMP
		)`).Error)
		hash, err := bcrypt.GenerateFromPassword([]byte(f.tokenA), bcrypt.MinCost)
		require.NoError(t, err)
		card := gormdb.APIToken{
			ID: "33333333-3333-4333-8333-333333333333", Name: "Synthetic lifecycle revocation control",
			TokenHash: string(hash), TokenPrefix: f.tokenA[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen],
			Scope: "read-write", Principal: target.OwnerPrincipal, PrincipalKind: "agent", CreatedAt: time.Now().UTC(),
		}
		require.NoError(t, f.db.Create(&card).Error)
		tokens := gormdb.NewTokenStore(&gormdb.Store{DB: f.db})
		f.tokenAuth.SetValidator(auth.NewValidator("", tokens))
		route := fmt.Sprintf("/api/memories/%d?project=%s", target.ID, f.project)
		status, body := f.call(t, http.MethodGet, route, f.tokenA, nil)
		require.Equal(t, http.StatusOK, status, "The same real-store key must authenticate before revocation: %s", body)
		require.Contains(t, body, target.Content)
		require.NoError(t, tokens.Revoke(context.Background(), card.ID))
		var revoked gormdb.APIToken
		require.NoError(t, f.db.First(&revoked, "id = ?", card.ID).Error)
		require.True(t, revoked.Revoked)
		require.NotNil(t, revoked.RevokedAt)
		for _, action := range []string{"suppress", "unsuppress", "archive"} {
			status, body = f.call(t, http.MethodPost, fmt.Sprintf("/api/memories/%d/transition", target.ID), f.tokenA, map[string]any{"action": action})
			assert.Equal(t, http.StatusUnauthorized, status, "Revoked keys must fail in real bearer middleware before %s: %s", action, body)
			assert.NotContains(t, body, target.Content)
			assert.NotContains(t, body, target.OwnerPrincipal)
			assert.Equal(t, target, f.rawRow(t, target.ID))
			assert.Equal(t, witness, f.rawRow(t, witness.ID))
		}
		status, body = f.call(t, http.MethodGet, route, f.tokenA, nil)
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.NotContains(t, body, target.Content)
	})
}

func TestOpenClawRegisteredMemoryLifecycleRealRouter(t *testing.T) {
	db := openClawMemoryFixtureDB(t)
	for _, scenario := range []struct{ name, tool, operation string }{
		{"engram_suppress", "engram_suppress", "suppress"},
		{"memory_forget_default", "memory_forget", "suppress"},
		{"memory_forget_permanent_active", "memory_forget", "archive"},
		{"memory_forget_permanent_flagged", "memory_forget", "archive"},
	} {
		t.Run(scenario.name+"/owner_success", func(t *testing.T) {
			f := newOpenClawLifecycleFixture(t, db)
			target := f.seedPrivate(t, f.tokenA)
			witness := f.seedPrivate(t, f.tokenB)
			params := map[string]any{"id": fmt.Sprint(target.ID)}
			if scenario.tool == "engram_suppress" {
				params["id"] = target.ID
			}
			increments := 1
			if scenario.name == "memory_forget_permanent_flagged" {
				f.runRegisteredLifecycleTool(t, "engram_suppress", "suppress", f.project, f.tokenA, map[string]any{"id": target.ID}, false)
				f.assertOwnerState(t, target, "flagged", 1)
				increments++
			}
			if scenario.operation == "archive" {
				params["permanent"] = true
			}
			f.runRegisteredLifecycleTool(t, scenario.tool, scenario.operation, f.project, f.tokenA, params, false)
			if scenario.operation == "archive" {
				f.assertOwnerState(t, target, "archived", increments)
			} else {
				f.assertOwnerState(t, target, "flagged", 1)
				// This is an input to an existing public tool, not a new test-only API.
				f.runRegisteredLifecycleTool(t, "engram_suppress", "unsuppress", f.project, f.tokenA, map[string]any{"id": target.ID, "action": "unsuppress"}, false)
				f.assertOwnerState(t, target, "active", 2)
			}
			assert.Equal(t, witness, f.rawRow(t, witness.ID), "Owner transitions must preserve the foreign-principal witness")
			assert.NotContains(t, f.activeList(t, f.project, f.tokenB), target.Content)
			assert.NotContains(t, f.searchIDs(t, f.project, f.tokenB, target.Content), target.ID)
		})
	}

	for _, scenario := range []struct{ name, tool, operation string }{
		{"engram_suppress", "engram_suppress", "suppress"},
		{"memory_forget_default", "memory_forget", "suppress"},
		{"memory_forget_permanent", "memory_forget", "archive"},
		{"engram_unsuppress", "engram_suppress", "unsuppress"},
	} {
		for _, caller := range []string{"foreign_principal", "alternate_project"} {
			t.Run(scenario.name+"/"+caller+"_refused", func(t *testing.T) {
				f := newOpenClawLifecycleFixture(t, db)
				target := f.seedPrivate(t, f.tokenA)
				witness := f.seedPrivate(t, f.tokenB)
				if scenario.operation == "unsuppress" {
					// Arrange a flagged input independently of the owner suppress RED.
					require.NoError(t, f.db.Model(&gormdb.Memory{}).Where("id = ?", target.ID).Update("status", "flagged").Error)
					target = f.rawRow(t, target.ID)
				}
				token, project := f.tokenB, f.project
				if caller == "alternate_project" {
					token, project = f.tokenA, f.other
				}
				detailStatus, detail := f.call(t, http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", target.ID, project), token, nil)
				require.Equal(t, http.StatusNotFound, detailStatus, detail)
				require.NotContains(t, detail, target.Content)
				require.NotContains(t, f.activeList(t, project, token), target.Content)
				params := map[string]any{"id": fmt.Sprint(target.ID)}
				if scenario.tool == "engram_suppress" {
					params["id"] = target.ID
				}
				if scenario.operation == "archive" {
					params["permanent"] = true
				}
				if scenario.operation == "unsuppress" {
					params["action"] = "unsuppress"
				}
				output := f.runRegisteredLifecycleTool(t, scenario.tool, scenario.operation, project, token, params, true)
				assert.NotContains(t, output, target.Content)
				assert.NotContains(t, output, target.OwnerPrincipal)
				assert.NotContains(t, output, witness.Content)
				assert.Equal(t, target, f.rawRow(t, target.ID), "Denied calls must preserve every target field, including status, version and deleted_at")
				assert.Equal(t, witness, f.rawRow(t, witness.ID), "Denied calls must preserve the unrelated foreign-principal witness")
				assert.NotContains(t, f.activeList(t, project, token), target.Content)
				assert.NotContains(t, f.searchIDs(t, project, token, target.Content), target.ID)
				detailStatus, detail = f.call(t, http.MethodGet, fmt.Sprintf("/api/memories/%d?project=%s", target.ID, project), token, nil)
				assert.Equal(t, http.StatusNotFound, detailStatus, detail)
				assert.NotContains(t, detail, target.Content)
			})
		}
	}
}
