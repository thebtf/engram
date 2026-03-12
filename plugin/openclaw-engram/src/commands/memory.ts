/**
 * /memory command — shows engram server status and recent context overview.
 */

import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import type { OpenClawPluginCommandDefinition, PluginCommandContext } from '../types/openclaw.js';

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
    acceptsArgs: false,

    async handler(_ctx: PluginCommandContext) {
      if (!client.isAvailable()) {
        const cooldown = client.availability.remainingCooldownMs();
        return {
          text:
            `engram status: UNAVAILABLE\n` +
            `Server: ${config.url}\n` +
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

      return { text: lines.join('\n') };
    },
  };
}
