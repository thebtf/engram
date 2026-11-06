package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestGraphDefaultTimestampHTTPAndMCPPostgres(t *testing.T) {
	previousLocal := time.Local
	time.Local = time.FixedZone("graph-test-plus-three", 3*60*60)
	t.Cleanup(func() { time.Local = previousLocal })
	store := openContextSearchPrivacyStore(t)
	project := uuid.NewString()
	require.NoError(t, store.DB.Create(&gormdb.Project{ID: project}).Error)
	nodes := graph.NewNodesStore(store.DB)
	makeNode := func(ref, privacy string) *models.KnowledgeNode {
		t.Helper()
		node, err := nodes.Create(t.Context(), &models.KnowledgeNode{NodeType: models.NodeTypeSkill, ExternalRef: ref, Project: project, PrivacyScope: privacy})
		require.NoError(t, err)
		return node
	}
	public := makeNode("graph-time-public", "project")
	private := makeNode("graph-time-private", "private")
	insertEdge := func(target int64, reasoning string) int64 {
		t.Helper()
		var id int64
		err := store.DB.Raw(`INSERT INTO knowledge_edges (node_source_id, node_target_id, source_type, target_type, edge_type, weight, reasoning) VALUES (?, ?, 'node', 'node', 'uses', 1, ?) RETURNING id`, public.ID, target, reasoning).Row().Scan(&id)
		require.NoError(t, err)
		return id
	}
	edgeID := insertEdge(public.ID, "visible-boundary-edge")
	insertEdge(private.ID, "private-boundary-edge")
	edges := graph.NewStore(store.DB, nodes)
	loaded, err := edges.Get(t.Context(), edgeID)
	require.NoError(t, err)
	var storedUTC string
	require.NoError(t, store.DB.Raw(`SELECT to_char(valid_until AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS') FROM knowledge_edges WHERE id = ?`, edgeID).Scan(&storedUTC).Error)
	require.Equal(t, "9999-12-31 23:59:59", storedUTC)
	require.Equal(t, 10000, loaded.ValidUntil.Year())
	_, offset := loaded.ValidUntil.Zone()
	require.Equal(t, 3*60*60, offset)
	t.Logf("PostgreSQL default UTC=%s loaded=%s", storedUTC, loaded.ValidUntil.Format("2006-01-02 15:04:05 -07:00"))

	service := &Service{graphStore: edges, graphNodeStore: nodes}
	service.ready.Store(true)
	httpServer := httptest.NewServer(graphRouter(service))
	t.Cleanup(httpServer.Close)
	response, err := http.Get(fmt.Sprintf("%s/api/graph/edges?node_id=%d", httpServer.URL, public.ID))
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, string(body))
	var httpBody struct {
		Edges []graph.Edge `json:"edges"`
		Count int          `json:"count"`
	}
	require.NoError(t, json.Unmarshal(body, &httpBody))
	require.Equal(t, 1, httpBody.Count)
	require.Len(t, httpBody.Edges, 1)
	require.Equal(t, edgeID, httpBody.Edges[0].ID)
	require.True(t, httpBody.Edges[0].ValidUntil.Equal(loaded.ValidUntil))
	require.NotContains(t, string(body), "private-boundary-edge")

	server := mcp.NewServer(mcp.ServerOptions{Version: "graph-time-fixture"})
	server.SetGraphStore(edges)
	server.SetNodesStore(nodes)
	memoryStore := gormdb.NewMemoryStore(store)
	server.SetMemoryStore(memoryStore)
	grpcServer, _ := grpcserver.New(&mcpHandlerAdapter{mcpServer: server}, nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { grpcServer.Stop(); _ = listener.Close() })
	go func() { _ = grpcServer.Serve(listener) }()
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	client := pb.NewEngramServiceClient(connection)
	call := func(arguments map[string]any) string {
		t.Helper()
		params, err := json.Marshal(arguments)
		require.NoError(t, err)
		result, err := client.CallTool(context.Background(), &pb.CallToolRequest{ToolName: "graph", ArgumentsJson: params, Project: project})
		require.NoError(t, err)
		require.False(t, result.IsError, string(result.ContentJson))
		var envelope struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		require.NoError(t, json.Unmarshal(result.ContentJson, &envelope))
		require.Len(t, envelope.Content, 1)
		return envelope.Content[0].Text
	}
	mcpText := call(map[string]any{"action": "get_edges", "node_id": public.ID})
	require.NotContains(t, mcpText, "private-boundary-edge")
	var mcpBody struct {
		Edges []graph.Edge `json:"edges"`
		Count int          `json:"count"`
	}
	require.NoError(t, json.Unmarshal([]byte(mcpText), &mcpBody))
	require.Equal(t, 1, mcpBody.Count)
	require.Len(t, mcpBody.Edges, 1)
	require.Equal(t, edgeID, mcpBody.Edges[0].ID)
	require.True(t, mcpBody.Edges[0].ValidUntil.Equal(loaded.ValidUntil))
	memory, err := memoryStore.Create(t.Context(), &models.Memory{Project: project, Content: "graph synonym boundary", PrivacyScope: "project"})
	require.NoError(t, err)
	var synonymID int64
	err = store.DB.Raw(`INSERT INTO knowledge_edges (source_id, target_id, edge_type, weight) VALUES (?, ?, 'synonym_of', 1) RETURNING id`, memory.ID, memory.ID).Row().Scan(&synonymID)
	require.NoError(t, err)
	synonymText := call(map[string]any{"action": "synonyms", "memory_id": memory.ID})
	var synonyms struct {
		Edges []graph.Edge `json:"synonyms"`
		Count int          `json:"count"`
	}
	require.NoError(t, json.Unmarshal([]byte(synonymText), &synonyms))
	require.Equal(t, 1, synonyms.Count)
	require.Len(t, synonyms.Edges, 1)
	require.Equal(t, synonymID, synonyms.Edges[0].ID)
	require.True(t, synonyms.Edges[0].ValidUntil.Equal(loaded.ValidUntil))
}
