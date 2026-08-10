package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
)

func TestDocsToolDiscoveryDescribesCurrentDocumentBehavior(t *testing.T) {
	t.Parallel()

	server := NewServer(ServerOptions{Version: "test", DocumentStore: &dbgorm.DocumentStore{}})
	response := server.HandleRequest(context.Background(), &Request{
		JSONRPC: "2.0", ID: float64(1), Method: "tools/list",
	})
	require.Nil(t, response.Error)

	tools := response.Result.(map[string]any)["tools"].([]Tool)
	var docs Tool
	for _, tool := range tools {
		if tool.Name == "docs" {
			docs = tool
			break
		}
	}
	require.NotEmpty(t, docs.Description)
	require.Contains(t, docs.Description, "content-addressed")
	require.Contains(t, docs.Description, "full content-addressed body")
	require.Contains(t, docs.Description, "chunk")
	require.Contains(t, docs.Description, "embedding")
	require.Contains(t, docs.Description, "search is unavailable")

	properties := docs.InputSchema["properties"].(map[string]any)
	actions := properties["action"].(map[string]any)["enum"].([]string)
	require.Contains(t, actions, "ingest")
	require.Contains(t, actions, "remove")
	require.NotContains(t, actions, "search")
	require.Contains(t, properties["content"].(map[string]any)["description"], "full content-addressed body")
}

func TestDocsIngestAndRemoveExposeTruthfulStorageLifecycle(t *testing.T) {
	collection := "document-truth-" + uuid.NewString()
	path := "guide.md"
	body := "document body " + uuid.NewString()
	hash := documentTruthHash(body)
	env := newMemoryServerForT007(t, collection)
	env.srv.documentStore = dbgorm.NewDocumentStore(env.store)
	t.Cleanup(func() {
		ctx := context.Background()
		require.NoError(t, env.store.DB.WithContext(ctx).Exec(`DELETE FROM documents WHERE collection = ?`, collection).Error)
		require.NoError(t, env.store.DB.WithContext(ctx).Exec(`DELETE FROM content WHERE hash = ?`, hash).Error)
	})

	ingested := requireDocsToolText(t, callDocsTool(t, env.srv, map[string]any{
		"action": "ingest", "collection": collection, "path": path, "title": "Guide", "content": body,
	}))
	require.Contains(t, ingested, "full body stored content-addressably")
	require.Contains(t, ingested, hash[:12])
	require.Contains(t, ingested, "chunks, embeddings, and search remain unavailable")
	require.Equal(t, body, requireDocsToolText(t, callDocsTool(t, env.srv, map[string]any{
		"action": "get_doc", "collection": collection, "path": path,
	})))

	removed := requireDocsToolText(t, callDocsTool(t, env.srv, map[string]any{
		"action": "remove", "collection": collection, "path": path,
	}))
	require.Contains(t, removed, "marked inactive")
	require.Contains(t, removed, "content-addressed body remains stored")
	requireDocsNotFound(t, callDocsTool(t, env.srv, map[string]any{
		"action": "remove", "collection": collection, "path": path,
	}), collection, path)

	var row struct{ Active bool }
	require.NoError(t, env.store.DB.Raw(`SELECT active FROM documents WHERE collection = ? AND path = ?`, collection, path).Scan(&row).Error)
	require.False(t, row.Active)
	var stored string
	require.NoError(t, env.store.DB.Raw(`SELECT doc FROM content WHERE hash = ?`, hash).Scan(&stored).Error)
	require.Equal(t, body, stored)
}

func TestDocsConcurrentRemoveHasOneWinner(t *testing.T) {
	collection := "document-truth-race-" + uuid.NewString()
	path := "race.md"
	body := "race body " + uuid.NewString()
	hash := documentTruthHash(body)
	triggerName := "document_truth_delay_" + uuid.NewString()
	env := newMemoryServerForT007(t, collection)
	env.srv.documentStore = dbgorm.NewDocumentStore(env.store)
	ctx := context.Background()
	cleanup := func() {
		require.NoError(t, env.store.DB.WithContext(ctx).Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %q ON documents`, triggerName)).Error)
		require.NoError(t, env.store.DB.WithContext(ctx).Exec(fmt.Sprintf(`DROP FUNCTION IF EXISTS %q()`, triggerName)).Error)
		require.NoError(t, env.store.DB.WithContext(ctx).Exec(`DELETE FROM documents WHERE collection = ?`, collection).Error)
		require.NoError(t, env.store.DB.WithContext(ctx).Exec(`DELETE FROM content WHERE hash = ?`, hash).Error)
	}
	t.Cleanup(cleanup)

	requireDocsToolText(t, callDocsTool(t, env.srv, map[string]any{
		"action": "ingest", "collection": collection, "path": path, "content": body,
	}))
	require.NoError(t, env.store.DB.WithContext(ctx).Exec(fmt.Sprintf(`
		CREATE FUNCTION %q() RETURNS trigger AS $$
		BEGIN
			PERFORM pg_sleep(0.25);
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`, triggerName)).Error)
	require.NoError(t, env.store.DB.WithContext(ctx).Exec(fmt.Sprintf(`
		CREATE TRIGGER %q
		BEFORE UPDATE OF active ON documents
		FOR EACH ROW
		WHEN (OLD.active IS TRUE AND NEW.active IS FALSE)
		EXECUTE FUNCTION %q()`, triggerName, triggerName)).Error)

	arguments, err := json.Marshal(map[string]any{
		"action": "remove", "collection": collection, "path": path,
	})
	require.NoError(t, err)
	params, err := json.Marshal(ToolCallParams{Name: "docs", Arguments: arguments})
	require.NoError(t, err)
	start := make(chan struct{})
	responses := make([]*Response, 2)
	var wg sync.WaitGroup
	for i := range responses {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			responses[index] = env.srv.HandleRequest(context.Background(), &Request{
				JSONRPC: "2.0", ID: float64(index + 1), Method: "tools/call", Params: params,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	var succeeded, notFound *Response
	for _, response := range responses {
		require.NotNil(t, response)
		if response.Error == nil {
			require.Nil(t, succeeded)
			succeeded = response
		} else {
			require.Nil(t, notFound)
			notFound = response
		}
	}
	require.Contains(t, requireDocsToolText(t, succeeded), "marked inactive")
	requireDocsNotFound(t, notFound, collection, path)

	var active bool
	require.NoError(t, env.store.DB.Raw(`SELECT active FROM documents WHERE collection = ? AND path = ?`, collection, path).Scan(&active).Error)
	require.False(t, active)
	var stored string
	require.NoError(t, env.store.DB.Raw(`SELECT doc FROM content WHERE hash = ?`, hash).Scan(&stored).Error)
	require.Equal(t, body, stored)
}

func callDocsTool(t *testing.T, server *Server, arguments map[string]any) *Response {
	t.Helper()
	argumentJSON, err := json.Marshal(arguments)
	require.NoError(t, err)
	params, err := json.Marshal(ToolCallParams{Name: "docs", Arguments: argumentJSON})
	require.NoError(t, err)
	response := server.HandleRequest(context.Background(), &Request{
		JSONRPC: "2.0", ID: float64(1), Method: "tools/call", Params: params,
	})
	require.NotNil(t, response)
	return response
}

func requireDocsToolText(t *testing.T, response *Response) string {
	t.Helper()
	require.Nil(t, response.Error)
	result := response.Result.(map[string]any)
	content := result["content"].([]map[string]any)
	require.Len(t, content, 1)
	return content[0]["text"].(string)
}

func requireDocsNotFound(t *testing.T, response *Response, collection, path string) {
	t.Helper()
	expected := fmt.Sprintf("document not found or inactive: %s/%s", collection, path)
	require.NotNil(t, response.Error)
	require.Equal(t, -32000, response.Error.Code)
	require.Equal(t, "Tool error: "+expected, response.Error.Message)
	require.Equal(t, expected, response.Error.Data)
}
func documentTruthHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

