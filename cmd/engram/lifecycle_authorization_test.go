package main

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	dbstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/scope"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Real gRPC authentication, dispatcher and MemoryStore SQL. SQLite proves
// visibility and rollback; PostgreSQL tests own locking and serialization.
func TestLifecycleLegacyAuthorization(t *testing.T) {
	originalConfig, err := config.Load()
	if err != nil {
		t.Fatal("original config load failed")
	}
	t.Cleanup(func() {
		restored, _, err := config.Reload()
		if err != nil {
			t.Errorf("restore shared config after lifecycle authorization test: %v", err)
			return
		}
		if config.Get() != restored {
			t.Error("global config snapshot was not restored")
		}
		if restored.EnforceSourceProject != originalConfig.EnforceSourceProject {
			t.Errorf("restored enforce_source_project = %t, want %t", restored.EnforceSourceProject, originalConfig.EnforceSourceProject)
		}
	})
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", "true")
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "false")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "false")
	if _, _, err := config.Reload(); err != nil {
		t.Fatal("isolated config reload failed")
	}
	f := newMemoryTestBackend(t)
	f.server.Stop()
	if err := f.db.Exec(`CREATE TABLE promotion_log (id INTEGER PRIMARY KEY, memory_id INTEGER NOT NULL, from_tier TEXT NOT NULL, to_tier TEXT NOT NULL, reason TEXT NOT NULL, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatal("promotion fixture setup failed")
	}
	f.mcp.SetPromotionStore(dbstore.NewPromotionStore(f.db))
	tokens := memoryTestTokens{}
	const readonlyOwnerB = "engram_facade00000000000000000000000ccc"
	for _, c := range []struct{ raw, id, principal, role string }{
		{memoryTestTokenA, "fixture-card-a", "agent/local-a", "read-only"},
		{memoryTestTokenB, "fixture-card-b", "agent/local-b", "read-write"},
		{readonlyOwnerB, "fixture-card-b", "agent/local-b", "read-only"},
	} {
		hash, err := bcrypt.GenerateFromPassword([]byte(c.raw), bcrypt.MinCost)
		if err != nil {
			t.Fatal("synthetic credential setup failed")
		}
		tokens = append(tokens, dbstore.APIToken{ID: c.id, TokenHash: string(hash), Scope: c.role, Principal: c.principal, PrincipalKind: "agent"})
	}
	validator := auth.NewValidator("", tokens)
	for _, c := range []struct{ raw, role, principal string }{
		{memoryTestTokenA, "read-only", "agent/local-a"}, {memoryTestTokenB, "read-write", "agent/local-b"},
	} {
		identity, err := validator.Validate(t.Context(), c.raw)
		if err != nil || identity.Source != auth.SourceClient || string(identity.Role) != c.role || identity.Principal != c.principal || identity.PrincipalKind != auth.PrincipalKindAgent {
			t.Fatal("real validator identity control failed")
		}
	}
	server, internal := grpcserver.New(f, validator)
	internal.SetDB(f.db)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("loopback listener failed")
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal("loopback client failed")
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := pb.NewEngramServiceClient(conn)
	call := func(token, project, tool, action string, id int64, extra map[string]any) (*pb.CallToolResponse, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if token != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
		}
		args := map[string]any{"action": action, "memory_id": id, "days_ahead": 10}
		for k, v := range extra {
			args[k] = v
		}
		raw, _ := json.Marshal(args)
		return client.CallTool(ctx, &pb.CallToolRequest{ToolName: tool, ArgumentsJson: raw, Project: project, SessionId: "fixture-a"})
	}
	body := func(t *testing.T, r *pb.CallToolResponse, err error) map[string]any {
		t.Helper()
		if err != nil || r == nil || r.IsError {
			t.Fatal("expected successful actual MCP call")
		}
		var envelope map[string]any
		if json.Unmarshal(r.ContentJson, &envelope) != nil {
			t.Fatal("invalid MCP envelope")
		}
		return memoryTestUnwrap(envelope)
	}
	seed := func(t *testing.T, id int64) {
		t.Helper()
		if err := f.db.Model(&dbstore.Memory{}).Where("id = ?", id).Updates(map[string]any{"tier": "episodic", "epistemic_type": "opinion", "confidence": 0.61, "stability": 137, "citation_count": 7, "defeasibility": "slow"}).Error; err != nil {
			t.Fatal("synthetic lifecycle seed failed")
		}
	}
	row := func(t *testing.T, id int64) dbstore.Memory {
		t.Helper()
		var result dbstore.Memory
		if err := f.db.Where("id = ?", id).First(&result).Error; err != nil {
			t.Fatal("independent row readback failed")
		}
		return result
	}
	t.Run("control_missing_auth", func(t *testing.T) {
		_, err := call("", memoryTestProjectA, "lifecycle", "info", 91002, nil)
		if status.Code(err) != codes.Unauthenticated {
			t.Fatal("missing auth was not denied")
		}
	})
	t.Run("control_missing_project", func(t *testing.T) {
		_, err := call(memoryTestTokenB, "", "lifecycle", "info", 91002, nil)
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatal("project-free lifecycle was not rejected")
		}
	})
	t.Run("control_session_memory_surface", func(t *testing.T) {
		if sessionMemoryTool("lifecycle") {
			t.Fatal("session-memory unexpectedly forwards lifecycle")
		}
	})
	t.Run("control_readonly_mutation_denied", func(t *testing.T) {
		seed(t, 91002)
		before := row(t, 91002)
		r, err := call(memoryTestTokenA, memoryTestProjectA, "lifecycle", "set_confidence", 91002, map[string]any{"confidence": 0.23, "reason": "synthetic check"})
		if err != nil || r == nil || !r.IsError || !strings.Contains(string(r.ContentJson), "read_only") {
			t.Fatal("read-only mutation gate failed")
		}
		if row(t, 91002).Confidence != before.Confidence {
			t.Fatal("read-only denial mutated row")
		}
	})
	t.Run("control_ordinary_recall_hides_foreign_private", func(t *testing.T) {
		r, err := call(memoryTestTokenA, memoryTestProjectA, "recall", "search", 0, map[string]any{"query": "", "limit": 20})
		b := body(t, r, err)
		rows, ok := b["memories"].([]any)
		if !ok {
			t.Fatal("recall rows missing")
		}
		seenOwn := false
		for _, v := range rows {
			m := v.(map[string]any)
			id := int64(m["id"].(float64))
			if id == 91004 || id == 91003 || id == 91005 || id >= 92000 {
				t.Fatal("ordinary recall visibility control failed")
			}
			if id == 91002 {
				seenOwn = true
			}
		}
		if !seenOwn {
			t.Fatal("owner private recall control missing")
		}
	})
	t.Run("control_shared_team_write", func(t *testing.T) {
		seed(t, 91001)
		r, err := call(memoryTestTokenB, memoryTestProjectA, "lifecycle", "set_confidence", 91001, map[string]any{"confidence": 0.23, "reason": "synthetic shared write"})
		_ = body(t, r, err)
		if row(t, 91001).Confidence != 0.23 {
			t.Fatal("authorized shared-team write was not persisted")
		}
	})
	t.Run("control_original_owner_readonly_metadata", func(t *testing.T) {
		if len(row(t, 92002).SourceSessions) != 0 {
			t.Fatal("positive must retain original sessionless owner row")
		}
		for _, action := range []string{"info", "decay_preview"} {
			r, err := call(readonlyOwnerB, memoryTestProjectB, "lifecycle", action, 92002, nil)
			_ = body(t, r, err)
		}
		r, err := call(readonlyOwnerB, memoryTestProjectB, "recall", "search", 0, map[string]any{"query": "", "limit": 20})
		b := body(t, r, err)
		seen := false
		for _, value := range b["memories"].([]any) {
			seen = seen || int64(value.(map[string]any)["id"].(float64)) == 92002
		}
		if !seen {
			t.Fatal("ordinary reader must expose the same original owner row")
		}
	})
	t.Run("control_owner_private_write", func(t *testing.T) {
		if err := f.db.Model(&dbstore.Memory{}).Where("id = ?", 92002).Update("source_sessions", "{fixture-a}").Error; err != nil {
			t.Fatal(err)
		}
		seed(t, 92002)
		r, err := call(memoryTestTokenB, memoryTestProjectB, "lifecycle", "set_confidence", 92002, map[string]any{"confidence": 0.23, "reason": "synthetic owner write"})
		_ = body(t, r, err)
		if row(t, 92002).Confidence != 0.23 {
			t.Fatal("authorized owner write was not persisted")
		}
	})
	t.Run("control_readonly_owner_metadata", func(t *testing.T) {
		seed(t, 91002)
		for _, action := range []string{"info", "decay_preview"} {
			r, err := call(memoryTestTokenA, memoryTestProjectA, "lifecycle", action, 91002, nil)
			_ = body(t, r, err)
		}
	})
	t.Run("deny_owner_wrong_session", func(t *testing.T) {
		if err := f.db.Model(&dbstore.Memory{}).Where("id = ?", 92002).Update("source_sessions", "{other-session}").Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := f.db.Model(&dbstore.Memory{}).Where("id = ?", 92002).Update("source_sessions", "{fixture-a}").Error; err != nil {
				t.Error(err)
			}
		})
		seed(t, 92002)
		before := row(t, 92002)
		var beforeHistory int64
		if err := f.db.Table("promotion_log").Where("memory_id = ?", 92002).Count(&beforeHistory).Error; err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"promote", "demote", "set_confidence", "set_defeasibility"} {
			target := "semantic"
			if action == "demote" {
				target = "working"
			}
			r, err := call(memoryTestTokenB, memoryTestProjectB, "lifecycle", action, 92002, map[string]any{"confidence": 0.23, "reason": "wrong session", "target_tier": target, "defeasibility": "rapid"})
			after := row(t, 92002)
			if err != nil || r == nil || !r.IsError || after.Confidence != before.Confidence || after.Tier != before.Tier || after.Defeasibility != before.Defeasibility {
				t.Fatal("same principal/workstation with a foreign session must not mutate private memory")
			}
		}
		var afterHistory int64
		if err := f.db.Table("promotion_log").Where("memory_id = ?", 92002).Count(&afterHistory).Error; err != nil || afterHistory != beforeHistory {
			t.Fatal("wrong-session denial must preserve history")
		}
	})
	t.Run("control_tier_history_and_rollback", func(t *testing.T) {
		seed(t, 91001)
		for _, step := range []struct{ action, target string }{{"promote", "semantic"}, {"demote", "episodic"}} {
			r, err := call(memoryTestTokenB, memoryTestProjectA, "lifecycle", step.action, 91001, map[string]any{"target_tier": step.target})
			_ = body(t, r, err)
			if row(t, 91001).Tier != step.target {
				t.Fatal("authorized tier change was not persisted")
			}
		}
		var history int64
		if err := f.db.Table("promotion_log").Where("memory_id = ?", 91001).Count(&history).Error; err != nil || history != 2 {
			t.Fatal("each successful tier change must persist exactly one history entry")
		}
		if err := f.db.Exec(`CREATE TRIGGER reject_lifecycle_history BEFORE INSERT ON promotion_log BEGIN SELECT RAISE(ABORT, 'synthetic history failure'); END`).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.db.Exec("DROP TRIGGER reject_lifecycle_history").Error })
		before := row(t, 91001)
		r, err := call(memoryTestTokenB, memoryTestProjectA, "lifecycle", "promote", 91001, map[string]any{"target_tier": "semantic"})
		if err != nil || r == nil || !r.IsError || row(t, 91001).Tier != before.Tier {
			t.Fatal("history failure must reject and roll back the tier change")
		}
		if err := f.db.Table("promotion_log").Where("memory_id = ?", 91001).Count(&history).Error; err != nil || history != 2 {
			t.Fatal("failed tier change must not append history")
		}
	})
	for _, c := range []struct {
		name string
		id   int64
	}{{"foreign_principal_same_project", 91004}, {"foreign_workstation_same_project", 91003}, {"foreign_domain_same_project", 91005}, {"foreign_private_other_project", 92002}} {
		for _, action := range []string{"info", "decay_preview"} {
			t.Run("deny_"+c.name+"_"+action, func(t *testing.T) {
				seed(t, c.id)
				target, readErr := dbstore.NewMemoryStore(&dbstore.Store{DB: f.db}).Get(t.Context(), c.id)
				if readErr != nil {
					t.Fatal("policy oracle materialization failed")
				}
				caller := scope.KeycardContext{Principal: "agent/local-a", PrincipalKind: "agent", WorkstationID: "fixture-card-a", SessionID: "fixture-a"}
				if scope.ResolveMemory(caller, target, scope.MemoryVisibilityOptions{ApplyPrivacyScope: true}) {
					t.Fatal("negative fixture does not violate existing privacy policy")
				}
				r, err := call(memoryTestTokenA, memoryTestProjectA, "lifecycle", action, c.id, nil)
				if err != nil {
					t.Fatal("transport failed before actual MCP authorization")
				}
				if r == nil || r.IsError {
					return
				}
				b := body(t, r, nil)
				_, hasInfo := b["confidence"]
				_, hasPreview := b["preview"]
				if hasInfo || hasPreview {
					t.Logf("OBSERVED unauthorized lifecycle metadata; action=%s; body_returned=false; source_project_enforced=true", action)
					t.Error("foreign private/domain/project selector returned lifecycle metadata")
				} else {
					t.Error("foreign selector unexpectedly succeeded without expected metadata")
				}
			})
		}
	}
	for _, c := range []struct {
		action, field string
		value         any
		extra         map[string]any
	}{
		{"promote", "tier", "semantic", map[string]any{"target_tier": "semantic"}},
		{"demote", "tier", "working", map[string]any{"target_tier": "working"}},
		{"set_confidence", "confidence", 0.23, map[string]any{"confidence": 0.23, "reason": "synthetic foreign write"}},
		{"set_defeasibility", "defeasibility", "rapid", map[string]any{"defeasibility": "rapid"}},
	} {
		t.Run("deny_foreign_private_mutation_"+c.action, func(t *testing.T) {
			seed(t, 91002)
			before := row(t, 91002)
			var historyBefore int64
			if f.db.Table("promotion_log").Where("memory_id = ?", 91002).Count(&historyBefore).Error != nil {
				t.Fatal("promotion count failed")
			}
			target, readErr := dbstore.NewMemoryStore(&dbstore.Store{DB: f.db}).Get(t.Context(), 91002)
			if readErr != nil {
				t.Fatal("policy oracle materialization failed")
			}
			caller := scope.KeycardContext{Principal: "agent/local-b", PrincipalKind: "agent", WorkstationID: "fixture-card-b", SessionID: "fixture-a"}
			if scope.ResolveMemory(caller, target, scope.MemoryVisibilityOptions{ApplyPrivacyScope: true}) {
				t.Fatal("negative fixture does not violate existing privacy policy")
			}
			r, err := call(memoryTestTokenB, memoryTestProjectA, "lifecycle", c.action, 91002, c.extra)
			if err != nil {
				t.Fatal("transport failed before actual MCP mutation")
			}
			after := row(t, 91002)
			changed := after.Tier != before.Tier || after.Confidence != before.Confidence || after.Defeasibility != before.Defeasibility
			var historyAfter int64
			if f.db.Table("promotion_log").Where("memory_id = ?", 91002).Count(&historyAfter).Error != nil {
				t.Fatal("promotion count failed")
			}
			contentStable := before.Content == after.Content && before.OwnerPrincipal == after.OwnerPrincipal && before.AgentVisibility == after.AgentVisibility && before.Project == after.Project
			t.Logf("OBSERVED action=%s accepted=%t persisted_change=%t unrelated_fields_preserved=%t promotion_delta=%d", c.action, r != nil && !r.IsError, changed, contentStable, historyAfter-historyBefore)
			if r == nil || !r.IsError || changed || historyAfter != historyBefore {
				t.Error("foreign writable principal bypassed private-record lifecycle authorization")
			}
		})
	}
}
