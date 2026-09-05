package uci

import (
	"context"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	queryContinuationTokenVersion = "v1"
	queryMaxClientSessionID       = 256
	queryMaxText                  = 16 << 10
	queryMaxLanguages             = 32
	queryMaxLanguageFilter        = 64
)

var queryServiceFallbackKeyCounter uint64

// QueryMode selects the retrieval predicate applied inside one authorized View.
type QueryMode string

const (
	QueryModeExactLocalName       QueryMode = "exact_local_name"
	QueryModeExactQualifiedSymbol QueryMode = "exact_qualified_symbol"
	QueryModeExactRelativePath    QueryMode = "exact_relative_path"
	QueryModeFTS                  QueryMode = "fts"
)

// QueryOrder selects the deterministic candidate ordering.
type QueryOrder string

const (
	QueryOrderPath      QueryOrder = "path"
	QueryOrderRelevance QueryOrder = "relevance"
)

// QueryFilter limits candidates without widening the selected View.
type QueryFilter struct {
	Languages []string
}

// QuerySpec is an untrusted query request presented after context authorization.
type QuerySpec struct {
	ClientSessionID string
	Mode            QueryMode
	Text            string
	Filter          QueryFilter
	Order           QueryOrder
	Limit           int
	// Offset is service-owned pagination state. Caller input is always reset to zero during normalization.
	Offset       int
	Continuation *string
}

// FTSTerms returns the normalized lexical terms that a QueryStore can bind to
// PostgreSQL FTS predicates. It retains original tokens and adds camel/snake
// identifier components without exposing a project-scoped fallback.
func (spec QuerySpec) FTSTerms() []string {
	return queryFTSTerms(spec.Text)
}

// QueryResult is the service-owned portion of a UCI query response. Exposure
// recording is deliberately owned by the later response boundary.
type QueryResult struct {
	Response QueryResponse
}

// QueryStore selects candidates only from the already-authorized View scope.
type QueryStore interface {
	SelectCandidates(context.Context, AuthorizedContext, QuerySpec) (QueryStoreResult, error)
}

// QueryStoreResult contains the selected View's candidates and availability
// evidence. Candidates must already have been selected inside that View.
type QueryStoreResult struct {
	Candidates  []QueryCandidate
	Coverage    IndexCoverageState
	Unavailable *QueryError
}

// QueryCandidate is one source-grounded candidate selected from an immutable View.
type QueryCandidate struct {
	Context         ContextRef
	Proof           IndexArtifactProof
	EntityKey       string
	LocalName       string
	QualifiedSymbol string
	RelativePath    string
	Span            IndexSpan
	Text            string
	Kind            QueryItemKind
	Language        string
	Score           float64
}

// QueryService normalizes requests, validates opaque continuations, and maps
// authorized store candidates to the closed UCI response DTO.
type QueryService struct {
	store           QueryStore
	continuationKey [sha256.Size]byte
}

// NewQueryService creates a query service for one injected candidate store.
func NewQueryService(store QueryStore) *QueryService {
	return &QueryService{
		store:           store,
		continuationKey: newQueryContinuationKey(),
	}
}

// Query executes one query in an already-authorized immutable ContextRef. It
// never resolves or authorizes the context again.
func (service *QueryService) Query(ctx context.Context, authorized AuthorizedContext, spec QuerySpec) (QueryResult, error) {
	if service == nil || service.store == nil {
		return QueryResult{}, fmt.Errorf("uci query: store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return QueryResult{}, err
	}

	ref := authorized.Ref()
	if !ref.valid() {
		return QueryResult{}, fmt.Errorf("uci query: authorized context is invalid")
	}
	normalized, err := normalizeQuerySpec(spec)
	if err != nil {
		return QueryResult{}, err
	}
	start, err := service.continuationOffset(ref, normalized)
	if err != nil {
		return QueryResult{}, err
	}

	storeSpec := normalized
	storeSpec.Offset = start
	selected, err := service.store.SelectCandidates(ctx, authorized, storeSpec)
	if err != nil {
		return QueryResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return QueryResult{}, err
	}
	if !validQueryCoverage(selected.Coverage) {
		return QueryResult{}, fmt.Errorf("uci query: store returned invalid coverage %q", selected.Coverage)
	}
	if selected.Unavailable != nil {
		if err := selected.Unavailable.Validate(); err != nil || !selected.Unavailable.Code.isAuthorizedUnavailable() {
			return QueryResult{}, fmt.Errorf("uci query: store returned invalid unavailable state")
		}
		if selected.Coverage != IndexCoverageUnavailable {
			return QueryResult{}, fmt.Errorf("uci query: unavailable store result requires unavailable coverage")
		}
		return QueryResult{Response: queryUnavailableResponse(ref, selected.Coverage, *selected.Unavailable)}, nil
	}
	if selected.Coverage == IndexCoverageUnavailable {
		return QueryResult{}, fmt.Errorf("uci query: unavailable coverage requires an unavailable outcome")
	}

	candidates := make([]QueryCandidate, 0, len(selected.Candidates))
	for _, candidate := range selected.Candidates {
		if !contextRefsEqual(candidate.Context, ref) || !validQueryCandidate(candidate) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	orderQueryCandidates(candidates, normalized.Order)
	if start > 0 && len(candidates) == 0 {
		return QueryResult{}, fmt.Errorf("uci query: continuation position is outside the selected view")
	}

	end := normalized.Limit
	if end > len(candidates) {
		end = len(candidates)
	}
	items := make(QueryItems, 0, end)
	warnings := QueryWarnings{}
	for _, candidate := range candidates[:end] {
		item, excerptOmitted := queryItemFromCandidate(candidate, normalized)
		items = append(items, item)
		if excerptOmitted && !containsQueryWarning(warnings, "excerpt_omitted_response_bound") {
			warnings = append(warnings, "excerpt_omitted_response_bound")
		}
	}

	truncated := end < len(candidates)
	continuation := QueryContinuation{}
	if truncated {
		token, err := service.encodeContinuation(ref, normalized, start+end)
		if err != nil {
			return QueryResult{}, err
		}
		continuation.Value = &token
	}
	return QueryResult{Response: queryAvailableResponse(ref, normalized, selected.Coverage, items, warnings, truncated, continuation)}, nil
}

func normalizeQuerySpec(spec QuerySpec) (QuerySpec, error) {
	normalized := QuerySpec{
		ClientSessionID: strings.TrimSpace(spec.ClientSessionID),
		Mode:            spec.Mode,
		Text:            strings.TrimSpace(spec.Text),
		Order:           spec.Order,
		Limit:           spec.Limit,
	}
	if !validQueryIdentity(normalized.ClientSessionID, queryMaxClientSessionID) {
		return QuerySpec{}, fmt.Errorf("uci query: client session ID is invalid")
	}
	if !normalized.Mode.valid() {
		return QuerySpec{}, fmt.Errorf("uci query: mode is invalid")
	}
	if !validQueryIdentity(normalized.Text, queryMaxText) {
		return QuerySpec{}, fmt.Errorf("uci query: text is invalid")
	}
	if !normalized.Order.valid() {
		return QuerySpec{}, fmt.Errorf("uci query: order is invalid")
	}
	if normalized.Limit < 1 || normalized.Limit > queryMaxItems {
		return QuerySpec{}, fmt.Errorf("uci query: limit must be between 1 and %d", queryMaxItems)
	}
	if len(spec.Filter.Languages) > queryMaxLanguages {
		return QuerySpec{}, fmt.Errorf("uci query: language filter exceeds limit")
	}

	languages := make([]string, 0, len(spec.Filter.Languages))
	for _, language := range spec.Filter.Languages {
		language = strings.ToLower(strings.TrimSpace(language))
		if !validQueryIdentity(language, queryMaxLanguageFilter) {
			return QuerySpec{}, fmt.Errorf("uci query: language filter is invalid")
		}
		languages = append(languages, language)
	}
	sort.Strings(languages)
	for _, language := range languages {
		if len(normalized.Filter.Languages) == 0 || normalized.Filter.Languages[len(normalized.Filter.Languages)-1] != language {
			normalized.Filter.Languages = append(normalized.Filter.Languages, language)
		}
	}

	if spec.Continuation != nil {
		token := *spec.Continuation
		if len(token) == 0 || len(token) > queryMaxContinuation || !utf8.ValidString(token) || strings.TrimSpace(token) != token {
			return QuerySpec{}, fmt.Errorf("uci query: continuation is invalid")
		}
		normalized.Continuation = &token
	}
	return normalized, nil
}

func validQueryIdentity(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validQueryCoverage(coverage IndexCoverageState) bool {
	switch coverage {
	case IndexCoverageComplete, IndexCoveragePartial, IndexCoverageUnavailable:
		return true
	default:
		return false
	}
}

func (mode QueryMode) valid() bool {
	switch mode {
	case QueryModeExactLocalName, QueryModeExactQualifiedSymbol, QueryModeExactRelativePath, QueryModeFTS:
		return true
	default:
		return false
	}
}

func (order QueryOrder) valid() bool {
	return order == QueryOrderPath || order == QueryOrderRelevance
}

func validQueryCandidate(candidate QueryCandidate) bool {
	if !candidate.Context.valid() || !canonicalContextUUID(candidate.Proof.ArtifactID) || !isIndexDigest(candidate.Proof.FactsDigest) || !queryBoundedText(candidate.EntityKey, 1, queryMaxEntityKey) ||
		!queryBoundedText(candidate.RelativePath, 1, queryMaxPath) || !queryBoundedText(candidate.Language, 1, queryMaxLanguage) ||
		!candidate.Kind.valid() || !utf8.ValidString(candidate.Text) || math.IsNaN(candidate.Score) || math.IsInf(candidate.Score, 0) {
		return false
	}
	if _, ok := queryBareContentDigest(candidate.Proof.ContentDigest); !ok {
		return false
	}
	return candidate.Span.ByteStart >= 0 && candidate.Span.ByteEnd > candidate.Span.ByteStart &&
		candidate.Span.LineStart >= 1 && candidate.Span.LineEnd >= candidate.Span.LineStart
}

func orderQueryCandidates(candidates []QueryCandidate, order QueryOrder) {
	sort.SliceStable(candidates, func(left, right int) bool {
		if order == QueryOrderRelevance && candidates[left].Score != candidates[right].Score {
			return candidates[left].Score > candidates[right].Score
		}
		if candidates[left].RelativePath != candidates[right].RelativePath {
			return candidates[left].RelativePath < candidates[right].RelativePath
		}
		if candidates[left].Span.ByteStart != candidates[right].Span.ByteStart {
			return candidates[left].Span.ByteStart < candidates[right].Span.ByteStart
		}
		if candidates[left].EntityKey != candidates[right].EntityKey {
			return candidates[left].EntityKey < candidates[right].EntityKey
		}
		if candidates[left].Proof.ArtifactID != candidates[right].Proof.ArtifactID {
			return candidates[left].Proof.ArtifactID < candidates[right].Proof.ArtifactID
		}
		return candidates[left].Proof.ContentDigest < candidates[right].Proof.ContentDigest
	})
}

func queryItemFromCandidate(candidate QueryCandidate, spec QuerySpec) (QueryItem, bool) {
	excerpt := candidate.Text
	excerptOmitted := utf8.RuneCountInString(excerpt) > queryMaxExcerpt
	if excerptOmitted {
		excerpt = ""
	}
	var score *float64
	if spec.Order == QueryOrderRelevance {
		value := candidate.Score
		score = &value
	}
	matchSource := QueryMatchExact
	if spec.Mode == QueryModeFTS {
		matchSource = QueryMatchFTS
	}
	contentDigest, _ := queryBareContentDigest(candidate.Proof.ContentDigest)
	return QueryItem{
		Ref: QueryEntityRef{
			SourceID:  candidate.Context.SourceID,
			ViewID:    candidate.Context.ViewID,
			EntityKey: candidate.EntityKey,
		},
		Path: candidate.RelativePath,
		Span: QuerySpan{
			ByteStart: candidate.Span.ByteStart,
			ByteEnd:   candidate.Span.ByteEnd,
			LineStart: int64(candidate.Span.LineStart),
			LineEnd:   int64(candidate.Span.LineEnd),
		},
		ContentDigest: contentDigest,
		Kind:          candidate.Kind,
		Language:      candidate.Language,
		Excerpt:       excerpt,
		MatchSources:  []QueryMatchSource{matchSource},
		Score:         score,
	}, excerptOmitted
}

func queryBareContentDigest(digest IndexDigest) (QueryContentDigest, bool) {
	value := string(digest)
	if strings.HasPrefix(value, "sha256:") {
		value = strings.TrimPrefix(value, "sha256:")
	}
	if len(value) != sha256.Size*2 {
		return "", false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return "", false
		}
	}
	return QueryContentDigest(value), true
}

func queryAvailableResponse(ref ContextRef, spec QuerySpec, coverage IndexCoverageState, items QueryItems, warnings QueryWarnings, truncated bool, continuation QueryContinuation) QueryResponse {
	zero := int64(0)
	contexts := QueryContexts{queryContextRef(ref)}
	response := QueryResponse{
		Schema:    QueryResponseSchema,
		Status:    QueryStatusOK,
		Contexts:  &contexts,
		Freshness: queryPinnedFreshness(ref.Generation),
		Retrieval: &QueryRetrieval{
			Mode:               queryRetrievalMode(spec.Mode),
			DegradationReasons: []string{},
		},
		Coverage: &QueryCoverage{
			Structural:       coverage,
			UnresolvedSites:  &zero,
			UnsupportedFiles: &zero,
		},
		Items:        &items,
		Truncated:    &truncated,
		Warnings:     &warnings,
		Continuation: &continuation,
	}
	if coverage == IndexCoveragePartial {
		response.Status = QueryStatusPartial
	} else if len(items) == 0 {
		response.Status = QueryStatusEmpty
	}
	return response
}

func queryUnavailableResponse(ref ContextRef, coverage IndexCoverageState, unavailable QueryError) QueryResponse {
	contexts := QueryContexts{queryContextRef(ref)}
	items := QueryItems{}
	warnings := QueryWarnings{}
	truncated := false
	continuation := QueryContinuation{}
	return QueryResponse{
		Schema:    QueryResponseSchema,
		Status:    QueryStatusUnavailable,
		Contexts:  &contexts,
		Freshness: queryPinnedFreshness(ref.Generation),
		Retrieval: &QueryRetrieval{
			Mode:               QueryRetrievalUnavailable,
			DegradationReasons: []string{},
		},
		Coverage:     &QueryCoverage{Structural: coverage},
		Error:        &unavailable,
		Items:        &items,
		Truncated:    &truncated,
		Warnings:     &warnings,
		Continuation: &continuation,
	}
}

func queryContextRef(ref ContextRef) QueryContextRef {
	return QueryContextRef{
		SpaceID:    cloneQuerySpaceID(ref.SpaceID),
		SourceID:   ref.SourceID,
		CheckoutID: ref.CheckoutID,
		ViewID:     ref.ViewID,
		Generation: ref.Generation,
		ProfileID:  ref.AnalysisProfileID,
	}
}

func cloneQuerySpaceID(spaceID *string) *string {
	if spaceID == nil {
		return nil
	}
	copy := *spaceID
	return &copy
}

func queryPinnedFreshness(generation int64) *QueryFreshness {
	return &QueryFreshness{
		State:          QueryFreshnessHistorical,
		Method:         QueryFreshnessPinnedHistory,
		PendingChanges: nil,
		EnrichmentWatermark: QueryEnrichmentWatermark{
			Sequence: generation,
			State:    QueryEnrichmentCurrent,
		},
	}
}

func queryRetrievalMode(mode QueryMode) QueryRetrievalMode {
	if mode == QueryModeFTS {
		return QueryRetrievalLexical
	}
	return QueryRetrievalExact
}

func containsQueryWarning(warnings QueryWarnings, want string) bool {
	for _, warning := range warnings {
		if warning == want {
			return true
		}
	}
	return false
}

type queryContinuationPayload struct {
	Version         string     `json:"version"`
	ClientSessionID string     `json:"client_session_id"`
	SpaceID         *string    `json:"space_id,omitempty"`
	SourceID        string     `json:"source_id"`
	CheckoutID      string     `json:"checkout_id"`
	ViewID          string     `json:"view_id"`
	ProfileID       string     `json:"profile_id"`
	Generation      int64      `json:"generation"`
	Mode            QueryMode  `json:"mode"`
	QueryDigest     string     `json:"query_digest"`
	FilterDigest    string     `json:"filter_digest"`
	Order           QueryOrder `json:"order"`
	Offset          int        `json:"offset"`
}

func (service *QueryService) continuationOffset(ref ContextRef, spec QuerySpec) (int, error) {
	if spec.Continuation == nil {
		return 0, nil
	}
	payload, err := service.decodeContinuation(*spec.Continuation)
	if err != nil {
		return 0, err
	}
	if !queryContinuationMatches(payload, ref, spec) {
		return 0, fmt.Errorf("uci query: continuation binding does not match request")
	}
	return payload.Offset, nil
}

func (service *QueryService) encodeContinuation(ref ContextRef, spec QuerySpec, offset int) (string, error) {
	if offset < 0 {
		return "", fmt.Errorf("uci query: continuation offset is invalid")
	}
	payload := queryContinuationPayloadFor(ref, spec, offset)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("uci query: encode continuation: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(encoded)
	signature := queryContinuationSignature(service.continuationKey, queryContinuationTokenVersion+"."+body)
	token := queryContinuationTokenVersion + "." + body + "." + base64.RawURLEncoding.EncodeToString(signature)
	if len(token) > queryMaxContinuation {
		return "", fmt.Errorf("uci query: continuation exceeds response bound")
	}
	return token, nil
}

func (service *QueryService) decodeContinuation(token string) (queryContinuationPayload, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != queryContinuationTokenVersion {
		return queryContinuationPayload{}, fmt.Errorf("uci query: continuation version is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return queryContinuationPayload{}, fmt.Errorf("uci query: continuation signature is invalid")
	}
	want := queryContinuationSignature(service.continuationKey, parts[0]+"."+parts[1])
	if !hmac.Equal(signature, want) {
		return queryContinuationPayload{}, fmt.Errorf("uci query: continuation integrity check failed")
	}
	encoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return queryContinuationPayload{}, fmt.Errorf("uci query: continuation payload is invalid")
	}
	var payload queryContinuationPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return queryContinuationPayload{}, fmt.Errorf("uci query: continuation payload is invalid")
	}
	if payload.Version != queryContinuationTokenVersion || payload.Offset < 0 {
		return queryContinuationPayload{}, fmt.Errorf("uci query: continuation payload is invalid")
	}
	return payload, nil
}

func queryContinuationPayloadFor(ref ContextRef, spec QuerySpec, offset int) queryContinuationPayload {
	return queryContinuationPayload{
		Version:         queryContinuationTokenVersion,
		ClientSessionID: spec.ClientSessionID,
		SpaceID:         cloneQuerySpaceID(ref.SpaceID),
		SourceID:        ref.SourceID,
		CheckoutID:      ref.CheckoutID,
		ViewID:          ref.ViewID,
		ProfileID:       ref.AnalysisProfileID,
		Generation:      ref.Generation,
		Mode:            spec.Mode,
		QueryDigest:     queryContinuationDigest("text", []string{spec.Text}),
		FilterDigest:    queryContinuationDigest("languages", spec.Filter.Languages),
		Order:           spec.Order,
		Offset:          offset,
	}
}

func queryContinuationMatches(payload queryContinuationPayload, ref ContextRef, spec QuerySpec) bool {
	expected := queryContinuationPayloadFor(ref, spec, payload.Offset)
	return payload.Version == expected.Version &&
		payload.ClientSessionID == expected.ClientSessionID &&
		queryOptionalStringEqual(payload.SpaceID, expected.SpaceID) &&
		payload.SourceID == expected.SourceID &&
		payload.CheckoutID == expected.CheckoutID &&
		payload.ViewID == expected.ViewID &&
		payload.ProfileID == expected.ProfileID &&
		payload.Generation == expected.Generation &&
		payload.Mode == expected.Mode &&
		payload.QueryDigest == expected.QueryDigest &&
		payload.FilterDigest == expected.FilterDigest &&
		payload.Order == expected.Order
}

func queryOptionalStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func queryContinuationDigest(kind string, values []string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("uci-query-continuation/"))
	_, _ = hash.Write([]byte(kind))
	for _, value := range values {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func queryContinuationSignature(key [sha256.Size]byte, value string) []byte {
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func newQueryContinuationKey() [sha256.Size]byte {
	var key [sha256.Size]byte
	if _, err := cryptorand.Read(key[:]); err == nil {
		return key
	}
	counter := atomic.AddUint64(&queryServiceFallbackKeyCounter, 1)
	return sha256.Sum256([]byte(fmt.Sprintf("%d:%d", time.Now().UnixNano(), counter)))
}

func queryFTSTerms(text string) []string {
	terms := make(map[string]struct{})
	for _, raw := range strings.FieldsFunc(text, func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsDigit(character)
	}) {
		if raw == "" {
			continue
		}
		terms[strings.ToLower(raw)] = struct{}{}
		for _, term := range queryIdentifierTerms(raw) {
			terms[strings.ToLower(term)] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(terms))
	for term := range terms {
		ordered = append(ordered, term)
	}
	sort.Strings(ordered)
	return ordered
}

func queryIdentifierTerms(identifier string) []string {
	runes := []rune(identifier)
	if len(runes) == 0 {
		return nil
	}
	start := 0
	terms := make([]string, 0, 2)
	for offset := range runes[1:] {
		index := offset + 1
		previous := runes[index-1]
		current := runes[index]
		nextIsLower := index+1 < len(runes) && unicode.IsLower(runes[index+1])
		boundary := (unicode.IsLower(previous) && unicode.IsUpper(current)) ||
			(unicode.IsUpper(previous) && unicode.IsUpper(current) && nextIsLower) ||
			(unicode.IsDigit(previous) != unicode.IsDigit(current))
		if !boundary {
			continue
		}
		terms = append(terms, string(runes[start:index]))
		start = index
	}
	return append(terms, string(runes[start:]))
}
