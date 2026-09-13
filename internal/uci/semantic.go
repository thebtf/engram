package uci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	semanticVectorDimension     = 1536
	semanticRRFConstant         = 60
	semanticInputSchema         = "engram.uci-semantic-input/2"
	semanticMaxProfileText      = 512
	semanticQueryProviderBudget = 20 * time.Second
)

// VectorProfile names one versioned semantic space. Equal dimensions alone are
// deliberately insufficient for vector reuse.
type VectorProfile struct {
	ProviderRef           string
	Model                 string
	Dimension             int
	PreprocessingRevision string
	IncludeRelativePath   bool
}

// SemanticEmbedder is the storage-agnostic provider boundary. *embedding.Client
// satisfies it structurally from the outer wiring layer.
type SemanticEmbedder interface {
	Model() string
	Embed(context.Context, []string) ([][]float32, error)
}

// SemanticStore persists candidate embeddings and selects only candidates that
// are already inside the supplied authorized View.
type SemanticStore interface {
	LookupCandidateEmbedding(context.Context, AuthorizedContext, VectorProfile, QueryCandidate) ([]float32, bool, error)
	StoreCandidateEmbedding(context.Context, AuthorizedContext, VectorProfile, QueryCandidate, []float32) error
	SelectSemanticCandidates(context.Context, AuthorizedContext, VectorProfile, []float32, QuerySpec) (SemanticStoreResult, error)
}

// SemanticStoreResult contains vector-ranked candidates and the scoped vector
// coverage used to decide whether a semantic result is honest to return.
type SemanticStoreResult struct {
	Candidates     []QueryCandidate
	Coverage       IndexCoverageState
	VectorCoverage float64
}

// SemanticService composes provider-backed vector retrieval with the existing
// View-scoped lexical candidate store. It never resolves or authorizes a
// context; callers must supply an already-authorized immutable context.
type SemanticService struct {
	profile             VectorProfile
	embedder            SemanticEmbedder
	store               SemanticStore
	lexical             *QueryService
	queryProviderBudget time.Duration
}

// NewSemanticService creates one profile-scoped semantic query workflow.
func NewSemanticService(profile VectorProfile, embedder SemanticEmbedder, store SemanticStore, lexical QueryStore) *SemanticService {
	return &SemanticService{
		profile:             profile,
		embedder:            embedder,
		store:               store,
		lexical:             NewQueryService(lexical),
		queryProviderBudget: semanticQueryProviderBudget,
	}
}

// SemanticEmbeddingInput returns the exact canonical chunk-scoped input sent to
// the provider for one current candidate and its digest. It is exported for
// storage adapters so cache lookup, write, and selection share one input identity.
func SemanticEmbeddingInput(profile VectorProfile, candidate QueryCandidate) (string, IndexDigest, error) {
	if err := validateSemanticProfile(profile); err != nil {
		return "", "", err
	}
	if !candidate.Context.valid() || !validQueryCandidate(candidate) {
		return "", "", fmt.Errorf("uci semantic: candidate is invalid")
	}
	if candidate.Text == "" || len(candidate.Text) > indexAdmissionMaxTextBytes {
		return "", "", fmt.Errorf("uci semantic: candidate text is outside the embedding bound")
	}
	chunkDigest := sha256.Sum256([]byte(candidate.Text))

	input := struct {
		Schema                string  `json:"schema"`
		PreprocessingRevision string  `json:"preprocessing_revision"`
		ContentDigest         string  `json:"content_digest"`
		Text                  string  `json:"text"`
		RelativePath          *string `json:"relative_path,omitempty"`
	}{
		Schema:                semanticInputSchema,
		PreprocessingRevision: profile.PreprocessingRevision,
		ContentDigest:         "sha256:" + hex.EncodeToString(chunkDigest[:]),
		Text:                  candidate.Text,
	}
	if profile.IncludeRelativePath {
		path := candidate.RelativePath
		input.RelativePath = &path
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", "", fmt.Errorf("uci semantic: encode candidate input: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return string(encoded), IndexDigest("sha256:" + hex.EncodeToString(digest[:])), nil
}

// EnsureCandidateEmbedding creates one provider vector only when no exact,
// current, profile-compatible cached vector exists.
func (service *SemanticService) EnsureCandidateEmbedding(ctx context.Context, authorized AuthorizedContext, candidate QueryCandidate) error {
	if err := service.validate(ctx, true); err != nil {
		return err
	}
	if !semanticContextMatches(candidate.Context, authorized.Ref()) {
		return fmt.Errorf("uci semantic: candidate is outside the authorized context")
	}
	input, _, err := SemanticEmbeddingInput(service.profile, candidate)
	if err != nil {
		return err
	}

	cached, found, err := service.store.LookupCandidateEmbedding(ctx, authorized, service.profile, candidate)
	if err != nil {
		return fmt.Errorf("uci semantic: lookup candidate embedding: %w", err)
	}
	if found {
		if err := validateSemanticVector(cached, service.profile.Dimension); err != nil {
			return fmt.Errorf("uci semantic: cached embedding is invalid: %w", err)
		}
		return nil
	}

	vector, err := service.embedOne(ctx, input)
	if err != nil {
		return err
	}
	if err := service.store.StoreCandidateEmbedding(ctx, authorized, service.profile, candidate, vector); err != nil {
		return fmt.Errorf("uci semantic: store candidate embedding: %w", err)
	}
	return nil
}

// Query executes one hybrid query inside the already-authorized View. Provider
// or vector coverage failures remain visible lexical-only results rather than
// fabricated semantic success.
func (service *SemanticService) Query(ctx context.Context, authorized AuthorizedContext, spec QuerySpec) (QueryResult, error) {
	if err := service.validate(ctx, false); err != nil {
		return QueryResult{}, err
	}
	ref := authorized.Ref()
	if !ref.valid() {
		return QueryResult{}, fmt.Errorf("uci semantic: authorized context is invalid")
	}
	normalized, err := normalizeQuerySpec(spec)
	if err != nil {
		return QueryResult{}, err
	}
	start, err := service.lexical.continuationOffset(ref, normalized)
	if err != nil {
		return QueryResult{}, err
	}

	storeSpec := normalized
	storeSpec.Offset = start
	lexicalSelection, unavailable, err := service.selectLexical(ctx, authorized, ref, storeSpec)
	if err != nil {
		return QueryResult{}, err
	}
	if unavailable != nil {
		return *unavailable, nil
	}
	lexical := lexicalSelection.candidates

	if reason := service.providerUnavailableReason(); reason != "" {
		return service.lexicalOnlyResult(ref, normalized, start, lexical, lexicalSelection.coverage, reason)
	}
	if semanticNil(service.store) {
		return service.lexicalOnlyResult(ref, normalized, start, lexical, lexicalSelection.coverage, "vector_store_unavailable")
	}

	input, err := semanticQueryEmbeddingInput(service.profile, normalized.Text)
	if err != nil {
		return QueryResult{}, err
	}
	vector, err := service.embedQuery(ctx, input)
	if err != nil {
		return service.lexicalOnlyResult(ref, normalized, start, lexical, lexicalSelection.coverage, semanticProviderDegradation(err))
	}

	semanticResult, err := service.store.SelectSemanticCandidates(ctx, authorized, service.profile, vector, storeSpec)
	if err != nil {
		return service.lexicalOnlyResult(ref, normalized, start, lexical, lexicalSelection.coverage, "vector_store_unavailable")
	}
	if !validQueryCoverage(semanticResult.Coverage) || !validSemanticCoverage(semanticResult.VectorCoverage) {
		return service.lexicalOnlyResult(ref, normalized, start, lexical, lexicalSelection.coverage, "vector_coverage_incomplete")
	}
	if semanticResult.Coverage != IndexCoverageComplete || semanticResult.VectorCoverage < 1 {
		return service.lexicalOnlyResult(ref, normalized, start, lexical, lexicalSelection.coverage, "vector_coverage_incomplete")
	}

	semantic := semanticCurrentCandidates(semanticResult.Candidates, ref)
	fused := semanticFuseCandidates(lexical, semantic, normalized.Mode, normalized.Order)
	return service.availableResult(semanticAvailableResultInput{
		ref:                ref,
		spec:               normalized,
		start:              start,
		candidates:         fused,
		coverage:           lexicalSelection.coverage,
		mode:               QueryRetrievalHybrid,
		vectorCoverage:     &semanticResult.VectorCoverage,
		degradationReasons: []string{},
	})
}

type semanticLexicalSelection struct {
	candidates []QueryCandidate
	coverage   IndexCoverageState
}

func (service *SemanticService) selectLexical(ctx context.Context, authorized AuthorizedContext, ref ContextRef, spec QuerySpec) (semanticLexicalSelection, *QueryResult, error) {
	result, err := service.lexical.store.SelectCandidates(ctx, authorized, spec)
	if err != nil {
		return semanticLexicalSelection{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		return semanticLexicalSelection{}, nil, err
	}
	if !validQueryCoverage(result.Coverage) {
		return semanticLexicalSelection{}, nil, fmt.Errorf("uci semantic: lexical store returned invalid coverage %q", result.Coverage)
	}
	if result.Unavailable == nil {
		if result.Coverage == IndexCoverageUnavailable {
			return semanticLexicalSelection{}, nil, fmt.Errorf("uci semantic: lexical unavailable coverage requires an unavailable outcome")
		}
		return semanticLexicalSelection{candidates: semanticCurrentCandidates(result.Candidates, ref), coverage: result.Coverage}, nil, nil
	}
	if err := result.Unavailable.Validate(); err != nil || !result.Unavailable.Code.isAuthorizedUnavailable() {
		return semanticLexicalSelection{}, nil, fmt.Errorf("uci semantic: lexical store returned invalid unavailable state")
	}
	if result.Coverage != IndexCoverageUnavailable {
		return semanticLexicalSelection{}, nil, fmt.Errorf("uci semantic: lexical unavailable result has available coverage")
	}
	unavailable := QueryResult{Response: queryUnavailableResponse(ref, result.Coverage, *result.Unavailable)}
	return semanticLexicalSelection{}, &unavailable, nil
}

func (service *SemanticService) lexicalOnlyResult(ref ContextRef, spec QuerySpec, start int, candidates []QueryCandidate, coverage IndexCoverageState, reason string) (QueryResult, error) {
	ordered := append([]QueryCandidate(nil), candidates...)
	orderQueryCandidates(ordered, spec.Order)
	results := make([]semanticResultCandidate, 0, len(ordered))
	for _, candidate := range ordered {
		results = append(results, semanticResultCandidate{
			candidate: candidate,
			sources:   []QueryMatchSource{semanticLexicalMatchSource(spec.Mode)},
		})
	}
	zero := float64(0)
	return service.availableResult(semanticAvailableResultInput{
		ref:                ref,
		spec:               spec,
		start:              start,
		candidates:         results,
		coverage:           coverage,
		mode:               QueryRetrievalLexical,
		vectorCoverage:     &zero,
		degradationReasons: []string{reason},
	})
}

type semanticAvailableResultInput struct {
	ref                ContextRef
	spec               QuerySpec
	start              int
	candidates         []semanticResultCandidate
	coverage           IndexCoverageState
	mode               QueryRetrievalMode
	vectorCoverage     *float64
	degradationReasons []string
}

func (service *SemanticService) availableResult(input semanticAvailableResultInput) (QueryResult, error) {
	if input.start > 0 && len(input.candidates) == 0 {
		return QueryResult{}, fmt.Errorf("uci semantic: continuation position is outside the selected view")
	}
	end := input.spec.Limit
	if end > len(input.candidates) {
		end = len(input.candidates)
	}
	items := make(QueryItems, 0, end)
	warnings := QueryWarnings{}
	for _, candidate := range input.candidates[:end] {
		item, excerptOmitted := semanticQueryItem(candidate, input.spec)
		items = append(items, item)
		if excerptOmitted && !containsQueryWarning(warnings, "excerpt_omitted_response_bound") {
			warnings = append(warnings, "excerpt_omitted_response_bound")
		}
	}

	truncated := end < len(input.candidates)
	continuation := QueryContinuation{}
	if truncated {
		token, err := service.lexical.encodeContinuation(input.ref, input.spec, input.start+end)
		if err != nil {
			return QueryResult{}, err
		}
		continuation.Value = &token
	}
	degradation := make([]string, len(input.degradationReasons))
	copy(degradation, input.degradationReasons)
	response := queryAvailableResponse(input.ref, input.spec, input.coverage, items, warnings, truncated, continuation)
	response.Retrieval = &QueryRetrieval{
		Mode:               input.mode,
		VectorCoverage:     input.vectorCoverage,
		DegradationReasons: degradation,
	}
	return QueryResult{Response: response}, nil
}

func (service *SemanticService) validate(ctx context.Context, requireStore bool) error {
	if service == nil || service.lexical == nil || semanticNil(service.lexical.store) {
		return fmt.Errorf("uci semantic: lexical store is not configured")
	}
	if ctx == nil {
		return fmt.Errorf("uci semantic: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSemanticProfile(service.profile); err != nil {
		return err
	}
	if requireStore && semanticNil(service.store) {
		return fmt.Errorf("uci semantic: store is not configured")
	}
	return nil
}

func (service *SemanticService) providerUnavailableReason() string {
	if semanticNil(service.embedder) {
		return "vector_provider_unavailable"
	}
	if model := service.embedder.Model(); model != service.profile.Model {
		return "vector_provider_model_mismatch"
	}
	return ""
}

func (service *SemanticService) embedQuery(ctx context.Context, input string) ([]float32, error) {
	budget := service.queryProviderBudget
	if budget <= 0 {
		budget = semanticQueryProviderBudget
	}
	providerCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return service.embedOne(providerCtx, input)
}

func (service *SemanticService) embedOne(ctx context.Context, input string) ([]float32, error) {
	if reason := service.providerUnavailableReason(); reason != "" {
		return nil, fmt.Errorf("uci semantic: %s", reason)
	}
	vectors, err := service.embedder.Embed(ctx, []string{input})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("uci semantic: provider returned %d vectors for one input", len(vectors))
	}
	if err := validateSemanticVector(vectors[0], service.profile.Dimension); err != nil {
		return nil, fmt.Errorf("uci semantic: provider vector is invalid: %w", err)
	}
	return vectors[0], nil
}

func validateSemanticProfile(profile VectorProfile) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"provider ref", profile.ProviderRef},
		{"model", profile.Model},
		{"preprocessing revision", profile.PreprocessingRevision},
	} {
		if !validSemanticProfileText(field.value) {
			return fmt.Errorf("uci semantic: %s is invalid", field.name)
		}
	}
	if profile.Dimension != semanticVectorDimension {
		return fmt.Errorf("uci semantic: dimension must be %d", semanticVectorDimension)
	}
	return nil
}

func validSemanticProfileText(value string) bool {
	if value == "" || len(value) > semanticMaxProfileText || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func semanticQueryEmbeddingInput(profile VectorProfile, text string) (string, error) {
	if err := validateSemanticProfile(profile); err != nil {
		return "", err
	}
	if !validQueryIdentity(text, queryMaxText) {
		return "", fmt.Errorf("uci semantic: query text is invalid")
	}
	input := struct {
		Schema                string `json:"schema"`
		PreprocessingRevision string `json:"preprocessing_revision"`
		Text                  string `json:"text"`
	}{
		Schema:                semanticInputSchema,
		PreprocessingRevision: profile.PreprocessingRevision,
		Text:                  text,
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("uci semantic: encode query input: %w", err)
	}
	return string(encoded), nil
}

func validateSemanticVector(vector []float32, dimension int) error {
	if len(vector) != dimension {
		return fmt.Errorf("dimension = %d, want %d", len(vector), dimension)
	}
	for index, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("non-finite value at index %d", index)
		}
	}
	return nil
}

func validSemanticCoverage(coverage float64) bool {
	return !math.IsNaN(coverage) && !math.IsInf(coverage, 0) && coverage >= 0 && coverage <= 1
}

func semanticProviderDegradation(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "vector_provider_timeout"
	}
	var status interface{ StatusCode() int }
	if errors.As(err, &status) && status.StatusCode() == 429 {
		return "vector_provider_quota"
	}
	return "vector_provider_unavailable"
}

func semanticCurrentCandidates(candidates []QueryCandidate, ref ContextRef) []QueryCandidate {
	current := make([]QueryCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if semanticContextMatches(candidate.Context, ref) && validQueryCandidate(candidate) {
			current = append(current, candidate)
		}
	}
	return current
}

type semanticResultCandidate struct {
	candidate QueryCandidate
	sources   []QueryMatchSource
}

type semanticCandidateKey struct {
	sourceID          string
	checkoutID        string
	viewID            string
	analysisProfileID string
	generation        int64
	artifactID        string
	contentDigest     IndexDigest
	entityKey         string
	relativePath      string
	byteStart         int64
	byteEnd           int64
}

type semanticFusionEntry struct {
	candidate QueryCandidate
	exact     bool
	fts       bool
	vector    bool
	score     float64
}

func semanticFuseCandidates(lexical, vector []QueryCandidate, mode QueryMode, order QueryOrder) []semanticResultCandidate {
	lexical = semanticRankCandidates(lexical)
	vector = semanticRankCandidates(vector)
	entries := make(map[semanticCandidateKey]*semanticFusionEntry, len(lexical)+len(vector))
	for rank, candidate := range lexical {
		key := semanticKey(candidate)
		entry := entries[key]
		if entry == nil {
			entry = &semanticFusionEntry{candidate: candidate}
			entries[key] = entry
		}
		entry.score += semanticRRFScore(rank)
		if mode == QueryModeFTS {
			entry.fts = true
		} else {
			entry.exact = true
		}
	}
	for rank, candidate := range vector {
		key := semanticKey(candidate)
		entry := entries[key]
		if entry == nil {
			entry = &semanticFusionEntry{candidate: candidate}
			entries[key] = entry
		}
		entry.score += semanticRRFScore(rank)
		entry.vector = true
	}

	fused := make([]semanticResultCandidate, 0, len(entries))
	for _, entry := range entries {
		candidate := entry.candidate
		candidate.Score = entry.score
		fused = append(fused, semanticResultCandidate{
			candidate: candidate,
			sources:   semanticMatchSources(entry.exact, entry.fts, entry.vector),
		})
	}
	sort.SliceStable(fused, func(left, right int) bool {
		return semanticCandidateLess(fused[left].candidate, fused[right].candidate, order)
	})
	return fused
}

func semanticRankCandidates(candidates []QueryCandidate) []QueryCandidate {
	ranked := append([]QueryCandidate(nil), candidates...)
	sort.SliceStable(ranked, func(left, right int) bool {
		return semanticCandidateLess(ranked[left], ranked[right], QueryOrderRelevance)
	})
	return ranked
}

func semanticCandidateLess(left, right QueryCandidate, order QueryOrder) bool {
	if order == QueryOrderRelevance && left.Score != right.Score {
		return left.Score > right.Score
	}
	if left.RelativePath != right.RelativePath {
		return left.RelativePath < right.RelativePath
	}
	if left.Span.ByteStart != right.Span.ByteStart {
		return left.Span.ByteStart < right.Span.ByteStart
	}
	if left.EntityKey != right.EntityKey {
		return left.EntityKey < right.EntityKey
	}
	if left.Proof.ArtifactID != right.Proof.ArtifactID {
		return left.Proof.ArtifactID < right.Proof.ArtifactID
	}
	return left.Proof.ContentDigest < right.Proof.ContentDigest
}

func semanticRRFScore(rank int) float64 {
	return 1 / (semanticRRFConstant + float64(rank) + 1)
}

func semanticKey(candidate QueryCandidate) semanticCandidateKey {
	return semanticCandidateKey{
		sourceID:          candidate.Context.SourceID,
		checkoutID:        candidate.Context.CheckoutID,
		viewID:            candidate.Context.ViewID,
		analysisProfileID: candidate.Context.AnalysisProfileID,
		generation:        candidate.Context.Generation,
		artifactID:        candidate.Proof.ArtifactID,
		contentDigest:     candidate.Proof.ContentDigest,
		entityKey:         candidate.EntityKey,
		relativePath:      candidate.RelativePath,
		byteStart:         candidate.Span.ByteStart,
		byteEnd:           candidate.Span.ByteEnd,
	}
}

func semanticMatchSources(exact, fts, vector bool) []QueryMatchSource {
	sources := make([]QueryMatchSource, 0, 3)
	if exact {
		sources = append(sources, QueryMatchExact)
	}
	if fts {
		sources = append(sources, QueryMatchFTS)
	}
	if vector {
		sources = append(sources, QueryMatchVector)
	}
	return sources
}

func semanticLexicalMatchSource(mode QueryMode) QueryMatchSource {
	if mode == QueryModeFTS {
		return QueryMatchFTS
	}
	return QueryMatchExact
}

func semanticQueryItem(candidate semanticResultCandidate, spec QuerySpec) (QueryItem, bool) {
	excerpt := candidate.candidate.Text
	excerptOmitted := utf8.RuneCountInString(excerpt) > queryMaxExcerpt
	if excerptOmitted {
		excerpt = ""
	}
	var score *float64
	if spec.Order == QueryOrderRelevance {
		value := candidate.candidate.Score
		score = &value
	}
	contentDigest, _ := queryBareContentDigest(candidate.candidate.Proof.ContentDigest)
	return QueryItem{
		Ref: QueryEntityRef{
			SourceID:  candidate.candidate.Context.SourceID,
			ViewID:    candidate.candidate.Context.ViewID,
			EntityKey: candidate.candidate.EntityKey,
		},
		Path: candidate.candidate.RelativePath,
		Span: QuerySpan{
			ByteStart: candidate.candidate.Span.ByteStart,
			ByteEnd:   candidate.candidate.Span.ByteEnd,
			LineStart: int64(candidate.candidate.Span.LineStart),
			LineEnd:   int64(candidate.candidate.Span.LineEnd),
		},
		ContentDigest: contentDigest,
		Kind:          candidate.candidate.Kind,
		Language:      candidate.candidate.Language,
		Excerpt:       excerpt,
		MatchSources:  append([]QueryMatchSource(nil), candidate.sources...),
		Score:         score,
	}, excerptOmitted
}

func semanticContextMatches(left, right ContextRef) bool {
	return queryOptionalStringEqual(left.SpaceID, right.SpaceID) &&
		left.SourceID == right.SourceID &&
		left.CheckoutID == right.CheckoutID &&
		left.ViewID == right.ViewID &&
		left.AnalysisProfileID == right.AnalysisProfileID &&
		left.Generation == right.Generation
}

func semanticNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
