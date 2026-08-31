package taskmemory

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/redaction"
	"github.com/thebtf/engram/internal/vectordim"
	"github.com/thebtf/engram/pkg/models"
)

type recordingAuthorityResolver struct {
	authority AuthorizedTaskContext
	err       error
	calls     int
	evidence  ProjectEvidenceV3
	deadline  time.Time
}

func (r *recordingAuthorityResolver) ResolveTaskAuthority(ctx context.Context, evidence ProjectEvidenceV3) (AuthorizedTaskContext, error) {
	r.calls++
	r.evidence = evidence
	if deadline, ok := ctx.Deadline(); ok {
		r.deadline = deadline
	}
	return r.authority, r.err
}

type recordingQueryEmbedder struct {
	vectors      [][]float32
	err          error
	calls        int
	texts        [][]string
	calledAt     time.Time
	deadline     time.Time
	beforeReturn func()
}

func (e *recordingQueryEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.calls++
	e.calledAt = time.Now()
	e.texts = append(e.texts, append([]string(nil), texts...))
	if deadline, ok := ctx.Deadline(); ok {
		e.deadline = deadline
	}
	if e.beforeReturn != nil {
		e.beforeReturn()
	}
	return e.vectors, e.err
}

type scriptedCandidateProvider struct {
	snapshots []CandidateSnapshot
	err       error
	calls     int
	queries   []AuthorizedCandidateQuery
}

func (p *scriptedCandidateProvider) Snapshot(_ context.Context, query AuthorizedCandidateQuery) (CandidateSnapshot, error) {
	p.calls++
	p.queries = append(p.queries, query)
	if p.err != nil {
		return CandidateSnapshot{}, p.err
	}
	index := p.calls - 1
	if index >= len(p.snapshots) {
		return CandidateSnapshot{}, errors.New("unexpected candidate read")
	}
	return p.snapshots[index], nil
}

func TestTaskFactsRejectUnsafeQuery(t *testing.T) {
	cases := []string{
		" \t\n ",
		"contains\x00nul",
		string([]byte{0xff}),
		strings.Repeat("a", MaxTaskQueryRunes+1),
	}
	for _, query := range cases {
		if _, err := NewTaskFacts(query); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("query %q error=%v, want ErrInvalidRequest", query, err)
		}
	}

	facts, err := NewTaskFacts("  bounded task query  ")
	if err != nil {
		t.Fatal(err)
	}
	if got := facts.Query(); got != "bounded task query" {
		t.Fatalf("query=%q", got)
	}
}

func TestAuthenticatedCallerAndAuthorityValidation(t *testing.T) {
	for _, test := range []struct {
		name                              string
		source, role, workstation, nameID string
		principalKind                     string
	}{
		{name: "wrong source", source: "master", role: "read-write", workstation: "ws-a"},
		{name: "wrong role", source: "client", role: "read-only", workstation: "ws-a"},
		{name: "missing workstation", source: "client", role: "read-write"},
		{name: "kind without principal", source: "client", role: "read-write", workstation: "ws-a", principalKind: "agent"},
		{name: "invalid principal kind", source: "client", role: "read-write", workstation: "ws-a", nameID: "alice", principalKind: "invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewAuthenticatedCaller(test.source, test.role, test.workstation, test.nameID, test.principalKind, nil)
			if !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("error=%v, want ErrUnauthorized", err)
			}
		})
	}

	future := time.Now().UTC().Add(time.Hour)
	caller, err := NewAuthenticatedCaller("client", "read-write", "ws-a", "alice", "agent", &future)
	if err != nil {
		t.Fatal(err)
	}
	future = time.Now().UTC().Add(-time.Hour)
	context, err := NewAuthorizedTaskContext(caller, testResolution(t, projectidentity.ReadFilterIntentV3, projectidentity.ProjectResolvedOutcomeV3), " source-session ")
	if err != nil {
		t.Fatal(err)
	}
	if got := context.KeycardContext(); got.WorkstationID != "ws-a" || got.SessionID != "source-session" || got.Principal != "alice" || got.PrincipalKind != "agent" {
		t.Fatalf("keycard context=%#v", got)
	}

	expired := time.Now().UTC().Add(-time.Second)
	if _, err := NewAuthenticatedCaller("client", "read-write", "ws-a", "", "", &expired); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired caller error=%v", err)
	}
	if _, err := NewAuthorizedTaskContext(caller, testResolution(t, projectidentity.ResolveExistingIntentV3, projectidentity.ProjectResolvedOutcomeV3), ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong intent error=%v", err)
	}
	if _, err := NewAuthorizedTaskContext(caller, testResolution(t, projectidentity.ReadFilterIntentV3, projectidentity.ProjectOnboardingRequiredOutcomeV3), ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("refusal error=%v", err)
	}
}

func TestCandidateLookupValidationAndValueOwnership(t *testing.T) {
	lookup, err := NewCandidateLookup(41, CandidateVector)
	if err != nil {
		t.Fatal(err)
	}
	if lookup.ID() != 41 || lookup.SourceTier() != CandidateVector {
		t.Fatalf("lookup=%#v", lookup)
	}
	if !lookup.Valid() || (CandidateLookup{}).Valid() {
		t.Fatal("lookup validity contract is not closed")
	}
	copied := lookup
	lookup.id = 99
	if copied.ID() != 41 || copied.SourceTier() != CandidateVector {
		t.Fatalf("copied lookup=%#v", copied)
	}
	for _, lookup := range []CandidateLookup{
		{id: 0, tier: CandidateExact},
		{id: 42, tier: CandidateSourceTier(99)},
	} {
		if _, err := NewCandidateLookup(lookup.id, lookup.tier); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("lookup=%#v error=%v", lookup, err)
		}
	}
}

func TestCandidateSnapshotRejectsBoundsDuplicatesAndUnknownTiers(t *testing.T) {
	overLimit := make([]AuthorizedCandidateRef, MaxPreparedCandidates+1)
	for index := range overLimit {
		overLimit[index] = AuthorizedCandidateRef{id: int64(index + 1), version: 1, tier: CandidateExact}
	}
	for _, snapshot := range []CandidateSnapshot{
		{mode: RetrievalExact, candidates: overLimit},
		{mode: RetrievalExact, candidates: []AuthorizedCandidateRef{{id: 1, version: 1, tier: CandidateExact}, {id: 1, version: 2, tier: CandidateExact}}},
		{mode: RetrievalExact, candidates: []AuthorizedCandidateRef{{id: 1, version: 1, tier: CandidateSourceTier(99)}}},
	} {
		if _, err := NewCandidateSnapshot(snapshot.mode, snapshot.candidates); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("snapshot=%#v error=%v", snapshot, err)
		}
	}
}

func TestCandidateSnapshotAndPreparedCandidatesAreCopied(t *testing.T) {
	first := testCandidate(t, 1, CandidateExact)
	second := testCandidate(t, 2, CandidateExact)
	input := []AuthorizedCandidateRef{first}
	snapshot, err := NewCandidateSnapshot(RetrievalExact, input)
	if err != nil {
		t.Fatal(err)
	}
	input[0] = second
	if got := snapshot.Candidates(); len(got) != 1 || got[0].ID() != first.ID() {
		t.Fatalf("snapshot copied input=%#v", got)
	}
	view := snapshot.Candidates()
	view[0] = second
	if got := snapshot.Candidates(); len(got) != 1 || got[0].ID() != first.ID() {
		t.Fatalf("snapshot exposed mutable candidates=%#v", got)
	}

	resolver := &recordingAuthorityResolver{authority: testAuthority(t, "alice", "agent")}
	provider := &scriptedCandidateProvider{snapshots: []CandidateSnapshot{snapshot, snapshot}}
	prepared, err := testPreparer(t, resolver, provider, nil).Prepare(context.Background(), testPrepareRequest(t, "copy ownership"))
	if err != nil {
		t.Fatal(err)
	}
	preparedView := prepared.Candidates()
	preparedView[0] = second
	if got := prepared.Candidates(); len(got) != 1 || got[0].ID() != first.ID() {
		t.Fatalf("prepared candidates=%#v", got)
	}
}

func TestAuthorizedCandidateQueryVectorValidationAndOwnership(t *testing.T) {
	vector := testEmbeddingVector()
	query := newAuthorizedCandidateQuery(testAuthority(t, "alice", "agent"), "vector task", vector)
	if !query.Valid() || !query.VectorEnabled() {
		t.Fatalf("valid query=%#v", query)
	}
	vector[0] = 0.9
	if got := query.Vector(); len(got) != vectordim.Dimension {
		t.Fatalf("query vector length=%d", len(got))
	} else if got[0] != 0.5 {
		t.Fatalf("query vector first value=%v", got[0])
	}
	view := query.Vector()
	view[0] = 0.8
	if got := query.Vector()[0]; got != 0.5 {
		t.Fatalf("query vector exposed mutable storage=%v", got)
	}

	withoutVector := newAuthorizedCandidateQuery(testAuthority(t, "alice", "agent"), "lexical task", nil)
	if !withoutVector.Valid() || withoutVector.VectorEnabled() || withoutVector.Vector() != nil {
		t.Fatalf("lexical query=%#v", withoutVector)
	}

	nonFinite := testEmbeddingVector()
	nonFinite[1] = float32(math.NaN())
	for _, vector := range [][]float32{
		{},
		make([]float32, vectordim.Dimension-1),
		make([]float32, vectordim.Dimension),
		nonFinite,
	} {
		malformed := query
		malformed.vector = vector
		if malformed.Valid() || malformed.VectorEnabled() {
			t.Fatalf("malformed vector accepted: len=%d", len(vector))
		}
	}
}

func TestAccessPolicyUsesCurrentScopeAndDomainOracle(t *testing.T) {
	allowedContext := testAuthority(t, "alice", "agent")
	policy := NewAccessPolicy(allowedContext)
	memory := &models.Memory{
		PrivacyScope:        "private",
		SourceWorkstationID: "ws-test",
		SourceSessions:      []string{"source-session"},
		AgentVisibility:     models.AgentVisibilityPrivate,
		OwnerPrincipal:      "alice",
		OwnerPrincipalKind:  "agent",
		Domain:              "owned-domain",
	}
	if !policy.Allows(memory) {
		t.Fatal("same principal, workstation, session, and domain owner must be allowed")
	}
	if NewAccessPolicy(testAuthority(t, "bob", "agent")).Allows(memory) {
		t.Fatal("scope/domain oracle must deny a cross-principal memory")
	}
	if !policy.VisibilityOptions().ApplyPrivacyScope {
		t.Fatal("task memory must use current privacy scope policy")
	}
	if (AuthorizedCandidateQuery{}).Valid() {
		t.Fatal("zero candidate query must be invalid")
	}
}

func TestPreparerConfigAndExactRepeatStability(t *testing.T) {
	resolver := &recordingAuthorityResolver{authority: testAuthority(t, "alice", "agent")}
	provider := &scriptedCandidateProvider{}
	if _, err := NewPreparer(PreparerConfig{Candidates: provider}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing authority error=%v", err)
	}
	if _, err := NewPreparer(PreparerConfig{Authority: resolver}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing provider error=%v", err)
	}
	if _, err := NewPreparer(PreparerConfig{Authority: resolver, Candidates: provider, MaxDuration: 2*time.Second + time.Nanosecond}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("overlong duration error=%v", err)
	}

	snapshot := testSnapshot(t, RetrievalExact, testCandidate(t, 5, CandidateExact))
	provider.snapshots = []CandidateSnapshot{snapshot, snapshot}
	preparer := testPreparer(t, resolver, provider, nil)
	deadline := time.Now().UTC().Add(time.Second)
	requestContext, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	prepared, err := preparer.Prepare(requestContext, testPrepareRequest(t, " exact task "))
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 || resolver.calls != 1 {
		t.Fatalf("provider calls=%d resolver calls=%d", provider.calls, resolver.calls)
	}
	if len(provider.queries) != 2 || !provider.queries[0].Valid() || !provider.queries[1].Valid() {
		t.Fatalf("provider queries=%#v", provider.queries)
	}
	if resolver.deadline.IsZero() || resolver.deadline.After(deadline) {
		t.Fatalf("resolver deadline=%v request deadline=%v", resolver.deadline, deadline)
	}
	if prepared.Mode() != RetrievalExact || prepared.Stability().method != StabilityMatchedDoubleRead || prepared.Stability().readCount != 2 || prepared.PreparationRevision() != PreparationRevision {
		t.Fatalf("prepared=%#v", prepared)
	}
}

func TestPreparerEmbedsOneSanitizedVectorWithBoundedDeadline(t *testing.T) {
	rules, err := redaction.CompileRules([]redaction.Rule{{ID: "private-note", Pattern: "private-note", Replacement: "[SCRUBBED]"}})
	if err != nil {
		t.Fatal(err)
	}
	hybrid := testSnapshot(t, RetrievalHybrid,
		testCandidate(t, 1, CandidateFTS),
		testCandidate(t, 2, CandidateVector),
	)
	resolver := &recordingAuthorityResolver{authority: testAuthority(t, "alice", "agent")}
	provider := &scriptedCandidateProvider{snapshots: []CandidateSnapshot{hybrid, hybrid}}
	embedder := &recordingQueryEmbedder{vectors: [][]float32{testEmbeddingVector()}}
	prepared, err := testPreparerWithEmbedder(t, resolver, provider, embedder, rules).Prepare(context.Background(), testPrepareRequest(t, " private-note semantic task "))
	if err != nil {
		t.Fatal(err)
	}
	if embedder.calls != 1 || !reflect.DeepEqual(embedder.texts, [][]string{{"[SCRUBBED] semantic task"}}) {
		t.Fatalf("embed calls=%d texts=%#v", embedder.calls, embedder.texts)
	}
	if embedder.deadline.IsZero() || !embedder.deadline.After(embedder.calledAt) || embedder.deadline.After(embedder.calledAt.Add(maxQueryEmbeddingDuration)) {
		t.Fatalf("embedding deadline=%v calledAt=%v", embedder.deadline, embedder.calledAt)
	}
	if provider.calls != 2 || len(provider.queries) != 2 || !provider.queries[0].VectorEnabled() || !provider.queries[1].VectorEnabled() {
		t.Fatalf("provider calls=%d queries=%#v", provider.calls, provider.queries)
	}
	if prepared.Mode() != RetrievalHybrid || prepared.Stability().method != StabilityMatchedDoubleRead || prepared.Stability().readCount != 2 {
		t.Fatalf("prepared=%#v", prepared)
	}
}

func TestPreparerUsesShorterCallerDeadlineForEmbedding(t *testing.T) {
	snapshot := testSnapshot(t, RetrievalHybrid, testCandidate(t, 1, CandidateVector))
	resolver := &recordingAuthorityResolver{authority: testAuthority(t, "alice", "agent")}
	provider := &scriptedCandidateProvider{snapshots: []CandidateSnapshot{snapshot, snapshot}}
	embedder := &recordingQueryEmbedder{vectors: [][]float32{testEmbeddingVector()}}
	deadline := time.Now().Add(500 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if _, err := testPreparerWithEmbedder(t, resolver, provider, embedder, nil).Prepare(ctx, testPrepareRequest(t, "short deadline")); err != nil {
		t.Fatal(err)
	}
	if !embedder.deadline.Equal(deadline) {
		t.Fatalf("embedding deadline=%v want caller deadline=%v", embedder.deadline, deadline)
	}
}

func TestPreparerAcceptsMatchingEmptySnapshots(t *testing.T) {
	empty := testSnapshot(t, RetrievalEmpty)
	resolver := &recordingAuthorityResolver{authority: testAuthority(t, "", "")}
	provider := &scriptedCandidateProvider{snapshots: []CandidateSnapshot{empty, empty}}
	embedder := &recordingQueryEmbedder{vectors: [][]float32{testEmbeddingVector()}}
	prepared, err := testPreparerWithEmbedder(t, resolver, provider, embedder, nil).Prepare(context.Background(), testPrepareRequest(t, "empty task"))
	if err != nil {
		t.Fatal(err)
	}
	if embedder.calls != 1 || provider.calls != 2 || !provider.queries[0].VectorEnabled() || prepared.Mode() != RetrievalEmpty || len(prepared.Candidates()) != 0 || prepared.Stability().method != StabilityMatchedDoubleRead {
		t.Fatalf("prepared=%#v embedding calls=%d provider calls=%d", prepared, embedder.calls, provider.calls)
	}
}

func TestPreparerAcceptsForwardConfirmedThirdRead(t *testing.T) {
	first := testSnapshot(t, RetrievalExact, testCandidate(t, 1, CandidateExact))
	second := testSnapshot(t, RetrievalLexicalDegraded, testCandidate(t, 2, CandidateFTS))
	resolver := &recordingAuthorityResolver{authority: testAuthority(t, "alice", "agent")}
	provider := &scriptedCandidateProvider{snapshots: []CandidateSnapshot{first, second, second}}
	prepared, err := testPreparer(t, resolver, provider, nil).Prepare(context.Background(), testPrepareRequest(t, "third read"))
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 3 || prepared.Stability().method != StabilityForwardConfirmedThirdRead || prepared.Stability().readCount != 3 {
		t.Fatalf("prepared=%#v calls=%d", prepared, provider.calls)
	}
	if candidates := prepared.Candidates(); len(candidates) != 1 || candidates[0].ID() != 2 || candidates[0].SourceTier() != CandidateFTS {
		t.Fatalf("candidates=%#v", candidates)
	}
}

func TestPreparerRefusesUnstableCandidates(t *testing.T) {
	first := testSnapshot(t, RetrievalExact, testCandidate(t, 1, CandidateExact))
	second := testSnapshot(t, RetrievalExact, testCandidate(t, 2, CandidateExact))
	third := testSnapshot(t, RetrievalExact, testCandidate(t, 3, CandidateExact))
	resolver := &recordingAuthorityResolver{authority: testAuthority(t, "alice", "agent")}
	provider := &scriptedCandidateProvider{snapshots: []CandidateSnapshot{first, second, third}}
	_, err := testPreparer(t, resolver, provider, nil).Prepare(context.Background(), testPrepareRequest(t, "unstable task"))
	if !errors.Is(err, ErrUnstable) {
		t.Fatalf("error=%v", err)
	}
	if provider.calls != 3 {
		t.Fatalf("provider calls=%d", provider.calls)
	}
}

func TestPreparerDegradesUnavailableAndMalformedEmbeddingsToLexical(t *testing.T) {
	nonFinite := testEmbeddingVector()
	nonFinite[1] = float32(math.Inf(1))
	for _, testCase := range []struct {
		name    string
		vectors [][]float32
		err     error
	}{
		{name: "error", err: errors.New("embedding unavailable")},
		{name: "empty cardinality"},
		{name: "multiple vectors", vectors: [][]float32{testEmbeddingVector(), testEmbeddingVector()}},
		{name: "empty vector", vectors: [][]float32{{}}},
		{name: "wrong dimension", vectors: [][]float32{make([]float32, vectordim.Dimension-1)}},
		{name: "zero vector", vectors: [][]float32{make([]float32, vectordim.Dimension)}},
		{name: "non-finite vector", vectors: [][]float32{nonFinite}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			lexical := testSnapshot(t, RetrievalLexicalDegraded, testCandidate(t, 5, CandidateFTS))
			resolver := &recordingAuthorityResolver{authority: testAuthority(t, "alice", "agent")}
			provider := &scriptedCandidateProvider{snapshots: []CandidateSnapshot{lexical, lexical}}
			embedder := &recordingQueryEmbedder{vectors: testCase.vectors, err: testCase.err}
			prepared, err := testPreparerWithEmbedder(t, resolver, provider, embedder, nil).Prepare(context.Background(), testPrepareRequest(t, "lexical fallback"))
			if err != nil {
				t.Fatal(err)
			}
			if embedder.calls != 1 || provider.calls != 2 || prepared.Mode() != RetrievalLexicalDegraded || provider.queries[0].VectorEnabled() || provider.queries[0].Vector() != nil {
				t.Fatalf("prepared=%#v embedding calls=%d provider calls=%d query=%#v", prepared, embedder.calls, provider.calls, provider.queries[0])
			}
		})
	}
}

func TestPreparerUsesLexicalSnapshotsWhenEmbedderIsAbsent(t *testing.T) {
	lexical := testSnapshot(t, RetrievalLexicalDegraded, testCandidate(t, 5, CandidateFTS))
	resolver := &recordingAuthorityResolver{authority: testAuthority(t, "alice", "agent")}
	provider := &scriptedCandidateProvider{snapshots: []CandidateSnapshot{lexical, lexical}}
	prepared, err := testPreparer(t, resolver, provider, nil).Prepare(context.Background(), testPrepareRequest(t, "disabled embedder"))
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 || len(provider.queries) != 2 || provider.queries[0].VectorEnabled() || provider.queries[0].Vector() != nil || prepared.Mode() != RetrievalLexicalDegraded {
		t.Fatalf("prepared=%#v provider calls=%d queries=%#v", prepared, provider.calls, provider.queries)
	}
}

func TestPreparerReportsCallerCancellationAsUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resolver := &recordingAuthorityResolver{authority: testAuthority(t, "alice", "agent")}
	provider := &scriptedCandidateProvider{}
	embedder := &recordingQueryEmbedder{
		vectors:      [][]float32{testEmbeddingVector()},
		beforeReturn: cancel,
	}
	_, err := testPreparerWithEmbedder(t, resolver, provider, embedder, nil).Prepare(ctx, testPrepareRequest(t, "cancelled caller"))
	if !errors.Is(err, ErrUnavailable) || embedder.calls != 1 || provider.calls != 0 {
		t.Fatalf("error=%v embedding calls=%d provider calls=%d", err, embedder.calls, provider.calls)
	}
}

func TestPreparerRejectsMalformedProviderSnapshots(t *testing.T) {
	overLimit := make([]AuthorizedCandidateRef, MaxPreparedCandidates+1)
	for index := range overLimit {
		overLimit[index] = AuthorizedCandidateRef{id: int64(index + 1), version: 1, tier: CandidateExact}
	}
	for _, snapshot := range []CandidateSnapshot{
		{mode: RetrievalExact, candidates: overLimit},
		{mode: RetrievalExact, candidates: []AuthorizedCandidateRef{{id: 1, version: 1, tier: CandidateExact}, {id: 1, version: 2, tier: CandidateExact}}},
		{mode: RetrievalExact, candidates: []AuthorizedCandidateRef{{id: 1, version: 1, tier: CandidateSourceTier(99)}}},
		{mode: RetrievalHybrid, candidates: []AuthorizedCandidateRef{{id: 1, version: 1, tier: CandidateExact}}},
	} {
		resolver := &recordingAuthorityResolver{authority: testAuthority(t, "alice", "agent")}
		provider := &scriptedCandidateProvider{snapshots: []CandidateSnapshot{snapshot}}
		_, err := testPreparer(t, resolver, provider, nil).Prepare(context.Background(), testPrepareRequest(t, "malformed provider"))
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("snapshot=%#v error=%v", snapshot, err)
		}
		if provider.calls != 1 {
			t.Fatalf("snapshot=%#v calls=%d", snapshot, provider.calls)
		}
	}
}

func TestPreparerRedactsTransientQueryWithoutMetadata(t *testing.T) {
	rules, err := redaction.CompileRules([]redaction.Rule{{ID: "private-note", Pattern: "private-note", Replacement: "[SCRUBBED]"}})
	if err != nil {
		t.Fatal(err)
	}
	resolver := &recordingAuthorityResolver{authority: testAuthority(t, "alice", "agent")}
	snapshot := testSnapshot(t, RetrievalExact, testCandidate(t, 7, CandidateExact))
	provider := &scriptedCandidateProvider{snapshots: []CandidateSnapshot{snapshot, snapshot}}
	secret := "sk-abcdefghijklmnopqrstuvwx"
	embedder := &recordingQueryEmbedder{vectors: [][]float32{testEmbeddingVector()}}
	prepared, err := testPreparerWithEmbedder(t, resolver, provider, embedder, rules).Prepare(context.Background(), testPrepareRequest(t, "private-note "+secret))
	if err != nil {
		t.Fatal(err)
	}
	if embedder.calls != 0 || len(provider.queries) != 2 || provider.queries[0].VectorEnabled() {
		t.Fatalf("embedding calls=%d queries=%d", embedder.calls, len(provider.queries))
	}
	query := provider.queries[0].Query()
	if strings.Contains(query, secret) || strings.Contains(query, "private-note") || !strings.Contains(query, "[SCRUBBED]") || !strings.Contains(query, "[REDACTED:") {
		t.Fatalf("sanitized query=%q", query)
	}
	for _, field := range []string{"Query", "Redaction", "MatchedRules", "Secret"} {
		if _, found := reflect.TypeOf(prepared).FieldByName(field); found {
			t.Fatalf("prepared task memory exposes %s", field)
		}
	}

	fullRules, err := redaction.CompileRules([]redaction.Rule{{ID: "full", Pattern: "(?s)^.*$", Replacement: ""}})
	if err != nil {
		t.Fatal(err)
	}
	blockedResolver := &recordingAuthorityResolver{authority: testAuthority(t, "alice", "agent")}
	blockedProvider := &scriptedCandidateProvider{snapshots: []CandidateSnapshot{snapshot, snapshot}}
	blockedEmbedder := &recordingQueryEmbedder{vectors: [][]float32{testEmbeddingVector()}}
	_, err = testPreparerWithEmbedder(t, blockedResolver, blockedProvider, blockedEmbedder, fullRules).Prepare(context.Background(), testPrepareRequest(t, "fully redacted"))
	if !errors.Is(err, ErrInvalidRequest) || blockedResolver.calls != 0 || blockedProvider.calls != 0 || blockedEmbedder.calls != 0 {
		t.Fatalf("error=%v resolver calls=%d provider calls=%d embedding calls=%d", err, blockedResolver.calls, blockedProvider.calls, blockedEmbedder.calls)
	}
}

func TestTaskMemoryClosedTypesDoNotExposeProjectOrCallerFields(t *testing.T) {
	for _, value := range []any{
		TaskFacts{},
		AuthenticatedCaller{},
		AuthorizedTaskContext{},
		AccessPolicy{},
		AuthorizedCandidateQuery{},
		CandidateLookup{},
		CandidateSnapshot{},
		PreparedTaskMemory{},
		PreparerConfig{},
	} {
		typeOfValue := reflect.TypeOf(value)
		for _, field := range []string{"Project", "Caller", "Identity", "Principal", "Workstation", "Keycard"} {
			if found, ok := typeOfValue.FieldByName(field); ok && found.PkgPath == "" {
				t.Fatalf("%s exposes authority field %s", typeOfValue, field)
			}
		}
	}
}

func testAuthority(t *testing.T, principal, principalKind string) AuthorizedTaskContext {
	t.Helper()
	caller, err := NewAuthenticatedCaller("client", "read-write", "ws-test", principal, principalKind, nil)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewAuthorizedTaskContext(caller, testResolution(t, projectidentity.ReadFilterIntentV3, projectidentity.ProjectResolvedOutcomeV3), "source-session")
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func testResolution(t *testing.T, intent projectidentity.ResolutionIntentV3, outcome projectidentity.ResolutionOutcomeV3) projectidentity.ResolutionResultV3 {
	t.Helper()
	correlation, err := projectidentity.NewCorrelationV3("task-memory-correlation")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.IsSuccess() {
		key, err := projectidentity.NewProjectKeyV3("11111111-1111-4111-8111-111111111111")
		if err != nil {
			t.Fatal(err)
		}
		result, err := projectidentity.NewSuccessResultV3(intent, outcome, key, projectidentity.RepositoryResolvedScopeV3, "", correlation)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result, err := projectidentity.NewRefusalResultV3(intent, outcome, correlation)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func testCandidate(t *testing.T, id int64, tier CandidateSourceTier) AuthorizedCandidateRef {
	t.Helper()
	candidate, err := NewAuthorizedCandidateRef(id, 1, tier)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func testSnapshot(t *testing.T, mode RetrievalMode, candidates ...AuthorizedCandidateRef) CandidateSnapshot {
	t.Helper()
	snapshot, err := NewCandidateSnapshot(mode, candidates)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func testPreparer(t *testing.T, resolver AuthorityResolver, provider AuthorizedCandidateProvider, rules []redaction.CompiledRule) Preparer {
	t.Helper()
	return testPreparerWithEmbedder(t, resolver, provider, nil, rules)
}

func testPreparerWithEmbedder(t *testing.T, resolver AuthorityResolver, provider AuthorizedCandidateProvider, embedder QueryEmbedder, rules []redaction.CompiledRule) Preparer {
	t.Helper()
	preparer, err := NewPreparer(PreparerConfig{Authority: resolver, Candidates: provider, Embedder: embedder, RedactionRules: rules})
	if err != nil {
		t.Fatal(err)
	}
	return preparer
}

func testPrepareRequest(t *testing.T, query string) PrepareRequest {
	t.Helper()
	facts, err := NewTaskFacts(query)
	if err != nil {
		t.Fatal(err)
	}
	return PrepareRequest{
		Project: ProjectEvidenceV3{
			Anchor: projectidentity.AnchorV3{
				Version:   3,
				ProjectID: "22222222-2222-4222-8222-222222222222",
				Name:      "task-memory",
				Scope:     "repository",
			},
			Descriptor: projectidentity.DescriptorV3{
				Version:         3,
				AnchorProjectID: "22222222-2222-4222-8222-222222222222",
				Name:            "task-memory",
				Scope:           "repository",
			},
		},
		Task: facts,
	}
}

func testEmbeddingVector() []float32 {
	vector := make([]float32, vectordim.Dimension)
	vector[0] = 0.5
	return vector
}
