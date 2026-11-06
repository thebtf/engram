package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auditcontext"
	authpkg "github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	"golang.org/x/crypto/bcrypt"
)

type session535Browser struct {
	cookie *http.Cookie
	agent  string
	status string
}

type session535Fixture struct {
	env      *authLifecycleEnv
	service  *Service
	admin    *gormdb.User
	operator *gormdb.User
	browsers []session535Browser
}

// Reuse the UUID-schema PostgreSQL fixture; never point these tests at user data.
// Legacy rows deliberately use only the pre-fix persisted columns, not a guessed
// future handle field or the session-creation code whose compatibility is at issue.
func openSession535Fixture(t *testing.T) *session535Fixture {
	t.Helper()
	t.Setenv("ENGRAM_AUTH_DISABLED", "false")
	env := openAuthLifecycleEnv(t)
	password := uuid.NewString()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	session535NoError(t, err)
	admin, err := env.users.CreateUser("session535-admin@example.test", string(hash), gormdb.DashboardRoleAdmin)
	session535NoError(t, err)
	operator, err := env.users.CreateUser("session535-operator@example.test", string(hash), gormdb.DashboardRoleOperator)
	session535NoError(t, err)
	f := &session535Fixture{env: env, service: authBoundaryRouter(t, env.handlers), admin: admin, operator: operator}
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, seed := range []struct {
		agent   string
		userID  int64
		expired bool
		revoked bool
	}{
		{agent: "session535-admin", userID: admin.ID},
		{agent: "session535-legacy", userID: operator.ID},
		{agent: "session535-expired", userID: operator.ID, expired: true},
		{agent: "session535-revoked", userID: operator.ID, revoked: true},
	} {
		credential := session535Credential(t)
		expiresAt := now.Add(2 * time.Hour)
		status := "active"
		if seed.expired {
			expiresAt = now.Add(-time.Hour)
			status = "expired"
		}
		row := map[string]any{
			"id": credential, "user_id": seed.userID, "created_at": now.Add(-time.Hour),
			"expires_at": expiresAt, "user_agent": seed.agent, "remote_addr": "192.0.2.53:5350",
		}
		if seed.revoked {
			row["revoked_at"] = now.Add(-time.Minute)
			row["revoked_by"] = admin.ID
			row["revocation_reason"] = "session535 historical revocation"
			status = "revoked"
		}
		session535NoError(t, env.store.DB.Table("sessions").Create(row).Error)
		f.browsers = append(f.browsers, session535Browser{
			cookie: &http.Cookie{Name: authSessionCookieName, Value: credential}, agent: seed.agent, status: status,
		})
	}
	payload := session535JSON(t, map[string]string{"email": operator.Email, "password": password})
	request := httptest.NewRequest(http.MethodPost, "http://localhost:37777/api/auth/user-login", bytes.NewReader(payload))
	request.RemoteAddr = "192.0.2.53:5350"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "session535-issued")
	recorder := httptest.NewRecorder()
	f.service.router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, "synthetic password login must issue a browser session")
	var issued *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == authSessionCookieName {
			issued = cookie
		}
	}
	require.True(t, issued != nil && issued.Value != "", "login must issue its credential only as a cookie")
	require.True(t, issued.HttpOnly && issued.Path == "/" && issued.MaxAge > 0, "login cookie lifecycle must survive")
	require.Equal(t, http.SameSiteLaxMode, issued.SameSite)
	f.browsers = append(f.browsers, session535Browser{cookie: issued, agent: "session535-issued", status: "active"})
	session535NoCredentials(t, recorder.Body.Bytes(), f.credentials()...)
	return f
}

// Assertions below never pass credentials, cookies, raw JSON or DB errors to
// testify: its ordinary operand/error diffs could disclose the regression itself.
func session535NoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal("synthetic session fixture operation failed; sensitive details omitted")
	}
}

func session535Credential(t *testing.T) string {
	t.Helper()
	var value [32]byte
	_, err := rand.Read(value[:])
	session535NoError(t, err)
	return hex.EncodeToString(value[:])
}

func session535JSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	session535NoError(t, err)
	return encoded
}

func session535Decode(t *testing.T, body []byte, target any) {
	t.Helper()
	session535NoError(t, json.Unmarshal(body, target))
}

func session535NoCredentials(t *testing.T, body []byte, credentials ...string) {
	t.Helper()
	for _, credential := range credentials {
		require.True(t, credential != "", "credential absence checks require a nonempty fixture")
		assert.False(t, bytes.Contains(body, []byte(credential)), "public projection contains a browser credential; values omitted")
	}
}

func session535Request(handler http.Handler, method, path string, cookie *http.Cookie, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://localhost:37777"+path, bytes.NewReader(body))
	request.RemoteAddr = "192.0.2.53:5350"
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Origin", "http://localhost:37777")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func (f *session535Fixture) credentials() []string {
	credentials := make([]string, 0, len(f.browsers))
	for _, browser := range f.browsers {
		credentials = append(credentials, browser.cookie.Value)
	}
	return credentials
}

func (f *session535Fixture) browser(agent string) session535Browser {
	for _, browser := range f.browsers {
		if browser.agent == agent {
			return browser
		}
	}
	panic("unknown synthetic browser label")
}

func session535Sessions(t *testing.T, f *session535Fixture, includeInactive bool) []gormdb.AccessSessionView {
	t.Helper()
	path := "/api/access/sessions?limit=100"
	if includeInactive {
		path += "&include_revoked=true"
	}
	recorder := session535Request(f.service.router, http.MethodGet, path, f.browser("session535-admin").cookie, nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	session535NoCredentials(t, recorder.Body.Bytes(), f.credentials()...)
	var payload struct {
		Sessions []gormdb.AccessSessionView `json:"sessions"`
	}
	session535Decode(t, recorder.Body.Bytes(), &payload)
	return payload.Sessions
}

func session535FindSession(t *testing.T, rows []gormdb.AccessSessionView, agent string) gormdb.AccessSessionView {
	t.Helper()
	var found []gormdb.AccessSessionView
	for _, row := range rows {
		if row.UserAgent == agent {
			found = append(found, row)
		}
	}
	require.Equal(t, 1, len(found), "each synthetic browser must remain individually manageable")
	require.True(t, found[0].ID != "", "a session needs a stable public selector")
	return found[0]
}

func session535Me(t *testing.T, f *session535Fixture, cookie *http.Cookie, want int) {
	t.Helper()
	recorder := session535Request(f.service.router, http.MethodGet, "/api/auth/me", cookie, nil)
	assert.Equal(t, want, recorder.Code, "cookie authentication must reflect only that session's lifecycle")
	session535NoCredentials(t, recorder.Body.Bytes(), f.credentials()...)
	if want == http.StatusOK && recorder.Code == http.StatusOK {
		var payload struct {
			Authenticated bool `json:"authenticated"`
			User          struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		session535Decode(t, recorder.Body.Bytes(), &payload)
		assert.True(t, payload.Authenticated && payload.User.ID > 0, "real browser identity must survive")
	}
}

func TestSession535AuthSessionJSONOmitsBrowserCredentials(t *testing.T) {
	f := openSession535Fixture(t)
	for _, agent := range []string{"session535-legacy", "session535-issued"} {
		t.Run(agent, func(t *testing.T) {
			session, err := f.env.sessions.GetSession(f.browser(agent).cookie.Value)
			session535NoError(t, err)
			require.True(t, session != nil, "a valid browser cookie must still resolve its session")
			session535NoCredentials(t, session535JSON(t, session), f.credentials()...)
		})
	}
}

func TestSession535BrowserSessionHandlesAreStableNonCredentials(t *testing.T) {
	f := openSession535Fixture(t)
	active := session535Sessions(t, f, false)
	all := session535Sessions(t, f, true)
	require.Equal(t, 3, len(active))
	require.Equal(t, len(f.browsers), len(all))
	repeated := session535Sessions(t, f, true)
	seen := make(map[string]bool)
	for _, browser := range f.browsers {
		t.Run(browser.agent, func(t *testing.T) {
			row := session535FindSession(t, all, browser.agent)
			assert.Equal(t, browser.status, row.Status)
			assert.True(t, row.ID != browser.cookie.Value, "management selector must not be the cookie credential")
			assert.False(t, seen[row.ID], "different browser sessions must not collapse to one public handle")
			seen[row.ID] = true
			assert.True(t, row.ID == session535FindSession(t, repeated, browser.agent).ID, "repeated reads must retain correlation")
			assert.Equal(t, "192.0.2.53:5350", row.RemoteAddr)
			assert.False(t, row.CreatedAt.IsZero() || row.ExpiresAt.IsZero(), "session lifecycle metadata must survive")
			want := http.StatusUnauthorized
			if browser.status == "active" {
				want = http.StatusOK
				assert.True(t, row.ID == session535FindSession(t, active, browser.agent).ID, "active-only projection must use the same handle")
			}
			session535Me(t, f, browser.cookie, want)
			session535Me(t, f, &http.Cookie{Name: authSessionCookieName, Value: row.ID}, http.StatusUnauthorized)
		})
	}
	path := "/api/access/users/" + strconv.FormatInt(f.operator.ID, 10)
	recorder := session535Request(f.service.router, http.MethodGet, path, f.browser("session535-admin").cookie, nil)
	require.Equal(t, http.StatusOK, recorder.Code)
	session535NoCredentials(t, recorder.Body.Bytes(), f.credentials()...)
	var detail gormdb.AccessUserDrilldown
	session535Decode(t, recorder.Body.Bytes(), &detail)
	require.True(t, detail.User != nil && detail.User.ID == f.operator.ID)
	require.Equal(t, 4, len(detail.Sessions), "drilldown includes active, expired and revoked sessions for only this user")
	for _, row := range detail.Sessions {
		assert.Equal(t, f.operator.ID, row.UserID)
		assert.True(t, row.ID == session535FindSession(t, all, row.UserAgent).ID, "list and drilldown must share a public selector")
	}
}

func TestSession535RevokeUsesOnlyPublicHandle(t *testing.T) {
	f := openSession535Fixture(t)
	rows := session535Sessions(t, f, true)
	var before int64
	session535NoError(t, f.env.store.DB.Model(&gormdb.AuditLogEntry{}).Where("action = ?", "auth_session_revoked").Count(&before).Error)
	for _, agent := range []string{"session535-legacy", "session535-issued"} {
		t.Run(agent+"_rejects_cookie_selector", func(t *testing.T) {
			browser := f.browser(agent)
			// Exercise the real handler/Chi selector boundary without putting a
			// credential in the outer request logger's URL, even on the RED base.
			request := requestWithRoute(http.MethodPost, "/api/access/sessions/fixture-selector/revoke", []byte(`{}`), authpkg.SessionForBrowserUser(f.admin.Role, f.admin.ID), map[string]string{"id": browser.cookie.Value})
			request.AddCookie(f.browser("session535-admin").cookie)
			recorder := httptest.NewRecorder()
			f.env.handlers.handleAccessRevokeSession(recorder, request)
			assert.True(t, recorder.Code == http.StatusBadRequest || recorder.Code == http.StatusNotFound, "cookie credential is not a public revoke selector")
			session535NoCredentials(t, recorder.Body.Bytes(), f.credentials()...)
			session535Me(t, f, browser.cookie, http.StatusOK)
		})
	}
	var after int64
	session535NoError(t, f.env.store.DB.Model(&gormdb.AuditLogEntry{}).Where("action = ?", "auth_session_revoked").Count(&after).Error)
	assert.Equal(t, before, after, "rejected credential selectors must not mutate lifecycle or audit")
	for _, agent := range []string{"session535-legacy", "session535-issued"} {
		t.Run(agent+"_revokes_by_handle", func(t *testing.T) {
			browser := f.browser(agent)
			row := session535FindSession(t, rows, agent)
			// Do not let a failing public-ID assertion place the old credential
			// in an actual URL; the previous subtest already supplies that RED.
			for _, credential := range f.credentials() {
				require.False(t, bytes.Contains([]byte(row.ID), []byte(credential)), "revoke URL requires a noncredential public handle")
			}
			path := "/api/access/sessions/" + url.PathEscape(row.ID) + "/revoke"
			reason := "session535 selected revocation " + agent
			for range 2 {
				recorder := session535Request(f.service.router, http.MethodPost, path, f.browser("session535-admin").cookie, session535JSON(t, map[string]string{"reason": reason}))
				require.Equal(t, http.StatusOK, recorder.Code, "handle revocation must remain idempotent")
				session535NoCredentials(t, recorder.Body.Bytes(), f.credentials()...)
				var payload struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				}
				session535Decode(t, recorder.Body.Bytes(), &payload)
				assert.True(t, payload.ID == row.ID && payload.Status == "ok", "revoke response must preserve the public handle")
			}
			session535Me(t, f, browser.cookie, http.StatusUnauthorized)
			session535Me(t, f, f.browser("session535-admin").cookie, http.StatusOK)
			if agent == "session535-legacy" {
				session535Me(t, f, f.browser("session535-issued").cookie, http.StatusOK)
			}
			updated := session535FindSession(t, session535Sessions(t, f, true), agent)
			assert.True(t, updated.ID == row.ID, "revocation must not change the public handle")
			assert.Equal(t, "revoked", updated.Status)
			require.NotNil(t, updated.RevokedAt)
			require.NotNil(t, updated.RevokedBy)
			assert.Equal(t, f.admin.ID, *updated.RevokedBy)
			assert.Equal(t, reason, updated.RevocationReason)
			var events []gormdb.AuditLogEntry
			session535NoError(t, f.env.store.DB.Where("action = ? AND reason = ?", "auth_session_revoked", reason).Find(&events).Error)
			require.Equal(t, 1, len(events), "idempotent revoke must write exactly one lifecycle event")
			session535NoCredentials(t, session535JSON(t, events), f.credentials()...)
			for _, state := range []*json.RawMessage{events[0].BeforeState, events[0].AfterState} {
				require.NotNil(t, state)
				var decoded map[string]any
				session535Decode(t, *state, &decoded)
				assert.True(t, decoded["session_id"] == row.ID, "new revoke audit must correlate through the same safe handle")
			}
		})
	}
	request := requestWithRoute(http.MethodPost, "/api/access/sessions/absent-fixture/revoke", []byte(`{}`), authpkg.SessionForBrowserUser(f.admin.Role, f.admin.ID), map[string]string{"id": uuid.NewString()})
	request.AddCookie(f.browser("session535-admin").cookie)
	recorder := httptest.NewRecorder()
	f.env.handlers.handleAccessRevokeSession(recorder, request)
	assert.Equal(t, http.StatusNotFound, recorder.Code, "unknown public handles retain not-found semantics")
}

func TestSession535HistoricalAuthAuditProjectionRetainsCorrelation(t *testing.T) {
	f := openSession535Fixture(t)
	rows := session535Sessions(t, f, true)
	orphanCredential := session535Credential(t)
	credentials := append(f.credentials(), orphanCredential)
	timestamp := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	var historical []gormdb.AuditLogEntry
	for i, agent := range []string{"session535-legacy", "session535-expired", "session535-revoked", "session535-orphan"} {
		credential := orphanCredential
		if agent != "session535-orphan" {
			credential = f.browser(agent).cookie.Value
		}
		after := map[string]any{"session_id": credential, "user_id": f.operator.ID, "user_agent": agent, "remote_addr": "192.0.2.53:5350"}
		var before *json.RawMessage
		action := "auth_login"
		if agent == "session535-revoked" {
			action = "auth_session_revoked"
			raw := json.RawMessage(session535JSON(t, after))
			before = &raw
			after = map[string]any{"session_id": credential, "revoked_by": f.admin.ID, "reason": "session535 historical revocation"}
		}
		rawAfter := json.RawMessage(session535JSON(t, after))
		entry := gormdb.AuditLogEntry{
			Action: action, Actor: f.operator.Email, Reason: "session535 historical " + agent,
			BeforeState: before, AfterState: &rawAfter, CreatedAt: timestamp.Add(time.Duration(i) * time.Second),
		}
		// Direct persistence models already-written history, bypassing any
		// eventual new-event sanitizer in LogAccessEvent or the login handler.
		session535NoError(t, f.env.store.DB.Create(&entry).Error)
		historical = append(historical, entry)
	}
	var snapshot []gormdb.AuditLogEntry
	session535NoError(t, f.env.store.DB.Order("id").Find(&snapshot).Error)
	correlations := make(map[int64]string)
	for _, projection := range []struct{ name, path string }{
		{name: "audit_list", path: "/api/access/log?limit=100"},
		{name: "user_drilldown", path: "/api/access/users/" + strconv.FormatInt(f.operator.ID, 10) + "?limit=100"},
		{name: "audit_list_repeat", path: "/api/access/log?limit=100"},
		{name: "user_drilldown_repeat", path: "/api/access/users/" + strconv.FormatInt(f.operator.ID, 10) + "?limit=100"},
	} {
		t.Run(projection.name, func(t *testing.T) {
			recorder := session535Request(f.service.router, http.MethodGet, projection.path, f.browser("session535-admin").cookie, nil)
			require.Equal(t, http.StatusOK, recorder.Code)
			session535NoCredentials(t, recorder.Body.Bytes(), credentials...)
			var payload struct {
				Entries []gormdb.AccessAuditEntry `json:"entries"`
				Audit   []gormdb.AccessAuditEntry `json:"audit"`
			}
			session535Decode(t, recorder.Body.Bytes(), &payload)
			entries := payload.Entries
			if entries == nil {
				entries = payload.Audit
			}
			require.Equal(t, len(historical)+1, len(entries), "history and the new password-login event must remain visible")
			for i, old := range historical {
				var matches []gormdb.AccessAuditEntry
				for _, entry := range entries {
					if entry.ID == old.ID {
						matches = append(matches, entry)
					}
				}
				require.Equal(t, 1, len(matches), "sanitization must not delete or duplicate historical events")
				entry := matches[0]
				assert.Equal(t, old.Action, entry.Action)
				assert.Equal(t, old.Actor, entry.Actor)
				assert.Equal(t, old.Reason, entry.Reason)
				assert.True(t, entry.CreatedAt.Equal(old.CreatedAt), "historical event time must survive projection")
				correlation, ok := entry.AfterState["session_id"].(string)
				require.True(t, ok && correlation != "", "even history without a surviving session row needs noncredential correlation")
				session535NoCredentials(t, []byte(correlation), credentials...)
				if previous, seen := correlations[entry.ID]; seen {
					assert.True(t, correlation == previous, "historical correlation must be stable across repeated reads and both readers")
				} else {
					correlations[entry.ID] = correlation
				}
				if i < 3 {
					agent := []string{"session535-legacy", "session535-expired", "session535-revoked"}[i]
					handle := session535FindSession(t, rows, agent).ID
					assert.True(t, entry.AfterState["session_id"] == handle, "historical session correlation must use its management handle")
					if entry.BeforeState != nil {
						assert.True(t, entry.BeforeState["session_id"] == handle, "before-state must use the same safe correlation")
						assert.True(t, entry.BeforeState["user_id"] == float64(f.operator.ID))
					} else {
						assert.True(t, entry.AfterState["user_id"] == float64(f.operator.ID))
					}
				}
				if i == 3 {
					assert.True(t, entry.AfterState["user_id"] == float64(f.operator.ID), "orphan history must retain its user metadata")
				}
			}
		})
	}
	var unchanged []gormdb.AuditLogEntry
	session535NoError(t, f.env.store.DB.Order("id").Find(&unchanged).Error)
	assert.True(t, reflect.DeepEqual(snapshot, unchanged), "safe historical reads must not rewrite or delete persisted audit rows")
	var login []gormdb.AuditLogEntry
	session535NoError(t, f.env.store.DB.Where("action = ? AND reason = ? AND actor = ?", "auth_login", "dashboard login", f.operator.Email).Find(&login).Error)
	require.Equal(t, 1, len(login))
	session535NoCredentials(t, session535JSON(t, login), credentials...)
	require.NotNil(t, login[0].AfterState)
	var state map[string]any
	session535Decode(t, *login[0].AfterState, &state)
	assert.True(t, state["session_id"] == session535FindSession(t, rows, "session535-issued").ID, "new login audit must persist a safe public session correlation")
	assert.True(t, state["user_id"] == float64(f.operator.ID))
}

func TestSession535HistoricalAuthAuditNestedAndNonObjectStatesAreSafe(t *testing.T) {
	f := openSession535Fixture(t)
	legacyCredential := f.browser("session535-legacy").cookie.Value
	orphanCredential := session535Credential(t)
	credentials := append(f.credentials(), orphanCredential)
	var orphanRows int64
	session535NoError(t, f.env.store.DB.Table("sessions").Where("id = ?", orphanCredential).Count(&orphanRows).Error)
	require.Zero(t, orphanRows, "orphan history must not depend on a surviving session row")

	// Read the existing management boundary only to obtain an opaque expected
	// correlation. This test's RED must come from audit states, not an assumed hash.
	list := session535Request(f.service.router, http.MethodGet, "/api/access/sessions?include_revoked=true", f.browser("session535-admin").cookie, nil)
	require.Equal(t, http.StatusOK, list.Code)
	var listed struct {
		Sessions []gormdb.AccessSessionView `json:"sessions"`
	}
	session535Decode(t, list.Body.Bytes(), &listed)
	legacyHandle := session535FindSession(t, listed.Sessions, "session535-legacy").ID
	type stateExpectation struct {
		markers   map[string]string
		arrayItem bool
	}
	type historicalCase struct {
		name   string
		entry  gormdb.AuditLogEntry
		before stateExpectation
		after  stateExpectation
	}
	makeState := func(shape, label, primary, secondary string) (json.RawMessage, stateExpectation) {
		expectation := stateExpectation{markers: make(map[string]string)}
		node := func(suffix, credential string) map[string]any {
			marker := label + "/" + suffix
			expectation.markers[marker] = credential
			return map[string]any{
				"session_id": credential, "user_id": f.operator.ID, "session535_marker": marker,
				"note": "session535 harmless note", "safe_count": 7, "safe_enabled": true,
			}
		}
		var state any
		switch shape {
		case "object":
			state = node("root", primary)
		case "nested_object":
			state = map[string]any{
				"details": node("nested", primary),
				"items":   []any{node("array", secondary), []any{node("deep_array", primary)}},
			}
		case "scalar":
			state = primary
		case "array":
			expectation.arrayItem = true
			state = []any{
				node("array_object", primary),
				[]any{node("array_nested", secondary), primary},
				"session535 harmless array item",
			}
		default:
			t.Fatal("unknown synthetic audit-state shape")
		}
		return json.RawMessage(session535JSON(t, state)), expectation
	}

	timestamp := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
	var historical []historicalCase
	for _, action := range []string{"auth_login", "auth_session_revoked"} {
		for _, shape := range []string{"object", "nested_object", "scalar", "array"} {
			name := action + "/" + shape
			before, beforeExpectation := makeState(shape, name+"/before", legacyCredential, orphanCredential)
			after, afterExpectation := makeState(shape, name+"/after", orphanCredential, legacyCredential)
			entry := gormdb.AuditLogEntry{
				Action: action, Actor: f.operator.Email, Reason: "session535 historical shapes " + name,
				BeforeState: &before, AfterState: &after,
				CreatedAt: timestamp.Add(time.Duration(len(historical)) * time.Second),
			}
			// Seed already-persisted JSONB directly, including states that cannot
			// decode into a map. Future write sanitization must not mask this path.
			session535NoError(t, f.env.store.DB.Create(&entry).Error)
			historical = append(historical, historicalCase{name: name, entry: entry, before: beforeExpectation, after: afterExpectation})
		}
	}
	var snapshot []gormdb.AuditLogEntry
	session535NoError(t, f.env.store.DB.Order("id").Find(&snapshot).Error)
	type projectedAudit struct {
		ID          int64           `json:"id"`
		Action      string          `json:"action"`
		Actor       string          `json:"actor"`
		Reason      string          `json:"reason"`
		CreatedAt   time.Time       `json:"created_at"`
		BeforeState json.RawMessage `json:"before_state"`
		AfterState  json.RawMessage `json:"after_state"`
	}
	correlations := make(map[string]string)
	for _, projection := range []struct{ name, path string }{
		{name: "audit_list", path: "/api/access/log?limit=100"},
		{name: "user_drilldown", path: "/api/access/users/" + strconv.FormatInt(f.operator.ID, 10) + "?limit=100"},
		{name: "audit_list_repeat", path: "/api/access/log?limit=100"},
		{name: "user_drilldown_repeat", path: "/api/access/users/" + strconv.FormatInt(f.operator.ID, 10) + "?limit=100"},
	} {
		recorder := session535Request(f.service.router, http.MethodGet, projection.path, f.browser("session535-admin").cookie, nil)
		require.Equal(t, http.StatusOK, recorder.Code)
		var payload struct {
			Entries []projectedAudit `json:"entries"`
			Audit   []projectedAudit `json:"audit"`
		}
		session535Decode(t, recorder.Body.Bytes(), &payload)
		entries := payload.Entries
		if entries == nil {
			entries = payload.Audit
		}
		for _, original := range historical {
			t.Run(projection.name+"/"+original.name, func(t *testing.T) {
				var matches []projectedAudit
				for _, entry := range entries {
					if entry.ID == original.entry.ID {
						matches = append(matches, entry)
					}
				}
				require.Equal(t, 1, len(matches), "unsafe states must not cause full-row deletion or duplication")
				entry := matches[0]
				assert.True(t, entry.Action == original.entry.Action && entry.Actor == original.entry.Actor && entry.Reason == original.entry.Reason, "event metadata must survive safe projection")
				assert.True(t, entry.CreatedAt.Equal(original.entry.CreatedAt), "event time must remain unchanged")
				session535NoCredentials(t, session535JSON(t, entry), credentials...)
				for _, state := range []struct {
					name        string
					raw         json.RawMessage
					expectation stateExpectation
				}{
					{name: "before", raw: entry.BeforeState, expectation: original.before},
					{name: "after", raw: entry.AfterState, expectation: original.after},
				} {
					t.Run(state.name, func(t *testing.T) {
						session535NoCredentials(t, state.raw, credentials...)
						// A credential-only scalar may safely project to an omitted,
						// null or otherwise safe value. Do not prescribe its JSON shape.
						if len(state.raw) == 0 {
							assert.Zero(t, len(state.expectation.markers), "representable harmless object data must not disappear")
							return
						}
						var decoded any
						session535Decode(t, state.raw, &decoded)
						nodes := make(map[string][]map[string]any)
						arrayItemPresent := false
						var visit func(any)
						visit = func(value any) {
							switch value := value.(type) {
							case map[string]any:
								if marker, ok := value["session535_marker"].(string); ok {
									nodes[marker] = append(nodes[marker], value)
								}
								for _, child := range value {
									visit(child)
								}
							case []any:
								for _, child := range value {
									visit(child)
								}
							case string:
								arrayItemPresent = arrayItemPresent || value == "session535 harmless array item"
							}
						}
						visit(decoded)
						if state.expectation.arrayItem {
							assert.True(t, arrayItemPresent, "unrelated harmless array data must remain")
						}
						for marker, credential := range state.expectation.markers {
							found := nodes[marker]
							require.Equal(t, 1, len(found), "safe projection must preserve each representable nested record")
							node := found[0]
							assert.True(t, node["note"] == "session535 harmless note" && node["safe_count"] == float64(7) && node["safe_enabled"] == true, "unrelated harmless object data must remain")
							assert.True(t, node["user_id"] == float64(f.operator.ID), "nested user metadata must remain")
							correlation, ok := node["session_id"].(string)
							require.True(t, ok && correlation != "", "representable nested session references need safe correlation")
							session535NoCredentials(t, []byte(correlation), credentials...)
							if credential == legacyCredential {
								assert.True(t, correlation == legacyHandle, "known nested references must use the management handle")
							}
							if previous, seen := correlations[credential]; seen {
								assert.True(t, correlation == previous, "including orphan references, correlation must survive nesting and repeated readers")
							} else {
								correlations[credential] = correlation
							}
						}
					})
				}
			})
		}
	}
	var unchanged []gormdb.AuditLogEntry
	session535NoError(t, f.env.store.DB.Order("id").Find(&unchanged).Error)
	assert.True(t, reflect.DeepEqual(snapshot, unchanged), "recursive and non-object safe projection must leave persisted audit bytes and metadata unchanged")
}

func TestSession535LogoutPreservesSelectedSessionLifecycle(t *testing.T) {
	f := openSession535Fixture(t)
	legacy := f.browser("session535-legacy")
	handle := session535FindSession(t, session535Sessions(t, f, true), legacy.agent).ID
	request := httptest.NewRequest(http.MethodPost, "http://localhost:37777/api/auth/user-logout", nil)
	request.AddCookie(legacy.cookie)
	request.Header.Set("Origin", "https://foreign.example.test")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	rejected := httptest.NewRecorder()
	f.service.router.ServeHTTP(rejected, request)
	assert.Equal(t, http.StatusForbidden, rejected.Code, "foreign-origin logout must remain rejected")
	assert.Zero(t, len(rejected.Result().Cookies()), "a rejected logout must not clear a cookie")
	session535Me(t, f, legacy.cookie, http.StatusOK)
	for attempt := range 2 {
		request := httptest.NewRequest(http.MethodPost, "http://localhost:37777/api/auth/user-logout", nil)
		request.AddCookie(legacy.cookie)
		request.Header.Set("Origin", "http://localhost:37777")
		if attempt > 0 {
			// The revoked DB cookie cannot authenticate a second request;
			// an independently valid HMAC session exercises logout idempotency.
			request.AddCookie(authBoundaryCookie(t, f.service.tokenAuth, time.Now().Add(time.Hour)))
		}
		recorder := httptest.NewRecorder()
		f.service.router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code)
		session535NoCredentials(t, recorder.Body.Bytes(), f.credentials()...)
		cleared := false
		for _, cookie := range recorder.Result().Cookies() {
			if cookie.Name == authSessionCookieName {
				cleared = cookie.Value == "" && cookie.MaxAge < 0 && cookie.HttpOnly && cookie.Path == "/"
			}
		}
		assert.True(t, cleared, "successful and repeated logout must clear only the browser credential")
	}
	session535Me(t, f, legacy.cookie, http.StatusUnauthorized)
	session535Me(t, f, f.browser("session535-issued").cookie, http.StatusOK)
	session535Me(t, f, f.browser("session535-admin").cookie, http.StatusOK)
	updated := session535FindSession(t, session535Sessions(t, f, true), legacy.agent)
	assert.True(t, updated.ID == handle, "logout must retain management/audit correlation")
	assert.Equal(t, "revoked", updated.Status)
	assert.Equal(t, "logout", updated.RevocationReason)
}

func TestSession535AccessReaderGuardsRemainSessionAdminOnly(t *testing.T) {
	f := openSession535Fixture(t)
	paths := []string{
		"/api/access/providers", "/api/access/invitations?limit=1", "/api/access/users",
		"/api/access/users/" + strconv.FormatInt(f.operator.ID, 10), "/api/access/roles",
		"/api/access/sessions?limit=1&include_revoked=true", "/api/access/log?limit=1",
	}
	for _, testCase := range []struct {
		name     string
		browser  string
		hmac     bool
		master   bool
		disabled bool
		stale    bool
		want     int
	}{
		{name: "anonymous", want: http.StatusUnauthorized},
		{name: "master_bearer", master: true, want: http.StatusForbidden},
		{name: "synthetic", disabled: true, want: http.StatusForbidden},
		{name: "synthetic_with_valid_db_cookie", disabled: true, browser: "session535-admin", want: http.StatusForbidden},
		{name: "db_operator", browser: "session535-issued", want: http.StatusForbidden},
		{name: "legacy_db_admin", browser: "session535-admin", want: http.StatusOK},
		{name: "hmac_admin", hmac: true, want: http.StatusOK},
		{name: "expired_cookie", browser: "session535-expired", want: http.StatusUnauthorized},
		{name: "revoked_cookie", browser: "session535-revoked", want: http.StatusUnauthorized},
		{name: "hmac_with_stale_db_cookie", hmac: true, stale: true, want: http.StatusUnauthorized},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			flag := "false"
			if testCase.disabled {
				flag = "true"
			}
			t.Setenv("ENGRAM_AUTH_DISABLED", flag)
			service := authBoundaryRouter(t, f.env.handlers)
			for _, path := range paths {
				request := httptest.NewRequest(http.MethodGet, "http://localhost:37777"+path, nil)
				if testCase.browser != "" {
					request.AddCookie(f.browser(testCase.browser).cookie)
				}
				if testCase.hmac {
					request.AddCookie(authBoundaryCookie(t, service.tokenAuth, time.Now().Add(time.Hour)))
				}
				if testCase.master {
					request.Header.Set("Authorization", "Bearer "+service.tokenAuth.Token())
				}
				if testCase.stale {
					request.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: session535Credential(t)})
				}
				recorder := httptest.NewRecorder()
				service.router.ServeHTTP(recorder, request)
				assert.Equal(t, testCase.want, recorder.Code, "reader guard changed for %s", path)
				if testCase.want != http.StatusOK {
					session535NoCredentials(t, recorder.Body.Bytes(), f.credentials()...)
				}
			}
			if testCase.want == http.StatusForbidden {
				request := httptest.NewRequest(http.MethodGet, "http://localhost:37777/api/access/users/0", nil)
				if testCase.browser != "" {
					request.AddCookie(f.browser(testCase.browser).cookie)
				}
				if testCase.master {
					request.Header.Set("Authorization", "Bearer "+service.tokenAuth.Token())
				}
				recorder := httptest.NewRecorder()
				service.router.ServeHTTP(recorder, request)
				assert.Equal(t, http.StatusForbidden, recorder.Code, "session-admin guard must run before user-ID parsing")
			}
		})
	}
	// Existing direct-handler identity seams cover client-keycard sources
	// without inventing a keycard issuer or a second token-store fixture.
	for _, scope := range []string{"read-only", "read-write"} {
		identity := authpkg.Client(scope, uuid.NewString())
		for _, reader := range []http.HandlerFunc{
			f.env.handlers.handleAccessProviders, f.env.handlers.handleAccessListInvitations,
			f.env.handlers.handleAccessListUsers, f.env.handlers.handleAccessGetUserDrilldown,
			f.env.handlers.handleAccessListRoles, f.env.handlers.handleAccessListSessions,
			f.env.handlers.handleAccessListAudit,
		} {
			request := requestWithRoute(http.MethodGet, "/api/access/fixture-reader", nil, identity, nil)
			recorder := httptest.NewRecorder()
			reader(recorder, request)
			assert.Equal(t, http.StatusForbidden, recorder.Code, "client-keycard identity is not a browser admin session")
		}
	}
}

func TestSession535BrowserAuditContextAndSDKCorrelationStayDistinct(t *testing.T) {
	f := openSession535Fixture(t)
	rows := session535Sessions(t, f, true)
	project := uuid.NewString()
	session535NoError(t, f.env.store.DB.Create(&gormdb.Project{ID: project}).Error)
	sdkIdentifier := "session535-host-" + uuid.NewString()
	sdkStore := gormdb.NewSessionStore(f.env.store)
	sdkID, err := sdkStore.CreateSDKSession(context.Background(), sdkIdentifier, project, "synthetic SDK correlation")
	session535NoError(t, err)
	f.service.sessionStore = sdkStore
	list := session535Request(f.service.router, http.MethodGet, "/api/sessions/list?project="+url.QueryEscape(project), f.browser("session535-admin").cookie, nil)
	require.Equal(t, http.StatusOK, list.Code)
	session535NoCredentials(t, list.Body.Bytes(), f.credentials()...)
	var listed struct {
		Sessions []models.SDKSession `json:"sessions"`
		Total    int64               `json:"total"`
	}
	session535Decode(t, list.Body.Bytes(), &listed)
	require.Equal(t, 1, len(listed.Sessions))
	assert.Equal(t, int64(1), listed.Total)
	lookup := session535Request(f.service.router, http.MethodGet, "/api/sessions?claudeSessionId="+url.QueryEscape(sdkIdentifier), f.browser("session535-admin").cookie, nil)
	require.Equal(t, http.StatusOK, lookup.Code)
	session535NoCredentials(t, lookup.Body.Bytes(), f.credentials()...)
	var found models.SDKSession
	session535Decode(t, lookup.Body.Bytes(), &found)
	for _, session := range []models.SDKSession{listed.Sessions[0], found} {
		assert.Equal(t, sdkID, session.ID, "SDK numeric identity must remain separate from browser handles")
		assert.Equal(t, sdkIdentifier, session.ClaudeSessionID)
		assert.True(t, session.SDKSessionID.Valid && session.SDKSessionID.String == sdkIdentifier, "host session correlation must not be blanket-redacted")
		assert.Equal(t, project, session.Project)
	}
	audit := gormdb.NewAuditStore(f.env.store.DB)
	entry := gormdb.AuditLogEntry{Action: "promote_candidate", Actor: "agent/session535", SourceSessionID: sdkIdentifier, Reason: "synthetic SDK provenance"}
	session535NoError(t, audit.Log(context.Background(), entry))
	var stored []gormdb.AuditLogEntry
	session535NoError(t, f.env.store.DB.Where("action = ? AND actor = ?", entry.Action, entry.Actor).Find(&stored).Error)
	require.Equal(t, 1, len(stored))
	assert.Equal(t, sdkIdentifier, stored[0].SourceSessionID, "noncredential SDK audit provenance must remain intact")
	for _, agent := range []string{"session535-legacy", "session535-issued"} {
		t.Run(agent, func(t *testing.T) {
			adapter, _ := newOperatorCodeHTTPTestAdapter(t)
			breadcrumb := &operatorCodeGrantBreadcrumbApplication{}
			adapter.onboarding = breadcrumb
			request := httptest.NewRequest(http.MethodGet, "http://localhost:37777/api/code/grants/choices", nil)
			request.Header.Set(operatorCodeRequestIDHeader, uuid.NewString())
			request.AddCookie(f.browser(agent).cookie)
			request = request.WithContext(auditcontext.WithSourceSession(request.Context(), sdkIdentifier))
			recorder := httptest.NewRecorder()
			f.service.tokenAuth.Middleware(http.HandlerFunc(adapter.HandleGrantChoices)).ServeHTTP(recorder, request)
			require.Equal(t, http.StatusOK, recorder.Code, "real browser authentication must still reach the existing audit-context boundary")
			session535NoCredentials(t, recorder.Body.Bytes(), f.credentials()...)
			require.True(t, len(breadcrumb.sessions) > 0, "the application must receive browser provenance")
			handle := session535FindSession(t, rows, agent).ID
			for _, source := range breadcrumb.sessions {
				assert.True(t, source != "" && source == handle, "browser audit provenance must use its stable management handle")
				assert.True(t, source != sdkIdentifier, "browser provenance must not impersonate inherited SDK correlation")
				for _, credential := range f.credentials() {
					assert.True(t, source != credential, "browser-origin audit context must never acquire a raw bearer")
				}
			}
		})
	}
}
