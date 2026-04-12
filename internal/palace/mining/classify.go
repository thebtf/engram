package mining

import (
	"math"
	"strings"
)

// memoryType defines a classification category with its keyword signals.
type memoryType struct {
	name     string
	keywords []string
}

var memoryTypes = []memoryType{
	{
		name:     "decision",
		keywords: []string{"decided", "chose", "selected", "opted for", "went with"},
	},
	{
		name:     "preference",
		keywords: []string{"prefer", "like", "enjoy", "favorite", "always use"},
	},
	{
		name:     "milestone",
		keywords: []string{"finished", "completed", "launched", "shipped", "released"},
	},
	{
		name:     "problem",
		keywords: []string{"bug", "issue", "broken", "failed", "error", "crash"},
	},
	{
		name:     "emotional",
		keywords: []string{"feel", "frustrated", "happy", "worried", "excited"},
	},
}

const classifyThreshold = 0.3

// Classify returns the memory type and confidence for a text chunk.
// Confidence is computed as a sigmoid-scaled count of keyword matches divided
// by the total number of keywords for the category, so that more matches push
// confidence higher. Returns "general" with confidence 0 when no category
// exceeds the threshold.
func Classify(text string) (string, float64) {
	lower := strings.ToLower(text)

	bestType := "general"
	bestConf := 0.0

	for _, mt := range memoryTypes {
		count := 0
		for _, kw := range mt.keywords {
			if strings.Contains(lower, kw) {
				count++
			}
		}
		if count == 0 {
			continue
		}
		// Confidence: sigmoid-style scaling so each additional hit increases
		// confidence with diminishing returns. Base = count/total, then scaled.
		raw := float64(count) / float64(len(mt.keywords))
		// Scale into [0, 1] with a mild amplifier so a single match is
		// meaningful (roughly 0.3 for 1/5, 0.5 for 2/5, etc.).
		conf := 1 - math.Exp(-float64(count)*0.7)

		// Ensure single-keyword categories always clear the threshold.
		if conf < raw {
			conf = raw
		}

		if conf > bestConf {
			bestConf = conf
			bestType = mt.name
		}
	}

	if bestConf < classifyThreshold {
		return "general", 0
	}
	return bestType, bestConf
}
