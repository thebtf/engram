package s2meta

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/cognitive/core"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/cognitive"
)

type s2SubsystemRecordingIndex struct {
	queries []gormdb.MetaIndexQuery
	hits    []gormdb.MetaIndexHit
}

func (r *s2SubsystemRecordingIndex) QueryMetaIndex(ctx context.Context, query gormdb.MetaIndexQuery) ([]gormdb.MetaIndexHit, error) {
	r.queries = append(r.queries, query)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]gormdb.MetaIndexHit(nil), r.hits...), nil
}

func TestSubsystemIdentityAndImplementsCandidateProposer(t *testing.T) {
	sub := NewSubsystem(&s2SubsystemRecordingIndex{})

	require.Equal(t, "engram.s2.meta_memory", sub.Name())
	require.Equal(t, "v1.0.0", sub.Version())
	require.Equal(t, []string{"CandidateProposer"}, sub.Implements())

	_, isSubsystem := any(sub).(core.Subsystem)
	require.True(t, isSubsystem, "S2 proposer must be registry-managed as a CORE subsystem")
	_, isProposer := any(sub).(cognitive.CandidateProposer)
	require.True(t, isProposer, "S2 proposer must satisfy the S3 CandidateProposer request surface")
}

func TestSubsystemProposeReturnsContentFreeMetaCandidates(t *testing.T) {
	createdAt := time.Unix(1700001000, 0).UTC()
	idx := &s2SubsystemRecordingIndex{hits: []gormdb.MetaIndexHit{{
		ID:        42,
		Project:   "engram",
		Title:     "Release handoff checklist",
		Tags:      []string{"release", "handoff"},
		CreatedAt: createdAt,
		UpdatedAt: createdAt.Add(time.Hour),
		Score:     0.875,
		Source:    "s2.meta_index",
		Reason:    "tag:handoff",
	}}}
	sub := NewSubsystem(idx)

	proposals, err := sub.Propose(context.Background(), cognitive.AttentionEvent{
		Type:      "user_prompt_submit",
		SessionID: "session-s2-content-free",
		Project:   "engram",
		Payload: map[string]interface{}{
			"text": "Find the release handoff checklist but never echo needle-private-body-token",
			"tags": []string{"handoff"},
		},
		Timestamp: createdAt.Add(time.Minute),
	}, 5)

	require.NoError(t, err)
	require.Len(t, proposals, 1, "S2 must return real candidates from the meta index; an empty stub would hide usable memory from S3")
	require.Equal(t, cognitive.HintProposal{
		ID:        "42",
		Title:     "Release handoff checklist",
		Tags:      []string{"release", "handoff"},
		CreatedAt: createdAt,
		Score:     0.875,
		Source:    "s2.meta_index",
		Reason:    "tag:handoff",
	}, proposals[0])

	payload, err := json.Marshal(proposals[0])
	require.NoError(t, err)
	var fields map[string]interface{}
	require.NoError(t, json.Unmarshal(payload, &fields))
	allowed := map[string]bool{
		"id": true, "title": true, "tags": true, "created_at": true,
		"score": true, "source": true, "reason": true,
	}
	for field := range fields {
		require.Truef(t, allowed[field], "S2 CandidateProposer leaked field %q in %s", field, string(payload))
	}
	require.Len(t, fields, len(allowed), "S2 proposal payload must stay to identity plus title/tags/date/score/source/reason only")
}

func TestSubsystemProposeHonorsCanceledContextBeforeQueryingIndex(t *testing.T) {
	idx := &s2SubsystemRecordingIndex{hits: []gormdb.MetaIndexHit{{
		ID:        99,
		Title:     "should not be queried after cancellation",
		CreatedAt: time.Unix(1700002000, 0).UTC(),
		Score:     1,
		Source:    "s2.meta_index",
	}}}
	sub := NewSubsystem(idx)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	proposals, err := sub.Propose(ctx, cognitive.AttentionEvent{
		Type:    "user_prompt_submit",
		Project: "engram",
		Payload: map[string]interface{}{"text": "cancelled request must not hit the index"},
	}, 5)

	require.True(t, errors.Is(err, context.Canceled), "canceled S3 candidate requests must surface context.Canceled, got %v", err)
	require.Empty(t, proposals)
	require.Empty(t, idx.queries, "a context canceled before Propose must not spend the S3 latency budget on a meta-index query")
}

func TestSubsystemProposeEmptyTopicReturnsEmptyListWithoutQuery(t *testing.T) {
	idx := &s2SubsystemRecordingIndex{hits: []gormdb.MetaIndexHit{{
		ID:        7,
		Title:     "irrelevant without a topic",
		CreatedAt: time.Unix(1700003000, 0).UTC(),
		Score:     1,
		Source:    "s2.meta_index",
	}}}
	sub := NewSubsystem(idx)

	proposals, err := sub.Propose(context.Background(), cognitive.AttentionEvent{
		Type:    "user_prompt_submit",
		Project: "engram",
		Payload: map[string]interface{}{"topic": "   "},
	}, 5)

	require.NoError(t, err)
	require.Empty(t, proposals, "empty topics must produce no memory candidates rather than arbitrary index hits")
	require.Empty(t, idx.queries, "empty topics must not query the meta index")
}

func TestSubsystemProposeEnforcesLimitEvenIfIndexOverReturns(t *testing.T) {
	hits := make([]gormdb.MetaIndexHit, 8)
	for i := range hits {
		hits[i] = gormdb.MetaIndexHit{
			ID:        int64(100 + i),
			Title:     "bounded candidate",
			CreatedAt: time.Unix(1700004000+int64(i), 0).UTC(),
			Score:     1 - float32(i)*0.01,
			Source:    "s2.meta_index",
		}
	}
	idx := &s2SubsystemRecordingIndex{hits: hits}
	sub := NewSubsystem(idx)

	proposals, err := sub.Propose(context.Background(), cognitive.AttentionEvent{
		Type:    "user_prompt_submit",
		Project: "engram",
		Payload: map[string]interface{}{"text": "bounded handoff candidates"},
	}, 3)

	require.NoError(t, err)
	require.Len(t, idx.queries, 1)
	require.Equal(t, 3, idx.queries[0].Limit, "S2 must push the caller limit into the meta-index query")
	require.Len(t, proposals, 3, "S2 must still enforce the caller limit if a buggy index over-returns")
}
