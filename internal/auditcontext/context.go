// Package auditcontext carries audit provenance across package boundaries.
package auditcontext

import (
	"context"
	"strings"
)

type (
	actorKey         struct{}
	sourceSessionKey struct{}
)

// WithActor carries an authenticated principal-derived audit actor.
func WithActor(ctx context.Context, actor string) context.Context {
	if actor = strings.TrimSpace(actor); actor == "" {
		return ctx
	}
	return context.WithValue(ctx, actorKey{}, actor)
}

// Actor returns the carried actor or the defined lifecycle fallback.
func Actor(ctx context.Context) string {
	if ctx != nil {
		if actor, ok := ctx.Value(actorKey{}).(string); ok {
			if actor = strings.TrimSpace(actor); actor != "" {
				return actor
			}
		}
	}
	return "system"
}

// WithSourceSession carries optional source-session provenance.
func WithSourceSession(ctx context.Context, sessionID string) context.Context {
	if sessionID = strings.TrimSpace(sessionID); sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, sourceSessionKey{}, sessionID)
}

// SourceSession returns the carried source-session ID or empty when absent.
func SourceSession(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	sessionID, _ := ctx.Value(sourceSessionKey{}).(string)
	return strings.TrimSpace(sessionID)
}
