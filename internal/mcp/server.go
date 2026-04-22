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
	"time"

	"sync"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/internal/chunking"
	"github.com/thebtf/engram/internal/collections"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
	gorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/sessions"
)

// Server is the MCP server that exposes engram tools.
// Field order optimized for memory alignment (fieldalignment).
type Server struct {
	stdin                  io.Reader
	stdout                 io.Writer
	relationStore          *gorm.RelationStore
	sessionStore           *gorm.SessionStore
	injectionStore         *gorm.InjectionStore
	collectionRegistry     *collections.Registry
	sessionIdxStore        *sessions.Store
	documentStore          *gorm.DocumentStore
	versionedDocumentStore *gorm.VersionedDocumentStore
	chunkManager           *chunking.Manager
	reasoningStore         *gorm.ReasoningTraceStore
	issueStore             *gorm.IssueStore
	memoryStore            *gorm.MemoryStore
	behavioralRulesStore   *gorm.BehavioralRulesStore
	vault                  *crypto.Vault
	vaultInitErr           error
	vaultOnce              sync.Once
	backfillStatusFunc     func() (any, error)
	version                string
}

// ServerOptions holds the dependencies injected into the MCP Server.
type ServerOptions struct {
	Version            string
	RelationStore      *gorm.RelationStore
	SessionStore       *gorm.SessionStore
	CollectionRegistry *collections.Registry
	SessionIdxStore    *sessions.Store
	DocumentStore      *gorm.DocumentStore
	ChunkManager       *chunking.Manager
}

// NewServer creates a new MCP server.
func NewServer(opts ServerOptions) *Server {
	return &Server{
		version:            opts.Version,
		stdin:              os.Stdin,
		stdout:             os.Stdout,
		relationStore:      opts.RelationStore,
		sessionStore:       opts.SessionStore,
		collectionRegistry: opts.CollectionRegistry,
		sessionIdxStore:    opts.SessionIdxStore,
		documentStore:      opts.DocumentStore,
		chunkManager:       opts.ChunkManager,
	}
}

// SetInjectionStore sets the injection store for learning MCP tools.
func (s *Server) SetInjectionStore(is *gorm.InjectionStore) {
	s.injectionStore = is
}

// SetBackfillStatusFunc sets the function to retrieve backfill run status.
func (s *Server) SetBackfillStatusFunc(fn func() (any, error)) {
	s.backfillStatusFunc = fn
}

// SetVersionedDocumentStore sets the versioned document store for document MCP tools.
func (s *Server) SetVersionedDocumentStore(vds *gorm.VersionedDocumentStore) {
	s.versionedDocumentStore = vds
}

// SetReasoningStore sets the reasoning trace store for System 2 memory tools.
func (s *Server) SetReasoningStore(rs *gorm.ReasoningTraceStore) {
	s.reasoningStore = rs
}

// SetIssueStore sets the issue store for cross-project agent issue tracking.
func (s *Server) SetIssueStore(is *gorm.IssueStore) {
	s.issueStore = is
}

// SetMemoryStore sets the memory store for the memories table (US3 Commit C).
func (s *Server) SetMemoryStore(ms *gorm.MemoryStore) {
	s.memoryStore = ms
}

// SetBehavioralRulesStore sets the behavioral rules store (US3 Commit C).
func (s *Server) SetBehavioralRulesStore(brs *gorm.BehavioralRulesStore) {
	s.behavioralRulesStore = brs
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

// Run starts the MCP server loop.
func (s *Server) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(s.stdin)

	// Channel to signal when scanner is done
	scanDone := make(chan error, 1)

	go func() {
		for scanner.Scan() {
			// Check for context cancellation before processing
			select {
			case <-ctx.Done():
				scanDone <- ctx.Err()
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

			resp := s.handleRequest(ctx, &req)
			if resp != nil {
				s.sendResponse(resp)
			}
		}
		scanDone <- scanner.Err()
	}()

	// Wait for either context cancellation or scanner completion
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-scanDone:
		if err != nil {
			return fmt.Errorf("scanner error: %w", err)
		}
		return nil
	}
}

// handleRequest dispatches the request to the appropriate handler.
// JSON-RPC 2.0 notifications (requests without "id") MUST NOT receive a response.
func (s *Server) handleRequest(ctx context.Context, req *Request) *Response {
	// JSON-RPC 2.0: notifications have no "id" field — server MUST NOT respond.
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
			Error: &Error{
				Code:    -32601,
				Message: "Method not found",
			},
		}
	}
}

// handleNotification processes JSON-RPC 2.0 notifications (no response sent).
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
2. ` + "`recall(action=\"by_file\", files=\"path/to/file\")`" + ` — before modifying any file.
3. ` + "`recall(action=\"preset\", preset=\"decisions\", query=\"...\")`" + ` — before architectural decisions.

**AFTER every task:**
4. ` + "`store(content=\"...\", title=\"...\", tags=\"...\")`" + ` — save decisions, discoveries, patterns, and lessons learned. If you learned something worth knowing next session, store it.
5. ` + "`feedback(action=\"rate\", id=N, rating=\"useful\")`" + ` — rate memories you used.
6. ` + "`feedback(action=\"outcome\", session_id=\"<claude-session-id>\", outcome=\"success\")`" + ` — record session outcome when done.

**Steps 4-6 are NOT optional.** Every completed task produces knowledge. Store it or it is lost forever.

## 8 Tools

| Tool | Purpose | Key Actions |
|------|---------|-------------|
| ` + "`recall`" + ` | **Search & retrieve** memories | search (default), preset, by_file, by_concept, by_type, similar, timeline, related, get, sessions, explain, reasoning |
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
**Before modifying code:** ` + "`recall(action=\"by_file\")`" + ` to find file-related memories.
**After completing a feature:** ` + "`store(content=\"...\", title=\"...\", type=\"decision\")`" + ` — capture what was built and why.
**After fixing a bug:** ` + "`store(content=\"...\", title=\"...\", type=\"discovery\")`" + ` — capture root cause and fix.
**After research:** ` + "`store(content=\"...\", title=\"...\", type=\"discovery\")`" + ` — capture findings. (Do NOT use memory_type values like ` + "`insight`" + ` in the observation ` + "`type`" + ` field.)
**Found a bug in another project:** ` + "`issues(action=\"create\", title=\"...\", target_project=\"...\", priority=\"high\")`" + ` — NOT store.
**Debugging:** ` + "`recall(action=\"related\", id=N)`" + ` to trace cause chains.
**Secrets:** ` + "`vault(action=\"store\")`" + ` for API keys. Never store secrets in observations.

## Advanced: All Tools

By default, engram exposes 9 consolidated tools (above). Each tool supports multiple actions.
If you need the full expanded tool list (50+ individual tools), call ` + "`tools/list`" + ` with ` + "`include_all: true`" + `.

`

// primaryTools returns the 7 consolidated primary tools shown by default.
func (s *Server) primaryTools() []Tool {
	return []Tool{
		{
			Name:        "recall",
			Description: "Search and retrieve memories. Actions: search (default, trivial SQL filter over memories), by_file, related, get, sessions, reasoning, hit_rate, wake_up, taxonomy, tunnels.",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":         map[string]any{"type": "string", "enum": []string{"search", "by_file", "related", "get", "sessions", "reasoning", "hit_rate", "wake_up", "taxonomy", "tunnels"}, "default": "search", "description": "Action to perform"},
					"query":          map[string]any{"type": "string", "description": "Search query / substring filter (for search, sessions)"},
					"files":          map[string]any{"type": "string", "description": "File paths (for action=by_file)"},
					"id":             map[string]any{"type": "number", "description": "Observation ID (for action=get, related)"},
					"project":        map[string]any{"type": "string", "description": "Project name filter"},
					"limit":          map[string]any{"type": "number", "description": "Max results"},
					"min_confidence": map[string]any{"type": "number", "description": "Min confidence 0-1 (for action=related)"},
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
					"action":        map[string]any{"type": "string", "enum": []string{"create", "edit", "merge", "import"}, "default": "create", "description": "Action to perform"},
					"content":       map[string]any{"type": "string", "description": "Observation content (for create)"},
					"title":         map[string]any{"type": "string", "description": "Title (for create, edit)"},
					"id":            map[string]any{"type": "number", "description": "Observation ID (for edit)"},
					"source_id":     map[string]any{"type": "number", "description": "Source observation ID (for merge)"},
					"target_id":     map[string]any{"type": "number", "description": "Target observation ID (for merge)"},
					"type":          map[string]any{"type": "string", "enum": []string{"decision", "bugfix", "feature", "refactor", "discovery", "change", "guidance", "credential", "entity", "wiki", "pitfall", "operational", "timeline"}, "description": "Observation type (for create). Must be an observation type, not a memory_type value like insight/context/pattern."},
					"tags":          map[string]any{"type": "string", "description": "Comma-separated tags (for create)"},
					"scope":         map[string]any{"type": "string", "description": "Scope: project/global/agent (for create)"},
					"always_inject": map[string]any{"type": "boolean", "description": "Always inject in context (for create, edit)"},
					"narrative":     map[string]any{"type": "string", "description": "Narrative text (for edit)"},
					"path":          map[string]any{"type": "string", "description": "File path (for import)"},
					"project":       map[string]any{"type": "string", "description": "Project name"},
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
					"id":         map[string]any{"type": "number", "description": "Observation ID (for rate, suppress)"},
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
		{
			Name:        "admin",
			Description: "Administrative operations: bulk ops, tagging, analytics. Actions: " + strings.Join(adminActions, ", ") + ". Action required.",
			tier:        tierUseful,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"action"},
				"properties": map[string]any{
					"action":  map[string]any{"type": "string", "description": "Action to perform (required). See tool description for valid actions."},
					"ids":     map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "description": "Observation IDs (for bulk_delete, bulk_supersede, bulk_boost)"},
					"id":      map[string]any{"type": "number", "description": "Observation ID (for tag, obs_quality, scoring)"},
					"tag":     map[string]any{"type": "string", "description": "Tag name (for by_tag, batch_tag)"},
					"project": map[string]any{"type": "string", "description": "Project name (for trends, quality, importance, etc.)"},
					"format":  map[string]any{"type": "string", "description": "Export format: json/jsonl/markdown (for export)"},
					"amount":  map[string]any{"type": "number", "description": "Boost amount (for bulk_boost)"},
					"add":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags to add (for tag)"},
					"remove":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags to remove (for tag)"},
					"pattern": map[string]any{"type": "string", "description": "Search pattern (for batch_tag)"},
					"days":    map[string]any{"type": "number", "description": "Days to analyze (for trends)"},
				},
			},
		},
		{
			Name: "issues",
			Description: "Cross-project issue tracker between agents. Do NOT use store/docs for issues.\n\n" +
				"`project` = YOUR current working project slug (identifies who is acting — audit trail).\n" +
				"`target_project` = project the issue is FOR (where it will be injected).\n" +
				"Lifecycle: open → acknowledged (auto) → resolved → closed ⟲ reopened.\n" +
				"Target agent resolves, source agent closes. Only source or dashboard operator can close.",
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
			Name:        "find_related_observations",
			Description: "Find observations related to a given observation ID filtered by confidence threshold. Returns related observations sorted by confidence score. Useful for discovering relevant context.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"id"},
				"properties": map[string]any{
					"id":             map[string]any{"type": "number", "description": "Observation ID"},
					"min_confidence": map[string]any{"type": "number", "default": 0.5, "minimum": 0.0, "maximum": 1.0, "description": "Minimum confidence threshold"},
					"limit":          map[string]any{"type": "number", "default": 20, "minimum": 1, "maximum": 100},
				},
			},
		},
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
			Name:        "bulk_delete_observations",
			Description: "Delete multiple observations by their IDs. Returns count of successfully deleted observations.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"ids"},
				"properties": map[string]any{
					"ids":            map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "description": "Array of observation IDs to delete"},
					"delete_vectors": map[string]any{"type": "boolean", "default": true, "description": "Also delete associated vectors"},
				},
			},
		},
		{
			Name:        "search_sessions",
			Description: "Full-text search across indexed Claude Code sessions.",
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
			Description: "List indexed Claude Code sessions with optional workstation/project filters.",
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
			Tool{
				Name:        "store_memory",
				Description: "Explicitly store a memory/observation. Use when you want to remember something specific across sessions.",
				tier:        tierCore,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"content"},
					"properties": map[string]any{
						"content":       map[string]any{"type": "string", "description": "The content/knowledge to remember"},
						"title":         map[string]any{"type": "string", "description": "Short title for the memory"},
						"tags":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Concept tags (supports hierarchical: lang:go:concurrency)"},
						"rejected":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Alternatives considered and dismissed (for decision observations)"},
						"type":          map[string]any{"type": "string", "description": "Memory type: decision, bugfix, feature, discovery, refactor"},
						"importance":    map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Importance score (0-1)"},
						"scope":         map[string]any{"type": "string", "enum": []string{"project", "global"}, "description": "Visibility scope"},
						"project":       map[string]any{"type": "string", "description": "Project ID (defaults to current)"},
						"ttl_days":      map[string]any{"type": "integer", "minimum": 1, "description": "TTL in days for verified facts. Auto-computed from tags if not provided. Only applies to observations with 'verified' tag."},
						"always_inject": map[string]any{"type": "boolean", "description": "If true, this memory will be injected into every agent context regardless of query relevance. Use for behavioral rules that must always be present."},
						"agent_source":  map[string]any{"type": "string", "enum": []string{"claude-code", "codex", "gemini", "other", "unknown"}, "description": "Which AI tool created this observation"},
					},
				},
			},
			Tool{
				Name:        "recall_memory",
				Description: "Recall memories/observations by semantic search. Use to retrieve previously stored knowledge.",
				tier:        tierCore,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"query"},
					"properties": map[string]any{
						"query":   map[string]any{"type": "string", "description": "Natural language query"},
						"tags":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Filter by concept tags"},
						"type":    map[string]any{"type": "string", "description": "Filter by observation type"},
						"limit":   map[string]any{"type": "number", "default": 10, "minimum": 1, "maximum": 50},
						"format":  map[string]any{"type": "string", "enum": []string{"text", "items", "detailed"}, "default": "text"},
						"project": map[string]any{"type": "string", "description": "Project ID to scope results (includes project-scoped and global observations)"},
					},
				},
			},
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
						"document_id": map[string]any{"type": "number", "description": "Document ID (from doc_create or doc_read response)"},
						"content":     map[string]any{"type": "string", "description": "Comment text"},
						"author":      map[string]any{"type": "string", "default": "agent", "description": "Author identifier"},
						"line_start":  map[string]any{"type": "number", "description": "Starting line number for line-anchored comment (optional)"},
						"line_end":    map[string]any{"type": "number", "description": "Ending line number for line-anchored comment (optional)"},
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
	// Default: return only primary tools (9 consolidated). Legacy aliases are
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

// handleToolsCall handles tool invocations.
func (s *Server) handleToolsCall(ctx context.Context, req *Request) *Response {
	var params ToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &Error{
				Code:    -32602,
				Message: "Invalid params",
				Data:    err.Error(),
			},
		}
	}

	result, err := s.callTool(ctx, params.Name, params.Arguments)
	if err != nil {
		// Truncated args for debugging (first 200 chars)
		argsStr := string(params.Arguments)
		if len(argsStr) > 200 {
			argsStr = argsStr[:200] + "..."
		}
		argsStr = privacy.RedactSecrets(argsStr)
		log.Error().Err(err).Str("tool", params.Name).Str("args", argsStr).Msg("Tool call failed")
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &Error{
				Code:    -32000,
				Message: "Tool error: " + err.Error(),
				Data:    err.Error(),
			},
		}
	}

	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"content": []map[string]any{
				{
					"type": "text",
					"text": result,
				},
			},
		},
	}
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
	case "docs":
		return s.handleDocsConsolidated(ctx, args)
	case "admin":
		return s.handleAdmin(ctx, args)
	}

	// Legacy alias handlers for non-search tools
	switch name {
	case "find_related_observations":
		return s.handleFindRelatedObservations(ctx, args)
	case "find_similar_observations":
		return s.handleFindSimilarObservations(ctx, args)
	case "get_memory_stats":
		return s.handleGetMemoryStats(ctx)
	case "bulk_delete_observations":
		return s.handleBulkDeleteObservations(ctx, args)
	case "bulk_mark_superseded":
		return s.handleBulkMarkSuperseded(ctx, args)
	case "bulk_boost_observations":
		return s.handleBulkBoostObservations(ctx, args)
	case "merge_observations":
		return s.handleMergeObservations(ctx, args)
	case "store_rule":
		return s.handleStoreRule(ctx, args)
	case "list_rules":
		return s.handleListRules(ctx, args)
	case "get_observation":
		return s.handleGetObservation(ctx, args)
	case "edit_observation":
		return s.handleEditObservation(ctx, args)
	case "get_observation_quality":
		return s.handleGetObservationQuality(ctx, args)
	case "tag_observation":
		return s.handleTagObservation(ctx, args)
	case "get_observations_by_tag":
		return s.handleGetObservationsByTag(ctx, args)
	case "get_temporal_trends":
		return s.handleGetTemporalTrends(ctx, args)
	case "get_data_quality_report":
		return s.handleGetDataQualityReport(ctx, args)
	case "batch_tag_by_pattern":
		return s.handleBatchTagByPattern(ctx, args)
	case "explain_search_ranking":
		// v5 (US9): explain_search_ranking removed — internal/search dropped.
		return "", fmt.Errorf("tool %q removed in v5 (internal/search dropped)", name)
	case "export_observations":
		return s.handleExportObservations(ctx, args)
	case "issues":
		return s.handleIssues(ctx, args)
	case "check_system_health":
		return s.handleCheckSystemHealth(ctx)
	case "analyze_search_patterns":
		return s.handleAnalyzeSearchPatterns(ctx, args)
	case "get_observation_scoring_breakdown":
		return s.handleGetObservationScoringBreakdown(ctx, args)
	case "analyze_observation_importance":
		return s.handleAnalyzeObservationImportance(ctx, args)
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
	// Vault tool aliases
	case "vault_store":
		return s.handleStoreCredential(ctx, args)
	case "vault_get":
		return s.handleGetCredential(ctx, args)
	case "vault_list":
		return s.handleListCredentials(ctx, args)
	case "vault_delete":
		return s.handleDeleteCredential(ctx, args)
	case "find_by_file_context":
		return s.handleFindByFileContext(ctx, args)
	case "store_memory":
		return s.handleStoreMemory(ctx, args)
	case "recall_memory":
		return s.handleRecallMemory(ctx, args)
	case "rate_memory":
		return s.handleRateMemory(ctx, args)
	case "suppress_memory":
		return s.handleSuppressMemory(ctx, args)
	}

	// v5 (US9): search/timeline/decisions/changes/how_it_works/find_by_concept/
	// find_by_type tools removed — internal/search package dropped.
	// find_by_file uses observationStore directly.
	switch name {
	case "find_by_file":
		return s.handleFindByFileObservations(ctx, args)
	case "search", "timeline", "decisions", "changes", "how_it_works",
		"find_by_concept", "find_by_type", "get_recent_context",
		"get_context_timeline", "get_timeline_by_query":
		return "", fmt.Errorf("tool %q removed in v5 (internal/search dropped) — use recall(action=\"search\") instead", name)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// handleFindRelatedObservations finds observations related to a given observation ID.
// v5 (US3): observation store removed; returns related IDs only (no full observation fetch).
func (s *Server) handleFindRelatedObservations(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		ID            int64
		MinConfidence float64
		Limit         int
	}
	params.ID = coerceInt64(m["id"], 0)
	params.MinConfidence = coerceFloat64(m["min_confidence"], 0)
	params.Limit = coerceInt(m["limit"], 0)

	if params.ID <= 0 {
		return "", fmt.Errorf("id is required and must be a positive integer")
	}
	if s.relationStore == nil {
		return "", fmt.Errorf("related observations unavailable: relation store not configured")
	}
	if params.MinConfidence < 0 {
		params.MinConfidence = 0.5
	}
	if params.Limit == 0 {
		params.Limit = 20
	}
	if params.Limit > 100 {
		params.Limit = 100
	}

	relatedIDs, err := s.relationStore.GetRelatedObservationIDs(ctx, params.ID, params.MinConfidence)
	if err != nil {
		return "", fmt.Errorf("failed to get related observations: %w", err)
	}
	if relatedIDs == nil {
		relatedIDs = []int64{}
	}
	if len(relatedIDs) > params.Limit {
		relatedIDs = relatedIDs[:params.Limit]
	}

	// v5 (US3): observation store removed; return IDs only.
	response := map[string]any{
		"observation_ids": relatedIDs,
		"count":           len(relatedIDs),
		"note":            "Full observation fetch unavailable in v5 — pass the returned IDs to tools that accept observation IDs",
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}
	return string(output), nil
}

// handleFindByFileObservations finds observations related to a file path.
// v5 (US3): observation store removed; tool returns not-available error.
func (s *Server) handleFindByFileObservations(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("find_by_file removed in v5 (US3) — use recall(action=\"search\") to locate relevant memories and recall(action=\"get\") to inspect a specific memory")
}

// sendResponse sends a JSON-RPC response.
func (s *Server) sendResponse(resp *Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		log.Error().Err(err).Msg("Failed to marshal response")
		return
	}
	fmt.Fprintln(s.stdout, string(data))
}

// sendError sends a JSON-RPC error response.
func (s *Server) sendError(id any, code int, message string, data any) {
	resp := &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &Error{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
	s.sendResponse(resp)
}

// handleFindSimilarObservations finds observations semantically similar to a query.
func (s *Server) handleFindSimilarObservations(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Query         string
		Project       string
		MinSimilarity float64
		Limit         int
	}
	params.Query = coerceString(m["query"], "")
	params.Project = coerceString(m["project"], "")
	params.MinSimilarity = coerceFloat64(m["min_similarity"], 0)
	params.Limit = coerceInt(m["limit"], 0)

	if params.Query == "" {
		return "", fmt.Errorf("query is required")
	}

	if params.MinSimilarity == 0 {
		params.MinSimilarity = 0.7
	}
	if params.Limit == 0 {
		params.Limit = 10
	}
	if params.Limit > 50 {
		params.Limit = 50
	}

	// Vector search removed in v5 (content_chunks table dropped). Return empty result set.
	response := map[string]any{
		"observations":   []any{},
		"count":          0,
		"min_similarity": params.MinSimilarity,
		"note":           "Vector similarity search removed in v5; use recall(action=\"search\") for FTS-based retrieval",
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleGetMemoryStats returns statistics about the memory system.
func (s *Server) handleGetMemoryStats(ctx context.Context) (string, error) {
	stats := make(map[string]any, 8) // Pre-allocate for expected stats keys

	// Vector storage and search.Manager removed in v5 (US9); no vector/search stats available.

	output, err := json.Marshal(stats)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleBulkDeleteObservations — removed in v5 (US3); observations table dropped.
func (s *Server) handleBulkDeleteObservations(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("bulk_delete_observations removed in v5 (US3) — observations table replaced by memories")
}

// handleBulkMarkSuperseded — removed in v5 (US3); observations table dropped.
func (s *Server) handleBulkMarkSuperseded(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("bulk_mark_superseded removed in v5 (US3) — observations table dropped")
}

// handleBulkBoostObservations — removed in v5 (US3); observations table dropped.
func (s *Server) handleBulkBoostObservations(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("bulk_boost_observations removed in v5 (US3) — observations table dropped")
}

// handleMergeObservations — removed in v5 (US3); observations table dropped.
func (s *Server) handleMergeObservations(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("merge_observations removed in v5 (US3) — observations table dropped")
}

// handleGetObservation — removed in v5 (US3); observations table dropped.
func (s *Server) handleGetObservation(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("get_observation removed in v5 (US3) — use recall(action=\"get\") instead")
}

// handleEditObservation — removed in v5 (US3); observations table dropped.
func (s *Server) handleEditObservation(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("edit_observation removed in v5 (US3) — observations table dropped")
}

// handleGetObservationQuality — removed in v5 (US3); observations table dropped.
func (s *Server) handleGetObservationQuality(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("get_observation_quality removed in v5 (US3) — observations table dropped")
}

// handleTagObservation — removed in v5 (US3); observations table dropped.
func (s *Server) handleTagObservation(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("tag_observation removed in v5 (US3) — observations table dropped")
}

// handleGetObservationsByTag — removed in v5 (US3); observations table dropped.
func (s *Server) handleGetObservationsByTag(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("get_observations_by_tag removed in v5 (US3) — observations table dropped")
}

// handleGetTemporalTrends — removed in v5 (US3); observations table dropped.
func (s *Server) handleGetTemporalTrends(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("get_temporal_trends removed in v5 (US3) — observations table dropped")
}

// handleGetDataQualityReport — removed in v5 (US3); observations table dropped.
func (s *Server) handleGetDataQualityReport(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("get_data_quality_report removed in v5 (US3) — observations table dropped")
}

// handleBatchTagByPattern — removed in v5 (US3); observations table dropped.
func (s *Server) handleBatchTagByPattern(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("batch_tag_by_pattern removed in v5 (US3) — observations table dropped")
}

// handleExplainSearchRanking was removed in v5 (US9): internal/search dropped.

// handleExportObservations — removed in v5 (US3); observations table dropped.
func (s *Server) handleExportObservations(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("export_observations removed in v5 (US3) — observations table dropped")
}

// handleBackfillStatus returns backfill run status via the injected status function.
func (s *Server) handleBackfillStatus() (string, error) {
	if s.backfillStatusFunc == nil {
		return "", fmt.Errorf("backfill status not available")
	}
	status, err := s.backfillStatusFunc()
	if err != nil {
		return "", fmt.Errorf("failed to get backfill status: %w", err)
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal backfill status: %w", err)
	}
	return string(data), nil
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

	// Vector storage removed in v5 (content_chunks table dropped).
	// Status is "healthy" (from the system's perspective this is intentional, not a fault).
	// Removed=true signals clients that enforce the original enum that the subsystem no longer exists.
	vectorHealth := &SubsystemHealth{
		Status:  "healthy",
		Message: "Vector storage permanently removed in v5; FTS-based retrieval is the sole search path",
		Metrics: make(map[string]any),
		Removed: true,
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

// handleAnalyzeSearchPatterns analyzes search query patterns.
func (s *Server) handleAnalyzeSearchPatterns(_ context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Days int
		TopN int
	}
	params.Days = coerceInt(m["days"], 0)
	params.TopN = coerceInt(m["top_n"], 0)
	if params.Days <= 0 {
		params.Days = 7
	}
	if params.TopN <= 0 {
		params.TopN = 10
	}

	analysis := map[string]any{
		"period":              fmt.Sprintf("Last %d days", params.Days),
		"top_queries":         []map[string]any{},
		"zero_result_queries": []string{},
		"insights": []string{
			"Search metrics unavailable in v5",
			"Observation-era search analytics were removed with the observations table cleanup",
		},
		"total_searches": 0,
		"unique_queries": 0,
		"note":           "search metrics unavailable in v5",
	}

	output, err := json.Marshal(analysis)
	if err != nil {
		return "", fmt.Errorf("marshal analysis: %w", err)
	}
	return string(output), nil
}

// handleGetObservationRelationships returns the relationship graph for an observation.
func (s *Server) handleGetObservationRelationships(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		ID       int64
		MaxDepth int
	}
	params.ID = coerceInt64(m["id"], 0)
	params.MaxDepth = coerceInt(m["max_depth"], 0)

	if params.ID <= 0 {
		return "", fmt.Errorf("id is required and must be positive")
	}
	if params.MaxDepth <= 0 {
		params.MaxDepth = 2
	}
	if params.MaxDepth > 5 {
		params.MaxDepth = 5
	}

	if s.relationStore == nil {
		return "", fmt.Errorf("relation store not available")
	}

	// Get the relationship graph
	graph, err := s.relationStore.GetRelationGraph(ctx, params.ID, params.MaxDepth)
	if err != nil {
		return "", fmt.Errorf("get relation graph: %w", err)
	}

	// Build response with additional context
	type RelationInfo struct {
		Type        string  `json:"type"`
		SourceTitle string  `json:"source_title,omitempty"`
		TargetTitle string  `json:"target_title,omitempty"`
		SourceType  string  `json:"source_type,omitempty"`
		TargetType  string  `json:"target_type,omitempty"`
		ID          int64   `json:"id"`
		SourceID    int64   `json:"source_id"`
		TargetID    int64   `json:"target_id"`
		Confidence  float64 `json:"confidence"`
	}

	type GraphResponse struct {
		Relations      []RelationInfo `json:"relations"`
		UniqueNodes    []int64        `json:"unique_nodes"`
		CenterID       int64          `json:"center_id"`
		MaxDepth       int            `json:"max_depth"`
		TotalRelations int            `json:"total_relations"`
	}

	// Collect unique node IDs
	nodeSet := make(map[int64]bool)
	nodeSet[params.ID] = true

	relations := make([]RelationInfo, 0, len(graph.Relations))
	for _, r := range graph.Relations {
		nodeSet[r.Relation.SourceID] = true
		nodeSet[r.Relation.TargetID] = true

		relations = append(relations, RelationInfo{
			ID:          r.Relation.ID,
			SourceID:    r.Relation.SourceID,
			TargetID:    r.Relation.TargetID,
			Type:        string(r.Relation.RelationType),
			Confidence:  r.Relation.Confidence,
			SourceTitle: r.SourceTitle,
			TargetTitle: r.TargetTitle,
			SourceType:  string(r.SourceType),
			TargetType:  string(r.TargetType),
		})
	}

	// Convert node set to slice
	nodes := make([]int64, 0, len(nodeSet))
	for id := range nodeSet {
		nodes = append(nodes, id)
	}

	response := GraphResponse{
		CenterID:       params.ID,
		MaxDepth:       params.MaxDepth,
		TotalRelations: len(relations),
		Relations:      relations,
		UniqueNodes:    nodes,
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}
	return string(output), nil
}

// handleGetObservationScoringBreakdown — removed in v5 (US3); observations table dropped.
func (s *Server) handleGetObservationScoringBreakdown(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("get_observation_scoring_breakdown removed in v5 (US3) — observations table dropped")
}

// handleAnalyzeObservationImportance — removed in v5 (US3); observations table dropped.
func (s *Server) handleAnalyzeObservationImportance(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("analyze_observation_importance removed in v5 (US3) — observations table dropped")
}

// handleSearchSessions handles full-text search across indexed sessions.
func (s *Server) handleSearchSessions(ctx context.Context, args json.RawMessage) (string, error) {
	if s.sessionIdxStore == nil {
		return "", fmt.Errorf("session indexing not configured")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Query string
		Limit int
	}
	params.Query = coerceString(m["query"], "")
	params.Limit = coerceInt(m["limit"], 0)
	if params.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	if params.Limit <= 0 {
		params.Limit = 10
	}

	results, err := s.sessionIdxStore.SearchSessions(ctx, params.Query, params.Limit)
	if err != nil {
		return "", fmt.Errorf("search sessions: %w", err)
	}

	type sessionResult struct {
		ID            string  `json:"id"`
		WorkstationID string  `json:"workstation_id"`
		ProjectPath   string  `json:"project_path,omitempty"`
		ExchangeCount int     `json:"exchange_count"`
		Rank          float64 `json:"rank"`
		Snippet       string  `json:"snippet,omitempty"`
	}

	out := make([]sessionResult, 0, len(results))
	for _, r := range results {
		sr := sessionResult{
			ID:            r.Session.ID,
			WorkstationID: r.Session.WorkstationID,
			ExchangeCount: r.Session.ExchangeCount,
			Rank:          r.Rank,
		}
		if r.Session.ProjectPath.Valid {
			sr.ProjectPath = r.Session.ProjectPath.String
		}
		if r.Session.Content.Valid && len(r.Session.Content.String) > 0 {
			snippet := r.Session.Content.String
			if len(snippet) > 200 {
				snippet = snippet[:200]
			}
			sr.Snippet = snippet
		}
		out = append(out, sr)
	}

	data, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal results: %w", err)
	}
	return string(data), nil
}

// handleListSessions lists indexed Claude Code sessions.
func (s *Server) handleListSessions(ctx context.Context, args json.RawMessage) (string, error) {
	if s.sessionIdxStore == nil {
		return "", fmt.Errorf("session indexing not configured")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		WorkstationID string
		ProjectID     string
		Limit         int
		Offset        int
	}
	params.WorkstationID = coerceString(m["workstation_id"], "")
	params.ProjectID = coerceString(m["project_id"], "")
	params.Limit = coerceInt(m["limit"], 0)
	params.Offset = coerceInt(m["offset"], 0)
	if params.Limit <= 0 {
		params.Limit = 20
	}

	opts := sessions.ListOptions{
		WorkstationID: params.WorkstationID,
		ProjectID:     params.ProjectID,
		Limit:         params.Limit,
		Offset:        params.Offset,
	}

	list, err := s.sessionIdxStore.ListSessions(ctx, opts)
	if err != nil {
		return "", fmt.Errorf("list sessions: %w", err)
	}

	type sessionItem struct {
		ID            string `json:"id"`
		WorkstationID string `json:"workstation_id"`
		ProjectID     string `json:"project_id"`
		ProjectPath   string `json:"project_path,omitempty"`
		ExchangeCount int    `json:"exchange_count"`
		GitBranch     string `json:"git_branch,omitempty"`
		LastMsgAt     string `json:"last_msg_at,omitempty"`
	}

	out := make([]sessionItem, 0, len(list))
	for _, sess := range list {
		item := sessionItem{
			ID:            sess.ID,
			WorkstationID: sess.WorkstationID,
			ProjectID:     sess.ProjectID,
			ExchangeCount: sess.ExchangeCount,
		}
		if sess.ProjectPath.Valid {
			item.ProjectPath = sess.ProjectPath.String
		}
		if sess.GitBranch.Valid {
			item.GitBranch = sess.GitBranch.String
		}
		if sess.LastMsgAt.Valid {
			item.LastMsgAt = sess.LastMsgAt.Time.UTC().Format(time.RFC3339)
		}
		out = append(out, item)
	}

	data, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal results: %w", err)
	}
	return string(data), nil
}

// handleFindByFileContext — removed in v5 (US3); observations table dropped.
func (s *Server) handleFindByFileContext(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("find_by_file_context removed in v5 (US3) — observations table dropped")
}
