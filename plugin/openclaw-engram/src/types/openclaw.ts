/**
 * Local type definitions matching the real OpenClaw Plugin SDK.
 *
 * These are locally defined since no SDK package is published.
 * Shapes match openclaw/openclaw:src/plugins/types.ts (verified via Nia research).
 */

// ---------------------------------------------------------------------------
// Core SDK types
// ---------------------------------------------------------------------------

/** Logger provided by the SDK on the plugin API. */
export interface PluginLogger {
  debug(message: string, ...args: unknown[]): void;
  info(message: string, ...args: unknown[]): void;
  warn(message: string, ...args: unknown[]): void;
  error(message: string, ...args: unknown[]): void;
}

/** Runtime services exposed by the SDK. */
export interface PluginRuntime {
  tools: {
    createMemorySearchTool(): unknown;
  };
}

/** OpenClaw global config (opaque to plugins). */
export type OpenClawConfig = Record<string, unknown>;

/** JSON Schema for plugin config (static, declared in plugin definition). */
export type OpenClawPluginConfigSchema = Record<string, unknown>;

export interface OpenClawPluginApi {
  /** Plugin ID. */
  id: string;
  /** Plugin display name. */
  name: string;
  /** OpenClaw global config. */
  config: OpenClawConfig;
  /** Plugin-specific config from the user's OpenClaw settings. */
  pluginConfig?: Record<string, unknown>;
  /** Runtime services. */
  runtime: PluginRuntime;
  /** Logger scoped to this plugin. */
  logger: PluginLogger;

  /** Register a tool or tool factory. */
  registerTool(
    toolOrFactory: AnyAgentTool | AnyAgentTool[] | ToolFactory,
    opts?: { names?: string[] },
  ): void;

  /** Register a lifecycle hook handler (typed). */
  on<K extends PluginHookName>(
    hookName: K,
    handler: PluginHookHandler<K>,
    opts?: { priority?: number },
  ): void;

  /** Register a CLI extension. */
  registerCli(
    registrar: (ctx: { program: CliProgram }) => void,
    opts?: { commands?: string[] },
  ): void;

  /** Register a background service. */
  registerService(service: OpenClawPluginService): void;

  /** Register a slash command. */
  registerCommand(command: OpenClawPluginCommandDefinition): void;

  /** Resolve a workspace-relative path safely. */
  resolvePath(input: string): string;
}

export interface OpenClawPluginDefinition {
  /** Unique plugin identifier. */
  id: string;
  /** Optional display name. */
  name?: string;
  /** Human-readable description. */
  description?: string;
  /** Version string. */
  version?: string;
  /** Plugin kind — "memory" is an exclusive slot. */
  kind?: 'memory' | 'context-engine';
  /** JSON Schema for plugin config (declared statically). */
  configSchema?: OpenClawPluginConfigSchema;
  /**
   * Called once when the plugin is loaded. Register all hooks, tools,
   * commands, and services here.
   */
  register?: (api: OpenClawPluginApi) => void | Promise<void>;
  /** Optional activation phase (called after all plugins registered). */
  activate?: (api: OpenClawPluginApi) => void | Promise<void>;
}

// ---------------------------------------------------------------------------
// Tool types (TypeBox-compatible)
// ---------------------------------------------------------------------------

/** A TSchema-compatible object (from @sinclair/typebox). */
export type TSchema = Record<string, unknown>;

export interface AnyAgentTool {
  name: string;
  label?: string;
  description: string;
  /** TypeBox Type.Object schema for tool parameters. */
  parameters: TSchema;
  /** Execute the tool. Returns a string result. */
  execute(toolCallId: string, params: Record<string, unknown>): Promise<string>;
}

/** Tool factory: receives context, returns array of tools. */
export type ToolFactory = (ctx: OpenClawPluginToolContext) => AnyAgentTool[];

export interface OpenClawPluginToolContext {
  config?: OpenClawConfig;
  workspaceDir?: string;
  agentDir?: string;
  agentId?: string;
  sessionKey?: string;
  /** Per-conversation ID, regenerated on /new and /reset. */
  sessionId?: string;
  messageChannel?: string;
}

// ---------------------------------------------------------------------------
// Hook types — 24 hook names
// ---------------------------------------------------------------------------

export type PluginHookName =
  | 'before_model_resolve' | 'before_prompt_build' | 'before_agent_start'
  | 'llm_input' | 'llm_output' | 'agent_end'
  | 'before_compaction' | 'after_compaction' | 'before_reset'
  | 'message_received' | 'message_sending' | 'message_sent'
  | 'before_tool_call' | 'after_tool_call' | 'tool_result_persist'
  | 'before_message_write' | 'session_start' | 'session_end'
  | 'subagent_spawning' | 'subagent_delivery_target' | 'subagent_spawned'
  | 'subagent_ended' | 'gateway_start' | 'gateway_stop';

/** Base fields present in every hook event. */
export interface BaseHookEvent {
  agentId?: string;
  sessionId?: string;
  sessionKey?: string;
  workspaceDir?: string;
  timestamp?: string;
}

export interface SessionStartEvent extends BaseHookEvent {
  initialPrompt?: string;
}

export interface BeforePromptBuildEvent extends BaseHookEvent {
  prompt?: string;
  turnIndex?: number;
}

export interface AfterToolCallEvent extends BaseHookEvent {
  toolName?: string;
  toolInput?: unknown;
  toolResult?: unknown;
  success?: boolean;
  error?: string;
}

export interface BeforeCompactionEvent extends BaseHookEvent {
  messages?: ConversationMessage[];
  reason?: string;
}

export interface SessionEndEvent extends BaseHookEvent {
  messages?: ConversationMessage[];
  reason?: string;
}

export interface ConversationMessage {
  role: 'user' | 'assistant' | 'system' | 'tool';
  content: string;
}

// ---------------------------------------------------------------------------
// Hook handler return types
// ---------------------------------------------------------------------------

export interface PromptBuildResult {
  prependContext?: string;
  appendSystemContext?: string;
}

export interface SessionStartResult {
  appendSystemContext?: string;
}

export type HookResult = void | PromptBuildResult | SessionStartResult | undefined;

/** Map hook names to their event types (for hooks we use). */
export interface HookEventMap {
  session_start: SessionStartEvent;
  before_prompt_build: BeforePromptBuildEvent;
  after_tool_call: AfterToolCallEvent;
  before_compaction: BeforeCompactionEvent;
  session_end: SessionEndEvent;
}

/** Generic hook handler type. */
export type PluginHookHandler<K extends PluginHookName> =
  K extends keyof HookEventMap
    ? (event: HookEventMap[K]) => HookResult | Promise<HookResult>
    : (event: BaseHookEvent) => HookResult | Promise<HookResult>;

// ---------------------------------------------------------------------------
// Service types
// ---------------------------------------------------------------------------

export interface OpenClawPluginServiceContext {
  logger: PluginLogger;
}

export interface OpenClawPluginService {
  id: string;
  start(ctx: OpenClawPluginServiceContext): Promise<void> | void;
  stop(ctx: OpenClawPluginServiceContext): Promise<void> | void;
}

// ---------------------------------------------------------------------------
// Command types
// ---------------------------------------------------------------------------

/**
 * Context passed to plugin command handlers by the SDK.
 * Commands are channel-facing (Telegram, Discord, etc.), not agent-facing.
 */
export interface PluginCommandContext {
  senderId?: string;
  channel: string;
  channelId?: string;
  isAuthorizedSender: boolean;
  /** Raw command arguments after the command name (single string, not array). */
  args?: string;
  commandBody: string;
  config: OpenClawConfig;
  from?: string;
  to?: string;
  accountId?: string;
  messageThreadId?: number;
}

/**
 * Result returned by a plugin command handler.
 * Must contain `text` field (not `output`).
 */
export interface PluginCommandResult {
  text: string;
}

/**
 * Handler function for plugin commands.
 */
export type PluginCommandHandler = (
  ctx: PluginCommandContext,
) => PluginCommandResult | Promise<PluginCommandResult>;

export interface OpenClawPluginCommandDefinition {
  name: string;
  description: string;
  /** Whether this command accepts arguments. */
  acceptsArgs?: boolean;
  /** Whether only authorized senders can use this command (default: true). */
  requireAuth?: boolean;
  /** The handler function — SDK checks typeof handler === "function". */
  handler: PluginCommandHandler;
}

// ---------------------------------------------------------------------------
// CLI types (minimal — Commander-like)
// ---------------------------------------------------------------------------

export interface CliProgram {
  command(name: string): CliCommand;
}

export interface CliCommand {
  command(name: string): CliCommand;
  description(desc: string): CliCommand;
  argument(name: string, desc?: string): CliCommand;
  option(flags: string, desc?: string, defaultValue?: unknown): CliCommand;
  action(fn: (...args: unknown[]) => void | Promise<void>): CliCommand;
}
