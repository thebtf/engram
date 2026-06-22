package principalmemory

import "testing"

func TestPrincipalAccessPolicy_PrivacyAndAuditDecisions(t *testing.T) {
	policy := PrincipalAccessPolicy{}

	tests := []struct {
		name          string
		caller        PrincipalRef
		target        PrincipalRef
		visibility    string
		callerIsAdmin bool
		wantAllowed   bool
		wantAudit     bool
		wantReason    string
	}{
		{
			name:        "self private principal is allowed without audit",
			caller:      PrincipalRef{Principal: "agent/alice", PrincipalKind: "agent"},
			target:      PrincipalRef{Principal: "agent/alice", PrincipalKind: "agent"},
			visibility:  "private",
			wantAllowed: true,
			wantAudit:   false,
		},
		{
			name:        "shared memory attributed to another principal is visible without audit",
			caller:      PrincipalRef{Principal: "agent/bob", PrincipalKind: "agent"},
			target:      PrincipalRef{Principal: "agent/alice", PrincipalKind: "agent"},
			visibility:  "shared",
			wantAllowed: true,
			wantAudit:   false,
		},
		{
			name:        "non admin cross private principal is denied",
			caller:      PrincipalRef{Principal: "agent/bob", PrincipalKind: "agent"},
			target:      PrincipalRef{Principal: "agent/alice", PrincipalKind: "agent"},
			visibility:  "private",
			wantAllowed: false,
			wantAudit:   false,
			wantReason:  "cross_principal_private_denied",
		},
		{
			name:          "admin cross private principal requires audit",
			caller:        PrincipalRef{Principal: "operator", PrincipalKind: "human"},
			target:        PrincipalRef{Principal: "agent/alice", PrincipalKind: "agent"},
			visibility:    "private",
			callerIsAdmin: true,
			wantAllowed:   true,
			wantAudit:     true,
		},
		{
			name:        "invalid principal kind is rejected",
			caller:      PrincipalRef{Principal: "agent/bob", PrincipalKind: "agent"},
			target:      PrincipalRef{Principal: "agent/alice", PrincipalKind: "robot"},
			visibility:  "shared",
			wantAllowed: false,
			wantAudit:   false,
			wantReason:  "invalid_principal_kind",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := policy.Decide(PrincipalAccessRequest{
				Caller:        tc.caller,
				Target:        tc.target,
				Visibility:    tc.visibility,
				CallerIsAdmin: tc.callerIsAdmin,
			})

			if got.Allowed != tc.wantAllowed {
				t.Fatalf("Allowed = %v, want %v", got.Allowed, tc.wantAllowed)
			}
			if got.AuditRequired != tc.wantAudit {
				t.Fatalf("AuditRequired = %v, want %v", got.AuditRequired, tc.wantAudit)
			}
			if tc.wantReason != "" && got.Reason != tc.wantReason {
				t.Fatalf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}
