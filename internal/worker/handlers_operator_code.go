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
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/uci"
)

const (
	operatorCodeRequestMaxBytes = 32 << 10
	operatorCodeSearchDefault   = 10
	operatorCodeSearchMax       = 50
	operatorCodeSearchMaxText   = 16 << 10
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
	grants       operatorCodeGrantReader
	bindings     operatorCodeBindingApplication
	authority    operatorCodeContextAuthorizer
	app          operatorCodeApplication
	contexts     operatorCodeContextStore
	indexTargets operatorCodeIndexTargetResolver
	graphSources operatorCodeGraphSourceReader
	recorder     operatorCodeExposureRecorder
	now          func() time.Time
}

// operatorCodeGrantReader retains the already-typed T012 grant checks. The
// adapter never queries grant persistence or accepts a grant reference itself.
type operatorCodeGrantReader interface {
	Active(context.Context, auth.Identity, string, string) (gormdb.BrowserReadGrant, bool, error)
}

// operatorCodeBindingApplication retains the T014 lifecycle and document-proof
// state machine. A binding is never Code-read authority.
type operatorCodeBindingApplication interface {
	Guard(context.Context, auth.Identity, string, BrowserBindingProof) (BrowserBindingGuarded, error)
	Handshake(context.Context, auth.Identity, string, BrowserBindingHandshakeInput) (BrowserBindingTransition, error)
	Resume(context.Context, auth.Identity, string, BrowserBindingResumeInput) (BrowserBindingTransition, error)
	Renew(context.Context, auth.Identity, string, BrowserBindingProof) error
	Close(context.Context, auth.Identity, string, BrowserBindingProof) error
}

// operatorCodeContextAuthorizer is the composition-owned bridge from an exact
// catalog ContextRef to an AuthorizedContext. Its implementation derives the
// source realm and checkout owner server-side; browser JSON never supplies a
// principal, workspace, project, Space, label, or path.
type operatorCodeContextAuthorizer interface {
	AuthorizeOperatorCode(context.Context, operatorCodeVerifiedCaller) (uci.AuthorizedContext, error)
}

// operatorCodeApplication is the existing UCI application surface after a
// browser request has been authorized. It contains no grant, tab-proof, or
// release responsibility.
type operatorCodeApplication interface {
	SearchOperatorCodebase(context.Context, uci.AuthorizedContext, uci.QuerySpec) (uci.QueryResponse, error)
	ExploreCodebase(context.Context, uci.AuthorizedContext, mcp.CodebaseGraphInput) (uci.QueryResponse, error)
	ReadCodebase(context.Context, uci.AuthorizedContext, mcp.CodebaseReadInput) (uci.QueryResponse, error)
	CodebaseStatus(context.Context, uci.AuthorizedContext) (mcp.CodebaseStatusSnapshot, error)
}

// operatorCodeContextStore owns the small cross-table API state that must be
// rechecked atomically: catalog choices, exact pins, and opaque search cursors.
type operatorCodeContextStore interface {
	ListCatalog(context.Context, int64) ([]gormdb.BrowserCodeContextCatalogEntry, error)
	Pin(context.Context, gormdb.BrowserCodeContextPin) error
	AuthorizeInitialIndexIntent(context.Context, gormdb.BrowserCodeIndexIntentTarget) (gormdb.BrowserCodeIndexIntentBinding, error)
	ReauthorizeIndexIntent(context.Context, gormdb.BrowserCodeIndexIntentTarget) (gormdb.BrowserCodeIndexIntentBinding, error)
	LoadContinuation(context.Context, string, gormdb.BrowserCodeContinuationBinding) (string, error)
	CreateContinuation(context.Context, gormdb.BrowserCodeContinuationBinding, string) (string, error)
	AdvanceContinuation(context.Context, string, gormdb.BrowserCodeContinuationBinding, string) (string, error)
}

type operatorCodeGraphSourceReader interface {
	DescribeGraphSource(context.Context, uci.AuthorizedContext, uci.QueryEntityRef) (uci.VersionedReadSpec, bool, error)
}

// operatorCodeIndexTargetResolver exposes only a currently polling
// daemon-owned no-View target. Browser input cannot choose its profile.
type operatorCodeIndexTargetResolver interface {
	Resolve(string, string) (uci.IndexBinding, bool)
}

// operatorCodeIndexIntentApplication is an optional durable-index capability.
// It receives only the reauthorized UCI context and opaque intent arguments;
// grant, binding, browser proof, and release ownership remain at this boundary.
type operatorCodeIndexIntentApplication interface {
	SubmitIndexIntent(context.Context, uci.AuthorizedContext, string, uci.IndexIntentKind) (uci.IndexIntent, error)
	GetIndexIntent(context.Context, uci.AuthorizedContext, string) (uci.IndexIntent, error)
	RetryIndexIntent(context.Context, uci.AuthorizedContext, string) (uci.IndexIntent, error)
}

// operatorCodeNoViewIndexIntentApplication is the first-index path. It keeps
// the server-derived checkout binding separate from a published ContextRef.
type operatorCodeNoViewIndexIntentApplication interface {
	SubmitNoViewIndexIntent(context.Context, uci.IndexScope, string, string, uci.IndexIntentKind) (uci.IndexIntent, error)
	NoViewIndexIntentByRequestRef(context.Context, string) (uci.IndexIntent, error)
	ReplayNoViewIndexIntent(context.Context, uci.IndexScope, string, string, uci.IndexIntentKind) (uci.IndexIntent, error)
	NoViewIndexIntentTarget(context.Context, string) (uci.IndexScope, string, error)
	GetNoViewIndexIntent(context.Context, uci.IndexScope, string, string) (uci.IndexIntent, error)
	RetryNoViewIndexIntent(context.Context, uci.IndexScope, string, string) (uci.IndexIntent, error)
}

// operatorCodeExposureRecorder is the existing T016 recorder boundary. A
// failed append must never permit contextual serialization.
type operatorCodeExposureRecorder interface {
	Record(context.Context, uci.AuthorizedContext, uci.ExposureInput) (uci.QueryExposure, error)
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

// HandlePin stores only a browser-selected exact ContextRef that remains
// grant-authorized and published in the same transaction as its audit append.
func (adapter *OperatorCodeHTTPAdapter) HandlePin(w http.ResponseWriter, r *http.Request) {
	var request operatorCodePinRequest
	identity, ok := adapter.decode(w, r, "operator-code-tab-context", &request)
	if !ok {
		return
	}
	proof, ok := operatorCodePathProof(r, request.operatorCodePathProofRequest)
	if !ok {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return
	}
	ref, ok := request.ContextRef.ref()
	if !ok {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return
	}
	subject, ok := identity.identity.SessionBrowserSubject()
	if !ok {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return
	}
	if adapter == nil || adapter.contexts == nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	err := adapter.contexts.Pin(r.Context(), gormdb.BrowserCodeContextPin{
		Caller:              gormdb.BrowserTabBindingCaller{SubjectUserID: subject.UserID, SessionID: identity.sessionID},
		TabBindingID:        proof.TabBindingID,
		DocumentProofDigest: browserBindingDigest(proof.DocumentProof),
		Context:             ref,
	})
	if err != nil {
		if errors.Is(err, gormdb.ErrBrowserCodeContextDenied) || errors.Is(err, gormdb.ErrBrowserTabBindingDenied) {
			operatorCodeWriteBodyless(w, http.StatusForbidden)
		} else {
			operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		}
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

// HandleIndexIntentSubmit acknowledges one durable indexing request. A 202 is
// only an admission projection; GET status is the sole current-state source.
func (adapter *OperatorCodeHTTPAdapter) HandleIndexIntentSubmit(w http.ResponseWriter, r *http.Request) {
	var request operatorCodeIndexIntentSubmitRequest
	identity, ok := adapter.decodeIndexIntentJSON(w, r, "operator-code-index-intent-submit", &request)
	if !ok || !request.valid() {
		if ok {
			operatorCodeWriteBodyless(w, http.StatusBadRequest)
		}
		return
	}
	identity.digest = operatorCodeIndexIntentDigest("operator-code-index-intent-submit", request.Proof(), "", request.RequestRef, request.Kind)
	r = r.WithContext(auditcontext.WithSourceSession(r.Context(), identity.sessionID))
	if request.Target != nil {
		application, available := adapter.app.(operatorCodeNoViewIndexIntentApplication)
		if !available {
			operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
			return
		}
		existing, lookupErr := application.NoViewIndexIntentByRequestRef(r.Context(), request.RequestRef)
		if lookupErr == nil {
			if existing.Kind != request.Kind || existing.Scope.SourceID != request.Target.SourceID || existing.Scope.CheckoutID != request.Target.CheckoutID {
				operatorCodeWriteBodyless(w, http.StatusConflict)
				return
			}
			target, failure := adapter.authorizeNoViewIndexIntent(r.Context(), identity, request.Proof(), *request.Target, existing.ProfileID, false)
			if failure != uci.ReleaseFailureNone {
				operatorCodeWriteFailure(w, failure)
				return
			}
			intent, err := application.ReplayNoViewIndexIntent(r.Context(), target.scope, target.profileID, request.RequestRef, request.Kind)
			if err != nil {
				operatorCodeWriteFailure(w, operatorCodeIndexIntentFailure(err))
				return
			}
			if !operatorCodeIndexIntentShapeValid(intent) {
				operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
				return
			}
			if !operatorCodeNoViewIndexIntentMatches(intent, target) {
				operatorCodeWriteBodyless(w, http.StatusConflict)
				return
			}
			operatorCodeWriteIndexIntentAcknowledgement(w, intent)
			return
		}
		if !errors.Is(lookupErr, uci.ErrIndexIntentNotFound) {
			operatorCodeWriteFailure(w, operatorCodeIndexIntentFailure(lookupErr))
			return
		}
		target, failure := adapter.authorizeNoViewIndexIntent(r.Context(), identity, request.Proof(), *request.Target, "", true)
		if failure != uci.ReleaseFailureNone {
			operatorCodeWriteFailure(w, failure)
			return
		}
		intent, err := application.SubmitNoViewIndexIntent(r.Context(), target.scope, target.profileID, request.RequestRef, request.Kind)
		if err != nil {
			operatorCodeWriteFailure(w, operatorCodeIndexIntentFailure(err))
			return
		}
		if !operatorCodeIndexIntentShapeValid(intent) {
			operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
			return
		}
		if !operatorCodeNoViewIndexIntentMatches(intent, target) {
			operatorCodeWriteBodyless(w, http.StatusConflict)
			return
		}
		operatorCodeWriteIndexIntentAcknowledgement(w, intent)
		return
	}
	caller, failure := adapter.authorize(r.Context(), identity, request.Proof())
	if failure != uci.ReleaseFailureNone {
		operatorCodeWriteFailure(w, failure)
		return
	}
	application, available := adapter.app.(operatorCodeIndexIntentApplication)
	if !available {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	intent, err := application.SubmitIndexIntent(r.Context(), caller.authorized, request.RequestRef, request.Kind)
	if err != nil {
		operatorCodeWriteFailure(w, operatorCodeIndexIntentFailure(err))
		return
	}
	if !operatorCodeIndexIntentShapeValid(intent) {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	if !operatorCodeIndexIntentMatches(intent, caller.authorized) {
		operatorCodeWriteBodyless(w, http.StatusConflict)
		return
	}
	operatorCodeWriteIndexIntentAcknowledgement(w, intent)
}

// HandleIndexIntentStatus returns durable operation state. Only a completed
// intent can release result metadata, and a release refusal remains concealed.
func (adapter *OperatorCodeHTTPAdapter) HandleIndexIntentStatus(w http.ResponseWriter, r *http.Request) {
	identity, intentRef, proof, ok := adapter.decodeIndexIntentStatus(w, r, "operator-code-index-intent-status")
	if !ok {
		return
	}
	r = r.WithContext(auditcontext.WithSourceSession(r.Context(), identity.sessionID))
	if application, available := adapter.app.(operatorCodeNoViewIndexIntentApplication); available {
		scope, profileID, err := application.NoViewIndexIntentTarget(r.Context(), intentRef)
		if err == nil {
			target, failure := adapter.authorizeNoViewIndexIntent(r.Context(), identity, proof, operatorCodeIndexIntentTargetRequest{SourceID: scope.SourceID, CheckoutID: scope.CheckoutID}, profileID, false)
			if failure != uci.ReleaseFailureNone {
				operatorCodeWriteFailure(w, failure)
				return
			}
			intent, err := application.GetNoViewIndexIntent(r.Context(), target.scope, target.profileID, intentRef)
			if err != nil {
				operatorCodeWriteFailure(w, operatorCodeIndexIntentFailure(err))
				return
			}
			if !operatorCodeIndexIntentShapeValid(intent) {
				operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
				return
			}
			if !operatorCodeNoViewIndexIntentMatches(intent, target) {
				operatorCodeWriteBodyless(w, http.StatusConflict)
				return
			}
			response := operatorCodeIndexIntentResponseFor(intent)
			if intent.State == uci.IndexIntentCompleted {
				decision := adapter.releaseNoViewIndexIntentResult(r.Context(), identity, proof, target, intent)
				if decision.Released() && intent.ResultView != nil && operatorCodeRefsEqual(*intent.ResultView, decision.Authorized.Ref()) {
					response.Result = &operatorCodeIndexIntentResultResponse{ViewRef: intent.ResultView.ViewID, Generation: intent.ResultView.Generation}
				}
			}
			writeJSON(w, response)
			return
		}
	}
	caller, failure := adapter.authorize(r.Context(), identity, proof)
	if failure != uci.ReleaseFailureNone {
		operatorCodeWriteFailure(w, failure)
		return
	}
	application, available := adapter.app.(operatorCodeIndexIntentApplication)
	if !available {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	intent, err := application.GetIndexIntent(r.Context(), caller.authorized, intentRef)
	if err != nil {
		operatorCodeWriteFailure(w, operatorCodeIndexIntentFailure(err))
		return
	}
	if !operatorCodeIndexIntentShapeValid(intent) {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	if !operatorCodeIndexIntentMatches(intent, caller.authorized) {
		operatorCodeWriteBodyless(w, http.StatusConflict)
		return
	}
	response := operatorCodeIndexIntentResponseFor(intent)
	if intent.State == uci.IndexIntentCompleted {
		decision := adapter.releaseNonContent(r.Context(), identity, caller, uci.ReleaseCategoryCodeIndexResult)
		if decision.Released() && operatorCodeIndexIntentMatches(intent, decision.Authorized) {
			response.Result = &operatorCodeIndexIntentResultResponse{ViewRef: intent.ResultView.ViewID, Generation: intent.ResultView.Generation}
		}
	}
	writeJSON(w, response)
}

// HandleIndexIntentRetry acknowledges a retry only after the application has
// reauthorized it. It never returns a completion result or schedules directly.
func (adapter *OperatorCodeHTTPAdapter) HandleIndexIntentRetry(w http.ResponseWriter, r *http.Request) {
	var request operatorCodeIndexIntentRetryRequest
	identity, ok := adapter.decodeIndexIntentJSON(w, r, "operator-code-index-intent-retry", &request)
	if !ok || !request.valid() {
		if ok {
			operatorCodeWriteBodyless(w, http.StatusBadRequest)
		}
		return
	}
	intentRef, ok := operatorCodeIndexIntentPath(r)
	if !ok {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return
	}
	identity.digest = operatorCodeIndexIntentDigest("operator-code-index-intent-retry", request.Proof(), intentRef, "", "")
	r = r.WithContext(auditcontext.WithSourceSession(r.Context(), identity.sessionID))
	if application, available := adapter.app.(operatorCodeNoViewIndexIntentApplication); available {
		scope, profileID, err := application.NoViewIndexIntentTarget(r.Context(), intentRef)
		if err == nil {
			target, failure := adapter.authorizeNoViewIndexIntent(r.Context(), identity, request.Proof(), operatorCodeIndexIntentTargetRequest{SourceID: scope.SourceID, CheckoutID: scope.CheckoutID}, profileID, false)
			if failure != uci.ReleaseFailureNone {
				operatorCodeWriteFailure(w, failure)
				return
			}
			intent, err := application.RetryNoViewIndexIntent(r.Context(), target.scope, target.profileID, intentRef)
			if err != nil {
				operatorCodeWriteFailure(w, operatorCodeIndexIntentFailure(err))
				return
			}
			if !operatorCodeIndexIntentShapeValid(intent) || !operatorCodeNoViewIndexIntentMatches(intent, target) || intent.State != uci.IndexIntentQueued {
				operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
				return
			}
			operatorCodeWriteIndexIntentAcknowledgement(w, intent)
			return
		}
	}
	caller, failure := adapter.authorize(r.Context(), identity, request.Proof())
	if failure != uci.ReleaseFailureNone {
		operatorCodeWriteFailure(w, failure)
		return
	}
	application, available := adapter.app.(operatorCodeIndexIntentApplication)
	if !available {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	intent, err := application.RetryIndexIntent(r.Context(), caller.authorized, intentRef)
	if err != nil {
		operatorCodeWriteFailure(w, operatorCodeIndexIntentFailure(err))
		return
	}
	if !operatorCodeIndexIntentShapeValid(intent) || !operatorCodeIndexIntentMatches(intent, caller.authorized) || intent.State != uci.IndexIntentQueued {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	operatorCodeWriteIndexIntentAcknowledgement(w, intent)
}

// HandleSearch executes one exact pinned-View page. Browser continuations are
// opaque, expiring, one-use server records rather than UCI's signed transport
// continuations.
func (adapter *OperatorCodeHTTPAdapter) HandleSearch(w http.ResponseWriter, r *http.Request) {
	var request operatorCodeSearchRequest
	identity, ok := adapter.decode(w, r, "operator-code-search", &request)
	if !ok || !request.valid() {
		if ok {
			operatorCodeWriteBodyless(w, http.StatusBadRequest)
		}
		return
	}
	filter, _ := request.filter()
	r = r.WithContext(auditcontext.WithSourceSession(r.Context(), identity.sessionID))
	caller, failure := adapter.authorize(r.Context(), identity, request.Proof())
	if failure != uci.ReleaseFailureNone {
		operatorCodeWriteFailure(w, failure)
		return
	}
	if adapter.app == nil || adapter.contexts == nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	binding := operatorCodeSearchContinuationBinding(caller, request, filter)
	var internalContinuation *string
	if request.Continuation != nil {
		value, err := adapter.contexts.LoadContinuation(r.Context(), *request.Continuation, binding)
		if err != nil {
			operatorCodeWriteCursorFailure(w, err)
			return
		}
		internalContinuation = &value
	}
	response, err := adapter.app.SearchOperatorCodebase(r.Context(), caller.authorized, uci.QuerySpec{
		ClientSessionID: "operator-code/" + caller.caller.BindingID,
		Mode:            uci.QueryModeFTS,
		Text:            request.Query,
		Filter:          filter,
		Order:           uci.QueryOrderRelevance,
		Limit:           request.limit(),
		Continuation:    internalContinuation,
	})
	if err != nil || !operatorCodeSearchResponseValid(response, caller.authorized) {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	nextServiceCursor := ""
	if response.Continuation != nil && response.Continuation.Value != nil {
		nextServiceCursor = *response.Continuation.Value
	}
	nextCursor := ""
	if request.Continuation == nil {
		nextCursor, err = adapter.contexts.CreateContinuation(r.Context(), binding, nextServiceCursor)
	} else {
		nextCursor, err = adapter.contexts.AdvanceContinuation(r.Context(), *request.Continuation, binding, nextServiceCursor)
	}
	if err != nil {
		operatorCodeWriteCursorFailure(w, err)
		return
	}
	if nextCursor == "" {
		response.Continuation = &uci.QueryContinuation{}
	} else {
		response.Continuation = &uci.QueryContinuation{Value: &nextCursor}
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
	r = r.WithContext(auditcontext.WithSourceSession(r.Context(), identity.sessionID))
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
	adapter.writeReleasedGraph(w, operationCtx, identity, caller, response, func(candidate uci.QueryResponse, authorized uci.AuthorizedContext) bool {
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
	r = r.WithContext(auditcontext.WithSourceSession(r.Context(), identity.sessionID))
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

// HandleContexts lists every active grant's safe Source → Checkout → View
// choices. It does not silently select a grant, checkout, or current View.
func (adapter *OperatorCodeHTTPAdapter) HandleContexts(w http.ResponseWriter, r *http.Request) {
	var request operatorCodeProofRequest
	identity, ok := adapter.decode(w, r, "operator-code-contexts", &request)
	if !ok {
		return
	}
	subject, ok := identity.identity.SessionBrowserSubject()
	if !ok {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return
	}
	if adapter == nil || adapter.bindings == nil || adapter.contexts == nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	guarded, err := adapter.bindings.Guard(r.Context(), identity.identity, identity.sessionID, request.Proof())
	if err != nil || guarded.TabBindingID != request.TabBindingID {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return
	}
	entries, err := adapter.contexts.ListCatalog(r.Context(), subject.UserID)
	if err != nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, operatorCodeContextsResponse{Contexts: operatorCodeCatalogEntries(entries, adapter.indexTargets)})
}

type operatorCodeProofRequest struct {
	TabBindingID  string `json:"tab_binding_id"`
	DocumentProof string `json:"document_proof"`
}

func (request operatorCodeProofRequest) Proof() BrowserBindingProof {
	return BrowserBindingProof{TabBindingID: request.TabBindingID, DocumentProof: request.DocumentProof}
}

type operatorCodeIndexIntentTargetRequest struct {
	SourceID   string `json:"source_id"`
	CheckoutID string `json:"checkout_id"`
}

func (target operatorCodeIndexIntentTargetRequest) valid() bool {
	return operatorCodeUUID(target.SourceID) && operatorCodeUUID(target.CheckoutID)
}

type operatorCodeIndexIntentSubmitRequest struct {
	operatorCodeProofRequest
	RequestRef string                                `json:"request_ref"`
	Kind       uci.IndexIntentKind                   `json:"kind"`
	Target     *operatorCodeIndexIntentTargetRequest `json:"target,omitempty"`
}

func (request operatorCodeIndexIntentSubmitRequest) valid() bool {
	return operatorCodeProofValid(request.Proof()) && operatorCodeText(request.RequestRef) && (request.Kind == uci.IndexIntentReindex || request.Kind == uci.IndexIntentReconcile) && (request.Target == nil || request.Target.valid())
}

type operatorCodeIndexIntentRetryRequest struct {
	operatorCodeProofRequest
}

func (request operatorCodeIndexIntentRetryRequest) valid() bool {
	return operatorCodeProofValid(request.Proof())
}

func operatorCodeProofValid(proof BrowserBindingProof) bool {
	return operatorCodeUUID(proof.TabBindingID) && operatorCodeText(proof.DocumentProof)
}

type operatorCodePathProofRequest struct {
	DocumentProof string `json:"document_proof"`
}

type operatorCodeContextRefRequest struct {
	SourceID          string `json:"source_id"`
	CheckoutID        string `json:"checkout_id"`
	ViewID            string `json:"view_id"`
	AnalysisProfileID string `json:"analysis_profile_id"`
	Generation        int64  `json:"generation"`
}

func (request operatorCodeContextRefRequest) ref() (uci.ContextRef, bool) {
	if !operatorCodeUUID(request.SourceID) || !operatorCodeUUID(request.CheckoutID) || !operatorCodeUUID(request.ViewID) || !operatorCodeUUID(request.AnalysisProfileID) || request.Generation < 1 {
		return uci.ContextRef{}, false
	}
	return uci.ContextRef{
		SourceID:          request.SourceID,
		CheckoutID:        request.CheckoutID,
		ViewID:            request.ViewID,
		AnalysisProfileID: request.AnalysisProfileID,
		Generation:        request.Generation,
	}, true
}

type operatorCodePinRequest struct {
	operatorCodePathProofRequest
	ContextRef operatorCodeContextRefRequest `json:"context_ref"`
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
	Query        string   `json:"query"`
	PathPrefix   string   `json:"path_prefix"`
	Languages    []string `json:"languages"`
	Limit        *int     `json:"limit"`
	Continuation *string  `json:"continuation"`
}

func (request operatorCodeSearchRequest) valid() bool {
	_, ok := request.filter()
	return operatorCodeSearchText(request.Query) && ok && (request.Limit == nil || (*request.Limit >= 1 && *request.Limit <= operatorCodeSearchMax)) && (request.Continuation == nil || operatorCodeUUID(*request.Continuation))
}

func (request operatorCodeSearchRequest) limit() int {
	if request.Limit == nil {
		return operatorCodeSearchDefault
	}
	return *request.Limit
}

func (request operatorCodeSearchRequest) filter() (uci.QueryFilter, bool) {
	prefix, err := uci.NormalizeQueryPathPrefix(request.PathPrefix)
	if err != nil || len(request.Languages) > 32 {
		return uci.QueryFilter{}, false
	}
	languages := make([]string, 0, len(request.Languages))
	for _, language := range request.Languages {
		language = strings.ToLower(strings.TrimSpace(language))
		if len(language) == 0 || len(language) > 64 || !utf8.ValidString(language) || strings.ContainsAny(language, "\x00\r\n") {
			return uci.QueryFilter{}, false
		}
		languages = append(languages, language)
	}
	sort.Strings(languages)
	unique := languages[:0]
	for _, language := range languages {
		if len(unique) == 0 || unique[len(unique)-1] != language {
			unique = append(unique, language)
		}
	}
	return uci.QueryFilter{PathPrefix: prefix, Languages: unique}, true
}

func operatorCodeSearchText(value string) bool {
	if len(value) == 0 || len(value) > operatorCodeSearchMaxText || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func operatorCodeSearchContinuationBinding(caller operatorCodeAuthorizedRequest, request operatorCodeSearchRequest, filter uci.QueryFilter) gormdb.BrowserCodeContinuationBinding {
	return gormdb.BrowserCodeContinuationBinding{
		SubjectUserID: caller.caller.Subject.UserID,
		AuthRealm:     caller.grant.AuthRealm,
		GrantRef:      caller.grant.GrantRef,
		GrantIssuedAt: caller.grant.IssuedAt,
		TabBindingID:  caller.caller.BindingID,
		Context:       caller.authorized.Ref(),
		QueryDigest:   operatorCodeSearchDigest("query", request.Query),
		FilterDigest:  operatorCodeSearchDigest("filter", filter.PathPrefix, strings.Join(filter.Languages, "\x00")),
		PageSize:      request.limit(),
	}
}

func operatorCodeSearchDigest(kind string, values ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("operator-code-search/v1"))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(kind))
	for _, value := range values {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func operatorCodeWriteCursorFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, gormdb.ErrBrowserCodeContinuationDenied) {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return
	}
	operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
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
	grant      gormdb.BrowserReadGrant
	authRealm  string
}

const (
	operatorCodeRequestIDHeader   = "X-Engram-Request-ID"
	operatorCodeContentTypeHeader = "Content-Type"
	operatorCodeJSONContentType   = "application/json"
)

func (adapter *OperatorCodeHTTPAdapter) decode(w http.ResponseWriter, r *http.Request, endpoint string, target any) (operatorCodeRequestIdentity, bool) {
	identity, ok := adapter.decodeIdentity(w, r, endpoint)
	if !ok {
		return operatorCodeRequestIdentity{}, false
	}
	raw, err := operatorCodeReadJSON(r, target)
	if err != nil {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return operatorCodeRequestIdentity{}, false
	}
	identity.digest = operatorCodeRequestDigest(endpoint, raw)
	return identity, true
}

func (adapter *OperatorCodeHTTPAdapter) decodeIdentity(w http.ResponseWriter, r *http.Request, endpoint string) (operatorCodeRequestIdentity, bool) {
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
	requestID := r.Header.Get(operatorCodeRequestIDHeader)
	if !operatorCodeText(requestID) {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return operatorCodeRequestIdentity{}, false
	}
	return operatorCodeRequestIdentity{requestID: requestID, identity: identity, sessionID: sessionID}, true
}

func (adapter *OperatorCodeHTTPAdapter) decodeIndexIntentJSON(w http.ResponseWriter, r *http.Request, endpoint string, target any) (operatorCodeRequestIdentity, bool) {
	if r == nil || r.URL == nil || r.Method != http.MethodPost || r.URL.RawQuery != "" || len(r.Header.Values(operatorCodeRequestIDHeader)) != 1 {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return operatorCodeRequestIdentity{}, false
	}
	identity, ok := adapter.decodeIdentity(w, r, endpoint)
	if !ok {
		return operatorCodeRequestIdentity{}, false
	}
	if _, err := operatorCodeReadJSON(r, target); err != nil {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return operatorCodeRequestIdentity{}, false
	}
	return identity, true
}

func (adapter *OperatorCodeHTTPAdapter) decodeIndexIntentStatus(w http.ResponseWriter, r *http.Request, endpoint string) (operatorCodeRequestIdentity, string, BrowserBindingProof, bool) {
	if r == nil || r.URL == nil || r.Method != http.MethodGet || r.URL.RawQuery != "" || !operatorCodeEmptyBody(r) || len(r.Header.Values(operatorCodeRequestIDHeader)) != 1 {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return operatorCodeRequestIdentity{}, "", BrowserBindingProof{}, false
	}
	intentRef, ok := operatorCodeIndexIntentPath(r)
	if !ok {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return operatorCodeRequestIdentity{}, "", BrowserBindingProof{}, false
	}
	tabBindingID, ok := operatorCodeSingleHeader(r, "X-Engram-Tab-Binding-ID")
	if !ok || !operatorCodeUUID(tabBindingID) {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return operatorCodeRequestIdentity{}, "", BrowserBindingProof{}, false
	}
	documentProof, ok := operatorCodeSingleHeader(r, "X-Engram-Document-Proof")
	if !ok {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return operatorCodeRequestIdentity{}, "", BrowserBindingProof{}, false
	}
	proof := BrowserBindingProof{TabBindingID: tabBindingID, DocumentProof: documentProof}
	identity, ok := adapter.decodeIdentity(w, r, endpoint)
	if !ok {
		return operatorCodeRequestIdentity{}, "", BrowserBindingProof{}, false
	}
	identity.digest = operatorCodeIndexIntentDigest(endpoint, proof, intentRef, "", "")
	return identity, intentRef, proof, true
}

func operatorCodeEmptyBody(r *http.Request) bool {
	if r.Body == nil {
		return true
	}
	if r.ContentLength > 0 {
		return false
	}
	value, err := io.ReadAll(io.LimitReader(r.Body, 1))
	return err == nil && len(value) == 0
}

func operatorCodeSingleHeader(r *http.Request, name string) (string, bool) {
	values := r.Header.Values(name)
	if len(values) != 1 || !operatorCodeText(values[0]) {
		return "", false
	}
	return values[0], true
}

func operatorCodeIndexIntentPath(r *http.Request) (string, bool) {
	intentRef := chi.URLParam(r, "intent_ref")
	return intentRef, operatorCodeUUID(intentRef)
}

type operatorCodeIndexIntentDigestBinding struct {
	TabBindingID  string              `json:"tab_binding_id"`
	DocumentProof string              `json:"document_proof"`
	IntentRef     string              `json:"intent_ref,omitempty"`
	RequestRef    string              `json:"request_ref,omitempty"`
	Kind          uci.IndexIntentKind `json:"kind,omitempty"`
}

func operatorCodeIndexIntentDigest(endpoint string, proof BrowserBindingProof, intentRef, requestRef string, kind uci.IndexIntentKind) string {
	encoded, _ := json.Marshal(operatorCodeIndexIntentDigestBinding{
		TabBindingID:  proof.TabBindingID,
		DocumentProof: proof.DocumentProof,
		IntentRef:     intentRef,
		RequestRef:    requestRef,
		Kind:          kind,
	})
	return operatorCodeRequestDigest(endpoint, encoded)
}

func operatorCodeRequestDigest(endpoint string, binding []byte) string {
	digest := sha256.Sum256(append(append([]byte(endpoint+"\n"), binding...), '\n'))
	return "sha256:" + hex.EncodeToString(digest[:])
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
	grant, found, err := adapter.grants.Active(ctx, identity.identity, ref.SourceID, ref.CheckoutID)
	if err != nil {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailureExposureUnavailable
	}
	if !found || grant.SubjectUserID != subject.UserID || grant.SourceID != ref.SourceID || grant.CheckoutID != ref.CheckoutID || !operatorCodeText(grant.AuthRealm) || grant.IssuedAt.IsZero() {
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
	return operatorCodeAuthorizedRequest{
		operatorCodeRequestIdentity: identity,
		caller:                      caller,
		proof:                       proof,
		authorized:                  authorized,
		grant:                       grant,
		authRealm:                   grant.AuthRealm,
	}, uci.ReleaseFailureNone
}

type operatorCodeNoViewIndexIntentRequest struct {
	operatorCodeRequestIdentity
	caller    operatorCodeVerifiedCaller
	proof     BrowserBindingProof
	scope     uci.IndexScope
	profileID string
	authRealm string
}

func (adapter *OperatorCodeHTTPAdapter) authorizeNoViewIndexIntent(ctx context.Context, identity operatorCodeRequestIdentity, proof BrowserBindingProof, requested operatorCodeIndexIntentTargetRequest, profileID string, initial bool) (operatorCodeNoViewIndexIntentRequest, uci.ReleaseFailureCode) {
	if adapter == nil || adapter.contexts == nil {
		return operatorCodeNoViewIndexIntentRequest{}, uci.ReleaseFailureExposureUnavailable
	}
	var advertised uci.IndexBinding
	if initial {
		if adapter.indexTargets == nil {
			return operatorCodeNoViewIndexIntentRequest{}, uci.ReleaseFailureContextMismatch
		}
		var found bool
		advertised, found = adapter.indexTargets.Resolve(requested.SourceID, requested.CheckoutID)
		if !found {
			return operatorCodeNoViewIndexIntentRequest{}, uci.ReleaseFailurePermissionDenied
		}
		if advertised.Context != nil || advertised.ProfileID == "" {
			return operatorCodeNoViewIndexIntentRequest{}, uci.ReleaseFailureContextMismatch
		}
		profileID = advertised.ProfileID
	}
	if !operatorCodeUUID(profileID) {
		return operatorCodeNoViewIndexIntentRequest{}, uci.ReleaseFailureContextMismatch
	}
	subject, ok := identity.identity.SessionBrowserSubject()
	if !ok {
		return operatorCodeNoViewIndexIntentRequest{}, uci.ReleaseFailurePermissionDenied
	}
	in := gormdb.BrowserCodeIndexIntentTarget{
		Caller:              gormdb.BrowserTabBindingCaller{SubjectUserID: subject.UserID, SessionID: identity.sessionID},
		TabBindingID:        proof.TabBindingID,
		DocumentProofDigest: browserBindingDigest(proof.DocumentProof),
		SourceID:            requested.SourceID,
		CheckoutID:          requested.CheckoutID,
		ProfileID:           profileID,
	}
	var (
		binding gormdb.BrowserCodeIndexIntentBinding
		err     error
	)
	if initial {
		binding, err = adapter.contexts.AuthorizeInitialIndexIntent(ctx, in)
	} else {
		binding, err = adapter.contexts.ReauthorizeIndexIntent(ctx, in)
	}
	if err != nil {
		if errors.Is(err, gormdb.ErrBrowserCodeContextDenied) || errors.Is(err, gormdb.ErrBrowserTabBindingDenied) {
			return operatorCodeNoViewIndexIntentRequest{}, uci.ReleaseFailurePermissionDenied
		}
		return operatorCodeNoViewIndexIntentRequest{}, uci.ReleaseFailureExposureUnavailable
	}
	if binding.Scope.SourceID != requested.SourceID || binding.Scope.CheckoutID != requested.CheckoutID || binding.ProfileID != profileID || !operatorCodeUUID(binding.Scope.IncarnationID) || !operatorCodeText(binding.AuthRealm) || (initial && binding.Scope != advertised.Scope) {
		return operatorCodeNoViewIndexIntentRequest{}, uci.ReleaseFailureContextMismatch
	}
	return operatorCodeNoViewIndexIntentRequest{
		operatorCodeRequestIdentity: identity,
		caller: operatorCodeVerifiedCaller{
			Subject: subject, SessionID: identity.sessionID, BindingID: proof.TabBindingID,
		},
		proof: proof, scope: binding.Scope, profileID: binding.ProfileID, authRealm: binding.AuthRealm,
	}, uci.ReleaseFailureNone
}

func (adapter *OperatorCodeHTTPAdapter) authorizeNoViewIndexIntentResult(ctx context.Context, identity operatorCodeRequestIdentity, proof BrowserBindingProof, target operatorCodeNoViewIndexIntentRequest, intent uci.IndexIntent) (operatorCodeAuthorizedRequest, uci.ReleaseFailureCode) {
	if adapter == nil || adapter.authority == nil || !operatorCodeNoViewIndexIntentMatches(intent, target) || intent.ResultView == nil {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailureContextMismatch
	}
	if _, failure := adapter.authorizeNoViewIndexIntent(ctx, identity, proof, operatorCodeIndexIntentTargetRequest{SourceID: target.scope.SourceID, CheckoutID: target.scope.CheckoutID}, target.profileID, false); failure != uci.ReleaseFailureNone {
		return operatorCodeAuthorizedRequest{}, failure
	}
	caller := target.caller
	caller.Context = intent.ResultView.Clone()
	authorized, err := adapter.authority.AuthorizeOperatorCode(ctx, caller)
	if err != nil {
		return operatorCodeAuthorizedRequest{}, operatorCodeContextFailure(err)
	}
	if !operatorCodeRefsEqual(authorized.Ref(), *intent.ResultView) {
		return operatorCodeAuthorizedRequest{}, uci.ReleaseFailureContextMismatch
	}
	return operatorCodeAuthorizedRequest{
		operatorCodeRequestIdentity: identity,
		caller:                      caller,
		proof:                       proof,
		authorized:                  authorized,
		authRealm:                   target.authRealm,
	}, uci.ReleaseFailureNone
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
	w.Header().Set(operatorCodeContentTypeHeader, operatorCodeJSONContentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(released)
}

func (adapter *OperatorCodeHTTPAdapter) writeReleasedGraph(
	w http.ResponseWriter,
	ctx context.Context,
	identity operatorCodeRequestIdentity,
	caller operatorCodeAuthorizedRequest,
	response uci.QueryResponse,
	matches func(uci.QueryResponse, uci.AuthorizedContext) bool,
) {
	released := uci.ReleaseQueryResponse(ctx, adapter.releaseRequest(identity, caller, uci.ReleaseCategoryCodeGraph, &response, matches), adapter.releaseGate(identity, caller.Proof()))
	if err := released.Validate(); err != nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return
	}
	if released.Status == uci.QueryStatusForbidden || released.Status == uci.QueryStatusContextRequired || released.Status == uci.QueryStatusUnavailable {
		operatorCodeWriteQueryResponse(w, released)
		return
	}
	reauthorized, failure := adapter.authorize(ctx, identity, caller.Proof())
	if failure != uci.ReleaseFailureNone {
		operatorCodeWriteFailure(w, failure)
		return
	}
	navigation := adapter.graphNavigation(ctx, reauthorized.authorized, released.Graph)
	operatorCodeWriteGraphResponse(w, released, navigation)
}

func (adapter *OperatorCodeHTTPAdapter) graphNavigation(ctx context.Context, authorized uci.AuthorizedContext, graph *uci.QueryGraph) operatorCodeGraphNavigation {
	navigation := operatorCodeGraphNavigation{Nodes: []operatorCodeGraphNavigationRef{}, Edges: []operatorCodeGraphEdgeNavigation{}}
	if graph == nil {
		return navigation
	}
	contextRef := operatorCodeContextDTO(authorized.Ref())
	nodes := make(map[uci.QueryEntityRef]operatorCodeGraphNavigationRef, len(graph.Nodes))
	for _, node := range graph.Nodes {
		entry := operatorCodeGraphNavigationRef{Entity: node, ContextRef: contextRef, SourceState: "unavailable"}
		if adapter != nil && adapter.graphSources != nil {
			descriptor, available, err := adapter.graphSources.DescribeGraphSource(ctx, authorized, node)
			if err == nil && available {
				entry.SourceState = "available"
				entry.SourceRead = &operatorCodeSourceReadDescriptor{
					EntityKey:     descriptor.Entity.EntityKey,
					Span:          descriptor.Span,
					ContentDigest: descriptor.ContentDigest,
				}
			}
		}
		navigation.Nodes = append(navigation.Nodes, entry)
		nodes[node] = entry
	}
	for _, edge := range graph.Edges {
		from, foundFrom := nodes[edge.From]
		to, foundTo := nodes[edge.To]
		if !foundFrom || !foundTo {
			continue
		}
		navigation.Edges = append(navigation.Edges, operatorCodeGraphEdgeNavigation{From: from, To: to, Relation: edge.Relation, EvidenceKind: edge.EvidenceKind})
	}
	return navigation
}

func operatorCodeWriteQueryResponse(w http.ResponseWriter, response uci.QueryResponse) {
	status := http.StatusOK
	switch response.Status {
	case uci.QueryStatusForbidden:
		status = http.StatusForbidden
	case uci.QueryStatusContextRequired:
		status = http.StatusConflict
	case uci.QueryStatusUnavailable:
		status = http.StatusServiceUnavailable
	}
	w.Header().Set(operatorCodeContentTypeHeader, operatorCodeJSONContentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func operatorCodeWriteGraphResponse(w http.ResponseWriter, response uci.QueryResponse, navigation operatorCodeGraphNavigation) {
	w.Header().Set(operatorCodeContentTypeHeader, operatorCodeJSONContentType)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(struct {
		uci.QueryResponse
		Navigation operatorCodeGraphNavigation `json:"navigation"`
	}{QueryResponse: response, Navigation: navigation})
}

func (adapter *OperatorCodeHTTPAdapter) releaseRequest(
	identity operatorCodeRequestIdentity,
	caller operatorCodeAuthorizedRequest,
	category uci.ReleaseCategory,
	response *uci.QueryResponse,
	matches func(uci.QueryResponse, uci.AuthorizedContext) bool,
) uci.ReleaseRequest {
	return uci.ReleaseRequest{
		AuthRealm:            caller.authRealm,
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

func (adapter *OperatorCodeHTTPAdapter) releaseNoViewIndexIntentResult(ctx context.Context, identity operatorCodeRequestIdentity, proof BrowserBindingProof, target operatorCodeNoViewIndexIntentRequest, intent uci.IndexIntent) uci.ReleaseDecision {
	caller, failure := adapter.authorizeNoViewIndexIntentResult(ctx, identity, proof, target, intent)
	if failure != uci.ReleaseFailureNone {
		return uci.ReleaseDecision{Failure: failure}
	}
	return uci.Release(ctx, adapter.releaseRequest(identity, caller, uci.ReleaseCategoryCodeIndexResult, nil, nil), operatorCodeNoViewIndexIntentReleaseGate{
		adapter: adapter, identity: identity, proof: proof, target: target, intent: intent.Clone(),
	})
}

type operatorCodeNoViewIndexIntentReleaseGate struct {
	adapter  *OperatorCodeHTTPAdapter
	identity operatorCodeRequestIdentity
	proof    BrowserBindingProof
	target   operatorCodeNoViewIndexIntentRequest
	intent   uci.IndexIntent
}

func (gate operatorCodeNoViewIndexIntentReleaseGate) Reauthorize(ctx context.Context) (uci.AuthorizedContext, uci.ReleaseFailureCode) {
	caller, failure := gate.adapter.authorizeNoViewIndexIntentResult(ctx, gate.identity, gate.proof, gate.target, gate.intent)
	if failure != uci.ReleaseFailureNone {
		return uci.AuthorizedContext{}, failure
	}
	return caller.authorized, uci.ReleaseFailureNone
}

func (gate operatorCodeNoViewIndexIntentReleaseGate) AppendExposure(ctx context.Context, authorized uci.AuthorizedContext, input uci.ExposureInput) (uci.QueryExposure, uci.ReleaseFailureCode) {
	return (operatorCodeReleaseGate{adapter: gate.adapter, identity: gate.identity, proof: gate.proof}).AppendExposure(ctx, authorized, input)
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

func operatorCodeIndexIntentShapeValid(intent uci.IndexIntent) bool {
	return intent.Validate() == nil && !intent.CreatedAt.IsZero() && !intent.UpdatedAt.IsZero() && !intent.UpdatedAt.Before(intent.CreatedAt)
}

func operatorCodeIndexIntentMatches(intent uci.IndexIntent, authorized uci.AuthorizedContext) bool {
	ref := authorized.Ref()
	if ref.SpaceID != nil || intent.Scope.SourceID != ref.SourceID || intent.Scope.CheckoutID != ref.CheckoutID || intent.ProfileID != ref.AnalysisProfileID || intent.PreviousView == nil || !operatorCodeRefsEqual(*intent.PreviousView, ref) {
		return false
	}
	if intent.State != uci.IndexIntentCompleted {
		return true
	}
	result := intent.ResultView
	return result != nil && result.SpaceID == nil && result.SourceID == ref.SourceID && result.CheckoutID == ref.CheckoutID && result.AnalysisProfileID == ref.AnalysisProfileID
}

func operatorCodeNoViewIndexIntentMatches(intent uci.IndexIntent, target operatorCodeNoViewIndexIntentRequest) bool {
	if intent.PreviousView != nil || intent.Scope != target.scope || intent.ProfileID != target.profileID {
		return false
	}
	if intent.State != uci.IndexIntentCompleted {
		return true
	}
	result := intent.ResultView
	return result != nil && result.SpaceID == nil && result.SourceID == target.scope.SourceID && result.CheckoutID == target.scope.CheckoutID && result.AnalysisProfileID == target.profileID
}

func operatorCodeIndexIntentFailure(err error) uci.ReleaseFailureCode {
	switch {
	case errors.Is(err, uci.ErrIndexIntentRetryUnauthorized):
		return uci.ReleaseFailurePermissionDenied
	case errors.Is(err, uci.ErrIndexIntentBindingMismatch), errors.Is(err, uci.ErrIndexIntentNotFound), errors.Is(err, uci.ErrIndexIntentInvalidTransition), errors.Is(err, uci.ErrIndexIntentResultInvalid), errors.Is(err, uci.ErrIndexIntentResultUnreadable):
		return uci.ReleaseFailureContextMismatch
	default:
		return operatorCodeContextFailure(err)
	}
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

type operatorCodeContextResponseRef struct {
	SourceID          string `json:"source_id"`
	CheckoutID        string `json:"checkout_id"`
	ViewID            string `json:"view_id"`
	AnalysisProfileID string `json:"analysis_profile_id"`
	Generation        int64  `json:"generation"`
}

func operatorCodeContextDTO(ref uci.ContextRef) operatorCodeContextResponseRef {
	return operatorCodeContextResponseRef{
		SourceID:          ref.SourceID,
		CheckoutID:        ref.CheckoutID,
		ViewID:            ref.ViewID,
		AnalysisProfileID: ref.AnalysisProfileID,
		Generation:        ref.Generation,
	}
}

type operatorCodeCatalogLabel struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type operatorCodeCatalogView struct {
	ContextRef operatorCodeContextResponseRef `json:"context_ref"`
	Label      string                         `json:"label"`
}

type operatorCodeCatalogEntry struct {
	Source               operatorCodeCatalogLabel `json:"source"`
	Checkout             operatorCodeCatalogLabel `json:"checkout"`
	View                 *operatorCodeCatalogView `json:"view"`
	IndexIntentAvailable bool                     `json:"index_intent_available"`
	AnalysisProfileID    *string                  `json:"analysis_profile_id,omitempty"`
}

type operatorCodeContextsResponse struct {
	Contexts []operatorCodeCatalogEntry `json:"contexts"`
}

func operatorCodeCatalogEntries(entries []gormdb.BrowserCodeContextCatalogEntry, targets operatorCodeIndexTargetResolver) []operatorCodeCatalogEntry {
	result := make([]operatorCodeCatalogEntry, 0, len(entries))
	for _, entry := range entries {
		available := false
		var profileID *string
		if entry.Context == nil && entry.IndexIntentAvailable && targets != nil {
			binding, found := targets.Resolve(entry.SourceID, entry.CheckoutID)
			if found && binding.Context == nil {
				available = true
				profile := binding.ProfileID
				profileID = &profile
			}
		}
		item := operatorCodeCatalogEntry{
			Source:               operatorCodeCatalogLabel{ID: entry.SourceID, Label: entry.SourceLabel},
			Checkout:             operatorCodeCatalogLabel{ID: entry.CheckoutID, Label: entry.CheckoutLabel},
			IndexIntentAvailable: available,
			AnalysisProfileID:    profileID,
		}
		if entry.Context != nil {
			item.View = &operatorCodeCatalogView{ContextRef: operatorCodeContextDTO(*entry.Context), Label: entry.ViewLabel}
		}
		result = append(result, item)
	}
	return result
}

type operatorCodeSourceReadDescriptor struct {
	EntityKey     string                 `json:"entity_key"`
	Span          uci.QuerySpan          `json:"span"`
	ContentDigest uci.QueryContentDigest `json:"content_digest"`
}

type operatorCodeGraphNavigationRef struct {
	Entity      uci.QueryEntityRef                `json:"entity"`
	ContextRef  operatorCodeContextResponseRef    `json:"context_ref"`
	SourceState string                            `json:"source_state"`
	SourceRead  *operatorCodeSourceReadDescriptor `json:"source_read,omitempty"`
}

type operatorCodeGraphEdgeNavigation struct {
	From         operatorCodeGraphNavigationRef `json:"from"`
	To           operatorCodeGraphNavigationRef `json:"to"`
	Relation     uci.IndexRelation              `json:"relation"`
	EvidenceKind uci.QueryEvidenceKind          `json:"evidence_kind"`
}

type operatorCodeGraphNavigation struct {
	Nodes []operatorCodeGraphNavigationRef  `json:"nodes"`
	Edges []operatorCodeGraphEdgeNavigation `json:"edges"`
}

type operatorCodeStatusResponse struct {
	TotalChunks    int64               `json:"total_chunks"`
	EmbeddedChunks int64               `json:"embedded_chunks"`
	Embedding      uci.EmbeddingStatus `json:"embedding"`
	Freshness      *uci.QueryFreshness `json:"freshness,omitempty"`
}

type operatorCodeIndexIntentResultResponse struct {
	ViewRef    string `json:"view_ref"`
	Generation int64  `json:"generation"`
}

type operatorCodeIndexIntentResponse struct {
	IntentRef string                                 `json:"intent_ref"`
	State     uci.IndexIntentState                   `json:"state"`
	Attempt   int                                    `json:"attempt"`
	Retryable bool                                   `json:"retryable"`
	CreatedAt time.Time                              `json:"created_at"`
	UpdatedAt time.Time                              `json:"updated_at"`
	Result    *operatorCodeIndexIntentResultResponse `json:"result,omitempty"`
}

func operatorCodeIndexIntentResponseFor(intent uci.IndexIntent) operatorCodeIndexIntentResponse {
	return operatorCodeIndexIntentResponse{
		IntentRef: intent.ID,
		State:     intent.State,
		Attempt:   intent.Attempt,
		Retryable: intent.State == uci.IndexIntentUnavailable,
		CreatedAt: intent.CreatedAt.UTC(),
		UpdatedAt: intent.UpdatedAt.UTC(),
	}
}

func operatorCodeWriteIndexIntentAcknowledgement(w http.ResponseWriter, intent uci.IndexIntent) {
	response := operatorCodeIndexIntentResponseFor(intent)
	if response.State != uci.IndexIntentSubmitted && response.State != uci.IndexIntentQueued {
		response.State = uci.IndexIntentSubmitted
	}
	response.Retryable = false
	w.Header().Set(operatorCodeContentTypeHeader, operatorCodeJSONContentType)
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(response)
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
