package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
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
			http.Error(w, "invalid queue candidate selection", http.StatusBadRequest)
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
		http.Error(w, "invalid queue candidate selection", http.StatusBadRequest)
	default:
		http.Error(w, "queue candidate selection unavailable", http.StatusServiceUnavailable)
	}
}

func writeQueueCandidateSelectionResult(w http.ResponseWriter, requestID string, items []queueCandidateSelectionOperationItemResult) {
	if len(items) == 0 {
		http.Error(w, "invalid queue candidate selection", http.StatusBadRequest)
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
