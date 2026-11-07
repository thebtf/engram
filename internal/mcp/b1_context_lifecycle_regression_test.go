package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
)

// Feature009 B1/B3/B4: use ENGRAM_SEARCH_PRIVACY_TEST_DSN and the existing
// isolated-schema fixture, never an inherited DATABASE_DSN. The parent runs
// these serially. Preparation uses the authoritative REST CreateInContext
// writer, not a fabricated MCP self-save API. The exercised boundary is actual
// HandleRequest -> tools/call -> lifecycle dispatch, not HTTP/gRPC transport.
func TestB1ContextLifecycleReadsPostgres(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(b1LifecycleAuthMode(disabled), func(t *testing.T) {
			f := newB1LifecycleFixture(t, disabled)
			for _, kind := range []string{"self_shared", "self_private", "global_private"} {
				t.Run(kind, func(t *testing.T) {
					target := f.ownCard(t, kind)
					f.requireOwnerRead(t, target)
					for _, action := range []string{"info", "decay_preview"} {
						t.Run(action, func(t *testing.T) {
							before := f.snapshot(t)
							missingID := f.missingID(t)
							missing := f.call(t, f.otherContext(t), action, missingID, nil)
							hidden := f.call(t, f.otherContext(t), action, target.ID, nil)
							b1LifecycleRequireHidden(t, hidden, missing, target, missingID)
							assert.Equal(t, before, f.snapshot(t), "read denials must not mutate rows, projects, audit or promotion history")
							f.requireOwnerRead(t, target)
						})
					}
				})
			}
			// No self-context lifecycle envelope is invented here. The supported
			// typed owner reader above and actual legacy-project MCP reader below
			// separately establish positive controls at their real boundaries.
			t.Run("authorized_legacy_project", func(t *testing.T) {
				target := f.legacyCard(t)
				before := f.snapshot(t)
				info := b1LifecycleBody(t, f.call(t, f.ownerProjectContext(t), "info", target.ID, nil))
				assert.EqualValues(t, target.ID, info["id"])
				assert.Equal(t, "episodic", info["tier"])
				assert.Equal(t, "opinion", info["epistemic_type"])
				assert.InDelta(t, 0.61, info["confidence"], 0.000001)
				assert.InDelta(t, 137, info["stability"], 0.000001)
				assert.EqualValues(t, 7, info["citation_count"])
				assert.Equal(t, "active", info["status"])
				preview := b1LifecycleBody(t, f.call(t, f.ownerProjectContext(t), "decay_preview", target.ID, nil))
				assert.EqualValues(t, target.ID, preview["memory_id"])
				assert.Greater(t, preview["stability"].(float64), float64(0))
				points, ok := preview["preview"].([]any)
				require.True(t, ok, "preview must contain actual decay samples")
				require.Len(t, points, 11, "the requested ten-day preview includes both endpoints")
				previous := 2.0
				for day, value := range points {
					point, ok := value.(map[string]any)
					require.True(t, ok)
					assert.EqualValues(t, day, point["day"])
					r, ok := point["retrievability"].(float64)
					require.True(t, ok)
					assert.GreaterOrEqual(t, r, float64(0))
					assert.LessOrEqual(t, r, float64(1))
					assert.Less(t, r, previous, "positive stability must produce a decreasing real preview")
					previous = r
				}
				assert.Equal(t, before, f.snapshot(t), "authorized reads must also be side-effect free")
			})
		})
	}
}

func TestB1ContextLifecycleMutationsPostgres(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(b1LifecycleAuthMode(disabled), func(t *testing.T) {
			f := newB1LifecycleFixture(t, disabled)
			for _, action := range []struct {
				name   string
				fields map[string]any
				field  string
				value  any
			}{
				{"promote", map[string]any{"target_tier": "semantic"}, "tier", "semantic"},
				{"demote", map[string]any{"target_tier": "working"}, "tier", "working"},
				{"set_confidence", map[string]any{"confidence": 0.23, "reason": "synthetic lifecycle privacy regression"}, "confidence", 0.23},
				{"set_defeasibility", map[string]any{"defeasibility": "rapid"}, "defeasibility", "rapid"},
			} {
				t.Run(action.name, func(t *testing.T) {
					for _, kind := range []string{"self_shared", "self_private", "global_private"} {
						t.Run(kind, func(t *testing.T) {
							target := f.ownCard(t, kind)
							f.requireOwnerRead(t, target)
							missingID := f.missingID(t)
							before := f.snapshot(t)
							missing := f.call(t, f.otherContext(t), action.name, missingID, action.fields)
							hidden := f.call(t, f.otherContext(t), action.name, target.ID, action.fields)
							b1LifecycleRequireHidden(t, hidden, missing, target, missingID)
							assert.Equal(t, before, f.snapshot(t), "foreign writable caller must leave complete memory rows, counts and history unchanged")
							// Independent real-store readback still executes when denial
							// assertions fail, exposing any raw-ID state change.
							f.requireOwnerRead(t, target)
						})
					}
					t.Run("authorized_legacy_owner", func(t *testing.T) {
						target := f.legacyCard(t)
						before := f.row(t, target.ID)
						var promotionsBefore int64
						require.NoError(t, f.store.DB.Table("promotion_log").Where("memory_id = ?", target.ID).Count(&promotionsBefore).Error)
						body := b1LifecycleBody(t, f.call(t, f.ownerProjectContext(t), action.name, target.ID, action.fields))
						assert.EqualValues(t, target.ID, body["memory_id"])
						after := f.row(t, target.ID)
						assert.Equal(t, action.value, after[action.field], "owner positive control must persist the requested change, not merely return success")
						assert.NotEqual(t, before[action.field], after[action.field])
						delete(before, action.field)
						delete(after, action.field)
						delete(before, "updated_at")
						delete(after, "updated_at")
						assert.Equal(t, before, after, "authorized lifecycle change must preserve content, ownership, context and unrelated fields")
						var promotionsAfter int64
						require.NoError(t, f.store.DB.Table("promotion_log").Where("memory_id = ?", target.ID).Count(&promotionsAfter).Error)
						wantDelta := int64(0)
						if action.name == "promote" || action.name == "demote" {
							wantDelta = 1
						}
						assert.Equal(t, promotionsBefore+wantDelta, promotionsAfter, "authorized tier transitions retain their real promotion audit")
					})
				})
			}
		})
	}
}

func b1LifecycleAuthMode(disabled bool) string {
	if disabled {
		return "auth_disabled_explicit_keycards"
	}
	return "auth_enabled"
}

type b1LifecycleFixture struct {
	store        *gormdb.Store
	memories     *gormdb.MemoryStore
	server       *Server
	owner        auth.Identity
	other        auth.Identity
	ownerProject string
	otherProject string
}

func newB1LifecycleFixture(t *testing.T, disabled bool) *b1LifecycleFixture {
	t.Helper()
	t.Cleanup(func() { reloadConfig(t) })
	t.Setenv("ENGRAM_AUTH_DISABLED", strconv.FormatBool(disabled))
	t.Setenv("ENGRAM_ENFORCE_SOURCE_PROJECT", "true")
	t.Setenv("ENGRAM_LIFECYCLE_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	reloadConfig(t)
	store := openRecallProjectPrivacyStore(t)
	memories := gormdb.NewMemoryStore(store)
	f := &b1LifecycleFixture{
		store: store, memories: memories, server: NewServer(ServerOptions{Version: "b1-lifecycle-regression"}),
		ownerProject: uuid.NewString(), otherProject: uuid.NewString(),
	}
	f.server.SetMemoryStore(memories)
	f.server.SetPromotionStore(gormdb.NewPromotionStore(store.DB))
	require.NoError(t, store.DB.Create(&[]gormdb.Project{{ID: f.ownerProject}, {ID: f.otherProject}}).Error)
	users := gormdb.NewUserStore(store.DB)
	admin, err := users.CreateUser("b1-lifecycle-"+uuid.NewString()+"@example.invalid", "synthetic-unused-password-hash", gormdb.DashboardRoleAdmin)
	require.NoError(t, err)
	sessions := gormdb.NewAuthSessionStore(store.DB)
	session, err := sessions.CreateSession(admin.ID, time.Hour, "b1-lifecycle-enrollment", "127.0.0.1")
	require.NoError(t, err)
	tokens := gormdb.NewTokenStore(store)
	validator := auth.NewValidator(uuid.NewString(), tokens)
	issue := func(label string) auth.Identity {
		t.Helper()
		principal := "agent/b1-lifecycle-" + label + "-" + uuid.NewString()
		raw := auth.TokenRawPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
		hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.MinCost)
		require.NoError(t, err)
		_, err = tokens.CreateWithPrincipal(t.Context(), gormdb.TokenCreatePrincipalInput{
			Name: label, TokenHash: string(hash),
			TokenPrefix: raw[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen],
			Scope:       "read-write", Principal: principal, PrincipalKind: "agent",
		})
		require.NoError(t, err)
		identity, err := validator.Validate(t.Context(), raw)
		require.NoError(t, err)
		require.Equal(t, auth.SourceClient, identity.Source)
		require.Equal(t, auth.RoleReadWrite, identity.Role, "a read-only gate must not mask lifecycle authorization")
		require.Equal(t, principal, identity.Principal)
		require.Equal(t, auth.PrincipalKindAgent, identity.PrincipalKind)
		require.NotEmpty(t, identity.WorkstationID())
		created, err := memories.EnrollPrincipal(t.Context(), "agent", principal, admin.ID, session.ID)
		require.NoError(t, err)
		require.True(t, created, "prepare enrollment through the actual persisted browser-admin guard")
		return identity
	}
	f.owner, f.other = issue("owner"), issue("other")
	return f
}

func b1LifecycleCaller(identity auth.Identity) scope.KeycardContext {
	return scope.KeycardContext{Principal: identity.Principal, PrincipalKind: string(identity.PrincipalKind), WorkstationID: identity.WorkstationID()}
}

func (f *b1LifecycleFixture) ownCard(t *testing.T, kind string) *models.Memory {
	t.Helper()
	owning := &models.MemoryContext{Kind: models.MemoryContextSelf, Principal: f.owner.Principal, PrincipalKind: string(f.owner.PrincipalKind)}
	visibility := models.AgentVisibilityShared
	if kind == "global_private" {
		owning = &models.MemoryContext{Kind: models.MemoryContextGlobal}
	}
	if kind != "self_shared" {
		visibility = models.AgentVisibilityPrivate
	}
	var projectsBefore int64
	require.NoError(t, f.store.DB.Table("projects").Count(&projectsBefore).Error)
	created, replayed, err := f.memories.CreateInContext(auth.WithIdentity(t.Context(), f.owner), &models.Memory{
		Context: owning, Content: "Synthetic confidential lifecycle card " + uuid.NewString(),
		Tags: []string{"type:fact", "b1-lifecycle-" + uuid.NewString()}, SourceAgent: "synthetic-client-provenance",
		OwnerPrincipal: f.owner.Principal, OwnerPrincipalKind: string(f.owner.PrincipalKind),
		AgentVisibility: visibility, PrivacyScope: "global", SourceWorkstationID: f.owner.WorkstationID(),
	}, b1LifecycleCaller(f.owner), "", nil)
	require.NoError(t, err)
	require.False(t, replayed)
	require.Positive(t, created.ID)
	require.Empty(t, created.Project, "typed self/global cards must not acquire a fake project")
	require.Equal(t, owning, created.Context)
	require.Equal(t, visibility, created.AgentVisibility)
	var projectsAfter int64
	require.NoError(t, f.store.DB.Table("projects").Count(&projectsAfter).Error)
	require.Equal(t, projectsBefore, projectsAfter)
	f.seedLifecycle(t, created.ID)
	return created
}

func (f *b1LifecycleFixture) legacyCard(t *testing.T) *models.Memory {
	t.Helper()
	created, err := f.memories.CreateForExistingProject(auth.WithIdentity(t.Context(), f.owner), &models.Memory{
		Project: f.ownerProject, Content: "Synthetic authorized legacy lifecycle card " + uuid.NewString(),
		OwnerPrincipal: f.owner.Principal, OwnerPrincipalKind: string(f.owner.PrincipalKind),
		AgentVisibility: models.AgentVisibilityPrivate, PrivacyScope: "project", SourceWorkstationID: f.owner.WorkstationID(),
	})
	require.NoError(t, err)
	require.Nil(t, created.Context, "positive control retains actual legacy project semantics")
	f.seedLifecycle(t, created.ID)
	return created
}

func (f *b1LifecycleFixture) seedLifecycle(t *testing.T, id int64) {
	t.Helper()
	require.NoError(t, f.memories.UpdateLifecycleFields(t.Context(), id, map[string]any{
		"tier": "episodic", "epistemic_type": "opinion", "confidence": 0.61,
		"stability": 137, "citation_count": 7, "defeasibility": "slow",
	}))
}

func (f *b1LifecycleFixture) requireOwnerRead(t *testing.T, target *models.Memory) {
	t.Helper()
	read, err := f.memories.GetInContext(auth.WithIdentity(t.Context(), f.owner), target.ID, *target.Context, "", b1LifecycleCaller(f.owner))
	require.NoError(t, err, "legitimate owner must read through the supported typed reader")
	assert.Equal(t, target.ID, read.ID)
	assert.Equal(t, target.Content, read.Content)
	assert.Equal(t, target.Tags, read.Tags)
	assert.Equal(t, target.Context, read.Context)
	assert.Equal(t, target.OwnerPrincipal, read.OwnerPrincipal)
	assert.Equal(t, target.AgentVisibility, read.AgentVisibility)
	assert.Empty(t, read.Project)
	assert.Equal(t, "episodic", read.Tier, "denied calls must not change the real tier")
	assert.InDelta(t, 0.61, read.Confidence, 0.000001, "denied calls must not change confidence")
	assert.Equal(t, "slow", read.Defeasibility, "denied calls must not change defeasibility")
}

func (f *b1LifecycleFixture) otherContext(t *testing.T) context.Context {
	t.Helper()
	return ContextWithProject(ContextWithSession(auth.WithIdentity(t.Context(), f.other), "b1-other-"+uuid.NewString()), f.otherProject)
}

func (f *b1LifecycleFixture) ownerProjectContext(t *testing.T) context.Context {
	t.Helper()
	return ContextWithProject(ContextWithSession(auth.WithIdentity(t.Context(), f.owner), "b1-owner-"+uuid.NewString()), f.ownerProject)
}

func (f *b1LifecycleFixture) call(t *testing.T, ctx context.Context, action string, id int64, fields map[string]any) *Response {
	t.Helper()
	arguments := map[string]any{"action": action, "memory_id": id, "days_ahead": 10}
	for key, value := range fields {
		arguments[key] = value
	}
	response := f.server.HandleRequest(ctx, &Request{
		JSONRPC: "2.0", ID: 1, Method: "tools/call",
		Params: mustJSON(t, map[string]any{"name": "lifecycle", "arguments": arguments}),
	})
	require.NotNil(t, response)
	require.Equal(t, "2.0", response.JSONRPC)
	return response
}

func b1LifecycleBody(t *testing.T, response *Response) map[string]any {
	t.Helper()
	require.Nil(t, response.Error, "authorized legacy-project action must actually reach and succeed at lifecycle")
	raw, err := json.Marshal(response.Result)
	require.NoError(t, err)
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(raw, &envelope))
	require.Len(t, envelope.Content, 1)
	require.Equal(t, "text", envelope.Content[0].Type)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(envelope.Content[0].Text), &body))
	return body
}

func b1LifecycleRequireHidden(t *testing.T, hidden, missing *Response, target *models.Memory, missingID int64) {
	t.Helper()
	for label, response := range map[string]*Response{"foreign": hidden, "nonexistent": missing} {
		assert.Nil(t, response.Result, "%s selector must not return a result body or metadata", label)
		if assert.NotNil(t, response.Error, "%s selector must be denied at actual MCP dispatch", label) {
			assert.Equal(t, -32000, response.Error.Code)
			assert.Contains(t, strings.ToLower(response.Error.Message), "not found", "unavailable and absent IDs must share the established error class")
			assert.NotContains(t, response.Error.Message, "read_only")
			assert.NotContains(t, response.Error.Message, "unknown")
		}
		raw, err := json.Marshal(response)
		require.NoError(t, err)
		for _, secret := range []string{target.Content, target.OwnerPrincipal, target.SourceWorkstationID, target.Tags[1]} {
			assert.NotContains(t, string(raw), secret, "%s response must not disclose hidden card metadata", label)
		}
		for _, key := range []string{"tier", "confidence", "stability", "citation_count", "created_at", "preview", "context", "owner_principal"} {
			assert.NotContains(t, string(raw), `\"`+key+`\"`, "%s MCP text must not contain lifecycle metadata", label)
			assert.NotContains(t, string(raw), `"`+key+`"`, "%s structured error data must not contain lifecycle metadata", label)
		}
	}
	if hidden.Error != nil && missing.Error != nil {
		// ID echoes are caller-known selectors, not evidence of existence.
		normalize := func(e *Error) string {
			raw, err := json.Marshal(e)
			require.NoError(t, err)
			text := string(raw)
			for _, id := range []int64{target.ID, missingID} {
				if id > 0 {
					for _, prefix := range []string{"memory ", "memory id=", "memory_id="} {
						text = strings.ReplaceAll(text, prefix+strconv.FormatInt(id, 10), prefix+"<id>")
					}
				}
			}
			return text
		}
		assert.Equal(t, normalize(missing.Error), normalize(hidden.Error), "error message, code and data must not distinguish an invisible ID from an absent ID")
	}
}

func (f *b1LifecycleFixture) missingID(t *testing.T) int64 {
	t.Helper()
	var id int64
	require.NoError(t, f.store.DB.Raw("SELECT COALESCE(MAX(id), 0) + 1000000 FROM memories").Scan(&id).Error)
	return id
}

func (f *b1LifecycleFixture) row(t *testing.T, id int64) map[string]any {
	t.Helper()
	var raw string
	require.NoError(t, f.store.DB.Raw("SELECT to_jsonb(m)::text FROM memories m WHERE id = ?", id).Scan(&raw).Error)
	require.NotEmpty(t, raw)
	var row map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &row))
	return row
}

func (f *b1LifecycleFixture) snapshot(t *testing.T) map[string]string {
	t.Helper()
	result := make(map[string]string)
	for _, table := range []string{"memories", "projects", "promotion_log", "audit_log"} {
		var raw string
		require.NoError(t, f.store.DB.Raw(fmt.Sprintf("SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id), '[]'::jsonb)::text FROM %s r", table)).Scan(&raw).Error)
		result[table] = raw
	}
	return result
}
