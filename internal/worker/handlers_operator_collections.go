package worker

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

const operatorCollectionPageDefault = 50

// OperatorCollectionHTTPAdapter is the shared browser-only boundary for
// collection snapshots and bounded pages. It never dispatches a domain action.
type OperatorCollectionHTTPAdapter struct {
	store      operatorCollectionSelectionStore
	resolver   operatorCollectionScopeResolver
	normalizer operatorCollectionFilterNormalizer
	freezer    operatorCollectionFreezer
	pager      operatorCollectionPager
}

type operatorCollectionSelectionStore interface {
	Save(context.Context, gormdb.CollectionSelectionScope, gormdb.CollectionSelection) (gormdb.CollectionSelection, error)
	Current(context.Context, gormdb.CollectionSelectionScope) (gormdb.CollectionSelection, error)
}

// operatorCollectionScopeResolver derives the current server-side collection
// revision and authorization epoch. Browser JSON cannot select either value.
type operatorCollectionScopeResolver interface {
	ResolveOperatorCollectionScope(context.Context, auth.Identity, string, string) (gormdb.CollectionSelectionScope, error)
}

// operatorCollectionFilterNormalizer canonicalizes browser filter values before
// they can select a page or frozen snapshot. It never accepts a client digest.
type operatorCollectionFilterNormalizer interface {
	NormalizeCollectionFilter(context.Context, gormdb.CollectionSelectionScope, string) (gormdb.CollectionFilter, error)
}

// operatorCollectionFreezer authorizes and freezes membership for one canonical
// filter or previously server-issued current page. It is deliberately separate
// from every domain action.
type operatorCollectionFreezer interface {
	FreezeCollectionSelection(context.Context, gormdb.CollectionSelectionScope, gormdb.CollectionFilter) (gormdb.CollectionFrozenSelection, error)
	FreezeCollectionPageSelection(context.Context, gormdb.CollectionSelectionScope, string) ([]gormdb.CollectionSelectionTarget, error)
}

// operatorCollectionPager returns one bounded page after its domain authorizes
// the request. A page result carries no selection token and cannot authorize an action.
type operatorCollectionPager interface {
	PageCollection(context.Context, gormdb.CollectionSelectionScope, gormdb.CollectionPageRequest) (gormdb.CollectionPage, error)
}

func NewOperatorCollectionHTTPAdapter(
	store operatorCollectionSelectionStore,
	resolver operatorCollectionScopeResolver,
	normalizer operatorCollectionFilterNormalizer,
	freezer operatorCollectionFreezer,
	pager operatorCollectionPager,
) *OperatorCollectionHTTPAdapter {
	return &OperatorCollectionHTTPAdapter{store: store, resolver: resolver, normalizer: normalizer, freezer: freezer, pager: pager}
}

// HandleSnapshot stores none, explicit IDs, current-page IDs, or a server-frozen
// all-filter selection under the current browser owner/session/domain scope.
func (adapter *OperatorCollectionHTTPAdapter) HandleSnapshot(w http.ResponseWriter, r *http.Request) {
	var request operatorCollectionSnapshotRequest
	identity, scope, ok := adapter.decode(w, r, &request)
	if !ok || !request.valid() {
		if ok {
			operatorCodeWriteBodyless(w, http.StatusBadRequest)
		}
		return
	}
	resolved, ok := adapter.resolveScope(w, r.Context(), identity, scope, request.Domain)
	if !ok {
		return
	}

	selection, ok := adapter.snapshotSelection(w, r, resolved, request)
	if !ok {
		return
	}
	if err := gormdb.ValidateCollectionSelectionInput(selection); err != nil {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return
	}
	if adapter.store == nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	saved, err := adapter.store.Save(r.Context(), resolved, selection)
	if err != nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, operatorCollectionSnapshotResponse{Selection: newOperatorCollectionSnapshotDTO(request.Domain, saved)})
}

func (adapter *OperatorCollectionHTTPAdapter) snapshotSelection(w http.ResponseWriter, r *http.Request, scope gormdb.CollectionSelectionScope, request operatorCollectionSnapshotRequest) (gormdb.CollectionSelection, bool) {
	selection, err := request.selection()
	if err != nil {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return gormdb.CollectionSelection{}, false
	}
	switch selection.Kind {
	case gormdb.CollectionSelectionFrozenFilter:
		filter, normalized := adapter.normalizeFilter(w, r.Context(), scope, request.Selection.Filter)
		if !normalized {
			return gormdb.CollectionSelection{}, false
		}
		selection.FilterFingerprint = filter.Fingerprint
		if err := gormdb.ValidateCollectionFrozenSelectionRequest(selection.FilterFingerprint, selection.ExcludedIDs); err != nil {
			operatorCodeWriteBodyless(w, http.StatusBadRequest)
			return gormdb.CollectionSelection{}, false
		}
		if adapter.freezer == nil {
			operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
			return gormdb.CollectionSelection{}, false
		}
		frozen, err := adapter.freezer.FreezeCollectionSelection(r.Context(), scope, filter)
		if err != nil {
			operatorCollectionWriteSnapshotError(w, err)
			return gormdb.CollectionSelection{}, false
		}
		selection.Targets, selection.ExpiresAt = frozen.Targets, frozen.ExpiresAt
	case gormdb.CollectionSelectionPage:
		if adapter.freezer == nil {
			operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
			return gormdb.CollectionSelection{}, false
		}
		targets, err := adapter.freezer.FreezeCollectionPageSelection(r.Context(), scope, selection.Cursor)
		if err != nil {
			operatorCollectionWriteSnapshotError(w, err)
			return gormdb.CollectionSelection{}, false
		}
		selection.Targets = targets
	}
	return selection, true
}

// HandleCurrent resolves only the current browser-scoped snapshot. An opaque
// selection token is intentionally not accepted as a selector on this route.
func (adapter *OperatorCollectionHTTPAdapter) HandleCurrent(w http.ResponseWriter, r *http.Request) {
	var request operatorCollectionCurrentRequest
	identity, scope, ok := adapter.decode(w, r, &request)
	if !ok || !request.valid() {
		if ok {
			operatorCodeWriteBodyless(w, http.StatusBadRequest)
		}
		return
	}
	resolved, ok := adapter.resolveScope(w, r.Context(), identity, scope, request.Domain)
	if !ok {
		return
	}
	if adapter.store == nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	selection, err := adapter.store.Current(r.Context(), resolved)
	if err != nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, operatorCollectionSnapshotResponse{Selection: newOperatorCollectionSnapshotDTO(request.Domain, selection)})
}

// HandlePage delegates one validated page request to its domain pager. It does
// not save a selection and cannot turn a cursor into action authority.
func (adapter *OperatorCollectionHTTPAdapter) HandlePage(w http.ResponseWriter, r *http.Request) {
	var request operatorCollectionPageRequest
	identity, scope, ok := adapter.decode(w, r, &request)
	if !ok || !request.valid() {
		if ok {
			operatorCodeWriteBodyless(w, http.StatusBadRequest)
		}
		return
	}
	resolved, ok := adapter.resolveScope(w, r.Context(), identity, scope, request.Domain)
	if !ok {
		return
	}
	filter, normalized := adapter.normalizeFilter(w, r.Context(), resolved, request.Filter)
	if !normalized {
		return
	}
	pageRequest := request.pageRequest(filter)
	if !pageRequest.Valid() {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return
	}
	if adapter.pager == nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	page, err := adapter.pager.PageCollection(r.Context(), resolved, pageRequest)
	if err != nil {
		if errors.Is(err, gormdb.ErrCollectionSelectionReconfirmationRequired) {
			operatorCodeWriteBodyless(w, http.StatusPreconditionFailed)
		} else if errors.Is(err, gormdb.ErrCollectionSelectionDenied) {
			operatorCodeWriteBodyless(w, http.StatusForbidden)
		} else if errors.Is(err, gormdb.ErrCollectionSelectionInvalid) {
			operatorCodeWriteBodyless(w, http.StatusBadRequest)
		} else {
			operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		}
		return
	}
	if !page.ValidFor(pageRequest) {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, operatorCollectionPageResponse{FilterFingerprint: filter.Fingerprint, Cursor: page.Cursor, Targets: page.Targets, NextCursor: page.NextCursor, Total: page.Total})
}

type operatorCollectionRequestScope struct {
	identity  auth.Identity
	sessionID string
}

func (adapter *OperatorCollectionHTTPAdapter) decode(w http.ResponseWriter, r *http.Request, target any) (auth.Identity, operatorCollectionRequestScope, bool) {
	if adapter == nil || r == nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return auth.Identity{}, operatorCollectionRequestScope{}, false
	}
	identity, found := auth.IdentityFrom(r.Context())
	if !found {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return auth.Identity{}, operatorCollectionRequestScope{}, false
	}
	if _, found := identity.SessionBrowserSubject(); !found {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return auth.Identity{}, operatorCollectionRequestScope{}, false
	}
	sessionID, found := operatorCodeSessionID(r)
	if !found {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return auth.Identity{}, operatorCollectionRequestScope{}, false
	}
	if !operatorCodeText(r.Header.Get("X-Engram-Request-ID")) {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return auth.Identity{}, operatorCollectionRequestScope{}, false
	}
	if _, err := operatorCodeReadJSON(r, target); err != nil {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return auth.Identity{}, operatorCollectionRequestScope{}, false
	}
	return identity, operatorCollectionRequestScope{identity: identity, sessionID: sessionID}, true
}

func (adapter *OperatorCollectionHTTPAdapter) resolveScope(w http.ResponseWriter, ctx context.Context, identity auth.Identity, request operatorCollectionRequestScope, domain string) (gormdb.CollectionSelectionScope, bool) {
	if adapter == nil || adapter.resolver == nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return gormdb.CollectionSelectionScope{}, false
	}
	subject, ok := identity.SessionBrowserSubject()
	if !ok {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return gormdb.CollectionSelectionScope{}, false
	}
	scope, err := adapter.resolver.ResolveOperatorCollectionScope(ctx, identity, request.sessionID, domain)
	if err != nil || scope.SubjectUserID != subject.UserID || scope.SessionID != request.sessionID || scope.Domain != domain {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return gormdb.CollectionSelectionScope{}, false
	}
	return scope, true
}

func (adapter *OperatorCollectionHTTPAdapter) normalizeFilter(w http.ResponseWriter, ctx context.Context, scope gormdb.CollectionSelectionScope, request *operatorCollectionFilterRequest) (gormdb.CollectionFilter, bool) {
	if adapter == nil || adapter.normalizer == nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return gormdb.CollectionFilter{}, false
	}
	if request == nil {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return gormdb.CollectionFilter{}, false
	}
	filter, err := adapter.normalizer.NormalizeCollectionFilter(ctx, scope, request.Scope)
	if err != nil || !filter.Valid() {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return gormdb.CollectionFilter{}, false
	}
	return filter, true
}

func operatorCollectionWriteSnapshotError(w http.ResponseWriter, err error) {
	if errors.Is(err, gormdb.ErrCollectionSelectionInvalid) {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return
	}
	if errors.Is(err, gormdb.ErrCollectionSelectionReconfirmationRequired) {
		operatorCodeWriteBodyless(w, http.StatusPreconditionFailed)
		return
	}
	operatorCodeWriteBodyless(w, http.StatusForbidden)
}

type operatorCollectionSnapshotRequest struct {
	Domain    string                             `json:"domain"`
	Selection operatorCollectionSelectionRequest `json:"selection"`
}

type operatorCollectionSelectionRequest struct {
	Kind        gormdb.CollectionSelectionKind     `json:"kind"`
	Targets     []gormdb.CollectionSelectionTarget `json:"targets,omitempty"`
	Cursor      string                             `json:"cursor,omitempty"`
	Filter      *operatorCollectionFilterRequest   `json:"filter,omitempty"`
	ExcludedIDs []string                           `json:"excluded_ids,omitempty"`
}

type operatorCollectionFilterRequest struct {
	Scope string `json:"scope"`
}

func (request operatorCollectionSnapshotRequest) valid() bool {
	return gormdb.ValidCollectionSelectionDomain(request.Domain) && request.Selection.Kind != ""
}

func (request operatorCollectionSnapshotRequest) selection() (gormdb.CollectionSelection, error) {
	selection := gormdb.CollectionSelection{
		Kind:        request.Selection.Kind,
		Targets:     append([]gormdb.CollectionSelectionTarget(nil), request.Selection.Targets...),
		Cursor:      request.Selection.Cursor,
		ExcludedIDs: append([]string(nil), request.Selection.ExcludedIDs...),
	}
	if selection.Kind == gormdb.CollectionSelectionFrozenFilter && (request.Selection.Filter == nil || len(selection.Targets) != 0) {
		return gormdb.CollectionSelection{}, errors.New("frozen filter requires a server-resolved filter and targets")
	}
	if selection.Kind != gormdb.CollectionSelectionFrozenFilter && request.Selection.Filter != nil {
		return gormdb.CollectionSelection{}, errors.New("only frozen filters accept a filter")
	}
	return selection, nil
}

type operatorCollectionCurrentRequest struct {
	Domain string `json:"domain"`
}

func (request operatorCollectionCurrentRequest) valid() bool {
	return gormdb.ValidCollectionSelectionDomain(request.Domain)
}

type operatorCollectionPageRequest struct {
	Domain string                           `json:"domain"`
	Filter *operatorCollectionFilterRequest `json:"filter"`
	Cursor string                           `json:"cursor,omitempty"`
	Limit  int                              `json:"limit,omitempty"`
}

func (request operatorCollectionPageRequest) valid() bool {
	limit := request.Limit
	if limit == 0 {
		limit = operatorCollectionPageDefault
	}
	return gormdb.ValidCollectionSelectionDomain(request.Domain) && request.Filter != nil && (request.Cursor == "" || len(request.Cursor) <= 512) && limit >= 1 && limit <= gormdb.CollectionPageMaxSize
}

func (request operatorCollectionPageRequest) pageRequest(filter gormdb.CollectionFilter) gormdb.CollectionPageRequest {
	limit := request.Limit
	if limit == 0 {
		limit = operatorCollectionPageDefault
	}
	return gormdb.CollectionPageRequest{Domain: request.Domain, Filter: filter, Cursor: request.Cursor, Limit: limit}
}

type operatorCollectionSnapshotResponse struct {
	Selection operatorCollectionSnapshotDTO `json:"selection"`
}

type operatorCollectionSnapshotDTO struct {
	Domain                 string                                         `json:"domain"`
	Kind                   gormdb.CollectionSelectionKind                 `json:"kind"`
	SelectionVersion       int64                                          `json:"selection_version,omitempty"`
	Targets                []gormdb.CollectionSelectionTarget             `json:"targets,omitempty"`
	Cursor                 string                                         `json:"cursor,omitempty"`
	FilterFingerprint      string                                         `json:"filter_fingerprint,omitempty"`
	ExcludedIDs            []string                                       `json:"excluded_ids,omitempty"`
	SelectionToken         string                                         `json:"selection_token,omitempty"`
	TargetCount            int                                            `json:"target_count,omitempty"`
	ExpiresAt              string                                         `json:"expires_at,omitempty"`
	ReconfirmationRequired bool                                           `json:"reconfirmation_required"`
	ReconfirmationReason   gormdb.CollectionSelectionReconfirmationReason `json:"reconfirmation_reason,omitempty"`
}

func newOperatorCollectionSnapshotDTO(domain string, selection gormdb.CollectionSelection) operatorCollectionSnapshotDTO {
	response := operatorCollectionSnapshotDTO{
		Domain:                 domain,
		Kind:                   selection.Kind,
		SelectionVersion:       selection.Version,
		Targets:                append([]gormdb.CollectionSelectionTarget(nil), selection.Targets...),
		Cursor:                 selection.Cursor,
		FilterFingerprint:      selection.FilterFingerprint,
		ExcludedIDs:            append([]string(nil), selection.ExcludedIDs...),
		SelectionToken:         selection.Token,
		ReconfirmationRequired: selection.ReconfirmationRequired,
		ReconfirmationReason:   selection.ReconfirmationReason,
	}
	if selection.Kind == gormdb.CollectionSelectionFrozenFilter {
		response.TargetCount = len(selection.Targets) - len(selection.ExcludedIDs)
		if !selection.ExpiresAt.IsZero() {
			response.ExpiresAt = selection.ExpiresAt.UTC().Format(time.RFC3339Nano)
		}
	}
	return response
}

type operatorCollectionPageResponse struct {
	FilterFingerprint string                             `json:"filter_fingerprint"`
	Cursor            string                             `json:"cursor"`
	Targets           []gormdb.CollectionSelectionTarget `json:"targets"`
	NextCursor        string                             `json:"next_cursor,omitempty"`
	Total             *int64                             `json:"total,omitempty"`
}
