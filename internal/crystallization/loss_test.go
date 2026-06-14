package crystallization

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDetectLoss_CandidateMissingText verifies loss when existing has Text but candidate does not.
func TestDetectLoss_CandidateMissingText(t *testing.T) {
	existing := ExtractedDecision{Text: "decided to use Redis", Confidence: 0.9}
	candidate := ExtractedDecision{Text: "", Confidence: 0.9}
	assert.True(t, DetectLoss(existing, candidate),
		"candidate missing Text while existing has it → loss=true")
}

// TestDetectLoss_CandidateMissingEvidence verifies loss when existing has Evidence but candidate does not.
func TestDetectLoss_CandidateMissingEvidence(t *testing.T) {
	existing := ExtractedDecision{
		Text:       "decided to use Redis",
		Evidence:   []string{"low-latency requirement", "team familiarity"},
		Confidence: 0.9,
	}
	candidate := ExtractedDecision{
		Text:       "decided to use Redis",
		Evidence:   nil,
		Confidence: 0.9,
	}
	assert.True(t, DetectLoss(existing, candidate),
		"candidate missing Evidence while existing has it → loss=true")
}

// TestDetectLoss_CandidateMissingConfidence verifies loss when existing has Confidence but candidate zeroed it.
func TestDetectLoss_CandidateMissingConfidence(t *testing.T) {
	existing := ExtractedDecision{Text: "decided to use Redis", Confidence: 0.8}
	candidate := ExtractedDecision{Text: "decided to use Redis", Confidence: 0}
	assert.True(t, DetectLoss(existing, candidate),
		"candidate drops Confidence while existing has it → loss=true")
}

// TestDetectLoss_CandidateSuperset verifies no loss when candidate has all existing fields populated.
func TestDetectLoss_CandidateSuperset(t *testing.T) {
	existing := ExtractedDecision{
		Text:       "decided to use Redis",
		Evidence:   []string{"latency"},
		Confidence: 0.7,
	}
	candidate := ExtractedDecision{
		Text:       "decided to use Redis for caching",
		Evidence:   []string{"latency", "ops familiarity"},
		Confidence: 0.85,
	}
	assert.False(t, DetectLoss(existing, candidate),
		"candidate superset → loss=false")
}

// TestDetectLoss_BothEqual verifies no loss when both decisions carry the same fields.
func TestDetectLoss_BothEqual(t *testing.T) {
	d := ExtractedDecision{Text: "decided to cache", Evidence: []string{"perf"}, Confidence: 0.9}
	assert.False(t, DetectLoss(d, d), "equal decisions → loss=false")
}

// TestDetectLoss_BothEmpty verifies no loss when both decisions are zero-value.
func TestDetectLoss_BothEmpty(t *testing.T) {
	assert.False(t, DetectLoss(ExtractedDecision{}, ExtractedDecision{}),
		"both zero-value → loss=false")
}

// TestDetectLoss_ExistingEmptyEvidenceCandidateEmpty verifies no loss when existing
// has no evidence and candidate also has no evidence.
func TestDetectLoss_ExistingEmptyEvidenceCandidateEmpty(t *testing.T) {
	existing := ExtractedDecision{Text: "decided to use Go", Evidence: nil}
	candidate := ExtractedDecision{Text: "decided to use Go", Evidence: nil}
	assert.False(t, DetectLoss(existing, candidate),
		"neither has evidence → no evidence loss")
}
