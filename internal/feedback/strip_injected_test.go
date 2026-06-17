package feedback

import (
	"strings"
	"testing"

	"github.com/thebtf/engram/pkg/models"
)

// --- StripInjectedBlocks: rank-2 anti-poisoning ---
//
// The stop hook extracts only assistant-role text into agent_output_text. Injected
// engram wrappers reach the citation path only when the agent echoes a block verbatim.
// StripInjectedBlocks removes those wrappers so an echo cannot self-cite.

func TestStripInjectedBlocks_RemovesStaticMemories(t *testing.T) {
	out := "Here is my plan.\n<engram-static-memories>\nuse the structural fix when 3+ call sites are touched\n</engram-static-memories>\nNow I will implement it."
	got := StripInjectedBlocks(out)
	if strings.Contains(got, "structural fix when 3+ call sites") {
		t.Errorf("injected memory text survived strip: %q", got)
	}
	if !strings.Contains(got, "Here is my plan.") || !strings.Contains(got, "Now I will implement it.") {
		t.Errorf("agent's own prose was removed: %q", got)
	}
}

func TestStripInjectedBlocks_RemovesBehaviorRules(t *testing.T) {
	out := "<user-behavior-rules>\nnever skip the build step\n</user-behavior-rules>\nI ran the build."
	got := StripInjectedBlocks(out)
	if strings.Contains(got, "never skip the build step") {
		t.Errorf("injected rule text survived strip: %q", got)
	}
	if !strings.Contains(got, "I ran the build.") {
		t.Errorf("agent prose removed: %q", got)
	}
}

func TestStripInjectedBlocks_RemovesOpenIssuesWithAttributes(t *testing.T) {
	out := `prefix <open-issues count="2" project="engram" action-required="true">
issue: fix the recall noise
</open-issues> suffix`
	got := StripInjectedBlocks(out)
	if strings.Contains(got, "fix the recall noise") {
		t.Errorf("injected issue text survived strip: %q", got)
	}
	if !strings.Contains(got, "prefix") || !strings.Contains(got, "suffix") {
		t.Errorf("surrounding prose removed: %q", got)
	}
}

func TestStripInjectedBlocks_RemovesReinjection(t *testing.T) {
	out := "<engram-reinjection>\ncreated_at is the staleness anchor, not updated_at\n</engram-reinjection>"
	got := StripInjectedBlocks(out)
	if strings.Contains(got, "created_at is the staleness anchor") {
		t.Errorf("pre-compact reinjection text survived strip: %q", got)
	}
}

// Future-tag coverage: a memory-bearing <engram-*> tag added later is stripped
// without editing the regex. This is the demolition/staleness guard — the test
// fails loudly if someone narrows the pattern back to a hard-coded tag list.
func TestStripInjectedBlocks_RemovesFutureEngramTag(t *testing.T) {
	out := "<engram-some-new-block>\nfuture injected memory payload\n</engram-some-new-block>"
	got := StripInjectedBlocks(out)
	if strings.Contains(got, "future injected memory payload") {
		t.Errorf("future <engram-*> block survived strip: %q", got)
	}
}

func TestStripInjectedBlocks_RemovesMultipleBlocks(t *testing.T) {
	out := "a <user-behavior-rules>\nrule one\n</user-behavior-rules> b <engram-static-memories>\nmemory two\n</engram-static-memories> c"
	got := StripInjectedBlocks(out)
	if strings.Contains(got, "rule one") || strings.Contains(got, "memory two") {
		t.Errorf("multi-block strip incomplete: %q", got)
	}
	for _, marker := range []string{"a ", " b ", " c"} {
		if !strings.Contains(got, marker) {
			t.Errorf("agent prose marker %q removed: %q", marker, got)
		}
	}
}

func TestStripInjectedBlocks_NoOpWhenNoWrappers(t *testing.T) {
	out := "I considered the structural fix and applied it across all call sites."
	got := StripInjectedBlocks(out)
	if got != out {
		t.Errorf("fast path mutated wrapper-free output:\n in:  %q\n out: %q", out, got)
	}
}

func TestStripInjectedBlocks_Empty(t *testing.T) {
	if got := StripInjectedBlocks(""); got != "" {
		t.Errorf("empty input should return empty, got %q", got)
	}
}

// --- End-to-end: echoed injection must NOT self-cite (the whole point of rank-2) ---

// When the agent echoes an injected <engram-static-memories> block whose text is the
// memory content verbatim, DetectCitations on the RAW output would mark it Cited
// (self-citation). On the STRIPPED output it must not — unless the agent ALSO
// references the memory in its own prose outside the wrapper.
func TestStripThenDetect_EchoedInjectionDoesNotSelfCite(t *testing.T) {
	memContent := "Cross-encoder rerank improves top-slot accuracy\nApply bge-reranker after RRF fusion."
	mem := makeMemWithContent(1, memContent)

	// Agent output that ONLY echoes the injected block — no independent reference.
	echoed := "<engram-static-memories>\n" + memContent + "\n</engram-static-memories>\nOkay, starting now."

	rawResults := DetectCitations(echoed, []*models.Memory{mem})
	if !rawResults[0].Cited {
		t.Fatalf("precondition: raw echoed output should self-cite (else test proves nothing)")
	}

	cleaned := StripInjectedBlocks(echoed)
	cleanResults := DetectCitations(cleaned, []*models.Memory{mem})
	if cleanResults[0].Cited {
		t.Errorf("anti-poisoning failed: echoed injection still self-cites after strip; excerpt=%q", cleanResults[0].Excerpt)
	}
}

// When the agent genuinely references the memory in its OWN prose (outside the
// wrapper), stripping the wrapper must still leave that reference intact, so a real
// citation is preserved.
func TestStripThenDetect_GenuineReferenceSurvives(t *testing.T) {
	memContent := "Cross-encoder rerank improves top-slot accuracy\nApply bge-reranker after RRF fusion."
	mem := makeMemWithContent(1, memContent)

	// Injected block PLUS an independent genuine reference in the agent's own words.
	mixed := "<engram-static-memories>\n" + memContent + "\n</engram-static-memories>\n" +
		"I applied the Cross-encoder rerank improves top-slot accuracy guidance to reorder results."

	cleaned := StripInjectedBlocks(mixed)
	results := DetectCitations(cleaned, []*models.Memory{mem})
	if !results[0].Cited {
		t.Errorf("genuine reference outside the wrapper was lost after strip; cleaned=%q", cleaned)
	}
}
