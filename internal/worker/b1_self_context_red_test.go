package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/graph"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/pkg/models"
)

func TestB1RegisteredPrincipalProjectFreeCard(t *testing.T) {
	db := memoryMutationSQLiteDB(t)
	f := newMemoryMutationSecurityFixture(t, db)

	// Seed registration directly; separate PostgreSQL coverage exercises enrollment.
	require.NoError(t, db.Exec(`INSERT INTO memory_principals (principal_kind, principal, enrolled_by) VALUES (?, ?, ?), (?, ?, ?)`, "agent", "agent/synthetic-security-a", 1, "agent", "agent/synthetic-security-b", 1).Error)
	require.NoError(t, db.Exec(`CREATE TABLE memory_revisions (memory_id INTEGER NOT NULL, version INTEGER NOT NULL, content TEXT NOT NULL, changed_by_principal TEXT NOT NULL, changed_by_principal_kind TEXT NOT NULL, changed_at TIMESTAMP NOT NULL, PRIMARY KEY (memory_id, version))`).Error)

	var projectsBefore int64
	require.NoError(t, db.Model(&gormdb.Project{}).Count(&projectsBefore).Error)
	payload := map[string]any{"content": "Synthetic self-owned context card", "source_agent": "synthetic-client", "tags": []string{"type:fact"}}
	created := f.serve(f.request(t, context.Background(), http.MethodPost, "/api/memories", f.tokenA, payload))
	require.Equal(t, http.StatusCreated, created.status, "registered principal must save without project: %s", created.body)

	var card struct {
		ID             int64  `json:"id"`
		Project        string `json:"project"`
		Content        string `json:"content"`
		Version        int    `json:"version"`
		OwnerPrincipal string `json:"owner_principal"`
		Context        struct {
			Kind          string `json:"kind"`
			Principal     string `json:"principal"`
			PrincipalKind string `json:"principal_kind"`
		} `json:"context"`
	}
	require.NoError(t, json.Unmarshal([]byte(created.body), &card))
	require.Positive(t, card.ID)
	require.Empty(t, card.Project)
	require.Equal(t, payload["content"], card.Content)
	require.Equal(t, "agent/synthetic-security-a", card.OwnerPrincipal)
	require.Equal(t, "self", card.Context.Kind)
	require.Equal(t, "agent/synthetic-security-a", card.Context.Principal)
	require.Equal(t, "agent", card.Context.PrincipalKind)
	var projectsAfter int64
	require.NoError(t, db.Model(&gormdb.Project{}).Count(&projectsAfter).Error)
	require.Equal(t, projectsBefore, projectsAfter, "a self card must not create a fake project")

	path := fmt.Sprintf("/api/memories/%d", card.ID)
	read := f.serve(f.request(t, context.Background(), http.MethodGet, path, f.tokenA2, nil))
	require.Equal(t, http.StatusOK, read.status, read.body)
	var readCard struct {
		ID      int64  `json:"id"`
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal([]byte(read.body), &readCard))
	require.Equal(t, card.ID, readCard.ID)
	require.Equal(t, card.Content, readCard.Content)
	denied := f.serve(f.request(t, context.Background(), http.MethodGet, path, f.tokenB, nil))
	require.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, denied.status)
	require.NotContains(t, denied.body, card.Content)
	invalid := f.serve(f.request(t, context.Background(), http.MethodGet, path+"?project=not-a-valid-project", f.tokenA, nil))
	require.Contains(t, []int{http.StatusBadRequest, http.StatusNotFound}, invalid.status)
	require.NotContains(t, invalid.body, card.Content)
	deniedRevision := f.serve(f.request(t, context.Background(), http.MethodPatch, path, f.tokenB, map[string]any{"version": card.Version, "content": "not authorized"}))
	require.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, deniedRevision.status)
	deniedHistory := f.serve(f.request(t, context.Background(), http.MethodGet, path+"/revisions", f.tokenB, nil))
	require.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, deniedHistory.status)
	revised := f.serve(f.request(t, context.Background(), http.MethodPatch, path, f.tokenA2, map[string]any{"version": card.Version, "content": "Revised synthetic self card"}))
	require.Equal(t, http.StatusOK, revised.status, revised.body)
	var after struct {
		ID      int64  `json:"id"`
		Version int    `json:"version"`
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal([]byte(revised.body), &after))
	require.Equal(t, card.ID, after.ID)
	require.Equal(t, card.Version+1, after.Version)
	require.Equal(t, "Revised synthetic self card", after.Content)
	retry := f.serve(f.request(t, context.Background(), http.MethodPatch, path, f.tokenA2, map[string]any{"version": card.Version, "content": "Revised synthetic self card"}))
	require.Equal(t, http.StatusConflict, retry.status)
	history := f.serve(f.request(t, context.Background(), http.MethodGet, path+"/revisions", f.tokenA2, nil))
	require.Equal(t, http.StatusOK, history.status, history.body)
	var prior struct {
		Revisions []struct {
			Version int    `json:"version"`
			Content string `json:"content"`
		} `json:"revisions"`
	}
	require.NoError(t, json.Unmarshal([]byte(history.body), &prior))
	require.Len(t, prior.Revisions, 1)
	require.Equal(t, card.Version, prior.Revisions[0].Version)
	require.Equal(t, card.Content, prior.Revisions[0].Content)
}

func TestB1TypedRawUpdateRetainsHistory(t *testing.T) {
	db := memoryMutationSQLiteDB(t)
	f := newMemoryMutationSecurityFixture(t, db)
	require.NoError(t, db.Exec(`INSERT INTO memory_principals (principal_kind, principal, enrolled_by) VALUES (?, ?, ?)`, "agent", "agent/synthetic-security-a", 1).Error)
	require.NoError(t, db.Exec(`CREATE TABLE memory_revisions (memory_id INTEGER NOT NULL, version INTEGER NOT NULL, content TEXT NOT NULL, changed_by_principal TEXT NOT NULL, changed_by_principal_kind TEXT NOT NULL, changed_at TIMESTAMP NOT NULL, PRIMARY KEY (memory_id, version))`).Error)
	response := f.serve(f.request(t, context.Background(), http.MethodPost, "/api/memories", f.tokenA, map[string]any{"content": "Synthetic original"}))
	require.Equal(t, http.StatusCreated, response.status, response.body)
	var created models.Memory
	require.NoError(t, json.Unmarshal([]byte(response.body), &created))
	modified := created
	modified.Content = "Synthetic changed"
	_, updateErr := f.service.memoryStore.Update(context.Background(), &modified)
	var current gormdb.Memory
	require.NoError(t, db.First(&current, created.ID).Error)
	var revisions []struct {
		Version int
		Content string
	}
	require.NoError(t, db.Table("memory_revisions").Where("memory_id = ?", created.ID).Find(&revisions).Error)
	if updateErr != nil {
		require.Equal(t, created.Content, current.Content)
		require.Equal(t, created.Version, current.Version)
		require.Empty(t, revisions)
		return
	}
	require.Equal(t, modified.Content, current.Content)
	require.Equal(t, created.Version+1, current.Version)
	require.Len(t, revisions, 1, "a successful typed update must retain the prior revision")
	require.Equal(t, created.Content, revisions[0].Content)
	require.Equal(t, created.Version, revisions[0].Version)
}

func TestB1SharedGlobalGraphVisibility(t *testing.T) {
	db := memoryMutationSQLiteDB(t)
	f := newMemoryMutationSecurityFixture(t, db)
	require.NoError(t, db.Exec(`INSERT INTO memory_principals (principal_kind, principal, enrolled_by) VALUES (?, ?, ?), (?, ?, ?)`, "agent", "agent/synthetic-security-a", 1, "agent", "agent/synthetic-security-b", 1).Error)
	create := func(content, visibility string) models.Memory {
		t.Helper()
		payload := map[string]any{"content": content, "context": map[string]any{"kind": "global"}}
		if visibility != "" {
			payload["agent_visibility"] = visibility
		}
		response := f.serve(f.request(t, context.Background(), http.MethodPost, "/api/memories", f.tokenA, payload))
		require.Equal(t, http.StatusCreated, response.status, response.body)
		var card models.Memory
		require.NoError(t, json.Unmarshal([]byte(response.body), &card))
		return card
	}
	first := create("Shared first", "shared")
	second := create("Shared second", "shared")
	private := create("Private third", "")
	f.service.graphEdgeStoreSeam = &fakeGraphEdgeStore{edges: []graph.Edge{
		{ID: 11, SourceID: &first.ID, TargetID: &second.ID, EdgeType: graph.EdgeUses, Reasoning: "shared-edge"},
		{ID: 12, SourceID: &first.ID, TargetID: &private.ID, EdgeType: graph.EdgeUses, Reasoning: "hidden-edge"},
	}}
	path := fmt.Sprintf("/api/graph/edges?memory_id=%d", first.ID)
	read := func(token string) graphEdgesResponse {
		t.Helper()
		response := f.serve(f.request(t, context.Background(), http.MethodGet, path, token, nil))
		require.Equal(t, http.StatusOK, response.status, response.body)
		var result graphEdgesResponse
		require.NoError(t, json.Unmarshal([]byte(response.body), &result))
		return result
	}
	require.Len(t, read(f.tokenA).Edges, 2)
	visible := read(f.tokenB)
	require.Len(t, visible.Edges, 1, "an enrolled reader must see explicitly shared endpoints, not the private edge")
	require.Equal(t, int64(11), visible.Edges[0].ID)
	require.NoError(t, db.Exec(`DELETE FROM memory_principals WHERE principal = ?`, "agent/synthetic-security-b").Error)
	require.Empty(t, read(f.tokenB).Edges, "an unregistered principal must not see shared edge metadata")
}

func TestB1PrincipalQuerySharedGlobalEnrollment(t *testing.T) {
	db := memoryMutationSQLiteDB(t)
	f := newMemoryMutationSecurityFixture(t, db)
	require.NoError(t, db.Exec(`INSERT INTO memory_principals (principal_kind, principal, enrolled_by) VALUES (?, ?, ?), (?, ?, ?)`, "agent", "agent/synthetic-security-a", 1, "agent", "agent/synthetic-security-b", 1).Error)
	for _, payload := range []map[string]any{
		{"content": "Visible shared global", "context": map[string]any{"kind": "global"}, "agent_visibility": "shared"},
		{"content": "Hidden private global", "context": map[string]any{"kind": "global"}},
		{"content": "Hidden self"},
	} {
		response := f.serve(f.request(t, context.Background(), http.MethodPost, "/api/memories", f.tokenA, payload))
		require.Equal(t, http.StatusCreated, response.status, response.body)
	}
	service := principalmemory.NewPrincipalMemoryQueryService(f.service.memoryStore, nil)
	request := principalmemory.PrincipalMemoryQueryRequest{Caller: principalmemory.PrincipalRef{Principal: "agent/synthetic-security-b", PrincipalKind: "agent"}, OwnerPrincipal: "agent/synthetic-security-a", OwnerPrincipalKind: "agent", Limit: 10}
	identity := auth.ClientWithPrincipal("read-only", "synthetic-reader", "agent/synthetic-security-b", auth.PrincipalKindAgent)
	ctx := auth.WithIdentity(context.Background(), identity)
	result, err := service.Query(ctx, request)
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	require.Equal(t, "Visible shared global", result.Items[0].Content)
	ownerRequest := request
	ownerRequest.Caller.Principal = "agent/synthetic-security-a"
	ownerCtx := auth.WithIdentity(context.Background(), auth.ClientWithPrincipal("read-only", "synthetic-owner", "agent/synthetic-security-a", auth.PrincipalKindAgent))
	ownerResult, err := service.Query(ownerCtx, ownerRequest)
	require.NoError(t, err)
	require.Len(t, ownerResult.Items, 3, "owner retains self and default-private access")
	spoofed := request
	spoofed.Caller.Principal = "agent/synthetic-security-a"
	result, err = service.Query(ctx, spoofed)
	require.NoError(t, err)
	require.Empty(t, result.Items)
	result, err = service.Query(context.Background(), request)
	require.NoError(t, err)
	require.Empty(t, result.Items)
	require.NoError(t, db.Exec(`DELETE FROM memory_principals WHERE principal = ?`, "agent/synthetic-security-b").Error)
	result, err = service.Query(ctx, request)
	require.NoError(t, err)
	require.Empty(t, result.Items)
}
