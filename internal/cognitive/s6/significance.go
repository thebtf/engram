package s6

import (
	"fmt"

	"github.com/thebtf/engram/pkg/models"
)

const (
	RatingUseful    = "useful"
	RatingNotUseful = "not_useful"
)

// ApplySignificanceRating maps explicit agent feedback onto the existing
// Thompson-learning fields used by S6 outcome policy.
func ApplySignificanceRating(mem *models.Memory, rating string) error {
	if mem == nil {
		return fmt.Errorf("memory significance updater not available")
	}
	if mem.TsAlpha <= 0 {
		mem.TsAlpha = 1
	}
	if mem.TsBeta <= 0 {
		mem.TsBeta = 1
	}
	if mem.ImportanceBase <= 0 {
		mem.ImportanceBase = 0.5
	}

	switch rating {
	case RatingUseful:
		mem.TsAlpha += 1
		mem.CitationCount++
		mem.ConsecutiveCitationCount++
		return nil
	case RatingNotUseful:
		mem.TsBeta += 1
		mem.ConsecutiveCitationCount = 0
		return nil
	default:
		return fmt.Errorf("rating must be '%s' or '%s'", RatingUseful, RatingNotUseful)
	}
}
