package s2meta

import (
	"context"
	"fmt"
	"strings"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/cognitive"
)

const (
	defaultQueryLimit = 10
	maxQueryLimit     = 25
)

// MetaIndex is the content-free S2 query seam consumed by the proposer and later
// MCP/session-start adapters.
type MetaIndex interface {
	QueryMetaIndex(ctx context.Context, query gormdb.MetaIndexQuery) ([]gormdb.MetaIndexHit, error)
}

// MetaIndexProposer adapts the content-free S2 index into the shared
// cognitive.CandidateProposer contract.
type MetaIndexProposer struct {
	index MetaIndex
}

func NewMetaIndexProposer(index MetaIndex) *MetaIndexProposer {
	return &MetaIndexProposer{index: index}
}

func (p *MetaIndexProposer) Propose(ctx context.Context, event cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error) {
	if p == nil || p.index == nil {
		return nil, fmt.Errorf("meta index is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	query := gormdb.MetaIndexQuery{
		Project: strings.TrimSpace(event.Project),
		Query:   attentionEventText(event.Payload),
		Tags:    attentionEventTags(event.Payload),
		Limit:   normalizeQueryLimit(limit),
	}
	if query.Query == "" && len(query.Tags) == 0 {
		return []cognitive.HintProposal{}, nil
	}

	hits, err := p.index.QueryMetaIndex(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(hits) > query.Limit {
		hits = hits[:query.Limit]
	}

	proposals := make([]cognitive.HintProposal, 0, len(hits))
	for _, hit := range hits {
		var tags []string
		if hit.Tags != nil {
			tags = make([]string, len(hit.Tags))
			copy(tags, hit.Tags)
		}
		proposals = append(proposals, cognitive.HintProposal{
			ID:        fmt.Sprintf("%d", hit.ID),
			Title:     hit.Title,
			Tags:      tags,
			CreatedAt: hit.CreatedAt,
			Score:     hit.Score,
			Source:    hit.Source,
			Reason:    hit.Reason,
		})
	}
	return proposals, nil
}

func normalizeQueryLimit(limit int) int {
	if limit <= 0 {
		return defaultQueryLimit
	}
	if limit > maxQueryLimit {
		return maxQueryLimit
	}
	return limit
}

func attentionEventText(payload map[string]interface{}) string {
	if payload == nil {
		return ""
	}
	for _, key := range []string{"text", "topic", "query"} {
		if value, ok := payload[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func attentionEventTags(payload map[string]interface{}) []string {
	if payload == nil {
		return nil
	}
	raw, ok := payload["tags"]
	if !ok || raw == nil {
		return nil
	}
	var tags []string
	switch value := raw.(type) {
	case []string:
		for _, tag := range value {
			if trimmed := strings.TrimSpace(tag); trimmed != "" {
				tags = append(tags, trimmed)
			}
		}
	case []interface{}:
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				continue
			}
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				tags = append(tags, trimmed)
			}
		}
	}
	if len(tags) == 0 {
		return nil
	}
	return tags
}
