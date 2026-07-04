package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

const knowAboutMaxLimit = 25

func s2MetaMemoryEnabled() bool {
	return os.Getenv("ENGRAM_V7_PLUG_ENABLED") == "true" && os.Getenv("ENGRAM_V7_S2_METAMEM") == "true"
}

func knowAboutTool() Tool {
	return Tool{
		Name:        "know_about",
		Description: "Content-free memory discovery by topic. Returns ids, titles, tags, dates, scores, and reasons without memory body text.",
		tier:        tierUseful,
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"topic"},
			"properties": map[string]any{
				"topic":   map[string]any{"type": "string", "description": "Topic to discover in project memory via tag-prefix and full-text metadata search."},
				"project": map[string]any{"type": "string", "description": "Optional project. Defaults from current request context."},
				"limit":   map[string]any{"type": "integer", "default": 10, "minimum": 1, "maximum": knowAboutMaxLimit},
			},
		},
	}
}

func (s *Server) handleKnowAbout(ctx context.Context, args json.RawMessage) (string, error) {
	if s.metaMemoryIndex == nil {
		return "", fmt.Errorf("meta memory index not available")
	}
	if !s2MetaMemoryEnabled() {
		return "", fmt.Errorf("know_about unavailable unless ENGRAM_V7_PLUG_ENABLED=true and ENGRAM_V7_S2_METAMEM=true")
	}
	m, err := parseArgs(args)
	if err != nil {
		return "", err
	}

	topic := strings.TrimSpace(coerceString(m["topic"], ""))
	if topic == "" {
		return "", fmt.Errorf("topic is required")
	}
	project := strings.TrimSpace(coerceString(m["project"], ""))
	if project == "" {
		project = strings.TrimSpace(projectFromContext(ctx))
	}
	if project == "" {
		return "", fmt.Errorf("project is required")
	}
	limit, err := parseKnowAboutLimit(m["limit"])
	if err != nil {
		return "", err
	}

	query := gormdb.MetaIndexQuery{
		Project: project,
		Query:   topic,
		Tags:    []string{topic},
		Limit:   limit,
	}
	if id, ok := auth.IdentityFrom(ctx); ok {
		if principal, kind, hasOwner := id.MemoryOwner(); hasOwner {
			query.OwnerPrincipal = principal
			query.OwnerPrincipalKind = kind
		}
	}

	hits, err := s.metaMemoryIndex.QueryMetaIndex(ctx, query)
	if err != nil {
		return "", fmt.Errorf("know_about query failed: %w", err)
	}
	if hits == nil {
		hits = []gormdb.MetaIndexHit{}
	}

	response := map[string]any{
		"topic":      topic,
		"project":    project,
		"count":      len(hits),
		"source":     "s2_meta_index",
		"top_tags":   summarizeMetaIndexTags(hits, 6),
		"date_range": summarizeMetaIndexDateRange(hits),
		"hits":       hits,
	}
	if len(hits) == 0 {
		response["message"] = "no meta-memory hits found for topic"
	}
	return marshalJSON(response)
}

func parseKnowAboutLimit(raw any) (int, error) {
	if raw == nil {
		return 10, nil
	}
	limit, err := parsePrincipalMemoryQueryInt(raw)
	if err != nil || limit < 1 {
		return 0, fmt.Errorf("limit must be a positive integer")
	}
	if limit > knowAboutMaxLimit {
		return knowAboutMaxLimit, nil
	}
	return limit, nil
}

func summarizeMetaIndexTags(hits []gormdb.MetaIndexHit, max int) []string {
	counts := map[string]int{}
	for _, hit := range hits {
		for _, tag := range hit.Tags {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			counts[tag]++
		}
	}
	type countedTag struct {
		tag   string
		count int
	}
	items := make([]countedTag, 0, len(counts))
	for tag, count := range counts {
		items = append(items, countedTag{tag: tag, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count != items[j].count {
			return items[i].count > items[j].count
		}
		return items[i].tag < items[j].tag
	})
	if max <= 0 || max > len(items) {
		max = len(items)
	}
	out := make([]string, 0, max)
	for _, item := range items[:max] {
		out = append(out, item.tag)
	}
	return out
}

func summarizeMetaIndexDateRange(hits []gormdb.MetaIndexHit) map[string]any {
	if len(hits) == 0 {
		return map[string]any{}
	}
	from := hits[0].CreatedAt
	to := hits[0].UpdatedAt
	for _, hit := range hits[1:] {
		if hit.CreatedAt.Before(from) {
			from = hit.CreatedAt
		}
		if hit.UpdatedAt.After(to) {
			to = hit.UpdatedAt
		}
	}
	return map[string]any{
		"from": from.UTC().Format(time.RFC3339),
		"to":   to.UTC().Format(time.RFC3339),
	}
}
