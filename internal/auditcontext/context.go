// Package auditcontext carries audit provenance across package boundaries.
package auditcontext

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SourceSessionMetadataKey carries the originating MCP client session across native gRPC calls.
const SourceSessionMetadataKey = "x-engram-source-session"

const maxUCITransportSessionBytes = 256

type (
	actorKey               struct{}
	sourceSessionKey       struct{}
	uciTransportSessionKey struct{}
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

// ValidUCITransportSession reports whether a transport correlation tag is safe
// to carry across an MCP-to-gRPC UCI boundary. It is correlation only, never
// an authentication or project-selection authority.
func ValidUCITransportSession(sessionID string) bool {
	if sessionID == "" || len(sessionID) > maxUCITransportSessionBytes || !utf8.ValidString(sessionID) || strings.TrimSpace(sessionID) != sessionID {
		return false
	}
	for _, character := range sessionID {
		if !unicode.IsPrint(character) {
			return false
		}
	}
	return true
}

// WithUCITransportSession carries a validated opaque downstream transport tag
// for UCI request correlation. It is distinct from audit actor and source
// session provenance.
func WithUCITransportSession(ctx context.Context, sessionID string) context.Context {
	if !ValidUCITransportSession(sessionID) {
		return ctx
	}
	return context.WithValue(ctx, uciTransportSessionKey{}, sessionID)
}

// UCITransportSession returns the validated opaque UCI transport tag or an
// empty string when none was carried.
func UCITransportSession(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	sessionID, _ := ctx.Value(uciTransportSessionKey{}).(string)
	if !ValidUCITransportSession(sessionID) {
		return ""
	}
	return sessionID
}
