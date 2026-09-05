package uci

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// ContextRef identifies one immutable, scoped UCI view.
type ContextRef struct {
	SpaceID           *string
	SourceID          string
	CheckoutID        string
	ViewID            string
	AnalysisProfileID string
	Generation        int64
}

func (ref ContextRef) clone() ContextRef {
	copy := ref
	if ref.SpaceID != nil {
		spaceID := *ref.SpaceID
		copy.SpaceID = &spaceID
	}
	return copy
}

func (ref ContextRef) valid() bool {
	return canonicalContextUUID(ref.SourceID) &&
		canonicalContextUUID(ref.CheckoutID) &&
		canonicalContextUUID(ref.ViewID) &&
		canonicalContextUUID(ref.AnalysisProfileID) &&
		(ref.SpaceID == nil || canonicalContextUUID(*ref.SpaceID)) &&
		ref.Generation > 0
}

func canonicalContextUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

// ContextRecord is the catalog-owned canonical tuple and its authentication realm.
type ContextRecord struct {
	Ref       ContextRef
	AuthRealm string
}

// ContextCatalog reloads a canonical context tuple from the authoritative catalog.
type ContextCatalog interface {
	LoadContext(ctx context.Context, ref ContextRef) (ContextRecord, error)
}

// ContextMetadata is safe display data for an already-resolved ContextRef.
// It deliberately omits authority inputs and private checkout locators.
type ContextMetadata struct {
	Source   string
	Checkout string
	View     string
}

// ContextDirectory supplies bounded owner-scoped context discovery and safe
// display metadata without making labels, paths, or local workstation data an
// authority input.
type ContextDirectory interface {
	ListAuthorizedContexts(ctx context.Context, authRealm, principal string, limit int) ([]ContextRef, error)
	LoadContextMetadata(ctx context.Context, ref ContextRef) (ContextMetadata, error)
}

// IndexBinding is the server-authorized index target for one checkout incarnation.
// Context is nil only when a registered checkout has no current published View.
type IndexBinding struct {
	Context       *ContextRef
	Scope         IndexScope
	ProfileID     string
	LocalRootID   string
	WorkstationID string
}

// Clone returns a defensive copy of the index binding.
func (binding IndexBinding) Clone() IndexBinding {
	copy := binding
	if binding.Context != nil {
		contextRef := binding.Context.clone()
		copy.Context = &contextRef
	}
	return copy
}

// Validate checks the complete binding shape. Catalog implementations additionally
// prove that a non-nil Context names a real View.
func (binding IndexBinding) Validate() error {
	if !validIndexScope(binding.Scope) {
		return fmt.Errorf("uci index binding: invalid scope")
	}
	if !canonicalContextUUID(binding.ProfileID) {
		return fmt.Errorf("uci index binding: invalid profile")
	}
	if !validContextIdentityText(binding.LocalRootID) {
		return fmt.Errorf("uci index binding: invalid local root")
	}
	if !validContextIdentityText(binding.WorkstationID) {
		return fmt.Errorf("uci index binding: invalid workstation")
	}
	if binding.Context == nil {
		return nil
	}
	if !binding.Context.valid() ||
		binding.Context.SourceID != binding.Scope.SourceID ||
		binding.Context.CheckoutID != binding.Scope.CheckoutID ||
		binding.Context.AnalysisProfileID != binding.ProfileID {
		return fmt.Errorf("uci index binding: context does not match scope and profile")
	}
	return nil
}

// RegisteredCheckoutSelector identifies one server-registered checkout and
// analysis profile. It intentionally contains no local path or workstation
// authority.
type RegisteredCheckoutSelector struct {
	Scope     IndexScope
	ProfileID string
}

// Clone returns a value copy of the registered checkout selector.
func (selector RegisteredCheckoutSelector) Clone() RegisteredCheckoutSelector {
	return selector
}

func (selector RegisteredCheckoutSelector) valid() bool {
	return validIndexScope(selector.Scope) && canonicalContextUUID(selector.ProfileID)
}

type indexBindingSelectorKind uint8

const (
	indexBindingSelectorInvalid indexBindingSelectorKind = iota
	indexBindingSelectorContext
	indexBindingSelectorCheckout
)

// IndexBindingSelector is a closed choice between a pinned ContextRef and a
// registered checkout/profile. Constructors prevent a caller from combining
// the two authority forms.
type IndexBindingSelector struct {
	kind     indexBindingSelectorKind
	context  ContextRef
	checkout RegisteredCheckoutSelector
}

// ContextIndexBindingSelector creates a selector pinned to one exact View.
func ContextIndexBindingSelector(ref ContextRef) (IndexBindingSelector, error) {
	if !ref.valid() {
		return IndexBindingSelector{}, fmt.Errorf("uci index binding selector: invalid context selector")
	}
	return IndexBindingSelector{kind: indexBindingSelectorContext, context: ref.clone()}, nil
}

// CheckoutIndexBindingSelector creates a checkout-following selector for one
// fixed source, checkout incarnation, and analysis profile.
func CheckoutIndexBindingSelector(checkout RegisteredCheckoutSelector) (IndexBindingSelector, error) {
	if !checkout.valid() {
		return IndexBindingSelector{}, fmt.Errorf("uci index binding selector: invalid checkout selector")
	}
	return IndexBindingSelector{kind: indexBindingSelectorCheckout, checkout: checkout.Clone()}, nil
}

// Clone returns an independent selector value.
func (selector IndexBindingSelector) Clone() IndexBindingSelector {
	copy := selector
	copy.context = selector.context.clone()
	copy.checkout = selector.checkout.Clone()
	return copy
}

// Context returns the exact View only for a pinned selector.
func (selector IndexBindingSelector) Context() (ContextRef, bool) {
	if selector.kind != indexBindingSelectorContext {
		return ContextRef{}, false
	}
	return selector.context.clone(), true
}

// Checkout returns the registered checkout only for a checkout-following selector.
func (selector IndexBindingSelector) Checkout() (RegisteredCheckoutSelector, bool) {
	if selector.kind != indexBindingSelectorCheckout {
		return RegisteredCheckoutSelector{}, false
	}
	return selector.checkout.Clone(), true
}

// Validate checks that the selector is one valid closed selection form.
func (selector IndexBindingSelector) Validate() error {
	switch selector.kind {
	case indexBindingSelectorContext:
		if selector.context.valid() && !selector.checkout.valid() && selector.checkout == (RegisteredCheckoutSelector{}) {
			return nil
		}
	case indexBindingSelectorCheckout:
		if selector.checkout.valid() && !selector.context.valid() && selector.context == (ContextRef{}) {
			return nil
		}
	}
	return fmt.Errorf("uci index binding selector: invalid closed selector")
}

// IndexBindingCatalog reloads a server-authorized index binding from the
// authoritative checkout registry. It never derives authority from a local path.
type IndexBindingCatalog interface {
	LoadIndexBinding(ctx context.Context, selector IndexBindingSelector) (IndexBinding, error)
}

// AuthorizedIndexBinding is an immutable, resolver-authorized index target.
// Only the resolver constructs it; callers receive a defensive binding copy.
type AuthorizedIndexBinding struct {
	binding IndexBinding
}

func newAuthorizedIndexBinding(binding IndexBinding) AuthorizedIndexBinding {
	return AuthorizedIndexBinding{binding: binding.Clone()}
}

// Binding returns a defensive copy of the authorized target.
func (binding AuthorizedIndexBinding) Binding() IndexBinding {
	return binding.binding.Clone()
}

// ContextAccess contains the catalog-validated scope presented to the authorizer.
type ContextAccess struct {
	AuthRealm  string
	Principal  string
	SourceID   string
	CheckoutID string
}

// ContextAuthorizer decides whether a principal can access a canonical context scope.
type ContextAuthorizer interface {
	AuthorizeContext(ctx context.Context, access ContextAccess) error
}

// ResolveContextInput is untrusted client resolution input.
type ResolveContextInput struct {
	ClientSessionID string
	AuthRealm       string
	Principal       string
	WorkstationID   string
	Ref             *ContextRef
	Candidates      []ContextRef
}

// ResolveIndexBindingInput is the trusted caller scope plus an optional closed
// selector. A nil Selector reuses only that client's in-memory selected slot.
// WorkstationID is derived from the authenticated transport, never client JSON.
type ResolveIndexBindingInput struct {
	ClientSessionID string
	AuthRealm       string
	Principal       string
	WorkstationID   string
	Selector        *IndexBindingSelector
}

// AuthorizedContext is a successful immutable resolution result.
type AuthorizedContext struct {
	ref ContextRef
}

func newAuthorizedContext(ref ContextRef) AuthorizedContext {
	return AuthorizedContext{ref: ref.clone()}
}

// Ref returns a deep copy of the resolved context reference.
func (context AuthorizedContext) Ref() ContextRef {
	return context.ref.clone()
}
