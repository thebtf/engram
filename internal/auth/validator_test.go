package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

// stubStore is a TokenStoreReader fake. It records FindByPrefix invocations
// for the absence-of-extra-IO assertion (NFR-2) and returns a fixed candidate
// slice keyed by prefix.
type stubStore struct {
	byPrefix    map[string][]gormdb.APIToken
	prefixCalls int
	returnErr   error
}

func (s *stubStore) FindByPrefix(_ context.Context, prefix string) ([]gormdb.APIToken, error) {
	s.prefixCalls++
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	return append([]gormdb.APIToken(nil), s.byPrefix[prefix]...), nil
}

// makeKeycard hashes raw using bcrypt.MinCost (test-only) and returns an
// APIToken row with the conventional engram_<prefix><tail> shape.
//
// Fixture validation is wired to the auth.Token* constants (TokenRawPrefix,
// TokenPrefixLen, TokenTotalLen) — single source of truth for token shape
// across producer (issuance), consumer (validator), and tests. A future
// shape change cannot leave these tests silently green.
func makeKeycard(t *testing.T, id, raw, scope string, revoked bool) gormdb.APIToken {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.MinCost)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(raw, auth.TokenRawPrefix),
		"test fixture: raw must start with auth.TokenRawPrefix (%q)", auth.TokenRawPrefix)
	require.Equal(t, auth.TokenTotalLen, len(raw),
		"test fixture: raw must be exactly auth.TokenTotalLen chars (got %d, want %d)",
		len(raw), auth.TokenTotalLen)
	prefix := raw[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen]
	return gormdb.APIToken{
		ID:          id,
		Name:        "test-" + id,
		TokenHash:   string(hash),
		TokenPrefix: prefix,
		Scope:       scope,
		Revoked:     revoked,
	}
}

func makePrincipalKeycard(t *testing.T, id, raw, scope, principal string, principalKind auth.PrincipalKind) gormdb.APIToken {
	t.Helper()
	token := makeKeycard(t, id, raw, scope, false)
	token.Principal = principal
	token.PrincipalKind = string(principalKind)
	return token
}

func TestIdentity_ClientPrincipalKinds(t *testing.T) {
	t.Parallel()

	legacy := auth.Client("read-write", "uuid-legacy")
	assert.Equal(t, "", legacy.Principal)
	assert.Equal(t, auth.PrincipalKind(""), legacy.PrincipalKind)

	humanDefault := auth.ClientWithPrincipal("read-write", "uuid-human", "olga", "")
	assert.Equal(t, "olga", humanDefault.Principal)
	assert.Equal(t, auth.PrincipalKindHuman, humanDefault.PrincipalKind)

	agent := auth.ClientWithPrincipal("read-write", "uuid-agent", "agent/codex", auth.PrincipalKindAgent)
	assert.Equal(t, "agent/codex", agent.Principal)
	assert.Equal(t, auth.PrincipalKindAgent, agent.PrincipalKind)

	service := auth.ClientWithPrincipal("read-only", "uuid-service", "service/ci", auth.PrincipalKindService)
	assert.Equal(t, "service/ci", service.Principal)
	assert.Equal(t, auth.PrincipalKindService, service.PrincipalKind)
}

func TestValidate_EmptyToken(t *testing.T) {
	t.Parallel()
	v := auth.NewValidator("master-secret", &stubStore{})

	id, err := v.Validate(context.Background(), "")

	assert.True(t, errors.Is(err, auth.ErrEmptyToken), "expected ErrEmptyToken")
	assert.Equal(t, auth.Identity{}, id)
}

func TestValidate_MasterMatch(t *testing.T) {
	t.Parallel()
	store := &stubStore{}
	v := auth.NewValidator("master-secret", store)

	id, err := v.Validate(context.Background(), "master-secret")

	require.NoError(t, err)
	assert.Equal(t, auth.RoleAdmin, id.Role)
	assert.Equal(t, auth.SourceMaster, id.Source)
	assert.Equal(t, "", id.KeycardID)
	assert.Equal(t, 0, store.prefixCalls,
		"NFR-2: master path MUST NOT touch token store")
}

func TestValidate_MasterEmpty_FallthroughToTier2(t *testing.T) {
	t.Parallel()
	// Server with auth disabled (master == "") should NOT match an empty
	// bearer; FR-1 storage disjointness is preserved by ErrEmptyToken
	// short-circuiting before tier-1.
	v := auth.NewValidator("", &stubStore{})

	_, err := v.Validate(context.Background(), "")

	assert.True(t, errors.Is(err, auth.ErrEmptyToken))
}

func TestValidate_RawGarbage_NoTier2Lookup(t *testing.T) {
	t.Parallel()
	store := &stubStore{}
	v := auth.NewValidator("master-secret", store)

	_, err := v.Validate(context.Background(), "not-a-token")

	assert.True(t, errors.Is(err, auth.ErrInvalidCredentials))
	assert.Equal(t, 0, store.prefixCalls,
		"prefix lookup MUST be skipped for tokens that don't match engram_<8hex>... shape")
}

func TestValidate_TooShortToken_NoTier2Lookup(t *testing.T) {
	t.Parallel()
	store := &stubStore{}
	v := auth.NewValidator("master-secret", store)

	_, err := v.Validate(context.Background(), "engram_abc") // 10 chars; need ≥ 15

	assert.True(t, errors.Is(err, auth.ErrInvalidCredentials))
	assert.Equal(t, 0, store.prefixCalls)
}

func TestValidate_ValidPrefix_NoMatchInStore(t *testing.T) {
	t.Parallel()
	store := &stubStore{
		byPrefix: map[string][]gormdb.APIToken{
			"abcd1234": {}, // empty candidate set for the prefix
		},
	}
	v := auth.NewValidator("master-secret", store)

	// 39-char total: TokenRawPrefix (7) + 32 hex chars.
	_, err := v.Validate(context.Background(), "engram_abcd1234000000000000000000000000")

	assert.True(t, errors.Is(err, auth.ErrInvalidCredentials))
	assert.Equal(t, 1, store.prefixCalls)
}

func TestValidate_ValidPrefix_NonRevokedMatch(t *testing.T) {
	t.Parallel()
	raw := "engram_deadbeef000000000000000000000001"
	keycard := makeKeycard(t, "uuid-1", raw, "read-write", false)
	store := &stubStore{
		byPrefix: map[string][]gormdb.APIToken{
			"deadbeef": {keycard},
		},
	}
	v := auth.NewValidator("master-secret", store)

	id, err := v.Validate(context.Background(), raw)

	require.NoError(t, err)
	assert.Equal(t, auth.RoleReadWrite, id.Role)
	assert.Equal(t, auth.SourceClient, id.Source)
	assert.Equal(t, "uuid-1", id.KeycardID)
	assert.Equal(t, "", id.Principal)
	assert.Equal(t, auth.PrincipalKind(""), id.PrincipalKind)
	assert.Equal(t, 1, store.prefixCalls,
		"NFR-2: exactly one prefix lookup")
}

func TestValidate_ValidPrefix_ReadOnlyScope(t *testing.T) {
	t.Parallel()
	raw := "engram_cafef00d000000000000000000000002"
	keycard := makeKeycard(t, "uuid-ro", raw, "read-only", false)
	store := &stubStore{byPrefix: map[string][]gormdb.APIToken{"cafef00d": {keycard}}}
	v := auth.NewValidator("master-secret", store)

	id, err := v.Validate(context.Background(), raw)

	require.NoError(t, err)
	assert.Equal(t, auth.RoleReadOnly, id.Role)
	assert.Equal(t, auth.SourceClient, id.Source)
}

func TestValidate_PrefixCollision_TwoCandidates_OneMatches(t *testing.T) {
	t.Parallel()
	rawA := "engram_facade00000000000000000000000aaa"
	rawB := "engram_facade00000000000000000000000bbb"
	cardA := makeKeycard(t, "uuid-a", rawA, "read-write", false)
	cardB := makeKeycard(t, "uuid-b", rawB, "read-only", false)
	store := &stubStore{
		byPrefix: map[string][]gormdb.APIToken{
			"facade00": {cardA, cardB}, // both share prefix
		},
	}
	v := auth.NewValidator("master-secret", store)

	idA, errA := v.Validate(context.Background(), rawA)
	idB, errB := v.Validate(context.Background(), rawB)

	require.NoError(t, errA)
	require.NoError(t, errB)
	assert.Equal(t, "uuid-a", idA.KeycardID)
	assert.Equal(t, "uuid-b", idB.KeycardID)
	assert.Equal(t, 2, store.prefixCalls)
}

func TestValidate_KeycardsWithSameRoleKeepDistinctPrincipals(t *testing.T) {
	t.Parallel()
	rawHuman := "engram_baadf00d000000000000000000000005"
	rawAgent := "engram_baadf00d000000000000000000000006"
	cardHuman := makePrincipalKeycard(t, "uuid-human", rawHuman, "read-write", "olga", auth.PrincipalKindHuman)
	cardAgent := makePrincipalKeycard(t, "uuid-agent", rawAgent, "read-write", "agent/codex", auth.PrincipalKindAgent)
	store := &stubStore{
		byPrefix: map[string][]gormdb.APIToken{
			"baadf00d": {cardHuman, cardAgent},
		},
	}
	v := auth.NewValidator("master-secret", store)

	idHuman, errHuman := v.Validate(context.Background(), rawHuman)
	idAgent, errAgent := v.Validate(context.Background(), rawAgent)

	require.NoError(t, errHuman)
	require.NoError(t, errAgent)
	assert.Equal(t, idHuman.Source, idAgent.Source)
	assert.Equal(t, idHuman.Role, idAgent.Role)
	assert.Equal(t, "olga", idHuman.Principal)
	assert.Equal(t, auth.PrincipalKindHuman, idHuman.PrincipalKind)
	assert.Equal(t, "agent/codex", idAgent.Principal)
	assert.Equal(t, auth.PrincipalKindAgent, idAgent.PrincipalKind)
	assert.NotEqual(t, idHuman.Principal, idAgent.Principal)
}

func TestValidate_KeycardPrincipalKindDefaultsToHuman(t *testing.T) {
	t.Parallel()
	raw := "engram_deafbeef000000000000000000000007"
	keycard := makeKeycard(t, "uuid-default-human", raw, "read-write", false)
	keycard.Principal = "oleg"
	store := &stubStore{byPrefix: map[string][]gormdb.APIToken{"deafbeef": {keycard}}}
	v := auth.NewValidator("master-secret", store)

	id, err := v.Validate(context.Background(), raw)

	require.NoError(t, err)
	assert.Equal(t, "oleg", id.Principal)
	assert.Equal(t, auth.PrincipalKindHuman, id.PrincipalKind)
}

func TestValidate_InvalidPrincipalKindFailsClosed(t *testing.T) {
	t.Parallel()
	raw := "engram_feedface000000000000000000000008"
	keycard := makeKeycard(t, "uuid-invalid-kind", raw, "read-write", false)
	keycard.Principal = "agent/unknown"
	keycard.PrincipalKind = "daemon"
	store := &stubStore{byPrefix: map[string][]gormdb.APIToken{"feedface": {keycard}}}
	v := auth.NewValidator("master-secret", store)

	_, err := v.Validate(context.Background(), raw)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected principal_kind")
}

func TestValidate_PrefixCollision_NeitherMatches(t *testing.T) {
	t.Parallel()
	otherRaw := "engram_facade00000000000000000000000fff"
	cardA := makeKeycard(t, "uuid-a", "engram_facade00000000000000000000000aaa", "read-write", false)
	cardB := makeKeycard(t, "uuid-b", "engram_facade00000000000000000000000bbb", "read-only", false)
	store := &stubStore{
		byPrefix: map[string][]gormdb.APIToken{"facade00": {cardA, cardB}},
	}
	v := auth.NewValidator("master-secret", store)

	_, err := v.Validate(context.Background(), otherRaw)

	assert.True(t, errors.Is(err, auth.ErrInvalidCredentials))
}

func TestValidate_StoreError_PropagatesAsAuthFailure(t *testing.T) {
	t.Parallel()
	store := &stubStore{returnErr: errors.New("db down")}
	v := auth.NewValidator("master-secret", store)

	_, err := v.Validate(context.Background(), "engram_abcd1234000000000000000000000000")

	require.Error(t, err)
	// Adapters expect ErrInvalidCredentials OR a wrapped store error; the
	// validator MUST NOT silently succeed on store failures.
	assert.False(t, errors.Is(err, auth.ErrEmptyToken))
}

func TestValidate_EmptyMaster_KeycardStillWorks(t *testing.T) {
	t.Parallel()
	// FR-1 + Validator doc string: when masterToken is empty, only Tier-2
	// validation runs. Verifies the documented "no operator key configured;
	// dashboard-issued keycards are the sole authentication path" mode.
	raw := "engram_d00dd00d000000000000000000000003"
	keycard := makeKeycard(t, "uuid-em", raw, "read-write", false)
	store := &stubStore{byPrefix: map[string][]gormdb.APIToken{"d00dd00d": {keycard}}}
	v := auth.NewValidator("", store)

	id, err := v.Validate(context.Background(), raw)

	require.NoError(t, err)
	assert.Equal(t, auth.SourceClient, id.Source)
	assert.Equal(t, auth.RoleReadWrite, id.Role)
	assert.Equal(t, "uuid-em", id.KeycardID)
}

func TestValidate_MasterAndKeycardDistinguishable(t *testing.T) {
	t.Parallel()
	// Verifies FR-1 storage disjointness from the validator's perspective:
	// the master path produces SourceMaster; the keycard path produces
	// SourceClient. There is no fallback that conflates them.
	rawClient := "engram_aaaa1111000000000000000000000004"
	keycard := makeKeycard(t, "uuid-c", rawClient, "read-write", false)
	store := &stubStore{byPrefix: map[string][]gormdb.APIToken{"aaaa1111": {keycard}}}
	v := auth.NewValidator("master-secret", store)

	idMaster, _ := v.Validate(context.Background(), "master-secret")
	idClient, _ := v.Validate(context.Background(), rawClient)

	assert.NotEqual(t, idMaster.Source, idClient.Source,
		"FR-1: master and client identities MUST be distinguishable by Source")
	assert.Equal(t, auth.SourceMaster, idMaster.Source)
	assert.Equal(t, auth.SourceClient, idClient.Source)
}

// Compile-time assertion: gormdb.TokenStore must satisfy auth.TokenStoreReader.
// If gormdb.TokenStore changes its FindByPrefix signature, this file fails to
// compile, surfacing the contract drift immediately.
var _ auth.TokenStoreReader = (*gormdb.TokenStore)(nil)
