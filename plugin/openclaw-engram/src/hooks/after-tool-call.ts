/**
 * after_tool_call hook — self-learning via tool event ingestion.
 *
 * Every tool call is sent to engram's /api/events/ingest endpoint so that
 * the server can extract patterns and update its observation store over time.
 * This is fire-and-forget: the hook never blocks on a response.
 */

import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import type { AfterToolCallEvent } from '../types/openclaw.js';

const TOOL_INPUT_MAX_CHARS = 500;
const TOOL_RESULT_MAX_CHARS = 500;

/**
 * Handle the after_tool_call hook.
 *
 * @param event  - The after_tool_call event from OpenClaw.
 * @param client - Shared engram REST client.
 * @param config - Resolved plugin config.
 */
export function handleAfterToolCall(
  event: AfterToolCallEvent,
  client: EngramRestClient,
  config: PluginConfig,
): void {
  if (!client.isAvailable()) return;
  if (!config.autoExtract) return;

  const agentId = event.agentId ?? '';
  const identity = resolveIdentity(agentId, event.workspaceDir);
  const project = config.project ?? identity.projectId;

  const toolInput = truncate(JSON.stringify(event.toolInput ?? ''), TOOL_INPUT_MAX_CHARS);
  const toolResult = truncate(JSON.stringify(event.toolResult ?? ''), TOOL_RESULT_MAX_CHARS);

  // Fire-and-forget — do not await
  void client.ingestEvent({
    session_id: agentId,
    project,
    tool_name: event.toolName ?? 'unknown',
    tool_input: toolInput,
    tool_result: toolResult,
  });
}

function truncate(value: string, maxChars: number): string {
  if (value.length <= maxChars) return value;
  return value.slice(0, maxChars);
}
