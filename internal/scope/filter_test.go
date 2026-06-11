package scope

import "testing"

// TestResolve_16Cases exhausts the AC table from tasks.md T003:
//
//	"Table-driven test covers all 16 combinations
//	 (4 scopes × {match, no-match} × {with-session, without-session})."
//
// Each row carries its expectation.
//
// T003b (AMEND 2026-05-25) widened private-scope to honor workstation
// identity per spec FR-F1 AMEND. To keep the original 16-case table covering
// the session dimension faithfully, every row populates BOTH caller and
// memorySource with the same WorkstationID — so the workstation gate is
// neutralised here and the session dimension is what drives the outcome.
// The workstation dimension itself is exercised by TestResolve_T003b_Workstation
// below.
//
// "match" means caller.SessionID appears in memorySource.Sessions.
// "no-match" means it does not.
// "with-session" means caller.SessionID is non-empty.
// "without-session" means caller.SessionID is the empty string.
//
// For private + without-session: T003 narrow interpretation said FALSE; T003b
// widening says TRUE (workstation-only-suffices branch fires when caller has
// no session_id but workstation matches). The expectations below reflect
// T003b's widened contract.
func TestResolve_16Cases(t *testing.T) {
	const knownSession = "sess-abc-123"
	const knownWorkstation = "ws-test-001"

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

		// 'shared' — currently same surface as global (TG1).
		{"shared / match / with-session", ScopeShared, true, true, true},
		{"shared / no-match / with-session", ScopeShared, true, false, true},
		{"shared / match / without-session", ScopeShared, false, true, true},
		{"shared / no-match / without-session", ScopeShared, false, false, true},

		// 'project' — project boundary enforced upstream; Resolve trusts it.
		{"project / match / with-session", ScopeProject, true, true, true},
		{"project / no-match / with-session", ScopeProject, true, false, true},
		{"project / match / without-session", ScopeProject, false, true, true},
		{"project / no-match / without-session", ScopeProject, false, false, true},

		// 'private' — workstation gate always neutralised here (both sides
		// carry knownWorkstation); session dimension drives the outcome.
		{"private / match / with-session", ScopePrivate, true, true, true},         // session match -> TRUE
		{"private / no-match / with-session", ScopePrivate, true, false, false},    // session-required + miss -> FALSE
		{"private / match / without-session", ScopePrivate, false, true, true},     // workstation-only-suffices -> TRUE (T003b widening)
		{"private / no-match / without-session", ScopePrivate, false, false, true}, // workstation-only-suffices -> TRUE (T003b widening)
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			caller := KeycardContext{WorkstationID: knownWorkstation}
			if tc.withSession {
				caller.SessionID = knownSession
			}

			meta := SourceMeta{WorkstationID: knownWorkstation}
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
				t.Errorf("Resolve(caller={Ws:%q, Sess:%q}, scope=%q, source={Ws:%q, Sess:%v}) = %v, want %v",
					caller.WorkstationID, caller.SessionID, tc.scope,
					meta.WorkstationID, meta.Sessions, got, tc.want)
			}
		})
	}
}

// TestResolve_T003b_Workstation exercises the workstation-identity dimension
// that T003b (AMEND 2026-05-25) added to ScopePrivate. The 16-case table
// above neutralises this dimension by keeping both sides on the same
// workstation; this test does the opposite — fix the session dimension and
// vary workstation_id state.
//
// Spec FR-F1 AMEND 2026-05-25 ScopePrivate decision tree:
//  1. caller.WorkstationID empty             -> false
//  2. memorySource.WorkstationID empty       -> false
//  3. workstation mismatch                   -> false
//  4. workstation match + session-required   -> session membership check
//  5. workstation match + workstation-only   -> true (suffices)
func TestResolve_T003b_Workstation(t *testing.T) {
	const ws = "ws-target"
	const sess = "sess-target"

	cases := []struct {
		name       string
		callerWs   string
		callerSess string
		sourceWs   string
		sourceSess []string
		want       bool
	}{
		{"empty caller workstation -> false", "", sess, ws, []string{sess}, false},
		{"empty memory workstation -> false", ws, sess, "", []string{sess}, false},
		{"workstation mismatch + with-session -> false", "ws-other", sess, ws, []string{sess}, false},
		{"workstation mismatch + without-session -> false", "ws-other", "", ws, []string{sess}, false},
		{"workstation match + with-session in sessions -> true", ws, sess, ws, []string{sess}, true},
		{"workstation match + with-session not in sessions -> false", ws, sess, ws, []string{"sess-x"}, false},
		{"workstation match + without-session (workstation-only-suffices) -> true", ws, "", ws, []string{sess}, true},
		{"workstation match + without-session + empty memory sessions -> true (workstation-only)", ws, "", ws, []string{}, true},
		{"both empty workstations -> false", "", "", "", []string{}, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			caller := KeycardContext{WorkstationID: tc.callerWs, SessionID: tc.callerSess}
			source := SourceMeta{WorkstationID: tc.sourceWs, Sessions: tc.sourceSess}
			got := Resolve(caller, ScopePrivate, source)
			if got != tc.want {
				t.Errorf("Resolve(caller={Ws:%q,Sess:%q}, private, source={Ws:%q,Sess:%v}) = %v, want %v",
					tc.callerWs, tc.callerSess, tc.sourceWs, tc.sourceSess, got, tc.want)
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

// TestResolve_PrivateSession_MultipleMatches verifies the session-membership
// check scans the full Sessions slice (regression guard against an
// early-return-on-first-element bug). Updated for T003b: populates matching
// WorkstationID on both sides so the workstation gate is satisfied, leaving
// the session dimension as the regression target.
func TestResolve_PrivateSession_MultipleMatches(t *testing.T) {
	const ws = "ws-target"
	caller := KeycardContext{WorkstationID: ws, SessionID: "sess-target"}
	meta := SourceMeta{WorkstationID: ws, Sessions: []string{"sess-a", "sess-b", "sess-target", "sess-c"}}
	if !Resolve(caller, ScopePrivate, meta) {
		t.Error("Resolve must scan full Sessions slice to find matching SessionID")
	}
}
