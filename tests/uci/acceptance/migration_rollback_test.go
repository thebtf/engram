package acceptance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/protobuf/encoding/protojson"
	gormlib "gorm.io/gorm"
)

const (
	uciMigrationRollbackLegacyProject = "uci-migration-rollback-legacy-project"
	uciMigrationRollbackLegacyFile    = "legacy/migration_rollback.go"
	uciMigrationRollbackLegacySHA     = "uci-migration-rollback-legacy-sha"
	uciMigrationRollbackLegacyQuery   = "migration rollback"
)

// TestUCIMigrationRollbackLegacyOnlyAcceptance binds the application rollback
// contract to a current, migrated PostgreSQL schema. Legacy-only means exactly
// a raw CodeChunkStore with neither a scoped MCP application nor a scoped gRPC
// transport; it does not invent a checkout, View, or UCI evidence record.
func TestUCIMigrationRollbackLegacyOnlyAcceptance(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")

	store := openUCIRetrievalSliceStore(t)
	db := store.GetDB()
	uciMigrationRollbackRequireMigrations(t, db)

	legacyStore := gormstore.NewCodeChunkStore(db)
	if err := legacyStore.Upsert(context.Background(), &gormstore.CodeChunk{
		ProjectID:      uciMigrationRollbackLegacyProject,
		FilePath:       uciMigrationRollbackLegacyFile,
		ByteStart:      0,
		ByteEnd:        64,
		Language:       "go",
		ChunkType:      "function",
		Content:        "package legacy\n\n// migration rollback compatibility needle\nfunc LegacyMigrationRollback() {}\n",
		ContentSHA256:  uciMigrationRollbackLegacySHA,
		IndexSessionID: "uci-migration-rollback-before-cutover",
	}); err != nil {
		t.Fatalf("seed prior unscoped code chunk: %v", err)
	}

	beforeScopedRows := uciMigrationRollbackScopedRowCounts(t, db)

	t.Run("legacy-only MCP routes are public and visibly limited", func(t *testing.T) {
		server := mcp.NewServer(mcp.ServerOptions{Version: "uci-migration-rollback"})
		server.SetLegacyUnscopedCodeChunkStore(legacyStore)
		uciMigrationRollbackRequirePublicSearchTool(t, server)

		search := uciMigrationRollbackToolPayload(t, uciMigrationRollbackCall(t, server, "codebase_search", map[string]any{
			"project": uciMigrationRollbackLegacyProject,
			"query":   uciMigrationRollbackLegacyQuery,
			"limit":   1,
		}), "codebase_search")
		uciMigrationRollbackRequireLegacySearch(t, search)

		status := uciMigrationRollbackToolPayload(t, uciMigrationRollbackCall(t, server, "codebase_status", map[string]any{
			"project": uciMigrationRollbackLegacyProject,
		}), "codebase_status")
		uciMigrationRollbackRequireLegacyStatus(t, status)

		afterScopedRows := uciMigrationRollbackScopedRowCounts(t, db)
		uciMigrationRollbackRequireScopedRowsInert(t, beforeScopedRows, afterScopedRows)
	})

	t.Run("prior gRPC negotiation remains raw-project compatible without UCI transport", func(t *testing.T) {
		transportServer, server := grpcserver.New(nil, nil)
		defer transportServer.Stop()
		server.SetDB(db)
		server.SetUCITransport(nil)

		response, err := server.CodeIndexNegotiate(context.Background(), &pb.CodeIndexNegotiateRequest{
			ProjectId:      uciMigrationRollbackLegacyProject,
			IndexSessionId: "uci-migration-rollback-negotiate",
			Manifest: []*pb.CodeChunkMeta{{
				ChunkId:       "uci-migration-rollback-chunk",
				FilePath:      uciMigrationRollbackLegacyFile,
				ByteStart:     0,
				ByteEnd:       64,
				ContentSha256: uciMigrationRollbackLegacySHA,
			}},
		})
		if err != nil {
			t.Fatalf("negotiate prior unscoped index after migrations 171-175: %v", err)
		}
		if len(response.GetNeedChunks()) != 0 || len(response.GetStaleChunks()) != 0 {
			t.Fatalf("prior unscoped negotiation = %#v, want no delta for the retained legacy chunk", response)
		}
		encoded, err := protojson.Marshal(response)
		if err != nil {
			t.Fatalf("marshal prior negotiate response: %v", err)
		}
		for _, forbidden := range []string{"checkout", "view", "context", "source", "profile", "exposure"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Errorf("prior negotiate response invented scoped %q precision: %s", forbidden, encoded)
			}
		}
		uciMigrationRollbackRequireScopedRowsInert(t, beforeScopedRows, uciMigrationRollbackScopedRowCounts(t, db))
	})

	t.Run("mixed UCI failure never falls back to the raw legacy store", func(t *testing.T) {
		fixture := newUCIAuthorizationMatrixFixture(t)
		fixture.server.SetLegacyUnscopedCodeChunkStore(legacyStore)
		fixture.application.mu.Lock()
		delete(fixture.application.responses, "search")
		fixture.application.mu.Unlock()

		response := fixture.call(t, fixture.clientA, "uci-migration-rollback-mixed", "codebase_search", map[string]any{
			"query": uciMigrationRollbackLegacyQuery,
		})
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatalf("marshal mixed-state response: %v", err)
		}
		for _, forbidden := range []string{"legacy_unscoped", uciMigrationRollbackLegacyFile, uciMigrationRollbackLegacyProject} {
			if strings.Contains(string(encoded), forbidden) {
				t.Errorf("typed UCI failure fell back to legacy data %q: %s", forbidden, encoded)
			}
		}
		if response.Error != nil {
			if response.Result != nil {
				t.Errorf("mixed UCI failure response = %#v, want a closed MCP error or typed UCI failure", response)
			}
		} else {
			payload := fixture.requireQueryResponse(t, response)
			if payload.Error == nil || (payload.Items != nil && len(*payload.Items) != 0) {
				t.Errorf("mixed UCI payload = %#v, want a closed failure with no successful items", payload)
			}
		}
		uciMigrationRollbackRequireScopedRowsInert(t, beforeScopedRows, uciMigrationRollbackScopedRowCounts(t, db))
	})
}

func uciMigrationRollbackRequireMigrations(t *testing.T, db *gormlib.DB) {
	t.Helper()
	ids := []string{
		"171_uci_context_registry",
		"172_uci_index_projection",
		"173_uci_fenced_publication",
		"174_uci_embedding_jobs",
		"175_uci_reference_source_text",
	}
	var applied int64
	if err := db.Table("migrations").Where("id IN ?", ids).Count(&applied).Error; err != nil {
		t.Fatalf("read applied UCI migration range: %v", err)
	}
	if applied != int64(len(ids)) {
		t.Fatalf("applied UCI migrations 171-175 = %d, want %d", applied, len(ids))
	}
}

func uciMigrationRollbackCall(t *testing.T, server *mcp.Server, name string, arguments map[string]any) *mcp.Response {
	t.Helper()
	argumentsJSON, err := json.Marshal(arguments)
	if err != nil {
		t.Fatalf("marshal %s arguments: %v", name, err)
	}
	params, err := json.Marshal(mcp.ToolCallParams{Name: name, Arguments: argumentsJSON})
	if err != nil {
		t.Fatalf("marshal %s request: %v", name, err)
	}
	response := server.HandleRequest(context.Background(), &mcp.Request{
		JSONRPC: "2.0",
		ID:      "uci-migration-rollback-" + name,
		Method:  "tools/call",
		Params:  params,
	})
	if response == nil {
		t.Fatalf("%s returned no MCP response", name)
	}
	return response
}

func uciMigrationRollbackToolPayload(t *testing.T, response *mcp.Response, name string) map[string]any {
	t.Helper()
	if response.Error != nil {
		t.Errorf("%s in explicit legacy-only rollback = %#v, want a legacy_unscoped payload", name, response.Error)
		return nil
	}
	result, ok := response.Result.(map[string]any)
	if !ok {
		t.Errorf("%s result = %#v, want MCP content envelope", name, response.Result)
		return nil
	}
	content, ok := result["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Errorf("%s content = %#v, want one text payload", name, result["content"])
		return nil
	}
	text, ok := content[0]["text"].(string)
	if !ok {
		t.Errorf("%s text = %#v, want JSON payload", name, content[0]["text"])
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Errorf("decode %s payload: %v; payload=%q", name, err, text)
		return nil
	}
	return payload
}

func uciMigrationRollbackRequirePublicSearchTool(t *testing.T, server *mcp.Server) {
	t.Helper()
	for _, tool := range server.ListTools() {
		if tool.Name == "codebase_search" {
			return
		}
	}
	t.Error("codebase_search is absent from tools/list in the explicit legacy-only rollback state")
}

func uciMigrationRollbackRequireLegacySearch(t *testing.T, payload map[string]any) {
	t.Helper()
	if payload == nil {
		return
	}
	uciMigrationRollbackRequireLegacyPayload(t, "codebase_search", payload)
	if payload["project"] != uciMigrationRollbackLegacyProject || payload["query"] != uciMigrationRollbackLegacyQuery {
		t.Errorf("legacy search payload = %#v, want the explicit prior-project request", payload)
	}
	results, ok := payload["results"].([]any)
	if !ok || len(results) != 1 {
		t.Errorf("legacy search results = %#v, want one retained prior chunk", payload["results"])
		return
	}
	hit, ok := results[0].(map[string]any)
	if !ok || hit["file_path"] != uciMigrationRollbackLegacyFile {
		t.Errorf("legacy search hit = %#v, want retained file %q", results[0], uciMigrationRollbackLegacyFile)
	}
}

func uciMigrationRollbackRequireLegacyStatus(t *testing.T, payload map[string]any) {
	t.Helper()
	if payload == nil {
		return
	}
	uciMigrationRollbackRequireLegacyPayload(t, "codebase_status", payload)
	if payload["project"] != uciMigrationRollbackLegacyProject || payload["total_chunks"] != float64(1) || payload["embedded_chunks"] != float64(0) {
		t.Errorf("legacy status payload = %#v, want exactly one retained unembedded prior chunk", payload)
	}
}

func uciMigrationRollbackRequireLegacyPayload(t *testing.T, operation string, payload map[string]any) {
	t.Helper()
	if payload["retrieval_mode"] != "legacy_unscoped" {
		t.Errorf("%s retrieval_mode = %#v, want explicit legacy_unscoped limitation", operation, payload["retrieval_mode"])
	}
	uciMigrationRollbackRequireNoScopedPrecision(t, operation, payload)
}

func uciMigrationRollbackRequireNoScopedPrecision(t *testing.T, operation string, value any) {
	t.Helper()
	for _, key := range []string{
		"context",
		"contexts",
		"source_id",
		"checkout_id",
		"view_id",
		"profile_id",
		"generation",
		"exposure",
		"evidence_recorder",
		"embedding",
	} {
		if uciMigrationRollbackPayloadHasKey(value, key) {
			t.Errorf("%s legacy payload invented scoped %q precision: %#v", operation, key, value)
		}
	}
}

func uciMigrationRollbackPayloadHasKey(value any, want string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == want || uciMigrationRollbackPayloadHasKey(child, want) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if uciMigrationRollbackPayloadHasKey(child, want) {
				return true
			}
		}
	}
	return false
}

func uciMigrationRollbackScopedRowCounts(t *testing.T, db *gormlib.DB) map[string]int64 {
	t.Helper()
	type tableName struct {
		Name string `gorm:"column:table_name"`
	}
	var tables []tableName
	if err := db.Raw(`
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = current_schema()
		  AND table_type = 'BASE TABLE'
		  AND LEFT(table_name, 3) IN ('ci_', 'uci_')
		ORDER BY table_name
	`).Scan(&tables).Error; err != nil {
		t.Fatalf("list migrated UCI evidence tables: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("migrated schema has no ci_/uci_ evidence tables")
	}
	counts := make(map[string]int64, len(tables))
	for _, table := range tables {
		var count int64
		if err := db.Table(table.Name).Count(&count).Error; err != nil {
			t.Fatalf("count %s rows: %v", table.Name, err)
		}
		counts[table.Name] = count
	}
	return counts
}

func uciMigrationRollbackRequireScopedRowsInert(t *testing.T, before, after map[string]int64) {
	t.Helper()
	if len(before) != len(after) {
		t.Errorf("scoped evidence table count changed from %d to %d", len(before), len(after))
	}
	for table, beforeCount := range before {
		afterCount, found := after[table]
		if !found {
			t.Errorf("scoped evidence table %q disappeared during rollback", table)
			continue
		}
		if beforeCount != 0 {
			t.Errorf("scoped evidence table %q starts with %d rows, want inert empty baseline", table, beforeCount)
		}
		if afterCount != beforeCount {
			t.Errorf("scoped evidence table %q rows = %d after legacy rollback route, want unchanged %d", table, afterCount, beforeCount)
		}
	}
}
