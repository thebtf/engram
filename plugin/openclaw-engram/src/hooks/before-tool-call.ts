/**
 * before_tool_call hook — inject file-context observations before Write/Edit tools.
 *
 * Matches CC PreToolUse behavior: before an agent modifies a file, inject
 * relevant observations so it doesn't repeat past mistakes or miss known patterns.
 *
 * Non-blocking: 500ms timeout, failures swallowed (Constitution Principle 3).
 */

import type { EngramRestClient, Observation } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import type { BaseHookEvent, PluginHookContext } from '../types/openclaw.js';

/** Tool name patterns that modify files. */
const FILE_MODIFY_TOOLS = ['write', 'edit', 'create_file', 'replace', 'patch'];

interface ToolCallEvent extends BaseHookEvent {
  tool_name?: string;
  tool_input?: Record<string, unknown>;
}

function extractFilePath(toolInput: Record<string, unknown>): string | null {
  // Common parameter names for file paths across tool implementations
  for (const key of ['file_path', 'path', 'filePath', 'file', 'filename']) {
    const val = toolInput[key];
    if (typeof val === 'string' && val.length > 0) return val;
  }
  return null;
}

function formatFileContext(file: string, observations: Observation[]): string {
  if (observations.length === 0) return '';

  let out = `<file-context>\n# Known Context for ${file}\n`;
  out += `Found ${observations.length} relevant observation(s).\n\n`;
  for (const obs of observations) {
    const typeLabel = (obs.type ?? 'observation').toUpperCase();
    out += `## [${typeLabel}] ${obs.title ?? 'Untitled'}\n`;
    if (obs.narrative) out += `${obs.narrative}\n`;
    out += '\n';
  }
  out += '</file-context>';
  return out;
}

/**
 * Handle the before_tool_call hook.
 *
 * Detects file-modifying tools (Write/Edit), fetches relevant observations
 * from engram, and injects them as system context.
 */
export async function handleBeforeToolCall(
  event: BaseHookEvent,
  ctx: PluginHookContext,
  client: EngramRestClient,
  config: PluginConfig,
): Promise<{ appendSystemContext?: string } | void> {
  try {
    if (!client.isAvailable()) return;

    const toolEvent = event as ToolCallEvent;
    const toolName = (toolEvent.tool_name ?? '').toLowerCase();

    // Only trigger for file-modifying tools
    const isFileModify = FILE_MODIFY_TOOLS.some((pattern) => toolName.includes(pattern));
    if (!isFileModify) return;

    const filePath = toolEvent.tool_input ? extractFilePath(toolEvent.tool_input) : null;
    if (!filePath) return;

    const identity = resolveIdentity(ctx.agentId ?? '', ctx.workspaceDir);
    const project = config.project ?? identity.projectId;

    // 500ms timeout — must not noticeably delay Write/Edit tools
    const observations = await client.getFileContext(filePath, project, 5, 500);
    if (observations.length === 0) return;

    const context = formatFileContext(filePath, observations);
    return { appendSystemContext: context };
  } catch {
    // Non-blocking: swallow all errors (Constitution Principle 3)
    return;
  }
}
