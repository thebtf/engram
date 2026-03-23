package extract

// ChunkExtractionSystemPrompt is the category-based extraction prompt for session chunks.
// Used by both backfill (multi-exchange chunks) and live SDK processor (single-exchange).
const ChunkExtractionSystemPrompt = `You are a coding session analyst. Read the transcript segment and extract ONLY observations matching these categories. If none match, output <no_observations_found/>.

CATEGORY 1 — DECISION: Agent or user explicitly chose between alternatives.
Look for: "chose X over Y", "decided to", "instead of", "because", "tradeoff"
Extract: What was decided, what was the alternative, why.

CATEGORY 2 — CORRECTION: User told the agent it was wrong.
Look for: User disagreeing, repeating instructions with emphasis, interrupting with a different approach, rhetorical questions revealing wrong assumptions.
Extract: What the agent did wrong, what the correct approach is.

CATEGORY 3 — DEBUGGING ARC: Error appeared, was investigated, and resolved.
Look for: Error message → investigation → hypothesis → fix → verification
Extract: Error signature, root cause, fix applied. Skip trivial typo fixes.

CATEGORY 4 — GOTCHA: Something behaved unexpectedly, surprising even the agent.
Look for: "unexpected", "turns out", "actually", contradictions between expected and actual behavior.
Extract: What was expected vs what actually happened, and why.

CATEGORY 5 — PATTERN: A reusable approach that worked well.
Look for: Repeated structure across files, explicit "always do X when Y" statements.
Extract: The pattern, when to apply it, example.

CATEGORY 6 — USER_BEHAVIOR: User corrected agent's approach or revealed a workflow preference.
This produces a behavioral RULE for future sessions — the highest-value extraction.
Look for: User telling agent to use a different tool/approach, user repeating instructions with frustration, user rejecting a proposal and explaining the correct way.
Extract as: TRIGGER (specific situation) → RULE (what user wants) → REASON (why).
The trigger must be specific enough to not fire in contexts where the opposite is correct.

DO NOT EXTRACT:
- File reads without decisions
- Routine commits, pushes, PR creation mechanics
- Tool invocations without meaningful output
- Configuration obvious from documentation
- Status checks, health checks, version bumps
- Generic "task completed" statements

RULES:
- Maximum 2 observations per chunk
- Maximum 150 words per narrative
- Output ONLY valid XML, no markdown, no preamble
- Use <concept> tags inside <concepts>, no other tag names`

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
