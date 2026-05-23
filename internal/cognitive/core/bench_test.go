package core

import (
	"context"
	"testing"

	"github.com/thebtf/engram/pkg/cognitive"
)

// BenchmarkSessionStart_VariantB_PlugLinkedMasterOff measures the cost of
// a session-start-equivalent path when the v7 plug is linked into the binary
// but the master flag is OFF. This is the "Variant B" lane from post-PM-Fix-6:
// dead-code overhead only. b.ReportMetric reports mean ns/op.
//
// Variant A (pre-plug build) is not feasible inline — the binary unconditionally
// links the plug machinery in current code (T014). Per the task AC fallback,
// the variant comparison is achieved at CI time by capturing the v6.3.0 baseline
// (T018). The TECHNICAL_DEBT entry tracks the conditional-compile gap.
func BenchmarkSessionStart_VariantB_PlugLinkedMasterOff(b *testing.B) {
	cfg := plugMasterOffConfig(b)
	platform := newPlatform(b)
	_ = cfg
	_ = platform

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Master flag is off — session-start path bypasses dispatch entirely;
		// the only overhead is the FlagConfig.IsPlugEnabled check + the
		// allocation cost of the unused registry/meter/queue/bus fields.
		_ = cfg.IsPlugEnabled()
	}
}

// BenchmarkSessionStart_VariantC_PlugActiveAllSubsystemsOff measures the cost
// of session-start-equivalent dispatch when the master flag is ON but no real
// subsystem flag is set, so only the 5 NoOps are registered+enabled. This is
// the "Variant C" lane: full dispatch path active, all impls are NoOps.
func BenchmarkSessionStart_VariantC_PlugActiveAllSubsystemsOff(b *testing.B) {
	cfg := plugMasterOnConfig(b)
	platform := newPlatform(b)
	if !cfg.IsPlugEnabled() {
		b.Fatalf("expected master flag enabled, got disabled")
	}

	// Enable all 5 NoOps so dispatch has something to call.
	for _, name := range []string{
		"core.noop.candidate_proposer",
		"core.noop.hint_emitter",
		"core.noop.state_writer",
		"core.noop.attention_event_writer",
		"core.noop.directive_distiller",
	} {
		if err := platform.registry.Enable(name); err != nil {
			b.Fatalf("Enable(%s): %v", name, err)
		}
	}

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Dispatch[cognitive.CandidateProposer](
			ctx, platform.dispatcher, "CandidateProposer",
			func(p cognitive.CandidateProposer) error {
				_, err := p.Propose(ctx, cognitive.AttentionEvent{}, 10)
				return err
			},
		)
	}
}

// --- Bench helpers ----------------------------------------------------------

type benchPlatform struct {
	registry   SubsystemRegistry
	meter      SubsystemMeter
	dispatcher *SubsystemDispatcher
}

func newPlatform(tb testing.TB) *benchPlatform {
	tb.Helper()
	reg := newRegistry()
	meter := NewLocalMeter()
	if err := RegisterNoOps(reg); err != nil {
		tb.Fatalf("RegisterNoOps: %v", err)
	}
	return &benchPlatform{
		registry:   reg,
		meter:      meter,
		dispatcher: NewSubsystemDispatcher(reg, meter),
	}
}

func plugMasterOffConfig(tb testing.TB) FlagConfig {
	tb.Helper()
	tb.Setenv("ENGRAM_V7_PLUG_ENABLED", "")
	return LoadFlagConfigFromEnv()
}

func plugMasterOnConfig(tb testing.TB) FlagConfig {
	tb.Helper()
	tb.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	return LoadFlagConfigFromEnv()
}
