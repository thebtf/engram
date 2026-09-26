package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/thebtf/engram/internal/uci"
)

const (
	codebaseReadDefaultMaxBytes = 8_192
	codebaseReadMaxBytes        = 8_192
)

// CodebaseReadApplication is an optional UCI capability of the existing
// client-scoped context application. It receives only an authorized immutable
// context and a View-grounded selector, then returns an unreleased
// pre-exposure response for the MCP boundary to record and release.
type CodebaseReadApplication interface {
	ReadCodebase(context.Context, uci.AuthorizedContext, CodebaseReadInput) (uci.QueryResponse, error)
}

// CodebaseReadCompatibilityApplication is needed only when a caller supplies
// legacy project evidence. The project is never a context selector.
type CodebaseReadCompatibilityApplication interface {
	ResolveLegacyProject(context.Context, uci.AuthorizedContext, string) (uci.AliasTarget, error)
}

// CodebaseReadInput is the fully validated source selector forwarded to UCI.
// MaxBytes bounds one exact stored span; callers cannot request truncation or
// replacement with current working-copy bytes.
type CodebaseReadInput struct {
	Ref               uci.QueryEntityRef
	Span              uci.QuerySpan
	ContentDigest     uci.QueryContentDigest
	ReferenceSiteID   *string
	VerifyWorkingCopy bool
	MaxBytes          int
}

type codebaseReadEntityRefArgs struct {
	SourceID  *string `json:"source_id"`
	ViewID    *string `json:"view_id"`
	EntityKey *string `json:"entity_key"`
}

type codebaseReadSpanArgs struct {
	ByteStart *int64 `json:"byte_start"`
	ByteEnd   *int64 `json:"byte_end"`
	LineStart *int64 `json:"line_start"`
	LineEnd   *int64 `json:"line_end"`
}

type codebaseReadArgs struct {
	ContextHandle     *string                    `json:"context_handle"`
	Project           *string                    `json:"project"`
	Ref               *codebaseReadEntityRefArgs `json:"ref"`
	Span              *codebaseReadSpanArgs      `json:"span"`
	ContentDigest     *string                    `json:"content_digest"`
	VerifyWorkingCopy *bool                      `json:"verify_working_copy"`
	MaxBytes          *int                       `json:"max_bytes"`
	hasContextHandle  bool
	hasMaxBytes       bool
}

// codebaseReadTool returns the adopted UCI-only versioned source-read tool.
func codebaseReadTool() Tool {
	return Tool{
		Name:        "codebase_read",
		Description: "Read one bounded, versioned source span from an authorized UCI View. verify_working_copy reports application-owned metadata only and never substitutes local-disk bytes.",
		tier:        tierCore,
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"ref", "span", "content_digest"},
			"properties": map[string]any{
				"context_handle": map[string]any{
					"type":        "string",
					"description": "Optional opaque client-local context handle; omitted uses this client's current UCI binding",
				},
				"project": map[string]any{
					"type":        "string",
					"description": "Optional legacy compatibility evidence only; it cannot choose a UCI context",
				},
				"ref": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"source_id", "view_id", "entity_key"},
					"properties": map[string]any{
						"source_id":  map[string]any{"type": "string"},
						"view_id":    map[string]any{"type": "string"},
						"entity_key": map[string]any{"type": "string"},
					},
				},
				"span": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"byte_start", "byte_end", "line_start", "line_end"},
					"properties": map[string]any{
						"byte_start": map[string]any{"type": "integer", "minimum": 0},
						"byte_end":   map[string]any{"type": "integer", "minimum": 1},
						"line_start": map[string]any{"type": "integer", "minimum": 1},
						"line_end":   map[string]any{"type": "integer", "minimum": 1},
					},
				},
				"content_digest": map[string]any{
					"type":        "string",
					"pattern":     "^[0-9a-f]{64}$",
					"description": "Bare SHA-256 digest from the selected View citation",
				},
				"verify_working_copy": map[string]any{
					"type":        "boolean",
					"description": "Request application-owned match, mismatch, or unavailable metadata without returning local-disk bytes",
				},
				"max_bytes": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     codebaseReadMaxBytes,
					"default":     codebaseReadDefaultMaxBytes,
					"description": "Maximum exact stored span size in bytes",
				},
			},
		},
	}
}

func (s *Server) handleCodebaseRead(ctx context.Context, raw json.RawMessage) (string, error) {
	if !codeIntelEnabled() {
		return "", fmt.Errorf("codebase_read requires ENGRAM_CODE_INTEL_ENABLED=true")
	}

	args, err := decodeCodebaseReadArgs(raw)
	if err != nil {
		return "", fmt.Errorf("codebase_read: invalid args: %w", err)
	}
	input := args.readInput()

	application, authorized, epoch, contextCode := s.resolveCodebaseReadContext(ctx, args.ContextHandle)
	if contextCode != "" {
		return codebaseSearchContextRefusal(contextCode)
	}
	if !codebaseReadInputMatchesContext(input, authorized) {
		return codebaseSearchContextRefusal(uci.ContextMismatch)
	}
	if contextCode = resolveCodebaseReadCompatibilityEvidence(ctx, application, authorized, args.Project); contextCode != "" {
		return codebaseSearchContextRefusal(contextCode)
	}
	if !s.codebaseContextEpochCurrent(epoch) {
		return codebaseSearchContextRefusal(uci.ContextMismatch)
	}

	response, err := application.ReadCodebase(ctx, authorized, input)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if code, isContextFailure := codebaseContextErrorCode(err); isContextFailure {
			return codebaseSearchContextRefusal(code)
		}
		return "", errors.New("codebase_read: UCI application unavailable")
	}
	if !codebaseQueryResponseHasExactContext(response, authorized) {
		return codebaseSearchContextRefusal(uci.ContextMismatch)
	}
	if !s.codebaseContextEpochCurrent(epoch) {
		return codebaseSearchContextRefusal(uci.ContextMismatch)
	}
	return s.releaseCodebaseQueryResponse(codebaseQueryReleaseInput{
		ctx: ctx, epoch: epoch, authorized: authorized, contextHandle: args.ContextHandle, operation: uci.ExposureOperationVersionedRead, response: response,
		matches: func(candidate uci.QueryResponse) bool {
			return validCodebaseReadPreExposureResponse(candidate, authorized, input)
		}, tool: "codebase_read",
	})
}

func decodeCodebaseReadArgs(raw json.RawMessage) (codebaseReadArgs, error) {
	var args codebaseReadArgs
	fields, err := decodeStrictCodebaseArgs(raw, &args)
	if err != nil {
		return codebaseReadArgs{}, err
	}
	_, args.hasContextHandle = fields["context_handle"]
	_, args.hasMaxBytes = fields["max_bytes"]
	if args.hasContextHandle && (args.ContextHandle == nil || !validCodebaseContextHandle(*args.ContextHandle)) {
		return codebaseReadArgs{}, errors.New("invalid context handle")
	}
	if args.Ref == nil || args.Ref.SourceID == nil || args.Ref.ViewID == nil || args.Ref.EntityKey == nil {
		return codebaseReadArgs{}, errors.New("ref is required")
	}
	if args.Span == nil || args.Span.ByteStart == nil || args.Span.ByteEnd == nil || args.Span.LineStart == nil || args.Span.LineEnd == nil {
		return codebaseReadArgs{}, errors.New("span is required")
	}
	if args.ContentDigest == nil || !validCodebaseReadContentDigest(*args.ContentDigest) {
		return codebaseReadArgs{}, errors.New("invalid content_digest")
	}
	if args.hasMaxBytes && (args.MaxBytes == nil || *args.MaxBytes < 1 || *args.MaxBytes > codebaseReadMaxBytes) {
		return codebaseReadArgs{}, fmt.Errorf("max_bytes must be between 1 and %d", codebaseReadMaxBytes)
	}

	input := args.readInput()
	if err := input.Ref.Validate(); err != nil {
		return codebaseReadArgs{}, errors.New("invalid ref")
	}
	if err := input.Span.Validate(); err != nil {
		return codebaseReadArgs{}, errors.New("invalid span")
	}
	if input.Span.ByteEnd-input.Span.ByteStart > int64(input.MaxBytes) {
		return codebaseReadArgs{}, fmt.Errorf("span exceeds max_bytes %d", input.MaxBytes)
	}
	return args, nil
}

func (args codebaseReadArgs) readInput() CodebaseReadInput {
	maxBytes := codebaseReadDefaultMaxBytes
	if args.MaxBytes != nil {
		maxBytes = *args.MaxBytes
	}
	verifyWorkingCopy := args.VerifyWorkingCopy != nil && *args.VerifyWorkingCopy
	return CodebaseReadInput{
		Ref: uci.QueryEntityRef{
			SourceID:  *args.Ref.SourceID,
			ViewID:    *args.Ref.ViewID,
			EntityKey: *args.Ref.EntityKey,
		},
		Span: uci.QuerySpan{
			ByteStart: *args.Span.ByteStart,
			ByteEnd:   *args.Span.ByteEnd,
			LineStart: *args.Span.LineStart,
			LineEnd:   *args.Span.LineEnd,
		},
		ContentDigest:     uci.QueryContentDigest(*args.ContentDigest),
		VerifyWorkingCopy: verifyWorkingCopy,
		MaxBytes:          maxBytes,
	}
}

func validCodebaseReadContentDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (s *Server) resolveCodebaseReadContext(ctx context.Context, contextHandle *string) (CodebaseReadApplication, uci.AuthorizedContext, uint64, uci.ContextErrorCode) {
	application, authorized, epoch, contextCode := s.resolveCodebaseAuthorizedView(ctx, contextHandle)
	if contextCode != "" {
		return nil, uci.AuthorizedContext{}, 0, contextCode
	}
	reader, ok := application.(CodebaseReadApplication)
	if !ok {
		return nil, uci.AuthorizedContext{}, 0, uci.ContextRequired
	}
	return reader, authorized, epoch, ""
}

func resolveCodebaseReadCompatibilityEvidence(ctx context.Context, application CodebaseReadApplication, authorized uci.AuthorizedContext, project *string) uci.ContextErrorCode {
	if project == nil {
		return ""
	}
	if *project == "" {
		return uci.ContextMismatch
	}
	resolver, ok := application.(CodebaseReadCompatibilityApplication)
	if !ok {
		return uci.ContextRequired
	}
	target, err := resolver.ResolveLegacyProject(ctx, authorized, *project)
	if err != nil {
		return codebaseContextFailureCode(err)
	}
	if !codebaseAliasMatchesAuthorizedContext(target, authorized.Ref()) {
		return uci.ContextMismatch
	}
	return ""
}

func codebaseReadInputMatchesContext(input CodebaseReadInput, authorized uci.AuthorizedContext) bool {
	ref := authorized.Ref()
	return input.Ref.SourceID == ref.SourceID && input.Ref.ViewID == ref.ViewID
}

func (s *Server) hasCodebaseReadApplication() bool {
	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	_, ok := s.codebaseContextApplication.(CodebaseReadApplication)
	return ok
}

func validCodebaseReadPreExposureResponse(response uci.QueryResponse, authorized uci.AuthorizedContext, input CodebaseReadInput) bool {
	if response.Exposure != nil || response.Graph != nil {
		return false
	}

	switch response.Status {
	case uci.QueryStatusOK, uci.QueryStatusPartial, uci.QueryStatusStale:
		if response.Retrieval == nil || response.Retrieval.Mode != uci.QueryRetrievalExact || response.Items == nil || len(*response.Items) != 1 || response.Truncated == nil || *response.Truncated {
			return false
		}
		item := (*response.Items)[0]
		return item.Ref == input.Ref &&
			item.Span == input.Span &&
			item.ContentDigest == input.ContentDigest &&
			len(item.Excerpt) == int(input.Span.ByteEnd-input.Span.ByteStart) &&
			len(item.Excerpt) <= input.MaxBytes &&
			codebaseReadRelativePath(item.Path)
	case uci.QueryStatusEmpty:
		return response.Retrieval != nil && response.Retrieval.Mode == uci.QueryRetrievalExact
	case uci.QueryStatusUnavailable:
		return true
	default:
		return false
	}
}

func codebaseReadRelativePath(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "\\") {
		return false
	}
	if len(value) >= 2 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' {
		return false
	}
	for _, part := range strings.FieldsFunc(value, func(character rune) bool {
		return character == '/' || character == '\\'
	}) {
		if part == ".." {
			return false
		}
	}
	return true
}
