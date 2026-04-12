package mcp

import (
	"context"
	"net/http"
)

type contextKey string

const projectContextKey contextKey = "engram-project"

// extractProjectFromHeader reads the X-Engram-Project header from an HTTP request.
// Returns empty string if header is absent.
func extractProjectFromHeader(r *http.Request) string {
	return r.Header.Get("X-Engram-Project")
}

// contextWithProject stores project identity in context.
func contextWithProject(ctx context.Context, project string) context.Context {
	return context.WithValue(ctx, projectContextKey, project)
}

// projectFromContext retrieves project identity from context.
// Returns empty string if not set.
func projectFromContext(ctx context.Context) string {
	v, _ := ctx.Value(projectContextKey).(string)
	return v
}
