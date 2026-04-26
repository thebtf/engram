package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// Token-shape constants. The dashboard issues raw tokens with the exact
// shape "engram_" + 32 hex chars (= 7 + 32 = 39 chars total). The first 8 hex
// chars form a non-unique prefix index in the api_tokens table; collisions are
// resolved by bcrypt comparison over the candidate set.
//
// These constants are EXPORTED so the issuance code in
// internal/worker/handlers_auth.go can reuse them — single source of truth
// prevents prefix-format drift across producer (handler) and consumer
// (validator).
const (
	// TokenRawPrefix is the literal byte sequence every dashboard-issued
	// keycard begins with. Tokens lacking this prefix are rejected without
	// touching the database.
	TokenRawPrefix = "engram_"

	// TokenPrefixLen is the number of characters AFTER TokenRawPrefix that
	// form the database prefix index.
	TokenPrefixLen = 8

	// TokenBodyLen is the EXACT number of hex characters following
	// TokenRawPrefix in a valid keycard. Issuance always emits exactly 32
	// hex chars (16 random bytes hex-encoded). The shape gate enforces this
	// to fail-closed before touching the database, which prevents
	// prefix-probing from generating bcrypt load.
	TokenBodyLen = 32

	// TokenTotalLen is the exact total length of a valid raw keycard:
	// TokenRawPrefix + TokenBodyLen.
	TokenTotalLen = len(TokenRawPrefix) + TokenBodyLen // 7 + 32 = 39

	// TokenMinLen is preserved for legacy callers that imported it pre-v6;
	// new code should use TokenTotalLen for length comparisons.
	//
	// Deprecated: use TokenTotalLen.
	TokenMinLen = TokenTotalLen
)

// isHex reports whether b is a valid lowercase or uppercase ASCII hex digit.
// Inlined to avoid a unicode-aware helper on the validator hot path.
func isHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// TokenStoreReader is the narrow read-side contract Validator depends on. The
// production binding is *gormdb.TokenStore; tests inject a fake. Keeping the
// interface in this package means callers cannot accidentally couple the
// validator to write-side TokenStore methods (Create, Revoke, IncrementStats —
// those belong to the issuance/audit paths, not the validation hot-path).
//
// Compile-time conformance is asserted in validator_test.go via
//
//	var _ auth.TokenStoreReader = (*gormdb.TokenStore)(nil)
//
// so a signature drift in gormdb is caught at build time.
type TokenStoreReader interface {
	// FindByPrefix returns all NON-revoked tokens whose token_prefix column
	// equals prefix. The contract on the engram side already filters
	// revoked rows; the validator therefore does not need to inspect the
	// Revoked field on returned rows.
	FindByPrefix(ctx context.Context, prefix string) ([]gormdb.APIToken, error)
}

// Validator is the single source of truth for token-based authentication.
// It is consumed by both the HTTP middleware (internal/worker/middleware.go)
// and the gRPC interceptor (internal/grpcserver/server.go).
//
// Validator is safe for concurrent use; the store field is the only mutable
// dependency and TokenStoreReader implementations are expected to be
// goroutine-safe (gormdb.TokenStore satisfies that via *gorm.DB).
type Validator struct {
	masterToken string           // Tier 1 — operator key from server-host env.
	store       TokenStoreReader // Tier 2 — dashboard-issued keycards.
}

// NewValidator constructs a Validator bound to the given master token and
// keycard store. masterToken MAY be empty — when empty, only Tier-2 (keycard)
// validation is attempted. Use empty masterToken when running with
// ENGRAM_AUTH_DISABLED=true is not desired but the operator chooses to
// surface keycards as the sole authentication path.
func NewValidator(masterToken string, store TokenStoreReader) *Validator {
	return &Validator{masterToken: masterToken, store: store}
}

// Validate runs the two-tier authentication chain on raw and returns the
// resulting Identity, or an error matching ErrEmptyToken / ErrInvalidCredentials
// / a wrapped store error.
//
// The chain is fixed-order:
//
//  1. Empty bearer → ErrEmptyToken.
//  2. Constant-time match against the master token → SourceMaster Identity.
//  3. Shape gate (tokenRawPrefix + ≥ tokenMinLen chars) — fails closed without
//     touching the database, preventing token-shape probing from generating DB
//     load.
//  4. Prefix lookup → bcrypt loop over candidates → SourceClient Identity on
//     the first match.
//  5. No match → ErrInvalidCredentials.
//
// The validator deliberately does NOT distinguish "no candidate" from "bcrypt
// mismatch" in the returned error: both paths surface ErrInvalidCredentials so
// callers (and downstream observers) cannot use response shape to fingerprint
// the api_tokens table.
func (v *Validator) Validate(ctx context.Context, raw string) (Identity, error) {
	if raw == "" {
		return Identity{}, ErrEmptyToken
	}

	// Tier 1 — master operator key. Constant-time compare blocks timing
	// oracles even when masterToken is unset (we still pay the constant
	// cost on the empty-vs-token comparison).
	if v.masterToken != "" {
		if subtle.ConstantTimeCompare([]byte(raw), []byte(v.masterToken)) == 1 {
			return Admin(), nil
		}
	}

	// Tier 2 shape gate. Enforce exact total length AND that the body is
	// pure hex. Either gate failing avoids any DB / bcrypt work, blocking
	// prefix-probing and shape-fuzzing as DoS vectors.
	if !strings.HasPrefix(raw, TokenRawPrefix) || len(raw) != TokenTotalLen {
		return Identity{}, ErrInvalidCredentials
	}
	body := raw[len(TokenRawPrefix):]
	for i := 0; i < len(body); i++ {
		if !isHex(body[i]) {
			return Identity{}, ErrInvalidCredentials
		}
	}
	prefix := body[:TokenPrefixLen]

	candidates, err := v.store.FindByPrefix(ctx, prefix)
	if err != nil {
		return Identity{}, fmt.Errorf("auth: token store lookup: %w", err)
	}
	if len(candidates) == 0 {
		return Identity{}, ErrInvalidCredentials
	}

	for i := range candidates {
		err := bcrypt.CompareHashAndPassword(
			[]byte(candidates[i].TokenHash),
			[]byte(raw),
		)
		if err == nil {
			// Defense-in-depth: api_tokens.scope is plain text. A row with
			// scope="admin" (data corruption, malicious INSERT, future
			// schema drift) MUST NOT promote a worker keycard to admin.
			// Whitelist the two values issuance is allowed to write.
			switch Role(candidates[i].Scope) {
			case RoleReadWrite, RoleReadOnly:
				return Client(candidates[i].Scope, candidates[i].ID), nil
			default:
				return Identity{}, fmt.Errorf(
					"auth: keycard %s has unexpected scope %q (allowed: %q, %q)",
					candidates[i].ID, candidates[i].Scope,
					RoleReadWrite, RoleReadOnly,
				)
			}
		}
		// bcrypt.ErrMismatchedHashAndPassword is the expected miss; only
		// log on unexpected errors (bcrypt cost/format issues).
		if !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return Identity{}, fmt.Errorf("auth: bcrypt: %w", err)
		}
	}
	return Identity{}, ErrInvalidCredentials
}
