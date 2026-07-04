package s4directives

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/pkg/cognitive"
)

type recordingDirectiveStore struct {
	records []cognitive.AttentionEventRecord
	err     error
}

func (s *recordingDirectiveStore) Create(_ context.Context, event cognitive.AttentionEventRecord) (*StoredAttentionEvent, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.records = append(s.records, event)
	return &StoredAttentionEvent{
		ID:             int64(len(s.records)),
		Project:        event.Project,
		SessionID:      event.SessionID,
		SourceTurnHash: event.SourceTurnHash,
		DerivedIntent:  event.DerivedIntent,
		AgentConfirmed: event.AgentConfirmed,
		Horizon:        event.Horizon,
		PrivacyClass:   event.PrivacyClass,
		CreatedAt:      time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC),
	}, nil
}

func TestRememberDirectiveStoresDistilledRecordWithoutRawTextOrSourceTurn(t *testing.T) {
	store := &recordingDirectiveStore{}
	svc := NewService(store)
	var distillInput cognitive.RawSignal
	svc.distill = func(_ context.Context, raw cognitive.RawSignal) (cognitive.Distilled, error) {
		distillInput = raw
		return cognitive.Distilled{
			Intent:     "Prefer short release notes with risk bullets",
			Horizon:    "permanent",
			Privacy:    "secret",
			Confidence: 0.99,
		}, nil
	}

	stored, err := svc.RememberDirective(context.Background(), " engram ", " session-1 ", RememberDirectiveRequest{
		Text:         "  RAW_DIRECTIVE_NEVER_STORE: keep deployment notes brief  ",
		SourceTurn:   " RAW_SOURCE_TURN_NEVER_STORE: creator said this in chat ",
		Horizon:      "permanent",
		PrivacyClass: "secret",
	})

	require.NoError(t, err)
	require.Len(t, store.records, 1)
	record := store.records[0]
	require.Equal(t, "engram", record.Project)
	require.Equal(t, "session-1", record.SessionID)
	require.Equal(t, "Prefer short release notes with risk bullets", record.DerivedIntent)
	require.Equal(t, "permanent", record.Horizon)
	require.Equal(t, "secret", record.PrivacyClass)
	require.True(t, record.AgentConfirmed)
	require.True(t, strings.HasPrefix(record.SourceTurnHash, "sha256:"))
	require.NotContains(t, record.SourceTurnHash, "RAW_SOURCE_TURN_NEVER_STORE")
	require.NotContains(t, record.SourceTurnHash, "RAW_DIRECTIVE_NEVER_STORE")
	require.Equal(t, record.SourceTurnHash, distillInput.SourceHash)
	require.Equal(t, "permanent", distillInput.Context["horizon"])
	require.Equal(t, "secret", distillInput.Context["privacy_class"])

	encoded, err := json.Marshal(stored)
	require.NoError(t, err)
	response := string(encoded)
	require.NotContains(t, response, "RAW_DIRECTIVE_NEVER_STORE")
	require.NotContains(t, response, "RAW_SOURCE_TURN_NEVER_STORE")
}

func TestRememberDirectiveDefaultDistillerDoesNotPersistVerbatimPrompt(t *testing.T) {
	store := &recordingDirectiveStore{}
	svc := NewService(store)
	rawText := "Please remember that deployment notes should list rollback risk before metrics"

	stored, err := svc.RememberDirective(context.Background(), "engram", "session-1", RememberDirectiveRequest{
		Text:         rawText,
		Horizon:      "project",
		PrivacyClass: "internal",
	})

	require.NoError(t, err)
	require.Len(t, store.records, 1)
	require.NotEqual(t, rawText, store.records[0].DerivedIntent, "default distillation must not persist the raw prompt verbatim")
	require.NotContains(t, store.records[0].SourceTurnHash, rawText)
	require.NotEqual(t, rawText, stored.DerivedIntent, "response must expose distilled intent, not raw prompt text")
}

func TestRememberDirectiveRejectsInvalidInputBeforeDistillOrWrite(t *testing.T) {
	tests := []struct {
		name      string
		project   string
		sessionID string
		req       RememberDirectiveRequest
		wantErr   error
	}{
		{
			name:      "project is required",
			project:   "  ",
			sessionID: "session-1",
			req:       RememberDirectiveRequest{Text: "remember this", Horizon: "project", PrivacyClass: "internal"},
			wantErr:   ErrProjectRequired,
		},
		{
			name:      "session is required",
			project:   "engram",
			sessionID: "  ",
			req:       RememberDirectiveRequest{Text: "remember this", Horizon: "project", PrivacyClass: "internal"},
			wantErr:   ErrSessionRequired,
		},
		{
			name:      "text is required",
			project:   "engram",
			sessionID: "session-1",
			req:       RememberDirectiveRequest{Text: " \t ", Horizon: "project", PrivacyClass: "internal"},
			wantErr:   ErrTextRequired,
		},
		{
			name:      "text is bounded",
			project:   "engram",
			sessionID: "session-1",
			req:       RememberDirectiveRequest{Text: strings.Repeat("a", MaxDirectiveTextBytes+1), Horizon: "project", PrivacyClass: "internal"},
			wantErr:   ErrTextTooLarge,
		},
		{
			name:      "source turn is bounded",
			project:   "engram",
			sessionID: "session-1",
			req:       RememberDirectiveRequest{Text: "remember this", SourceTurn: strings.Repeat("s", MaxSourceTurnBytes+1), Horizon: "project", PrivacyClass: "internal"},
			wantErr:   ErrSourceTurnTooLarge,
		},
		{
			name:      "horizon enum is enforced",
			project:   "engram",
			sessionID: "session-1",
			req:       RememberDirectiveRequest{Text: "remember this", Horizon: "foreverish", PrivacyClass: "internal"},
			wantErr:   ErrInvalidHorizon,
		},
		{
			name:      "privacy enum is enforced",
			project:   "engram",
			sessionID: "session-1",
			req:       RememberDirectiveRequest{Text: "RAW_PRIVATE_DIRECTIVE", Horizon: "project", PrivacyClass: "private-ish"},
			wantErr:   ErrInvalidPrivacyClass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &recordingDirectiveStore{}
			distillCalls := 0
			svc := NewService(store)
			svc.distill = func(context.Context, cognitive.RawSignal) (cognitive.Distilled, error) {
				distillCalls++
				return cognitive.Distilled{Intent: "should not be reached", Horizon: "project", Privacy: "internal", Confidence: 1}, nil
			}

			_, err := svc.RememberDirective(context.Background(), tt.project, tt.sessionID, tt.req)

			require.ErrorIs(t, err, tt.wantErr)
			require.Zero(t, distillCalls, "invalid input must fail before distillation")
			require.Empty(t, store.records, "invalid input must fail before persistence")
			require.NotContains(t, err.Error(), "RAW_PRIVATE_DIRECTIVE")
		})
	}
}

func TestRememberDirectiveRejectsUnsafeDistillationBeforeWrite(t *testing.T) {
	tests := []struct {
		name      string
		distilled cognitive.Distilled
		wantErr   error
	}{
		{
			name:      "empty distilled intent",
			distilled: cognitive.Distilled{Intent: "  ", Horizon: "project", Privacy: "internal", Confidence: 1},
			wantErr:   ErrDistillEmptyIntent,
		},
		{
			name:      "low confidence distilled intent",
			distilled: cognitive.Distilled{Intent: "valid intent", Horizon: "project", Privacy: "internal", Confidence: MinDistillConfidence - 0.01},
			wantErr:   ErrDistillLowConfidence,
		},
		{
			name:      "invalid distilled privacy",
			distilled: cognitive.Distilled{Intent: "valid intent", Horizon: "project", Privacy: "confidential", Confidence: 1},
			wantErr:   ErrInvalidPrivacyClass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &recordingDirectiveStore{}
			svc := NewService(store)
			svc.distill = func(context.Context, cognitive.RawSignal) (cognitive.Distilled, error) {
				return tt.distilled, nil
			}

			_, err := svc.RememberDirective(context.Background(), "engram", "session-1", RememberDirectiveRequest{
				Text:         "RAW_DIRECTIVE_DO_NOT_ECHO",
				Horizon:      "project",
				PrivacyClass: "internal",
			})

			require.ErrorIs(t, err, tt.wantErr)
			require.Empty(t, store.records, "unsafe distillation must not persist a partial row")
			require.NotContains(t, err.Error(), "RAW_DIRECTIVE_DO_NOT_ECHO")
		})
	}
}

func TestRememberDirectiveRateLimitIsPerSessionAndRejectsBeforeWrite(t *testing.T) {
	store := &recordingDirectiveStore{}
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	distillCalls := 0
	svc := NewService(store)
	svc.now = func() time.Time { return now }
	svc.distill = func(context.Context, cognitive.RawSignal) (cognitive.Distilled, error) {
		distillCalls++
		return cognitive.Distilled{Intent: "bounded capture", Horizon: "project", Privacy: "internal", Confidence: 1}, nil
	}

	for i := 0; i < defaultRateLimitAccepted; i++ {
		_, err := svc.RememberDirective(context.Background(), "engram", "session-a", RememberDirectiveRequest{
			Text:         "remember directive",
			Horizon:      "project",
			PrivacyClass: "internal",
		})
		require.NoError(t, err, "accepted capture %d", i+1)
	}
	require.Len(t, store.records, defaultRateLimitAccepted)
	require.Equal(t, defaultRateLimitAccepted, distillCalls)

	_, err := svc.RememberDirective(context.Background(), "engram", "session-a", RememberDirectiveRequest{
		Text:         "RAW_ELEVENTH_DIRECTIVE",
		Horizon:      "project",
		PrivacyClass: "internal",
	})
	var rateErr *RateLimitError
	require.ErrorAs(t, err, &rateErr)
	require.Equal(t, "session-a", rateErr.SessionID)
	require.Len(t, store.records, defaultRateLimitAccepted, "rejected capture must not write")
	require.Equal(t, defaultRateLimitAccepted, distillCalls, "rejected capture must not distill")
	require.NotContains(t, err.Error(), "RAW_ELEVENTH_DIRECTIVE")

	_, err = svc.RememberDirective(context.Background(), "engram", "session-b", RememberDirectiveRequest{
		Text:         "remember directive",
		Horizon:      "project",
		PrivacyClass: "internal",
	})
	require.NoError(t, err, "a different session must have an independent quota")
	require.Len(t, store.records, defaultRateLimitAccepted+1)
	require.Equal(t, defaultRateLimitAccepted+1, distillCalls)

	now = now.Add(time.Minute + time.Nanosecond)
	_, err = svc.RememberDirective(context.Background(), "engram", "session-a", RememberDirectiveRequest{
		Text:         "remember after window",
		Horizon:      "project",
		PrivacyClass: "internal",
	})
	require.NoError(t, err, "the same session should accept captures after the limiter window")
	require.Len(t, store.records, defaultRateLimitAccepted+2)
}

func TestRememberDirectiveStoreFailureDoesNotConsumeLimiterSlot(t *testing.T) {
	store := &recordingDirectiveStore{err: errors.New("database unavailable")}
	svc := NewService(store)
	distillCalls := 0
	svc.distill = func(context.Context, cognitive.RawSignal) (cognitive.Distilled, error) {
		distillCalls++
		return cognitive.Distilled{Intent: "safe intent", Horizon: "project", Privacy: "internal", Confidence: 1}, nil
	}

	_, err := svc.RememberDirective(context.Background(), "engram", "session-a", RememberDirectiveRequest{
		Text:         "remember directive",
		Horizon:      "project",
		PrivacyClass: "internal",
	})
	require.ErrorContains(t, err, "database unavailable")

	store.err = nil
	for i := 0; i < defaultRateLimitAccepted; i++ {
		_, err = svc.RememberDirective(context.Background(), "engram", "session-a", RememberDirectiveRequest{
			Text:         "remember directive",
			Horizon:      "project",
			PrivacyClass: "internal",
		})
		require.NoError(t, err, "accepted capture %d after failed write", i+1)
	}
	require.Len(t, store.records, defaultRateLimitAccepted)
	require.Equal(t, defaultRateLimitAccepted+1, distillCalls)
}

func TestWriteAttentionEventRejectsMissingOrInvalidSourceTurnHashBeforeWrite(t *testing.T) {
	tests := []struct {
		name    string
		hash    string
		wantErr error
	}{
		{name: "missing hash", hash: "", wantErr: ErrSourceTurnHashRequired},
		{name: "raw text instead of hash", hash: "RAW_SOURCE_TURN_NEVER_STORE", wantErr: ErrInvalidSourceTurnHash},
		{name: "non-canonical hash shape", hash: "sha256:abc", wantErr: ErrInvalidSourceTurnHash},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &recordingDirectiveStore{}
			svc := NewService(store)

			err := svc.WriteAttentionEvent(context.Background(), cognitive.AttentionEventRecord{
				Project:        "engram",
				SessionID:      "session-1",
				SourceTurnHash: tt.hash,
				DerivedIntent:  "keep release notes short",
				AgentConfirmed: true,
				Horizon:        "project",
				PrivacyClass:   "internal",
			})

			require.ErrorIs(t, err, tt.wantErr)
			require.Empty(t, store.records, "invalid source hashes must fail before persistence")
		})
	}
}

func TestWriteAttentionEventRejectsUnconfirmedAgentBeforeWrite(t *testing.T) {
	store := &recordingDirectiveStore{}
	svc := NewService(store)

	err := svc.WriteAttentionEvent(context.Background(), cognitive.AttentionEventRecord{
		Project:        "engram",
		SessionID:      "session-1",
		SourceTurnHash: hashSourceMaterial("source turn", "keep release notes short"),
		DerivedIntent:  "keep release notes short",
		AgentConfirmed: false,
		Horizon:        "project",
		PrivacyClass:   "internal",
	})

	require.ErrorIs(t, err, ErrAgentConfirmationRequired)
	require.Empty(t, store.records, "unconfirmed writes must fail before persistence")
}

func TestRememberDirectiveFailsClosedWhenLimiterMissing(t *testing.T) {
	store := &recordingDirectiveStore{}
	svc := NewService(store)
	svc.limiter = nil

	_, err := svc.RememberDirective(context.Background(), "engram", "session-1", RememberDirectiveRequest{
		Text:         "remember this",
		Horizon:      "project",
		PrivacyClass: "internal",
	})

	require.ErrorIs(t, err, ErrRateLimiterNotConfigured)
	require.Empty(t, store.records)
}

func TestRememberDirectiveFailsClosedWhenDistillerMissing(t *testing.T) {
	store := &recordingDirectiveStore{}
	svc := NewService(store)
	svc.distill = nil

	_, err := svc.RememberDirective(context.Background(), "engram", "session-1", RememberDirectiveRequest{
		Text:         "remember this",
		Horizon:      "project",
		PrivacyClass: "internal",
	})

	require.ErrorIs(t, err, ErrDistillerNotConfigured)
	require.Empty(t, store.records)
}

func TestDistillFailsClosedWhenDistillerMissing(t *testing.T) {
	svc := &Service{}

	distilled, err := svc.Distill(context.Background(), cognitive.RawSignal{Text: "remember this"})

	require.ErrorIs(t, err, ErrDistillerNotConfigured)
	require.Equal(t, cognitive.Distilled{}, distilled)
}

func TestLimiterCommitKeepsAcceptedTimestampsSorted(t *testing.T) {
	limiter := newSessionLimiter(time.Minute, defaultRateLimitAccepted, 2*time.Minute)
	early := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	late := early.Add(10 * time.Second)

	earlyReservation, err := limiter.Reserve("session-1", early)
	require.NoError(t, err)
	lateReservation, err := limiter.Reserve("session-1", late)
	require.NoError(t, err)

	lateReservation.Commit()
	earlyReservation.Commit()

	entry := limiter.entries["session-1"]
	require.Len(t, entry.accepted, 2)
	require.Equal(t, early, entry.accepted[0])
	require.Equal(t, late, entry.accepted[1])
}
