/** agent_end carries the final conversation; session_end carries metadata only. */

import { handleBeforeCompaction } from './before-compaction.js';
import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import type { AgentEndEvent, PluginHookContext, PluginLogger } from '../types/openclaw.js';

export async function handleAgentEnd(
  event: AgentEndEvent,
  ctx: PluginHookContext,
  client: EngramRestClient,
  config: PluginConfig,
  logger?: PluginLogger,
): Promise<void> {
  // A completed agent run is not evidence that the user's task succeeded.
  await handleBeforeCompaction(
    { messageCount: event.messages?.length ?? 0, messages: event.messages },
    ctx, client, config, logger,
  );
}
