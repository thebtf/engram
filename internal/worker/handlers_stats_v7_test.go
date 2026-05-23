package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/cognitive/core"
)

// --- Mocks ------------------------------------------------------------------

// mockSubsystemForHandlers is the minimum core.Subsystem implementation a
// stats-endpoint test needs. Tests register it under different names so the
// registry returns multiple entries.
type mockSubsystemForHandlers struct {
	name string
	impl []string
}

func (m *mockSubsystemForHandlers) Name() string    { return m.name }
func (m *mockSubsystemForHandlers) Version() string { return "v1.0.0" }
func (m *mockSubsystemForHandlers) Start(ctx context.Context, deps core.Dependencies) error {
	return nil
}
func (m *mockSubsystemForHandlers) Stop() error          { return nil }
func (m *mockSubsystemForHandlers) Implements() []string { return m.impl }

// stubProductMetricsProvider implements both core.Subsystem and
// core.ProductMetricsProvider so we can register it via SubsystemRegistry and
// have ResolveImpls("ProductMetricsProvider") return a value satisfying the
// product-metrics interface.
type stubProductMetricsProvider struct {
	mockSubsystemForHandlers
	snap core.ProductMetricsSnapshot
	err  error
}

func (s *stubProductMetricsProvider) ProductMetrics(ctx context.Context, w core.ProductMetricsWindow) (core.ProductMetricsSnapshot, error) {
	if s.err != nil {
		return core.ProductMetricsSnapshot{}, s.err
	}
	return s.snap, nil
}

// newServiceWithCognitive builds a minimal *Service holding wired CORE
// platform fields and nothing else. It is the substrate every handler test
// in this file uses — DB, HTTP server, gRPC, etc. stay zero.
func newServiceWithCognitive(t *testing.T) *Service {
	t.Helper()
	meter := core.NewLocalMeter()
	bus := core.NewAttentionEventBus(meter)
	queue := core.NewHintQueue()
	registry := core.NewRegistry()
	return &Service{
		cognitiveRegistry: registry,
		cognitiveMeter:    meter,
		cognitiveQueue:    queue,
		cognitiveBus:      bus,
	}
}

// newRequestWithSource builds an *http.Request whose context carries an
// auth.Identity with the supplied Source. When source is the empty string,
// no Identity is attached (mimicking the no-auth path).
func newRequestWithSource(method, path string, source auth.Source) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if source != "" {
		var id auth.Identity
		switch source {
		case auth.SourceMaster:
			id = auth.Admin()
		case auth.SourceClient:
			id = auth.Client("read-write", "test-keycard-uuid")
		case auth.SourceSession:
			id = auth.Session("admin")
		}
		r = r.WithContext(auth.WithIdentity(r.Context(), id))
	}
	return r
}

// --- Subsystems handler ------------------------------------------------------

func TestSubsystems_SourceClient_Returns200WithList(t *testing.T) {
	s := newServiceWithCognitive(t)
	// Register two mock subsystems so the list has measurable content.
	for _, name := range []string{"alpha", "beta"} {
		if err := s.cognitiveRegistry.Register(&mockSubsystemForHandlers{
			name: name, impl: []string{"CandidateProposer"},
		}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}

	w := httptest.NewRecorder()
	r := newRequestWithSource(http.MethodGet, "/api/stats/v7/subsystems", auth.SourceClient)
	s.handleStatsV7Subsystems(w, r)

	if got := w.Code; got != http.StatusOK {
		t.Fatalf("status: got %d, want 200", got)
	}
	var got []core.SubsystemInfo
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("subsystem count: got %d, want 2", len(got))
	}
}

func TestSubsystems_SourceMaster_Returns403(t *testing.T) {
	s := newServiceWithCognitive(t)
	w := httptest.NewRecorder()
	r := newRequestWithSource(http.MethodGet, "/api/stats/v7/subsystems", auth.SourceMaster)
	s.handleStatsV7Subsystems(w, r)

	if got := w.Code; got != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403", got)
	}
	if body := w.Body.String(); !strings.Contains(body, "workstation keycard") {
		t.Errorf("body missing keycard hint: %s", body)
	}
}

func TestSubsystems_NoAuth_Returns401(t *testing.T) {
	s := newServiceWithCognitive(t)
	w := httptest.NewRecorder()
	r := newRequestWithSource(http.MethodGet, "/api/stats/v7/subsystems", "")
	s.handleStatsV7Subsystems(w, r)

	if got := w.Code; got != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", got)
	}
}

// --- Substrate handler -------------------------------------------------------

func TestSubstrate_NoQuery_ReturnsAggregate(t *testing.T) {
	s := newServiceWithCognitive(t)
	s.cognitiveMeter.IncrCounter("calls_total", 1, map[string]string{"subsystem": "alpha"})
	s.cognitiveMeter.IncrCounter("calls_total", 1, map[string]string{"subsystem": "beta"})
	s.cognitiveMeter.IncrCounter("events_emitted", 1, nil)

	w := httptest.NewRecorder()
	r := newRequestWithSource(http.MethodGet, "/api/stats/v7/substrate", auth.SourceClient)
	s.handleStatsV7Substrate(w, r)

	if got := w.Code; got != http.StatusOK {
		t.Fatalf("status: got %d, want 200", got)
	}
	var snap core.MetricsSnapshot
	if err := json.NewDecoder(w.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 3 counters total (2 with subsystem tag + 1 untagged).
	if len(snap.Counters) != 3 {
		t.Errorf("counter count: got %d, want 3", len(snap.Counters))
	}
}

func TestSubstrate_WithSubsystemName_FiltersSnapshot(t *testing.T) {
	s := newServiceWithCognitive(t)
	s.cognitiveMeter.IncrCounter("calls_total", 1, map[string]string{"subsystem": "alpha"})
	s.cognitiveMeter.IncrCounter("calls_total", 1, map[string]string{"subsystem": "beta"})
	s.cognitiveMeter.IncrCounter("events_emitted", 1, nil)

	w := httptest.NewRecorder()
	r := newRequestWithSource(http.MethodGet, "/api/stats/v7/substrate?subsystem=alpha", auth.SourceClient)
	s.handleStatsV7Substrate(w, r)

	if got := w.Code; got != http.StatusOK {
		t.Fatalf("status: got %d, want 200", got)
	}
	var snap core.MetricsSnapshot
	if err := json.NewDecoder(w.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(snap.Counters) != 1 {
		t.Errorf("filtered counter count: got %d, want 1", len(snap.Counters))
	}
	// The single entry must carry subsystem=alpha in its tag block.
	for key := range snap.Counters {
		if !strings.Contains(key, "subsystem=alpha") {
			t.Errorf("filtered key missing subsystem=alpha: %q", key)
		}
	}
}

// --- Product handler ---------------------------------------------------------

func TestProduct_NoProvider_Returns404(t *testing.T) {
	s := newServiceWithCognitive(t)
	w := httptest.NewRecorder()
	r := newRequestWithSource(http.MethodGet, "/api/stats/v7/product", auth.SourceClient)
	s.handleStatsV7Product(w, r)

	if got := w.Code; got != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", got)
	}
	if body := w.Body.String(); !strings.Contains(body, "s5-telemetry not enabled") {
		t.Errorf("body missing not-enabled hint: %s", body)
	}
}

func TestProduct_WithProvider_Delegates(t *testing.T) {
	s := newServiceWithCognitive(t)
	provider := &stubProductMetricsProvider{
		mockSubsystemForHandlers: mockSubsystemForHandlers{
			name: "s5-stub",
			impl: []string{"ProductMetricsProvider"},
		},
		snap: core.ProductMetricsSnapshot{
			Metrics: map[string]float64{"some_metric": 0.42},
			SampleN: 7,
		},
	}
	if err := s.cognitiveRegistry.Register(provider); err != nil {
		t.Fatalf("register stub: %v", err)
	}
	if err := s.cognitiveRegistry.Enable("s5-stub"); err != nil {
		t.Fatalf("enable stub: %v", err)
	}

	w := httptest.NewRecorder()
	r := newRequestWithSource(http.MethodGet, "/api/stats/v7/product", auth.SourceClient)
	s.handleStatsV7Product(w, r)

	if got := w.Code; got != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body=%s", got, w.Body.String())
	}
	var snap core.ProductMetricsSnapshot
	if err := json.NewDecoder(w.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.SampleN != 7 {
		t.Errorf("SampleN: got %d, want 7", snap.SampleN)
	}
}

func TestProduct_SourceMaster_Returns403(t *testing.T) {
	s := newServiceWithCognitive(t)
	w := httptest.NewRecorder()
	r := newRequestWithSource(http.MethodGet, "/api/stats/v7/product", auth.SourceMaster)
	s.handleStatsV7Product(w, r)

	if got := w.Code; got != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403", got)
	}
}
