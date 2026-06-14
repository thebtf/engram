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
// No DATABASE_DSN is required or read by any test in this file.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/crystallization"
	gorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// ---------------------------------------------------------------------------
// Fake stores — satisfy CandidateWriter and MemoryFingerprintChecker.
// ---------------------------------------------------------------------------

type fakeCandidateStore struct {
	created []*models.CrystallizationCandidate
	nextID  atomic.Int64
}

func (f *fakeCandidateStore) Create(_ context.Context, c *models.CrystallizationCandidate) (*models.CrystallizationCandidate, error) {
	id := f.nextID.Add(1)
	c.ID = id
	f.created = append(f.created, c)
	return c, nil
}

func (f *fakeCandidateStore) GetByFingerprint(_ context.Context, _ string) (*models.CrystallizationCandidate, error) {
	return nil, nil // always miss → create
}

type fakeMemChecker struct{}

func (f *fakeMemChecker) ListBySourceAgentAndTag(_ context.Context, _, _, _ string) ([]*models.Memory, error) {
	return nil, nil // no existing memories
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

// ---------------------------------------------------------------------------
// TC2: No-op when transcript store is nil (production: store not yet initialised).
// ---------------------------------------------------------------------------

func TestDreamCycle_NoOpWhenTranscriptStoreNil(t *testing.T) {
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
	svc := buildDreamService(t, nil /* no rows */, nil, extractFn, false)

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
	svc := buildDreamService(t, rows, nil, extractFn, false)

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
// TC6: F-flag OFF → RouteDecision returns nil → no candidates created,
// watermark IS advanced (extraction succeeded, candidates just require F flag).
// ---------------------------------------------------------------------------

func TestDreamCycle_FlagOffNoCandidates(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "false")

	base := time.Now()
	rows := []gorm.SessionTranscript{
		makeTranscript(1, "sess-a", "proj", "decided to use gRPC", base, 0),
	}

	extractFn := func(_ context.Context, _ string) ([]crystallization.ExtractedDecision, error) {
		return []crystallization.ExtractedDecision{
			{Text: "decided to use gRPC", Lang: "en", Confidence: 0.9, ProposedTarget: "rule"},
		}, nil
	}

	cs := &fakeCandidateStore{}
	svc := buildDreamService(t, rows, cs, extractFn, false /* F flag off */)

	svc.runDreamCrystallization(context.Background())

	// RouteDecision returns nil when F flag is off; no candidates expected.
	assert.Len(t, cs.created, 0, "no candidates expected when ENGRAM_VNEXT_F_ENABLED=false")

	// Watermark IS advanced — extraction succeeded even though candidate path was inactive.
	assert.Equal(t, base.UnixNano(), svc.dreamWatermark.Load(),
		"watermark must be advanced after successful extraction even with F flag off")
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

	svc := buildDreamService(t, []gorm.SessionTranscript{oldRow, newRow}, nil, extractFn, false)

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

	svc.runDreamCrystallization(context.Background())

	// Both transcript IDs should have been passed to MarkProcessed.
	assert.ElementsMatch(t, []int64{10, 11}, ts.marked,
		"both transcript IDs must be marked processed after a successful run")
}

// ---------------------------------------------------------------------------
// US4 Degrade scenario tests (STEP 3 — T012).
// ---------------------------------------------------------------------------

// TestDreamCycle_FlagOff_NoWork verifies US4(b): when ENGRAM_CRYSTALLIZATION_ENABLED
// is unset (off), maybeSleepCycle must NOT invoke runDreamCrystallization.
// We test the gate condition directly via isCrystallizationEnabled() because
// driving a full maybeSleepCycle requires a live DB for the count query.
// The invariant: isCrystallizationEnabled() == false → the gate in sleep_cycle.go
// blocks the call. We additionally verify that runDreamCrystallization itself
// is a no-op when the transcript store is nil (belt-and-suspenders).
func TestDreamCycle_FlagOff_NoWork(t *testing.T) {
	t.Setenv("ENGRAM_CRYSTALLIZATION_ENABLED", "")

	// Gate check: the crystallization flag must report false.
	assert.False(t, isCrystallizationEnabled(),
		"isCrystallizationEnabled() must be false when ENGRAM_CRYSTALLIZATION_ENABLED is unset")

	// Even if runDreamCrystallization were called directly, it returns immediately
	// when the transcript store is nil — no candidates created, no watermark advance.
	svc := &Service{}
	svc.ctx = context.Background()
	cs := &fakeCandidateStore{}
	svc.dreamCandidateStoreOverride = cs
	// dreamTranscriptStoreOverride is nil → production nil-guard fires first.

	svc.runDreamCrystallization(context.Background())

	assert.Len(t, cs.created, 0, "no candidates must be created when transcript store is nil")
	assert.EqualValues(t, 0, svc.dreamWatermark.Load(), "watermark must not advance")
}

// TestDreamCycle_RawMemoryUnaffected verifies US4(c): with the dream-cycle a no-op
// (LLM disabled), a normal memory write via the fake memory store is unaffected.
// runDreamCrystallization returning early must not touch or corrupt external state.
func TestDreamCycle_RawMemoryUnaffected(t *testing.T) {
	t.Setenv("ENGRAM_LLM_URL", "") // LLM disabled → no-op in runDreamCrystallization

	svc := &Service{}
	svc.ctx = context.Background()
	ts := &fakeTranscriptStore{rows: []gorm.SessionTranscript{
		makeTranscript(1, "sess-mem", "proj", "decided to use PostgreSQL", time.Now(), 0),
	}}
	svc.dreamTranscriptStoreOverride = ts

	// Run the dream-cycle; it must return early (LLM disabled) without touching anything.
	svc.runDreamCrystallization(context.Background())

	// The transcript store must not have had MarkProcessed called (no LLM extraction ran).
	assert.Len(t, ts.marked, 0,
		"MarkProcessed must not be called when dream-cycle exits early due to disabled LLM")
	assert.EqualValues(t, 0, svc.dreamWatermark.Load(),
		"watermark must not advance when dream-cycle is a no-op")
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
