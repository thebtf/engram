package retrieval

import (
	"testing"
	"time"

	"github.com/thebtf/engram/pkg/models"
)

func TestScore_NewMemory(t *testing.T) {
	now := time.Now()
	m := &models.Memory{
		CreatedAt:      now.Add(-1 * time.Hour),
		ImportanceBase: 0.5,
	}
	result := Score(m, 0.8, now)
	if result.Score <= 0 {
		t.Errorf("new memory should have positive score, got %v", result.Score)
	}
	if result.Recency < 0.9 {
		t.Errorf("1-hour-old memory should have high recency, got %v", result.Recency)
	}
}

func TestScore_OldMemory(t *testing.T) {
	now := time.Now()
	m := &models.Memory{
		CreatedAt:      now.Add(-720 * time.Hour),
		ImportanceBase: 0.5,
	}
	result := Score(m, 0.8, now)
	if result.Recency > 0.3 {
		t.Errorf("30-day-old memory should have low recency, got %v", result.Recency)
	}
}

func TestScore_HighCitation(t *testing.T) {
	now := time.Now()
	cited := &models.Memory{
		CreatedAt:      now.Add(-24 * time.Hour),
		ImportanceBase: 0.5,
		CitationCount:  10,
		InjectionCount: 10,
	}
	uncited := &models.Memory{
		CreatedAt:      now.Add(-24 * time.Hour),
		ImportanceBase: 0.5,
		CitationCount:  0,
		InjectionCount: 10,
	}
	scoreCited := Score(cited, 0.5, now)
	scoreUncited := Score(uncited, 0.5, now)
	if scoreCited.Score <= scoreUncited.Score {
		t.Errorf("cited memory should score higher: cited=%v, uncited=%v", scoreCited.Score, scoreUncited.Score)
	}
}

// --- Rank-5: Thompson reinforcement in the recall scorer ---

// A memory with strong accumulated citation evidence (high ts_alpha) must rank above an
// otherwise-identical memory that has been repeatedly injected but uncited (high ts_beta),
// at equal relevance/recency/base importance.
func TestScore_TsReinforcement_CitedRanksAboveUncited(t *testing.T) {
	now := time.Now()
	base := models.Memory{CreatedAt: now.Add(-24 * time.Hour), ImportanceBase: 0.5}

	reinforced := base
	reinforced.TsAlpha = 9.0 // ~8 successful citations on top of the 1.0 prior
	reinforced.TsBeta = 1.0

	neglected := base
	neglected.TsAlpha = 1.0
	neglected.TsBeta = 9.0 // injected repeatedly, never cited

	hi := Score(&reinforced, 0.5, now)
	lo := Score(&neglected, 0.5, now)
	if hi.Score <= lo.Score {
		t.Errorf("reinforced memory should outrank neglected: hi=%v lo=%v", hi.Score, lo.Score)
	}
	// Posterior 9/10=0.9 lifts importance above the 0.5 base; 1/10=0.1 pulls it below.
	if hi.Importance <= 0.5 {
		t.Errorf("high-ts importance should exceed base 0.5, got %v", hi.Importance)
	}
	if lo.Importance >= 0.5 {
		t.Errorf("low-ts importance should fall below base 0.5, got %v", lo.Importance)
	}
}

// A memory penalized for violations (large ts_beta) must rank below a neutral memory.
func TestScore_TsReinforcement_ViolationPenaltyRanksLow(t *testing.T) {
	now := time.Now()
	violated := &models.Memory{
		CreatedAt: now.Add(-24 * time.Hour), ImportanceBase: 0.6,
		TsAlpha: 1.0, TsBeta: 11.0, // strong violation/uncited beta accumulation
	}
	neutral := &models.Memory{
		CreatedAt: now.Add(-24 * time.Hour), ImportanceBase: 0.6,
		// no feedback evidence: ts at prior, sum == threshold, term skipped
		TsAlpha: 1.0, TsBeta: 1.0,
	}
	v := Score(violated, 0.5, now)
	n := Score(neutral, 0.5, now)
	if v.Score >= n.Score {
		t.Errorf("violation-penalized memory should rank below neutral: violated=%v neutral=%v", v.Score, n.Score)
	}
}

// A memory with NO feedback evidence (ts at the 1.0/1.0 prior, sum == threshold) must be
// scored exactly as if the reinforcement term did not exist — the 0.5 posterior must NOT be
// blended in and drag importance toward the prior.
func TestScore_TsReinforcement_NoEvidenceUnchanged(t *testing.T) {
	now := time.Now()
	withPrior := &models.Memory{
		CreatedAt: now.Add(-24 * time.Hour), ImportanceBase: 0.8,
		TsAlpha: 1.0, TsBeta: 1.0,
	}
	got := Score(withPrior, 0.5, now)
	// Importance must remain the untouched base (0.8), not pulled toward 0.5.
	if got.Importance != 0.8 {
		t.Errorf("no-evidence memory importance should equal base 0.8, got %v", got.Importance)
	}
}

// Cold-start: a single citation (ts_alpha just above prior, sum just above threshold) must
// NOT over-boost importance to 1.0 — the Bayesian posterior smooths small samples.
func TestScore_TsReinforcement_ColdStartNotOverBoosted(t *testing.T) {
	now := time.Now()
	// One successful citation under the default multiplier: ts_alpha 1->~3, ts_beta stays 1.
	coldStart := &models.Memory{
		CreatedAt: now.Add(-24 * time.Hour), ImportanceBase: 0.5,
		TsAlpha: 3.0, TsBeta: 1.0,
	}
	got := Score(coldStart, 0.5, now)
	posterior := 3.0 / 4.0 // 0.75, not 1.0
	wantImportance := 0.5*(1-feedbackBlend) + posterior*feedbackBlend
	if got.Importance < wantImportance-1e-9 || got.Importance > wantImportance+1e-9 {
		t.Errorf("cold-start importance = %v, want %v (smoothed posterior, not maxed)", got.Importance, wantImportance)
	}
	if got.Importance >= 1.0 {
		t.Errorf("single citation should not max importance, got %v", got.Importance)
	}
}

// Backward-compat: a memory with pre-ts citation history (counts > 0 but ts still at prior)
// must still receive the raw-rate boost via the fallback branch, so rank-5 does not regress
// memories that accumulated citations before the ts columns existed.
func TestScore_TsReinforcement_PreTsCountFallback(t *testing.T) {
	now := time.Now()
	legacyCited := &models.Memory{
		CreatedAt: now.Add(-24 * time.Hour), ImportanceBase: 0.5,
		CitationCount: 10, InjectionCount: 10,
		TsAlpha: 1.0, TsBeta: 1.0, // ts at prior (columns added after these citations)
	}
	got := Score(legacyCited, 0.5, now)
	// Falls to raw-rate branch: rate 1.0 lifts importance above base 0.5.
	if got.Importance <= 0.5 {
		t.Errorf("legacy-cited memory should still be boosted via fallback, got %v", got.Importance)
	}
}

// The rank-4 observability fields must survive the rank-5 importance change untouched.
func TestScore_TsReinforcement_PreservesRerankSentinel(t *testing.T) {
	now := time.Now()
	m := &models.Memory{CreatedAt: now.Add(-24 * time.Hour), ImportanceBase: 0.5, TsAlpha: 5.0, TsBeta: 1.0}
	got := Score(m, 0.5, now)
	if got.RerankScore != -1 {
		t.Errorf("RerankScore sentinel must remain -1 (no reranker ran), got %v", got.RerankScore)
	}
}

func TestRRF(t *testing.T) {
	a := []int64{1, 2, 3}
	b := []int64{2, 3, 4}
	merged := RRF(a, b, 60)
	if len(merged) != 4 {
		t.Fatalf("expected 4 unique IDs, got %d", len(merged))
	}
	if merged[0] != 2 {
		t.Errorf("ID 2 should rank first (appears in both lists), got %d", merged[0])
	}
}
