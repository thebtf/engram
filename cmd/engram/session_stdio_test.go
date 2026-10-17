package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/auth"
	dbstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/projectidentity"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
	"modernc.org/sqlite"
)

const (
	memoryTestProjectA = "33333333-3333-4333-8333-333333333333"
	memoryTestProjectB = "44444444-4444-4444-8444-444444444444"
	// Public synthetic auth-test keycards, never installed credentials.
	memoryTestTokenA = "engram_facade00000000000000000000000aaa"
	memoryTestTokenB = "engram_facade00000000000000000000000bbb"
)

type memoryTestTokens []dbstore.APIToken

func (tokens memoryTestTokens) FindByPrefix(_ context.Context, prefix string) ([]dbstore.APIToken, error) {
	if prefix == "facade00" {
		return tokens, nil
	}
	return nil, nil
}

type memoryTestBackend struct {
	server    *grpc.Server
	mcp       *mcp.Server
	db        *gorm.DB
	url       string
	started   chan string
	cancelled chan string
}

var memoryTestSQLite sync.Once

func newMemoryTestBackend(t *testing.T) *memoryTestBackend {
	t.Helper()
	memoryTestSQLite.Do(func() {
		if err := sqlite.RegisterScalarFunction("now", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
			return time.Now().UTC().Format("2006-01-02 15:04:05"), nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	// ponytail: one SQLite connection; PostgreSQL locking is outside this fixture.
	sqlDB.SetMaxOpenConns(1)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{SkipDefaultTransaction: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []any{dbstore.Memory{}} {
		parsed, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatal(err)
		}
		columns := make([]string, 0, len(parsed.Fields))
		for _, field := range parsed.Fields {
			kind := "TEXT"
			switch field.DataType {
			case schema.Int, schema.Uint:
				kind = "INTEGER"
			case schema.Float:
				kind = "REAL"
			case schema.Bool:
				kind = "BOOLEAN"
			case schema.Time, schema.DataType("timestamptz"):
				kind = "TIMESTAMP"
			}
			if field.PrimaryKey {
				kind += " PRIMARY KEY"
			}
			columns = append(columns, fmt.Sprintf("%q %s", field.DBName, kind))
		}
		if err := db.Exec(fmt.Sprintf("CREATE TABLE %q (%s)", parsed.Table, strings.Join(columns, ","))).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []dbstore.Memory{
		{ID: 91001, Project: memoryTestProjectA, Content: "fixture-public-a", PrivacyScope: "project"},
		{ID: 91002, Project: memoryTestProjectA, Content: "fixture-private-a", PrivacyScope: "private", SourceWorkstationID: "fixture-card-a", SourceSessions: []string{"fixture-a"}, OwnerPrincipal: "agent/local-a", OwnerPrincipalKind: "agent", AgentVisibility: "private"},
		{ID: 91003, Project: memoryTestProjectA, Content: "fixture-foreign-workstation", PrivacyScope: "private", SourceWorkstationID: "fixture-card-b", SourceSessions: []string{"fixture-a"}},
		{ID: 91004, Project: memoryTestProjectA, Content: "fixture-foreign-principal", PrivacyScope: "project", OwnerPrincipal: "agent/local-b", OwnerPrincipalKind: "agent", AgentVisibility: "private"},
		{ID: 91005, Project: memoryTestProjectA, Content: "fixture-foreign-domain", PrivacyScope: "project", Domain: "owned-domain", OwnerPrincipal: "agent/local-b", OwnerPrincipalKind: "agent", AgentVisibility: "shared"},
		{ID: 92001, Project: memoryTestProjectB, Content: "fixture-public-b", PrivacyScope: "project"},
		{ID: 92002, Project: memoryTestProjectB, Content: "fixture-private-b", PrivacyScope: "private", SourceWorkstationID: "fixture-card-b", OwnerPrincipal: "agent/local-b", OwnerPrincipalKind: "agent", AgentVisibility: "private"},
		{ID: 92003, Project: memoryTestProjectB, Content: "fixture-foreign-principal-a", PrivacyScope: "project", OwnerPrincipal: "agent/local-a", OwnerPrincipalKind: "agent", AgentVisibility: "private"},
	} {
		row.Status, row.Version = "active", 1
		row.CreatedAt, row.UpdatedAt = time.Now().UTC(), time.Now().UTC()
		row.Tags = []string{}
		if row.SourceSessions == nil {
			row.SourceSessions = []string{}
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	tokens := memoryTestTokens{}
	for _, value := range []struct{ raw, id, principal string }{{memoryTestTokenA, "fixture-card-a", "agent/local-a"}, {memoryTestTokenB, "fixture-card-b", "agent/local-b"}} {
		hash, err := bcrypt.GenerateFromPassword([]byte(value.raw), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, dbstore.APIToken{ID: value.id, TokenHash: string(hash), Scope: "read-only", Principal: value.principal, PrincipalKind: "agent"})
	}
	fixture := &memoryTestBackend{db: db, mcp: mcp.NewServer(mcp.ServerOptions{Version: "local-handler-fixture"}), started: make(chan string, 32), cancelled: make(chan string, 32)}
	fixture.mcp.SetMemoryStore(dbstore.NewMemoryStore(&dbstore.Store{DB: db}))
	server, internal := grpcserver.New(fixture, auth.NewValidator("", tokens))
	fixture.server = server
	internal.SetDB(db)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture.url = "http://" + listener.Addr().String()
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return fixture
}

func (f *memoryTestBackend) HandleToolCall(ctx context.Context, name string, args []byte) ([]byte, bool, error) {
	var request struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal(args, &request)
	if name == "recall" && request.Query == "__sessionlocal_block__" {
		identity, _ := auth.IdentityFrom(ctx)
		f.started <- identity.Principal
		<-ctx.Done()
		f.cancelled <- identity.Principal
		return nil, false, ctx.Err()
	}
	params, err := json.Marshal(map[string]any{"name": name, "arguments": json.RawMessage(args)})
	if err != nil {
		return nil, false, err
	}
	response := f.mcp.HandleRequest(ctx, &mcp.Request{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params})
	if response == nil {
		return nil, false, errors.New("no MCP response")
	}
	if response.Error != nil {
		raw, err := json.Marshal(response.Error)
		return raw, true, err
	}
	raw, err := json.Marshal(response.Result)
	return raw, false, err
}

func (f *memoryTestBackend) ToolDefinitions() []grpcserver.ToolDef {
	var definitions []grpcserver.ToolDef
	for _, tool := range f.mcp.ListTools() {
		raw, _ := json.Marshal(tool.InputSchema)
		definitions = append(definitions, grpcserver.ToolDef{Name: tool.Name, Description: tool.Description, InputSchemaJSON: raw})
	}
	// Expose an excluded tool to prove the client does not advertise the full backend.
	return append(definitions, grpcserver.ToolDef{Name: "codebase_search", InputSchemaJSON: []byte(`{"type":"object"}`)})
}
func (f *memoryTestBackend) ServerInfo() (string, string) { return "engram", f.mcp.Version() }

type memoryTestClient struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output io.ReadCloser
	frames chan []byte
	exited chan error
	stderr bytes.Buffer
	closed bool
}

func startMemoryTestClient(t *testing.T, binary, root, installation, serverURL, token string, extra ...string) *memoryTestClient {
	t.Helper()
	return startMemoryTestClientWithOutput(t, binary, root, installation, serverURL, token, true, extra...)
}

func startMemoryTestClientWithOutput(t *testing.T, binary, root, installation, serverURL, token string, readOutput bool, extra ...string) *memoryTestClient {
	t.Helper()
	command := exec.Command(binary, sessionMemoryFlag)
	command.Dir = root
	// Clear inherited overrides; each installation uses the real file resolver.
	command.Env = append(os.Environ(), "ENGRAM_URL="+serverURL, "ENGRAM_SERVER_URL=", "ENGRAM_TOKEN="+token, "ENGRAM_CLIENT_INSTANCE_ID=", "ENGRAM_DATA_DIR="+installation, "ENGRAM_TLS_CA=", "CLAUDE_SESSION_ID=fixture-a")
	command.Env = append(command.Env, extra...)
	client := &memoryTestClient{cmd: command, frames: make(chan []byte, 32), exited: make(chan error, 1)}
	var err error
	client.input, err = command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	client.output, err = command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = &client.stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if readOutput {
		go func() {
			scanner := bufio.NewScanner(client.output)
			scanner.Buffer(make([]byte, 4096), sessionMemoryFrameBytes+2)
			for scanner.Scan() {
				client.frames <- bytes.Clone(scanner.Bytes())
			}
			close(client.frames)
		}()
	}
	go func() { client.exited <- command.Wait() }()
	t.Cleanup(func() {
		if !client.closed {
			client.close(t, false)
		}
	})
	return client
}

func (c *memoryTestClient) send(t *testing.T, id any, method string, params any) {
	t.Helper()
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		req["id"] = id
	}
	if params != nil {
		req["params"] = params
	}
	if err := json.NewEncoder(c.input).Encode(req); err != nil {
		t.Fatal(err)
	}
}

func (c *memoryTestClient) receive(t *testing.T) map[string]any {
	t.Helper()
	select {
	case raw, ok := <-c.frames:
		if !ok {
			t.Fatalf("stdio closed before response: %s", c.stderr.String())
		}
		var response map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&response); err != nil {
			t.Fatalf("bad output frame: %s", raw)
		}
		return response
	case <-time.After(10 * time.Second):
		t.Fatal("stdio response timeout")
	}
	return nil
}

func (c *memoryTestClient) call(t *testing.T, id any, method string, params any) map[string]any {
	t.Helper()
	c.send(t, id, method, params)
	response := c.receive(t)
	want, _ := json.Marshal(id)
	got, _ := json.Marshal(response["id"])
	if !bytes.Equal(want, got) {
		t.Fatalf("response ID %s, want %s", got, want)
	}
	return response
}

func (c *memoryTestClient) close(t *testing.T, wantFailure bool) {
	t.Helper()
	if c.closed {
		return
	}
	c.closed = true
	_ = c.input.Close()
	select {
	case err := <-c.exited:
		if (err != nil) != wantFailure {
			t.Fatalf("stdio exit: %v (want failure %v): %s", err, wantFailure, c.stderr.String())
		}
	case <-time.After(10 * time.Second):
		// Own test child only, never a shared daemon.
		if c.cmd != nil {
			_ = c.cmd.Process.Kill()
		} else {
			_ = c.output.Close()
		}
		<-c.exited
		t.Fatal("stdio did not exit after teardown")
	}
}

func (c *memoryTestClient) waitForSignalExit(t *testing.T) {
	t.Helper()
	// Do not close stdin to make this test pass: the child must exit first.
	select {
	case err := <-c.exited:
		c.closed = true
		_ = c.input.Close()
		if err != nil {
			t.Fatalf("stdio signal exit: %v: %s", err, c.stderr.String())
		}
	case <-time.After(10 * time.Second):
		_ = c.cmd.Process.Kill()
		<-c.exited
		c.closed = true
		_ = c.input.Close()
		t.Fatal("stdio did not exit after signal with stdin still open")
	}
}

func memoryTestUnwrap(value map[string]any) map[string]any {
	if result, ok := value["result"].(map[string]any); ok {
		return memoryTestUnwrap(result)
	}
	if content, ok := value["content"].([]any); ok && len(content) == 1 {
		if block, ok := content[0].(map[string]any); ok {
			if text, ok := block["text"].(string); ok {
				var nested map[string]any
				if json.Unmarshal([]byte(text), &nested) == nil {
					return memoryTestUnwrap(nested)
				}
			}
		}
	}
	return value
}

func memoryTestRefused(response map[string]any) bool {
	if response["error"] != nil || response["isError"] == true {
		return true
	}
	inner := memoryTestUnwrap(response)
	return inner["error"] != nil || inner["isError"] == true
}

func memoryTestInitialize(t *testing.T, client *memoryTestClient) {
	t.Helper()
	response := client.call(t, "initialize", "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "memory-transport-regression", "version": "1"}})
	result := memoryTestUnwrap(response)
	if result["protocolVersion"] != "2024-11-05" || result["instructions"] != sessionMemoryDescription {
		t.Fatalf("initialize: %v", response)
	}
	client.send(t, nil, "notifications/initialized", nil)
	response = client.call(t, "list", "tools/list", nil)
	if memoryTestRefused(response) {
		t.Fatalf("tools/list: %v", response)
	}
	tools, ok := memoryTestUnwrap(response)["tools"].([]any)
	if !ok {
		t.Fatalf("missing inventory: %v", response)
	}
	found := false
	for _, tool := range tools {
		name := tool.(map[string]any)["name"].(string)
		if name == "recall" {
			found = true
		}
		if !sessionMemoryTool(name) {
			t.Fatalf("non-memory tool advertised: %s", name)
		}
	}
	if !found {
		t.Fatal("real recall not advertised")
	}
}

func memoryTestRecall(t *testing.T, client *memoryTestClient, project, supplied string, want []int64) {
	t.Helper()
	args := map[string]any{"project": supplied, "query": "", "limit": 20, "owner_principal": "agent/local-b"}
	if project == memoryTestProjectA {
		args["session_id"] = "fixture-a"
	}
	response := client.call(t, "recall", "tools/call", map[string]any{"name": "recall", "arguments": args})
	if memoryTestRefused(response) {
		t.Fatalf("recall: %v", response)
	}
	rows, ok := memoryTestUnwrap(response)["memories"].([]any)
	if !ok {
		t.Fatalf("missing real records: %v", response)
	}
	var ids []int64
	for _, value := range rows {
		row := value.(map[string]any)
		if row["project"] != project {
			t.Fatalf("foreign scope leaked: %v", row)
		}
		ids = append(ids, int64(row["id"].(float64)))
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("record IDs %v want %v", ids, want)
	}
}

func memoryTestEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("backend principal %q want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("backend lifecycle event timeout")
	}
}

func memoryTestRepository(t *testing.T, parent, name, project string) string {
	t.Helper()
	root := filepath.Join(parent, name)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	marker, err := json.Marshal(projectidentity.Project{ProjectID: project, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".engram-project"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet", "-b", "main"}, {"add", "--", ".engram-project"}, {"-c", "user.name=Local Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "Owned local fixture project"}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if raw, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v: %s", err, raw)
		}
	}
	return root
}

// The real mutex proves contention before signaling: no scheduler sleeps,
// goroutine inspection, or backend-return event stands in for output wait.
type memoryTestOutputLock struct {
	sync.Mutex
	observe  sync.Mutex
	waiting  chan struct{}
	released chan struct{}
}

func (m *memoryTestOutputLock) Lock() {
	if m.Mutex.TryLock() {
		return
	}
	m.observe.Lock()
	if m.waiting != nil {
		close(m.waiting)
		m.waiting = nil
	}
	m.observe.Unlock()
	m.Mutex.Lock()
}

func (m *memoryTestOutputLock) Unlock() {
	m.Mutex.Unlock()
	m.observe.Lock()
	if m.released != nil {
		close(m.released)
		m.released = nil
	}
	m.observe.Unlock()
}

func (m *memoryTestOutputLock) waitForContention() <-chan struct{} {
	m.observe.Lock()
	defer m.observe.Unlock()
	m.waiting = make(chan struct{})
	return m.waiting
}

func (m *memoryTestOutputLock) waitForRelease() <-chan struct{} {
	m.observe.Lock()
	defer m.observe.Unlock()
	m.released = make(chan struct{})
	return m.released
}

// Forward real response bytes, then pause Write's return. This exposes the
// exact consumer-visible boundary without replacing the dispatcher or backend.
type memoryTestOutputGate struct {
	io.WriteCloser
	delivered   chan struct{}
	release     chan struct{}
	gateOnce    sync.Once
	releaseOnce sync.Once
	serial      memoryTestOutputLock
}

func (g *memoryTestOutputGate) Write(raw []byte) (int, error) {
	n, err := g.WriteCloser.Write(raw)
	var frame struct {
		ID string `json:"id"`
	}
	if err == nil && json.Unmarshal(raw, &frame) == nil && frame.ID == "gate" {
		g.gateOnce.Do(func() { close(g.delivered); <-g.release })
	}
	return n, err
}

func (g *memoryTestOutputGate) resume()      { g.releaseOnce.Do(func() { close(g.release) }) }
func (g *memoryTestOutputGate) Close() error { g.resume(); return g.WriteCloser.Close() }

func startMemoryTestSession(t *testing.T, root, installation, serverURL string) (*memoryTestClient, *memoryTestOutputGate) {
	t.Helper()
	t.Chdir(root)
	for key, value := range map[string]string{"ENGRAM_URL": serverURL, "ENGRAM_SERVER_URL": "", "ENGRAM_TOKEN": memoryTestTokenA, "ENGRAM_CLIENT_INSTANCE_ID": "", "ENGRAM_DATA_DIR": installation, "ENGRAM_TLS_CA": "", "CLAUDE_SESSION_ID": "fixture-a"} {
		t.Setenv(key, value)
	}
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	gate := &memoryTestOutputGate{WriteCloser: outputWriter, delivered: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	client := &memoryTestClient{input: inputWriter, output: outputReader, frames: make(chan []byte, 32), exited: make(chan error, 1)}
	go func() {
		scanner := bufio.NewScanner(outputReader)
		scanner.Buffer(make([]byte, 4096), sessionMemoryFrameBytes+2)
		for scanner.Scan() {
			client.frames <- bytes.Clone(scanner.Bytes())
		}
		close(client.frames)
	}()
	go func() { client.exited <- runSessionMemoryWithOutputLock(ctx, inputReader, gate, &gate.serial) }()
	t.Cleanup(func() { cancel(); gate.resume(); client.close(t, false) })
	return client, gate
}

func memoryTestObservedGate(t *testing.T, client *memoryTestClient, gate *memoryTestOutputGate) {
	t.Helper()
	response := client.call(t, "gate", "tools/call", map[string]any{"name": "recall", "arguments": map[string]any{"project": memoryTestProjectA, "query": "", "limit": 20}})
	if memoryTestRefused(response) || memoryTestUnwrap(response)["count"] != float64(2) {
		t.Fatalf("gated response did not contain real project-A records: %v", response)
	}
	select {
	case <-gate.delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("real response was not forwarded into the write-return gate")
	}
}

// One permanent consumer-level regression; no alternate memory implementation.
func TestSessionMemoryTransport(t *testing.T) {
	scratch := clientIdentityTempDir(t)
	binary := filepath.Join(scratch, "engram")
	goName := "go"
	if runtime.GOOS == "windows" {
		binary += ".exe"
		goName += ".exe"
	}
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", goName), "build", "-p=2", "-o", binary, ".")
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual CLI: %v: %s", err, raw)
	}
	rootA := memoryTestRepository(t, scratch, "a", memoryTestProjectA)
	rootB := memoryTestRepository(t, scratch, "b", memoryTestProjectB)
	installationA, installationB := filepath.Join(scratch, "install-a"), filepath.Join(scratch, "install-b")
	for _, flag := range []string{"true", "false"} {
		t.Run("privacy_flag_"+flag, func(t *testing.T) {
			t.Setenv("ENGRAM_VNEXT_F_ENABLED", flag)
			backend := newMemoryTestBackend(t)
			a := startMemoryTestClient(t, binary, rootA, installationA, backend.url, memoryTestTokenA)
			b := startMemoryTestClient(t, binary, rootB, installationB, backend.url, memoryTestTokenB, "CLAUDE_SESSION_ID=fixture-b")
			memoryTestInitialize(t, a)
			memoryTestInitialize(t, b)
			wantA := []int64{91001, 91002}
			if flag == "false" {
				wantA = append(wantA, 91003)
			}
			memoryTestRecall(t, a, memoryTestProjectA, memoryTestProjectA, wantA)
			// Actual CLI consumer immediately reuses the just-observed recall ID.
			memoryTestRecall(t, a, memoryTestProjectA, memoryTestProjectA, wantA)
			memoryTestRecall(t, b, memoryTestProjectB, memoryTestProjectA, []int64{92001, 92002})
			t.Logf("actual CLI initialize/list/recall: A=%v B=[92001 92002], flag=%s; foreign principal/domain excluded", wantA, flag)
			for _, binding := range []struct{ root, project string }{{rootA, memoryTestProjectA}, {rootB, memoryTestProjectB}} {
				raw, err := os.ReadFile(filepath.Join(binding.root, ".git", "engram-project.json"))
				if err != nil {
					t.Fatal(err)
				}
				var cached projectidentity.Project
				if err := json.Unmarshal(raw, &cached); err != nil || cached.ProjectID != binding.project {
					t.Fatalf("common-dir cache %s: project=%+v error=%v", binding.root, cached, err)
				}
			}
			if err := os.Remove(filepath.Join(rootA, ".engram-project")); err != nil {
				t.Fatal(err)
			}
			memoryTestRecall(t, a, memoryTestProjectA, memoryTestProjectB, wantA)
			for _, token := range []string{"", "invalid-local-fixture-token"} {
				denied := startMemoryTestClient(t, binary, rootA, installationA, backend.url, token)
				denied.call(t, 1, "initialize", nil)
				if !memoryTestRefused(denied.call(t, 2, "tools/list", nil)) {
					t.Fatal("bad auth discovered memory")
				}
				if !memoryTestRefused(denied.call(t, 3, "tools/call", map[string]any{"name": "recall", "arguments": map[string]any{"query": ""}})) {
					t.Fatal("bad auth read memory")
				}
				denied.close(t, false)
			}
			badIdentity := startMemoryTestClient(t, binary, rootA, installationA, backend.url, memoryTestTokenA, "ENGRAM_CLIENT_INSTANCE_ID=https://invalid")
			badIdentity.close(t, true)
			if err := os.WriteFile(filepath.Join(rootA, ".engram-project"), []byte(`{"project_id":"invalid"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			badMarker := startMemoryTestClient(t, binary, rootA, installationA, backend.url, memoryTestTokenA)
			memoryTestInitialize(t, badMarker)
			if !memoryTestRefused(badMarker.call(t, 3, "tools/call", map[string]any{"name": "recall", "arguments": map[string]any{"project": memoryTestProjectA}})) {
				t.Fatal("invalid project marker fell back to caller scope or the pinned cache")
			}
			badMarker.close(t, false)
			marker, err := json.Marshal(projectidentity.Project{ProjectID: memoryTestProjectA, Name: "a"})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(rootA, ".engram-project"), marker, 0o600); err != nil {
				t.Fatal(err)
			}
			if flag == "false" {
				return
			}

			t.Run("response_commit_order", func(t *testing.T) {
				t.Run("observed_response_immediate_id_reuse", func(t *testing.T) {
					client, gate := startMemoryTestSession(t, rootA, installationA, backend.url)
					memoryTestInitialize(t, client)
					memoryTestObservedGate(t, client, gate)
					// Cancellation is too late for the observed response. A new call
					// with that ID must be admitted before the old Write returns.
					client.send(t, nil, "notifications/cancelled", map[string]any{"requestId": "gate"})
					client.send(t, "gate", "tools/call", map[string]any{"name": "recall", "arguments": map[string]any{"query": "__sessionlocal_block__"}})
					memoryTestEvent(t, backend.started, "agent/local-a")
					released := gate.serial.waitForRelease()
					gate.resume()
					select {
					case <-released:
					case <-time.After(5 * time.Second):
						t.Fatal("old response write did not release serialization")
					}
					// Keep the reused-ID RPC active across the old Write's return.
					// A late old-worker deletion must not erase its cancel target.
					client.call(t, "old-write-returned", "ping", nil)
					client.send(t, nil, "notifications/cancelled", map[string]any{"requestId": "gate"})
					memoryTestEvent(t, backend.cancelled, "agent/local-a")
					client.call(t, "after-reuse-cancel", "ping", nil)
					memoryTestRecall(t, client, memoryTestProjectA, memoryTestProjectA, []int64{91001, 91002})
				})
				t.Run("cancel_before_queued_response_commit", func(t *testing.T) {
					client, gate := startMemoryTestSession(t, rootA, installationA, backend.url)
					memoryTestInitialize(t, client)
					memoryTestObservedGate(t, client, gate)
					waiting := gate.serial.waitForContention()
					client.send(t, "ready", "tools/call", map[string]any{"name": "recall", "arguments": map[string]any{"project": memoryTestProjectA, "query": ""}})
					select {
					case <-waiting:
					case <-time.After(5 * time.Second):
						t.Fatal("real recall response did not reach serialization wait")
					}
					client.send(t, nil, "notifications/cancelled", map[string]any{"requestId": "ready"})
					// A real backend call after the notification proves the reader
					// processed cancellation while output is still blocked.
					client.send(t, "reader-barrier", "tools/call", map[string]any{"name": "recall", "arguments": map[string]any{"query": "__sessionlocal_block__"}})
					memoryTestEvent(t, backend.started, "agent/local-a")
					client.send(t, nil, "notifications/cancelled", map[string]any{"requestId": "reader-barrier"})
					memoryTestEvent(t, backend.cancelled, "agent/local-a")
					gate.resume()
					// A leaked cancelled response would precede this ping and fail
					// its exact ID assertion; no sleep or negative-time oracle.
					client.call(t, "after-queued-cancel", "ping", nil)
					memoryTestRecall(t, client, memoryTestProjectA, memoryTestProjectA, []int64{91001, 91002})
				})
			})

			block := map[string]any{"name": "recall", "arguments": map[string]any{"query": "__sessionlocal_block__"}}
			a.send(t, "same-id", "tools/call", block)
			memoryTestEvent(t, backend.started, "agent/local-a")
			b.send(t, "same-id", "tools/call", block)
			memoryTestEvent(t, backend.started, "agent/local-b")
			a.send(t, nil, "notifications/cancelled", map[string]any{"requestId": "same-id"})
			memoryTestEvent(t, backend.cancelled, "agent/local-a")
			if memoryTestRefused(a.call(t, "ping", "ping", nil)) {
				t.Fatal("cancel broke ping")
			}
			select {
			case event := <-backend.cancelled:
				t.Fatalf("cancel crossed clients: %s", event)
			default:
			}
			b.send(t, nil, "notifications/cancelled", map[string]any{"requestId": "same-id"})
			memoryTestEvent(t, backend.cancelled, "agent/local-b")

			for _, id := range []json.Number{"9007199254740993", "1e100000", "1.5"} {
				response := a.call(t, id, "ping", nil)
				if response["error"] != nil {
					t.Fatalf("valid numeric ID refused: %s", id)
				}
			}
			// Exact numeric and decoded string keys, including values beyond float64 precision.
			for _, ids := range [][2]string{{`9007199254740993`, `9007199254740993.0`}, {`1e100000`, `10e99999`}, {`"escaped"`, `"\u0065scaped"`}} {
				fmt.Fprintf(a.input, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"method\":\"tools/call\",\"params\":{\"name\":\"recall\",\"arguments\":{\"query\":\"__sessionlocal_block__\"}}}\n", ids[0])
				memoryTestEvent(t, backend.started, "agent/local-a")
				fmt.Fprintf(a.input, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"method\":\"ping\"}\n", ids[1])
				if a.receive(t)["error"] == nil {
					t.Fatal("duplicate canonical ID accepted")
				}
				fmt.Fprintf(a.input, "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/cancelled\",\"params\":{\"requestId\":%s}}\n", ids[1])
				memoryTestEvent(t, backend.cancelled, "agent/local-a")
			}
			for index := range sessionMemoryInFlight {
				a.send(t, 100+index, "tools/call", block)
				memoryTestEvent(t, backend.started, "agent/local-a")
			}
			if a.call(t, "overflow", "ping", nil)["error"] == nil {
				t.Fatal("unbounded in-flight accepted")
			}
			a.close(t, false)
			for range sessionMemoryInFlight {
				memoryTestEvent(t, backend.cancelled, "agent/local-a")
			}
			memoryTestRecall(t, b, memoryTestProjectB, memoryTestProjectB, []int64{92001, 92002})
			before, err := os.ReadFile(filepath.Join(installationA, "client-instance-id"))
			if err != nil {
				t.Fatal(err)
			}
			reconnected := startMemoryTestClient(t, binary, rootA, installationA, backend.url, memoryTestTokenA)
			memoryTestInitialize(t, reconnected)
			memoryTestRecall(t, reconnected, memoryTestProjectA, memoryTestProjectA, wantA)
			after, _ := os.ReadFile(filepath.Join(installationA, "client-instance-id"))
			if !bytes.Equal(before, after) || !strings.HasPrefix(string(after), "engram-") {
				t.Fatal("reconnect changed real installation identity")
			}

			for _, frame := range []string{`{`, `[]`, `{"jsonrpc":"1.0","id":1,"method":"ping"}`, `{"JSONRPC":"2.0","id":1,"method":"ping"}`, `{"jsonrpc":"2.0","id":null,"method":"ping"}`, `{"jsonrpc":"2.0","id":true,"method":"ping"}`, `{"jsonrpc":"2.0","id":{},"method":"ping"}`, `{"jsonrpc":"2.0","id":1,"id":2,"method":"ping"}`, `{"jsonrpc":"2.0","id":1,"method":"ping","params":false}`, `{"jsonrpc":"2.0","id":1,"method":"notifications/initialized"}`} {
				fmt.Fprintln(reconnected.input, frame)
				if reconnected.receive(t)["error"] == nil {
					t.Fatalf("malformed frame accepted: %s", frame)
				}
			}
			fmt.Fprintln(reconnected.input, `{"jsonrpc":"2.0","id":"exact-id","ID":"shadow-id","method":"ping","Method":"tools/call"}`)
			if response := reconnected.receive(t); response["id"] != "exact-id" || response["error"] != nil {
				t.Fatalf("case-insensitive envelope shadowed admitted ID/method: %v", response)
			}
			fmt.Fprintf(reconnected.input, "{\"jsonrpc\":\"2.0\",\"id\":%q,\"method\":\"ping\"}\n", strings.Repeat("x", sessionMemoryIDBytes+1))
			if reconnected.receive(t)["error"] == nil {
				t.Fatal("unbounded ID accepted")
			}
			fmt.Fprintln(reconnected.input, `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"recall","arguments":{"query":"__sessionlocal_block__"}}}`)
			reconnected.call(t, "after-notification", "ping", nil)
			select {
			case <-backend.started:
				t.Fatal("notification executed request handler")
			default:
			}
			for _, name := range []string{"codebase_search", "loom_submit", "project_identity.register_v3", "settings"} {
				if !memoryTestRefused(reconnected.call(t, name, "tools/call", map[string]any{"name": name, "arguments": map[string]any{}})) {
					t.Fatalf("non-memory call %s accepted", name)
				}
			}
			reconnected.close(t, false)

			t.Run("frame_overflow", func(t *testing.T) {
				client := startMemoryTestClient(t, binary, rootA, installationA, backend.url, memoryTestTokenA)
				_, _ = io.WriteString(client.input, strings.Repeat("x", sessionMemoryFrameBytes+3)+"\n")
				client.close(t, true)
			})
			t.Run("output_failure", func(t *testing.T) {
				client := startMemoryTestClient(t, binary, rootA, installationA, backend.url, memoryTestTokenA)
				memoryTestInitialize(t, client)
				client.send(t, "blocked", "tools/call", block)
				memoryTestEvent(t, backend.started, "agent/local-a")
				_ = client.output.Close()
				client.send(t, "fail-output", "ping", nil)
				memoryTestEvent(t, backend.cancelled, "agent/local-a")
				client.close(t, true)
			})
			if runtime.GOOS != "windows" {
				for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
					for _, busy := range []bool{false, true} {
						t.Run(fmt.Sprintf("signal_%s/busy_%t", signal.String(), busy), func(t *testing.T) {
							client := startMemoryTestClient(t, binary, rootA, installationA, backend.url, memoryTestTokenA)
							memoryTestInitialize(t, client)
							if busy {
								client.send(t, "blocked", "tools/call", block)
								memoryTestEvent(t, backend.started, "agent/local-a")
							}
							if err := client.cmd.Process.Signal(signal); err != nil {
								t.Fatal(err)
							}
							if busy {
								memoryTestEvent(t, backend.cancelled, "agent/local-a")
							}
							client.waitForSignalExit(t)
							memoryTestRecall(t, b, memoryTestProjectB, memoryTestProjectB, []int64{92001, 92002})
						})
					}
				}
				if runtime.GOOS == "linux" {
					t.Run("signal_terminated/unread_output", func(t *testing.T) {
						stalled := newMemoryTestBackend(t)
						if err := stalled.db.Model(&dbstore.Memory{}).Where("id = ?", 91001).Update("content", strings.Repeat("x", 900*1024)).Error; err != nil {
							t.Fatal(err)
						}
						client := startMemoryTestClientWithOutput(t, binary, rootA, installationA, stalled.url, memoryTestTokenA, false)
						output := client.output.(*os.File)
						if err := output.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
							t.Fatal(err)
						}
						reader := bufio.NewScanner(output)
						reader.Buffer(make([]byte, 4096), sessionMemoryFrameBytes+2)
						for _, method := range []string{"initialize", "tools/list"} {
							client.send(t, method, method, nil)
							if !reader.Scan() {
								t.Fatalf("stalled-output %s setup: %v", method, reader.Err())
							}
						}
						client.send(t, "blocked", "tools/call", block)
						memoryTestEvent(t, stalled.started, "agent/local-a")
						client.send(t, "large", "tools/call", map[string]any{"name": "recall", "arguments": map[string]any{"project": memoryTestProjectA, "query": "", "limit": 20}})
						// Observe a real large reply starting, then keep the pipe open
						// and unread: inherited stdout writes must not delay process exit.
						var first [1]byte
						if _, err := output.Read(first[:]); err != nil {
							t.Fatal(err)
						}
						if err := client.cmd.Process.Signal(syscall.SIGTERM); err != nil {
							t.Fatal(err)
						}
						memoryTestEvent(t, stalled.cancelled, "agent/local-a")
						client.waitForSignalExit(t)
						memoryTestRecall(t, b, memoryTestProjectB, memoryTestProjectB, []int64{92001, 92002})
					})
				}
			} else {
				t.Log("SIGINT/SIGTERM delivery not exercised on Windows; signal.NotifyContext wiring retained, EOF/output failure exercised")
			}
		})
	}
}
