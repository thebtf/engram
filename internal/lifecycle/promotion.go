package lifecycle

import "github.com/thebtf/engram/pkg/models"

// PromotionResult describes the outcome of a promotion/demotion evaluation.
type PromotionResult struct {
	NewTier  string
	Reason   string
	Changed  bool
}

// EvaluatePromotion checks whether a memory qualifies for promotion to a higher tier.
func EvaluatePromotion(m *models.Memory) PromotionResult {
	switch m.Tier {
	case TierEpisodic:
		if m.AccessCount >= 3 && m.Confidence >= 0.7 && m.Retrievability > 0.8 {
			return PromotionResult{
				NewTier: TierSemantic,
				Reason:  "episodic→semantic: access_count≥3, confidence≥0.7, retrievability>0.8",
				Changed: true,
			}
		}
	case TierSemantic:
		if m.RecurrenceCount >= 5 && m.CitationCount >= 3 && m.Confidence >= 0.8 {
			return PromotionResult{
				NewTier: TierProcedural,
				Reason:  "semantic→procedural: recurrence≥5, citations≥3, confidence≥0.8",
				Changed: true,
			}
		}
	}
	return PromotionResult{}
}

// EvaluateDemotion checks whether a memory should be demoted to a lower tier.
// Uses 25% hysteresis gap: promote at T, demote at T-25%.
func EvaluateDemotion(m *models.Memory) PromotionResult {
	switch m.Tier {
	case TierProcedural:
		if m.Confidence < 0.6 || m.Retrievability < 0.3 {
			return PromotionResult{
				NewTier: TierSemantic,
				Reason:  "procedural→semantic: confidence<0.6 or retrievability<0.3 (hysteresis)",
				Changed: true,
			}
		}
	case TierSemantic:
		if m.Confidence < 0.45 && m.AccessCount < 2 {
			return PromotionResult{
				NewTier: TierEpisodic,
				Reason:  "semantic→episodic: confidence<0.45 and low access (hysteresis)",
				Changed: true,
			}
		}
	}
	return PromotionResult{}
}

// ValidPromotion checks if a tier transition is valid (no skipping tiers).
func ValidPromotion(from, to string) bool {
	order := map[string]int{
		TierWorking:    0,
		TierEpisodic:   1,
		TierSemantic:   2,
		TierProcedural: 3,
	}
	f, fOk := order[from]
	t, tOk := order[to]
	if !fOk || !tOk {
		return false
	}
	diff := t - f
	return diff == 1 || diff == -1
}
