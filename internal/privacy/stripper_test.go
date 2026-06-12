package privacy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// -----------------------------------------------------------------------------
// Contract: StripPrivateTags
// - Removes all <private>...</private> spans including their delimiters.
// - The regex is non-greedy and multiline ((?s) flag), so it matches the
//   shortest possible content between the first opening and first closing tag.
// - Unmatched opening or closing tags are left untouched.
// - Text outside all tags is preserved verbatim.
// -----------------------------------------------------------------------------

func TestStripPrivateTags_NoTags(t *testing.T) {
	assert.Equal(t, "Hello world", StripPrivateTags("Hello world"))
}

func TestStripPrivateTags_EmptyInput(t *testing.T) {
	assert.Equal(t, "", StripPrivateTags(""))
}

func TestStripPrivateTags_SingleTag(t *testing.T) {
	assert.Equal(t, "Hello  world", StripPrivateTags("Hello <private>secret</private> world"))
}

func TestStripPrivateTags_MultipleTagsOnOneLine(t *testing.T) {
	result := StripPrivateTags("A <private>s1</private> B <private>s2</private> C")
	assert.Equal(t, "A  B  C", result)
}

func TestStripPrivateTags_MultilineContent(t *testing.T) {
	input := "Hello <private>\nmultiline\nsecret\n</private> world"
	assert.Equal(t, "Hello  world", StripPrivateTags(input))
}

func TestStripPrivateTags_EmptyPrivateTag(t *testing.T) {
	assert.Equal(t, "Hello  world", StripPrivateTags("Hello <private></private> world"))
}

func TestStripPrivateTags_EntirelyPrivate(t *testing.T) {
	assert.Equal(t, "", StripPrivateTags("<private>everything</private>"))
}

func TestStripPrivateTags_UnmatchedOpeningTag(t *testing.T) {
	// No closing tag → nothing stripped.
	assert.Equal(t, "Hello <private>unclosed", StripPrivateTags("Hello <private>unclosed"))
}

func TestStripPrivateTags_UnmatchedClosingTag(t *testing.T) {
	// No opening tag → nothing stripped.
	assert.Equal(t, "Hello </private> world", StripPrivateTags("Hello </private> world"))
}

func TestStripPrivateTags_NonGreedyMatchesFirstClose(t *testing.T) {
	// Non-greedy: first <private> matches the first </private>.
	// Content between second </private> is left.
	input := "<private>outer <private>inner</private> outer</private>"
	result := StripPrivateTags(input)
	// First match consumes "<private>outer <private>inner</private>",
	// leaving " outer</private>" intact.
	assert.Equal(t, " outer</private>", result)
}

func TestStripPrivateTags_CaseSensitive(t *testing.T) {
	// Uppercase tags must NOT be stripped.
	input := "Hello <PRIVATE>secret</PRIVATE> world"
	assert.Equal(t, input, StripPrivateTags(input))
}

func TestStripPrivateTags_UnicodeAndSpecialChars(t *testing.T) {
	assert.Equal(t, "pre  post", StripPrivateTags("pre <private>秘密 🔒 $%^&*()</private> post"))
}

func TestStripPrivateTags_VeryLongContent(t *testing.T) {
	longSecret := strings.Repeat("x", 10000)
	input := "pre <private>" + longSecret + "</private> post"
	assert.Equal(t, "pre  post", StripPrivateTags(input))
}

func TestStripPrivateTags_NonPrivateHTMLLike(t *testing.T) {
	input := "Hello <div>world</div>"
	assert.Equal(t, input, StripPrivateTags(input))
}

// -----------------------------------------------------------------------------
// Contract: StripMemoryTags
// - Removes all <engram-context>...</engram-context> spans.
// - Same non-greedy, multiline semantics as StripPrivateTags.
// - Does NOT affect <private> tags.
// -----------------------------------------------------------------------------

func TestStripMemoryTags_NoTags(t *testing.T) {
	assert.Equal(t, "Hello world", StripMemoryTags("Hello world"))
}

func TestStripMemoryTags_SingleTag(t *testing.T) {
	assert.Equal(t, "Hello  world", StripMemoryTags("Hello <engram-context>memory</engram-context> world"))
}

func TestStripMemoryTags_MultilineContent(t *testing.T) {
	input := "pre <engram-context>\nline1\nline2\n</engram-context> post"
	assert.Equal(t, "pre  post", StripMemoryTags(input))
}

func TestStripMemoryTags_EntirelyMemory(t *testing.T) {
	assert.Equal(t, "", StripMemoryTags("<engram-context>all memory</engram-context>"))
}

func TestStripMemoryTags_MultipleOccurrences(t *testing.T) {
	input := "A <engram-context>m1</engram-context> B <engram-context>m2</engram-context> C"
	assert.Equal(t, "A  B  C", StripMemoryTags(input))
}

func TestStripMemoryTags_DoesNotAffectPrivateTags(t *testing.T) {
	// <private> tags must survive StripMemoryTags.
	input := "x <private>secret</private> y <engram-context>mem</engram-context> z"
	result := StripMemoryTags(input)
	assert.Equal(t, "x <private>secret</private> y  z", result)
}

// -----------------------------------------------------------------------------
// Contract: StripAllTags
// - Applies StripPrivateTags then StripMemoryTags in order.
// - Both tag types are removed; surrounding text is preserved.
// -----------------------------------------------------------------------------

func TestStripAllTags_NoTags(t *testing.T) {
	assert.Equal(t, "Hello world", StripAllTags("Hello world"))
}

func TestStripAllTags_BothTagTypes(t *testing.T) {
	input := "A <private>secret</private> B <engram-context>mem</engram-context> C"
	assert.Equal(t, "A  B  C", StripAllTags(input))
}

func TestStripAllTags_OnlyPrivate(t *testing.T) {
	assert.Equal(t, "A  B", StripAllTags("A <private>x</private> B"))
}

func TestStripAllTags_OnlyMemory(t *testing.T) {
	assert.Equal(t, "A  B", StripAllTags("A <engram-context>x</engram-context> B"))
}

func TestStripAllTags_Interleaved(t *testing.T) {
	// Tags of different types interleaved must both be stripped.
	input := "A <private>P</private> C <engram-context>M</engram-context> E"
	assert.Equal(t, "A  C  E", StripAllTags(input))
}

func TestStripAllTags_Empty(t *testing.T) {
	assert.Equal(t, "", StripAllTags(""))
}

// -----------------------------------------------------------------------------
// Contract: IsEntirelyPrivate
// - Returns true when stripping <private> tags leaves only whitespace (or empty).
// - Returns false when any non-whitespace survives after stripping.
// -----------------------------------------------------------------------------

func TestIsEntirelyPrivate_PlainText(t *testing.T) {
	assert.False(t, IsEntirelyPrivate("Hello world"))
}

func TestIsEntirelyPrivate_EntirelyPrivate(t *testing.T) {
	assert.True(t, IsEntirelyPrivate("<private>secret</private>"))
}

func TestIsEntirelyPrivate_EntirelyPrivateWithOuterWhitespace(t *testing.T) {
	assert.True(t, IsEntirelyPrivate("  <private>secret</private>  "))
}

func TestIsEntirelyPrivate_PartiallyPrivate(t *testing.T) {
	assert.False(t, IsEntirelyPrivate("Hello <private>secret</private>"))
}

func TestIsEntirelyPrivate_MultiplePrivateCoversAll(t *testing.T) {
	assert.True(t, IsEntirelyPrivate("<private>a</private><private>b</private>"))
}

func TestIsEntirelyPrivate_EmptyString(t *testing.T) {
	// Empty string → after stripping, TrimSpace("") == "" → true.
	assert.True(t, IsEntirelyPrivate(""))
}

func TestIsEntirelyPrivate_WhitespaceOnly(t *testing.T) {
	// Whitespace-only → TrimSpace → "" → true.
	assert.True(t, IsEntirelyPrivate("   \t\n"))
}

func TestIsEntirelyPrivate_MemoryTagsNotCountedAsPrivate(t *testing.T) {
	// IsEntirelyPrivate only strips <private> tags, not <engram-context> tags.
	// So <engram-context> content is visible text → not entirely private.
	assert.False(t, IsEntirelyPrivate("<engram-context>mem</engram-context>"))
}

// -----------------------------------------------------------------------------
// Contract: Clean
// - Applies StripAllTags (both tag types) then TrimSpace.
// - The result never starts or ends with ASCII whitespace.
// - An all-private input produces "".
// -----------------------------------------------------------------------------

func TestClean_NoTagsNoWhitespace(t *testing.T) {
	assert.Equal(t, "Hello world", Clean("Hello world"))
}

func TestClean_StripsPrivateAndTrims(t *testing.T) {
	assert.Equal(t, "Hello  world", Clean("  Hello <private>secret</private> world  "))
}

func TestClean_StripsMemoryAndTrims(t *testing.T) {
	assert.Equal(t, "Hello  world", Clean("  Hello <engram-context>memory</engram-context> world  "))
}

func TestClean_StripsBothAndTrims(t *testing.T) {
	input := "\n  Hello <private>secret</private> and <engram-context>memory</engram-context> world  \n"
	assert.Equal(t, "Hello  and  world", Clean(input))
}

func TestClean_EntirelyStrippedContent(t *testing.T) {
	assert.Equal(t, "", Clean("  <private>secret</private>  "))
}

func TestClean_EmptyInput(t *testing.T) {
	assert.Equal(t, "", Clean(""))
}

func TestClean_WhitespaceOnlyInput(t *testing.T) {
	assert.Equal(t, "", Clean("   \t\n"))
}

func TestClean_ResultHasNoLeadingTrailingWhitespace(t *testing.T) {
	result := Clean("  text  ")
	assert.Equal(t, "text", result)
	assert.NotEqual(t, ' ', rune(result[0]), "should not start with space")
}
