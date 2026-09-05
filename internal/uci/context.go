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

// IndexBindingSelector supplies an already-authorized View or a registered checkout.
// Exactly one selector is permitted. A checkout selector includes the requested
// analysis profile because an unindexed checkout has no View from which to derive it.
type IndexBindingSelector struct {
	Context   *ContextRef
	Scope     *IndexScope
	ProfileID string
}

// Clone returns a defensive copy of the selector.
func (selector IndexBindingSelector) Clone() IndexBindingSelector {
	copy := selector
	if selector.Context != nil {
		contextRef := selector.Context.clone()
		copy.Context = &contextRef
	}
	if selector.Scope != nil {
		scope := *selector.Scope
		copy.Scope = &scope
	}
	return copy
}

// Validate checks that the selector has exactly one valid selection form.
func (selector IndexBindingSelector) Validate() error {
	switch {
	case selector.Context != nil && selector.Scope == nil:
		if selector.ProfileID != "" || !selector.Context.valid() {
			return fmt.Errorf("uci index binding selector: invalid context selector")
		}
	case selector.Context == nil && selector.Scope != nil:
		if !validIndexScope(*selector.Scope) || !canonicalContextUUID(selector.ProfileID) {
			return fmt.Errorf("uci index binding selector: invalid checkout selector")
		}
	default:
		return fmt.Errorf("uci index binding selector: exactly one selector is required")
	}
	return nil
}

// IndexBindingCatalog reloads a server-authorized index binding from the
// authoritative checkout registry. It never derives authority from a local path.
type IndexBindingCatalog interface {
	LoadIndexBinding(ctx context.Context, selector IndexBindingSelector) (IndexBinding, error)
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
	Ref             *ContextRef
	Candidates      []ContextRef
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
