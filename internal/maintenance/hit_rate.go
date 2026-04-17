package maintenance

import (
	"context"
)

// analyzeHitRate was driven by injection_log which was dropped in v5 (US1).
// The function is retained as a no-op so the maintenance scheduler loop compiles
// without changes to the subtask table. Hit-rate flags (noise_candidate, high_value)
// will no longer be updated; the observations table still carries whatever values
// were set before the migration. A follow-up task should replace this with a
// effectiveness_score-based approach.
func (s *Service) analyzeHitRate(_ context.Context) (int, error) {
	return 0, nil
}
