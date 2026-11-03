package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/reranking"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

const feature009ProducerQuery = "cobalt lantern"

type feature009ProviderOptions struct {
	stallPath     string
	stallFor      time.Duration
	unavailable   bool
	healthyDelay  time.Duration
	gateReranking bool
}

type feature009ProviderCall struct {
	path        string
	input       []string
	query       string
	documents   []string
	cancelledAt time.Time
	done        chan struct{}
}

type feature009Provider struct {
	mu      sync.Mutex
	calls   []*feature009ProviderCall
	options feature009ProviderOptions
	prefer  string
	stop    chan struct{}
	entered chan struct{}
	release chan struct{}
}

func (p *feature009Provider) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || (r.URL.Path != "/v1/embeddings" && r.URL.Path != "/v1/rerank") {
		http.Error(w, "unexpected synthetic provider request", http.StatusBadRequest)
		return
	}
	var payload struct {
		Input     []string `json:"input"`
		Query     string   `json:"query"`
		Documents []string `json:"documents"`
	}
	if json.NewDecoder(r.Body).Decode(&payload) != nil {
		http.Error(w, "invalid synthetic provider body", http.StatusBadRequest)
		return
	}
	call := &feature009ProviderCall{
		path: r.URL.Path, input: payload.Input, query: payload.Query,
		documents: payload.Documents, done: make(chan struct{}),
	}
	p.mu.Lock()
	p.calls = append(p.calls, call)
	p.mu.Unlock()
	defer close(call.done)
	cancelled := func() {
		p.mu.Lock()
		call.cancelledAt = time.Now()
		p.mu.Unlock()
	}
	if r.URL.Path == "/v1/rerank" && p.options.gateReranking {
		select {
		case p.entered <- struct{}{}:
		default:
		}
		select {
		case <-p.release:
		case <-r.Context().Done():
			cancelled()
			return
		case <-p.stop:
			return
		}
	}
	delay := time.Duration(0)
	if r.URL.Path == p.options.stallPath {
		delay = p.options.stallFor
	} else if r.URL.Path == "/v1/rerank" {
		delay = p.options.healthyDelay
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			cancelled()
			return
		case <-p.stop:
			return
		case <-timer.C:
		}
	}
	if r.URL.Path == "/v1/embeddings" && p.options.unavailable {
		http.Error(w, "synthetic embedding unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/v1/embeddings" {
		vector := make([]float32, embedding.EmbeddingDim)
		vector[0] = 1
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": vector}}})
		return
	}
	// Put the selected secondary row first, regardless of the fusion input order.
	// The HTTP assertion therefore detects disabling reranking as a timeout fix.
	scores := make([]reranking.PassageScore, 0, len(payload.Documents))
	for i, document := range payload.Documents {
		if document == p.prefer {
			scores = append(scores, reranking.PassageScore{Index: i, RelevanceScore: 0.99})
		}
	}
	for i, document := range payload.Documents {
		if document != p.prefer {
			scores = append(scores, reranking.PassageScore{Index: i, RelevanceScore: 0.1})
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"results": scores})
}

func (p *feature009Provider) since(offset int) []*feature009ProviderCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*feature009ProviderCall(nil), p.calls[offset:]...)
}

func (p *feature009Provider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

type feature009ProducerFixture struct {
	store     *gormdb.Store
	memories  *gormdb.MemoryStore
	rules     *gormdb.BehavioralRulesStore
	service   *Service
	server    *httptest.Server
	provider  *feature009Provider
	project   string
	token     string
	identity  auth.Identity
	primary   *models.Memory
	secondary *models.Memory
	hidden    *models.Memory
}

func newFeature009ProducerFixture(t *testing.T, options feature009ProviderOptions) *feature009ProducerFixture {
	t.Helper()
	// Reuse the exact opt-in, IP-loopback-only, empty-public-schema fixture.
	// It preflights existing public.pgvector, creates a UUID schema, migrates
	// only there, closes the schema's pool and drops only that schema at cleanup.
	store := openContextSearchPrivacyStore(t)
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	t.Setenv("ENGRAM_HAP_01B_LEGACY_DIRECT_ENFORCEMENT", "false")
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "false")
	f := &feature009ProducerFixture{
		store: store, memories: gormdb.NewMemoryStore(store), rules: gormdb.NewBehavioralRulesStore(store),
		project: uuid.NewString(),
	}
	require.NoError(t, store.DB.Create(&gormdb.Project{ID: f.project}).Error)
	tokenStore := gormdb.NewTokenStore(store)
	master := uuid.NewString()
	validator := auth.NewValidator(master, tokenStore)
	issueKeycard := func(principal string) (string, auth.Identity) {
		t.Helper()
		raw := auth.TokenRawPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
		hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.MinCost)
		require.NoError(t, err)
		_, err = tokenStore.CreateWithPrincipal(t.Context(), gormdb.TokenCreatePrincipalInput{
			Name: principal, TokenHash: string(hash),
			TokenPrefix: raw[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen],
			Scope:       "read-write", Principal: principal, PrincipalKind: "agent",
		})
		require.NoError(t, err)
		identity, err := validator.Validate(t.Context(), raw)
		require.NoError(t, err)
		require.Equal(t, auth.SourceClient, identity.Source)
		require.Equal(t, principal, identity.Principal)
		require.Equal(t, auth.PrincipalKindAgent, identity.PrincipalKind)
		require.NotEmpty(t, identity.WorkstationID())
		return raw, identity
	}
	f.token, f.identity = issueKeycard("agent/feature009-producer-reader")
	_, other := issueKeycard("agent/feature009-producer-other")
	f.primary = f.createMemory(t, "cobalt authorized northstar lantern — заметка 🧭\ne\u0301 remains decomposed.", f.identity.Principal, "private", 3)
	f.secondary = f.createMemory(t, "cobalt shared submarine lantern — 知識\nBasalt orbit protocol.", other.Principal, "shared", 5)
	f.hidden = f.createMemory(t, strings.Repeat(feature009ProducerQuery+" ", 12)+"hiddenproducerpassage", other.Principal, "private", 1)
	caller := scope.KeycardContext{Principal: f.identity.Principal, PrincipalKind: string(f.identity.PrincipalKind), WorkstationID: f.identity.WorkstationID()}
	for _, memory := range []*models.Memory{f.primary, f.secondary} {
		require.True(t, scope.ResolveMemory(caller, memory, scope.MemoryVisibilityOptions{ApplyPrivacyScope: true}))
	}
	require.False(t, scope.ResolveMemory(caller, f.hidden, scope.MemoryVisibilityOptions{ApplyPrivacyScope: true}), "negative fixture must genuinely be inaccessible to the validated HTTP caller")
	var stronger bool
	require.NoError(t, store.DB.Raw(`
		WITH q AS (SELECT websearch_to_tsquery('english', ?) AS query)
		SELECT ts_rank_cd(h.search_vector, q.query) > (
			SELECT max(ts_rank_cd(v.search_vector, q.query)) FROM memories v WHERE v.id IN (?, ?)
		) FROM memories h, q WHERE h.id = ?`, feature009ProducerQuery, f.primary.ID, f.secondary.ID, f.hidden.ID).Scan(&stronger).Error)
	require.True(t, stronger, "the inaccessible row must outrank both visible lexical candidates before authorization")
	lexical, err := f.memories.SearchFTS(t.Context(), f.project, feature009ProducerQuery, 10)
	require.NoError(t, err)
	var lexicalIDs []int64
	for _, memory := range lexical {
		lexicalIDs = append(lexicalIDs, memory.ID)
	}
	require.ElementsMatch(t, []int64{f.primary.ID, f.secondary.ID, f.hidden.ID}, lexicalIDs, "both useful rows and the stronger inaccessible row must be real indexed lexical matches")
	p := &feature009Provider{
		options: options, prefer: f.secondary.Content, stop: make(chan struct{}),
		entered: make(chan struct{}, 8), release: make(chan struct{}),
	}
	f.provider = p
	providerServer := httptest.NewServer(http.HandlerFunc(p.serveHTTP))
	t.Cleanup(func() {
		close(p.stop)
		providerServer.Close()
	})
	t.Setenv("ENGRAM_EMBEDDING_URL", providerServer.URL)
	t.Setenv("ENGRAM_EMBEDDING_API_KEY", "")
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "feature009-producer-synthetic")
	t.Setenv("ENGRAM_EMBEDDING_DIMENSIONS", "")
	embedder, err := embedding.NewClient()
	require.NoError(t, err)
	embeddingStore := embedding.NewStore(store.DB)
	for i, memory := range []*models.Memory{f.primary, f.secondary, f.hidden} {
		vector := make([]float32, embedding.EmbeddingDim)
		switch i {
		case 0:
			vector[0], vector[1] = 0.8, 0.6
		case 1:
			vector[0], vector[1] = 0.6, 0.8
		default:
			vector[0] = 1
		}
		require.NoError(t, embeddingStore.StoreChunks(t.Context(), []embedding.Chunk{{
			MemoryID: memory.ID, Text: memory.Content, Model: embedder.Model(), Embedding: pgvector.NewVector(vector),
		}}))
	}
	t.Setenv("ENGRAM_RERANK_URL", providerServer.URL)
	t.Setenv("ENGRAM_RERANK_API_KEY", "")
	t.Setenv("ENGRAM_RERANK_MODEL", "feature009-producer-synthetic")
	reranker, err := reranking.NewClient()
	require.NoError(t, err)
	ta, err := NewTokenAuth(master)
	require.NoError(t, err)
	ta.SetTokenStore(tokenStore)
	ta.SetValidator(validator)
	cfg := config.Default()
	cfg.ContextMaxPromptResults, cfg.ContextMaxTokens, cfg.AlwaysInjectLimit = 10, 0, 10
	f.service = &Service{
		router: chi.NewRouter(), tokenAuth: ta, config: cfg, store: store,
		memoryStore: f.memories, behavioralRulesStore: f.rules,
		embeddingClient: embedder, embeddingStore: embeddingStore, rerankClient: reranker,
		retrievalStats: make(map[string]*RetrievalStats), mcpHealth: mcp.NewMCPHealth(),
	}
	f.service.ready.Store(true)
	f.service.setupMiddleware()
	f.service.setupRoutes()
	f.server = httptest.NewServer(f.service.router)
	t.Cleanup(f.server.Close)
	return f
}

func (f *feature009ProducerFixture) createMemory(t *testing.T, body, owner, visibility string, version int) *models.Memory {
	t.Helper()
	memory, err := f.memories.Create(t.Context(), &models.Memory{
		Project: f.project, Content: body, Tags: []string{"type:fact", "feature009-producer-synthetic"},
		PrivacyScope: "project", OwnerPrincipal: owner, OwnerPrincipalKind: "agent", AgentVisibility: visibility,
		Version: version,
	})
	require.NoError(t, err)
	return memory
}

func (f *feature009ProducerFixture) state(t *testing.T) string {
	t.Helper()
	var state string
	require.NoError(t, f.store.DB.Raw(`SELECT jsonb_build_object(
		'memories', (SELECT COALESCE(jsonb_agg(to_jsonb(m) ORDER BY m.id), '[]'::jsonb) FROM memories m),
		'rules', (SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id), '[]'::jsonb) FROM behavioral_rules r)
	)::text`).Scan(&state).Error)
	return state
}

type feature009ProducerExchange struct {
	started  time.Time
	finished time.Time
	status   int
	body     []byte
	err      error
}

func (f *feature009ProducerFixture) exchange(ctx context.Context, method, query string, budget *string) feature009ProducerExchange {
	request, err := feature009ProducerRequest(ctx, method, f.server.URL, f.project, query, budget)
	if err != nil {
		return feature009ProducerExchange{err: err}
	}
	request.Header.Set("Authorization", "Bearer "+f.token)
	x := feature009ProducerExchange{started: time.Now()}
	response, err := f.server.Client().Do(request)
	if err == nil {
		x.status = response.StatusCode
		x.body, err = io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if err == nil {
			err = closeErr
		}
	}
	x.finished, x.err = time.Now(), err
	return x
}

func (f *feature009ProducerFixture) decode(t *testing.T, x feature009ProducerExchange, query string) feature009ProducerResponse {
	t.Helper()
	require.NoError(t, x.err)
	require.Equal(t, http.StatusOK, x.status, "real authenticated context-search HTTP status")
	var response feature009ProducerResponse
	require.NoError(t, json.Unmarshal(x.body, &response))
	require.Equal(t, f.project, response.Project)
	require.Equal(t, query, response.Query)
	return response
}

func (f *feature009ProducerFixture) requireProviderPrivacy(t *testing.T, calls []*feature009ProviderCall, query string) {
	t.Helper()
	embeddingCalled := false
	for _, call := range calls {
		if call.path == "/v1/embeddings" {
			embeddingCalled = true
			require.Equal(t, []string{query}, call.input, "embedding receives only the caller query")
		} else {
			require.Equal(t, query, call.query)
			for _, document := range call.documents {
				require.Contains(t, []string{f.primary.Content, f.secondary.Content}, document, "every external passage must come from an authorized fixture row")
				require.NotContains(t, document, "hiddenproducerpassage")
			}
		}
	}
	require.True(t, embeddingCalled, "the real HTTP request must use the configured embedding provider before useful degradation")
}

func (f *feature009ProducerFixture) requireCancelled(t *testing.T, calls []*feature009ProviderCall, path string, deadline time.Time) {
	t.Helper()
	found := false
	for _, call := range calls {
		if call.path != path {
			continue
		}
		found = true
		// This is an event wait bounded by the same requested budget, never an
		// extra timeout allowance for the real HTTP response.
		if remaining := time.Until(deadline); remaining > 0 {
			timer := time.NewTimer(remaining)
			select {
			case <-call.done:
			case <-timer.C:
			}
			timer.Stop()
		}
		f.provider.mu.Lock()
		cancelledAt := call.cancelledAt
		f.provider.mu.Unlock()
		assert.False(t, cancelledAt.IsZero(), "the deliberately stalled provider call must be cancelled, not merely abandoned")
		if !cancelledAt.IsZero() {
			assert.False(t, cancelledAt.After(deadline), "provider cancellation must happen within the requested budget")
		}
	}
	require.True(t, found, "the real HTTP request must actually reach the stalled provider endpoint")
}

// ENGRAM_SEARCH_PRIVACY_TEST_DSN is the existing opt-in disposable fixture DSN.
// Each case owns a new schema; no inherited DATABASE_DSN or external provider is used.
func TestContextSearchProducerFallbackPostgres(t *testing.T) {
	const budget = 350 * time.Millisecond
	for _, test := range []struct {
		name    string
		options feature009ProviderOptions
		header  string
		present bool
		reranks bool
	}{
		{name: "slow_embedding", options: feature009ProviderOptions{stallPath: "/v1/embeddings", stallFor: 3 * budget}, header: "350", present: true},
		{name: "slow_reranking", options: feature009ProviderOptions{stallPath: "/v1/rerank", stallFor: 3 * budget}, header: "350", present: true},
		{name: "unavailable_embedding", options: feature009ProviderOptions{unavailable: true}, header: "350", present: true},
		{name: "healthy_reranking_remains_enabled", header: "900", present: true, reranks: true},
		{name: "absent_header_preserves_existing_default", options: feature009ProviderOptions{healthyDelay: 600 * time.Millisecond}, reranks: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFeature009ProducerFixture(t, test.options)
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				t.Run(method, func(t *testing.T) {
					before := f.state(t)
					callsBefore := f.provider.count()
					var header *string
					if test.present {
						header = &test.header
					}
					// The guard only bounds a broken pre-implementation request.
					// The actual response oracle below has no allowance past its header.
					ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
					defer cancel()
					x := f.exchange(ctx, method, feature009ProducerQuery, header)
					elapsed := x.finished.Sub(x.started)
					if test.present {
						ms, err := strconv.ParseInt(test.header, 10, 64)
						require.NoError(t, err)
						assert.LessOrEqual(t, elapsed, time.Duration(ms)*time.Millisecond, "the complete real HTTP response must fit the requested processing budget")
					}
					response := f.decode(t, x, feature009ProducerQuery)
					var ids []int64
					for _, observation := range response.Observations {
						ids = append(ids, feature009ProducerID(t, observation))
						require.Equal(t, f.project, observation.Project)
						require.Contains(t, []string{f.primary.Content, f.secondary.Content}, observation.Narrative)
					}
					require.ElementsMatch(t, []int64{f.primary.ID, f.secondary.ID}, ids, "fallback must contain both useful authorized lexical results, not an empty deadline abort")
					require.NotContains(t, ids, f.hidden.ID)
					calls := f.provider.since(callsBefore)
					f.requireProviderPrivacy(t, calls, feature009ProducerQuery)
					if test.options.stallPath != "" {
						f.requireCancelled(t, calls, test.options.stallPath, x.started.Add(budget))
					}
					if test.reranks {
						reranked := false
						for _, call := range calls {
							if call.path == "/v1/rerank" && len(call.documents) >= 2 {
								reranked = true
							}
						}
						require.True(t, reranked, "sufficient time must retain the configured cross-encoder")
						require.Equal(t, f.secondary.ID, ids[0], "the HTTP result must honor the controlled cross-encoder's order")
					}
					if !test.present {
						require.GreaterOrEqual(t, elapsed, 600*time.Millisecond, "absent budget must not introduce a new global 500ms cap")
					}
					require.Equal(t, before, f.state(t), "search must not mutate memory bodies, versions, lifecycle fields or behavioral rules")
				})
			}
		})
	}
}

func feature009RequireSourceRef(t *testing.T, observation feature009ProducerObservation, kind string, id int64, version int, body string) {
	t.Helper()
	require.Equal(t, id, feature009ProducerID(t, observation))
	require.Greater(t, id, int64(0))
	require.LessOrEqual(t, id, feature009ProducerMaxSafeID)
	require.NotEmpty(t, observation.SourceRef, "authoritative safe-ID rows must carry truthful source provenance")
	var source map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(observation.SourceRef, &source))
	require.NotNil(t, source)
	// This is the selected wire contract, not a reflection of Go internals.
	// In particular, a mutable row version must never be called revision.
	if version < 0 || int64(version) > feature009ProducerMaxSafeID {
		require.Len(t, source, 3)
		require.NotContains(t, source, "record_version", "unsafe versions omit only the optional version, not truthful source identity and digest")
	} else {
		require.Len(t, source, 4)
	}
	for key := range source {
		require.Contains(t, []string{"kind", "id", "record_version", "content_sha256"}, key, "do not fabricate revision, author, owner or source metadata")
	}
	var actualKind, digest string
	require.NoError(t, json.Unmarshal(source["kind"], &actualKind))
	require.Equal(t, kind, actualKind)
	require.Regexp(t, `^[1-9][0-9]*$`, string(source["id"]))
	actualID, err := strconv.ParseInt(string(source["id"]), 10, 64)
	require.NoError(t, err)
	require.Equal(t, id, actualID)
	require.LessOrEqual(t, actualID, feature009ProducerMaxSafeID)
	if version >= 0 && int64(version) <= feature009ProducerMaxSafeID {
		require.Regexp(t, `^(0|[1-9][0-9]*)$`, string(source["record_version"]))
		actualVersion, err := strconv.ParseInt(string(source["record_version"]), 10, 64)
		require.NoError(t, err)
		require.LessOrEqual(t, actualVersion, feature009ProducerMaxSafeID)
		require.Equal(t, int64(version), actualVersion, "record_version is current mutable row state")
	}
	require.NoError(t, json.Unmarshal(source["content_sha256"], &digest))
	require.Regexp(t, `^[0-9a-f]{64}$`, digest)
	sum := sha256.Sum256([]byte(body))
	require.Equal(t, hex.EncodeToString(sum[:]), digest, "digest must cover the exact stored UTF-8 body, not a title or normalized rendering")
	require.Equal(t, body, observation.Narrative)
}

func (f *feature009ProducerFixture) setNextID(t *testing.T, table string, id int64) {
	t.Helper()
	require.Contains(t, []string{"memories", "behavioral_rules"}, table)
	var schema string
	require.NoError(t, f.store.DB.Raw("SELECT current_schema()").Scan(&schema).Error)
	require.Regexp(t, `^worker_search_privacy_[0-9a-f]{32}$`, schema, "sequence writes must target only this fixture's private schema")
	qualified := fmt.Sprintf(`"%s"."%s"`, schema, table)
	require.NoError(t, f.store.DB.Exec("SELECT setval(pg_get_serial_sequence(?, 'id'), ?, false)", qualified, id).Error)
}

func TestContextSearchProducerProvenancePostgres(t *testing.T) {
	f := newFeature009ProducerFixture(t, feature009ProviderOptions{})
	// The source-body oracle is lexical-only and still uses real HTTP/SQL/auth.
	f.service.embeddingClient = nil
	const query = "provenance009"
	var collisionID int64
	require.NoError(t, f.store.DB.Raw(`SELECT GREATEST(
		COALESCE((SELECT max(id) FROM memories), 0),
		COALESCE((SELECT max(id) FROM behavioral_rules), 0),
		COALESCE(pg_sequence_last_value(pg_get_serial_sequence('memories', 'id')::regclass), 0),
		COALESCE(pg_sequence_last_value(pg_get_serial_sequence('behavioral_rules', 'id')::regclass), 0)
	) + 1`).Scan(&collisionID).Error)
	f.setNextID(t, "memories", collisionID)
	f.setNextID(t, "behavioral_rules", collisionID)
	memory := f.createMemory(t, query+" amber ledger — память 🧭\ne\u0301 exact body", f.identity.Principal, "private", 7)
	rule, err := f.rules.Create(t.Context(), &models.BehavioralRule{
		Project: &f.project, Content: query + " lunar guidance — правило 🌙\nNever infer authorship.", Priority: 10, Version: 11,
	})
	require.NoError(t, err)
	require.Equal(t, memory.ID, rule.ID, "independent stores must genuinely share a numeric ID")
	require.NotEqual(t, memory.Version, rule.Version)
	check := func(t *testing.T) {
		t.Helper()
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(method, func(t *testing.T) {
				storedMemory, err := f.memories.Get(t.Context(), memory.ID)
				require.NoError(t, err)
				storedRule, err := f.rules.Get(t.Context(), rule.ID)
				require.NoError(t, err)
				before := f.state(t)
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				response := f.decode(t, f.exchange(ctx, method, query, nil), query)
				require.Len(t, response.Observations, 2)
				seen := make(map[string]bool)
				for _, observation := range response.Observations {
					require.Equal(t, f.project, observation.Project)
					seen[observation.Type] = true
					switch observation.Type {
					case string(models.ObsTypeDiscovery):
						feature009RequireSourceRef(t, observation, "memory", storedMemory.ID, storedMemory.Version, storedMemory.Content)
					case string(models.ObsTypeGuidance):
						feature009RequireSourceRef(t, observation, "behavioral_rule", storedRule.ID, storedRule.Version, storedRule.Content)
					default:
						t.Fatalf("unexpected selected observation type %q", observation.Type)
					}
				}
				require.True(t, seen[string(models.ObsTypeDiscovery)])
				require.True(t, seen[string(models.ObsTypeGuidance)])
				foundAlways := false
				for _, observation := range response.AlwaysInject {
					if feature009ProducerID(t, observation) == storedRule.ID {
						foundAlways = true
						feature009RequireSourceRef(t, observation, "behavioral_rule", storedRule.ID, storedRule.Version, storedRule.Content)
					}
				}
				require.True(t, foundAlways, "the existing always_inject rule projection must retain correct provenance too")
				require.Equal(t, before, f.state(t), "provenance delivery is read-only")
			})
		}
	}
	t.Run("initial_authoritative_rows", check)
	oldMemoryVersion, oldRuleVersion := memory.Version, rule.Version
	oldMemoryBody, oldRuleBody := memory.Content, rule.Content
	memory.Tags = append(append([]string(nil), memory.Tags...), "metadata-only-change")
	memory, err = f.memories.Update(t.Context(), memory)
	require.NoError(t, err)
	rule.Priority++
	rule, err = f.rules.Update(t.Context(), rule)
	require.NoError(t, err)
	require.Greater(t, memory.Version, oldMemoryVersion)
	require.Greater(t, rule.Version, oldRuleVersion)
	require.Equal(t, oldMemoryBody, memory.Content)
	require.Equal(t, oldRuleBody, rule.Content)
	t.Run("metadata_changes_version_not_body_digest", check)
	memory.Content += "\nA changed source body: Ж."
	memory, err = f.memories.Update(t.Context(), memory)
	require.NoError(t, err)
	rule.Content += "\nA different rule body: 語."
	rule, err = f.rules.Update(t.Context(), rule)
	require.NoError(t, err)
	require.NotEqual(t, sha256.Sum256([]byte(oldMemoryBody)), sha256.Sum256([]byte(memory.Content)))
	require.NotEqual(t, sha256.Sum256([]byte(oldRuleBody)), sha256.Sum256([]byte(rule.Content)))
	t.Run("body_changes_digest_and_current_version", check)
	// Rule rows have no positive-version materialization precondition. Exercise
	// the wire's nonnegative boundary without invalidating a memory reference.
	require.NoError(t, f.store.DB.Exec("UPDATE behavioral_rules SET version = 0 WHERE id = ?", rule.ID).Error)
	t.Run("zero_rule_version_remains_zero", check)
	require.NoError(t, f.store.DB.Exec("UPDATE behavioral_rules SET version = -1 WHERE id = ?", rule.ID).Error)
	t.Run("negative_rule_version_omits_only_optional_version", check)
}

func TestContextSearchProducerSafeIDBoundaryPostgres(t *testing.T) {
	f := newFeature009ProducerFixture(t, feature009ProviderOptions{})
	f.service.embeddingClient = nil
	const query = "safeboundary009"
	const unsafeID = feature009ProducerMaxSafeID + 2
	f.setNextID(t, "memories", feature009ProducerMaxSafeID)
	f.setNextID(t, "behavioral_rules", feature009ProducerMaxSafeID)
	safeMemory := f.createMemory(t, query+" heliotrope archive Δοκιμή 🧭", f.identity.Principal, "private", 2)
	safeRule, err := f.rules.Create(t.Context(), &models.BehavioralRule{Project: &f.project, Content: query + " polar maple guidance 雪", Version: 3})
	require.NoError(t, err)
	f.setNextID(t, "memories", unsafeID)
	f.setNextID(t, "behavioral_rules", unsafeID)
	unsafeMemory := f.createMemory(t, query+" submarine basalt protocol knowledge", f.identity.Principal, "private", 4)
	unsafeRule, err := f.rules.Create(t.Context(), &models.BehavioralRule{Project: &f.project, Content: query + " orchard lunar policy lantern", Version: 5})
	require.NoError(t, err)
	require.Equal(t, feature009ProducerMaxSafeID, safeMemory.ID)
	require.Equal(t, feature009ProducerMaxSafeID, safeRule.ID)
	require.Equal(t, unsafeID, unsafeMemory.ID)
	require.Equal(t, unsafeID, unsafeRule.ID)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			before := f.state(t)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			response := f.decode(t, f.exchange(ctx, method, query, nil), query)
			require.Len(t, response.Observations, 4)
			seen := make(map[string]bool)
			for _, observation := range response.Observations {
				id := feature009ProducerID(t, observation)
				key := observation.Type + "/" + strconv.FormatInt(id, 10)
				require.False(t, seen[key])
				seen[key] = true
				switch {
				case id == safeMemory.ID && observation.Type == string(models.ObsTypeDiscovery):
					stored, err := f.memories.Get(t.Context(), id)
					require.NoError(t, err)
					feature009RequireSourceRef(t, observation, "memory", stored.ID, stored.Version, stored.Content)
				case id == safeRule.ID && observation.Type == string(models.ObsTypeGuidance):
					stored, err := f.rules.Get(t.Context(), id)
					require.NoError(t, err)
					feature009RequireSourceRef(t, observation, "behavioral_rule", stored.ID, stored.Version, stored.Content)
				case id == unsafeMemory.ID && observation.Type == string(models.ObsTypeDiscovery):
					require.Equal(t, strconv.FormatInt(unsafeID, 10), string(observation.ID), "unsafe observation IDs must remain exact JSON integer numbers, never rounded through float64")
					require.Equal(t, unsafeMemory.Content, observation.Narrative)
					require.Empty(t, observation.SourceRef, "unsafe memory ID must omit the whole source_ref instead of rounding or string-coercing it")
				case id == unsafeRule.ID && observation.Type == string(models.ObsTypeGuidance):
					require.Equal(t, strconv.FormatInt(unsafeID, 10), string(observation.ID), "unsafe rule observation IDs must remain exact JSON integer numbers")
					require.Equal(t, unsafeRule.Content, observation.Narrative)
					require.Empty(t, observation.SourceRef, "unsafe rule ID must omit the whole source_ref")
				default:
					t.Fatalf("unexpected fixture observation %s", key)
				}
			}
			foundUnsafeAlways := false
			for _, observation := range response.AlwaysInject {
				if feature009ProducerID(t, observation) == unsafeRule.ID {
					foundUnsafeAlways = true
					require.Equal(t, strconv.FormatInt(unsafeID, 10), string(observation.ID), "always_inject must preserve the exact unsafe rule ID too")
					require.Empty(t, observation.SourceRef)
				}
			}
			require.True(t, foundUnsafeAlways)
			require.Equal(t, before, f.state(t))
		})
	}
}

func TestContextSearchProducerFinalRevalidationPostgres(t *testing.T) {
	for _, change := range []string{"acl", "body"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(change+"/"+method, func(t *testing.T) {
				f := newFeature009ProducerFixture(t, feature009ProviderOptions{gateReranking: true})
				header := "900"
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				result := make(chan feature009ProducerExchange, 1)
				go func() { result <- f.exchange(ctx, method, feature009ProducerQuery, &header) }()
				select {
				case <-f.provider.entered:
				case <-ctx.Done():
					t.Fatal("real authenticated HTTP search did not reach the controlled reranker")
				}
				current, err := f.memories.Get(t.Context(), f.primary.ID)
				require.NoError(t, err)
				if change == "acl" {
					require.NoError(t, f.store.DB.Exec(`UPDATE memories SET owner_principal = ?, version = version + 1 WHERE id = ?`, f.hidden.OwnerPrincipal, current.ID).Error)
					current, err = f.memories.Get(t.Context(), current.ID)
					require.NoError(t, err)
					caller := scope.KeycardContext{Principal: f.identity.Principal, PrincipalKind: string(f.identity.PrincipalKind), WorkstationID: f.identity.WorkstationID()}
					require.False(t, scope.ResolveMemory(caller, current, scope.MemoryVisibilityOptions{ApplyPrivacyScope: true}))
				} else {
					current.Content += "\nChanged while the provider held the selected body."
					current, err = f.memories.Update(t.Context(), current)
					require.NoError(t, err)
				}
				// Only the controlled concurrent writer may change this state.
				beforeReturn := f.state(t)
				close(f.provider.release)
				var x feature009ProducerExchange
				select {
				case x = <-result:
				case <-ctx.Done():
					t.Fatal("context search did not finish after the controlled provider was released")
				}
				assert.LessOrEqual(t, x.finished.Sub(x.started), 900*time.Millisecond)
				response := f.decode(t, x, feature009ProducerQuery)
				foundUseful := false
				for _, observation := range response.Observations {
					id := feature009ProducerID(t, observation)
					require.NotEqual(t, f.hidden.ID, id)
					if id == f.secondary.ID {
						foundUseful = true
						stored, err := f.memories.Get(t.Context(), id)
						require.NoError(t, err)
						feature009RequireSourceRef(t, observation, "memory", stored.ID, stored.Version, stored.Content)
					} else if id == f.primary.ID {
						require.Equal(t, "body", change, "a newly inaccessible row must not survive final ACL revalidation")
						feature009RequireSourceRef(t, observation, "memory", current.ID, current.Version, current.Content)
					} else {
						t.Fatalf("unexpected selected fixture memory %d", id)
					}
				}
				require.True(t, foundUseful, "revalidation must retain useful unchanged authorized work")
				f.requireProviderPrivacy(t, f.provider.since(0), feature009ProducerQuery)
				require.Equal(t, beforeReturn, f.state(t), "search itself must not mutate rows during final revalidation")
			})
		}
	}
}
