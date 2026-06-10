package mcp

import (
	"context"
	"net/http"
)

type contextKey string

const (
	projectContextKey contextKey = "engram-project"
	sessionContextKey contextKey = "engram-session"
)

// extractProjectFromHeader reads the X-Engram-Project header from an HTTP request.
// Returns empty string if header is absent.
func extractProjectFromHeader(r *http.Request) string {
	return r.Header.Get("X-Engram-Project")
}

// contextWithProject stores project identity in context.
func contextWithProject(ctx context.Context, project string) context.Context {
	return context.WithValue(ctx, projectContextKey, project)
}

// ContextWithProject is the exported variant of contextWithProject for use by
// packages that need to inject project identity without importing internal/mcp
// fully (e.g. internal/grpcserver).
func ContextWithProject(ctx context.Context, project string) context.Context {
	return contextWithProject(ctx, project)
}

// projectFromContext retrieves project identity from context.
// Returns empty string if not set.
func projectFromContext(ctx context.Context) string {
	v, _ := ctx.Value(projectContextKey).(string)
	return v
}

// contextWithSession stores session identity in context for audit actor derivation.
func contextWithSession(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionContextKey, sessionID)
}

// ContextWithSession is the exported variant of contextWithSession for use by
// packages that inject session identity without importing internal/mcp fully
// (e.g. internal/grpcserver).
func ContextWithSession(ctx context.Context, sessionID string) context.Context {
	return contextWithSession(ctx, sessionID)
}

// sessionFromContext retrieves the session ID from context.
// Returns empty string if not set.
func sessionFromContext(ctx context.Context) string {
	v, _ := ctx.Value(sessionContextKey).(string)
	return v
}

// actorFromContext derives an audit actor string from context.
// Uses session_id if set; falls back to "agent".
func actorFromContext(ctx context.Context) string {
	if s := sessionFromContext(ctx); s != "" {
		return s
	}
	return "agent"
}
