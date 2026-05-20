// Package lifecycle provides memory lifecycle management: tier classification,
// decay computation, confidence scoring, promotion triggers, and sleep cycle.
package lifecycle

// Cognitive tier constants.
const (
	TierWorking    = "working"
	TierEpisodic   = "episodic"
	TierSemantic   = "semantic"
	TierProcedural = "procedural"
)

// Epistemic type constants.
const (
	EpistemicFact        = "fact"
	EpistemicExperience  = "experience"
	EpistemicOpinion     = "opinion"
	EpistemicObservation = "observation"
)

// Defeasibility class constants.
const (
	DefeasibilityNonDefeasible = "non_defeasible"
	DefeasibilitySlow          = "slow"
	DefeasibilityRapid         = "rapid"
)

// Promotion target constants.
const (
	PromotionTargetNone  = "none"
	PromotionTargetRule  = "rule"
	PromotionTargetSkill = "skill"
	PromotionTargetSpec  = "spec"
	PromotionTargetIssue = "issue"
	PromotionTargetDocs  = "docs"
)

// TierWeight returns the stability multiplier for a given tier.
func TierWeight(tier string) float64 {
	switch tier {
	case TierWorking:
		return 0.1
	case TierEpisodic:
		return 0.5
	case TierSemantic:
		return 1.0
	case TierProcedural:
		return 2.0
	default:
		return 1.0
	}
}

// EpistemicWeight returns the stability multiplier for a given epistemic type.
func EpistemicWeight(epistemicType string) float64 {
	switch epistemicType {
	case EpistemicFact:
		return 1.5
	case EpistemicExperience:
		return 1.0
	case EpistemicOpinion:
		return 0.8
	case EpistemicObservation:
		return 0.5
	default:
		return 1.0
	}
}

// ValidTier returns true if tier is a recognized cognitive tier.
func ValidTier(tier string) bool {
	switch tier {
	case TierWorking, TierEpisodic, TierSemantic, TierProcedural:
		return true
	}
	return false
}

// ValidEpistemicType returns true if t is a recognized epistemic type.
func ValidEpistemicType(t string) bool {
	switch t {
	case EpistemicFact, EpistemicExperience, EpistemicOpinion, EpistemicObservation:
		return true
	}
	return false
}

// ValidDefeasibility returns true if d is a recognized defeasibility class.
func ValidDefeasibility(d string) bool {
	switch d {
	case DefeasibilityNonDefeasible, DefeasibilitySlow, DefeasibilityRapid:
		return true
	}
	return false
}
