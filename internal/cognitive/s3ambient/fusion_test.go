package s3ambient

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/cognitive"
)

type recordingCandidateProposer struct {
	proposals []cognitive.HintProposal
	err       error
	delay     time.Duration
	calls     int
	limits    []int
}

func (p *recordingCandidateProposer) Propose(ctx context.Context, _ cognitive.AttentionEvent, limit int) ([]cognitive.HintProposal, error) {
	p.calls++
	p.limits = append(p.limits, limit)
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if p.err != nil {
		return nil, p.err
	}
	out := append([]cognitive.HintProposal(nil), p.proposals...)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type blockingCandidateProposer struct {
	started     chan struct{}
	release     <-chan struct{}
	finished    chan struct{}
	startedOnce sync.Once
}

func (p *blockingCandidateProposer) Propose(_ context.Context, _ cognitive.AttentionEvent, _ int) ([]cognitive.HintProposal, error) {
	p.startedOnce.Do(func() { close(p.started) })
	<-p.release
	close(p.finished)
	return nil, nil
}

func TestS3AmbientFusion_ContextCancellationDoesNotWaitForBlockedProposer(t *testing.T) {
	releaseBlocked := make(chan struct{})
	blocked := &blockingCandidateProposer{
		started:  make(chan struct{}),
		release:  releaseBlocked,
		finished: make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseBlocked) }) }
	defer release()

	cooperative := &recordingCandidateProposer{proposals: []cognitive.HintProposal{
		{ID: "cooperative", Title: "usable hint before caller deadline", Score: 0.9, Source: "s2.meta_index"},
	}}
	fusion := NewFusion(true, []cognitive.CandidateProposer{blocked, cooperative})

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	type proposeResult struct {
		proposals []cognitive.HintProposal
		err       error
		elapsed   time.Duration
	}
	done := make(chan proposeResult, 1)
	startedAt := time.Now()
	go func() {
		proposals, err := fusion.Propose(ctx, cognitive.AttentionEvent{Project: "project-s3-blocked-proposer"}, 3)
		done <- proposeResult{proposals: proposals, err: err, elapsed: time.Since(startedAt)}
	}()

	select {
	case <-blocked.started:
	case <-time.After(100 * time.Millisecond):
		release()
		t.Fatal("blocked proposer was not invoked; regression setup did not exercise fan-out cancellation")
	}

	select {
	case result := <-done:
		release()
		t.Fatalf("Fusion.Propose returned before caller context cancellation: proposals=%v err=%v elapsed=%s", proposalIDs(result.proposals), result.err, result.elapsed)
	case <-ctx.Done():
	}

	select {
	case result := <-done:
		release()
		require.ErrorIs(t, result.err, context.DeadlineExceeded, "caller cancellation must surface as the request error instead of waiting for blocked proposer output")
		require.Empty(t, result.proposals)
		require.Less(t, result.elapsed, 250*time.Millisecond, "Fusion.Propose must return promptly after ctx.Done rather than waiting for every proposer")
	case <-time.After(250 * time.Millisecond):
		release()
		select {
		case result := <-done:
			t.Fatalf("Fusion.Propose waited for a non-cooperative proposer after ctx.Done before returning proposals=%v err=%v elapsed=%s", proposalIDs(result.proposals), result.err, result.elapsed)
		case <-time.After(500 * time.Millisecond):
			t.Fatal("Fusion.Propose did not return even after the blocked proposer was released")
		}
	}

	release()
	select {
	case <-blocked.finished:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("blocked proposer did not exit after test cleanup release")
	}
}

func TestS3AmbientFusion_RRFTopThreeAcrossEnabledProposers(t *testing.T) {
	p1 := &recordingCandidateProposer{proposals: []cognitive.HintProposal{
		{ID: "1", Title: "recent release handoff", Score: 0.9, Source: "s2.meta_index"},
		{ID: "2", Title: "retry failed command", Score: 0.8, Source: "s2.meta_index"},
		{ID: "3", Title: "stale branch warning", Score: 0.7, Source: "s2.meta_index"},
	}}
	p2 := &recordingCandidateProposer{proposals: []cognitive.HintProposal{
		{ID: "2", Title: "retry failed command", Score: 0.95, Source: "s6.outcome_policy"},
		{ID: "4", Title: "capture missing evidence", Score: 0.85, Source: "s6.outcome_policy"},
		{ID: "1", Title: "recent release handoff", Score: 0.75, Source: "s6.outcome_policy"},
	}}

	fusion := NewFusion(true, []cognitive.CandidateProposer{p1, p2})
	proposals, err := fusion.Propose(context.Background(), cognitive.AttentionEvent{
		Type:      "user_prompt_submit",
		SessionID: "session-s3-rrf",
		Project:   "project-s3-rrf",
		Payload:   map[string]interface{}{"text": "show the strongest ambient suggestions"},
		Timestamp: time.Unix(1700020000, 0).UTC(),
	}, 3)

	require.NoError(t, err)
	require.Equal(t, []int{3}, p1.limits, "fusion must push the caller bound into each proposer query")
	require.Equal(t, []int{3}, p2.limits, "fusion must push the caller bound into each proposer query")
	require.Equal(t, []string{"2", "1", "4"}, proposalIDs(proposals), "S3 must reciprocal-rank-fuse enabled proposer results and keep only the top three hints")
}

func TestS3AmbientFusion_DeadlineExpiredBeforeFanoutReturnsContextError(t *testing.T) {
	proposer := &recordingCandidateProposer{proposals: []cognitive.HintProposal{{ID: "late", Title: "late hint", Source: "s2.meta_index"}}}
	fusion := NewFusion(true, []cognitive.CandidateProposer{proposer})

	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1700020100, 0).UTC())
	cancel()
	proposals, err := fusion.Propose(ctx, cognitive.AttentionEvent{Project: "project-s3-deadline"}, 3)

	require.True(t, errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded), "expired S3 candidate requests must surface a context error, got %v", err)
	require.Empty(t, proposals)
	require.Zero(t, proposer.calls, "fusion must not spend budget on proposer fanout when the caller deadline is already expired")
}

func TestS3AmbientFusion_ContinuesPastNonFatalProposerErrors(t *testing.T) {
	good := &recordingCandidateProposer{proposals: []cognitive.HintProposal{
		{ID: "9", Title: "keep release notes in sync", Score: 0.88, Source: "s2.meta_index"},
	}}
	bad := &recordingCandidateProposer{err: errors.New("backend unavailable")}

	fusion := NewFusion(true, []cognitive.CandidateProposer{bad, good})
	proposals, err := fusion.Propose(context.Background(), cognitive.AttentionEvent{
		Type:    "tool_result_surprise",
		Project: "project-s3-isolation",
		Payload: map[string]interface{}{"text": "continue despite one proposer failing"},
	}, 3)

	require.NoError(t, err, "one failing proposer must not blank the entire ambient hint pass when another proposer succeeds")
	require.Equal(t, []string{"9"}, proposalIDs(proposals))
	require.Equal(t, 1, bad.calls)
	require.Equal(t, 1, good.calls)
}

func TestS3AmbientFusion_DisabledOrNoopReturnsEmptyHints(t *testing.T) {
	t.Run("disabled returns empty without calling proposers", func(t *testing.T) {
		proposer := &recordingCandidateProposer{proposals: []cognitive.HintProposal{{ID: "1", Title: "should stay hidden", Source: "s2.meta_index"}}}
		fusion := NewFusion(false, []cognitive.CandidateProposer{proposer})

		proposals, err := fusion.Propose(context.Background(), cognitive.AttentionEvent{Project: "project-s3-disabled"}, 3)
		require.NoError(t, err)
		require.NotNil(t, proposals, "disabled ambient mode should still return an explicit empty slice, not a nil success that hides a stubbed implementation")
		require.Empty(t, proposals)
		require.Zero(t, proposer.calls, "disabled ambient mode must not invoke proposers")
	})

	t.Run("no enabled proposers returns empty", func(t *testing.T) {
		fusion := NewFusion(true, nil)

		proposals, err := fusion.Propose(context.Background(), cognitive.AttentionEvent{Project: "project-s3-noop"}, 3)
		require.NoError(t, err)
		require.NotNil(t, proposals)
		require.Empty(t, proposals, "an empty proposer set is a no-op, not a panic or nil placeholder")
	})
}

func proposalIDs(proposals []cognitive.HintProposal) []string {
	ids := make([]string, len(proposals))
	for i, proposal := range proposals {
		ids[i] = proposal.ID
	}
	return ids
}
