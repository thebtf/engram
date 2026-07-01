package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	experiencehistory "github.com/thebtf/engram/internal/experience"
	"github.com/thebtf/engram/pkg/cognitive"
)

type experienceProvider interface {
	QueryExperience(ctx context.Context, request cognitive.ExperienceQueryRequest) ([]cognitive.ExperienceResponse, error)
}

// SetExperienceProvider wires the CR-009 first-class experience/history read seam.
func (s *Server) SetExperienceProvider(provider experienceProvider) {
	s.experienceProvider = provider
}

func experienceHistoryTools() []Tool {
	return []Tool{
		{
			Name:        "experience_history.read",
			Description: "CR-009: read bounded historical experience through the first-class experience/applicability path. Archive resurfacing runs only for named trigger classes or explicit_archive_lookup.",
			tier:        tierCore,
			InputSchema: experienceHistoryReadSchema(),
		},
		{
			Name:        "experience_history.detail",
			Description: "CR-009: read one experience detail with lesson, applicability/anti-applicability evidence, provenance, storage_origin, and archive trace.",
			tier:        tierCore,
			InputSchema: experienceHistoryDetailSchema(),
		},
	}
}

func experienceHistoryReadSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"project"},
		"properties": map[string]any{
			"project":                 map[string]any{"type": "string", "description": "Project scope for the experience read."},
			"principal":               map[string]any{"type": "string", "description": "Requesting principal or agent identity."},
			"domain":                  map[string]any{"type": "string", "description": "Optional memory domain scope."},
			"query_text":              map[string]any{"type": "string", "description": "Historical question or intent. Alias: query."},
			"query":                   map[string]any{"type": "string", "description": "Alias for query_text."},
			"current_context":         map[string]any{"type": "string", "description": "Current work context for applicability and anti-applicability checks."},
			"situation":               map[string]any{"type": "string", "description": "Optional situation facet."},
			"decision":                map[string]any{"type": "string", "description": "Optional decision facet."},
			"action":                  map[string]any{"type": "string", "description": "Optional action facet."},
			"outcome":                 map[string]any{"type": "string", "description": "Optional outcome facet."},
			"revision":                map[string]any{"type": "string", "description": "Optional revision facet."},
			"reversal":                map[string]any{"type": "string", "description": "Optional reversal facet."},
			"trigger_class":           map[string]any{"type": "string", "enum": experienceHistoryTriggerEnum(), "description": "Single named archive trigger class."},
			"archive_trigger_classes": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": experienceHistoryTriggerEnum()}, "description": "Named archive trigger classes. Ordinary hot-path requests leave this empty."},
			"explicit_archive_lookup": map[string]any{"type": "boolean", "description": "When true, adds explicit_archive_lookup as the archive trigger class."},
			"limit":                   map[string]any{"type": "integer", "minimum": 1, "maximum": experiencehistory.MaxQueryLimit, "description": "Bounded result limit. Defaults to 5, max 10."},
		},
	}
}

func experienceHistoryDetailSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"project", "experience_id"},
		"properties": map[string]any{
			"project":                 map[string]any{"type": "string", "description": "Project scope for the detail read."},
			"principal":               map[string]any{"type": "string", "description": "Requesting principal or agent identity."},
			"domain":                  map[string]any{"type": "string", "description": "Optional memory domain scope."},
			"experience_id":           map[string]any{"type": "string", "description": "Experience id returned by experience_history.read, or a raw provenance id."},
			"current_context":         map[string]any{"type": "string", "description": "Current context for applicability checks."},
			"trigger_class":           map[string]any{"type": "string", "enum": experienceHistoryTriggerEnum(), "description": "Single named archive trigger class."},
			"archive_trigger_classes": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": experienceHistoryTriggerEnum()}, "description": "Named archive trigger classes for detail archive trace."},
			"explicit_archive_lookup": map[string]any{"type": "boolean", "description": "When true, adds explicit_archive_lookup as the archive trigger class."},
		},
	}
}

func experienceHistoryTriggerEnum() []string {
	classes := experiencehistory.AllowedArchiveTriggerClasses()
	out := make([]string, 0, len(classes))
	for _, class := range classes {
		out = append(out, string(class))
	}
	return out
}

func (s *Server) handleExperienceHistoryRead(ctx context.Context, args json.RawMessage) (string, error) {
	request, err := parseExperienceHistoryReadArgs(args)
	if err != nil {
		return "", err
	}
	response, err := experiencehistory.ReadHistory(ctx, s.experienceProvider, request, time.Now().UTC())
	if err != nil {
		return "", err
	}
	return marshalExperienceHistory("experience_history.read", response)
}

func (s *Server) handleExperienceHistoryDetail(ctx context.Context, args json.RawMessage) (string, error) {
	request, err := parseExperienceHistoryDetailArgs(args)
	if err != nil {
		return "", err
	}
	response, err := experiencehistory.ReadHistoryDetail(ctx, s.experienceProvider, request, time.Now().UTC())
	if err != nil {
		return "", err
	}
	return marshalExperienceHistory("experience_history.detail", response)
}

func parseExperienceHistoryReadArgs(args json.RawMessage) (cognitive.ExperienceQueryRequest, error) {
	m, err := parseArgs(args)
	if err != nil {
		return cognitive.ExperienceQueryRequest{}, err
	}
	query := strings.TrimSpace(coerceString(m["query_text"], ""))
	if query == "" {
		query = strings.TrimSpace(coerceString(m["query"], ""))
	}
	request := cognitive.ExperienceQueryRequest{
		Project:               strings.TrimSpace(coerceString(m["project"], "")),
		Principal:             strings.TrimSpace(coerceString(m["principal"], "")),
		Domain:                strings.TrimSpace(coerceString(m["domain"], "")),
		Query:                 query,
		CurrentContext:        strings.TrimSpace(coerceString(m["current_context"], "")),
		Situation:             strings.TrimSpace(coerceString(m["situation"], "")),
		Decision:              strings.TrimSpace(coerceString(m["decision"], "")),
		Action:                strings.TrimSpace(coerceString(m["action"], "")),
		Outcome:               strings.TrimSpace(coerceString(m["outcome"], "")),
		Revision:              strings.TrimSpace(coerceString(m["revision"], "")),
		Reversal:              strings.TrimSpace(coerceString(m["reversal"], "")),
		ArchiveTriggerClasses: experienceHistoryTriggersFromArgs(m),
		Limit:                 coerceInt(m["limit"], 0),
	}
	return request, nil
}

func parseExperienceHistoryDetailArgs(args json.RawMessage) (experiencehistory.HistoryDetailRequest, error) {
	m, err := parseArgs(args)
	if err != nil {
		return experiencehistory.HistoryDetailRequest{}, err
	}
	return experiencehistory.HistoryDetailRequest{
		Project:               strings.TrimSpace(coerceString(m["project"], "")),
		Principal:             strings.TrimSpace(coerceString(m["principal"], "")),
		Domain:                strings.TrimSpace(coerceString(m["domain"], "")),
		ExperienceID:          strings.TrimSpace(coerceString(m["experience_id"], "")),
		CurrentContext:        strings.TrimSpace(coerceString(m["current_context"], "")),
		ArchiveTriggerClasses: experienceHistoryTriggersFromArgs(m),
	}, nil
}

func experienceHistoryTriggersFromArgs(m map[string]any) []cognitive.ExperienceArchiveTriggerClass {
	seen := map[cognitive.ExperienceArchiveTriggerClass]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		seen[cognitive.ExperienceArchiveTriggerClass(value)] = true
	}
	add(coerceString(m["trigger_class"], ""))
	for _, value := range coerceStringSlice(m["archive_trigger_classes"]) {
		add(value)
	}
	if coerceBool(m["explicit_archive_lookup"], false) {
		seen[cognitive.ExperienceArchiveTriggerExplicitLookup] = true
	}
	out := make([]cognitive.ExperienceArchiveTriggerClass, 0, len(seen))
	for class := range seen {
		out = append(out, class)
	}
	return out
}

func marshalExperienceHistory(tool string, payload any) (string, error) {
	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("%s marshal response: %w", tool, err)
	}
	return string(out), nil
}
