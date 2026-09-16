package mcp

import (
	"context"
	"errors"

	"github.com/thebtf/engram/internal/uci"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UCIContextHandlePort adapts the one bounded MCP-owned handle registry to the
// private UCI gRPC transport. It stores closed selectors, never an authorized
// IndexBinding, and reloads/re-authorizes every operation.
type UCIContextHandlePort struct {
	server     *Server
	catalog    uci.IndexBindingCatalog
	authorizer uci.ContextAuthorizer
}

// NewUCIContextHandlePort creates the shared MCP/gRPC handle bridge.
func NewUCIContextHandlePort(server *Server, catalog uci.IndexBindingCatalog, authorizer uci.ContextAuthorizer) (*UCIContextHandlePort, error) {
	if server == nil || catalog == nil || authorizer == nil {
		return nil, errors.New("UCI context handle port is not configured")
	}
	return &UCIContextHandlePort{server: server, catalog: catalog, authorizer: authorizer}, nil
}

// AuthorizeCodeContextHandle reloads and reauthorizes exactly one client-owned
// opaque handle. A View-pinned selector remains pinned; a checkout selector can
// observe only the current published View of its fixed scope/profile.
func (port *UCIContextHandlePort) AuthorizeCodeContextHandle(ctx context.Context, clientSessionID, handle string) (uci.IndexBinding, error) {
	caller, err := port.caller(ctx, clientSessionID)
	if err != nil || !validCodebaseContextHandle(handle) {
		return uci.IndexBinding{}, codebaseContextHandleClosedError(uci.ContextMismatch)
	}
	epoch, selector, found := port.server.codebaseContextHandleSelector(clientSessionID, handle)
	if !found {
		return uci.IndexBinding{}, codebaseContextHandleClosedError(uci.ContextMismatch)
	}
	binding, err := port.reauthorize(ctx, caller, selector)
	if err != nil {
		return uci.IndexBinding{}, err
	}
	if !port.server.codebaseContextHandleCurrent(clientSessionID, handle, epoch, selector, binding) {
		return uci.IndexBinding{}, codebaseContextHandleClosedError(uci.ContextMismatch)
	}
	return binding.Clone(), nil
}

// IssueCodeContextHandle canonicalizes the supplied binding before registering
// its closed selector. The supplied value is never treated as a cached grant.
func (port *UCIContextHandlePort) IssueCodeContextHandle(ctx context.Context, clientSessionID string, requested uci.IndexBinding) (string, error) {
	caller, err := port.caller(ctx, clientSessionID)
	if err != nil {
		return "", codebaseContextHandleClosedError(uci.ContextMismatch)
	}
	selector, err := codebaseContextSelectorForBinding(requested)
	if err != nil {
		return "", codebaseContextHandleClosedError(uci.ContextMismatch)
	}
	binding, err := port.reauthorize(ctx, caller, selector)
	if err != nil {
		return "", err
	}
	if !codebaseContextBindingsEqual(requested, binding) {
		return "", codebaseContextHandleClosedError(uci.ContextMismatch)
	}
	handle, ok := port.server.codebaseContextHandleForSelector(clientSessionID, selector, &binding, port.server.codebaseContextRegistryEpoch())
	if !ok {
		return "", codebaseContextHandleClosedError(uci.ContextMismatch)
	}
	return handle, nil
}

// AuthorizeCodeIndexScope requires a live client admission from the shared
// handle registry, then reauthorizes a canonical checkout selector. The scope
// establishes admission only; it never selects a parent View.
func (port *UCIContextHandlePort) AuthorizeCodeIndexScope(ctx context.Context, clientSessionID string, scope uci.IndexScope, profileID string) (uci.IndexBinding, error) {
	caller, err := port.caller(ctx, clientSessionID)
	if err != nil {
		return uci.IndexBinding{}, codebaseContextHandleClosedError(uci.ContextMismatch)
	}
	selector, err := uci.CheckoutIndexBindingSelector(uci.RegisteredCheckoutSelector{Scope: scope, ProfileID: profileID})
	if err != nil {
		return uci.IndexBinding{}, codebaseContextHandleClosedError(uci.ContextMismatch)
	}
	key := codebaseContextScopeKeyForBinding(uci.IndexBinding{Scope: scope, ProfileID: profileID, LocalRootID: "admission", WorkstationID: "admission"})
	epoch := port.server.codebaseContextRegistryEpoch()
	if !port.server.codebaseContextScopeAdmitted(clientSessionID, key, epoch) {
		return uci.IndexBinding{}, codebaseContextHandleClosedError(uci.ContextMismatch)
	}
	binding, err := port.reauthorize(ctx, caller, selector)
	if err != nil {
		return uci.IndexBinding{}, err
	}
	if !port.server.codebaseContextScopeAdmitted(clientSessionID, key, epoch) || binding.Scope != scope || binding.ProfileID != profileID {
		return uci.IndexBinding{}, codebaseContextHandleClosedError(uci.ContextMismatch)
	}
	return binding.Clone(), nil
}

func (port *UCIContextHandlePort) caller(ctx context.Context, clientSessionID string) (uci.ResolveIndexBindingInput, error) {
	if port == nil || port.server == nil || !codebaseContextIdentityText(clientSessionID) {
		return uci.ResolveIndexBindingInput{}, errors.New("invalid UCI handle caller")
	}
	input, err := codebaseContextCallerInput(ctx)
	if err != nil || input.ClientSessionID != clientSessionID {
		return uci.ResolveIndexBindingInput{}, errors.New("UCI handle session mismatch")
	}
	return codebaseContextIndexInput(input, nil), nil
}

func (port *UCIContextHandlePort) reauthorize(ctx context.Context, caller uci.ResolveIndexBindingInput, selector uci.IndexBindingSelector) (uci.IndexBinding, error) {
	if port == nil || port.catalog == nil || port.authorizer == nil || selector.Validate() != nil {
		return uci.IndexBinding{}, codebaseContextHandleClosedError(uci.ContextMismatch)
	}
	binding, err := port.catalog.LoadIndexBinding(ctx, selector.Clone())
	if err != nil || !codebaseContextBindingMatchesSelector(binding, selector) {
		return uci.IndexBinding{}, codebaseContextHandleClosedError(uci.ContextMismatch)
	}
	if err := port.authorizer.AuthorizeContext(ctx, uci.ContextAccess{
		AuthRealm:  caller.AuthRealm,
		Principal:  caller.Principal,
		SourceID:   binding.Scope.SourceID,
		CheckoutID: binding.Scope.CheckoutID,
	}); err != nil {
		return uci.IndexBinding{}, codebaseContextHandleClosedError(uci.PermissionDenied)
	}
	if binding.WorkstationID != caller.WorkstationID {
		return uci.IndexBinding{}, codebaseContextHandleClosedError(uci.PermissionDenied)
	}
	return binding.Clone(), nil
}

func codebaseContextSelectorForBinding(binding uci.IndexBinding) (uci.IndexBindingSelector, error) {
	binding = binding.Clone()
	if err := binding.Validate(); err != nil {
		return uci.IndexBindingSelector{}, err
	}
	if binding.Context != nil {
		return uci.ContextIndexBindingSelector(*binding.Context)
	}
	return uci.CheckoutIndexBindingSelector(uci.RegisteredCheckoutSelector{
		Scope:     binding.Scope,
		ProfileID: binding.ProfileID,
	})
}

func codebaseContextBindingsEqual(left, right uci.IndexBinding) bool {
	if left.Scope != right.Scope || left.ProfileID != right.ProfileID || left.LocalRootID != right.LocalRootID || left.WorkstationID != right.WorkstationID || (left.Context == nil) != (right.Context == nil) {
		return false
	}
	return left.Context == nil || codebaseContextRefsEqual(*left.Context, *right.Context)
}

func codebaseContextHandleClosedError(code uci.ContextErrorCode) error {
	switch code {
	case uci.ContextRequired, uci.ContextMismatch:
		return status.Error(codes.FailedPrecondition, string(code))
	case uci.PermissionDenied:
		return status.Error(codes.PermissionDenied, string(code))
	default:
		return status.Error(codes.Internal, "UCI context handle failed")
	}
}
