package taskmemory

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/redaction"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/internal/vectordim"
	"github.com/thebtf/engram/pkg/models"
)

const (
	MaxPreparedCandidates       = 8
	MaxTaskQueryRunes           = 4096
	MaxMaterializedExcerptBytes = 768
	PreparationRevision         = "task-memory-prepare/1"
	maxQueryEmbeddingDuration   = 600 * time.Millisecond
)

// MaxCandidateLookups bounds pre-ranked exact references before authorization.
// It permits denied rows to be removed without consuming the final eight slots.
const MaxCandidateLookups = MaxPreparedCandidates * 5

var (
	ErrInvalidRequest = errors.New("task memory request is invalid")
	ErrUnauthorized   = errors.New("task memory authority is unauthorized")
	ErrUnavailable    = errors.New("task memory preparation is unavailable")
	ErrUnstable       = errors.New("task memory candidates are unstable")
)

// ProjectEvidenceV3 carries only the existing V3 evidence needed by an authority resolver.
type ProjectEvidenceV3 struct {
	Anchor     projectidentity.AnchorV3
	Descriptor projectidentity.DescriptorV3
}

// TaskFacts holds validated caller task text until Prepare applies runtime redaction.
type TaskFacts struct {
	query string
}

func NewTaskFacts(query string) (TaskFacts, error) {
	normalized, err := normalizeTaskQuery(query)
	if err != nil {
		return TaskFacts{}, err
	}
	return TaskFacts{query: normalized}, nil
}

func (f TaskFacts) Query() string {
	return f.query
}

// AuthenticatedCaller is the neutral, immutable authority projection admitted to task memory.
type AuthenticatedCaller struct {
	source      string
	role        string
	workstation WorkstationRef
	principal   PrincipalRef
	expiresAt   *time.Time
}

func NewAuthenticatedCaller(source, role, workstationID, principal, principalKind string, expiresAt *time.Time) (AuthenticatedCaller, error) {
	caller := AuthenticatedCaller{
		source:      strings.TrimSpace(source),
		role:        strings.TrimSpace(role),
		workstation: WorkstationRef{id: strings.TrimSpace(workstationID)},
		principal: PrincipalRef{
			principal: strings.TrimSpace(principal),
			kind:      strings.TrimSpace(principalKind),
		},
	}
	if expiresAt != nil {
		expiry := expiresAt.UTC()
		caller.expiresAt = &expiry
	}
	if caller.expiresAt != nil && !time.Now().UTC().Before(*caller.expiresAt) {
		return AuthenticatedCaller{}, ErrUnauthorized
	}
	if !caller.valid() {
		return AuthenticatedCaller{}, ErrUnauthorized
	}
	return caller, nil
}

func (c AuthenticatedCaller) valid() bool {
	if c.source != "client" || c.role != "read-write" || c.workstation.id == "" {
		return false
	}
	if c.principal.principal == "" {
		return c.principal.kind == ""
	}
	return validPrincipalKind(c.principal.kind)
}

func validPrincipalKind(kind string) bool {
	switch kind {
	case "human", "agent", "service":
		return true
	default:
		return false
	}
}

// PrincipalRef is the private principal projection retained by authorized context.
type PrincipalRef struct {
	principal string
	kind      string
}

// WorkstationRef is the private workstation projection retained by authorized context.
type WorkstationRef struct {
	id string
}

// AuthorizedTaskContext is a read-only, resolved task-memory authority capability.
type AuthorizedTaskContext struct {
	canonicalProject projectidentity.ProjectKeyV3
	resolvedScope    projectidentity.ResolvedScopeV3
	correlation      projectidentity.CorrelationV3
	principal        PrincipalRef
	workstation      WorkstationRef
	sessionID        string
}

func NewAuthorizedTaskContext(caller AuthenticatedCaller, resolution projectidentity.ResolutionResultV3, sessionID string) (AuthorizedTaskContext, error) {
	if !caller.valid() {
		return AuthorizedTaskContext{}, ErrUnauthorized
	}
	if caller.expiresAt != nil && !time.Now().UTC().Before(caller.expiresAt.UTC()) {
		return AuthorizedTaskContext{}, ErrUnauthorized
	}
	if resolution.Intent() != projectidentity.ReadFilterIntentV3 ||
		!resolution.Outcome().IsSuccess() ||
		resolution.CanonicalProjectKey() == "" ||
		resolution.ResolvedScope() == "" ||
		resolution.Correlation() == "" {
		return AuthorizedTaskContext{}, ErrUnauthorized
	}

	return AuthorizedTaskContext{
		canonicalProject: resolution.CanonicalProjectKey(),
		resolvedScope:    resolution.ResolvedScope(),
		correlation:      resolution.Correlation(),
		principal:        caller.principal,
		workstation:      caller.workstation,
		sessionID:        strings.TrimSpace(sessionID),
	}, nil
}

func (c AuthorizedTaskContext) CanonicalProject() projectidentity.ProjectKeyV3 {
	return c.canonicalProject
}

func (c AuthorizedTaskContext) ResolvedScope() projectidentity.ResolvedScopeV3 {
	return c.resolvedScope
}

func (c AuthorizedTaskContext) ResolutionCorrelation() projectidentity.CorrelationV3 {
	return c.correlation
}

func (c AuthorizedTaskContext) KeycardContext() scope.KeycardContext {
	return scope.KeycardContext{
		WorkstationID: c.workstation.id,
		SessionID:     c.sessionID,
		Principal:     c.principal.principal,
		PrincipalKind: c.principal.kind,
	}
}

func (c AuthorizedTaskContext) valid() bool {
	return c.canonicalProject != "" && c.resolvedScope != "" && c.correlation != "" && c.workstation.id != ""
}

// Valid reports whether this authority was produced by the task-memory boundary.
func (c AuthorizedTaskContext) Valid() bool {
	return c.valid()
}

// AuthorityResolver resolves V3 evidence into a scoped read authority.
type AuthorityResolver interface {
	ResolveTaskAuthority(context.Context, ProjectEvidenceV3) (AuthorizedTaskContext, error)
}

// QueryEmbedder is the minimal query-embedding seam shared with the existing client.
type QueryEmbedder interface {
	Embed(context.Context, []string) ([][]float32, error)
}

// CandidateSourceTier identifies the retrieval leg that produced a candidate reference.
type CandidateSourceTier uint8

const (
	CandidateExact CandidateSourceTier = iota + 1
	CandidateFTS
	CandidateVector
)

// RetrievalMode identifies the retrieval strategy used for a candidate snapshot.
type RetrievalMode uint8

const (
	RetrievalEmpty RetrievalMode = iota
	RetrievalExact
	RetrievalLexicalDegraded
	RetrievalHybrid
)

// CandidateLookup preserves the caller-selected source tier for an authorized by-reference read.
type CandidateLookup struct {
	id   int64
	tier CandidateSourceTier
}

func NewCandidateLookup(id int64, tier CandidateSourceTier) (CandidateLookup, error) {
	lookup := CandidateLookup{id: id, tier: tier}
	if !lookup.valid() {
		return CandidateLookup{}, ErrInvalidRequest
	}
	return lookup, nil
}

func (l CandidateLookup) ID() int64 {
	return l.id
}

func (l CandidateLookup) SourceTier() CandidateSourceTier {
	return l.tier
}

// Valid reports whether the lookup can be used at the authorized store boundary.
func (l CandidateLookup) Valid() bool {
	return l.valid()
}

func (l CandidateLookup) valid() bool {
	return l.id > 0 && validCandidateTier(l.tier)
}

// AuthorizedCandidateRef is an immutable, content-free candidate reference.
type AuthorizedCandidateRef struct {
	id      int64
	version int
	tier    CandidateSourceTier
}

func NewAuthorizedCandidateRef(id int64, version int, tier CandidateSourceTier) (AuthorizedCandidateRef, error) {
	candidate := AuthorizedCandidateRef{id: id, version: version, tier: tier}
	if !candidate.valid() {
		return AuthorizedCandidateRef{}, ErrInvalidRequest
	}
	return candidate, nil
}

func (c AuthorizedCandidateRef) ID() int64 {
	return c.id
}

func (c AuthorizedCandidateRef) Version() int {
	return c.version
}

func (c AuthorizedCandidateRef) SourceTier() CandidateSourceTier {
	return c.tier
}

func (c AuthorizedCandidateRef) valid() bool {
	return c.id > 0 && c.version > 0 && validCandidateTier(c.tier)
}

// Valid reports whether this exact candidate reference can cross a trusted read boundary.
func (c AuthorizedCandidateRef) Valid() bool {
	return c.valid()
}

// MaterializedCandidate is the sealed, body-free result of an exact authorized
// candidate materialization. It retains only the source pointer, SHA-256 of the
// exact stored text, and one safe bounded excerpt.
type MaterializedCandidate struct {
	reference     AuthorizedCandidateRef
	sourceProject projectidentity.ProjectKeyV3
	excerpt       string
	textDigest    [sha256.Size]byte
}

// NewMaterializedCandidate derives a safe context-reference value from the
// exact source text. Secret values are redacted before excerpting while the
// digest remains bound to the unmodified source text.
func NewMaterializedCandidate(reference AuthorizedCandidateRef, sourceProject string, sourceText string) (MaterializedCandidate, error) {
	project, err := projectidentity.NewProjectKeyV3(sourceProject)
	if err != nil || !reference.valid() || !utf8.ValidString(sourceText) {
		return MaterializedCandidate{}, ErrInvalidRequest
	}
	excerpt, ok := materializedExcerpt(privacy.RedactSecrets(sourceText))
	if !ok {
		return MaterializedCandidate{}, ErrInvalidRequest
	}
	candidate := MaterializedCandidate{
		reference:     reference,
		sourceProject: project,
		excerpt:       excerpt,
		textDigest:    sha256.Sum256([]byte(sourceText)),
	}
	if !candidate.valid() {
		return MaterializedCandidate{}, ErrInvalidRequest
	}
	return candidate, nil
}

// Reference returns the exact authorized source pointer without exposing the
// source body.
func (c MaterializedCandidate) Reference() AuthorizedCandidateRef {
	return c.reference
}

// SourceProject returns the canonical project that owned the materialized row.
func (c MaterializedCandidate) SourceProject() string {
	return string(c.sourceProject)
}

// Excerpt returns the safe bounded excerpt, never the full source body.
func (c MaterializedCandidate) Excerpt() string {
	return c.excerpt
}

// TextDigest returns the SHA-256 of the exact stored source text.
func (c MaterializedCandidate) TextDigest() [sha256.Size]byte {
	return c.textDigest
}

// Valid reports whether the sealed materialization has every exact source fact.
func (c MaterializedCandidate) Valid() bool {
	return c.valid()
}

func (c MaterializedCandidate) valid() bool {
	return c.reference.valid() && c.sourceProject != "" &&
		validMaterializedExcerpt(c.excerpt) && c.textDigest != ([sha256.Size]byte{})
}

func validMaterializedExcerpt(value string) bool {
	if value == "" || len(value) > MaxMaterializedExcerptBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func materializedExcerpt(source string) (string, bool) {
	for _, paragraph := range strings.Split(source, "\n\n") {
		paragraph = strings.Join(strings.Fields(paragraph), " ")
		if paragraph == "" {
			continue
		}
		if len(paragraph) <= MaxMaterializedExcerptBytes {
			return paragraph, true
		}
		if sentence, ok := firstBoundedSentence(paragraph); ok {
			return sentence, true
		}
	}
	return "", false
}

func firstBoundedSentence(paragraph string) (string, bool) {
	for index, runeValue := range paragraph {
		if !isSentenceTerminator(runeValue) {
			continue
		}
		end := index + utf8.RuneLen(runeValue)
		for end < len(paragraph) {
			runeValue, width := utf8.DecodeRuneInString(paragraph[end:])
			if !strings.ContainsRune("\"'”’)]}", runeValue) {
				break
			}
			end += width
		}
		sentence := strings.TrimSpace(paragraph[:end])
		if len(sentence) <= MaxMaterializedExcerptBytes {
			return sentence, true
		}
		return "", false
	}
	return "", false
}

func isSentenceTerminator(value rune) bool {
	switch value {
	case '.', '!', '?', '。', '！', '？':
		return true
	default:
		return false
	}
}

// Materializer exposes exact authorized materialization. false is the single
// indistinguishable result for denied, missing, and version-drifted references.
type Materializer interface {
	Materialize(context.Context, AuthorizedTaskContext, AuthorizedCandidateRef) (MaterializedCandidate, bool, error)
}

func validCandidateTier(tier CandidateSourceTier) bool {
	switch tier {
	case CandidateExact, CandidateFTS, CandidateVector:
		return true
	default:
		return false
	}
}

// AccessPolicy delegates candidate authorization to the existing scope and domain oracle.
type AccessPolicy struct {
	keycardContext scope.KeycardContext
	visibility     scope.MemoryVisibilityOptions
}

func NewAccessPolicy(context AuthorizedTaskContext) AccessPolicy {
	return AccessPolicy{
		keycardContext: context.KeycardContext(),
		visibility: scope.MemoryVisibilityOptions{
			ApplyPrivacyScope: true,
		},
	}
}

func (p AccessPolicy) KeycardContext() scope.KeycardContext {
	return p.keycardContext
}

func (p AccessPolicy) VisibilityOptions() scope.MemoryVisibilityOptions {
	return cloneVisibilityOptions(p.visibility)
}

func (p AccessPolicy) Allows(memory *models.Memory) bool {
	return scope.ResolveMemory(p.keycardContext, memory, p.visibility)
}

func (p AccessPolicy) copy() AccessPolicy {
	p.visibility = cloneVisibilityOptions(p.visibility)
	return p
}

func cloneVisibilityOptions(options scope.MemoryVisibilityOptions) scope.MemoryVisibilityOptions {
	copied := options
	if len(options.IncludeScopes) == 0 {
		copied.IncludeScopes = nil
		return copied
	}
	copied.IncludeScopes = make(map[string]bool, len(options.IncludeScopes))
	for scopeName, included := range options.IncludeScopes {
		copied.IncludeScopes[scopeName] = included
	}
	return copied
}

// AuthorizedCandidateQuery is a closed, provider-facing retrieval query.
type AuthorizedCandidateQuery struct {
	canonicalProject string
	query            string
	limit            int
	accessPolicy     AccessPolicy
	vector           []float32
}

func (q AuthorizedCandidateQuery) CanonicalProject() string {
	return q.canonicalProject
}

func (q AuthorizedCandidateQuery) Query() string {
	return q.query
}

func (q AuthorizedCandidateQuery) Limit() int {
	return q.limit
}

func (q AuthorizedCandidateQuery) AccessPolicy() AccessPolicy {
	return q.accessPolicy.copy()
}

// Vector returns an immutable copy of the validated query embedding, when present.
func (q AuthorizedCandidateQuery) Vector() []float32 {
	if q.vector == nil {
		return nil
	}
	vector := make([]float32, len(q.vector))
	copy(vector, q.vector)
	return vector
}

// VectorEnabled reports whether this query carries one validated query embedding.
func (q AuthorizedCandidateQuery) VectorEnabled() bool {
	return q.vector != nil && validEmbeddingVector(q.vector)
}

// Valid reports whether this query was constructed by an authorized Preparer.
func (q AuthorizedCandidateQuery) Valid() bool {
	if strings.TrimSpace(q.canonicalProject) == "" || strings.TrimSpace(q.canonicalProject) != q.canonicalProject || q.limit != MaxPreparedCandidates {
		return false
	}
	if q.vector != nil && !validEmbeddingVector(q.vector) {
		return false
	}
	normalized, err := normalizeTaskQuery(q.query)
	if err != nil || normalized != q.query {
		return false
	}
	caller := q.accessPolicy.keycardContext
	if strings.TrimSpace(caller.WorkstationID) == "" || strings.TrimSpace(caller.WorkstationID) != caller.WorkstationID || !q.accessPolicy.visibility.ApplyPrivacyScope {
		return false
	}
	if caller.Principal == "" {
		return caller.PrincipalKind == ""
	}
	return strings.TrimSpace(caller.Principal) == caller.Principal &&
		strings.TrimSpace(caller.PrincipalKind) == caller.PrincipalKind &&
		validPrincipalKind(caller.PrincipalKind)
}

func newAuthorizedCandidateQuery(context AuthorizedTaskContext, query string, vector []float32) AuthorizedCandidateQuery {
	var copiedVector []float32
	if vector != nil {
		copiedVector = make([]float32, len(vector))
		copy(copiedVector, vector)
	}
	return AuthorizedCandidateQuery{
		canonicalProject: string(context.CanonicalProject()),
		query:            query,
		limit:            MaxPreparedCandidates,
		accessPolicy:     NewAccessPolicy(context),
		vector:           copiedVector,
	}
}

func validEmbeddingVector(vector []float32) bool {
	if len(vector) != vectordim.Dimension {
		return false
	}
	normSquared := float64(0)
	for _, value := range vector {
		converted := float64(value)
		if math.IsNaN(converted) || math.IsInf(converted, 0) {
			return false
		}
		normSquared += converted * converted
	}
	return normSquared > 0 && !math.IsInf(normSquared, 0)
}

// CandidateSnapshot is an immutable candidate result from one provider read.
type CandidateSnapshot struct {
	mode       RetrievalMode
	candidates []AuthorizedCandidateRef
}

func NewCandidateSnapshot(mode RetrievalMode, candidates []AuthorizedCandidateRef) (CandidateSnapshot, error) {
	copied := append([]AuthorizedCandidateRef(nil), candidates...)
	snapshot := CandidateSnapshot{mode: mode, candidates: copied}
	if !snapshot.valid() {
		return CandidateSnapshot{}, ErrInvalidRequest
	}
	return snapshot, nil
}

func (s CandidateSnapshot) Mode() RetrievalMode {
	return s.mode
}

func (s CandidateSnapshot) Candidates() []AuthorizedCandidateRef {
	return append([]AuthorizedCandidateRef(nil), s.candidates...)
}

func (s CandidateSnapshot) valid() bool {
	if len(s.candidates) > MaxPreparedCandidates {
		return false
	}
	switch s.mode {
	case RetrievalEmpty:
		return len(s.candidates) == 0
	case RetrievalExact:
		if len(s.candidates) == 0 {
			return false
		}
		for _, candidate := range s.candidates {
			if !candidate.valid() || candidate.tier != CandidateExact {
				return false
			}
		}
	case RetrievalLexicalDegraded:
		if len(s.candidates) == 0 {
			return false
		}
		for _, candidate := range s.candidates {
			if !candidate.valid() || candidate.tier != CandidateFTS {
				return false
			}
		}
	case RetrievalHybrid:
		if len(s.candidates) == 0 {
			return false
		}
		for _, candidate := range s.candidates {
			if !candidate.valid() || (candidate.tier != CandidateFTS && candidate.tier != CandidateVector) {
				return false
			}
		}
	default:
		return false
	}
	for index, candidate := range s.candidates {
		for previous := range index {
			if s.candidates[previous].id == candidate.id {
				return false
			}
		}
	}
	return true
}

// AuthorizedCandidateProvider supplies one authorized candidate snapshot.
type AuthorizedCandidateProvider interface {
	Snapshot(context.Context, AuthorizedCandidateQuery) (CandidateSnapshot, error)
}

// StabilityMethod identifies the accepted consecutive-read stability rule.
type StabilityMethod uint8

const (
	StabilityMatchedDoubleRead StabilityMethod = iota + 1
	StabilityForwardConfirmedThirdRead
)

// StabilityEvidence records the accepted read pattern without exposing candidate contents.
type StabilityEvidence struct {
	method     StabilityMethod
	readCount  int
	refsDigest [sha256.Size]byte
}

// PreparedTaskMemory is an immutable task-memory preparation result.
type PreparedTaskMemory struct {
	context    AuthorizedTaskContext
	candidates []AuthorizedCandidateRef
	mode       RetrievalMode
	stability  StabilityEvidence
}

func (p PreparedTaskMemory) Context() AuthorizedTaskContext {
	return p.context
}

func (p PreparedTaskMemory) Candidates() []AuthorizedCandidateRef {
	return append([]AuthorizedCandidateRef(nil), p.candidates...)
}

func (p PreparedTaskMemory) Mode() RetrievalMode {
	return p.mode
}

func (p PreparedTaskMemory) Stability() StabilityEvidence {
	return p.stability
}

func (p PreparedTaskMemory) PreparationRevision() string {
	return PreparationRevision
}

// Preparer performs bounded, write-free task-memory preparation.
type Preparer interface {
	Prepare(context.Context, PrepareRequest) (PreparedTaskMemory, error)
}

// PrepareRequest holds the task facts and V3 evidence needed for one preparation.
type PrepareRequest struct {
	Project ProjectEvidenceV3
	Task    TaskFacts
}

// PreparerConfig configures the authority, candidate read, and optional embedding seams.
type PreparerConfig struct {
	Authority      AuthorityResolver
	Candidates     AuthorizedCandidateProvider
	Embedder       QueryEmbedder
	RedactionRules []redaction.CompiledRule
	MaxDuration    time.Duration
}

type preparer struct {
	authority      AuthorityResolver
	candidates     AuthorizedCandidateProvider
	embedder       QueryEmbedder
	redactionRules []redaction.CompiledRule
	maxDuration    time.Duration
}

func NewPreparer(config PreparerConfig) (Preparer, error) {
	if config.Authority == nil || config.Candidates == nil {
		return nil, ErrUnavailable
	}
	if config.MaxDuration < 0 || config.MaxDuration > 2*time.Second {
		return nil, ErrInvalidRequest
	}
	maxDuration := config.MaxDuration
	if maxDuration == 0 {
		maxDuration = 2 * time.Second
	}
	return &preparer{
		authority:      config.Authority,
		candidates:     config.Candidates,
		embedder:       config.Embedder,
		redactionRules: append([]redaction.CompiledRule(nil), config.RedactionRules...),
		maxDuration:    maxDuration,
	}, nil
}

func (p *preparer) Prepare(ctx context.Context, request PrepareRequest) (PreparedTaskMemory, error) {
	if ctx == nil {
		return PreparedTaskMemory{}, ErrInvalidRequest
	}
	preparedContext, cancel := context.WithTimeout(ctx, p.maxDuration)
	defer cancel()
	if preparedContext.Err() != nil {
		return PreparedTaskMemory{}, ErrUnavailable
	}

	query, containsSecret, err := sanitizePreparedQuery(request.Task.Query(), p.redactionRules)
	if err != nil {
		return PreparedTaskMemory{}, err
	}
	authority, err := p.authority.ResolveTaskAuthority(preparedContext, request.Project)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return PreparedTaskMemory{}, ErrUnauthorized
		}
		return PreparedTaskMemory{}, ErrUnavailable
	}
	if !authority.valid() {
		return PreparedTaskMemory{}, ErrUnauthorized
	}
	vector, err := p.embedQueryVector(preparedContext, query, containsSecret)
	if err != nil {
		return PreparedTaskMemory{}, err
	}
	queryForProvider := newAuthorizedCandidateQuery(authority, query, vector)

	first, err := p.snapshot(preparedContext, queryForProvider)
	if err != nil {
		return PreparedTaskMemory{}, err
	}
	second, err := p.snapshot(preparedContext, queryForProvider)
	if err != nil {
		return PreparedTaskMemory{}, err
	}
	if snapshotsEqual(first, second) {
		return preparedTaskMemory(authority, second, StabilityMatchedDoubleRead, 2), nil
	}
	third, err := p.snapshot(preparedContext, queryForProvider)
	if err != nil {
		return PreparedTaskMemory{}, err
	}
	if !snapshotsEqual(second, third) {
		return PreparedTaskMemory{}, ErrUnstable
	}
	return preparedTaskMemory(authority, third, StabilityForwardConfirmedThirdRead, 3), nil
}

func (p *preparer) snapshot(ctx context.Context, query AuthorizedCandidateQuery) (CandidateSnapshot, error) {
	if ctx.Err() != nil {
		return CandidateSnapshot{}, ErrUnavailable
	}
	snapshot, err := p.candidates.Snapshot(ctx, query)
	if err != nil || ctx.Err() != nil {
		return CandidateSnapshot{}, ErrUnavailable
	}
	copied := CandidateSnapshot{
		mode:       snapshot.mode,
		candidates: append([]AuthorizedCandidateRef(nil), snapshot.candidates...),
	}
	if !copied.valid() {
		return CandidateSnapshot{}, ErrUnavailable
	}
	return copied, nil
}

func (p *preparer) embedQueryVector(ctx context.Context, query string, containsSecret bool) ([]float32, error) {
	if ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	if containsSecret || p.embedder == nil {
		return nil, nil
	}
	embedContext, cancel := context.WithTimeout(ctx, maxQueryEmbeddingDuration)
	vectors, err := p.embedder.Embed(embedContext, []string{query})
	embedTimedOut := embedContext.Err() != nil
	cancel()
	if ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	if err != nil || embedTimedOut || len(vectors) != 1 || !validEmbeddingVector(vectors[0]) {
		return nil, nil
	}
	return vectors[0], nil
}

func sanitizePreparedQuery(query string, rules []redaction.CompiledRule) (string, bool, error) {
	normalized, err := normalizeTaskQuery(query)
	if err != nil {
		return "", false, err
	}
	containsSecret := privacy.ContainsSecrets(normalized)
	if containsSecret {
		normalized = privacy.RedactSecrets(normalized)
	}
	scrubbed, _, err := redaction.ScrubCompiled(normalized, rules)
	if err != nil {
		return "", false, ErrInvalidRequest
	}
	sanitized, err := normalizeTaskQuery(scrubbed)
	if err != nil {
		return "", false, err
	}
	return sanitized, containsSecret, nil
}

func normalizeTaskQuery(query string) (string, error) {
	if !utf8.ValidString(query) {
		return "", ErrInvalidRequest
	}
	query = strings.TrimSpace(query)
	if query == "" || strings.IndexByte(query, 0) >= 0 || utf8.RuneCountInString(query) > MaxTaskQueryRunes {
		return "", ErrInvalidRequest
	}
	return query, nil
}

func snapshotsEqual(left, right CandidateSnapshot) bool {
	if left.mode != right.mode || len(left.candidates) != len(right.candidates) {
		return false
	}
	for index, candidate := range left.candidates {
		if candidate != right.candidates[index] {
			return false
		}
	}
	return true
}

func preparedTaskMemory(authority AuthorizedTaskContext, snapshot CandidateSnapshot, method StabilityMethod, readCount int) PreparedTaskMemory {
	return PreparedTaskMemory{
		context:    authority,
		candidates: snapshot.Candidates(),
		mode:       snapshot.Mode(),
		stability: StabilityEvidence{
			method:     method,
			readCount:  readCount,
			refsDigest: snapshotDigest(snapshot),
		},
	}
}

func snapshotDigest(snapshot CandidateSnapshot) [sha256.Size]byte {
	var encoded [2 + MaxPreparedCandidates*17]byte
	encoded[0] = byte(snapshot.mode)
	encoded[1] = byte(len(snapshot.candidates))
	offset := 2
	for _, candidate := range snapshot.candidates {
		binary.BigEndian.PutUint64(encoded[offset:], uint64(candidate.id))
		offset += 8
		binary.BigEndian.PutUint64(encoded[offset:], uint64(candidate.version))
		offset += 8
		encoded[offset] = byte(candidate.tier)
		offset++
	}
	return sha256.Sum256(encoded[:offset])
}
