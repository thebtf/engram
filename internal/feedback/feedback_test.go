package feedback

import (
	"testing"

	"github.com/thebtf/engram/pkg/models"
)

// makeMemWithContent creates a Memory whose Content has a first-line title followed by body text.
func makeMemWithContent(id int64, content string) *models.Memory {
	return &models.Memory{
		ID:      id,
		Content: content,
	}
}

// --- Scenario 1: Title match (FR-A1 bullet 3a) ---
// The first line of Content acts as the title. If the title appears verbatim in
// agentOutput, the memory is Cited.

func TestDetectCitations_TitleMatch(t *testing.T) {
	mem := makeMemWithContent(1, "Error handling pattern in auth module\nDetailed description goes here.")
	agentOutput := "I used the Error handling pattern in auth module approach to fix this."

	results := DetectCitations(agentOutput, []*models.Memory{mem})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Cited {
		t.Errorf("FR-A1: title appears in agent output but Cited=false")
	}
	if results[0].MemoryID != 1 {
		t.Errorf("expected MemoryID=1, got %d", results[0].MemoryID)
	}
}

// --- Scenario 2: Title NOT matched ---

func TestDetectCitations_TitleNotMatched(t *testing.T) {
	mem := makeMemWithContent(2, "Database migration strategy\nAlways run migrations inside a transaction.")
	agentOutput := "I fixed the authentication bug by rotating the JWT secret."

	results := DetectCitations(agentOutput, []*models.Memory{mem})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Cited {
		t.Errorf("FR-A1: title absent from output but Cited=true (false positive)")
	}
}

// --- Scenario 3: Empty inputs ---

func TestDetectCitations_EmptyInputs(t *testing.T) {
	mem := makeMemWithContent(1, "Some memory title\nBody text.")

	t.Run("empty agent output", func(t *testing.T) {
		results := DetectCitations("", []*models.Memory{mem})
		for _, r := range results {
			if r.Cited {
				t.Errorf("empty output: Cited=true for memory ID=%d", r.MemoryID)
			}
		}
	})

	t.Run("nil memories", func(t *testing.T) {
		results := DetectCitations("agent said something", nil)
		if len(results) != 0 {
			t.Errorf("nil memories: expected 0 results, got %d", len(results))
		}
	})

	t.Run("empty memories slice", func(t *testing.T) {
		results := DetectCitations("agent said something", []*models.Memory{})
		if len(results) != 0 {
			t.Errorf("empty memories: expected 0 results, got %d", len(results))
		}
	})
}

// --- Scenario 4: Multiple memories — mixed cited / not cited ---

func TestDetectCitations_MixedMemories(t *testing.T) {
	mems := []*models.Memory{
		makeMemWithContent(10, "Authentication token refresh strategy\nUse sliding windows."),
		makeMemWithContent(20, "Database connection pooling\nSet max connections to 25."),
		makeMemWithContent(30, "Cache invalidation approach\nPurge on write."),
	}

	// Output explicitly references memory 10 and 30 titles; not memory 20.
	agentOutput := "Following the Authentication token refresh strategy and Cache invalidation approach."

	results := DetectCitations(agentOutput, mems)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	byID := make(map[int64]CitationResult)
	for _, r := range results {
		byID[r.MemoryID] = r
	}

	if !byID[10].Cited {
		t.Errorf("memory 10 title appears in output but Cited=false")
	}
	if byID[20].Cited {
		t.Errorf("memory 20 title absent from output but Cited=true (false positive)")
	}
	if !byID[30].Cited {
		t.Errorf("memory 30 title appears in output but Cited=false")
	}
}

// --- Scenario 5: Short content must not false-positive match everything ---
// A memory whose entire content is fewer than 5 characters cannot meaningfully
// contribute a "title" that triggers a citation — the spec must not false-positive
// every response as citing a 1-char fragment.

func TestDetectCitations_ShortContentIgnored(t *testing.T) {
	// Content shorter than 5 characters — "ab" appears in many sentences by coincidence.
	shortMem := makeMemWithContent(99, "ab")
	agentOutput := "The label contains ab characters and some other words about database tables."

	results := DetectCitations(agentOutput, []*models.Memory{shortMem})
	if len(results) == 0 {
		// implementation returned nothing — acceptable (no result is not a false positive)
		return
	}
	for _, r := range results {
		if r.MemoryID == 99 && r.Cited {
			t.Errorf("short content (<5 chars) caused a false-positive citation")
		}
	}
}

// --- Scenario 6: Result carries a non-empty Excerpt when cited ---

func TestDetectCitations_ExcerptPopulated(t *testing.T) {
	mem := makeMemWithContent(5, "Zero-copy buffer strategy\nAvoid allocating intermediate buffers.")
	agentOutput := "We applied the Zero-copy buffer strategy to reduce GC pressure."

	results := DetectCitations(agentOutput, []*models.Memory{mem})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Cited && results[0].Excerpt == "" {
		t.Errorf("FR-A1: Cited=true but Excerpt is empty; spec requires a non-empty excerpt when a citation is detected")
	}
}
