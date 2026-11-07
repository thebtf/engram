package worker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type b2Link struct {
	ID              json.RawMessage         `json:"id"`
	SourceID        int64                   `json:"source_id"`
	TargetID        int64                   `json:"target_id"`
	Relation        string                  `json:"relation"`
	TargetVersion   *int                    `json:"target_version"`
	Author          *ownCardContextIdentity `json:"author"`
	SourceSessionID string                  `json:"source_session_id"`
}

type b2LinkPage struct {
	Links      []b2Link        `json:"links"`
	NextCursor json.RawMessage `json:"next_cursor"`
}

func b2MemoryLinksPath(id int64) string {
	return "/api/memories/" + strconv.FormatInt(id, 10) + "/links"
}

func b2LinkID(t *testing.T, link b2Link) string {
	t.Helper()
	require.NotEmpty(t, link.ID)
	var text string
	if json.Unmarshal(link.ID, &text) == nil {
		require.NotEmpty(t, text)
		return text
	}
	var number int64
	require.NoError(t, json.Unmarshal(link.ID, &number))
	require.Positive(t, number)
	return strconv.FormatInt(number, 10)
}

func b2DecodeLink(t *testing.T, rec *httptest.ResponseRecorder, status int) b2Link {
	t.Helper()
	require.Equal(t, status, rec.Code, "link create status; response withheld")
	var link b2Link
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &link))
	b2LinkID(t, link)
	return link
}

func b2ListLinks(t *testing.T, f *ownCardContextFixture, id int64, caller ownCardContextCaller, limit int) b2LinkPage {
	t.Helper()
	rec := f.call(t, http.MethodGet, b2MemoryLinksPath(id)+"?limit="+strconv.Itoa(limit), nil, caller)
	require.Equal(t, http.StatusOK, rec.Code, "link list status; response withheld")
	var page b2LinkPage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.NotNil(t, page.Links, "list envelope must include links, even when empty")
	require.NotEmpty(t, page.NextCursor, "list envelope must include next_cursor, including null")
	require.LessOrEqual(t, len(page.Links), limit)
	return page
}

func b2CreateLink(t *testing.T, f *ownCardContextFixture, source, target int64, relation, key string, version *int, caller ownCardContextCaller, status int) b2Link {
	t.Helper()
	payload := map[string]any{"target_id": target, "relation": relation, "request_key": key}
	if version != nil {
		payload["target_version"] = *version
	}
	return b2DecodeLink(t, f.call(t, http.MethodPost, b2MemoryLinksPath(source), payload, caller), status)
}

func b2DeleteLink(t *testing.T, f *ownCardContextFixture, id string, caller ownCardContextCaller) {
	t.Helper()
	rec := f.call(t, http.MethodDelete, "/api/memory-links/"+id, nil, caller)
	require.Contains(t, []int{http.StatusOK, http.StatusNoContent}, rec.Code, "active link deletion must succeed")
}

func TestB2KnowledgeLinksPostgres(t *testing.T) {
	t.Run("symmetric_dedup_removal_and_intent", func(t *testing.T) {
		f := newOwnCardContextFixture(t, false)
		owner := f.registered(t, "links-symmetric")
		projects := f.state(t).projects
		a := f.create(t, owner, ownCardContextPayload("synthetic link source"))
		b := f.create(t, owner, ownCardContextPayload("synthetic link target"))
		before := f.state(t)
		key1, key2, key3 := uuid.NewString(), uuid.NewString(), uuid.NewString()
		session1, session2 := uuid.NewString(), uuid.NewString()
		firstPayload := map[string]any{"target_id": b.ID, "relation": "related_to", "request_key": key1, "source_session_id": session1}
		first := b2DecodeLink(t, f.call(t, http.MethodPost, b2MemoryLinksPath(a.ID), firstPayload, owner.caller()), http.StatusCreated)
		id := b2LinkID(t, first)
		require.Equal(t, "related_to", first.Relation)
		require.Equal(t, a.ID, first.SourceID)
		require.Equal(t, b.ID, first.TargetID)
		require.Nil(t, first.TargetVersion)
		require.NotNil(t, first.Author)
		require.Equal(t, owner.identity, *first.Author)
		require.Equal(t, session1, first.SourceSessionID)
		secondPayload := map[string]any{"target_id": a.ID, "relation": "related_to", "request_key": key2, "source_session_id": session2}
		duplicate := b2DecodeLink(t, f.call(t, http.MethodPost, b2MemoryLinksPath(b.ID), secondPayload, owner.caller()), http.StatusOK)
		require.Equal(t, id, b2LinkID(t, duplicate))
		require.Equal(t, first.Author, duplicate.Author)
		require.Equal(t, session1, duplicate.SourceSessionID)
		changed := map[string]any{"target_id": b.ID, "relation": "references", "target_version": b.Version, "request_key": key1, "source_session_id": session1}
		require.Equal(t, http.StatusConflict, f.call(t, http.MethodPost, b2MemoryLinksPath(a.ID), changed, owner.caller()).Code)
		require.Equal(t, before, f.state(t), "conflicting key reuse must not alter either card")
		f.reopen(t)
		for _, endpoint := range []int64{a.ID, b.ID} {
			page := b2ListLinks(t, f, endpoint, owner.caller(), 10)
			require.Len(t, page.Links, 1)
			require.Equal(t, id, b2LinkID(t, page.Links[0]))
			require.Equal(t, first.SourceID, page.Links[0].SourceID)
			require.Equal(t, first.TargetID, page.Links[0].TargetID)
			require.Equal(t, "related_to", page.Links[0].Relation)
			require.Equal(t, first.Author, page.Links[0].Author)
			require.Equal(t, session1, page.Links[0].SourceSessionID)
		}
		b2DeleteLink(t, f, id, owner.caller())
		require.Empty(t, b2ListLinks(t, f, a.ID, owner.caller(), 10).Links)
		for _, replay := range []struct {
			source  int64
			payload map[string]any
		}{{a.ID, firstPayload}, {b.ID, secondPayload}} {
			response := f.call(t, http.MethodPost, b2MemoryLinksPath(replay.source), replay.payload, owner.caller())
			require.Equal(t, http.StatusConflict, response.Code, "old intent must not resurrect a removed link")
		}
		recreated := b2CreateLink(t, f, a.ID, b.ID, "related_to", key3, nil, owner.caller(), http.StatusCreated)
		newID := b2LinkID(t, recreated)
		require.NotEqual(t, id, newID)
		repeated := f.call(t, http.MethodDelete, "/api/memory-links/"+id, nil, owner.caller())
		require.Contains(t, []int{http.StatusOK, http.StatusNoContent, http.StatusNotFound, http.StatusGone}, repeated.Code)
		page := b2ListLinks(t, f, b.ID, owner.caller(), 10)
		require.Len(t, page.Links, 1)
		require.Equal(t, newID, b2LinkID(t, page.Links[0]))
		require.Equal(t, before, f.state(t), "link operations must not rewrite card bodies, projects, users or credentials")
		require.Equal(t, projects, f.state(t).projects, "own cards must not synthesize projects")
	})

	t.Run("concurrent_equivalent_create", func(t *testing.T) {
		f := newOwnCardContextFixture(t, false)
		owner := f.registered(t, "links-concurrent")
		a := f.create(t, owner, ownCardContextPayload("synthetic concurrent source"))
		b := f.create(t, owner, ownCardContextPayload("synthetic concurrent target"))
		before := f.state(t)
		requests := []*http.Request{
			ownCardContextRequest(t, http.MethodPost, b2MemoryLinksPath(a.ID), map[string]any{"target_id": b.ID, "relation": "related_to", "request_key": uuid.NewString()}, owner.caller()),
			ownCardContextRequest(t, http.MethodPost, b2MemoryLinksPath(b.ID), map[string]any{"target_id": a.ID, "relation": "related_to", "request_key": uuid.NewString()}, owner.caller()),
		}
		recorders := []*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
		start := make(chan struct{})
		var done sync.WaitGroup
		for i := range requests {
			done.Add(1)
			go func(i int) {
				defer done.Done()
				<-start
				f.service.router.ServeHTTP(recorders[i], requests[i])
			}(i)
		}
		close(start)
		done.Wait()
		require.ElementsMatch(t, []int{http.StatusCreated, http.StatusOK}, []int{recorders[0].Code, recorders[1].Code})
		first := b2DecodeLink(t, recorders[0], recorders[0].Code)
		second := b2DecodeLink(t, recorders[1], recorders[1].Code)
		require.Equal(t, b2LinkID(t, first), b2LinkID(t, second))
		f.reopen(t)
		for _, endpoint := range []int64{a.ID, b.ID} {
			page := b2ListLinks(t, f, endpoint, owner.caller(), 10)
			require.Len(t, page.Links, 1)
			require.Equal(t, b2LinkID(t, first), b2LinkID(t, page.Links[0]))
		}
		require.Equal(t, before, f.state(t))
	})

	t.Run("references_keep_revision_and_direction", func(t *testing.T) {
		f := newOwnCardContextFixture(t, false)
		owner := f.registered(t, "links-revisions")
		source := f.create(t, owner, ownCardContextPayload("synthetic reference source"))
		target := f.create(t, owner, ownCardContextPayload("synthetic reference target old"))
		oldVersion := target.Version
		old := b2CreateLink(t, f, source.ID, target.ID, "references", uuid.NewString(), &oldVersion, owner.caller(), http.StatusCreated)
		path := "/api/memories/" + strconv.FormatInt(target.ID, 10)
		updated := ownCardContextDecode(t, f.call(t, http.MethodPatch, path, map[string]any{"version": oldVersion, "content": "synthetic reference target revised"}, owner.caller()), http.StatusOK)
		require.Equal(t, oldVersion+1, updated.Version)
		newVersion := updated.Version
		newer := b2CreateLink(t, f, source.ID, target.ID, "references", uuid.NewString(), &newVersion, owner.caller(), http.StatusCreated)
		require.NotEqual(t, b2LinkID(t, old), b2LinkID(t, newer))
		f.reopen(t)
		page := b2ListLinks(t, f, source.ID, owner.caller(), 10)
		require.Len(t, page.Links, 2)
		versions := map[string]int{}
		for _, link := range page.Links {
			require.Equal(t, source.ID, link.SourceID)
			require.Equal(t, target.ID, link.TargetID)
			require.Equal(t, "references", link.Relation)
			require.NotNil(t, link.TargetVersion)
			versions[b2LinkID(t, link)] = *link.TargetVersion
		}
		require.Equal(t, oldVersion, versions[b2LinkID(t, old)])
		require.Equal(t, newVersion, versions[b2LinkID(t, newer)])
		history := f.call(t, http.MethodGet, path+"/revisions", nil, owner.caller())
		require.Equal(t, http.StatusOK, history.Code)
		var revisions struct {
			Revisions []struct {
				Version int    `json:"version"`
				Content string `json:"content"`
			} `json:"revisions"`
		}
		require.NoError(t, json.Unmarshal(history.Body.Bytes(), &revisions))
		require.Len(t, revisions.Revisions, 1)
		require.Equal(t, versions[b2LinkID(t, old)], revisions.Revisions[0].Version)
		require.Equal(t, target.Content, revisions.Revisions[0].Content)
		require.NotEqual(t, updated.Content, revisions.Revisions[0].Content)
		reverse := b2CreateLink(t, f, target.ID, source.ID, "references", uuid.NewString(), &source.Version, owner.caller(), http.StatusCreated)
		require.NotEqual(t, b2LinkID(t, old), b2LinkID(t, reverse))
	})

	t.Run("access_scope_filtering_and_lifecycle", func(t *testing.T) {
		f := newOwnCardContextFixture(t, false)
		owner := f.registered(t, "links-owner")
		other := f.registered(t, "links-other")
		replacement := f.publicKeycard(t, owner.identity)
		readonly := f.publicKeycard(t, owner.identity)
		require.NoError(t, f.store.DB.Table("api_tokens").Where("id = ?", readonly.id).Update("scope", "read-only").Error)
		source := f.create(t, owner, ownCardContextPayload("synthetic access source"))
		privatePayload := ownCardContextPayload("synthetic workstation private target")
		privatePayload["privacy_scope"] = "private"
		private := f.create(t, owner, privatePayload)
		visible := f.create(t, owner, ownCardContextPayload("synthetic visible target"))
		hiddenLink := b2CreateLink(t, f, source.ID, private.ID, "related_to", uuid.NewString(), nil, owner.caller(), http.StatusCreated)
		visibleLink := b2CreateLink(t, f, source.ID, visible.ID, "related_to", uuid.NewString(), nil, owner.caller(), http.StatusCreated)
		ownerPage := b2ListLinks(t, f, source.ID, owner.caller(), 10)
		require.Len(t, ownerPage.Links, 2)
		ownerIDs := []string{b2LinkID(t, ownerPage.Links[0]), b2LinkID(t, ownerPage.Links[1])}
		require.Contains(t, ownerIDs, b2LinkID(t, hiddenLink))
		require.Contains(t, ownerIDs, b2LinkID(t, visibleLink))
		before := f.state(t)
		foreign := f.call(t, http.MethodGet, b2MemoryLinksPath(source.ID)+"?limit=1", nil, other.caller())
		require.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, foreign.Code)
		for _, secret := range []string{`"source_id"`, `"target_id"`, `"relation"`, private.Content, owner.identity.Principal} {
			require.NotContains(t, foreign.Body.String(), secret)
		}
		denied := f.call(t, http.MethodPost, b2MemoryLinksPath(source.ID), map[string]any{"target_id": visible.ID, "relation": "related_to"}, other.caller())
		require.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, denied.Code)
		denied = f.call(t, http.MethodPost, b2MemoryLinksPath(source.ID), map[string]any{"target_id": visible.ID, "relation": "related_to"}, readonly.caller())
		require.Equal(t, http.StatusForbidden, denied.Code)
		denied = f.call(t, http.MethodDelete, "/api/memory-links/"+b2LinkID(t, visibleLink), nil, readonly.caller())
		require.Equal(t, http.StatusForbidden, denied.Code)
		require.Len(t, b2ListLinks(t, f, source.ID, readonly.caller(), 10).Links, 1, "read-only owner may read visible links")
		page := b2ListLinks(t, f, source.ID, replacement.caller(), 1)
		require.Len(t, page.Links, 1, "authorization must filter before limiting")
		require.Equal(t, b2LinkID(t, visibleLink), b2LinkID(t, page.Links[0]))
		require.JSONEq(t, `null`, string(page.NextCursor), "hidden edges must not influence the page cursor")
		require.Equal(t, before, f.state(t))
		// Fixture-only state control: this is not an ordinary self-retirement API proof.
		require.NoError(t, f.store.DB.Table("memories").Where("id = ?", visible.ID).Update("status", "archived").Error)
		require.Empty(t, b2ListLinks(t, f, source.ID, replacement.caller(), 10).Links)
		require.NoError(t, f.store.DB.Table("memories").Where("id = ?", visible.ID).Update("status", visible.Status).Error)
		page = b2ListLinks(t, f, source.ID, replacement.caller(), 10)
		require.Len(t, page.Links, 1)
		require.Equal(t, b2LinkID(t, visibleLink), b2LinkID(t, page.Links[0]))
		b2DeleteLink(t, f, b2LinkID(t, visibleLink), owner.caller())
		require.NoError(t, f.store.DB.Table("memories").Where("id = ?", visible.ID).Update("status", "archived").Error)
		require.NoError(t, f.store.DB.Table("memories").Where("id = ?", visible.ID).Update("status", visible.Status).Error)
		require.Empty(t, b2ListLinks(t, f, source.ID, replacement.caller(), 10).Links, "restoration must not resurrect an explicitly removed link")
	})

	t.Run("auth_disabled_requires_verified_key", func(t *testing.T) {
		f := newOwnCardContextFixture(t, false)
		owner := f.registered(t, "links-noauth")
		a := f.create(t, owner, ownCardContextPayload("synthetic disabled source"))
		b := f.create(t, owner, ownCardContextPayload("synthetic disabled target"))
		t.Setenv("ENGRAM_AUTH_DISABLED", "true")
		f.reopen(t)
		before := f.state(t)
		payload := map[string]any{"target_id": b.ID, "relation": "related_to", "request_key": uuid.NewString(), "principal": owner.identity.Principal, "principal_kind": owner.identity.PrincipalKind, "context": map[string]any{"kind": "self"}, "session_id": "forged-session"}
		for _, caller := range []ownCardContextCaller{{}, {explicit: true}, {cookie: f.admin.cookie}, {token: "invalid-synthetic-key", explicit: true}} {
			response := f.call(t, http.MethodPost, b2MemoryLinksPath(a.ID), payload, caller)
			require.Contains(t, []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden}, response.Code)
		}
		require.Equal(t, before, f.state(t))
		link := b2CreateLink(t, f, a.ID, b.ID, "related_to", uuid.NewString(), nil, owner.caller(), http.StatusCreated)
		page := b2ListLinks(t, f, a.ID, owner.caller(), 10)
		require.Len(t, page.Links, 1)
		require.Equal(t, b2LinkID(t, link), b2LinkID(t, page.Links[0]))
		b2DeleteLink(t, f, b2LinkID(t, link), owner.caller())
		require.Empty(t, b2ListLinks(t, f, a.ID, owner.caller(), 10).Links)
		require.Equal(t, before, f.state(t))
	})
}

func TestB2LinksOrdinaryHTTPConsumer(t *testing.T) {
	f := newOwnCardContextFixture(t, false)
	owner := f.registered(t, "tcp-owner")
	other := f.registered(t, "tcp-other")
	server := httptest.NewServer(f.service.router)
	t.Cleanup(server.Close)
	client := server.Client()
	call := func(method, path string, payload any, caller ownCardContextCaller, status int, result any) {
		t.Helper()
		req := ownCardContextRequest(t, method, path, payload, caller)
		req.URL.Scheme = "http"
		req.URL.Host = server.Listener.Addr().String()
		req.Host = req.URL.Host
		req.RequestURI = ""
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, status, resp.StatusCode)
		if result != nil {
			require.NoError(t, json.NewDecoder(resp.Body).Decode(result))
		}
	}
	projects := f.state(t).projects
	var first, second ownCardContextRecord
	call(http.MethodPost, "/api/memories", ownCardContextPayload("The battery is in the blue locker."), owner.caller(), http.StatusCreated, &first)
	call(http.MethodPost, "/api/memories", ownCardContextPayload("The blue locker opens with the silver key."), owner.caller(), http.StatusCreated, &second)
	require.NotEqual(t, first.ID, second.ID)
	key := uuid.NewString()
	payload := map[string]any{"target_id": second.ID, "relation": "references", "target_version": second.Version, "request_key": key}
	path := b2MemoryLinksPath(first.ID)
	var link, replay b2Link
	call(http.MethodPost, path, payload, owner.caller(), http.StatusCreated, &link)
	call(http.MethodPost, path, payload, owner.caller(), http.StatusOK, &replay)
	require.Equal(t, link, replay)
	call(http.MethodGet, path, nil, other.caller(), http.StatusNotFound, nil)
	var page b2LinkPage
	call(http.MethodGet, path, nil, owner.caller(), http.StatusOK, &page)
	require.Len(t, page.Links, 1)
	require.Equal(t, second.ID, page.Links[0].TargetID)
	var target ownCardContextRecord
	call(http.MethodGet, "/api/memories/"+strconv.FormatInt(page.Links[0].TargetID, 10), nil, owner.caller(), http.StatusOK, &target)
	require.Equal(t, second.Content, target.Content)
	call(http.MethodDelete, "/api/memory-links/"+b2LinkID(t, link), nil, owner.caller(), http.StatusNoContent, nil)
	call(http.MethodPost, path, payload, owner.caller(), http.StatusConflict, nil)
	call(http.MethodGet, path, nil, owner.caller(), http.StatusOK, &page)
	require.Empty(t, page.Links)
	require.Equal(t, projects, f.state(t).projects)
}

func TestB2LinkSessionProvenanceByteLimit(t *testing.T) {
	f := newOwnCardContextFixture(t, false)
	owner := f.registered(t, "session-size")
	a := f.create(t, owner, ownCardContextPayload("session source"))
	b := f.create(t, owner, ownCardContextPayload("session target"))
	c := f.create(t, owner, ownCardContextPayload("oversize target"))
	session := strings.Repeat("я", 128)
	payload := map[string]any{"target_id": b.ID, "relation": "references", "source_session_id": session}
	created := b2DecodeLink(t, f.call(t, http.MethodPost, b2MemoryLinksPath(a.ID), payload, owner.caller()), http.StatusCreated)
	require.Equal(t, session, created.SourceSessionID, "opaque Unicode provenance must survive exactly")
	page := b2ListLinks(t, f, a.ID, owner.caller(), 10)
	require.Len(t, page.Links, 1)
	require.Equal(t, session, page.Links[0].SourceSessionID)
	payload["target_id"] = c.ID
	payload["source_session_id"] = session + "x"
	require.Equal(t, http.StatusBadRequest, f.call(t, http.MethodPost, b2MemoryLinksPath(a.ID), payload, owner.caller()).Code, "oversized provenance must not create a row")
	require.Len(t, b2ListLinks(t, f, a.ID, owner.caller(), 10).Links, 1)
}
