/**
 * Shared content normalization helpers used across hook implementations.
 *
 * Centralizes strip-and-truncate logic to ensure consistent behaviour
 * in before-compaction and agent-end hooks.
 */

import { classifyMessage } from './message-classifier.js';

/** Soft character limit for the content field (server hard limit: 10,000). */
export const CONTENT_MAX_CHARS = 6000;

/**
 * Strip `<engram-context>` blocks from `text` and truncate to {@link CONTENT_MAX_CHARS}.
 *
 * @param content - Raw serialized conversation text.
 * @returns Normalized content safe for the engram backfill API.
 */
export function normalizeEngramContent(content: string): string {
  const stripped = content
    .replace(/<(engram-context|engram-behavioral-rules|engram-rule-router)>[\s\S]*?<\/\1>/g, '')
    .replace(/<private>[\s\S]*?<\/private>/gi, '');
  return stripped.length > CONTENT_MAX_CHARS ? stripped.slice(-CONTENT_MAX_CHARS) : stripped;
}

/** Read only visible user/assistant text, never tool payloads, images or reasoning. */
export function conversationText(message: unknown): string {
  if (!message || typeof message !== 'object' || !('role' in message) || !('content' in message)) return '';
  if (message.role !== 'user' && message.role !== 'assistant') return '';
  if (typeof message.content === 'string') return message.content;
  if (!Array.isArray(message.content)) return '';
  return message.content.flatMap((block: unknown) =>
    block && typeof block === 'object' && 'type' in block && block.type === 'text' && 'text' in block && typeof block.text === 'string'
      ? [block.text] : [],
  ).join('\n');
}

export function serializeConversation(messages: unknown[]): string {
  const visible = messages.flatMap(message => {
    if (!message || typeof message !== 'object' || !('role' in message)) return [];
    const text = normalizeEngramContent(conversationText(message));
    if (!text.trim() || classifyMessage(text) !== 'user_prompt') return [];
    const role = message.role;
    return [`[${role}]: ${text}`];
  });
  // Keep the tail: the final assistant reply must survive the existing character cap.
  return visible.slice(-20).join('\n\n').slice(-CONTENT_MAX_CHARS);
}
