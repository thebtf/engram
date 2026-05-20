package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/thebtf/engram/internal/lifecycle"
)

type lifecycleArgs struct {
	Action        string  `json:"action"`
	MemoryID      int64   `json:"memory_id"`
	TargetTier    string  `json:"target_tier"`
	Confidence    float64 `json:"confidence"`
	Reason        string  `json:"reason"`
	Defeasibility string  `json:"defeasibility"`
	DaysAhead     int     `json:"days_ahead"`
}

func (s *Server) handleLifecycle(ctx context.Context, args json.RawMessage) (string, error) {
	var a lifecycleArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("parse lifecycle args: %w", err)
	}

	switch a.Action {
	case "info":
		return s.lifecycleInfo(ctx, a.MemoryID)
	case "promote":
		return s.lifecyclePromote(ctx, a.MemoryID, a.TargetTier)
	case "demote":
		return s.lifecycleDemote(ctx, a.MemoryID, a.TargetTier)
	case "set_confidence":
		return s.lifecycleSetConfidence(ctx, a.MemoryID, a.Confidence, a.Reason)
	case "set_defeasibility":
		return s.lifecycleSetDefeasibility(ctx, a.MemoryID, a.Defeasibility)
	case "sleep_status":
		return s.lifecycleSleepStatus()
	case "decay_preview":
		return s.lifecycleDecayPreview(ctx, a.MemoryID, a.DaysAhead)
	default:
		return "", fmt.Errorf("unknown lifecycle action: %s", a.Action)
	}
}

func (s *Server) lifecycleInfo(ctx context.Context, id int64) (string, error) {
	if id == 0 {
		return "", fmt.Errorf("memory_id required")
	}
	mem, err := s.memoryStore.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get memory: %w", err)
	}

	elapsed := time.Since(mem.CreatedAt).Hours() / 24
	if mem.LastRetrievedAt != nil {
		elapsed = time.Since(*mem.LastRetrievedAt).Hours() / 24
	}
	liveRetrievability := lifecycle.ComputeRetrievability(mem.Stability, elapsed)

	result := map[string]any{
		"id":               mem.ID,
		"tier":             mem.Tier,
		"epistemic_type":   mem.EpistemicType,
		"confidence":       mem.Confidence,
		"stability":        mem.Stability,
		"retrievability":   liveRetrievability,
		"recurrence_count": mem.RecurrenceCount,
		"citation_count":   mem.CitationCount,
		"injection_count":  mem.InjectionCount,
		"access_count":     mem.AccessCount,
		"defeasibility":    mem.Defeasibility,
		"promotion_target": mem.PromotionTarget,
		"status":           mem.Status,
		"created_at":       mem.CreatedAt,
		"last_retrieved_at": mem.LastRetrievedAt,
		"last_confirmed":   mem.LastConfirmed,
		"review_after":     mem.ReviewAfter,
		"valid_from":       mem.ValidFrom,
		"valid_until":      mem.ValidUntil,
	}
	return marshalJSON(result)
}

func (s *Server) lifecyclePromote(ctx context.Context, id int64, targetTier string) (string, error) {
	if id == 0 {
		return "", fmt.Errorf("memory_id required")
	}
	if !lifecycle.ValidTier(targetTier) {
		return "", fmt.Errorf("invalid target tier: %s", targetTier)
	}
	mem, err := s.memoryStore.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get memory: %w", err)
	}
	if !lifecycle.ValidPromotion(mem.Tier, targetTier) {
		return "", fmt.Errorf("invalid promotion: %s → %s (can only move one tier at a time)", mem.Tier, targetTier)
	}

	if err := s.memoryStore.UpdateLifecycleFields(ctx, id, map[string]any{
		"tier": targetTier,
	}); err != nil {
		return "", err
	}
	if s.promotionStore != nil {
		_ = s.promotionStore.LogPromotion(ctx, id, mem.Tier, targetTier, "manual promotion")
	}
	return marshalJSON(map[string]any{
		"memory_id": id,
		"from_tier": mem.Tier,
		"to_tier":   targetTier,
		"message":   "promoted successfully",
	})
}

func (s *Server) lifecycleDemote(ctx context.Context, id int64, targetTier string) (string, error) {
	if id == 0 {
		return "", fmt.Errorf("memory_id required")
	}
	if !lifecycle.ValidTier(targetTier) {
		return "", fmt.Errorf("invalid target tier: %s", targetTier)
	}
	mem, err := s.memoryStore.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get memory: %w", err)
	}
	if !lifecycle.ValidPromotion(mem.Tier, targetTier) {
		return "", fmt.Errorf("invalid demotion: %s → %s (can only move one tier at a time)", mem.Tier, targetTier)
	}

	if err := s.memoryStore.UpdateLifecycleFields(ctx, id, map[string]any{
		"tier": targetTier,
	}); err != nil {
		return "", err
	}
	if s.promotionStore != nil {
		_ = s.promotionStore.LogPromotion(ctx, id, mem.Tier, targetTier, "manual demotion")
	}
	return marshalJSON(map[string]any{
		"memory_id": id,
		"from_tier": mem.Tier,
		"to_tier":   targetTier,
		"message":   "demoted successfully",
	})
}

func (s *Server) lifecycleSetConfidence(ctx context.Context, id int64, confidence float64, reason string) (string, error) {
	if id == 0 {
		return "", fmt.Errorf("memory_id required")
	}
	if reason == "" {
		return "", fmt.Errorf("reason required for confidence override")
	}
	if confidence < 0 || confidence > 1 {
		return "", fmt.Errorf("confidence must be between 0.0 and 1.0")
	}
	if err := s.memoryStore.UpdateLifecycleFields(ctx, id, map[string]any{
		"confidence": confidence,
	}); err != nil {
		return "", err
	}
	return marshalJSON(map[string]any{
		"memory_id":  id,
		"confidence": confidence,
		"reason":     reason,
		"message":    "confidence updated",
	})
}

func (s *Server) lifecycleSetDefeasibility(ctx context.Context, id int64, defeasibility string) (string, error) {
	if id == 0 {
		return "", fmt.Errorf("memory_id required")
	}
	if !lifecycle.ValidDefeasibility(defeasibility) {
		return "", fmt.Errorf("invalid defeasibility: %s", defeasibility)
	}
	if err := s.memoryStore.UpdateLifecycleFields(ctx, id, map[string]any{
		"defeasibility": defeasibility,
	}); err != nil {
		return "", err
	}
	return marshalJSON(map[string]any{
		"memory_id":     id,
		"defeasibility": defeasibility,
		"message":       "defeasibility updated",
	})
}

func (s *Server) lifecycleSleepStatus() (string, error) {
	return marshalJSON(map[string]any{
		"message": "sleep cycle status not yet tracked (no cycle has run since server start)",
	})
}

func (s *Server) lifecycleDecayPreview(ctx context.Context, id int64, daysAhead int) (string, error) {
	if id == 0 {
		return "", fmt.Errorf("memory_id required")
	}
	if daysAhead <= 0 {
		daysAhead = 30
	}
	mem, err := s.memoryStore.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get memory: %w", err)
	}

	elapsed := time.Since(mem.CreatedAt).Hours() / 24
	if mem.LastRetrievedAt != nil {
		elapsed = time.Since(*mem.LastRetrievedAt).Hours() / 24
	}

	stability := lifecycle.ComputeStability(mem.Stability, mem.Tier, mem.EpistemicType, mem.CitationCount)

	preview := make([]map[string]any, 0, daysAhead)
	for d := 0; d <= daysAhead; d += max(1, daysAhead/10) {
		r := lifecycle.ComputeRetrievability(stability, elapsed+float64(d))
		preview = append(preview, map[string]any{
			"day":            d,
			"retrievability": r,
		})
	}

	return marshalJSON(map[string]any{
		"memory_id":   id,
		"stability":   stability,
		"current_day": int(elapsed),
		"preview":     preview,
	})
}

func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal response: %w", err)
	}
	return string(b), nil
}
