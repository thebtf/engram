package uci

import (
	"context"
	"strings"
	"testing"
)

type releaseTestBrowserSubject struct {
	Principal string
	valid     bool
}

func (subject releaseTestBrowserSubject) Valid() bool {
	return subject.valid
}

type releaseGateFake struct {
	authorized         AuthorizedContext
	reauthorizeFailure ReleaseFailureCode
	appendFailure      ReleaseFailureCode
	reauthorizeCalls   int
	appendCalls        int
	appendInput        ExposureInput
	receipt            QueryExposure
}

func (gate *releaseGateFake) Reauthorize(context.Context) (AuthorizedContext, ReleaseFailureCode) {
	gate.reauthorizeCalls++
	if gate.reauthorizeFailure != ReleaseFailureNone {
		return AuthorizedContext{}, gate.reauthorizeFailure
	}
	return gate.authorized, ReleaseFailureNone
}

func (gate *releaseGateFake) AppendExposure(_ context.Context, _ AuthorizedContext, input ExposureInput) (QueryExposure, ReleaseFailureCode) {
	gate.appendCalls++
	gate.appendInput = input
	if gate.appendFailure != ReleaseFailureNone {
		return QueryExposure{}, gate.appendFailure
	}
	if gate.receipt.ExposureRef == "" {
		gate.receipt = QueryExposure{ExposureRef: NewExposureRef(), CompletionState: QueryCompletionUnknown}
	}
	return gate.receipt, ReleaseFailureNone
}

func TestReleaseMapsBrowserCallerAndNonContentCategories(t *testing.T) {
	contextRef := exposureTestContextRef()
	authorized := newAuthorizedContext(contextRef)
	subject := releaseTestBrowserSubject{Principal: "browser-user/71", valid: true}
	caller := ReleaseCaller{Browser: &BrowserReleaseCaller{
		Subject:         subject,
		SessionID:       "browser-session-71",
		DocumentBinding: "50000000-0000-4000-8000-000000000071",
	}}
	for _, testCase := range []struct {
		name            string
		category        ReleaseCategory
		operation       ExposureOperation
		recordsExposure bool
	}{
		{name: "search", category: ReleaseCategoryCodeSearch, operation: ExposureOperationCodeSearch, recordsExposure: true},
		{name: "graph", category: ReleaseCategoryCodeGraph, operation: ExposureOperationCodeGraph, recordsExposure: true},
		{name: "read", category: ReleaseCategoryVersionedRead, operation: ExposureOperationVersionedRead, recordsExposure: true},
		{name: "status", category: ReleaseCategoryCodeStatus},
		{name: "index result", category: ReleaseCategoryCodeIndexResult},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			gate := &releaseGateFake{authorized: authorized}
			request := releaseTestRequest(testCase.category, caller, nil)
			if testCase.recordsExposure {
				response := exposureTestResponse(QueryStatusOK)
				request.Response = &response
			}
			decision := Release(context.Background(), request, gate)
			releaseTestRequireBrowserDecision(t, gate, decision, caller, subject, testCase.operation, testCase.recordsExposure)
		})
	}
}

func releaseTestRequireBrowserDecision(t *testing.T, gate *releaseGateFake, decision ReleaseDecision, caller ReleaseCaller, subject releaseTestBrowserSubject, operation ExposureOperation, recordsExposure bool) {
	t.Helper()
	if !decision.Released() {
		t.Fatalf("Release() failure = %q", decision.Failure)
	}
	if gate.reauthorizeCalls != 1 {
		t.Fatalf("reauthorize calls = %d, want 1", gate.reauthorizeCalls)
	}
	if !recordsExposure {
		if gate.appendCalls != 0 || decision.Exposure != nil {
			t.Fatalf("non-content category append/receipt = %d/%#v, want 0/nil", gate.appendCalls, decision.Exposure)
		}
		return
	}
	if gate.appendCalls != 1 || decision.Exposure == nil {
		t.Fatalf("append calls/receipt = %d/%#v, want 1/non-nil", gate.appendCalls, decision.Exposure)
	}
	releaseTestRequireBrowserExposure(t, gate, caller, subject, operation)
}

func releaseTestRequireBrowserExposure(t *testing.T, gate *releaseGateFake, caller ReleaseCaller, subject releaseTestBrowserSubject, operation ExposureOperation) {
	t.Helper()
	if gate.appendInput.ClientKeycard != "" || gate.appendInput.BrowserSubject == nil || gate.appendInput.BrowserDocumentBinding != caller.Browser.DocumentBinding || gate.appendInput.Operation != operation {
		t.Fatalf("browser exposure input = %#v", gate.appendInput)
	}
	record, err := deriveExposureRecord(gate.authorized, gate.appendInput)
	if err != nil {
		t.Fatalf("deriveExposureRecord() error = %v", err)
	}
	wantClientRef, err := opaqueExposureHash("browser_subject", subject)
	if err != nil {
		t.Fatalf("opaque browser subject hash error = %v", err)
	}
	keycardRef, err := opaqueExposureHash("client_keycard", subject.Principal)
	if err != nil {
		t.Fatalf("opaque keycard hash error = %v", err)
	}
	if record.ClientRef != wantClientRef || record.ClientRef == keycardRef {
		t.Fatalf("browser client reference = %q, want browser-subject hash and never keycard alias", record.ClientRef)
	}
}

func TestReleaseMapsMCPKeycardSeparately(t *testing.T) {
	contextRef := exposureTestContextRef()
	gate := &releaseGateFake{authorized: newAuthorizedContext(contextRef)}
	response := exposureTestResponse(QueryStatusOK)
	request := releaseTestRequest(ReleaseCategoryCodeSearch, ReleaseCaller{MCP: &MCPReleaseCaller{
		Keycard:   "mcp-keycard",
		SessionID: "mcp-session",
	}}, &response)

	decision := Release(context.Background(), request, gate)
	if !decision.Released() || gate.appendInput.ClientKeycard != "mcp-keycard" || gate.appendInput.BrowserSubject != nil {
		t.Fatalf("MCP release mapping = %#v / %#v", decision, gate.appendInput)
	}
}

func TestReleaseQueryResponseSuppressesFailedContextualReleases(t *testing.T) {
	contextRef := exposureTestContextRef()
	caller := ReleaseCaller{Browser: &BrowserReleaseCaller{
		Subject:         releaseTestBrowserSubject{Principal: "browser-user/72", valid: true},
		SessionID:       "browser-session-72",
		DocumentBinding: "50000000-0000-4000-8000-000000000072",
	}}
	for _, testCase := range []struct {
		name               string
		response           QueryResponse
		reauthorizeFailure ReleaseFailureCode
		appendFailure      ReleaseFailureCode
		wantStatus         QueryResponseStatus
		wantCode           QueryErrorCode
		wantAuthorize      int
		wantAppend         int
	}{
		{
			name:               "revocation between application and release",
			response:           exposureTestResponse(QueryStatusOK),
			reauthorizeFailure: ReleaseFailurePermissionDenied,
			wantStatus:         QueryStatusForbidden,
			wantCode:           QueryErrorPermissionDenied,
			wantAuthorize:      1,
		},
		{
			name:               "caller mismatch",
			response:           exposureTestResponse(QueryStatusOK),
			reauthorizeFailure: ReleaseFailureContextMismatch,
			wantStatus:         QueryStatusContextRequired,
			wantCode:           QueryErrorContextMismatch,
			wantAuthorize:      1,
		},
		{
			name:          "recorder failure",
			response:      exposureTestResponse(QueryStatusOK),
			appendFailure: ReleaseFailureExposureUnavailable,
			wantStatus:    QueryStatusUnavailable,
			wantCode:      QueryErrorExposureUnavailable,
			wantAuthorize: 1,
			wantAppend:    1,
		},
		{
			name: "invalid application response",
			response: QueryResponse{
				Schema: QueryResponseSchema,
				Status: QueryStatusOK,
			},
			wantStatus: QueryStatusUnavailable,
			wantCode:   QueryErrorExposureUnavailable,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			gate := &releaseGateFake{authorized: newAuthorizedContext(contextRef), reauthorizeFailure: testCase.reauthorizeFailure, appendFailure: testCase.appendFailure}
			request := releaseTestRequest(ReleaseCategoryCodeSearch, caller, &testCase.response)
			response := ReleaseQueryResponse(context.Background(), request, gate)
			if err := response.Validate(); err != nil {
				t.Fatalf("closed release response did not validate: %v", err)
			}
			if response.Status != testCase.wantStatus || response.Error == nil || response.Error.Code != testCase.wantCode {
				t.Fatalf("closed release response = %#v, want %s/%s", response, testCase.wantStatus, testCase.wantCode)
			}
			requireReleaseSuppressed(t, response)
			if gate.reauthorizeCalls != testCase.wantAuthorize || gate.appendCalls != testCase.wantAppend {
				t.Fatalf("release calls = reauthorize:%d append:%d, want %d/%d", gate.reauthorizeCalls, gate.appendCalls, testCase.wantAuthorize, testCase.wantAppend)
			}
		})
	}
}

func TestReleaseQueryResponseSuppressesRelationEvidenceAfterRevocation(t *testing.T) {
	contextRef := exposureTestContextRef()
	source := QueryEntityRef{SourceID: contextRef.SourceID, ViewID: contextRef.ViewID, EntityKey: "fixture.Source"}
	target := QueryEntityRef{SourceID: contextRef.SourceID, ViewID: contextRef.ViewID, EntityKey: "fixture.Target"}
	referenceSiteID := "50000000-0000-4000-8000-000000000073"
	response := exposureTestResponse(QueryStatusOK)
	response.Graph = &QueryGraph{
		Nodes: []QueryEntityRef{source, target},
		Edges: []QueryGraphEdge{{
			From:         source,
			To:           target,
			Relation:     IndexRelation("calls"),
			EvidenceKind: QueryEvidenceResolved,
			EvidenceRefs: []QueryEntityRef{source},
			Evidence:     []QueryRelationEvidence{{Ref: source, Precision: QueryEvidencePrecisionReferenceSite, ReferenceSiteID: &referenceSiteID}},
		}},
		StopReason: QueryGraphComplete,
	}
	if err := response.ValidatePreExposure(); err != nil {
		t.Fatalf("pre-release relation evidence is invalid: %v", err)
	}

	gate := &releaseGateFake{authorized: newAuthorizedContext(contextRef), reauthorizeFailure: ReleaseFailurePermissionDenied}
	released := ReleaseQueryResponse(context.Background(), releaseTestRequest(ReleaseCategoryCodeGraph, ReleaseCaller{Browser: &BrowserReleaseCaller{
		Subject:         releaseTestBrowserSubject{Principal: "browser-user/73", valid: true},
		SessionID:       "browser-session-73",
		DocumentBinding: "50000000-0000-4000-8000-000000000073",
	}}, &response), gate)
	if err := released.Validate(); err != nil {
		t.Fatalf("revoked evidence response is invalid: %v", err)
	}
	if released.Status != QueryStatusForbidden || released.Error == nil || released.Error.Code != QueryErrorPermissionDenied {
		t.Fatalf("revoked evidence response = %#v", released)
	}
	requireReleaseSuppressed(t, released)
}

func TestReleaseQueryResponsePreservesNonContextualAdmissionRefusals(t *testing.T) {
	response := QueryResponse{Schema: QueryResponseSchema, Status: QueryStatusContextRequired, Error: &QueryError{Code: QueryErrorContextRequired}}
	gate := &releaseGateFake{authorized: newAuthorizedContext(exposureTestContextRef())}
	released := ReleaseQueryResponse(context.Background(), ReleaseRequest{Category: ReleaseCategoryCodeSearch, Response: &response}, gate)
	if released != response {
		t.Fatalf("non-contextual refusal changed: %#v", released)
	}
	if gate.reauthorizeCalls != 0 || gate.appendCalls != 0 {
		t.Fatalf("non-contextual refusal reached release gate: reauthorize:%d append:%d", gate.reauthorizeCalls, gate.appendCalls)
	}
}

func releaseTestRequest(category ReleaseCategory, caller ReleaseCaller, response *QueryResponse) ReleaseRequest {
	return ReleaseRequest{
		AuthRealm:            "client",
		Caller:               caller,
		RequestID:            "request-1",
		RequestBindingDigest: "sha256:" + strings.Repeat("a", 64),
		Category:             category,
		Response:             response,
	}
}

func requireReleaseSuppressed(t *testing.T, response QueryResponse) {
	t.Helper()
	if response.Exposure != nil || response.Contexts != nil || response.Freshness != nil || response.Retrieval != nil || response.Coverage != nil || response.Items != nil || response.Graph != nil || response.Truncated != nil || response.Warnings != nil || response.Continuation != nil {
		t.Fatalf("closed release leaked contextual fields: %#v", response)
	}
}
