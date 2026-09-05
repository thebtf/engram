package uci

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"unicode/utf8"
)

const (
	// QueryResponseSchema is the only query-response schema accepted at this boundary.
	QueryResponseSchema = "engram.code-query/1"

	queryMaxContexts       = 8
	queryMaxItems          = 50
	queryMaxGraphNodes     = 200
	queryMaxGraphEdges     = 400
	queryMaxWarnings       = 32
	queryMaxBarrierPaths   = 50
	queryMaxBarrierWaitMS  = 60_000
	queryMaxWatermark      = int64(9_007_199_254_740_991)
	queryMaxContinuation   = 2_048
	queryMaxReason         = 128
	queryMaxEntityKey      = 256
	queryMaxPath           = 4_096
	queryMaxLanguage       = 64
	queryMaxExcerpt        = 8_192
	queryMaxExplanation    = 2_048
	queryMaxExposureRef    = 128
	queryMaxWarning        = 2_048
	queryMinExposureRefLen = 9
)

// QueryResponseStatus is the closed result-state vocabulary for one UCI query.
type QueryResponseStatus string

const (
	QueryStatusOK              QueryResponseStatus = "ok"
	QueryStatusEmpty           QueryResponseStatus = "empty"
	QueryStatusPartial         QueryResponseStatus = "partial"
	QueryStatusStale           QueryResponseStatus = "stale"
	QueryStatusUnavailable     QueryResponseStatus = "unavailable"
	QueryStatusContextRequired QueryResponseStatus = "context_required"
	QueryStatusForbidden       QueryResponseStatus = "forbidden"
)

// QueryResponse is the closed serialization boundary for a UCI query result.
// Pointer fields preserve an omitted contextual field separately from an explicit
// null where the contract permits one.
type QueryResponse struct {
	Schema       string              `json:"schema"`
	Status       QueryResponseStatus `json:"status"`
	Contexts     *QueryContexts      `json:"contexts,omitempty"`
	Freshness    *QueryFreshness     `json:"freshness,omitempty"`
	Retrieval    *QueryRetrieval     `json:"retrieval,omitempty"`
	Coverage     *QueryCoverage      `json:"coverage,omitempty"`
	Exposure     *QueryExposure      `json:"exposure"`
	Error        *QueryError         `json:"error,omitempty"`
	Items        *QueryItems         `json:"items,omitempty"`
	Graph        *QueryGraph         `json:"graph,omitempty"`
	Truncated    *bool               `json:"truncated,omitempty"`
	Warnings     *QueryWarnings      `json:"warnings,omitempty"`
	Continuation *QueryContinuation  `json:"continuation,omitempty"`
}

// QueryContexts is present only for an authorized contextual result envelope.
type QueryContexts []QueryContextRef

// QueryItems is present only for an authorized contextual result envelope.
type QueryItems []QueryItem

// QueryWarnings is present only for an authorized contextual result envelope.
type QueryWarnings []string

// QueryContextRef is the JSON representation of one selected immutable context.
type QueryContextRef struct {
	SourceID   string  `json:"source_id"`
	CheckoutID string  `json:"checkout_id"`
	ViewID     string  `json:"view_id"`
	Generation int64   `json:"generation"`
	ProfileID  string  `json:"profile_id"`
	SpaceID    *string `json:"space_id,omitempty"`
}

// QueryFreshness describes the bounded freshness evidence for a selected view.
type QueryFreshness struct {
	State               QueryFreshnessState      `json:"state"`
	Method              QueryFreshnessMethod     `json:"method"`
	PendingChanges      *int64                   `json:"pending_changes"`
	EnrichmentWatermark QueryEnrichmentWatermark `json:"enrichment_watermark"`
	Barrier             *QueryBarrier            `json:"barrier"`
}

// QueryFreshnessState is the closed freshness-state vocabulary.
type QueryFreshnessState string

const (
	QueryFreshnessObservedCurrent QueryFreshnessState = "observed_current"
	QueryFreshnessCatchingUp      QueryFreshnessState = "catching_up"
	QueryFreshnessOffline         QueryFreshnessState = "offline"
	QueryFreshnessUnknown         QueryFreshnessState = "unknown"
	QueryFreshnessHistorical      QueryFreshnessState = "historical"
)

// QueryFreshnessMethod is the evidence method used to establish freshness.
type QueryFreshnessMethod string

const (
	QueryFreshnessWatchWatermark  QueryFreshnessMethod = "watch_watermark"
	QueryFreshnessPathHashBarrier QueryFreshnessMethod = "path_hash_barrier"
	QueryFreshnessFullReconcile   QueryFreshnessMethod = "full_reconcile"
	QueryFreshnessPinnedHistory   QueryFreshnessMethod = "pinned_history"
	QueryFreshnessNone            QueryFreshnessMethod = "none"
)

// QueryEnrichmentWatermark is a bounded selected-view enrichment watermark.
type QueryEnrichmentWatermark struct {
	Sequence int64                `json:"sequence"`
	State    QueryEnrichmentState `json:"state"`
}

// QueryEnrichmentState is the closed enrichment-watermark vocabulary.
type QueryEnrichmentState string

const (
	QueryEnrichmentCurrent     QueryEnrichmentState = "current"
	QueryEnrichmentPending     QueryEnrichmentState = "pending"
	QueryEnrichmentUnavailable QueryEnrichmentState = "unavailable"
)

// QueryBarrier is the non-content result of a read-your-save barrier.
type QueryBarrier struct {
	Scope      QueryBarrierScope `json:"scope"`
	DeadlineMS int64             `json:"deadline_ms"`
	State      QueryBarrierState `json:"state"`
}

// QueryBarrierScope names only the kind and size of the scoped barrier input.
type QueryBarrierScope struct {
	Kind      QueryBarrierScopeKind `json:"kind"`
	PathCount int64                 `json:"path_count"`
}

// QueryBarrierScopeKind is the closed barrier-scope vocabulary.
type QueryBarrierScopeKind string

const (
	QueryBarrierPaths           QueryBarrierScopeKind = "paths"
	QueryBarrierPathsWithHashes QueryBarrierScopeKind = "paths_with_hashes"
)

// QueryBarrierState is the closed barrier-outcome vocabulary.
type QueryBarrierState string

const (
	QueryBarrierSatisfied QueryBarrierState = "satisfied"
	QueryBarrierStale     QueryBarrierState = "stale"
	QueryBarrierTimedOut  QueryBarrierState = "timed_out"
)

// QueryRetrieval describes the mode and degradation evidence behind the result.
type QueryRetrieval struct {
	Mode               QueryRetrievalMode `json:"mode"`
	VectorCoverage     *float64           `json:"vector_coverage"`
	DegradationReasons []string           `json:"degradation_reasons"`
}

// QueryRetrievalMode is the closed retrieval-mode vocabulary.
type QueryRetrievalMode string

const (
	QueryRetrievalExact       QueryRetrievalMode = "exact"
	QueryRetrievalLexical     QueryRetrievalMode = "lexical"
	QueryRetrievalHybrid      QueryRetrievalMode = "hybrid"
	QueryRetrievalGraph       QueryRetrievalMode = "graph"
	QueryRetrievalUnavailable QueryRetrievalMode = "unavailable"
)

// QueryCoverage records structural coverage independently from retrieval and completion.
type QueryCoverage struct {
	Structural       IndexCoverageState `json:"structural"`
	UnresolvedSites  *int64             `json:"unresolved_sites"`
	UnsupportedFiles *int64             `json:"unsupported_files"`
}

// QueryExposure is the only receipt exposed to the query caller.
type QueryExposure struct {
	ExposureRef     string               `json:"exposure_ref"`
	CompletionState QueryCompletionState `json:"completion_state"`
}

// QueryCompletionState is evidence from a verified supported-host callback.
type QueryCompletionState string

const (
	QueryCompletionUnknown   QueryCompletionState = "unknown"
	QueryCompletionSucceeded QueryCompletionState = "succeeded"
	QueryCompletionPartial   QueryCompletionState = "partial"
	QueryCompletionFailed    QueryCompletionState = "failed"
	QueryCompletionAbandoned QueryCompletionState = "abandoned"
)

// QueryError is a deliberately non-diagnostic query outcome.
type QueryError struct {
	Code QueryErrorCode `json:"code"`
}

// QueryErrorCode is the closed query error vocabulary. Completion callback-only
// failures are intentionally excluded from this type.
type QueryErrorCode string

const (
	QueryErrorContextRequired     QueryErrorCode = "CONTEXT_REQUIRED"
	QueryErrorContextMismatch     QueryErrorCode = "CONTEXT_MISMATCH"
	QueryErrorViewRetired         QueryErrorCode = "VIEW_RETIRED"
	QueryErrorSourceUnavailable   QueryErrorCode = "SOURCE_UNAVAILABLE"
	QueryErrorCheckoutOffline     QueryErrorCode = "CHECKOUT_OFFLINE"
	QueryErrorIndexCatchingUp     QueryErrorCode = "INDEX_CATCHING_UP"
	QueryErrorParserUnsupported   QueryErrorCode = "PARSER_UNSUPPORTED"
	QueryErrorParserPartial       QueryErrorCode = "PARSER_PARTIAL"
	QueryErrorVectorUnavailable   QueryErrorCode = "VECTOR_UNAVAILABLE"
	QueryErrorProfileMismatch     QueryErrorCode = "PROFILE_MISMATCH"
	QueryErrorBudgetExceeded      QueryErrorCode = "BUDGET_EXCEEDED"
	QueryErrorLeaseStale          QueryErrorCode = "LEASE_STALE"
	QueryErrorBuildIncomplete     QueryErrorCode = "BUILD_INCOMPLETE"
	QueryErrorPermissionDenied    QueryErrorCode = "PERMISSION_DENIED"
	QueryErrorExposureUnavailable QueryErrorCode = "EXPOSURE_UNAVAILABLE"
	QueryErrorIdempotencyMismatch QueryErrorCode = "IDEMPOTENCY_MISMATCH"
)

// QueryEntityRef is an entity scoped to one selected source and view.
type QueryEntityRef struct {
	SourceID  string `json:"source_id"`
	ViewID    string `json:"view_id"`
	EntityKey string `json:"entity_key"`
}

// QuerySpan identifies a non-empty byte span and its inclusive line range.
type QuerySpan struct {
	ByteStart int64 `json:"byte_start"`
	ByteEnd   int64 `json:"byte_end"`
	LineStart int64 `json:"line_start"`
	LineEnd   int64 `json:"line_end"`
}

// QueryContentDigest is a bare lowercase SHA-256 digest of an item body.
type QueryContentDigest string

// QueryItemKind is the closed returned-content vocabulary.
type QueryItemKind string

const (
	QueryItemCode     QueryItemKind = "code"
	QueryItemDocument QueryItemKind = "document"
	QueryItemSchema   QueryItemKind = "schema"
	QueryItemConfig   QueryItemKind = "config"
)

// QueryMatchSource is the closed provenance vocabulary for one item.
type QueryMatchSource string

const (
	QueryMatchExact  QueryMatchSource = "exact"
	QueryMatchFTS    QueryMatchSource = "fts"
	QueryMatchVector QueryMatchSource = "vector"
	QueryMatchGraph  QueryMatchSource = "graph"
)

// QueryItem is one bounded, context-scoped query hit.
type QueryItem struct {
	Ref           QueryEntityRef     `json:"ref"`
	Path          string             `json:"path"`
	Span          QuerySpan          `json:"span"`
	ContentDigest QueryContentDigest `json:"content_digest"`
	Kind          QueryItemKind      `json:"kind"`
	Language      string             `json:"language"`
	Excerpt       string             `json:"excerpt"`
	MatchSources  []QueryMatchSource `json:"match_sources"`
	Score         *float64           `json:"score,omitempty"`
}

// QueryGraph is an optional bounded graph result in a contextual response.
type QueryGraph struct {
	Nodes      []QueryEntityRef     `json:"nodes"`
	Edges      []QueryGraphEdge     `json:"edges"`
	StopReason QueryGraphStopReason `json:"stop_reason"`
}

// QueryGraphEdge records a typed graph relation with scoped evidence references.
type QueryGraphEdge struct {
	From         QueryEntityRef    `json:"from"`
	To           QueryEntityRef    `json:"to"`
	Relation     IndexRelation     `json:"relation"`
	EvidenceKind QueryEvidenceKind `json:"evidence_kind"`
	EvidenceRefs []QueryEntityRef  `json:"evidence_refs"`
	Explanation  *string           `json:"explanation,omitempty"`
}

// QueryEvidenceKind is the closed graph-evidence vocabulary exposed to callers.
type QueryEvidenceKind string

const (
	QueryEvidenceExtracted QueryEvidenceKind = "EXTRACTED"
	QueryEvidenceResolved  QueryEvidenceKind = "RESOLVED"
	QueryEvidenceHeuristic QueryEvidenceKind = "HEURISTIC"
	QueryEvidenceSemantic  QueryEvidenceKind = "SEMANTIC"
)

// QueryGraphStopReason is the closed graph traversal stopping vocabulary.
type QueryGraphStopReason string

const (
	QueryGraphComplete    QueryGraphStopReason = "complete"
	QueryGraphDepthCap    QueryGraphStopReason = "depth_cap"
	QueryGraphNodeCap     QueryGraphStopReason = "node_cap"
	QueryGraphDeadline    QueryGraphStopReason = "deadline"
	QueryGraphCoverageGap QueryGraphStopReason = "coverage_gap"
)

// QueryContinuation preserves an explicit null continuation separately from an
// omitted continuation field in a suppressed failure envelope.
type QueryContinuation struct {
	Value *string `json:"-"`
}

// UnmarshalJSON rejects unknown response members while preserving required nulls
// and optional omissions in the QueryResponse DTO.
func (response *QueryResponse) UnmarshalJSON(data []byte) error {
	var wire struct {
		Schema       json.RawMessage `json:"schema"`
		Status       json.RawMessage `json:"status"`
		Contexts     json.RawMessage `json:"contexts"`
		Freshness    json.RawMessage `json:"freshness"`
		Retrieval    json.RawMessage `json:"retrieval"`
		Coverage     json.RawMessage `json:"coverage"`
		Exposure     json.RawMessage `json:"exposure"`
		Error        json.RawMessage `json:"error"`
		Items        json.RawMessage `json:"items"`
		Graph        json.RawMessage `json:"graph"`
		Truncated    json.RawMessage `json:"truncated"`
		Warnings     json.RawMessage `json:"warnings"`
		Continuation json.RawMessage `json:"continuation"`
	}
	if err := queryDecodeObject(data, &wire); err != nil {
		return fmt.Errorf("uci query response: %w", err)
	}

	schema, err := queryDecodeRequired[string](wire.Schema, "schema")
	if err != nil {
		return err
	}
	status, err := queryDecodeRequired[QueryResponseStatus](wire.Status, "status")
	if err != nil {
		return err
	}
	contexts, err := queryDecodeOptional[QueryContexts](wire.Contexts, "contexts")
	if err != nil {
		return err
	}
	freshness, err := queryDecodeOptional[QueryFreshness](wire.Freshness, "freshness")
	if err != nil {
		return err
	}
	retrieval, err := queryDecodeOptional[QueryRetrieval](wire.Retrieval, "retrieval")
	if err != nil {
		return err
	}
	coverage, err := queryDecodeOptional[QueryCoverage](wire.Coverage, "coverage")
	if err != nil {
		return err
	}
	exposure, err := queryDecodeNullable[QueryExposure](wire.Exposure, "exposure")
	if err != nil {
		return err
	}
	queryError, err := queryDecodeOptional[QueryError](wire.Error, "error")
	if err != nil {
		return err
	}
	items, err := queryDecodeOptional[QueryItems](wire.Items, "items")
	if err != nil {
		return err
	}
	graph, err := queryDecodeOptional[QueryGraph](wire.Graph, "graph")
	if err != nil {
		return err
	}
	truncated, err := queryDecodeOptional[bool](wire.Truncated, "truncated")
	if err != nil {
		return err
	}
	warnings, err := queryDecodeOptional[QueryWarnings](wire.Warnings, "warnings")
	if err != nil {
		return err
	}
	continuation, err := queryDecodeContinuation(wire.Continuation)
	if err != nil {
		return err
	}

	*response = QueryResponse{
		Schema:       schema,
		Status:       status,
		Contexts:     contexts,
		Freshness:    freshness,
		Retrieval:    retrieval,
		Coverage:     coverage,
		Exposure:     exposure,
		Error:        queryError,
		Items:        items,
		Graph:        graph,
		Truncated:    truncated,
		Warnings:     warnings,
		Continuation: continuation,
	}
	return nil
}

// Validate enforces the complete closed query state machine after decoding or
// before a response is emitted.
func (response QueryResponse) Validate() error {
	if response.Schema != QueryResponseSchema {
		return fmt.Errorf("uci query response: unsupported schema %q", response.Schema)
	}
	if !response.Status.valid() {
		return fmt.Errorf("uci query response: invalid status %q", response.Status)
	}

	switch response.Status {
	case QueryStatusOK, QueryStatusEmpty, QueryStatusPartial, QueryStatusStale:
		if response.Error != nil {
			return fmt.Errorf("uci query response: successful status %q cannot contain an error", response.Status)
		}
		if response.Exposure == nil {
			return fmt.Errorf("uci query response: successful status %q requires an exposure receipt", response.Status)
		}
		if err := response.Exposure.Validate(); err != nil {
			return err
		}
		if _, err := response.validateContextual(); err != nil {
			return err
		}
		if response.Status == QueryStatusEmpty && len(*response.Items) != 0 {
			return fmt.Errorf("uci query response: empty status cannot contain items")
		}
		return nil

	case QueryStatusUnavailable:
		if response.Error == nil {
			return fmt.Errorf("uci query response: unavailable status requires an error")
		}
		if err := response.Error.Validate(); err != nil {
			return err
		}
		if response.Error.Code.isRecorderFailure() {
			if response.Exposure != nil {
				return fmt.Errorf("uci query response: recorder failure cannot expose a receipt")
			}
			return response.validateSuppressed()
		}
		if !response.Error.Code.isAuthorizedUnavailable() {
			return fmt.Errorf("uci query response: unavailable status cannot use error %q", response.Error.Code)
		}
		if response.Exposure == nil {
			return fmt.Errorf("uci query response: authorized unavailable status requires an exposure receipt")
		}
		if err := response.Exposure.Validate(); err != nil {
			return err
		}
		if _, err := response.validateContextual(); err != nil {
			return err
		}
		if len(*response.Items) != 0 {
			return fmt.Errorf("uci query response: authorized unavailable status cannot contain items")
		}
		if response.Graph != nil {
			return fmt.Errorf("uci query response: authorized unavailable status cannot contain a graph")
		}
		return nil

	case QueryStatusContextRequired:
		if response.Exposure != nil {
			return fmt.Errorf("uci query response: context refusal cannot expose a receipt")
		}
		if err := response.validateSuppressed(); err != nil {
			return err
		}
		if response.Error == nil {
			return fmt.Errorf("uci query response: context refusal requires an error")
		}
		if err := response.Error.Validate(); err != nil {
			return err
		}
		if response.Error.Code != QueryErrorContextRequired && response.Error.Code != QueryErrorContextMismatch {
			return fmt.Errorf("uci query response: context refusal cannot use error %q", response.Error.Code)
		}
		return nil

	case QueryStatusForbidden:
		if response.Exposure != nil {
			return fmt.Errorf("uci query response: forbidden status cannot expose a receipt")
		}
		if err := response.validateSuppressed(); err != nil {
			return err
		}
		if response.Error == nil {
			return fmt.Errorf("uci query response: forbidden status requires an error")
		}
		if err := response.Error.Validate(); err != nil {
			return err
		}
		if response.Error.Code != QueryErrorPermissionDenied {
			return fmt.Errorf("uci query response: forbidden status cannot use error %q", response.Error.Code)
		}
		return nil
	}

	return fmt.Errorf("uci query response: invalid status %q", response.Status)
}

func (response QueryResponse) validateContextual() (queryContextSet, error) {
	if response.Contexts == nil {
		return nil, fmt.Errorf("uci query response: contextual envelope requires contexts")
	}
	if len(*response.Contexts) == 0 || len(*response.Contexts) > queryMaxContexts {
		return nil, fmt.Errorf("uci query response: contexts count is invalid")
	}
	contexts := make(queryContextSet, len(*response.Contexts))
	for _, contextRef := range *response.Contexts {
		if err := contextRef.Validate(); err != nil {
			return nil, err
		}
		key := contextRef.key()
		if _, duplicate := contexts[key]; duplicate {
			return nil, fmt.Errorf("uci query response: duplicate source/view context")
		}
		contexts[key] = struct{}{}
	}

	if response.Freshness == nil {
		return nil, fmt.Errorf("uci query response: contextual envelope requires freshness")
	}
	if err := response.Freshness.Validate(); err != nil {
		return nil, err
	}
	if response.Retrieval == nil {
		return nil, fmt.Errorf("uci query response: contextual envelope requires retrieval")
	}
	if err := response.Retrieval.Validate(); err != nil {
		return nil, err
	}
	if response.Coverage == nil {
		return nil, fmt.Errorf("uci query response: contextual envelope requires coverage")
	}
	if err := response.Coverage.Validate(); err != nil {
		return nil, err
	}
	if response.Items == nil {
		return nil, fmt.Errorf("uci query response: contextual envelope requires items")
	}
	if len(*response.Items) > queryMaxItems {
		return nil, fmt.Errorf("uci query response: item count exceeds limit")
	}
	for _, item := range *response.Items {
		if err := item.Validate(contexts); err != nil {
			return nil, err
		}
	}
	if response.Truncated == nil {
		return nil, fmt.Errorf("uci query response: contextual envelope requires truncated")
	}
	if response.Warnings == nil {
		return nil, fmt.Errorf("uci query response: contextual envelope requires warnings")
	}
	if len(*response.Warnings) > queryMaxWarnings {
		return nil, fmt.Errorf("uci query response: warning count exceeds limit")
	}
	for _, warning := range *response.Warnings {
		if !queryBoundedText(warning, 0, queryMaxWarning) {
			return nil, fmt.Errorf("uci query response: warning exceeds limit")
		}
	}
	if response.Continuation == nil {
		return nil, fmt.Errorf("uci query response: contextual envelope requires continuation")
	}
	if err := response.Continuation.Validate(); err != nil {
		return nil, err
	}
	if *response.Truncated {
		if response.Continuation.Value == nil {
			return nil, fmt.Errorf("uci query response: truncated response requires a continuation")
		}
	} else if response.Continuation.Value != nil {
		return nil, fmt.Errorf("uci query response: untruncated response cannot contain a continuation")
	}
	if response.Graph != nil {
		if err := response.Graph.Validate(contexts); err != nil {
			return nil, err
		}
	}
	return contexts, nil
}

func (response QueryResponse) validateSuppressed() error {
	if response.Contexts != nil || response.Freshness != nil || response.Retrieval != nil || response.Coverage != nil || response.Items != nil || response.Graph != nil || response.Truncated != nil || response.Warnings != nil || response.Continuation != nil {
		return fmt.Errorf("uci query response: suppressed response cannot disclose a contextual field")
	}
	return nil
}

// Validate checks the complete immutable context identity against the established
// UCI ContextRef validation rule.
func (ref QueryContextRef) Validate() error {
	contextRef := ContextRef{
		SpaceID:           ref.SpaceID,
		SourceID:          ref.SourceID,
		CheckoutID:        ref.CheckoutID,
		ViewID:            ref.ViewID,
		AnalysisProfileID: ref.ProfileID,
		Generation:        ref.Generation,
	}
	if !contextRef.valid() {
		return fmt.Errorf("uci query response: invalid context reference")
	}
	return nil
}

func (ref QueryContextRef) key() queryContextKey {
	return queryContextKey{SourceID: ref.SourceID, ViewID: ref.ViewID}
}

// Validate checks the freshness object, including its barrier coupling.
func (freshness QueryFreshness) Validate() error {
	if !freshness.State.valid() {
		return fmt.Errorf("uci query response: invalid freshness state %q", freshness.State)
	}
	if !freshness.Method.valid() {
		return fmt.Errorf("uci query response: invalid freshness method %q", freshness.Method)
	}
	if freshness.PendingChanges != nil && *freshness.PendingChanges < 0 {
		return fmt.Errorf("uci query response: pending changes cannot be negative")
	}
	if err := freshness.EnrichmentWatermark.Validate(); err != nil {
		return err
	}
	if freshness.Method == QueryFreshnessPathHashBarrier {
		if freshness.Barrier == nil {
			return fmt.Errorf("uci query response: path hash freshness requires a barrier")
		}
	} else if freshness.Barrier != nil {
		return fmt.Errorf("uci query response: barrier requires path hash freshness")
	}
	if freshness.Barrier != nil {
		if err := freshness.Barrier.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks the bounded enrichment watermark.
func (watermark QueryEnrichmentWatermark) Validate() error {
	if watermark.Sequence < 0 || watermark.Sequence > queryMaxWatermark {
		return fmt.Errorf("uci query response: enrichment sequence is out of range")
	}
	if !watermark.State.valid() {
		return fmt.Errorf("uci query response: invalid enrichment state %q", watermark.State)
	}
	return nil
}

// Validate checks the bounded barrier metadata.
func (barrier QueryBarrier) Validate() error {
	if err := barrier.Scope.Validate(); err != nil {
		return err
	}
	if barrier.DeadlineMS < 1 || barrier.DeadlineMS > queryMaxBarrierWaitMS {
		return fmt.Errorf("uci query response: barrier deadline is out of range")
	}
	if !barrier.State.valid() {
		return fmt.Errorf("uci query response: invalid barrier state %q", barrier.State)
	}
	return nil
}

// Validate checks the bounded non-content barrier scope.
func (scope QueryBarrierScope) Validate() error {
	if !scope.Kind.valid() {
		return fmt.Errorf("uci query response: invalid barrier scope %q", scope.Kind)
	}
	if scope.PathCount < 1 || scope.PathCount > queryMaxBarrierPaths {
		return fmt.Errorf("uci query response: barrier path count is out of range")
	}
	return nil
}

// Validate checks retrieval mode, vector coverage, and bounded degradation labels.
func (retrieval QueryRetrieval) Validate() error {
	if !retrieval.Mode.valid() {
		return fmt.Errorf("uci query response: invalid retrieval mode %q", retrieval.Mode)
	}
	if retrieval.VectorCoverage != nil {
		if math.IsNaN(*retrieval.VectorCoverage) || math.IsInf(*retrieval.VectorCoverage, 0) || *retrieval.VectorCoverage < 0 || *retrieval.VectorCoverage > 1 {
			return fmt.Errorf("uci query response: vector coverage is out of range")
		}
	}
	if retrieval.Mode == QueryRetrievalUnavailable && retrieval.VectorCoverage != nil {
		return fmt.Errorf("uci query response: unavailable retrieval cannot report vector coverage")
	}
	if retrieval.Mode == QueryRetrievalHybrid && retrieval.VectorCoverage == nil {
		return fmt.Errorf("uci query response: hybrid retrieval requires vector coverage")
	}
	if retrieval.DegradationReasons == nil {
		return fmt.Errorf("uci query response: retrieval requires degradation reasons")
	}
	for _, reason := range retrieval.DegradationReasons {
		if !queryBoundedText(reason, 0, queryMaxReason) {
			return fmt.Errorf("uci query response: degradation reason exceeds limit")
		}
	}
	return nil
}

// Validate checks coverage state and its conditional counts.
func (coverage QueryCoverage) Validate() error {
	switch coverage.Structural {
	case IndexCoverageComplete, IndexCoveragePartial:
		if coverage.UnresolvedSites == nil || coverage.UnsupportedFiles == nil {
			return fmt.Errorf("uci query response: available coverage requires counts")
		}
	case IndexCoverageUnavailable:
		if coverage.UnresolvedSites != nil || coverage.UnsupportedFiles != nil {
			return fmt.Errorf("uci query response: unavailable coverage cannot report counts")
		}
	default:
		return fmt.Errorf("uci query response: invalid structural coverage %q", coverage.Structural)
	}
	if coverage.UnresolvedSites != nil && *coverage.UnresolvedSites < 0 {
		return fmt.Errorf("uci query response: unresolved sites cannot be negative")
	}
	if coverage.UnsupportedFiles != nil && *coverage.UnsupportedFiles < 0 {
		return fmt.Errorf("uci query response: unsupported files cannot be negative")
	}
	return nil
}

// Validate checks the opaque receipt and supported-host completion outcome.
func (exposure QueryExposure) Validate() error {
	if !validQueryExposureRef(exposure.ExposureRef) {
		return fmt.Errorf("uci query response: invalid exposure reference")
	}
	if !exposure.CompletionState.valid() {
		return fmt.Errorf("uci query response: invalid completion state %q", exposure.CompletionState)
	}
	return nil
}

// Validate checks the closed error code.
func (queryError QueryError) Validate() error {
	if !queryError.Code.valid() {
		return fmt.Errorf("uci query response: invalid error code %q", queryError.Code)
	}
	return nil
}

// Validate checks one scoped entity reference.
func (ref QueryEntityRef) Validate() error {
	if !canonicalContextUUID(ref.SourceID) || !canonicalContextUUID(ref.ViewID) || !queryBoundedText(ref.EntityKey, 1, queryMaxEntityKey) {
		return fmt.Errorf("uci query response: invalid entity reference")
	}
	return nil
}

// Validate checks the non-empty source span.
func (span QuerySpan) Validate() error {
	if span.ByteStart < 0 || span.ByteEnd <= span.ByteStart {
		return fmt.Errorf("uci query response: invalid byte span")
	}
	if span.LineStart < 1 || span.LineEnd < span.LineStart {
		return fmt.Errorf("uci query response: invalid line span")
	}
	return nil
}

func (item QueryItem) Validate(contexts queryContextSet) error {
	if err := item.Ref.Validate(); err != nil {
		return err
	}
	if !contexts.contains(item.Ref) {
		return fmt.Errorf("uci query response: item reference is outside selected contexts")
	}
	if !queryBoundedText(item.Path, 1, queryMaxPath) {
		return fmt.Errorf("uci query response: item path is invalid")
	}
	if err := item.Span.Validate(); err != nil {
		return err
	}
	if !validQueryContentDigest(item.ContentDigest) {
		return fmt.Errorf("uci query response: item content digest is invalid")
	}
	if !item.Kind.valid() {
		return fmt.Errorf("uci query response: invalid item kind %q", item.Kind)
	}
	if !queryBoundedText(item.Language, 1, queryMaxLanguage) {
		return fmt.Errorf("uci query response: item language is invalid")
	}
	if !queryBoundedText(item.Excerpt, 0, queryMaxExcerpt) {
		return fmt.Errorf("uci query response: item excerpt exceeds limit")
	}
	if len(item.MatchSources) == 0 {
		return fmt.Errorf("uci query response: item requires match sources")
	}
	seen := make(map[QueryMatchSource]struct{}, len(item.MatchSources))
	for _, source := range item.MatchSources {
		if !source.valid() {
			return fmt.Errorf("uci query response: invalid match source %q", source)
		}
		if _, duplicate := seen[source]; duplicate {
			return fmt.Errorf("uci query response: duplicate match source %q", source)
		}
		seen[source] = struct{}{}
	}
	if item.Score != nil && (math.IsNaN(*item.Score) || math.IsInf(*item.Score, 0)) {
		return fmt.Errorf("uci query response: item score is invalid")
	}
	return nil
}

// Validate checks graph bounds, selected-context ownership, and endpoint closure.
func (graph QueryGraph) Validate(contexts queryContextSet) error {
	if len(graph.Nodes) > queryMaxGraphNodes {
		return fmt.Errorf("uci query response: graph node count exceeds limit")
	}
	if len(graph.Edges) > queryMaxGraphEdges {
		return fmt.Errorf("uci query response: graph edge count exceeds limit")
	}
	if !graph.StopReason.valid() {
		return fmt.Errorf("uci query response: invalid graph stop reason %q", graph.StopReason)
	}
	nodes := make(map[QueryEntityRef]struct{}, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if err := node.Validate(); err != nil {
			return err
		}
		if !contexts.contains(node) {
			return fmt.Errorf("uci query response: graph node is outside selected contexts")
		}
		if _, duplicate := nodes[node]; duplicate {
			return fmt.Errorf("uci query response: graph contains a duplicate node")
		}
		nodes[node] = struct{}{}
	}
	for _, edge := range graph.Edges {
		if err := edge.Validate(contexts, nodes); err != nil {
			return err
		}
	}
	return nil
}

func (edge QueryGraphEdge) Validate(contexts queryContextSet, nodes map[QueryEntityRef]struct{}) error {
	if err := edge.From.Validate(); err != nil {
		return err
	}
	if !contexts.contains(edge.From) {
		return fmt.Errorf("uci query response: graph edge source is outside selected contexts")
	}
	if _, found := nodes[edge.From]; !found {
		return fmt.Errorf("uci query response: graph edge source is not a graph node")
	}
	if err := edge.To.Validate(); err != nil {
		return err
	}
	if !contexts.contains(edge.To) {
		return fmt.Errorf("uci query response: graph edge target is outside selected contexts")
	}
	if _, found := nodes[edge.To]; !found {
		return fmt.Errorf("uci query response: graph edge target is not a graph node")
	}
	if !isIndexRelation(edge.Relation) {
		return fmt.Errorf("uci query response: invalid graph relation %q", edge.Relation)
	}
	if !edge.EvidenceKind.valid() {
		return fmt.Errorf("uci query response: invalid graph evidence kind %q", edge.EvidenceKind)
	}
	if len(edge.EvidenceRefs) == 0 {
		return fmt.Errorf("uci query response: graph edge requires evidence references")
	}
	for _, evidenceRef := range edge.EvidenceRefs {
		if err := evidenceRef.Validate(); err != nil {
			return err
		}
		if !contexts.contains(evidenceRef) {
			return fmt.Errorf("uci query response: graph evidence reference is outside selected contexts")
		}
	}
	if edge.Explanation != nil && !queryBoundedText(*edge.Explanation, 0, queryMaxExplanation) {
		return fmt.Errorf("uci query response: graph explanation exceeds limit")
	}
	return nil
}

// MarshalJSON emits the null continuation represented by a non-nil wrapper.
func (continuation QueryContinuation) MarshalJSON() ([]byte, error) {
	return json.Marshal(continuation.Value)
}

// UnmarshalJSON accepts only an explicit string or null continuation value.
func (continuation *QueryContinuation) UnmarshalJSON(data []byte) error {
	if queryJSONNull(data) {
		continuation.Value = nil
		return nil
	}
	value, err := queryDecodeRequired[string](data, "continuation")
	if err != nil {
		return err
	}
	continuation.Value = &value
	return nil
}

// Validate checks only the bounded opaque continuation value. State coupling is
// enforced by QueryResponse.Validate.
func (continuation QueryContinuation) Validate() error {
	if continuation.Value != nil && !queryBoundedText(*continuation.Value, 0, queryMaxContinuation) {
		return fmt.Errorf("uci query response: continuation exceeds limit")
	}
	return nil
}

func (status QueryResponseStatus) valid() bool {
	switch status {
	case QueryStatusOK, QueryStatusEmpty, QueryStatusPartial, QueryStatusStale, QueryStatusUnavailable, QueryStatusContextRequired, QueryStatusForbidden:
		return true
	default:
		return false
	}
}

func (state QueryFreshnessState) valid() bool {
	switch state {
	case QueryFreshnessObservedCurrent, QueryFreshnessCatchingUp, QueryFreshnessOffline, QueryFreshnessUnknown, QueryFreshnessHistorical:
		return true
	default:
		return false
	}
}

func (method QueryFreshnessMethod) valid() bool {
	switch method {
	case QueryFreshnessWatchWatermark, QueryFreshnessPathHashBarrier, QueryFreshnessFullReconcile, QueryFreshnessPinnedHistory, QueryFreshnessNone:
		return true
	default:
		return false
	}
}

func (state QueryEnrichmentState) valid() bool {
	switch state {
	case QueryEnrichmentCurrent, QueryEnrichmentPending, QueryEnrichmentUnavailable:
		return true
	default:
		return false
	}
}

func (kind QueryBarrierScopeKind) valid() bool {
	return kind == QueryBarrierPaths || kind == QueryBarrierPathsWithHashes
}

func (state QueryBarrierState) valid() bool {
	switch state {
	case QueryBarrierSatisfied, QueryBarrierStale, QueryBarrierTimedOut:
		return true
	default:
		return false
	}
}

func (mode QueryRetrievalMode) valid() bool {
	switch mode {
	case QueryRetrievalExact, QueryRetrievalLexical, QueryRetrievalHybrid, QueryRetrievalGraph, QueryRetrievalUnavailable:
		return true
	default:
		return false
	}
}

func (state QueryCompletionState) valid() bool {
	switch state {
	case QueryCompletionUnknown, QueryCompletionSucceeded, QueryCompletionPartial, QueryCompletionFailed, QueryCompletionAbandoned:
		return true
	default:
		return false
	}
}

func (code QueryErrorCode) valid() bool {
	switch code {
	case QueryErrorContextRequired, QueryErrorContextMismatch, QueryErrorViewRetired, QueryErrorSourceUnavailable, QueryErrorCheckoutOffline, QueryErrorIndexCatchingUp, QueryErrorParserUnsupported, QueryErrorParserPartial, QueryErrorVectorUnavailable, QueryErrorProfileMismatch, QueryErrorBudgetExceeded, QueryErrorLeaseStale, QueryErrorBuildIncomplete, QueryErrorPermissionDenied, QueryErrorExposureUnavailable, QueryErrorIdempotencyMismatch:
		return true
	default:
		return false
	}
}

func (code QueryErrorCode) isRecorderFailure() bool {
	return code == QueryErrorExposureUnavailable || code == QueryErrorIdempotencyMismatch
}

func (code QueryErrorCode) isAuthorizedUnavailable() bool {
	switch code {
	case QueryErrorViewRetired, QueryErrorSourceUnavailable, QueryErrorCheckoutOffline, QueryErrorIndexCatchingUp, QueryErrorParserUnsupported, QueryErrorParserPartial, QueryErrorVectorUnavailable, QueryErrorProfileMismatch, QueryErrorBudgetExceeded, QueryErrorLeaseStale, QueryErrorBuildIncomplete:
		return true
	default:
		return false
	}
}

func (kind QueryItemKind) valid() bool {
	switch kind {
	case QueryItemCode, QueryItemDocument, QueryItemSchema, QueryItemConfig:
		return true
	default:
		return false
	}
}

func (source QueryMatchSource) valid() bool {
	switch source {
	case QueryMatchExact, QueryMatchFTS, QueryMatchVector, QueryMatchGraph:
		return true
	default:
		return false
	}
}

func (kind QueryEvidenceKind) valid() bool {
	switch kind {
	case QueryEvidenceExtracted, QueryEvidenceResolved, QueryEvidenceHeuristic, QueryEvidenceSemantic:
		return true
	default:
		return false
	}
}

func (reason QueryGraphStopReason) valid() bool {
	switch reason {
	case QueryGraphComplete, QueryGraphDepthCap, QueryGraphNodeCap, QueryGraphDeadline, QueryGraphCoverageGap:
		return true
	default:
		return false
	}
}

type queryContextKey struct {
	SourceID string
	ViewID   string
}

type queryContextSet map[queryContextKey]struct{}

func (contexts queryContextSet) contains(ref QueryEntityRef) bool {
	_, found := contexts[queryContextKey{SourceID: ref.SourceID, ViewID: ref.ViewID}]
	return found
}

func validQueryExposureRef(value string) bool {
	if len(value) < queryMinExposureRefLen || len(value) > queryMaxExposureRef || !bytes.HasPrefix([]byte(value), []byte("uci-exp_")) {
		return false
	}
	for _, character := range value[len("uci-exp_"):] {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func validQueryContentDigest(value QueryContentDigest) bool {
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

func queryBoundedText(value string, minimum, maximum int) bool {
	if !utf8.ValidString(value) {
		return false
	}
	length := utf8.RuneCountInString(value)
	return length >= minimum && length <= maximum
}

func queryDecodeObject[T any](data []byte, target *T) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func queryDecodeRequired[T any](raw json.RawMessage, field string) (T, error) {
	var zero T
	if len(raw) == 0 {
		return zero, fmt.Errorf("uci query response: %s is required", field)
	}
	if queryJSONNull(raw) {
		return zero, fmt.Errorf("uci query response: %s cannot be null", field)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return zero, fmt.Errorf("uci query response: decode %s: %w", field, err)
	}
	return value, nil
}

func queryDecodeOptional[T any](raw json.RawMessage, field string) (*T, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if queryJSONNull(raw) {
		return nil, fmt.Errorf("uci query response: %s cannot be null", field)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("uci query response: decode %s: %w", field, err)
	}
	return &value, nil
}

func queryDecodeNullable[T any](raw json.RawMessage, field string) (*T, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("uci query response: %s is required", field)
	}
	if queryJSONNull(raw) {
		return nil, nil
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("uci query response: decode %s: %w", field, err)
	}
	return &value, nil
}

func queryDecodeContinuation(raw json.RawMessage) (*QueryContinuation, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var continuation QueryContinuation
	if err := json.Unmarshal(raw, &continuation); err != nil {
		return nil, fmt.Errorf("uci query response: decode continuation: %w", err)
	}
	return &continuation, nil
}

func queryJSONNull(raw []byte) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (ref *QueryContextRef) UnmarshalJSON(data []byte) error {
	var wire struct {
		SourceID   json.RawMessage `json:"source_id"`
		CheckoutID json.RawMessage `json:"checkout_id"`
		ViewID     json.RawMessage `json:"view_id"`
		Generation json.RawMessage `json:"generation"`
		ProfileID  json.RawMessage `json:"profile_id"`
		SpaceID    json.RawMessage `json:"space_id"`
	}
	if err := queryDecodeObject(data, &wire); err != nil {
		return fmt.Errorf("uci query context: %w", err)
	}
	sourceID, err := queryDecodeRequired[string](wire.SourceID, "source_id")
	if err != nil {
		return err
	}
	checkoutID, err := queryDecodeRequired[string](wire.CheckoutID, "checkout_id")
	if err != nil {
		return err
	}
	viewID, err := queryDecodeRequired[string](wire.ViewID, "view_id")
	if err != nil {
		return err
	}
	generation, err := queryDecodeRequired[int64](wire.Generation, "generation")
	if err != nil {
		return err
	}
	profileID, err := queryDecodeRequired[string](wire.ProfileID, "profile_id")
	if err != nil {
		return err
	}
	spaceID, err := queryDecodeOptional[string](wire.SpaceID, "space_id")
	if err != nil {
		return err
	}
	*ref = QueryContextRef{SourceID: sourceID, CheckoutID: checkoutID, ViewID: viewID, Generation: generation, ProfileID: profileID, SpaceID: spaceID}
	return nil
}

func (freshness *QueryFreshness) UnmarshalJSON(data []byte) error {
	var wire struct {
		State               json.RawMessage `json:"state"`
		Method              json.RawMessage `json:"method"`
		PendingChanges      json.RawMessage `json:"pending_changes"`
		EnrichmentWatermark json.RawMessage `json:"enrichment_watermark"`
		Barrier             json.RawMessage `json:"barrier"`
	}
	if err := queryDecodeObject(data, &wire); err != nil {
		return fmt.Errorf("uci query freshness: %w", err)
	}
	state, err := queryDecodeRequired[QueryFreshnessState](wire.State, "state")
	if err != nil {
		return err
	}
	method, err := queryDecodeRequired[QueryFreshnessMethod](wire.Method, "method")
	if err != nil {
		return err
	}
	pendingChanges, err := queryDecodeNullable[int64](wire.PendingChanges, "pending_changes")
	if err != nil {
		return err
	}
	watermark, err := queryDecodeRequired[QueryEnrichmentWatermark](wire.EnrichmentWatermark, "enrichment_watermark")
	if err != nil {
		return err
	}
	barrier, err := queryDecodeNullable[QueryBarrier](wire.Barrier, "barrier")
	if err != nil {
		return err
	}
	*freshness = QueryFreshness{State: state, Method: method, PendingChanges: pendingChanges, EnrichmentWatermark: watermark, Barrier: barrier}
	return nil
}

func (watermark *QueryEnrichmentWatermark) UnmarshalJSON(data []byte) error {
	var wire struct {
		Sequence json.RawMessage `json:"sequence"`
		State    json.RawMessage `json:"state"`
	}
	if err := queryDecodeObject(data, &wire); err != nil {
		return fmt.Errorf("uci query enrichment watermark: %w", err)
	}
	sequence, err := queryDecodeRequired[int64](wire.Sequence, "sequence")
	if err != nil {
		return err
	}
	state, err := queryDecodeRequired[QueryEnrichmentState](wire.State, "state")
	if err != nil {
		return err
	}
	*watermark = QueryEnrichmentWatermark{Sequence: sequence, State: state}
	return nil
}

func (barrier *QueryBarrier) UnmarshalJSON(data []byte) error {
	var wire struct {
		Scope      json.RawMessage `json:"scope"`
		DeadlineMS json.RawMessage `json:"deadline_ms"`
		State      json.RawMessage `json:"state"`
	}
	if err := queryDecodeObject(data, &wire); err != nil {
		return fmt.Errorf("uci query barrier: %w", err)
	}
	scope, err := queryDecodeRequired[QueryBarrierScope](wire.Scope, "scope")
	if err != nil {
		return err
	}
	deadlineMS, err := queryDecodeRequired[int64](wire.DeadlineMS, "deadline_ms")
	if err != nil {
		return err
	}
	state, err := queryDecodeRequired[QueryBarrierState](wire.State, "state")
	if err != nil {
		return err
	}
	*barrier = QueryBarrier{Scope: scope, DeadlineMS: deadlineMS, State: state}
	return nil
}

func (scope *QueryBarrierScope) UnmarshalJSON(data []byte) error {
	var wire struct {
		Kind      json.RawMessage `json:"kind"`
		PathCount json.RawMessage `json:"path_count"`
	}
	if err := queryDecodeObject(data, &wire); err != nil {
		return fmt.Errorf("uci query barrier scope: %w", err)
	}
	kind, err := queryDecodeRequired[QueryBarrierScopeKind](wire.Kind, "kind")
	if err != nil {
		return err
	}
	pathCount, err := queryDecodeRequired[int64](wire.PathCount, "path_count")
	if err != nil {
		return err
	}
	*scope = QueryBarrierScope{Kind: kind, PathCount: pathCount}
	return nil
}

func (retrieval *QueryRetrieval) UnmarshalJSON(data []byte) error {
	var wire struct {
		Mode               json.RawMessage `json:"mode"`
		VectorCoverage     json.RawMessage `json:"vector_coverage"`
		DegradationReasons json.RawMessage `json:"degradation_reasons"`
	}
	if err := queryDecodeObject(data, &wire); err != nil {
		return fmt.Errorf("uci query retrieval: %w", err)
	}
	mode, err := queryDecodeRequired[QueryRetrievalMode](wire.Mode, "mode")
	if err != nil {
		return err
	}
	vectorCoverage, err := queryDecodeNullable[float64](wire.VectorCoverage, "vector_coverage")
	if err != nil {
		return err
	}
	reasons, err := queryDecodeRequired[[]string](wire.DegradationReasons, "degradation_reasons")
	if err != nil {
		return err
	}
	*retrieval = QueryRetrieval{Mode: mode, VectorCoverage: vectorCoverage, DegradationReasons: reasons}
	return nil
}

func (coverage *QueryCoverage) UnmarshalJSON(data []byte) error {
	var wire struct {
		Structural       json.RawMessage `json:"structural"`
		UnresolvedSites  json.RawMessage `json:"unresolved_sites"`
		UnsupportedFiles json.RawMessage `json:"unsupported_files"`
	}
	if err := queryDecodeObject(data, &wire); err != nil {
		return fmt.Errorf("uci query coverage: %w", err)
	}
	structural, err := queryDecodeRequired[IndexCoverageState](wire.Structural, "structural")
	if err != nil {
		return err
	}
	unresolvedSites, err := queryDecodeNullable[int64](wire.UnresolvedSites, "unresolved_sites")
	if err != nil {
		return err
	}
	unsupportedFiles, err := queryDecodeNullable[int64](wire.UnsupportedFiles, "unsupported_files")
	if err != nil {
		return err
	}
	*coverage = QueryCoverage{Structural: structural, UnresolvedSites: unresolvedSites, UnsupportedFiles: unsupportedFiles}
	return nil
}

func (exposure *QueryExposure) UnmarshalJSON(data []byte) error {
	var wire struct {
		ExposureRef     json.RawMessage `json:"exposure_ref"`
		CompletionState json.RawMessage `json:"completion_state"`
	}
	if err := queryDecodeObject(data, &wire); err != nil {
		return fmt.Errorf("uci query exposure: %w", err)
	}
	exposureRef, err := queryDecodeRequired[string](wire.ExposureRef, "exposure_ref")
	if err != nil {
		return err
	}
	completionState, err := queryDecodeRequired[QueryCompletionState](wire.CompletionState, "completion_state")
	if err != nil {
		return err
	}
	*exposure = QueryExposure{ExposureRef: exposureRef, CompletionState: completionState}
	return nil
}

func (queryError *QueryError) UnmarshalJSON(data []byte) error {
	var wire struct {
		Code json.RawMessage `json:"code"`
	}
	if err := queryDecodeObject(data, &wire); err != nil {
		return fmt.Errorf("uci query error: %w", err)
	}
	code, err := queryDecodeRequired[QueryErrorCode](wire.Code, "code")
	if err != nil {
		return err
	}
	*queryError = QueryError{Code: code}
	return nil
}

func (ref *QueryEntityRef) UnmarshalJSON(data []byte) error {
	var wire struct {
		SourceID  json.RawMessage `json:"source_id"`
		ViewID    json.RawMessage `json:"view_id"`
		EntityKey json.RawMessage `json:"entity_key"`
	}
	if err := queryDecodeObject(data, &wire); err != nil {
		return fmt.Errorf("uci query entity reference: %w", err)
	}
	sourceID, err := queryDecodeRequired[string](wire.SourceID, "source_id")
	if err != nil {
		return err
	}
	viewID, err := queryDecodeRequired[string](wire.ViewID, "view_id")
	if err != nil {
		return err
	}
	entityKey, err := queryDecodeRequired[string](wire.EntityKey, "entity_key")
	if err != nil {
		return err
	}
	*ref = QueryEntityRef{SourceID: sourceID, ViewID: viewID, EntityKey: entityKey}
	return nil
}

func (span *QuerySpan) UnmarshalJSON(data []byte) error {
	var wire struct {
		ByteStart json.RawMessage `json:"byte_start"`
		ByteEnd   json.RawMessage `json:"byte_end"`
		LineStart json.RawMessage `json:"line_start"`
		LineEnd   json.RawMessage `json:"line_end"`
	}
	if err := queryDecodeObject(data, &wire); err != nil {
		return fmt.Errorf("uci query span: %w", err)
	}
	byteStart, err := queryDecodeRequired[int64](wire.ByteStart, "byte_start")
	if err != nil {
		return err
	}
	byteEnd, err := queryDecodeRequired[int64](wire.ByteEnd, "byte_end")
	if err != nil {
		return err
	}
	lineStart, err := queryDecodeRequired[int64](wire.LineStart, "line_start")
	if err != nil {
		return err
	}
	lineEnd, err := queryDecodeRequired[int64](wire.LineEnd, "line_end")
	if err != nil {
		return err
	}
	*span = QuerySpan{ByteStart: byteStart, ByteEnd: byteEnd, LineStart: lineStart, LineEnd: lineEnd}
	return nil
}

func (item *QueryItem) UnmarshalJSON(data []byte) error {
	var wire struct {
		Ref           json.RawMessage `json:"ref"`
		Path          json.RawMessage `json:"path"`
		Span          json.RawMessage `json:"span"`
		ContentDigest json.RawMessage `json:"content_digest"`
		Kind          json.RawMessage `json:"kind"`
		Language      json.RawMessage `json:"language"`
		Excerpt       json.RawMessage `json:"excerpt"`
		MatchSources  json.RawMessage `json:"match_sources"`
		Score         json.RawMessage `json:"score"`
	}
	if err := queryDecodeObject(data, &wire); err != nil {
		return fmt.Errorf("uci query item: %w", err)
	}
	ref, err := queryDecodeRequired[QueryEntityRef](wire.Ref, "ref")
	if err != nil {
		return err
	}
	path, err := queryDecodeRequired[string](wire.Path, "path")
	if err != nil {
		return err
	}
	span, err := queryDecodeRequired[QuerySpan](wire.Span, "span")
	if err != nil {
		return err
	}
	contentDigest, err := queryDecodeRequired[QueryContentDigest](wire.ContentDigest, "content_digest")
	if err != nil {
		return err
	}
	kind, err := queryDecodeRequired[QueryItemKind](wire.Kind, "kind")
	if err != nil {
		return err
	}
	language, err := queryDecodeRequired[string](wire.Language, "language")
	if err != nil {
		return err
	}
	excerpt, err := queryDecodeRequired[string](wire.Excerpt, "excerpt")
	if err != nil {
		return err
	}
	matchSources, err := queryDecodeRequired[[]QueryMatchSource](wire.MatchSources, "match_sources")
	if err != nil {
		return err
	}
	score, err := queryDecodeOptional[float64](wire.Score, "score")
	if err != nil {
		return err
	}
	*item = QueryItem{Ref: ref, Path: path, Span: span, ContentDigest: contentDigest, Kind: kind, Language: language, Excerpt: excerpt, MatchSources: matchSources, Score: score}
	return nil
}

func (graph *QueryGraph) UnmarshalJSON(data []byte) error {
	var wire struct {
		Nodes      json.RawMessage `json:"nodes"`
		Edges      json.RawMessage `json:"edges"`
		StopReason json.RawMessage `json:"stop_reason"`
	}
	if err := queryDecodeObject(data, &wire); err != nil {
		return fmt.Errorf("uci query graph: %w", err)
	}
	nodes, err := queryDecodeRequired[[]QueryEntityRef](wire.Nodes, "nodes")
	if err != nil {
		return err
	}
	edges, err := queryDecodeRequired[[]QueryGraphEdge](wire.Edges, "edges")
	if err != nil {
		return err
	}
	stopReason, err := queryDecodeRequired[QueryGraphStopReason](wire.StopReason, "stop_reason")
	if err != nil {
		return err
	}
	*graph = QueryGraph{Nodes: nodes, Edges: edges, StopReason: stopReason}
	return nil
}

func (edge *QueryGraphEdge) UnmarshalJSON(data []byte) error {
	var wire struct {
		From         json.RawMessage `json:"from"`
		To           json.RawMessage `json:"to"`
		Relation     json.RawMessage `json:"relation"`
		EvidenceKind json.RawMessage `json:"evidence_kind"`
		EvidenceRefs json.RawMessage `json:"evidence_refs"`
		Explanation  json.RawMessage `json:"explanation"`
	}
	if err := queryDecodeObject(data, &wire); err != nil {
		return fmt.Errorf("uci query graph edge: %w", err)
	}
	from, err := queryDecodeRequired[QueryEntityRef](wire.From, "from")
	if err != nil {
		return err
	}
	to, err := queryDecodeRequired[QueryEntityRef](wire.To, "to")
	if err != nil {
		return err
	}
	relation, err := queryDecodeRequired[IndexRelation](wire.Relation, "relation")
	if err != nil {
		return err
	}
	evidenceKind, err := queryDecodeRequired[QueryEvidenceKind](wire.EvidenceKind, "evidence_kind")
	if err != nil {
		return err
	}
	evidenceRefs, err := queryDecodeRequired[[]QueryEntityRef](wire.EvidenceRefs, "evidence_refs")
	if err != nil {
		return err
	}
	explanation, err := queryDecodeOptional[string](wire.Explanation, "explanation")
	if err != nil {
		return err
	}
	*edge = QueryGraphEdge{From: from, To: to, Relation: relation, EvidenceKind: evidenceKind, EvidenceRefs: evidenceRefs, Explanation: explanation}
	return nil
}
