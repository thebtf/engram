package s2meta

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/cognitive"
)

type recordingMetaIndex struct {
	queries []gormdb.MetaIndexQuery
	hits    []gormdb.MetaIndexHit
}

func (r *recordingMetaIndex) QueryMetaIndex(ctx context.Context, query gormdb.MetaIndexQuery) ([]gormdb.MetaIndexHit, error) {
	r.queries = append(r.queries, query)
	if r.hits != nil {
		return r.hits, nil
	}

	limit := query.Limit
	if limit < 0 {
		limit = 0
	}

	hits := make([]gormdb.MetaIndexHit, limit)
	for i := range limit {
		hits[i] = gormdb.MetaIndexHit{
			ID:        int64(9000 + i),
			Project:   query.Project,
			Title:     fmt.Sprintf("index title %02d", i),
			Tags:      []string{"s2:meta", "intent:handoff"},
			CreatedAt: time.Unix(1700000000+int64(i), 0).UTC(),
			Score:     1 - float32(i)*0.01,
			Source:    "s2.meta_index",
		}
	}
	return hits, nil
}

func TestMetaIndexProposer_DefaultLimitAndMaxLimit(t *testing.T) {
	idx := &recordingMetaIndex{}
	proposer := NewMetaIndexProposer(idx)
	ctx := context.Background()
	event := cognitive.AttentionEvent{
		Type:      "user_prompt_submit",
		SessionID: "session-s2-default-limit",
		Project:   "project-s2-default-limit",
		Payload: map[string]interface{}{
			"text": "handoff summary asks for recurring release checklist context",
			"tags": []string{"intent:handoff"},
		},
		Timestamp: time.Unix(1700000100, 0).UTC(),
	}

	defaulted, err := proposer.Propose(ctx, event, 0)
	require.NoError(t, err)
	require.Len(t, defaulted, 10, "zero caller limit must still return the bounded ten best meta-index hints")
	require.Len(t, idx.queries, 1)
	require.Equal(t, 10, idx.queries[0].Limit, "the S2 proposer must bound the store query itself, not over-fetch then truncate")

	capped, err := proposer.Propose(ctx, event, 99)
	require.NoError(t, err)
	require.Len(t, capped, 25, "oversized caller limit must cap at twenty-five content-free hints")
	require.Len(t, idx.queries, 2)
	require.Equal(t, 25, idx.queries[1].Limit, "the max-limit cap is part of the index query contract, not just presentation trimming")
}

func TestMetaIndexProposer_EmitsContentFreeHintProposals(t *testing.T) {
	idx := &recordingMetaIndex{}
	proposer := NewMetaIndexProposer(idx)
	ctx := context.Background()
	event := cognitive.AttentionEvent{
		Type:      "tool_result_surprise",
		SessionID: "session-s2-content-free",
		Project:   "project-s2-content-free",
		Payload: map[string]interface{}{
			"text": "needle-private-body-token should only be used for retrieval, never emitted",
			"tags": []string{"s2:meta"},
		},
		Timestamp: time.Unix(1700000200, 0).UTC(),
	}

	proposals, err := proposer.Propose(ctx, event, 3)
	require.NoError(t, err)
	require.Len(t, proposals, 3, "non-empty meta-index hits must become candidate proposals; a nil/empty stub is not acceptable")

	payload, err := json.Marshal(proposals)
	require.NoError(t, err)
	serialized := strings.ToLower(string(payload))
	require.NotContains(t, serialized, "content", "S2 proposals must not expose a content field; callers expand by memory id")
	require.NotContains(t, serialized, "needle-private-body-token", "S2 proposals must not leak raw event or memory body text")

	for _, proposal := range proposals {
		require.NotEmpty(t, proposal.ID, "proposal must carry the expandable memory identity")
		require.NotEmpty(t, proposal.Title, "proposal must carry a bounded title instead of raw content")
		require.Equal(t, "s2.meta_index", proposal.Source)
	}
}

func TestMetaIndexProposer_ClonesHintProposalTags(t *testing.T) {
	idx := &recordingMetaIndex{hits: []gormdb.MetaIndexHit{
		{
			ID:        1,
			Project:   "project-s2-clone",
			Title:     "tag clone",
			Tags:      []string{"s2:meta"},
			CreatedAt: time.Unix(1700000300, 0).UTC(),
			Score:     0.9,
			Source:    "s2.meta_index",
		},
		{
			ID:        2,
			Project:   "project-s2-clone",
			Title:     "explicit empty tags",
			Tags:      []string{},
			CreatedAt: time.Unix(1700000301, 0).UTC(),
			Score:     0.8,
			Source:    "s2.meta_index",
		},
	}}
	proposer := NewMetaIndexProposer(idx)
	event := cognitive.AttentionEvent{Project: "project-s2-clone", Payload: map[string]interface{}{"text": "tag clone", "tags": []string{"s2:meta"}}}

	proposals, err := proposer.Propose(context.Background(), event, 2)
	require.NoError(t, err)
	require.Len(t, proposals, 2)

	proposals[0].Tags[0] = "mutated"
	require.Equal(t, "s2:meta", idx.hits[0].Tags[0], "hint proposal tags must not alias the source meta-index hit backing array")
	require.NotNil(t, proposals[1].Tags, "explicit empty tag slices should remain explicit after cloning")
	require.Len(t, proposals[1].Tags, 0)
}
