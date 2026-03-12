/**
 * /memory command — shows engram server status and recent context overview.
 */

import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import type { OpenClawPluginCommandDefinition, CommandContext, CommandResult } from '../types/openclaw.js';

/**
 * Build the /memory command definition.
 */
export function buildMemoryCommand(
  client: EngramRestClient,
  config: PluginConfig,
): OpenClawPluginCommandDefinition {
  return {
    name: 'memory',
    description: 'Show engram memory server status and recent observations',
    usage: '/memory',

    async execute(
      _args: string[],
      context: CommandContext,
    ): Promise<CommandResult> {
      return runMemoryCommand(context, client, config);
    },
  };
}

async function runMemoryCommand(
  context: CommandContext,
  client: EngramRestClient,
  config: PluginConfig,
): Promise<CommandResult> {
  const identity = resolveIdentity(context.agentId ?? '', context.workspaceDir);
  const project = config.project ?? identity.projectId;

  if (!client.isAvailable()) {
    const cooldown = client.availability.remainingCooldownMs();
    return {
      output:
        `engram status: UNAVAILABLE\n` +
        `Server: ${config.url}\n` +
        `Project: ${project}\n` +
        (cooldown > 0 ? `Retry in: ${Math.ceil(cooldown / 1000)}s\n` : ''),
    };
  }

  const [health, selfCheck] = await Promise.all([
    client.health(),
    client.selfCheck(),
  ]);

  const lines: string[] = [];
  lines.push(`engram status: ${health?.status ?? 'UNKNOWN'}`);
  lines.push(`Server: ${config.url}`);
  if (health?.version) lines.push(`Version: ${health.version}`);
  lines.push(`Project: ${project}`);
  lines.push(`Agent ID: ${context.agentId ?? ''}`);
  if (context.workspaceDir) lines.push(`Workspace: ${context.workspaceDir}`);

  if (selfCheck?.components) {
    lines.push('\nComponents:');
    for (const [name, comp] of Object.entries(selfCheck.components)) {
      const icon = comp.status === 'ok' ? 'OK' : 'WARN';
      lines.push(`  [${icon}] ${name}${comp.message ? ': ' + comp.message : ''}`);
    }
  }

  lines.push(`\nConfig:`);
  lines.push(`  Token budget: ${config.tokenBudget} tokens`);
  lines.push(`  Context limit: ${config.contextLimit} observations/turn`);
  lines.push(`  Session context limit: ${config.sessionContextLimit} observations`);
  lines.push(`  Auto-extract: ${config.autoExtract ? 'enabled' : 'disabled'}`);

  return { output: lines.join('\n') };
}
