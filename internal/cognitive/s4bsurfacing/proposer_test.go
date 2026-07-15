package s4bsurfacing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	cognitivecore "github.com/thebtf/engram/internal/cognitive/core"
	"github.com/thebtf/engram/internal/cognitive/s4directives"
	"github.com/thebtf/engram/pkg/cognitive"
)

type sourceCall struct {
	project string
	limit   int
}

type fakeSource struct {
	events []s4directives.StoredAttentionEvent
	err    error
	calls  []sourceCall
	fn     func(context.Context) error
}

func (f *fakeSource) ListByProject(ctx context.Context, project string, limit int) ([]s4directives.StoredAttentionEvent, error) {
	f.calls = append(f.calls, sourceCall{project: project, limit: limit})
	if f.fn != nil {
		if err := f.fn(ctx); err != nil {
			return nil, err
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return append([]s4directives.StoredAttentionEvent(nil), f.events...), nil
}

func directive(id int64, project, session, intent, horizon, privacy string, createdAt time.Time) s4directives.StoredAttentionEvent {
	return s4directives.StoredAttentionEvent{
		ID:             id,
		Project:        project,
		SessionID:      session,
		DerivedIntent:  intent,
		AgentConfirmed: true,
		Horizon:        horizon,
		PrivacyClass:   privacy,
		CreatedAt:      createdAt,
	}
}

func attention(project, session string, payload map[string]interface{}) cognitive.AttentionEvent {
	return cognitive.AttentionEvent{Type: "user_prompt_shift", Project: project, SessionID: session, Payload: payload}
}

func TestProposerStrictPayloadTypeReturnsExplicitEmptyWithoutSourceRead(t *testing.T) {
	row := directive(1, "engram", "session-1", "release checklist", "project", "internal", time.Unix(1, 0).UTC())
	for _, tc := range []struct {
		name    string
		payload map[string]interface{}
	}{
		{name: "invalid text only", payload: map[string]interface{}{"text": []string{"release checklist"}}},
		{name: "invalid text before valid query", payload: map[string]interface{}{"text": 123, "query": "release checklist"}},
		{name: "invalid topic after valid text", payload: map[string]interface{}{"text": "release checklist", "topic": map[string]string{"value": "release"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &fakeSource{events: []s4directives.StoredAttentionEvent{row}}
			proposals, err := NewProposer(source).Propose(context.Background(), attention("engram", "session-1", tc.payload), 5)
			require.NoError(t, err)
			require.NotNil(t, proposals)
			require.Empty(t, proposals)
			require.Empty(t, source.calls)
		})
	}

	source := &fakeSource{events: []s4directives.StoredAttentionEvent{row}}
	proposals, err := NewProposer(source).Propose(context.Background(), attention("engram", "   ", map[string]interface{}{
		"text": "release checklist",
	}), 5)
	require.NoError(t, err)
	require.NotNil(t, proposals)
	require.Empty(t, proposals)
	require.Empty(t, source.calls)
}

func TestProposerNormalizesRequestAndAppliesScopePrivacyAndMalformedRowGates(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	rows := []s4directives.StoredAttentionEvent{
		directive(1, "Project-X", " session-a ", "release same session", " SESSION ", " SECRET ", now.Add(time.Minute)),
		directive(2, "Project-X", "session-b", "release wrong session horizon", "session", "internal", now.Add(2*time.Minute)),
		directive(3, "Other", "session-a", "release wrong project", "project", "internal", now.Add(3*time.Minute)),
		directive(4, " Project-X ", "session-b", "release project scope", " PROJECT ", " INTERNAL ", now.Add(4*time.Minute)),
		directive(5, "Project-X", "session-c", "release permanent scope", "permanent", "public", now.Add(5*time.Minute)),
		directive(6, "Project-X", "session-b", "release cross session secret", "project", "secret", now.Add(6*time.Minute)),
		directive(7, "Project-X", "session-a", "release unconfirmed", "project", "internal", now.Add(7*time.Minute)),
		directive(8, "Project-X", "session-a", "release invalid horizon", "forever", "internal", now.Add(8*time.Minute)),
		directive(9, "Project-X", "session-a", "release invalid privacy", "project", "classified", now.Add(9*time.Minute)),
		directive(10, "Project-X", "   ", "release blank session", "project", "internal", now.Add(10*time.Minute)),
		directive(11, "project-x", "session-a", "release case mismatch", "project", "internal", now.Add(11*time.Minute)),
		directive(0, "Project-X", "session-a", "release invalid id", "project", "internal", now.Add(12*time.Minute)),
		directive(12, "Project-X", "session-a", "   ", "project", "internal", now.Add(13*time.Minute)),
		directive(13, "Project-X", "session-a", "release zero time", "project", "internal", time.Time{}),
		directive(14, "Project-X", "session-a", "unrelated tokens", "project", "internal", now.Add(14*time.Minute)),
	}
	rows[6].AgentConfirmed = false
	source := &fakeSource{events: rows}

	proposals, err := NewProposer(source).Propose(context.Background(), attention("  Project-X  ", " session-a ", map[string]interface{}{
		"text": "release",
	}), 10)
	require.NoError(t, err)
	require.Equal(t, []string{"s4b:directive:5", "s4b:directive:4", "s4b:directive:1"}, proposalIDs(proposals))
	require.Equal(t, []sourceCall{{project: "Project-X", limit: 50}}, source.calls)
}

func TestProposerRanksByScoreCreatedAtAndIDThenTruncates(t *testing.T) {
	now := time.Unix(1700001000, 0).UTC()
	source := &fakeSource{events: []s4directives.StoredAttentionEvent{
		directive(30, "engram", "s", "release alpha beta plan", "project", "internal", now),
		directive(20, "engram", "s", "release alpha", "project", "internal", now.Add(time.Hour)),
		directive(10, "engram", "s", "release alpha", "project", "internal", now.Add(time.Hour)),
	}}

	proposals, err := NewProposer(source).Propose(context.Background(), attention("engram", "s", map[string]interface{}{
		"query": "release alpha beta",
	}), 2)
	require.NoError(t, err)
	require.Equal(t, []string{"s4b:directive:30", "s4b:directive:10"}, proposalIDs(proposals))
	require.Greater(t, proposals[0].Score, proposals[1].Score)
}

func TestProposerNormalizesNonPositiveAndOversizedLimits(t *testing.T) {
	now := time.Unix(1700002000, 0).UTC()
	rows := make([]s4directives.StoredAttentionEvent, 30)
	for i := range rows {
		rows[i] = directive(int64(i+1), "engram", "s", fmt.Sprintf("release item %d", i+1), "project", "internal", now.Add(time.Duration(i)*time.Second))
	}
	source := &fakeSource{events: rows}
	proposer := NewProposer(source)

	defaults, err := proposer.Propose(context.Background(), attention("engram", "s", map[string]interface{}{"text": "release"}), 0)
	require.NoError(t, err)
	require.Len(t, defaults, 10)
	capped, err := proposer.Propose(context.Background(), attention("engram", "s", map[string]interface{}{"text": "release"}), 100)
	require.NoError(t, err)
	require.Len(t, capped, 25)
	require.Equal(t, []sourceCall{{project: "engram", limit: 50}, {project: "engram", limit: 50}}, source.calls)
}

func TestProposerBoundsUnicodeTitleAndNeverCopiesRawPrompt(t *testing.T) {
	const rawPrompt = "RAW-PROMPT-MARKER release"
	intent := "release " + strings.Repeat("界", 100)
	source := &fakeSource{events: []s4directives.StoredAttentionEvent{
		directive(42, "engram", "s", intent, "project", "internal", time.Unix(1700003000, 0).UTC()),
	}}

	proposals, err := NewProposer(source).Propose(context.Background(), attention("engram", "s", map[string]interface{}{"text": rawPrompt}), 1)
	require.NoError(t, err)
	require.Len(t, proposals, 1)
	require.Equal(t, "s4b:directive:42", proposals[0].ID)
	require.Equal(t, 80, utf8.RuneCountInString(proposals[0].Title))
	require.Equal(t, "Related confirmed creator directive (event_type=user_prompt_shift, overlap_tokens=1)", proposals[0].Reason)
	require.Equal(t, proposalSource, proposals[0].Source)
	require.NotContains(t, fmt.Sprint(proposals[0]), "RAW-PROMPT-MARKER")
}

func TestProposerRequiresNormalizedSafeAttentionEventTypeAndReasonMetadata(t *testing.T) {
	source := &fakeSource{events: []s4directives.StoredAttentionEvent{
		directive(51, "engram", "s", "release checklist", "project", "internal", time.Unix(1700003500, 0).UTC()),
	}}
	proposer := NewProposer(source)

	blankType := attention("engram", "s", map[string]interface{}{"text": "release"})
	blankType.Type = "   "
	proposals, err := proposer.Propose(context.Background(), blankType, 1)
	require.NoError(t, err)
	require.NotNil(t, proposals)
	require.Empty(t, proposals)

	unsafeType := attention("engram", "s", map[string]interface{}{"text": "release"})
	unsafeType.Type = "user prompt;shift"
	proposals, err = proposer.Propose(context.Background(), unsafeType, 1)
	require.NoError(t, err)
	require.NotNil(t, proposals)
	require.Empty(t, proposals)
	require.Empty(t, source.calls)

	valid := attention("engram", "s", map[string]interface{}{"text": "RAW-PROMPT-WORD release"})
	valid.Type = " User_Prompt_Shift "
	proposals, err = proposer.Propose(context.Background(), valid, 1)
	require.NoError(t, err)
	require.Len(t, proposals, 1)
	require.Equal(t, "Related confirmed creator directive (event_type=user_prompt_shift, overlap_tokens=1)", proposals[0].Reason)
	require.NotContains(t, proposals[0].Reason, "release")
	require.NotContains(t, proposals[0].Reason, "RAW-PROMPT-WORD")
	require.Equal(t, []sourceCall{{project: "engram", limit: 50}}, source.calls)
}

func TestProposerWrapsSourceFailureAndPropagatesContextErrorsUnchanged(t *testing.T) {
	boom := errors.New("database unavailable")
	source := &fakeSource{err: boom}
	_, err := NewProposer(source).Propose(context.Background(), attention("engram", "s", map[string]interface{}{"text": "release"}), 1)
	require.ErrorIs(t, err, ErrSourceFailure)
	require.ErrorIs(t, err, boom)
	var sourceErr *SourceError
	require.ErrorAs(t, err, &sourceErr)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	beforeRead := &fakeSource{}
	_, err = NewProposer(beforeRead).Propose(canceled, attention("engram", "s", map[string]interface{}{"text": "release"}), 1)
	require.Equal(t, context.Canceled, err)
	require.Empty(t, beforeRead.calls)

	deadlineSource := &fakeSource{err: context.DeadlineExceeded}
	_, err = NewProposer(deadlineSource).Propose(context.Background(), attention("engram", "s", map[string]interface{}{"text": "release"}), 1)
	require.Equal(t, context.DeadlineExceeded, err)
	wrappedDeadline := &fakeSource{err: fmt.Errorf("source: %w", context.DeadlineExceeded)}
	_, err = NewProposer(wrappedDeadline).Propose(context.Background(), attention("engram", "s", map[string]interface{}{"text": "release"}), 1)
	require.Equal(t, context.DeadlineExceeded, err)

	duringRead, cancelDuringRead := context.WithCancel(context.Background())
	cancelSource := &fakeSource{fn: func(context.Context) error {
		cancelDuringRead()
		return boom
	}}
	_, err = NewProposer(cancelSource).Propose(duringRead, attention("engram", "s", map[string]interface{}{"text": "release"}), 1)
	require.Equal(t, context.Canceled, err)
}

func TestProposerGenericMeterCountsFullScanSkipsAndFinalProposed(t *testing.T) {
	now := time.Unix(1700004000, 0).UTC()
	source := &fakeSource{events: []s4directives.StoredAttentionEvent{
		directive(1, "engram", "s", "release checklist", "project", "internal", now.Add(time.Second)),
		directive(2, "engram", "s", "release steps", "project", "internal", now),
		directive(3, "engram", "s", "unrelated words", "project", "internal", now),
		directive(0, "engram", "s", "release malformed", "project", "internal", now),
		directive(4, "other", "s", "release wrong project", "project", "internal", now),
	}}
	meter := cognitivecore.NewLocalMeter()
	subsystem := NewSubsystem(source)
	require.NoError(t, subsystem.Start(context.Background(), cognitivecore.Dependencies{Meter: meter}))

	proposals, err := subsystem.Propose(context.Background(), attention("engram", "s", map[string]interface{}{"text": "release"}), 1)
	require.NoError(t, err)
	require.Len(t, proposals, 1)

	snapshot := meter.Snapshot()
	require.Equal(t, uint64(1), snapshot.Counters["calls_total{operation=propose,subsystem="+subsystemName+"}"])
	require.Equal(t, uint64(5), snapshot.Counters["event_emitted{stage=scanned,subsystem="+subsystemName+"}"])
	require.Equal(t, uint64(1), snapshot.Counters["event_emitted{stage=proposed,subsystem="+subsystemName+"}"])
	require.Equal(t, uint64(3), snapshot.Counters["event_dropped{stage=skipped,subsystem="+subsystemName+"}"])
	require.Equal(t, uint64(1), snapshot.Counters["event_dropped{stage=limit,subsystem="+subsystemName+"}"])
	for key := range snapshot.Counters {
		name := strings.SplitN(key, "{", 2)[0]
		require.Contains(t, []string{"calls_total", "errors_total", "event_emitted", "event_dropped"}, name)
	}
}

func TestProposerGenericMeterCountsEmptyCanceledDeadlineAndSourceErrors(t *testing.T) {
	source := &fakeSource{}
	meter := cognitivecore.NewLocalMeter()
	subsystem := NewSubsystem(source)
	require.NoError(t, subsystem.Start(context.Background(), cognitivecore.Dependencies{Meter: meter}))

	empty := attention("engram", "s", map[string]interface{}{"text": "release"})
	empty.Type = ""
	proposals, err := subsystem.Propose(context.Background(), empty, 1)
	require.NoError(t, err)
	require.NotNil(t, proposals)
	require.Empty(t, proposals)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = subsystem.Propose(canceled, attention("engram", "s", map[string]interface{}{"text": "release"}), 1)
	require.Equal(t, context.Canceled, err)

	deadline, cancelDeadline := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancelDeadline()
	_, err = subsystem.Propose(deadline, attention("engram", "s", map[string]interface{}{"text": "release"}), 1)
	require.Equal(t, context.DeadlineExceeded, err)

	source.err = errors.New("database unavailable")
	_, err = subsystem.Propose(context.Background(), attention("engram", "s", map[string]interface{}{"text": "release"}), 1)
	require.ErrorIs(t, err, ErrSourceFailure)

	snapshot := meter.Snapshot()
	require.Equal(t, uint64(4), snapshot.Counters["calls_total{operation=propose,subsystem="+subsystemName+"}"])
	require.Equal(t, uint64(1), snapshot.Counters["event_dropped{stage=empty,subsystem="+subsystemName+"}"])
	require.Equal(t, uint64(1), snapshot.Counters["errors_total{kind=canceled,subsystem="+subsystemName+"}"])
	require.Equal(t, uint64(1), snapshot.Counters["errors_total{kind=deadline,subsystem="+subsystemName+"}"])
	require.Equal(t, uint64(1), snapshot.Counters["errors_total{kind=source,subsystem="+subsystemName+"}"])
	require.Equal(t, []sourceCall{{project: "engram", limit: 50}}, source.calls)
}

func proposalIDs(proposals []cognitive.HintProposal) []string {
	ids := make([]string, 0, len(proposals))
	for _, proposal := range proposals {
		ids = append(ids, proposal.ID)
	}
	return ids
}
