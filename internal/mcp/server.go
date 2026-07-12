// Package mcp provides the MCP (Model Context Protocol) server for engram.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/bulkops"
	"github.com/thebtf/engram/internal/chunking"
	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/internal/collections"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
	gorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/redaction"
	"github.com/thebtf/engram/internal/reranking"
	"github.com/thebtf/engram/internal/sessions"
	"github.com/thebtf/engram/internal/writelint"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

// Server is the MCP server that exposes engram tools.
// Field order optimized for memory alignment (fieldalignment).
type Server struct {
	stdin                         io.Reader
	stdout                        io.Writer
	sessionStore                  *gorm.SessionStore
	collectionRegistry            *collections.Registry
	sessionIdxStore               *sessions.Store
	documentStore                 *gorm.DocumentStore
	versionedDocumentStore        *gorm.VersionedDocumentStore
	chunkManager                  *chunking.Manager
	issueStore                    *gorm.IssueStore
	embeddingClient               *embedding.Client
	embeddingStore                *embedding.Store
	rerankClient                  *reranking.Client
	memoryStore                   *gorm.MemoryStore
	metaMemoryIndex               metaMemoryIndex
	hintQueue                     cognitivecore.HintQueue
	stateStore                    statePlane
	experienceProvider            experienceProvider
	temporalTruthProvider         temporalTruthProvider
	directiveCaptureService       directiveCaptureService
	principalMemoryQuerySvc       principalMemoryQueryService
	domainRegistryService         domainRegistryService
	behavioralRulesStore          *gorm.BehavioralRulesStore
	promotionStore                *gorm.PromotionStore
	graphStore                    *graph.Store
	nodesStore                    nodesStoreAPI // T014: Milestone F TG2 add_node action (*graph.NodesStore satisfies this interface)
	auditStore                    *gorm.AuditStore
	purgeStore                    *gorm.PurgeStore
	candidateStore                *gorm.CandidateStore      // Milestone-F TG4: non-nil when ENGRAM_VNEXT_F_ENABLED=true
	snapshotStore                 *gorm.SnapshotStore       // Milestone-F TG6: non-nil when ENGRAM_VNEXT_F_ENABLED=true
	reviewLoopCandidateStoreSeam  reviewLoopCandidateLister // CR-008 test seam for review metrics/queue reads
	codeChunkStore                *gorm.CodeChunkStore      // CR-006: non-nil when ENGRAM_CODE_INTEL_ENABLED=true
	ruleGovernanceStore           ruleGovernanceCandidateWriter
	ruleGovernanceReadStore       ruleGovernanceReadStore
	ruleInjectionTelemetry        ruleInjectionTelemetryReader
	settingsStoreWired            *gorm.SettingsStore       // #259 CR-3: wired from the worker's open store so the settings tool reuses one pool (no per-call NewStore)
	bulkFacade                    *bulkops.Facade           // Milestone-F TG6 T044: bulk_promote/delete/supersede with dry-run
	testAuditWriter               auditWriter               // set only in tests via setTestAuditWriter
	testMemoryEditor              memoryEditor              // set only in tests via setTestMemoryEditor
	testMemorySignificanceUpdater memorySignificanceUpdater // set only in tests via setTestMemorySignificanceUpdater
	vault                         *crypto.Vault
	vaultInitErr                  error
	vaultOnce                     sync.Once
	backfillStatusFunc            func() (any, error)
	writeLint                     *writelint.Orchestrator  // T035: two-phase write-lint protocol (nil → legacy path)
	redactionRules                []redaction.CompiledRule // T036: operator scrub layer, loaded once at startup
	statsDB                       *gormlib.DB              // raw DB handle for stats raw-SQL queries; set via SetStatsDB
	version                       string
}

type ruleGovernanceCandidateWriter interface {
	CreateRuleCandidate(ctx context.Context, c *models.RuleCandidate) (*models.RuleCandidate, error)
}

type ruleGovernanceReadStore interface {
	GetLifecycleHealth(ctx context.Context, params gorm.RuleGovernanceHealthParams) (gorm.RuleGovernanceHealth, error)
	ListExceptionQueueGroups(ctx context.Context, params gorm.RuleGovernanceExceptionQueueParams) ([]gorm.RuleGovernanceExceptionQueueGroup, error)
	ListRuleGovernanceSnapshots(ctx context.Context, params gorm.RuleGovernanceSnapshotListParams) ([]gorm.RuleGovernanceSnapshotSummary, error)
}

type ruleGovernanceWriteStore interface {
	TransitionRuleVersion(ctx context.Context, versionID int64, to models.RuleVersionState, req gorm.RuleTransitionRequest) (*models.RuleVersion, error)
	PinRuleGovernanceSnapshot(ctx context.Context, snapshotID string, pinned bool) (gorm.RuleGovernanceSnapshotSummary, error)
	RollbackRuleGovernanceSnapshot(ctx context.Context, snapshotID string, req gorm.RuleTransitionRequest) (gorm.RuleGovernanceRollbackResult, error)
}

type ruleGovernanceStoreAPI interface {
	ruleGovernanceCandidateWriter
	ruleGovernanceReadStore
	ruleGovernanceWriteStore
}

type ruleInjectionTelemetryReader interface {
	AggregateByProjectRuleAndEventType(ctx context.Context, params gorm.RuleInjectionTelemetryParams) (gorm.RuleInjectionTelemetryAggregate, error)
}

// ServerOptions holds the dependencies injected into the MCP Server.
type ServerOptions struct {
	Version            string
	SessionStore       *gorm.SessionStore
	CollectionRegistry *collections.Registry
	SessionIdxStore    *sessions.Store
	DocumentStore      *gorm.DocumentStore
	ChunkManager       *chunking.Manager
}

type metaMemoryIndex interface {
	QueryMetaIndex(ctx context.Context, query gorm.MetaIndexQuery) ([]gorm.MetaIndexHit, error)
}

// NewServer creates a new MCP server.
func NewServer(opts ServerOptions) *Server {
	return &Server{
		version:            opts.Version,
		stdin:              os.Stdin,
		stdout:             os.Stdout,
		sessionStore:       opts.SessionStore,
		collectionRegistry: opts.CollectionRegistry,
		sessionIdxStore:    opts.SessionIdxStore,
		documentStore:      opts.DocumentStore,
		chunkManager:       opts.ChunkManager,
	}
}

// SetBackfillStatusFunc sets the function to retrieve backfill run status.
func (s *Server) SetBackfillStatusFunc(fn func() (any, error)) {
	s.backfillStatusFunc = fn
}

// SetVersionedDocumentStore sets the versioned document store for document MCP tools.
func (s *Server) SetVersionedDocumentStore(vds *gorm.VersionedDocumentStore) {
	s.versionedDocumentStore = vds
}

// SetIssueStore sets the issue store for cross-project agent issue tracking.
func (s *Server) SetIssueStore(is *gorm.IssueStore) {
	s.issueStore = is
}

// SetMemoryStore sets the memory store for the memories table (US3 Commit C).
func (s *Server) SetMemoryStore(ms *gorm.MemoryStore) {
	s.memoryStore = ms
}

// SetMetaMemoryIndex wires the content-free S2 meta-memory query seam.
func (s *Server) SetMetaMemoryIndex(idx metaMemoryIndex) {
	s.metaMemoryIndex = idx
}

// SetHintQueue wires the CORE ambient hint queue for get_ambient_hints fallback polling.
func (s *Server) SetHintQueue(queue cognitivecore.HintQueue) {
	s.hintQueue = queue
}

// SetStateStore sets the native state-plane read/write store.
func (s *Server) SetStateStore(store statePlane) {
	s.stateStore = store
}

// SetDirectiveCaptureService wires the S4a remember_directive service seam.
func (s *Server) SetDirectiveCaptureService(service directiveCaptureService) {
	s.directiveCaptureService = service
}

// SetBehavioralRulesStore sets the behavioral rules store (US3 Commit C).
func (s *Server) SetBehavioralRulesStore(brs *gorm.BehavioralRulesStore) {
	s.behavioralRulesStore = brs
}

// SetRuleGovernanceStore wires proposal-only rule governance capture.
func (s *Server) SetRuleGovernanceStore(store ruleGovernanceStoreAPI) {
	s.ruleGovernanceStore = store
	s.ruleGovernanceReadStore = store
}

func (s *Server) SetRuleInjectionTelemetryStore(store ruleInjectionTelemetryReader) {
	s.ruleInjectionTelemetry = store
}

func (s *Server) SetPromotionStore(ps *gorm.PromotionStore) {
	s.promotionStore = ps
}

func (s *Server) SetGraphStore(gs *graph.Store) {
	s.graphStore = gs
}

// SetNodesStore wires the NodesStore for the add_node graph action (T014, TG2).
func (s *Server) SetNodesStore(ns *graph.NodesStore) {
	s.nodesStore = ns
}

// SetAuditStore sets the audit store for mutation audit logging (Milestone D FR-D2).
// When ENGRAM_VNEXT_ENABLED != "true", this setter may still be called but audit
// writes are gated at the handler level.
func (s *Server) SetAuditStore(as *gorm.AuditStore) {
	s.auditStore = as
}

// SetPurgeStore sets the purge store for the purge_project admin action (Milestone D T008).
func (s *Server) SetPurgeStore(ps *gorm.PurgeStore) {
	s.purgeStore = ps
}

// SetCandidateStore wires the crystallization candidate store (Milestone-F TG4 T026).
// Must be called when ENGRAM_VNEXT_F_ENABLED=true to enable the 4 candidate MCP tools.
func (s *Server) SetCandidateStore(cs *gorm.CandidateStore) {
	s.candidateStore = cs
}

// SetSnapshotStore wires the bulk-op snapshot store (Milestone-F TG6 T043).
// Must be called when ENGRAM_VNEXT_F_ENABLED=true to enable the 4 governance MCP tools.
func (s *Server) SetSnapshotStore(ss *gorm.SnapshotStore) {
	s.snapshotStore = ss
}

// SetBulkFacade wires the bulk-op facade (Milestone-F TG6 T044).
// Enables bulk_promote, bulk_delete, bulk_supersede MCP tools with dry-run support.
func (s *Server) SetBulkFacade(f *bulkops.Facade) {
	s.bulkFacade = f
}

// setTestAuditWriter injects a mock auditWriter for unit tests.
// Must only be called from _test.go files.
func (s *Server) setTestAuditWriter(w auditWriter) {
	s.testAuditWriter = w
}

// setTestMemoryEditor injects a mock memoryEditor for unit tests.
// Must only be called from _test.go files.
func (s *Server) setTestMemoryEditor(me memoryEditor) {
	s.testMemoryEditor = me
}

// setTestMemorySignificanceUpdater injects a mock memorySignificanceUpdater for unit tests.
// Must only be called from _test.go files.
func (s *Server) setTestMemorySignificanceUpdater(msu memorySignificanceUpdater) {
	s.testMemorySignificanceUpdater = msu
}

// SetWriteLintOrchestrator wires the write-lint orchestrator for the two-phase
// write-lint protocol (T035, Milestone F TG5). When nil (the default), the
// store_memory handler follows the pre-TG5 legacy path exactly.
// Call this only when ENGRAM_VNEXT_F_ENABLED=true is expected at runtime.
func (s *Server) SetWriteLintOrchestrator(o *writelint.Orchestrator) {
	s.writeLint = o
}

// SetRedactionRules wires pre-compiled operator redaction rules (T036, ADR-F-004).
// Rules are compiled once at startup from ENGRAM_REDACTION_RULES_PATH; this setter
// transfers the compiled slice from the service to the MCP server so handleStoreMemory
// can run ScrubCompiled on the hot path without per-call regexp.Compile.
// An empty/nil slice is a valid no-op (redaction layer disabled).
func (s *Server) SetRedactionRules(rules []redaction.CompiledRule) {
	s.redactionRules = rules
}

// SetEmbeddingStores wires the embedding client and store for async memory embedding.
func (s *Server) SetEmbeddingStores(client *embedding.Client, store *embedding.Store) {
	s.embeddingClient = client
	s.embeddingStore = store
}

// SetRerankClient wires the cross-encoder rerank client for the recall path (rank-4).
// A nil client (the default when ENGRAM_RERANK_URL is unset) leaves recall on the
// fusion order — the reranker is strictly additive and failure-silent.
func (s *Server) SetRerankClient(client *reranking.Client) {
	s.rerankClient = client
}

// SetStatsDB wires the raw gorm.DB handle used by handleGetMemoryStats for
// injection_log / citation_log / memories-by-status raw SQL queries.
// Must be called with the same *gorm.DB handle the worker Service uses
// (s.store.GetDB()) to share the connection pool.
func (s *Server) SetStatsDB(db *gormlib.DB) {
	s.statsDB = db
}

// HandleRequest dispatches a JSON-RPC request and returns the response.
// This is the public wrapper for the private handleRequest method,
// enabling the gRPC adapter to invoke tool calls without duplicating dispatch logic.
func (s *Server) HandleRequest(ctx context.Context, req *Request) *Response {
	return s.handleRequest(ctx, req)
}

// ListTools returns all available tool definitions (primary + secondary).
// Used by the gRPC adapter to populate the Initialize response.
func (s *Server) ListTools() []Tool {
	// Build tools list identical to handleToolsList with include_all=true.
	// We construct a synthetic request to reuse the existing tool-list logic.
	req := &Request{
		JSONRPC: "2.0",
		ID:      float64(0),
		Method:  "tools/list",
		Params:  json.RawMessage(`{"include_all":true}`),
	}
	resp := s.handleToolsList(req)
	if resp == nil || resp.Error != nil {
		return nil
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := result["tools"]
	if !ok {
		return nil
	}
	tools, ok := raw.([]Tool)
	if !ok {
		return nil
	}
	return tools
}

// Version returns the server version string.
func (s *Server) Version() string {
	return s.version
}

// Request represents a JSON-RPC request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response represents a JSON-RPC response.
type Response struct {
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
	JSONRPC string `json:"jsonrpc"`
}

// Error represents a JSON-RPC error.
type Error struct {
	Data    any    `json:"data,omitempty"`
	Message string `json:"message"`
	Code    int    `json:"code"`
}

// ToolCallParams represents parameters for tools/call method.
type ToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Tool tier constants for tool visibility grouping.
const (
	tierCore   = 1 // T1: Always visible — most-used tools
	tierUseful = 2 // T2: Visible by default — regularly useful tools
	tierAdmin  = 3 // T3+: Hidden by default — admin, analytics, bulk ops
)

// Tool represents an MCP tool definition.
type Tool struct {
	InputSchema map[string]any `json:"inputSchema"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	tier        int            // not exported, not serialized — used for tiering
}

// Run processes incoming JSON-RPC requests from stdin until ctx is cancelled
// or stdin is closed. Each newline-delimited JSON object is dispatched to
// handleRequest and the response written back to stdout.
func (s *Server) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(s.stdin)
	finished := make(chan error, 1)

	go func() {
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				finished <- ctx.Err()
				return
			default:
			}

			line := scanner.Text()
			if line == "" {
				continue
			}

			var req Request
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				s.sendError(nil, -32700, "Parse error", err)
				continue
			}

			if resp := s.handleRequest(ctx, &req); resp != nil {
				s.sendResponse(resp)
			}
		}
		finished <- scanner.Err()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-finished:
		if err != nil {
			return fmt.Errorf("scanner error: %w", err)
		}
		return nil
	}
}

// handleRequest routes a parsed JSON-RPC request to the appropriate handler.
// Per JSON-RPC 2.0, notifications (ID == nil) must not produce a response.
func (s *Server) handleRequest(ctx context.Context, req *Request) *Response {
	if req.ID == nil {
		s.handleNotification(req)
		return nil
	}

	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	// Graceful stubs for unimplemented capabilities — return empty results
	// instead of "Method not found" to prevent clients from treating missing
	// features as errors.
	case "resources/list":
		return &Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"resources": []any{}}}
	case "resources/templates/list":
		return &Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"resourceTemplates": []any{}}}
	case "prompts/list":
		return &Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"prompts": []any{}}}
	case "completion/complete":
		return &Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"completion": map[string]any{"values": []any{}}}}
	default:
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &Error{Code: -32601, Message: "Method not found"},
		}
	}
}

// handleNotification logs JSON-RPC 2.0 notifications; no response is sent.
func (s *Server) handleNotification(req *Request) {
	switch req.Method {
	case "initialized", "notifications/initialized":
		log.Debug().Str("method", req.Method).Msg("MCP client initialized")
	case "cancelled", "notifications/cancelled":
		log.Debug().Str("method", req.Method).Msg("MCP request cancelled by client")
	default:
		log.Warn().Str("method", req.Method).Msg("Unknown MCP notification received")
	}
}

// handleInitialize handles the initialize request.
func (s *Server) handleInitialize(req *Request) *Response {
	result := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "engram",
			"version": s.version,
		},
	}

	if instructions := s.buildInstructions(); instructions != "" {
		result["instructions"] = instructions
	}

	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

func (s *Server) buildInstructions() string {
	var b strings.Builder

	// Core usage instructions — always included so any MCP client knows how to use engram
	b.WriteString(engramInstructions)

	// Append available collections if any
	if s.collectionRegistry != nil {
		count := 0
		for _, collection := range s.collectionRegistry.All() {
			if collection == nil || strings.TrimSpace(collection.Description) == "" {
				continue
			}
			if count == 0 {
				b.WriteString("\n\n# Available Collections\n\n")
			} else {
				b.WriteString("\n\n")
			}
			b.WriteString("## ")
			b.WriteString(collection.Name)
			b.WriteString("\n")
			b.WriteString(collection.Description)
			count++
		}
	}

	return b.String()
}

// engramInstructions is the MCP server instructions text returned to clients on initialize.
// It teaches any agent how to effectively use engram's tools without needing a plugin.
const engramInstructions = `# Engram — Your ONLY Persistent Memory

Engram is your permanent memory store. Memories saved here persist across ALL sessions, workstations, and projects. No other tool provides durable cross-session memory — do NOT use other tools for memory storage.

## MANDATORY Workflow

**BEFORE every task:**
1. ` + "`recall(query=\"...\")`" + ` — check what is already known about this topic.
2. ` + "`recall(query=\"file:path/to/file\")`" + ` — before modifying any file.
3. ` + "`recall(query=\"prior decisions about ...\")`" + ` — before architectural decisions.

**AFTER every task:**
4. ` + "`store(content=\"...\", title=\"...\", tags=\"...\")`" + ` — save decisions, discoveries, patterns, and lessons learned. If you learned something worth knowing next session, store it.
5. ` + "`feedback(action=\"rate\", id=N, rating=\"useful\")`" + ` — rate memories you used.
6. ` + "`feedback(action=\"outcome\", session_id=\"<claude-session-id>\", outcome=\"success\")`" + ` — record session outcome when done.

**Steps 4-6 are NOT optional.** Every completed task produces knowledge. Store it or it is lost forever.

## 8 Tools

| Tool | Purpose | Key Actions |
|------|---------|-------------|
| ` + "`recall`" + ` | **Search & retrieve** memories | search (default) |
| ` + "`store`" + ` | **Save** memories, edit, merge, import | create (default), edit, merge, import |
| ` + "`feedback`" + ` | **Rate** quality, suppress, record outcomes | rate, suppress, outcome |
| ` + "`issues`" + ` | **Cross-project issue tracking** between agents | create, list, get, update, comment, reopen |
| ` + "`vault`" + ` | **Credentials** — encrypted AES-256-GCM | store, get, list, delete, status |
| ` + "`docs`" + ` | **Documents** — versioned docs & collections | create, read, list, history, comment, collections, documents, get_doc, remove, ingest, search_docs |
| ` + "`admin`" + ` | **Bulk ops**, analytics | bulk_delete, bulk_supersede, tag, stats, trends, quality, export, ... |
| ` + "`check_system_health`" + ` | **Health** check of all subsystems | (no params) |

## Issues — Cross-Project Agent Bug Tracker

The ` + "`issues`" + ` tool tracks bugs, feature requests, and tasks between agents across projects.
**Do NOT use ` + "`store`" + ` or ` + "`docs`" + ` for issues** — they lack lifecycle management.

**For the full triage workflow use the ` + "`/engram:issue`" + ` slash command.** It walks through: (1) live issues assigned to you, (2) your reopens, (3) your cross-project issues to verify, (4) filing new issues. Run it at the start of every session in a tracked project.

**Engram issues are ONLY for engram-tracked projects.** Before filing an issue against another project, check it exists in the tracker: ` + "`GET /api/issues/tracked-projects`" + ` returns the list of projects that have observations or issues in engram. If the target project is NOT in that list, use its native issue tracker (GitHub, Linear, etc.) — engram agents working on it won't see injected issues otherwise.

### Lifecycle
` + "`open → acknowledged (auto) → resolved (target agent) → closed (source confirms) ⟲ reopened`" + `
- **Target agent** (assignee): resolves issues or comments with progress. Cannot close.
- **Source agent** (creator): verifies fix and closes, OR reopens if fix didn't work.
- **Closed/rejected** issues disappear from all injections.

### Your Role as TARGET Agent (issue assigned to your project)
When you see ` + "`<open-issues>`" + ` in your session context:
1. Read each issue carefully — understand what the source agent needs
2. Work on it OR comment explaining timeline/blockers
3. When fixed: ` + "`issues(action=\"update\", id=N, status=\"resolved\", comment=\"Fixed in commit abc123\")`" + `
4. Do NOT close — only the source agent (or human operator) closes after verifying

### Your Role as SOURCE Agent (you created the issue)
When you see ` + "`<resolved-issues from-you>`" + ` in your session context:
1. Read the resolution comment — understand what was done
2. Verify the fix actually works (test, read code, check behavior)
3. If fix works: ` + "`issues(action=\"close\", id=N)`" + ` — confirms fix, removes from all injections
4. If fix doesn't work: ` + "`issues(action=\"reopen\", id=N, body=\"Still broken because...\")`" + `

### Actions (ALWAYS pass your current project via ` + "`project`" + ` param for audit trail)
**Create:** ` + "`issues(action=\"create\", title=\"...\", body=\"...\", priority=\"high\", target_project=\"other-project\", project=\"my-project\")`" + `
**List yours:** ` + "`issues(action=\"list\", source_project=\"my-project\", status=\"resolved\")`" + ` — issues you created
**List assigned:** ` + "`issues(action=\"list\", project=\"my-project\")`" + ` — issues targeting your project
**Comment:** ` + "`issues(action=\"comment\", id=N, body=\"...\", project=\"my-project\")`" + `
**Resolve:** ` + "`issues(action=\"update\", id=N, status=\"resolved\", comment=\"Fixed in...\", project=\"my-project\")`" + `
**Close:** ` + "`issues(action=\"close\", id=N, project=\"my-project\")`" + ` — source confirms fix (only source project can close)
**Reopen:** ` + "`issues(action=\"reopen\", id=N, body=\"Still broken...\", project=\"my-project\")`" + `

**CRITICAL:** The ` + "`project`" + ` parameter identifies WHO is acting. Without it, comments show as anonymous in audit trails. Always pass your current working project name.

### DO NOT
- Close issues you didn't create (only source/operator can close)
- Change issue status without reading the issue first
- Reopen resolved issues without verifying the fix failed
- Use store/docs for cross-project bugs — use issues
- Create an issue without first checking for duplicates: ` + "`issues(action=\"list\", target_project=\"...\", status=\"\")`" + `
- Decide that "your part is done, remaining work is another project's problem" — that is a scope decision for the operator, not the agent. Comment your analysis, but do not resolve and split into new issues without operator approval
- Resolve an issue if the original requirement is only partially met — comment with progress instead

## What to Store

After completing work, store observations about:
- **Decisions made** and WHY (architecture, library choices, trade-offs)
- **Bugs found** in the CURRENT project and their root cause (prevents recurrence)
- **Patterns discovered** (recurring code structures, project conventions)
- **Lessons learned** (what worked, what failed, what to avoid)
- **File knowledge** (what a file does, gotchas, non-obvious behavior)

**Bugs/tasks for OTHER projects → use ` + "`issues`" + `**, not ` + "`store`" + `.** ` + "`store`" + ` is for knowledge. ` + "`issues`" + ` is for actionable work items.

Use ` + "`store(action=\"import\", path=\"...\")`" + ` to bulk import pre-authored observations when you already have them in file form.

## Workflow Patterns

**Starting work:** Context is auto-injected by hooks. Use ` + "`recall(query=\"...\")`" + ` for deeper search.
**Before modifying code:** ` + "`recall(query=\"file:path/to/file\")`" + ` to find file-related memories.
**After completing a feature:** ` + "`store(content=\"...\", title=\"...\", type=\"decision\")`" + ` — capture what was built and why.
**After fixing a bug:** ` + "`store(content=\"...\", title=\"...\", type=\"discovery\")`" + ` — capture root cause and fix.
**After research:** ` + "`store(content=\"...\", title=\"...\", type=\"discovery\")`" + ` — capture findings. (Do NOT use memory_type values like ` + "`insight`" + ` in the observation ` + "`type`" + ` field.)
**Found a bug in another project:** ` + "`issues(action=\"create\", title=\"...\", target_project=\"...\", priority=\"high\")`" + ` — NOT store.
**Secrets:** ` + "`vault(action=\"store\")`" + ` for API keys. Never store secrets in observations.

## Markdown Formatting (MANDATORY for all text content)

All text rendered in the dashboard supports full Markdown with syntax highlighting.
**Always use proper formatting** — never paste raw unformatted code or terminal output.

- **Code:** wrap in fenced blocks with language tag: ` + "````" + `go\\nfunc main(){}\\n` + "````" + `
- **Terminal:** ` + "````" + `bash\\n$ command\\noutput\\n` + "````" + `
- **Diffs:** ` + "````" + `diff\\n-old line\\n+new line\\n` + "````" + `
- **YAML/JSON/Config:** ` + "````" + `yaml\\nkey: value\\n` + "````" + `
- **Inline code:** ` + "\\`" + `variable_name` + "\\`" + ` for identifiers, paths, commands
- **Lists:** ` + "`- item`" + ` for findings, steps, bullet points
- **Bold:** ` + "`**important**`" + ` for emphasis

This applies to: ` + "`store(content=...)`" + `, ` + "`issues(body=..., comment=...)`" + `, ` + "`docs(content=...)`" + `.
Unfenced text renders as plain paragraphs — code without fencing is unreadable.

## Advanced: All Tools

By default, engram exposes 9 consolidated tools (above). Each tool supports multiple actions.
If you need the full expanded tool list (50+ individual tools), call ` + "`tools/list`" + ` with ` + "`include_all: true`" + `.

`

// storeMemoryTool returns the store_memory tool definition.
//
// Schema contract (per TestStoreMemoryToolSchema_FlagOff_HasNewProperties_ButRuntimeIgnores):
//   - privacy_scope and session_id are advertised unconditionally so clients always
//     discover them; the runtime handler honors them only when ENGRAM_VNEXT_F_ENABLED=true.
//   - dry_run, force, resolution_token, option, target_memory_id are advertised only
//     when ENGRAM_VNEXT_F_ENABLED=true (T044 FR-F6.b — write-lint phase fields).
func storeMemoryTool() Tool {
	props := map[string]any{
		"content":    map[string]any{"type": "string", "description": "The content/knowledge to remember"},
		"title":      map[string]any{"type": "string", "description": "Short title for the memory"},
		"tags":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Concept tags (supports hierarchical: lang:go:concurrency)"},
		"rejected":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Alternatives considered and dismissed (for decision observations)"},
		"type":       map[string]any{"type": "string", "description": "Memory type: decision, bugfix, feature, discovery, refactor"},
		"importance": map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Importance score (0-1)"},
		"scope":      map[string]any{"type": "string", "enum": []string{"project", "global"}, "description": "Legacy 2-tier visibility scope. Preserved for backward compatibility (RI-F2); prefer privacy_scope when ENGRAM_VNEXT_F_ENABLED is on."},
		// privacy_scope and session_id are unconditional in schema (clients discover them at all
		// times); the runtime handler ignores them when ENGRAM_VNEXT_F_ENABLED=false (RI-F1).
		"privacy_scope":    map[string]any{"type": "string", "enum": []string{"private", "project", "shared", "global"}, "description": "4-tier visibility scope (engram vNext Milestone F). Honored when ENGRAM_VNEXT_F_ENABLED=true. Empty defaults to project (or to the 4-tier mapping of legacy `scope` when both omitted). Invalid values return 'invalid_privacy_scope:' structured error."},
		"session_id":       map[string]any{"type": "string", "description": "Caller's session identifier. Populates Memory.SourceSessions for private-scope filtering. Honored only when ENGRAM_VNEXT_F_ENABLED=true. Empty means workstation-only-suffices branch is used on subsequent recalls (per spec FR-F1 AMEND 2026-05-25)."},
		"agent_visibility": map[string]any{"type": "string", "enum": []string{"private", "shared"}, "description": "Principal-memory visibility. private requires an authenticated principal; shared is the default for owned writes. Owner fields are server-derived from auth.Identity and are not accepted as tool arguments."},
		"domain":           map[string]any{"type": "string", "description": "Optional memory domain for later ownership policy and operator filtering. Does not grant ownership by itself."},
		"project":          map[string]any{"type": "string", "description": "Project ID (defaults to current)"},
		"ttl_days":         map[string]any{"type": "integer", "minimum": 1, "description": "TTL in days for verified facts. Auto-computed from tags if not provided. Only applies to observations with 'verified' tag."},
		"always_inject":    map[string]any{"type": "boolean", "description": "If true, this memory will be injected into every agent context regardless of query relevance. Use for behavioral rules that must always be present."},
		"agent_source":     map[string]any{"type": "string", "enum": []string{"claude-code", "codex", "gemini", "other", "unknown"}, "description": "Which AI tool created this observation"},
		"supersedes":       map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "IDs of memories this new memory replaces"},
	}
	// dry_run and write-lint phase fields are only advertised when the flag is on.
	// Advertising them unconditionally would imply they work on flag-off servers.
	if vnextFEnabled() {
		props["dry_run"] = map[string]any{"type": "boolean", "description": "T044 FR-F6.b: when true, returns a preview of what would be stored (after redaction) without writing to DB. write_lint field in response indicates whether Phase1 would run on a live call."}
		props["force"] = map[string]any{"type": "boolean", "description": "T035: when true, bypasses write-lint Phase1/Phase2 and writes directly (legacy path). Logged as legacy_force_write in audit. Honored only when ENGRAM_VNEXT_F_ENABLED=true."}
		props["resolution_token"] = map[string]any{"type": "string", "description": "T035 Phase2: resolution token minted by a prior Phase1 response. When set, commits the write using the chosen option. Cannot combine with force=true."}
		props["option"] = map[string]any{"type": "string", "description": "T035 Phase2: resolution option chosen by the caller (e.g. merge_with, supersede, link_contradiction, ignore_signals). Required when resolution_token is set."}
		props["target_memory_id"] = map[string]any{"type": "integer", "description": "T035 Phase2: target memory ID for merge_with / supersede options. Required for those options."}
	}
	return Tool{
		Name:        "store_memory",
		Description: "Explicitly store a memory/observation. Use when you want to remember something specific across sessions.",
		tier:        tierCore,
		InputSchema: map[string]any{
			"type":       "object",
			"required":   []string{"content"},
			"properties": props,
		},
	}
}

// recallMemoryTool returns the recall_memory tool definition.
// Schema composition (two independent flags):
//
//   - ENGRAM_VNEXT_ENABLED=true: adds expand_graph/min_confidence/tier_filter/explain
//     (W3 hybrid retrieval FR-C4 params).
//   - ENGRAM_VNEXT_F_ENABLED=true: adds session_id/include_scopes
//     (F-TG1 privacy_scope filtering per T005).
//
// Flags are independent; both may be on simultaneously (ON/ON = hybrid + scope params).
// Flag-OFF clients see only the base 6-field schema (byte-identical to pre-W3/pre-F).
func recallMemoryTool() Tool {
	// session_id and include_scopes are always in the schema (unconditional,
	// matching the store_memory design precedent in TestStoreMemoryToolSchema_FlagOff):
	// schema discovery is deterministic regardless of runtime env; ENGRAM_VNEXT_F_ENABLED
	// gates runtime behavior only (same pattern as store_memory privacy_scope/session_id).
	props := map[string]any{
		"query":   map[string]any{"type": "string", "description": "Natural language query"},
		"tags":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Filter by concept tags"},
		"type":    map[string]any{"type": "string", "description": "Filter by observation type"},
		"limit":   map[string]any{"type": "number", "default": 10, "minimum": 1, "maximum": 50},
		"format":  map[string]any{"type": "string", "enum": []string{"text", "items", "detailed"}, "default": "text"},
		"project": map[string]any{"type": "string", "description": "Project ID to scope results (includes project-scoped and global observations)"},
		"session_id": map[string]any{
			"type":        "string",
			"description": "Caller's session identifier. Used by scope.Resolve to admit private-scope rows that name this session in source_sessions. Honored only when ENGRAM_VNEXT_F_ENABLED=true. Empty means workstation-only-suffices branch is used (per spec FR-F1 AMEND 2026-05-25).",
		},
		"include_scopes": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string", "enum": []string{"private", "project", "shared", "global"}},
			"description": "Restrict returned memories to the named privacy_scope tiers. Empty/omitted means all 4 tiers are returned (subject to scope.Resolve visibility). Honored only when ENGRAM_VNEXT_F_ENABLED=true. Unknown enum values return 'invalid_include_scopes:' structured error.",
		},
		"include_principals": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":     "object",
				"required": []string{"principal", "principal_kind"},
				"properties": map[string]any{
					"principal":      map[string]any{"type": "string", "description": "Principal identifier to explicitly include, e.g. agent/alice."},
					"principal_kind": map[string]any{"type": "string", "enum": []string{"human", "agent", "service"}, "description": "Principal kind."},
				},
			},
			"description": "Explicit principal widening for recall. Self include is allowed; cross-principal private widening requires admin identity and durable audit.",
		},
		// T019 TG3: explainable rerank + missing filters (Milestone F TG3).
		// Schema-unconditional (same pattern as session_id/include_scopes above):
		// schema discovery is deterministic regardless of env; ENGRAM_VNEXT_F_ENABLED
		// gates runtime behavior (tg3Active = vnextFEnabled && any non-default flag).
		"confidence_min": map[string]any{
			"type":        "number",
			"minimum":     0,
			"maximum":     1,
			"description": "Only return memories whose confidence score is ≥ this value [0,1]. Applied at SQL layer via ListWithFilters. Honored only when ENGRAM_VNEXT_F_ENABLED=true.",
		},
		"include_superseded": map[string]any{
			"type":        "boolean",
			"description": "When true, include memories with status='superseded' in addition to status='active'. Honored only when ENGRAM_VNEXT_F_ENABLED=true.",
		},
		"include_rationale": map[string]any{
			"type":        "boolean",
			"description": "When true, attach a per-result ranking_rationale object with v5-surface fields: recency_days, confidence, citation_count, tier, substring_match, filters_applied. Honored only when ENGRAM_VNEXT_F_ENABLED=true.",
		},
	}
	desc := "Recall memories/observations by semantic search. Use to retrieve previously stored knowledge."
	if os.Getenv("ENGRAM_VNEXT_ENABLED") == "true" {
		// Vnext-gated additional parameters (FR-C4 hybrid retrieval, W3).
		// Independent of ENGRAM_VNEXT_F_ENABLED; both flags may be active simultaneously
		// (ON/ON = hybrid retrieval + scope filtering enforced per d9eea82 contract).
		props["expand_graph"] = map[string]any{
			"type":        "boolean",
			"description": "Enable Tier2 graph expansion: fetches 1-hop neighbours of top-5 results (opt-in, <200ms budget). Requires knowledge graph edges to be present.",
		}
		props["min_confidence"] = map[string]any{
			"type":        "number",
			"minimum":     0,
			"maximum":     1,
			"description": "Minimum composite score floor [0,1]. Results below this threshold are excluded from the response.",
		}
		props["tier_filter"] = map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string", "enum": []string{"tier0_exact", "tier1_fts", "tier1_vector", "tier2_graph"}},
			"description": "Restrict results to specific retrieval tiers. Empty = all tiers. Values: tier0_exact, tier1_fts, tier1_vector, tier2_graph.",
		}
		props["explain"] = map[string]any{
			"type":        "boolean",
			"description": "Include per-result ranking_explanation {relevance, recency, importance, fused_score, source_tier} in the response.",
		}
		desc = "Recall memories/observations using FR-C4 hybrid retrieval (FTS+vector RRF + recency + importance scoring). Supports tier filtering, graph expansion, and score explanations."
	}
	return Tool{
		Name:        "recall_memory",
		Description: desc,
		tier:        tierCore,
		InputSchema: map[string]any{
			"type":       "object",
			"required":   []string{"query"},
			"properties": props,
		},
	}
}

// primaryTools returns the consolidated primary tools shown by default (without
// include_all): recall, store, feedback, vault, settings, docs, admin, issues,
// check_system_health. TestHandleToolsList_DefaultCountMatchesPrimary pins the count.
func (s *Server) primaryTools() []Tool {
	return []Tool{
		{
			Name:        "recall",
			Description: "Search and retrieve memories. Actions: search (default, trivial SQL filter over memories).",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":  map[string]any{"type": "string", "enum": []string{"search"}, "default": "search", "description": "Action to perform"},
					"query":   map[string]any{"type": "string", "description": "Search query / substring filter (for search)"},
					"project": map[string]any{"type": "string", "description": "Project name filter"},
					"limit":   map[string]any{"type": "number", "description": "Max results"},
				},
			},
		},
		{
			Name:        "store",
			Description: "Store, edit, merge, or import memories. Actions: create (default), edit, merge, import.",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":           map[string]any{"type": "string", "enum": []string{"create", "edit", "merge", "import"}, "default": "create", "description": "Action to perform"},
					"content":          map[string]any{"type": "string", "description": "Observation content (for create)"},
					"title":            map[string]any{"type": "string", "description": "Title (for create, edit)"},
					"id":               map[string]any{"type": "integer", "description": "Observation ID (for edit)"},
					"source_id":        map[string]any{"type": "integer", "description": "Source observation ID (for removed merge compatibility errors)"},
					"target_id":        map[string]any{"type": "integer", "description": "Target observation ID (for removed merge compatibility errors)"},
					"type":             map[string]any{"type": "string", "enum": []string{"decision", "bugfix", "feature", "refactor", "discovery", "change", "guidance", "credential", "entity", "wiki", "pitfall", "operational", "timeline"}, "description": "Observation type (for create). Must be an observation type, not a memory_type value like insight/context/pattern."},
					"tags":             map[string]any{"type": "string", "description": "Comma-separated tags (for create)"},
					"scope":            map[string]any{"type": "string", "description": "Scope: project/global/agent (for create)"},
					"always_inject":    map[string]any{"type": "boolean", "description": "Always inject in context (for create, edit)"},
					"dry_run":          map[string]any{"type": "boolean", "description": "Preview a create without durable writes"},
					"force":            map[string]any{"type": "boolean", "description": "Bypass write-lint for a create when the vNext F gate is enabled"},
					"supersedes":       map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Memory IDs replaced by a create"},
					"rejected":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Rejected alternatives for a create"},
					"importance":       map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Explicit importance for a create"},
					"ttl_days":         map[string]any{"type": "integer", "minimum": 1, "description": "TTL in days for a create"},
					"target_memory_id": map[string]any{"type": "integer", "minimum": 1, "description": "Write-lint phase-2 target memory ID"},
					"narrative":        map[string]any{"type": "string", "description": "Narrative text (for edit)"},
					"path":             map[string]any{"type": "string", "description": "File path (for import)"},
					"project":          map[string]any{"type": "string", "description": "Project name"},
				},
			},
		},
		{
			Name:        "feedback",
			Description: "Rate observations, suppress bad ones, or record session outcome. Actions: rate, suppress, outcome. Action required.",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"action"},
				"properties": map[string]any{
					"action":     map[string]any{"type": "string", "enum": []string{"rate", "suppress", "outcome"}, "description": "Action to perform (required)"},
					"id":         map[string]any{"type": "integer", "description": "Observation ID (for rate, suppress)"},
					"rating":     map[string]any{"type": "string", "enum": []string{"useful", "not_useful"}, "description": "Rating value for action=rate"},
					"session_id": map[string]any{"type": "string", "description": "Claude session ID string (required for action=outcome)"},
					"outcome":    map[string]any{"type": "string", "enum": []string{"success", "partial", "failure", "abandoned"}, "description": "Session outcome (for action=outcome)"},
					"reason":     map[string]any{"type": "string", "description": "Outcome reason (for action=outcome)"},
				},
			},
		},
		{
			Name:        "vault",
			Description: "Manage encrypted credentials. Actions: store, get, list, delete, status. Action required.",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"action"},
				"properties": map[string]any{
					"action":  map[string]any{"type": "string", "enum": []string{"store", "get", "list", "delete", "status"}, "description": "Action to perform (required)"},
					"name":    map[string]any{"type": "string", "description": "Credential name (for store, get, delete)"},
					"value":   map[string]any{"type": "string", "description": "Credential value (for store)"},
					"scope":   map[string]any{"type": "string", "description": "Scope: project/global (for store)"},
					"project": map[string]any{"type": "string", "description": "Project name (for store)"},
				},
			},
		},
		{
			Name: "settings",
			Description: "Manage server-global model settings (reranker/embedder URL, model, API key) read by the " +
				"server on the recall path (#259). Env vars still override stored settings. Actions: set, get, list, " +
				"delete. set/delete require admin (operator) auth — they change behavior for every consumer. Secret " +
				"keys (ending .api_key) are vault-encrypted and never returned in plaintext by get/list. Action required.",
			tier: tierUseful,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"action"},
				"properties": map[string]any{
					"action":      map[string]any{"type": "string", "enum": []string{"set", "get", "list", "delete"}, "description": "Action to perform (required)"},
					"key":         map[string]any{"type": "string", "description": "Setting key, e.g. reranker.url, reranker.model, reranker.api_key, embedder.url, embedder.model, embedder.api_key (for set, get, delete)"},
					"value":       map[string]any{"type": "string", "description": "Setting value (for set)"},
					"encrypt":     map[string]any{"type": "boolean", "description": "Force vault encryption even if the key name does not end in .api_key (for set). Keys ending .api_key are always encrypted."},
					"description": map[string]any{"type": "string", "description": "Optional human description of the setting (for set)"},
				},
			},
		},
		{
			Name:        "docs",
			Description: "Versioned documents and collections. Actions: create, read, list, history, comment, collections, documents, get_doc, remove, ingest, search_docs. Action required.",
			tier:        tierUseful,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"action"},
				"properties": map[string]any{
					"action":     map[string]any{"type": "string", "enum": []string{"create", "read", "list", "history", "comment", "collections", "documents", "get_doc", "remove", "ingest", "search_docs"}, "description": "Action to perform (required)"},
					"path":       map[string]any{"type": "string", "description": "Document path (for create, read, list, history, comment)"},
					"project":    map[string]any{"type": "string", "description": "Project name"},
					"content":    map[string]any{"type": "string", "description": "Document content (for create, ingest)"},
					"collection": map[string]any{"type": "string", "description": "Collection name (for documents, get_doc, remove, ingest, search_docs)"},
					"query":      map[string]any{"type": "string", "description": "Search query (for search_docs)"},
					"version":    map[string]any{"type": "number", "description": "Version number (for read)"},
					"comment":    map[string]any{"type": "string", "description": "Comment text (for comment)"},
					"doc_type":   map[string]any{"type": "string", "description": "Document type (for create, list)"},
					"id":         map[string]any{"type": "string", "description": "Document ID (for get_doc, remove)"},
				},
			},
		},
		buildAdminTool(),
		{
			Name: "issues",
			Description: "Cross-project issue tracker between agents. Do NOT use store/docs for issues.\n\n" +
				"`project` = YOUR current working project slug (identifies who is acting — audit trail).\n" +
				"`target_project` = project the issue is FOR (where it will be injected).\n" +
				"Lifecycle: open → acknowledged (auto) → resolved → closed ⟲ reopened.\n" +
				"Target agent resolves, source agent closes. Only source or dashboard operator can close.\n\n" +
				"FORMATTING (body and comments support Markdown — rendered in dashboard):\n" +
				"- Wrap code in fenced blocks: ```go\\nfunc main(){}\\n``` (with language tag)\n" +
				"- Wrap terminal output: ```bash\\n$ command\\noutput\\n```\n" +
				"- Wrap diffs: ```diff\\n-old line\\n+new line\\n```\n" +
				"- Use **bold**, *italic*, `inline code` for emphasis\n" +
				"- Use - bullet lists for steps/findings\n" +
				"- Do NOT paste raw unformatted code/output — always fence it.",
			tier:        tierCore,
			InputSchema: issuesToolSchema(),
		},
	}
}

// handleToolsList returns the list of available tools.
// Note (v5/US9): search, timeline, decisions, changes, how_it_works, find_by_concept,
// find_by_type removed — they were backed by internal/search (dropped). Use the
// consolidated recall(action="search") tool instead.
func (s *Server) handleToolsList(req *Request) *Response {
	tools := []Tool{
		{
			Name:        "find_by_file",
			Description: "Find observations related to specific file paths.",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"files"},
				"properties": map[string]any{
					"files":     map[string]any{"type": "string", "description": "File path(s) to filter by"},
					"type":      map[string]any{"type": "string"},
					"concepts":  map[string]any{"type": "string"},
					"project":   map[string]any{"type": "string"},
					"dateStart": map[string]any{"type": []string{"string", "number"}},
					"dateEnd":   map[string]any{"type": []string{"string", "number"}},
					"orderBy":   map[string]any{"type": "string", "enum": []string{"date_desc", "date_asc"}, "default": "date_desc"},
					"limit":     map[string]any{"type": "number", "default": 20},
					"offset":    map[string]any{"type": "number", "default": 0},
					"format":    map[string]any{"type": "string", "enum": []string{"index", "full"}, "default": "index"},
				},
			},
		},
		// find_by_file_context, find_by_type, timeline, get_recent_context, get_context_timeline,
		// get_timeline_by_query removed in v5 (US9) — backed by internal/search which is dropped.
		{
			Name:        "find_similar_observations",
			Description: "Find observations semantically similar to a query or observation. Uses vector similarity search to find related content. Useful for detecting duplicates before creating new observations.",
			tier:        tierUseful,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]any{
					"query":          map[string]any{"type": "string", "description": "Text to find similar observations for"},
					"project":        map[string]any{"type": "string", "description": "Filter by project name"},
					"min_similarity": map[string]any{"type": "number", "default": 0.7, "minimum": 0.0, "maximum": 1.0, "description": "Minimum similarity threshold (0-1)"},
					"limit":          map[string]any{"type": "number", "default": 10, "minimum": 1, "maximum": 50},
				},
			},
		},
		{
			Name:        "get_memory_stats",
			Description: "Get statistics about the memory system including observation counts, vector stats, pattern counts, and search metrics.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "search_sessions",
			Description: "[DEPRECATED] Full-text search across indexed Claude Code sessions. Removed in v5 (US3) — indexed_sessions table dropped.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Search query for session content"},
					"limit": map[string]any{"type": "number", "default": 10, "minimum": 1, "maximum": 50},
				},
			},
		},
		{
			Name:        "list_sessions",
			Description: "[DEPRECATED] List indexed Claude Code sessions with optional workstation/project filters. Removed in v5 (US3) — indexed_sessions table dropped.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"workstation_id": map[string]any{"type": "string", "description": "Filter by workstation ID"},
					"project_id":     map[string]any{"type": "string", "description": "Filter by project ID"},
					"limit":          map[string]any{"type": "number", "default": 20, "minimum": 1, "maximum": 100},
					"offset":         map[string]any{"type": "number", "default": 0, "minimum": 0},
				},
			},
		},
		// Import instincts tool (always available — observation store is required for MCP)
		{
			Name:        "import_instincts",
			Description: "Import ECC instinct files as guidance observations. Provide EITHER 'files' (array of {name, content} — preferred for remote servers) OR 'path' (legacy local directory). Exactly one of these is required; server enforces. Idempotent — duplicates are skipped via vector similarity check.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"files": map[string]any{
						"type":        "array",
						"description": "REQUIRED (alternative to 'path'): array of instinct files with content. Preferred for client-server mode.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"name":    map[string]any{"type": "string", "description": "Filename (e.g. 'my-instinct.md')"},
								"content": map[string]any{"type": "string", "description": "Full file content including YAML frontmatter"},
							},
							"required": []string{"name", "content"},
						},
					},
					"path": map[string]any{"type": "string", "description": "REQUIRED (alternative to 'files'): [DEPRECATED] Local filesystem path to instincts directory. Use 'files' for remote servers."},
				},
			},
		},
	}

	// Behavioral rules tools — only advertise when the store is wired (US3 Commit C).
	// Advertising store_rule/list_rules when behavioralRulesStore is nil would cause
	// every call to fail with "behavioral rules store not initialised" — unhelpful noise
	// for deployments that have not yet run migration 089.
	if s.behavioralRulesStore != nil {
		tools = append(tools,
			Tool{
				Name:        "store_rule",
				Description: "Store a behavioral rule (always-inject guidance applied at session-start). Project-scoped if project is set, global otherwise.",
				tier:        tierUseful,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"content"},
					"properties": map[string]any{
						"project":  map[string]any{"type": "string", "description": "Project name (omit for global rule)"},
						"content":  map[string]any{"type": "string", "description": "Rule content (required)"},
						"priority": map[string]any{"type": "number", "description": "Priority — higher values inject first (default 0)"},
					},
				},
			},
			Tool{
				Name:        "list_rules",
				Description: "List behavioral rules for a project (always includes global rules where project is NULL).",
				tier:        tierUseful,
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"project": map[string]any{"type": "string", "description": "Project name (omit to list only global rules)"},
						"limit":   map[string]any{"type": "number", "description": "Max results (default 50, max 500)"},
					},
				},
			},
		)
	}

	// Backfill status tool — only advertise when backfill tracker is available
	if s.backfillStatusFunc != nil {
		tools = append(tools, Tool{
			Name:        "backfill_status",
			Description: "Get status of backfill runs — total runs, per-run stored/skipped/error counts, and total observations imported from historical sessions.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		})
	}

	// Memory management tools — advertise when memory storage is available
	if s.memoryStore != nil {
		tools = append(tools,
			storeMemoryTool(),
			recallMemoryTool(),
			Tool{
				Name:        "rate_memory",
				Description: "Rate a memory as useful or not useful. Affects future ranking in search results.",
				tier:        tierCore,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"id", "rating"},
					"properties": map[string]any{
						"id":     map[string]any{"type": "integer", "description": "Observation ID to rate"},
						"rating": map[string]any{"type": "string", "enum": []string{"useful", "not_useful"}, "description": "Rating: useful or not_useful"},
					},
				},
			},
			Tool{
				Name:        "suppress_memory",
				Description: "Suppress an observation so it is excluded from future search results. The observation remains in the database but is hidden.",
				tier:        tierCore,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"id"},
					"properties": map[string]any{
						"id": map[string]any{"type": "integer", "description": "Observation ID to suppress"},
					},
				},
			},
		)
	}
	if s6OutcomeEnabledFromEnv() && s.effectiveMemorySignificanceUpdater() != nil {
		tools = append(tools, rateMemorySignificanceTool())
	}
	if s2MetaMemoryEnabled() && s.metaMemoryIndex != nil {
		tools = append(tools, knowAboutTool())
	}
	if s.principalMemoryQuerySvc != nil {
		tools = append(tools, principalMemoryQueryTool())
	}
	// Native state-plane tools are advertised only when the runtime state store is wired.
	if s.stateStore != nil {
		tools = append(tools, stateTool(), setStateTool())
	}
	// Experience/history read tools are advertised only when the CR-009 provider is wired.
	if s.experienceProvider != nil {
		tools = append(tools, experienceHistoryTools()...)
	}
	if s.temporalTruthProvider != nil && temporalTruthEnabledFromEnv() {
		tools = append(tools, temporalTruthTool(), temporalTruthRefreshTool())
	}
	if s.directiveCaptureService != nil && directivesCaptureEnabledFromEnv() {
		tools = append(tools, rememberDirectiveTool())
	}
	// Session outcome tool — only advertise when session store is available
	if s.sessionStore != nil {
		tools = append(tools,
			Tool{
				Name:        "set_session_outcome",
				Description: "Record the outcome of the current session (success/partial/failure/abandoned). Use at session end to enable closed-loop learning.",
				tier:        tierUseful,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"session_id", "outcome"},
					"properties": map[string]any{
						"session_id": map[string]any{"type": "string", "description": "Claude session ID"},
						"outcome":    map[string]any{"type": "string", "enum": []string{"success", "partial", "failure", "abandoned"}, "description": "Session outcome"},
						"reason":     map[string]any{"type": "string", "description": "Optional explanation for the outcome"},
					},
				},
			},
		)
	}

	// Adaptive memory tools — advertise only when memory store is available AND adaptive features are enabled.
	if s.memoryStore != nil && os.Getenv("ENGRAM_ADAPTIVE_ENABLED") == "true" {
		tools = append(tools,
			Tool{
				Name:        "get_memory_brief",
				Description: "Get a compact memory context for sub-agent delegation briefs. Call before spawning sub-agents to give them relevant project memory. Returns top-N memories scored by Thompson Sampling.",
				tier:        tierUseful,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{},
					"properties": map[string]any{
						"topic":           map[string]any{"type": "string", "description": "Topic hint for memory relevance filtering"},
						"project":         map[string]any{"type": "string", "description": "Project name (auto-detected if omitted)"},
						"principal":       map[string]any{"type": "string", "description": "Optional principal identifier for a principal-scoped brief."},
						"principal_kind":  map[string]any{"type": "string", "enum": []string{"human", "agent", "service"}, "default": "human", "description": "Principal kind used when principal is set."},
						"domain":          map[string]any{"type": "string", "description": "Optional memory domain for scoped briefs."},
						"visibility":      map[string]any{"type": "string", "enum": []string{"private", "shared", "all"}, "description": "Optional principal visibility filter for scoped briefs."},
						"include_private": map[string]any{"type": "boolean", "description": "Request private rows for the scoped principal. Cross-principal private widening requires admin identity and durable audit."},
						"session_id":      map[string]any{"type": "string", "description": "Optional session id for private-read audit provenance."},
						"limit":           map[string]any{"type": "integer", "description": "Max memories to return (default: 5, max: 10)"},
					},
				},
			},
		)
	}

	// Crystallization candidate tools — advertised only when ENGRAM_VNEXT_F_ENABLED=true
	// and the candidate store is wired (Milestone-F TG4 T026).
	if vnextFEnabled() && s.candidateStore != nil {
		tools = append(tools, candidateTools()...)
	}

	// Governance tools (Milestone-F TG6 T043): snapshot list/rollback/pin + redaction status.
	// Admin-gated at handler level; advertised only when snapshotStore is wired.
	if vnextFEnabled() && s.snapshotStore != nil {
		tools = append(tools, governanceTools()...)
	}

	if s.ruleGovernanceReadStore != nil {
		tools = append(tools, ruleGovernanceReadTools(s.ruleInjectionTelemetry != nil)...)
	}

	// Bulk-op tools (Milestone-F TG6 T044): bulk_promote/delete/supersede with dry-run.
	// Admin-gated at handler level. Advertised whenever ENGRAM_VNEXT_F_ENABLED=true
	// (facade may be nil — dry_run still works with nil facade via seam).
	if vnextFEnabled() {
		tools = append(tools, bulkOpsTools()...)
	}

	// Code intelligence tools (CR-006) — advertise only when ENGRAM_CODE_INTEL_ENABLED=true
	// AND the code chunk store is wired. Flag-off path is byte-identical to pre-CR-006.
	//
	// Only codebase_search is advertised as a server-side tool. codebase_status is
	// deliberately NOT advertised here: it is the daemon-side static tool's name
	// (internal/handlers/codeintel), and the daemon merges its in-memory run state
	// with the server's chunk counts by calling THIS server's codebase_status
	// handler over the engramcore proxy. Advertising it on both the daemon (static)
	// and the server (proxied) would surface a DUPLICATE codebase_status entry in
	// the daemon's tools/list (the dispatcher appends proxy tools without dedup).
	// The handler + callTool case stay registered so the daemon's proxy call still
	// resolves; only the external advertisement is suppressed.
	if codeIntelEnabled() && s.codeChunkStore != nil {
		tools = append(tools, codebaseSearchTool())
	}

	// Ambient fallback polling — advertise only when the S3 flag is on and the queue seam is wired.
	if ambientHintsEnabledFromEnv() && s.hintQueue != nil {
		tools = append(tools, ambientHintsTool())
	}

	// Credential vault tools — advertise only when credential persistence and vault keying are actually available.
	if config.GetDatabaseDSN() != "" && crypto.VaultExists(config.Get()) {
		tools = append(tools,
			Tool{
				Name:        "store_credential",
				Description: "[Vault] Securely store an encrypted credential (API key, password, token). Value is encrypted with AES-256-GCM.",
				tier:        tierUseful,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"name", "value"},
					"properties": map[string]any{
						"name":  map[string]any{"type": "string", "description": "Credential name/identifier"},
						"value": map[string]any{"type": "string", "description": "Secret value to encrypt and store"},
						"scope": map[string]any{"type": "string", "enum": []string{"project", "global"}, "default": "project", "description": "Scope: 'project' (default) or 'global' (cross-project)"},
						"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional concept tags"},
					},
				},
			},
			Tool{
				Name:        "get_credential",
				Description: "[Vault] Retrieve and decrypt a stored credential by name. Returns the decrypted value.",
				tier:        tierUseful,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"name"},
					"properties": map[string]any{
						"name": map[string]any{"type": "string", "description": "Credential name to retrieve"},
					},
				},
			},
			Tool{
				Name:        "list_credentials",
				Description: "[Vault] List all stored credentials (names and metadata only, no values).",
				tier:        tierAdmin,
				InputSchema: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
			Tool{
				Name:        "delete_credential",
				Description: "[Vault] Delete a stored credential by name.",
				tier:        tierAdmin,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"name"},
					"properties": map[string]any{
						"name": map[string]any{"type": "string", "description": "Credential name to delete"},
					},
				},
			},
			Tool{
				Name:        "vault_status",
				Description: "[Vault] Check vault encryption status: key configured, fingerprint, credential count.",
				tier:        tierAdmin,
				InputSchema: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		)
	}

	// Lifecycle tool — advertise when memory store + promotion store are available
	// and ENGRAM_LIFECYCLE_ENABLED is true.
	if s.memoryStore != nil && s.promotionStore != nil && os.Getenv("ENGRAM_LIFECYCLE_ENABLED") == "true" {
		tools = append(tools, Tool{
			Name:        "lifecycle",
			Description: "[Lifecycle] Manage memory lifecycle: view tier/decay info, promote/demote, set confidence, preview decay, check sleep cycle status.",
			tier:        tierUseful,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"action"},
				"properties": map[string]any{
					"action":        map[string]any{"type": "string", "description": "Action: info, promote, demote, set_confidence, set_defeasibility, sleep_status, decay_preview", "enum": []string{"info", "promote", "demote", "set_confidence", "set_defeasibility", "sleep_status", "decay_preview"}},
					"memory_id":     map[string]any{"type": "integer", "description": "Memory ID (required for info, promote, demote, set_confidence, set_defeasibility, decay_preview)"},
					"target_tier":   map[string]any{"type": "string", "description": "Target tier for promote/demote (working, episodic, semantic, procedural)"},
					"confidence":    map[string]any{"type": "number", "description": "Confidence value for set_confidence (0.0-1.0)"},
					"reason":        map[string]any{"type": "string", "description": "Reason for set_confidence"},
					"defeasibility": map[string]any{"type": "string", "description": "Defeasibility class for set_defeasibility (non_defeasible, slow, rapid)"},
					"days_ahead":    map[string]any{"type": "integer", "description": "Days ahead for decay_preview (default: 30)"},
				},
			},
		})
	}

	// Graph tool — advertise when graph store is available and enabled
	if s.graphStore != nil && os.Getenv("ENGRAM_GRAPH_ENABLED") == "true" {
		tools = append(tools, Tool{
			Name:        "graph",
			Description: "[Graph] Navigate and manage the knowledge graph: add/remove edges, traverse relationships, find paths, discover synonyms.",
			tier:        tierUseful,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"action"},
				"properties": map[string]any{
					"action": map[string]any{"type": "string", "description": "Action: add_edge, remove_edge, get_edges, traverse, find_path, synonyms" + func() string {
						if vnextFEnabled() {
							return ", add_node"
						}
						return ""
					}(), "enum": func() []string {
						base := []string{"add_edge", "remove_edge", "get_edges", "traverse", "find_path", "synonyms"}
						if vnextFEnabled() {
							base = append(base, "add_node")
						}
						return base
					}()},
					"source_id": map[string]any{"type": "integer", "description": "Source memory ID (for add_edge memory→memory, find_path)"},
					"target_id": map[string]any{"type": "integer", "description": "Target memory ID (for add_edge memory→memory, find_path)"},
					"memory_id": map[string]any{"type": "integer", "description": "Memory ID (for get_edges, traverse, synonyms)"},
					"edge_id":   map[string]any{"type": "integer", "description": "Edge ID (for remove_edge)"},
					"edge_type": map[string]any{"type": "string", "description": "Relationship type (e.g. uses, depends_on, contradicts, synonym_of)"},
					"weight":    map[string]any{"type": "number", "description": "Edge confidence 0.0-1.0 (default 1.0)"},
					"reasoning": map[string]any{"type": "string", "description": "Why this edge exists"},
					"direction": map[string]any{"type": "string", "description": "Edge direction for get_edges: outgoing, incoming, both"},
					"depth":     map[string]any{"type": "integer", "description": "Traversal depth (1-3, default 1)"},
					"max_depth": map[string]any{"type": "integer", "description": "Max path depth for find_path (1-3)"},
					// T014: Milestone F TG2 params (default to 'memory' for v6.2.x backward compat).
					"source_type":    map[string]any{"type": "string", "description": "Source endpoint type: 'memory' (default) or 'node'. Requires ENGRAM_VNEXT_F_ENABLED=true for 'node' value.", "enum": []string{"memory", "node"}},
					"target_type":    map[string]any{"type": "string", "description": "Target endpoint type: 'memory' (default) or 'node'. Requires ENGRAM_VNEXT_F_ENABLED=true for 'node' value.", "enum": []string{"memory", "node"}},
					"node_source_id": map[string]any{"type": "integer", "description": "Source node ID when source_type='node' (for add_edge)"},
					"node_target_id": map[string]any{"type": "integer", "description": "Target node ID when target_type='node' (for add_edge)"},
					"node_id":        map[string]any{"type": "integer", "description": "Node ID (for get_edges by node)"},
					"node_type":      map[string]any{"type": "string", "description": "Node type filter for get_edges; or the type to create for add_node (e.g. skill, file, agent)"},
					"external_ref":   map[string]any{"type": "string", "description": "External reference string for the node (for add_node)"},
					"project":        map[string]any{"type": "string", "description": "Project slug (for add_node)"},
					"privacy_scope":  map[string]any{"type": "string", "description": "Privacy scope for add_node: project (default), private, shared, global"},
				},
			},
		})
	}

	// Ingest tool — always available when memory store exists
	if s.memoryStore != nil {
		tools = append(tools, Tool{
			Name:        "ingest",
			Description: "[Ingest] Ingest documents into engram as chunked memories with source provenance. Level 1: raw chunking, no LLM needed.",
			tier:        tierUseful,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"action"},
				"properties": map[string]any{
					"action":         map[string]any{"type": "string", "description": "Action: ingest", "enum": []string{"ingest"}},
					"content":        map[string]any{"type": "string", "description": "Document content to ingest"},
					"source_title":   map[string]any{"type": "string", "description": "Human-readable source title"},
					"source_type":    map[string]any{"type": "string", "description": "Source type: text, markdown, json, yaml"},
					"project":        map[string]any{"type": "string", "description": "Target project"},
					"chunk_strategy": map[string]any{"type": "string", "description": "Chunking strategy: paragraphs, sections, fixed"},
					"dry_run":        map[string]any{"type": "boolean", "description": "Preview without storing"},
				},
			},
		})
	}

	// Document / Collection tools — only advertise when dependencies are available
	if s.documentStore != nil {
		tools = append(tools,
			Tool{
				Name:        "list_collections",
				Description: "[Documents] List all configured document collections with active document counts.",
				tier:        tierAdmin,
				InputSchema: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
			Tool{
				Name:        "list_documents",
				Description: "[Documents] List documents in a collection with metadata (path, title, hash, timestamps).",
				tier:        tierAdmin,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"collection"},
					"properties": map[string]any{
						"collection": map[string]any{"type": "string", "description": "Collection name to list documents from"},
					},
				},
			},
			Tool{
				Name:        "get_document",
				Description: "[Documents] Retrieve full document content by collection and path.",
				tier:        tierAdmin,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"collection", "path"},
					"properties": map[string]any{
						"collection": map[string]any{"type": "string", "description": "Collection name"},
						"path":       map[string]any{"type": "string", "description": "Document path within the collection"},
					},
				},
			},
			Tool{
				Name:        "remove_document",
				Description: "[Documents] Deactivate (soft delete) a document from a collection. The document and its chunks remain in storage but are excluded from search.",
				tier:        tierAdmin,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"collection", "path"},
					"properties": map[string]any{
						"collection": map[string]any{"type": "string", "description": "Collection name"},
						"path":       map[string]any{"type": "string", "description": "Document path to deactivate"},
					},
				},
			},
		)
	}

	// Document ingest/search tools are document-store gated (embedding removed in v5).
	if s.documentStore != nil {
		tools = append(tools,
			Tool{
				Name:        "ingest_document",
				Description: "[Documents] Ingest a document into a collection. Chunks the content, generates embeddings, and stores for semantic search. Skips re-embedding if content hash unchanged.",
				tier:        tierAdmin,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"collection", "path", "content"},
					"properties": map[string]any{
						"collection": map[string]any{"type": "string", "description": "Target collection name"},
						"path":       map[string]any{"type": "string", "description": "Document path (used as identifier within the collection)"},
						"content":    map[string]any{"type": "string", "description": "Full document content to ingest"},
						"title":      map[string]any{"type": "string", "description": "Optional document title"},
					},
				},
			},
			Tool{
				Name:        "search_collection",
				Description: "[Documents] Semantic search across document chunks in a collection. Returns ranked results with chunk text.",
				tier:        tierAdmin,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"query"},
					"properties": map[string]any{
						"query":      map[string]any{"type": "string", "description": "Natural language search query"},
						"collection": map[string]any{"type": "string", "description": "Collection to search (omit to search all collections)"},
						"limit":      map[string]any{"type": "number", "default": 10, "minimum": 1, "maximum": 50},
					},
				},
			},
		)
	}

	// Versioned document tools — only advertise when versioned document store is available
	if s.versionedDocumentStore != nil {
		tools = append(tools,
			Tool{
				Name:        "doc_create",
				Description: "[Versioned Docs] Create a new versioned document or a new version of an existing document. Each call increments the version atomically.",
				tier:        tierUseful,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"path", "project", "content"},
					"properties": map[string]any{
						"path":     map[string]any{"type": "string", "description": "Document path identifier (e.g. 'docs/architecture.md')"},
						"project":  map[string]any{"type": "string", "description": "Project name"},
						"content":  map[string]any{"type": "string", "description": "Full document content"},
						"doc_type": map[string]any{"type": "string", "default": "markdown", "description": "Document type (e.g. markdown, text, json)"},
						"metadata": map[string]any{"type": "string", "default": "{}", "description": "JSON metadata string"},
						"author":   map[string]any{"type": "string", "default": "agent", "description": "Author identifier"},
					},
				},
			},
			Tool{
				Name:        "doc_read",
				Description: "[Versioned Docs] Read the latest or a specific version of a versioned document.",
				tier:        tierUseful,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"path", "project"},
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": "Document path identifier"},
						"project": map[string]any{"type": "string", "description": "Project name"},
						"version": map[string]any{"type": "number", "description": "Specific version to read (omit for latest)"},
					},
				},
			},
			// doc_update removed from registration (alias of doc_create) — dispatch alias retained in handleCallTool
			Tool{
				Name:        "doc_list",
				Description: "[Versioned Docs] List the latest version of each document path in a project. Supports filtering by doc_type and path prefix.",
				tier:        tierUseful,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"project"},
					"properties": map[string]any{
						"project":     map[string]any{"type": "string", "description": "Project name"},
						"doc_type":    map[string]any{"type": "string", "description": "Filter by document type (optional)"},
						"path_prefix": map[string]any{"type": "string", "description": "Filter by path prefix (optional)"},
						"limit":       map[string]any{"type": "number", "default": 50, "minimum": 1, "maximum": 500, "description": "Maximum documents to return"},
					},
				},
			},
			Tool{
				Name:        "doc_history",
				Description: "[Versioned Docs] Get the full version history of a document. Returns all versions ordered newest first.",
				tier:        tierAdmin,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"path", "project"},
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": "Document path identifier"},
						"project": map[string]any{"type": "string", "description": "Project name"},
						"limit":   map[string]any{"type": "number", "default": 0, "minimum": 0, "description": "Maximum versions to return (0 = no limit)"},
					},
				},
			},
			Tool{
				Name:        "doc_comment",
				Description: "[Versioned Docs] Add a comment to a specific document version. Optionally anchored to a line range.",
				tier:        tierAdmin,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"document_id", "content"},
					"properties": map[string]any{
						"document_id": map[string]any{"type": "integer", "description": "Document ID (from doc_create or doc_read response)"},
						"content":     map[string]any{"type": "string", "description": "Comment text"},
						"author":      map[string]any{"type": "string", "default": "agent", "description": "Author identifier"},
						"line_start":  map[string]any{"type": "integer", "minimum": 1, "description": "Starting line number for line-anchored comment (optional)"},
						"line_end":    map[string]any{"type": "integer", "minimum": 1, "description": "Ending line number for line-anchored comment (optional)"},
					},
				},
			},
		)
	}

	// Tool tiering: consolidated primary tools by default, all aliases with cursor=all.
	var listParams struct {
		Cursor     string `json:"cursor"`
		IncludeAll bool   `json:"include_all"`
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &listParams)
	}

	primary := s.primaryTools()

	// Always include check_system_health with primary tools
	primary = append(primary, Tool{
		Name:        "check_system_health",
		Description: "Comprehensive system health check. Returns status of all subsystems (database, vectors, cache, search) with actionable diagnostics.",
		tier:        tierCore,
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	})

	// include_all=true or cursor=all: return primary + all expanded secondary tools.
	// Default: return only primary tools (8 consolidated: 7 primary + check_system_health). Legacy aliases are
	// handled by callTool dispatch — they appear in tools/list only with include_all.
	if listParams.IncludeAll || listParams.Cursor == "all" {
		// Deduplicate: secondary tools list may contain names already in primary.
		primaryNames := make(map[string]bool, len(primary))
		for _, t := range primary {
			primaryNames[t.Name] = true
		}
		for _, t := range tools {
			if !primaryNames[t.Name] {
				primary = append(primary, t)
			}
		}
	}

	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"tools": primary,
		},
	}
}

// handleToolsCall decodes a tools/call request, invokes the named tool, and
// wraps the result (or error) in the standard MCP content response shape.
func (s *Server) handleToolsCall(ctx context.Context, req *Request) *Response {
	var params ToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &Error{Code: -32602, Message: "Invalid params", Data: err.Error()},
		}
	}

	result, err := s.callTool(ctx, params.Name, params.Arguments)
	if err != nil {
		args := sanitizeToolCallArgs(params.Name, params.Arguments)
		log.Error().Err(err).Str("tool", params.Name).Str("args", args).Msg("Tool call failed")
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &Error{Code: -32000, Message: "Tool error: " + err.Error(), Data: err.Error()},
		}
	}

	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": result},
			},
		},
	}
}

func sanitizeToolCallArgs(tool string, args json.RawMessage) string {
	if tool == "remember_directive" {
		return "<redacted>"
	}
	value := string(args)
	if len(value) > 200 {
		value = value[:200] + "..."
	}
	return privacy.RedactSecrets(value)
}

// callTool dispatches to the appropriate tool handler.
func (s *Server) callTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	// Primary consolidated tool handlers
	switch name {
	case "recall":
		return s.handleRecall(ctx, args)
	case "store":
		return s.handleStoreConsolidated(ctx, args)
	case "feedback":
		return s.handleFeedbackConsolidated(ctx, args)
	case "vault":
		return s.handleVaultConsolidated(ctx, args)
	case "settings":
		return s.handleSettingsConsolidated(ctx, args)
	case "docs":
		return s.handleDocsConsolidated(ctx, args)
	case "admin":
		return s.handleAdmin(ctx, args)
	}

	// Legacy alias handlers for non-search tools
	switch name {
	case "find_similar_observations":
		return s.handleFindSimilarObservations(ctx, args)
	case "get_memory_stats":
		return s.handleGetMemoryStats(ctx)
	case "store_rule":
		return s.handleStoreRule(ctx, args)
	case "list_rules":
		return s.handleListRules(ctx, args)
	case "issues":
		return s.handleIssues(ctx, args)
	case "check_system_health":
		return s.handleCheckSystemHealth(ctx)
	case "analyze_search_patterns":
		return s.handleAnalyzeSearchPatterns(ctx, args)
	case "search_sessions":
		return s.handleSearchSessions(ctx, args)
	case "list_sessions":
		return s.handleListSessions(ctx, args)
	// Document / Collection tools
	case "list_collections":
		return s.handleListCollections(ctx)
	case "list_documents":
		return s.handleListDocuments(ctx, args)
	case "get_document":
		return s.handleGetDocument(ctx, args)
	case "ingest_document":
		return s.handleIngestDocument(ctx, args)
	case "search_collection":
		return s.handleSearchCollection(ctx, args)
	case "remove_document":
		return s.handleRemoveDocument(ctx, args)
	// Document tool aliases
	case "doc_list_collections":
		return s.handleListCollections(ctx)
	case "doc_list_documents":
		return s.handleListDocuments(ctx, args)
	case "doc_get":
		return s.handleGetDocument(ctx, args)
	case "doc_ingest":
		return s.handleIngestDocument(ctx, args)
	case "doc_search":
		return s.handleSearchCollection(ctx, args)
	case "doc_remove":
		return s.handleRemoveDocument(ctx, args)
	// Versioned document tools
	case "doc_create":
		return s.handleDocCreate(ctx, args)
	case "doc_read":
		return s.handleDocRead(ctx, args)
	case "doc_update":
		return s.handleDocUpdate(ctx, args)
	case "doc_list":
		return s.handleDocList(ctx, args)
	case "doc_history":
		return s.handleDocHistory(ctx, args)
	case "doc_comment":
		return s.handleDocComment(ctx, args)
	case "import_instincts":
		return s.handleImportInstincts(ctx, args)
	case "backfill_status":
		return s.handleBackfillStatus()
	case "store_credential":
		return s.handleStoreCredential(ctx, args)
	case "get_credential":
		return s.handleGetCredential(ctx, args)
	case "list_credentials":
		return s.handleListCredentials(ctx, args)
	case "delete_credential":
		return s.handleDeleteCredential(ctx, args)
	case "vault_status":
		return s.handleVaultStatus(ctx, args)
	case "lifecycle":
		return s.handleLifecycle(ctx, args)
	case "graph":
		return s.handleGraph(ctx, args)
	case "ingest":
		return s.handleIngest(ctx, args)
	// Vault tool aliases
	case "vault_store":
		return s.handleStoreCredential(ctx, args)
	case "vault_get":
		return s.handleGetCredential(ctx, args)
	case "vault_list":
		return s.handleListCredentials(ctx, args)
	case "vault_delete":
		return s.handleDeleteCredential(ctx, args)
	case "store_memory":
		return s.handleStoreMemory(ctx, args)
	case "recall_memory":
		return s.handleRecallMemory(ctx, args)
	case "know_about":
		return s.handleKnowAbout(ctx, args)
	case "query_principal_memory":
		return s.handleQueryPrincipalMemory(ctx, args)
	case "get_state":
		return s.handleGetState(ctx, args)
	case "set_state":
		return s.handleSetState(ctx, args)
	case "experience_history.read":
		return s.handleExperienceHistoryRead(ctx, args)
	case "experience_history.detail":
		return s.handleExperienceHistoryDetail(ctx, args)
	case "temporal_truth":
		return s.handleTemporalTruth(ctx, args)
	case "temporal_truth_refresh":
		return s.handleTemporalTruthRefresh(ctx, args)
	case "remember_directive":
		return s.handleRememberDirective(ctx, args)
	case "rate_memory":
		return s.handleRateMemory(ctx, args)
	case "rate_memory_significance":
		return s.handleRateMemorySignificance(ctx, args)
	case "suppress_memory":
		return s.handleSuppressMemory(ctx, args)
	case "set_session_outcome":
		return s.handleSetSessionOutcome(ctx, args)
	case "get_memory_brief":
		return s.handleGetMemoryBrief(ctx, args)
	case "get_ambient_hints":
		return s.handleGetAmbientHints(ctx, args)
	// Crystallization candidate tools (Milestone-F TG4 T026).
	case "list_candidates":
		return s.handleListCandidates(ctx, args)
	case "get_candidate":
		return s.handleGetCandidate(ctx, args)
	case "promote_candidate":
		return s.handlePromoteCandidate(ctx, args)
	case "reject_candidate":
		return s.handleRejectCandidate(ctx, args)
	case "supersede_candidate":
		return s.handleSupersedeCandidate(ctx, args)
	// CR-008 usefulness/noise review-loop tools.
	case "review_metrics.read":
		return s.handleReviewMetricsRead(ctx, args)
	case "review_queue.read":
		return s.handleReviewQueueRead(ctx, args)
	case "review_packet.detail":
		return s.handleReviewPacketDetail(ctx, args)
	case "review_packet.preview_action":
		return s.handleReviewPacketPreviewAction(ctx, args)
	case "review_packet.apply_action":
		return s.handleReviewPacketApplyAction(ctx, args)
	// Governance tools (Milestone-F TG6 T043).
	case "list_snapshots":
		return s.handleListSnapshots(ctx, args)
	case "rollback_snapshot":
		return s.handleRollbackSnapshot(ctx, args)
	case "pin_snapshot":
		return s.handlePinSnapshot(ctx, args)
	case "redaction_rules_status":
		return s.handleRedactionRulesStatus(ctx, args)
	case "rule_governance_health":
		return s.handleRuleGovernanceHealth(ctx, args)
	case "rule_governance_queue":
		return s.handleRuleGovernanceQueue(ctx, args)
	case "rule_governance_snapshots":
		return s.handleRuleGovernanceSnapshots(ctx, args)
	case "rule_governance_usefulness":
		return s.handleRuleGovernanceUsefulness(ctx, args)
	case "rule_governance_transition":
		return s.handleRuleGovernanceTransition(ctx, args)
	case "rule_governance_pin_snapshot":
		return s.handleRuleGovernancePinSnapshot(ctx, args)
	case "rule_governance_rollback":
		return s.handleRuleGovernanceRollback(ctx, args)
	// Bulk-op tools (Milestone-F TG6 T044).
	case "bulk_promote":
		return s.handleBulkPromote(ctx, args)
	case "bulk_delete":
		return s.handleBulkDelete(ctx, args)
	case "bulk_supersede":
		return s.handleBulkSupersede(ctx, args)
	// Code intelligence tools (CR-006).
	case "codebase_search":
		return s.handleCodebaseSearch(ctx, args)
	case "codebase_status":
		return s.handleCodebaseStatus(ctx, args)
	}

	// v5 (US9): search/timeline/decisions/changes/how_it_works/find_by_concept/
	// find_by_type tools removed — internal/search package dropped.
	// find_by_file (US3) also removed — observation store dropped in v5.
	switch name {
	case "find_by_file":
		// find_by_file was backed by the observation store, not internal/search —
		// keep its removal cause accurate (US3).
		return "", fmt.Errorf("tool %q removed in v5 (US3, observation store dropped) — use recall(query=\"file:...\") instead", name)
	case "search", "timeline", "decisions", "changes", "how_it_works",
		"find_by_concept", "find_by_type", "get_recent_context",
		"get_context_timeline", "get_timeline_by_query":
		return "", fmt.Errorf("tool %q removed in v5 (internal/search dropped) — use recall(action=\"search\") instead", name)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// sendResponse serialises resp to JSON and writes it as a single line to stdout.
func (s *Server) sendResponse(resp *Response) {
	b, err := json.Marshal(resp)
	if err != nil {
		log.Error().Err(err).Msg("Failed to marshal response")
		return
	}
	fmt.Fprintln(s.stdout, string(b))
}

// sendError constructs a JSON-RPC error response and sends it via sendResponse.
func (s *Server) sendError(id any, code int, message string, data any) {
	s.sendResponse(&Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &Error{Code: code, Message: message, Data: data},
	})
}

// handleFindSimilarObservations returns an empty result set.
// The observation-vector-search feature was retired in v5; this tool is a no-op
// shell. (content_chunks was later restored at migration 108 for vNext memory
// embeddings, but this legacy observation-similarity tool was not rewired to it.)
func (s *Server) handleFindSimilarObservations(_ context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	query := coerceString(m["query"], "")
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	minSim := coerceFloat64(m["min_similarity"], 0)
	if minSim == 0 {
		minSim = 0.7
	}

	out, err := json.Marshal(map[string]any{
		"observations":   []any{},
		"count":          0,
		"min_similarity": minSim,
		"note":           "Vector similarity search removed in v5; use recall(action=\"search\") for FTS-based retrieval",
	})
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}
	return string(out), nil
}

// handleGetMemoryStats returns real server-side telemetry for the MCP agent.
// Sections whose backing store / DB handle is nil are omitted rather than causing
// an error — telemetry must never fail just because a subsystem is off.
func (s *Server) handleGetMemoryStats(ctx context.Context) (string, error) {
	result := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	}

	db := s.statsDB
	if db == nil {
		result["note"] = "stats db not wired"
		out, err := json.Marshal(result)
		if err != nil {
			return "", fmt.Errorf("marshal response: %w", err)
		}
		return string(out), nil
	}

	// --- memory section: by-status counts from the memories table ---
	type statusRow struct {
		Status string
		Count  int64
	}
	var memRows []statusRow
	if err := db.WithContext(ctx).
		Raw(`SELECT status, count(*) AS count FROM memories WHERE deleted_at IS NULL GROUP BY status`).
		Scan(&memRows).Error; err != nil {
		log.Debug().Err(err).Msg("get_memory_stats: memories by-status query failed")
	} else {
		byStatus := make(map[string]int64, len(memRows))
		var total int64
		for _, r := range memRows {
			byStatus[r.Status] = r.Count
			total += r.Count
		}
		result["memory"] = map[string]any{
			"by_status": byStatus,
			"total":     total,
		}
	}

	// --- vnext section: injection / citation counts (gated on ENGRAM_VNEXT_ENABLED) ---
	if vnextEnabled() {
		var injectionCount int64
		injErr := db.WithContext(ctx).Raw(`SELECT count(*) FROM injection_log`).Scan(&injectionCount).Error

		type citationRow struct {
			Cited bool
			Count int64
		}
		var citRows []citationRow
		citErr := db.WithContext(ctx).Raw(`SELECT cited, count(*) AS count FROM citation_log GROUP BY cited`).Scan(&citRows).Error

		if injErr != nil {
			log.Debug().Err(injErr).Msg("get_memory_stats: injection_log count failed")
		}
		if citErr != nil {
			log.Debug().Err(citErr).Msg("get_memory_stats: citation_log count failed")
		}

		// Include the section only when both queries succeeded.
		if injErr == nil && citErr == nil {
			var citationCount, uncitedCount int64
			for _, r := range citRows {
				if r.Cited {
					citationCount = r.Count
				} else {
					uncitedCount = r.Count
				}
			}
			var noiseRatio float64
			if total := citationCount + uncitedCount; total > 0 {
				noiseRatio = float64(uncitedCount) / float64(total)
			}
			result["vnext"] = map[string]any{
				"injection_count": injectionCount,
				"citation_count":  citationCount,
				"uncited_count":   uncitedCount,
				"noise_ratio":     noiseRatio,
			}
		}
	}

	// --- embedding section: reuse embeddingStore.Stats ---
	if s.embeddingStore != nil {
		if embStats, err := s.embeddingStore.Stats(ctx); err != nil {
			log.Debug().Err(err).Msg("get_memory_stats: embedding stats unavailable")
		} else {
			result["embedding"] = embStats
		}
	}

	// --- candidates section: by-status counts from crystallization_candidates ---
	// db is non-nil here — the early return at the top of the function guarantees it.
	// crystallization_candidates (migration 132) has NO deleted_at column; lifecycle
	// is tracked via the status enum (pending/promoted/rejected/superseded/decayed),
	// so no soft-delete filter applies.
	if s.candidateStore != nil {
		type candRow struct {
			Status string
			Count  int64
		}
		var candRows []candRow
		if err := db.WithContext(ctx).
			Raw(`SELECT status, count(*) AS count FROM crystallization_candidates GROUP BY status`).
			Scan(&candRows).Error; err != nil {
			log.Debug().Err(err).Msg("get_memory_stats: crystallization_candidates count failed")
		} else {
			var pending, total int64
			for _, r := range candRows {
				total += r.Count
				if r.Status == "pending" {
					pending = r.Count
				}
			}
			result["candidates"] = map[string]any{
				"pending": pending,
				"total":   total,
			}
		}
	}

	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}
	return string(out), nil
}

// handleBackfillStatus returns backfill run status via the injected status function.
// handleBulkDeleteObservations was removed in v5 (US3); the observations table was dropped.
func (s *Server) handleBackfillStatus() (string, error) {
	if s.backfillStatusFunc == nil {
		return "", fmt.Errorf("backfill status not available")
	}
	status, err := s.backfillStatusFunc()
	if err != nil {
		return "", fmt.Errorf("failed to get backfill status: %w", err)
	}
	b, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal backfill status: %w", err)
	}
	return string(b), nil
}

// handleCheckSystemHealth performs comprehensive system health checks.
func (s *Server) handleCheckSystemHealth(ctx context.Context) (string, error) {
	type SubsystemHealth struct {
		Status   string         `json:"status"` // "healthy", "degraded", "unhealthy"
		Message  string         `json:"message,omitempty"`
		Metrics  map[string]any `json:"metrics,omitempty"`
		Warnings []string       `json:"warnings,omitempty"`
		Removed  bool           `json:"removed,omitempty"` // true when the subsystem was intentionally removed (not a fault)
	}

	type HealthReport struct {
		Timestamp     time.Time                   `json:"timestamp"`
		Subsystems    map[string]*SubsystemHealth `json:"subsystems"`
		OverallStatus string                      `json:"overall_status"`
		Actions       []string                    `json:"recommended_actions,omitempty"`
		HealthScore   int                         `json:"health_score"`
	}

	report := &HealthReport{
		OverallStatus: "healthy",
		HealthScore:   100,
		Timestamp:     time.Now(),
		Subsystems:    make(map[string]*SubsystemHealth),
		Actions:       []string{},
	}

	// Check database health with a real ping/query against the configured database.
	dbHealth := &SubsystemHealth{
		Status:  "unhealthy",
		Message: "Database subsystem not configured",
		Metrics: map[string]any{
			"health_check": "gorm store ping + SELECT 1 latency check",
		},
	}
	dsn := config.GetDatabaseDSN()
	if dsn != "" {
		store, err := gorm.NewStore(gorm.Config{DSN: dsn})
		if err != nil {
			dbHealth.Message = "Database health check failed: " + err.Error()
			report.HealthScore -= 50
			report.Actions = append(report.Actions, "Check database connectivity and PostgreSQL DSN configuration")
		} else {
			defer func() {
				_ = store.Close()
			}()
			health := store.HealthCheckForce(ctx)
			dbHealth.Status = health.Status
			if health.Error != "" {
				dbHealth.Message = "Database health check failed: " + health.Error
			} else {
				dbHealth.Message = "Database health check succeeded"
				if health.Warning != "" {
					dbHealth.Warnings = append(dbHealth.Warnings, health.Warning)
				}
			}
			dbHealth.Metrics["query_latency_ms"] = health.QueryLatency.Milliseconds()
			dbHealth.Metrics["open_connections"] = health.PoolStats.OpenConnections
			dbHealth.Metrics["in_use_connections"] = health.PoolStats.InUse
			dbHealth.Metrics["idle_connections"] = health.PoolStats.Idle
			switch health.Status {
			case "healthy":
				// no-op
			case "degraded":
				report.HealthScore -= 20
				report.Actions = append(report.Actions, "Investigate database latency or connection pool contention")
			default:
				report.HealthScore -= 50
				report.Actions = append(report.Actions, "Check database connectivity and PostgreSQL availability")
			}
		}
	} else {
		report.HealthScore -= 50
		report.Actions = append(report.Actions, "Configure PostgreSQL DSN before calling check_system_health")
	}
	report.Subsystems["database"] = dbHealth

	// Vector subsystem: reports actual availability based on ENGRAM_VNEXT_ENABLED.
	// content_chunks table and pgvector are present in v5+ (re-added in feat/embedding-hardening).
	// Removed=false: the subsystem exists; the feature flag gates active retrieval use.
	var vectorMessage string
	if os.Getenv("ENGRAM_VNEXT_ENABLED") == "true" {
		vectorMessage = "pgvector hybrid retrieval active"
	} else {
		vectorMessage = "pgvector available; vNext retrieval gated by ENGRAM_VNEXT_ENABLED"
	}
	vectorHealth := &SubsystemHealth{
		Status:  "healthy",
		Message: vectorMessage,
		Metrics: make(map[string]any),
		Removed: false,
	}
	report.Subsystems["vectors"] = vectorHealth

	// Check session store health
	sessionHealth := &SubsystemHealth{
		Status:  "healthy",
		Metrics: make(map[string]any),
	}
	if s.sessionStore != nil {
		sessionsToday, err := s.sessionStore.GetSessionsToday(ctx)
		if err != nil {
			sessionHealth.Status = "degraded"
			sessionHealth.Message = "Could not query sessions: " + err.Error()
		} else {
			sessionHealth.Metrics["sessions_today"] = sessionsToday
		}
	}
	report.Subsystems["sessions"] = sessionHealth

	// Determine overall status
	unhealthyCount := 0
	degradedCount := 0
	for _, sub := range report.Subsystems {
		switch sub.Status {
		case "unhealthy":
			unhealthyCount++
		case "degraded":
			degradedCount++
		}
	}

	if unhealthyCount > 0 {
		report.OverallStatus = "unhealthy"
	} else if degradedCount > 0 {
		report.OverallStatus = "degraded"
	}

	// Cap health score
	if report.HealthScore < 0 {
		report.HealthScore = 0
	}

	// Add recommended actions based on issues
	if report.HealthScore < 70 {
		report.Actions = append(report.Actions, "System needs attention - check subsystem details")
	}

	output, err := json.Marshal(report)
	if err != nil {
		return "", fmt.Errorf("marshal health report: %w", err)
	}
	return string(output), nil
}

// handleAnalyzeSearchPatterns returns a stub analysis response.
// Observation-era search metrics were removed in v5 along with the observations table.
func (s *Server) handleAnalyzeSearchPatterns(_ context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	days := coerceInt(m["days"], 0)
	if days <= 0 {
		days = 7
	}

	out, err := json.Marshal(map[string]any{
		"period":              fmt.Sprintf("Last %d days", days),
		"top_queries":         []map[string]any{},
		"zero_result_queries": []string{},
		"insights": []string{
			"Search metrics unavailable in v5",
			"Observation-era search analytics were removed with the observations table cleanup",
		},
		"total_searches": 0,
		"unique_queries": 0,
		"note":           "search metrics unavailable in v5",
	})
	if err != nil {
		return "", fmt.Errorf("marshal analysis: %w", err)
	}
	return string(out), nil
}

// handleSearchSessions is a tombstone for the removed v5 search_sessions tool.
func (s *Server) handleSearchSessions(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("search_sessions removed in v5 (US3) — indexed_sessions table dropped")
}

// handleListSessions is a tombstone for the removed v5 list_sessions tool.
func (s *Server) handleListSessions(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("list_sessions removed in v5 (US3) — indexed_sessions table dropped")
}
