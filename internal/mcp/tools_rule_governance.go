package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

func ruleGovernanceReadTools(includeUsefulness bool) []Tool {
	tools := []Tool{
		{
			Name:        "rule_governance_health",
			Description: "Read rule-governance lifecycle health counts. Side-effect free; does not call LLM or mutate rule state.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project":     map[string]any{"type": "string", "description": "Project filter. Required for non-admin callers; omission is admin-only."},
					"since":       map[string]any{"type": "string", "description": "Optional RFC3339 lower bound."},
					"since_hours": map[string]any{"type": "number", "minimum": 0, "description": "Optional lookback window when since is omitted."},
					"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Evidence handle limit (default 100)."},
				},
			},
		},
		{
			Name:        "rule_governance_queue",
			Description: "Read grouped rule-governance exception queues. Side-effect free; empty queues return count=0.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project": map[string]any{"type": "string", "description": "Project filter. Required for non-admin callers; omission is admin-only."},
					"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Candidate scan limit (default 100)."},
				},
			},
		},
		{
			Name:        "rule_governance_snapshots",
			Description: "List rule_governance_snapshots. This is separate from bulk-op list_snapshots.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project": map[string]any{"type": "string", "description": "Project filter. Required for non-admin callers; omission is admin-only."},
					"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Snapshot limit (default 50)."},
				},
			},
		},
		{
			Name:        "rule_governance_transition",
			Description: "Admin-only rule-version transition control. Uses the canonical rule lifecycle state machine and audit evidence requirements.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"rule_version_id", "to_state", "actor", "actor_kind", "reason", "evidence_handles"},
				"properties": map[string]any{
					"rule_version_id":  map[string]any{"type": "integer", "minimum": 1},
					"to_state":         map[string]any{"type": "string", "description": "Target rule version state."},
					"actor":            map[string]any{"type": "string"},
					"actor_kind":       map[string]any{"type": "string", "enum": []string{"agent", "operator", "admin", "system", "background", "llm"}},
					"reason":           map[string]any{"type": "string"},
					"evidence_handles": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"snapshot_id":      map[string]any{"type": "string", "description": "Required by active-state transitions."},
				},
			},
		},
		{
			Name:        "rule_governance_pin_snapshot",
			Description: "Admin-only pin/unpin for rule_governance_snapshots. This is separate from bulk-op pin_snapshot.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"snapshot_id"},
				"properties": map[string]any{
					"snapshot_id": map[string]any{"type": "string"},
					"pinned":      map[string]any{"type": "boolean", "description": "Defaults to true."},
				},
			},
		},
		{
			Name:        "rule_governance_rollback",
			Description: "Admin-only rollback for rule_governance_snapshots. Conflict-aware and audit-backed.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"snapshot_id", "actor", "actor_kind", "reason", "evidence_handles"},
				"properties": map[string]any{
					"snapshot_id":      map[string]any{"type": "string"},
					"actor":            map[string]any{"type": "string"},
					"actor_kind":       map[string]any{"type": "string", "enum": []string{"agent", "operator", "admin", "system", "background", "llm"}},
					"reason":           map[string]any{"type": "string"},
					"evidence_handles": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
			},
		},
	}
	if includeUsefulness {
		tools = append(tools, Tool{
			Name:        "rule_governance_usefulness",
			Description: "Read advisory canary/usefulness telemetry from rule_injection_events. Never promotes rules.",
			tier:        tierAdmin,
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"project"},
				"properties": map[string]any{
					"project":         map[string]any{"type": "string", "description": "Required project filter."},
					"rule_version_id": map[string]any{"type": "integer", "minimum": 1, "description": "Optional rule version filter."},
					"since":           map[string]any{"type": "string", "description": "Optional RFC3339 lower bound."},
					"since_hours":     map[string]any{"type": "number", "minimum": 0, "description": "Optional lookback window when since is omitted."},
					"limit":           map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Bucket limit (default 100)."},
				},
			},
		})
	}
	return tools
}

func (s *Server) handleRuleGovernanceHealth(ctx context.Context, args json.RawMessage) (string, error) {
	if err := requireRuleGovernanceReadAccess(ctx); err != nil {
		return "", err
	}
	if s.ruleGovernanceReadStore == nil {
		return "", fmt.Errorf("rule_governance_health: rule governance store not available")
	}
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	since, err := parseRuleGovernanceSince(m)
	if err != nil {
		return "", err
	}
	params := gormdb.RuleGovernanceHealthParams{
		Project:                       strings.TrimSpace(coerceString(m["project"], "")),
		Since:                         since,
		Limit:                         boundedRuleGovernanceLimit(m["limit"], 100, 100),
		IncludeGlobalArbiterRunCounts: ruleGovernanceCallerIsAdmin(ctx),
	}
	if err := requireRuleGovernanceProjectOrAdmin(ctx, params.Project, "rule_governance_health"); err != nil {
		return "", err
	}
	health, err := s.ruleGovernanceReadStore.GetLifecycleHealth(ctx, params)
	if err != nil {
		return "", fmt.Errorf("rule_governance_health: %w", err)
	}
	out := map[string]any{
		"generated_at":                time.Now().UTC().Format(time.RFC3339),
		"project":                     health.Project,
		"since":                       formatRuleGovernanceTime(health.Since),
		"limit":                       health.Limit,
		"no_data":                     health.NoData,
		"candidate_status_counts":     stringRuleCandidateStatusCounts(health.CandidateStatusCounts),
		"version_state_counts":        stringRuleVersionStateCounts(health.VersionStateCounts),
		"arbiter_run_status_counts":   stringRuleArbiterRunStatusCounts(health.ArbiterRunStatusCounts),
		"transition_action_counts":    health.TransitionActionCounts,
		"snapshot_status_counts":      health.SnapshotStatusCounts,
		"injection_event_type_counts": stringRuleInjectionEventTypeCounts(health.InjectionEventTypeCounts),
		"evidence_handles":            health.EvidenceHandles,
		"source_tables":               []string{"rule_candidates", "rule_versions", "rule_arbiter_runs", "rule_transition_log", "rule_governance_snapshots", "rule_injection_events"},
		"side_effect_free":            true,
	}
	if health.NoData {
		out["legal_escape"] = models.RuleEscapeNoData
	}
	return marshalJSON(out)
}

func (s *Server) handleRuleGovernanceQueue(ctx context.Context, args json.RawMessage) (string, error) {
	if err := requireRuleGovernanceReadAccess(ctx); err != nil {
		return "", err
	}
	if s.ruleGovernanceReadStore == nil {
		return "", fmt.Errorf("rule_governance_queue: rule governance store not available")
	}
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	params := gormdb.RuleGovernanceExceptionQueueParams{
		Project: strings.TrimSpace(coerceString(m["project"], "")),
		Limit:   boundedRuleGovernanceLimit(m["limit"], 100, 100),
	}
	if err := requireRuleGovernanceProjectOrAdmin(ctx, params.Project, "rule_governance_queue"); err != nil {
		return "", err
	}
	groups, err := s.ruleGovernanceReadStore.ListExceptionQueueGroups(ctx, params)
	if err != nil {
		return "", fmt.Errorf("rule_governance_queue: %w", err)
	}
	total := 0
	items := make([]ruleGovernanceQueueGroupResponse, 0, len(groups))
	for _, group := range groups {
		converted := ruleGovernanceQueueGroupResponse{
			Reason:                 group.Reason,
			Count:                  group.Count,
			RecommendedNextActions: group.RecommendedNextActions,
			Items:                  make([]ruleGovernanceQueueItemResponse, 0, len(group.Items)),
		}
		for _, item := range group.Items {
			converted.Items = append(converted.Items, ruleGovernanceQueueItemResponse{
				EntityID:               item.EntityID,
				EntityType:             item.EntityType,
				Project:                item.Project,
				Scope:                  item.Scope,
				Reason:                 item.Reason,
				EvidenceHandles:        redactRuleGovernanceEvidenceHandles(item.EvidenceHandles),
				LastActivityAt:         formatRuleGovernanceTime(item.LastActivityAt),
				RecommendedNextActions: item.RecommendedNextActions,
			})
		}
		total += group.Count
		items = append(items, converted)
	}
	return marshalJSON(map[string]any{
		"project":          params.Project,
		"limit":            params.Limit,
		"groups":           items,
		"group_count":      len(items),
		"total_count":      total,
		"empty":            total == 0,
		"side_effect_free": true,
	})
}

func (s *Server) handleRuleGovernanceSnapshots(ctx context.Context, args json.RawMessage) (string, error) {
	if err := requireRuleGovernanceReadAccess(ctx); err != nil {
		return "", err
	}
	if s.ruleGovernanceReadStore == nil {
		return "", fmt.Errorf("rule_governance_snapshots: rule governance store not available")
	}
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	params := gormdb.RuleGovernanceSnapshotListParams{
		Project: strings.TrimSpace(coerceString(m["project"], "")),
		Limit:   boundedRuleGovernanceLimit(m["limit"], 50, 100),
	}
	if err := requireRuleGovernanceProjectOrAdmin(ctx, params.Project, "rule_governance_snapshots"); err != nil {
		return "", err
	}
	snapshots, err := s.ruleGovernanceReadStore.ListRuleGovernanceSnapshots(ctx, params)
	if err != nil {
		return "", fmt.Errorf("rule_governance_snapshots: %w", err)
	}
	out := make([]ruleGovernanceSnapshotResponse, 0, len(snapshots))
	for _, snap := range snapshots {
		out = append(out, ruleGovernanceSnapshotResponse{
			SnapshotID:   snap.SnapshotID,
			OpType:       snap.OpType,
			Actor:        snap.Actor,
			Status:       snap.Status,
			CreatedAt:    formatRuleGovernanceTime(snap.CreatedAt),
			RolledBackAt: formatRuleGovernanceTimePtr(snap.RolledBackAt),
			Pinned:       snap.Pinned,
		})
	}
	return marshalJSON(map[string]any{
		"project":          params.Project,
		"limit":            params.Limit,
		"snapshots":        out,
		"count":            len(out),
		"source_table":     "rule_governance_snapshots",
		"bulk_op_surface":  false,
		"side_effect_free": true,
	})
}

func (s *Server) handleRuleGovernanceUsefulness(ctx context.Context, args json.RawMessage) (string, error) {
	if err := requireRuleGovernanceReadAccess(ctx); err != nil {
		return "", err
	}
	if s.ruleInjectionTelemetry == nil {
		return "", fmt.Errorf("rule_governance_usefulness: rule injection telemetry store not available")
	}
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	project := strings.TrimSpace(coerceString(m["project"], ""))
	if project == "" {
		return "", fmt.Errorf("project_required: rule_governance_usefulness requires project")
	}
	if err := requireRuleGovernanceProjectOrAdmin(ctx, project, "rule_governance_usefulness"); err != nil {
		return "", err
	}
	since, err := parseRuleGovernanceSince(m)
	if err != nil {
		return "", err
	}
	params := gormdb.RuleInjectionTelemetryParams{
		Project:       project,
		Since:         since,
		RuleVersionID: coerceInt64(m["rule_version_id"], 0),
		Limit:         boundedRuleGovernanceLimit(m["limit"], 100, 100),
	}
	aggregate, err := s.ruleInjectionTelemetry.AggregateByProjectRuleAndEventType(ctx, params)
	if err != nil {
		return "", fmt.Errorf("rule_governance_usefulness: %w", err)
	}
	buckets := make([]ruleGovernanceUsefulnessBucketResponse, 0, len(aggregate.Buckets))
	for _, bucket := range aggregate.Buckets {
		buckets = append(buckets, ruleGovernanceUsefulnessBucketResponse{
			EventType:  string(bucket.EventType),
			Count:      bucket.Count,
			LastSeenAt: formatRuleGovernanceTime(bucket.LastSeenAt),
			Reasons:    bucket.Reasons,
		})
	}
	out := map[string]any{
		"project":          aggregate.Project,
		"rule_version_id":  aggregate.RuleVersionID,
		"since":            formatRuleGovernanceTime(params.Since),
		"limit":            params.Limit,
		"buckets":          buckets,
		"bucket_count":     len(buckets),
		"no_data":          aggregate.NoData,
		"advisory_only":    true,
		"auto_promotion":   false,
		"source_table":     "rule_injection_events",
		"side_effect_free": true,
	}
	if aggregate.NoData {
		out["legal_escape"] = models.RuleEscapeNoData
	}
	return marshalJSON(out)
}

func (s *Server) handleRuleGovernanceTransition(ctx context.Context, args json.RawMessage) (string, error) {
	if err := requireRuleGovernanceAdminAccess(ctx); err != nil {
		return "", err
	}
	if s.ruleGovernanceReadStore == nil {
		return "", fmt.Errorf("rule_governance_transition: rule governance store not available")
	}
	writeStore, ok := s.ruleGovernanceReadStore.(ruleGovernanceWriteStore)
	if !ok {
		return "", fmt.Errorf("rule_governance_transition: transition store not available")
	}
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	versionID, err := requireInt64Arg(m, "rule_version_id")
	if err != nil || versionID <= 0 {
		return "", fmt.Errorf("rule_governance_transition: rule_version_id must be a positive integer")
	}
	toValue, _, err := optionalStringArg(m, "to_state")
	if err != nil {
		return "", fmt.Errorf("rule_governance_transition: %w", err)
	}
	to := models.RuleVersionState(strings.TrimSpace(toValue))
	req, err := parseRuleGovernanceTransitionRequest(m)
	if err != nil {
		return "", fmt.Errorf("rule_governance_transition: %w", err)
	}
	version, err := writeStore.TransitionRuleVersion(ctx, versionID, to, req)
	if err != nil {
		return "", fmt.Errorf("rule_governance_transition: %w", err)
	}
	return marshalJSON(map[string]any{
		"rule_version_id":  version.ID,
		"state":            string(version.State),
		"scope":            version.Scope,
		"audience":         version.Audience,
		"snapshot_id":      req.SnapshotID,
		"actor":            req.Actor,
		"actor_kind":       string(req.ActorKind),
		"evidence_handles": req.EvidenceHandles,
		"source_table":     "rule_versions",
	})
}

func (s *Server) handleRuleGovernancePinSnapshot(ctx context.Context, args json.RawMessage) (string, error) {
	if err := requireRuleGovernanceAdminAccess(ctx); err != nil {
		return "", err
	}
	if s.ruleGovernanceReadStore == nil {
		return "", fmt.Errorf("rule_governance_pin_snapshot: rule governance store not available")
	}
	writeStore, ok := s.ruleGovernanceReadStore.(ruleGovernanceWriteStore)
	if !ok {
		return "", fmt.Errorf("rule_governance_pin_snapshot: transition store not available")
	}
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	snapshotValue, _, err := optionalStringArg(m, "snapshot_id")
	if err != nil {
		return "", fmt.Errorf("rule_governance_pin_snapshot: %w", err)
	}
	snapshotID := strings.TrimSpace(snapshotValue)
	pinned, present, err := optionalBoolArg(m, "pinned")
	if err != nil {
		return "", fmt.Errorf("rule_governance_pin_snapshot: %w", err)
	}
	if !present {
		pinned = true
	}
	summary, err := writeStore.PinRuleGovernanceSnapshot(ctx, snapshotID, pinned)
	if err != nil {
		return "", fmt.Errorf("rule_governance_pin_snapshot: %w", err)
	}
	return marshalJSON(map[string]any{
		"snapshot_id":     summary.SnapshotID,
		"status":          summary.Status,
		"pinned":          summary.Pinned,
		"source_table":    "rule_governance_snapshots",
		"bulk_op_surface": false,
	})
}

func (s *Server) handleRuleGovernanceRollback(ctx context.Context, args json.RawMessage) (string, error) {
	if err := requireRuleGovernanceAdminAccess(ctx); err != nil {
		return "", err
	}
	if s.ruleGovernanceReadStore == nil {
		return "", fmt.Errorf("rule_governance_rollback: rule governance store not available")
	}
	writeStore, ok := s.ruleGovernanceReadStore.(ruleGovernanceWriteStore)
	if !ok {
		return "", fmt.Errorf("rule_governance_rollback: transition store not available")
	}
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	snapshotValue, _, err := optionalStringArg(m, "snapshot_id")
	if err != nil {
		return "", fmt.Errorf("rule_governance_rollback: %w", err)
	}
	snapshotID := strings.TrimSpace(snapshotValue)
	req, err := parseRuleGovernanceTransitionRequest(m)
	if err != nil {
		return "", fmt.Errorf("rule_governance_rollback: %w", err)
	}
	result, err := writeStore.RollbackRuleGovernanceSnapshot(ctx, snapshotID, req)
	if err != nil {
		if len(result.ConflictVersionIDs) > 0 {
			return marshalJSON(map[string]any{
				"snapshot_id": result.SnapshotID, "restored_version_ids": result.RestoredVersionIDs,
				"conflict_version_ids": result.ConflictVersionIDs, "status": "rollback_conflict", "ok": false,
				"source_table": "rule_governance_snapshots", "bulk_op_surface": false,
			})
		}
		return "", fmt.Errorf("rule_governance_rollback: %w", err)
	}
	return marshalJSON(map[string]any{
		"snapshot_id": result.SnapshotID, "restored_version_ids": result.RestoredVersionIDs,
		"conflict_version_ids": result.ConflictVersionIDs, "status": "rolled_back", "ok": true,
		"source_table": "rule_governance_snapshots", "bulk_op_surface": false,
	})
}

type ruleGovernanceQueueGroupResponse struct {
	Items                  []ruleGovernanceQueueItemResponse `json:"items"`
	RecommendedNextActions []string                          `json:"recommended_next_actions"`
	Reason                 string                            `json:"reason"`
	Count                  int                               `json:"count"`
}

type ruleGovernanceQueueItemResponse struct {
	LastActivityAt         string   `json:"last_activity_at"`
	EvidenceHandles        []string `json:"evidence_handles"`
	RecommendedNextActions []string `json:"recommended_next_actions"`
	EntityType             string   `json:"entity_type"`
	Project                string   `json:"project"`
	Scope                  string   `json:"scope"`
	Reason                 string   `json:"reason"`
	EntityID               int64    `json:"entity_id"`
}

type ruleGovernanceSnapshotResponse struct {
	RolledBackAt *string `json:"rolled_back_at,omitempty"`
	SnapshotID   string  `json:"snapshot_id"`
	OpType       string  `json:"op_type"`
	Actor        string  `json:"actor"`
	Status       string  `json:"status"`
	CreatedAt    string  `json:"created_at"`
	Pinned       bool    `json:"pinned"`
}

type ruleGovernanceUsefulnessBucketResponse struct {
	Reasons    []string `json:"reasons"`
	LastSeenAt string   `json:"last_seen_at"`
	EventType  string   `json:"event_type"`
	Count      int      `json:"count"`
}

func requireRuleGovernanceReadAccess(ctx context.Context) error {
	if id, ok := auth.IdentityFrom(ctx); ok {
		if id.Role != "" && id.Source != "" {
			return nil
		}
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("ENGRAM_AUTH_DISABLED")), "true") {
		return nil
	}
	return fmt.Errorf("auth_required: rule governance read tools require authenticated identity")
}

func requireRuleGovernanceProjectOrAdmin(ctx context.Context, project string, toolName string) error {
	if strings.TrimSpace(project) != "" {
		return nil
	}
	if id, ok := auth.IdentityFrom(ctx); ok && id.IsAdmin() {
		return nil
	}
	return fmt.Errorf("project_required: %s requires project for non-admin reads", toolName)
}

func ruleGovernanceCallerIsAdmin(ctx context.Context) bool {
	id, ok := auth.IdentityFrom(ctx)
	return ok && id.IsAdmin()
}

func requireRuleGovernanceAdminAccess(ctx context.Context) error {
	id, ok := auth.IdentityFrom(ctx)
	if !ok || !id.IsAdmin() {
		return fmt.Errorf("admin_required: rule governance mutation tools require admin identity")
	}
	return nil
}

func redactRuleGovernanceEvidenceHandles(handles []string) []string {
	out := make([]string, 0, len(handles))
	seen := map[string]struct{}{}
	for _, handle := range handles {
		redacted := redactRuleGovernanceEvidenceHandle(handle)
		if redacted == "" {
			continue
		}
		if _, ok := seen[redacted]; ok {
			continue
		}
		seen[redacted] = struct{}{}
		out = append(out, redacted)
	}
	return out
}

func redactRuleGovernanceEvidenceHandle(handle string) string {
	trimmed := strings.TrimSpace(handle)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "session:") {
		return "session:<redacted>"
	}
	if ruleGovernanceEvidenceHandleHasSensitiveText(lower) {
		return "evidence:<redacted-sensitive>"
	}
	if isCanonicalRuleGovernanceEvidenceHandle(trimmed) {
		return trimmed
	}
	return "evidence:<redacted>"
}

func ruleGovernanceEvidenceHandleHasSensitiveText(lower string) bool {
	return strings.Contains(lower, "secret") || strings.Contains(lower, "token") ||
		strings.Contains(lower, "credential") || strings.Contains(lower, "private") ||
		strings.Contains(lower, "key")
}

func isCanonicalRuleGovernanceEvidenceHandle(handle string) bool {
	prefix, value, ok := strings.Cut(handle, ":")
	if !ok {
		return false
	}
	prefix = strings.TrimSpace(prefix)
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	switch strings.ToLower(prefix) {
	case "rule_candidate", "rule_version", "rule_injection_event", "legacy_behavioral_rule", "candidate":
		id, err := strconv.ParseInt(value, 10, 64)
		return err == nil && id > 0
	case "rule_governance_snapshot":
		return isSafeRuleGovernanceEvidenceID(value)
	default:
		return false
	}
}

func isSafeRuleGovernanceEvidenceID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func parseRuleGovernanceTransitionRequest(m map[string]any) (gormdb.RuleTransitionRequest, error) {
	values := make(map[string]string, 4)
	for _, key := range []string{"actor", "actor_kind", "reason", "snapshot_id"} {
		value, _, err := optionalStringArg(m, key)
		if err != nil {
			return gormdb.RuleTransitionRequest{}, err
		}
		values[key] = strings.TrimSpace(value)
	}
	evidence, _, err := optionalStringSliceArg(m, "evidence_handles")
	if err != nil {
		return gormdb.RuleTransitionRequest{}, err
	}
	return gormdb.RuleTransitionRequest{
		Actor: values["actor"], ActorKind: models.RuleActorKind(values["actor_kind"]), Reason: values["reason"],
		EvidenceHandles: evidence, SnapshotID: values["snapshot_id"],
	}, nil
}

func parseRuleGovernanceSince(m map[string]any) (time.Time, error) {
	if raw := strings.TrimSpace(coerceString(m["since"], "")); raw != "" {
		since, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, fmt.Errorf("since must be RFC3339: %w", err)
		}
		return since.UTC(), nil
	}
	hours := coerceFloat64(m["since_hours"], 0)
	if hours <= 0 {
		return time.Time{}, nil
	}
	const maxRuleGovernanceSinceHours = 24 * 365 * 10
	if hours > maxRuleGovernanceSinceHours {
		hours = maxRuleGovernanceSinceHours
	}
	return time.Now().UTC().Add(-time.Duration(hours * float64(time.Hour))), nil
}

func boundedRuleGovernanceLimit(raw any, fallback, max int) int {
	limit := coerceInt(raw, fallback)
	if limit <= 0 {
		return fallback
	}
	if limit > max {
		return max
	}
	return limit
}

func formatRuleGovernanceTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func formatRuleGovernanceTimePtr(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	formatted := formatRuleGovernanceTime(*t)
	return &formatted
}

func stringRuleCandidateStatusCounts(in map[models.RuleCandidateStatus]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[string(key)] = value
	}
	return out
}

func stringRuleVersionStateCounts(in map[models.RuleVersionState]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[string(key)] = value
	}
	return out
}

func stringRuleArbiterRunStatusCounts(in map[models.RuleArbiterRunStatus]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[string(key)] = value
	}
	return out
}

func stringRuleInjectionEventTypeCounts(in map[models.RuleInjectionEventType]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[string(key)] = value
	}
	return out
}
