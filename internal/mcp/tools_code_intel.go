// Package mcp — code intelligence MCP tools (CR-006).
//
// Exposes the existing codebase_search and codebase_status tool names when
// ENGRAM_CODE_INTEL_ENABLED=true. Legacy callers retain their raw-project route
// only until a client-scoped UCI application has been selected. UCI callers are
// resolved through an opaque client-local context handle or their current binding.

// Flag contract: when ENGRAM_CODE_INTEL_ENABLED != "true", none of these tools
// appear in tools/list and any tools/call for them returns "unknown tool".
// The ListTools output MUST be byte-identical to the pre-CR-006 surface when
// the flag is off.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	gorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/retrieval"
	"github.com/thebtf/engram/internal/uci"
)

// codeIntelEnabled reports whether the code intelligence feature is enabled.
// All three codebase_* tools are gated behind this flag. String equality with
// "true" matches the convention used by vnextFEnabled and other flag checks in
// this package.
func codeIntelEnabled() bool {
	return os.Getenv("ENGRAM_CODE_INTEL_ENABLED") == "true"
}

const (
	codebaseSearchDefaultLimit = 10
	codebaseSearchMaxLimit     = 50
)

// codebaseIntelligenceApplication is an optional capability of the existing
// client-scoped context application. Keeping it separate preserves the context
// selection seam for callers that do not provide code-intelligence runtime.
type codebaseIntelligenceApplication interface {
	ResolveLegacyProject(context.Context, uci.AuthorizedContext, string) (uci.AliasTarget, error)
	SearchCodebase(context.Context, uci.AuthorizedContext, codebaseSearchInput) (uci.QueryResponse, error)
	CodebaseStatus(context.Context, uci.AuthorizedContext) (codebaseStatusSnapshot, error)
}

type codebaseSearchInput struct {
	Query      string
	PathPrefix string
	Limit      int
}

type codebaseEvidenceRecorderHealth struct {
	State           string `json:"state"`
	LastFailureCode string `json:"last_failure_code"`
}

type codebaseStatusSnapshot struct {
	TotalChunks      int64
	EmbeddedChunks   int64
	EvidenceRecorder codebaseEvidenceRecorderHealth
}

// SetCodeChunkStore wires the code chunk store into the MCP server.
// Must be called when ENGRAM_CODE_INTEL_ENABLED=true to enable codebase_search
// and codebase_status. When nil, the server still starts; those two tools are
// simply absent from tools/list (guarded by the nil check below).
func (s *Server) SetCodeChunkStore(cs *gorm.CodeChunkStore) {
	s.codeChunkStore = cs
}

// codebaseSearchTool returns the codebase_search tool definition.
// Advertised only when codeIntelEnabled() && s.codeChunkStore != nil.
func codebaseSearchTool() Tool {
	return Tool{
		Name:        "codebase_search",
		Description: "Search the indexed codebase. UCI calls resolve the caller's opaque context handle or current binding before search; legacy project is compatibility evidence only, never selection authority.",
		tier:        tierCore,
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"query"},
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Natural language or keyword search query",
				},
				"path_prefix": map[string]any{
					"type":        "string",
					"description": "Optional relative path prefix for the UCI search runtime",
				},
				"limit": map[string]any{
					"type":        "number",
					"description": "Maximum results to return (default 10, max 50)",
					"default":     codebaseSearchDefaultLimit,
					"minimum":     1,
					"maximum":     codebaseSearchMaxLimit,
				},
				"context_handle": map[string]any{
					"type":        "string",
					"description": "Optional opaque client-local context handle; omitted uses this client's current UCI binding",
				},
				"project": map[string]any{
					"type":        "string",
					"description": "Optional legacy compatibility evidence only; it never selects a UCI context",
				},
			},
		},
	}
}

// codebaseStatusTool returns the codebase_status tool definition.
// Advertised only when codeIntelEnabled() && s.codeChunkStore != nil.
func codebaseStatusTool() Tool {
	return Tool{
		Name:        "codebase_status",
		Description: "Report code index status for an authorized UCI context. Legacy project is compatibility evidence only, never selection authority.",
		tier:        tierUseful,
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"context_handle": map[string]any{
					"type":        "string",
					"description": "Optional opaque client-local context handle; omitted uses this client's current UCI binding",
				},
				"project": map[string]any{
					"type":        "string",
					"description": "Optional legacy compatibility evidence only; it never selects a UCI context",
				},
			},
		},
	}
}

// handleCodebaseSearch dispatches either the client-scoped UCI route or the
// explicitly retained raw-project legacy route.
func (s *Server) handleCodebaseSearch(ctx context.Context, args json.RawMessage) (string, error) {
	if !codeIntelEnabled() {
		return "", fmt.Errorf("codebase_search requires ENGRAM_CODE_INTEL_ENABLED=true")
	}
	if s.hasCodebaseContextApplication() || codebaseArgsSelectUCI(args) {
		return s.handleUCICodebaseSearch(ctx, args)
	}
	return s.handleLegacyCodebaseSearch(ctx, args)
}

// handleLegacyCodebaseSearch preserves the pre-UCI raw-project behavior for
// callers that have not selected a UCI context.
func (s *Server) handleLegacyCodebaseSearch(ctx context.Context, args json.RawMessage) (string, error) {
	if !codeIntelEnabled() {
		return "", fmt.Errorf("codebase_search requires ENGRAM_CODE_INTEL_ENABLED=true")
	}
	if s.codeChunkStore == nil {
		return "", fmt.Errorf("codebase_search: code chunk store not wired")
	}

	var params struct {
		Query   string `json:"query"`
		Limit   int    `json:"limit"`
		Project string `json:"project"`
	}
	if args != nil {
		if err := json.Unmarshal(args, &params); err != nil {
			return "", fmt.Errorf("codebase_search: invalid args: %w", err)
		}
	}
	if params.Query == "" {
		return "", fmt.Errorf("codebase_search: query is required")
	}
	if params.Limit <= 0 {
		params.Limit = 10
	}
	if params.Limit > 50 {
		params.Limit = 50
	}

	// Project ID resolution: use the explicit arg when provided, otherwise derive
	// from the context. For server-side tools the project context is not available
	// the same way as in the daemon, so we require the caller to supply it when
	// the tool is used from a direct gRPC/HTTP path. In V1 we accept the
	// project field as the authoritative project ID (same convention as the
	// store/recall tools that accept a "project" param).
	projectID := params.Project
	if projectID == "" {
		return "", fmt.Errorf("codebase_search: project ID is required (supply via 'project' param)")
	}

	// V1: FTS-only mode. QueryVec is empty, which causes CodeHybridSearch to
	// skip the vector leg and run FTS only. This is explicitly documented as
	// acceptable for V1 — see file header note.
	opts := retrieval.CodeHybridOptions{
		QueryVec: nil, // FTS-only for V1
	}

	hits, err := retrieval.CodeHybridSearch(ctx, projectID, params.Query, params.Limit, s.codeChunkStore, opts)
	if err != nil {
		return "", fmt.Errorf("codebase_search: %w", err)
	}

	// Format results as a JSON array. Each element carries the fields a
	// SocratiCode-compatible client expects: file_path, byte_start, byte_end,
	// language, content, score.
	type hitResult struct {
		ID        int64   `json:"id"`
		FilePath  string  `json:"file_path"`
		ByteStart int     `json:"byte_start"`
		ByteEnd   int     `json:"byte_end"`
		Language  string  `json:"language"`
		Content   string  `json:"content"`
		Score     float64 `json:"score"`
	}

	results := make([]hitResult, len(hits))
	for i, h := range hits {
		results[i] = hitResult{
			ID:        h.ID,
			FilePath:  h.FilePath,
			ByteStart: h.ByteStart,
			ByteEnd:   h.ByteEnd,
			Language:  h.Language,
			Content:   h.Content,
			Score:     h.Score,
		}
	}

	out, err := json.Marshal(map[string]any{
		"results": results,
		"count":   len(results),
		"query":   params.Query,
		"project": projectID,
	})
	if err != nil {
		return "", fmt.Errorf("codebase_search: marshal results: %w", err)
	}
	return string(out), nil
}

// handleCodebaseStatus dispatches either the client-scoped UCI route or the
// explicitly retained raw-project legacy route.
func (s *Server) handleCodebaseStatus(ctx context.Context, args json.RawMessage) (string, error) {
	if !codeIntelEnabled() {
		return "", fmt.Errorf("codebase_status requires ENGRAM_CODE_INTEL_ENABLED=true")
	}
	if s.hasCodebaseContextApplication() || codebaseArgsSelectUCI(args) {
		return s.handleUCICodebaseStatus(ctx, args)
	}
	return s.handleLegacyCodebaseStatus(ctx, args)
}

// handleLegacyCodebaseStatus preserves the pre-UCI raw-project behavior for
// callers that have not selected a UCI context.
func (s *Server) handleLegacyCodebaseStatus(ctx context.Context, args json.RawMessage) (string, error) {
	if !codeIntelEnabled() {
		return "", fmt.Errorf("codebase_status requires ENGRAM_CODE_INTEL_ENABLED=true")
	}
	if s.codeChunkStore == nil {
		return "", fmt.Errorf("codebase_status: code chunk store not wired")
	}

	var params struct {
		Project string `json:"project"`
	}
	if args != nil {
		_ = json.Unmarshal(args, &params)
	}
	projectID := params.Project
	if projectID == "" {
		return "", fmt.Errorf("codebase_status: project ID is required (supply via 'project' param)")
	}

	total, err := s.codeChunkStore.CountByProject(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("codebase_status count_total: %w", err)
	}
	embedded, err := s.codeChunkStore.CountEmbeddedByProject(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("codebase_status count_embedded: %w", err)
	}
	lastAt, hasAt, err := s.codeChunkStore.MaxUpdatedAtByProject(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("codebase_status max_updated_at: %w", err)
	}

	result := map[string]any{
		"project":         projectID,
		"total_chunks":    total,
		"embedded_chunks": embedded,
	}
	if hasAt {
		result["last_indexed_at"] = lastAt.Format(time.RFC3339)
	}

	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("codebase_status: marshal: %w", err)
	}
	return string(out), nil
}

type codebaseSearchArgs struct {
	ContextHandle    *string `json:"context_handle"`
	Query            *string `json:"query"`
	PathPrefix       *string `json:"path_prefix"`
	Limit            *int    `json:"limit"`
	Project          *string `json:"project"`
	hasContextHandle bool
}

type codebaseStatusArgs struct {
	ContextHandle    *string `json:"context_handle"`
	Project          *string `json:"project"`
	hasContextHandle bool
}

type codebaseStatusResponse struct {
	Context          uci.QueryContextRef            `json:"context"`
	TotalChunks      int64                          `json:"total_chunks"`
	EmbeddedChunks   int64                          `json:"embedded_chunks"`
	EvidenceRecorder codebaseEvidenceRecorderHealth `json:"evidence_recorder"`
}

func codebaseArgsSelectUCI(raw json.RawMessage) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return false
	}
	_, selected := fields["context_handle"]
	return selected
}

func decodeCodebaseSearchArgs(raw json.RawMessage) (codebaseSearchArgs, error) {
	var args codebaseSearchArgs
	fields, err := decodeStrictCodebaseArgs(raw, &args)
	if err != nil {
		return codebaseSearchArgs{}, err
	}
	_, args.hasContextHandle = fields["context_handle"]
	if args.Query == nil || *args.Query == "" {
		return codebaseSearchArgs{}, errors.New("query is required")
	}
	if args.hasContextHandle && (args.ContextHandle == nil || !validCodebaseContextHandle(*args.ContextHandle)) {
		return codebaseSearchArgs{}, errors.New("invalid context handle")
	}
	if args.Limit != nil && (*args.Limit < 1 || *args.Limit > codebaseSearchMaxLimit) {
		return codebaseSearchArgs{}, fmt.Errorf("limit must be between 1 and %d", codebaseSearchMaxLimit)
	}
	return args, nil
}

func decodeCodebaseStatusArgs(raw json.RawMessage) (codebaseStatusArgs, error) {
	var args codebaseStatusArgs
	fields, err := decodeStrictCodebaseArgs(raw, &args)
	if err != nil {
		return codebaseStatusArgs{}, err
	}
	_, args.hasContextHandle = fields["context_handle"]
	if args.hasContextHandle && (args.ContextHandle == nil || !validCodebaseContextHandle(*args.ContextHandle)) {
		return codebaseStatusArgs{}, errors.New("invalid context handle")
	}
	return args, nil
}

func decodeStrictCodebaseArgs(raw json.RawMessage, target any) (map[string]json.RawMessage, error) {
	input := bytes.TrimSpace(raw)
	if len(input) == 0 {
		input = []byte("{}")
	}
	if input[0] != '{' {
		return nil, errors.New("arguments must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("multiple JSON values")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func (args codebaseSearchArgs) searchInput() codebaseSearchInput {
	input := codebaseSearchInput{Query: *args.Query, Limit: codebaseSearchDefaultLimit}
	if args.PathPrefix != nil {
		input.PathPrefix = *args.PathPrefix
	}
	if args.Limit != nil {
		input.Limit = *args.Limit
	}
	return input
}

func (s *Server) handleUCICodebaseSearch(ctx context.Context, raw json.RawMessage) (string, error) {
	args, err := decodeCodebaseSearchArgs(raw)
	if err != nil {
		return "", fmt.Errorf("codebase_search: invalid args: %w", err)
	}
	application, authorized, epoch, contextCode := s.resolveCodebaseIntelligenceContext(ctx, args.ContextHandle)
	if contextCode != "" {
		return codebaseSearchContextRefusal(contextCode)
	}
	if contextCode = resolveCodebaseCompatibilityEvidence(ctx, application, authorized, args.Project); contextCode != "" {
		return codebaseSearchContextRefusal(contextCode)
	}
	if !s.codebaseContextEpochCurrent(epoch) {
		return codebaseSearchContextRefusal(uci.ContextMismatch)
	}

	response, err := application.SearchCodebase(ctx, authorized, args.searchInput())
	if err != nil {
		if code, isContextFailure := codebaseContextErrorCode(err); isContextFailure {
			return codebaseSearchContextRefusal(code)
		}
		return "", errors.New("codebase_search: UCI application unavailable")
	}
	if !s.codebaseContextEpochCurrent(epoch) || !codebaseQueryResponseMatchesContext(response, authorized) {
		return codebaseSearchContextRefusal(uci.ContextMismatch)
	}
	return marshalValidatedCodebaseQueryResponse(response)
}

func (s *Server) handleUCICodebaseStatus(ctx context.Context, raw json.RawMessage) (string, error) {
	args, err := decodeCodebaseStatusArgs(raw)
	if err != nil {
		return "", fmt.Errorf("codebase_status: invalid args: %w", err)
	}
	application, authorized, epoch, contextCode := s.resolveCodebaseIntelligenceContext(ctx, args.ContextHandle)
	if contextCode != "" {
		return "", codebaseContextClosedError(contextCode)
	}
	if contextCode = resolveCodebaseCompatibilityEvidence(ctx, application, authorized, args.Project); contextCode != "" {
		return "", codebaseContextClosedError(contextCode)
	}
	if !s.codebaseContextEpochCurrent(epoch) {
		return "", codebaseContextClosedError(uci.ContextMismatch)
	}

	snapshot, err := application.CodebaseStatus(ctx, authorized)
	if err != nil {
		if code, isContextFailure := codebaseContextErrorCode(err); isContextFailure {
			return "", codebaseContextClosedError(code)
		}
		return "", errors.New("codebase_status: UCI application unavailable")
	}
	if !s.codebaseContextEpochCurrent(epoch) {
		return "", codebaseContextClosedError(uci.ContextMismatch)
	}

	encoded, err := json.Marshal(codebaseStatusResponse{
		Context:          codebaseQueryContextRef(authorized.Ref()),
		TotalChunks:      snapshot.TotalChunks,
		EmbeddedChunks:   snapshot.EmbeddedChunks,
		EvidenceRecorder: snapshot.EvidenceRecorder,
	})
	if err != nil {
		return "", errors.New("codebase_status: marshal UCI response")
	}
	return string(encoded), nil
}

func (s *Server) resolveCodebaseIntelligenceContext(ctx context.Context, contextHandle *string) (codebaseIntelligenceApplication, uci.AuthorizedContext, uint64, uci.ContextErrorCode) {
	input, err := codebaseContextCallerInput(ctx)
	if err != nil {
		return nil, uci.AuthorizedContext{}, 0, uci.ContextMismatch
	}

	var (
		application codebaseContextApplication
		epoch       uint64
		expected    *uci.ContextRef
	)
	if contextHandle != nil {
		var ref uci.ContextRef
		var found bool
		application, epoch, ref, found = s.codebaseContextRefForHandle(input.ClientSessionID, *contextHandle)
		if !found {
			return nil, uci.AuthorizedContext{}, 0, uci.ContextMismatch
		}
		expected = &ref
		input.Ref = expected
	} else {
		var found bool
		application, epoch, found = s.codebaseContextApplicationSnapshot()
		if !found {
			return nil, uci.AuthorizedContext{}, 0, uci.ContextRequired
		}
	}

	authorized, err := application.Resolve(ctx, input)
	if err != nil {
		return nil, uci.AuthorizedContext{}, 0, codebaseContextFailureCode(err)
	}
	if !s.codebaseContextEpochCurrent(epoch) {
		return nil, uci.AuthorizedContext{}, 0, uci.ContextMismatch
	}
	if expected != nil && codebaseContextKey(authorized.Ref()) != codebaseContextKey(*expected) {
		return nil, uci.AuthorizedContext{}, 0, uci.ContextMismatch
	}
	intelligence, ok := application.(codebaseIntelligenceApplication)
	if !ok {
		return nil, uci.AuthorizedContext{}, 0, uci.ContextRequired
	}
	return intelligence, authorized, epoch, ""
}

func resolveCodebaseCompatibilityEvidence(ctx context.Context, application codebaseIntelligenceApplication, authorized uci.AuthorizedContext, project *string) uci.ContextErrorCode {
	if project == nil {
		return ""
	}
	if *project == "" {
		return uci.ContextMismatch
	}
	target, err := application.ResolveLegacyProject(ctx, authorized, *project)
	if err != nil {
		return codebaseContextFailureCode(err)
	}
	if !codebaseAliasMatchesAuthorizedContext(target, authorized.Ref()) {
		return uci.ContextMismatch
	}
	return ""
}

func codebaseAliasMatchesAuthorizedContext(target uci.AliasTarget, ref uci.ContextRef) bool {
	if target.SourceID == nil || *target.SourceID != ref.SourceID {
		return false
	}
	if target.SpaceID == nil {
		return true
	}
	return ref.SpaceID != nil && *target.SpaceID == *ref.SpaceID
}

func codebaseContextFailureCode(err error) uci.ContextErrorCode {
	if code, ok := codebaseContextErrorCode(err); ok {
		return code
	}
	return uci.ContextMismatch
}

func codebaseContextErrorCode(err error) (uci.ContextErrorCode, bool) {
	var contextErr *uci.ContextError
	if !errors.As(err, &contextErr) {
		return "", false
	}
	switch contextErr.Code() {
	case uci.ContextRequired, uci.ContextMismatch, uci.PermissionDenied:
		return contextErr.Code(), true
	default:
		return "", false
	}
}

func codebaseSearchContextRefusal(code uci.ContextErrorCode) (string, error) {
	status := uci.QueryStatusContextRequired
	errorCode := uci.QueryErrorContextMismatch
	switch code {
	case uci.ContextRequired:
		errorCode = uci.QueryErrorContextRequired
	case uci.PermissionDenied:
		status = uci.QueryStatusForbidden
		errorCode = uci.QueryErrorPermissionDenied
	case uci.ContextMismatch:
		// The default remains the closed mismatch envelope.
	default:
		errorCode = uci.QueryErrorContextMismatch
	}
	return marshalValidatedCodebaseQueryResponse(uci.QueryResponse{
		Schema: uci.QueryResponseSchema,
		Status: status,
		Error:  &uci.QueryError{Code: errorCode},
	})
}

func marshalValidatedCodebaseQueryResponse(response uci.QueryResponse) (string, error) {
	if err := response.Validate(); err != nil {
		return "", errors.New("codebase_search: invalid UCI response")
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", errors.New("codebase_search: marshal UCI response")
	}
	return string(encoded), nil
}

func codebaseQueryResponseMatchesContext(response uci.QueryResponse, authorized uci.AuthorizedContext) bool {
	if response.Status == uci.QueryStatusContextRequired || response.Status == uci.QueryStatusForbidden {
		return true
	}
	if response.Status == uci.QueryStatusUnavailable && response.Contexts == nil {
		return true
	}
	if response.Contexts == nil || len(*response.Contexts) != 1 {
		return false
	}
	return codebaseQueryContextMatchesRef((*response.Contexts)[0], authorized.Ref())
}

func codebaseQueryContextMatchesRef(context uci.QueryContextRef, ref uci.ContextRef) bool {
	if context.SpaceID == nil || ref.SpaceID == nil {
		if context.SpaceID != ref.SpaceID {
			return false
		}
	} else if *context.SpaceID != *ref.SpaceID {
		return false
	}
	return context.SourceID == ref.SourceID &&
		context.CheckoutID == ref.CheckoutID &&
		context.ViewID == ref.ViewID &&
		context.ProfileID == ref.AnalysisProfileID &&
		context.Generation == ref.Generation
}

func codebaseQueryContextRef(ref uci.ContextRef) uci.QueryContextRef {
	return uci.QueryContextRef{
		SpaceID:    copyCodebaseContextSpaceID(ref.SpaceID),
		SourceID:   ref.SourceID,
		CheckoutID: ref.CheckoutID,
		ViewID:     ref.ViewID,
		Generation: ref.Generation,
		ProfileID:  ref.AnalysisProfileID,
	}
}
