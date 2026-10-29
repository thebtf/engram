package worker

// dream_cycle_test.go — unit tests for runDreamCrystallization (T011).
//
// Test seams used:
//   - s.dreamExtractorFunc: injects a fake crystallization.Extractor so no real
//     LLM endpoint is required. Mirrors the crystallizeFunc pattern in handlers_hooks.go.
//   - s.dreamTranscriptStoreOverride: injects a fakeTranscriptStore so no real
//     database is required.
//   - fakeCandidateStore / fakeMemoryFingerprintChecker: satisfy the
//     crystallization.CandidateWriter and crystallization.MemoryFingerprintChecker
//     interfaces respectively.
//
// DATABASE_DSN is optional: TestDreamCycle_RealDBRestartRetryPersistsExactlyOneCandidate
// uses it for the durable restart proof and skips when it is unset.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crystallization"
	gorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// ---------------------------------------------------------------------------
// Fake stores — satisfy CandidateWriter and MemoryFingerprintChecker.
// ---------------------------------------------------------------------------

type fakeCandidateStore struct {
	created       []*models.CrystallizationCandidate
	byFingerprint map[string]*models.CrystallizationCandidate
	createErr     error
	nextID        atomic.Int64
}

func (f *fakeCandidateStore) Create(_ context.Context, c *models.CrystallizationCandidate) (*models.CrystallizationCandidate, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	id := f.nextID.Add(1)
	c.ID = id
	f.created = append(f.created, c)
	if f.byFingerprint == nil {
		f.byFingerprint = make(map[string]*models.CrystallizationCandidate)
	}
	f.byFingerprint[c.Fingerprint] = c
	return c, nil
}

func (f *fakeCandidateStore) GetByFingerprint(_ context.Context, fingerprint string) (*models.CrystallizationCandidate, error) {
	return f.byFingerprint[fingerprint], nil
}

type fakeMemChecker struct{}

func (f *fakeMemChecker) ListBySourceAgentAndTag(_ context.Context, _, _, _ string) ([]*models.Memory, error) {
	return nil, nil // no existing memories
}

type markFailOnceTranscriptStore struct {
	dreamTranscriptStore
	failed bool
}

func (s *markFailOnceTranscriptStore) MarkProcessed(ctx context.Context, ids []int64) error {
	if !s.failed {
		s.failed = true
		return errors.New("injected mark failure")
	}
	return s.dreamTranscriptStore.MarkProcessed(ctx, ids)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildDreamService returns a minimal Service wired for dream-cycle unit tests.
// It injects:
//   - fake transcript store with the supplied rows
//   - fake candidate store (if candidateStore != nil)
//   - fake extractor function
//   - ENGRAM_VNEXT_F_ENABLED as directed by wantCandidatePath
func buildDreamService(
	t *testing.T,
	rows []gorm.SessionTranscript,
	cs *fakeCandidateStore,
	extractFn dreamExtractFunc,
	vnextFEnabled bool,
) *Service {
	t.Helper()
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")
	if vnextFEnabled {
		t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	} else {
		t.Setenv("ENGRAM_VNEXT_F_ENABLED", "false")
	}

	svc := &Service{}
	svc.ctx = context.Background()

	ts := &fakeTranscriptStore{rows: rows}
	svc.dreamTranscriptStoreOverride = ts
	svc.dreamExtractorFunc = extractFn

	if cs != nil {
		// Use the test-seam override field (typed as crystallization.CandidateWriter)
		// rather than assigning to the concrete *gorm.CandidateStore field.
		svc.dreamCandidateStoreOverride = cs
	}

	return svc
}

// makeTranscript returns a SessionTranscript with the given fields and a
// created_at set to base + offset for ordering stability.
func makeTranscript(id int64, sessionID, project, content string, base time.Time, offsetSec int) gorm.SessionTranscript {
	return gorm.SessionTranscript{
		ID:        id,
		SessionID: sessionID,
		Project:   project,
		Content:   content,
		ByteLen:   len(content),
		CreatedAt: base.Add(time.Duration(offsetSec) * time.Second),
	}
}

// ---------------------------------------------------------------------------
// TC1: No-op when ENGRAM_LLM_URL is unset and no extractor seam is injected.
//
// Verifies US4: runDreamCrystallization must return cleanly without error and
// must not create any candidates when the LLM is disabled.
// ---------------------------------------------------------------------------

func TestDreamCycle_NoOpWhenLLMDisabled(t *testing.T) {
	t.Setenv("ENGRAM_LLM_URL", "") // explicitly unset
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	base := time.Now()
	rows := []gorm.SessionTranscript{
		makeTranscript(1, "sess-a", "proj", "decided to use Redis because fast", base, 0),
	}
	cs := &fakeCandidateStore{}

	// Wire the real LLM path (no dreamExtractorFunc) so the disabled-check fires.
	svc := &Service{}
	svc.ctx = context.Background()
	svc.dreamTranscriptStoreOverride = &fakeTranscriptStore{rows: rows}
	// dreamExtractorFunc is nil → runDreamCrystallization calls llm.NewClient()
	// Use the test-seam override so we don't need to assign concrete *gorm.CandidateStore.
	svc.dreamCandidateStoreOverride = cs

	// Must not panic, must not create candidates.
	assert.NotPanics(t, func() {
		svc.runDreamCrystallization(context.Background())
	})
	assert.Len(t, cs.created, 0, "no candidates must be created when LLM is disabled")
	// Watermark must not be advanced (still 0 == epoch).
	assert.EqualValues(t, 0, svc.dreamWatermark.Load(), "watermark must not advance on LLM-disabled no-op")
}

type retentionTranscriptStore struct {
	fakeTranscriptStore
	now       time.Time
	listCalls int
}

func (s *retentionTranscriptStore) ListUnprocessedSince(ctx context.Context, watermark time.Time) ([]gorm.SessionTranscript, error) {
	s.listCalls++
	rows, err := s.fakeTranscriptStore.ListUnprocessedSince(ctx, watermark)
	return slices.DeleteFunc(rows, func(row gorm.SessionTranscript) bool { return row.ProcessedAt != nil }), err
}

func (s *retentionTranscriptStore) PruneProcessed(context.Context) (int64, error) {
	before := len(s.rows)
	s.rows = slices.DeleteFunc(s.rows, func(row gorm.SessionTranscript) bool { return row.ProcessedAt != nil || s.processed[row.ID] })
	return int64(before - len(s.rows)), nil
}

func (s *retentionTranscriptStore) PruneUnprocessedOlderThan(_ context.Context, days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	before := len(s.rows)
	cutoff := s.now.AddDate(0, 0, -days)
	s.rows = slices.DeleteFunc(s.rows, func(row gorm.SessionTranscript) bool {
		return row.ProcessedAt == nil && row.CreatedAt.Before(cutoff)
	})
	return int64(before - len(s.rows)), nil
}

func TestDreamCycle_RetentionRunsWithoutLLMBeforeSleepGates(t *testing.T) {
	for _, tc := range []struct {
		name string
		days int
		busy bool
	}{
		{name: "positive retention while active", days: 7, busy: true},
		{name: "positive retention below new memory threshold", days: 7},
		{name: "zero preserves unprocessed while active", busy: true},
		{name: "zero preserves unprocessed below threshold"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			processed := now.Add(-time.Hour)
			ts := &retentionTranscriptStore{now: now, fakeTranscriptStore: fakeTranscriptStore{rows: []gorm.SessionTranscript{
				{ID: 1, CreatedAt: now.AddDate(0, 0, -8), Content: "expired pending transcript"},
				{ID: 2, CreatedAt: now.Add(-time.Hour), Content: "nonexpired pending transcript"},
				{ID: 3, CreatedAt: now.Add(-time.Hour), Content: "already processed transcript", ProcessedAt: &processed},
			}}}
			db := knowledgeSQLiteDB(t)
			require.NoError(t, db.Create(&gorm.Memory{ID: 100, Project: uuid.NewString(), Content: "durable memory stays", Status: "active", CreatedAt: now, UpdatedAt: now}).Error)
			cfg := config.Default()
			cfg.TranscriptRetentionDays = tc.days
			svc := &Service{config: cfg, memoryStore: gorm.NewMemoryStore(&gorm.Store{DB: db}), dreamTranscriptStoreOverride: ts}
			if tc.busy {
				svc.lastRequestAt.Store(now.UnixNano())
			}
			svc.maybeSleepCycle(context.Background())
			ids := make([]int64, 0, len(ts.rows))
			for _, row := range ts.rows {
				ids = append(ids, row.ID)
			}
			if tc.days > 0 {
				require.Equal(t, []int64{2}, ids, "only expired unprocessed and already processed transcripts may be removed")
			} else {
				require.Equal(t, []int64{1, 2}, ids, "explicit zero must preserve all unprocessed transcripts")
			}
			require.Zero(t, ts.listCalls, "maintenance must not read pending content for inference")
			require.Empty(t, ts.marked)
			require.Zero(t, svc.dreamWatermark.Load())
			var memory gorm.Memory
			require.NoError(t, db.First(&memory, 100).Error)
			require.Equal(t, "durable memory stays", memory.Content)
		})
	}
}

func TestDreamCycle_RetentionFollowsInferenceOutcome(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("extraction_failure_%t", fail), func(t *testing.T) {
			now := time.Now().UTC()
			processed := now.Add(-time.Hour)
			ts := &retentionTranscriptStore{now: now, fakeTranscriptStore: fakeTranscriptStore{rows: []gorm.SessionTranscript{
				{ID: 1, Project: "project", SessionID: "session", CreatedAt: now.AddDate(0, 0, -8), Content: "expired retry transcript"},
				{ID: 2, Project: "project", SessionID: "session", CreatedAt: now.Add(-time.Hour), Content: "fresh pending transcript"},
				{ID: 3, Project: "project", SessionID: "session", CreatedAt: now.Add(-time.Hour), ProcessedAt: &processed},
			}}}
			cfg := config.Default()
			cfg.TranscriptRetentionDays = 7
			svc := &Service{config: cfg, dreamTranscriptStoreOverride: ts, dreamCandidateStoreOverride: &fakeCandidateStore{}}
			svc.dreamExtractorFunc = func(_ context.Context, digest string) ([]crystallization.ExtractedDecision, error) {
				require.Len(t, ts.rows, 3, "admitted inference must run before retention")
				require.Contains(t, digest, "expired retry transcript")
				if fail {
					return nil, errors.New("synthetic inference failure")
				}
				return nil, nil
			}
			pruneUnprocessed := svc.runDreamCrystallization(context.Background())
			svc.pruneDreamTranscripts(context.Background(), pruneUnprocessed)
			if fail {
				require.False(t, pruneUnprocessed)
				require.Len(t, ts.rows, 2, "failed inference retains both expired and fresh pending work for retry")
				require.Equal(t, int64(1), ts.rows[0].ID)
				require.Equal(t, int64(2), ts.rows[1].ID)
			} else {
				require.True(t, pruneUnprocessed)
				require.Empty(t, ts.rows, "successful inference marks work before processed-row pruning")
			}
		})
	}
}

func TestDreamCycle_FailedQueueSurvivesSkippedTickAndRestart(t *testing.T) {
	for _, days := range []int{0, 7} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("retention_%d_restart_%t", days, restart), func(t *testing.T) {
				now := time.Now().UTC()
				project := uuid.NewString()
				db := knowledgeSQLiteDB(t)
				for id := int64(1); id <= sleepCycleMemoryCountThreshold; id++ {
					require.NoError(t, db.Create(&gorm.Memory{ID: id, Project: project, Content: "existing durable memory", Status: "active", Tier: "episodic", EpistemicType: "fact", Stability: 30, Retrievability: 1, Confidence: 0.5, CreatedAt: now, UpdatedAt: now}).Error)
				}
				ts := &retentionTranscriptStore{now: now, fakeTranscriptStore: fakeTranscriptStore{rows: []gorm.SessionTranscript{
					{ID: 1, SessionID: "failed-queue", Project: project, Content: "failed transcript survives the next tick", CreatedAt: now.AddDate(0, 0, -8)},
				}}}
				cfg := config.Default()
				cfg.TranscriptRetentionDays = days
				calls := 0
				recovered := false
				extract := func(_ context.Context, digest string) ([]crystallization.ExtractedDecision, error) {
					calls++
					require.Contains(t, digest, "failed transcript survives the next tick")
					if !recovered {
						return nil, errors.New("synthetic temporary provider failure")
					}
					return nil, nil
				}
				newService := func() *Service {
					return &Service{config: cfg, memoryStore: gorm.NewMemoryStore(&gorm.Store{DB: db}), dreamTranscriptStoreOverride: ts, dreamCandidateStoreOverride: &fakeCandidateStore{}, dreamExtractorFunc: extract}
				}
				svc := newService()
				svc.maybeSleepCycle(context.Background())
				require.Equal(t, 1, calls)
				require.Len(t, ts.rows, 1, "failed inference must keep the durable pending queue")
				require.EqualValues(t, sleepCycleMemoryCountThreshold, svc.sleepCycleWatermarkID.Load())
				if restart {
					svc = newService() // Queue/store survives; no volatile failure flag or watermark is carried over.
					svc.lastRequestAt.Store(time.Now().UnixNano())
				}
				svc.maybeSleepCycle(context.Background())
				require.Len(t, ts.rows, 1, "the following count/idle-gated tick must not expire failed pending work")
				require.Empty(t, ts.marked)
				if restart {
					require.Equal(t, 1, calls, "the existing idle gate must still withhold inference")
				} else {
					require.Equal(t, 2, calls, "queued transcripts retry without requiring new durable memories")
				}
				recovered = true
				svc.lastRequestAt.Store(0)
				svc.maybeSleepCycle(context.Background())
				require.Empty(t, ts.rows, "only successful analysis may consume the pending queue")
				require.Equal(t, []int64{1}, ts.marked)
				var memories int64
				require.NoError(t, db.Model(&gorm.Memory{}).Count(&memories).Error)
				require.EqualValues(t, sleepCycleMemoryCountThreshold, memories)
			})
		}
	}
}

// ---------------------------------------------------------------------------
// TC2: No-op when transcript store is nil (production: store not yet initialised).
// ---------------------------------------------------------------------------

func TestDreamCycle_NoOpWhenTranscriptStoreNil(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	svc := &Service{}
	svc.ctx = context.Background()
	// dreamTranscriptStoreOverride is nil AND real transcriptStore is nil.
	// Should log debug and return without panic.

	assert.NotPanics(t, func() {
		svc.runDreamCrystallization(context.Background())
	})
	assert.EqualValues(t, 0, svc.dreamWatermark.Load())
}

// ---------------------------------------------------------------------------
// TC3: No-op when zero unprocessed transcripts exist since the watermark.
// ---------------------------------------------------------------------------

func TestDreamCycle_NoOpWhenNoTranscripts(t *testing.T) {
	var extractCalled bool
	extractFn := func(_ context.Context, _ string) ([]crystallization.ExtractedDecision, error) {
		extractCalled = true
		return nil, nil
	}
	svc := buildDreamService(t, nil /* no rows */, &fakeCandidateStore{}, extractFn, true)

	svc.runDreamCrystallization(context.Background())

	assert.False(t, extractCalled, "extractor must not be called when there are no transcripts")
	assert.EqualValues(t, 0, svc.dreamWatermark.Load(), "watermark must not advance when no transcripts")
}

// ---------------------------------------------------------------------------
// TC4: Watermark NOT advanced when extraction returns an error.
// ---------------------------------------------------------------------------

func TestDreamCycle_WatermarkNotAdvancedOnExtractError(t *testing.T) {
	base := time.Now()
	rows := []gorm.SessionTranscript{
		makeTranscript(1, "sess-err", "proj", "decided to use Kafka", base, 0),
	}

	extractErr := errors.New("LLM timeout")
	extractFn := func(_ context.Context, _ string) ([]crystallization.ExtractedDecision, error) {
		return nil, extractErr
	}
	svc := buildDreamService(t, rows, &fakeCandidateStore{}, extractFn, true)

	svc.runDreamCrystallization(context.Background())

	assert.EqualValues(t, 0, svc.dreamWatermark.Load(),
		"watermark must not advance when extraction fails")
}

// ---------------------------------------------------------------------------
// TC5: RU + ZH transcripts → ≥1 candidate per language via fake extractor.
//
// This is the main acceptance test. It injects:
//   - one Russian-language transcript
//   - one Chinese-language transcript
//   - a fake extractor that returns one RU decision and one ZH decision
//   - ENGRAM_VNEXT_F_ENABLED=true so RouteDecision creates candidates
//
// Asserts: ≥1 candidate per language was created, watermark was advanced.
// ---------------------------------------------------------------------------

func TestDreamCycle_RUAndZHCandidatesCreated(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	base := time.Now()
	rows := []gorm.SessionTranscript{
		makeTranscript(1, "sess-ru", "proj-ru", "решено использовать PostgreSQL потому что надёжно", base, 0),
		makeTranscript(2, "sess-zh", "proj-zh", "决定使用Redis因为速度快", base, 1),
	}

	// Fake extractor returns one decision per language.
	extractFn := func(_ context.Context, digest string) ([]crystallization.ExtractedDecision, error) {
		require.NotEmpty(t, digest, "digest must be non-empty")
		return []crystallization.ExtractedDecision{
			{
				Text:           "решено использовать PostgreSQL потому что надёжно",
				Lang:           "ru",
				Confidence:     0.9,
				ProposedTarget: "rule",
			},
			{
				Text:           "决定使用Redis因为速度快",
				Lang:           "zh",
				Confidence:     0.85,
				ProposedTarget: "rule",
			},
		}, nil
	}

	cs := &fakeCandidateStore{}
	svc := buildDreamService(t, rows, cs, extractFn, true)

	svc.runDreamCrystallization(context.Background())

	require.GreaterOrEqual(t, len(cs.created), 2,
		"expected ≥2 candidates (at least one per language)")

	langs := make(map[string]int)
	for _, c := range cs.created {
		// CrystallizationCandidate stores the decision text in ProposedContent.
		// Identify language by substring match on the known per-language source texts.
		switch {
		case containsSubstr(c.ProposedContent, "PostgreSQL") || containsSubstr(c.ProposedContent, "надёжно"):
			langs["ru"]++
		case containsSubstr(c.ProposedContent, "Redis") || containsSubstr(c.ProposedContent, "速度快"):
			langs["zh"]++
		}
	}
	assert.GreaterOrEqual(t, langs["ru"], 1, "expected ≥1 RU candidate")
	assert.GreaterOrEqual(t, langs["zh"], 1, "expected ≥1 ZH candidate")

	// Watermark must be advanced to max created_at in the batch.
	expectedWatermark := base.Add(1 * time.Second).UnixNano()
	assert.Equal(t, expectedWatermark, svc.dreamWatermark.Load(),
		"watermark must be advanced to max created_at after successful run")
}

// ---------------------------------------------------------------------------
// TC6: retired startup overrides cannot discard ordinary candidate work.
// ---------------------------------------------------------------------------

func TestDreamCycle_RetiredFlagCannotDisableCandidates(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "false")

	base := time.Now()
	rows := []gorm.SessionTranscript{
		makeTranscript(1, "sess-a", "proj", "decided to use gRPC", base, 0),
	}

	extractCalled := false
	extractFn := func(_ context.Context, _ string) ([]crystallization.ExtractedDecision, error) {
		extractCalled = true
		return []crystallization.ExtractedDecision{
			{Text: "decided to use gRPC", Lang: "en", Confidence: 0.9, ProposedTarget: "rule"},
		}, nil
	}

	cs := &fakeCandidateStore{}
	svc := buildDreamService(t, rows, cs, extractFn, false /* F flag off */)

	svc.runDreamCrystallization(context.Background())

	assert.Len(t, cs.created, 1)
	assert.True(t, extractCalled)
	assert.NotEmpty(t, svc.dreamTranscriptStoreOverride.(*fakeTranscriptStore).marked)
	assert.Equal(t, base.UnixNano(), svc.dreamWatermark.Load())
}

// ---------------------------------------------------------------------------
// TC7: Watermark gate — transcripts created before the watermark are excluded.
// ---------------------------------------------------------------------------

func TestDreamCycle_WatermarkFiltersOldTranscripts(t *testing.T) {
	base := time.Now()
	oldRow := makeTranscript(1, "sess-old", "proj", "old content", base.Add(-10*time.Second), 0)
	newRow := makeTranscript(2, "sess-new", "proj", "decided to use Postgres", base, 0)

	var digestSeen string
	extractFn := func(_ context.Context, digest string) ([]crystallization.ExtractedDecision, error) {
		digestSeen = digest
		return []crystallization.ExtractedDecision{
			{Text: "decided to use Postgres", Lang: "en", Confidence: 0.9, ProposedTarget: "rule"},
		}, nil
	}

	svc := buildDreamService(t, []gorm.SessionTranscript{oldRow, newRow}, &fakeCandidateStore{}, extractFn, true)

	// Set watermark to base - 5s so oldRow (base-10s) is excluded.
	svc.dreamWatermark.Store(base.Add(-5 * time.Second).UnixNano())

	svc.runDreamCrystallization(context.Background())

	// Only newRow content should be in the digest.
	assert.Contains(t, digestSeen, newRow.Content, "digest must include new transcript")
	assert.NotContains(t, digestSeen, oldRow.Content, "digest must NOT include old transcript before watermark")
}

// ---------------------------------------------------------------------------
// TC8: Transcripts are marked processed after a successful run.
// ---------------------------------------------------------------------------

func TestDreamCycle_TranscriptsMarkedProcessed(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	base := time.Now()
	rows := []gorm.SessionTranscript{
		makeTranscript(10, "sess-a", "proj", "decided to adopt TDD", base, 0),
		makeTranscript(11, "sess-b", "proj", "decided to use Go modules", base, 1),
	}

	extractFn := func(_ context.Context, _ string) ([]crystallization.ExtractedDecision, error) {
		return []crystallization.ExtractedDecision{
			{Text: "decided to adopt TDD", Lang: "en", Confidence: 0.9, ProposedTarget: "rule"},
		}, nil
	}

	ts := &fakeTranscriptStore{rows: rows}
	svc := &Service{}
	svc.ctx = context.Background()
	svc.dreamTranscriptStoreOverride = ts
	svc.dreamExtractorFunc = extractFn
	svc.dreamCandidateStoreOverride = &fakeCandidateStore{}

	svc.runDreamCrystallization(context.Background())

	// Both transcript IDs should have been passed to MarkProcessed.
	assert.ElementsMatch(t, []int64{10, 11}, ts.marked,
		"both transcript IDs must be marked processed after a successful run")
}

func TestDreamCycle_CandidatePersistenceUnavailableDoesNotExtractOrMark(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	base := time.Now()
	ts := &fakeTranscriptStore{rows: []gorm.SessionTranscript{
		makeTranscript(91, "session-unavailable", "project-unavailable", "decision", base, 0),
	}}
	extractCalled := false
	svc := &Service{
		dreamTranscriptStoreOverride: ts,
		dreamExtractorFunc: func(context.Context, string) ([]crystallization.ExtractedDecision, error) {
			extractCalled = true
			return nil, nil
		},
	}

	svc.runDreamCrystallization(context.Background())

	assert.False(t, extractCalled)
	assert.Empty(t, ts.marked)
	assert.Zero(t, svc.dreamWatermark.Load())
}

func TestDreamCycle_RouteFailurePreservesUnprocessedBatch(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	base := time.Now()
	ts := &fakeTranscriptStore{rows: []gorm.SessionTranscript{
		makeTranscript(92, "session-route-fail", "project-route-fail", "decision", base, 0),
	}}
	svc := &Service{
		dreamTranscriptStoreOverride: ts,
		dreamCandidateStoreOverride:  &fakeCandidateStore{createErr: errors.New("candidate write failed")},
		dreamExtractorFunc: func(context.Context, string) ([]crystallization.ExtractedDecision, error) {
			return []crystallization.ExtractedDecision{{Text: "decision", Lang: "en", Confidence: 0.9, ProposedTarget: "rule"}}, nil
		},
	}

	svc.runDreamCrystallization(context.Background())

	assert.Empty(t, ts.marked)
	assert.Zero(t, svc.dreamWatermark.Load())
}

func TestDreamCycle_RetiredFlagMutationCannotInterruptBatch(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	base := time.Now()
	ts := &fakeTranscriptStore{rows: []gorm.SessionTranscript{
		makeTranscript(96, "session-flag-flip", "project-flag-flip", "decision", base, 0),
	}}
	cs := &fakeCandidateStore{}
	svc := &Service{
		dreamTranscriptStoreOverride: ts,
		dreamCandidateStoreOverride:  cs,
		dreamExtractorFunc: func(context.Context, string) ([]crystallization.ExtractedDecision, error) {
			require.NoError(t, os.Setenv("ENGRAM_VNEXT_F_ENABLED", "false"))
			return []crystallization.ExtractedDecision{{Text: "decision", Lang: "en", Confidence: 0.9, ProposedTarget: "rule"}}, nil
		},
	}

	svc.runDreamCrystallization(context.Background())

	require.Len(t, cs.created, 1)
	require.Equal(t, []int64{96}, ts.marked)
	require.Equal(t, base.UnixNano(), svc.dreamWatermark.Load())
}

func TestDreamCycle_GroupsExactProjectAndSessionProvenance(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	base := time.Now()
	rows := []gorm.SessionTranscript{
		makeTranscript(93, "session-a", "project-a", "decision-a", base, 0),
		makeTranscript(94, "session-b", "project-b", "decision-b", base, 1),
	}
	cs := &fakeCandidateStore{}
	ts := &fakeTranscriptStore{rows: rows}
	svc := &Service{
		dreamTranscriptStoreOverride: ts,
		dreamCandidateStoreOverride:  cs,
		dreamExtractorFunc: func(_ context.Context, digest string) ([]crystallization.ExtractedDecision, error) {
			return []crystallization.ExtractedDecision{{Text: digest, Lang: "en", Confidence: 0.9, ProposedTarget: "rule"}}, nil
		},
	}

	svc.runDreamCrystallization(context.Background())

	require.Len(t, cs.created, 2)
	bySession := map[string]*models.CrystallizationCandidate{}
	for _, candidate := range cs.created {
		bySession[candidate.SourceSessionID] = candidate
	}
	require.Equal(t, []string{"project-a"}, bySession["session-a"].AffectedProjects)
	require.Equal(t, []string{"project-b"}, bySession["session-b"].AffectedProjects)
	require.NotContains(t, bySession["session-a"].ProposedContent, "decision-b")
	require.NotContains(t, bySession["session-b"].ProposedContent, "decision-a")
	assert.ElementsMatch(t, []int64{93, 94}, ts.marked)
}

func TestDreamCycle_MarkFailureRetriesByFingerprintExactlyOnce(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	base := time.Now()
	ts := &fakeTranscriptStore{
		rows: []gorm.SessionTranscript{
			makeTranscript(95, "session-retry", "project-retry", "decision-retry", base, 0),
		},
		markFailures: 1,
		markErr:      errors.New("mark unavailable"),
	}
	cs := &fakeCandidateStore{}
	extractCalls := 0
	newService := func() *Service {
		return &Service{
			dreamTranscriptStoreOverride: ts,
			dreamCandidateStoreOverride:  cs,
			dreamExtractorFunc: func(context.Context, string) ([]crystallization.ExtractedDecision, error) {
				extractCalls++
				return []crystallization.ExtractedDecision{{Text: "decision-retry", Lang: "en", Confidence: 0.9, ProposedTarget: "rule"}}, nil
			},
		}
	}

	first := newService()
	first.runDreamCrystallization(context.Background())
	require.Len(t, cs.created, 1, "candidate creation committed before injected mark failure")
	require.Empty(t, ts.marked)
	require.Zero(t, first.dreamWatermark.Load())

	// A new Service models process-local state loss. The durable transcript and
	// candidate stores survive; the retry must accept the fingerprint duplicate,
	// mark the row, and avoid a second candidate.
	restarted := newService()
	restarted.runDreamCrystallization(context.Background())
	require.Len(t, cs.created, 1, "fingerprint retry must not duplicate the durable candidate")
	require.Equal(t, []int64{95}, ts.marked)
	require.Equal(t, base.UnixNano(), restarted.dreamWatermark.Load())

	// A second restart sees no unprocessed row and therefore does not extract.
	afterSuccess := newService()
	afterSuccess.runDreamCrystallization(context.Background())
	require.Len(t, cs.created, 1)
	require.Equal(t, 2, extractCalls)
}

func TestDreamCycle_RealDBRestartRetryPersistsExactlyOneCandidate(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set; dream-cycle restart proof requires PostgreSQL")
	}
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	marker := fmt.Sprintf("dream-restart-%d", time.Now().UnixNano())
	project := uuid.NewString()
	decisionText := "decision " + marker
	createdAt := time.Now().UTC().Truncate(time.Microsecond)

	cleanup := func() {
		store, err := gorm.NewStore(gorm.Config{DSN: dsn, MaxConns: 2})
		if err != nil {
			return
		}
		_ = store.DB.Exec("DELETE FROM audit_log WHERE source_session_id = ? OR actor = ?", marker, marker).Error
		_ = store.DB.Exec("DELETE FROM crystallization_candidates WHERE source_session_id = ?", marker).Error
		_ = store.DB.Exec("DELETE FROM session_transcripts WHERE session_id = ?", marker).Error
		_ = store.DB.Where("id = ?", project).Delete(&gorm.Project{}).Error
		_ = store.Close()
	}
	cleanup()
	t.Cleanup(cleanup)

	extractCalls := 0
	extractFn := func(context.Context, string) ([]crystallization.ExtractedDecision, error) {
		extractCalls++
		return []crystallization.ExtractedDecision{{Text: decisionText, Lang: "en", Confidence: 0.9, ProposedTarget: "rule"}}, nil
	}

	store1, err := gorm.NewStore(gorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)
	require.NoError(t, store1.DB.Create(&gorm.Project{ID: project}).Error)
	ts1 := gorm.NewTranscriptStore(store1.DB)
	require.NoError(t, ts1.Create(context.Background(), &gorm.SessionTranscript{SessionID: marker, Project: project, Content: decisionText, CreatedAt: createdAt}))
	cs1 := gorm.NewCandidateStore(store1.DB, gorm.NewAuditStore(store1.DB))
	first := &Service{dreamTranscriptStoreOverride: &markFailOnceTranscriptStore{dreamTranscriptStore: ts1}, dreamCandidateStoreOverride: cs1, dreamExtractorFunc: extractFn}
	first.runDreamCrystallization(context.Background())

	var candidates, unprocessed int64
	require.NoError(t, store1.DB.Table("crystallization_candidates").Where("source_session_id = ?", marker).Count(&candidates).Error)
	require.NoError(t, store1.DB.Table("session_transcripts").Where("session_id = ? AND processed_at IS NULL", marker).Count(&unprocessed).Error)
	require.EqualValues(t, 1, candidates)
	require.EqualValues(t, 1, unprocessed, "mark failure must preserve the durable transcript for restart retry")
	require.Zero(t, first.dreamWatermark.Load())
	require.NoError(t, store1.Close())

	store2, err := gorm.NewStore(gorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)
	restarted := &Service{dreamTranscriptStoreOverride: gorm.NewTranscriptStore(store2.DB), dreamCandidateStoreOverride: gorm.NewCandidateStore(store2.DB, gorm.NewAuditStore(store2.DB)), dreamExtractorFunc: extractFn}
	restarted.runDreamCrystallization(context.Background())
	require.NoError(t, store2.DB.Table("crystallization_candidates").Where("source_session_id = ?", marker).Count(&candidates).Error)
	require.NoError(t, store2.DB.Table("session_transcripts").Where("session_id = ?", marker).Count(&unprocessed).Error)
	require.EqualValues(t, 1, candidates, "fingerprint duplicate on retry must not create a second candidate")
	require.Zero(t, unprocessed, "successful retry must mark and prune the transcript")
	require.Equal(t, createdAt.UnixNano(), restarted.dreamWatermark.Load())
	require.NoError(t, store2.Close())

	store3, err := gorm.NewStore(gorm.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)
	afterSuccess := &Service{dreamTranscriptStoreOverride: gorm.NewTranscriptStore(store3.DB), dreamCandidateStoreOverride: gorm.NewCandidateStore(store3.DB, gorm.NewAuditStore(store3.DB)), dreamExtractorFunc: extractFn}
	afterSuccess.runDreamCrystallization(context.Background())
	require.NoError(t, store3.DB.Table("crystallization_candidates").Where("source_session_id = ?", marker).Count(&candidates).Error)
	require.EqualValues(t, 1, candidates)
	require.Equal(t, 2, extractCalls, "a post-success process restart must see no work")
	require.NoError(t, store3.Close())
}

// ---------------------------------------------------------------------------
// US4 Degrade scenario tests (STEP 3 — T012).
// ---------------------------------------------------------------------------

// TestDreamCycle_LLMDisabledDoesNotMark verifies that the active candidate path
// still leaves transcripts untouched when no LLM client can be configured.
func TestDreamCycle_LLMDisabledDoesNotMark(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	t.Setenv("ENGRAM_LLM_URL", "")

	svc := &Service{dreamCandidateStoreOverride: &fakeCandidateStore{}}
	ts := &fakeTranscriptStore{rows: []gorm.SessionTranscript{
		makeTranscript(1, "sess-llm-disabled", "proj", "decided to use PostgreSQL", time.Now(), 0),
	}}
	svc.dreamTranscriptStoreOverride = ts

	svc.runDreamCrystallization(context.Background())

	assert.Empty(t, ts.marked, "LLM-disabled runs must not mark transcripts processed")
	assert.Zero(t, svc.dreamWatermark.Load(), "LLM-disabled runs must not advance the watermark")
}

// ---------------------------------------------------------------------------
// Helper: containsSubstr returns true when s contains substr.
// Using a helper avoids importing strings in the test file.
// ---------------------------------------------------------------------------

func containsSubstr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		indexSubstr(s, substr) >= 0)
}

func indexSubstr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
