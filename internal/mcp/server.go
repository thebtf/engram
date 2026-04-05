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

	"github.com/thebtf/engram/internal/chunking"
	"github.com/thebtf/engram/internal/collections"
	"github.com/thebtf/engram/internal/consolidation"
	"github.com/thebtf/engram/internal/crypto"
	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	graphpkg "github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/internal/maintenance"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/scoring"
	"github.com/thebtf/engram/internal/search"
	"github.com/thebtf/engram/internal/sessions"
	"github.com/thebtf/engram/internal/vector"
	"github.com/thebtf/engram/pkg/models"
	"github.com/rs/zerolog/log"
)

// Server is the MCP server that exposes search tools.
// Field order optimized for memory alignment (fieldalignment).
type Server struct {
	stdin                  io.Reader
	stdout                 io.Writer
	searchMgr              *search.Manager
	observationStore       *gorm.ObservationStore
	patternStore           *gorm.PatternStore
	relationStore          *gorm.RelationStore
	sessionStore           *gorm.SessionStore
	vectorClient           vector.Client
	scoreCalculator        *scoring.Calculator
	recalculator           *scoring.Recalculator
	maintenanceService     *maintenance.Service
	collectionRegistry     *collections.Registry
	sessionIdxStore        *sessions.Store
	consolidationScheduler *consolidation.Scheduler
	documentStore          *gorm.DocumentStore
	versionedDocumentStore *gorm.VersionedDocumentStore
	embedSvc               *embedding.Service
	chunkManager           *chunking.Manager
	graphStore             graphpkg.GraphStore
	reasoningStore         *gorm.ReasoningTraceStore
	vault                  *crypto.Vault
	vaultInitErr           error
	vaultOnce              sync.Once
	backfillStatusFunc     func() (any, error)
	version                string
}

// NewServer creates a new MCP server.
func NewServer(
	searchMgr *search.Manager,
	version string,
	observationStore *gorm.ObservationStore,
	patternStore *gorm.PatternStore,
	relationStore *gorm.RelationStore,
	sessionStore *gorm.SessionStore,
	vectorClient vector.Client,
	scoreCalculator *scoring.Calculator,
	recalculator *scoring.Recalculator,
	maintenanceService *maintenance.Service,
	collectionRegistry *collections.Registry,
	sessionIdxStore *sessions.Store,
	consolidationScheduler *consolidation.Scheduler,
	documentStore *gorm.DocumentStore,
	embedSvc *embedding.Service,
	chunkManager *chunking.Manager,
) *Server {
	return &Server{
		searchMgr:              searchMgr,
		version:                version,
		stdin:                  os.Stdin,
		stdout:                 os.Stdout,
		observationStore:       observationStore,
		patternStore:           patternStore,
		relationStore:          relationStore,
		sessionStore:           sessionStore,
		vectorClient:           vectorClient,
		scoreCalculator:        scoreCalculator,
		recalculator:           recalculator,
		maintenanceService:     maintenanceService,
		collectionRegistry:     collectionRegistry,
		sessionIdxStore:        sessionIdxStore,
		consolidationScheduler: consolidationScheduler,
		documentStore:          documentStore,
		embedSvc:               embedSvc,
		chunkManager:           chunkManager,
	}
}

// SetGraphStore sets the graph store for graph-related MCP tools.
func (s *Server) SetGraphStore(gs graphpkg.GraphStore) {
	s.graphStore = gs
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
	tierCore    = 1 // T1: Always visible — most-used tools
	tierUseful  = 2 // T2: Visible by default — regularly useful tools
	tierAdmin   = 3 // T3+: Hidden by default — admin, analytics, bulk ops
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
5. ` + "`feedback(action=\"rate\", id=N, useful=true)`" + ` — rate memories you used.
6. ` + "`feedback(action=\"outcome\", outcome=\"success\")`" + ` — record session outcome when done.

**Steps 4-6 are NOT optional.** Every completed task produces knowledge. Store it or it is lost forever.

## 7 Tools

| Tool | Purpose | Key Actions |
|------|---------|-------------|
| ` + "`recall`" + ` | **Search & retrieve** memories | search (default), preset, by_file, by_concept, by_type, similar, timeline, related, patterns, get, sessions, explain, reasoning |
| ` + "`store`" + ` | **Save** memories, edit, merge, extract | create (default), edit, merge, import, extract |
| ` + "`feedback`" + ` | **Rate** quality, suppress, record outcomes | rate, suppress, outcome |
| ` + "`vault`" + ` | **Credentials** — encrypted AES-256-GCM | store, get, list, delete, status |
| ` + "`docs`" + ` | **Documents** — versioned docs & collections | create, read, list, history, comment, collections, documents, get_doc, remove, ingest, search_docs |
| ` + "`admin`" + ` | **Bulk ops**, maintenance, analytics | bulk_delete, bulk_supersede, tag, graph, stats, trends, quality, export, maintenance, ... |
| ` + "`check_system_health`" + ` | **Health** check of all subsystems | (no params) |

## What to Store

After completing work, store observations about:
- **Decisions made** and WHY (architecture, library choices, trade-offs)
- **Bugs found** and their root cause (prevents recurrence)
- **Patterns discovered** (recurring code structures, project conventions)
- **Lessons learned** (what worked, what failed, what to avoid)
- **File knowledge** (what a file does, gotchas, non-obvious behavior)

Use ` + "`store(action=\"extract\", content=\"...\")`" + ` to let the LLM extract structured observations from raw content automatically.

## Workflow Patterns

**Starting work:** Context is auto-injected by hooks. Use ` + "`recall(query=\"...\")`" + ` for deeper search.
**Before modifying code:** ` + "`recall(action=\"by_file\")`" + ` + ` + "`recall(action=\"preset\", preset=\"how_it_works\")`" + `
**After completing a feature:** ` + "`store(content=\"...\", title=\"...\", type=\"decision\")`" + ` — capture what was built and why.
**After fixing a bug:** ` + "`store(content=\"...\", title=\"...\", type=\"discovery\")`" + ` — capture root cause and fix.
**After research:** ` + "`store(content=\"...\", title=\"...\", type=\"insight\")`" + ` — capture findings.
**Debugging:** ` + "`recall(action=\"related\", id=N)`" + ` to trace cause chains.
**Secrets:** ` + "`vault(action=\"store\")`" + ` for API keys. Never store secrets in observations.

`

// primaryTools returns the 7 consolidated primary tools shown by default.
func (s *Server) primaryTools() []Tool {
	return []Tool{
		{
			Name:        "recall",
			Description: "Search and retrieve memories. Actions: search (default), preset (decisions/changes/how_it_works), by_file, by_concept, by_type, similar, timeline, related, patterns, get, sessions, explain, reasoning.",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":         map[string]any{"type": "string", "enum": []string{"search", "preset", "by_file", "by_concept", "by_type", "similar", "timeline", "related", "patterns", "get", "sessions", "explain", "reasoning"}, "default": "search", "description": "Action to perform"},
					"query":          map[string]any{"type": "string", "description": "Search query (for search, preset, similar, timeline:query, sessions, explain)"},
					"preset":         map[string]any{"type": "string", "enum": []string{"decisions", "changes", "how_it_works"}, "description": "Search preset (for action=preset)"},
					"files":          map[string]any{"type": "string", "description": "File paths (for action=by_file)"},
					"concept":        map[string]any{"type": "string", "description": "Concept tag (for action=by_concept)"},
					"type":           map[string]any{"type": "string", "description": "Observation type (for action=by_type)"},
					"id":             map[string]any{"type": "number", "description": "Observation ID (for action=get, related)"},
					"project":        map[string]any{"type": "string", "description": "Project name filter"},
					"limit":          map[string]any{"type": "number", "description": "Max results"},
					"mode":           map[string]any{"type": "string", "description": "Timeline mode: recent/anchor/query (for action=timeline)"},
					"min_similarity": map[string]any{"type": "number", "description": "Min similarity 0-1 (for action=similar)"},
					"min_confidence": map[string]any{"type": "number", "description": "Min confidence 0-1 (for action=related)"},
					"format":         map[string]any{"type": "string", "description": "Output format: text/items/detailed"},
				},
			},
		},
		{
			Name:        "store",
			Description: "Store, edit, merge, or extract memories. Actions: create (default), edit, merge, import, extract.",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":        map[string]any{"type": "string", "enum": []string{"create", "edit", "merge", "import", "extract"}, "default": "create", "description": "Action to perform"},
					"content":       map[string]any{"type": "string", "description": "Observation content (for create)"},
					"title":         map[string]any{"type": "string", "description": "Title (for create, edit)"},
					"id":            map[string]any{"type": "number", "description": "Observation ID (for edit)"},
					"source_id":     map[string]any{"type": "number", "description": "Source observation ID (for merge)"},
					"target_id":     map[string]any{"type": "number", "description": "Target observation ID (for merge)"},
					"type":          map[string]any{"type": "string", "description": "Observation type (for create)"},
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
					"action":  map[string]any{"type": "string", "enum": []string{"rate", "suppress", "outcome"}, "description": "Action to perform (required)"},
					"id":      map[string]any{"type": "number", "description": "Observation ID (for rate, suppress)"},
					"useful":  map[string]any{"type": "boolean", "description": "Was it helpful? (for rate)"},
					"outcome": map[string]any{"type": "string", "enum": []string{"success", "partial", "failure", "abandoned"}, "description": "Session outcome (for outcome action)"},
					"reason":  map[string]any{"type": "string", "description": "Outcome reason (for outcome action)"},
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
			Description: "Administrative operations: bulk ops, tagging, graph, analytics, maintenance. Actions: bulk_delete, bulk_supersede, bulk_boost, tag, by_tag, batch_tag, graph, graph_stats, stats, trends, quality, importance, search_analytics, obs_quality, scoring, consolidations, maintenance, maintenance_stats, consolidation, export, backfill_status. Action required.",
			tier:        tierUseful,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"action"},
				"properties": map[string]any{
					"action":  map[string]any{"type": "string", "description": "Action to perform (required). See tool description for valid actions."},
					"ids":     map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "description": "Observation IDs (for bulk_delete, bulk_supersede, bulk_boost)"},
					"id":      map[string]any{"type": "number", "description": "Observation ID (for tag, obs_quality, scoring, graph)"},
					"tag":     map[string]any{"type": "string", "description": "Tag name (for by_tag, batch_tag)"},
					"project": map[string]any{"type": "string", "description": "Project name (for trends, quality, importance, etc.)"},
					"format":  map[string]any{"type": "string", "description": "Export format: json/jsonl/markdown (for export)"},
					"mode":    map[string]any{"type": "string", "description": "Graph mode (for graph action)"},
					"amount":  map[string]any{"type": "number", "description": "Boost amount (for bulk_boost)"},
					"add":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags to add (for tag)"},
					"remove":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags to remove (for tag)"},
					"pattern": map[string]any{"type": "string", "description": "Search pattern (for batch_tag)"},
					"days":    map[string]any{"type": "number", "description": "Days to analyze (for trends)"},
				},
			},
		},
	}
}

// handleToolsList returns the list of available tools.
func (s *Server) handleToolsList(req *Request) *Response {
	tools := []Tool{
		{
			Name:        "search",
			Description: "Unified search across all memory types (observations, sessions, and user prompts) using vector-first semantic search (pgvector).",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":     map[string]any{"type": "string", "description": "Natural language search query for semantic ranking"},
					"preset":    map[string]any{"type": "string", "enum": []string{"decisions", "changes", "how_it_works"}, "description": "Shortcut presets that set type/concept filters automatically"},
					"type":      map[string]any{"type": "string", "enum": []string{"observations", "sessions", "prompts"}, "description": "Filter by document type"},
					"project":   map[string]any{"type": "string", "description": "Filter by project name"},
					"obs_type":  map[string]any{"type": "string", "description": "Filter observations by type"},
					"concepts":  map[string]any{"type": "string", "description": "Filter by concept tags"},
					"files":     map[string]any{"type": "string", "description": "Filter by file paths"},
					"dateStart": map[string]any{"type": []string{"string", "number"}, "description": "Start date for filtering"},
					"dateEnd":   map[string]any{"type": []string{"string", "number"}, "description": "End date for filtering"},
					"orderBy":   map[string]any{"type": "string", "enum": []string{"relevance", "date_desc", "date_asc"}, "default": "date_desc"},
					"limit":     map[string]any{"type": "number", "default": 20, "minimum": 1, "maximum": 100},
					"offset":    map[string]any{"type": "number", "default": 0, "minimum": 0},
					"format":    map[string]any{"type": "string", "enum": []string{"index", "full"}, "default": "index"},
				},
				"required": []string{},
			},
		},
		{
			Name:        "timeline",
			Description: "Fetch timeline of observations around a specific point in time. Consolidates get_context_timeline, get_timeline_by_query, and get_recent_context.",
			tier:        tierUseful,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"mode":      map[string]any{"type": "string", "enum": []string{"anchor", "query", "recent"}, "default": "anchor", "description": "Timeline mode: anchor (by ID), query (search+timeline), recent (latest observations)"},
					"anchor_id": map[string]any{"type": "number", "description": "Observation ID to use as anchor (mode=anchor)"},
					"query":     map[string]any{"type": "string", "description": "Natural language query to find anchor observation (mode=query)"},
					"before":    map[string]any{"type": "number", "default": 10, "minimum": 0, "maximum": 100},
					"after":     map[string]any{"type": "number", "default": 10, "minimum": 0, "maximum": 100},
					"project":   map[string]any{"type": "string"},
					"concepts":  map[string]any{"type": "string"},
					"files":     map[string]any{"type": "string"},
					"obs_type":  map[string]any{"type": "string"},
					"format":    map[string]any{"type": "string", "enum": []string{"index", "full"}, "default": "index"},
				},
			},
		},
		{
			Name:        "decisions",
			Description: "Semantic shortcut for finding architectural, design, and implementation decisions.",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]any{
					"query":     map[string]any{"type": "string", "description": "Natural language query for finding decisions"},
					"dateStart": map[string]any{"type": []string{"string", "number"}},
					"dateEnd":   map[string]any{"type": []string{"string", "number"}},
					"limit":     map[string]any{"type": "number", "default": 20, "minimum": 1, "maximum": 100},
					"format":    map[string]any{"type": "string", "enum": []string{"index", "full"}, "default": "index"},
				},
			},
		},
		{
			Name:        "changes",
			Description: "Semantic shortcut for finding code changes, refactorings, and modifications.",
			tier:        tierUseful,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]any{
					"query":     map[string]any{"type": "string", "description": "Natural language query for finding changes"},
					"dateStart": map[string]any{"type": []string{"string", "number"}},
					"dateEnd":   map[string]any{"type": []string{"string", "number"}},
					"limit":     map[string]any{"type": "number", "default": 20, "minimum": 1, "maximum": 100},
					"format":    map[string]any{"type": "string", "enum": []string{"index", "full"}, "default": "index"},
				},
			},
		},
		{
			Name:        "how_it_works",
			Description: "Semantic shortcut for understanding system architecture, design patterns, and implementation details.",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]any{
					"query":     map[string]any{"type": "string", "description": "Natural language query for understanding how something works"},
					"dateStart": map[string]any{"type": []string{"string", "number"}},
					"dateEnd":   map[string]any{"type": []string{"string", "number"}},
					"limit":     map[string]any{"type": "number", "default": 20, "minimum": 1, "maximum": 100},
					"format":    map[string]any{"type": "string", "enum": []string{"index", "full"}, "default": "index"},
				},
			},
		},
		{
			Name:        "find_by_concept",
			Description: "Find observations tagged with specific concepts.",
			tier:        tierUseful,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"concepts"},
				"properties": map[string]any{
					"concepts":  map[string]any{"type": "string", "description": "Concept tag(s) to filter by"},
					"type":      map[string]any{"type": "string"},
					"files":     map[string]any{"type": "string"},
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
		// find_by_file_context removed from registration (near-duplicate of find_by_file) — dispatch alias retained
		{
			Name:        "find_by_type",
			Description: "Find observations of specific types.",
			tier:        tierUseful,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"type"},
				"properties": map[string]any{
					"type":      map[string]any{"type": "string", "description": "Observation type(s) to filter by"},
					"concepts":  map[string]any{"type": "string"},
					"files":     map[string]any{"type": "string"},
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
		// get_recent_context removed from registration (consolidated into timeline) — dispatch alias retained
		// get_context_timeline removed from registration (consolidated into timeline) — dispatch alias retained
		// get_timeline_by_query removed from registration (consolidated into timeline) — dispatch alias retained
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
			Name:        "graph_query",
			Description: "Unified graph query tool. Consolidates find_related_observations, get_observation_relationships, and get_graph_neighbors.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"id"},
				"properties": map[string]any{
					"mode":      map[string]any{"type": "string", "enum": []string{"related", "relationships", "neighbors"}, "default": "related", "description": "Graph query mode"},
					"id":        map[string]any{"type": "number", "description": "Observation ID to query around"},
					"max_depth": map[string]any{"type": "number", "default": 2, "minimum": 1, "maximum": 5, "description": "Maximum traversal depth (mode=relationships)"},
					"limit":     map[string]any{"type": "number", "default": 20, "minimum": 1, "maximum": 100},
				},
			},
		},
		{
			Name:        "get_patterns",
			Description: "Get detected patterns from observations. Patterns represent recurring themes, workflows, or practices discovered across observations.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type":    map[string]any{"type": "string", "enum": []string{"workflow", "preference", "best_practice", "anti_pattern", "tooling"}, "description": "Filter by pattern type"},
					"project": map[string]any{"type": "string", "description": "Filter by project"},
					"query":   map[string]any{"type": "string", "description": "Search patterns by name/description"},
					"limit":   map[string]any{"type": "number", "default": 20, "minimum": 1, "maximum": 100},
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
			Name:        "bulk_mark_superseded",
			Description: "Mark multiple observations as superseded (stale). Useful for cleanup without permanent deletion.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"ids"},
				"properties": map[string]any{
					"ids": map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "description": "Array of observation IDs to mark as superseded"},
				},
			},
		},
		{
			Name:        "bulk_boost_observations",
			Description: "Boost or reduce the importance score of multiple observations. Positive values increase importance, negative decrease.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"ids", "boost"},
				"properties": map[string]any{
					"ids":   map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "description": "Array of observation IDs to boost"},
					"boost": map[string]any{"type": "number", "minimum": -1.0, "maximum": 1.0, "description": "Boost amount (-1.0 to 1.0)"},
				},
			},
		},
		{
			Name:        "trigger_maintenance",
			Description: "Trigger an immediate maintenance run (cleanup old observations, optimize database).",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "get_maintenance_stats",
			Description: "Get statistics about the maintenance system including last run time, cleanup counts, and configuration.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "merge_observations",
			Description: "Merge two observations into one. The target observation is kept and boosted, the source is marked as superseded. Useful for deduplication without data loss.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"source_id", "target_id"},
				"properties": map[string]any{
					"source_id": map[string]any{"type": "number", "description": "ID of the observation to merge FROM (will be superseded)"},
					"target_id": map[string]any{"type": "number", "description": "ID of the observation to merge INTO (will be kept and boosted)"},
					"boost":     map[string]any{"type": "number", "default": 0.1, "minimum": 0, "maximum": 0.5, "description": "Score boost for the target observation (default 0.1)"},
				},
			},
		},
		{
			Name:        "get_observation",
			Description: "Get a single observation by its ID. Returns full observation details including all metadata.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"id"},
				"properties": map[string]any{
					"id": map[string]any{"type": "number", "description": "Observation ID to retrieve"},
				},
			},
		},
		{
			Name:        "edit_observation",
			Description: "Edit an existing observation. Only provided fields will be updated, others remain unchanged. Useful for correcting errors, adding details, or updating scope.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"id"},
				"properties": map[string]any{
					"id":             map[string]any{"type": "number", "description": "Observation ID to edit"},
					"title":          map[string]any{"type": "string", "description": "New title (optional)"},
					"subtitle":       map[string]any{"type": "string", "description": "New subtitle (optional)"},
					"narrative":      map[string]any{"type": "string", "description": "New narrative text (optional)"},
					"facts":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "New facts array (optional)"},
					"concepts":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "New concept tags (optional)"},
					"files_read":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "New files read list (optional)"},
					"files_modified": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "New files modified list (optional)"},
					"scope":          map[string]any{"type": "string", "enum": []string{"project", "global"}, "description": "New scope (optional)"},
					"status":         map[string]any{"type": "string", "description": "Observation status: active or resolved", "enum": []string{"active", "resolved"}},
					"status_reason":  map[string]any{"type": "string", "description": "Reason for status change"},
					"always_inject":  map[string]any{"type": "boolean", "description": "If true, add always-inject concept (injected into every context). If false, remove it."},
				},
			},
		},
		{
			Name:        "get_observation_quality",
			Description: "Get quality metrics for an observation. Returns completeness score, usage stats, and improvement suggestions.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"id"},
				"properties": map[string]any{
					"id": map[string]any{"type": "number", "description": "Observation ID to analyze"},
				},
			},
		},
		{
			Name:        "suggest_consolidations",
			Description: "Find observations that could be merged or consolidated. Returns groups of similar observations with merge recommendations.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project":        map[string]any{"type": "string", "description": "Filter by project"},
					"min_similarity": map[string]any{"type": "number", "default": 0.8, "minimum": 0.5, "maximum": 1.0, "description": "Minimum similarity threshold for grouping"},
					"limit":          map[string]any{"type": "number", "default": 10, "minimum": 1, "maximum": 50, "description": "Maximum groups to return"},
				},
			},
		},
		{
			Name:        "tag_observation",
			Description: "Add or remove concept tags from an observation. Tags help with organization and filtering. Use mode 'add' to add new tags, 'remove' to remove specific tags, or 'set' to replace all tags.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"id", "tags"},
				"properties": map[string]any{
					"id":   map[string]any{"type": "number", "description": "Observation ID to tag"},
					"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags to add, remove, or set"},
					"mode": map[string]any{"type": "string", "enum": []string{"add", "remove", "set"}, "default": "add", "description": "Operation mode: 'add' appends tags, 'remove' removes specific tags, 'set' replaces all tags"},
				},
			},
		},
		{
			Name:        "get_observations_by_tag",
			Description: "Find all observations that have a specific concept tag. Useful for browsing by category.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"tag"},
				"properties": map[string]any{
					"tag":     map[string]any{"type": "string", "description": "Tag/concept to search for"},
					"project": map[string]any{"type": "string", "description": "Filter by project (optional)"},
					"limit":   map[string]any{"type": "number", "default": 50, "minimum": 1, "maximum": 200, "description": "Maximum observations to return"},
				},
			},
		},
		{
			Name:        "get_temporal_trends",
			Description: "Analyze observation creation patterns over time. Returns daily counts, peak activity times, and trend insights. Useful for understanding work patterns.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project":  map[string]any{"type": "string", "description": "Filter by project (optional)"},
					"days":     map[string]any{"type": "number", "default": 30, "minimum": 1, "maximum": 365, "description": "Number of days to analyze"},
					"group_by": map[string]any{"type": "string", "enum": []string{"day", "week", "hour_of_day"}, "default": "day", "description": "How to group the data"},
				},
			},
		},
		{
			Name:        "get_data_quality_report",
			Description: "Get a comprehensive quality assessment of observations. Shows completeness distribution, common issues, and improvement suggestions.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project": map[string]any{"type": "string", "description": "Filter by project (optional)"},
					"limit":   map[string]any{"type": "number", "default": 100, "minimum": 10, "maximum": 500, "description": "Number of observations to analyze"},
				},
			},
		},
		{
			Name:        "batch_tag_by_pattern",
			Description: "Apply tags to observations matching a pattern. Useful for retroactive organization and categorization.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"pattern", "tags"},
				"properties": map[string]any{
					"pattern":     map[string]any{"type": "string", "description": "Search pattern to match (searches title, narrative, facts)"},
					"tags":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags to add to matching observations"},
					"project":     map[string]any{"type": "string", "description": "Filter by project (optional)"},
					"dry_run":     map[string]any{"type": "boolean", "default": true, "description": "If true, only preview matches without applying tags"},
					"max_matches": map[string]any{"type": "number", "default": 100, "minimum": 1, "maximum": 500, "description": "Maximum observations to tag"},
				},
			},
		},
		{
			Name:        "explain_search_ranking",
			Description: "Debug search results by showing score breakdown for top matches. Explains why each observation ranked where it did.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]any{
					"query":   map[string]any{"type": "string", "description": "Search query to analyze"},
					"project": map[string]any{"type": "string", "description": "Project context for search"},
					"top_n":   map[string]any{"type": "number", "default": 5, "minimum": 1, "maximum": 20, "description": "Number of top results to explain"},
				},
			},
		},
		{
			Name:        "export_observations",
			Description: "Export observations in various formats for backup or analysis.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"format":     map[string]any{"type": "string", "enum": []string{"json", "jsonl", "markdown"}, "default": "json", "description": "Export format"},
					"project":    map[string]any{"type": "string", "description": "Filter by project (optional)"},
					"limit":      map[string]any{"type": "number", "default": 100, "minimum": 1, "maximum": 1000, "description": "Maximum observations to export"},
					"date_start": map[string]any{"type": "number", "description": "Filter by creation date (epoch milliseconds)"},
					"date_end":   map[string]any{"type": "number", "description": "Filter by creation date (epoch milliseconds)"},
					"obs_type":   map[string]any{"type": "string", "description": "Filter by observation type"},
				},
			},
		},
		{
			Name:        "check_system_health",
			Description: "Comprehensive system health check. Returns status of all subsystems (database, vectors, cache, search) with actionable diagnostics.",
			tier:        tierCore,
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "analyze_search_patterns",
			Description: "Analyze search query patterns to identify common searches, missed queries, and optimization opportunities.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"days":  map[string]any{"type": "number", "default": 7, "minimum": 1, "maximum": 30, "description": "Number of days to analyze"},
					"top_n": map[string]any{"type": "number", "default": 10, "minimum": 1, "maximum": 50, "description": "Number of top patterns to return"},
				},
			},
		},
		// get_observation_relationships removed from registration (subset of graph_query) — dispatch alias retained
		// get_graph_neighbors removed from registration (subset of graph_query) — dispatch alias retained
		{
			Name:        "get_graph_stats",
			Description: "Get graph backend statistics. Returns provider, connection status, node count, and edge count.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "get_observation_scoring_breakdown",
			Description: "Get detailed scoring breakdown for an observation. Shows how importance scores are calculated including type weight, recency decay, feedback contribution, concept boost, and retrieval frequency. Useful for understanding why observations are ranked the way they are.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"id"},
				"properties": map[string]any{
					"id": map[string]any{"type": "number", "description": "Observation ID to get scoring breakdown for"},
				},
			},
		},
		{
			Name:        "analyze_observation_importance",
			Description: "Analyze observation importance patterns in a project. Returns statistics on feedback distribution, top-scoring observations, most-retrieved observations, and concept weights. Useful for understanding what makes observations valuable.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project":                 map[string]any{"type": "string", "description": "Project to analyze (optional, analyzes all if omitted)"},
					"include_top_scored":      map[string]any{"type": "boolean", "default": true, "description": "Include top-scoring observations"},
					"include_most_retrieved":  map[string]any{"type": "boolean", "default": true, "description": "Include most-retrieved observations"},
					"include_concept_weights": map[string]any{"type": "boolean", "default": true, "description": "Include concept weight analysis"},
					"limit":                   map[string]any{"type": "number", "default": 10, "minimum": 1, "maximum": 50, "description": "Number of top observations to include"},
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
		{
			Name:        "run_consolidation",
			Description: "Manually trigger the memory consolidation lifecycle. Runs decay (relevance recalculation), creative association discovery, and optionally forgetting. Use when you want to consolidate memories immediately rather than waiting for scheduled intervals.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cycle": map[string]any{
						"type":        "string",
						"enum":        []string{"all", "decay", "associations", "forgetting"},
						"default":     "all",
						"description": "Which consolidation cycle to run: 'all' runs everything, 'decay' recalculates relevance scores, 'associations' discovers creative associations, 'forgetting' archives low-relevance observations",
					},
				},
			},
		},
		// Import instincts tool (always available — observation store is required for MCP)
		{
			Name:        "import_instincts",
			Description: "Import ECC instinct files as guidance observations. Supports sending file content directly (preferred for remote servers) or reading from a local path (legacy). Idempotent — duplicates are skipped via vector similarity check.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type": "object",
				"anyOf": []map[string]any{
					{"required": []string{"files"}},
					{"required": []string{"path"}},
				},
				"properties": map[string]any{
					"files": map[string]any{
						"type":        "array",
						"description": "Array of instinct files with content (preferred for client-server mode)",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"name":    map[string]any{"type": "string", "description": "Filename (e.g. 'my-instinct.md')"},
								"content": map[string]any{"type": "string", "description": "Full file content including YAML frontmatter"},
							},
							"required": []string{"name", "content"},
						},
					},
					"path": map[string]any{"type": "string", "description": "[DEPRECATED] Local filesystem path to instincts directory. Use 'files' parameter instead for remote servers."},
				},
			},
		},
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

	// Memory management tools — only advertise when observation store is available
	if s.observationStore != nil {
		tools = append(tools,
			Tool{
				Name:        "store_memory",
				Description: "Explicitly store a memory/observation. Use when you want to remember something specific across sessions.",
				tier:        tierCore,
				InputSchema: map[string]any{
					"type":     "object",
					"required": []string{"content"},
					"properties": map[string]any{
						"content":    map[string]any{"type": "string", "description": "The content/knowledge to remember"},
						"title":      map[string]any{"type": "string", "description": "Short title for the memory"},
						"tags":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Concept tags (supports hierarchical: lang:go:concurrency)"},
						"rejected":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Alternatives considered and dismissed (for decision observations)"},
						"type":       map[string]any{"type": "string", "description": "Memory type: decision, bugfix, feature, discovery, refactor"},
						"importance": map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Importance score (0-1)"},
						"scope":      map[string]any{"type": "string", "enum": []string{"project", "global"}, "description": "Visibility scope"},
						"project":    map[string]any{"type": "string", "description": "Project ID (defaults to current)"},
						"ttl_days":       map[string]any{"type": "integer", "minimum": 1, "description": "TTL in days for verified facts. Auto-computed from tags if not provided. Only applies to observations with 'verified' tag."},
						"always_inject":  map[string]any{"type": "boolean", "description": "If true, this memory will be injected into every agent context regardless of query relevance. Use for behavioral rules that must always be present."},
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
						"query":  map[string]any{"type": "string", "description": "Natural language query"},
						"tags":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Filter by concept tags"},
						"type":   map[string]any{"type": "string", "description": "Filter by observation type"},
						"limit":  map[string]any{"type": "number", "default": 10, "minimum": 1, "maximum": 50},
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

	// Credential vault tools — only advertise when observation store is available
	if s.observationStore != nil {
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

	// Ingest/search require both documentStore and embedSvc
	if s.documentStore != nil && s.embedSvc != nil {
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

	// Always return only the 7 primary tools. Legacy aliases are handled
	// by callTool dispatch only — they don't appear in tools/list.
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
	case "graph_query":
		// Consolidated graph tool — routes by mode parameter
		gm, gErr := parseArgs(args)
		if gErr != nil {
			return "", gErr
		}
		mode := coerceString(gm["mode"], "related")
		switch mode {
		case "relationships":
			return s.handleGetObservationRelationships(ctx, args)
		case "neighbors":
			return s.handleGetGraphNeighbors(ctx, args)
		default: // "related"
			return s.handleFindRelatedObservations(ctx, args)
		}
	case "find_related_observations":
		return s.handleFindRelatedObservations(ctx, args)
	case "find_similar_observations":
		return s.handleFindSimilarObservations(ctx, args)
	case "get_patterns":
		return s.handleGetPatterns(ctx, args)
	case "get_memory_stats":
		return s.handleGetMemoryStats(ctx)
	case "bulk_delete_observations":
		return s.handleBulkDeleteObservations(ctx, args)
	case "bulk_mark_superseded":
		return s.handleBulkMarkSuperseded(ctx, args)
	case "bulk_boost_observations":
		return s.handleBulkBoostObservations(ctx, args)
	case "trigger_maintenance":
		return s.handleTriggerMaintenance(ctx)
	case "get_maintenance_stats":
		return s.handleGetMaintenanceStats(ctx)
	case "merge_observations":
		return s.handleMergeObservations(ctx, args)
	case "get_observation":
		return s.handleGetObservation(ctx, args)
	case "edit_observation":
		return s.handleEditObservation(ctx, args)
	case "get_observation_quality":
		return s.handleGetObservationQuality(ctx, args)
	case "suggest_consolidations":
		return s.handleSuggestConsolidations(ctx, args)
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
		return s.handleExplainSearchRanking(ctx, args)
	case "export_observations":
		return s.handleExportObservations(ctx, args)
	case "check_system_health":
		return s.handleCheckSystemHealth(ctx)
	case "analyze_search_patterns":
		return s.handleAnalyzeSearchPatterns(ctx, args)
	case "get_observation_relationships":
		return s.handleGetObservationRelationships(ctx, args)
	case "get_graph_neighbors":
		return s.handleGetGraphNeighbors(ctx, args)
	case "get_graph_stats":
		return s.handleGetGraphStats(ctx)
	case "get_observation_scoring_breakdown":
		return s.handleGetObservationScoringBreakdown(ctx, args)
	case "analyze_observation_importance":
		return s.handleAnalyzeObservationImportance(ctx, args)
	case "search_sessions":
		return s.handleSearchSessions(ctx, args)
	case "list_sessions":
		return s.handleListSessions(ctx, args)
	case "run_consolidation":
		return s.handleRunConsolidation(ctx, args)
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
	case "set_session_outcome":
		return s.handleSetSessionOutcomeMCP(ctx, args)
	}

	// Search-based tools: use parseArgs + coercion instead of direct unmarshal
	// to handle MCP clients sending numeric values as strings or floats.
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	params := buildSearchParams(m)

	var result *search.UnifiedSearchResult

	switch name {
	case "search":
		// Handle preset parameter for consolidated search
		preset := coerceString(m["preset"], "")
		switch preset {
		case "decisions":
			result, err = s.searchMgr.Decisions(ctx, params)
		case "changes":
			result, err = s.searchMgr.Changes(ctx, params)
		case "how_it_works":
			result, err = s.searchMgr.HowItWorks(ctx, params)
		default:
			result, err = s.searchMgr.UnifiedSearch(ctx, params)
		}

	// Timeline tools: consolidated via mode/alias
	case "timeline":
		mode := coerceString(m["mode"], "")
		switch mode {
		case "query":
			result, err = s.handleTimelineByQuery(ctx, m)
		case "recent":
			result, err = s.searchMgr.UnifiedSearch(ctx, params)
		default: // "anchor" or empty — default behavior
			result, err = s.handleTimeline(ctx, m)
		}

	// Search aliases (backward compatibility)
	case "decisions":
		result, err = s.searchMgr.Decisions(ctx, params)
	case "changes":
		result, err = s.searchMgr.Changes(ctx, params)
	case "how_it_works":
		result, err = s.searchMgr.HowItWorks(ctx, params)
	case "find_by_concept":
		params.Type = "observations"
		result, err = s.searchMgr.UnifiedSearch(ctx, params)
	case "find_by_file":
		params.Type = "observations"
		result, err = s.searchMgr.UnifiedSearch(ctx, params)
	case "find_by_type":
		params.Type = "observations"
		result, err = s.searchMgr.UnifiedSearch(ctx, params)
	case "get_recent_context":
		result, err = s.searchMgr.UnifiedSearch(ctx, params)

	// Timeline aliases (backward compatibility)
	case "get_context_timeline":
		result, err = s.handleTimeline(ctx, m)
	case "get_timeline_by_query":
		result, err = s.handleTimelineByQuery(ctx, m)

	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}

	if err != nil {
		return "", err
	}

	output, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}

	return string(output), nil
}

// TimelineParams represents parameters for timeline operations.
type TimelineParams struct {
	Query     string `json:"query"`
	Project   string `json:"project"`
	ObsType   string `json:"obs_type"`
	Concepts  string `json:"concepts"`
	Files     string `json:"files"`
	Format    string `json:"format"`
	AnchorID  int64  `json:"anchor_id"`
	Before    int    `json:"before"`
	After     int    `json:"after"`
	DateStart int64  `json:"dateStart"`
	DateEnd   int64  `json:"dateEnd"`
}

// handleTimeline handles timeline requests using pre-parsed args map.
func (s *Server) handleTimeline(ctx context.Context, m map[string]any) (*search.UnifiedSearchResult, error) {
	params := buildTimelineParams(m)

	if params.Before <= 0 {
		params.Before = 10
	}
	if params.After <= 0 {
		params.After = 10
	}

	// If query provided, first find anchor
	if params.Query != "" && params.AnchorID == 0 {
		searchParams := search.SearchParams{
			Query:   params.Query,
			Type:    "observations",
			Project: params.Project,
			Limit:   1,
		}
		result, err := s.searchMgr.UnifiedSearch(ctx, searchParams)
		if err != nil {
			return nil, err
		}
		if len(result.Results) > 0 {
			params.AnchorID = result.Results[0].ID
		}
	}

	if params.AnchorID == 0 {
		return &search.UnifiedSearchResult{Results: []search.SearchResult{}}, nil
	}

	// Fetch observations around anchor
	searchParams := search.SearchParams{
		Type:     "observations",
		Project:  params.Project,
		ObsType:  params.ObsType,
		Concepts: params.Concepts,
		Files:    params.Files,
		Limit:    params.Before + params.After + 1,
		Format:   params.Format,
	}

	return s.searchMgr.UnifiedSearch(ctx, searchParams)
}

// handleTimelineByQuery handles combined search + timeline requests using pre-parsed args map.
func (s *Server) handleTimelineByQuery(ctx context.Context, m map[string]any) (*search.UnifiedSearchResult, error) {
	params := buildTimelineParams(m)

	if params.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	// First search
	searchParams := search.SearchParams{
		Query:     params.Query,
		Type:      "observations",
		Project:   params.Project,
		DateStart: params.DateStart,
		DateEnd:   params.DateEnd,
		Limit:     1,
	}

	result, err := s.searchMgr.UnifiedSearch(ctx, searchParams)
	if err != nil {
		return nil, err
	}

	if len(result.Results) == 0 {
		return result, nil
	}

	// Now get timeline around that result
	m["anchor_id"] = result.Results[0].ID
	return s.handleTimeline(ctx, m)
}

// handleFindRelatedObservations finds observations related to a given observation ID.
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

	if params.ID == 0 {
		return "", fmt.Errorf("id is required")
	}

	// Use -1 as sentinel for "not provided" since 0.0 is a valid threshold
	if params.MinConfidence < 0 {
		params.MinConfidence = 0.5
	}

	if params.Limit == 0 {
		params.Limit = 20
	}
	if params.Limit > 100 {
		params.Limit = 100
	}

	// Get related observation IDs with confidence filter
	relatedIDs, err := s.relationStore.GetRelatedObservationIDs(ctx, params.ID, params.MinConfidence)
	if err != nil {
		return "", fmt.Errorf("failed to get related observations: %w", err)
	}

	if relatedIDs == nil {
		relatedIDs = []int64{}
	}

	// Limit results
	if len(relatedIDs) > params.Limit {
		relatedIDs = relatedIDs[:params.Limit]
	}

	// Fetch full observations in batch (avoids N+1 query problem)
	observations, err := s.observationStore.GetObservationsByIDsPreserveOrder(ctx, relatedIDs)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to batch fetch related observations, falling back to individual fetch")
		// Fallback to individual fetch if batch fails
		observations = make([]*models.Observation, 0, len(relatedIDs))
		for _, id := range relatedIDs {
			obs, fetchErr := s.observationStore.GetObservationByID(ctx, id)
			if fetchErr == nil && obs != nil {
				observations = append(observations, obs)
			}
		}
	}

	response := map[string]any{
		"observations": observations,
		"count":        len(observations),
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
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

	// Use vector search to find similar observations
	if s.vectorClient == nil {
		return "", fmt.Errorf("vector search not available")
	}

	where := vector.BuildWhereFilter(vector.DocTypeObservation, params.Project, true)
	results, err := s.vectorClient.Query(ctx, params.Query, params.Limit*2, where)
	if err != nil {
		return "", fmt.Errorf("vector search failed: %w", err)
	}

	// Filter by similarity threshold
	filtered := vector.FilterByThreshold(results, params.MinSimilarity, params.Limit)

	// Extract observation IDs and build similarity map
	obsIDs := vector.ExtractObservationIDs(filtered, params.Project)
	similarityMap := make(map[int64]float64, len(filtered))
	for _, r := range filtered {
		if sqliteID, ok := r.Metadata["sqlite_id"].(float64); ok {
			id := int64(sqliteID)
			if _, exists := similarityMap[id]; !exists {
				similarityMap[id] = r.Similarity
			}
		}
	}

	// Fetch full observations in batch (avoids N+1 query problem)
	observations, err := s.observationStore.GetObservationsByIDsPreserveOrder(ctx, obsIDs)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to batch fetch similar observations, falling back to individual fetch")
		observations = make([]*models.Observation, 0, len(obsIDs))
		for _, id := range obsIDs {
			obs, fetchErr := s.observationStore.GetObservationByID(ctx, id)
			if fetchErr == nil && obs != nil {
				observations = append(observations, obs)
			}
		}
	}

	// Build response with similarity scores
	type SimilarObservation struct {
		*models.Observation
		Similarity float64 `json:"similarity"`
	}

	similarObs := make([]SimilarObservation, 0, len(observations))
	for _, obs := range observations {
		sim := similarityMap[obs.ID]
		similarObs = append(similarObs, SimilarObservation{
			Observation: obs,
			Similarity:  sim,
		})
	}

	response := map[string]any{
		"observations":   similarObs,
		"count":          len(similarObs),
		"min_similarity": params.MinSimilarity,
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleGetPatterns returns patterns from the pattern store.
func (s *Server) handleGetPatterns(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Type    string
		Project string
		Query   string
		Limit   int
	}
	params.Type = coerceString(m["type"], "")
	params.Project = coerceString(m["project"], "")
	params.Query = coerceString(m["query"], "")
	params.Limit = coerceInt(m["limit"], 0)

	if params.Limit == 0 {
		params.Limit = 20
	}
	if params.Limit > 100 {
		params.Limit = 100
	}

	var patterns []*models.Pattern

	// Query patterns based on filters
	if params.Query != "" {
		// FTS search
		patterns, err = s.patternStore.SearchPatternsFTS(ctx, params.Query, params.Limit)
	} else if params.Type != "" {
		// Filter by type
		patterns, err = s.patternStore.GetPatternsByType(ctx, models.PatternType(params.Type), params.Limit)
	} else if params.Project != "" {
		// Filter by project
		patterns, err = s.patternStore.GetPatternsByProject(ctx, params.Project, params.Limit)
	} else {
		// Get all active patterns
		patterns, err = s.patternStore.GetActivePatterns(ctx, params.Limit, 0, "")
	}

	if err != nil {
		return "", fmt.Errorf("failed to get patterns: %w", err)
	}

	response := map[string]any{
		"patterns": patterns,
		"count":    len(patterns),
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

	// Get vector count
	if s.vectorClient != nil {
		count, err := s.vectorClient.Count(ctx)
		if err == nil {
			stats["vector_count"] = count
		}

		// Cache stats
		cacheStats := s.vectorClient.GetCacheStats()
		stats["embedding_cache"] = map[string]any{
			"hit_rate": cacheStats.HitRate(),
		}

		// Model version
		stats["embedding_model"] = s.vectorClient.ModelVersion()
	}

	// Get pattern stats
	if s.patternStore != nil {
		patternStats, err := s.patternStore.GetPatternStats(ctx)
		if err == nil && patternStats != nil {
			stats["patterns"] = map[string]any{
				"total":             patternStats.Total,
				"active":            patternStats.Active,
				"deprecated":        patternStats.Deprecated,
				"merged":            patternStats.Merged,
				"total_occurrences": patternStats.TotalOccurrences,
				"avg_confidence":    patternStats.AvgConfidence,
			}
		}
	}

	// Get search metrics
	if s.searchMgr != nil {
		searchMetrics := s.searchMgr.Metrics()
		if searchMetrics != nil {
			stats["search"] = searchMetrics.GetStats()
		}
	}

	output, err := json.Marshal(stats)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleBulkDeleteObservations deletes multiple observations by ID.
func (s *Server) handleBulkDeleteObservations(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		IDs           []int64
		DeleteVectors bool
	}
	params.IDs = coerceInt64Slice(m["ids"])
	params.DeleteVectors = coerceBool(m["delete_vectors"], true)

	if len(params.IDs) == 0 {
		return "", fmt.Errorf("ids is required")
	}

	if len(params.IDs) > 1000 {
		return "", fmt.Errorf("maximum 1000 IDs per request")
	}

	var deleted int64
	var errors []string

	// Delete in batches
	batchSize := 100
	for i := 0; i < len(params.IDs); i += batchSize {
		end := min(i+batchSize, len(params.IDs))
		batch := params.IDs[i:end]

		for _, id := range batch {
			if err := s.observationStore.DeleteObservation(ctx, id); err != nil {
				errors = append(errors, fmt.Sprintf("id %d: %v", id, err))
				continue
			}
			deleted++

			// Delete associated vectors if requested
			if params.DeleteVectors && s.vectorClient != nil {
				_ = s.vectorClient.DeleteByObservationID(ctx, id)
			}
		}
	}

	response := map[string]any{
		"deleted": deleted,
		"total":   len(params.IDs),
	}
	if len(errors) > 0 {
		response["errors"] = errors
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	// Return error if all deletions failed (complete failure)
	if deleted == 0 && len(errors) > 0 {
		return string(output), fmt.Errorf("bulk delete failed: %d errors, first: %s", len(errors), errors[0])
	}

	return string(output), nil
}

// handleBulkMarkSuperseded marks multiple observations as superseded.
func (s *Server) handleBulkMarkSuperseded(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		IDs []int64
	}
	params.IDs = coerceInt64Slice(m["ids"])

	if len(params.IDs) == 0 {
		return "", fmt.Errorf("ids is required")
	}

	if len(params.IDs) > 1000 {
		return "", fmt.Errorf("maximum 1000 IDs per request")
	}

	// Use batch update for efficiency (single query instead of N queries)
	updated, err := s.observationStore.MarkAsSupersededBatch(ctx, params.IDs)
	if err != nil {
		return "", fmt.Errorf("batch mark as superseded: %w", err)
	}

	response := map[string]any{
		"updated": updated,
		"total":   len(params.IDs),
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleBulkBoostObservations boosts the importance score of multiple observations.
func (s *Server) handleBulkBoostObservations(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		IDs   []int64
		Boost float64
	}
	params.IDs = coerceInt64Slice(m["ids"])
	params.Boost = coerceFloat64(m["boost"], 0)

	if len(params.IDs) == 0 {
		return "", fmt.Errorf("ids is required")
	}

	if len(params.IDs) > 1000 {
		return "", fmt.Errorf("maximum 1000 IDs per request")
	}

	if params.Boost < -1.0 || params.Boost > 1.0 {
		return "", fmt.Errorf("boost must be between -1.0 and 1.0")
	}

	var boosted int64
	var errors []string

	// Batch fetch all observations in one query instead of N queries
	observations, err := s.observationStore.GetObservationsByIDs(ctx, params.IDs, "", 0)
	if err != nil {
		return "", fmt.Errorf("batch fetch observations: %w", err)
	}

	// Build a map for O(1) lookup
	obsMap := make(map[int64]*models.Observation, len(observations))
	for _, obs := range observations {
		obsMap[obs.ID] = obs
	}

	// Calculate new scores and prepare batch update
	scoresToUpdate := make(map[int64]float64, len(params.IDs))
	for _, id := range params.IDs {
		obs, found := obsMap[id]
		if !found {
			errors = append(errors, fmt.Sprintf("id %d: not found", id))
			continue
		}

		// Calculate new importance score (clamp between 0 and 1)
		newScore := obs.ImportanceScore + params.Boost
		if newScore < 0 {
			newScore = 0
		}
		if newScore > 1 {
			newScore = 1
		}
		scoresToUpdate[id] = newScore
	}

	// Batch update all scores in one operation
	if len(scoresToUpdate) > 0 {
		if err := s.observationStore.UpdateImportanceScores(ctx, scoresToUpdate); err != nil {
			return "", fmt.Errorf("batch update scores: %w", err)
		}
		boosted = int64(len(scoresToUpdate))
	}

	response := map[string]any{
		"boosted":    boosted,
		"total":      len(params.IDs),
		"boost_used": params.Boost,
	}
	if len(errors) > 0 {
		response["errors"] = errors
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleTriggerMaintenance triggers an immediate maintenance run.
func (s *Server) handleTriggerMaintenance(ctx context.Context) (string, error) {
	if s.maintenanceService == nil {
		return "", fmt.Errorf("maintenance service not available")
	}

	s.maintenanceService.RunNow(ctx)

	response := map[string]any{
		"status":  "triggered",
		"message": "Maintenance run started in background",
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleGetMaintenanceStats returns maintenance statistics.
func (s *Server) handleGetMaintenanceStats(_ context.Context) (string, error) {
	if s.maintenanceService == nil {
		return "", fmt.Errorf("maintenance service not available")
	}

	stats := s.maintenanceService.Stats()

	output, err := json.Marshal(stats)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleMergeObservations merges two observations, keeping the target and superseding the source.
func (s *Server) handleMergeObservations(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		SourceID int64
		TargetID int64
		Boost    float64
	}
	params.SourceID = coerceInt64(m["source_id"], 0)
	params.TargetID = coerceInt64(m["target_id"], 0)
	params.Boost = coerceFloat64(m["boost"], 0)

	if params.SourceID == 0 || params.TargetID == 0 {
		return "", fmt.Errorf("source_id and target_id are required")
	}

	if params.SourceID == params.TargetID {
		return "", fmt.Errorf("source_id and target_id cannot be the same")
	}

	// Set default boost if not provided
	if params.Boost == 0 {
		params.Boost = 0.1
	}
	if params.Boost < 0 || params.Boost > 0.5 {
		return "", fmt.Errorf("boost must be between 0 and 0.5")
	}

	// Get both observations to verify they exist
	source, err := s.observationStore.GetObservationByID(ctx, params.SourceID)
	if err != nil {
		return "", fmt.Errorf("get source observation: %w", err)
	}
	if source == nil {
		return "", fmt.Errorf("source observation %d not found", params.SourceID)
	}

	target, err := s.observationStore.GetObservationByID(ctx, params.TargetID)
	if err != nil {
		return "", fmt.Errorf("get target observation: %w", err)
	}
	if target == nil {
		return "", fmt.Errorf("target observation %d not found", params.TargetID)
	}

	// Mark source as superseded
	if err := s.observationStore.MarkAsSuperseded(ctx, params.SourceID); err != nil {
		return "", fmt.Errorf("mark source as superseded: %w", err)
	}

	// Boost target's importance score
	newScore := target.ImportanceScore + params.Boost
	if newScore > 1.0 {
		newScore = 1.0
	}
	if err := s.observationStore.UpdateImportanceScore(ctx, params.TargetID, newScore); err != nil {
		return "", fmt.Errorf("update target score: %w", err)
	}

	response := map[string]any{
		"merged":           true,
		"source_id":        params.SourceID,
		"source_title":     source.Title.String,
		"target_id":        params.TargetID,
		"target_title":     target.Title.String,
		"target_new_score": newScore,
		"target_old_score": target.ImportanceScore,
		"boost_applied":    params.Boost,
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleGetObservation returns a single observation by ID.
func (s *Server) handleGetObservation(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		ID int64
	}
	params.ID = coerceInt64(m["id"], 0)

	if params.ID == 0 {
		return "", fmt.Errorf("id is required")
	}

	obs, err := s.observationStore.GetObservationByID(ctx, params.ID)
	if err != nil {
		return "", fmt.Errorf("get observation: %w", err)
	}
	if obs == nil {
		return "", fmt.Errorf("observation %d not found", params.ID)
	}

	output, err := json.Marshal(obs)
	if err != nil {
		return "", fmt.Errorf("marshal observation: %w", err)
	}

	return string(output), nil
}

// handleEditObservation updates an existing observation with provided fields.
func (s *Server) handleEditObservation(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Title         *string
		Subtitle      *string
		Narrative     *string
		Scope         *string
		Status        *string
		StatusReason  *string
		Facts         []string
		Concepts      []string
		FilesRead     []string
		FilesModified []string
		ID            int64
	}
	params.ID = coerceInt64(m["id"], 0)
	if v, ok := m["title"]; ok && v != nil {
		s := coerceString(v, "")
		params.Title = &s
	}
	if v, ok := m["subtitle"]; ok && v != nil {
		s := coerceString(v, "")
		params.Subtitle = &s
	}
	if v, ok := m["narrative"]; ok && v != nil {
		s := coerceString(v, "")
		params.Narrative = &s
	}
	if v, ok := m["scope"]; ok && v != nil {
		s := coerceString(v, "")
		params.Scope = &s
	}
	if v, ok := m["status"]; ok && v != nil {
		s := coerceString(v, "")
		params.Status = &s
	}
	if v, ok := m["status_reason"]; ok && v != nil {
		s := coerceString(v, "")
		params.StatusReason = &s
	}
	params.Facts = coerceStringSlice(m["facts"])
	params.Concepts = coerceStringSlice(m["concepts"])
	params.FilesRead = coerceStringSlice(m["files_read"])
	params.FilesModified = coerceStringSlice(m["files_modified"])

	if params.ID == 0 {
		return "", fmt.Errorf("id is required")
	}

	// Validate scope if provided
	if params.Scope != nil && *params.Scope != "project" && *params.Scope != "global" {
		return "", fmt.Errorf("scope must be 'project' or 'global'")
	}

	// Validate status if provided
	if params.Status != nil && *params.Status != "active" && *params.Status != "resolved" {
		return "", fmt.Errorf("status must be 'active' or 'resolved'")
	}

	// Handle always_inject: merge "always-inject" concept into existing concepts
	if v, ok := m["always_inject"]; ok && v != nil {
		alwaysInject := coerceBool(v, false)
		// Fetch current observation to get existing concepts
		existing, fetchErr := s.observationStore.GetObservationByID(ctx, params.ID)
		if fetchErr == nil && existing != nil {
			concepts := make([]string, 0, len(existing.Concepts)+1)
			hasIt := false
			for _, c := range existing.Concepts {
				if c == "always-inject" {
					hasIt = true
					if !alwaysInject {
						continue // remove it
					}
				}
				concepts = append(concepts, c)
			}
			if alwaysInject && !hasIt {
				concepts = append(concepts, "always-inject")
			}
			if alwaysInject != hasIt { // changed
				params.Concepts = concepts
			}
		}
	}

	// Build update struct
	update := &gorm.ObservationUpdate{}
	if params.Title != nil {
		update.Title = params.Title
	}
	if params.Subtitle != nil {
		update.Subtitle = params.Subtitle
	}
	if params.Narrative != nil {
		update.Narrative = params.Narrative
	}
	if params.Facts != nil {
		update.Facts = &params.Facts
	}
	if params.Concepts != nil {
		update.Concepts = &params.Concepts
	}
	if params.FilesRead != nil {
		update.FilesRead = &params.FilesRead
	}
	if params.FilesModified != nil {
		update.FilesModified = &params.FilesModified
	}
	if params.Scope != nil {
		update.Scope = params.Scope
	}
	if params.Status != nil {
		update.Status = params.Status
	}
	if params.StatusReason != nil {
		update.StatusReason = params.StatusReason
	}

	// Update the observation
	updatedObs, err := s.observationStore.UpdateObservation(ctx, params.ID, update)
	if err != nil {
		return "", fmt.Errorf("update observation: %w", err)
	}

	// Note: Vector resync is handled by the worker service when available
	// The MCP server doesn't have access to the embedding service

	response := map[string]any{
		"updated":       true,
		"observation":   updatedObs,
		"vector_resync": "deferred",
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleGetObservationQuality returns quality metrics for an observation.
func (s *Server) handleGetObservationQuality(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		ID int64
	}
	params.ID = coerceInt64(m["id"], 0)

	if params.ID == 0 {
		return "", fmt.Errorf("id is required")
	}

	obs, err := s.observationStore.GetObservationByID(ctx, params.ID)
	if err != nil {
		return "", fmt.Errorf("get observation: %w", err)
	}
	if obs == nil {
		return "", fmt.Errorf("observation %d not found", params.ID)
	}

	// Calculate completeness score
	completenessScore := 0.0
	maxScore := 5.0
	suggestions := []string{}

	// Check title (required, 1 point)
	if obs.Title.Valid && obs.Title.String != "" {
		completenessScore += 1.0
	} else {
		suggestions = append(suggestions, "Add a descriptive title")
	}

	// Check narrative (important, 1.5 points)
	if obs.Narrative.Valid && len(obs.Narrative.String) > 50 {
		completenessScore += 1.5
	} else if obs.Narrative.Valid && obs.Narrative.String != "" {
		completenessScore += 0.5
		suggestions = append(suggestions, "Expand the narrative to provide more context (aim for 50+ characters)")
	} else {
		suggestions = append(suggestions, "Add a narrative explaining the observation")
	}

	// Check facts (valuable, 1 point)
	if len(obs.Facts) >= 2 {
		completenessScore += 1.0
	} else if len(obs.Facts) == 1 {
		completenessScore += 0.5
		suggestions = append(suggestions, "Add more key facts (aim for 2+)")
	} else {
		suggestions = append(suggestions, "Add key facts to capture important details")
	}

	// Check concepts (useful, 0.75 points)
	if len(obs.Concepts) >= 2 {
		completenessScore += 0.75
	} else if len(obs.Concepts) == 1 {
		completenessScore += 0.25
		suggestions = append(suggestions, "Add more concept tags for better discoverability")
	} else {
		suggestions = append(suggestions, "Add concept tags to categorize this observation")
	}

	// Check file references (helpful, 0.75 points)
	if len(obs.FilesRead) > 0 || len(obs.FilesModified) > 0 {
		completenessScore += 0.75
	} else {
		suggestions = append(suggestions, "Consider adding file references if applicable")
	}

	// Determine quality tier
	qualityTier := "poor"
	switch {
	case completenessScore >= 4.0:
		qualityTier = "excellent"
	case completenessScore >= 3.0:
		qualityTier = "good"
	case completenessScore >= 2.0:
		qualityTier = "fair"
	}

	response := map[string]any{
		"id":                 params.ID,
		"completeness_score": completenessScore,
		"max_score":          maxScore,
		"completeness_pct":   (completenessScore / maxScore) * 100,
		"quality_tier":       qualityTier,
		"importance_score":   obs.ImportanceScore,
		"retrieval_count":    obs.RetrievalCount,
		"is_superseded":      obs.IsSuperseded,
		"suggestions":        suggestions,
		"field_stats": map[string]any{
			"has_title":            obs.Title.Valid && obs.Title.String != "",
			"has_narrative":        obs.Narrative.Valid && obs.Narrative.String != "",
			"narrative_length":     len(obs.Narrative.String),
			"facts_count":          len(obs.Facts),
			"concepts_count":       len(obs.Concepts),
			"files_read_count":     len(obs.FilesRead),
			"files_modified_count": len(obs.FilesModified),
		},
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleSuggestConsolidations finds observations that could be merged.
func (s *Server) handleSuggestConsolidations(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Project       string
		MinSimilarity float64
		Limit         int
	}
	params.Project = coerceString(m["project"], "")
	params.MinSimilarity = coerceFloat64(m["min_similarity"], 0)
	params.Limit = coerceInt(m["limit"], 0)

	// Set defaults
	if params.MinSimilarity == 0 {
		params.MinSimilarity = 0.8
	}
	if params.Limit == 0 {
		params.Limit = 10
	}
	if params.MinSimilarity < 0.5 || params.MinSimilarity > 1.0 {
		return "", fmt.Errorf("min_similarity must be between 0.5 and 1.0")
	}

	// Get recent observations to analyze
	obs, err := s.observationStore.GetRecentObservations(ctx, params.Project, 200)
	if err != nil {
		return "", fmt.Errorf("get observations: %w", err)
	}

	if len(obs) < 2 {
		response := map[string]any{
			"groups":  []any{},
			"message": "Not enough observations to analyze",
		}
		output, _ := json.Marshal(response)
		return string(output), nil
	}

	// Find similar pairs using vector search if available
	type consolidationGroup struct {
		Primary    *models.Observation   `json:"primary"`
		Reason     string                `json:"reason"`
		Similar    []*models.Observation `json:"similar"`
		Similarity float64               `json:"avg_similarity"`
	}

	groups := []consolidationGroup{}
	seen := make(map[int64]bool)

	// For each observation, find similar ones
	for _, primary := range obs {
		if seen[primary.ID] {
			continue
		}

		// Build search text from observation
		searchText := primary.Title.String
		if primary.Narrative.Valid {
			searchText += " " + primary.Narrative.String
		}

		if searchText == "" || s.vectorClient == nil {
			continue
		}

		// Query for similar observations
		where := vector.BuildWhereFilter(vector.DocTypeObservation, params.Project, true)
		results, err := s.vectorClient.Query(ctx, searchText, 10, where)
		if err != nil {
			continue
		}

		// Find similar observations above threshold
		similar := []*models.Observation{}
		totalSimilarity := 0.0

		for _, r := range results {
			// Extract observation ID from metadata
			sqliteID, ok := r.Metadata["sqlite_id"].(float64)
			if !ok {
				continue
			}
			obsID := int64(sqliteID)

			if obsID == primary.ID || seen[obsID] {
				continue
			}
			if r.Similarity >= params.MinSimilarity {
				// Fetch the similar observation
				simObs, err := s.observationStore.GetObservationByID(ctx, obsID)
				if err != nil || simObs == nil {
					continue
				}
				similar = append(similar, simObs)
				totalSimilarity += r.Similarity
				seen[obsID] = true
			}
		}

		if len(similar) > 0 {
			seen[primary.ID] = true
			avgSimilarity := totalSimilarity / float64(len(similar))

			// Determine consolidation reason
			reason := "Content similarity detected"
			if len(primary.Concepts) > 0 && len(similar) > 0 {
				// Check for concept overlap
				conceptMap := make(map[string]bool)
				for _, c := range primary.Concepts {
					conceptMap[c] = true
				}
				for _, sim := range similar {
					for _, c := range sim.Concepts {
						if conceptMap[c] {
							reason = "Similar content with shared concepts"
							break
						}
					}
				}
			}

			groups = append(groups, consolidationGroup{
				Primary:    primary,
				Similar:    similar,
				Similarity: avgSimilarity,
				Reason:     reason,
			})

			if len(groups) >= params.Limit {
				break
			}
		}
	}

	response := map[string]any{
		"groups":         groups,
		"total_analyzed": len(obs),
		"groups_found":   len(groups),
		"min_similarity": params.MinSimilarity,
		"recommendation": "Review each group and use merge_observations to consolidate where appropriate",
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleTagObservation adds, removes, or sets tags on an observation.
func (s *Server) handleTagObservation(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Mode string
		Tags []string
		ID   int64
	}
	params.Mode = coerceString(m["mode"], "add")
	params.Tags = coerceStringSlice(m["tags"])
	params.ID = coerceInt64(m["id"], 0)

	if params.ID == 0 {
		return "", fmt.Errorf("id is required")
	}
	if len(params.Tags) == 0 {
		return "", fmt.Errorf("tags is required")
	}
	if params.Mode != "add" && params.Mode != "remove" && params.Mode != "set" {
		return "", fmt.Errorf("mode must be 'add', 'remove', or 'set'")
	}

	// Get current observation
	obs, err := s.observationStore.GetObservationByID(ctx, params.ID)
	if err != nil {
		return "", fmt.Errorf("get observation: %w", err)
	}
	if obs == nil {
		return "", fmt.Errorf("observation %d not found", params.ID)
	}

	// Compute new tags
	var newTags []string
	switch params.Mode {
	case "set":
		newTags = params.Tags
	case "add":
		// Add new tags, avoiding duplicates
		tagSet := make(map[string]bool)
		for _, t := range obs.Concepts {
			tagSet[t] = true
			newTags = append(newTags, t)
		}
		for _, t := range params.Tags {
			if !tagSet[t] {
				tagSet[t] = true
				newTags = append(newTags, t)
			}
		}
	case "remove":
		// Remove specified tags
		removeSet := make(map[string]bool)
		for _, t := range params.Tags {
			removeSet[t] = true
		}
		for _, t := range obs.Concepts {
			if !removeSet[t] {
				newTags = append(newTags, t)
			}
		}
	}

	// Update using existing UpdateObservation method
	update := &gorm.ObservationUpdate{
		Concepts: &newTags,
	}
	updatedObs, err := s.observationStore.UpdateObservation(ctx, params.ID, update)
	if err != nil {
		return "", fmt.Errorf("update observation: %w", err)
	}

	response := map[string]any{
		"id":           params.ID,
		"mode":         params.Mode,
		"tags_applied": params.Tags,
		"current_tags": updatedObs.Concepts,
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleGetObservationsByTag retrieves observations with a specific concept tag.
func (s *Server) handleGetObservationsByTag(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Tag     string
		Project string
		Limit   int
	}
	params.Tag = coerceString(m["tag"], "")
	params.Project = coerceString(m["project"], "")
	params.Limit = coerceInt(m["limit"], 50)

	if params.Tag == "" {
		return "", fmt.Errorf("tag is required")
	}
	if params.Limit < 1 || params.Limit > 200 {
		params.Limit = 50
	}

	// Use search with concept filter
	searchParams := search.SearchParams{
		Query:    params.Tag,
		Type:     "observations",
		Project:  params.Project,
		Limit:    params.Limit,
		Concepts: params.Tag,
	}

	result, err := s.searchMgr.UnifiedSearch(ctx, searchParams)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}

	// Filter results to only include observations with the exact tag in metadata
	var filtered []search.SearchResult
	for _, r := range result.Results {
		if r.Type != "observation" {
			continue
		}
		// Check if concepts metadata contains the tag
		if concepts, ok := r.Metadata["concepts"].([]any); ok {
			for _, c := range concepts {
				if cs, ok := c.(string); ok && cs == params.Tag {
					filtered = append(filtered, r)
					break
				}
			}
		}
	}

	response := map[string]any{
		"tag":          params.Tag,
		"observations": filtered,
		"count":        len(filtered),
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleGetTemporalTrends analyzes observation creation patterns over time.
func (s *Server) handleGetTemporalTrends(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Project string
		GroupBy string
		Days    int
	}
	params.Project = coerceString(m["project"], "")
	params.GroupBy = coerceString(m["group_by"], "day")
	params.Days = coerceInt(m["days"], 30)

	if params.Days < 1 || params.Days > 365 {
		params.Days = 30
	}

	// Get observations for analysis
	obs, err := s.observationStore.GetRecentObservations(ctx, params.Project, params.Days*50) // Rough estimate
	if err != nil {
		return "", fmt.Errorf("get observations: %w", err)
	}

	// Calculate time range
	now := time.Now()
	startTime := now.AddDate(0, 0, -params.Days)
	startEpoch := startTime.UnixMilli()

	// Group observations by time bucket
	buckets := make(map[string]int)
	typeDistribution := make(map[string]int)
	conceptCounts := make(map[string]int)
	totalInRange := 0

	for _, o := range obs {
		if o.CreatedAtEpoch < startEpoch {
			continue
		}
		totalInRange++

		created := time.UnixMilli(o.CreatedAtEpoch)
		var key string
		switch params.GroupBy {
		case "week":
			year, week := created.ISOWeek()
			key = fmt.Sprintf("%d-W%02d", year, week)
		case "hour_of_day":
			key = fmt.Sprintf("%02d:00", created.Hour())
		default: // day
			key = created.Format("2006-01-02")
		}
		buckets[key]++

		// Track type distribution
		typeDistribution[string(o.Type)]++

		// Track top concepts
		for _, c := range o.Concepts {
			conceptCounts[c]++
		}
	}

	// Find peak period
	peakPeriod := ""
	peakCount := 0
	for k, v := range buckets {
		if v > peakCount {
			peakCount = v
			peakPeriod = k
		}
	}

	// Sort and get top concepts
	type conceptEntry struct {
		name  string
		count int
	}
	var topConcepts []conceptEntry
	for name, count := range conceptCounts {
		topConcepts = append(topConcepts, conceptEntry{name, count})
	}
	// Simple sort - just take top 10
	for i := 0; i < len(topConcepts) && i < 10; i++ {
		for j := i + 1; j < len(topConcepts); j++ {
			if topConcepts[j].count > topConcepts[i].count {
				topConcepts[i], topConcepts[j] = topConcepts[j], topConcepts[i]
			}
		}
	}
	if len(topConcepts) > 10 {
		topConcepts = topConcepts[:10]
	}
	topConceptsMap := make([]map[string]any, len(topConcepts))
	for i, c := range topConcepts {
		topConceptsMap[i] = map[string]any{"concept": c.name, "count": c.count}
	}

	response := map[string]any{
		"period": map[string]any{
			"start":    startTime.Format("2006-01-02"),
			"end":      now.Format("2006-01-02"),
			"days":     params.Days,
			"group_by": params.GroupBy,
		},
		"summary": map[string]any{
			"total_observations": totalInRange,
			"daily_average":      float64(totalInRange) / float64(params.Days),
			"peak_period":        peakPeriod,
			"peak_count":         peakCount,
		},
		"distribution":      buckets,
		"type_distribution": typeDistribution,
		"top_concepts":      topConceptsMap,
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleGetDataQualityReport generates a comprehensive quality assessment.
func (s *Server) handleGetDataQualityReport(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Project string
		Limit   int
	}
	params.Project = coerceString(m["project"], "")
	params.Limit = coerceInt(m["limit"], 100)

	if params.Limit < 10 || params.Limit > 500 {
		params.Limit = 100
	}

	// Get observations for analysis
	obs, err := s.observationStore.GetRecentObservations(ctx, params.Project, params.Limit)
	if err != nil {
		return "", fmt.Errorf("get observations: %w", err)
	}

	if len(obs) == 0 {
		return `{"error": "no observations found", "analyzed": 0}`, nil
	}

	// Quality analysis
	qualityScores := make([]float64, 0, len(obs))
	issuesFound := make(map[string]int)
	improvements := make(map[string]int)
	scoreDistribution := map[string]int{"excellent": 0, "good": 0, "fair": 0, "poor": 0}

	for _, o := range obs {
		score := 0.0
		maxScore := 5.0

		// Check completeness
		if o.Title.Valid && o.Title.String != "" {
			score += 1.0
		} else {
			issuesFound["missing_title"]++
			improvements["add_title"]++
		}

		if o.Narrative.Valid && o.Narrative.String != "" {
			score += 1.0
		} else {
			issuesFound["missing_narrative"]++
			improvements["add_narrative"]++
		}

		if len(o.Facts) > 0 {
			score += 1.0
			if len(o.Facts) >= 3 {
				score += 0.5 // Bonus for multiple facts
			}
		} else {
			issuesFound["no_facts"]++
			improvements["add_facts"]++
		}

		if len(o.Concepts) > 0 {
			score += 1.0
		} else {
			issuesFound["no_concepts"]++
			improvements["add_concepts"]++
		}

		if len(o.FilesRead) > 0 || len(o.FilesModified) > 0 {
			score += 0.5
		}

		normalized := (score / maxScore) * 100
		qualityScores = append(qualityScores, normalized)

		// Categorize
		switch {
		case normalized >= 80:
			scoreDistribution["excellent"]++
		case normalized >= 60:
			scoreDistribution["good"]++
		case normalized >= 40:
			scoreDistribution["fair"]++
		default:
			scoreDistribution["poor"]++
		}
	}

	// Calculate average
	var avgScore float64
	for _, s := range qualityScores {
		avgScore += s
	}
	avgScore /= float64(len(qualityScores))

	// Build top issues list
	type issueEntry struct {
		name  string
		count int
	}
	var topIssues []issueEntry
	for name, count := range issuesFound {
		topIssues = append(topIssues, issueEntry{name, count})
	}
	for i := 0; i < len(topIssues) && i < 5; i++ {
		for j := i + 1; j < len(topIssues); j++ {
			if topIssues[j].count > topIssues[i].count {
				topIssues[i], topIssues[j] = topIssues[j], topIssues[i]
			}
		}
	}
	if len(topIssues) > 5 {
		topIssues = topIssues[:5]
	}

	// Convert top issues to response format
	topIssuesList := make([]map[string]any, 0, len(topIssues))
	for _, issue := range topIssues {
		topIssuesList = append(topIssuesList, map[string]any{
			"issue": issue.name,
			"count": issue.count,
		})
	}

	response := map[string]any{
		"analyzed": len(obs),
		"project":  params.Project,
		"quality_summary": map[string]any{
			"average_score": fmt.Sprintf("%.1f%%", avgScore),
			"distribution":  scoreDistribution,
		},
		"issues_found": issuesFound,
		"top_issues":   topIssuesList,
		"improvements": improvements,
		"recommendations": []string{
			"Add titles to observations for better discoverability",
			"Include narratives to provide context",
			"Add concept tags for better organization",
			"Include at least 2-3 key facts per observation",
		},
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleBatchTagByPattern applies tags to observations matching a pattern.
func (s *Server) handleBatchTagByPattern(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Pattern    string
		Project    string
		Tags       []string
		MaxMatches int
		DryRun     bool
	}
	params.Pattern = coerceString(m["pattern"], "")
	params.Project = coerceString(m["project"], "")
	params.Tags = coerceStringSlice(m["tags"])
	params.MaxMatches = coerceInt(m["max_matches"], 100)
	params.DryRun = coerceBool(m["dry_run"], true)

	if params.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if len(params.Tags) == 0 {
		return "", fmt.Errorf("tags is required")
	}
	if params.MaxMatches < 1 || params.MaxMatches > 500 {
		params.MaxMatches = 100
	}

	// Search for matching observations using the pattern
	searchParams := search.SearchParams{
		Query:   params.Pattern,
		Type:    "observations",
		Project: params.Project,
		Limit:   params.MaxMatches,
	}

	result, err := s.searchMgr.UnifiedSearch(ctx, searchParams)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}

	// Collect matching observation IDs
	var matches []map[string]any
	var taggedCount int

	for _, r := range result.Results {
		if r.Type != "observation" {
			continue
		}

		match := map[string]any{
			"id":    r.ID,
			"title": r.Title,
			"score": r.Score,
		}
		matches = append(matches, match)

		// Apply tags if not dry run
		if !params.DryRun {
			obs, err := s.observationStore.GetObservationByID(ctx, r.ID)
			if err != nil || obs == nil {
				continue
			}

			// Merge existing tags with new tags (avoid duplicates)
			tagSet := make(map[string]bool)
			newTags := make([]string, 0, len(obs.Concepts)+len(params.Tags))
			for _, t := range obs.Concepts {
				tagSet[t] = true
				newTags = append(newTags, t)
			}
			for _, t := range params.Tags {
				if !tagSet[t] {
					tagSet[t] = true
					newTags = append(newTags, t)
				}
			}

			update := &gorm.ObservationUpdate{
				Concepts: &newTags,
			}
			_, err = s.observationStore.UpdateObservation(ctx, r.ID, update)
			if err == nil {
				taggedCount++
			}
		}
	}

	response := map[string]any{
		"pattern":       params.Pattern,
		"tags":          params.Tags,
		"dry_run":       params.DryRun,
		"matches_found": len(matches),
		"matches":       matches,
	}

	if !params.DryRun {
		response["tagged_count"] = taggedCount
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleExplainSearchRanking explains why each observation ranked where it did in search results.
func (s *Server) handleExplainSearchRanking(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Query   string
		Project string
		TopN    int
	}
	params.Query = coerceString(m["query"], "")
	params.Project = coerceString(m["project"], "")
	params.TopN = coerceInt(m["top_n"], 5)

	if params.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	if params.TopN < 1 || params.TopN > 20 {
		params.TopN = 5
	}

	// Perform search to get results
	searchParams := search.SearchParams{
		Query:   params.Query,
		Type:    "observations",
		Project: params.Project,
		Limit:   params.TopN,
		OrderBy: "relevance",
	}

	result, err := s.searchMgr.UnifiedSearch(ctx, searchParams)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}

	// Build detailed explanations for each result
	type RankExplanation struct {
		ScoreBreakdown map[string]float64 `json:"score_breakdown"`
		Metadata       map[string]any     `json:"metadata,omitempty"`
		Title          string             `json:"title"`
		Type           string             `json:"type"`
		MatchedFields  []string           `json:"matched_fields"`
		Rank           int                `json:"rank"`
		ID             int64              `json:"id"`
		Score          float64            `json:"score"`
	}

	explanations := make([]RankExplanation, 0, len(result.Results))
	for i, r := range result.Results {
		exp := RankExplanation{
			Rank:     i + 1,
			ID:       r.ID,
			Title:    r.Title,
			Type:     r.Type,
			Score:    r.Score,
			Metadata: r.Metadata,
		}

		// Build score breakdown from available metadata
		exp.ScoreBreakdown = make(map[string]float64)
		if vs, ok := r.Metadata["vector_score"].(float64); ok {
			exp.ScoreBreakdown["vector_similarity"] = vs
		}
		if is, ok := r.Metadata["importance_score"].(float64); ok {
			exp.ScoreBreakdown["importance"] = is
		}
		if ts, ok := r.Metadata["text_score"].(float64); ok {
			exp.ScoreBreakdown["text_match"] = ts
		}
		if rs, ok := r.Metadata["recency_score"].(float64); ok {
			exp.ScoreBreakdown["recency"] = rs
		}
		// Add base score estimate if breakdown is incomplete
		if len(exp.ScoreBreakdown) == 0 {
			exp.ScoreBreakdown["combined_score"] = r.Score
		}

		// Determine matched fields
		exp.MatchedFields = []string{}
		if r.Metadata["field_type"] != nil {
			if ft, ok := r.Metadata["field_type"].(string); ok && ft != "" {
				exp.MatchedFields = append(exp.MatchedFields, ft)
			}
		}

		explanations = append(explanations, exp)
	}

	response := map[string]any{
		"query":        params.Query,
		"project":      params.Project,
		"result_count": len(explanations),
		"explanations": explanations,
		"tips": []string{
			"Higher vector_similarity indicates better semantic match with query",
			"Importance score reflects user feedback and retrieval history",
			"Recency boosts newer observations slightly",
			"Use tag_observation to boost important observations",
		},
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}

	return string(output), nil
}

// handleExportObservations exports observations in various formats.
func (s *Server) handleExportObservations(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Format    string
		Project   string
		ObsType   string
		Limit     int
		DateStart int64
		DateEnd   int64
	}
	params.Format = coerceString(m["format"], "json")
	params.Project = coerceString(m["project"], "")
	params.ObsType = coerceString(m["obs_type"], "")
	params.Limit = coerceInt(m["limit"], 100)
	params.DateStart = coerceInt64(m["date_start"], 0)
	params.DateEnd = coerceInt64(m["date_end"], 0)

	if params.Limit < 1 || params.Limit > 1000 {
		params.Limit = 100
	}

	// Build search params to fetch observations
	searchParams := search.SearchParams{
		Type:      "observations",
		Project:   params.Project,
		Limit:     params.Limit,
		OrderBy:   "date_desc",
		DateStart: params.DateStart,
		DateEnd:   params.DateEnd,
		ObsType:   params.ObsType,
	}

	result, err := s.searchMgr.UnifiedSearch(ctx, searchParams)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}

	// Fetch full observation data for export
	ids := make([]int64, 0, len(result.Results))
	for _, r := range result.Results {
		if r.Type == "observation" {
			ids = append(ids, r.ID)
		}
	}

	observations, err := s.observationStore.GetObservationsByIDs(ctx, ids, "", 0)
	if err != nil {
		return "", fmt.Errorf("get observations: %w", err)
	}

	// Format output based on requested format
	var output string
	switch params.Format {
	case "jsonl":
		// JSON Lines format - one JSON object per line
		var lines []string
		for _, obs := range observations {
			line, err := json.Marshal(obs)
			if err != nil {
				continue
			}
			lines = append(lines, string(line))
		}
		// Use proper JSON marshaling to avoid injection issues
		jsonlOutput := struct {
			Format string `json:"format"`
			Data   string `json:"data"`
			Count  int    `json:"count"`
		}{
			Format: "jsonl",
			Count:  len(observations),
			Data:   strings.Join(lines, "\n"),
		}
		outputBytes, err := json.Marshal(jsonlOutput)
		if err != nil {
			return "", fmt.Errorf("marshal jsonl output: %w", err)
		}
		output = string(outputBytes)

	case "markdown":
		// Markdown format for human reading
		var md strings.Builder
		md.WriteString("# Observations Export\n\n")
		md.WriteString(fmt.Sprintf("Total: %d observations\n\n", len(observations)))
		md.WriteString("---\n\n")

		for _, obs := range observations {
			title := ""
			if obs.Title.Valid {
				title = obs.Title.String
			}
			md.WriteString(fmt.Sprintf("## [%s] %s\n\n", obs.Type, title))
			if obs.Subtitle.Valid && obs.Subtitle.String != "" {
				md.WriteString(fmt.Sprintf("*%s*\n\n", obs.Subtitle.String))
			}
			if obs.Narrative.Valid && obs.Narrative.String != "" {
				md.WriteString(fmt.Sprintf("%s\n\n", obs.Narrative.String))
			}
			if len(obs.Facts) > 0 {
				md.WriteString("### Key Facts\n")
				for _, fact := range obs.Facts {
					md.WriteString(fmt.Sprintf("- %s\n", fact))
				}
				md.WriteString("\n")
			}
			if len(obs.Concepts) > 0 {
				md.WriteString(fmt.Sprintf("**Tags:** %s\n\n", strings.Join(obs.Concepts, ", ")))
			}
			md.WriteString(fmt.Sprintf("**ID:** %d | **Created:** %s | **Importance:** %.2f\n\n",
				obs.ID, obs.CreatedAt, obs.ImportanceScore))
			md.WriteString("---\n\n")
		}

		// Wrap markdown in JSON response
		response := map[string]any{
			"format": "markdown",
			"count":  len(observations),
			"data":   md.String(),
		}
		outputBytes, err := json.Marshal(response)
		if err != nil {
			return "", fmt.Errorf("marshal response: %w", err)
		}
		output = string(outputBytes)

	default: // json
		response := map[string]any{
			"format":       "json",
			"count":        len(observations),
			"observations": observations,
		}
		outputBytes, err := json.Marshal(response)
		if err != nil {
			return "", fmt.Errorf("marshal response: %w", err)
		}
		output = string(outputBytes)
	}

	return output, nil
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

	// Check database health
	dbHealth := &SubsystemHealth{
		Status:  "healthy",
		Metrics: make(map[string]any),
	}
	if s.observationStore != nil {
		// Count observations
		count, err := s.observationStore.GetObservationCount(ctx, "")
		if err != nil {
			dbHealth.Status = "unhealthy"
			dbHealth.Message = "Database query failed: " + err.Error()
			report.HealthScore -= 30
		} else {
			dbHealth.Metrics["total_observations"] = count
			dbHealth.Message = "Database operational"
		}

		// Check for recent activity
		recent, err := s.observationStore.GetAllRecentObservations(ctx, 1)
		if err == nil && len(recent) > 0 {
			dbHealth.Metrics["last_observation"] = recent[0].CreatedAt
			// Check epoch for staleness warning
			if recent[0].CreatedAtEpoch > 0 {
				lastActivityTime := time.UnixMilli(recent[0].CreatedAtEpoch)
				if time.Since(lastActivityTime) > 7*24*time.Hour {
					dbHealth.Warnings = append(dbHealth.Warnings, "No observations in the last 7 days")
				}
			}
		}
	} else {
		dbHealth.Status = "unhealthy"
		dbHealth.Message = "Observation store not initialized"
		report.HealthScore -= 50
	}
	report.Subsystems["database"] = dbHealth

	// Check vector store health
	vectorHealth := &SubsystemHealth{
		Status:  "healthy",
		Metrics: make(map[string]any),
	}
	if s.vectorClient != nil {
		stats, err := s.vectorClient.GetHealthStats(ctx)
		if err != nil {
			vectorHealth.Status = "degraded"
			vectorHealth.Message = "Could not get vector stats: " + err.Error()
			report.HealthScore -= 15
		} else {
			vectorHealth.Metrics["total_vectors"] = stats.TotalVectors
			vectorHealth.Metrics["stale_vectors"] = stats.StaleVectors
			vectorHealth.Metrics["current_model"] = stats.CurrentModel
			vectorHealth.Metrics["needs_rebuild"] = stats.NeedsRebuild

			if stats.NeedsRebuild {
				vectorHealth.Status = "degraded"
				vectorHealth.Warnings = append(vectorHealth.Warnings, "Vector rebuild recommended: "+stats.RebuildReason)
				report.Actions = append(report.Actions, "Run vector rebuild to update embeddings")
				report.HealthScore -= 10
			}

			// Check stale ratio
			if stats.TotalVectors > 0 {
				staleRatio := float64(stats.StaleVectors) / float64(stats.TotalVectors)
				if staleRatio > 0.2 {
					vectorHealth.Warnings = append(vectorHealth.Warnings,
						fmt.Sprintf("%.1f%% of vectors are stale", staleRatio*100))
					report.HealthScore -= 5
				}
			}
		}

		// Check cache performance
		cacheStats := s.vectorClient.GetCacheStats()
		vectorHealth.Metrics["cache_hit_rate"] = fmt.Sprintf("%.1f%%", cacheStats.HitRate())
		vectorHealth.Metrics["embedding_hits"] = cacheStats.EmbeddingHits
		vectorHealth.Metrics["embedding_misses"] = cacheStats.EmbeddingMisses
		vectorHealth.Metrics["result_hits"] = cacheStats.ResultHits
		vectorHealth.Metrics["result_misses"] = cacheStats.ResultMisses

		if cacheStats.HitRate() < 20 && (cacheStats.EmbeddingHits+cacheStats.EmbeddingMisses) > 100 {
			vectorHealth.Warnings = append(vectorHealth.Warnings, "Low cache hit rate - consider cache tuning")
		}
	} else {
		vectorHealth.Status = "unhealthy"
		vectorHealth.Message = "Vector client not initialized"
		report.HealthScore -= 30
	}
	report.Subsystems["vectors"] = vectorHealth

	// Check pattern detection health
	patternHealth := &SubsystemHealth{
		Status:  "healthy",
		Metrics: make(map[string]any),
	}
	if s.patternStore != nil {
		patterns, err := s.patternStore.GetActivePatterns(ctx, 100, 0, "")
		if err != nil {
			patternHealth.Status = "degraded"
			patternHealth.Message = "Could not query patterns: " + err.Error()
		} else {
			patternHealth.Metrics["total_patterns"] = len(patterns)

			// Count by type
			typeCounts := make(map[string]int)
			for _, p := range patterns {
				typeCounts[string(p.Type)]++
			}
			patternHealth.Metrics["patterns_by_type"] = typeCounts
		}
	}
	report.Subsystems["patterns"] = patternHealth

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
func (s *Server) handleAnalyzeSearchPatterns(ctx context.Context, args json.RawMessage) (string, error) {
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

	type QueryPattern struct {
		Query       string  `json:"query"`
		LastUsed    string  `json:"last_used"`
		Count       int     `json:"count"`
		AvgResults  float64 `json:"avg_results"`
		ZeroResults int     `json:"zero_result_count"`
	}

	type PatternAnalysis struct {
		Period            string         `json:"period"`
		TopQueries        []QueryPattern `json:"top_queries"`
		ZeroResultQueries []string       `json:"zero_result_queries,omitempty"`
		Insights          []string       `json:"insights,omitempty"`
		TotalSearches     int            `json:"total_searches"`
		UniqueQueries     int            `json:"unique_queries"`
	}

	analysis := &PatternAnalysis{
		Period:            fmt.Sprintf("Last %d days", params.Days),
		TopQueries:        []QueryPattern{},
		ZeroResultQueries: []string{},
		Insights:          []string{},
	}

	// Get search stats from the search manager if available
	if s.searchMgr != nil {
		metrics := s.searchMgr.Metrics()
		if metrics != nil {
			stats := metrics.GetStats()
			if totalSearches, ok := stats["total_searches"].(int); ok && totalSearches > 0 {
				analysis.TotalSearches = totalSearches
				analysis.Insights = append(analysis.Insights,
					fmt.Sprintf("Total searches: %d", totalSearches))
			}
			if avgLatency, ok := stats["avg_latency_ms"].(float64); ok {
				analysis.Insights = append(analysis.Insights,
					fmt.Sprintf("Average search latency: %.2fms", avgLatency))
			}
		}

		// Get cache stats
		cacheStats := s.searchMgr.CacheStats()
		if hitRate, ok := cacheStats["hit_rate"].(float64); ok {
			analysis.Insights = append(analysis.Insights,
				fmt.Sprintf("Cache hit rate: %.1f%%", hitRate*100))
		}
	}

	// Analyze observation patterns to suggest search improvements
	if s.observationStore != nil {
		// Get recent observations to understand content patterns
		observations, err := s.observationStore.GetAllRecentObservations(ctx, 100)
		if err == nil {
			analysis.UniqueQueries = len(observations)

			// Analyze observation types
			typeCounts := make(map[string]int)
			for _, obs := range observations {
				typeCounts[string(obs.Type)]++
			}

			// Find most common types
			mostCommon := ""
			maxCount := 0
			for t, c := range typeCounts {
				if c > maxCount {
					mostCommon = t
					maxCount = c
				}
			}
			if mostCommon != "" {
				analysis.Insights = append(analysis.Insights,
					fmt.Sprintf("Most common observation type: %s (%d occurrences)", mostCommon, maxCount))
			}

			// Check for concept coverage
			conceptCounts := make(map[string]int)
			for _, obs := range observations {
				for _, c := range obs.Concepts {
					conceptCounts[c]++
				}
			}
			if len(conceptCounts) > 0 {
				analysis.Insights = append(analysis.Insights,
					fmt.Sprintf("%d unique concepts across %d observations", len(conceptCounts), len(observations)))
			}
		}
	}

	// Add general recommendations
	if len(analysis.Insights) == 0 {
		analysis.Insights = append(analysis.Insights, "Insufficient data for pattern analysis")
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

// handleGetObservationScoringBreakdown returns detailed scoring breakdown for an observation.
func (s *Server) handleGetObservationScoringBreakdown(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		ID int64
	}
	params.ID = coerceInt64(m["id"], 0)

	if params.ID <= 0 {
		return "", fmt.Errorf("id is required and must be positive")
	}

	// Get the observation
	obs, err := s.observationStore.GetObservationByID(ctx, params.ID)
	if err != nil {
		return "", fmt.Errorf("get observation: %w", err)
	}
	if obs == nil {
		return "", fmt.Errorf("observation not found: %d", params.ID)
	}

	// Calculate scoring components
	if s.scoreCalculator == nil {
		return "", fmt.Errorf("score calculator not initialized")
	}

	components := s.scoreCalculator.CalculateComponents(obs, time.Now())

	// Build response with observation context
	response := map[string]any{
		"observation": map[string]any{
			"id":         obs.ID,
			"title":      obs.Title.String,
			"type":       string(obs.Type),
			"project":    obs.Project,
			"created_at": obs.CreatedAtEpoch,
		},
		"scoring": map[string]any{
			"final_score":       components.FinalScore,
			"type_weight":       components.TypeWeight,
			"recency_decay":     components.RecencyDecay,
			"core_score":        components.CoreScore,
			"feedback_contrib":  components.FeedbackContrib,
			"concept_contrib":   components.ConceptContrib,
			"retrieval_contrib": components.RetrievalContrib,
			"age_days":          components.AgeDays,
		},
		"explanation": map[string]any{
			"type_impact":      fmt.Sprintf("Observation type '%s' has weight %.2f", obs.Type, components.TypeWeight),
			"recency_impact":   fmt.Sprintf("%.1f days old, decay factor %.2f", components.AgeDays, components.RecencyDecay),
			"feedback_impact":  fmt.Sprintf("User feedback contributes %.2f to score", components.FeedbackContrib),
			"concept_impact":   fmt.Sprintf("Concept tags contribute %.2f to score", components.ConceptContrib),
			"retrieval_impact": fmt.Sprintf("Retrieval frequency contributes %.2f to score", components.RetrievalContrib),
		},
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}
	return string(output), nil
}

// handleAnalyzeObservationImportance returns importance analysis for a project's observations.
func (s *Server) handleAnalyzeObservationImportance(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		IncludeTopScored      *bool
		IncludeMostRetrieved  *bool
		IncludeConceptWeights *bool
		Project               string
		Limit                 int
	}
	params.Project = coerceString(m["project"], "")
	params.Limit = coerceInt(m["limit"], 0)
	if v, ok := m["include_top_scored"]; ok && v != nil {
		b := coerceBool(v, true)
		params.IncludeTopScored = &b
	}
	if v, ok := m["include_most_retrieved"]; ok && v != nil {
		b := coerceBool(v, true)
		params.IncludeMostRetrieved = &b
	}
	if v, ok := m["include_concept_weights"]; ok && v != nil {
		b := coerceBool(v, true)
		params.IncludeConceptWeights = &b
	}

	// Set defaults
	if params.Limit <= 0 {
		params.Limit = 10
	}
	if params.Limit > 50 {
		params.Limit = 50
	}
	includeTopScored := params.IncludeTopScored == nil || *params.IncludeTopScored
	includeMostRetrieved := params.IncludeMostRetrieved == nil || *params.IncludeMostRetrieved
	includeConceptWeights := params.IncludeConceptWeights == nil || *params.IncludeConceptWeights

	response := make(map[string]any)
	response["project"] = params.Project
	if params.Project == "" {
		response["project"] = "(all projects)"
	}

	// Get feedback statistics
	stats, err := s.observationStore.GetObservationFeedbackStats(ctx, params.Project)
	if err != nil {
		return "", fmt.Errorf("get feedback stats: %w", err)
	}
	response["feedback_stats"] = stats

	// Get top-scoring observations
	if includeTopScored {
		topScored, err := s.observationStore.GetTopScoringObservations(ctx, params.Project, params.Limit)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to get top-scoring observations")
		} else {
			topScoredSummary := make([]map[string]any, 0, len(topScored))
			for _, obs := range topScored {
				topScoredSummary = append(topScoredSummary, map[string]any{
					"id":               obs.ID,
					"title":            obs.Title.String,
					"type":             string(obs.Type),
					"importance_score": obs.ImportanceScore,
				})
			}
			response["top_scoring_observations"] = topScoredSummary
		}
	}

	// Get most-retrieved observations
	if includeMostRetrieved {
		mostRetrieved, err := s.observationStore.GetMostRetrievedObservations(ctx, params.Project, params.Limit)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to get most-retrieved observations")
		} else {
			mostRetrievedSummary := make([]map[string]any, 0, len(mostRetrieved))
			for _, obs := range mostRetrieved {
				mostRetrievedSummary = append(mostRetrievedSummary, map[string]any{
					"id":              obs.ID,
					"title":           obs.Title.String,
					"type":            string(obs.Type),
					"retrieval_count": obs.RetrievalCount,
				})
			}
			response["most_retrieved_observations"] = mostRetrievedSummary
		}
	}

	// Get concept weights
	if includeConceptWeights {
		conceptWeights, err := s.observationStore.GetConceptWeights(ctx)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to get concept weights")
		} else if len(conceptWeights) > 0 {
			response["concept_weights"] = conceptWeights
		}
	}

	// Generate insights
	insights := []string{}
	if stats != nil {
		if stats.Positive > 0 {
			insights = append(insights, fmt.Sprintf("%d observations marked as valuable (positive feedback)", stats.Positive))
		}
		if stats.Negative > 0 {
			insights = append(insights, fmt.Sprintf("%d observations marked as not helpful (negative feedback)", stats.Negative))
		}
		if stats.AvgScore > 0 {
			insights = append(insights, fmt.Sprintf("Average importance score: %.2f", stats.AvgScore))
		}
		if stats.AvgRetrieval > 0 {
			insights = append(insights, fmt.Sprintf("Average retrieval count: %.1f", stats.AvgRetrieval))
		}
	}
	if len(insights) > 0 {
		response["insights"] = insights
	}

	output, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}
	return string(output), nil
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

func (s *Server) handleRunConsolidation(ctx context.Context, args json.RawMessage) (string, error) {
	if s.consolidationScheduler == nil {
		return "", fmt.Errorf("consolidation scheduler not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		Cycle string
	}
	params.Cycle = coerceString(m["cycle"], "all")
	if params.Cycle == "" {
		params.Cycle = "all"
	}

	switch params.Cycle {
	case "all":
		err = s.consolidationScheduler.RunAll(ctx)
	case "decay":
		err = s.consolidationScheduler.RunDecay(ctx)
	case "associations":
		err = s.consolidationScheduler.RunAssociations(ctx)
	case "forgetting":
		err = s.consolidationScheduler.RunForgetting(ctx)
	default:
		return "", fmt.Errorf("unknown cycle: %s (use 'all', 'decay', 'associations', or 'forgetting')", params.Cycle)
	}

	if err != nil {
		return "", fmt.Errorf("consolidation %s cycle failed: %w", params.Cycle, err)
	}

	result := map[string]any{
		"status": "completed",
		"cycle":  params.Cycle,
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	return string(b), nil
}

// handleGetGraphNeighbors returns graph neighbors via FalkorDB.
func (s *Server) handleGetGraphNeighbors(ctx context.Context, args json.RawMessage) (string, error) {
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		ObservationID int64
		MaxHops       int
		Limit         int
	}
	params.ObservationID = coerceInt64(m["observation_id"], 0)
	params.MaxHops = coerceInt(m["max_hops"], 0)
	params.Limit = coerceInt(m["limit"], 0)
	if params.ObservationID <= 0 {
		return "", fmt.Errorf("observation_id is required")
	}
	if params.MaxHops <= 0 {
		params.MaxHops = 2
	}
	if params.Limit <= 0 {
		params.Limit = 20
	}

	if s.graphStore == nil {
		result := map[string]any{"error": "graph backend not configured"}
		b, _ := json.MarshalIndent(result, "", "  ")
		return string(b), nil
	}

	if err := s.graphStore.Ping(ctx); err != nil {
		result := map[string]any{"error": "graph backend not connected", "details": err.Error()}
		b, _ := json.MarshalIndent(result, "", "  ")
		return string(b), nil
	}

	neighbors, err := s.graphStore.GetNeighbors(ctx, params.ObservationID, params.MaxHops, params.Limit)
	if err != nil {
		return "", fmt.Errorf("get graph neighbors: %w", err)
	}

	// Enrich with observation details.
	type NeighborInfo struct {
		ID           int64  `json:"id"`
		Title        string `json:"title"`
		Type         string `json:"type"`
		Project      string `json:"project"`
		RelationType string `json:"relation_type"`
		Hops         int    `json:"hops"`
	}

	result := make([]NeighborInfo, 0, len(neighbors))
	for _, n := range neighbors {
		info := NeighborInfo{
			ID:           n.ObsID,
			RelationType: string(n.RelationType),
			Hops:         n.Hops,
		}
		// Try to enrich with observation details.
		obs, err := s.observationStore.GetObservationByID(ctx, n.ObsID)
		if err == nil && obs != nil {
			info.Title = obs.Title.String
			info.Type = string(obs.Type)
			info.Project = obs.Project
		}
		result = append(result, info)
	}

	response := map[string]any{
		"observation_id": params.ObservationID,
		"max_hops":       params.MaxHops,
		"neighbors":      result,
		"count":          len(result),
	}
	b, _ := json.MarshalIndent(response, "", "  ")
	return string(b), nil
}

// handleGetGraphStats returns graph backend statistics.
func (s *Server) handleGetGraphStats(ctx context.Context) (string, error) {
	if s.graphStore == nil {
		result := map[string]any{
			"provider":  "none",
			"connected": false,
		}
		b, _ := json.MarshalIndent(result, "", "  ")
		return string(b), nil
	}

	stats, err := s.graphStore.Stats(ctx)
	if err != nil {
		result := map[string]any{
			"provider":  stats.Provider,
			"connected": false,
			"error":     err.Error(),
		}
		b, _ := json.MarshalIndent(result, "", "  ")
		return string(b), nil
	}

	result := map[string]any{
		"provider":   stats.Provider,
		"connected":  stats.Connected,
		"node_count": stats.NodeCount,
		"edge_count": stats.EdgeCount,
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	return string(b), nil
}

// handleFindByFileContext retrieves observations directly associated with a specific file path,
// ordered by importance score DESC. Uses the JSONB file-index for precise, fast lookups.
func (s *Server) handleFindByFileContext(ctx context.Context, args json.RawMessage) (string, error) {
	if s.observationStore == nil {
		return "", fmt.Errorf("observation store not available")
	}

	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	var params struct {
		FilePath string
		Limit    int
	}
	params.FilePath = coerceString(m["file_path"], "")
	params.Limit = coerceInt(m["limit"], 10)

	if params.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}
	if params.Limit < 1 || params.Limit > 100 {
		params.Limit = 10
	}

	observations, err := s.observationStore.GetObservationsByFile(ctx, params.FilePath, params.Limit)
	if err != nil {
		return "", fmt.Errorf("get observations by file: %w", err)
	}

	type fileContextResult struct {
		FilePath     string               `json:"file_path"`
		Count        int                  `json:"count"`
		Observations []*models.Observation `json:"observations"`
	}

	out := fileContextResult{
		FilePath:     params.FilePath,
		Count:        len(observations),
		Observations: observations,
	}
	if out.Observations == nil {
		out.Observations = []*models.Observation{}
	}

	output, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}

	return string(output), nil
}
