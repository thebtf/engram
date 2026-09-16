package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/uci"
)

const (
	codebaseGraphDefaultMaxDepth   = 4
	codebaseGraphMaxDepth          = 8
	codebaseGraphDefaultMaxVisited = 64
	codebaseGraphMaxVisited        = 5_000
	codebaseGraphDefaultMaxNodes   = 32
	codebaseGraphMaxNodes          = 200
	codebaseGraphDefaultMaxEdges   = 64
	codebaseGraphMaxEdges          = 400
	codebaseGraphDefaultDeadlineMS = int64(30_000)
	codebaseGraphMaxDeadlineMS     = int64(30_000)
	codebaseGraphMaxRelations      = 32
	codebaseGraphMaxEvidenceKinds  = 4
	codebaseGraphMaxContinuation   = 2_048
)

// CodebaseGraphApplication is an optional UCI capability of the existing
// client-scoped context application. It owns graph traversal, evidence, and
// continuation binding, then returns an unreleased pre-exposure response.
type CodebaseGraphApplication interface {
	ExploreCodebase(context.Context, uci.AuthorizedContext, CodebaseGraphInput) (uci.QueryResponse, error)
}

// CodebaseGraphInput is the fully normalized graph request forwarded after the
// MCP boundary has verified every explicit Source/View selector.
type CodebaseGraphInput struct {
	Action       uci.GraphAction
	Target       uci.GraphTarget
	Destination  *uci.GraphTarget
	Filter       uci.GraphFilter
	Budget       uci.GraphBudget
	Continuation *string
}

type codebaseGraphTargetArgs struct {
	SourceID  *string `json:"source_id"`
	ViewID    *string `json:"view_id"`
	EntityKey *string `json:"entity_key"`
	Name      *string `json:"name"`
}

type codebaseGraphArgs struct {
	ContextHandle *string                   `json:"context_handle"`
	Project       *string                   `json:"project"`
	Action        *string                   `json:"action"`
	Target        *codebaseGraphTargetArgs  `json:"target"`
	Destination   *codebaseGraphTargetArgs  `json:"destination"`
	Direction     *string                   `json:"direction"`
	Relations     []string                  `json:"relations"`
	EvidenceKinds []string                  `json:"evidence_kinds"`
	MaxDepth      *int                      `json:"max_depth"`
	MaxVisited    *int                      `json:"max_visited"`
	MaxNodes      *int                      `json:"max_nodes"`
	MaxEdges      *int                      `json:"max_edges"`
	DeadlineMS    *int64                    `json:"deadline_ms"`
	Continuation  *string                   `json:"continuation"`
	AfterBarrier  *codebaseAfterBarrierArgs `json:"after_barrier"`

	hasContextHandle bool
	hasProject       bool
	hasDestination   bool
	hasDirection     bool
	hasRelations     bool
	hasEvidenceKinds bool
	hasMaxDepth      bool
	hasMaxVisited    bool
	hasMaxNodes      bool
	hasMaxEdges      bool
	hasDeadlineMS    bool
	hasContinuation  bool
	hasAfterBarrier  bool

	action       uci.GraphAction
	target       codebaseGraphTarget
	destination  *codebaseGraphTarget
	filter       uci.GraphFilter
	maxDepth     int
	maxVisited   int
	maxNodes     int
	maxEdges     int
	deadlineMS   int64
	continuation *string
}

type codebaseGraphTarget struct {
	sourceID string
	viewID   string
	target   uci.GraphTarget
}

func codebaseGraphTool() Tool {
	return Tool{
		Name:        "codebase_graph",
		Description: "Explore bounded static graph evidence within an authorized UCI View. The optional project is compatibility evidence only and never selection authority.",
		tier:        tierCore,
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"action", "target"},
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"explain", "neighbors", "path", "impact", "flow", "cycles"},
					"description": "Bounded static graph operation",
				},
				"context_handle": map[string]any{
					"type":        "string",
					"description": "Optional opaque client-local context handle; omitted uses this client's current UCI binding",
				},
				"project": map[string]any{
					"type":        "string",
					"description": "Optional legacy compatibility evidence only; it cannot choose a UCI context",
				},
				"target":      codebaseGraphTargetSchema(),
				"destination": codebaseGraphTargetSchema(),
				"direction": map[string]any{
					"type":        "string",
					"enum":        []string{"incoming", "outgoing", "both"},
					"default":     "both",
					"description": "Traversal direction",
				},
				"relations": map[string]any{
					"type":        "array",
					"maxItems":    codebaseGraphMaxRelations,
					"description": "Optional closed relation filter",
					"items": map[string]any{
						"type": "string",
						"enum": codebaseGraphRelationNames(),
					},
				},
				"evidence_kinds": map[string]any{
					"type":        "array",
					"maxItems":    codebaseGraphMaxEvidenceKinds,
					"description": "Optional closed graph-evidence filter",
					"items": map[string]any{
						"type": "string",
						"enum": []string{"EXTRACTED", "RESOLVED", "HEURISTIC", "SEMANTIC"},
					},
				},
				"max_depth": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": codebaseGraphMaxDepth,
					"default": codebaseGraphDefaultMaxDepth,
				},
				"max_visited": map[string]any{
					"type":    "integer",
					"minimum": 1,
					"maximum": codebaseGraphMaxVisited,
					"default": codebaseGraphDefaultMaxVisited,
				},
				"max_nodes": map[string]any{
					"type":    "integer",
					"minimum": 1,
					"maximum": codebaseGraphMaxNodes,
					"default": codebaseGraphDefaultMaxNodes,
				},
				"max_edges": map[string]any{
					"type":    "integer",
					"minimum": 1,
					"maximum": codebaseGraphMaxEdges,
					"default": codebaseGraphDefaultMaxEdges,
				},
				"deadline_ms": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     int(codebaseGraphMaxDeadlineMS),
					"default":     int(codebaseGraphDefaultDeadlineMS),
					"description": "Maximum end-to-end graph operation time in milliseconds",
				},
				"continuation": map[string]any{
					"type":        "string",
					"minLength":   1,
					"maxLength":   codebaseGraphMaxContinuation,
					"description": "Opaque application-issued continuation token",
				},
				"after_barrier": codebaseAfterBarrierSchema(),
			},
		},
	}
}

func codebaseGraphTargetSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"source_id", "view_id"},
		"properties": map[string]any{
			"source_id":  map[string]any{"type": "string"},
			"view_id":    map[string]any{"type": "string"},
			"entity_key": map[string]any{"type": "string"},
			"name":       map[string]any{"type": "string"},
		},
		"oneOf": []map[string]any{
			{"required": []string{"entity_key"}, "not": map[string]any{"required": []string{"name"}}},
			{"required": []string{"name"}, "not": map[string]any{"required": []string{"entity_key"}}},
		},
	}
}

type codebaseGraphOperation struct {
	application CodebaseGraphApplication
	authorized  uci.AuthorizedContext
	epoch       uint64
	input       CodebaseGraphInput
}

func (s *Server) handleCodebaseGraph(ctx context.Context, raw json.RawMessage) (string, error) {
	if !codeIntelEnabled() {
		return "", fmt.Errorf("codebase_graph requires ENGRAM_CODE_INTEL_ENABLED=true")
	}
	if ctx == nil {
		return "", errors.New("codebase_graph: missing context")
	}
	args, err := decodeCodebaseGraphArgs(raw)
	if err != nil {
		return "", fmt.Errorf("codebase_graph: invalid args: %w", err)
	}
	operationCtx, cancel := context.WithTimeout(ctx, time.Duration(args.deadlineMS)*time.Millisecond)
	defer cancel()
	operation, refusal, err := s.prepareCodebaseGraph(operationCtx, args)
	if err != nil {
		return "", err
	}
	if refusal != "" {
		return refusal, nil
	}
	freshness, disposition, refusal, err := s.codebaseGraphFreshnessResponse(operationCtx, operation, args.AfterBarrier)
	if err != nil {
		return "", err
	}
	if refusal != "" {
		return refusal, nil
	}
	response, refusal, err := s.codebaseGraphApplicationResponse(operationCtx, operation)
	if err != nil {
		return "", err
	}
	if refusal != "" {
		return refusal, nil
	}
	if response.Status == uci.QueryStatusContextRequired || response.Status == uci.QueryStatusForbidden {
		return s.releaseCodebaseGraphResponse(operationCtx, args.ContextHandle, operation, response)
	}
	response, err = codebaseGraphWithFreshness(response, freshness, disposition)
	if err != nil {
		return "", err
	}
	return s.releaseCodebaseGraphResponse(operationCtx, args.ContextHandle, operation, response)
}

func (s *Server) prepareCodebaseGraph(ctx context.Context, args codebaseGraphArgs) (codebaseGraphOperation, string, error) {
	application, authorized, epoch, contextCode := s.resolveCodebaseGraphContext(ctx, args.ContextHandle)
	if contextCode != "" {
		return codebaseGraphContextRefusal(ctx, contextCode)
	}
	deadline, _ := ctx.Deadline()
	input, matchesContext := args.graphInput(authorized.Ref(), deadline)
	if !matchesContext {
		return codebaseGraphContextRefusal(ctx, uci.ContextMismatch)
	}
	if contextCode = resolveCodebaseGraphCompatibilityEvidence(ctx, application, authorized, args.Project); contextCode != "" {
		return codebaseGraphContextRefusal(ctx, contextCode)
	}
	if !s.codebaseContextEpochCurrent(epoch) {
		return codebaseGraphContextRefusal(ctx, uci.ContextMismatch)
	}
	return codebaseGraphOperation{application: application, authorized: authorized, epoch: epoch, input: input}, "", nil
}

func codebaseGraphContextRefusal(ctx context.Context, code uci.ContextErrorCode) (codebaseGraphOperation, string, error) {
	if err := ctx.Err(); err != nil {
		return codebaseGraphOperation{}, "", err
	}
	refusal, err := codebaseSearchContextRefusal(code)
	return codebaseGraphOperation{}, refusal, err
}

func (s *Server) codebaseGraphFreshnessResponse(ctx context.Context, operation codebaseGraphOperation, afterBarrier *codebaseAfterBarrierArgs) (*uci.QueryFreshness, uci.QueryFreshnessDisposition, string, error) {
	freshness, disposition, err := codebaseGraphFreshness(ctx, operation.application, operation.authorized, afterBarrier)
	if err != nil {
		if code, isContextFailure := codebaseContextErrorCode(err); isContextFailure {
			_, refusal, refusalErr := codebaseGraphContextRefusal(ctx, code)
			return nil, "", refusal, refusalErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, "", "", ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, "", "", err
		}
		return nil, "", "", errors.New("codebase_graph: UCI freshness unavailable")
	}
	if !s.codebaseContextEpochCurrent(operation.epoch) {
		_, refusal, refusalErr := codebaseGraphContextRefusal(ctx, uci.ContextMismatch)
		return nil, "", refusal, refusalErr
	}
	return freshness, disposition, "", nil
}

func (s *Server) codebaseGraphApplicationResponse(ctx context.Context, operation codebaseGraphOperation) (uci.QueryResponse, string, error) {
	response, err := operation.application.ExploreCodebase(ctx, operation.authorized, operation.input)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return uci.QueryResponse{}, "", ctxErr
		}
		if code, isContextFailure := codebaseContextErrorCode(err); isContextFailure {
			_, refusal, refusalErr := codebaseGraphContextRefusal(ctx, code)
			return uci.QueryResponse{}, refusal, refusalErr
		}
		return uci.QueryResponse{}, "", errors.New("codebase_graph: UCI application unavailable")
	}
	if !s.codebaseContextEpochCurrent(operation.epoch) || !codebaseGraphResponseMatchesContext(response, operation.authorized) {
		_, refusal, refusalErr := codebaseGraphContextRefusal(ctx, uci.ContextMismatch)
		return uci.QueryResponse{}, refusal, refusalErr
	}
	return response, "", nil
}

func codebaseGraphWithFreshness(response uci.QueryResponse, freshness *uci.QueryFreshness, disposition uci.QueryFreshnessDisposition) (uci.QueryResponse, error) {
	if freshness == nil || (response.Freshness != nil && response.Freshness.State == uci.QueryFreshnessHistorical) {
		return response, nil
	}
	if disposition == uci.QueryFreshnessDispositionOffline && !codebaseOfflineResponseRecordable(response) {
		return uci.QueryResponse{}, errors.New("codebase_graph: invalid offline UCI response")
	}
	response.Freshness = freshness
	if disposition == uci.QueryFreshnessDispositionStale {
		switch response.Status {
		case uci.QueryStatusOK, uci.QueryStatusEmpty, uci.QueryStatusPartial, uci.QueryStatusStale:
			response.Status = uci.QueryStatusStale
		}
	}
	return response, nil
}

func (s *Server) releaseCodebaseGraphResponse(ctx context.Context, contextHandle *string, operation codebaseGraphOperation, response uci.QueryResponse) (string, error) {
	return s.releaseCodebaseQueryResponse(codebaseQueryReleaseInput{
		ctx: ctx, epoch: operation.epoch, authorized: operation.authorized, contextHandle: contextHandle, operation: uci.ExposureOperationCodeGraph, response: response,
		matches: func(candidate uci.QueryResponse) bool {
			return validCodebaseGraphPreExposureResponse(candidate, operation.authorized, operation.input)
		}, tool: "codebase_graph",
	})
}

func decodeCodebaseGraphArgs(raw json.RawMessage) (codebaseGraphArgs, error) {
	var args codebaseGraphArgs
	fields, err := decodeStrictCodebaseArgs(raw, &args)
	if err != nil {
		return codebaseGraphArgs{}, err
	}
	_, args.hasContextHandle = fields["context_handle"]
	_, args.hasProject = fields["project"]
	_, args.hasDestination = fields["destination"]
	_, args.hasDirection = fields["direction"]
	_, args.hasRelations = fields["relations"]
	_, args.hasEvidenceKinds = fields["evidence_kinds"]
	_, args.hasMaxDepth = fields["max_depth"]
	_, args.hasMaxVisited = fields["max_visited"]
	_, args.hasMaxNodes = fields["max_nodes"]
	_, args.hasMaxEdges = fields["max_edges"]
	_, args.hasDeadlineMS = fields["deadline_ms"]
	_, args.hasContinuation = fields["continuation"]
	_, args.hasAfterBarrier = fields["after_barrier"]

	if args.hasContextHandle && (args.ContextHandle == nil || !validCodebaseContextHandle(*args.ContextHandle)) {
		return codebaseGraphArgs{}, errors.New("invalid context handle")
	}
	if args.hasProject && args.Project == nil {
		return codebaseGraphArgs{}, errors.New("invalid project")
	}
	if args.Action == nil {
		return codebaseGraphArgs{}, errors.New("action is required")
	}
	if args.hasDestination && args.Destination == nil {
		return codebaseGraphArgs{}, errors.New("invalid destination")
	}
	if args.hasDirection && args.Direction == nil {
		return codebaseGraphArgs{}, errors.New("invalid direction")
	}
	if args.hasRelations && args.Relations == nil {
		return codebaseGraphArgs{}, errors.New("invalid relations")
	}
	if args.hasEvidenceKinds && args.EvidenceKinds == nil {
		return codebaseGraphArgs{}, errors.New("invalid evidence_kinds")
	}
	if err := validateCodebaseAfterBarrier(args.AfterBarrier, args.hasAfterBarrier); err != nil {
		return codebaseGraphArgs{}, err
	}
	if err := args.normalize(); err != nil {
		return codebaseGraphArgs{}, err
	}
	return args, nil
}

func (args *codebaseGraphArgs) normalize() error {
	action, err := normalizeCodebaseGraphAction(*args.Action)
	if err != nil {
		return err
	}
	target, err := normalizeCodebaseGraphTarget(args.Target)
	if err != nil {
		return fmt.Errorf("target: %w", err)
	}
	var destination *codebaseGraphTarget
	if args.Destination != nil {
		normalized, err := normalizeCodebaseGraphTarget(args.Destination)
		if err != nil {
			return fmt.Errorf("destination: %w", err)
		}
		destination = &normalized
	}
	if action == uci.GraphActionPath {
		if destination == nil {
			return errors.New("path requires destination")
		}
	} else if destination != nil {
		return errors.New("destination is only valid for path")
	}

	filter, err := normalizeCodebaseGraphFilter(args.Direction, args.Relations, args.EvidenceKinds)
	if err != nil {
		return err
	}
	maxDepth, err := normalizeCodebaseGraphBound(args.MaxDepth, args.hasMaxDepth, codebaseGraphDefaultMaxDepth, 0, codebaseGraphMaxDepth, "max_depth")
	if err != nil {
		return err
	}
	maxVisited, err := normalizeCodebaseGraphBound(args.MaxVisited, args.hasMaxVisited, codebaseGraphDefaultMaxVisited, 1, codebaseGraphMaxVisited, "max_visited")
	if err != nil {
		return err
	}
	maxNodes, err := normalizeCodebaseGraphBound(args.MaxNodes, args.hasMaxNodes, codebaseGraphDefaultMaxNodes, 1, codebaseGraphMaxNodes, "max_nodes")
	if err != nil {
		return err
	}
	maxEdges, err := normalizeCodebaseGraphBound(args.MaxEdges, args.hasMaxEdges, codebaseGraphDefaultMaxEdges, 1, codebaseGraphMaxEdges, "max_edges")
	if err != nil {
		return err
	}
	deadlineMS, err := normalizeCodebaseGraphDeadline(args.DeadlineMS, args.hasDeadlineMS)
	if err != nil {
		return err
	}
	var continuation *string
	if args.hasContinuation {
		if args.Continuation == nil || !validCodebaseGraphContinuation(*args.Continuation) {
			return errors.New("invalid continuation")
		}
		value := *args.Continuation
		continuation = &value
	}

	args.action = action
	args.target = target
	args.destination = destination
	args.filter = filter
	args.maxDepth = maxDepth
	args.maxVisited = maxVisited
	args.maxNodes = maxNodes
	args.maxEdges = maxEdges
	args.deadlineMS = deadlineMS
	args.continuation = continuation
	return nil
}

func normalizeCodebaseGraphAction(value string) (uci.GraphAction, error) {
	action := uci.GraphAction(strings.ToLower(strings.TrimSpace(value)))
	switch action {
	case uci.GraphActionExplain, uci.GraphActionNeighbors, uci.GraphActionPath, uci.GraphActionImpact, uci.GraphActionFlow, uci.GraphActionCycles:
		return action, nil
	default:
		return "", errors.New("invalid action")
	}
}

func normalizeCodebaseGraphTarget(args *codebaseGraphTargetArgs) (codebaseGraphTarget, error) {
	if args == nil || args.SourceID == nil || args.ViewID == nil {
		return codebaseGraphTarget{}, errors.New("source_id and view_id are required")
	}
	if (args.EntityKey == nil) == (args.Name == nil) {
		return codebaseGraphTarget{}, errors.New("provide exactly one entity_key or name")
	}

	target := uci.GraphTarget{}
	selector := ""
	if args.EntityKey != nil {
		selector = strings.TrimSpace(*args.EntityKey)
		target.EntityKey = selector
	} else {
		selector = strings.TrimSpace(*args.Name)
		target.Name = selector
	}
	if err := (uci.QueryEntityRef{
		SourceID:  *args.SourceID,
		ViewID:    *args.ViewID,
		EntityKey: selector,
	}).Validate(); err != nil {
		return codebaseGraphTarget{}, errors.New("invalid source_id, view_id, or target selector")
	}
	return codebaseGraphTarget{
		sourceID: *args.SourceID,
		viewID:   *args.ViewID,
		target:   target,
	}, nil
}

func normalizeCodebaseGraphFilter(direction *string, relations, evidenceKinds []string) (uci.GraphFilter, error) {
	normalizedDirection := uci.GraphDirectionBoth
	if direction != nil {
		normalizedDirection = uci.GraphDirection(strings.ToLower(strings.TrimSpace(*direction)))
	}
	switch normalizedDirection {
	case uci.GraphDirectionIncoming, uci.GraphDirectionOutgoing, uci.GraphDirectionBoth:
	default:
		return uci.GraphFilter{}, errors.New("invalid direction")
	}

	normalizedRelations, err := normalizeCodebaseGraphRelations(relations)
	if err != nil {
		return uci.GraphFilter{}, err
	}
	normalizedEvidenceKinds, err := normalizeCodebaseGraphEvidenceKinds(evidenceKinds)
	if err != nil {
		return uci.GraphFilter{}, err
	}
	return uci.GraphFilter{
		Direction:     normalizedDirection,
		Relations:     normalizedRelations,
		EvidenceKinds: normalizedEvidenceKinds,
	}, nil
}

func normalizeCodebaseGraphRelations(values []string) ([]uci.IndexRelation, error) {
	if len(values) > codebaseGraphMaxRelations {
		return nil, errors.New("relation filter exceeds limit")
	}
	normalized := make([]uci.IndexRelation, 0, len(values))
	for _, value := range values {
		relation := uci.IndexRelation(strings.ToLower(strings.TrimSpace(value)))
		if !validCodebaseGraphRelation(relation) {
			return nil, errors.New("invalid relation filter")
		}
		normalized = append(normalized, relation)
	}
	sort.Slice(normalized, func(left, right int) bool {
		return normalized[left] < normalized[right]
	})
	return uniqueCodebaseGraphRelations(normalized), nil
}

func normalizeCodebaseGraphEvidenceKinds(values []string) ([]uci.QueryEvidenceKind, error) {
	if len(values) > codebaseGraphMaxEvidenceKinds {
		return nil, errors.New("evidence filter exceeds limit")
	}
	normalized := make([]uci.QueryEvidenceKind, 0, len(values))
	for _, value := range values {
		kind := uci.QueryEvidenceKind(strings.ToUpper(strings.TrimSpace(value)))
		switch kind {
		case uci.QueryEvidenceExtracted, uci.QueryEvidenceResolved, uci.QueryEvidenceHeuristic, uci.QueryEvidenceSemantic:
		default:
			return nil, errors.New("invalid evidence filter")
		}
		normalized = append(normalized, kind)
	}
	sort.Slice(normalized, func(left, right int) bool {
		return normalized[left] < normalized[right]
	})
	return uniqueCodebaseGraphEvidenceKinds(normalized), nil
}

func normalizeCodebaseGraphBound(value *int, present bool, fallback, minimum, maximum int, name string) (int, error) {
	if !present {
		return fallback, nil
	}
	if value == nil || *value < minimum || *value > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return *value, nil
}

func normalizeCodebaseGraphDeadline(value *int64, present bool) (int64, error) {
	if !present {
		return codebaseGraphDefaultDeadlineMS, nil
	}
	if value == nil || *value < 1 || *value > codebaseGraphMaxDeadlineMS {
		return 0, fmt.Errorf("deadline_ms must be between 1 and %d", codebaseGraphMaxDeadlineMS)
	}
	return *value, nil
}

func validCodebaseGraphContinuation(value string) bool {
	return len(value) > 0 && len(value) <= codebaseGraphMaxContinuation && utf8.ValidString(value) && codebaseContextIdentityText(value)
}

func (args codebaseGraphArgs) graphInput(ref uci.ContextRef, deadline time.Time) (CodebaseGraphInput, bool) {
	if args.target.sourceID != ref.SourceID || args.target.viewID != ref.ViewID {
		return CodebaseGraphInput{}, false
	}
	var destination *uci.GraphTarget
	if args.destination != nil {
		if args.destination.sourceID != ref.SourceID || args.destination.viewID != ref.ViewID {
			return CodebaseGraphInput{}, false
		}
		value := args.destination.target
		destination = &value
	}
	return CodebaseGraphInput{
		Action:      args.action,
		Target:      args.target.target,
		Destination: destination,
		Filter:      args.filter,
		Budget: uci.GraphBudget{
			MaxDepth:   args.maxDepth,
			MaxVisited: args.maxVisited,
			MaxNodes:   args.maxNodes,
			MaxEdges:   args.maxEdges,
			Deadline:   deadline,
		},
		Continuation: args.continuation,
	}, true
}

func uniqueCodebaseGraphRelations(values []uci.IndexRelation) []uci.IndexRelation {
	if len(values) == 0 {
		return nil
	}
	unique := values[:0]
	for _, value := range values {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}

func uniqueCodebaseGraphEvidenceKinds(values []uci.QueryEvidenceKind) []uci.QueryEvidenceKind {
	if len(values) == 0 {
		return nil
	}
	unique := values[:0]
	for _, value := range values {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}

func validCodebaseGraphRelation(relation uci.IndexRelation) bool {
	switch relation {
	case "contains", "imports", "exports", "references", "calls", "may_call", "inherits", "implements", "documents", "mentions", "configures", "schema_references", "tests", "depends_on":
		return true
	default:
		return false
	}
}

func codebaseGraphRelationNames() []string {
	return []string{
		"contains",
		"imports",
		"exports",
		"references",
		"calls",
		"may_call",
		"inherits",
		"implements",
		"documents",
		"mentions",
		"configures",
		"schema_references",
		"tests",
		"depends_on",
	}
}

func (s *Server) resolveCodebaseGraphContext(ctx context.Context, contextHandle *string) (CodebaseGraphApplication, uci.AuthorizedContext, uint64, uci.ContextErrorCode) {
	application, authorized, epoch, contextCode := s.resolveCodebaseAuthorizedView(ctx, contextHandle)
	if contextCode != "" {
		return nil, uci.AuthorizedContext{}, 0, contextCode
	}
	graph, ok := application.(CodebaseGraphApplication)
	if !ok {
		return nil, uci.AuthorizedContext{}, 0, uci.ContextRequired
	}
	return graph, authorized, epoch, ""
}

func resolveCodebaseGraphCompatibilityEvidence(ctx context.Context, application CodebaseGraphApplication, authorized uci.AuthorizedContext, project *string) uci.ContextErrorCode {
	if project == nil {
		return ""
	}
	intelligence, ok := application.(CodebaseIntelligenceApplication)
	if !ok {
		return uci.ContextRequired
	}
	return resolveCodebaseCompatibilityEvidence(ctx, intelligence, authorized, project)
}

func codebaseGraphFreshness(ctx context.Context, application CodebaseGraphApplication, authorized uci.AuthorizedContext, afterBarrier *codebaseAfterBarrierArgs) (*uci.QueryFreshness, uci.QueryFreshnessDisposition, error) {
	intelligence, ok := application.(CodebaseIntelligenceApplication)
	if !ok {
		if afterBarrier != nil {
			return nil, "", errors.New("UCI freshness capability is unavailable")
		}
		return nil, "", nil
	}
	return codebaseFreshness(ctx, intelligence, authorized, afterBarrier)
}

func (s *Server) hasCodebaseGraphApplication() bool {
	s.codebaseContextMu.Lock()
	defer s.codebaseContextMu.Unlock()
	_, ok := s.codebaseContextApplication.(CodebaseGraphApplication)
	return ok
}

func codebaseGraphResponseMatchesContext(response uci.QueryResponse, authorized uci.AuthorizedContext) bool {
	switch response.Status {
	case uci.QueryStatusContextRequired, uci.QueryStatusForbidden:
		return true
	case uci.QueryStatusOK, uci.QueryStatusEmpty, uci.QueryStatusPartial, uci.QueryStatusStale, uci.QueryStatusUnavailable:
		return codebaseQueryResponseHasExactContext(response, authorized)
	default:
		return false
	}
}

func validCodebaseGraphPreExposureResponse(response uci.QueryResponse, authorized uci.AuthorizedContext, input CodebaseGraphInput) bool {
	if response.Exposure != nil {
		return false
	}
	switch response.Status {
	case uci.QueryStatusUnavailable:
		return response.Retrieval != nil && response.Retrieval.Mode == uci.QueryRetrievalGraph &&
			response.Items != nil && len(*response.Items) == 0 &&
			response.Graph == nil && codebaseQueryResponseHasExactContext(response, authorized)
	case uci.QueryStatusOK, uci.QueryStatusEmpty, uci.QueryStatusPartial, uci.QueryStatusStale:
		return response.Retrieval != nil && response.Retrieval.Mode == uci.QueryRetrievalGraph &&
			response.Items != nil && len(*response.Items) == 0 &&
			response.Graph != nil && len(response.Graph.Nodes) <= input.Budget.MaxNodes &&
			len(response.Graph.Edges) <= input.Budget.MaxEdges && codebaseQueryResponseHasExactContext(response, authorized)
	default:
		return false
	}
}
