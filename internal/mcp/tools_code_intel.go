// Package mcp — code intelligence MCP tools (CR-006).
//
// codebase_search and codebase_status are current UCI-only entry points.
// They resolve an opaque client-local context handle or current binding before
// invoking the UCI application; project is compatibility evidence only.
//
// The internal legacy-unscoped handlers below remain rollback-only code. They
// are not MCP tool definitions or Server ToolCall endpoints; their JSON stays
// visibly labeled legacy_unscoped for direct rollback tests and future T081.
//
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
	"unicode/utf8"

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
	codebaseSearchDefaultLimit                = 10
	codebaseSearchMaxLimit                    = 50
	codebaseAfterBarrierMaxTokenLength        = 2_048
	codebaseAfterBarrierMaxWaitMS       int64 = 60_000
	codebaseStatusStabilizationAttempts       = 2
	legacyUnscopedCodebaseRetrievalMode       = "legacy_unscoped"
)

// CodebaseIntelligenceApplication is an optional capability of the existing
// client-scoped context application. Keeping it separate preserves the context
// selection seam for callers that do not provide code-intelligence runtime.
type CodebaseIntelligenceApplication interface {
	ResolveLegacyProject(context.Context, uci.AuthorizedContext, string) (uci.AliasTarget, error)
	SearchCodebase(context.Context, uci.AuthorizedContext, CodebaseSearchInput) (uci.QueryResponse, error)
	CodebaseStatus(context.Context, uci.AuthorizedContext) (CodebaseStatusSnapshot, error)
}

type CodebaseFreshnessApplication interface {
	CodebaseFreshness(context.Context, uci.AuthorizedContext, string) (uci.QueryFreshness, error)
}

type CodebaseSearchInput struct {
	Query      string
	PathPrefix string
	Limit      int
}

type CodebaseEvidenceRecorderHealth struct {
	State           string `json:"state"`
	LastFailureCode string `json:"last_failure_code"`
}

type CodebaseStatusSnapshot struct {
	TotalChunks      int64
	EmbeddedChunks   int64
	Embedding        uci.EmbeddingStatus
	Freshness        *uci.QueryFreshness
	EvidenceRecorder CodebaseEvidenceRecorderHealth
}

// SetLegacyUnscopedCodeChunkStore wires the explicit raw-project rollback reader.
// UCI remains preferred whenever a scoped code intelligence application exists.
func (s *Server) SetLegacyUnscopedCodeChunkStore(cs *gorm.CodeChunkStore) {
	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	s.legacyUnscopedCodeChunkStore = cs
}

// codebaseSearchTool defines the public codebase_search schema.
func codebaseSearchTool() Tool {
	return Tool{
		Name:        "codebase_search",
		Description: "Search the codebase within an authorized UCI context. The optional project is compatibility evidence only and never selection authority.",
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
					"description": "Optional relative path prefix that filters only the already-authorized UCI View",
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
				"after_barrier": codebaseAfterBarrierSchema(),
			},
		},
	}
}

// codebaseStatusTool defines the direct codebase_status schema.
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
				"after_barrier": codebaseAfterBarrierSchema(),
			},
		},
	}
}

func codebaseAfterBarrierSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"token", "wait_ms"},
		"properties": map[string]any{
			"token": map[string]any{
				"type":        "string",
				"description": "Opaque server-issued read-your-save barrier token",
				"minLength":   1,
				"maxLength":   codebaseAfterBarrierMaxTokenLength,
			},
			"wait_ms": map[string]any{
				"type":        "integer",
				"description": "Maximum barrier wait in milliseconds",
				"minimum":     1,
				"maximum":     codebaseAfterBarrierMaxWaitMS,
			},
		},
	}
}

// handleCodebaseSearch prefers scoped UCI and selects raw-project retrieval only
// for the explicit legacy-only rollback state.
func (s *Server) handleCodebaseSearch(ctx context.Context, args json.RawMessage) (string, error) {
	if !codeIntelEnabled() {
		return "", fmt.Errorf("codebase_search requires ENGRAM_CODE_INTEL_ENABLED=true")
	}
	if s.hasCodebaseIntelligenceApplication() {
		return s.handleUCICodebaseSearch(ctx, args)
	}
	if s.hasLegacyUnscopedCodeChunkStore() {
		return s.handleLegacyUnscopedCodebaseSearch(ctx, args)
	}
	return s.handleUCICodebaseSearch(ctx, args)
}

// handleLegacyUnscopedCodebaseSearch preserves the explicit raw-project
// rollback route and labels every successful response legacy_unscoped.
func (s *Server) handleLegacyUnscopedCodebaseSearch(ctx context.Context, args json.RawMessage) (string, error) {
	if !codeIntelEnabled() {
		return "", errors.New("legacy unscoped code search requires ENGRAM_CODE_INTEL_ENABLED=true")
	}
	if s.legacyUnscopedCodeChunkStore == nil {
		return "", errors.New("legacy unscoped code search: code chunk store not wired")
	}

	params, err := decodeLegacyUnscopedCodebaseSearchArgs(args)
	if err != nil {
		return "", fmt.Errorf("legacy unscoped code search: invalid args: %w", err)
	}

	hits, err := retrieval.LegacyUnscopedCodeHybridSearch(
		ctx,
		params.Project,
		params.Query,
		params.Limit,
		s.legacyUnscopedCodeChunkStore,
		retrieval.LegacyUnscopedCodeHybridOptions{},
	)
	if err != nil {
		return "", fmt.Errorf("legacy unscoped code search: %w", err)
	}

	results := make([]legacyUnscopedCodebaseSearchHit, len(hits))
	for i, hit := range hits {
		results[i] = legacyUnscopedCodebaseSearchHit{
			ID:        hit.ID,
			FilePath:  hit.FilePath,
			ByteStart: hit.ByteStart,
			ByteEnd:   hit.ByteEnd,
			Language:  hit.Language,
			Content:   hit.Content,
			Score:     hit.Score,
		}
	}

	encoded, err := json.Marshal(legacyUnscopedCodebaseSearchResponse{
		RetrievalMode: legacyUnscopedCodebaseRetrievalMode,
		Results:       results,
		Count:         len(results),
		Query:         params.Query,
		Project:       params.Project,
	})
	if err != nil {
		return "", fmt.Errorf("legacy unscoped code search: marshal results: %w", err)
	}
	return string(encoded), nil
}

// handleCodebaseStatus prefers scoped UCI and selects raw-project retrieval
// only for the explicit legacy-only rollback state.
func (s *Server) handleCodebaseStatus(ctx context.Context, args json.RawMessage) (string, error) {
	if !codeIntelEnabled() {
		return "", fmt.Errorf("codebase_status requires ENGRAM_CODE_INTEL_ENABLED=true")
	}
	if s.hasCodebaseIntelligenceApplication() {
		return s.handleUCICodebaseStatus(ctx, args)
	}
	if s.hasLegacyUnscopedCodeChunkStore() {
		return s.handleLegacyUnscopedCodebaseStatus(ctx, args)
	}
	return s.handleUCICodebaseStatus(ctx, args)
}

// handleLegacyUnscopedCodebaseStatus preserves the explicit raw-project
// rollback route and labels every successful response legacy_unscoped.
func (s *Server) handleLegacyUnscopedCodebaseStatus(ctx context.Context, args json.RawMessage) (string, error) {
	if !codeIntelEnabled() {
		return "", errors.New("legacy unscoped code status requires ENGRAM_CODE_INTEL_ENABLED=true")
	}
	if s.legacyUnscopedCodeChunkStore == nil {
		return "", errors.New("legacy unscoped code status: code chunk store not wired")
	}

	params, err := decodeLegacyUnscopedCodebaseStatusArgs(args)
	if err != nil {
		return "", fmt.Errorf("legacy unscoped code status: invalid args: %w", err)
	}

	total, err := s.legacyUnscopedCodeChunkStore.CountByProject(ctx, params.Project)
	if err != nil {
		return "", fmt.Errorf("legacy unscoped code status: count total: %w", err)
	}
	embedded, err := s.legacyUnscopedCodeChunkStore.CountEmbeddedByProject(ctx, params.Project)
	if err != nil {
		return "", fmt.Errorf("legacy unscoped code status: count embedded: %w", err)
	}
	lastAt, hasAt, err := s.legacyUnscopedCodeChunkStore.MaxUpdatedAtByProject(ctx, params.Project)
	if err != nil {
		return "", fmt.Errorf("legacy unscoped code status: max updated at: %w", err)
	}

	response := legacyUnscopedCodebaseStatusResponse{
		RetrievalMode:  legacyUnscopedCodebaseRetrievalMode,
		Project:        params.Project,
		TotalChunks:    total,
		EmbeddedChunks: embedded,
	}
	if hasAt {
		response.LastIndexedAt = lastAt.Format(time.RFC3339)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("legacy unscoped code status: marshal status: %w", err)
	}
	return string(encoded), nil
}

type legacyUnscopedCodebaseSearchArgs struct {
	Query   *string `json:"query"`
	Limit   *int    `json:"limit"`
	Project *string `json:"project"`
}

type legacyUnscopedCodebaseSearchInput struct {
	Query   string
	Limit   int
	Project string
}

type legacyUnscopedCodebaseStatusArgs struct {
	Project *string `json:"project"`
}

type legacyUnscopedCodebaseStatusInput struct {
	Project string
}

type legacyUnscopedCodebaseSearchHit struct {
	ID        int64   `json:"id"`
	FilePath  string  `json:"file_path"`
	ByteStart int     `json:"byte_start"`
	ByteEnd   int     `json:"byte_end"`
	Language  string  `json:"language"`
	Content   string  `json:"content"`
	Score     float64 `json:"score"`
}

type legacyUnscopedCodebaseSearchResponse struct {
	RetrievalMode string                            `json:"retrieval_mode"`
	Results       []legacyUnscopedCodebaseSearchHit `json:"results"`
	Count         int                               `json:"count"`
	Query         string                            `json:"query"`
	Project       string                            `json:"project"`
}

type legacyUnscopedCodebaseStatusResponse struct {
	RetrievalMode  string `json:"retrieval_mode"`
	Project        string `json:"project"`
	TotalChunks    int64  `json:"total_chunks"`
	EmbeddedChunks int64  `json:"embedded_chunks"`
	LastIndexedAt  string `json:"last_indexed_at,omitempty"`
}

type codebaseAfterBarrierArgs struct {
	Token  string `json:"token"`
	WaitMS int64  `json:"wait_ms"`
}

type codebaseSearchArgs struct {
	ContextHandle    *string                   `json:"context_handle"`
	Query            *string                   `json:"query"`
	PathPrefix       *string                   `json:"path_prefix"`
	Limit            *int                      `json:"limit"`
	Project          *string                   `json:"project"`
	AfterBarrier     *codebaseAfterBarrierArgs `json:"after_barrier"`
	hasContextHandle bool
	hasAfterBarrier  bool
	hasPathPrefix    bool
}

type codebaseStatusArgs struct {
	ContextHandle    *string                   `json:"context_handle"`
	Project          *string                   `json:"project"`
	AfterBarrier     *codebaseAfterBarrierArgs `json:"after_barrier"`
	hasContextHandle bool
	hasAfterBarrier  bool
}
type codebaseEmbeddingStatusResponse struct {
	EmbeddingProfileID *string                   `json:"embedding_profile_id"`
	Coverage           uci.IndexCoverageState    `json:"coverage"`
	TotalCandidates    uint64                    `json:"total_candidates"`
	ReadyCandidates    uint64                    `json:"ready_candidates"`
	PendingJobs        uint64                    `json:"pending_jobs"`
	JobState           *uci.IndexStatusJobState  `json:"job_state"`
	ErrorCode          *uci.EmbeddingFailureCode `json:"error_code"`
	RetryAfter         *time.Time                `json:"retry_after"`
}

type codebaseStatusResponse struct {
	Context          uci.QueryContextRef             `json:"context"`
	TotalChunks      int64                           `json:"total_chunks"`
	EmbeddedChunks   int64                           `json:"embedded_chunks"`
	Embedding        codebaseEmbeddingStatusResponse `json:"embedding"`
	EvidenceRecorder CodebaseEvidenceRecorderHealth  `json:"evidence_recorder"`
	Freshness        *uci.QueryFreshness             `json:"freshness,omitempty"`
}

func codebaseEmbeddingStatusResponseFrom(status uci.EmbeddingStatus) codebaseEmbeddingStatusResponse {
	return codebaseEmbeddingStatusResponse{
		EmbeddingProfileID: status.EmbeddingProfileID,
		Coverage:           status.Coverage,
		TotalCandidates:    status.TotalCandidates,
		ReadyCandidates:    status.ReadyCandidates,
		PendingJobs:        status.PendingJobs,
		JobState:           status.JobState,
		ErrorCode:          status.ErrorCode,
		RetryAfter:         status.RetryAfter,
	}
}

func decodeCodebaseSearchArgs(raw json.RawMessage) (codebaseSearchArgs, error) {
	var args codebaseSearchArgs
	fields, err := decodeStrictCodebaseArgs(raw, &args)
	if err != nil {
		return codebaseSearchArgs{}, err
	}
	_, args.hasContextHandle = fields["context_handle"]
	_, args.hasAfterBarrier = fields["after_barrier"]
	_, args.hasPathPrefix = fields["path_prefix"]
	if args.Query == nil || *args.Query == "" {
		return codebaseSearchArgs{}, errors.New("query is required")
	}
	if args.hasPathPrefix {
		if args.PathPrefix == nil {
			return codebaseSearchArgs{}, errors.New("invalid path prefix")
		}
		normalized, err := uci.NormalizeQueryPathPrefix(*args.PathPrefix)
		if err != nil {
			return codebaseSearchArgs{}, errors.New("invalid path prefix")
		}
		args.PathPrefix = &normalized
	}
	if args.hasContextHandle && (args.ContextHandle == nil || !validCodebaseContextHandle(*args.ContextHandle)) {
		return codebaseSearchArgs{}, errors.New("invalid context handle")
	}
	if err := validateCodebaseAfterBarrier(args.AfterBarrier, args.hasAfterBarrier); err != nil {
		return codebaseSearchArgs{}, err
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
	_, args.hasAfterBarrier = fields["after_barrier"]
	if args.hasContextHandle && (args.ContextHandle == nil || !validCodebaseContextHandle(*args.ContextHandle)) {
		return codebaseStatusArgs{}, errors.New("invalid context handle")
	}
	if err := validateCodebaseAfterBarrier(args.AfterBarrier, args.hasAfterBarrier); err != nil {
		return codebaseStatusArgs{}, err
	}
	return args, nil
}

func validateCodebaseAfterBarrier(afterBarrier *codebaseAfterBarrierArgs, present bool) error {
	if !present {
		return nil
	}
	if afterBarrier == nil || !validCodebaseAfterBarrierToken(afterBarrier.Token) {
		return errors.New("invalid after_barrier token")
	}
	if afterBarrier.WaitMS < 1 || afterBarrier.WaitMS > codebaseAfterBarrierMaxWaitMS {
		return fmt.Errorf("after_barrier wait_ms must be between 1 and %d", codebaseAfterBarrierMaxWaitMS)
	}
	return nil
}

func validCodebaseAfterBarrierToken(token string) bool {
	return len(token) <= codebaseAfterBarrierMaxTokenLength && codebaseContextIdentityText(token)
}

func decodeLegacyUnscopedCodebaseSearchArgs(raw json.RawMessage) (legacyUnscopedCodebaseSearchInput, error) {
	var args legacyUnscopedCodebaseSearchArgs
	if _, err := decodeStrictCodebaseArgs(raw, &args); err != nil {
		return legacyUnscopedCodebaseSearchInput{}, err
	}
	if args.Query == nil || *args.Query == "" {
		return legacyUnscopedCodebaseSearchInput{}, errors.New("query is required")
	}
	if args.Project == nil || *args.Project == "" {
		return legacyUnscopedCodebaseSearchInput{}, errors.New("project is required")
	}
	input := legacyUnscopedCodebaseSearchInput{
		Query:   *args.Query,
		Limit:   codebaseSearchDefaultLimit,
		Project: *args.Project,
	}
	if args.Limit != nil {
		if *args.Limit < 1 || *args.Limit > codebaseSearchMaxLimit {
			return legacyUnscopedCodebaseSearchInput{}, fmt.Errorf("limit must be between 1 and %d", codebaseSearchMaxLimit)
		}
		input.Limit = *args.Limit
	}
	return input, nil
}

func decodeLegacyUnscopedCodebaseStatusArgs(raw json.RawMessage) (legacyUnscopedCodebaseStatusInput, error) {
	var args legacyUnscopedCodebaseStatusArgs
	if _, err := decodeStrictCodebaseArgs(raw, &args); err != nil {
		return legacyUnscopedCodebaseStatusInput{}, err
	}
	if args.Project == nil || *args.Project == "" {
		return legacyUnscopedCodebaseStatusInput{}, errors.New("project is required")
	}
	return legacyUnscopedCodebaseStatusInput{Project: *args.Project}, nil
}

func decodeStrictCodebaseArgs(raw json.RawMessage, target any) (map[string]json.RawMessage, error) {
	input := bytes.TrimSpace(raw)
	if len(input) == 0 {
		input = []byte("{}")
	}
	if !utf8.Valid(input) {
		return nil, errors.New("arguments must be valid UTF-8")
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

func (args codebaseSearchArgs) searchInput() CodebaseSearchInput {
	input := CodebaseSearchInput{Query: *args.Query, Limit: codebaseSearchDefaultLimit}
	if args.PathPrefix != nil {
		input.PathPrefix = *args.PathPrefix
	}
	if args.Limit != nil {
		input.Limit = *args.Limit
	}
	return input
}

func codebaseFreshness(ctx context.Context, application CodebaseIntelligenceApplication, authorized uci.AuthorizedContext, afterBarrier *codebaseAfterBarrierArgs) (*uci.QueryFreshness, uci.QueryFreshnessDisposition, error) {
	freshnessApplication, ok := application.(CodebaseFreshnessApplication)
	if !ok {
		if afterBarrier != nil {
			return nil, "", errors.New("UCI freshness capability is unavailable")
		}
		return nil, "", nil
	}

	token := ""
	freshnessContext := ctx
	var cancel context.CancelFunc
	if afterBarrier != nil {
		token = afterBarrier.Token
		freshnessContext, cancel = context.WithTimeout(ctx, time.Duration(afterBarrier.WaitMS)*time.Millisecond)
	}
	if cancel != nil {
		defer cancel()
	}

	freshness, err := freshnessApplication.CodebaseFreshness(freshnessContext, authorized, token)
	if err != nil {
		if childErr := freshnessContext.Err(); childErr != nil {
			return nil, "", childErr
		}
		if parentErr := ctx.Err(); parentErr != nil {
			return nil, "", parentErr
		}
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	disposition, err := uci.ClassifyQueryFreshness(freshness)
	if err != nil {
		return nil, "", err
	}
	return &freshness, disposition, nil
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

	freshness, disposition, err := codebaseFreshness(ctx, application, authorized, args.AfterBarrier)
	if err != nil {
		if code, isContextFailure := codebaseContextErrorCode(err); isContextFailure {
			return codebaseSearchContextRefusal(code)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		return "", errors.New("codebase_search: UCI freshness unavailable")
	}
	if !s.codebaseContextEpochCurrent(epoch) {
		return codebaseSearchContextRefusal(uci.ContextMismatch)
	}

	response, err := application.SearchCodebase(ctx, authorized, args.searchInput())
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if code, isContextFailure := codebaseContextErrorCode(err); isContextFailure {
			return codebaseSearchContextRefusal(code)
		}
		return "", errors.New("codebase_search: UCI application unavailable")
	}
	if !codebaseQueryResponseHasExactContext(response, authorized) {
		return codebaseSearchContextRefusal(uci.ContextMismatch)
	}
	if freshness != nil && (response.Freshness == nil || response.Freshness.State != uci.QueryFreshnessHistorical) {
		if disposition == uci.QueryFreshnessDispositionOffline && !codebaseOfflineResponseRecordable(response) {
			return "", errors.New("codebase_search: invalid offline UCI response")
		}
		response.Freshness = freshness
		if disposition == uci.QueryFreshnessDispositionStale {
			switch response.Status {
			case uci.QueryStatusOK, uci.QueryStatusEmpty, uci.QueryStatusPartial, uci.QueryStatusStale:
				response.Status = uci.QueryStatusStale
			}
		}
	}
	return s.releaseCodebaseQueryResponse(ctx, epoch, authorized, args.ContextHandle, uci.ExposureOperationCodeSearch, response, func(candidate uci.QueryResponse) bool {
		return codebaseQueryResponseHasExactContext(candidate, authorized)
	}, "codebase_search")
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

	for attempt := range codebaseStatusStabilizationAttempts {
		var freshness *uci.QueryFreshness
		if args.AfterBarrier != nil {
			freshness, _, err = codebaseFreshness(ctx, application, authorized, args.AfterBarrier)
			if err != nil {
				if code, isContextFailure := codebaseContextErrorCode(err); isContextFailure {
					return "", codebaseContextClosedError(code)
				}
				if ctxErr := ctx.Err(); ctxErr != nil {
					return "", ctxErr
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return "", err
				}
				return "", errors.New("codebase_status: UCI freshness unavailable")
			}
			if !s.codebaseContextEpochCurrent(epoch) {
				return "", codebaseContextClosedError(uci.ContextMismatch)
			}
		}

		snapshot, err := application.CodebaseStatus(ctx, authorized)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			if code, isContextFailure := codebaseContextErrorCode(err); isContextFailure {
				return "", codebaseContextClosedError(code)
			}
			return "", errors.New("codebase_status: UCI application unavailable")
		}
		if !s.codebaseContextEpochCurrent(epoch) {
			return "", codebaseContextClosedError(uci.ContextMismatch)
		}
		if args.AfterBarrier == nil {
			switch {
			case snapshot.Freshness != nil:
				value := *snapshot.Freshness
				if err := value.Validate(); err != nil {
					return "", errors.New("codebase_status: UCI snapshot freshness unavailable")
				}
				freshness = &value
			default:
				freshness, _, err = codebaseFreshness(ctx, application, authorized, nil)
				if err != nil {
					if code, isContextFailure := codebaseContextErrorCode(err); isContextFailure {
						return "", codebaseContextClosedError(code)
					}
					if ctxErr := ctx.Err(); ctxErr != nil {
						return "", ctxErr
					}
					return "", errors.New("codebase_status: UCI freshness unavailable")
				}
				if !s.codebaseContextEpochCurrent(epoch) {
					return "", codebaseContextClosedError(uci.ContextMismatch)
				}
			}
		}

		reauthorized, contextCode := s.reauthorizeCodebaseContext(ctx, epoch, authorized, args.ContextHandle)
		if contextCode != "" {
			return "", codebaseContextClosedError(contextCode)
		}
		currentRef := authorized.Ref()
		reauthorizedRef := reauthorized.Ref()
		if codebaseContextRefsEqual(reauthorizedRef, currentRef) {
			encoded, err := json.Marshal(codebaseStatusResponse{
				Context:          codebaseQueryContextRef(reauthorizedRef),
				TotalChunks:      snapshot.TotalChunks,
				EmbeddedChunks:   snapshot.EmbeddedChunks,
				Embedding:        codebaseEmbeddingStatusResponseFrom(snapshot.Embedding),
				EvidenceRecorder: s.codebaseExposureRecorderHealth(),
				Freshness:        freshness,
			})
			if err != nil {
				return "", errors.New("codebase_status: marshal UCI response")
			}
			return string(encoded), nil
		}
		if attempt+1 == codebaseStatusStabilizationAttempts || !codebaseStatusContextAdvanced(currentRef, reauthorizedRef) {
			return "", codebaseContextClosedError(uci.ContextMismatch)
		}
		authorized = reauthorized
	}

	return "", codebaseContextClosedError(uci.ContextMismatch)
}

func codebaseStatusContextAdvanced(previous, current uci.ContextRef) bool {
	if previous.SourceID != current.SourceID ||
		previous.CheckoutID != current.CheckoutID ||
		previous.AnalysisProfileID != current.AnalysisProfileID ||
		current.Generation <= previous.Generation {
		return false
	}
	if previous.SpaceID == nil || current.SpaceID == nil {
		return previous.SpaceID == nil && current.SpaceID == nil
	}
	return *previous.SpaceID == *current.SpaceID
}

func (s *Server) hasCodebaseIntelligenceApplication() bool {
	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	_, ok := s.codebaseContextApplication.(CodebaseIntelligenceApplication)
	return ok
}

func (s *Server) hasLegacyUnscopedCodeChunkStore() bool {
	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	return s.legacyUnscopedCodeChunkStore != nil
}

// releaseCodebaseQueryResponse adapts the MCP context registry to the shared
// UCI release mapper. The mapper owns typed caller mapping, closed suppression,
// and receipt attachment; this adapter retains MCP's epoch-atomic recheck.
func (s *Server) releaseCodebaseQueryResponse(
	ctx context.Context,
	epoch uint64,
	authorized uci.AuthorizedContext,
	contextHandle *string,
	operation uci.ExposureOperation,
	response uci.QueryResponse,
	matches func(uci.QueryResponse) bool,
	tool string,
) (string, error) {
	if response.Status == uci.QueryStatusContextRequired || response.Status == uci.QueryStatusForbidden {
		return marshalValidatedCodebaseQueryResponse(response)
	}
	if matches == nil || !matches(response) || response.ValidatePreExposure() != nil {
		return "", fmt.Errorf("%s: invalid UCI response", tool)
	}
	category, ok := uci.ReleaseCategoryForExposureOperation(operation)
	if !ok {
		return marshalValidatedCodebaseQueryResponse(uci.QueryResponse{
			Schema: uci.QueryResponseSchema,
			Status: uci.QueryStatusUnavailable,
			Error:  &uci.QueryError{Code: uci.QueryErrorExposureUnavailable},
		})
	}
	exposureInput, err := codebaseExposureInput(ctx, operation, response)
	if err != nil {
		return marshalValidatedCodebaseQueryResponse(uci.QueryResponse{
			Schema: uci.QueryResponseSchema,
			Status: uci.QueryStatusUnavailable,
			Error:  &uci.QueryError{Code: uci.QueryErrorExposureUnavailable},
		})
	}

	released := uci.ReleaseQueryResponse(ctx, uci.ReleaseRequest{
		AuthRealm:            exposureInput.AuthRealm,
		Caller:               uci.ReleaseCaller{MCP: &uci.MCPReleaseCaller{Keycard: exposureInput.ClientKeycard, SessionID: exposureInput.ClientSession}},
		RequestID:            exposureInput.RequestID,
		RequestBindingDigest: exposureInput.RequestBindingDigest,
		Category:             category,
		Response:             &response,
		RecordedAt:           exposureInput.RecordedAt,
	}, codebaseQueryReleaseGate{
		server:        s,
		epoch:         epoch,
		authorized:    authorized,
		contextHandle: contextHandle,
	})
	if err := released.Validate(); err != nil {
		return "", fmt.Errorf("%s: invalid released UCI response", tool)
	}
	return marshalValidatedCodebaseQueryResponse(released)
}

type codebaseQueryReleaseGate struct {
	server        *Server
	epoch         uint64
	authorized    uci.AuthorizedContext
	contextHandle *string
}

func (gate codebaseQueryReleaseGate) Reauthorize(ctx context.Context) (uci.AuthorizedContext, uci.ReleaseFailureCode) {
	if gate.server == nil || !gate.server.codebaseContextEpochCurrent(gate.epoch) {
		return uci.AuthorizedContext{}, uci.ReleaseFailureContextMismatch
	}
	reauthorized, contextCode := gate.server.reauthorizeCodebaseContext(ctx, gate.epoch, gate.authorized, gate.contextHandle)
	if contextCode != "" {
		return uci.AuthorizedContext{}, codebaseReleaseFailure(contextCode)
	}
	if !codebaseContextRefsEqual(reauthorized.Ref(), gate.authorized.Ref()) {
		return uci.AuthorizedContext{}, uci.ReleaseFailureContextMismatch
	}
	return reauthorized, uci.ReleaseFailureNone
}

func (gate codebaseQueryReleaseGate) AppendExposure(ctx context.Context, authorized uci.AuthorizedContext, input uci.ExposureInput) (uci.QueryExposure, uci.ReleaseFailureCode) {
	receipt, current, err := gate.server.recordCodebaseExposure(ctx, gate.epoch, authorized, input)
	if !current {
		return uci.QueryExposure{}, uci.ReleaseFailureContextMismatch
	}
	if err != nil {
		if errors.Is(err, uci.ErrIdempotencyMismatch) {
			return uci.QueryExposure{}, uci.ReleaseFailureIdempotencyMismatch
		}
		return uci.QueryExposure{}, uci.ReleaseFailureExposureUnavailable
	}
	return receipt, uci.ReleaseFailureNone
}

func codebaseReleaseFailure(code uci.ContextErrorCode) uci.ReleaseFailureCode {
	switch code {
	case uci.ContextRequired:
		return uci.ReleaseFailureContextRequired
	case uci.PermissionDenied:
		return uci.ReleaseFailurePermissionDenied
	default:
		return uci.ReleaseFailureContextMismatch
	}
}

func (s *Server) reauthorizeCodebaseContext(ctx context.Context, epoch uint64, authorized uci.AuthorizedContext, contextHandle *string) (uci.AuthorizedContext, uci.ContextErrorCode) {
	input, err := codebaseContextCallerInput(ctx)
	if err != nil {
		return uci.AuthorizedContext{}, uci.ContextMismatch
	}
	want := authorized.Ref()
	application, currentEpoch, found := s.codebaseContextApplicationSnapshot()
	if !found || currentEpoch != epoch {
		return uci.AuthorizedContext{}, uci.ContextMismatch
	}

	if contextHandle == nil {
		input.Ref = &want
		reauthorized, err := application.Authorize(ctx, input)
		if err != nil {
			return uci.AuthorizedContext{}, codebaseContextFailureCode(err)
		}
		if !codebaseContextRefsEqual(reauthorized.Ref(), want) || !s.codebaseContextEpochCurrent(epoch) {
			return uci.AuthorizedContext{}, uci.ContextMismatch
		}
		return reauthorized, ""
	}

	_, handleEpoch, selector, found := s.codebaseContextSelectorForHandle(input.ClientSessionID, *contextHandle)
	if !found || handleEpoch != epoch {
		return uci.AuthorizedContext{}, uci.ContextMismatch
	}
	reauthorized, binding, contextCode := authorizeCodebaseExplicitSelector(ctx, application, input, selector)
	if contextCode != "" {
		return uci.AuthorizedContext{}, contextCode
	}
	if !s.codebaseContextHandleStillCurrent(input.ClientSessionID, *contextHandle, epoch, selector, binding) {
		return uci.AuthorizedContext{}, uci.ContextMismatch
	}
	return reauthorized, ""
}

// recordCodebaseExposure establishes the release linearization point: replacing
// the context application cannot race an already re-authorized durable append.
func (s *Server) recordCodebaseExposure(ctx context.Context, epoch uint64, authorized uci.AuthorizedContext, input uci.ExposureInput) (uci.QueryExposure, bool, error) {
	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	if s.codebaseContextApplication == nil || s.codebaseContextEpoch != epoch {
		return uci.QueryExposure{}, false, nil
	}
	if s.uciExposureRecorder == nil {
		return uci.QueryExposure{}, true, uci.ErrExposureUnavailable
	}
	receipt, err := s.uciExposureRecorder.Record(ctx, authorized, input)
	return receipt, true, err
}

func (s *Server) codebaseExposureRecorderHealth() CodebaseEvidenceRecorderHealth {
	recorder := s.uciExposureRecorderSnapshot()
	if recorder == nil {
		return CodebaseEvidenceRecorderHealth{
			State:           string(uci.ExposureHealthUnavailable),
			LastFailureCode: string(uci.ExposureHealthFailureExposureUnavailable),
		}
	}
	snapshot := recorder.Health()
	return CodebaseEvidenceRecorderHealth{
		State:           string(snapshot.State),
		LastFailureCode: string(snapshot.LastFailureCode),
	}
}

func (s *Server) resolveCodebaseIntelligenceContext(ctx context.Context, contextHandle *string) (CodebaseIntelligenceApplication, uci.AuthorizedContext, uint64, uci.ContextErrorCode) {
	application, authorized, epoch, contextCode := s.resolveCodebaseAuthorizedView(ctx, contextHandle)
	if contextCode != "" {
		return nil, uci.AuthorizedContext{}, 0, contextCode
	}
	intelligence, ok := application.(CodebaseIntelligenceApplication)
	if !ok {
		return nil, uci.AuthorizedContext{}, 0, uci.ContextRequired
	}
	return intelligence, authorized, epoch, ""
}

func resolveCodebaseCompatibilityEvidence(ctx context.Context, application CodebaseIntelligenceApplication, authorized uci.AuthorizedContext, project *string) uci.ContextErrorCode {
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
	return codebaseQueryResponseHasExactContext(response, authorized)
}

func codebaseQueryResponseHasExactContext(response uci.QueryResponse, authorized uci.AuthorizedContext) bool {
	if response.Contexts == nil || len(*response.Contexts) != 1 {
		return false
	}
	return codebaseQueryContextMatchesRef((*response.Contexts)[0], authorized.Ref())
}

func codebaseOfflineResponseRecordable(response uci.QueryResponse) bool {
	return response.Status == uci.QueryStatusUnavailable &&
		response.Error != nil && response.Error.Code == uci.QueryErrorCheckoutOffline &&
		response.Items != nil && len(*response.Items) == 0 &&
		response.Exposure == nil
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
