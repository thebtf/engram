package proxy

import (
	"strings"
	"testing"
)

// The historical test name is retained for Sonar continuity, but the matrix
// belongs at the pure normalization boundary; real Git integration is covered
// separately by the resolver and linked-worktree tests.
func TestResolveProjectIdentityV2_FencesAuthorityUserinfoWithoutChangingScpOrLocalRemotes(t *testing.T) {
	tests := []struct {
		name       string
		remote     string
		wantRemote string
		wantErr    bool
	}{
		{name: "malformed network authority", remote: "//fixture-user:fixture-credential@example.invalid/%zz", wantErr: true},
		{name: "scp-like remote", remote: "fixture-user@example.invalid:repo.git", wantRemote: "fixture-user@example.invalid:repo.git"},
		{name: "local path remote", remote: "./fixture@directory:repo.git", wantRemote: "./fixture@directory:repo.git"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remote, err := normalizeGitRemote(tt.remote)
			if tt.wantErr {
				if err == nil || remote != "" || strings.Contains(err.Error(), "fixture-credential") {
					t.Fatal("authority userinfo was not rejected safely")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize remote: %v", err)
			}
			if remote != tt.wantRemote {
				t.Fatalf("remote=%q, want %q", remote, tt.wantRemote)
			}
		})
	}
}
