package uci

import (
	"context"
	"strings"
	"sync"
)

type clientContextBinding struct {
	ref     ContextRef
	version uint64
}

// ContextResolver validates, authorizes, and binds contexts per client session.
type ContextResolver struct {
	catalog    ContextCatalog
	authorizer ContextAuthorizer

	mu                 sync.Mutex
	bindings           map[string]clientContextBinding
	nextBindingVersion uint64
}

// NewContextResolver creates a resolver with client-scoped mutable bindings.
func NewContextResolver(catalog ContextCatalog, authorizer ContextAuthorizer) *ContextResolver {
	return &ContextResolver{
		catalog:    catalog,
		authorizer: authorizer,
		bindings:   make(map[string]clientContextBinding),
	}
}

// Resolve validates a client selector, reloads the canonical record, authorizes it, and binds it when selected.
func (resolver *ContextResolver) Resolve(ctx context.Context, input ResolveContextInput) (AuthorizedContext, error) {
	if resolver == nil || ctx == nil ||
		!validContextIdentityText(input.ClientSessionID) ||
		!validContextIdentityText(input.AuthRealm) ||
		!validContextIdentityText(input.Principal) {
		return AuthorizedContext{}, newContextError(ContextMismatch, nil)
	}

	var (
		ref            ContextRef
		bind           bool
		reusingBinding bool
		bindingVersion uint64
	)
	switch {
	case input.Ref != nil:
		ref = input.Ref.clone()
		bind = true
	case len(input.Candidates) == 1:
		ref = input.Candidates[0].clone()
		bind = true
	case len(input.Candidates) > 1:
		return AuthorizedContext{}, newContextError(ContextRequired, nil)
	default:
		resolver.mu.Lock()
		binding, found := resolver.bindings[input.ClientSessionID]
		if found {
			ref = binding.ref.clone()
			bindingVersion = binding.version
		}
		resolver.mu.Unlock()
		if !found {
			return AuthorizedContext{}, newContextError(ContextRequired, nil)
		}
		reusingBinding = true
	}

	if !ref.valid() {
		return AuthorizedContext{}, newContextError(ContextMismatch, nil)
	}
	if resolver.catalog == nil {
		return AuthorizedContext{}, newContextError(ContextMismatch, nil)
	}

	record, err := resolver.catalog.LoadContext(ctx, ref.clone())
	if err != nil || !contextRefsEqual(record.Ref, ref) || record.AuthRealm != input.AuthRealm {
		if reusingBinding {
			resolver.clearBinding(input.ClientSessionID, bindingVersion)
		}
		return AuthorizedContext{}, newContextError(ContextMismatch, err)
	}
	if resolver.authorizer == nil {
		return AuthorizedContext{}, newContextError(PermissionDenied, nil)
	}

	access := ContextAccess{
		AuthRealm:  input.AuthRealm,
		Principal:  input.Principal,
		SourceID:   ref.SourceID,
		CheckoutID: ref.CheckoutID,
	}
	if err := resolver.authorizer.AuthorizeContext(ctx, access); err != nil {
		if reusingBinding {
			resolver.clearBinding(input.ClientSessionID, bindingVersion)
		}
		return AuthorizedContext{}, newContextError(PermissionDenied, err)
	}

	if bind {
		resolver.mu.Lock()
		if resolver.bindings == nil {
			resolver.bindings = make(map[string]clientContextBinding)
		}
		resolver.nextBindingVersion++
		resolver.bindings[input.ClientSessionID] = clientContextBinding{
			ref:     ref.clone(),
			version: resolver.nextBindingVersion,
		}
		resolver.mu.Unlock()
	}
	return newAuthorizedContext(ref), nil
}

func (resolver *ContextResolver) clearBinding(clientSessionID string, version uint64) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()

	binding, found := resolver.bindings[clientSessionID]
	if found && binding.version == version {
		delete(resolver.bindings, clientSessionID)
	}
}

func contextRefsEqual(left, right ContextRef) bool {
	if left.SpaceID == nil || right.SpaceID == nil {
		if left.SpaceID != right.SpaceID {
			return false
		}
	} else if *left.SpaceID != *right.SpaceID {
		return false
	}
	return left.SourceID == right.SourceID &&
		left.CheckoutID == right.CheckoutID &&
		left.ViewID == right.ViewID &&
		left.AnalysisProfileID == right.AnalysisProfileID &&
		left.Generation == right.Generation
}

func validContextIdentityText(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
