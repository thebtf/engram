// Package auditcontext carries audit provenance across package boundaries.
package auditcontext

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SourceSessionMetadataKey carries the originating MCP client session across native gRPC calls.
const SourceSessionMetadataKey = "x-engram-source-session"

// UCIRequestCorrelationMetadataKey carries an opaque representation of the
// outer MCP JSON-RPC ID for UCI exposure idempotency. It is neither transport
// session provenance nor a project-identity comparison request ID.
const UCIRequestCorrelationMetadataKey = "x-engram-uci-request-correlation"

const (
	maxUCITransportSessionBytes           = 256
	maxUCIRequestIdentityBytes            = 256
	maxUCIRequestIdentityRawBytes         = maxUCIRequestIdentityBytes*6 + 2
	maxUCIRequestCorrelationMetadataBytes = 342
)

type (
	actorKey                         struct{}
	sourceSessionKey                 struct{}
	uciTransportSessionKey           struct{}
	uciRequestCorrelationKey         struct{}
	uciRequestCorrelationRequiredKey struct{}
)

// UCIRequestCorrelation holds one canonical, bounded JSON-RPC request ID for
// a daemon-originated UCI operation. Its in-process value preserves canonical
// JSON; its metadata form is ASCII-only base64url and never exposes the raw ID.
type UCIRequestCorrelation struct {
	value string
}

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

// NewUCIRequestCorrelation creates a typed carrier for one bounded string or
// numeric JSON-RPC request ID. String IDs are opaque; only canonical JSON
// encoding bounds apply, while numeric lexical form and type are preserved.
func NewUCIRequestCorrelation(requestID json.RawMessage) (UCIRequestCorrelation, bool) {
	canonical, ok := canonicalUCIRequestIdentity(requestID)
	if !ok {
		return UCIRequestCorrelation{}, false
	}
	return UCIRequestCorrelation{value: string(canonical)}, true
}

// ParseUCIRequestCorrelation decodes one ASCII-only base64url metadata value
// and accepts it only when it contains an already-canonical bounded JSON-RPC
// string or number.
func ParseUCIRequestCorrelation(value string) (UCIRequestCorrelation, bool) {
	if len(value) == 0 || len(value) > maxUCIRequestCorrelationMetadataBytes {
		return UCIRequestCorrelation{}, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return UCIRequestCorrelation{}, false
	}
	canonical, ok := canonicalUCIRequestIdentity(json.RawMessage(decoded))
	if !ok || string(canonical) != string(decoded) {
		return UCIRequestCorrelation{}, false
	}
	return UCIRequestCorrelation{value: string(canonical)}, true
}

// Valid reports whether the in-process value has the canonical bounded form.
func (correlation UCIRequestCorrelation) Valid() bool {
	canonical, ok := canonicalUCIRequestIdentity(json.RawMessage(correlation.value))
	return ok && string(canonical) == correlation.value
}

// MetadataValue returns the ASCII-only metadata encoding or an empty string
// when the correlation is invalid.
func (correlation UCIRequestCorrelation) MetadataValue() string {
	if !correlation.Valid() {
		return ""
	}
	metadataValue := base64.RawURLEncoding.EncodeToString([]byte(correlation.value))
	if len(metadataValue) > maxUCIRequestCorrelationMetadataBytes {
		return ""
	}
	return metadataValue
}

// JSONRPCID returns the canonical type-preserving ID for the server-side MCP
// bridge. Direct and local MCP calls retain their original request ID instead.
func (correlation UCIRequestCorrelation) JSONRPCID() (json.RawMessage, bool) {
	if !correlation.Valid() {
		return nil, false
	}
	return json.RawMessage([]byte(correlation.value)), true
}

// WithUCIRequestCorrelationRequired marks an exposure-producing UCI call that
// must carry a validated request correlation before reaching the MCP adapter.
func WithUCIRequestCorrelationRequired(ctx context.Context) context.Context {
	return context.WithValue(ctx, uciRequestCorrelationRequiredKey{}, true)
}

// UCIRequestCorrelationRequired reports whether an adapter must reject a
// correlation-free UCI exposure call instead of assigning its legacy local ID.
func UCIRequestCorrelationRequired(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	required, _ := ctx.Value(uciRequestCorrelationRequiredKey{}).(bool)
	return required
}

// WithUCIRequestCorrelation carries a validated opaque request correlation.
// It is deliberately separate from audit actor, source-session provenance, and
// V3 comparison request IDs.
func WithUCIRequestCorrelation(ctx context.Context, correlation UCIRequestCorrelation) context.Context {
	if !correlation.Valid() {
		return ctx
	}
	return context.WithValue(ctx, uciRequestCorrelationKey{}, correlation)
}

// UCIRequestCorrelationFromContext returns the validated opaque request
// correlation or false when none was carried.
func UCIRequestCorrelationFromContext(ctx context.Context) (UCIRequestCorrelation, bool) {
	if ctx == nil {
		return UCIRequestCorrelation{}, false
	}
	correlation, ok := ctx.Value(uciRequestCorrelationKey{}).(UCIRequestCorrelation)
	return correlation, ok && correlation.Valid()
}

func canonicalUCIRequestIdentity(raw json.RawMessage) ([]byte, bool) {
	if len(raw) == 0 || len(raw) > maxUCIRequestIdentityRawBytes || !utf8.Valid(raw) {
		return nil, false
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var requestID any
	if err := decoder.Decode(&requestID); err != nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}

	switch requestID.(type) {
	case string, json.Number:
		// UseNumber plus json.Marshal preserves numeric lexical spelling while
		// canonicalizing JSON string encodings.
	default:
		return nil, false
	}

	canonical, err := json.Marshal(requestID)
	if err != nil || len(canonical) == 0 || len(canonical) > maxUCIRequestIdentityBytes || !utf8.Valid(canonical) {
		return nil, false
	}
	return canonical, true
}
