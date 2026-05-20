// Package injection implements Thompson Sampling-based memory selection for
// context injection. It provides Score for candidate ranking and Tracker for
// recording which memories were injected so the feedback loop can measure
// citation outcomes.
package injection

import (
	"context"
	"math/rand"
	"sort"

	"github.com/rs/zerolog/log"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// ScoredMemory pairs a memory with its Thompson-sampled score and whether it
// was selected for injection.
type ScoredMemory struct {
	Memory   *models.Memory
	Score    float64
	Selected bool
}

// Score applies Thompson Sampling to rank memories and selects the top topK.
//
// Each memory's reward probability is sampled from Beta(alpha, beta) where
// alpha = TsAlpha and beta = TsBeta (both stored on the memory row; both
// default to 1.0, yielding a uniform prior for unseen memories).
//
// The returned slice contains ALL input memories annotated with their sampled
// scores; the top topK are marked Selected=true. The slice is ordered by score
// descending so callers can iterate and stop at the first Selected=false entry.
//
// If topK <= 0 or topK >= len(memories), all memories are selected.
func Score(memories []*models.Memory, topK int) []ScoredMemory {
	if len(memories) == 0 {
		return nil
	}

	scored := make([]ScoredMemory, len(memories))
	for i, m := range memories {
		alpha := m.TsAlpha
		beta := m.TsBeta
		if alpha <= 0 {
			alpha = 1.0
		}
		if beta <= 0 {
			beta = 1.0
		}
		scored[i] = ScoredMemory{
			Memory: m,
			Score:  sampleBeta(alpha, beta),
		}
	}

	// Sort descending by sampled score.
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	// Mark top-K as selected.
	selectN := topK
	if selectN <= 0 || selectN >= len(scored) {
		selectN = len(scored)
	}
	for i := 0; i < selectN; i++ {
		scored[i].Selected = true
	}

	return scored
}

// ExplorationRatio returns the fraction of selected memories whose score came
// from the exploration tail — those with a uniform prior (alpha==beta==1.0),
// indicating they have never been cited or measured before.
func ExplorationRatio(scored []ScoredMemory) float64 {
	var selected, exploratory int
	for _, s := range scored {
		if !s.Selected {
			continue
		}
		selected++
		if s.Memory.TsAlpha <= 1.0 && s.Memory.TsBeta <= 1.0 {
			exploratory++
		}
	}
	if selected == 0 {
		return 0
	}
	return float64(exploratory) / float64(selected)
}

// sampleBeta draws a single sample from Beta(alpha, beta) using Johnk's method
// via Gamma variates (Marsaglia-Tsang).
func sampleBeta(alpha, beta float64) float64 {
	if alpha == 1.0 && beta == 1.0 {
		return rand.Float64() //nolint:gosec
	}
	x := sampleGamma(alpha)
	y := sampleGamma(beta)
	if x+y == 0 {
		return 0.5
	}
	return x / (x + y)
}

// sampleGamma draws from Gamma(shape, 1) using Marsaglia-Tsang's method.
func sampleGamma(shape float64) float64 {
	if shape < 1.0 {
		return sampleGamma(shape+1) * gammaSmallShape(rand.Float64(), shape) //nolint:gosec
	}
	d := shape - 1.0/3.0
	c := 1.0 / mathSqrt(9.0*d)
	for {
		x := rand.NormFloat64() //nolint:gosec
		v := 1.0 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rand.Float64() //nolint:gosec
		if u < 1.0-0.0331*(x*x)*(x*x) {
			return d * v
		}
		if mathLog(u) < 0.5*x*x+d*(1.0-v+mathLog(v)) {
			return d * v
		}
	}
}

// gammaSmallShape computes u^(1/shape) for the shape < 1 reduction.
func gammaSmallShape(u, shape float64) float64 {
	if u <= 0 {
		return 0
	}
	return mathExp(mathLog(u) / shape)
}

// mathSqrt computes sqrt(x) via Newton's method.
func mathSqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 40; i++ {
		z -= (z*z - x) / (2 * z)
	}
	return z
}

// mathLog computes ln(x) via range reduction + atanh series.
func mathLog(x float64) float64 {
	if x <= 0 {
		return -1e300
	}
	e := 0
	for x >= 2.0 {
		x /= 2.0
		e++
	}
	for x < 0.5 {
		x *= 2.0
		e--
	}
	y := (x - 1.0) / (x + 1.0)
	y2 := y * y
	ln := 2 * (y + y2*y/3 + y2*y2*y/5 + y2*y2*y2*y/7 + y2*y2*y2*y2*y/9)
	const ln2 = 0.6931471805599453
	return ln + float64(e)*ln2
}

// mathExp computes e^x via range reduction + Horner evaluation.
func mathExp(x float64) float64 {
	n := int(x)
	if x < 0 && float64(n) != x {
		n--
	}
	f := x - float64(n)
	ef := 1.0 + f*(1+f*(0.5+f*(1.0/6+f*(1.0/24+f*(1.0/120+f*(1.0/720+f*(1.0/5040+f*(1.0/40320+f*(1.0/362880+f/3628800)))))))))
	const e = 2.718281828459045
	abs := n
	base := e
	if abs < 0 {
		abs = -abs
		base = 1.0 / e
	}
	en := 1.0
	for abs > 0 {
		if abs&1 == 1 {
			en *= base
		}
		base *= base
		abs >>= 1
	}
	return en * ef
}

// Tracker records injection events so the feedback loop can correlate them
// with citation outcomes at session end.
type Tracker struct {
	store *gormdb.InjectionStore
}

// NewTracker creates a Tracker backed by the given InjectionStore.
// A nil store is accepted — Track becomes a no-op.
func NewTracker(store *gormdb.InjectionStore) *Tracker {
	return &Tracker{store: store}
}

// Track records the selected memories as injection events for the given session.
// Only ScoredMemory entries where Selected==true are recorded.
// Designed for fire-and-forget goroutine use; all errors are logged and discarded.
func (t *Tracker) Track(ctx context.Context, sessionID, project string, scored []ScoredMemory) {
	if t.store == nil || sessionID == "" {
		return
	}
	var records []gormdb.InjectionRecord
	for _, s := range scored {
		if !s.Selected || s.Memory == nil {
			continue
		}
		records = append(records, gormdb.InjectionRecord{
			ObservationID:    s.Memory.ID,
			SessionID:        sessionID,
			InjectionSection: "vnext_thompson",
		})
	}
	if len(records) == 0 {
		return
	}
	if err := t.store.RecordInjections(ctx, records); err != nil {
		log.Warn().Err(err).Str("session_id", sessionID).Str("project", project).
			Msg("injection tracker: failed to record injections")
	}
}
