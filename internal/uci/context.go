package uci

import (
	"context"

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
