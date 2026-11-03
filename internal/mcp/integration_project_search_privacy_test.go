package mcp

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/internal/reranking"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

// This covers native tools/call -> handleRecallMemory -> hybrid retrieval.
// The native layer receives an identity from the real token validator, as its
// authenticated transport does; this does not claim stdio/gRPC transport proof.
func TestRecallMemoryProjectPrivacyPostgres(t *testing.T) {
	store := openRecallProjectPrivacyStore(t)
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "false")
	memoryStore := gormdb.NewMemoryStore(store)
	embeddingStore := embedding.NewStore(store.DB)
	graphStore := graph.NewStore(store.DB, nil)
	project, foreignProject := uuid.NewString(), uuid.NewString()
	require.NoError(t, store.DB.Create(&[]gormdb.Project{{ID: project}, {ID: foreignProject}}).Error)
	tokenStore := gormdb.NewTokenStore(store)
	validator := auth.NewValidator(uuid.NewString(), tokenStore)
	issueIdentity := func(principal string) auth.Identity {
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
		return identity
	}
	alice := issueIdentity("agent/search-privacy-alice")
	bob := issueIdentity("agent/search-privacy-bob")
	_, err := validator.Validate(t.Context(), auth.TokenRawPrefix+"invalid-synthetic-keycard")
	require.Error(t, err, "native fixture identities must come from actual keycard validation")

	const ftsQuery = "cobalt lantern"
	const vectorQuery = "luminous orbital navigation"
	const graphQuery = "graphanchorprobe"
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

	allowed := map[string]map[string]bool{alice.Principal: {}, bob.Principal: {}}
	byContent := make(map[string]*models.Memory)
	insert := func(mem *models.Memory, vec []float32, readers ...string) *models.Memory {
		t.Helper()
		mem.Tags = []string{"type:fact", "project-privacy-synthetic"}
		created, err := memoryStore.Create(t.Context(), mem)
		require.NoError(t, err)
		if vec != nil {
			require.NoError(t, embeddingStore.StoreChunks(t.Context(), []embedding.Chunk{{
				MemoryID: created.ID, Text: created.Content, Model: embedder.Model(), Embedding: pgvector.NewVector(vec),
			}}))
		}
		byContent[created.Content] = created
		for _, reader := range readers {
			allowed[reader][created.Content] = true
		}
		return created
	}
	own := insert(&models.Memory{
		Project: project, Content: "cobalt authorized northstar lantern aliceprivateprobe " + graphQuery,
		PrivacyScope: "project", OwnerPrincipal: alice.Principal, OwnerPrincipalKind: "agent", AgentVisibility: "private",
	}, vector(0.8, 0.6), alice.Principal)
	shared := insert(&models.Memory{
		Project: project, Content: "cobalt shared southstar lantern teamvisibleprobe " + graphQuery,
		PrivacyScope: "project", OwnerPrincipal: bob.Principal, OwnerPrincipalKind: "agent", AgentVisibility: "shared",
	}, vector(0.75, 0.66), alice.Principal, bob.Principal)
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

	// Neighbors have no chunks and no query lexemes: only real graph expansion
	// can deliver them. Each inaccessible class independently exceeds the current
	// 20-neighbor cap; their smaller IDs must not starve the authorized neighbor.
	var graphNeighbors []*models.Memory
	for _, class := range []string{"principal", "domain"} {
		for i := range 24 {
			mem := &models.Memory{
				Project: project, Content: fmt.Sprintf("inaccessible %s graph neighbor %03d", class, i),
				PrivacyScope: "project", OwnerPrincipal: bob.Principal, OwnerPrincipalKind: "agent", AgentVisibility: "private",
			}
			if class == "domain" {
				mem.AgentVisibility, mem.Domain = "shared", "search-privacy-domain"
			}
			graphNeighbors = append(graphNeighbors, insert(mem, nil, bob.Principal))
		}
	}
	graphVisible := insert(&models.Memory{
		Project: project, Content: "authorized graph neighbor control",
		PrivacyScope: "project", OwnerPrincipal: bob.Principal, OwnerPrincipalKind: "agent", AgentVisibility: "shared",
	}, nil, alice.Principal, bob.Principal)
	for _, neighbor := range graphNeighbors {
		require.Less(t, neighbor.ID, graphVisible.ID, "inaccessible graph neighbors must precede the authorized neighbor under the real ID-ordered cap")
	}
	graphNeighbors = append(graphNeighbors, graphVisible)
	for _, neighbor := range graphNeighbors {
		for _, anchor := range []*models.Memory{own, shared} {
			_, err := graphStore.Create(t.Context(), &graph.Edge{
				SourceID: &anchor.ID, TargetID: &neighbor.ID, SourceType: "memory", TargetType: "memory",
				EdgeType: graph.EdgeUses, Weight: 1, SourceSessionID: "search-privacy-synthetic",
			})
			require.NoError(t, err)
		}
	}
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
	require.GreaterOrEqual(t, strongerHidden, int64(128), "fixture must rank inaccessible FTS rows ahead of both visible matches")
	ftsOnly, err := memoryStore.SearchFTS(t.Context(), project, vectorQuery, 200)
	require.NoError(t, err)
	require.Empty(t, ftsOnly, "vector-only native recall must not be rescued by FTS")

	srv := NewServer(ServerOptions{Version: "project-search-privacy-fixture"})
	srv.memoryStore = memoryStore
	srv.SetRerankClient(reranker)
	srv.SetGraphStore(graphStore)
	call := func(t *testing.T, identity auth.Identity, query string, tiers []string, expand bool) []struct {
		ID                 int64  `json:"id"`
		Project            string `json:"project"`
		RankingExplanation struct {
			SourceTier string `json:"source_tier"`
		} `json:"ranking_explanation"`
	} {
		t.Helper()
		args := mustJSON(t, map[string]any{
			"query": query, "project": project, "format": "items", "limit": 1,
			"tier_filter": tiers, "expand_graph": expand, "explain": true,
			"agent_id": bob.Principal,
		})
		// Identity construction is deliberately not ClientWithPrincipal: use the
		// persisted keycard's validator result at this native dispatch boundary.
		ctx := auth.WithIdentity(t.Context(), identity)
		response := srv.HandleRequest(ctx, &Request{
			JSONRPC: "2.0", ID: t.Name(), Method: "tools/call",
			Params: mustJSON(t, ToolCallParams{Name: "recall_memory", Arguments: args}),
		})
		require.NotNil(t, response)
		require.Nil(t, response.Error, "real native recall returned an error; private payload intentionally omitted")
		encoded, err := json.Marshal(response.Result)
		require.NoError(t, err)
		var envelope struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}
		require.NoError(t, json.Unmarshal(encoded, &envelope))
		require.False(t, envelope.IsError)
		require.Len(t, envelope.Content, 1)
		var rows []struct {
			ID                 int64  `json:"id"`
			Project            string `json:"project"`
			RankingExplanation struct {
				SourceTier string `json:"source_tier"`
			} `json:"ranking_explanation"`
		}
		require.NoError(t, json.Unmarshal([]byte(envelope.Content[0].Text), &rows))
		return rows
	}
	for _, branch := range []struct {
		name, query, tier string
		tiers             []string
		expand            bool
	}{
		{"fts", ftsQuery, "tier1_fts", []string{"tier1_fts"}, false},
		{"pgvector", vectorQuery, "tier1_vector", []string{"tier1_vector"}, false},
		{"graph", graphQuery, "", []string{"tier1_fts", "tier2_graph"}, true},
	} {
		t.Run(branch.name, func(t *testing.T) {
			srv.SetEmbeddingStores(nil, embeddingStore)
			if branch.name == "pgvector" {
				srv.SetEmbeddingStores(embedder, embeddingStore)
			}
			for _, identity := range []struct {
				name string
				id   auth.Identity
			}{{"alice", alice}, {"bob", bob}} {
				if branch.expand && identity.name != "alice" {
					// Bob legitimately owns all graph neighbors, so his ordinary cap
					// may exclude the last one. Alice has only one authorized neighbor.
					continue
				}
				t.Run(identity.name, func(t *testing.T) {
					callsMu.Lock()
					before, embeddingsBefore := len(calls), embeddingCalls
					callsMu.Unlock()
					rows := call(t, identity.id, branch.query, branch.tiers, branch.expand)
					assert.Len(t, rows, 1, "authorized native result must survive hidden candidates at limit=1")
					for _, row := range rows {
						visible := false
						for content, memory := range byContent {
							visible = visible || (memory.ID == row.ID && allowed[identity.id.Principal][content])
						}
						assert.True(t, visible, "native recall returned an inaccessible memory ID %d", row.ID)
						assert.Equal(t, project, row.Project)
						if branch.tier != "" {
							assert.Equal(t, branch.tier, row.RankingExplanation.SourceTier, "native oracle must exercise the requested real retrieval leg")
						}
					}
					callsMu.Lock()
					observed := append([]rerankCall(nil), calls[before:]...)
					embeddingsAfter := embeddingCalls
					callsMu.Unlock()
					assert.NotEmpty(t, observed, "external reranker must receive authorized passages; disabling reranking is not a privacy fix")
					seenOwn, seenShared, seenGraph := false, false, false
					for _, request := range observed {
						assert.GreaterOrEqual(t, len(request.Documents), 2, "reranking must stay active after authorization")
						for _, document := range request.Documents {
							assert.True(t, allowed[identity.id.Principal][document], "external reranker received an inaccessible passage (payload withheld)")
							seenOwn = seenOwn || document == own.Content
							seenShared = seenShared || document == shared.Content
							seenGraph = seenGraph || document == graphVisible.Content
						}
					}
					if identity.name == "alice" {
						assert.True(t, seenOwn && seenShared, "both authorized seed passages must reach reranking despite hidden candidates")
					}
					if branch.expand {
						assert.True(t, seenGraph, "authorized graph-only neighbor must actually reach reranking; disabling expansion is not a privacy fix")
					}
					if branch.name == "pgvector" {
						assert.Greater(t, embeddingsAfter, embeddingsBefore, "native recall must request a query embedding")
					}
				})
			}
		})
	}
	t.Run("bob_cannot_recall_alice_private_fts_match", func(t *testing.T) {
		srv.SetEmbeddingStores(nil, embeddingStore)
		callsMu.Lock()
		before := len(calls)
		callsMu.Unlock()
		assert.Empty(t, call(t, bob, "aliceprivateprobe", []string{"tier1_fts"}, false), "cross-principal private match must not leak")
		callsMu.Lock()
		defer callsMu.Unlock()
		for _, request := range calls[before:] {
			for _, document := range request.Documents {
				assert.True(t, allowed[bob.Principal][document], "negative native recall sent another principal's private passage to reranking")
			}
		}
	})
}

func openRecallProjectPrivacyStore(t *testing.T) *gormdb.Store {
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
	schema := "mcp_search_privacy_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
