package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/principalmemory"
	gormlib "gorm.io/gorm"
)

// Exercise the source-built MCP JSON-RPC stream and its real PostgreSQL store.
// The synthetic identity and in-process stream do not establish installed-client behavior.
func TestRecallCanonicalMembershipNative(t *testing.T) {
	store := openRecallProjectPrivacyStore(t)
	project, other := uuid.NewString(), uuid.NewString()
	seed := store.DB.Begin()
	require.NoError(t, seed.Error)
	defer seed.Rollback()
	require.NoError(t, seed.Exec("SET LOCAL session_replication_role=replica").Error)
	require.NoError(t, seed.Create(&[]dbgorm.Project{{ID: project}, {ID: other}}).Error)
	historical := dbgorm.Memory{Project: "former-alpha", Content: "paritynative historical body", Status: "active", PrivacyScope: "project", Version: 3}
	conflict := dbgorm.Memory{Project: project, Content: "paritynative conflicting body", Status: "active", PrivacyScope: "project", ImportanceBase: 0.95, Version: 1}
	require.NoError(t, seed.Create(&historical).Error)
	require.NoError(t, seed.Create(&conflict).Error)
	principalHistorical := dbgorm.Memory{Project: "former-alpha", Content: "principalneedle authorized body", Status: "active", PrivacyScope: "project", OwnerPrincipal: "agent/bob", OwnerPrincipalKind: "agent", AgentVisibility: "private", Version: 1}
	principalConflict := dbgorm.Memory{Project: project, Content: "principalneedle conflicting body", Status: "active", PrivacyScope: "project", OwnerPrincipal: "agent/bob", OwnerPrincipalKind: "agent", AgentVisibility: "private", Version: 1}
	require.NoError(t, seed.Create(&principalHistorical).Error)
	require.NoError(t, seed.Create(&principalConflict).Error)
	for range 4 {
		private := dbgorm.Memory{Project: project, Content: "paritynative hidden private", Status: "active", PrivacyScope: "project", OwnerPrincipal: "agent/bob", OwnerPrincipalKind: "agent", AgentVisibility: "private", ImportanceBase: 0.99, Version: 1}
		require.NoError(t, seed.Create(&private).Error)
	}
	for index, entry := range []struct {
		row     int64
		project string
	}{{historical.ID, project}, {conflict.ID, project}, {conflict.ID, other}, {principalHistorical.ID, project}, {principalConflict.ID, project}, {principalConflict.ID, other}} {
		p := entry.project
		require.NoError(t, seed.Create(&dbgorm.ConsolidationMembership{
			Reference: fmt.Sprintf("urn:engram:fixture:mcp-parity:%d", index), Table: "memories",
			RowKey: dbgorm.JSONRaw(fmt.Sprintf(`{"id":%d}`, entry.row)), FieldPath: dbgorm.JSONRaw(`["project"]`),
			ProjectID: &p, PlanFingerprint: "synthetic-mcp-parity",
		}).Error)
	}
	require.NoError(t, seed.Commit().Error)
	srv := NewServer(ServerOptions{Version: "synthetic-parity"})
	srv.memoryStore = dbgorm.NewMemoryStore(store)
	srv.SetPrincipalMemoryQueryService(principalmemory.NewPrincipalMemoryQueryService(srv.memoryStore, dbgorm.NewAuditStore(store.DB)))
	caller := auth.WithIdentity(t.Context(), auth.Identity{Role: auth.Role("admin"), Source: auth.SourceClient, KeycardID: "parity-ws"})
	var input, output bytes.Buffer
	encoder := json.NewEncoder(&input)
	require.NoError(t, encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "parity-fixture", "version": "1"}}}))
	require.NoError(t, encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{
		"name": "recall", "arguments": map[string]any{"action": "search", "project": project, "query": "paritynative", "limit": 1},
	}}))
	srv.stdin, srv.stdout = &input, &output
	require.NoError(t, srv.Run(caller))
	decoder := json.NewDecoder(&output)
	var initResp, resp struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	require.NoError(t, decoder.Decode(&initResp))
	require.Equal(t, 1, initResp.ID)
	require.Empty(t, initResp.Error)
	require.NoError(t, decoder.Decode(&resp))
	require.Equal(t, 2, resp.ID)
	require.Empty(t, resp.Error)
	var envelope struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &envelope))
	require.False(t, envelope.IsError)
	require.Len(t, envelope.Content, 1)
	var result struct {
		Memories []struct {
			ID      int64  `json:"id"`
			Project string `json:"project"`
			Content string `json:"content"`
		} `json:"memories"`
	}
	require.NoError(t, json.Unmarshal([]byte(envelope.Content[0].Text), &result))
	require.Len(t, result.Memories, 1)
	require.Equal(t, historical.ID, result.Memories[0].ID)
	require.Equal(t, project, result.Memories[0].Project)
	require.Equal(t, historical.Content, result.Memories[0].Content)
	brief := srv.HandleRequest(caller, &Request{JSONRPC: "2.0", ID: 3, Method: "tools/call", Params: mustJSON(t, map[string]any{
		"name": "get_memory_brief", "arguments": map[string]any{"project": project, "limit": 1},
	})})
	require.NotNil(t, brief)
	require.Nil(t, brief.Error)
	encoded, err := json.Marshal(brief.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encoded, &envelope))
	require.False(t, envelope.IsError)
	require.Len(t, envelope.Content, 1)
	require.NoError(t, json.Unmarshal([]byte(envelope.Content[0].Text), &result))
	require.Len(t, result.Memories, 1, "private and conflicting higher-importance rows must not consume the public brief candidate limit")
	require.Equal(t, historical.ID, result.Memories[0].ID)
	require.Equal(t, historical.Content, result.Memories[0].Content)
	capture := func(query string, arg any) string {
		t.Helper()
		var raw string
		require.NoError(t, store.DB.Raw(query, arg).Row().Scan(&raw))
		return raw
	}
	rowSQL := "SELECT row_to_json(m)::text FROM memories m WHERE m.id = ?"
	memberSQL := "SELECT row_to_json(p)::text FROM project_consolidation_memberships p WHERE p.table_name = 'memories' AND p.row_key->>'id' = ?"
	beforeRow := capture(rowSQL, principalHistorical.ID)
	beforeMembership := capture(memberSQL, fmt.Sprint(principalHistorical.ID))
	updateDone := make(chan error, 1)
	require.NoError(t, store.DB.Callback().Update().After("gorm:update").Register("synthetic:recall-update", func(tx *gormlib.DB) {
		select {
		case updateDone <- tx.Error:
		default:
		}
	}))
	principal := srv.HandleRequest(caller, &Request{JSONRPC: "2.0", ID: 4, Method: "tools/call", Params: mustJSON(t, map[string]any{
		"name": "recall_memory", "arguments": map[string]any{"project": project, "query": "principalneedle", "format": "items", "limit": 1, "include_principals": []map[string]any{{"principal": "agent/bob", "principal_kind": "agent"}}},
	})})
	require.NotNil(t, principal)
	require.Nil(t, principal.Error)
	encoded, err = json.Marshal(principal.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encoded, &envelope))
	require.Len(t, envelope.Content, 1)
	var principalRows []struct {
		ID      int64  `json:"id"`
		Project string `json:"project"`
	}
	require.NoError(t, json.Unmarshal([]byte(envelope.Content[0].Text), &principalRows))
	require.Len(t, principalRows, 1)
	require.Equal(t, principalHistorical.ID, principalRows[0].ID)
	require.Equal(t, project, principalRows[0].Project)
	var audits int64
	require.NoError(t, store.DB.Model(&dbgorm.AuditLogEntry{}).Where("memory_id = ? AND action = ?", principalHistorical.ID, "principal_memory_private_read").Count(&audits).Error)
	require.EqualValues(t, 1, audits, "cross-principal private widening must retain its durable audit")
	select {
	case updateErr := <-updateDone:
		require.NoError(t, updateErr)
	case <-time.After(15 * time.Second):
		t.Fatal("recall lifecycle update did not complete before fixture cleanup")
	}
	require.Equal(t, beforeRow, capture(rowSQL, principalHistorical.ID), "public recall must preserve the historical raw row")
	require.Equal(t, beforeMembership, capture(memberSQL, fmt.Sprint(principalHistorical.ID)), "public recall must preserve membership")
	history := srv.HandleRequest(caller, &Request{JSONRPC: "2.0", ID: 5, Method: "tools/call", Params: mustJSON(t, map[string]any{
		"name": "recall_memory", "arguments": map[string]any{"project": project, "query": "absent-history-marker", "format": "items", "include_superseded": true, "include_principals": []map[string]any{{"principal": "agent/bob", "principal_kind": "agent"}}},
	})})
	require.NotNil(t, history)
	require.Nil(t, history.Error, "explicit-principal legacy recall supports include_superseded")
	encoded, err = json.Marshal(history.Result)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encoded, &envelope))
	require.False(t, envelope.IsError)
}

func TestRecallInvalidProjectNative(t *testing.T) {
	store := openRecallProjectPrivacyStore(t)
	valid, removed := uuid.NewString(), uuid.NewString()
	now := time.Now()
	require.NoError(t, store.DB.Create(&[]dbgorm.Project{{ID: valid}, {ID: removed, RemovedAt: &now}}).Error)
	srv := NewServer(ServerOptions{Version: "synthetic-invalid-project"})
	srv.memoryStore = dbgorm.NewMemoryStore(store)
	caller := auth.WithIdentity(t.Context(), auth.Identity{Role: auth.Role("admin"), Source: auth.SourceClient, KeycardID: "parity-ws"})
	call := func(tool, project string) *Response {
		t.Helper()
		return srv.HandleRequest(caller, &Request{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: mustJSON(t, map[string]any{
			"name": tool, "arguments": map[string]any{"action": "search", "project": project, "query": "absentneedle", "format": "items"},
		})})
	}
	for _, tool := range []string{"recall", "recall_memory"} {
		t.Run(tool, func(t *testing.T) {
			validResponse := call(tool, valid)
			require.NotNil(t, validResponse)
			require.Nil(t, validResponse.Error, "a valid project with no matching memory is not a context failure")
			envelope, ok := validResponse.Result.(map[string]any)
			require.True(t, ok)
			content, ok := envelope["content"].([]map[string]any)
			require.True(t, ok)
			require.Len(t, content, 1)
			text, ok := content[0]["text"].(string)
			require.True(t, ok)
			if tool == "recall" {
				var empty struct {
					Memories []json.RawMessage `json:"memories"`
				}
				require.NoError(t, json.Unmarshal([]byte(text), &empty))
				require.Empty(t, empty.Memories)
			} else {
				var empty []json.RawMessage
				require.NoError(t, json.Unmarshal([]byte(text), &empty))
				require.Empty(t, empty)
			}
			for _, selector := range []string{"malformed-project", uuid.NewString(), removed} {
				response := call(tool, selector)
				require.NotNil(t, response)
				require.NotNil(t, response.Error, "invalid, unknown or removed project must not appear as an empty successful search")
				require.Equal(t, "project read forbidden", response.Error.Data)
			}
		})
	}
	projectFree := call("recall", "")
	require.NotNil(t, projectFree)
	require.Nil(t, projectFree.Error, "the existing project-free recall response must remain available")
}
