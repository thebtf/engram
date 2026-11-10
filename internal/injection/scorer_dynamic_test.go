package injection

import (
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/models"
)

func TestScore_DynamicPrior_UnseenMemories(t *testing.T) {
	// Score uses the legacy package-level generator; reset the same stream for
	// the sampler oracle and Score without depending on probabilistic averages.
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",randseednop=0")
	memory := &models.Memory{ID: 1, TsAlpha: 1, TsBeta: 1}
	const seed = 314159
	rand.Seed(seed)
	want := sampleBeta(1+0.6*2, 1+(1-0.6)*2)
	rand.Seed(seed)
	scored := Score([]*models.Memory{memory}, 1, ScoreOpts{DynamicPrior: true, ProjectCitationRate: 0.6})
	require.Len(t, scored, 1)
	assert.Equal(t, want, scored[0].Score, "unseen memory must use the citation-rate-derived beta prior")
	rand.Seed(seed)
	baseline := Score([]*models.Memory{memory}, 1, ScoreOpts{DynamicPrior: false, ProjectCitationRate: 0.6})
	require.Len(t, baseline, 1)
	assert.NotEqual(t, baseline[0].Score, scored[0].Score, "enabled prior must differ from the uniform-prior control")
	assert.True(t, scored[0].Selected)
	assert.Equal(t, 1.0, memory.TsAlpha)
	assert.Equal(t, 1.0, memory.TsBeta)
}

func TestScore_NewcomerBonus(t *testing.T) {
	old := time.Now().Add(-72 * time.Hour)
	fresh := time.Now().Add(-12 * time.Hour)

	// Run multiple trials and compare average scores.
	trials := 1000
	var oldTotal, freshTotal float64
	for i := 0; i < trials; i++ {
		oldMem := &models.Memory{ID: 1, TsAlpha: 1.0, TsBeta: 1.0, CreatedAt: old}
		freshMem := &models.Memory{ID: 2, TsAlpha: 1.0, TsBeta: 1.0, CreatedAt: fresh}
		scored := Score([]*models.Memory{oldMem, freshMem}, 2)
		for _, sm := range scored {
			if sm.Memory.ID == 1 {
				oldTotal += sm.Score
			} else {
				freshTotal += sm.Score
			}
		}
	}

	avgOld := oldTotal / float64(trials)
	avgFresh := freshTotal / float64(trials)
	assert.Greater(t, avgFresh, avgOld, "newcomer bonus should boost fresh memory average score")
}

func TestScore_BackwardCompatible(t *testing.T) {
	memories := []*models.Memory{
		{ID: 1, TsAlpha: 3.0, TsBeta: 1.0},
		{ID: 2, TsAlpha: 1.0, TsBeta: 3.0},
	}

	// No opts — should work exactly as before.
	scored := Score(memories, 1)
	assert.Len(t, scored, 2)

	selectedCount := 0
	for _, sm := range scored {
		if sm.Selected {
			selectedCount++
		}
	}
	assert.Equal(t, 1, selectedCount)
}

func TestScore_DynamicPrior_NotAppliedToSeenMemories(t *testing.T) {
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",randseednop=0")
	for _, prior := range []struct {
		name        string
		alpha, beta float64
	}{
		{"both observed", 3, 2},
		{"alpha observed", 3, 1},
		{"beta observed", 1, 2},
	} {
		t.Run(prior.name, func(t *testing.T) {
			memory := &models.Memory{ID: 1, TsAlpha: prior.alpha, TsBeta: prior.beta}
			const seed = 271828
			rand.Seed(seed)
			want := sampleBeta(prior.alpha, prior.beta)
			rand.Seed(seed)
			scored := Score([]*models.Memory{memory}, 1, ScoreOpts{DynamicPrior: true, ProjectCitationRate: 0.8})
			require.Len(t, scored, 1)
			assert.Equal(t, want, scored[0].Score, "observed memory must retain its stored beta prior")
			assert.Equal(t, prior.alpha, memory.TsAlpha)
			assert.Equal(t, prior.beta, memory.TsBeta)
		})
	}
}
