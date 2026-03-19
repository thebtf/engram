import { z } from 'zod';

// ---------------------------------------------------------------------------
// Config schema
// ---------------------------------------------------------------------------

export const PluginConfigSchema = z.object({
  /** Engram server base URL, e.g. "http://localhost:37777". */
  url: z
    .string()
    .url('url must be a valid HTTP URL')
    .refine(
      (u) => /^https?:\/\//i.test(u),
      { message: 'url must use http or https scheme' },
    )
    .default('http://localhost:37777'),

  /** Bearer token for engram API authentication. Marked sensitive in UI hints. */
  token: z.string().min(1, 'token is required for engram API authentication'),

  /**
   * Project scope override. When set, all observations are stored under this
   * project regardless of workspace identity.
   */
  project: z.string().optional(),

  /** Maximum observations to inject per prompt turn. */
  contextLimit: z.number().int().positive().default(10),

  /** Maximum observations to inject at session start. */
  sessionContextLimit: z.number().int().positive().default(20),

  /**
   * Token budget for context injection. Approximately 4 chars per token.
   * Observations are trimmed to fit within this budget.
   */
  tokenBudget: z.number().int().positive().default(2000),

  /** Per-request HTTP timeout in milliseconds. */
  timeoutMs: z.number().int().positive().default(5000),

  /**
   * Enable automatic observation extraction on compaction and session end.
   * When false, only explicit tool calls store memories.
   */
  autoExtract: z.boolean().default(true),

  /**
   * Workspace directory path for memory file discovery (MEMORY.md, memory/).
   * Required for /migrate command when called from channel context (Telegram, Discord)
   * where PluginCommandContext doesn't carry workspaceDir.
   * Defaults to ~/.openclaw/workspace/ (default agent workspace).
   */
  workspaceDir: z.string().optional(),

  /** Heartbeat / keep-alive event settings. */
  heartbeat: z.object({
    /** Send heartbeat events to engram for ingestion. */
    ingest: z.boolean().default(false),
  }).default({ ingest: false }),

  /** Log verbosity level. */
  logLevel: z.enum(['debug', 'info', 'warn', 'error']).default('warn'),
});

export type PluginConfig = z.infer<typeof PluginConfigSchema>;

/**
 * Parse and validate raw config from the OpenClaw plugin manifest.
 * Returns a fully-populated config with defaults applied.
 * Throws a ZodError if validation fails.
 */
export function parseConfig(raw: Record<string, unknown>): PluginConfig {
  return PluginConfigSchema.parse(raw);
}

/**
 * Export the config schema as a JSON Schema object for OpenClaw's manifest
 * `configSchema` field. This is a minimal static representation — the authoritative
 * validation is done by `parseConfig()` at runtime.
 */
export function getJsonSchema(): Record<string, unknown> {
  return {
    type: 'object',
    properties: {
      url: { type: 'string', description: 'Engram server URL', default: 'http://localhost:37777' },
      token: { type: 'string', description: 'Bearer token for API authentication', uiHints: { sensitive: true } },
      project: { type: 'string', description: 'Project scope override' },
      contextLimit: { type: 'number', description: 'Max observations per prompt', default: 10 },
      sessionContextLimit: { type: 'number', description: 'Max observations at session start', default: 20 },
      tokenBudget: { type: 'number', description: 'Token budget for context injection', default: 2000 },
      timeoutMs: { type: 'number', description: 'Per-request timeout (ms)', default: 5000 },
      autoExtract: { type: 'boolean', description: 'Auto-extract on compaction/session-end', default: true },
      workspaceDir: { type: 'string', description: 'Workspace dir for /migrate (default: ~/.openclaw/workspace/)' },
      heartbeat: {
        type: 'object',
        properties: {
          ingest: { type: 'boolean', description: 'Send heartbeat events to engram for ingestion', default: false },
        },
      },
      logLevel: { type: 'string', enum: ['debug', 'info', 'warn', 'error'], default: 'warn' },
    },
    required: ['url', 'token'],
  };
}
