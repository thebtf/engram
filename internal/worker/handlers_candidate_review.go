package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

const queueCandidateSelectionDomain = "queue"

type queueCandidateSelectionStore interface {
	Current(context.Context, gormdb.CollectionSelectionScope) (gormdb.CollectionSelection, error)
	Frozen(context.Context, gormdb.CollectionSelectionScope, string) (gormdb.CollectionSelection, error)
}

type QueueCandidateSelectionHandler struct {
	service    *Service
	selections queueCandidateSelectionStore
	resolver   operatorCollectionScopeResolver
}

func NewQueueCandidateSelectionHandler(service *Service, selections queueCandidateSelectionStore, resolver operatorCollectionScopeResolver) *QueueCandidateSelectionHandler {
	return &QueueCandidateSelectionHandler{service: service, selections: selections, resolver: resolver}
}

const queueCandidateCollectionCursorVersion = 1

// queueCandidateCollectionProvider exposes only pending candidates through the
// shared selection boundary; candidate actions retain their own rechecks.
type queueCandidateCollectionProvider struct {
	candidates candidateReviewStore
}

type queueCandidateCollectionFilter struct {
	project string
}

type queueCandidateCollectionCursor struct {
	Fingerprint string `json:"f"`
	Revision    string `json:"r"`
	Scope       string `json:"s"`
	Offset      int    `json:"o"`
	Limit       int    `json:"l"`
	Version     int    `json:"v"`
}

func newQueueCandidateCollectionProvider(candidates candidateReviewStore) *queueCandidateCollectionProvider {
	return &queueCandidateCollectionProvider{candidates: candidates}
}

func (provider *queueCandidateCollectionProvider) NormalizeCollectionFilter(_ context.Context, scope gormdb.CollectionSelectionScope, rawScope string) (gormdb.CollectionFilter, error) {
	if scope.Domain != queueCandidateSelectionDomain {
		return gormdb.CollectionFilter{}, gormdb.ErrCollectionSelectionDenied
	}
	filter, err := queueCandidateCollectionFilterForScope(rawScope)
	if err != nil {
		return gormdb.CollectionFilter{}, err
	}
	return filter.collectionFilter(), nil
}

func (provider *queueCandidateCollectionProvider) FreezeCollectionSelection(ctx context.Context, scope gormdb.CollectionSelectionScope, filter gormdb.CollectionFilter) (gormdb.CollectionFrozenSelection, error) {
	if scope.Domain != queueCandidateSelectionDomain {
		return gormdb.CollectionFrozenSelection{}, gormdb.ErrCollectionSelectionDenied
	}
	resolved, err := queueCandidateCollectionFilterFrom(filter)
	if err != nil {
		return gormdb.CollectionFrozenSelection{}, err
	}
	rows, err := provider.rows(ctx, resolved)
	if err != nil {
		return gormdb.CollectionFrozenSelection{}, err
	}
	if len(rows) == 0 {
		return gormdb.CollectionFrozenSelection{}, gormdb.ErrCollectionSelectionDenied
	}
	return gormdb.CollectionFrozenSelection{
		Targets:   queueCandidateCollectionTargets(rows),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}, nil
}

func (provider *queueCandidateCollectionProvider) FreezeCollectionPageSelection(ctx context.Context, scope gormdb.CollectionSelectionScope, cursor string) ([]gormdb.CollectionSelectionTarget, error) {
	if scope.Domain != queueCandidateSelectionDomain {
		return nil, gormdb.ErrCollectionSelectionDenied
	}
	state, filter, err := decodeQueueCandidateCollectionCursor(cursor)
	if err != nil {
		return nil, err
	}
	rows, err := provider.rows(ctx, filter)
	if err != nil {
		return nil, err
	}
	if state.Revision != queueCandidateCollectionRevision(filter.collectionFilter(), rows) {
		return nil, gormdb.ErrCollectionSelectionReconfirmationRequired
	}
	if state.Offset < 0 || state.Offset >= len(rows) {
		return nil, gormdb.ErrCollectionSelectionInvalid
	}
	return queueCandidateCollectionTargets(rows[state.Offset:min(state.Offset+state.Limit, len(rows))]), nil
}

func (provider *queueCandidateCollectionProvider) PageCollection(ctx context.Context, scope gormdb.CollectionSelectionScope, request gormdb.CollectionPageRequest) (gormdb.CollectionPage, error) {
	if scope.Domain != queueCandidateSelectionDomain {
		return gormdb.CollectionPage{}, gormdb.ErrCollectionSelectionDenied
	}
	if !request.Valid() || request.Domain != queueCandidateSelectionDomain {
		return gormdb.CollectionPage{}, gormdb.ErrCollectionSelectionInvalid
	}
	filter, err := queueCandidateCollectionFilterFrom(request.Filter)
	if err != nil {
		return gormdb.CollectionPage{}, err
	}
	rows, err := provider.rows(ctx, filter)
	if err != nil {
		return gormdb.CollectionPage{}, err
	}
	revision := queueCandidateCollectionRevision(request.Filter, rows)
	offset := 0
	cursor := request.Cursor
	if cursor != "" {
		state, cursorFilter, cursorErr := decodeQueueCandidateCollectionCursor(cursor)
		if cursorErr != nil || cursorFilter.project != filter.project || state.Fingerprint != request.Filter.Fingerprint || state.Limit != request.Limit {
			return gormdb.CollectionPage{}, gormdb.ErrCollectionSelectionInvalid
		}
		if state.Revision != revision {
			return gormdb.CollectionPage{}, gormdb.ErrCollectionSelectionReconfirmationRequired
		}
		if state.Offset < 0 || (len(rows) > 0 && state.Offset >= len(rows)) {
			return gormdb.CollectionPage{}, gormdb.ErrCollectionSelectionInvalid
		}
		offset = state.Offset
	} else {
		cursor, err = encodeQueueCandidateCollectionCursor(request.Filter, revision, offset, request.Limit)
		if err != nil {
			return gormdb.CollectionPage{}, err
		}
	}
	end := min(offset+request.Limit, len(rows))
	var nextCursor string
	if end < len(rows) {
		nextCursor, err = encodeQueueCandidateCollectionCursor(request.Filter, revision, end, request.Limit)
		if err != nil {
			return gormdb.CollectionPage{}, err
		}
	}
	total := int64(len(rows))
	return gormdb.CollectionPage{
		Cursor:     cursor,
		Targets:    queueCandidateCollectionTargets(rows[offset:end]),
		NextCursor: nextCursor,
		Total:      &total,
	}, nil
}

func queueCandidateCollectionFilterForScope(rawScope string) (queueCandidateCollectionFilter, error) {
	if strings.TrimSpace(rawScope) != rawScope {
		return queueCandidateCollectionFilter{}, gormdb.ErrCollectionSelectionInvalid
	}
	if rawScope == "" || rawScope == "*" || strings.EqualFold(rawScope, candidateQueueAllProjects) {
		return queueCandidateCollectionFilter{project: candidateQueueAllProjects}, nil
	}
	if !operatorCodeText(rawScope) {
		return queueCandidateCollectionFilter{}, gormdb.ErrCollectionSelectionInvalid
	}
	return queueCandidateCollectionFilter{project: rawScope}, nil
}

func (filter queueCandidateCollectionFilter) collectionFilter() gormdb.CollectionFilter {
	digest := sha256.Sum256([]byte("queue-candidate-collection-filter/v1\x00status:pending\x00created_at:desc\x00id:desc\x00project:" + filter.project))
	return gormdb.CollectionFilter{Fingerprint: "sha256:" + hex.EncodeToString(digest[:]), Value: filter.project}
}

func queueCandidateCollectionFilterFrom(filter gormdb.CollectionFilter) (queueCandidateCollectionFilter, error) {
	if !filter.Valid() {
		return queueCandidateCollectionFilter{}, gormdb.ErrCollectionSelectionInvalid
	}
	resolved, err := queueCandidateCollectionFilterForScope(filter.Value)
	if err != nil || resolved.collectionFilter().Fingerprint != filter.Fingerprint {
		return queueCandidateCollectionFilter{}, gormdb.ErrCollectionSelectionInvalid
	}
	return resolved, nil
}

func (provider *queueCandidateCollectionProvider) rows(ctx context.Context, filter queueCandidateCollectionFilter) ([]*models.CrystallizationCandidate, error) {
	if provider == nil || provider.candidates == nil {
		return nil, errors.New("queue candidate collection provider is not configured")
	}
	project := filter.project
	if project == candidateQueueAllProjects {
		project = ""
	}
	rows, err := provider.candidates.ListByStatus(ctx, project, models.CandidateStatusPending, gormdb.CollectionSelectionMaxTargets+1)
	if err != nil {
		return nil, err
	}
	if len(rows) > gormdb.CollectionSelectionMaxTargets {
		return nil, gormdb.ErrCollectionSelectionInvalid
	}
	for _, row := range rows {
		if row == nil || row.ID < 1 || row.Status != models.CandidateStatusPending {
			return nil, gormdb.ErrCollectionSelectionInvalid
		}
	}
	return rows, nil
}

func queueCandidateCollectionRevision(filter gormdb.CollectionFilter, rows []*models.CrystallizationCandidate) string {
	hash := sha256.New()
	hash.Write([]byte("queue-candidate-collection-revision/v1\x00" + filter.Fingerprint + "\x00"))
	for _, row := range rows {
		var value [32]byte
		hash.Write(strconv.AppendInt(value[:0], row.ID, 10))
		hash.Write([]byte{0})
		hash.Write(strconv.AppendInt(value[:0], row.UpdatedAt.UnixNano(), 10))
		hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func queueCandidateCollectionTargets(rows []*models.CrystallizationCandidate) []gormdb.CollectionSelectionTarget {
	targets := make([]gormdb.CollectionSelectionTarget, len(rows))
	for index, row := range rows {
		version := uint64(row.UpdatedAt.UnixNano())
		if version == 0 {
			version = 1
		}
		targets[index] = gormdb.CollectionSelectionTarget{ID: strconv.FormatInt(row.ID, 10), ExpectedVersion: version}
	}
	return targets
}

func encodeQueueCandidateCollectionCursor(filter gormdb.CollectionFilter, revision string, offset, limit int) (string, error) {
	if !filter.Valid() || !(gormdb.CollectionFilter{Fingerprint: revision, Value: candidateQueueAllProjects}).Valid() || offset < 0 || limit < 1 || limit > gormdb.CollectionPageMaxSize {
		return "", gormdb.ErrCollectionSelectionInvalid
	}
	payload, err := json.Marshal(queueCandidateCollectionCursor{Fingerprint: filter.Fingerprint, Revision: revision, Scope: filter.Value, Offset: offset, Limit: limit, Version: queueCandidateCollectionCursorVersion})
	if err != nil {
		return "", err
	}
	cursor := base64.RawURLEncoding.EncodeToString(payload)
	if cursor == "" || len(cursor) > 512 {
		return "", gormdb.ErrCollectionSelectionInvalid
	}
	return cursor, nil
}

func decodeQueueCandidateCollectionCursor(cursor string) (queueCandidateCollectionCursor, queueCandidateCollectionFilter, error) {
	if cursor == "" || len(cursor) > 512 {
		return queueCandidateCollectionCursor{}, queueCandidateCollectionFilter{}, gormdb.ErrCollectionSelectionInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return queueCandidateCollectionCursor{}, queueCandidateCollectionFilter{}, gormdb.ErrCollectionSelectionInvalid
	}
	var state queueCandidateCollectionCursor
	if json.Unmarshal(payload, &state) != nil || state.Version != queueCandidateCollectionCursorVersion || state.Offset < 0 || state.Limit < 1 || state.Limit > gormdb.CollectionPageMaxSize || !(gormdb.CollectionFilter{Fingerprint: state.Revision, Value: candidateQueueAllProjects}).Valid() {
		return queueCandidateCollectionCursor{}, queueCandidateCollectionFilter{}, gormdb.ErrCollectionSelectionInvalid
	}
	filter, filterErr := queueCandidateCollectionFilterForScope(state.Scope)
	if filterErr != nil || filter.collectionFilter().Fingerprint != state.Fingerprint {
		return queueCandidateCollectionCursor{}, queueCandidateCollectionFilter{}, gormdb.ErrCollectionSelectionInvalid
	}
	return state, filter, nil
}

type queueCandidateSelectionAction string

const (
	queueCandidateSelectionPromote   queueCandidateSelectionAction = "promote"
	queueCandidateSelectionReject    queueCandidateSelectionAction = "reject"
	queueCandidateSelectionSupersede queueCandidateSelectionAction = "supersede"
)

type queueCandidateSelectionOperationRequest struct {
	RequestID string                           `json:"request_id"`
	Action    queueCandidateSelectionAction    `json:"action"`
	Selection queueCandidateOperationSelection `json:"selection"`
	Reason    string                           `json:"reason,omitempty"`
}

type queueCandidateOperationSelection struct {
	Kind    gormdb.CollectionSelectionKind `json:"kind"`
	Version int64                          `json:"selection_version"`
	Token   string                         `json:"selection_token,omitempty"`
}

type queueCandidateSelectionOperationResponse struct {
	RequestID      string                                       `json:"request_id"`
	OperationState string                                       `json:"operation_state"`
	ItemResults    []queueCandidateSelectionOperationItemResult `json:"item_results,omitempty"`
	Readback       *queueCandidateSelectionOperationReadback    `json:"readback,omitempty"`
}

type queueCandidateSelectionOperationItemResult struct {
	TargetID        int64  `json:"target_id"`
	Outcome         string `json:"outcome"`
	CandidateStatus string `json:"candidate_status,omitempty"`
}

type queueCandidateSelectionOperationReadback struct {
	Authoritative bool   `json:"authoritative"`
	Kind          string `json:"kind"`
	CurrentState  any    `json:"current_state"`
}

type queueCandidateActionStatus struct {
	CandidateID     int64  `json:"candidate_id"`
	CandidateStatus string `json:"candidate_status"`
}

type queueCandidateActionRecorder struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (recorder *queueCandidateActionRecorder) Header() http.Header {
	return recorder.header
}

func (recorder *queueCandidateActionRecorder) WriteHeader(status int) {
	if recorder.status == 0 {
		recorder.status = status
	}
}

func (recorder *queueCandidateActionRecorder) Write(body []byte) (int, error) {
	if recorder.status == 0 {
		recorder.status = http.StatusOK
	}
	return recorder.body.Write(body)
}

func (recorder *queueCandidateActionRecorder) Status() int {
	if recorder.status == 0 {
		return http.StatusOK
	}
	return recorder.status
}

func decodeQueueCandidateSelectionOperationRequest(r *http.Request) (queueCandidateSelectionOperationRequest, error) {
	var request queueCandidateSelectionOperationRequest
	if r == nil || r.Body == nil {
		return request, errors.New("queue candidate operation request is required")
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return request, errors.New("queue candidate operation has multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return request, err
	}
	request.Reason = strings.TrimSpace(request.Reason)
	return request, nil
}

func (request queueCandidateSelectionOperationRequest) valid() bool {
	if !operatorCodeText(request.RequestID) || request.Selection.Version < 1 {
		return false
	}
	switch request.Action {
	case queueCandidateSelectionPromote, queueCandidateSelectionReject, queueCandidateSelectionSupersede:
	default:
		return false
	}
	if request.Action != queueCandidateSelectionReject && request.Reason != "" {
		return false
	}
	switch request.Selection.Kind {
	case gormdb.CollectionSelectionExplicit, gormdb.CollectionSelectionPage:
		return request.Selection.Token == ""
	case gormdb.CollectionSelectionFrozenFilter:
		return request.Selection.Token != ""
	default:
		return false
	}
}

const queueCandidateSelectionInvalidMessage = "invalid queue candidate selection"

func (handler *QueueCandidateSelectionHandler) Handle(w http.ResponseWriter, r *http.Request) {
	request, err := decodeQueueCandidateSelectionOperationRequest(r)
	if err != nil || !request.valid() || r.Header.Get("X-Engram-Request-ID") != request.RequestID {
		http.Error(w, "invalid queue candidate operation", http.StatusBadRequest)
		return
	}
	selection, err := handler.resolveSelection(r.Context(), r, request.Selection)
	if err != nil {
		writeQueueCandidateSelectionError(w, err)
		return
	}
	if handler.service == nil {
		http.Error(w, "queue candidate operation unavailable", http.StatusServiceUnavailable)
		return
	}

	items := make([]queueCandidateSelectionOperationItemResult, 0, len(selection.Targets))
	for _, target := range selection.Targets {
		id, err := strconv.ParseInt(target.ID, 10, 64)
		if err != nil || id < 1 {
			http.Error(w, queueCandidateSelectionInvalidMessage, http.StatusBadRequest)
			return
		}
		items = append(items, handler.executeAction(r, id, request.Action, request.Reason))
	}
	writeQueueCandidateSelectionResult(w, request.RequestID, items)
}

func (handler *QueueCandidateSelectionHandler) resolveSelection(ctx context.Context, r *http.Request, requested queueCandidateOperationSelection) (gormdb.CollectionSelection, error) {
	if handler == nil || handler.selections == nil || handler.resolver == nil || r == nil {
		return gormdb.CollectionSelection{}, errors.New("queue candidate selection is unavailable")
	}
	identity, found := auth.IdentityFrom(r.Context())
	if !found {
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionDenied
	}
	subject, found := identity.SessionBrowserSubject()
	if !found {
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionDenied
	}
	sessionID, found := operatorCodeSessionID(r)
	if !found {
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionDenied
	}
	scope, err := handler.resolver.ResolveOperatorCollectionScope(ctx, identity, sessionID, queueCandidateSelectionDomain)
	if err != nil || scope.SubjectUserID != subject.UserID || scope.SessionID != sessionID || scope.Domain != queueCandidateSelectionDomain {
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionDenied
	}

	var selection gormdb.CollectionSelection
	switch requested.Kind {
	case gormdb.CollectionSelectionExplicit, gormdb.CollectionSelectionPage:
		selection, err = handler.selections.Current(ctx, scope)
	case gormdb.CollectionSelectionFrozenFilter:
		selection, err = handler.selections.Frozen(ctx, scope, requested.Token)
	default:
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
	}
	if err != nil {
		return gormdb.CollectionSelection{}, err
	}
	if selection.ReconfirmationRequired || selection.Kind != requested.Kind || selection.Version != requested.Version {
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionReconfirmationRequired
	}
	if selection.Kind == gormdb.CollectionSelectionFrozenFilter && selection.Token != requested.Token {
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionDenied
	}
	if len(selection.Targets) == 0 {
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
	}
	return selection, nil
}

func (handler *QueueCandidateSelectionHandler) executeAction(r *http.Request, id int64, action queueCandidateSelectionAction, reason string) queueCandidateSelectionOperationItemResult {
	recorder := &queueCandidateActionRecorder{header: make(http.Header)}
	request := queueCandidateActionRequest(r, id, action, reason)
	switch action {
	case queueCandidateSelectionPromote:
		handler.service.handlePromoteMemoryCandidate(recorder, request)
	case queueCandidateSelectionReject:
		handler.service.handleRejectMemoryCandidate(recorder, request)
	case queueCandidateSelectionSupersede:
		handler.service.handleSupersedeMemoryCandidate(recorder, request)
	}

	result := queueCandidateSelectionOperationItemResult{TargetID: id}
	switch recorder.Status() {
	case http.StatusOK:
		var receipt candidateActionReceipt
		if json.Unmarshal(recorder.body.Bytes(), &receipt) != nil || receipt.CandidateID != id || receipt.Action != string(action) || receipt.CandidateStatus != queueCandidateStatusForAction(action) {
			result.Outcome = "outcome_unknown"
			return result
		}
		result.Outcome = "committed"
		result.CandidateStatus = receipt.CandidateStatus
	case http.StatusForbidden:
		result.Outcome = "denied"
	case http.StatusNotFound, http.StatusConflict:
		result.Outcome = "conflict"
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		result.Outcome = "validation_error"
	case statusClientClosedRequest, http.StatusGatewayTimeout:
		result.Outcome = "outcome_unknown"
	default:
		result.Outcome = "outcome_unknown"
	}
	return result
}

func queueCandidateActionRequest(r *http.Request, id int64, action queueCandidateSelectionAction, reason string) *http.Request {
	body := []byte(nil)
	if action == queueCandidateSelectionReject {
		body, _ = json.Marshal(rejectCandidateRequest{Reason: reason})
	}
	request := r.Clone(r.Context())
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	request.Header = r.Header.Clone()
	if len(body) != 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	route := chi.NewRouteContext()
	route.URLParams.Add("id", strconv.FormatInt(id, 10))
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
}

func queueCandidateStatusForAction(action queueCandidateSelectionAction) string {
	switch action {
	case queueCandidateSelectionPromote:
		return "promoted"
	case queueCandidateSelectionReject:
		return "rejected"
	case queueCandidateSelectionSupersede:
		return "superseded"
	default:
		return ""
	}
}

func writeQueueCandidateSelectionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gormdb.ErrCollectionSelectionDenied):
		http.Error(w, "queue candidate operation forbidden", http.StatusForbidden)
	case errors.Is(err, gormdb.ErrCollectionSelectionReconfirmationRequired):
		http.Error(w, "queue candidate selection is stale", http.StatusPreconditionFailed)
	case errors.Is(err, gormdb.ErrCollectionSelectionInvalid):
		http.Error(w, queueCandidateSelectionInvalidMessage, http.StatusBadRequest)
	default:
		http.Error(w, "queue candidate selection unavailable", http.StatusServiceUnavailable)
	}
}

func writeQueueCandidateSelectionResult(w http.ResponseWriter, requestID string, items []queueCandidateSelectionOperationItemResult) {
	if len(items) == 0 {
		http.Error(w, queueCandidateSelectionInvalidMessage, http.StatusBadRequest)
		return
	}
	committed := 0
	firstOutcome := items[0].Outcome
	mixed := false
	for _, item := range items {
		if item.Outcome == "committed" {
			committed++
		}
		if item.Outcome != firstOutcome {
			mixed = true
		}
	}
	if committed == len(items) {
		states := make([]queueCandidateActionStatus, 0, len(items))
		for _, item := range items {
			states = append(states, queueCandidateActionStatus{CandidateID: item.TargetID, CandidateStatus: item.CandidateStatus})
		}
		var currentState any = states
		if len(states) == 1 {
			currentState = states[0]
		}
		writeJSON(w, queueCandidateSelectionOperationResponse{
			RequestID:      requestID,
			OperationState: "completed",
			ItemResults:    items,
			Readback:       &queueCandidateSelectionOperationReadback{Authoritative: true, Kind: "current", CurrentState: currentState},
		})
		return
	}
	if mixed {
		w.WriteHeader(http.StatusMultiStatus)
		writeJSON(w, queueCandidateSelectionOperationResponse{RequestID: requestID, OperationState: "partial", ItemResults: items})
		return
	}

	switch firstOutcome {
	case "denied":
		http.Error(w, "queue candidate operation forbidden", http.StatusForbidden)
	case "conflict":
		http.Error(w, "queue candidate target conflicts with current state", http.StatusConflict)
	case "validation_error":
		http.Error(w, "invalid queue candidate operation", http.StatusBadRequest)
	default:
		http.Error(w, "queue candidate operation outcome is unknown", http.StatusInternalServerError)
	}
}
