package lifecycle

import (
	"testing"

	"github.com/thebtf/engram/pkg/models"
)

func TestEvaluatePromotion_EpisodicToSemantic(t *testing.T) {
	m := &models.Memory{
		Tier:           TierEpisodic,
		AccessCount:    3,
		Confidence:     0.75,
		Retrievability: 0.85,
	}
	result := EvaluatePromotion(m)
	if !result.Changed || result.NewTier != TierSemantic {
		t.Errorf("expected episodic→semantic promotion, got %+v", result)
	}
}

func TestEvaluatePromotion_EpisodicBelowThreshold(t *testing.T) {
	m := &models.Memory{
		Tier:           TierEpisodic,
		AccessCount:    2,
		Confidence:     0.75,
		Retrievability: 0.85,
	}
	result := EvaluatePromotion(m)
	if result.Changed {
		t.Errorf("access_count<3 should not promote, got %+v", result)
	}
}

func TestEvaluatePromotion_SemanticToProcedural(t *testing.T) {
	m := &models.Memory{
		Tier:            TierSemantic,
		RecurrenceCount: 5,
		CitationCount:   3,
		Confidence:      0.85,
	}
	result := EvaluatePromotion(m)
	if !result.Changed || result.NewTier != TierProcedural {
		t.Errorf("expected semantic→procedural promotion, got %+v", result)
	}
}

func TestEvaluateDemotion_ProceduralHysteresis(t *testing.T) {
	above := &models.Memory{Tier: TierProcedural, Confidence: 0.65, Retrievability: 0.5}
	result := EvaluateDemotion(above)
	if result.Changed {
		t.Errorf("confidence=0.65 should not demote (hysteresis threshold 0.6), got %+v", result)
	}

	below := &models.Memory{Tier: TierProcedural, Confidence: 0.55, Retrievability: 0.5}
	result = EvaluateDemotion(below)
	if !result.Changed || result.NewTier != TierSemantic {
		t.Errorf("confidence=0.55 should demote procedural→semantic, got %+v", result)
	}
}

func TestEvaluateDemotion_SemanticHysteresis(t *testing.T) {
	m := &models.Memory{Tier: TierSemantic, Confidence: 0.40, AccessCount: 1}
	result := EvaluateDemotion(m)
	if !result.Changed || result.NewTier != TierEpisodic {
		t.Errorf("low confidence + low access should demote semantic→episodic, got %+v", result)
	}
}

func TestValidPromotion(t *testing.T) {
	if !ValidPromotion(TierEpisodic, TierSemantic) {
		t.Error("episodic→semantic should be valid")
	}
	if ValidPromotion(TierEpisodic, TierProcedural) {
		t.Error("episodic→procedural should be invalid (skip)")
	}
	if !ValidPromotion(TierSemantic, TierEpisodic) {
		t.Error("semantic→episodic demotion should be valid")
	}
	if ValidPromotion("invalid", TierSemantic) {
		t.Error("invalid tier should be invalid")
	}
}
