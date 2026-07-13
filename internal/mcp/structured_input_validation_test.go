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
	server        *Server
	store         *gormdb.Store
	rules         *fakeRuleGovernanceStore
	significance  *fakeMemorySignificanceUpdater
	editor        *mockMemoryEditor
	ctx           context.Context
	marker        string
	documentID    int64
	suppressID    int64
	ragCollection string
	ragPath       string
	ragHash       string
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
	documentID, err := versionedStore.Create(
		context.Background(), marker+".md", marker, "fixture", "markdown", "{}", marker,
	)
	require.NoError(t, err)
	documentStore := gormdb.NewDocumentStore(store)
	ragCollection := marker + "-collection"
	ragPath := marker + ".txt"
	ragDocument, err := documentStore.UpsertDocument(context.Background(), ragCollection, ragPath, marker, marker+"-body")
	require.NoError(t, err)
	require.True(t, ragDocument.Hash.Valid)

	rules := &fakeRuleGovernanceStore{}
	significance := &fakeMemorySignificanceUpdater{}
	editor := newMockMemoryEditor()
	editor.seed(&models.Memory{ID: 42, Project: marker, Content: "before"})
	memoryStore := gormdb.NewMemoryStore(store)
	suppressFixture, err := memoryStore.Create(context.Background(), &models.Memory{
		Project: marker, Content: "suppress-fixture-" + marker, SourceAgent: "mb1-strict",
	})
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

	ctx := auth.WithIdentity(context.Background(), auth.Admin())
	ctx = ContextWithProject(ctx, marker)
	ctx = ContextWithSession(ctx, marker)

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

	return &structuredMutationHarness{
		server:        server,
		store:         store,
		rules:         rules,
		significance:  significance,
		editor:        editor,
		ctx:           ctx,
		marker:        marker,
		documentID:    documentID,
		suppressID:    suppressFixture.ID,
		ragCollection: ragCollection,
		ragPath:       ragPath,
		ragHash:       ragDocument.Hash.String,
	}
}

func (h *structuredMutationHarness) requireZeroDurableDelta(t *testing.T) {
	t.Helper()
	checks := []struct {
		name  string
		query string
		args  []any
	}{
		{name: "memories", query: "SELECT count(*) FROM memories WHERE content LIKE ?", args: []any{h.marker + "%"}},
		{name: "audit", query: "SELECT count(*) FROM audit_log WHERE actor = ? OR source_session_id = ?", args: []any{h.marker, h.marker}},
		{name: "snapshots", query: "SELECT count(*) FROM bulk_op_snapshots WHERE actor = ? OR source_session_id = ?", args: []any{h.marker, h.marker}},
		{name: "settings", query: "SELECT count(*) FROM model_settings WHERE key LIKE ?", args: []any{h.marker + "%"}},
		{name: "comments", query: "SELECT count(*) FROM versioned_document_comments WHERE document_id = ?", args: []any{h.documentID}},
	}
	for _, check := range checks {
		var count int64
		require.NoError(t, h.store.DB.Raw(check.query, check.args...).Scan(&count).Error, check.name)
		require.Zero(t, count, "%s changed after malformed mutation input", check.name)
	}
	var activeSuppressFixture int64
	require.NoError(t, h.store.DB.Raw(
		"SELECT count(*) FROM memories WHERE id = ? AND deleted_at IS NULL", h.suppressID,
	).Scan(&activeSuppressFixture).Error)
	require.EqualValues(t, 1, activeSuppressFixture, "malformed suppress selector deleted the durable row")
	var activeDocument int64
	require.NoError(t, h.store.DB.Raw(
		"SELECT count(*) FROM documents WHERE collection = ? AND path = ? AND active = true AND hash = ?",
		h.ragCollection, h.ragPath, h.ragHash,
	).Scan(&activeDocument).Error)
	require.EqualValues(t, 1, activeDocument, "malformed document mutation changed the durable document")
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
		if tool.Name != toolName {
			continue
		}
		properties, ok := tool.InputSchema["properties"].(map[string]any)
		require.True(t, ok, "%s schema properties", toolName)
		field, ok := properties[property].(map[string]any)
		require.True(t, ok, "%s.%s schema", toolName, property)
		require.Equal(t, wantType, field["type"], "%s.%s handler/schema type drift", toolName, property)
		return
	}
	t.Fatalf("mutation tool %s was not advertised by the fully wired server", toolName)
}

func TestStructuredMutationSchemasMatchStrictHandlers(t *testing.T) {
	h := newStructuredMutationHarness(t)
	tools := h.server.ListTools()
	for _, expected := range []struct {
		tool     string
		property string
		typeName string
	}{
		{tool: "promote_candidate", property: "id", typeName: "integer"},
		{tool: "promote_candidate", property: "dry_run", typeName: "boolean"},
		{tool: "store_memory", property: "tags", typeName: "array"},
		{tool: "store_memory", property: "supersedes", typeName: "array"},
		{tool: "store_memory", property: "dry_run", typeName: "boolean"},
		{tool: "store", property: "id", typeName: "integer"},
		{tool: "store", property: "supersedes", typeName: "array"},
		{tool: "store", property: "dry_run", typeName: "boolean"},
		{tool: "settings", property: "encrypt", typeName: "boolean"},
		{tool: "doc_comment", property: "document_id", typeName: "integer"},
		{tool: "doc_comment", property: "line_start", typeName: "integer"},
		{tool: "doc_comment", property: "line_end", typeName: "integer"},
		{tool: "remove_document", property: "collection", typeName: "string"},
		{tool: "remove_document", property: "path", typeName: "string"},
		{tool: "ingest_document", property: "collection", typeName: "string"},
		{tool: "ingest_document", property: "path", typeName: "string"},
		{tool: "ingest_document", property: "content", typeName: "string"},
		{tool: "ingest_document", property: "title", typeName: "string"},
		{tool: "rule_governance_transition", property: "rule_version_id", typeName: "integer"},
		{tool: "rule_governance_pin_snapshot", property: "pinned", typeName: "boolean"},
		{tool: "rate_memory_significance", property: "id", typeName: "integer"},
	} {
		requireMutationSchemaType(t, tools, expected.tool, expected.property, expected.typeName)
	}
}

func TestStructuredMutationInputsRejectWithoutDurableDelta(t *testing.T) {
	h := newStructuredMutationHarness(t)
	cases := []struct {
		name string
		call func() (string, error)
	}{
		{
			name: "promote candidate string dry run",
			call: func() (string, error) {
				return h.server.handlePromoteCandidate(h.ctx, json.RawMessage(`{"id":9007199254740993,"dry_run":"false"}`))
			},
		},
		{
			name: "store memory string dry run",
			call: func() (string, error) {
				return h.server.handleStoreMemory(h.ctx, json.RawMessage(fmt.Sprintf(`{"content":%q,"project":%q,"dry_run":"false"}`, h.marker+"-dry-run", h.marker)))
			},
		},
		{
			name: "store alias mixed tags",
			call: func() (string, error) {
				return h.server.handleStoreConsolidated(h.ctx, json.RawMessage(fmt.Sprintf(`{"action":"create","content":%q,"project":%q,"tags":["safe",7]}`, h.marker+"-tags", h.marker)))
			},
		},
		{
			name: "store memory mixed supersedes",
			call: func() (string, error) {
				return h.server.handleStoreMemory(h.ctx, json.RawMessage(fmt.Sprintf(`{"content":%q,"project":%q,"supersedes":[1,"2"]}`, h.marker+"-supersedes", h.marker)))
			},
		},
		{
			name: "edit alias mixed tags",
			call: func() (string, error) {
				return h.server.handleStoreConsolidated(h.ctx, json.RawMessage(`{"action":"edit","id":42,"narrative":"after","tags":["safe",7]}`))
			},
		},
		{
			name: "suppress numeric string selector",
			call: func() (string, error) {
				return h.server.handleSuppressMemory(h.ctx, json.RawMessage(fmt.Sprintf(`{"id":"%d"}`, h.suppressID)))
			},
		},
		{
			name: "settings string encrypt",
			call: func() (string, error) {
				return h.server.handleSettingsConsolidated(h.ctx, json.RawMessage(fmt.Sprintf(`{"action":"set","key":%q,"value":"secret","encrypt":"false"}`, h.marker+".setting")))
			},
		},
		{
			name: "document comment numeric string line",
			call: func() (string, error) {
				return h.server.handleDocComment(h.ctx, json.RawMessage(fmt.Sprintf(`{"document_id":%d,"content":%q,"author":%q,"line_start":"1"}`, h.documentID, h.marker+"-comment", h.marker)))
			},
		},
		{
			name: "document remove coerced collection and path",
			call: func() (string, error) {
				return h.server.handleRemoveDocument(h.ctx, json.RawMessage(`{"collection":123,"path":true}`))
			},
		},
		{
			name: "document ingest numeric collection",
			call: func() (string, error) {
				return h.server.handleIngestDocument(h.ctx, json.RawMessage(fmt.Sprintf(`{"collection":123,"path":%q,"content":"replacement","title":"title"}`, h.ragPath)))
			},
		},
		{
			name: "document ingest boolean path",
			call: func() (string, error) {
				return h.server.handleIngestDocument(h.ctx, json.RawMessage(fmt.Sprintf(`{"collection":%q,"path":true,"content":"replacement","title":"title"}`, h.ragCollection)))
			},
		},
		{
			name: "document ingest numeric content",
			call: func() (string, error) {
				return h.server.handleIngestDocument(h.ctx, json.RawMessage(fmt.Sprintf(`{"collection":%q,"path":%q,"content":123,"title":"title"}`, h.ragCollection, h.ragPath)))
			},
		},
		{
			name: "document ingest boolean title",
			call: func() (string, error) {
				return h.server.handleIngestDocument(h.ctx, json.RawMessage(fmt.Sprintf(`{"collection":%q,"path":%q,"content":"replacement","title":true}`, h.ragCollection, h.ragPath)))
			},
		},
		{
			name: "rule transition fractional selector",
			call: func() (string, error) {
				return h.server.handleRuleGovernanceTransition(h.ctx, json.RawMessage(`{"rule_version_id":7.5,"to_state":"active_project","actor":"operator","actor_kind":"operator","reason":"strict","evidence_handles":[]}`))
			},
		},
		{
			name: "rule pin string boolean",
			call: func() (string, error) {
				return h.server.handleRuleGovernancePinSnapshot(h.ctx, json.RawMessage(`{"snapshot_id":"rg-snap","pinned":"false"}`))
			},
		},
		{
			name: "significance numeric string selector",
			call: func() (string, error) {
				return h.server.handleRateMemorySignificance(h.ctx, json.RawMessage(`{"id":"42","rating":"useful"}`))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.call()
			require.Error(t, err)
			require.Empty(t, out)
		})
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
	for i := 0; i < repeats; i++ {
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
