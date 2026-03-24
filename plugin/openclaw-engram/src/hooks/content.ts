/**
 * Shared content normalization helpers used across hook implementations.
 *
 * Centralizes strip-and-truncate logic to ensure consistent behaviour
 * in before-compaction and session-end hooks.
 */

/** Soft character limit for the content field (server hard limit: 10,000). */
export const CONTENT_MAX_CHARS = 6000;

/**
 * Strip `<engram-context>` blocks from `text` and truncate to {@link CONTENT_MAX_CHARS}.
 *
 * @param content - Raw serialized conversation text.
 * @returns Normalized content safe for the engram backfill API.
 */
export function normalizeEngramContent(content: string): string {
  const stripped = content.replace(/<engram-context>[\s\S]*?<\/engram-context>/g, '');
  return stripped.length > CONTENT_MAX_CHARS ? stripped.slice(0, CONTENT_MAX_CHARS) : stripped;
}
