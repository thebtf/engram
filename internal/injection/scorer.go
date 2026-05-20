package injection

import (
	"math"
	"math/rand/v2"
	"sort"

	"github.com/thebtf/engram/pkg/models"
)

// ScoredMemory pairs a memory with its Thompson-sampled score.
type ScoredMemory struct {
	Memory *models.Memory
	Score  float64
}

// Score selects top-K memories via Thompson Sampling with Beta(alpha, beta) priors.
// Each memory's score is sampled from Beta(ts_alpha, ts_beta).
// New memories (alpha=1, beta=1) have uniform distribution → natural exploration.
// Proven memories (high alpha) have peaked distribution → reliable selection.
// Returns at most topK memories sorted by sampled score descending.
func Score(memories []*models.Memory, topK int) []ScoredMemory {
	if len(memories) == 0 {
		return nil
	}
	if topK <= 0 || topK > len(memories) {
		topK = len(memories)
	}

	scored := make([]ScoredMemory, len(memories))
	for i, mem := range memories {
		alpha := mem.TsAlpha
		beta := mem.TsBeta
		if alpha < 1 {
			alpha = 1
		}
		if beta < 1 {
			beta = 1
		}

		// Sample from Beta(alpha, beta) via Gamma distribution:
		// If X ~ Gamma(alpha, 1) and Y ~ Gamma(beta, 1), then X/(X+Y) ~ Beta(alpha, beta)
		x := sampleGamma(alpha)
		y := sampleGamma(beta)
		var theta float64
		if x+y > 0 {
			theta = x / (x + y)
		} else {
			theta = 0.5
		}

		scored[i] = ScoredMemory{
			Memory: mem,
			Score:  theta,
		}
	}

	// Sort by score descending
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	return scored[:topK]
}

// sampleGamma samples from Gamma(shape, 1) using the Marsaglia-Tsang method.
func sampleGamma(shape float64) float64 {
	if shape < 1 {
		// For shape < 1, use: Gamma(shape) = Gamma(shape+1) * U^(1/shape)
		return sampleGamma(shape+1) * math.Pow(rand.Float64(), 1/shape)
	}
	d := shape - 1.0/3.0
	c := 1.0 / math.Sqrt(9.0*d)
	for {
		var x float64
		for {
			x = rand.NormFloat64()
			if 1+c*x > 0 {
				break
			}
		}
		v := math.Pow(1+c*x, 3)
		u := rand.Float64()
		if u < 1-0.0331*(x*x)*(x*x) {
			return d * v
		}
		if math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}
