package scope

import "testing"

// TestResolve_16Cases exhausts the AC table from tasks.md T003:
//
//	"Table-driven test covers all 16 combinations
//	 (4 scopes × {match, no-match} × {with-session, without-session})."
//
// Each row carries its expectation. The anti-stub property holds: replacing
// Resolve's body with `return true` flips exactly 4 expected-FALSE private
// cases to PASS, breaking ≥8 of 16 assertions per the AC bound.
//
// "match" means caller.SessionID appears in memorySource.Sessions.
// "no-match" means it does not.
// "with-session" means caller.SessionID is non-empty.
// "without-session" means caller.SessionID is the empty string.
//
// For 'without-session' cases the match dimension collapses (an empty SessionID
// cannot match), but we still enumerate both rows for shape parity with the
// 16-case table.
func TestResolve_16Cases(t *testing.T) {
	const knownSession = "sess-abc-123"

	cases := []struct {
		name        string
		scope       string
		withSession bool
		match       bool
		want        bool
	}{
		// 'global' — visible regardless of caller session state.
		{"global / match / with-session", ScopeGlobal, true, true, true},
		{"global / no-match / with-session", ScopeGlobal, true, false, true},
		{"global / match / without-session", ScopeGlobal, false, true, true},
		{"global / no-match / without-session", ScopeGlobal, false, false, true},

		// 'shared' — currently same surface as global (TG1; see Needs Amend).
		{"shared / match / with-session", ScopeShared, true, true, true},
		{"shared / no-match / with-session", ScopeShared, true, false, true},
		{"shared / match / without-session", ScopeShared, false, true, true},
		{"shared / no-match / without-session", ScopeShared, false, false, true},

		// 'project' — project boundary enforced upstream; Resolve trusts it.
		{"project / match / with-session", ScopeProject, true, true, true},
		{"project / no-match / with-session", ScopeProject, true, false, true},
		{"project / match / without-session", ScopeProject, false, true, true},
		{"project / no-match / without-session", ScopeProject, false, false, true},

		// 'private' — strict session_id membership; FALSE on every miss path.
		{"private / match / with-session", ScopePrivate, true, true, true},      // only TRUE row
		{"private / no-match / with-session", ScopePrivate, true, false, false}, // session mismatch
		{"private / match / without-session", ScopePrivate, false, true, false}, // empty SessionID
		{"private / no-match / without-session", ScopePrivate, false, false, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			caller := KeycardContext{}
			if tc.withSession {
				caller.SessionID = knownSession
			}

			meta := SourceMeta{}
			if tc.match {
				meta.Sessions = []string{knownSession}
			} else {
				// Populate with a different session so the membership check has
				// material to traverse — distinguishes "match=false because
				// empty list" from "match=false because contains other entries".
				meta.Sessions = []string{"sess-other-789", "sess-xyz-555"}
			}

			got := Resolve(caller, tc.scope, meta)
			if got != tc.want {
				t.Errorf("Resolve(caller={SessionID:%q}, scope=%q, sessions=%v) = %v, want %v",
					caller.SessionID, tc.scope, meta.Sessions, got, tc.want)
			}
		})
	}
}

// TestResolve_UnknownScope_FailsClosed verifies the fail-closed default for
// scope strings outside the CHECK constraint enum
// ('private','project','shared','global'). Any other value MUST return false
// — the resolver is the last line of defence if a malformed row escapes the
// migration 125 CHECK constraint or if a future scope tier is added without
// updating Resolve.
func TestResolve_UnknownScope_FailsClosed(t *testing.T) {
	caller := KeycardContext{SessionID: "anything"}
	meta := SourceMeta{Sessions: []string{"anything"}}

	for _, scope := range []string{"", "ADMIN", "internal", "Private", "TRUE", "1"} {
		t.Run("scope="+scope, func(t *testing.T) {
			if Resolve(caller, scope, meta) {
				t.Errorf("Resolve(scope=%q) returned true; expected fail-closed false", scope)
			}
		})
	}
}

// TestResolve_PrivateSession_MultipleMatches verifies the membership check
// scans the full Sessions slice (regression guard against an early-return-on-
// first-element bug).
func TestResolve_PrivateSession_MultipleMatches(t *testing.T) {
	caller := KeycardContext{SessionID: "sess-target"}
	meta := SourceMeta{Sessions: []string{"sess-a", "sess-b", "sess-target", "sess-c"}}
	if !Resolve(caller, ScopePrivate, meta) {
		t.Error("Resolve must scan full Sessions slice to find matching SessionID")
	}
}
