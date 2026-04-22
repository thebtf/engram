package instincts

import (
	"fmt"
	"math"

	"github.com/thebtf/engram/pkg/models"
)

// ConvertToObservation maps an instinct to a parsed guidance observation
// suitable for storage as a memory record.
func ConvertToObservation(inst *Instinct) *models.ParsedObservation {
	// Build concepts from domain
	var concepts []string
	if inst.Domain != "" {
		concepts = append(concepts, inst.Domain)
	}

	// Build tags preserving original metadata
	tags := []string{"instinct", fmt.Sprintf("source:%s", inst.Source)}
	if inst.ID != "" {
		tags = append(tags, fmt.Sprintf("instinct-id:%s", inst.ID))
	}

	return &models.ParsedObservation{
		Type:       models.ObsTypeGuidance,
		MemoryType: models.MemTypeGuidance,
		SourceType: models.SourceInstinctImport,
		Title:      inst.Trigger,
		Narrative:  inst.Body,
		Concepts:   append(concepts, tags...),
	}
}

// InstinctImportanceScore returns the importance score derived from
// an instinct's confidence value. The returned score is in the [0, 1] range,
// consistent with the system-wide importance score convention.
func InstinctImportanceScore(confidence float64) float64 {
	score := math.Round(confidence*100) / 100 // round to 2 decimal places
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}
