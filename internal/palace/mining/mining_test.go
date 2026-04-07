package mining

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Normalize
// ---------------------------------------------------------------------------

func TestNormalize_ANSI(t *testing.T) {
	input := "\x1b[31mHello\x1b[0m World"
	got, _ := Normalize(input)
	assert.Equal(t, "Hello World", got)
	assert.NotContains(t, got, "\x1b")
}

func TestNormalize_DetectClaude(t *testing.T) {
	input := "Human: what is 2+2?\nAssistant: It is 4."
	_, format := Normalize(input)
	assert.Equal(t, FormatClaude, format)
}

func TestNormalize_DetectChatGPT(t *testing.T) {
	input := "User: explain recursion\nChatGPT: Recursion is a function calling itself."
	_, format := Normalize(input)
	assert.Equal(t, FormatChatGPT, format)
}

func TestNormalize_CollapseBlankLines(t *testing.T) {
	input := "line1\n\n\n\n\nline2"
	got, _ := Normalize(input)
	assert.NotContains(t, got, "\n\n\n")
	assert.Contains(t, got, "line1")
	assert.Contains(t, got, "line2")
}

func TestNormalize_WindowsLineEndings(t *testing.T) {
	input := "Hello\r\nWorld"
	got, _ := Normalize(input)
	assert.NotContains(t, got, "\r")
	assert.Contains(t, got, "Hello\nWorld")
}

// ---------------------------------------------------------------------------
// ChunkText
// ---------------------------------------------------------------------------

func TestChunkText_Basic(t *testing.T) {
	// Build a text larger than 800 chars.
	text := strings.Repeat("This is a sentence about something interesting. ", 40)
	chunks := ChunkText(text, 800, 100)
	require.NotEmpty(t, chunks)
	for _, ch := range chunks {
		assert.LessOrEqual(t, len(ch.Text), 900, "chunk should not hugely overshoot maxSize")
	}
}

func TestChunkText_PreferNewlines(t *testing.T) {
	// Two paragraphs of ~400 chars each separated by \n\n — total ~820 chars.
	para := strings.Repeat("word ", 80) // ~400 chars
	text := para + "\n\n" + para
	chunks := ChunkText(text, 800, 0)
	require.NotEmpty(t, chunks)
	// First chunk should not cut mid-word.
	first := chunks[0].Text
	assert.False(t, strings.HasSuffix(strings.TrimRight(first, "\n"), "wor"),
		"chunk should not cut mid-word")
}

func TestChunkText_IndexesAreSequential(t *testing.T) {
	text := strings.Repeat("Sentence number one two three. ", 100)
	chunks := ChunkText(text, 800, 100)
	for i, ch := range chunks {
		assert.Equal(t, i, ch.Index)
	}
}

// ---------------------------------------------------------------------------
// FilterCode
// ---------------------------------------------------------------------------

func TestFilterCode_RemovesFenced(t *testing.T) {
	input := "Some prose here.\n```go\nfmt.Println(\"hello\")\n```\nMore prose."
	got := FilterCode(input)
	assert.NotContains(t, got, "fmt.Println")
	assert.Contains(t, got, "Some prose here.")
	assert.Contains(t, got, "More prose.")
}

func TestFilterCode_PreservesProse(t *testing.T) {
	input := "We decided to use PostgreSQL for the database.\nIt provides good performance."
	got := FilterCode(input)
	assert.Contains(t, got, "decided to use PostgreSQL")
	assert.Contains(t, got, "good performance")
}

func TestFilterCode_RemovesShellPrompts(t *testing.T) {
	input := "Run the server:\n$ go run main.go\nThen check the output."
	got := FilterCode(input)
	assert.NotContains(t, got, "$ go run")
	assert.Contains(t, got, "Run the server:")
}

// ---------------------------------------------------------------------------
// ExtractExchanges
// ---------------------------------------------------------------------------

func TestExtractExchanges_Claude(t *testing.T) {
	input := "Human: What is Go?\nAssistant: Go is a compiled language.\nHuman: Why use it?\nAssistant: It is fast and simple."
	exchanges := ExtractExchanges(input, FormatClaude)
	require.Len(t, exchanges, 2)
	assert.Contains(t, exchanges[0].UserTurn, "What is Go")
	assert.Contains(t, exchanges[0].AITurn, "compiled language")
	assert.Equal(t, 0, exchanges[0].Index)
	assert.Equal(t, 1, exchanges[1].Index)
}

func TestExtractExchanges_Generic(t *testing.T) {
	input := "First user paragraph.\n\nFirst AI response paragraph.\n\nSecond user paragraph.\n\nSecond AI response."
	exchanges := ExtractExchanges(input, FormatGeneric)
	require.Len(t, exchanges, 2)
	assert.Contains(t, exchanges[0].UserTurn, "First user")
	assert.Contains(t, exchanges[0].AITurn, "First AI")
}

// ---------------------------------------------------------------------------
// Classify
// ---------------------------------------------------------------------------

func TestClassify_Decision(t *testing.T) {
	text := "We decided to use Redis as our caching layer and chose it over Memcached."
	memType, conf := Classify(text)
	assert.Equal(t, "decision", memType)
	assert.GreaterOrEqual(t, conf, classifyThreshold)
}

func TestClassify_BelowThreshold(t *testing.T) {
	text := "The sky is blue and the weather is nice today."
	memType, conf := Classify(text)
	assert.Equal(t, "general", memType)
	assert.Equal(t, 0.0, conf)
}

func TestClassify_Problem(t *testing.T) {
	text := "There is a bug in the authentication module causing a crash on login."
	memType, conf := Classify(text)
	assert.Equal(t, "problem", memType)
	assert.GreaterOrEqual(t, conf, classifyThreshold)
}

func TestClassify_Milestone(t *testing.T) {
	text := "We finally shipped the feature and launched it to production."
	memType, conf := Classify(text)
	assert.Equal(t, "milestone", memType)
	assert.GreaterOrEqual(t, conf, classifyThreshold)
}

// ---------------------------------------------------------------------------
// DetectConcepts
// ---------------------------------------------------------------------------

func TestDetectConcepts_Technical(t *testing.T) {
	text := "We deploy the api to the docker server and connect it to the database."
	concepts := DetectConcepts(text, DefaultBuckets())
	require.NotEmpty(t, concepts)
	assert.Equal(t, "technical", concepts[0], "technical should be the top bucket")
}

func TestDetectConcepts_ReturnsAtMostTwo(t *testing.T) {
	text := "The api deploy to docker. We decided to use this pattern in the service layer component."
	concepts := DetectConcepts(text, DefaultBuckets())
	assert.LessOrEqual(t, len(concepts), 2)
}

func TestDetectConcepts_EmptyOnNoMatch(t *testing.T) {
	text := "The quick brown fox jumps over the lazy dog."
	concepts := DetectConcepts(text, DefaultBuckets())
	assert.Empty(t, concepts)
}

// ---------------------------------------------------------------------------
// Mine (full pipeline)
// ---------------------------------------------------------------------------

func TestMine_FullPipeline(t *testing.T) {
	input := "Human: How should we handle authentication?\n" +
		"Assistant: We decided to use JWT tokens because they are stateless. " +
		"We chose this approach after evaluating OAuth and session cookies. " +
		"JWT fits our architecture pattern as a service layer component."

	results, err := Mine(input, MineOptions{Project: "test"})
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// All results must have non-empty hashes.
	for _, r := range results {
		assert.NotEmpty(t, r.SourceHash, "each result must have a SHA-256 hash")
		assert.NotEmpty(t, r.Type)
		assert.Greater(t, r.Confidence, 0.0)
	}
}

func TestMine_Idempotent(t *testing.T) {
	input := "Human: Why did you choose Go?\n" +
		"Assistant: We chose Go because it compiles fast and has excellent concurrency support. " +
		"We decided on Go after evaluating Rust, Python, and Node. " +
		"It fits the service architecture we prefer."

	results1, err := Mine(input, MineOptions{Project: "test"})
	require.NoError(t, err)

	results2, err := Mine(input, MineOptions{Project: "test"})
	require.NoError(t, err)

	require.Equal(t, len(results1), len(results2), "same input must yield same number of results")
	for i := range results1 {
		assert.Equal(t, results1[i].SourceHash, results2[i].SourceHash,
			"hashes must be identical across runs")
	}
}

func TestMine_MaxResults(t *testing.T) {
	// Generate enough content to produce several results.
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		sb.WriteString("Human: Tell me about issue number " + string(rune('0'+i)) + ".\n")
		sb.WriteString("Assistant: We decided to fix bug number " + string(rune('0'+i)) +
			". The error was causing a crash in the system. We chose a straightforward fix.\n\n")
	}

	results, err := Mine(sb.String(), MineOptions{Project: "test", MaxResults: 3})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(results), 3)
}

func TestMine_DeduplicatesSameHash(t *testing.T) {
	// Duplicate the same exchange — second occurrence should be deduped.
	turn := "Human: Why?\nAssistant: We decided to use PostgreSQL because it is reliable and battle-tested."
	input := turn + "\n\n" + turn

	results, err := Mine(input, MineOptions{Project: "test"})
	require.NoError(t, err)

	// Collect all hashes and ensure uniqueness.
	seen := make(map[string]int)
	for _, r := range results {
		seen[r.SourceHash]++
	}
	for hash, count := range seen {
		assert.Equal(t, 1, count, "hash %s appeared %d times — dedup failed", hash, count)
	}
}
