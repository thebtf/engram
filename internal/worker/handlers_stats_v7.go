// T012 — TokenAuth middleware verification (verified 2026-05-23 against feat/v7-core
// HEAD 9a36f1c; full investigation log at
// `.agent/tasks/engram-v7-core/T012/implementation-log.md`).
//
// Auth context wiring:
//   - Middleware attaches the resolved auth.Identity via
//     internal/worker/middleware.go:464 buildAuthCtx() →
//     internal/auth/context.go:38 auth.WithIdentity.
//   - Canonical context key: auth.IdentityKey (internal/auth/context.go:31).
//   - Access helpers: auth.IdentityFrom / auth.RoleFrom / auth.SourceFrom.
//
// Source constants (internal/auth/identity.go:11-27 — VERIFIED, NOT SourceAdmin):
//   - auth.SourceMaster ("master")   — ENGRAM_AUTH_ADMIN_TOKEN bearer; admin-only;
//     MUST NOT be issued to workstation processes.
//   - auth.SourceClient ("client")   — per-workstation keycard from api_tokens table.
//   - auth.SourceSession ("session") — browser session cookie / Authentik forward-auth.
//
// Stats-endpoint gating policy:
//   - SourceMaster  → 403 (operator key is server-host only).
//   - SourceClient  → 200 (legitimate workstation caller).
//   - SourceSession → 200 (browser dashboard user).
//   - no Identity   → 401 (middleware rejects before reaching this handler).
//
// ENGRAM_INTERNAL=1 bypass mentioned in plan §Risks R1 does NOT exist in current code
// (verified by grep across internal/). No special path required for this scope.
package worker

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/cognitive/core"
)

// requireKeycardSource enforces the FR-8 source gate on stats endpoints.
// Returns true when the caller carries an explicit whitelisted Source
// (SourceClient or SourceSession). When false is returned, the appropriate
// HTTP error response has already been written; the caller MUST NOT write
// any further bytes.
//
// The whitelist is positive (not default-allow): any future auth.Source
// added to internal/auth must be reviewed against this gate before it
// reaches stats endpoints. Unknown or missing sources route to 401 so
// the auth-failure path stays explicit.
func requireKeycardSource(w http.ResponseWriter, r *http.Request) bool {
	src := auth.SourceFrom(r.Context())
	switch src {
	case string(auth.SourceClient), string(auth.SourceSession):
		// Workstation keycard or browser session — both are legitimate
		// callers for v7 stats endpoints per the T012 staged policy.
		return true
	case string(auth.SourceMaster):
		// Operator key is server-host only — reject for workstation-scoped
		// stats endpoints per the T012 staged policy.
		writeJSONError(w, http.StatusForbidden,
			"forbidden: stats endpoints require workstation keycard")
		return false
	default:
		// Empty (no identity) or any future-unknown source — middleware
		// should have rejected first, but defend in depth with 401.
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
}

// writeJSONError emits a JSON error envelope with the given HTTP status code.
// Kept private to this file so the v7 stats endpoints stay consistent on shape.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// writeJSONStatus emits a JSON success envelope with the given HTTP status code.
func writeJSONStatus(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// handleStatsV7Subsystems returns the registered v7 subsystems as
// `[]core.SubsystemInfo`. Operator-key callers are rejected; keycard and
// session callers proceed.
func (s *Service) handleStatsV7Subsystems(w http.ResponseWriter, r *http.Request) {
	if !requireKeycardSource(w, r) {
		return
	}
	if s.cognitiveRegistry == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "v7 platform not initialised")
		return
	}
	writeJSONStatus(w, http.StatusOK, s.cognitiveRegistry.List())
}

// handleStatsV7Substrate returns the substrate `MetricsSnapshot`. The optional
// `?subsystem=<name>` query parameter filters the snapshot to entries whose
// fold-key tag set contains `subsystem=<name>`; omitting the parameter returns
// the full aggregate.
func (s *Service) handleStatsV7Substrate(w http.ResponseWriter, r *http.Request) {
	if !requireKeycardSource(w, r) {
		return
	}
	if s.cognitiveMeter == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "v7 platform not initialised")
		return
	}
	snap := s.cognitiveMeter.Snapshot()
	if name := r.URL.Query().Get("subsystem"); name != "" {
		snap = filterSnapshotBySubsystem(snap, name)
	}
	writeJSONStatus(w, http.StatusOK, snap)
}

// handleStatsV7Product resolves the registered `ProductMetricsProvider` impl
// (S5 in production; absent during v7.0 boot when S5 is disabled) and returns
// the snapshot. Absence → 404 with the documented body.
func (s *Service) handleStatsV7Product(w http.ResponseWriter, r *http.Request) {
	if !requireKeycardSource(w, r) {
		return
	}
	if s.cognitiveRegistry == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "v7 platform not initialised")
		return
	}
	// ResolveImpls is on the concrete registry impl, not the SubsystemRegistry
	// interface. Type-assert to access it; failure indicates a registry
	// substitution that violates the platform contract.
	type implsResolver interface {
		ResolveImpls(interfaceName string) []core.Subsystem
	}
	resolver, ok := s.cognitiveRegistry.(implsResolver)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError,
			"registry implementation does not expose impl resolution")
		return
	}
	impls := resolver.ResolveImpls("ProductMetricsProvider")
	if len(impls) == 0 {
		writeJSONError(w, http.StatusNotFound, "s5-telemetry not enabled")
		return
	}
	provider, ok := impls[0].(core.ProductMetricsProvider)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError,
			"registered ProductMetricsProvider does not satisfy interface")
		return
	}
	snap, err := provider.ProductMetrics(r.Context(), core.ProductMetricsWindow{})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "product metrics query failed")
		return
	}
	writeJSONStatus(w, http.StatusOK, snap)
}

// filterSnapshotBySubsystem returns a MetricsSnapshot containing only entries
// whose fold-key tag set carries `subsystem=<name>`. The fold-key format is
// `name{k1=v1,k2=v2}` (alphabetic key order) as produced by T009
// SubsystemMeter.foldKey. Entries that match are copied; the original snapshot
// is not mutated. Untagged entries (no `{...}` suffix) are excluded from a
// filtered response.
func filterSnapshotBySubsystem(snap core.MetricsSnapshot, name string) core.MetricsSnapshot {
	filtered := core.MetricsSnapshot{
		Counters:   make(map[string]uint64),
		Histograms: make(map[string]core.HistogramSummary),
	}
	target := "subsystem=" + name
	for key, val := range snap.Counters {
		if foldKeyContainsTag(key, target) {
			filtered.Counters[key] = val
		}
	}
	for key, val := range snap.Histograms {
		if foldKeyContainsTag(key, target) {
			filtered.Histograms[key] = val
		}
	}
	return filtered
}

// foldKeyContainsTag reports whether the fold-key `name{...}` form embeds the
// supplied tagPair (e.g. `subsystem=foo`). Keys without a tag block never
// match; tag blocks are split on `,` and compared verbatim.
func foldKeyContainsTag(foldKey, tagPair string) bool {
	open := strings.IndexByte(foldKey, '{')
	if open < 0 || !strings.HasSuffix(foldKey, "}") {
		return false
	}
	tagBlock := foldKey[open+1 : len(foldKey)-1]
	for _, part := range strings.Split(tagBlock, ",") {
		if part == tagPair {
			return true
		}
	}
	return false
}
