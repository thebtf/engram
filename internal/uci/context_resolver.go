package uci

import (
	"context"
	"strings"
	"sync"
)

type clientContextBinding struct {
	selector IndexBindingSelector
	version  uint64
}

// ContextResolver validates, authorizes, and binds closed context selectors per
// authenticated client session. Its selections are in-memory conveniences, not
// reusable authorization grants.
type ContextResolver struct {
	catalog      ContextCatalog
	indexCatalog IndexBindingCatalog
	authorizer   ContextAuthorizer

	mu                 sync.Mutex
	bindings           map[string]clientContextBinding
	nextBindingVersion uint64
}

// NewContextResolver creates a resolver with client-scoped mutable bindings.
// indexCatalog is used only for checkout-following index selections; exact
// ContextRef reads retain their existing ContextCatalog semantics.
func NewContextResolver(catalog ContextCatalog, authorizer ContextAuthorizer, indexCatalog IndexBindingCatalog) *ContextResolver {
	return &ContextResolver{
		catalog:      catalog,
		indexCatalog: indexCatalog,
		authorizer:   authorizer,
		bindings:     make(map[string]clientContextBinding),
	}
}

// Resolve validates, authorizes, and selects an exact View context for one
// client session. A checkout-following default remains stored as a checkout
// selector even when it currently resolves to a published View.
func (resolver *ContextResolver) Resolve(ctx context.Context, input ResolveContextInput) (AuthorizedContext, error) {
	return resolver.resolve(ctx, input, true)
}

// Authorize validates one explicit context without changing the client's
// selected default.
func (resolver *ContextResolver) Authorize(ctx context.Context, input ResolveContextInput) (AuthorizedContext, error) {
	if input.Ref == nil || len(input.Candidates) != 0 {
		return AuthorizedContext{}, newContextError(ContextMismatch, nil)
	}
	return resolver.resolve(ctx, input, false)
}

func (resolver *ContextResolver) resolve(ctx context.Context, input ResolveContextInput, bindSelection bool) (AuthorizedContext, error) {
	if !resolver.validCaller(ctx, input.ClientSessionID, input.AuthRealm, input.Principal) {
		return AuthorizedContext{}, newContextError(ContextMismatch, nil)
	}

	var (
		ref            ContextRef
		selector       IndexBindingSelector
		bind           bool
		reusingBinding bool
		bindingVersion uint64
	)
	switch {
	case input.Ref != nil:
		ref = input.Ref.clone()
		bind = bindSelection
	case len(input.Candidates) == 1:
		ref = input.Candidates[0].clone()
		bind = bindSelection
	case len(input.Candidates) > 1:
		return AuthorizedContext{}, newContextError(ContextRequired, nil)
	default:
		var found bool
		selector, bindingVersion, found = resolver.boundSelector(input.ClientSessionID)
		if !found {
			return AuthorizedContext{}, newContextError(ContextRequired, nil)
		}
		reusingBinding = true
		if boundRef, pinned := selector.Context(); pinned {
			ref = boundRef
			break
		}

		binding, err := resolver.resolveIndexBinding(ctx, ResolveIndexBindingInput{
			ClientSessionID: input.ClientSessionID,
			AuthRealm:       input.AuthRealm,
			Principal:       input.Principal,
			WorkstationID:   input.WorkstationID,
		}, false)
		if err != nil {
			return AuthorizedContext{}, err
		}
		resolvedBinding := binding.Binding()
		if resolvedBinding.Context == nil {
			return AuthorizedContext{}, newContextError(ContextRequired, nil)
		}
		ref = resolvedBinding.Context.clone()
	}

	return resolver.authorizeContextRef(ctx, input, ref, bind, reusingBinding, bindingVersion)
}

// ResolveIndexBinding validates, authorizes, and selects a closed index target.
// A checkout target remains valid before first publication and therefore can
// return an IndexBinding whose Context is nil.
func (resolver *ContextResolver) ResolveIndexBinding(ctx context.Context, input ResolveIndexBindingInput) (AuthorizedIndexBinding, error) {
	return resolver.resolveIndexBinding(ctx, input, true)
}

// AuthorizeIndexBinding validates one explicit closed index target without
// changing the client's selected default.
func (resolver *ContextResolver) AuthorizeIndexBinding(ctx context.Context, input ResolveIndexBindingInput) (AuthorizedIndexBinding, error) {
	if input.Selector == nil {
		return AuthorizedIndexBinding{}, newContextError(ContextMismatch, nil)
	}
	return resolver.resolveIndexBinding(ctx, input, false)
}

func (resolver *ContextResolver) resolveIndexBinding(ctx context.Context, input ResolveIndexBindingInput, bindSelection bool) (AuthorizedIndexBinding, error) {
	if !resolver.validCaller(ctx, input.ClientSessionID, input.AuthRealm, input.Principal) ||
		!validContextIdentityText(input.WorkstationID) {
		return AuthorizedIndexBinding{}, newContextError(ContextMismatch, nil)
	}

	var (
		selector       IndexBindingSelector
		bind           bool
		reusingBinding bool
		bindingVersion uint64
	)
	if input.Selector != nil {
		selector = input.Selector.Clone()
		bind = bindSelection
	} else {
		var found bool
		selector, bindingVersion, found = resolver.boundSelector(input.ClientSessionID)
		if !found {
			return AuthorizedIndexBinding{}, newContextError(ContextRequired, nil)
		}
		reusingBinding = true
	}
	if err := selector.Validate(); err != nil {
		resolver.clearReuse(input.ClientSessionID, reusingBinding, bindingVersion)
		return AuthorizedIndexBinding{}, newContextError(ContextMismatch, nil)
	}
	if resolver == nil || resolver.indexCatalog == nil {
		resolver.clearReuse(input.ClientSessionID, reusingBinding, bindingVersion)
		return AuthorizedIndexBinding{}, newContextError(ContextMismatch, nil)
	}

	binding, err := resolver.indexCatalog.LoadIndexBinding(ctx, selector.Clone())
	if err != nil || !indexBindingMatchesSelector(binding, selector) {
		resolver.clearReuse(input.ClientSessionID, reusingBinding, bindingVersion)
		return AuthorizedIndexBinding{}, newContextError(ContextMismatch, err)
	}
	if resolver.authorizer == nil {
		resolver.clearReuse(input.ClientSessionID, reusingBinding, bindingVersion)
		return AuthorizedIndexBinding{}, newContextError(PermissionDenied, nil)
	}
	if err := resolver.authorizer.AuthorizeContext(ctx, ContextAccess{
		AuthRealm:  input.AuthRealm,
		Principal:  input.Principal,
		SourceID:   binding.Scope.SourceID,
		CheckoutID: binding.Scope.CheckoutID,
	}); err != nil {
		resolver.clearReuse(input.ClientSessionID, reusingBinding, bindingVersion)
		return AuthorizedIndexBinding{}, newContextError(PermissionDenied, err)
	}
	if binding.WorkstationID != input.WorkstationID {
		resolver.clearReuse(input.ClientSessionID, reusingBinding, bindingVersion)
		return AuthorizedIndexBinding{}, newContextError(PermissionDenied, nil)
	}

	if bind {
		resolver.bindSelector(input.ClientSessionID, selector)
	}
	return newAuthorizedIndexBinding(binding), nil
}

func (resolver *ContextResolver) authorizeContextRef(ctx context.Context, input ResolveContextInput, ref ContextRef, bind, reusingBinding bool, bindingVersion uint64) (AuthorizedContext, error) {
	if !ref.valid() {
		resolver.clearReuse(input.ClientSessionID, reusingBinding, bindingVersion)
		return AuthorizedContext{}, newContextError(ContextMismatch, nil)
	}
	if resolver == nil || resolver.catalog == nil {
		resolver.clearReuse(input.ClientSessionID, reusingBinding, bindingVersion)
		return AuthorizedContext{}, newContextError(ContextMismatch, nil)
	}

	record, err := resolver.catalog.LoadContext(ctx, ref.clone())
	if err != nil || !contextRefsEqual(record.Ref, ref) || record.AuthRealm != input.AuthRealm {
		resolver.clearReuse(input.ClientSessionID, reusingBinding, bindingVersion)
		return AuthorizedContext{}, newContextError(ContextMismatch, err)
	}
	if resolver.authorizer == nil {
		resolver.clearReuse(input.ClientSessionID, reusingBinding, bindingVersion)
		return AuthorizedContext{}, newContextError(PermissionDenied, nil)
	}
	if err := resolver.authorizer.AuthorizeContext(ctx, ContextAccess{
		AuthRealm:  input.AuthRealm,
		Principal:  input.Principal,
		SourceID:   ref.SourceID,
		CheckoutID: ref.CheckoutID,
	}); err != nil {
		resolver.clearReuse(input.ClientSessionID, reusingBinding, bindingVersion)
		return AuthorizedContext{}, newContextError(PermissionDenied, err)
	}

	if bind {
		selector, err := ContextIndexBindingSelector(ref)
		if err != nil {
			return AuthorizedContext{}, newContextError(ContextMismatch, err)
		}
		resolver.bindSelector(input.ClientSessionID, selector)
	}
	return newAuthorizedContext(ref), nil
}

// BoundContext returns a cloned pinned ContextRef without resolving or
// authorizing it. Checkout-following selections deliberately have no synthetic
// ContextRef to expose through this legacy peek.
func (resolver *ContextResolver) BoundContext(clientSessionID string) (ContextRef, bool) {
	selector, _, found := resolver.boundSelector(clientSessionID)
	if !found {
		return ContextRef{}, false
	}
	return selector.Context()
}

// BoundSelector returns a cloned existing selector without resolving or
// authorizing it.
func (resolver *ContextResolver) BoundSelector(clientSessionID string) (IndexBindingSelector, bool) {
	selector, _, found := resolver.boundSelector(clientSessionID)
	return selector, found
}

// ForgetClient removes every in-memory default for one expired or evicted
// transport client. Durable contexts, builds, and views remain untouched.
func (resolver *ContextResolver) ForgetClient(clientSessionID string) {
	if resolver == nil || !validContextIdentityText(clientSessionID) {
		return
	}
	resolver.mu.Lock()
	delete(resolver.bindings, clientSessionID)
	resolver.mu.Unlock()
}

func (resolver *ContextResolver) validCaller(ctx context.Context, clientSessionID, authRealm, principal string) bool {
	return resolver != nil && ctx != nil &&
		validContextIdentityText(clientSessionID) &&
		validContextIdentityText(authRealm) &&
		validContextIdentityText(principal)
}

func (resolver *ContextResolver) boundSelector(clientSessionID string) (IndexBindingSelector, uint64, bool) {
	if resolver == nil || !validContextIdentityText(clientSessionID) {
		return IndexBindingSelector{}, 0, false
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	binding, found := resolver.bindings[clientSessionID]
	if !found {
		return IndexBindingSelector{}, 0, false
	}
	return binding.selector.Clone(), binding.version, true
}

func (resolver *ContextResolver) bindSelector(clientSessionID string, selector IndexBindingSelector) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if resolver.bindings == nil {
		resolver.bindings = make(map[string]clientContextBinding)
	}
	resolver.nextBindingVersion++
	resolver.bindings[clientSessionID] = clientContextBinding{
		selector: selector.Clone(),
		version:  resolver.nextBindingVersion,
	}
}

func (resolver *ContextResolver) clearReuse(clientSessionID string, reusingBinding bool, version uint64) {
	if reusingBinding {
		resolver.clearBinding(clientSessionID, version)
	}
}

func (resolver *ContextResolver) clearBinding(clientSessionID string, version uint64) {
	if resolver == nil {
		return
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	binding, found := resolver.bindings[clientSessionID]
	if found && binding.version == version {
		delete(resolver.bindings, clientSessionID)
	}
}

func indexBindingMatchesSelector(binding IndexBinding, selector IndexBindingSelector) bool {
	if err := binding.Validate(); err != nil {
		return false
	}
	if ref, pinned := selector.Context(); pinned {
		return binding.Context != nil && contextRefsEqual(*binding.Context, ref)
	}
	checkout, following := selector.Checkout()
	return following && binding.Scope == checkout.Scope && binding.ProfileID == checkout.ProfileID
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
