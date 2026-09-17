// operator-code-retirement-fixture drives the disposable D-A retirement proof.
// It accepts a private PostgreSQL DSN file, starts exactly one local server at a
// time, and emits only non-secret observations.
package main

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
	booksdomain "github.com/thebtf/engram/internal/books"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/internal/retrieval"
	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	retirementReceiptSchema = "operator-code-console/da03-retirement-fixture/v1"
	retirementProject       = "operator-code-retirement-fixture"
	retirementReason        = booksdomain.RetirementFailureReason
)

type invocation struct {
	dsnFile string
	server  string
}

type receipt struct {
	Schema            string                     `json:"schema"`
	Status            string                     `json:"status"`
	WriterDenominator writerDenominator          `json:"writer_denominator"`
	Observations      map[string]json.RawMessage `json:"observations"`
	Cleanup           cleanupObservation         `json:"cleanup"`
}

type writerDenominator struct {
	HTTPDirectOldMethods int `json:"http_direct_old_methods"`
	MCPWriterActions     int `json:"mcp_writer_actions"`
	OldBookmarks         int `json:"old_bookmarks"`
	Total                int `json:"total"`
}

type cleanupObservation struct {
	ServerProcessesStopped bool `json:"server_processes_stopped"`
	SchemaRemoved          bool `json:"schema_removed"`
	TemporaryRootRemoved   bool `json:"temporary_root_removed"`
}

type booksJobFixtureRecord struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Status    string    `gorm:"column:status;not null" json:"status"`
	SourceRef string    `gorm:"column:source_ref;not null" json:"source_ref"`
	Error     string    `gorm:"column:error" json:"error"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (booksJobFixtureRecord) TableName() string { return "books_jobs" }

type fixtureState struct {
	project        string
	pendingJobID   int64
	processingID   int64
	doneJobID      int64
	publicNodeID   int64
	privateNodeID  int64
	publicEdgeID   int64
	firstMemoryID  int64
	secondMemoryID int64
	documentID     int64
	documentPath   string
	ruleContent    string
	issueTitle     string
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	in, err := parseInvocation(args)
	if err != nil {
		fmt.Fprintln(stderr, "usage: operator-code-retirement-fixture --dsn-file <private-file> --server <candidate-server>")
		return 2
	}
	dsnBytes, err := os.ReadFile(in.dsnFile)
	if err != nil || !validFixtureDSN(string(dsnBytes)) {
		fmt.Fprintln(stderr, "operator-code-retirement-fixture: private disposable database input unavailable")
		return 1
	}
	result, err := execute(ctx, strings.TrimSpace(string(dsnBytes)), in.server)
	if err != nil {
		fmt.Fprintln(stderr, "operator-code-retirement-fixture: verification failed")
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintln(stderr, "operator-code-retirement-fixture: receipt write failed")
		return 1
	}
	return 0
}

func parseInvocation(args []string) (invocation, error) {
	if len(args) != 4 {
		return invocation{}, errors.New("expected exactly two fixture flags")
	}
	var in invocation
	for index := 0; index < len(args); index += 2 {
		switch args[index] {
		case "--dsn-file":
			in.dsnFile = args[index+1]
		case "--server":
			in.server = args[index+1]
		default:
			return invocation{}, errors.New("unknown fixture flag")
		}
	}
	if in.dsnFile == "" || in.server == "" || !filepath.IsAbs(in.server) {
		return invocation{}, errors.New("fixture input is invalid")
	}
	info, err := os.Stat(in.server)
	if err != nil || !info.Mode().IsRegular() {
		return invocation{}, errors.New("candidate server is unavailable")
	}
	return in, nil
}

func validFixtureDSN(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return false
	}
	lower := strings.ToLower(raw)
	return strings.Contains(lower, "test") && !strings.Contains(lower, "prod") && !strings.Contains(lower, "staging")
}

func execute(ctx context.Context, rawDSN, serverBinary string) (result receipt, retErr error) {
	schema, err := retirementSchemaName()
	if err != nil {
		return result, err
	}
	admin, err := sql.Open("postgres", rawDSN)
	if err != nil {
		return result, fmt.Errorf("open disposable PostgreSQL administration: %w", err)
	}
	defer admin.Close()
	if err := admin.PingContext(ctx); err != nil {
		return result, fmt.Errorf("ping disposable PostgreSQL administration: %w", err)
	}
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		return result, fmt.Errorf("create disposable schema: %w", err)
	}
	schemaRemoved := false
	defer func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err == nil {
			schemaRemoved = true
		}
		result.Cleanup.SchemaRemoved = schemaRemoved
	}()

	scopedDSN, err := schemaDSN(rawDSN, schema)
	if err != nil {
		return result, err
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: scopedDSN, MaxConns: 4, LogLevel: logger.Silent})
	if err != nil {
		return result, fmt.Errorf("open disposable fixture store: %w", err)
	}
	defer store.Close()
	state, err := seedHistoricalFixture(ctx, store)
	if err != nil {
		return result, err
	}
	if err := assertInternalGraphACL(ctx, store, state); err != nil {
		return result, err
	}
	if err := assertTierTwo(ctx, store, state); err != nil {
		return result, err
	}

	root, err := os.MkdirTemp("", "operator-code-retirement-")
	if err != nil {
		return result, fmt.Errorf("create disposable fixture root: %w", err)
	}
	temporaryRootRemoved := false
	defer func() {
		if err := os.RemoveAll(root); err == nil {
			temporaryRootRemoved = true
		}
		result.Cleanup.TemporaryRootRemoved = temporaryRootRemoved
	}()
	masterToken, err := randomSecret()
	if err != nil {
		return result, err
	}

	server, err := startServer(ctx, serverBinary, scopedDSN, masterToken, root)
	if err != nil {
		return result, err
	}
	serverStopped := false
	defer func() {
		if server != nil {
			if stopErr := server.stop(); stopErr == nil {
				serverStopped = true
			}
		}
		result.Cleanup.ServerProcessesStopped = serverStopped
	}()
	if err := server.waitReady(ctx); err != nil {
		return result, err
	}
	first, err := observeLiveRetirement(ctx, server, masterToken, state)
	if err != nil {
		return result, err
	}
	if err := server.stop(); err != nil {
		return result, err
	}
	serverStopped = true
	server, err = startServer(ctx, serverBinary, scopedDSN, masterToken, root)
	if err != nil {
		return result, err
	}
	serverStopped = false
	if err := server.waitReady(ctx); err != nil {
		return result, err
	}
	second, err := observeLiveRetirement(ctx, server, masterToken, state)
	if err != nil {
		return result, err
	}
	if err := assertResidualState(ctx, store, state, first.BookState); err != nil {
		return result, err
	}
	if err := assertResidualState(ctx, store, state, second.BookState); err != nil {
		return result, err
	}
	if err := assertDocumentProvenance(ctx, store, state); err != nil {
		return result, err
	}
	if !sameObservedBookStates(first.BookState, second.BookState) {
		return result, errors.New("restart changed terminal historical book rows")
	}

	result = receipt{
		Schema:            retirementReceiptSchema,
		Status:            "PASS",
		WriterDenominator: writerDenominator{HTTPDirectOldMethods: 5, MCPWriterActions: 3, OldBookmarks: 2, Total: 10},
		Observations: map[string]json.RawMessage{
			"quiescence":       mustJSON(map[string]any{"started_server_processes": 1, "old_book_writer_processes": 0, "precondition": "no old writer process was launched by the single-server fixture"}),
			"first_start":      mustJSON(first),
			"restart":          mustJSON(second),
			"retained_readers": mustJSON(map[string]any{"tier2_traverse": "observed", "historical_graph_http": "observed", "internal_graph_acl": "observed", "context_search": "observed", "rules": "observed", "issues": "observed", "versioned_documents": "observed", "document_provenance": "observed"}),
		},
	}
	return result, nil
}

func retirementSchemaName() (string, error) {
	bytes := make([]byte, 12)
	if _, err := cryptorand.Read(bytes); err != nil {
		return "", fmt.Errorf("random fixture schema suffix: %w", err)
	}
	return "operator_code_retirement_" + hex.EncodeToString(bytes), nil
}

func schemaDSN(raw, schema string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse fixture DSN: %w", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func randomSecret() (string, error) {
	bytes := make([]byte, 32)
	if _, err := cryptorand.Read(bytes); err != nil {
		return "", fmt.Errorf("create fixture credential: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func seedHistoricalFixture(ctx context.Context, store *gormdb.Store) (fixtureState, error) {
	state := fixtureState{project: retirementProject, documentPath: "books/jobs/fixture/history.md", ruleContent: "retirement fixture rule remains readable", issueTitle: "retirement fixture issue remains readable"}
	now := time.Now().UTC().Add(-time.Minute)
	jobs := []booksJobFixtureRecord{
		{Status: string(booksdomain.StatusPending), SourceRef: "historical-pending.md", CreatedAt: now, UpdatedAt: now},
		{Status: string(booksdomain.StatusProcessing), SourceRef: "historical-processing.md", CreatedAt: now, UpdatedAt: now},
		{Status: string(booksdomain.StatusDone), SourceRef: "historical-done.md", CreatedAt: now, UpdatedAt: now},
	}
	for index := range jobs {
		if err := store.DB.WithContext(ctx).Create(&jobs[index]).Error; err != nil {
			return state, fmt.Errorf("seed historical book job: %w", err)
		}
	}
	state.pendingJobID, state.processingID, state.doneJobID = jobs[0].ID, jobs[1].ID, jobs[2].ID
	documents := gormdb.NewVersionedDocumentStore(store)
	metadata := fmt.Sprintf(`{"source_book_job_id":%d,"provenance":"historical-book"}`, state.pendingJobID)
	firstDocument, err := documents.Create(ctx, state.documentPath, state.project, "partial historical document version one", "markdown", metadata, "retirement-fixture")
	if err != nil {
		return state, fmt.Errorf("seed historical document first version: %w", err)
	}
	state.documentID = firstDocument
	if _, err := documents.Create(ctx, state.documentPath, state.project, "partial historical document version two", "markdown", metadata, "retirement-fixture"); err != nil {
		return state, fmt.Errorf("seed historical document second version: %w", err)
	}
	if _, err := documents.AddComment(ctx, firstDocument, "retirement-fixture", "historical provenance remains", nil, nil); err != nil {
		return state, fmt.Errorf("seed historical document provenance: %w", err)
	}

	memoryStore := gormdb.NewMemoryStore(store)
	firstMemory, err := memoryStore.Create(ctx, &models.Memory{Project: state.project, Content: "retirement fixture anchor for graph traversal", ImportanceBase: 0.7})
	if err != nil {
		return state, fmt.Errorf("seed first historical memory: %w", err)
	}
	secondMemory, err := memoryStore.Create(ctx, &models.Memory{Project: state.project, Content: "retained graph neighbour without the anchor terms", ImportanceBase: 0.6})
	if err != nil {
		return state, fmt.Errorf("seed second historical memory: %w", err)
	}
	state.firstMemoryID, state.secondMemoryID = firstMemory.ID, secondMemory.ID
	nodes := graph.NewNodesStore(store.DB)
	publicNode, err := nodes.Create(ctx, &models.KnowledgeNode{NodeType: models.NodeTypeSkill, ExternalRef: "retirement-visible-node", Project: state.project, PrivacyScope: "project"})
	if err != nil {
		return state, fmt.Errorf("seed public historical graph node: %w", err)
	}
	privateNode, err := nodes.Create(ctx, &models.KnowledgeNode{NodeType: models.NodeTypeSkill, ExternalRef: "retirement-private-node", Project: state.project, PrivacyScope: "private"})
	if err != nil {
		return state, fmt.Errorf("seed private historical graph node: %w", err)
	}
	state.publicNodeID, state.privateNodeID = publicNode.ID, privateNode.ID
	graphStore := graph.NewStore(store.DB, nodes)
	publicEdge, err := graphStore.Create(ctx, &graph.Edge{NodeSourceID: &publicNode.ID, NodeTargetID: &publicNode.ID, SourceType: "node", TargetType: "node", EdgeType: graph.EdgeUses, Weight: 1, ValidFrom: now, ValidUntil: now.AddDate(1, 0, 0)})
	if err != nil {
		return state, fmt.Errorf("seed public historical graph edge: %w", err)
	}
	state.publicEdgeID = publicEdge.ID
	if _, err := graphStore.Create(ctx, &graph.Edge{SourceID: &firstMemory.ID, TargetID: &secondMemory.ID, SourceType: "memory", TargetType: "memory", EdgeType: graph.EdgeUses, Weight: 1, ValidFrom: now, ValidUntil: now.AddDate(1, 0, 0)}); err != nil {
		return state, fmt.Errorf("seed historical memory graph edge: %w", err)
	}

	project := state.project
	if _, err := gormdb.NewBehavioralRulesStore(store).Create(ctx, &models.BehavioralRule{Project: &project, Content: state.ruleContent, Priority: 10, Enabled: true}); err != nil {
		return state, fmt.Errorf("seed retained rule: %w", err)
	}
	if _, err := gormdb.NewIssueStore(store.DB).CreateIssue(ctx, &gormdb.Issue{Title: state.issueTitle, Body: "historical issue body", SourceProject: state.project, TargetProject: state.project, Type: "task", Priority: "medium"}); err != nil {
		return state, fmt.Errorf("seed retained issue: %w", err)
	}
	return state, nil
}

func assertInternalGraphACL(ctx context.Context, store *gormdb.Store, state fixtureState) error {
	nodes := graph.NewNodesStore(store.DB)
	visible, err := nodes.Get(ctx, state.publicNodeID, false)
	if err != nil || visible.ExternalRef != "retirement-visible-node" {
		return fmt.Errorf("internal graph public reader: %w", err)
	}
	if _, err := nodes.Get(ctx, state.privateNodeID, false); !errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.New("internal graph reader disclosed private historical node")
	}
	return nil
}

func assertTierTwo(ctx context.Context, store *gormdb.Store, state fixtureState) error {
	result, explanations, err := retrieval.HybridSearch(ctx, state.project, "retirement fixture anchor", 10, gormdb.NewMemoryStore(store), nil, graph.NewStore(store.DB, graph.NewNodesStore(store.DB)), retrieval.HybridOptions{ExpandGraph: true, Explain: true, SkipTier0: true})
	if err != nil {
		return fmt.Errorf("Tier2 historical traversal: %w", err)
	}
	if len(result) < 2 {
		return errors.New("Tier2 historical traversal did not retain graph neighbour")
	}
	for _, explanation := range explanations {
		if explanation.MemoryID == state.secondMemoryID && explanation.SourceTier == "tier2_graph" {
			return nil
		}
	}
	return errors.New("Tier2 historical traversal did not report graph provenance")
}

type liveServer struct {
	command *exec.Cmd
	baseURL string
	address string
}

func startServer(ctx context.Context, binary, dsn, token, root string) (*liveServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("reserve local server port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return nil, fmt.Errorf("release local server port reservation: %w", err)
	}
	command := exec.CommandContext(ctx, binary)
	command.Dir = root
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.Env = append(cleanEnvironment(os.Environ(), "DATABASE_DSN", "ENGRAM_AUTH_ADMIN_TOKEN", "ENGRAM_AUTH_DISABLED", "ENGRAM_WORKER_PORT", "ENGRAM_WORKER_HOST", "ENGRAM_GRAPH_ENABLED", "ENGRAM_BOOKS_ENABLED", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA"),
		"DATABASE_DSN="+dsn,
		"ENGRAM_AUTH_ADMIN_TOKEN="+token,
		"ENGRAM_WORKER_HOST=127.0.0.1",
		"ENGRAM_WORKER_PORT="+strconv.Itoa(port),
		"ENGRAM_GRAPH_ENABLED=true",
		"ENGRAM_BOOKS_ENABLED=true",
		"ENGRAM_TELEMETRY_ENABLED=false",
		"HOME="+root,
		"USERPROFILE="+root,
		"APPDATA="+root,
		"LOCALAPPDATA="+root,
	)
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start local candidate server: %w", err)
	}
	return &liveServer{command: command, baseURL: "http://127.0.0.1:" + strconv.Itoa(port), address: "127.0.0.1:" + strconv.Itoa(port)}, nil
}

func cleanEnvironment(values []string, names ...string) []string {
	blocked := make(map[string]struct{}, len(names))
	for _, name := range names {
		blocked[strings.ToUpper(name)] = struct{}{}
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		name, _, found := strings.Cut(value, "=")
		if found {
			if _, blocked := blocked[strings.ToUpper(name)]; blocked {
				continue
			}
		}
		out = append(out, value)
	}
	return out
}

func (server *liveServer) waitReady(ctx context.Context) error {
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.baseURL+"/api/ready", nil)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("candidate server did not become ready")
		case <-ticker.C:
		}
	}
}

func (server *liveServer) stop() error {
	if server == nil || server.command == nil || server.command.Process == nil {
		return nil
	}
	killed := false
	if err := server.command.Process.Kill(); err != nil {
		if !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("stop local candidate server: %w", err)
		}
	} else {
		killed = true
	}
	if err := server.command.Wait(); err != nil && !errors.Is(err, os.ErrProcessDone) && !killed {
		return fmt.Errorf("wait for local candidate server stop: %w", err)
	}
	server.command = nil
	return nil
}

type liveObservation struct {
	HTTPWriterStatuses []int                   `json:"http_writer_statuses"`
	MCPWriterErrors    int                     `json:"mcp_writer_errors"`
	BookState          []booksJobFixtureRecord `json:"book_state"`
	ReaderChecks       []string                `json:"reader_checks"`
	ToolNamesDigest    string                  `json:"tool_names_digest"`
}

func observeLiveRetirement(ctx context.Context, server *liveServer, token string, state fixtureState) (liveObservation, error) {
	observation := liveObservation{}
	for _, id := range []int64{state.pendingJobID, state.processingID, state.doneJobID} {
		status, body, err := server.request(ctx, token, http.MethodGet, "/api/books/"+strconv.FormatInt(id, 10)+"/status")
		if err != nil || status != http.StatusOK {
			return observation, fmt.Errorf("read historical book status %d: %w", id, err)
		}
		var job booksJobFixtureRecord
		if err := json.Unmarshal([]byte(body), &job); err != nil || job.ID != id {
			return observation, fmt.Errorf("decode historical book status %d", id)
		}
		observation.BookState = append(observation.BookState, job)
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/books"},
		{http.MethodPost, "/api/graph/nodes"},
		{http.MethodPost, "/api/graph/edges"},
		{http.MethodDelete, "/api/graph/nodes/1"},
		{http.MethodDelete, "/api/graph/edges/1"},
	} {
		status, body, err := server.request(ctx, token, route.method, route.path)
		if err != nil {
			return observation, err
		}
		if status != http.StatusMethodNotAllowed {
			return observation, fmt.Errorf("retired HTTP writer %s %s status=%d body=%s", route.method, route.path, status, body)
		}
		observation.HTTPWriterStatuses = append(observation.HTTPWriterStatuses, status)
	}
	checks := []struct{ path, required, forbidden string }{
		{"/api/books/" + strconv.FormatInt(state.pendingJobID, 10) + "/status", retirementReason, ""},
		{"/api/graph/nodes?project=" + url.QueryEscape(state.project), "retirement-visible-node", "retirement-private-node"},
		{"/api/documents/history?project=" + url.QueryEscape(state.project) + "&path=" + url.QueryEscape(state.documentPath), state.documentPath, ""},
		{"/api/documents/comments?document_id=" + strconv.FormatInt(state.documentID, 10), "historical provenance remains", ""},
		{"/api/rules?project=" + url.QueryEscape(state.project), state.ruleContent, ""},
		{"/api/issues?project=" + url.QueryEscape(state.project), state.issueTitle, ""},
		{"/api/context/search?project=" + url.QueryEscape(state.project) + "&query=retirement%20fixture%20anchor", "retirement fixture anchor", ""},
	}
	for _, check := range checks {
		status, body, err := server.request(ctx, token, http.MethodGet, check.path)
		if err != nil {
			return observation, fmt.Errorf("call retained reader %s: %w", check.path, err)
		}
		if status != http.StatusOK {
			return observation, fmt.Errorf("retained reader %s status=%d", check.path, status)
		}
		if check.required != "" && !strings.Contains(body, check.required) {
			return observation, fmt.Errorf("retained reader %s omitted required historical value", check.path)
		}
		if check.forbidden != "" && strings.Contains(body, check.forbidden) {
			return observation, fmt.Errorf("retained reader %s disclosed private historical value", check.path)
		}
		observation.ReaderChecks = append(observation.ReaderChecks, check.path)
	}

	connection, err := grpc.DialContext(ctx, server.address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		return observation, fmt.Errorf("connect local MCP gRPC: %w", err)
	}
	defer connection.Close()
	mcpCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	client := pb.NewEngramServiceClient(connection)
	initialized, err := client.Initialize(mcpCtx, &pb.InitializeRequest{})
	if err != nil {
		return observation, fmt.Errorf("list MCP tools: %w", err)
	}
	names := make([]string, 0, len(initialized.Tools))
	var graphTool *pb.ToolDefinition
	for _, tool := range initialized.Tools {
		names = append(names, tool.Name)
		if tool.Name == "graph" {
			graphTool = tool
		}
		if strings.Contains(tool.Name, "book") || tool.Name == "add_edge" || tool.Name == "remove_edge" || tool.Name == "add_node" {
			return observation, fmt.Errorf("retired writer tool remains listed: %s", tool.Name)
		}
	}
	if graphTool == nil {
		return observation, errors.New("retained graph MCP reader is absent")
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(graphTool.InputSchemaJson, &schema); err != nil {
		return observation, errors.New("graph MCP schema is invalid")
	}
	if strings.Contains(string(schema.Properties["action"]), "add_edge") || strings.Contains(string(schema.Properties["action"]), "remove_edge") || strings.Contains(string(schema.Properties["action"]), "add_node") {
		return observation, errors.New("retired graph writer action remains advertised")
	}
	sort.Strings(names)
	observation.ToolNamesDigest = digestStrings(names)
	for _, action := range []string{"add_edge", "remove_edge", "add_node"} {
		response, err := client.CallTool(mcpCtx, &pb.CallToolRequest{ToolName: "graph", ArgumentsJson: mustJSON(map[string]any{"action": action})})
		if err != nil || !response.IsError {
			return observation, fmt.Errorf("retired MCP graph writer %s was accepted", action)
		}
		observation.MCPWriterErrors++
	}
	reader, err := client.CallTool(mcpCtx, &pb.CallToolRequest{ToolName: "graph", ArgumentsJson: mustJSON(map[string]any{"action": "get_edges", "node_id": state.publicNodeID})})
	if err != nil {
		return observation, fmt.Errorf("call retained MCP graph reader: %w", err)
	}
	if reader.IsError {
		return observation, errors.New("retained MCP graph reader returned an error")
	}
	if !strings.Contains(string(reader.ContentJson), strconv.FormatInt(state.publicEdgeID, 10)) {
		return observation, errors.New("retained MCP graph reader omitted historical edge")
	}
	return observation, nil
}

func (server *liveServer) request(ctx context.Context, token, method, path string) (int, string, error) {
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader("{}")
	}
	request, err := http.NewRequestWithContext(ctx, method, server.baseURL+path, body)
	if err != nil {
		return 0, "", err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, "", fmt.Errorf("call local server %s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0, "", err
	}
	return response.StatusCode, string(responseBody), nil
}

func assertResidualState(ctx context.Context, store *gormdb.Store, state fixtureState, want []booksJobFixtureRecord) error {
	var jobs []booksJobFixtureRecord
	if err := store.DB.WithContext(ctx).Where("id IN ?", []int64{state.pendingJobID, state.processingID, state.doneJobID}).Order("id").Find(&jobs).Error; err != nil {
		return fmt.Errorf("read residual historical jobs: %w", err)
	}
	if len(jobs) != 3 || !sameBookStates(jobs, want) {
		return errors.New("residual job state was not idempotent")
	}
	if jobs[0].Status != string(booksdomain.StatusFailed) || jobs[0].Error != retirementReason || jobs[1].Status != string(booksdomain.StatusFailed) || jobs[1].Error != retirementReason || jobs[2].Status != string(booksdomain.StatusDone) {
		return errors.New("residual jobs did not preserve terminal transition contract")
	}
	return nil
}

func assertDocumentProvenance(ctx context.Context, store *gormdb.Store, state fixtureState) error {
	documents := gormdb.NewVersionedDocumentStore(store)
	history, err := documents.GetHistory(ctx, state.documentPath, state.project, 10)
	if err != nil || len(history) != 2 {
		return fmt.Errorf("read historical document versions: %w", err)
	}
	wantContent := map[int]string{1: "partial historical document version one", 2: "partial historical document version two"}
	for _, version := range history {
		if want, found := wantContent[version.Version]; !found || version.Content != want {
			return errors.New("historical document version lost partial content")
		}
		var metadata struct {
			SourceBookJobID int64 `json:"source_book_job_id"`
		}
		if err := json.Unmarshal([]byte(version.Metadata), &metadata); err != nil || metadata.SourceBookJobID != state.pendingJobID {
			return errors.New("historical document lost source_book_job_id provenance")
		}
	}
	comments, err := documents.GetComments(ctx, state.documentID)
	if err != nil || len(comments) != 1 || comments[0].Content != "historical provenance remains" {
		return fmt.Errorf("read historical document provenance comment: %w", err)
	}
	return nil
}

func sameBookStates(left, right []booksJobFixtureRecord) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID || left[index].Status != right[index].Status || left[index].Error != right[index].Error || left[index].SourceRef != right[index].SourceRef {
			return false
		}
	}
	return true
}

func sameObservedBookStates(left, right []booksJobFixtureRecord) bool {
	if !sameBookStates(left, right) {
		return false
	}
	for index := range left {
		if !left[index].CreatedAt.Equal(right[index].CreatedAt) || !left[index].UpdatedAt.Equal(right[index].UpdatedAt) {
			return false
		}
	}
	return true
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func digestStrings(values []string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\n")))
	return hex.EncodeToString(digest[:])
}

var _ = runtime.GOOS
