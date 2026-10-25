package worker

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/projectidentity"
)

type retainedProjectResponse struct {
	AttributionScope string  `json:"attribution_scope,omitempty"`
	Reference        string  `json:"reference"`
	Kind             string  `json:"kind,omitempty"`
	ID               int64   `json:"id,omitempty"`
	Items            any     `json:"items"`
	Total            int64   `json:"total"`
	NextCursor       *string `json:"next_cursor"`
}

type retainedProjectCursor struct {
	Binding string `json:"b"`
	Offset  int    `json:"o"`
}

func retainedProjectPage(r *http.Request, reference, revision string) (limit, offset int, binding string, err error) {
	limit = 50
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return
	}
	for key, values := range params {
		if len(values) != 1 {
			err = errors.New("duplicate reader parameter")
			return
		}
		if key != "kind" && key != "id" && key != "limit" && key != "cursor" && key != "q" && key != "attribution_scope" {
			err = errors.New("unknown reader parameter")
			return
		}
	}
	if params.Has("attribution_scope") && (params.Get("attribution_scope") != retainedHistoryScope || strings.HasSuffix(r.URL.Path, "/code")) {
		err = errors.New("invalid retained attribution scope")
		return
	}
	if params.Has("limit") {
		limit, err = strconv.Atoi(params.Get("limit"))
		if err != nil || limit < 1 || limit > 200 {
			err = errors.New("limit must be 1..200")
			return
		}
	}
	if !boundedKnowledgeText(params.Get("q"), 1024) {
		err = errors.New("invalid search query")
		return
	}
	digest := sha256.Sum256([]byte(reference + "\x00" + revision + "\x00" + r.URL.Path + "\x00" + params.Get("kind") + "\x00" + params.Get("id") + "\x00" + params.Get("q") + "\x00" + strconv.Itoa(limit)))
	binding = hex.EncodeToString(digest[:])
	if raw := params.Get("cursor"); raw != "" {
		if len(raw) > 512 {
			err = errors.New("invalid cursor")
			return
		}
		var encoded []byte
		encoded, err = base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			return
		}
		var cursor retainedProjectCursor
		err = json.Unmarshal(encoded, &cursor)
		if err == nil && (cursor.Binding != binding || cursor.Offset < 0 || cursor.Offset > int(^uint(0)>>1)-limit) {
			err = errors.New("cursor does not match this reader")
		}
		offset = cursor.Offset
	}
	return
}

func retainedProjectNext(binding string, offset, limit int, total int64) *string {
	if int64(offset)+int64(limit) >= total {
		return nil
	}
	raw, _ := json.Marshal(retainedProjectCursor{Binding: binding, Offset: offset + limit})
	value := base64.RawURLEncoding.EncodeToString(raw)
	return &value
}

func (s *Service) logicalMembershipStore(w http.ResponseWriter, r *http.Request) *gormdb.ProjectConsolidationStore {
	w.Header().Set("Cache-Control", "private, no-store")
	identity, ok := auth.IdentityFrom(r.Context())
	if !ok || !hapHTTPRouteAllowed(identity, r.Method, r.URL.Path) {
		writeDocumentError(w, http.StatusForbidden, "project read forbidden")
		return nil
	}
	if s.store == nil || s.vault == nil || s.sessionStore == nil {
		writeDocumentError(w, http.StatusServiceUnavailable, "project membership unavailable")
		return nil
	}
	return gormdb.NewProjectConsolidationStore(s.store.GetDB(), s.vault, nil)
}

func writeRetainedProjectError(w http.ResponseWriter, err error) {
	var refusal *gormdb.ConsolidationError
	if errors.As(err, &refusal) {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": refusal.Code})
		return
	}
	writeDocumentError(w, http.StatusServiceUnavailable, "retained content unavailable")
}

func (s *Service) handleResolveLogicalProject(w http.ResponseWriter, r *http.Request) {
	store := s.logicalMembershipStore(w, r)
	if store == nil {
		return
	}
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(params["legacy_id"]) != 1 || !boundedKnowledgeText(params.Get("legacy_id"), 1024) || params.Get("legacy_id") == "" {
		writeDocumentError(w, 400, "legacy_id is required")
		return
	}
	selector := params.Get("legacy_id")
	if projectidentity.ValidProjectID(selector) {
		exists, err := s.sessionStore.CanonicalProjectExists(r.Context(), selector)
		if err != nil {
			writeRetainedProjectError(w, err)
			return
		}
		if exists {
			writeJSON(w, map[string]string{"status": "resolved", "project_id": selector, "name": s.getProjectDisplayName(r.Context(), selector)})
			return
		}
	}
	sessionID, _ := operatorCodeSessionID(r)
	id, err := store.ResolveLegacy(r.Context(), selector, sessionID)
	if err == nil {
		exists, lookupErr := s.sessionStore.CanonicalProjectExists(r.Context(), id)
		if lookupErr != nil {
			writeRetainedProjectError(w, lookupErr)
			return
		}
		if exists {
			writeJSON(w, map[string]string{"status": "resolved", "project_id": id, "name": s.getProjectDisplayName(r.Context(), id)})
			return
		}
	} else {
		var refusal *gormdb.ConsolidationError
		if !errors.As(err, &refusal) {
			writeRetainedProjectError(w, err)
			return
		}
	}
	unresolved, err := store.LegacyUnresolved(r.Context(), selector)
	if err != nil {
		writeRetainedProjectError(w, err)
		return
	}
	writeJSON(w, map[string]any{"status": "unresolved", "reference": unresolved, "reason": "Historical membership remains unresolved"})
}

// handleRetainedProjectContent godoc
// @Summary Read an exact retained association
// @Description Validates the complete selected reference/kind group, then reads its visible historical native payloads. Optional positive id selects only that exact member; without id the old whole-group behavior remains. Optional attribution_scope accepts only unresolved_history; omitted scope has the same historical meaning. Responses and record projections identify unresolved_history and omit project_key. A stale group is refused, never repaired through another group. Memory privacy remains enforced; documents/issues have ordinary authenticated route and exact-membership authority. private,no-store on success and errors.
// @Tags Observations
// @Produce json
// @Security ApiKeyAuth
// @Param reference path string true "Exact issued unresolved reference"
// @Param kind query string true "Native family" Enums(memory,document,issue)
// @Param attribution_scope query string false "Historical attribution only" Enums(unresolved_history)
// @Param id query int false "Exact positive native ID within this reference and kind"
// @Param q query string false "Literal retained-reader search"
// @Param limit query int false "Page size 1..200, default 50"
// @Param cursor query string false "Continuation bound to reference, group revision, reader, kind, id, query and limit"
// @Success 200 {object} retainedProjectResponse
// @Failure 400 {object} documentErrorResponse "invalid retained reader parameters"
// @Failure 403 {object} documentErrorResponse "project read forbidden"
// @Failure 404 {object} documentErrorResponse "unknown/stale association or foreign native ID"
// @Failure 503 {object} documentErrorResponse "retained content unavailable"
// @Router /api/projects/unresolved/{reference}/content [get]
func (s *Service) handleRetainedProjectContent(w http.ResponseWriter, r *http.Request) {
	s.readRetainedProject(w, r, false)
}

// handleRetainedProjectHistory godoc
// @Summary Read history for an exact retained native record
// @Description Requires the exact issued reference/kind/positive id and complete current group validation. Optional attribution_scope accepts only unresolved_history; old omitted scope keeps that meaning. Document history preserves validated group IDs plus the selected path and historical project, including other versions, never one-ID truncation. Memory history keeps visibility and manage checks. Historical projections omit project_key. Stale/foreign selections are refused without reassociation. private,no-store on success and errors.
// @Tags Observations
// @Produce json
// @Security ApiKeyAuth
// @Param reference path string true "Exact issued unresolved reference"
// @Param kind query string true "Native family" Enums(memory,document,issue)
// @Param id query int true "Exact positive native ID within this reference and kind"
// @Param attribution_scope query string false "Historical attribution only" Enums(unresolved_history)
// @Param limit query int false "Page size 1..200, default 50"
// @Param cursor query string false "Continuation bound to the exact selection and group revision"
// @Success 200 {object} retainedProjectResponse
// @Failure 400 {object} documentErrorResponse "invalid retained reader parameters"
// @Failure 403 {object} documentErrorResponse "project read forbidden"
// @Failure 404 {object} documentErrorResponse "unknown/stale/foreign/hidden association or denied memory history"
// @Failure 503 {object} documentErrorResponse "retained history unavailable"
// @Router /api/projects/unresolved/{reference}/history [get]
func (s *Service) handleRetainedProjectHistory(w http.ResponseWriter, r *http.Request) {
	s.readRetainedProject(w, r, true)
}

func retainedProjectReference(r *http.Request) (string, error) {
	reference := chi.URLParam(r, "reference")
	if r.URL.RawPath != "" {
		return url.PathUnescape(reference)
	}
	return reference, nil
}

func (s *Service) readRetainedProject(w http.ResponseWriter, r *http.Request, history bool) {
	store := s.logicalMembershipStore(w, r)
	if store == nil {
		return
	}
	reference, err := retainedProjectReference(r)
	if err != nil {
		writeDocumentError(w, http.StatusBadRequest, "invalid retained reference")
		return
	}
	kind := r.URL.Query().Get("kind")
	rows, err := store.UnresolvedMembership(r.Context(), reference, kind)
	if err != nil {
		writeRetainedProjectError(w, err)
		return
	}
	ids, err := gormdb.RetainedContentIDs(rows)
	if err != nil {
		writeRetainedProjectError(w, err)
		return
	}
	revision := strings.Builder{}
	for _, row := range rows {
		revision.WriteString(row.Reference)
		revision.WriteString(row.PlanFingerprint)
	}
	limit, offset, binding, err := retainedProjectPage(r, reference, revision.String())
	if err != nil {
		writeDocumentError(w, 400, "invalid retained reader page")
		return
	}
	response := retainedProjectResponse{Reference: reference, Kind: kind, AttributionScope: retainedHistoryScope}
	var id int64
	if history || r.URL.Query().Has("id") {
		id, err = strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		found := false
		for _, member := range ids {
			if member == id {
				found = true
				break
			}
		}
		if err != nil || id < 1 {
			writeDocumentError(w, 400, "invalid content id")
			return
		}
		if !found {
			writeDocumentError(w, 404, "content not in retained membership")
			return
		}
		response.ID = id
	}
	caller, opts := memoryVisibilityCaller(r.Context(), ""), memoryVisibilityOptions()
	if !history {
		bodyIDs := ids
		if id > 0 {
			bodyIDs = []int64{id}
		}
		response.Items, response.Total, err = s.readRetainedBodies(r.Context(), kind, bodyIDs, r.URL.Query().Get("q"), caller, opts, limit, offset)
	} else {
		switch kind {
		case "memory":
			memories, _, readErr := s.sessionStore.RetainedMemories(r.Context(), []int64{id}, "", caller, opts, 1, 0)
			if readErr != nil {
				err = readErr
				break
			}
			if len(memories) != 1 || !memoryDomainManageAllowedREST(r.Context(), memories[0]) {
				writeDocumentError(w, 404, "memory history not found")
				return
			}
			entries, total, readErr := s.sessionStore.RetainedMemoryAudit(r.Context(), id, limit, offset)
			response.Items, response.Total, err = mapMemoryAuditResponse(id, entries).Entries, total, readErr
		case "document":
			selected, _, readErr := s.sessionStore.RetainedDocuments(r.Context(), []int64{id}, "", 1, 0)
			if readErr != nil {
				err = readErr
				break
			}
			if len(selected) != 1 {
				writeDocumentError(w, 404, "document history not found")
				return
			}
			versions, total, readErr := s.sessionStore.RetainedDocumentHistory(r.Context(), ids, selected[0], limit, offset)
			response.Items, response.Total, err = documentHistoryItems(versions), total, readErr
		case "issue":
			items, total, readErr := s.sessionStore.RetainedIssueHistory(r.Context(), id, limit, offset)
			response.Items, response.Total, err = items, total, readErr
		default:
			writeDocumentError(w, 400, "invalid content kind")
			return
		}
	}
	if err != nil {
		writeRetainedProjectError(w, err)
		return
	}
	response.Items, err = retainedHistoricalItems(response.Items)
	if err != nil {
		writeRetainedProjectError(w, err)
		return
	}
	response.NextCursor = retainedProjectNext(binding, offset, limit, response.Total)
	writeJSON(w, response)
}

func (s *Service) handleRetainedProjectCode(w http.ResponseWriter, r *http.Request) {
	store := s.logicalMembershipStore(w, r)
	if store == nil {
		return
	}
	reference, err := retainedProjectReference(r)
	if err != nil {
		writeDocumentError(w, http.StatusBadRequest, "invalid retained reference")
		return
	}
	rows, err := store.UnresolvedMembership(r.Context(), reference, "code")
	if err != nil {
		writeRetainedProjectError(w, err)
		return
	}
	revision := strings.Builder{}
	for _, row := range rows {
		revision.WriteString(row.Reference)
		revision.WriteString(row.PlanFingerprint)
	}
	limit, offset, binding, err := retainedProjectPage(r, reference, revision.String())
	if err != nil {
		writeDocumentError(w, 400, "invalid retained reader page")
		return
	}
	adapter := s.operatorCodeAdapter
	if adapter == nil || adapter.contexts == nil {
		writeDocumentError(w, 503, "code catalog unavailable")
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())
	filter := gormdb.BrowserCodeCatalogFilter{UnresolvedReference: reference}
	var entries []gormdb.BrowserCodeContextCatalogEntry
	if identity.Source == auth.SourceAuthDisabled {
		entries, err = adapter.contexts.ListNoAuthCatalog(r.Context(), filter)
	} else if subject, ok := identity.SessionBrowserSubject(); ok {
		entries, err = adapter.contexts.ListCatalog(r.Context(), subject.UserID, filter)
	} else {
		writeDocumentError(w, 403, "code read forbidden")
		return
	}
	if err != nil {
		writeRetainedProjectError(w, err)
		return
	}
	items := make([]operatorCodeCatalogEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.ProjectID != nil || entry.UnresolvedReference == nil || *entry.UnresolvedReference != reference {
			writeDocumentError(w, 409, "retained code membership changed")
			return
		}
		item := operatorCodeCatalogEntry{ProjectID: nil, UnresolvedReference: entry.UnresolvedReference, Repository: entry.SourceLabel, WorkingCopy: entry.CheckoutLabel, SourceRef: operatorCodeCatalogRef("source", entry.SourceID), CheckoutRef: operatorCodeCatalogRef("checkout", entry.SourceID, entry.CheckoutID)}
		if entry.Context != nil {
			item.ViewRef = operatorCodeCatalogRef("view", strconv.FormatInt(identity.BrowserSubject.UserID, 10), entry.Context.SourceID, entry.Context.CheckoutID, entry.Context.ViewID, entry.Context.AnalysisProfileID, strconv.FormatInt(entry.Context.Generation, 10))
			item.IndexedSnapshot = &operatorCodeCatalogView{Label: entry.ViewLabel, Revision: entry.SnapshotRevision, PublishedAt: entry.SnapshotPublishedAt}
		}
		items = append(items, item)
	}
	total := int64(len(items))
	if offset > len(items) {
		offset = len(items)
	}
	end := min(offset+limit, len(items))
	writeJSON(w, retainedProjectResponse{Reference: reference, Items: items[offset:end], Total: total, NextCursor: retainedProjectNext(binding, offset, limit, total)})
}
