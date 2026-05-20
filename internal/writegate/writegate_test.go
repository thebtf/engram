package writegate

import (
	"context"
	"math"
	"testing"

	"github.com/thebtf/engram/pkg/models"
)

// tolerance for float64 comparisons
const tol = 0.01

func floatEq(a, b float64) bool {
	return math.Abs(a-b) < tol
}

// ---------------------------------------------------------------------------
// Jaccard tests (FR-A7)
// ---------------------------------------------------------------------------

// Scenario 1: identical strings → 1.0
func TestJaccard_IdenticalStrings(t *testing.T) {
	got := Jaccard("the quick brown fox", "the quick brown fox")
	if !floatEq(got, 1.0) {
		t.Errorf("Jaccard(identical): want 1.0, got %f", got)
	}
}

// Scenario 2: completely different tokens → 0.0
func TestJaccard_CompletelyDifferent(t *testing.T) {
	got := Jaccard("hello world", "foo bar baz")
	if !floatEq(got, 0.0) {
		t.Errorf("Jaccard(completely different): want 0.0, got %f", got)
	}
}

// Scenario 3: partial overlap
// "the quick brown fox" → {the, quick, brown, fox}
// "the quick brown dog" → {the, quick, brown, dog}
// intersection = {the, quick, brown} = 3
// union        = {the, quick, brown, fox, dog} = 5
// Jaccard = 3/5 = 0.6
func TestJaccard_PartialOverlap(t *testing.T) {
	got := Jaccard("the quick brown fox", "the quick brown dog")
	want := 0.6
	if !floatEq(got, want) {
		t.Errorf("Jaccard(partial overlap): want %f, got %f", want, got)
	}
}

// Scenario 4: case insensitive
func TestJaccard_CaseInsensitive(t *testing.T) {
	got := Jaccard("Hello World", "hello world")
	if !floatEq(got, 1.0) {
		t.Errorf("Jaccard(case insensitive): want 1.0, got %f", got)
	}
}

// Scenario 5a: both empty → 1.0 (identical emptiness)
func TestJaccard_BothEmpty(t *testing.T) {
	got := Jaccard("", "")
	if !floatEq(got, 1.0) {
		t.Errorf("Jaccard(both empty): want 1.0, got %f", got)
	}
}

// Scenario 5b: one non-empty, one empty → 0.0
func TestJaccard_OneEmpty_A(t *testing.T) {
	got := Jaccard("hello", "")
	if !floatEq(got, 0.0) {
		t.Errorf("Jaccard(a non-empty, b empty): want 0.0, got %f", got)
	}
}

func TestJaccard_OneEmpty_B(t *testing.T) {
	got := Jaccard("", "hello")
	if !floatEq(got, 0.0) {
		t.Errorf("Jaccard(a empty, b non-empty): want 0.0, got %f", got)
	}
}

// Scenario 6: punctuation stripped → treated as same tokens
func TestJaccard_PunctuationStripped(t *testing.T) {
	got := Jaccard("hello, world!", "hello world")
	if !floatEq(got, 1.0) {
		t.Errorf("Jaccard(punctuation stripped): want 1.0, got %f", got)
	}
}

// ---------------------------------------------------------------------------
// Check tests (FR-A7)
// ---------------------------------------------------------------------------

// Scenario 7: unique content → pass
func TestCheck_UniqueContent_Pass(t *testing.T) {
	stored := []*models.Memory{
		{ID: 1, Content: "database migration strategy"},
	}
	result := Check(context.Background(), "authentication token refresh", stored)

	if result.Decision != "pass" {
		t.Errorf("unique content: want Decision=pass, got %q", result.Decision)
	}
	if result.NoveltyScore <= 0.7 {
		t.Errorf("unique content: want NoveltyScore > 0.7, got %f", result.NoveltyScore)
	}
	if result.MaxJaccard >= 0.3 {
		t.Errorf("unique content: want MaxJaccard < 0.3, got %f", result.MaxJaccard)
	}
}

// Scenario 8: exact duplicate → flag
func TestCheck_ExactDuplicate_Flag(t *testing.T) {
	stored := []*models.Memory{
		{ID: 42, Content: "fix authentication bug in login handler"},
	}
	result := Check(context.Background(), "fix authentication bug in login handler", stored)

	if result.Decision != "flag" {
		t.Errorf("exact duplicate: want Decision=flag, got %q", result.Decision)
	}
	if !floatEq(result.NoveltyScore, 0.0) {
		t.Errorf("exact duplicate: want NoveltyScore ≈ 0.0, got %f", result.NoveltyScore)
	}
	if !floatEq(result.MaxJaccard, 1.0) {
		t.Errorf("exact duplicate: want MaxJaccard ≈ 1.0, got %f", result.MaxJaccard)
	}
	if result.SimilarExisting == nil {
		t.Error("exact duplicate: want SimilarExisting != nil, got nil")
	}
}

// Scenario 9: near-duplicate (Jaccard > 0.7) → flag, SimilarExisting set
func TestCheck_NearDuplicate_Flag(t *testing.T) {
	stored := []*models.Memory{
		{ID: 7, Content: "fix authentication bug in login handler"},
	}
	// "fix authentication bug in the login handler code"
	// vs "fix authentication bug in login handler"
	// Tokens a: {fix, authentication, bug, in, the, login, handler, code} = 8
	// Tokens b: {fix, authentication, bug, in, login, handler} = 6
	// Intersection: {fix, authentication, bug, in, login, handler} = 6
	// Union: {fix, authentication, bug, in, the, login, handler, code} = 8
	// Jaccard = 6/8 = 0.75 → novelty = 0.25 < 0.3 → flag
	result := Check(context.Background(), "fix authentication bug in the login handler code", stored)

	if result.Decision != "flag" {
		t.Errorf("near-duplicate: want Decision=flag, got %q", result.Decision)
	}
	if result.SimilarExisting == nil {
		t.Error("near-duplicate: want SimilarExisting != nil, got nil")
	} else if *result.SimilarExisting != 7 {
		t.Errorf("near-duplicate: want SimilarExisting=7, got %d", *result.SimilarExisting)
	}
}

// Scenario 10: empty stored memories → always pass, novelty = 1.0
func TestCheck_EmptyStoredMemories(t *testing.T) {
	result := Check(context.Background(), "some content", nil)

	if result.Decision != "pass" {
		t.Errorf("empty stored: want Decision=pass, got %q", result.Decision)
	}
	if !floatEq(result.NoveltyScore, 1.0) {
		t.Errorf("empty stored: want NoveltyScore=1.0, got %f", result.NoveltyScore)
	}
}

// Scenario 11: empty content → pass (nothing to compare)
func TestCheck_EmptyContent(t *testing.T) {
	stored := []*models.Memory{
		{ID: 1, Content: "some stored memory"},
	}
	result := Check(context.Background(), "", stored)

	if result.Decision != "pass" {
		t.Errorf("empty content: want Decision=pass, got %q", result.Decision)
	}
}

// Scenario 12: SimilarExisting points to the most-similar memory ID
func TestCheck_SimilarExistingPointsToCorrectMemory(t *testing.T) {
	stored := []*models.Memory{
		{ID: 1, Content: "aaa"},
		{ID: 2, Content: "same content"},
		{ID: 3, Content: "bbb"},
	}
	result := Check(context.Background(), "same content", stored)

	if result.Decision != "flag" {
		t.Errorf("correct ID: want Decision=flag, got %q", result.Decision)
	}
	if result.SimilarExisting == nil {
		t.Fatal("correct ID: want SimilarExisting != nil, got nil")
	}
	if *result.SimilarExisting != 2 {
		t.Errorf("correct ID: want SimilarExisting=2, got %d", *result.SimilarExisting)
	}
}
