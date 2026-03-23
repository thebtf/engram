package extract

// ChunkExtractionSystemPrompt is the category-based extraction prompt for session chunks.
// Used by both backfill (multi-exchange chunks) and live SDK processor (single-exchange).
const ChunkExtractionSystemPrompt = `You are a coding session analyst. Extract ONLY observations matching these categories. If none match, output <no_observations_found/>.

CATEGORY 1 — DECISION: Agent or user explicitly chose between alternatives.
CATEGORY 2 — CORRECTION: User told the agent it was wrong.
CATEGORY 3 — DEBUGGING ARC: Error appeared, was investigated, and resolved.
CATEGORY 4 — GOTCHA: Something behaved unexpectedly.
CATEGORY 5 — PATTERN: A reusable approach that worked well.
CATEGORY 6 — USER_BEHAVIOR: User corrected agent's approach or revealed a workflow preference. Extract as TRIGGER/RULE/REASON.

DO NOT EXTRACT: File reads without decisions, routine commits, tool invocations without meaningful output, status checks, version bumps, generic descriptions of what code does.

RULES:
- EXACTLY 0, 1, or 2 observations. NEVER more than 2.
- Maximum 150 words per narrative.
- Do NOT include any text before or after the XML. Output ONLY the XML.

EXAMPLES:

Example 1 (user_behavior):
Transcript: "USER: you have tavily for this, FYI"
<observations><observation>
<category>user_behavior</category><type>decision</type>
<title>Rule: Use Tavily for external documentation research</title>
<narrative>TRIGGER: When studying external library documentation. RULE: Use Tavily search instead of manual WebFetch. REASON: Manual browsing wastes 10+ tool calls.</narrative>
<concepts><concept>user-preference</concept><concept>tools</concept></concepts>
</observation></observations>

Example 2 (no_observations_found):
Transcript: "ASSISTANT: [tool: Read] Reading config.go. ASSISTANT: [tool: Read] Reading service.go."
<no_observations_found/>

Example 3 (debugging):
Transcript: "Build failed: cannot insert multiple commands. ASSISTANT: Split into separate Exec calls."
<observations><observation>
<category>debugging</category><type>bugfix</type>
<title>PostgreSQL rejects multi-command prepared statements</title>
<narrative>Migration with CREATE TABLE + CREATE INDEX in single Exec failed. Root cause: PostgreSQL prepared statements accept only one command. Fix: split into separate Exec calls per statement.</narrative>
<concepts><concept>gotcha</concept><concept>postgresql</concept></concepts>
</observation></observations>`

// ChunkExtractionUserTemplate is the user prompt template for chunk extraction.
// Format args: projectPath, exchangeCount, chunkInfo, transcript
const ChunkExtractionUserTemplate = `Analyze this coding session segment and extract observations.

<metadata>
  <project>%s</project>
  <exchanges>%d</exchanges>
  <chunk_info>%s</chunk_info>
</metadata>

<transcript>
%s
</transcript>

If nothing matches the categories: <no_observations_found/>

Otherwise, output ONLY this XML:
<observations>
  <observation>
    <category>decision|correction|debugging|gotcha|pattern|user_behavior</category>
    <type>decision|bugfix|feature|refactor|discovery|change</type>
    <title>Clear searchable title (max 60 chars)</title>
    <narrative>Context → Problem → Action → Outcome. Max 150 words.</narrative>
    <concepts>
      <concept>tag-name</concept>
    </concepts>
    <files>
      <file>relative/path/to/file</file>
    </files>
  </observation>
</observations>`

// RetrospectiveSystemPrompt is the session-level retrospective prompt.
// Processes full session holistically after chunk extraction.
const RetrospectiveSystemPrompt = `You are summarizing a completed coding session. You receive session metadata, observations already extracted from chunks, and the opening/closing exchanges for context.

Produce TWO outputs in a single XML response:

OUTPUT 1 — SESSION SUMMARY: What happened in this session overall.
OUTPUT 2 — SESSION-LEVEL OBSERVATIONS (0-2): Insights only visible at session level, not in individual chunks:
- Did the session change direction mid-way?
- Were there cascading failures (fix A broke B)?
- Was a decision from early in the session reversed later?
- Did the user express frustration or repeat corrections?

RULES:
- Summary fields: max 2 sentences each
- Session observations: max 150 words narrative, max 2
- If nothing to add beyond chunk observations, use empty <session_observations/>
- Output ONLY valid XML`

// RetrospectiveUserTemplate is the user prompt template for session retrospective.
// Format args: project, durationMin, totalExchanges, userMessages, commits, filesModified, alreadyExtracted, sessionOpening, sessionClosing
const RetrospectiveUserTemplate = `Summarize this completed coding session.

<session_metadata>
  <project>%s</project>
  <duration_minutes>%d</duration_minutes>
  <total_exchanges>%d</total_exchanges>
  <user_messages>%d</user_messages>
  <commits>%d</commits>
  <files_modified>%d</files_modified>
</session_metadata>

<already_extracted_observations>
%s
</already_extracted_observations>

<session_opening>
%s
</session_opening>

<session_closing>
%s
</session_closing>

Output ONLY this XML:
<session_retrospective>
  <summary>
    <request>What the user asked for (1 sentence)</request>
    <completed>What was actually done (1-2 sentences)</completed>
    <learned>Key technical insight (1 sentence, or "none")</learned>
    <next_steps>What remains (1 sentence, or "complete")</next_steps>
    <outcome>shipped|partial|blocked|investigation_only</outcome>
  </summary>
  <session_observations>
    <observation>
      <category>correction|debugging|gotcha|decision|user_behavior</category>
      <type>decision|bugfix|discovery</type>
      <title>...</title>
      <narrative>Max 150 words</narrative>
      <concepts><concept>...</concept></concepts>
    </observation>
  </session_observations>
</session_retrospective>`

// FeedbackImportSystemPrompt converts feedback_*.md files into structured TRIGGER→RULE→REASON observations.
const FeedbackImportSystemPrompt = `You are converting a user preference note into a structured behavioral rule.

The input is a markdown file describing a preference or correction the user gave to an AI agent. Convert it into a structured observation with TRIGGER → RULE → REASON format.

CRITICAL: The TRIGGER must be specific enough that it would NOT fire in a project where the opposite behavior is correct.
BAD: "Never SSH to server" (too broad)
GOOD: "When checking engram server logs → use HTTP API endpoint, not SSH"

Output ONLY this XML:
<observation>
  <category>user_behavior</category>
  <type>decision</type>
  <title>Rule: [specific action in specific context]</title>
  <narrative>
TRIGGER: When [specific situation]
RULE: [what the user wants]
REASON: [why, from the note]
  </narrative>
  <concepts>
    <concept>user-preference</concept>
    <concept>[one of: tools, process, quality, communication]</concept>
  </concepts>
</observation>`
