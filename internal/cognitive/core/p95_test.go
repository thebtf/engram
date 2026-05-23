package core

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/thebtf/engram/pkg/cognitive"
)

// nfr2IterCount sizes the timed loop. NFR-2 budgets the p95 plug overhead at
// ≤1ms; 10_000 iterations give a stable p95 (95th index = 9500) without
// blowing test wall time.
const nfr2IterCount = 10_000

// TestNFR2_PlugOverheadP95 is the NFR-2 verification gate. It compares two
// timed loops:
//
//   - Variant B (master off):    only the FlagConfig.IsPlugEnabled check
//   - Variant C (master on):     full dispatch via 5 enabled NoOps
//
// The gate asserts p95_C - p95_B <= 1 ms (NFR-2 budget). Variant A (pre-plug
// build) is captured at CI time via the v6.3.0 baseline (T018 fixture
// pipeline); see TECHNICAL_DEBT.md for the conditional-compile gap.
func TestNFR2_PlugOverheadP95(t *testing.T) {
	if testing.Short() {
		t.Skip("NFR-2 p95 timing test skipped under -short")
	}

	// 1ms p95 budget per NFR-2 (spec §NFR-2).
	const p95BudgetNs = int64(1_000_000)

	bDurations := measureVariantB(t, nfr2IterCount)
	cDurations := measureVariantC(t, nfr2IterCount)

	p95B := percentileNs(bDurations, 95)
	p95C := percentileNs(cDurations, 95)
	overhead := p95C - p95B

	t.Logf("NFR-2 measurement (iterations=%d):", nfr2IterCount)
	t.Logf("  p95 Variant B (master off):    %d ns", p95B)
	t.Logf("  p95 Variant C (plug active):   %d ns", p95C)
	t.Logf("  p95 overhead (C - B):          %d ns (budget %d ns)", overhead, p95BudgetNs)

	if overhead > p95BudgetNs {
		t.Errorf("NFR-2 p95 plug overhead exceeds 1ms budget: %d ns > %d ns",
			overhead, p95BudgetNs)
	}
}

// measureVariantB collects per-iteration durations for the master-off path.
func measureVariantB(t *testing.T, n int) []int64 {
	cfg := plugMasterOffConfig(t)
	durations := make([]int64, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		_ = cfg.IsPlugEnabled()
		durations[i] = time.Since(start).Nanoseconds()
	}
	return durations
}

// measureVariantC collects per-iteration durations for the plug-active path
// with all 5 NoOps registered+enabled.
func measureVariantC(t *testing.T, n int) []int64 {
	_ = plugMasterOnConfig(t)
	platform := newPlatform(t)
	for _, name := range []string{
		"core.noop.candidate_proposer",
		"core.noop.hint_emitter",
		"core.noop.state_writer",
		"core.noop.attention_event_writer",
		"core.noop.directive_distiller",
	} {
		if err := platform.registry.Enable(name); err != nil {
			t.Fatalf("Enable(%s): %v", name, err)
		}
	}
	ctx := context.Background()
	durations := make([]int64, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		_ = Dispatch[cognitive.CandidateProposer](
			ctx, platform.dispatcher, "CandidateProposer",
			func(p cognitive.CandidateProposer) error {
				_, err := p.Propose(ctx, cognitive.AttentionEvent{}, 10)
				return err
			},
		)
		durations[i] = time.Since(start).Nanoseconds()
	}
	return durations
}

// percentileNs returns the p-th percentile of durations using nearest-rank.
func percentileNs(durations []int64, p int) int64 {
	if len(durations) == 0 {
		return 0
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	idx := (p * len(durations)) / 100
	if idx >= len(durations) {
		idx = len(durations) - 1
	}
	return durations[idx]
}
