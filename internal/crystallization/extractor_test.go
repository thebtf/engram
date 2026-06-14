package crystallization

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCompleter implements llm.Completer and returns a fixed string regardless of input.
type fakeCompleter struct {
	response string
	err      error
}

func (f *fakeCompleter) Complete(_ context.Context, _, _ string) (string, error) {
	return f.response, f.err
}

// TestLLMExtractor_Russian verifies that a Russian-language decision digest
// produces ≥1 ExtractedDecision with Lang=="ru" and non-empty Text.
// This is the inversion of the measured EN=2/RU=0/ZH=0 regression.
func TestLLMExtractor_Russian(t *testing.T) {
	resp := `[
		{
			"text": "Решено использовать PostgreSQL из-за поддержки pgvector",
			"lang": "ru",
			"confidence": 0.9,
			"evidence": ["pgvector нативно поддерживается"],
			"proposed_target": "rule"
		}
	]`
	e := NewLLMExtractor(&fakeCompleter{response: resp})
	decisions, err := e.Extract(context.Background(), "дайджест на русском языке")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(decisions), 1, "RU digest must yield ≥1 ExtractedDecision")
	assert.Equal(t, "ru", decisions[0].Lang, "Lang must be 'ru'")
	assert.NotEmpty(t, decisions[0].Text, "Text must be non-empty")
}

// TestLLMExtractor_English verifies that an English-language decision digest
// produces ≥1 ExtractedDecision with Lang=="en" and non-empty Text.
func TestLLMExtractor_English(t *testing.T) {
	resp := `[
		{
			"text": "Decided to use Redis for session caching",
			"lang": "en",
			"confidence": 0.85,
			"evidence": ["low-latency requirement"],
			"proposed_target": "rule"
		},
		{
			"text": "Going forward, all secrets must be stored in the vault",
			"lang": "en",
			"confidence": 0.95,
			"evidence": [],
			"proposed_target": "rule"
		}
	]`
	e := NewLLMExtractor(&fakeCompleter{response: resp})
	decisions, err := e.Extract(context.Background(), "english session digest")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(decisions), 1, "EN digest must yield ≥1 ExtractedDecision")
	assert.Equal(t, "en", decisions[0].Lang)
	assert.NotEmpty(t, decisions[0].Text)
}

// TestLLMExtractor_Chinese verifies that a Chinese-language decision digest
// produces ≥1 ExtractedDecision with Lang=="zh" and non-empty Text.
// This is the inversion of the measured EN=2/RU=0/ZH=0 regression.
func TestLLMExtractor_Chinese(t *testing.T) {
	resp := `[
		{
			"text": "决定使用 PostgreSQL，因为它原生支持 pgvector",
			"lang": "zh",
			"confidence": 0.88,
			"evidence": ["pgvector 原生支持"],
			"proposed_target": "rule"
		}
	]`
	e := NewLLMExtractor(&fakeCompleter{response: resp})
	decisions, err := e.Extract(context.Background(), "中文会话摘要")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(decisions), 1, "ZH digest must yield ≥1 ExtractedDecision")
	assert.Equal(t, "zh", decisions[0].Lang, "Lang must be 'zh'")
	assert.NotEmpty(t, decisions[0].Text, "Text must be non-empty")
}

// TestLLMExtractor_MalformedJSON verifies skip-on-malformed: a response that
// cannot be parsed as a JSON array returns an empty slice and nil error.
func TestLLMExtractor_MalformedJSON(t *testing.T) {
	e := NewLLMExtractor(&fakeCompleter{response: "Sorry, I cannot help with that."})
	decisions, err := e.Extract(context.Background(), "some digest")
	assert.NoError(t, err, "malformed JSON must return nil error")
	assert.Empty(t, decisions, "malformed JSON must return empty slice")
}

// TestLLMExtractor_MarkdownFences verifies that JSON wrapped in markdown fences
// is correctly parsed (tolerant parse).
func TestLLMExtractor_MarkdownFences(t *testing.T) {
	resp := "```json\n[\n  {\"text\": \"Decided to cache aggressively\", \"lang\": \"en\", \"confidence\": 0.8}\n]\n```"
	e := NewLLMExtractor(&fakeCompleter{response: resp})
	decisions, err := e.Extract(context.Background(), "digest")
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	assert.Equal(t, "Decided to cache aggressively", decisions[0].Text)
}

// TestLLMExtractor_EmptyArray verifies that an empty JSON array produces an empty slice.
func TestLLMExtractor_EmptyArray(t *testing.T) {
	e := NewLLMExtractor(&fakeCompleter{response: "[]"})
	decisions, err := e.Extract(context.Background(), "uneventful digest")
	assert.NoError(t, err)
	assert.Empty(t, decisions)
}

// TestLLMExtractor_DefaultProposedTarget verifies that a missing proposed_target
// field defaults to "rule".
func TestLLMExtractor_DefaultProposedTarget(t *testing.T) {
	resp := `[{"text": "Chose Go over Python", "lang": "en", "confidence": 0.75}]`
	e := NewLLMExtractor(&fakeCompleter{response: resp})
	decisions, err := e.Extract(context.Background(), "digest")
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	assert.Equal(t, "rule", decisions[0].ProposedTarget, "empty proposed_target must default to 'rule'")
}
