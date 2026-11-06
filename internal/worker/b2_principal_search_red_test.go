package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/principalmemory"
)

func TestB2OwnCardSearchReplacementCredential(t *testing.T) {
	db := memoryMutationSQLiteDB(t)
	f := newMemoryMutationSecurityFixture(t, db)
	f.service.principalMemoryQueryService = principalmemory.NewPrincipalMemoryQueryService(f.service.memoryStore, f.service.auditStore)
	require.NoError(t, db.Exec(`INSERT INTO memory_principals (principal_kind, principal, enrolled_by) VALUES (?, ?, ?), (?, ?, ?)`, "agent", "agent/synthetic-security-a", 1, "agent", "agent/synthetic-security-b", 1).Error)
	var projectsBefore int64
	require.NoError(t, db.Model(&gormdb.Project{}).Count(&projectsBefore).Error)
	create := func(token, content string) struct{ ID int64 } {
		t.Helper()
		response := f.serve(f.request(t, context.Background(), http.MethodPost, "/api/memories", token, map[string]any{"content": content, "tags": []string{"type:fact"}, "agent_visibility": "private"}))
		require.Equal(t, http.StatusCreated, response.status, response.body)
		var record struct {
			ID int64 `json:"id"`
		}
		require.NoError(t, json.Unmarshal([]byte(response.body), &record))
		require.Positive(t, record.ID)
		return struct{ ID int64 }{record.ID}
	}
	english := create(f.tokenA, "When the harbor lantern fails, replace its blue battery before dusk.")
	russian := create(f.tokenA, "Маяк хранит запасной аккумулятор в синем ящике у причала.")
	hidden := create(f.tokenB, "The harbor lantern secret is hidden in the private red cabinet.")
	create(f.tokenA, "If the kitchen timer stops working, replace its spring.")
	russianDistractor := create(f.tokenA, "Где находится ключ для кладовки: на полке в коридоре.")
	var projectsAfter int64
	require.NoError(t, db.Model(&gormdb.Project{}).Count(&projectsAfter).Error)
	require.Equal(t, projectsBefore, projectsAfter)

	query := func(token string, values url.Values) memoryMutationHTTPResult {
		t.Helper()
		return f.serve(f.request(t, context.Background(), http.MethodGet, "/api/memories/principal?"+values.Encode(), token, nil))
	}
	decode := func(t *testing.T, response memoryMutationHTTPResult) principalMemoryQueryResponse {
		t.Helper()
		require.Equal(t, http.StatusOK, response.status, response.body)
		var result principalMemoryQueryResponse
		require.NoError(t, json.Unmarshal([]byte(response.body), &result))
		return result
	}
	assertHidden := func(t *testing.T, response memoryMutationHTTPResult) {
		t.Helper()
		assert.NotContains(t, response.body, "private red cabinet")
		assert.NotContains(t, response.body, "agent/synthetic-security-b")
		assert.NotContains(t, response.body, fmt.Sprintf(`"id":%d`, hidden.ID))
	}
	for _, tc := range []struct {
		name, question string
		id             int64
	}{
		{"english", "What should I do if the harbor lantern stops working?", english.ID},
		{"russian", "Где находится запасной аккумулятор для маяка?", russian.ID},
		{"russian_shorter", "Где находится аккумулятор для маяка?", russian.ID},
		{"russian_subject", "Где находится ящик?", russian.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := query(f.tokenA2, url.Values{"q": {tc.question}, "limit": {"1"}})
			assertHidden(t, response)
			result := decode(t, response)
			require.Len(t, result.Items, 1, "the useful own card must survive authorization and limit")
			require.Equal(t, tc.id, result.Items[0].ID)
			read := f.serve(f.request(t, context.Background(), http.MethodGet, fmt.Sprintf("/api/memories/%d", result.Items[0].ID), f.tokenA2, nil))
			require.Equal(t, http.StatusOK, read.status, read.body)
			var card struct {
				ID             int64  `json:"id"`
				Version        int    `json:"version"`
				OwnerPrincipal string `json:"owner_principal"`
				Context        struct {
					Kind      string `json:"kind"`
					Principal string `json:"principal"`
				} `json:"context"`
			}
			require.NoError(t, json.Unmarshal([]byte(read.body), &card))
			assert.Equal(t, tc.id, card.ID)
			assert.Positive(t, card.Version)
			assert.Equal(t, "agent/synthetic-security-a", card.OwnerPrincipal)
			assert.Equal(t, "self", card.Context.Kind)
			assert.Equal(t, "agent/synthetic-security-a", card.Context.Principal)
		})
	}
	t.Run("explicit_principal_control", func(t *testing.T) {
		response := query(f.tokenA2, url.Values{"principal": {"agent/synthetic-security-a"}, "principal_kind": {"agent"}, "q": {"harbor lantern"}})
		assertHidden(t, response)
		result := decode(t, response)
		require.Len(t, result.Items, 1)
		assert.Equal(t, english.ID, result.Items[0].ID)
	})
	t.Run("russian_literal_term", func(t *testing.T) {
		response := query(f.tokenA2, url.Values{"q": {"находится"}, "limit": {"1"}})
		assertHidden(t, response)
		result := decode(t, response)
		require.Len(t, result.Items, 1)
		assert.Equal(t, russianDistractor.ID, result.Items[0].ID)
	})
	t.Run("explicit_principal_invalid_context", func(t *testing.T) {
		response := query(f.tokenA2, url.Values{"principal": {"agent/synthetic-security-a"}, "principal_kind": {"agent"}, "context": {"invalid"}, "q": {"harbor lantern"}})
		assert.Equal(t, http.StatusBadRequest, response.status)
		assert.NotContains(t, response.body, "blue battery")
		assertHidden(t, response)
	})
	t.Run("short_subjects", func(t *testing.T) {
		for _, tc := range []struct {
			name, content, natural, literal string
		}{
			{"go", "go compiler releases", "What is Go?", "Go"},
			{"ci", "ci automation verifies builds", "What is CI?", "CI"},
			{"russian_db", "бд хранит записи", "Что такое бд?", "бд"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				card := create(f.tokenA, tc.content)
				for _, test := range []struct{ name, question string }{{"natural", tc.natural}, {"literal", tc.literal}} {
					t.Run(test.name, func(t *testing.T) {
						response := query(f.tokenA2, url.Values{"q": {test.question}, "limit": {"1"}})
						assertHidden(t, response)
						result := decode(t, response)
						require.Len(t, result.Items, 1, "question %q", test.question)
						assert.Equal(t, card.ID, result.Items[0].ID, "question %q", test.question)
					})
				}
			})
		}
	})
	t.Run("question_terms_after_sixteen_words", func(t *testing.T) {
		card := create(f.tokenA, "astrolabe")
		question := "Could you explain the following background about routine tasks, planning, paperwork, meetings, travel, calendars, communications, scheduling, budgets, invoices, staffing, administration, policies, and then describe astrolabe?"
		response := query(f.tokenA2, url.Values{"q": {question}, "limit": {"1"}})
		result := decode(t, response)
		require.Len(t, result.Items, 1)
		assert.Equal(t, card.ID, result.Items[0].ID)
	})
	t.Run("excessive_question_rejected", func(t *testing.T) {
		response := query(f.tokenA2, url.Values{"q": {strings.Repeat("orbit ", 180)}})
		assert.Equal(t, http.StatusBadRequest, response.status)
		assertHidden(t, response)
	})
	t.Run("too_many_distinct_terms_rejected", func(t *testing.T) {
		var terms []string
		for i := range 65 {
			terms = append(terms, fmt.Sprintf("token%03d", i))
		}
		question := strings.Join(terms, " ")
		require.Less(t, len(question), 1024)
		response := query(f.tokenA2, url.Values{"q": {question}})
		assert.Equal(t, http.StatusBadRequest, response.status)
		assert.Contains(t, response.body, "too many distinct terms")
		assertHidden(t, response)
	})
	t.Run("no_usable_terms", func(t *testing.T) {
		response := query(f.tokenA2, url.Values{"q": {"!!!"}})
		assertHidden(t, response)
		assert.Equal(t, http.StatusBadRequest, response.status)
	})
	t.Run("newer_other_contexts_do_not_consume_limit", func(t *testing.T) {
		for i := range 52 {
			payload := map[string]any{"content": fmt.Sprintf("harbor lantern unrelated other context %d", i), "agent_visibility": "private"}
			if i%2 == 0 {
				payload["context"] = map[string]any{"kind": "global"}
			} else {
				payload["project"] = f.project
			}
			created := f.serve(f.request(t, context.Background(), http.MethodPost, "/api/memories", f.tokenA, payload))
			require.Equal(t, http.StatusCreated, created.status, created.body)
		}
		response := query(f.tokenA2, url.Values{"q": {"harbor lantern"}, "limit": {"1"}})
		assertHidden(t, response)
		result := decode(t, response)
		require.Len(t, result.Items, 1)
		assert.Equal(t, english.ID, result.Items[0].ID)
		assert.NotContains(t, response.body, "unrelated other context")
	})
	for _, values := range []url.Values{{"principal": {""}}, {"context": {"invalid"}}, {"project": {"not-a-valid-project"}}} {
		t.Run("invalid_"+values.Encode(), func(t *testing.T) {
			response := query(f.tokenA2, values)
			assertHidden(t, response)
			assert.Contains(t, []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound}, response.status)
			assert.NotContains(t, response.body, "blue battery")
		})
	}
	t.Run("unauthenticated", func(t *testing.T) {
		response := query("", url.Values{"q": {"harbor lantern"}})
		assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, response.status)
		assertHidden(t, response)
		assert.NotContains(t, response.body, "blue battery")
	})
	t.Run("unenrolled", func(t *testing.T) {
		require.NoError(t, db.Exec(`DELETE FROM memory_principals WHERE principal = ?`, "agent/synthetic-security-b").Error)
		response := query(f.tokenB, url.Values{"q": {"harbor lantern"}})
		assert.Contains(t, []int{http.StatusBadRequest, http.StatusForbidden, http.StatusUnauthorized}, response.status)
		assertHidden(t, response)
		assert.NotContains(t, response.body, "blue battery")
	})
}
