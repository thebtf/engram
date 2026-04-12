package dedup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckCrossModelPromotion_DifferentKnownSources(t *testing.T) {
	result := &Result{Action: ActionUpdate, ExistingID: 1, Similarity: 0.85}
	assert.True(t, CheckCrossModelPromotion(result, "claude-code", "codex"))
}

func TestCheckCrossModelPromotion_SameSource(t *testing.T) {
	result := &Result{Action: ActionUpdate, ExistingID: 1, Similarity: 0.85}
	assert.False(t, CheckCrossModelPromotion(result, "claude-code", "claude-code"))
}

func TestCheckCrossModelPromotion_UnknownNewSource(t *testing.T) {
	result := &Result{Action: ActionUpdate, ExistingID: 1, Similarity: 0.85}
	assert.False(t, CheckCrossModelPromotion(result, "unknown", "codex"))
}

func TestCheckCrossModelPromotion_UnknownExistingSource(t *testing.T) {
	result := &Result{Action: ActionUpdate, ExistingID: 1, Similarity: 0.85}
	assert.False(t, CheckCrossModelPromotion(result, "claude-code", "unknown"))
}

func TestCheckCrossModelPromotion_EmptySource(t *testing.T) {
	result := &Result{Action: ActionUpdate, ExistingID: 1, Similarity: 0.85}
	assert.False(t, CheckCrossModelPromotion(result, "", "codex"))
	assert.False(t, CheckCrossModelPromotion(result, "claude-code", ""))
}

func TestCheckCrossModelPromotion_NotUpdateAction(t *testing.T) {
	// Only UPDATE triggers cross-model, not ADD or NOOP
	addResult := &Result{Action: ActionAdd}
	assert.False(t, CheckCrossModelPromotion(addResult, "claude-code", "codex"))

	noopResult := &Result{Action: ActionNoop, ExistingID: 1, Similarity: 0.95}
	assert.False(t, CheckCrossModelPromotion(noopResult, "claude-code", "codex"))
}

func TestCheckCrossModelPromotion_NilResult(t *testing.T) {
	assert.False(t, CheckCrossModelPromotion(nil, "claude-code", "codex"))
}

func TestCheckCrossModelPromotion_AllAgentCombinations(t *testing.T) {
	result := &Result{Action: ActionUpdate, ExistingID: 1, Similarity: 0.85}

	// All valid cross-model pairs
	agents := []string{"claude-code", "codex", "gemini"}
	for _, a := range agents {
		for _, b := range agents {
			if a != b {
				assert.True(t, CheckCrossModelPromotion(result, a, b),
					"different known agents %s/%s should trigger cross-model", a, b)
			}
		}
	}

	// "other" is a known non-unknown value
	assert.True(t, CheckCrossModelPromotion(result, "claude-code", "other"))
}
