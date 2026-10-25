package worker

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/scope"
)

const retainedHistoryScope = "unresolved_history"

type retainedRecordQuery struct {
	Kind   string
	Query  string
	Limit  int
	Cursor string
}

type retainedRecordItem struct {
	Reference string                     `json:"reference"`
	ID        int64                      `json:"id"`
	Record    map[string]json.RawMessage `json:"record"`
}

type retainedRecordResponse struct {
	View             string               `json:"view"`
	AttributionScope string               `json:"attribution_scope"`
	Kind             string               `json:"kind"`
	Items            []retainedRecordItem `json:"items"`
	NextCursor       *string              `json:"next_cursor"`
}

func parseRetainedRecordQuery(r *http.Request) (retainedRecordQuery, error) {
	query := retainedRecordQuery{Limit: 50}
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return query, err
	}
	for key, values := range params {
		if len(values) != 1 {
			return query, errors.New("duplicate retained query parameter")
		}
		switch key {
		case "view", "attribution_scope", "kind", "q", "limit", "cursor":
		default:
			return query, errors.New("unknown retained query parameter")
		}
	}
	if params.Get("view") != "retained_records" || params.Get("attribution_scope") != retainedHistoryScope {
		return query, errors.New("invalid retained query scope")
	}
	query.Kind = params.Get("kind")
	if query.Kind != "memory" && query.Kind != "document" && query.Kind != "issue" {
		return query, errors.New("invalid retained query kind")
	}
	if text := params.Get("q"); !utf8.ValidString(text) || len(text) > 1024 {
		return query, errors.New("invalid retained query text")
	} else {
		query.Query = strings.TrimSpace(text)
	}
	if params.Has("limit") {
		query.Limit, err = strconv.Atoi(params.Get("limit"))
		if err != nil || query.Limit < 1 || query.Limit > 200 {
			return query, errors.New("invalid retained query limit")
		}
	}
	query.Cursor = params.Get("cursor")
	if params.Has("cursor") && query.Cursor == "" {
		return query, errors.New("empty retained query cursor")
	}
	return query, nil
}

func (s *Service) retainedRecordCipher() (cipher.AEAD, error) {
	if s.vault == nil {
		return nil, errors.New("retained cursor vault unavailable")
	}
	key := s.vault.DeriveKey("engram.retained-records/continuation/v1")
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func retainedRecordBinding(r *http.Request, query retainedRecordQuery) []byte {
	identity, _ := auth.IdentityFrom(r.Context())
	binding, _ := json.Marshal(struct {
		Identity                       auth.Identity
		Caller                         scope.KeycardContext
		Privacy                        scope.MemoryVisibilityOptions
		View, Attribution, Kind, Query string
		Limit                          int
	}{identity, memoryVisibilityCaller(r.Context(), ""), memoryVisibilityOptions(), "retained_records", retainedHistoryScope, query.Kind, query.Query, query.Limit})
	return binding
}

func openRetainedRecordCursor(aead cipher.AEAD, raw string, binding []byte) (gormdb.RetainedRecordPosition, error) {
	var position gormdb.RetainedRecordPosition
	if len(raw) > 1024 {
		return position, errors.New("invalid retained cursor")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) < aead.NonceSize()+aead.Overhead() {
		return position, errors.New("invalid retained cursor")
	}
	plaintext, err := aead.Open(nil, decoded[:aead.NonceSize()], decoded[aead.NonceSize():], binding)
	if err != nil {
		return position, err
	}
	if json.Unmarshal(plaintext, &position) != nil || position.BeforeID < 0 || position.PendingID < 0 || (position.BeforeID == 0 && position.PendingID == 0) || (position.PendingID == 0 && position.AfterWitness != "") || len(position.AfterWitness) > 128 || (position.BeforeID > 0 && position.PendingID >= position.BeforeID) {
		return position, errors.New("invalid retained cursor position")
	}
	return position, nil
}

func sealRetainedRecordCursor(aead cipher.AEAD, position *gormdb.RetainedRecordPosition, binding []byte) (*string, error) {
	if position == nil {
		return nil, nil
	}
	plaintext, err := json.Marshal(position)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	value := base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, plaintext, binding))
	return &value, nil
}

func (s *Service) handleRetainedRecordQuery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	fail := func(status int, code string) { writeJSONStatus(w, status, map[string]string{"error": code}) }
	query, err := parseRetainedRecordQuery(r)
	if err != nil {
		fail(http.StatusBadRequest, "INVALID_QUERY")
		return
	}
	identity, ok := auth.IdentityFrom(r.Context())
	if !ok || !hapHTTPRouteAllowed(identity, r.Method, r.URL.Path) {
		writeDocumentError(w, http.StatusForbidden, "project read forbidden")
		return
	}
	aead, err := s.retainedRecordCipher()
	if err != nil {
		fail(http.StatusServiceUnavailable, "RETAINED_RECORDS_UNAVAILABLE")
		return
	}
	binding := retainedRecordBinding(r, query)
	var position gormdb.RetainedRecordPosition
	if query.Cursor != "" {
		position, err = openRetainedRecordCursor(aead, query.Cursor, binding)
		if err != nil {
			fail(http.StatusBadRequest, "INVALID_CURSOR")
			return
		}
	}
	if s.store == nil || s.store.GetDB() == nil || s.sessionStore == nil {
		fail(http.StatusServiceUnavailable, "RETAINED_RECORDS_UNAVAILABLE")
		return
	}
	store := gormdb.NewProjectConsolidationStore(s.store.GetDB(), s.vault, nil)
	selected, next, err := s.sessionStore.RetainedRecordSelections(r.Context(), store, query.Kind, query.Query, query.Limit, position, memoryVisibilityCaller(r.Context(), ""), memoryVisibilityOptions())
	if err != nil {
		fail(http.StatusServiceUnavailable, "RETAINED_RECORDS_UNAVAILABLE")
		return
	}
	items := make([]retainedRecordItem, 0, len(selected))
	if len(selected) > 0 {
		ids := make([]int64, len(selected))
		for i := range selected {
			ids[i] = selected[i].ID
		}
		bodies, _, readErr := s.readRetainedBodies(r.Context(), query.Kind, ids, query.Query, memoryVisibilityCaller(r.Context(), ""), memoryVisibilityOptions(), query.Limit, 0)
		if readErr != nil {
			fail(http.StatusServiceUnavailable, "RETAINED_RECORDS_UNAVAILABLE")
			return
		}
		projected, readErr := retainedHistoricalItems(bodies)
		if readErr != nil {
			fail(http.StatusServiceUnavailable, "RETAINED_RECORDS_UNAVAILABLE")
			return
		}
		byID := make(map[int64]map[string]json.RawMessage, len(projected))
		for _, body := range projected {
			var id int64
			if json.Unmarshal(body["id"], &id) != nil || id < 1 {
				fail(http.StatusServiceUnavailable, "RETAINED_RECORDS_UNAVAILABLE")
				return
			}
			byID[id] = body
		}
		for _, selection := range selected {
			if body, visible := byID[selection.ID]; visible {
				items = append(items, retainedRecordItem{Reference: selection.Reference, ID: selection.ID, Record: body})
			}
		}
	}
	cursor, err := sealRetainedRecordCursor(aead, next, binding)
	if err != nil {
		fail(http.StatusServiceUnavailable, "RETAINED_RECORDS_UNAVAILABLE")
		return
	}
	writeJSON(w, retainedRecordResponse{View: "retained_records", AttributionScope: retainedHistoryScope, Kind: query.Kind, Items: items, NextCursor: cursor})
}

func retainedHistoricalItems(items any) ([]map[string]json.RawMessage, error) {
	raw, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	projected := make([]map[string]json.RawMessage, 0)
	if err := json.Unmarshal(raw, &projected); err != nil {
		return nil, err
	}
	if projected == nil {
		projected = make([]map[string]json.RawMessage, 0)
	}
	for _, item := range projected {
		delete(item, "project_key")
		item["attribution_scope"] = json.RawMessage(`"unresolved_history"`)
	}
	return projected, nil
}

func (s *Service) readRetainedBodies(ctx context.Context, kind string, ids []int64, query string, caller scope.KeycardContext, opts scope.MemoryVisibilityOptions, limit, offset int) (any, int64, error) {
	switch kind {
	case "memory":
		items, total, err := s.sessionStore.RetainedMemories(ctx, ids, query, caller, opts, limit, offset)
		return jsonSafeMemories(items), total, err
	case "document":
		docs, total, err := s.sessionStore.RetainedDocuments(ctx, ids, query, limit, offset)
		items := make([]documentReadResponse, len(docs))
		for i := range docs {
			items[i] = documentReadItem(&docs[i])
		}
		return items, total, err
	case "issue":
		if s.issueStore == nil {
			return nil, 0, errors.New("retained issue store unavailable")
		}
		issues, total, err := s.sessionStore.RetainedIssues(ctx, ids, query, limit, offset)
		if err != nil {
			return nil, 0, err
		}
		issueIDs := make([]int64, len(issues))
		for i := range issues {
			issueIDs[i] = issues[i].ID
		}
		roles, err := s.issueStore.ProjectRoles(ctx, issueIDs)
		if err != nil {
			return nil, 0, err
		}
		items := make([]issueReadItem, len(issues))
		for i := range issues {
			items[i] = issueReadProjection(issues[i], roles[issues[i].ID])
		}
		return items, total, nil
	default:
		return nil, 0, errors.New("invalid retained body kind")
	}
}
