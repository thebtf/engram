/**
 * OpenClaw Engram Plugin
 *
 * Connects OpenClaw agents to engram persistent memory via REST API.
 * Provides:
 *   - Session-level static context injection (appendSystemContext)
 *   - Per-turn dynamic context search (prependContext)
 *   - Automatic self-learning via tool event ingestion
 *   - Transcript backfill on compaction / session end
 *   - Agent tools: engram_search, engram_remember, engram_decisions,
 *                  memory_search, memory_store, memory_forget, memory_get,
 *                  memory_migrate, engram_issues
 *   - Slash commands: /memory, /remember, /migrate
 *   - CLI: openclaw memory status|search|store|migrate
 */

import type {
  OpenClawPluginDefinition,
  OpenClawPluginApi,
  OpenClawPluginToolContext,
  PluginCommandContext,
  PluginHookContext,
} from './types/openclaw.js';
import { parseConfig, getJsonSchema } from './config.js';
import { EngramRestClient } from './client.js';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { resolveIdentity } from './identity.js';

import { handleSessionStart } from './hooks/session-start.js';
import { handleBeforeAgentStart } from './hooks/before-agent-start.js';
import { handleBeforePromptBuild } from './hooks/before-prompt-build.js';
import { handleAfterToolCall } from './hooks/after-tool-call.js';
import { handleBeforeCompaction } from './hooks/before-compaction.js';
import { handleSessionEnd } from './hooks/session-end.js';
import { handleBeforeToolCall } from './hooks/before-tool-call.js';

import { createEngramSearchTool, createMemorySearchTool } from './tools/engram-search.js';
import { createEngramRememberTool, createMemoryStoreTool } from './tools/engram-remember.js';
import { createEngramDecisionsTool } from './tools/engram-decisions.js';
import { createMemoryForgetTool } from './tools/memory-forget.js';
import { createMemoryGetTool } from './tools/memory-get.js';
import { createMemoryMigrateTool } from './tools/memory-migrate.js';
import { createEngramRateTool } from './tools/engram-rate.js';
import { createEngramSuppressTool } from './tools/engram-suppress.js';
import { createEngramOutcomeTool } from './tools/engram-outcome.js';
import { createEngramFindByFileTool } from './tools/engram-find-by-file.js';
import { createEngramTimelineTool } from './tools/engram-timeline.js';
import { createEngramChangesTool, createEngramHowItWorksTool } from './tools/engram-presets.js';
import { createEngramVaultStoreTool, createEngramVaultGetTool } from './tools/engram-vault.js';
import { createEngramIssuesTool } from './tools/engram-issues.js';

import { buildMemoryCommand } from './commands/memory.js';
import { buildRememberCommand } from './commands/remember.js';
import { createFileWatcherService } from './services/file-watcher.js';

// ---------------------------------------------------------------------------
// Plugin definition
// ---------------------------------------------------------------------------

const plugin: OpenClawPluginDefinition = {
  id: 'engram',
  name: 'Engram Memory',
  description: 'Persistent shared memory via engram server',
  version: '0.2.0',
  kind: 'memory',
  configSchema: getJsonSchema(),

  register(api: OpenClawPluginApi): void {
    const config = parseConfig(api.pluginConfig ?? {});
    const client = new EngramRestClient(config);

    api.logger.info(`[engram] initializing — server: ${config.url}`);

    // ------------------------------------------------------------------
    // Hooks
    // ------------------------------------------------------------------

    api.on('session_start', (event, ctx: PluginHookContext) =>
      handleSessionStart(event, ctx, client, config, api.logger),
    );

    api.on('before_agent_start', (event, ctx: PluginHookContext) =>
      handleBeforeAgentStart(event, ctx, client, config, api.logger),
    );

    api.on('before_prompt_build', (event, ctx: PluginHookContext) =>
      handleBeforePromptBuild(event, ctx, client, config, api.logger),
    );

    api.on('after_tool_call', (event, ctx: PluginHookContext) => {
      handleAfterToolCall(event, ctx, client, config);
    });

    api.on('before_compaction', (event, ctx: PluginHookContext) => {
      handleBeforeCompaction(event, ctx, client, config, api.logger);
    });

    api.on('before_tool_call', (event, ctx: PluginHookContext) =>
      handleBeforeToolCall(event, ctx, client, config),
    );

    api.on('session_end', (event, ctx: PluginHookContext) => {
      handleSessionEnd(event, ctx, client, config, api.logger);
    });

    // ------------------------------------------------------------------
    // Tools (factory pattern)
    // ------------------------------------------------------------------

    const toolFactory = (ctx: OpenClawPluginToolContext) => [
      createEngramSearchTool(ctx, client, config),
      createMemorySearchTool(ctx, client, config),
      createEngramRememberTool(ctx, client, config),
      createMemoryStoreTool(ctx, client, config),
      createEngramDecisionsTool(ctx, client, config),
      createMemoryForgetTool(ctx, client, config),
      createMemoryGetTool(ctx, client, config, api),
      createMemoryMigrateTool(ctx, client, config, api),
      createEngramRateTool(ctx, client, config),
      createEngramSuppressTool(ctx, client, config),
      createEngramOutcomeTool(ctx, client, config),
      createEngramFindByFileTool(ctx, client, config),
      createEngramTimelineTool(ctx, client, config),
      createEngramChangesTool(ctx, client, config),
      createEngramHowItWorksTool(ctx, client, config),
      createEngramVaultStoreTool(ctx, client, config),
      createEngramVaultGetTool(ctx, client, config),
      createEngramIssuesTool(ctx, client, config),
    ];

    api.registerTool(toolFactory, {
      names: [
        'engram_search', 'memory_search',
        'engram_remember', 'memory_store',
        'engram_decisions',
        'memory_forget',
        'memory_get',
        'memory_migrate',
      ],
    });

    // ------------------------------------------------------------------
    // Commands
    // ------------------------------------------------------------------

    api.registerCommand(buildMemoryCommand(client, config));
    api.registerCommand(buildRememberCommand(client, config));

    api.registerCommand({
      name: 'migrate',
      description: 'Import local memory files (MEMORY.md, memory/**/*.md) into engram',
      acceptsArgs: true,
      async handler(ctx: PluginCommandContext) {
        const rawArgs = (ctx.args ?? '').trim();
        const parts = rawArgs.split(/\s+/).filter(Boolean);
        const dryRun = parts.includes('--dry-run');
        const force = parts.includes('--force');
        const pathArgs = parts.filter((a) => !a.startsWith('--'));
        const migratePath = pathArgs.length > 0 ? pathArgs.join(' ') : undefined;

        // Resolve workspace: slash commands (Telegram, Discord) don't carry
        // workspaceDir in PluginCommandContext. Use plugin config or default agent path.
        const workspaceDir = config.workspaceDir
          ?? join(homedir(), '.openclaw', 'workspace');
        const toolCtx: OpenClawPluginToolContext = { workspaceDir };
        const tool = createMemoryMigrateTool(toolCtx, client, config, api);
        const result = await tool.execute('cmd-migrate', { dryRun, force, path: migratePath });
        return { text: result };
      },
    });

    // ------------------------------------------------------------------
    // CLI: openclaw memory <subcommand>
    // ------------------------------------------------------------------

    api.registerCli(({ program }) => {
      const memCmd = program.command('memory').description('Engram memory operations');

      memCmd
        .command('status')
        .description('Show engram server status')
        .action(async () => {
          const health = await client.health();
          const status = health?.status ?? 'UNKNOWN';
          const version = health?.version ? ` v${health.version}` : '';
          console.log(`engram: ${status}${version} (${config.url})`);
        });

      memCmd
        .command('search')
        .description('Search engram memory')
        .argument('<query>', 'Search query')
        .action(async (query: unknown) => {
          const identity = resolveIdentity('', process.cwd());
          const response = await client.searchContext({
            project: config.project ?? identity.projectId,
            query: String(query),
          });
          const obs = response?.observations ?? [];
          if (obs.length === 0) {
            console.log('No results found.');
            return;
          }
          for (const o of obs) {
            const score = typeof o.similarity === 'number' ? ` [${o.similarity.toFixed(2)}]` : '';
            console.log(`- ${o.title}${score}`);
          }
        });

      memCmd
        .command('store')
        .description('Store a memory')
        .argument('<text>', 'Text to remember')
        .action(async (text: unknown) => {
          const textStr = String(text);
          const title = textStr.length > 80 ? textStr.slice(0, 77) + '...' : textStr;
          const storeIdentity = resolveIdentity('', process.cwd());
          const response = await client.bulkImport([{
            title,
            content: textStr.slice(0, 900),
            type: 'change',
            project: config.project ?? storeIdentity.projectId,
            scope: 'project',
          }]);
          if (response && response.imported > 0) {
            console.log(`Stored: "${title}"`);
          } else {
            console.log('Failed to store memory.');
          }
        });

      // CLI context is stable per process — cache the tool instance.
      const cliMigrateTool = createMemoryMigrateTool(
        { workspaceDir: process.cwd() },
        client, config, api,
      );

      memCmd
        .command('migrate')
        .description('Import local memory files into engram')
        .option('--dry-run', 'Preview without importing')
        .option('--force', 'Re-import already migrated files')
        .action(async (...args: unknown[]) => {
          const opts = (args[args.length - 1] ?? {}) as Record<string, unknown>;
          const dryRun = Boolean(opts['dryRun'] ?? opts['dry-run']);
          const force = Boolean(opts.force);
          const result = await cliMigrateTool.execute('cli-migrate', { dryRun, force });
          console.log(result);
        });
    }, { commands: ['memory'] });

    // File watcher service (replaces memory-core chokidar watcher)
    // Use configured workspace, or default agent workspace path.
    const watcherWorkspaceDir = config.workspaceDir
      ?? join(homedir(), '.openclaw', 'workspace');
    api.registerService(createFileWatcherService(watcherWorkspaceDir, client, config, api.logger));

    api.logger.info('[engram] plugin registered successfully');
  },
};

export default plugin;

// Named exports for consumers that prefer explicit imports
export { EngramRestClient } from './client.js';
export { parseConfig, getJsonSchema } from './config.js';
export { resolveIdentity, projectIDFromWorkspace } from './identity.js';
export { formatContext } from './context/formatter.js';
export { AvailabilityTracker } from './availability.js';
