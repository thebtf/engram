package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	"gorm.io/gorm/logger"
)

type structuredMutationHarness struct {
	server                          *Server
	store                           *gormdb.Store
	rules                           *fakeRuleGovernanceStore
	significance                    *fakeMemorySignificanceUpdater
	editor                          *mockMemoryEditor
	ctx                             context.Context
	marker                          string
	documentID                      int64
	suppressID                      int64
	ragCollection, ragPath, ragHash string
}

func newStructuredMutationHarness(t *testing.T) *structuredMutationHarness {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set; structured mutation durable-delta proof requires PostgreSQL")
	}
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S6_OUTCOME", "true")
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, MaxConns: 4, LogLevel: logger.Silent})
	require.NoError(t, err)
	marker := fmt.Sprintf("mb1-strict-%d", time.Now().UnixNano())
	auditStore := gormdb.NewAuditStore(store.DB)
	versionedStore := gormdb.NewVersionedDocumentStore(store)
	documentID, err := versionedStore.Create(context.Background(), marker+".md", marker, "fixture", "markdown", "{}", marker)
	require.NoError(t, err)
	documentStore := gormdb.NewDocumentStore(store)
	ragCollection, ragPath := marker+"-collection", marker+".txt"
	ragDocument, err := documentStore.UpsertDocument(context.Background(), ragCollection, ragPath, marker, marker+"-body")
	require.NoError(t, err)
	require.True(t, ragDocument.Hash.Valid)
	rules := &fakeRuleGovernanceStore{}
	significance := &fakeMemorySignificanceUpdater{}
	editor := newMockMemoryEditor()
	editor.seed(&models.Memory{ID: 42, Project: marker, Content: "before"})
	memoryStore := gormdb.NewMemoryStore(store)
	suppressFixture, err := memoryStore.Create(context.Background(), &models.Memory{Project: marker, Content: "suppress-fixture-" + marker, SourceAgent: "mb1-strict"})
	require.NoError(t, err)
	server := NewServer(ServerOptions{Version: "mb1-structured-input", DocumentStore: documentStore})
	server.SetMemoryStore(memoryStore)
	server.SetAuditStore(auditStore)
	server.SetCandidateStore(gormdb.NewCandidateStore(store.DB, auditStore))
	server.SetSnapshotStore(gormdb.NewSnapshotStore(store.DB))
	server.SetVersionedDocumentStore(versionedStore)
	server.SetSettingsStore(gormdb.NewSettingsStore(store))
	server.SetRuleGovernanceStore(rules)
	server.setTestMemoryEditor(editor)
	server.setTestMemorySignificanceUpdater(significance)
	ctx := ContextWithSession(ContextWithProject(auth.WithIdentity(context.Background(), auth.Admin()), marker), marker)
	t.Cleanup(func() {
		_ = store.DB.Exec("DELETE FROM versioned_document_comments WHERE document_id = ?", documentID).Error
		_ = store.DB.Exec("DELETE FROM versioned_documents WHERE id = ?", documentID).Error
		_ = store.DB.Exec("DELETE FROM documents WHERE collection = ? AND path = ?", ragCollection, ragPath).Error
		_ = store.DB.Exec("DELETE FROM content WHERE hash = ?", ragDocument.Hash.String).Error
		_ = store.DB.Unscoped().Exec("DELETE FROM memories WHERE project = ? OR content LIKE ?", marker, marker+"%").Error
		_ = store.DB.Exec("DELETE FROM audit_log WHERE actor = ? OR source_session_id = ?", marker, marker).Error
		_ = store.DB.Exec("DELETE FROM bulk_op_snapshots WHERE actor = ? OR source_session_id = ?", marker, marker).Error
		_ = store.DB.Exec("DELETE FROM model_settings WHERE key LIKE ?", marker+"%").Error
		require.NoError(t, store.Close())
	})
	return &structuredMutationHarness{server: server, store: store, rules: rules, significance: significance, editor: editor, ctx: ctx, marker: marker, documentID: documentID, suppressID: suppressFixture.ID, ragCollection: ragCollection, ragPath: ragPath, ragHash: ragDocument.Hash.String}
}

func (h *structuredMutationHarness) requireZeroDurableDelta(t *testing.T) {
	t.Helper()
	for _, check := range []struct {
		name, query string
		args        []any
	}{
		{"memories", "SELECT count(*) FROM memories WHERE content LIKE ?", []any{h.marker + "%"}},
		{"audit", "SELECT count(*) FROM audit_log WHERE actor = ? OR source_session_id = ?", []any{h.marker, h.marker}},
		{"snapshots", "SELECT count(*) FROM bulk_op_snapshots WHERE actor = ? OR source_session_id = ?", []any{h.marker, h.marker}},
		{"settings", "SELECT count(*) FROM model_settings WHERE key LIKE ?", []any{h.marker + "%"}},
		{"comments", "SELECT count(*) FROM versioned_document_comments WHERE document_id = ?", []any{h.documentID}},
	} {
		var count int64
		require.NoError(t, h.store.DB.Raw(check.query, check.args...).Scan(&count).Error, check.name)
		require.Zero(t, count, "%s changed after malformed mutation input", check.name)
	}
	var active int64
	require.NoError(t, h.store.DB.Raw("SELECT count(*) FROM memories WHERE id = ? AND deleted_at IS NULL", h.suppressID).Scan(&active).Error)
	require.EqualValues(t, 1, active)
	require.NoError(t, h.store.DB.Raw("SELECT count(*) FROM documents WHERE collection = ? AND path = ? AND active = true AND hash = ?", h.ragCollection, h.ragPath, h.ragHash).Scan(&active).Error)
	require.EqualValues(t, 1, active)
	require.Zero(t, h.rules.transitionID)
	require.Empty(t, h.rules.pinSnapshotID)
	require.Empty(t, h.rules.rollbackID)
	require.Empty(t, h.significance.calls)
	require.Zero(t, h.editor.getCalls)
	require.Zero(t, h.editor.updates)
}

func requireMutationSchemaType(t *testing.T, tools []Tool, toolName, property, wantType string) {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == toolName {
			props, ok := tool.InputSchema["properties"].(map[string]any)
			require.True(t, ok)
			field, ok := props[property].(map[string]any)
			require.True(t, ok, "%s.%s must be advertised by the consolidated mutation schema", toolName, property)
			require.Equal(t, wantType, field["type"], "%s.%s handler/schema type drift", toolName, property)
			return
		}
	}
	t.Fatalf("mutation tool %s was not advertised by the fully wired server", toolName)
}

func TestStructuredMutationSchemasMatchStrictHandlers(t *testing.T) {
	h := newStructuredMutationHarness(t)
	for _, expected := range []struct{ tool, property, typeName string }{
		{"promote_candidate", "id", "integer"},
		{"promote_candidate", "dry_run", "boolean"},
		{"store", "id", "integer"},
		{"store", "tags", "array"},
		{"store", "supersedes", "array"},
		{"store", "dry_run", "boolean"},
		{"settings", "encrypt", "boolean"},
		{"docs", "document_id", "integer"},
		{"docs", "line_start", "integer"},
		{"docs", "line_end", "integer"},
		{"docs", "collection", "string"},
		{"docs", "path", "string"},
		{"docs", "content", "string"},
		{"docs", "title", "string"},
		{"rule_governance_transition", "rule_version_id", "integer"},
		{"rule_governance_pin_snapshot", "pinned", "boolean"},
		{"rate_memory_significance", "id", "integer"},
	} {
		requireMutationSchemaType(t, h.server.ListTools(), expected.tool, expected.property, expected.typeName)
	}
}

func TestStructuredMutationInputsRejectWithoutDurableDelta(t *testing.T) {
	h := newStructuredMutationHarness(t)
	cases := []func() (string, error){
		func() (string, error) {
			return h.server.handlePromoteCandidate(h.ctx, json.RawMessage(`{"id":9007199254740993,"dry_run":"false"}`))
		},
		func() (string, error) {
			return h.server.handleStoreMemory(h.ctx, json.RawMessage(fmt.Sprintf(`{"content":%q,"project":%q,"dry_run":"false"}`, h.marker+"-dry-run", h.marker)))
		},
		func() (string, error) {
			return h.server.handleStoreConsolidated(h.ctx, json.RawMessage(fmt.Sprintf(`{"action":"create","content":%q,"project":%q,"tags":["safe",7]}`, h.marker+"-tags", h.marker)))
		},
		func() (string, error) {
			return h.server.handleStoreMemory(h.ctx, json.RawMessage(fmt.Sprintf(`{"content":%q,"project":%q,"supersedes":[1,"2"]}`, h.marker+"-supersedes", h.marker)))
		},
		func() (string, error) {
			return h.server.handleStoreConsolidated(h.ctx, json.RawMessage(`{"action":"edit","id":42,"narrative":"after","tags":["safe",7]}`))
		},
		func() (string, error) {
			return h.server.handleSuppressMemory(h.ctx, json.RawMessage(fmt.Sprintf(`{"id":"%d"}`, h.suppressID)))
		},
		func() (string, error) {
			return h.server.handleSettingsConsolidated(h.ctx, json.RawMessage(fmt.Sprintf(`{"action":"set","key":%q,"value":"secret","encrypt":"false"}`, h.marker+".setting")))
		},
		func() (string, error) {
			return h.server.handleDocComment(h.ctx, json.RawMessage(fmt.Sprintf(`{"document_id":%d,"content":%q,"author":%q,"line_start":"1"}`, h.documentID, h.marker+"-comment", h.marker)))
		},
		func() (string, error) {
			return h.server.handleRemoveDocument(h.ctx, json.RawMessage(`{"collection":123,"path":true}`))
		},
		func() (string, error) {
			return h.server.handleIngestDocument(h.ctx, json.RawMessage(fmt.Sprintf(`{"collection":123,"path":%q,"content":"replacement","title":"title"}`, h.ragPath)))
		},
		func() (string, error) {
			return h.server.handleIngestDocument(h.ctx, json.RawMessage(fmt.Sprintf(`{"collection":%q,"path":true,"content":"replacement","title":"title"}`, h.ragCollection)))
		},
		func() (string, error) {
			return h.server.handleDocCreate(h.ctx, json.RawMessage(`{"path":7,"project":"strict","content":"bad"}`))
		},
		func() (string, error) {
			return h.server.handleIngestDocument(h.ctx, json.RawMessage(fmt.Sprintf(`{"collection":%q,"path":%q,"content":123,"title":"title"}`, h.ragCollection, h.ragPath)))
		},
		func() (string, error) {
			return h.server.handleIngestDocument(h.ctx, json.RawMessage(fmt.Sprintf(`{"collection":%q,"path":%q,"content":"replacement","title":true}`, h.ragCollection, h.ragPath)))
		},
		func() (string, error) {
			return h.server.handleRuleGovernanceTransition(h.ctx, json.RawMessage(`{"rule_version_id":7.5,"to_state":"active_project","actor":"operator","actor_kind":"operator","reason":"strict","evidence_handles":[]}`))
		},
		func() (string, error) {
			return h.server.handleRuleGovernancePinSnapshot(h.ctx, json.RawMessage(`{"snapshot_id":"rg-snap","pinned":"false"}`))
		},
		func() (string, error) {
			return h.server.handleRateMemorySignificance(h.ctx, json.RawMessage(`{"id":"42","rating":"useful"}`))
		},
	}
	for _, call := range cases {
		out, err := call()
		require.Error(t, err)
		require.Empty(t, out)
	}
	h.requireZeroDurableDelta(t)
}

func TestStructuredMutationInputsConcurrentRejectionHasZeroDurableDelta(t *testing.T) {
	h := newStructuredMutationHarness(t)
	malformed := []func() (string, error){
		func() (string, error) {
			return h.server.handleStoreMemory(h.ctx, json.RawMessage(fmt.Sprintf(`{"content":%q,"project":%q,"tags":["safe",7]}`, h.marker+"-concurrent-memory", h.marker)))
		},
		func() (string, error) {
			return h.server.handleSettingsConsolidated(h.ctx, json.RawMessage(fmt.Sprintf(`{"action":"set","key":%q,"value":"secret","encrypt":"false"}`, h.marker+".concurrent")))
		},
		func() (string, error) {
			return h.server.handleDocComment(h.ctx, json.RawMessage(fmt.Sprintf(`{"document_id":%d,"content":%q,"author":%q,"line_end":1.5}`, h.documentID, h.marker+"-concurrent-comment", h.marker)))
		},
	}
	const repeats = 12
	errCh := make(chan error, repeats*len(malformed))
	var wg sync.WaitGroup
	for range repeats {
		for _, call := range malformed {
			wg.Add(1)
			go func(call func() (string, error)) {
				defer wg.Done()
				out, err := call()
				if err == nil {
					errCh <- fmt.Errorf("malformed concurrent mutation returned success: %s", out)
				}
			}(call)
		}
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	h.requireZeroDurableDelta(t)
}
