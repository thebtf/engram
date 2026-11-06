package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/docs"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

func TestB1RejectedTypedWriteAudit(t *testing.T) {
	f := newOwnCardContextFixture(t, false)
	owner := f.registered(t, "reject-writer")
	domains := gormdb.NewDomainOwnerStore(f.store)
	f.service.domainRegistryService = principalmemory.NewDomainRegistryService(domains, f.service.auditStore)
	domain := "reject-" + uuid.NewString()
	_, err := domains.Upsert(t.Context(), &gormdb.DomainOwner{Domain: domain, OwnerPrincipal: "agent/another-owner", OwnerPrincipalKind: "agent", Mode: gormdb.DomainOwnerModeReject})
	require.NoError(t, err)
	payload := ownCardContextPayload("rejected typed write")
	payload["domain"] = domain
	payload["request_key"] = "reject-" + uuid.NewString()
	before := f.memoryCount(t)
	rec := f.call(t, http.MethodPost, "/api/memories", payload, owner.caller())
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, before, f.memoryCount(t), "rejected write must not create a memory")
	var receipts, audits int64
	require.NoError(t, f.store.DB.Table("memory_request_keys").Where("request_key = ?", payload["request_key"]).Count(&receipts).Error)
	assert.Zero(t, receipts, "rejected write must not reserve a retry receipt")
	require.NoError(t, f.store.DB.Table("audit_log").Where("action = ? AND actor = ? AND reason = ?", principalmemory.AuditActionDomainWriteReject, owner.identity.Principal, principalmemory.AuditReasonDomainCrossOwnerReject).Count(&audits).Error)
	assert.EqualValues(t, 1, audits, "rejected typed write must retain its audit")
}

func TestB1TypedDomainAuditFailureRollsBack(t *testing.T) {
	f := newOwnCardContextFixture(t, false)
	owner := f.registered(t, "audit-failure")
	domains := gormdb.NewDomainOwnerStore(f.store)
	f.service.domainRegistryService = principalmemory.NewDomainRegistryService(domains, f.service.auditStore)
	domain := "audit-failure-" + uuid.NewString()
	_, err := domains.Upsert(t.Context(), &gormdb.DomainOwner{Domain: domain, OwnerPrincipal: "agent/another-owner", OwnerPrincipalKind: "agent", Mode: gormdb.DomainOwnerModeReject})
	require.NoError(t, err)
	name := "b1_audit_reject_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	t.Cleanup(func() {
		assert.NoError(t, f.store.DB.Exec("DROP TRIGGER IF EXISTS "+name+" ON audit_log").Error)
		assert.NoError(t, f.store.DB.Exec("DROP FUNCTION IF EXISTS "+name+"()").Error)
	})
	require.NoError(t, f.store.DB.Exec("CREATE FUNCTION "+name+"() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit failure'; END; $$").Error)
	require.NoError(t, f.store.DB.Exec("CREATE TRIGGER "+name+" BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION "+name+"()").Error)
	payload := ownCardContextPayload("must not persist")
	payload["domain"] = domain
	payload["request_key"] = "audit-failure-" + uuid.NewString()
	before := f.memoryCount(t)
	rec := f.call(t, http.MethodPost, "/api/memories", payload, owner.caller())
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, before, f.memoryCount(t))
	var receipts int64
	require.NoError(t, f.store.DB.Table("memory_request_keys").Where("request_key = ?", payload["request_key"]).Count(&receipts).Error)
	assert.Zero(t, receipts)
}

func TestB1LegacyTypedPatchRefused(t *testing.T) {
	f := newOwnCardContextFixture(t, false)
	owner := f.registered(t, "legacy-history")
	project := f.project(t)
	payload := ownCardContextPayload("legacy original")
	payload["project"] = project
	created := ownCardContextDecode(t, f.call(t, http.MethodPost, "/api/memories", payload, ownCardContextCaller{token: owner.token}), http.StatusCreated)
	var row gormdb.Memory
	require.NoError(t, f.store.DB.First(&row, created.ID).Error)
	require.Equal(t, models.MemoryContextLegacy, row.ContextKind)
	require.Equal(t, owner.identity.Principal, row.OwnerPrincipal)
	path := "/api/memories/" + strconv.FormatInt(created.ID, 10) + "?project=" + project
	patch := f.call(t, http.MethodPatch, path, map[string]any{"version": created.Version, "content": "typed partial history"}, owner.caller())
	assert.Equal(t, http.StatusNotFound, patch.Code, "typed PATCH must conceal legacy cards")
	var current gormdb.Memory
	require.NoError(t, f.store.DB.First(&current, created.ID).Error)
	assert.Equal(t, created.Content, current.Content, "refusal must not mutate the body")
	assert.Equal(t, created.Version, current.Version, "refusal must not advance the version")
	assert.Equal(t, models.MemoryContextLegacy, current.ContextKind, "refusal must not change the legacy context")
	var history int64
	require.NoError(t, f.store.DB.Table("memory_revisions").Where("memory_id = ?", created.ID).Count(&history).Error)
	assert.Zero(t, history, "legacy refusal must not create partial history")
	updated, err := gormdb.NewMemoryStore(f.store).Update(t.Context(), &models.Memory{ID: created.ID, Content: "legacy writer remains usable"})
	require.NoError(t, err)
	assert.Equal(t, "legacy writer remains usable", updated.Content)
}

func b1ProjectCard(t *testing.T, f *ownCardContextFixture, content string) (ownCardContextCredential, ownCardContextRecord, string) {
	t.Helper()
	owner := f.registered(t, "embedding")
	project := f.project(t)
	payload := ownCardContextPayload(content)
	payload["project"] = project
	payload["context"] = map[string]any{"kind": "project"}
	return owner, f.create(t, owner, payload), project
}

func b1Vector(index int) []float32 {
	v := make([]float32, embedding.EmbeddingDim)
	v[index] = 1
	return v
}

func b1Patch(t *testing.T, f *ownCardContextFixture, owner ownCardContextCredential, card ownCardContextRecord, project, content string) {
	t.Helper()
	path := "/api/memories/" + strconv.FormatInt(card.ID, 10) + "?project=" + project
	rec := f.call(t, http.MethodPatch, path, map[string]any{"version": card.Version, "content": content}, owner.caller())
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestB1RevisionInvalidatesVectorAndBackfills(t *testing.T) {
	f := newOwnCardContextFixture(t, false)
	owner, card, project := b1ProjectCard(t, f, "old embedded body")
	store := embedding.NewStore(f.store.DB)
	oldVec, newVec := b1Vector(0), b1Vector(1)
	require.NoError(t, store.StoreChunks(t.Context(), []embedding.Chunk{{MemoryID: card.ID, Text: card.Content, Embedding: pgvector.NewVector(oldVec), Model: "synthetic"}}))
	before, err := store.FindSimilarForProject(t.Context(), project, oldVec, 10, .9)
	require.NoError(t, err)
	require.Len(t, before, 1)
	b1Patch(t, f, owner, card, project, "new embedded body")
	after, err := store.FindSimilarForProject(t.Context(), project, oldVec, 10, .9)
	require.NoError(t, err)
	assert.Empty(t, after, "old vector must not be returned after revision")
	var candidate bool
	require.NoError(t, f.store.DB.Raw(`SELECT EXISTS (SELECT 1 FROM memories m LEFT JOIN content_chunks c ON c.memory_id=m.id WHERE m.id=? AND m.deleted_at IS NULL AND c.id IS NULL)`, card.ID).Scan(&candidate).Error)
	assert.True(t, candidate, "new body must be eligible for the existing backfill")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Input []string `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		data := make([]map[string]any, len(input.Input))
		for i := range input.Input {
			data[i] = map[string]any{"index": i, "embedding": newVec}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	t.Setenv("ENGRAM_EMBEDDING_URL", server.URL)
	client, err := embedding.NewClient()
	require.NoError(t, err)
	require.NoError(t, embedding.Backfill(t.Context(), f.store.DB, client, store, 10, nil))
	found, err := store.FindSimilarForProject(t.Context(), project, newVec, 10, .9)
	require.NoError(t, err)
	assert.Len(t, found, 1, "re-embedding must make the new body searchable")
	if len(found) == 1 {
		assert.Equal(t, "new embedded body", found[0].Text)
	}
}

func TestB1VersionBoundChunkInstallation(t *testing.T) {
	f := newOwnCardContextFixture(t, false)
	owner, card, project := b1ProjectCard(t, f, "old complete body")
	store := embedding.NewStore(f.store.DB)
	stored, err := store.StoreChunksForVersion(t.Context(), card.ID, card.Version, nil)
	require.NoError(t, err)
	assert.False(t, stored, "empty chunks must not report an installation")
	invalidChunk := embedding.Chunk{MemoryID: card.ID, Text: "invalid dimension", Model: "synthetic", Embedding: pgvector.NewVector([]float32{1})}
	stored, err = store.StoreChunksForVersion(t.Context(), card.ID, card.Version, []embedding.Chunk{invalidChunk})
	require.NoError(t, err)
	assert.False(t, stored, "invalid dimensions must not report an installation")
	oldChunk := embedding.Chunk{MemoryID: card.ID, Text: "old partial", Model: "synthetic", Embedding: pgvector.NewVector(b1Vector(0))}
	stored, err = store.StoreChunksForVersion(t.Context(), card.ID, card.Version, []embedding.Chunk{oldChunk})
	require.NoError(t, err)
	require.True(t, stored)
	b1Patch(t, f, owner, card, project, "new complete body")
	stored, err = store.StoreChunksForVersion(t.Context(), card.ID, card.Version, []embedding.Chunk{oldChunk})
	require.NoError(t, err)
	assert.False(t, stored)
	old, err := store.FindSimilarForProject(t.Context(), project, b1Vector(0), 10, .9)
	require.NoError(t, err)
	assert.Empty(t, old)
	newChunk := embedding.Chunk{MemoryID: card.ID, Text: "new partial", Model: "synthetic", Embedding: pgvector.NewVector(b1Vector(1))}
	stored, err = store.StoreChunksForVersion(t.Context(), card.ID, card.Version+1, []embedding.Chunk{newChunk})
	require.NoError(t, err)
	assert.True(t, stored)
}

func TestB1DelayedBackfillCannotReinstallOldVector(t *testing.T) {
	f := newOwnCardContextFixture(t, false)
	owner, card, project := b1ProjectCard(t, f, "old delayed body")
	oldVec, newVec := b1Vector(0), b1Vector(1)
	entered := make(chan string, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Input []string `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Input) != 1 {
			http.Error(w, "invalid", 400)
			return
		}
		body := input.Input[0]
		entered <- body
		vec := newVec
		if body == "old delayed body" {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			vec = oldVec
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vec}}})
	}))
	defer server.Close()
	t.Setenv("ENGRAM_EMBEDDING_URL", server.URL)
	client, err := embedding.NewClient()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	store := embedding.NewStore(f.store.DB)
	done := make(chan error, 1)
	go func() { done <- embedding.Backfill(ctx, f.store.DB, client, store, 1, nil) }()
	select {
	case body := <-entered:
		require.Equal(t, card.Content, body)
	case <-ctx.Done():
		t.Fatal("backfill did not reach provider")
	}
	b1Patch(t, f, owner, card, project, "new delayed body")
	close(release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("backfill did not finish")
	}
	old, err := store.FindSimilarForProject(t.Context(), project, oldVec, 10, .9)
	require.NoError(t, err)
	assert.Empty(t, old, "late old backfill must not reintroduce stale vector")
	fresh, err := store.FindSimilarForProject(t.Context(), project, newVec, 10, .9)
	require.NoError(t, err)
	assert.Len(t, fresh, 1, "backfill must embed the current body")
	if len(fresh) == 1 {
		assert.Equal(t, "new delayed body", fresh[0].Text)
	}
}

func TestB1EnrollmentSwaggerDoesNotRequireAPIKey(t *testing.T) {
	published, err := os.ReadFile("../../docs/swagger.json")
	require.NoError(t, err)
	for name, raw := range map[string][]byte{"served": []byte(docs.SwaggerInfo.ReadDoc()), "published": published} {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			require.NoError(t, json.Unmarshal(raw, &doc))
			paths := doc["paths"].(map[string]any)
			post := paths["/api/auth/principals"].(map[string]any)["post"].(map[string]any)
			security, exists := post["security"]
			if !exists {
				security = doc["security"]
			}
			if requirements, ok := security.([]any); ok {
				for _, requirement := range requirements {
					assert.NotContains(t, requirement.(map[string]any), "ApiKeyAuth", "enrollment needs a browser-admin cookie, not the API key scheme")
				}
			}
		})
	}
}

// The maintenance transaction is a lock-only stand-in for consolidation's
// sorted EXCLUSIVE relation locks; the competing operation is the real B1 path.
func TestB1ConsolidationRelationLockOrderPostgres(t *testing.T) {
	for _, tc := range []struct {
		name, relation string
		revise         bool
	}{
		{name: "embedding_content_chunks", relation: "content_chunks"},
		{name: "revision_content_chunks", relation: "content_chunks", revise: true},
		{name: "revision_audit_log", relation: "audit_log", revise: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOwnCardContextFixture(t, false)
			owner := f.registered(t, "relation-lock")
			card := f.create(t, owner, ownCardContextPayload("relation lock original"))
			pool, err := f.store.DB.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(5)
			var schema string
			require.NoError(t, f.store.DB.Raw("SELECT current_schema()").Scan(&schema).Error)
			t.Logf("owned_schema=%s", schema)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			tx := f.store.DB.WithContext(ctx).Begin()
			require.NoError(t, tx.Error)
			var workers sync.WaitGroup
			defer func() {
				cancel()
				_ = tx.Rollback().Error
				workers.Wait()
			}()
			var maintenancePID int
			require.NoError(t, tx.Raw("SELECT pg_backend_pid()").Scan(&maintenancePID).Error)
			require.NoError(t, tx.Exec("SET LOCAL lock_timeout='5s'").Error)
			require.NoError(t, tx.Exec("LOCK TABLE "+tc.relation+" IN EXCLUSIVE MODE").Error)

			operationDone := make(chan error, 1)
			workers.Add(1)
			go func() {
				defer workers.Done()
				if tc.revise {
					caller := scope.KeycardContext{Principal: owner.identity.Principal, PrincipalKind: owner.identity.PrincipalKind, WorkstationID: owner.id}
					owning := models.MemoryContext{Kind: models.MemoryContextSelf, Principal: caller.Principal, PrincipalKind: caller.PrincipalKind}
					_, err := f.service.memoryStore.ReviseInContext(ctx, card.ID, owning, "", caller, card.Version, "relation lock revised")
					operationDone <- err
					return
				}
				chunk := embedding.Chunk{MemoryID: card.ID, Text: card.Content, Model: "synthetic", Embedding: pgvector.NewVector(b1Vector(0))}
				stored, err := embedding.NewStore(f.store.DB).StoreChunksForVersion(ctx, card.ID, card.Version, []embedding.Chunk{chunk})
				if err == nil && !stored {
					err = fmt.Errorf("current embedding was not stored")
				}
				operationDone <- err
			}()

			// Wait for the actual operation to block on the maintenance relation.
			var operationPID int
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
		waitForBlock:
			for {
				err := f.store.DB.WithContext(ctx).Raw(`SELECT COALESCE((SELECT a.pid FROM pg_stat_activity a JOIN pg_locks l ON l.pid = a.pid
					WHERE a.datname = current_database() AND a.wait_event_type = 'Lock' AND ? = ANY(pg_blocking_pids(a.pid))
					AND l.locktype = 'relation' AND NOT l.granted AND l.relation = to_regclass(?) LIMIT 1), 0)`, maintenancePID, tc.relation).Scan(&operationPID).Error
				require.NoError(t, err, "INVALID: lock observer")
				if operationPID != 0 {
					break waitForBlock
				}
				select {
				case err := <-operationDone:
					t.Fatalf("INVALID: operation finished before observed lock wait: %v", err)
				case <-deadline.C:
					t.Fatal("INVALID: no operation wait on maintenance relation")
				case <-ctx.Done():
					t.Fatal("INVALID: deadline observing relation lock")
				case <-ticker.C:
				}
			}
			t.Logf("observed operation_pid=%d blocked_by=%d relation=%s", operationPID, maintenancePID, tc.relation)
			maintenanceDone := make(chan error, 1)
			workers.Add(1)
			go func() {
				defer workers.Done()
				if err := tx.Exec("LOCK TABLE memories IN EXCLUSIVE MODE").Error; err != nil {
					_ = tx.Rollback().Error
					maintenanceDone <- err
					return
				}
				maintenanceDone <- tx.Commit().Error
			}()
			var operationErr, maintenanceErr error
			select {
			case operationErr = <-operationDone:
			case <-ctx.Done():
				t.Fatal("INVALID: operation completion timed out")
			}
			select {
			case maintenanceErr = <-maintenanceDone:
			case <-ctx.Done():
				t.Fatal("INVALID: maintenance completion timed out")
			}
			for label, err := range map[string]error{"operation": operationErr, "maintenance": maintenanceErr} {
				if err == nil {
					continue
				}
				var state interface{ SQLState() string }
				if errors.As(err, &state) && state.SQLState() == "40P01" {
					t.Errorf("RED: %s deadlocked with consolidation relation locks (SQLSTATE 40P01)", label)
				} else {
					t.Errorf("INVALID: %s failed without proven deadlock: %v", label, err)
				}
			}
			if operationErr != nil || maintenanceErr != nil {
				return
			}
			if tc.revise {
				path := "/api/memories/" + strconv.FormatInt(card.ID, 10)
				current := ownCardContextDecode(t, f.call(t, http.MethodGet, path, nil, owner.caller()), http.StatusOK)
				assert.Equal(t, "relation lock revised", current.Content)
				assert.Equal(t, card.Version+1, current.Version)
			} else {
				var count int64
				require.NoError(t, f.store.DB.Table("content_chunks").Where("memory_id = ?", card.ID).Count(&count).Error)
				assert.EqualValues(t, 1, count)
			}
		})
	}
}
