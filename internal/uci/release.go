package uci

import (
	"context"
	"time"
)

// ReleaseCategory classifies the contextual surface being released. Only the
// categories with a persisted ExposureOperation append an exposure receipt.
// Status and index-result releases remain contextual but use the explicitly
// non-content evidence path until durable operation support is added.
type ReleaseCategory string

const (
	ReleaseCategoryCodeSearch      ReleaseCategory = "code_search"
	ReleaseCategoryCodeGraph       ReleaseCategory = "code_graph"
	ReleaseCategoryVersionedRead   ReleaseCategory = "versioned_read"
	ReleaseCategoryCodeStatus      ReleaseCategory = "code_status"
	ReleaseCategoryCodeIndexResult ReleaseCategory = "code_index_result"
)

// MCPReleaseCaller identifies an MCP caller with the existing keycard/session
// tuple. It is mutually exclusive with BrowserReleaseCaller.
type MCPReleaseCaller struct {
	Keycard   string
	SessionID string
}

// BrowserReleaseCaller identifies a real authenticated browser subject and one
// already-guarded current document. DocumentBinding is the opaque tab-binding
// ID returned only after a successful document-proof guard; it is not a
// keycard and never aliases one.
type BrowserReleaseCaller struct {
	Subject         BrowserSubject
	SessionID       string
	DocumentBinding string
}

// ReleaseCaller is a closed caller choice. Exactly one caller form is required.
type ReleaseCaller struct {
	MCP     *MCPReleaseCaller
	Browser *BrowserReleaseCaller
}

// ReleaseRequest is the transport-independent description of one contextual
// release. Response is required only for categories that append an exposure;
// status and index-result callers first obtain a successful Release decision,
// then serialize their separate non-content response DTO.
type ReleaseRequest struct {
	AuthRealm            string
	Caller               ReleaseCaller
	RequestID            string
	RequestBindingDigest string
	Category             ReleaseCategory
	Response             *QueryResponse
	RecordedAt           time.Time

	// ResponseMatches is an application-owned structural guard. It runs after
	// reauthorization and before an evidence append, so specialized query
	// shapes retain their existing exact-context validation.
	ResponseMatches func(QueryResponse, AuthorizedContext) bool
}

// ReleaseFailureCode is the complete closed outcome vocabulary returned by a
// transport-specific release gate. It is intentionally display-safe and maps
// directly to an empty UCI envelope where one is needed.
type ReleaseFailureCode string

const (
	ReleaseFailureNone                ReleaseFailureCode = ""
	ReleaseFailureContextRequired     ReleaseFailureCode = "CONTEXT_REQUIRED"
	ReleaseFailureContextMismatch     ReleaseFailureCode = "CONTEXT_MISMATCH"
	ReleaseFailurePermissionDenied    ReleaseFailureCode = "PERMISSION_DENIED"
	ReleaseFailureExposureUnavailable ReleaseFailureCode = "EXPOSURE_UNAVAILABLE"
	ReleaseFailureIdempotencyMismatch ReleaseFailureCode = "IDEMPOTENCY_MISMATCH"
)

// ReleaseGate is owned by the transport composition layer. Reauthorize proves
// the caller and its exact context immediately before release. AppendExposure
// is responsible for the transport's atomic current-epoch/binding check and
// its durable recorder append.
type ReleaseGate interface {
	Reauthorize(context.Context) (AuthorizedContext, ReleaseFailureCode)
	AppendExposure(context.Context, AuthorizedContext, ExposureInput) (QueryExposure, ReleaseFailureCode)
}

// ReleaseDecision contains no contextual result body. A composition owner may
// serialize a contextual payload only when Released is true.
type ReleaseDecision struct {
	Authorized AuthorizedContext
	Exposure   *QueryExposure
	Failure    ReleaseFailureCode
}

// Released reports whether the release gate reauthorized the caller and, where
// required, persisted an exposure receipt.
func (decision ReleaseDecision) Released() bool {
	return decision.Failure == ReleaseFailureNone
}

// Release maps a typed caller to an exposure input, reauthorizes it, and appends
// durable evidence before permitting a contextual response. Status and
// index-result categories deliberately have no persisted ExposureOperation;
// they still require successful reauthorization and return a body-free decision.
func Release(ctx context.Context, request ReleaseRequest, gate ReleaseGate) ReleaseDecision {
	operation, recordsExposure, validCategory := request.Category.exposureOperation()
	if !validCategory || gate == nil {
		return releaseFailureDecision(ReleaseFailureExposureUnavailable)
	}
	if !releaseRequestValid(request, recordsExposure) {
		return releaseFailureDecision(ReleaseFailureExposureUnavailable)
	}

	input, ok := request.exposureInput(operation, recordsExposure)
	if !ok {
		return releaseFailureDecision(ReleaseFailureExposureUnavailable)
	}
	authorized, failure := gate.Reauthorize(ctx)
	if failure != ReleaseFailureNone {
		return releaseFailureDecision(failure)
	}
	if !recordsExposure {
		return ReleaseDecision{Authorized: authorized}
	}
	if request.ResponseMatches != nil && !request.ResponseMatches(*request.Response, authorized) {
		return releaseFailureDecision(ReleaseFailureContextMismatch)
	}

	receipt, failure := gate.AppendExposure(ctx, authorized, input)
	if failure != ReleaseFailureNone || receipt.Validate() != nil {
		if failure == ReleaseFailureNone {
			failure = ReleaseFailureExposureUnavailable
		}
		return releaseFailureDecision(failure)
	}
	return ReleaseDecision{Authorized: authorized, Exposure: &receipt}
}

func releaseRequestValid(request ReleaseRequest, recordsExposure bool) bool {
	if !recordsExposure {
		return request.Response == nil
	}
	return request.Response != nil && request.Response.ValidatePreExposure() == nil
}

// ReleaseQueryResponse releases the UCI response form used by search, graph,
// and versioned-read. It preserves already-suppressed discovery/admission
// responses exactly; all failed contextual releases return an empty closed
// envelope with neither result body nor exposure receipt.
func ReleaseQueryResponse(ctx context.Context, request ReleaseRequest, gate ReleaseGate) QueryResponse {
	if request.Response == nil {
		return releaseFailureResponse(ReleaseFailureExposureUnavailable)
	}
	response := *request.Response
	switch response.Status {
	case QueryStatusContextRequired, QueryStatusForbidden:
		if response.Validate() != nil {
			return releaseFailureResponse(ReleaseFailureExposureUnavailable)
		}
		return response
	}

	decision := Release(ctx, request, gate)
	if !decision.Released() || decision.Exposure == nil {
		return releaseFailureResponse(decision.Failure)
	}
	response.Exposure = decision.Exposure
	if response.Validate() != nil {
		return releaseFailureResponse(ReleaseFailureExposureUnavailable)
	}
	return response
}

// ReleaseCategoryForExposureOperation maps the existing persisted operation
// vocabulary to a contextual release category. It deliberately has no mapping
// for non-content status/index-result categories.
func ReleaseCategoryForExposureOperation(operation ExposureOperation) (ReleaseCategory, bool) {
	switch operation {
	case ExposureOperationCodeSearch:
		return ReleaseCategoryCodeSearch, true
	case ExposureOperationCodeGraph:
		return ReleaseCategoryCodeGraph, true
	case ExposureOperationVersionedRead:
		return ReleaseCategoryVersionedRead, true
	default:
		return "", false
	}
}

func (category ReleaseCategory) exposureOperation() (ExposureOperation, bool, bool) {
	switch category {
	case ReleaseCategoryCodeSearch:
		return ExposureOperationCodeSearch, true, true
	case ReleaseCategoryCodeGraph:
		return ExposureOperationCodeGraph, true, true
	case ReleaseCategoryVersionedRead:
		return ExposureOperationVersionedRead, true, true
	case ReleaseCategoryCodeStatus, ReleaseCategoryCodeIndexResult:
		return "", false, true
	default:
		return "", false, false
	}
}

func (request ReleaseRequest) exposureInput(operation ExposureOperation, recordsExposure bool) (ExposureInput, bool) {
	if !validExposureText(request.AuthRealm) || !validExposureText(request.RequestID) || !validExposureDigest(request.RequestBindingDigest) {
		return ExposureInput{}, false
	}
	caller := request.Caller
	if (caller.MCP == nil) == (caller.Browser == nil) {
		return ExposureInput{}, false
	}

	input := ExposureInput{
		AuthRealm:            request.AuthRealm,
		RequestID:            request.RequestID,
		RequestBindingDigest: request.RequestBindingDigest,
		RecordedAt:           request.RecordedAt,
	}
	if caller.MCP != nil {
		if !validExposureText(caller.MCP.Keycard) || !validExposureText(caller.MCP.SessionID) {
			return ExposureInput{}, false
		}
		input.ClientKeycard = caller.MCP.Keycard
		input.ClientSession = caller.MCP.SessionID
	} else {
		browser := caller.Browser
		if browser.Subject == nil || !browser.Subject.Valid() || !validExposureText(browser.SessionID) || !validExposureUUID(browser.DocumentBinding) {
			return ExposureInput{}, false
		}
		input.BrowserSubject = browser.Subject
		input.BrowserDocumentBinding = browser.DocumentBinding
		input.ClientSession = browser.SessionID
	}
	if !recordsExposure {
		return input, true
	}
	input.Operation = operation
	input.Response = *request.Response
	return input, true
}

func releaseFailureDecision(failure ReleaseFailureCode) ReleaseDecision {
	if failure == ReleaseFailureNone {
		failure = ReleaseFailureExposureUnavailable
	}
	return ReleaseDecision{Failure: failure}
}

func releaseFailureResponse(failure ReleaseFailureCode) QueryResponse {
	switch failure {
	case ReleaseFailureContextRequired:
		return QueryResponse{Schema: QueryResponseSchema, Status: QueryStatusContextRequired, Error: &QueryError{Code: QueryErrorContextRequired}}
	case ReleaseFailurePermissionDenied:
		return QueryResponse{Schema: QueryResponseSchema, Status: QueryStatusForbidden, Error: &QueryError{Code: QueryErrorPermissionDenied}}
	case ReleaseFailureContextMismatch:
		return QueryResponse{Schema: QueryResponseSchema, Status: QueryStatusContextRequired, Error: &QueryError{Code: QueryErrorContextMismatch}}
	case ReleaseFailureIdempotencyMismatch:
		return QueryResponse{Schema: QueryResponseSchema, Status: QueryStatusUnavailable, Error: &QueryError{Code: QueryErrorIdempotencyMismatch}}
	default:
		return QueryResponse{Schema: QueryResponseSchema, Status: QueryStatusUnavailable, Error: &QueryError{Code: QueryErrorExposureUnavailable}}
	}
}
