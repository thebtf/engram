// Package crystallization provides LLM-based session decision extraction.
package crystallization

// ExtractedDecision represents a decision extracted from agent output.
//
// Fields are populated by the LLM extraction path (LLMExtractor.Extract).
// The Text, Pattern, and Position fields are retained for compatibility with
// any stored candidate records written by the previous regex path.
type ExtractedDecision struct {
	Text     string `json:"text"`
	Pattern  string `json:"pattern"`
	Position int    `json:"position"`

	// Lang is the detected BCP-47-ish language code of the decision text, e.g. "ru", "en", "zh".
	Lang string `json:"lang,omitempty"`
	// Confidence is the LLM-assigned extraction confidence in [0, 1].
	Confidence float64 `json:"confidence,omitempty"`
	// Recurrence is an optional count of how many times this decision pattern recurs across
	// the digest. Zero means single occurrence or unknown.
	Recurrence int `json:"recurrence,omitempty"`
	// Evidence holds supporting text snippets cited by the LLM for this decision.
	Evidence []string `json:"evidence,omitempty"`
	// ProposedTarget is the suggested promotion target, typically "rule" for session decisions.
	ProposedTarget string `json:"proposed_target,omitempty"`
	// PrivacyStatus records the result of any privacy classification applied to this decision.
	PrivacyStatus string `json:"privacy_status,omitempty"`
}
