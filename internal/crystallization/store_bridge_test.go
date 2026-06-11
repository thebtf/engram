package crystallization

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildMemories_EmptyDecisions(t *testing.T) {
	mems := BuildMemories(nil, "sess-1", "myproject")
	assert.Nil(t, mems, "nil decisions must return nil slice")

	mems = BuildMemories([]ExtractedDecision{}, "sess-2", "myproject")
	assert.Nil(t, mems, "empty decisions must return nil slice")
}

func TestBuildMemories_Fields(t *testing.T) {
	decisions := []ExtractedDecision{
		{Text: "decided to use PostgreSQL because it scales well", Pattern: "decided", Position: 0},
		{Text: "chose Go over Python for performance", Pattern: "chose", Position: 10},
	}

	mems := BuildMemories(decisions, "test-session-id", "test-project")
	require.Len(t, mems, 2)

	for i, mem := range mems {
		assert.Equal(t, "test-project", mem.Project, "memory %d: wrong project", i)
		assert.Equal(t, "decision", mem.EpistemicType, "memory %d: wrong epistemic_type", i)
		assert.Equal(t, "episodic", mem.Tier, "memory %d: wrong tier", i)
		assert.Equal(t, "crystallization", mem.SourceAgent, "memory %d: wrong source_agent", i)
		assert.Contains(t, mem.Tags, "crystallization", "memory %d: missing crystallization tag", i)
		assert.Contains(t, mem.Tags, "session:test-session-id", "memory %d: missing session tag", i)
		assert.NotEmpty(t, mem.Content, "memory %d: content must not be empty", i)
	}

	// Content should match decision text (no redaction needed for these inputs).
	assert.Equal(t, decisions[0].Text, mems[0].Content)
	assert.Equal(t, decisions[1].Text, mems[1].Content)
}

func TestBuildMemories_PrivacyRedaction(t *testing.T) {
	// API key embedded in a decision text must be redacted before storage.
	decisions := []ExtractedDecision{
		{Text: "decided to use SECRET_KEY=abc123supersecretvalue123 for auth", Pattern: "decided", Position: 0},
	}

	mems := BuildMemories(decisions, "sess-redact", "proj")
	require.Len(t, mems, 1)

	assert.NotContains(t, mems[0].Content, "abc123supersecretvalue123",
		"raw secret must not appear in stored content")
	assert.Contains(t, mems[0].Content, "[REDACTED",
		"stored content must contain a redaction marker")
}

func TestBuildMemories_SessionTag(t *testing.T) {
	decisions := []ExtractedDecision{
		{Text: "going forward, use Redis for caching", Pattern: "going forward", Position: 0},
	}

	mems := BuildMemories(decisions, "my-session-42", "proj")
	require.Len(t, mems, 1)

	found := false
	for _, tag := range mems[0].Tags {
		if strings.HasPrefix(tag, "session:") {
			assert.Equal(t, "session:my-session-42", tag)
			found = true
		}
	}
	assert.True(t, found, "session tag must be present")
}
