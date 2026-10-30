package worker

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
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
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/internal/retrieval"
	"github.com/thebtf/engram/pkg/models"
)

const issue520Paraphrase = "renew dialogue upon reaching budget ceiling"

type issue520Observation struct {
	ID         int64           `json:"id"`
	Type       string          `json:"type"`
	Project    string          `json:"project"`
	Title      string          `json:"title"`
	Narrative  string          `json:"narrative"`
	Concepts   []string        `json:"concepts"`
	Similarity json.RawMessage `json:"similarity"`
}

type issue520SearchResponse struct {
	Project      string                `json:"project"`
	Query        string                `json:"query"`
	Observations []issue520Observation `json:"observations"`
	AlwaysInject []issue520Observation `json:"always_inject"`
	MaxResults   int                   `json:"max_results"`
}

func TestContextSearchHybridPostgres(t *testing.T) {
	// Check the existing extension before the shared fixture invokes migrations.
	// Never install extensions or touch the database's public schema in this test.
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set; requires local synthetic PostgreSQL with existing pgvector")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"postgres", "postgresql"}, parsed.Scheme)
	require.Contains(t, []string{"127.0.0.1", "localhost", "::1"}, parsed.Hostname(), "fixture must stay on the workstation")
	preflight, err := gormlib.Open(postgres.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := preflight.DB()
	require.NoError(t, err)
	var vectorInstalled bool
	err = preflight.Raw(`SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')`).Scan(&vectorInstalled).Error
	closeErr := pool.Close()
	require.NoError(t, err)
	require.NoError(t, closeErr)
	require.True(t, vectorInstalled, "pgvector must already be installed")

	// The existing helper creates one UUID-named schema, verifies search_path,
	// closes its store, then drops only that schema after all route consumers close.
	store := openWorkerUCIContextCompositionStore(t)
	memoryStore := gormdb.NewMemoryStore(store)
	embeddingStore := embedding.NewStore(store.DB)
	rules := gormdb.NewBehavioralRulesStore(store)
	project, foreignProject := uuid.NewString(), uuid.NewString()
	require.NoError(t, store.DB.Create(&[]gormdb.Project{{ID: project}, {ID: foreignProject}}).Error)
	var replicationRole string
	require.NoError(t, store.DB.Raw("SHOW session_replication_role").Scan(&replicationRole).Error)
	require.Equal(t, "origin", replicationRole)

	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	t.Setenv("ENGRAM_HAP_01B_LEGACY_DIRECT_ENFORCEMENT", "false")
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
		require.Equal(t, principal, identity.Principal)
		require.NotEmpty(t, identity.WorkstationID())
		return raw, identity
	}
	aliceToken, alice := issueKeycard("agent/issue520-alice")
	bobToken, bob := issueKeycard("agent/issue520-bob")

	vector := func(first, second float32) []float32 {
		v := make([]float32, embedding.EmbeddingDim)
		v[0], v[1] = first, second
		return v
	}
	unrelatedVector := make([]float32, embedding.EmbeddingDim)
	unrelatedVector[2] = 1
	workstationVector := make([]float32, embedding.EmbeddingDim)
	workstationVector[3] = 1
	var embeddingRequests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" {
			http.Error(w, "unexpected embedding route", http.StatusBadRequest)
			return
		}
		var request struct {
			Input []string `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Input) != 1 {
			http.Error(w, "one query vector required", http.StatusBadRequest)
			return
		}
		embeddingRequests.Add(1)
		queryVector := unrelatedVector
		switch request.Input[0] {
		case "ClaudeReset", "Claude Reset", issue520Paraphrase, "ClaudeReset principalonly confidential synthetic notes.":
			queryVector = vector(1, 0)
		case "ownerworkstation":
			queryVector = workstationVector
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": queryVector}}})
	}))
	t.Cleanup(provider.Close)
	t.Setenv("ENGRAM_EMBEDDING_URL", provider.URL)
	t.Setenv("ENGRAM_EMBEDDING_API_KEY", "")
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "issue520-local-query-vectors")
	t.Setenv("ENGRAM_EMBEDDING_DIMENSIONS", "")
	embedder, err := embedding.NewClient()
	require.NoError(t, err)
	insert := func(mem *models.Memory, v []float32) *models.Memory {
		t.Helper()
		mem.Tags = []string{"type:fact", "issue520-synthetic"}
		created, err := memoryStore.Create(t.Context(), mem)
		require.NoError(t, err)
		require.NoError(t, embeddingStore.StoreChunks(t.Context(), []embedding.Chunk{{
			MemoryID: created.ID, Text: created.Content, Model: embedder.Model(), Embedding: pgvector.NewVector(v),
		}}))
		return created
	}
	visible := insert(&models.Memory{
		Project: project, Content: "ClaudeReset restores an exhausted assistant conversation.",
		OwnerPrincipal: alice.Principal, OwnerPrincipalKind: "agent", AgentVisibility: "private", PrivacyScope: "project",
	}, vector(0.8, 0.6))
	hidden := insert(&models.Memory{
		Project: project, Content: "ClaudeReset principalonly confidential synthetic notes.",
		OwnerPrincipal: bob.Principal, OwnerPrincipalKind: "agent", AgentVisibility: "private", PrivacyScope: "project",
	}, vector(1, 0))
	workstationPrivate := insert(&models.Memory{
		Project: project, Content: "ownerworkstation confidential synthetic notes.", PrivacyScope: "private",
		SourceWorkstationID: alice.WorkstationID(), SourceSessions: []string{"recorded-session"},
	}, workstationVector)
	foreign := insert(&models.Memory{
		Project: foreignProject, Content: "ClaudeReset foreignprojectonly synthetic notes.", PrivacyScope: "project",
	}, vector(1, 0))
	globalRule, err := rules.Create(t.Context(), &models.BehavioralRule{Content: "Synthetic global safety instruction.", Priority: 20})
	require.NoError(t, err)
	projectRule, err := rules.Create(t.Context(), &models.BehavioralRule{Project: &project, Content: "Synthetic project safety instruction.", Priority: 10})
	require.NoError(t, err)
	_, err = rules.Create(t.Context(), &models.BehavioralRule{Project: &foreignProject, Content: "Synthetic foreign instruction.", Priority: 30})
	require.NoError(t, err)
	disabledRule, err := rules.Create(t.Context(), &models.BehavioralRule{Project: &project, Content: "Synthetic disabled instruction.", Priority: 40})
	require.NoError(t, err)
	_, err = rules.SetEnabled(t.Context(), disabledRule.ID, false, nil)
	require.NoError(t, err)

	ta, err := NewTokenAuth(master)
	require.NoError(t, err)
	ta.SetTokenStore(tokenStore)
	ta.SetValidator(validator)
	cfg := config.Default()
	cfg.ContextMaxPromptResults, cfg.AlwaysInjectLimit, cfg.ContextMaxTokens = 50, 2, 0
	service := &Service{
		router: chi.NewRouter(), tokenAuth: ta, config: cfg, store: store,
		memoryStore: memoryStore, behavioralRulesStore: rules,
		embeddingClient: embedder, embeddingStore: embeddingStore,
		retrievalStats: make(map[string]*RetrievalStats), mcpHealth: mcp.NewMCPHealth(),
	}
	service.ready.Store(true)
	service.setupMiddleware()
	service.setupRoutes()
	server := httptest.NewServer(service.router)
	t.Cleanup(server.Close)
	require.Nil(t, service.retrievalHooks)
	require.Nil(t, service.memoryStoreSeam)

	call := func(t *testing.T, token, method, path string, body any, status int, result any) {
		t.Helper()
		var payload []byte
		if body != nil {
			var err error
			payload, err = json.Marshal(body)
			require.NoError(t, err)
		}
		request, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, bytes.NewReader(payload))
		require.NoError(t, err)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Equal(t, status, response.StatusCode, "synthetic route status; response body intentionally omitted")
		if result != nil {
			require.NoError(t, json.Unmarshal(raw, result))
		}
	}
	memoryState := func(t *testing.T) string {
		t.Helper()
		var state string
		require.NoError(t, store.DB.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(m) ORDER BY m.id), '[]'::jsonb)::text FROM memories AS m`).Scan(&state).Error)
		return state
	}
	search := func(t *testing.T, token, method, query, session string, limit int) issue520SearchResponse {
		t.Helper()
		values := url.Values{"project": {project}, "query": {query}, "session_id": {session}}
		if limit == 1 {
			values.Set("limit", "1")
		}
		var body any
		if method == http.MethodPost {
			body = map[string]any{
				"project": project, "query": query, "session_id": session,
				"agent_id": bob.Principal, "cwd": "ignored-synthetic-client-path",
			}
		}
		var result issue520SearchResponse
		before := memoryState(t)
		call(t, token, method, "/api/context/search?"+values.Encode(), body, http.StatusOK, &result)
		require.True(t, before == memoryState(t), "REST search must not mutate stored memory/lifecycle fields")
		require.Equal(t, project, result.Project)
		require.Equal(t, query, result.Query)
		return result
	}
	ids := func(rows []issue520Observation) []int64 {
		result := make([]int64, 0, len(rows))
		for _, row := range rows {
			result = append(result, row.ID)
		}
		return result
	}

	if !t.Run("real_vector_storage_and_rank_oracle", func(t *testing.T) {
		for _, query := range []string{"Claude Reset", issue520Paraphrase} {
			require.NotContains(t, strings.ToLower(visible.Content), strings.ToLower(query))
			fts, err := memoryStore.SearchFTS(t.Context(), project, query, 10)
			require.NoError(t, err)
			require.Empty(t, fts, "equivalent-expression oracle must not be a lexical match")
		}
		vectors, err := embedder.Embed(t.Context(), []string{issue520Paraphrase})
		require.NoError(t, err)
		require.Len(t, vectors, 1)
		ranked, _, err := retrieval.HybridSearch(t.Context(), project, issue520Paraphrase, 4,
			memoryStore, embeddingStore, nil, retrieval.HybridOptions{QueryVec: vectors[0], Explain: true})
		require.NoError(t, err)
		require.Len(t, ranked, 2, "real project-scoped vector retrieval must materialize exactly the two matching stored rows")
		require.Equal(t, hidden.ID, ranked[0].Memory.ID, "the invisible candidate must actually outrank the visible candidate")
		require.Equal(t, visible.ID, ranked[1].Memory.ID)
		require.NotEqual(t, foreign.ID, ranked[0].Memory.ID)
	}) {
		return
	}

	compatibilityProject := func(t *testing.T) string {
		t.Helper()
		project := uuid.NewString()
		require.NoError(t, store.DB.Create(&gormdb.Project{ID: project}).Error)
		return project
	}
	compatibilityMemory := func(t *testing.T, project, content string, tags []string) *models.Memory {
		t.Helper()
		memory, err := memoryStore.Create(t.Context(), &models.Memory{
			Project: project, Content: content, Tags: tags, PrivacyScope: "project",
			OwnerPrincipal: alice.Principal, OwnerPrincipalKind: "agent", AgentVisibility: "private",
		})
		require.NoError(t, err)
		require.NoError(t, embeddingStore.StoreChunks(t.Context(), []embedding.Chunk{{
			MemoryID: memory.ID, Text: memory.Content, Model: embedder.Model(), Embedding: pgvector.NewVector(unrelatedVector),
		}}))
		return memory
	}
	compatibilitySearch := func(t *testing.T, project, method, query string, limit int) issue520SearchResponse {
		t.Helper()
		values := url.Values{"project": {project}, "query": {query}, "limit": {strconv.Itoa(limit)}}
		var body any
		if method == http.MethodPost {
			body = map[string]any{"project": project, "query": query, "session_id": "audit-only-compatibility-session"}
		}
		before := memoryState(t)
		var response issue520SearchResponse
		call(t, aliceToken, method, "/api/context/search?"+values.Encode(), body, http.StatusOK, &response)
		require.True(t, before == memoryState(t), "REST compatibility search must not mutate memory rows")
		require.Equal(t, project, response.Project)
		require.Equal(t, query, response.Query)
		return response
	}

	t.Run("rest_memory_store_failure_preserves_server_error", func(t *testing.T) {
		service.embeddingClient = nil
		defer func() {
			service.embeddingClient = embedder
			service.memoryStoreSeam = nil
		}()
		requestsBefore := embeddingRequests.Load()
		project := compatibilityProject(t)
		const query = "storagefailureprobe520"
		memory := compatibilityMemory(t, project, query+" synthetic cedar archive", []string{"type:fact", "storage-error-only-memory"})
		var schema string
		require.NoError(t, store.DB.Raw("SELECT current_schema()").Scan(&schema).Error)
		require.True(t, strings.HasPrefix(schema, "worker_uci_context_"), "rename must target only this fixture's UUID schema")
		quotedSchema := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
		originalTable := quotedSchema + `."memories"`
		renamedTable := quotedSchema + `."issue520_unreadable_memories"`
		var originalOID int64
		require.NoError(t, store.DB.Raw("SELECT to_regclass(?)::oid::bigint", originalTable).Scan(&originalOID).Error)
		require.NotZero(t, originalOID)
		t.Logf("test_owned_schema=%s original_memories_oid=%d", schema, originalOID)
		renamed := false
		restore := func() error {
			if !renamed {
				return nil
			}
			err := store.DB.Exec("ALTER TABLE " + renamedTable + ` RENAME TO "memories"`).Error
			if err == nil {
				renamed = false
			}
			return err
		}
		defer func() {
			if err := restore(); err != nil {
				t.Errorf("restore test-owned memory table: %v", err)
			}
		}()
		setPath := func(path string) {
			service.memoryStoreSeam = nil
			if path == "fallback" {
				// Select the retained fallback implementation, not a fake failing store.
				service.memoryStoreSeam = memoryStore
			}
		}
		healthy := func(phase string) {
			for _, path := range []string{"fallback", "hybrid"} {
				setPath(path)
				for _, method := range []string{http.MethodGet, http.MethodPost} {
					t.Run(phase+"_"+path+"_"+method, func(t *testing.T) {
						response := compatibilitySearch(t, project, method, query, 1)
						require.Equal(t, []int64{memory.ID}, ids(response.Observations), "the same authenticated synthetic memory must remain visible")
					})
				}
			}
		}
		healthy("healthy_before")
		require.NoError(t, store.DB.Exec("ALTER TABLE "+originalTable+` RENAME TO "issue520_unreadable_memories"`).Error)
		renamed = true
		if !t.Run("real_memory_store_is_unreadable", func(t *testing.T) {
			var memoryTableAbsent bool
			require.NoError(t, store.DB.Raw("SELECT to_regclass('memories') IS NULL").Scan(&memoryTableAbsent).Error)
			require.True(t, memoryTableAbsent, "no other schema may rescue the renamed test-owned memory table")
			_, err := memoryStore.ListWithOffset(t.Context(), project, 1, 0)
			require.Error(t, err, "the actual PostgreSQL memory store must be unreadable")
		}) {
			return
		}
		for _, path := range []string{"fallback", "hybrid"} {
			setPath(path)
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				t.Run("unreadable_"+path+"_"+method, func(t *testing.T) {
					values := url.Values{"project": {project}, "query": {query}, "limit": {"1"}}
					var body any
					if method == http.MethodPost {
						body = map[string]any{"project": project, "query": query}
					}
					call(t, aliceToken, method, "/api/context/search?"+values.Encode(), body, http.StatusInternalServerError, nil)
				})
			}
		}
		require.NoError(t, restore())
		t.Run("original_table_restored", func(t *testing.T) {
			var restoredOID int64
			require.NoError(t, store.DB.Raw("SELECT to_regclass(?)::oid::bigint", originalTable).Scan(&restoredOID).Error)
			require.Equal(t, originalOID, restoredOID, "restore must preserve the original table identity")
			var renamedAbsent bool
			require.NoError(t, store.DB.Raw("SELECT to_regclass(?) IS NULL", renamedTable).Scan(&renamedAbsent).Error)
			require.True(t, renamedAbsent)
			t.Log("original memory table restored; temporary renamed object absent")
		})
		healthy("healthy_after")
		require.Equal(t, requestsBefore, embeddingRequests.Load(), "embedding fallback must not mask the memory-store failure")
	})

	t.Run("rest_review_rule_score_identity", func(t *testing.T) {
		service.embeddingClient = nil
		defer func() { service.embeddingClient = embedder }()
		requestsBefore := embeddingRequests.Load()
		project := compatibilityProject(t)
		const query = "collisionprobe520"
		memory := compatibilityMemory(t, project, query+" cedar memory archive", []string{"type:fact", "collision-only-memory"})
		rule, err := rules.Create(t.Context(), &models.BehavioralRule{
			Project: &project, Content: query + " lunar guidance instruction", Priority: 10,
		})
		require.NoError(t, err)
		require.Equal(t, memory.ID, rule.ID, "the independent private-schema sequences must produce the intended numeric ID collision")
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(method, func(t *testing.T) {
				response := compatibilitySearch(t, project, method, query, 10)
				require.Len(t, response.Observations, 2, "both stored records must be returned in untyped observations")
				byType := make(map[string]issue520Observation)
				for _, observation := range response.Observations {
					byType[observation.Type] = observation
				}
				require.Contains(t, byType, string(models.ObsTypeDiscovery))
				require.Contains(t, byType, string(models.ObsTypeGuidance))
				memoryObservation := byType[string(models.ObsTypeDiscovery)]
				ruleObservation := byType[string(models.ObsTypeGuidance)]
				require.Equal(t, memory.ID, memoryObservation.ID)
				require.Equal(t, memory.Content, memoryObservation.Narrative)
				require.NotEmpty(t, memoryObservation.Similarity, "the memory must have its own real retrieval score")
				require.Equal(t, rule.ID, ruleObservation.ID)
				require.Equal(t, rule.Content, ruleObservation.Narrative)
				require.Empty(t, ruleObservation.Similarity, "guidance must not inherit a memory score from the same numeric ID")
			})
		}
		require.Equal(t, requestsBefore, embeddingRequests.Load(), "the unavailable embedder must not supply results")
	})
	t.Run("rest_review_guidance_recency_before_limit", func(t *testing.T) {
		service.embeddingClient = nil
		defer func() { service.embeddingClient = embedder }()
		project := compatibilityProject(t)
		const query = "recencymergeprobe520"
		memory := compatibilityMemory(t, project, query+" older cedar memory", []string{"type:fact", "recency-only-memory"})
		older := time.Now().UTC().Add(-2 * time.Hour)
		require.NoError(t, store.DB.Model(&gormdb.Memory{}).Where("id = ?", memory.ID).Update("created_at", older).Error)
		rule, err := rules.Create(t.Context(), &models.BehavioralRule{
			Project: &project, Content: query + " newer lunar guidance", Priority: 10,
		})
		require.NoError(t, err)
		storedMemory, err := memoryStore.Get(t.Context(), memory.ID)
		require.NoError(t, err)
		require.True(t, rule.CreatedAt.After(storedMemory.CreatedAt), "stored guidance must be newer than the matching stored memory")
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(method, func(t *testing.T) {
				control := compatibilitySearch(t, project, method, query, 10)
				require.Len(t, control.Observations, 2, "both records must match before the visible cap")
				response := compatibilitySearch(t, project, method, query, 1)
				require.Len(t, response.Observations, 1)
				require.Equal(t, string(models.ObsTypeGuidance), response.Observations[0].Type,
					"the newer matching rule must survive the untyped observations limit")
				require.Equal(t, rule.ID, response.Observations[0].ID)
				require.Equal(t, rule.Content, response.Observations[0].Narrative)
			})
		}
	})
	t.Run("rest_review_literal_compatibility_without_embedder", func(t *testing.T) {
		service.embeddingClient = nil
		defer func() { service.embeddingClient = embedder }()
		requestsBefore := embeddingRequests.Load()
		project := compatibilityProject(t)
		substringMemory := compatibilityMemory(t, project,
			"The VaporLedgerBridge adapter retains local journal checkpoints.", []string{"type:fact", "substring-only-memory"})
		tagMemory := compatibilityMemory(t, project,
			"Cedar navigation archive stores ordinary bearings.", []string{"type:fact", "tagonlyneedle520"})
		require.NotContains(t, strings.ToLower(tagMemory.Content), "tagonlyneedle520")
		require.Contains(t, tagMemory.Tags, "tagonlyneedle520")
		for _, testcase := range []struct {
			name, query, control string
			memory               *models.Memory
		}{
			{name: "content_substring", query: "LedgerBridge", control: "VaporLedgerBridge", memory: substringMemory},
			{name: "tag_only", query: "tagonlyneedle520", control: "navigation", memory: tagMemory},
		} {
			fts, err := memoryStore.SearchFTS(t.Context(), project, testcase.query, 10)
			require.NoError(t, err)
			require.Empty(t, fts, "the compatibility case must not be rescued by content FTS")
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				t.Run(testcase.name+"_"+method, func(t *testing.T) {
					control := compatibilitySearch(t, project, method, testcase.control, 1)
					require.Equal(t, []int64{testcase.memory.ID}, ids(control.Observations), "authenticated lexical owner control")
					response := compatibilitySearch(t, project, method, testcase.query, 1)
					require.Equal(t, []int64{testcase.memory.ID}, ids(response.Observations),
						"existing literal content-substring and concept/tag matches must remain available without embeddings")
				})
			}
		}
		require.Equal(t, requestsBefore, embeddingRequests.Load(), "the unavailable embedder must not supply results")
	})
	t.Run("rest_review_literal_visibility_before_candidate_cap", func(t *testing.T) {
		service.embeddingClient = nil
		defer func() { service.embeddingClient = embedder }()
		requestsBefore := embeddingRequests.Load()
		project := compatibilityProject(t)
		const query = "privatecrowdingtag520"
		visible := compatibilityMemory(t, project, "Older authorized cedar archive.", []string{"type:fact", query})
		require.NoError(t, store.DB.Model(&gormdb.Memory{}).Where("id = ?", visible.ID).
			Update("created_at", time.Now().UTC().Add(-2*time.Hour)).Error)
		hiddenIDs := make(map[int64]bool)
		for i := range 64 {
			memory, err := memoryStore.Create(t.Context(), &models.Memory{
				Project: project, Content: "Newer private archive " + strconv.Itoa(i),
				Tags: []string{"type:fact", query}, PrivacyScope: "project",
				OwnerPrincipal: bob.Principal, OwnerPrincipalKind: "agent", AgentVisibility: "private",
			})
			require.NoError(t, err)
			hiddenIDs[memory.ID] = true
		}
		require.Greater(t, len(hiddenIDs), 50, "invisible matches must exceed the hybrid text candidate cap")
		stored, err := memoryStore.List(t.Context(), project, 100)
		require.NoError(t, err)
		require.Len(t, stored, 65)
		require.Equal(t, visible.ID, stored[len(stored)-1].ID, "the visible match must be older than every private match")
		fts, err := memoryStore.SearchFTS(t.Context(), project, query, 100)
		require.NoError(t, err)
		require.Empty(t, fts, "only tag/literal discovery can satisfy this request")
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(method, func(t *testing.T) {
				response := compatibilitySearch(t, project, method, query, 1)
				for _, observation := range response.Observations {
					require.False(t, hiddenIDs[observation.ID], "a private row must never leak")
				}
				require.Equal(t, []int64{visible.ID}, ids(response.Observations),
					"literal candidates must be authorized before invisible rows consume the candidate cap")
			})
		}
		require.Equal(t, requestsBefore, embeddingRequests.Load(), "the unavailable embedder must not rescue literal discovery")
	})
	t.Run("rest_review_guidance_keeps_hybrid_memory_order", func(t *testing.T) {
		project := compatibilityProject(t)
		create := func(content string, embeddingVector []float32) *models.Memory {
			t.Helper()
			memory, err := memoryStore.Create(t.Context(), &models.Memory{
				Project: project, Content: content, Tags: []string{"type:fact", "ordering-synthetic"}, PrivacyScope: "project",
				OwnerPrincipal: alice.Principal, OwnerPrincipalKind: "agent", AgentVisibility: "private",
			})
			require.NoError(t, err)
			require.NoError(t, embeddingStore.StoreChunks(t.Context(), []embedding.Chunk{{
				MemoryID: memory.ID, Text: memory.Content, Model: embedder.Model(), Embedding: pgvector.NewVector(embeddingVector),
			}}))
			return memory
		}
		olderRelevant := create("Older atlas route archive.", vector(1, 0))
		newerLessRelevant := create("Newer birch cluster guide.", vector(0.8, 0.6))
		require.NoError(t, store.DB.Model(&gormdb.Memory{}).Where("id = ?", olderRelevant.ID).
			Update("created_at", time.Now().UTC().Add(-2*time.Hour)).Error)
		const query = "Claude Reset"
		rule, err := rules.Create(t.Context(), &models.BehavioralRule{
			Project: &project, Content: query + " ordering instruction.", Priority: 10,
		})
		require.NoError(t, err)
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(method, func(t *testing.T) {
				response := compatibilitySearch(t, project, method, query, 10)
				require.Len(t, response.Observations, 3)
				require.Equal(t, string(models.ObsTypeGuidance), response.Observations[0].Type)
				require.Equal(t, rule.ID, response.Observations[0].ID)
				var memoryIDs []int64
				for _, observation := range response.Observations {
					if observation.Type == string(models.ObsTypeDiscovery) {
						memoryIDs = append(memoryIDs, observation.ID)
					}
				}
				require.Equal(t, []int64{olderRelevant.ID, newerLessRelevant.ID}, memoryIDs,
					"matching guidance must not replace the hybrid relevance order with memory recency order")
			})
		}
	})

	t.Run("authenticated_owner_and_get_post_parity", func(t *testing.T) {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			result := search(t, aliceToken, method, "ClaudeReset", "", 1)
			require.Equal(t, []int64{visible.ID}, ids(result.Observations))
			require.Equal(t, 1, result.MaxResults)
			owner := search(t, bobToken, method, "ClaudeReset", "", 1)
			require.Equal(t, []int64{hidden.ID}, ids(owner.Observations), "private principal memory is visible to its authenticated owner")
		}
	})
	t.Run("unrelated_query_and_always_inject_shape", func(t *testing.T) {
		result := search(t, aliceToken, http.MethodPost, "volcanic basalt mineralogy", "", 1)
		require.Empty(t, result.Observations)
		require.Len(t, result.AlwaysInject, 2)
		require.Equal(t, globalRule.ID, result.AlwaysInject[0].ID)
		require.Equal(t, projectRule.ID, result.AlwaysInject[1].ID)
		for _, rule := range result.AlwaysInject {
			require.Equal(t, string(models.ObsTypeGuidance), rule.Type)
			require.Equal(t, rule.Title, rule.Narrative)
			require.ElementsMatch(t, []string{"behavioral-rule", "always-inject"}, rule.Concepts)
		}
	})
	t.Run("foreign_and_private_rows_are_not_authorized_by_client_attribution", func(t *testing.T) {
		for _, query := range []string{"principalonly", "foreignprojectonly", "ownerworkstation"} {
			result := search(t, bobToken, http.MethodPost, query, "recorded-session", 1)
			if query == "principalonly" {
				result = search(t, aliceToken, http.MethodPost, query, "recorded-session", 1)
			}
			require.Empty(t, result.Observations, "foreign project/principal/workstation must remain invisible")
		}
	})
	t.Run("rest_session_id_is_audit_only_not_a_visibility_narrower", func(t *testing.T) {
		for _, session := range []string{"", "recorded-session", "unrelated-session"} {
			result := search(t, aliceToken, http.MethodPost, "ownerworkstation", session, 1)
			require.Equal(t, []int64{workstationPrivate.ID}, ids(result.Observations))
		}
	})
	t.Run("rest_fts_degradation_preserves_visible_result", func(t *testing.T) {
		service.embeddingClient = nil
		defer func() { service.embeddingClient = embedder }()
		result := search(t, aliceToken, http.MethodPost, "ClaudeReset", "unrelated-session", 1)
		require.Equal(t, []int64{visible.ID}, ids(result.Observations))
	})
	t.Run("rest_invisible_tier0_falls_through_to_visible_vector_match", func(t *testing.T) {
		result := search(t, aliceToken, http.MethodPost, hidden.Content, "unrelated-session", 1)
		require.Equal(t, []int64{visible.ID}, ids(result.Observations))
	})
	t.Run("rest_guidance_filter_precedes_visible_limit", func(t *testing.T) {
		before := memoryState(t)
		var result issue520SearchResponse
		call(t, aliceToken, http.MethodPost, "/api/context/search?limit=1", map[string]any{
			"project": project, "query": "synthetic", "obs_type": "guidance",
		}, http.StatusOK, &result)
		require.True(t, before == memoryState(t), "guidance search must remain read-only")
		require.Len(t, result.Observations, 1)
		require.Equal(t, string(models.ObsTypeGuidance), result.Observations[0].Type)
		require.Equal(t, globalRule.ID, result.Observations[0].ID)
		require.Len(t, result.AlwaysInject, 2)
	})
	t.Run("missing_auth_and_noncanonical_project_are_rejected", func(t *testing.T) {
		body := map[string]any{"project": project, "query": "ClaudeReset"}
		call(t, "", http.MethodPost, "/api/context/search", body, http.StatusUnauthorized, nil)
		body["project"] = "not-a-canonical-project"
		call(t, aliceToken, http.MethodPost, "/api/context/search", body, http.StatusBadRequest, nil)
	})

	for _, testcase := range []struct{ name, method, query string }{
		{"rest_post_equivalent_spelling_visible_limit", http.MethodPost, "Claude Reset"},
		{"rest_get_equivalent_spelling_parity", http.MethodGet, "Claude Reset"},
		{"rest_post_semantic_expression_visible_limit", http.MethodPost, issue520Paraphrase},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			result := search(t, aliceToken, testcase.method, testcase.query, "unrelated-session", 1)
			require.Equal(t, []int64{visible.ID}, ids(result.Observations),
				"real REST retrieval must return the stored semantic match after excluding the higher-ranked invisible row")
		})
	}
	// REST snapshots run before MCP's existing asynchronous reconsolidation.

	mcpServer := mcp.NewServer(mcp.ServerOptions{Version: "issue520-test-only"})
	mcpServer.SetMemoryStore(memoryStore)
	mcpServer.SetEmbeddingStores(embedder, embeddingStore)
	mcpServer.SetPrincipalMemoryQueryService(principalmemory.NewPrincipalMemoryQueryService(memoryStore, gormdb.NewAuditStore(store.DB)))
	adapter := &mcpHandlerAdapter{mcpServer: mcpServer}
	recall := func(t *testing.T, arguments map[string]any) []struct {
		ID                 int64 `json:"id"`
		RankingExplanation struct {
			SourceTier string `json:"source_tier"`
		} `json:"ranking_explanation"`
	} {
		t.Helper()
		arguments["project"], arguments["format"] = project, "items"
		args, err := json.Marshal(arguments)
		require.NoError(t, err)
		ctx := mcp.ContextWithProject(auth.WithIdentity(t.Context(), alice), project)
		raw, isError, err := adapter.HandleToolCall(ctx, "recall_memory", args)
		require.NoError(t, err)
		require.False(t, isError, "MCP must reach the existing recall handler")
		var envelope struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		require.NoError(t, json.Unmarshal(raw, &envelope))
		require.False(t, envelope.IsError, "MCP handler must not return a fixture/validation error")
		require.Len(t, envelope.Content, 1)
		var rows []struct {
			ID                 int64 `json:"id"`
			RankingExplanation struct {
				SourceTier string `json:"source_tier"`
			} `json:"ranking_explanation"`
		}
		require.NoError(t, json.Unmarshal([]byte(envelope.Content[0].Text), &rows))
		return rows
	}
	t.Run("current_mcp_semantic_filter_before_limit_control", func(t *testing.T) {
		before := embeddingRequests.Load()
		rows := recall(t, map[string]any{
			"query": issue520Paraphrase, "limit": 1, "explain": true,
			"type": "fact", "tags": []string{"issue520-synthetic"},
		})
		require.Greater(t, embeddingRequests.Load(), before)
		require.Len(t, rows, 1)
		require.Equal(t, visible.ID, rows[0].ID)
		require.Equal(t, "tier1_vector", rows[0].RankingExplanation.SourceTier)
		require.Empty(t, recall(t, map[string]any{"query": issue520Paraphrase, "tags": []string{"absent-synthetic-tag"}}))
	})
	t.Run("current_mcp_fts_degradation_and_explicit_principal_control", func(t *testing.T) {
		mcpServer.SetEmbeddingStores(nil, embeddingStore)
		defer mcpServer.SetEmbeddingStores(embedder, embeddingStore)
		rows := recall(t, map[string]any{"query": "ClaudeReset", "limit": 1, "explain": true})
		require.Len(t, rows, 1)
		require.Equal(t, visible.ID, rows[0].ID)
		require.Equal(t, "tier1_fts", rows[0].RankingExplanation.SourceTier)
		widened := recall(t, map[string]any{
			"query": "ClaudeReset", "limit": 10,
			"include_principals": []map[string]string{{"principal": bob.Principal, "principal_kind": "agent"}},
		})
		got := make([]int64, 0, len(widened))
		for _, row := range widened {
			got = append(got, row.ID)
		}
		require.Equal(t, []int64{visible.ID}, got, "non-admin explicit inclusion must not reveal another principal's private row")
		// All three successful MCP recalls update only the returned visible row.
		// Await their real final consumer before the shared store/schema cleanup.
		require.Eventually(t, func() bool {
			row, err := memoryStore.Get(t.Context(), visible.ID)
			return err == nil && row != nil && row.AccessCount == 3
		}, 3*time.Second, 10*time.Millisecond)
		unreturned, err := memoryStore.Get(t.Context(), hidden.ID)
		require.NoError(t, err)
		require.Zero(t, unreturned.AccessCount, "invisible candidates must not acquire MCP lifecycle effects")
	})
	t.Run("current_context_inject_visibility_and_rules_control", func(t *testing.T) {
		var result struct {
			Observations []issue520Observation `json:"observations"`
			Recent       []issue520Observation `json:"recent"`
			Relevant     []issue520Observation `json:"relevant"`
			Guidance     []issue520Observation `json:"guidance"`
			AlwaysInject []issue520Observation `json:"always_inject"`
		}
		call(t, aliceToken, http.MethodPost, "/api/context/inject?format=compact",
			map[string]any{"project": project, "session_id": "unrelated-session", "agent_id": bob.Principal}, http.StatusOK, &result)
		require.Contains(t, ids(result.Observations), visible.ID)
		for _, group := range [][]issue520Observation{result.Observations, result.Recent, result.Relevant} {
			for _, row := range group {
				if row.Type == string(models.ObsTypeGuidance) {
					continue
				}
				require.NotEqual(t, hidden.ID, row.ID)
				require.NotEqual(t, foreign.ID, row.ID)
			}
		}
		require.ElementsMatch(t, []int64{globalRule.ID, projectRule.ID}, ids(result.Guidance))
		require.Empty(t, result.AlwaysInject, "guidance rules must not be repeated in always_inject")
	})
}
