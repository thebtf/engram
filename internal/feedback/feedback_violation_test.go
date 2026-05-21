package feedback

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/thebtf/engram/pkg/models"
)

func TestDetectViolations_GuidanceViolated(t *testing.T) {
	memories := []*models.Memory{
		{ID: 1, Content: "never use placeholder implementations in production code", EpistemicType: "guidance"},
		{ID: 2, Content: "always write tests before code", EpistemicType: "guidance"},
		{ID: 3, Content: "regular memory about database setup", EpistemicType: "fact"},
	}
	results := []CitationResult{
		{MemoryID: 1, Cited: false},
		{MemoryID: 2, Cited: false},
		{MemoryID: 3, Cited: false},
	}

	output := "I created placeholder implementations in production code for the handler and will fill it in later"
	results = DetectViolations(output, results, memories)

	assert.True(t, results[0].Violated, "rule about placeholders should be violated")
	assert.False(t, results[1].Violated, "rule about tests should not be violated")
	assert.False(t, results[2].Violated, "non-guidance memory should not be checked")
}

func TestDetectViolations_CitedMemoriesSkipped(t *testing.T) {
	memories := []*models.Memory{
		{ID: 1, Content: "don't use placeholder implementations", EpistemicType: "guidance"},
	}
	results := []CitationResult{
		{MemoryID: 1, Cited: true},
	}

	output := "I created a placeholder implementations for now"
	results = DetectViolations(output, results, memories)

	assert.False(t, results[0].Violated, "cited memories should not be checked for violations")
}

func TestDetectViolations_EmptyOutput(t *testing.T) {
	memories := []*models.Memory{
		{ID: 1, Content: "never skip tests", EpistemicType: "guidance"},
	}
	results := []CitationResult{
		{MemoryID: 1, Cited: false},
	}

	results = DetectViolations("", results, memories)
	assert.False(t, results[0].Violated)
}

func TestDetectViolations_TagBasedGuidance(t *testing.T) {
	memories := []*models.Memory{
		{ID: 1, Content: "avoid using global variables in production code for configuration", Tags: []string{"rule", "coding"}},
	}
	results := []CitationResult{
		{MemoryID: 1, Cited: false},
	}

	output := "I added global variables in production code for configuration to store the settings"
	results = DetectViolations(output, results, memories)

	assert.True(t, results[0].Violated, "tag-based guidance should be detected")
}

func TestDetectViolations_RussianKeywords(t *testing.T) {
	memories := []*models.Memory{
		{ID: 1, Content: "никогда не делай прямой пуш в основную ветку репозитория", EpistemicType: "guidance"},
	}
	results := []CitationResult{
		{MemoryID: 1, Cited: false},
	}

	output := "я сделал прямой пуш в основную ветку репозитория чтобы ускорить процесс"
	results = DetectViolations(output, results, memories)

	assert.True(t, results[0].Violated, "Russian negative keywords should be detected")
}

func TestIsGuidanceMemory(t *testing.T) {
	tests := []struct {
		name     string
		memory   *models.Memory
		expected bool
	}{
		{"epistemic guidance", &models.Memory{EpistemicType: "guidance"}, true},
		{"tag guidance", &models.Memory{Tags: []string{"guidance"}}, true},
		{"tag rule", &models.Memory{Tags: []string{"rule"}}, true},
		{"tag behavioral", &models.Memory{Tags: []string{"behavioral"}}, true},
		{"fact type", &models.Memory{EpistemicType: "fact"}, false},
		{"no tags", &models.Memory{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isGuidanceMemory(tt.memory))
		})
	}
}
