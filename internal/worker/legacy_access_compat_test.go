package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/principalmemory"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func TestLegacyKeycardPrincipalMemoryWorkstationCompatibilitySQLite(t *testing.T) {
	runLegacyKeycardPrincipalMemoryWorkstationCompatibility(t, memoryMutationSQLiteDB(t), false)
}

func TestLegacyKeycardPrincipalMemoryWorkstationCompatibilityPostgres(t *testing.T) {
	store := openContextSearchPrivacyStore(t)
	runLegacyKeycardPrincipalMemoryWorkstationCompatibility(t, store.DB, true)
}

func runLegacyKeycardPrincipalMemoryWorkstationCompatibility(t *testing.T, db *gorm.DB, postgres bool) {
	t.Helper()
	f := newMemoryMutationSecurityFixture(t, db)
	f.service.principalMemoryQueryService = principalmemory.NewPrincipalMemoryQueryService(f.service.memoryStore, f.service.auditStore)

	var cards knowledgeFixtureTokens
	newCard := func(id string) string {
		t.Helper()
		var secret [16]byte
		_, err := rand.Read(secret[:])
		require.NoError(t, err)
		raw := auth.TokenRawPrefix + hex.EncodeToString(secret[:])
		hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.MinCost)
		require.NoError(t, err)
		cards = append(cards, gormdb.APIToken{ID: id, TokenPrefix: raw[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen], TokenHash: string(hash), Scope: "read-write"})
		return raw
	}
	const ownerCard = "synthetic-legacy-workstation-owner"
	ownerToken := newCard(ownerCard)
	otherToken := newCard("synthetic-legacy-workstation-other")
	validator := auth.NewValidator("", cards)
	for _, card := range []struct{ token, id string }{{ownerToken, ownerCard}, {otherToken, "synthetic-legacy-workstation-other"}} {
		identity, err := validator.Validate(context.Background(), card.token)
		require.NoError(t, err)
		require.Equal(t, auth.SourceClient, identity.Source)
		require.Equal(t, card.id, identity.WorkstationID())
		_, _, hasOwner := identity.MemoryOwner()
		require.False(t, hasOwner)
	}
	f.tokenAuth.SetValidator(validator)

	const target = "agent/synthetic-legacy-query-owner"
	insert := `INSERT INTO memories (id, project, content, tags, status, context_kind, context_principal, context_principal_kind, privacy_scope, source_workstation_id, source_sessions, owner_principal, owner_principal_kind, agent_visibility, domain, created_at, updated_at, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ?)`
	for _, row := range []struct {
		id               int
		content, privacy string
	}{{921001, "synthetic legacy private marker", "private"}, {921002, "synthetic legacy project marker", "project"}} {
		require.NoError(t, db.Exec(insert, row.id, f.project, row.content, "[]", "active", "legacy", "", "", row.privacy, ownerCard, "{}", target, "agent", "shared", "", 1).Error)
	}
	enrolledBy := int64(1)
	if postgres {
		user, err := gormdb.NewUserStore(db).CreateUser("legacy-compat@example.invalid", "synthetic-unused-hash", gormdb.DashboardRoleAdmin)
		require.NoError(t, err)
		enrolledBy = user.ID
	}
	require.NoError(t, db.Exec(`INSERT INTO memory_principals (principal_kind, principal, enrolled_by) VALUES (?, ?, ?)`, "agent", target, enrolledBy).Error)
	for _, row := range []struct {
		id                                           int
		kind, contextPrincipal, contextKind, privacy string
	}{
		{921003, "self", target, "agent", "project"},
		{921004, "global", "", "", "project"},
		{921005, "project", "", "", "private"},
	} {
		project := ""
		if row.kind == "project" {
			project = f.project
		}
		require.NoError(t, db.Exec(insert, row.id, project, "synthetic typed "+row.kind+" marker", "[]", "active", row.kind, row.contextPrincipal, row.contextKind, row.privacy, ownerCard, "{}", target, "agent", "shared", "", 1).Error)
	}
	server := httptest.NewServer(f.service.router)
	t.Cleanup(server.Close)
	query := func(t *testing.T, token, term string) principalMemoryQueryResponse {
		t.Helper()
		values := url.Values{"principal": {target}, "principal_kind": {"agent"}, "q": {term}}
		values.Set("session_id", "synthetic-untrusted-session")
		request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/api/memories/principal?"+values.Encode(), nil)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
		var result principalMemoryQueryResponse
		require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
		return result
	}
	t.Run("project_visible_control", func(t *testing.T) {
		for _, token := range []string{ownerToken, otherToken} {
			result := query(t, token, "synthetic legacy project marker")
			require.Len(t, result.Items, 1)
			require.EqualValues(t, 921002, result.Items[0].ID)
		}
	})
	t.Run("different_workstation_excluded", func(t *testing.T) {
		result := query(t, otherToken, "synthetic legacy private marker")
		require.Empty(t, result.Items)
	})
	t.Run("typed_contexts_remain_excluded", func(t *testing.T) {
		for _, kind := range []string{"self", "global", "project"} {
			t.Run(kind, func(t *testing.T) {
				result := query(t, ownerToken, "synthetic typed "+kind+" marker")
				require.Empty(t, result.Items)
			})
		}
	})
	t.Run("same_workstation_visible", func(t *testing.T) {
		result := query(t, ownerToken, "synthetic legacy private marker")
		require.Len(t, result.Items, 1)
		require.EqualValues(t, 921001, result.Items[0].ID)
	})
}
