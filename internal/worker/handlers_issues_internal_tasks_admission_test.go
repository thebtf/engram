package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/privacy"
	"github.com/thebtf/engram/internal/redaction"
)

func TestIssueHTTPInternalTaskAdmission(t *testing.T) {
	fixture := newIssue513ActorFixture(t)
	service := fixture.service
	// Preserve the fixture's credential-log checks with concurrent real requests.
	log.Logger = zerolog.New(zerolog.SyncWriter(&fixture.capturedLogs))
	call := func(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		return fixture.call(t, fixture.client, method, path, body)
	}
	errorCode := func(t *testing.T, response *httptest.ResponseRecorder) string {
		t.Helper()
		var result struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		assert.True(t, json.Unmarshal(response.Body.Bytes(), &result) == nil, "error must use the structured contract without printing response content")
		return result.Error.Code
	}
	assertNoDisclosure := func(t *testing.T, response *httptest.ResponseRecorder, values ...string) {
		t.Helper()
		for _, value := range append([]string{`"payload"`, `"digest"`, `"receipt"`, `"creator_keycard_id"`}, values...) {
			assert.False(t, strings.Contains(response.Body.String(), value), "response must not disclose protected fields or request content")
		}
	}
	detail := func(t *testing.T, id int64) map[string]json.RawMessage {
		t.Helper()
		response := call(t, http.MethodGet, fmt.Sprintf("/api/issues/%d?detail_mode=paged", id), "")
		require.Equal(t, http.StatusOK, response.Code)
		result := issue513JSON(t, response)
		var issue map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(result["issue"], &issue))
		return issue
	}
	assertMetadata := func(t *testing.T, id int64, expected string) {
		t.Helper()
		var want, got any
		require.True(t, json.Unmarshal([]byte(expected), &want) == nil, "synthetic expected metadata must be valid JSON")
		require.True(t, json.Unmarshal(detail(t, id)["metadata"], &got) == nil, "authorized detail must retain metadata as JSON")
		assert.True(t, reflect.DeepEqual(want, got), "admitted metadata must round-trip unchanged, including unknown keys and boundary values")
	}
	withKey := func(t *testing.T, key string) string {
		t.Helper()
		raw, err := json.Marshal(key)
		require.NoError(t, err)
		return strings.Replace(issue513CreateBody(issue513Source, "", "Synthetic key boundary", `{}`), `"request_key":""`, `"request_key":`+string(raw), 1)
	}

	// These controls use existing fields only. Missing future auth-me.actor must
	// not prevent the already-supported client path from reaching persistence.
	if !t.Run("persisted_keycard_and_production_auth_controls", func(t *testing.T) {
		validator := auth.NewValidator("", service.tokenStore)
		identity, err := validator.Validate(t.Context(), fixture.client.bearer)
		require.NoError(t, err)
		require.Equal(t, auth.SourceClient, identity.Source)
		require.Equal(t, auth.RoleReadWrite, identity.Role)
		require.NotEmpty(t, identity.KeycardID)
		other, err := validator.Validate(t.Context(), fixture.otherClient.bearer)
		require.NoError(t, err)
		require.NotEqual(t, identity.KeycardID, other.KeycardID)
		var persisted int64
		require.NoError(t, service.store.GetDB().Table("api_tokens").Where("id = ? AND scope = ?", identity.KeycardID, "read-write").Count(&persisted).Error)
		require.EqualValues(t, 1, persisted, "the actor must resolve to a real issued fixture row")
		me := fixture.call(t, fixture.client, http.MethodGet, "/api/auth/me", "")
		require.Equal(t, http.StatusOK, me.Code)
		assert.JSONEq(t, `true`, string(issue513JSON(t, me)["authenticated"]))
		assert.JSONEq(t, `"read-write"`, string(issue513JSON(t, me)["role"]))
		assert.JSONEq(t, `false`, string(issue513JSON(t, me)["auth_disabled"]))
		body := issue513CreateBody(issue513Source, "auth-persistence-control", "Synthetic auth control", `{}`)
		before := issue513Count(t, service)
		created, writes := issue513ObserveDomainAccess(t, service, func() *httptest.ResponseRecorder {
			return call(t, http.MethodPost, "/api/issues", body)
		})
		id := issue513CreatedID(t, created)
		require.Positive(t, writes, "zero-access source/auth negatives need a working persistence observer")
		require.Equal(t, before+1, issue513Count(t, service))
		stored, _, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		require.Equal(t, identity.KeycardID, stored.CreatorKeycardID, "production middleware must supply the persisted keycard actor")
		read, reads := issue513ObserveDomainAccess(t, service, func() *httptest.ResponseRecorder {
			return call(t, http.MethodGet, fmt.Sprintf("/api/issues/%d", id), "")
		})
		require.Equal(t, http.StatusOK, read.Code)
		require.Positive(t, reads, "observer must detect issue reads as well as writes")
		tampered := fixture.client.bearer[:len(fixture.client.bearer)-1]
		if strings.HasSuffix(fixture.client.bearer, "0") {
			tampered += "1"
		} else {
			tampered += "0"
		}
		fixture.privateValues = append(fixture.privateValues, tampered)
		for _, test := range []struct {
			credential issue513ActorCredential
			status     int
		}{
			{credential: issue513ActorCredential{}, status: http.StatusUnauthorized},
			{credential: issue513ActorCredential{bearer: tampered}, status: http.StatusUnauthorized},
			{credential: fixture.readOnly, status: http.StatusForbidden},
		} {
			counts := issue513IssueDomainCounts(t, service)
			require.Contains(t, counts, "issue_changes", "the T004 history schema must exist before behavioral RED")
			require.Contains(t, counts, "issue_create_receipts", "the T004 receipt schema must exist before behavioral RED")
			response, accesses := issue513ObserveDomainAccess(t, service, func() *httptest.ResponseRecorder {
				return fixture.call(t, test.credential, http.MethodPost, "/api/issues", body)
			})
			assert.Equal(t, test.status, response.Code)
			assert.Zero(t, accesses)
			assert.Equal(t, counts, issue513IssueDomainCounts(t, service))
			assertNoDisclosure(t, response, `"id"`, "Synthetic auth control")
		}
		counts := issue513IssueDomainCounts(t, service)
		denied := fixture.call(t, fixture.otherClient, http.MethodPatch, fmt.Sprintf("/api/issues/%d", id), `{"expected_revision":"1","title":"Synthetic unauthorized edit","comment":"Synthetic unauthorized comment"}`)
		assert.Equal(t, http.StatusForbidden, denied.Code, "a different real client still lacks creator field-edit authority")
		assert.Equal(t, counts, issue513IssueDomainCounts(t, service))
		unchanged, comments, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		assert.Equal(t, stored, unchanged)
		assert.Empty(t, comments)
	}) {
		t.Fatal("authenticated persistence controls failed; subsequent behavior would be fixture failure, not RED")
	}

	t.Run("inactive_source_cannot_replay_or_compare_receipt", func(t *testing.T) {
		body := issue513CreateBody(issue513OtherSource, "inactive-source-intent", "Synthetic admission task", `{}`)
		issue513CreatedID(t, call(t, http.MethodPost, "/api/issues", body))
		require.NoError(t, service.store.GetDB().Exec(`UPDATE projects SET removed_at = ? WHERE id = ?`, time.Now(), issue513OtherSource).Error)
		defer func() {
			require.NoError(t, service.store.GetDB().Exec(`UPDATE projects SET removed_at = NULL WHERE id = ?`, issue513OtherSource).Error)
		}()
		active, err := service.sessionStore.CanonicalProjectExists(t.Context(), issue513OtherSource)
		require.NoError(t, err)
		require.False(t, active, "the real canonical source owner must confirm removal")
		for _, retry := range []string{body, strings.Replace(body, "Synthetic admission task", "Synthetic changed admission task", 1)} {
			before := issue513IssueDomainCounts(t, service)
			response, accesses := issue513ObserveDomainAccess(t, service, func() *httptest.ResponseRecorder {
				return call(t, http.MethodPost, "/api/issues", retry)
			})
			assert.Contains(t, []int{http.StatusNotFound, http.StatusForbidden}, response.Code)
			assert.Zero(t, accesses, "source admission must precede issue/receipt lookup, equality comparison and writes")
			assert.Equal(t, before, issue513IssueDomainCounts(t, service))
			assertNoDisclosure(t, response, `"id"`, "Synthetic admission task", "Synthetic changed admission task", "IDEMPOTENCY_CONFLICT")
		}
	})

	t.Run("normalized_original_payload_survives_object_order_and_number_forms", func(t *testing.T) {
		before := issue513Count(t, service)
		first := issue513CreateBody(issue513Source, "canonical-original-intent", "Synthetic canonical task", `{"nested":{"n":1,"flag":true}}`)
		first = strings.Replace(first, `"type":"task"`, `"type":" TASK "`, 1)
		first = strings.Replace(first, `"labels":["internal","synthetic"]`, `"labels":["synthetic","internal","internal"]`, 1)
		id := issue513CreatedID(t, call(t, http.MethodPost, "/api/issues", first))
		retry := issue513CreateBody(issue513Source, "canonical-original-intent", "Synthetic canonical task", `{"nested":{"flag":true,"n":1.0}}`)
		response := call(t, http.MethodPost, "/api/issues", retry)
		var result struct {
			ID       int64 `json:"id"`
			Replayed bool  `json:"replayed"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
		assert.Equal(t, http.StatusOK, response.Code)
		assert.Equal(t, id, result.ID)
		assert.True(t, result.Replayed)
		assert.Equal(t, before+1, issue513Count(t, service))
		assertMetadata(t, id, `{"nested":{"flag":true,"n":1}}`)
		counts := issue513IssueDomainCounts(t, service)
		conflict := call(t, http.MethodPost, "/api/issues", strings.Replace(retry, `"n":1.0`, `"n":2`, 1))
		assert.Equal(t, http.StatusConflict, conflict.Code)
		assert.Equal(t, "IDEMPOTENCY_CONFLICT", errorCode(t, conflict))
		assert.Equal(t, counts, issue513IssueDomainCounts(t, service))
		assertNoDisclosure(t, conflict, `"id"`, `"revision"`, `"metadata"`, "Synthetic canonical task", "nested")
	})

	t.Run("different_persisted_actors_and_active_sources_have_independent_receipts", func(t *testing.T) {
		key := "real-actor-isolation-intent"
		firstBody := issue513CreateBody(issue513Source, key, "Synthetic first namespace", `{"original_only":true}`)
		firstID := issue513CreatedID(t, call(t, http.MethodPost, "/api/issues", firstBody))
		for _, test := range []struct {
			name       string
			credential issue513ActorCredential
			source     string
		}{
			{name: "other_persisted_keycard", credential: fixture.otherClient, source: issue513Source},
			{name: "other_active_canonical_source", credential: fixture.client, source: issue513OtherSource},
		} {
			t.Run(test.name, func(t *testing.T) {
				before := issue513Count(t, service)
				body := issue513CreateBody(test.source, key, "Synthetic independent namespace", `{"independent_only":true}`)
				created := fixture.call(t, test.credential, http.MethodPost, "/api/issues", body)
				id := issue513CreatedID(t, created)
				assert.NotEqual(t, firstID, id)
				assertNoDisclosure(t, created, "Synthetic first namespace", "original_only")
				retry := fixture.call(t, test.credential, http.MethodPost, "/api/issues", body)
				var result struct {
					ID       int64 `json:"id"`
					Replayed bool  `json:"replayed"`
				}
				require.NoError(t, json.Unmarshal(retry.Body.Bytes(), &result))
				assert.Equal(t, http.StatusOK, retry.Code)
				assert.Equal(t, id, result.ID)
				assert.True(t, result.Replayed)
				assert.Equal(t, before+1, issue513Count(t, service))
				assertNoDisclosure(t, retry, "Synthetic first namespace", "original_only")
			})
		}
	})

	t.Run("original_create_binding_survives_authorized_material_edit", func(t *testing.T) {
		original := issue513CreateBody(issue513Source, "original-after-edit", "Synthetic original binding", `{"state":"original"}`)
		id := issue513CreatedID(t, call(t, http.MethodPost, "/api/issues", original))
		stored, _, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		base := strconv.FormatInt(stored.Revision, 10)
		edit := call(t, http.MethodPatch, fmt.Sprintf("/api/issues/%d", id), `{"expected_revision":"`+base+`","title":"Synthetic edited binding","metadata":{"state":"edited"}}`)
		require.Equal(t, http.StatusOK, edit.Code)
		edited, _, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		require.Equal(t, "Synthetic edited binding", edited.Title, "existing authorized material edit must actually persist before replay")
		before := issue513IssueDomainCounts(t, service)
		replay := call(t, http.MethodPost, "/api/issues", original)
		var result struct {
			ID       int64  `json:"id"`
			Revision string `json:"revision"`
			Replayed bool   `json:"replayed"`
		}
		require.NoError(t, json.Unmarshal(replay.Body.Bytes(), &result))
		assert.Equal(t, http.StatusOK, replay.Code)
		assert.Equal(t, id, result.ID)
		assert.True(t, result.Replayed)
		assert.Equal(t, strconv.FormatInt(edited.Revision, 10), result.Revision)
		assert.Equal(t, before, issue513IssueDomainCounts(t, service))
		currentPayload := issue513CreateBody(issue513Source, "original-after-edit", "Synthetic edited binding", `{"state":"edited"}`)
		conflict := call(t, http.MethodPost, "/api/issues", currentPayload)
		assert.Equal(t, http.StatusConflict, conflict.Code)
		assert.Equal(t, "IDEMPOTENCY_CONFLICT", errorCode(t, conflict))
		assert.Equal(t, before, issue513IssueDomainCounts(t, service))
		current, _, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		assert.Equal(t, edited, current)
		assertNoDisclosure(t, conflict, `"id"`, `"metadata"`, "Synthetic original binding", "Synthetic edited binding")
	})

	t.Run("cas_material_and_losing_compound_patch_are_atomic", func(t *testing.T) {
		id := issue513CreatedID(t, call(t, http.MethodPost, "/api/issues", issue513CreateBody(issue513Source, "cas-compound", "Synthetic CAS base", `{}`)))
		path := fmt.Sprintf("/api/issues/%d", id)
		before := issue513IssueDomainCounts(t, service)
		winner := call(t, http.MethodPatch, path, `{"expected_revision":"1","title":"Synthetic CAS winner","metadata":{"outcome":"won"},"comment":"Synthetic winner comment","comment_metadata":{"winner":true},"source_project":"`+issue513Source+`","source_agent":"synthetic-task-author"}`)
		require.Equal(t, http.StatusOK, winner.Code)
		var update struct {
			ID        int64  `json:"id"`
			Revision  string `json:"revision"`
			Changed   bool   `json:"changed"`
			CommentID *int64 `json:"comment_id"`
		}
		require.NoError(t, json.Unmarshal(winner.Body.Bytes(), &update))
		assert.Equal(t, id, update.ID)
		assert.Equal(t, "2", update.Revision)
		assert.True(t, update.Changed)
		assert.NotNil(t, update.CommentID)
		won, comments, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		assert.EqualValues(t, 2, won.Revision)
		assert.Equal(t, "Synthetic CAS winner", won.Title)
		assertMetadata(t, id, `{"outcome":"won"}`)
		if assert.Len(t, comments, 1) {
			assert.JSONEq(t, `{"winner":true}`, string(comments[0].Metadata))
			if update.CommentID != nil {
				assert.Equal(t, comments[0].ID, *update.CommentID)
			}
		}
		after := issue513IssueDomainCounts(t, service)
		assert.Equal(t, before["issue_changes"]+1, after["issue_changes"], "one material revision must have exactly one durable history entry")
		assert.Equal(t, before["issue_comments"]+1, after["issue_comments"])
		// Even a same-value material edit is stale when its original base lost.
		loser := call(t, http.MethodPatch, path, `{"expected_revision":"1","title":"Synthetic CAS winner","metadata":{"outcome":"lost"},"comment":"Synthetic losing comment","comment_metadata":{"loser":true},"source_project":"`+issue513Source+`"}`)
		assert.Equal(t, http.StatusConflict, loser.Code)
		assert.Equal(t, "REVISION_CONFLICT", errorCode(t, loser))
		current, currentComments, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		assert.True(t, reflect.DeepEqual(won, current), "stale compound PATCH must leave all issue fields, revision and timestamps unchanged")
		assert.True(t, reflect.DeepEqual(comments, currentComments), "stale compound PATCH must not append its comment")
		assert.Equal(t, after, issue513IssueDomainCounts(t, service))
		assertNoDisclosure(t, loser, "Synthetic losing comment", `"outcome"`, `"comment_id"`)
	})

	t.Run("current_revision_noop_and_comment_only_noop", func(t *testing.T) {
		id := issue513CreatedID(t, call(t, http.MethodPost, "/api/issues", issue513CreateBody(issue513Source, "cas-noop", "Synthetic no-op task", `{}`)))
		path := fmt.Sprintf("/api/issues/%d", id)
		before, comments, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		counts := issue513IssueDomainCounts(t, service)
		response := call(t, http.MethodPatch, path, `{"expected_revision":"1","title":"Synthetic no-op task"}`)
		require.Equal(t, http.StatusOK, response.Code)
		result := issue513JSON(t, response)
		assert.JSONEq(t, `false`, string(result["changed"]))
		assert.JSONEq(t, `"1"`, string(result["revision"]))
		assert.JSONEq(t, `null`, string(result["comment_id"]))
		after, afterComments, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		assert.True(t, reflect.DeepEqual(before, after), "a real no-op must not update timestamps or issue fields")
		assert.Equal(t, comments, afterComments)
		assert.Equal(t, counts, issue513IssueDomainCounts(t, service))
		commented := call(t, http.MethodPatch, path, `{"expected_revision":"1","title":"Synthetic no-op task","comment":"Synthetic no-op outcome","comment_metadata":{"unknown":[true,null,1]},"source_project":"`+issue513Source+`"}`)
		require.Equal(t, http.StatusOK, commented.Code)
		commentResult := issue513JSON(t, commented)
		assert.JSONEq(t, `false`, string(commentResult["changed"]))
		assert.JSONEq(t, `"1"`, string(commentResult["revision"]))
		var commentID int64
		assert.True(t, json.Unmarshal(commentResult["comment_id"], &commentID) == nil)
		assert.Positive(t, commentID)
		current, currentComments, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		assert.EqualValues(t, 1, current.Revision)
		if assert.Len(t, currentComments, 1) {
			assert.JSONEq(t, `{"unknown":[true,null,1]}`, string(currentComments[0].Metadata))
		}
		finalCounts := issue513IssueDomainCounts(t, service)
		assert.Equal(t, counts["issue_changes"], finalCounts["issue_changes"], "a comment accompanying equal fields does not invent a material revision")
		assert.Equal(t, counts["issue_comments"]+1, finalCounts["issue_comments"])
	})

	t.Run("invalid_compound_patch_preserves_existing_atomicity", func(t *testing.T) {
		id := issue513CreatedID(t, call(t, http.MethodPost, "/api/issues", issue513CreateBody(issue513Source, "invalid-compound", "Synthetic atomic control", `{}`)))
		before, comments, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		counts := issue513IssueDomainCounts(t, service)
		response := call(t, http.MethodPatch, fmt.Sprintf("/api/issues/%d", id), `{"expected_revision":"1","title":"Synthetic invalid edit","status":"invalid","comment":"Synthetic invalid comment","source_project":"`+issue513Source+`"}`)
		assert.Equal(t, http.StatusBadRequest, response.Code)
		after, afterComments, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		assert.Equal(t, before, after)
		assert.Equal(t, comments, afterComments)
		assert.Equal(t, counts, issue513IssueDomainCounts(t, service))
	})

	t.Run("concurrent_equal_creates_commit_one_card", func(t *testing.T) {
		before := issue513Count(t, service)
		body := issue513CreateBody(issue513Source, "concurrent-original-intent", "Synthetic concurrent task", `{}`)
		const callers = 10
		ids := make([]int64, callers)
		statuses := make([]int, callers)
		decodeErrors := make([]error, callers)
		start := make(chan struct{})
		var group sync.WaitGroup
		for index := range callers {
			group.Add(1)
			go func(index int) {
				defer group.Done()
				<-start
				response := call(t, http.MethodPost, "/api/issues", body)
				statuses[index] = response.Code
				var result struct {
					ID int64 `json:"id"`
				}
				decodeErrors[index] = json.Unmarshal(response.Body.Bytes(), &result)
				ids[index] = result.ID
			}(index)
		}
		close(start)
		group.Wait()
		created := 0
		for index := range callers {
			require.NoError(t, decodeErrors[index])
			assert.Positive(t, ids[index])
			assert.Equal(t, ids[0], ids[index])
			assert.Contains(t, []int{http.StatusCreated, http.StatusOK}, statuses[index])
			if statuses[index] == http.StatusCreated {
				created++
			}
		}
		assert.Equal(t, 1, created)
		assert.Equal(t, before+1, issue513Count(t, service))
	})

	// T003 already records ordinary nested duplicate and unsafe-integer RED.
	// Exercise additional received-wire discriminators, not those same cases.
	for _, test := range []struct {
		name     string
		metadata string
	}{
		{name: "null", metadata: `null`},
		{name: "array", metadata: `[]`},
		{name: "rounded_fraction", metadata: `{"n":0.10000000000000001}`},
		{name: "underflow", metadata: `{"n":1e-400}`},
		{name: "overflow", metadata: `{"n":1e400}`},
		{name: "escaped_duplicate_member", metadata: `{"outer":{"name":1,"\u006eame":2}}`},
	} {
		t.Run("reject_unpreservable_metadata_"+test.name, func(t *testing.T) {
			body := issue513CreateBody(issue513Source, "invalid-object-"+test.name, "Synthetic invalid object", test.metadata)
			require.True(t, json.Valid([]byte(body)), "the raw discriminator must be syntactically valid JSON")
			before := issue513IssueDomainCounts(t, service)
			response := call(t, http.MethodPost, "/api/issues", body)
			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.Equal(t, "INVALID_ARGUMENT", errorCode(t, response))
			assert.Equal(t, before, issue513IssueDomainCounts(t, service))
			assertNoDisclosure(t, response, `"id"`, `"metadata"`)
		})
	}
	t.Run("duplicate_received_top_level_member_is_not_last_value_wins", func(t *testing.T) {
		body := strings.Replace(issue513CreateBody(issue513Source, "duplicate-top-level", "Synthetic duplicate wire", `{}`), `"request_key":"duplicate-top-level"`, `"request_key":"duplicate-top-level","request_key":"duplicate-top-level"`, 1)
		require.True(t, json.Valid([]byte(body)))
		before := issue513IssueDomainCounts(t, service)
		response := call(t, http.MethodPost, "/api/issues", body)
		assert.Equal(t, http.StatusBadRequest, response.Code)
		assert.Equal(t, "INVALID_ARGUMENT", errorCode(t, response))
		assert.Equal(t, before, issue513IssueDomainCounts(t, service))
	})
	t.Run("admitted_numeric_and_unknown_metadata_values_roundtrip", func(t *testing.T) {
		metadata := `{"extension":{"min":-9007199254740991,"max":9007199254740991,"fraction":0.1,"tiny":1e-300,"ordered":[2,1,null,true],"opaque":"outcome"}}`
		id := issue513CreatedID(t, call(t, http.MethodPost, "/api/issues", issue513CreateBody(issue513Source, "admitted-values", "Synthetic admitted values", metadata)))
		assertMetadata(t, id, metadata)
	})

	for _, test := range []struct {
		name   string
		key    string
		status int
	}{
		{name: "ascii_512_bytes", key: strings.Repeat("a", 512), status: http.StatusCreated},
		{name: "utf8_512_bytes", key: strings.Repeat("я", 256), status: http.StatusCreated},
		{name: "opaque_utf8", key: " синтетика / opaque : key ", status: http.StatusCreated},
		{name: "ascii_513_bytes", key: strings.Repeat("a", 513), status: http.StatusRequestEntityTooLarge},
		{name: "utf8_513_bytes", key: strings.Repeat("я", 256) + "a", status: http.StatusRequestEntityTooLarge},
		{name: "empty", key: "", status: http.StatusBadRequest},
		{name: "nul", key: "synthetic\x00key", status: http.StatusBadRequest},
	} {
		t.Run("request_key_"+test.name, func(t *testing.T) {
			body := withKey(t, test.key)
			require.True(t, json.Valid([]byte(body)), "key controls must exercise admission, not invalid Go string escapes")
			before := issue513Count(t, service)
			response := call(t, http.MethodPost, "/api/issues", body)
			assert.Equal(t, test.status, response.Code)
			if test.status == http.StatusCreated {
				issue513CreatedID(t, response)
				assert.Equal(t, before+1, issue513Count(t, service))
			} else {
				assert.Equal(t, before, issue513Count(t, service))
				code := "INVALID_ARGUMENT"
				if test.status == http.StatusRequestEntityTooLarge {
					code = "PAYLOAD_TOO_LARGE"
				}
				assert.Equal(t, code, errorCode(t, response))
				assertNoDisclosure(t, response, `"id"`)
			}
		})
	}
	t.Run("request_key_received_invalid_utf8_is_not_replaced", func(t *testing.T) {
		body := strings.Replace(withKey(t, ""), `"request_key":""`, `"request_key":"`+string([]byte{0xff})+`"`, 1)
		require.False(t, utf8.ValidString(body), "raw wire must retain the invalid UTF-8 byte")
		before := issue513IssueDomainCounts(t, service)
		response := call(t, http.MethodPost, "/api/issues", body)
		assert.Equal(t, http.StatusBadRequest, response.Code)
		assert.Equal(t, "INVALID_ARGUMENT", errorCode(t, response))
		assert.Equal(t, before, issue513IssueDomainCounts(t, service))
	})

	const metadataLimit = 1 << 20
	for _, test := range []struct {
		name     string
		metadata string
		status   int
	}{
		{name: "compact_exactly_1MiB", metadata: `{"v":"` + strings.Repeat("a", metadataLimit-len(`{"v":""}`)) + `"}`, status: http.StatusCreated},
		{name: "compact_over_1MiB", metadata: `{"v":"` + strings.Repeat("a", metadataLimit-len(`{"v":""}`)+1) + `"}`, status: http.StatusRequestEntityTooLarge},
		{name: "root_inclusive_depth_256", metadata: `{"v":` + strings.Repeat("[", 255) + "0" + strings.Repeat("]", 255) + "}", status: http.StatusCreated},
		{name: "root_inclusive_depth_257", metadata: `{"v":` + strings.Repeat("[", 256) + "0" + strings.Repeat("]", 256) + "}", status: http.StatusRequestEntityTooLarge},
	} {
		t.Run("metadata_boundary_"+test.name, func(t *testing.T) {
			require.True(t, json.Valid([]byte(test.metadata)))
			var compact bytes.Buffer
			require.NoError(t, json.Compact(&compact, []byte(test.metadata)))
			if test.name == "compact_exactly_1MiB" {
				require.Equal(t, metadataLimit, compact.Len())
			}
			if test.name == "compact_over_1MiB" {
				require.Equal(t, metadataLimit+1, compact.Len())
			}
			before := issue513IssueDomainCounts(t, service)
			response := call(t, http.MethodPost, "/api/issues", issue513CreateBody(issue513Source, "metadata-boundary-"+test.name, "Synthetic metadata boundary", test.metadata))
			assert.Equal(t, test.status, response.Code)
			if test.status == http.StatusCreated {
				id := issue513CreatedID(t, response)
				assertMetadata(t, id, test.metadata)
			} else {
				assert.Equal(t, "PAYLOAD_TOO_LARGE", errorCode(t, response))
				assert.Equal(t, before, issue513IssueDomainCounts(t, service))
				assertNoDisclosure(t, response, `"id"`, `"metadata"`)
			}
		})
	}

	for _, field := range []string{"title", "body", "metadata"} {
		t.Run("recognized_secret_"+field+"_is_refused_without_rewriting_or_logging", func(t *testing.T) {
			// Runtime-built synthetic assignment, never an issued credential.
			value := "api_key=" + strings.Repeat("x", 32)
			require.True(t, privacy.ContainsSecrets(value), "the existing recognized-class detector must match the synthetic control")
			body := issue513CreateBody(issue513Source, "recognized-class-"+field, "Synthetic rejected task", `{}`)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(body), &fields))
			raw, err := json.Marshal(value)
			require.NoError(t, err)
			if field == "metadata" {
				fields[field] = json.RawMessage(`{"nested":{"example":` + string(raw) + `}}`)
			} else {
				fields[field] = raw
			}
			encoded, err := json.Marshal(fields)
			require.NoError(t, err)
			before := issue513IssueDomainCounts(t, service)
			logStart := fixture.capturedLogs.Len()
			response := call(t, http.MethodPost, "/api/issues", string(encoded))
			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.Equal(t, before, issue513IssueDomainCounts(t, service))
			assert.Equal(t, "PRIVACY_REJECTED", errorCode(t, response))
			assertNoDisclosure(t, response, value, `"id"`, `"metadata"`)
			assert.False(t, strings.Contains(fixture.capturedLogs.String()[logStart:], value), "operational output must not contain the recognized value")
		})
	}

	t.Run("configured_rule_refuses_without_silent_metadata_rewrite_or_logging", func(t *testing.T) {
		value := "synthetic-" + "blocked-value"
		var err error
		service.redactionRules, err = redaction.CompileRules([]redaction.Rule{{ID: "synthetic-issue-rule", Pattern: value, Replacement: "[removed]"}})
		require.NoError(t, err)
		defer func() { service.redactionRules = nil }()
		scrubbed, matched, err := redaction.ScrubCompiled(value, service.redactionRules)
		require.NoError(t, err)
		require.True(t, scrubbed != value && len(matched) == 1, "existing configured protection must match the synthetic control")
		before := issue513IssueDomainCounts(t, service)
		logStart := fixture.capturedLogs.Len()
		response := call(t, http.MethodPost, "/api/issues", issue513CreateBody(issue513Source, "configured-rule-intent", "Synthetic configured protection", fmt.Sprintf(`{"unknown":%q}`, value)))
		assert.Equal(t, http.StatusBadRequest, response.Code)
		assert.Equal(t, "PRIVACY_REJECTED", errorCode(t, response))
		assert.Equal(t, before, issue513IssueDomainCounts(t, service))
		assertNoDisclosure(t, response, value, `"id"`, "[removed]")
		assert.False(t, strings.Contains(fixture.capturedLogs.String()[logStart:], value), "configured refusal must not log protected content")
	})

	t.Run("recognized_secret_compound_patch_is_atomic", func(t *testing.T) {
		id := issue513CreatedID(t, call(t, http.MethodPost, "/api/issues", issue513CreateBody(issue513Source, "privacy-patch", "Synthetic safe patch base", `{}`)))
		before, comments, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		counts := issue513IssueDomainCounts(t, service)
		value := "api_key=" + strings.Repeat("x", 32)
		logStart := fixture.capturedLogs.Len()
		body := fmt.Sprintf(`{"expected_revision":"1","title":"Synthetic rejected compound edit","comment":%q,"comment_metadata":{"unknown":true},"source_project":%q}`, value, issue513Source)
		response := call(t, http.MethodPatch, fmt.Sprintf("/api/issues/%d", id), body)
		assert.Equal(t, http.StatusBadRequest, response.Code)
		assert.Equal(t, "PRIVACY_REJECTED", errorCode(t, response))
		after, afterComments, err := service.issueStore.GetIssue(t.Context(), id)
		require.NoError(t, err)
		assert.True(t, reflect.DeepEqual(before, after), "privacy refusal must not commit a companion material edit")
		assert.True(t, reflect.DeepEqual(comments, afterComments), "privacy refusal must not append or rewrite the comment")
		assert.Equal(t, counts, issue513IssueDomainCounts(t, service))
		assertNoDisclosure(t, response, value, `"id"`)
		assert.False(t, strings.Contains(fixture.capturedLogs.String()[logStart:], value), "compound refusal must not log protected content")
	})

	t.Run("create_does_not_log_private_title_body_or_metadata", func(t *testing.T) {
		logStart := fixture.capturedLogs.Len()
		body := issue513CreateBody(issue513Source, "private-log-intent", "Synthetic private title marker", `{"private":"Synthetic private metadata marker"}`)
		body = strings.Replace(body, "Record a resumable synthetic outcome.", "Synthetic private body marker", 1)
		issue513CreatedID(t, call(t, http.MethodPost, "/api/issues", body))
		for _, value := range []string{"Synthetic private title marker", "Synthetic private body marker", "Synthetic private metadata marker"} {
			assert.False(t, strings.Contains(fixture.capturedLogs.String()[logStart:], value), "operational output must not contain private request values")
		}
	})
}
