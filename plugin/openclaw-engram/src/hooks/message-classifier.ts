/**
 * message-classifier — content-based message classification for hook filtering.
 *
 * Provides an allowlist/blocklist approach to classify incoming prompts so that
 * hooks can skip low-value messages (heartbeats, SDK metadata) without duplicating
 * detection logic across multiple hook files.
 */

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

/** Semantic category of an incoming message. */
export type MessageCategory = 'user_prompt' | 'heartbeat' | 'system' | 'agent_internal';

// ---------------------------------------------------------------------------
// Pattern lists
// ---------------------------------------------------------------------------

/**
 * Content substrings that positively identify a heartbeat message.
 * All comparisons are case-sensitive to avoid false positives on common words.
 */
const HEARTBEAT_CONTENT_PATTERNS: readonly string[] = [
  'heartbeat.md',
  'heartbeat_ok',
  'HEARTBEAT',
  'Read HEARTBEAT',
  'check heartbeat',
];

/**
 * Tool names that operate on files. Used together with content inspection:
 * a Read/Write tool call is only a heartbeat if its target IS the heartbeat file.
 */
const FILE_TOOLS: ReadonlySet<string> = new Set(['Read', 'Write']);

/**
 * SDK-injected metadata patterns that should not be treated as real user prompts.
 * These appear at the beginning of forwarded agent-to-agent messages or tool events.
 */
const SYSTEM_CONTENT_PATTERNS: readonly string[] = [
  'Conversation info (untrusted metadata)',
  'Sender (untrusted metadata)',
  '<task-notification>',
  '<command-name>',
];

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Classify an incoming message based on its content and optional tool name.
 *
 * Decision order:
 *   1. Heartbeat  — always block (workspace health check noise)
 *   2. System     — always block (SDK metadata, not a user query)
 *   3. Default    — treat as user_prompt (allow)
 *
 * @param prompt   - The raw prompt or message text to classify.
 * @param toolName - Optional tool name associated with the message (after_tool_call hook).
 * @returns        The most specific category that matches, or 'user_prompt' by default.
 */
export function classifyMessage(prompt: string, toolName?: string): MessageCategory {
  if (isHeartbeat(prompt, toolName)) return 'heartbeat';
  if (isSystemMessage(prompt)) return 'system';
  return 'user_prompt';
}

// ---------------------------------------------------------------------------
// Internal classifiers
// ---------------------------------------------------------------------------

/**
 * Returns true if the prompt is a workspace heartbeat / keep-alive check.
 *
 * Two detection strategies:
 *   a) Content match — prompt contains a known heartbeat substring.
 *   b) Tool match — a file-operating tool (Read/Write) targets the HEARTBEAT file
 *      (handles cases where the prompt text itself is sparse).
 */
function isHeartbeat(prompt: string, toolName?: string): boolean {
  // Strategy (a): content substring match
  for (const pattern of HEARTBEAT_CONTENT_PATTERNS) {
    if (prompt.includes(pattern)) return true;
  }

  // Strategy (b): file tool whose target is the heartbeat file
  if (toolName && FILE_TOOLS.has(toolName) && prompt.includes('HEARTBEAT')) {
    return true;
  }

  return false;
}

/**
 * Returns true if the prompt is an SDK-injected metadata block rather than a
 * real user query. These appear in agent-to-agent forwarded messages.
 */
function isSystemMessage(prompt: string): boolean {
  return SYSTEM_CONTENT_PATTERNS.some((pattern) => prompt.includes(pattern));
}
