package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
)

const (
	operatorCodeRequestMaxBytes = 32 << 10
	operatorCodeSearchDefault   = 10
	operatorCodeSearchMax       = 50
	operatorCodeReadDefault     = 8_192
	operatorCodeReadMax         = 8_192
	operatorCodeGraphMaxDepth   = 8
	operatorCodeGraphMaxVisited = 5_000
	operatorCodeGraphMaxNodes   = 200
	operatorCodeGraphMaxEdges   = 400
	operatorCodeGraphMaxWaitMS  = int64(30_000)
)

// OperatorCodeHTTPAdapter is the browser-only HTTP boundary for the UCI code
// reader. Route composition deliberately stays elsewhere: every request reaches
// this adapter only after the normal authentication middleware has installed a
// real browser-session identity.
type OperatorCodeHTTPAdapter struct {
	grants    operatorCodeGrantReader
	bindings  operatorCodeBindingApplication
	authority operatorCodeContextAuthorizer
	app       operatorCodeApplication
	recorder  operatorCodeExposureRecorder
	now       func() time.Time
}

// operatorCodeGrantReader retains the already-typed T012 grant checks. The
// adapter never queries grant persistence or accepts a grant reference itself.
type operatorCodeGrantReader interface {
	CanRead(context.Context, auth.Identity, string, string) (bool, error)
	Current(context.Context, auth.Identity) (gormdb.BrowserReadGrant, bool, error)
}

// operatorCodeBindingApplication retains the T014 lifecycle and document-proof
// state machine. A binding is never Code-read authority.
type operatorCodeBindingApplication interface {
	Guard(context.Context, auth.Identity, string, BrowserBindingProof) (BrowserBindingGuarded, error)
	Handshake(context.Context, auth.Identity, string, BrowserBindingHandshakeInput) (BrowserBindingTransition, error)
	Resume(context.Context, auth.Identity, string, BrowserBindingResumeInput) (BrowserBindingTransition, error)
	Renew(context.Context, auth.Identity, string, BrowserBindingProof) error
	Close(context.Context, auth.Identity, string, BrowserBindingProof) error
	Pin(context.Context, auth.Identity, string, BrowserBindingProof, BrowserBindingContext) error
}

// operatorCodeContextAuthorizer is the composition-owned bridge from a
// server-resolved grant/pin to an AuthorizedContext. Its implementation derives
// the source realm and checkout owner server-side; browser JSON never supplies
// a principal, workspace, project, Space, label, path, or ContextRef selector.
type operatorCodeContextAuthorizer interface {
	AuthorizeOperatorCode(context.Context, operatorCodeVerifiedCaller) (uci.AuthorizedContext, error)
	ResolveCurrentOperatorCode(context.Context, operatorCodeBindingCaller, gormdb.BrowserReadGrant) (uci.AuthorizedContext, error)
}

// operatorCodeApplication is the existing UCI application surface after a
// browser request has been authorized. It is intentionally narrower than route
// composition and contains no grant, tab-proof, or release responsibility.
type operatorCodeApplication interface {
	SearchCodebase(context.Context, uci.AuthorizedContext, mcp.CodebaseSearchInput) (uci.QueryResponse, error)
	ExploreCodebase(context.Context, uci.AuthorizedContext, mcp.CodebaseGraphInput) (uci.QueryResponse, error)
	ReadCodebase(context.Context, uci.AuthorizedContext, mcp.CodebaseReadInput) (uci.QueryResponse, error)
	CodebaseStatus(context.Context, uci.AuthorizedContext) (mcp.CodebaseStatusSnapshot, error)
	Project(context.Context, uci.ContextRef) (map[string]string, error)
}

// operatorCodeExposureRecorder is the existing T016 recorder boundary. A
// failed append must never permit contextual serialization.
type operatorCodeExposureRecorder interface {
	Record(context.Context, uci.AuthorizedContext, uci.ExposureInput) (uci.QueryExposure, error)
}

// operatorCodeBindingCaller is trusted request state for an unpinned document.
type operatorCodeBindingCaller struct {
	Subject   auth.BrowserSubject
	SessionID string
	BindingID string
}

// operatorCodeVerifiedCaller is private trusted state passed only after the
// grant and current-document proof have both been rechecked.
type operatorCodeVerifiedCaller struct {
	Subject   auth.BrowserSubject
	SessionID string
	BindingID string
	Context   uci.ContextRef
}

// NewOperatorCodeHTTPAdapter creates the route-independent HTTP adapter. A
// missing dependency stays closed at request time so an incomplete composition
// can never publish a contextual body.
func NewOperatorCodeHTTPAdapter(
	grants operatorCodeGrantReader,
	bindings operatorCodeBindingApplication,
	authority operatorCodeContextAuthorizer,
	app operatorCodeApplication,
	recorder operatorCodeExposureRecorder,
) *OperatorCodeHTTPAdapter {
	return &OperatorCodeHTTPAdapter{
		grants:    grants,
		bindings:  bindings,
		authority: authority,
		app:       app,
		recorder:  recorder,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

// HandleHandshake creates a new document binding. It returns only material for
// that document; a binding begins unpinned and unselected.
func (adapter *OperatorCodeHTTPAdapter) HandleHandshake(w http.ResponseWriter, r *http.Request) {
	var request operatorCodeHandshakeRequest
	identity, ok := adapter.decode(w, r, "operator-code-tab-handshake", &request)
	if !ok {
		return
	}
	if !request.valid() {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return
	}
	if adapter.bindings == nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	transition, err := adapter.bindings.Handshake(r.Context(), identity.identity, identity.sessionID, request.input())
	if err != nil {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return
	}
	operatorCodeWriteTransition(w, transition)
}

// HandleResume delegates the no-opener reload transition to T014. Pending
// reloads intentionally expose no document material.
func (adapter *OperatorCodeHTTPAdapter) HandleResume(w http.ResponseWriter, r *http.Request) {
	var request operatorCodeResumeRequest
	identity, ok := adapter.decode(w, r, "operator-code-tab-resume", &request)
	if !ok {
		return
	}
	if !request.valid() {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return
	}
	if adapter.bindings == nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	transition, err := adapter.bindings.Resume(r.Context(), identity.identity, identity.sessionID, request.input())
	if err != nil {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return
	}
	operatorCodeWriteTransition(w, transition)
}

// HandleRenew extends only the current path-bound document lease.
func (adapter *OperatorCodeHTTPAdapter) HandleRenew(w http.ResponseWriter, r *http.Request) {
	var request operatorCodePathProofRequest
	identity, ok := adapter.decode(w, r, "operator-code-tab-lease", &request)
	if !ok {
		return
	}
	proof, ok := operatorCodePathProof(r, request)
	if !ok {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return
	}
	if adapter.bindings == nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	if err := adapter.bindings.Renew(r.Context(), identity.identity, identity.sessionID, proof); err != nil {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return
	}
	operatorCodeWriteBodyless(w, http.StatusNoContent)
}

// HandleClose closes only the current path-bound document lease.
func (adapter *OperatorCodeHTTPAdapter) HandleClose(w http.ResponseWriter, r *http.Request) {
	var request operatorCodePathProofRequest
	identity, ok := adapter.decode(w, r, "operator-code-tab-close", &request)
	if !ok {
		return
	}
	proof, ok := operatorCodePathProof(r, request)
	if !ok {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return
	}
	if adapter.bindings == nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	if err := adapter.bindings.Close(r.Context(), identity.identity, identity.sessionID, proof); err != nil {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return
	}
	operatorCodeWriteBodyless(w, http.StatusNoContent)
}

// HandlePin resolves the one active grant and current UCI view on the server;
// the browser supplies only the current document proof.
func (adapter *OperatorCodeHTTPAdapter) HandlePin(w http.ResponseWriter, r *http.Request) {
	var request operatorCodePathProofRequest
	identity, ok := adapter.decode(w, r, "operator-code-tab-context", &request)
	if !ok {
		return
	}
	proof, ok := operatorCodePathProof(r, request)
	if !ok {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return
	}
	caller, failure := adapter.authorizeCurrent(r.Context(), identity, proof)
	if failure != uci.ReleaseFailureNone {
		operatorCodeWriteFailure(w, failure)
		return
	}
	if err := adapter.bindings.Pin(r.Context(), identity.identity, identity.sessionID, proof, browserBindingContext(caller.authorized.Ref())); err != nil {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return
	}
	operatorCodeWriteBodyless(w, http.StatusNoContent)
}

// HandleStatus serves the non-content index status. T016 Release still
// reauthorizes this category, but status does not append an exposure receipt.
func (adapter *OperatorCodeHTTPAdapter) HandleStatus(w http.ResponseWriter, r *http.Request) {
	var request operatorCodeProofRequest
	identity, ok := adapter.decode(w, r, "operator-code-status", &request)
	if !ok {
		return
	}
	caller, failure := adapter.authorize(r.Context(), identity, request.Proof())
	if failure != uci.ReleaseFailureNone {
		operatorCodeWriteFailure(w, failure)
		return
	}
	if adapter.app == nil {
		operatorCodeWriteFailure(w, uci.ReleaseFailureExposureUnavailable)
		return
	}

	snapshot, err := adapter.app.CodebaseStatus(r.Context(), caller.authorized)
	if err != nil || !operatorCodeStatusValid(snapshot) {
		operatorCodeWriteFailure(w, uci.ReleaseFailureExposureUnavailable)
		return
	}
	decision := adapter.releaseNonContent(r.Context(), identity, caller, uci.ReleaseCategoryCodeStatus)
	if !decision.Released() {
		operatorCodeWriteFailure(w, decision.Failure)
		return
	}
	writeJSON(w, operatorCodeStatusResponse{
		TotalChunks:    snapshot.TotalChunks,
		EmbeddedChunks: snapshot.EmbeddedChunks,
		Embedding:      snapshot.Embedding,
		Freshness:      snapshot.Freshness,
	})
}

// HandleSearch reads bounded lexical code results from the one current grant.
// It deliberately accepts no project, path, label, Space, or context selector.
func (adapter *OperatorCodeHTTPAdapter) HandleSearch(w http.ResponseWriter, r *http.Request) {
	var request operatorCodeSearchRequest
	identity, ok := adapter.decode(w, r, "operator-code-search", &request)
	if !ok || !request.valid() {
		if ok {
			operatorCodeWriteBodyless(w, http.StatusBadRequest)
		}
		return
	}
	caller, failure := adapter.authorize(r.Context(), identity, request.Proof())
	if failure != uci.ReleaseFailureNone {
		operatorCodeWriteFailure(w, failure)
		return
	}
	if adapter.app == nil {
		operatorCodeWriteFailure(w, uci.ReleaseFailureExposureUnavailable)
		return
	}

	response, err := adapter.app.SearchCodebase(r.Context(), caller.authorized, mcp.CodebaseSearchInput{
		Query: request.Query,
		Limit: request.limit(),
	})
	if err != nil || !operatorCodeSearchResponseValid(response, caller.authorized) {
		operatorCodeWriteFailure(w, uci.ReleaseFailureExposureUnavailable)
		return
	}
	adapter.writeReleasedQuery(w, r.Context(), identity, caller, uci.ReleaseCategoryCodeSearch, response, func(candidate uci.QueryResponse, authorized uci.AuthorizedContext) bool {
		return operatorCodeSearchResponseValid(candidate, authorized)
	})
}

// HandleGraph explores bounded graph evidence inside the one current grant.
func (adapter *OperatorCodeHTTPAdapter) HandleGraph(w http.ResponseWriter, r *http.Request) {
	var request operatorCodeGraphRequest
	identity, ok := adapter.decode(w, r, "operator-code-graph", &request)
	if !ok {
		return
	}
	input, deadlineMS, valid := request.input(time.Now().UTC())
	if !valid {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return
	}
	operationCtx, cancel := context.WithTimeout(r.Context(), time.Duration(deadlineMS)*time.Millisecond)
	defer cancel()
	caller, failure := adapter.authorize(operationCtx, identity, request.Proof())
	if failure != uci.ReleaseFailureNone {
		operatorCodeWriteFailure(w, failure)
		return
	}
	if adapter.app == nil {
		operatorCodeWriteFailure(w, uci.ReleaseFailureExposureUnavailable)
		return
	}

	response, err := adapter.app.ExploreCodebase(operationCtx, caller.authorized, input)
	if err != nil || !operatorCodeGraphResponseValid(response, caller.authorized, input) {
		operatorCodeWriteFailure(w, uci.ReleaseFailureExposureUnavailable)
		return
	}
	adapter.writeReleasedQuery(w, operationCtx, identity, caller, uci.ReleaseCategoryCodeGraph, response, func(candidate uci.QueryResponse, authorized uci.AuthorizedContext) bool {
		return operatorCodeGraphResponseValid(candidate, authorized, input)
	})
}

// HandleVersionedRead reads one bounded persisted span. A client may name only
// the entity key from a result it already received; the source and View come
// exclusively from the guarded binding pin.
func (adapter *OperatorCodeHTTPAdapter) HandleVersionedRead(w http.ResponseWriter, r *http.Request) {
	var request operatorCodeVersionedReadRequest
	identity, ok := adapter.decode(w, r, "operator-code-read", &request)
	if !ok || !request.valid() {
		if ok {
			operatorCodeWriteBodyless(w, http.StatusBadRequest)
		}
		return
	}
	caller, failure := adapter.authorize(r.Context(), identity, request.Proof())
	if failure != uci.ReleaseFailureNone {
		operatorCodeWriteFailure(w, failure)
		return
	}
	if adapter.app == nil {
		operatorCodeWriteFailure(w, uci.ReleaseFailureExposureUnavailable)
		return
	}
	ref := caller.authorized.Ref()
	input := mcp.CodebaseReadInput{
		Ref: uci.QueryEntityRef{
			SourceID:  ref.SourceID,
			ViewID:    ref.ViewID,
			EntityKey: request.EntityKey,
		},
		Span:              request.Span,
		ContentDigest:     uci.QueryContentDigest(request.ContentDigest),
		VerifyWorkingCopy: request.VerifyWorkingCopy,
		MaxBytes:          request.maxBytes(),
	}
	response, err := adapter.app.ReadCodebase(r.Context(), caller.authorized, input)
	if err != nil || !operatorCodeReadResponseValid(response, caller.authorized, input) {
		operatorCodeWriteFailure(w, uci.ReleaseFailureExposureUnavailable)
		return
	}
	adapter.writeReleasedQuery(w, r.Context(), identity, caller, uci.ReleaseCategoryVersionedRead, response, func(candidate uci.QueryResponse, authorized uci.AuthorizedContext) bool {
		return operatorCodeReadResponseValid(candidate, authorized, input)
	})
}

// HandleContexts resolves only the subject's one current grant before pinning.
// It returns labels alone, never the grant, binding, proof, or ContextRef.
func (adapter *OperatorCodeHTTPAdapter) HandleContexts(w http.ResponseWriter, r *http.Request) {
	var request operatorCodeProofRequest
	identity, ok := adapter.decode(w, r, "operator-code-contexts", &request)
	if !ok {
		return
	}
	caller, failure := adapter.authorizeCurrent(r.Context(), identity, request.Proof())
	if failure != uci.ReleaseFailureNone {
		operatorCodeWriteFailure(w, failure)
		return
	}
	if adapter.app == nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	metadata, err := adapter.app.Project(r.Context(), caller.authorized.Ref())
	response, valid := operatorCodeSafeMetadata(metadata)
	if err != nil || !valid {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, operatorCodeContextsResponse{Context: response})
}

type operatorCodeProofRequest struct {
	TabBindingID  string `json:"tab_binding_id"`
	DocumentProof string `json:"document_proof"`
}

func (request operatorCodeProofRequest) Proof() BrowserBindingProof {
	return BrowserBindingProof{TabBindingID: request.TabBindingID, DocumentProof: request.DocumentProof}
}

type operatorCodePathProofRequest struct {
	DocumentProof string `json:"document_proof"`
}

type operatorCodeHandshakeRequest struct {
	DocumentNonce      string `json:"document_nonce"`
	CopiedTabBindingID string `json:"copied_tab_binding_id,omitempty"`
	CopiedResumeNonce  string `json:"copied_resume_nonce,omitempty"`
	Ambiguous          bool   `json:"ambiguous,omitempty"`
}

func (request operatorCodeHandshakeRequest) valid() bool {
	return operatorCodeText(request.DocumentNonce) &&
		(request.CopiedTabBindingID == "" || operatorCodeUUID(request.CopiedTabBindingID)) &&
		(request.CopiedResumeNonce == "" || operatorCodeText(request.CopiedResumeNonce))
}

func (request operatorCodeHandshakeRequest) input() BrowserBindingHandshakeInput {
	return BrowserBindingHandshakeInput{
		DocumentNonce:      request.DocumentNonce,
		CopiedTabBindingID: request.CopiedTabBindingID,
		CopiedResumeNonce:  request.CopiedResumeNonce,
		Ambiguous:          request.Ambiguous,
	}
}

type operatorCodeResumeRequest struct {
	TabBindingID  string `json:"tab_binding_id"`
	ResumeNonce   string `json:"resume_nonce"`
	ReloadToken   string `json:"reload_token"`
	DocumentNonce string `json:"document_nonce"`
}

func (request operatorCodeResumeRequest) valid() bool {
	return operatorCodeUUID(request.TabBindingID) && operatorCodeText(request.ResumeNonce) && operatorCodeText(request.ReloadToken) && operatorCodeText(request.DocumentNonce)
}

func (request operatorCodeResumeRequest) input() BrowserBindingResumeInput {
	return BrowserBindingResumeInput{
		TabBindingID:  request.TabBindingID,
		ResumeNonce:   request.ResumeNonce,
		ReloadToken:   request.ReloadToken,
		DocumentNonce: request.DocumentNonce,
	}
}

type operatorCodeSearchRequest struct {
	operatorCodeProofRequest
	Query string `json:"query"`
	Limit *int   `json:"limit"`
}

func (request operatorCodeSearchRequest) valid() bool {
	return request.Query != "" && (request.Limit == nil || (*request.Limit >= 1 && *request.Limit <= operatorCodeSearchMax))
}

func (request operatorCodeSearchRequest) limit() int {
	if request.Limit == nil {
		return operatorCodeSearchDefault
	}
	return *request.Limit
}

type operatorCodeGraphTargetRequest struct {
	EntityKey *string `json:"entity_key"`
	Name      *string `json:"name"`
}

type operatorCodeGraphRequest struct {
	operatorCodeProofRequest
	Action        string                          `json:"action"`
	Target        *operatorCodeGraphTargetRequest `json:"target"`
	Destination   *operatorCodeGraphTargetRequest `json:"destination"`
	Direction     *string                         `json:"direction"`
	Relations     []string                        `json:"relations"`
	EvidenceKinds []string                        `json:"evidence_kinds"`
	MaxDepth      *int                            `json:"max_depth"`
	MaxVisited    *int                            `json:"max_visited"`
	MaxNodes      *int                            `json:"max_nodes"`
	MaxEdges      *int                            `json:"max_edges"`
	DeadlineMS    *int64                          `json:"deadline_ms"`
	Continuation  *string                         `json:"continuation"`
}

func (request operatorCodeGraphRequest) input(now time.Time) (mcp.CodebaseGraphInput, int64, bool) {
	action := uci.GraphAction(strings.ToLower(strings.TrimSpace(request.Action)))
	switch action {
	case uci.GraphActionExplain, uci.GraphActionNeighbors, uci.GraphActionPath, uci.GraphActionImpact, uci.GraphActionFlow, uci.GraphActionCycles:
	default:
		return mcp.CodebaseGraphInput{}, 0, false
	}
	target, valid := operatorCodeGraphTarget(request.Target)
	if !valid {
		return mcp.CodebaseGraphInput{}, 0, false
	}
	var destination *uci.GraphTarget
	if request.Destination != nil {
		value, targetValid := operatorCodeGraphTarget(request.Destination)
		if !targetValid {
			return mcp.CodebaseGraphInput{}, 0, false
		}
		destination = &value
	}
	if (action == uci.GraphActionPath) != (destination != nil) {
		return mcp.CodebaseGraphInput{}, 0, false
	}
	direction := uci.GraphDirectionBoth
	if request.Direction != nil {
		direction = uci.GraphDirection(strings.ToLower(strings.TrimSpace(*request.Direction)))
	}
	if direction != uci.GraphDirectionIncoming && direction != uci.GraphDirectionOutgoing && direction != uci.GraphDirectionBoth {
		return mcp.CodebaseGraphInput{}, 0, false
	}
	relations, valid := operatorCodeGraphRelations(request.Relations)
	if !valid {
		return mcp.CodebaseGraphInput{}, 0, false
	}
	evidenceKinds, valid := operatorCodeGraphEvidenceKinds(request.EvidenceKinds)
	if !valid {
		return mcp.CodebaseGraphInput{}, 0, false
	}
	maxDepth, valid := operatorCodeGraphBound(request.MaxDepth, 4, 0, operatorCodeGraphMaxDepth)
	if !valid {
		return mcp.CodebaseGraphInput{}, 0, false
	}
	maxVisited, valid := operatorCodeGraphBound(request.MaxVisited, 64, 1, operatorCodeGraphMaxVisited)
	if !valid {
		return mcp.CodebaseGraphInput{}, 0, false
	}
	maxNodes, valid := operatorCodeGraphBound(request.MaxNodes, 32, 1, operatorCodeGraphMaxNodes)
	if !valid {
		return mcp.CodebaseGraphInput{}, 0, false
	}
	maxEdges, valid := operatorCodeGraphBound(request.MaxEdges, 64, 1, operatorCodeGraphMaxEdges)
	if !valid {
		return mcp.CodebaseGraphInput{}, 0, false
	}
	deadlineMS := int64(30_000)
	if request.DeadlineMS != nil {
		deadlineMS = *request.DeadlineMS
	}
	if deadlineMS < 1 || deadlineMS > operatorCodeGraphMaxWaitMS {
		return mcp.CodebaseGraphInput{}, 0, false
	}
	var continuation *string
	if request.Continuation != nil {
		if !operatorCodeText(*request.Continuation) || len(*request.Continuation) > 2_048 {
			return mcp.CodebaseGraphInput{}, 0, false
		}
		value := *request.Continuation
		continuation = &value
	}
	return mcp.CodebaseGraphInput{
		Action:      action,
		Target:      target,
		Destination: destination,
		Filter: uci.GraphFilter{
			Direction:     direction,
			Relations:     relations,
			EvidenceKinds: evidenceKinds,
		},
		Budget: uci.GraphBudget{
			MaxDepth:   maxDepth,
			MaxVisited: maxVisited,
			MaxNodes:   maxNodes,
			MaxEdges:   maxEdges,
			Deadline:   now.Add(time.Duration(deadlineMS) * time.Millisecond),
		},
		Continuation: continuation,
	}, deadlineMS, true
}

func operatorCodeGraphTarget(request *operatorCodeGraphTargetRequest) (uci.GraphTarget, bool) {
	if request == nil || (request.EntityKey == nil) == (request.Name == nil) {
		return uci.GraphTarget{}, false
	}
	if request.EntityKey != nil {
		value := strings.TrimSpace(*request.EntityKey)
		if !operatorCodeText(value) {
			return uci.GraphTarget{}, false
		}
		return uci.GraphTarget{EntityKey: value}, true
	}
	value := strings.TrimSpace(*request.Name)
	if !operatorCodeText(value) {
		return uci.GraphTarget{}, false
	}
	return uci.GraphTarget{Name: value}, true
}

func operatorCodeGraphRelations(values []string) ([]uci.IndexRelation, bool) {
	if len(values) > 32 {
		return nil, false
	}
	result := make([]uci.IndexRelation, 0, len(values))
	for _, value := range values {
		relation := uci.IndexRelation(strings.ToLower(strings.TrimSpace(value)))
		switch relation {
		case "contains", "imports", "exports", "references", "calls", "may_call", "inherits", "implements", "documents", "mentions", "configures", "schema_references", "tests", "depends_on":
			result = append(result, relation)
		default:
			return nil, false
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return operatorCodeUniqueRelations(result), true
}

func operatorCodeGraphEvidenceKinds(values []string) ([]uci.QueryEvidenceKind, bool) {
	if len(values) > 4 {
		return nil, false
	}
	result := make([]uci.QueryEvidenceKind, 0, len(values))
	for _, value := range values {
		kind := uci.QueryEvidenceKind(strings.ToUpper(strings.TrimSpace(value)))
		switch kind {
		case uci.QueryEvidenceExtracted, uci.QueryEvidenceResolved, uci.QueryEvidenceHeuristic, uci.QueryEvidenceSemantic:
			result = append(result, kind)
		default:
			return nil, false
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return operatorCodeUniqueEvidenceKinds(result), true
}

func operatorCodeGraphBound(value *int, fallback, minimum, maximum int) (int, bool) {
	if value == nil {
		return fallback, true
	}
	if *value < minimum || *value > maximum {
		return 0, false
	}
	return *value, true
}

func operatorCodeUniqueRelations(values []uci.IndexRelation) []uci.IndexRelation {
	if len(values) == 0 {
		return nil
	}
	unique := values[:0]
	for _, value := range values {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}

func operatorCodeUniqueEvidenceKinds(values []uci.QueryEvidenceKind) []uci.QueryEvidenceKind {
	if len(values) == 0 {
		return nil
	}
	unique := values[:0]
	for _, value := range values {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}

type operatorCodeVersionedReadRequest struct {
	operatorCodeProofRequest
	EntityKey         string        `json:"entity_key"`
	Span              uci.QuerySpan `json:"span"`
	ContentDigest     string        `json:"content_digest"`
	VerifyWorkingCopy bool          `json:"verify_working_copy"`
	MaxBytes          *int          `json:"max_bytes"`
}

func (request operatorCodeVersionedReadRequest) valid() bool {
	if !operatorCodeText(request.EntityKey) || !operatorCodeDigest(request.ContentDigest) || request.Span.Validate() != nil {
		return false
	}
	maxBytes := request.maxBytes()
	return maxBytes >= 1 && maxBytes <= operatorCodeReadMax && request.Span.ByteEnd-request.Span.ByteStart <= int64(maxBytes)
}

func (request operatorCodeVersionedReadRequest) maxBytes() int {
	if request.MaxBytes == nil {
		return operatorCodeReadDefault
	}
	return *request.MaxBytes
}

type operatorCodeRequestIdentity struct {
	requestID string
	digest    string
	identity  auth.Identity
	sessionID string
}

type operatorCodeAuthorizedRequest struct {
	operatorCodeRequestIdentity
	caller     operatorCodeVerifiedCaller
	proof      BrowserBindingProof
	authorized uci.AuthorizedContext
}

func (adapter *OperatorCodeHTTPAdapter) decode(w http.ResponseWriter, r *http.Request, endpoint string, target any) (operatorCodeRequestIdentity, bool) {
	if adapter == nil || r == nil || !operatorCodeText(endpoint) {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return operatorCodeRequestIdentity{}, false
	}
	identity, found := auth.IdentityFrom(r.Context())
	if !found {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return operatorCodeRequestIdentity{}, false
	}
	sessionID, found := operatorCodeSessionID(r)
	if !found {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return operatorCodeRequestIdentity{}, false
	}
	requestID := r.Header.Get("X-Engram-Request-ID")
	if !operatorCodeText(requestID) {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return operatorCodeRequestIdentity{}, false
	}
	raw, err := operatorCodeReadJSON(r, target)
	if err != nil {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return operatorCodeRequestIdentity{}, false
	}
	digest := sha256.Sum256(append(append([]byte(endpoint+"\n"), raw...), '\n'))
	return operatorCodeRequestIdentity{
		requestID: requestID,
		digest:    "sha256:" + hex.EncodeToString(digest[:]),
		identity:  identity,
		sessionID: sessionID,
	}, true
}

func operatorCodeReadJSON(r *http.Request, target any) ([]byte, error) {
	if r.Body == nil {
		return nil, errors.New("request body is required")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, operatorCodeRequestMaxBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > operatorCodeRequestMaxBytes || !utf8.Valid(raw) {
		return nil, errors.New("invalid request body")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("multiple JSON values")
	}
	return raw, nil
}

func operatorCodeSessionID(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(authSessionCookieName)
	if err != nil || !operatorCodeText(cookie.Value) {
		return "", false
	}
	return cookie.Value, true
}

func operatorCodeText(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func operatorCodeDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (adapter *OperatorCodeHTTPAdapter) authorize(ctx context.Context, identity operatorCodeRequestIdentity, proof BrowserBindingProof) (operatorCodeAuthorizedRequest, uci.ReleaseFailureCode) {
	if adapter == nil || adapter.grants == nil || adapter.bindings == nil || adapter.authority == nil {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailureExposureUnavailable
	}
	subject, ok := identity.identity.SessionBrowserSubject()
	if !ok {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailurePermissionDenied
	}
	guarded, err := adapter.bindings.Guard(ctx, identity.identity, identity.sessionID, proof)
	if err != nil || guarded.Pinned == nil || guarded.TabBindingID != proof.TabBindingID {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailurePermissionDenied
	}
	ref, ok := operatorCodeContextRef(*guarded.Pinned)
	if !ok {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailureContextMismatch
	}
	granted, err := adapter.grants.CanRead(ctx, identity.identity, ref.SourceID, ref.CheckoutID)
	if err != nil {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailureExposureUnavailable
	}
	if !granted {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailurePermissionDenied
	}
	caller := operatorCodeVerifiedCaller{
		Subject:   subject,
		SessionID: identity.sessionID,
		BindingID: guarded.TabBindingID,
		Context:   ref,
	}
	authorized, err := adapter.authority.AuthorizeOperatorCode(ctx, caller)
	if err != nil {
		return operatorCodeAuthorizedRequest{}, operatorCodeContextFailure(err)
	}
	if !operatorCodeRefsEqual(authorized.Ref(), ref) {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailureContextMismatch
	}
	return operatorCodeAuthorizedRequest{operatorCodeRequestIdentity: identity, caller: caller, proof: proof, authorized: authorized}, uci.ReleaseFailureNone
}

func (adapter *OperatorCodeHTTPAdapter) authorizeCurrent(ctx context.Context, identity operatorCodeRequestIdentity, proof BrowserBindingProof) (operatorCodeAuthorizedRequest, uci.ReleaseFailureCode) {
	if adapter == nil || adapter.grants == nil || adapter.bindings == nil || adapter.authority == nil {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailureExposureUnavailable
	}
	subject, ok := identity.identity.SessionBrowserSubject()
	if !ok {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailurePermissionDenied
	}
	guarded, err := adapter.bindings.Guard(ctx, identity.identity, identity.sessionID, proof)
	if err != nil || guarded.TabBindingID != proof.TabBindingID {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailurePermissionDenied
	}
	grant, found, err := adapter.grants.Current(ctx, identity.identity)
	if err != nil {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailureExposureUnavailable
	}
	if !found {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailurePermissionDenied
	}
	bound := operatorCodeBindingCaller{Subject: subject, SessionID: identity.sessionID, BindingID: guarded.TabBindingID}
	authorized, err := adapter.authority.ResolveCurrentOperatorCode(ctx, bound, grant)
	if err != nil {
		return operatorCodeAuthorizedRequest{}, operatorCodeContextFailure(err)
	}
	ref := authorized.Ref()
	if ref.SpaceID != nil || ref.SourceID != grant.SourceID || ref.CheckoutID != grant.CheckoutID {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailureContextMismatch
	}
	granted, err := adapter.grants.CanRead(ctx, identity.identity, ref.SourceID, ref.CheckoutID)
	if err != nil {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailureExposureUnavailable
	}
	if !granted {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailurePermissionDenied
	}
	caller := operatorCodeVerifiedCaller{Subject: subject, SessionID: identity.sessionID, BindingID: guarded.TabBindingID, Context: ref}
	return operatorCodeAuthorizedRequest{operatorCodeRequestIdentity: identity, caller: caller, proof: proof, authorized: authorized}, uci.ReleaseFailureNone
}

func operatorCodeContextRef(pinned BrowserBindingContext) (uci.ContextRef, bool) {
	if !operatorCodeUUID(pinned.SourceID) || !operatorCodeUUID(pinned.CheckoutID) || !operatorCodeUUID(pinned.ViewID) || !operatorCodeUUID(pinned.AnalysisProfileID) || pinned.Generation < 1 {
		return uci.ContextRef{}, false
	}
	return uci.ContextRef{
		SourceID:          pinned.SourceID,
		CheckoutID:        pinned.CheckoutID,
		ViewID:            pinned.ViewID,
		AnalysisProfileID: pinned.AnalysisProfileID,
		Generation:        pinned.Generation,
	}, true
}

func operatorCodeUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func operatorCodeRefsEqual(left, right uci.ContextRef) bool {
	if left.SpaceID != nil || right.SpaceID != nil {
		return false
	}
	return left.SourceID == right.SourceID &&
		left.CheckoutID == right.CheckoutID &&
		left.ViewID == right.ViewID &&
		left.AnalysisProfileID == right.AnalysisProfileID &&
		left.Generation == right.Generation
}

func operatorCodeContextFailure(err error) uci.ReleaseFailureCode {
	var contextErr *uci.ContextError
	if errors.As(err, &contextErr) {
		switch contextErr.Code() {
		case uci.ContextRequired:
			return uci.ReleaseFailureContextRequired
		case uci.PermissionDenied:
			return uci.ReleaseFailurePermissionDenied
		default:
			return uci.ReleaseFailureContextMismatch
		}
	}
	return uci.ReleaseFailureExposureUnavailable
}

func (adapter *OperatorCodeHTTPAdapter) releaseNonContent(ctx context.Context, identity operatorCodeRequestIdentity, caller operatorCodeAuthorizedRequest, category uci.ReleaseCategory) uci.ReleaseDecision {
	return uci.Release(ctx, adapter.releaseRequest(identity, caller, category, nil, nil), adapter.releaseGate(identity, caller.Proof()))
}

func (adapter *OperatorCodeHTTPAdapter) writeReleasedQuery(
	w http.ResponseWriter,
	ctx context.Context,
	identity operatorCodeRequestIdentity,
	caller operatorCodeAuthorizedRequest,
	category uci.ReleaseCategory,
	response uci.QueryResponse,
	matches func(uci.QueryResponse, uci.AuthorizedContext) bool,
) {
	released := uci.ReleaseQueryResponse(ctx, adapter.releaseRequest(identity, caller, category, &response, matches), adapter.releaseGate(identity, caller.Proof()))
	if err := released.Validate(); err != nil {
		operatorCodeWriteFailure(w, uci.ReleaseFailureExposureUnavailable)
		return
	}
	status := http.StatusOK
	switch released.Status {
	case uci.QueryStatusForbidden:
		status = http.StatusForbidden
	case uci.QueryStatusContextRequired:
		status = http.StatusConflict
	case uci.QueryStatusUnavailable:
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(released)
}

func (adapter *OperatorCodeHTTPAdapter) releaseRequest(
	identity operatorCodeRequestIdentity,
	caller operatorCodeAuthorizedRequest,
	category uci.ReleaseCategory,
	response *uci.QueryResponse,
	matches func(uci.QueryResponse, uci.AuthorizedContext) bool,
) uci.ReleaseRequest {
	return uci.ReleaseRequest{
		AuthRealm:            string(auth.SourceSession),
		Caller:               uci.ReleaseCaller{Browser: &uci.BrowserReleaseCaller{Subject: caller.caller.Subject, SessionID: identity.sessionID, DocumentBinding: caller.caller.BindingID}},
		RequestID:            identity.requestID,
		RequestBindingDigest: identity.digest,
		Category:             category,
		Response:             response,
		ResponseMatches:      matches,
		RecordedAt:           adapter.currentTime(),
	}
}

func (adapter *OperatorCodeHTTPAdapter) releaseGate(identity operatorCodeRequestIdentity, proof BrowserBindingProof) uci.ReleaseGate {
	return operatorCodeReleaseGate{adapter: adapter, identity: identity, proof: proof}
}

func (adapter *OperatorCodeHTTPAdapter) currentTime() time.Time {
	if adapter != nil && adapter.now != nil {
		return adapter.now().UTC()
	}
	return time.Now().UTC()
}

type operatorCodeReleaseGate struct {
	adapter  *OperatorCodeHTTPAdapter
	identity operatorCodeRequestIdentity
	proof    BrowserBindingProof
}

func (gate operatorCodeReleaseGate) Reauthorize(ctx context.Context) (uci.AuthorizedContext, uci.ReleaseFailureCode) {
	caller, failure := gate.adapter.authorize(ctx, gate.identity, gate.proof)
	if failure != uci.ReleaseFailureNone {
		return uci.AuthorizedContext{}, failure
	}
	return caller.authorized, uci.ReleaseFailureNone
}

func (gate operatorCodeReleaseGate) AppendExposure(ctx context.Context, authorized uci.AuthorizedContext, input uci.ExposureInput) (uci.QueryExposure, uci.ReleaseFailureCode) {
	if gate.adapter == nil || gate.adapter.recorder == nil {
		return uci.QueryExposure{}, uci.ReleaseFailureExposureUnavailable
	}
	receipt, err := gate.adapter.recorder.Record(ctx, authorized, input)
	if err != nil {
		if errors.Is(err, uci.ErrIdempotencyMismatch) {
			return uci.QueryExposure{}, uci.ReleaseFailureIdempotencyMismatch
		}
		return uci.QueryExposure{}, uci.ReleaseFailureExposureUnavailable
	}
	return receipt, uci.ReleaseFailureNone
}

func operatorCodeSearchResponseValid(response uci.QueryResponse, authorized uci.AuthorizedContext) bool {
	if response.Status == uci.QueryStatusContextRequired || response.Status == uci.QueryStatusForbidden {
		return response.Validate() == nil
	}
	return response.Exposure == nil && response.Graph == nil && response.ValidatePreExposure() == nil && operatorCodeResponseContextExact(response, authorized)
}

func operatorCodeGraphResponseValid(response uci.QueryResponse, authorized uci.AuthorizedContext, input mcp.CodebaseGraphInput) bool {
	if response.Status == uci.QueryStatusContextRequired || response.Status == uci.QueryStatusForbidden {
		return response.Validate() == nil
	}
	if response.Exposure != nil || response.ValidatePreExposure() != nil || !operatorCodeResponseContextExact(response, authorized) || response.Retrieval == nil || response.Retrieval.Mode != uci.QueryRetrievalGraph || response.Items == nil || len(*response.Items) != 0 {
		return false
	}
	if response.Status == uci.QueryStatusUnavailable {
		return response.Graph == nil
	}
	return response.Graph != nil && len(response.Graph.Nodes) <= input.Budget.MaxNodes && len(response.Graph.Edges) <= input.Budget.MaxEdges
}

func operatorCodeReadResponseValid(response uci.QueryResponse, authorized uci.AuthorizedContext, input mcp.CodebaseReadInput) bool {
	if response.Status == uci.QueryStatusContextRequired || response.Status == uci.QueryStatusForbidden {
		return response.Validate() == nil
	}
	if response.Exposure != nil || response.Graph != nil || response.ValidatePreExposure() != nil || !operatorCodeResponseContextExact(response, authorized) {
		return false
	}
	switch response.Status {
	case uci.QueryStatusOK, uci.QueryStatusPartial, uci.QueryStatusStale:
		if response.Retrieval == nil || response.Retrieval.Mode != uci.QueryRetrievalExact || response.Items == nil || len(*response.Items) != 1 || response.Truncated == nil || *response.Truncated {
			return false
		}
		item := (*response.Items)[0]
		return item.Ref == input.Ref && item.Span == input.Span && item.ContentDigest == input.ContentDigest && len(item.Excerpt) == int(input.Span.ByteEnd-input.Span.ByteStart) && len(item.Excerpt) <= input.MaxBytes
	case uci.QueryStatusEmpty:
		return response.Retrieval != nil && response.Retrieval.Mode == uci.QueryRetrievalExact
	case uci.QueryStatusUnavailable:
		return true
	default:
		return false
	}
}

func operatorCodeResponseContextExact(response uci.QueryResponse, authorized uci.AuthorizedContext) bool {
	if response.Contexts == nil || len(*response.Contexts) != 1 {
		return false
	}
	context := (*response.Contexts)[0]
	ref := authorized.Ref()
	if ref.SpaceID != nil || context.SpaceID != nil {
		return false
	}
	return context.SourceID == ref.SourceID &&
		context.CheckoutID == ref.CheckoutID &&
		context.ViewID == ref.ViewID &&
		context.ProfileID == ref.AnalysisProfileID &&
		context.Generation == ref.Generation
}

func operatorCodePathProof(r *http.Request, request operatorCodePathProofRequest) (BrowserBindingProof, bool) {
	tabBindingID := chi.URLParam(r, "tab_binding_id")
	if !operatorCodeUUID(tabBindingID) || !operatorCodeText(request.DocumentProof) {
		return BrowserBindingProof{}, false
	}
	return BrowserBindingProof{TabBindingID: tabBindingID, DocumentProof: request.DocumentProof}, true
}

func browserBindingContext(ref uci.ContextRef) BrowserBindingContext {
	return BrowserBindingContext{
		SourceID:          ref.SourceID,
		CheckoutID:        ref.CheckoutID,
		ViewID:            ref.ViewID,
		AnalysisProfileID: ref.AnalysisProfileID,
		Generation:        ref.Generation,
	}
}

func operatorCodeStatusValid(snapshot mcp.CodebaseStatusSnapshot) bool {
	if snapshot.TotalChunks < 0 || snapshot.EmbeddedChunks < 0 || snapshot.EmbeddedChunks > snapshot.TotalChunks || snapshot.Embedding.Validate() != nil {
		return false
	}
	return snapshot.Freshness == nil || snapshot.Freshness.Validate() == nil
}

type operatorCodeTransitionResponse struct {
	State         BrowserBindingTransitionState `json:"state"`
	TabBindingID  string                        `json:"tab_binding_id,omitempty"`
	DocumentProof string                        `json:"document_proof,omitempty"`
	ResumeNonce   string                        `json:"resume_nonce,omitempty"`
	ReloadToken   string                        `json:"reload_token,omitempty"`
}

func operatorCodeWriteTransition(w http.ResponseWriter, transition BrowserBindingTransition) {
	response := operatorCodeTransitionResponse{State: transition.State}
	switch transition.State {
	case BrowserBindingReloadPending:
		writeJSON(w, response)
	case BrowserBindingReady, BrowserBindingCollision, BrowserBindingAmbiguous:
		if !operatorCodeUUID(transition.TabBindingID) || !operatorCodeText(transition.DocumentProof) || !operatorCodeText(transition.ResumeNonce) || !operatorCodeText(transition.ReloadToken) {
			operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
			return
		}
		response.TabBindingID = transition.TabBindingID
		response.DocumentProof = transition.DocumentProof
		response.ResumeNonce = transition.ResumeNonce
		response.ReloadToken = transition.ReloadToken
		writeJSON(w, response)
	default:
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
	}
}

type operatorCodeMetadata struct {
	Source   string `json:"source"`
	Checkout string `json:"checkout"`
	View     string `json:"view"`
}

type operatorCodeContextsResponse struct {
	Context operatorCodeMetadata `json:"context"`
}

type operatorCodeStatusResponse struct {
	TotalChunks    int64               `json:"total_chunks"`
	EmbeddedChunks int64               `json:"embedded_chunks"`
	Embedding      uci.EmbeddingStatus `json:"embedding"`
	Freshness      *uci.QueryFreshness `json:"freshness,omitempty"`
}

func operatorCodeSafeMetadata(metadata map[string]string) (operatorCodeMetadata, bool) {
	if len(metadata) != 3 {
		return operatorCodeMetadata{}, false
	}
	for _, key := range []string{"source", "checkout", "view"} {
		if !operatorCodeText(metadata[key]) {
			return operatorCodeMetadata{}, false
		}
	}
	return operatorCodeMetadata{Source: metadata["source"], Checkout: metadata["checkout"], View: metadata["view"]}, true
}

func operatorCodeWriteFailure(w http.ResponseWriter, failure uci.ReleaseFailureCode) {
	switch failure {
	case uci.ReleaseFailurePermissionDenied:
		operatorCodeWriteBodyless(w, http.StatusForbidden)
	case uci.ReleaseFailureContextRequired, uci.ReleaseFailureContextMismatch:
		operatorCodeWriteBodyless(w, http.StatusConflict)
	case uci.ReleaseFailureIdempotencyMismatch:
		operatorCodeWriteBodyless(w, http.StatusConflict)
	default:
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
	}
}

func operatorCodeWriteBodyless(w http.ResponseWriter, status int) {
	if w != nil {
		w.WriteHeader(status)
	}
}

func (caller operatorCodeAuthorizedRequest) Proof() BrowserBindingProof {
	return caller.proof
}
