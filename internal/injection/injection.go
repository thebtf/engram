// Package injection implements Thompson Sampling-based memory selection for
// context injection. It provides Score for candidate ranking and Tracker for
// recording which memories were injected so the feedback loop can measure
// citation outcomes.
package injection

import (
	"context"
	"math"
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
		return sampleGamma(shape+1.0) * math.Pow(rand.Float64(), 1.0/shape) //nolint:gosec
	}
	d := shape - 1.0/3.0
	c := 1.0 / math.Sqrt(9.0*d)
	for {
		var x float64
		for {
			x = rand.NormFloat64() //nolint:gosec
			if 1.0+c*x > 0 {
				break
			}
		}
		v := math.Pow(1.0+c*x, 3)
		u := rand.Float64() //nolint:gosec
		if u < 1.0-0.0331*(x*x)*(x*x) {
			return d * v
		}
		if math.Log(u) < 0.5*x*x+d*(1.0-v+math.Log(v)) {
			return d * v
		}
	}
}

// Tracker records injection events so the feedback loop can correlate them
// with citation outcomes at session end.
type Tracker struct {
	store *gormdb.InjectionLogStore
}

// NewTracker creates a Tracker backed by the given InjectionLogStore.
// A nil store is accepted — Track becomes a no-op.
func NewTracker(store *gormdb.InjectionLogStore) *Tracker {
	return &Tracker{store: store}
}

// Track records the selected memories as injection events for the given session.
// Only ScoredMemory entries where Selected==true are recorded.
// Designed for fire-and-forget goroutine use; all errors are logged and discarded.
func (t *Tracker) Track(ctx context.Context, sessionID, project string, scored []ScoredMemory) {
	if t.store == nil || sessionID == "" {
		return
	}
	var ids []int64
	for _, s := range scored {
		if !s.Selected || s.Memory == nil {
			continue
		}
		ids = append(ids, s.Memory.ID)
	}
	if len(ids) == 0 {
		return
	}
	if err := t.store.Record(ctx, sessionID, project, ids); err != nil {
		log.Warn().Err(err).Str("session_id", sessionID).Str("project", project).
			Msg("injection tracker: failed to record injections")
	}
}
