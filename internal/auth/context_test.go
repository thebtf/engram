package auth_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/thebtf/engram/internal/auth"
)

func TestWithIdentity_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := auth.WithIdentity(context.Background(), auth.Client("read-write", "uuid-1"))

	got, ok := auth.IdentityFrom(ctx)

	assert.True(t, ok)
	assert.Equal(t, auth.RoleReadWrite, got.Role)
	assert.Equal(t, auth.SourceClient, got.Source)
}

func TestIdentityFrom_NoIdentity(t *testing.T) {
	t.Parallel()

	id, ok := auth.IdentityFrom(context.Background())

	assert.False(t, ok)
	assert.Equal(t, auth.Identity{}, id)
}

func TestRoleFrom_AdminMaster(t *testing.T) {
	t.Parallel()
	ctx := auth.WithIdentity(context.Background(), auth.Admin())

	assert.Equal(t, "admin", auth.RoleFrom(ctx))
	assert.Equal(t, "master", auth.SourceFrom(ctx))
}

func TestRoleFrom_NoIdentity(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "", auth.RoleFrom(context.Background()))
	assert.Equal(t, "", auth.SourceFrom(context.Background()))
}

func TestWithIdentity_SessionAdmin(t *testing.T) {
	t.Parallel()
	ctx := auth.WithIdentity(context.Background(), auth.Session("admin"))

	id, ok := auth.IdentityFrom(ctx)
	assert.True(t, ok)
	assert.True(t, id.IsSessionAdmin(), "FR-6 / C4: session admin must satisfy IsSessionAdmin")
	assert.True(t, id.IsAdmin())
}

func TestIdentity_IsSessionAdmin_RejectsMaster(t *testing.T) {
	t.Parallel()
	id := auth.Admin()

	assert.False(t, id.IsSessionAdmin(),
		"FR-6 / C4: operator-key admin must NOT satisfy IsSessionAdmin (issuance is browser-only)")
	assert.True(t, id.IsAdmin(),
		"operator-key admin still has admin role for non-issuance gates")
}

func TestIdentity_IsSessionAdmin_RejectsClient(t *testing.T) {
	t.Parallel()
	id := auth.Client("read-write", "uuid-x")

	assert.False(t, id.IsSessionAdmin())
	assert.False(t, id.IsAdmin(), "read-write client is not admin")
}

func TestIdentity_IsSessionAdmin_RejectsSessionNonAdmin(t *testing.T) {
	t.Parallel()
	// Hypothetical future: a session-cookie user with non-admin role from
	// the users table. Issuance must still be denied.
	id := auth.Session("user")

	assert.False(t, id.IsSessionAdmin())
}

// TestIdentity_WorkstationID_T003b verifies the WorkstationID accessor added
// by engram vNext Milestone F T003b (AMEND 2026-05-25). Per spec FR-F1 AMEND
// 2026-05-25 contract:
//
//   - SourceClient bearers return their KeycardID (per-workstation keycard)
//   - SourceMaster returns "" (system-wide, not workstation-bound)
//   - SourceSession returns "" (user-bound, not workstation-bound)
//
// The accessor is consumed by scope.Resolve ScopePrivate to enforce the
// keycard identity invariant. Empty WorkstationID is treated as fail-closed
// by the resolver (cannot match against any memory's source_workstation_id).
func TestIdentity_WorkstationID_T003b(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		id   auth.Identity
		want string
	}{
		{
			name: "SourceClient returns KeycardID",
			id:   auth.Client("read-write", "kc-uuid-abc-123"),
			want: "kc-uuid-abc-123",
		},
		{
			name: "SourceClient with admin role still returns KeycardID",
			id:   auth.Client("admin", "kc-uuid-admin-456"),
			want: "kc-uuid-admin-456",
		},
		{
			name: "SourceMaster returns empty",
			id:   auth.Admin(),
			want: "",
		},
		{
			name: "SourceSession admin returns empty",
			id:   auth.Session("admin"),
			want: "",
		},
		{
			name: "SourceSession non-admin returns empty",
			id:   auth.Session("user"),
			want: "",
		},
		{
			name: "Zero-value Identity returns empty",
			id:   auth.Identity{},
			want: "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.id.WorkstationID()
			assert.Equal(t, tc.want, got,
				"Identity.WorkstationID() must derive from Source + KeycardID per spec FR-F1 AMEND 2026-05-25")
		})
	}
}
