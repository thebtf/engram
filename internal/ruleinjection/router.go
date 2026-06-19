package ruleinjection

import (
	"fmt"
	"html"
	"sort"
	"strings"

	"github.com/thebtf/engram/pkg/models"
)

const (
	ReasonMatched                = "matched"
	ReasonUnknownField           = "unknown_field"
	ReasonProjectMismatch        = "project_mismatch"
	ReasonScopeMismatch          = "scope_mismatch"
	ReasonAudienceMismatch       = "audience_mismatch"
	ReasonSurfaceMismatch        = "surface_mismatch"
	ReasonTagsMismatch           = "tags_mismatch"
	ReasonInvalidPredicate       = "invalid_predicate"
	ReasonSuppressedState        = "suppressed_state"
	ReasonSuppressedPredicate    = "suppressed_predicate"
	ReasonDeferredBudget         = "deferred_budget"
	ReasonSuppressedPromptSafety = "suppressed_prompt_safety"
)

type Metadata struct {
	Project  string
	Scope    string
	Audience string
	Surface  string
	Tags     []string
}

type Caps struct {
	MaxKernel        int
	MaxContextual    int
	MaxRenderedChars int
}

type RulePacket struct {
	ActivationPredicate    map[string]any
	EvidenceHandles        []string
	Content                string
	Summary                string
	Scope                  string
	Audience               string
	State                  models.RuleVersionState
	BudgetClass            string
	ID                     int64
	LegacyBehavioralRuleID *int64
	Priority               int
	Protected              bool
	Pinned                 bool
}

type SuppressedPacket struct {
	ID                     int64
	LegacyBehavioralRuleID *int64
	Reason                 string
}

type SelectionResult struct {
	Kernel        []RulePacket
	Contextual    []RulePacket
	Suppressed    []SuppressedPacket
	BudgetOutcome string
}

func PacketFromRuleVersion(version *models.RuleVersion) RulePacket {
	if version == nil {
		return RulePacket{}
	}
	return RulePacket{
		ID:                     version.ID,
		Content:                version.Content,
		Summary:                version.Summary,
		Scope:                  version.Scope,
		Audience:               version.Audience,
		ActivationPredicate:    copyPredicate(version.ActivationPredicate),
		EvidenceHandles:        append([]string(nil), version.EvidenceHandles...),
		State:                  version.State,
		BudgetClass:            version.BudgetClass,
		LegacyBehavioralRuleID: version.ActiveBehavioralRuleID,
		Priority:               version.Priority,
		Protected:              version.Protected,
		Pinned:                 version.Pinned,
	}
}

func PacketFromBehavioralRule(rule *models.BehavioralRule) RulePacket {
	if rule == nil {
		return RulePacket{}
	}
	legacyID := rule.ID
	scope := "global"
	if rule.Project != nil && strings.TrimSpace(*rule.Project) != "" {
		scope = strings.TrimSpace(*rule.Project)
	}
	return RulePacket{
		ID:                     0,
		Content:                rule.Content,
		Scope:                  scope,
		Audience:               "developer",
		State:                  models.RuleStateActiveGlobal,
		BudgetClass:            "legacy",
		LegacyBehavioralRuleID: &legacyID,
		Priority:               rule.Priority,
	}
}

func (r SelectionResult) HasSuppression(id int64, reason string) bool {
	for _, item := range r.Suppressed {
		if item.ID == id && item.Reason == reason {
			return true
		}
	}
	return false
}

func EvaluatePredicate(predicate map[string]any, meta Metadata) (bool, string) {
	for key, value := range predicate {
		switch key {
		case "project":
			if !matchesOneOf(value, meta.Project) {
				return false, ReasonProjectMismatch
			}
		case "scope":
			if !matchesOneOf(value, meta.Scope) {
				return false, ReasonScopeMismatch
			}
		case "audience":
			if !matchesOneOf(value, meta.Audience) {
				return false, ReasonAudienceMismatch
			}
		case "surface":
			if !matchesOneOf(value, meta.Surface) {
				return false, ReasonSurfaceMismatch
			}
		case "tags_any":
			if !intersects(valuesFromAny(value), meta.Tags) {
				return false, ReasonTagsMismatch
			}
		case "tags_all":
			if !containsAll(meta.Tags, valuesFromAny(value)) {
				return false, ReasonTagsMismatch
			}
		default:
			return false, ReasonUnknownField + ":" + key
		}
	}
	return true, ReasonMatched
}

func SelectPackets(packets []RulePacket, meta Metadata, caps Caps) SelectionResult {
	caps = normalizeCaps(caps)
	ordered := append([]RulePacket(nil), packets...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority == ordered[j].Priority {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Priority > ordered[j].Priority
	})

	result := SelectionResult{BudgetOutcome: "within_budget"}
	renderedChars := 0
	for _, packet := range ordered {
		bucket, reason := renderBucket(packet, meta)
		if bucket == "suppress" {
			result.Suppressed = append(result.Suppressed, suppressedPacket(packet, reason))
			continue
		}
		cost := len(packet.Content) + len(packet.Summary) + 64
		if caps.MaxRenderedChars > 0 && renderedChars+cost > caps.MaxRenderedChars {
			result.Suppressed = append(result.Suppressed, suppressedPacket(packet, ReasonDeferredBudget))
			result.BudgetOutcome = "truncated"
			continue
		}
		switch bucket {
		case "kernel":
			if len(result.Kernel) >= caps.MaxKernel {
				result.Suppressed = append(result.Suppressed, suppressedPacket(packet, ReasonDeferredBudget))
				result.BudgetOutcome = "truncated"
				continue
			}
			result.Kernel = append(result.Kernel, packet)
		case "contextual":
			if len(result.Contextual) >= caps.MaxContextual {
				result.Suppressed = append(result.Suppressed, suppressedPacket(packet, ReasonDeferredBudget))
				result.BudgetOutcome = "truncated"
				continue
			}
			result.Contextual = append(result.Contextual, packet)
		}
		renderedChars += cost
	}
	return result
}

func RenderPrompt(result SelectionResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<engram-rule-packets kernel_count=%d contextual_count=%d suppressed_count=%d budget_outcome=%q>\n",
		len(result.Kernel), len(result.Contextual), len(result.Suppressed), result.BudgetOutcome)
	for _, packet := range result.Kernel {
		fmt.Fprintf(&b, "kernel id=%d scope=%q audience=%q content=%s\n", packet.ID, packet.Scope, packet.Audience, quotePromptData(packet.Content))
	}
	for _, packet := range result.Contextual {
		fmt.Fprintf(&b, "contextual id=%d scope=%q audience=%q content=%s\n", packet.ID, packet.Scope, packet.Audience, quotePromptData(packet.Content))
	}
	b.WriteString("</engram-rule-packets>\n")
	return b.String()
}

func renderBucket(packet RulePacket, meta Metadata) (string, string) {
	if packet.Audience != "" && meta.Audience != "" && packet.Audience != meta.Audience {
		return "suppress", ReasonAudienceMismatch
	}
	switch packet.State {
	case models.RuleStateKernel:
		if packet.LegacyBehavioralRuleID != nil {
			return "contextual", ReasonMatched
		}
		return "kernel", ReasonMatched
	case models.RuleStateActiveProject:
		if !activeProjectAllowed(packet, meta) {
			return "suppress", ReasonSuppressedPredicate
		}
	case models.RuleStateActiveShared, models.RuleStateActiveGlobal:
		// Eligible for contextual routing subject to predicate and budget.
	default:
		return "suppress", ReasonSuppressedState
	}
	ok, reason := EvaluatePredicate(packet.ActivationPredicate, meta)
	if !ok {
		return "suppress", predicateSuppressionReason(reason)
	}
	return "contextual", ReasonMatched
}

func suppressedPacket(packet RulePacket, reason string) SuppressedPacket {
	return SuppressedPacket{
		ID:                     packet.ID,
		LegacyBehavioralRuleID: packet.LegacyBehavioralRuleID,
		Reason:                 reason,
	}
}

func activeProjectAllowed(packet RulePacket, meta Metadata) bool {
	if strings.TrimSpace(meta.Project) == "" {
		return false
	}
	if packet.Scope == meta.Project {
		return true
	}
	value, ok := packet.ActivationPredicate["project"]
	return ok && matchesOneOf(value, meta.Project)
}

func predicateSuppressionReason(reason string) string {
	if strings.HasPrefix(reason, ReasonUnknownField) || reason == ReasonInvalidPredicate {
		return ReasonSuppressedPredicate
	}
	return ReasonSuppressedPredicate
}

func normalizeCaps(caps Caps) Caps {
	if caps.MaxKernel <= 0 {
		caps.MaxKernel = 8
	}
	if caps.MaxContextual <= 0 {
		caps.MaxContextual = 12
	}
	if caps.MaxRenderedChars <= 0 {
		caps.MaxRenderedChars = 4800
	}
	return caps
}

func matchesOneOf(value any, actual string) bool {
	for _, candidate := range valuesFromAny(value) {
		if candidate == actual {
			return true
		}
	}
	return false
}

func valuesFromAny(value any) []string {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func intersects(a, b []string) bool {
	set := make(map[string]struct{}, len(b))
	for _, item := range b {
		set[item] = struct{}{}
	}
	for _, item := range a {
		if _, ok := set[item]; ok {
			return true
		}
	}
	return false
}

func containsAll(haystack, needles []string) bool {
	set := make(map[string]struct{}, len(haystack))
	for _, item := range haystack {
		set[item] = struct{}{}
	}
	for _, item := range needles {
		if _, ok := set[item]; !ok {
			return false
		}
	}
	return len(needles) > 0
}

func quotePromptData(value string) string {
	return html.EscapeString(value)
}

func copyPredicate(value map[string]any) map[string]any {
	if len(value) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(value))
	for k, v := range value {
		out[k] = v
	}
	return out
}
