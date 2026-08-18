package auth

import (
	"context"

	"github.com/thebtf/engram/internal/auditcontext"
)

// Context-key types. Both are unexported as type definitions but exported as
// VALUE singletons (RoleKey, SourceKey) so other packages can read context
// values without taking a dependency on internal/worker's authRoleKey{}
// (which historically lives in handlers_auth.go and predates this package).
//
// Two packages need to read these keys:
//   - internal/grpcserver — sets identity from gRPC interceptor
//   - internal/worker     — sets identity from HTTP middleware AND reads it
//                           from request handlers (issuance gate per FR-6)
//
// To preserve backwards compatibility with existing internal/worker code that
// reads the literal authRoleKey{} struct value, internal/worker/middleware.go
// MAY alias auth.RoleKey to its own authRoleKey{} (same Type Identity is not
// required — both consumers will be migrated together in Phase 3).

type identityKeyType struct{}

// IdentityKey is the canonical context value key for the resolved Identity.
// Both transports (HTTP middleware, gRPC interceptor) store the entire
// Identity (Role + Source + KeycardID) under this single key after
// authentication succeeds. Downstream handlers read it via IdentityFrom,
// or read individual scalars via RoleFrom / SourceFrom.
//
// The key is unexported as a type but exported as a value singleton so
// other packages can reach into context.Value directly when an interface
// boundary makes IdentityFrom inconvenient.
var IdentityKey = identityKeyType{}

// WithIdentity returns a new context carrying id under IdentityKey. Intended
// to be called once per request after Validator.Validate (or after a
// session-cookie path resolves the user role). The full Identity (including
// KeycardID for SourceClient) is preserved so issuance audit / stats handlers
// can attribute requests to a specific keycard.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	ctx = context.WithValue(ctx, IdentityKey, id)
	if principal, _, ok := id.MemoryOwner(); ok {
		return auditcontext.WithActor(ctx, principal)
	}
	return ctx
}

// IdentityFrom extracts an Identity from ctx. The second return is false when
// no Identity has been set on the context — callers should treat that as
// "unauthenticated" and respond accordingly.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	if ctx == nil {
		return Identity{}, false
	}
	id, ok := ctx.Value(IdentityKey).(Identity)
	return id, ok
}

// RoleFrom returns the role string for backward-compat with handlers that
// previously read worker.authRoleKey{}. Empty string when no Identity is on
// the context.
func RoleFrom(ctx context.Context) string {
	if id, ok := IdentityFrom(ctx); ok {
		return string(id.Role)
	}
	return ""
}

// SourceFrom returns the auth source string. Empty string when no Identity is
// on the context (e.g., authentication was disabled).
func SourceFrom(ctx context.Context) string {
	if id, ok := IdentityFrom(ctx); ok {
		return string(id.Source)
	}
	return ""
}
