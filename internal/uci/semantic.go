package uci

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	semanticVectorDimension = 1536
	// SemanticRRFConstant is the fixed reciprocal-rank-fusion offset used by every semantic lane.
	SemanticRRFConstant = 60
	// SemanticRetrievalCandidatePoolLimit bounds each ranked lane before fusion.
	// Four response pages retain rank-51 overlap while keeping the retrieval pool
	// independent from caller-controlled response pagination.
	SemanticRetrievalCandidatePoolLimit = 4 * queryMaxItems
	semanticInputSchema                 = "engram.uci-semantic-input/2"
	semanticMaxProfileText              = 512
	semanticQueryProviderBudget         = 20 * time.Second
	semanticHybridRankingRevision       = "engram.uci-semantic-rrf/2"
	semanticLexicalRankingRevision      = "engram.uci-semantic-lexical/1"
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
	SelectHybridCandidates(context.Context, AuthorizedContext, VectorProfile, []float32, QuerySpec) (SemanticStoreResult, error)
}

// SemanticCandidate is one bounded-pool-fused, page-ordered semantic retrieval hit.
type SemanticCandidate struct {
	Candidate    QueryCandidate
	MatchSources []QueryMatchSource
}

// SemanticStoreResult contains one page from the stable fused retrieval pool
// and the scoped availability evidence that makes hybrid retrieval honest.
// The pool is a bounded per-lane top-K candidate set, not an exhaustive corpus rank.
type SemanticStoreResult struct {
	Candidates     []SemanticCandidate
	Coverage       IndexCoverageState
	VectorCoverage float64
	Unavailable    *QueryError
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
	query, result, err := service.prepareSemanticQuery(ctx, authorized, spec)
	if err != nil {
		return QueryResult{}, err
	}
	if result != nil {
		return *result, nil
	}
	return service.queryHybrid(ctx, authorized, query)
}

type semanticQueryState struct {
	ref          ContextRef
	spec         QuerySpec
	continuation semanticContinuationState
}

func (service *SemanticService) prepareSemanticQuery(ctx context.Context, authorized AuthorizedContext, spec QuerySpec) (semanticQueryState, *QueryResult, error) {
	ref := authorized.Ref()
	if !ref.valid() {
		return semanticQueryState{}, nil, fmt.Errorf("uci semantic: authorized context is invalid")
	}
	normalized, err := normalizeQuerySpec(spec)
	if err != nil {
		return semanticQueryState{}, nil, err
	}
	continuation, err := service.semanticContinuation(ref, normalized)
	if err != nil {
		return semanticQueryState{}, nil, err
	}
	query := semanticQueryState{ref: ref, spec: normalized, continuation: continuation}
	if continuation.present && continuation.mode == QueryRetrievalLexical {
		if continuation.rankingDigest != semanticLexicalRankingDigest() {
			return semanticQueryState{}, nil, fmt.Errorf("uci semantic: continuation ranking does not match request")
		}
		result, err := service.lexicalResult(ctx, authorized, ref, normalized, continuation.offset, []string{})
		if err != nil {
			return semanticQueryState{}, nil, err
		}
		return query, &result, nil
	}
	if continuation.present && continuation.mode != QueryRetrievalHybrid {
		return semanticQueryState{}, nil, fmt.Errorf("uci semantic: continuation retrieval mode is invalid")
	}
	return query, nil, nil
}

func (service *SemanticService) queryHybrid(ctx context.Context, authorized AuthorizedContext, query semanticQueryState) (QueryResult, error) {
	if reason := service.providerUnavailableReason(); reason != "" {
		return service.fallbackToLexical(ctx, authorized, query, reason, nil)
	}
	if semanticNil(service.store) {
		return service.fallbackToLexical(ctx, authorized, query, "vector_store_unavailable", nil)
	}
	input, err := semanticQueryEmbeddingInput(service.profile, query.spec.Text)
	if err != nil {
		return QueryResult{}, err
	}
	vector, err := service.embedQuery(ctx, input)
	if err != nil {
		return service.fallbackToLexical(ctx, authorized, query, semanticProviderDegradation(err), err)
	}
	rankingDigest := semanticHybridRankingDigest(service.profile, vector)
	if query.continuation.present && query.continuation.rankingDigest != rankingDigest {
		return QueryResult{}, fmt.Errorf("uci semantic: continuation ranking does not match request")
	}
	storeSpec := query.spec
	storeSpec.Offset = query.continuation.offset
	semanticResult, err := service.store.SelectHybridCandidates(ctx, authorized, service.profile, vector, storeSpec)
	if err != nil {
		if query.continuation.present {
			return QueryResult{}, err
		}
		return service.fallbackToLexical(ctx, authorized, query, "vector_store_unavailable", nil)
	}
	return service.hybridResult(ctx, authorized, query, rankingDigest, semanticResult)
}

func (service *SemanticService) hybridResult(ctx context.Context, authorized AuthorizedContext, query semanticQueryState, rankingDigest string, semanticResult SemanticStoreResult) (QueryResult, error) {
	if semanticResult.Unavailable != nil {
		if err := semanticUnavailableResult(query.ref, semanticResult); err != nil {
			return QueryResult{}, err
		}
		if query.continuation.present {
			return QueryResult{}, fmt.Errorf("uci semantic: continuation ranking is unavailable")
		}
		return QueryResult{Response: queryUnavailableResponse(query.ref, semanticResult.Coverage, *semanticResult.Unavailable)}, nil
	}
	if !validQueryCoverage(semanticResult.Coverage) || semanticResult.Coverage == IndexCoverageUnavailable || !validSemanticCoverage(semanticResult.VectorCoverage) {
		return QueryResult{}, fmt.Errorf("uci semantic: semantic store returned invalid coverage")
	}
	if semanticResult.VectorCoverage < 1 {
		return service.fallbackToLexical(ctx, authorized, query, "vector_coverage_incomplete", nil)
	}
	for _, candidate := range semanticResult.Candidates {
		if !semanticHybridCandidateValid(candidate, query.ref, query.spec.Mode) {
			return QueryResult{}, fmt.Errorf("uci semantic: store returned an invalid fused candidate")
		}
	}
	return service.availableResult(semanticAvailableResultInput{
		ref:                query.ref,
		spec:               query.spec,
		start:              query.continuation.offset,
		candidates:         semanticResult.Candidates,
		coverage:           semanticResult.Coverage,
		mode:               QueryRetrievalHybrid,
		rankingDigest:      rankingDigest,
		vectorCoverage:     &semanticResult.VectorCoverage,
		degradationReasons: []string{},
	})
}

func (service *SemanticService) fallbackToLexical(ctx context.Context, authorized AuthorizedContext, query semanticQueryState, reason string, cause error) (QueryResult, error) {
	if query.continuation.present {
		if cause != nil {
			return QueryResult{}, fmt.Errorf("uci semantic: continuation ranking is unavailable: %w", cause)
		}
		return QueryResult{}, fmt.Errorf("uci semantic: continuation ranking is unavailable")
	}
	return service.lexicalResult(ctx, authorized, query.ref, query.spec, 0, []string{reason})
}

type semanticContinuationState struct {
	offset        int
	mode          QueryRetrievalMode
	rankingDigest string
	present       bool
}

func (service *SemanticService) semanticContinuation(ref ContextRef, spec QuerySpec) (semanticContinuationState, error) {
	if spec.Continuation == nil {
		return semanticContinuationState{}, nil
	}
	payload, err := service.lexical.decodeContinuation(*spec.Continuation)
	if err != nil {
		return semanticContinuationState{}, err
	}
	if !queryContinuationMatchesBase(payload, ref, spec) || payload.RankingDigest == "" {
		return semanticContinuationState{}, fmt.Errorf("uci semantic: continuation binding does not match request")
	}
	if payload.RetrievalMode != QueryRetrievalHybrid && payload.RetrievalMode != QueryRetrievalLexical {
		return semanticContinuationState{}, fmt.Errorf("uci semantic: continuation retrieval mode is invalid")
	}
	return semanticContinuationState{
		offset:        payload.Offset,
		mode:          payload.RetrievalMode,
		rankingDigest: payload.RankingDigest,
		present:       true,
	}, nil
}

func semanticUnavailableResult(ref ContextRef, result SemanticStoreResult) error {
	if err := result.Unavailable.Validate(); err != nil || !result.Unavailable.Code.isAuthorizedUnavailable() || result.Coverage != IndexCoverageUnavailable {
		return fmt.Errorf("uci semantic: store returned invalid unavailable state")
	}
	return nil
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

func (service *SemanticService) lexicalResult(ctx context.Context, authorized AuthorizedContext, ref ContextRef, spec QuerySpec, start int, degradationReasons []string) (QueryResult, error) {
	pageSpec := spec
	pageSpec.Offset = start
	selection, unavailable, err := service.selectLexical(ctx, authorized, ref, pageSpec)
	if err != nil {
		return QueryResult{}, err
	}
	if unavailable != nil {
		return *unavailable, nil
	}
	ordered := append([]QueryCandidate(nil), selection.candidates...)
	orderQueryCandidates(ordered, spec.Order)
	candidates := make([]SemanticCandidate, 0, len(ordered))
	for _, candidate := range ordered {
		candidates = append(candidates, SemanticCandidate{
			Candidate:    candidate,
			MatchSources: []QueryMatchSource{semanticLexicalMatchSource(spec.Mode)},
		})
	}
	zero := float64(0)
	return service.availableResult(semanticAvailableResultInput{
		ref:                ref,
		spec:               spec,
		start:              start,
		candidates:         candidates,
		coverage:           selection.coverage,
		mode:               QueryRetrievalLexical,
		rankingDigest:      semanticLexicalRankingDigest(),
		vectorCoverage:     &zero,
		degradationReasons: degradationReasons,
	})
}

type semanticAvailableResultInput struct {
	ref                ContextRef
	spec               QuerySpec
	start              int
	candidates         []SemanticCandidate
	coverage           IndexCoverageState
	mode               QueryRetrievalMode
	rankingDigest      string
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
		payload := queryContinuationPayloadFor(input.ref, input.spec, input.start+end)
		payload.RetrievalMode = input.mode
		payload.RankingDigest = input.rankingDigest
		token, err := service.lexical.encodeContinuationPayload(payload)
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

func semanticHybridCandidateValid(candidate SemanticCandidate, ref ContextRef, mode QueryMode) bool {
	if !semanticContextMatches(candidate.Candidate.Context, ref) || !validQueryCandidate(candidate.Candidate) {
		return false
	}
	if len(candidate.MatchSources) == 1 {
		return candidate.MatchSources[0] == QueryMatchVector
	}
	return len(candidate.MatchSources) == 2 &&
		candidate.MatchSources[0] == semanticLexicalMatchSource(mode) &&
		candidate.MatchSources[1] == QueryMatchVector
}

func semanticHybridRankingDigest(profile VectorProfile, vector []float32) string {
	hash := sha256.New()
	writeString := func(value string) {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(value))
	}
	writeString(semanticHybridRankingRevision)
	writeString(profile.ProviderRef)
	writeString(profile.Model)
	writeString(profile.PreprocessingRevision)
	var dimension [8]byte
	binary.BigEndian.PutUint64(dimension[:], uint64(profile.Dimension))
	_, _ = hash.Write(dimension[:])
	if profile.IncludeRelativePath {
		_, _ = hash.Write([]byte{1})
	} else {
		_, _ = hash.Write([]byte{0})
	}
	var bits [4]byte
	for _, value := range vector {
		binary.BigEndian.PutUint32(bits[:], math.Float32bits(value))
		_, _ = hash.Write(bits[:])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func semanticLexicalRankingDigest() string {
	return queryContinuationDigest("semantic-ranking", []string{semanticLexicalRankingRevision})
}

func semanticLexicalMatchSource(mode QueryMode) QueryMatchSource {
	if mode == QueryModeFTS {
		return QueryMatchFTS
	}
	return QueryMatchExact
}

func semanticQueryItem(candidate SemanticCandidate, spec QuerySpec) (QueryItem, bool) {
	excerpt := candidate.Candidate.Text
	excerptOmitted := utf8.RuneCountInString(excerpt) > queryMaxExcerpt
	if excerptOmitted {
		excerpt = ""
	}
	var score *float64
	if spec.Order == QueryOrderRelevance {
		value := candidate.Candidate.Score
		score = &value
	}
	contentDigest, _ := queryBareContentDigest(candidate.Candidate.Proof.ContentDigest)
	return QueryItem{
		Ref: QueryEntityRef{
			SourceID:  candidate.Candidate.Context.SourceID,
			ViewID:    candidate.Candidate.Context.ViewID,
			EntityKey: candidate.Candidate.EntityKey,
		},
		Path: candidate.Candidate.RelativePath,
		Span: QuerySpan{
			ByteStart: candidate.Candidate.Span.ByteStart,
			ByteEnd:   candidate.Candidate.Span.ByteEnd,
			LineStart: int64(candidate.Candidate.Span.LineStart),
			LineEnd:   int64(candidate.Candidate.Span.LineEnd),
		},
		ContentDigest: contentDigest,
		Kind:          candidate.Candidate.Kind,
		Language:      candidate.Candidate.Language,
		Excerpt:       excerpt,
		MatchSources:  append([]QueryMatchSource(nil), candidate.MatchSources...),
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
