package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/reranking"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

// This is a real HTTP/auth/SQL consumer oracle, not a direct memorysearch probe.
// ENGRAM_SEARCH_PRIVACY_TEST_DSN must explicitly name a disposable loopback DB.
// DATABASE_DSN is deliberately neither read nor used as a fallback.
func TestContextSearchProjectPrivacyPostgres(t *testing.T) {
	store := openContextSearchPrivacyStore(t)
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	t.Setenv("ENGRAM_HAP_01B_LEGACY_DIRECT_ENFORCEMENT", "false")
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "false")
	memoryStore := gormdb.NewMemoryStore(store)
	embeddingStore := embedding.NewStore(store.DB)
	project, foreignProject := uuid.NewString(), uuid.NewString()
	require.NoError(t, store.DB.Create(&[]gormdb.Project{{ID: project}, {ID: foreignProject}}).Error)

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
	aliceToken, alice := issueKeycard("agent/search-privacy-alice")
	bobToken, bob := issueKeycard("agent/search-privacy-bob")

	const ftsQuery = "cobalt lantern"
	const vectorQuery = "luminous orbital navigation"
	vector := func(first, second float32) []float32 {
		v := make([]float32, embedding.EmbeddingDim)
		v[0], v[1] = first, second
		return v
	}
	type rerankCall struct {
		Query     string   `json:"query"`
		Documents []string `json:"documents"`
	}
	var callsMu sync.Mutex
	var calls []rerankCall
	var embeddingCalls int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/embeddings":
			var request struct {
				Input []string `json:"input"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Input) != 1 || request.Input[0] != vectorQuery {
				http.Error(w, "unexpected synthetic embedding input", http.StatusBadRequest)
				return
			}
			callsMu.Lock()
			embeddingCalls++
			callsMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": vector(1, 0)}}})
		case "/v1/rerank":
			var call rerankCall
			if json.NewDecoder(r.Body).Decode(&call) != nil || len(call.Documents) < 2 {
				http.Error(w, "at least two passages required", http.StatusBadRequest)
				return
			}
			callsMu.Lock()
			calls = append(calls, call)
			callsMu.Unlock()
			scores := make([]reranking.PassageScore, len(call.Documents))
			for i := range call.Documents {
				scores[i] = reranking.PassageScore{Index: i, RelevanceScore: 1 - float64(i)/float64(len(call.Documents)+1)}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": scores})
		default:
			http.Error(w, "unexpected provider route", http.StatusBadRequest)
		}
	}))
	t.Cleanup(provider.Close)
	t.Setenv("ENGRAM_EMBEDDING_URL", provider.URL)
	t.Setenv("ENGRAM_EMBEDDING_API_KEY", "")
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "project-privacy-synthetic")
	t.Setenv("ENGRAM_EMBEDDING_DIMENSIONS", "")
	embedder, err := embedding.NewClient()
	require.NoError(t, err)
	t.Setenv("ENGRAM_RERANK_URL", provider.URL)
	t.Setenv("ENGRAM_RERANK_API_KEY", "")
	t.Setenv("ENGRAM_RERANK_MODEL", "project-privacy-synthetic")
	reranker, err := reranking.NewClient()
	require.NoError(t, err)

	// Expected access is fixture data, not inferred from the search result.
	allowed := map[string]map[string]bool{alice.Principal: {}, bob.Principal: {}}
	byContent := make(map[string]*models.Memory)
	insert := func(mem *models.Memory, vec []float32, readers ...string) *models.Memory {
		t.Helper()
		mem.Tags = []string{"type:fact", "project-privacy-synthetic"}
		created, err := memoryStore.Create(t.Context(), mem)
		require.NoError(t, err)
		require.NoError(t, embeddingStore.StoreChunks(t.Context(), []embedding.Chunk{{
			MemoryID: created.ID, Text: created.Content, Model: embedder.Model(), Embedding: pgvector.NewVector(vec),
		}}))
		byContent[created.Content] = created
		for _, reader := range readers {
			allowed[reader][created.Content] = true
		}
		return created
	}
	own := insert(&models.Memory{
		Project: project, Content: "cobalt authorized northstar lantern aliceprivateprobe",
		PrivacyScope: "project", OwnerPrincipal: alice.Principal, OwnerPrincipalKind: "agent", AgentVisibility: "private",
	}, vector(0.8, 0.6), alice.Principal)
	shared := insert(&models.Memory{
		Project: project, Content: "cobalt shared southstar lantern teamvisibleprobe",
		PrivacyScope: "project", OwnerPrincipal: bob.Principal, OwnerPrincipalKind: "agent", AgentVisibility: "shared",
	}, vector(0.75, 0.66), alice.Principal, bob.Principal)
	// Both classes independently exceed the 50-row FTS/vector candidate limits.
	// Their repeated adjacent lexemes outrank both separated-lexeme visible rows.
	for _, class := range []string{"principal", "domain"} {
		for i := range 64 {
			mem := &models.Memory{
				Project: project, Content: strings.Repeat(ftsQuery+" ", 12) + fmt.Sprintf("hidden%s%03d", class, i),
				PrivacyScope: "project", OwnerPrincipal: bob.Principal, OwnerPrincipalKind: "agent", AgentVisibility: "private",
			}
			if class == "domain" {
				mem.AgentVisibility, mem.Domain = "shared", "search-privacy-domain"
			}
			insert(mem, vector(1, 0), bob.Principal)
		}
	}
	insert(&models.Memory{
		Project: project, Content: strings.Repeat(ftsQuery+" ", 12) + "hiddenworkstation",
		PrivacyScope: "private", SourceWorkstationID: "synthetic-unrelated-workstation",
		OwnerPrincipal: alice.Principal, OwnerPrincipalKind: "agent", AgentVisibility: "shared",
	}, vector(1, 0))
	insert(&models.Memory{
		Project: project, Content: strings.Repeat(ftsQuery+" ", 12) + "hiddenprincipalkind",
		PrivacyScope: "project", OwnerPrincipal: alice.Principal, OwnerPrincipalKind: "human", AgentVisibility: "private",
	}, vector(1, 0))
	insert(&models.Memory{
		Project: foreignProject, Content: strings.Repeat(ftsQuery+" ", 12) + "hiddenforeignproject",
		PrivacyScope: "global", OwnerPrincipal: alice.Principal, OwnerPrincipalKind: "agent", AgentVisibility: "shared",
	}, vector(1, 0))

	// Prove that the negative rows really are inaccessible to these validated
	// identities. A project boundary is separate from ResolveMemory's contract.
	for _, identity := range []auth.Identity{alice, bob} {
		caller := scope.KeycardContext{Principal: identity.Principal, PrincipalKind: string(identity.PrincipalKind), WorkstationID: identity.WorkstationID()}
		for content, memory := range byContent {
			if memory.Project == project {
				require.Equal(t, allowed[identity.Principal][content], scope.ResolveMemory(caller, memory, scope.MemoryVisibilityOptions{ApplyPrivacyScope: true}), "fixture access mismatch for memory ID %d", memory.ID)
			}
		}
	}
	var strongerHidden int64
	require.NoError(t, store.DB.Raw(`
		WITH q AS (SELECT websearch_to_tsquery('english', ?) AS query)
		SELECT count(*) FROM memories m, q
		WHERE m.project = ? AND m.id NOT IN (?, ?)
		AND ts_rank_cd(m.search_vector, q.query) > (
			SELECT max(ts_rank_cd(v.search_vector, q.query)) FROM memories v WHERE v.id IN (?, ?)
		)`, ftsQuery, project, own.ID, shared.ID, own.ID, shared.ID).Scan(&strongerHidden).Error)
	require.GreaterOrEqual(t, strongerHidden, int64(128), "fixture must place inaccessible matches ahead of both visible FTS rows")
	ftsOnly, err := memoryStore.SearchFTS(t.Context(), project, vectorQuery, 200)
	require.NoError(t, err)
	require.Empty(t, ftsOnly, "vector-only HTTP query must not be rescued by FTS")
	for _, memory := range []*models.Memory{own, shared} {
		require.False(t, strings.Contains(strings.ToLower(memory.Content), ftsQuery), "visible FTS rows must not be rescued by the literal-substring leg")
	}

	ta, err := NewTokenAuth(master)
	require.NoError(t, err)
	ta.SetTokenStore(tokenStore)
	ta.SetValidator(validator)
	cfg := config.Default()
	cfg.ContextMaxPromptResults, cfg.ContextMaxTokens = 1, 0
	service := &Service{
		router: chi.NewRouter(), tokenAuth: ta, config: cfg, store: store,
		memoryStore: memoryStore, embeddingStore: embeddingStore, rerankClient: reranker,
		retrievalStats: make(map[string]*RetrievalStats), mcpHealth: mcp.NewMCPHealth(),
	}
	service.ready.Store(true)
	service.setupMiddleware()
	service.setupRoutes()
	server := httptest.NewServer(service.router)
	t.Cleanup(server.Close)

	call := func(t *testing.T, method, token, query string, status int) []int64 {
		t.Helper()
		values := url.Values{"project": {project}, "query": {query}, "limit": {"1"}, "agent_id": {bob.Principal}}
		var body []byte
		if method == http.MethodPost {
			var err error
			body, err = json.Marshal(map[string]any{"project": project, "query": query, "agent_id": bob.Principal})
			require.NoError(t, err)
		}
		request, err := http.NewRequestWithContext(t.Context(), method, server.URL+"/api/context/search?"+values.Encode(), bytes.NewReader(body))
		require.NoError(t, err)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		if method == http.MethodPost {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Equal(t, status, response.StatusCode, "real context-search HTTP status; response payload intentionally omitted")
		if status != http.StatusOK {
			return nil
		}
		var result struct {
			Project      string `json:"project"`
			MaxResults   int    `json:"max_results"`
			Observations []struct {
				ID      int64  `json:"id"`
				Project string `json:"project"`
			} `json:"observations"`
		}
		require.NoError(t, json.Unmarshal(raw, &result))
		assert.Equal(t, project, result.Project)
		assert.Equal(t, 1, result.MaxResults)
		ids := make([]int64, len(result.Observations))
		for i, observation := range result.Observations {
			assert.Equal(t, project, observation.Project)
			ids[i] = observation.ID
		}
		return ids
	}
	t.Run("auth_middleware_rejects_missing_and_invalid_bearers", func(t *testing.T) {
		call(t, http.MethodGet, "", ftsQuery, http.StatusUnauthorized)
		call(t, http.MethodGet, auth.TokenRawPrefix+"invalid-synthetic-keycard", ftsQuery, http.StatusUnauthorized)
		callsMu.Lock()
		defer callsMu.Unlock()
		assert.Empty(t, calls, "unauthenticated HTTP requests must never reach the reranker")
	})
	for _, branch := range []string{"fts", "pgvector"} {
		t.Run(branch, func(t *testing.T) {
			query := ftsQuery
			service.embeddingClient = nil
			if branch == "pgvector" {
				query, service.embeddingClient = vectorQuery, embedder
			}
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				for _, identity := range []struct {
					name, token string
					identity    auth.Identity
				}{{"alice", aliceToken, alice}, {"bob", bobToken, bob}} {
					t.Run(method+"/"+identity.name, func(t *testing.T) {
						callsMu.Lock()
						before, embeddingsBefore := len(calls), embeddingCalls
						callsMu.Unlock()
						ids := call(t, method, identity.token, query, http.StatusOK)
						assert.Len(t, ids, 1, "authorized result must survive higher-ranked inaccessible candidates at limit=1")
						for _, id := range ids {
							visible := false
							for content, memory := range byContent {
								visible = visible || (memory.ID == id && allowed[identity.identity.Principal][content])
							}
							assert.True(t, visible, "HTTP returned an inaccessible memory ID %d", id)
						}
						callsMu.Lock()
						observed := append([]rerankCall(nil), calls[before:]...)
						embeddingsAfter := embeddingCalls
						callsMu.Unlock()
						assert.NotEmpty(t, observed, "external reranker must receive authorized passages; disabling reranking is not a privacy fix")
						seenOwn, seenShared := false, false
						for _, request := range observed {
							assert.GreaterOrEqual(t, len(request.Documents), 2, "reranking must remain active after authorization")
							for _, document := range request.Documents {
								assert.True(t, allowed[identity.identity.Principal][document], "external reranker received an inaccessible passage (payload withheld)")
								seenOwn = seenOwn || document == own.Content
								seenShared = seenShared || document == shared.Content
							}
						}
						if identity.name == "alice" {
							assert.True(t, seenOwn && seenShared, "both authorized passages must reach reranking despite the hidden candidate flood")
						}
						if branch == "pgvector" {
							assert.Greater(t, embeddingsAfter, embeddingsBefore, "real HTTP search must request a query embedding")
						}
					})
				}
			}
		})
	}
	t.Run("bob_cannot_recall_alice_private_fts_match", func(t *testing.T) {
		service.embeddingClient = nil
		callsMu.Lock()
		before := len(calls)
		callsMu.Unlock()
		assert.Empty(t, call(t, http.MethodGet, bobToken, "aliceprivateprobe", http.StatusOK), "cross-principal private match must not leak")
		callsMu.Lock()
		defer callsMu.Unlock()
		for _, request := range calls[before:] {
			for _, document := range request.Documents {
				assert.True(t, allowed[bob.Principal][document], "negative recall sent another principal's private passage to reranking")
			}
		}
	})
}

func openContextSearchPrivacyStore(t *testing.T) *gormdb.Store {
	t.Helper()
	dsn := os.Getenv("ENGRAM_SEARCH_PRIVACY_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_SEARCH_PRIVACY_TEST_DSN not set; explicitly select a disposable loopback PostgreSQL fixture with pgvector")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"postgres", "postgresql"}, parsed.Scheme)
	ip := net.ParseIP(parsed.Hostname())
	require.NotNil(t, ip, "fixture DSN must use an explicit loopback IP, not an inherited host or DNS name")
	require.True(t, ip.IsLoopback(), "fixture DSN must stay on loopback")
	require.NotEmpty(t, parsed.Port(), "fixture DSN must explicitly select its disposable PostgreSQL port")
	require.NotEmpty(t, strings.Trim(parsed.Path, "/"), "fixture DSN must explicitly select its disposable database")
	for key := range parsed.Query() {
		switch strings.ToLower(key) {
		case "host", "hostaddr", "port", "dbname", "database", "service", "servicefile":
			t.Fatal("fixture DSN must not override its explicit loopback target through query parameters")
		}
	}
	t.Setenv("PGSERVICE", "")
	admin, err := gormlib.Open(postgres.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := admin.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(2)
	t.Cleanup(func() { assert.NoError(t, pool.Close()) })
	var extensionReady, publicUnused bool
	require.NoError(t, admin.Raw(`SELECT EXISTS (SELECT 1 FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'vector' AND n.nspname = 'public')`).Scan(&extensionReady).Error)
	require.True(t, extensionReady, "parent must preinstall pgvector in the disposable DB's public schema")
	require.NoError(t, admin.Raw(`SELECT to_regclass('public.memories') IS NULL AND to_regclass('public.migrations') IS NULL`).Scan(&publicUnused).Error)
	require.True(t, publicUnused, "refusing a populated public application schema; never use the mirror or production DSN")
	schema := "worker_search_privacy_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() { assert.NoError(t, admin.Exec("DROP SCHEMA "+schema+" CASCADE").Error) })
	options := parsed.Query()
	options.Set("search_path", schema+",public")
	parsed.RawQuery = options.Encode()
	store, err := gormdb.NewStore(gormdb.Config{DSN: parsed.String(), MaxConns: 2, LogLevel: logger.Silent})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	var actualSchema string
	require.NoError(t, store.DB.Raw("SELECT current_schema()").Scan(&actualSchema).Error)
	require.Equal(t, schema, actualSchema, "fixture must resolve only its new application schema before public")
	return store
}
