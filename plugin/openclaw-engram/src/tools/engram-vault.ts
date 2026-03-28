/**
 * engram_vault_store and engram_vault_get — credential management.
 *
 * Securely store and retrieve encrypted credentials via engram's vault.
 * Encryption happens server-side (AES-256-GCM). Credentials never leave the server unencrypted.
 */

import { z } from 'zod';
import { Type } from '@sinclair/typebox';
import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import type { AnyAgentTool, OpenClawPluginToolContext } from '../types/openclaw.js';

const StoreParamsSchema = z.object({
  name: z.string().min(1),
  value: z.string().min(1),
  scope: z.enum(['project', 'global']).optional().default('project'),
});

const storeParameters = Type.Object({
  name: Type.String({ description: 'Credential name (e.g., OPENAI_API_KEY)' }),
  value: Type.String({ description: 'Credential value to encrypt and store' }),
  scope: Type.Optional(Type.String({
    description: 'Scope: project (default) or global',
    enum: ['project', 'global'],
  })),
});

const GetParamsSchema = z.object({
  name: z.string().min(1),
});

const getParameters = Type.Object({
  name: Type.String({ description: 'Credential name to retrieve' }),
});

export function createEngramVaultStoreTool(
  ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  config: PluginConfig,
): AnyAgentTool {
  return {
    name: 'engram_vault_store',
    description:
      'Securely store an encrypted credential in engram vault. ' +
      'Server-side AES-256-GCM encryption. Use for API keys, tokens, passwords.',
    parameters: storeParameters,

    async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
      const parsed = StoreParamsSchema.safeParse(params);
      if (!parsed.success) {
        return `Invalid parameters: ${parsed.error.message}`;
      }

      if (!client.isAvailable()) {
        return 'engram is currently unreachable — vault store unavailable';
      }

      const identity = resolveIdentity(ctx.agentId ?? '', ctx.workspaceDir);
      const project = config.project ?? identity.projectId;

      const success = await client.storeCredential(
        parsed.data.name,
        parsed.data.value,
        parsed.data.scope,
        project,
      );

      return success
        ? `Credential "${parsed.data.name}" stored securely (scope: ${parsed.data.scope})`
        : `Failed to store credential "${parsed.data.name}" — server may be unavailable or vault not configured`;
    },
  };
}

export function createEngramVaultGetTool(
  _ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  _config: PluginConfig,
): AnyAgentTool {
  return {
    name: 'engram_vault_get',
    description:
      'Retrieve and decrypt a credential from engram vault by name. ' +
      'Returns the decrypted value. Use for accessing stored API keys and tokens.',
    parameters: getParameters,

    async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
      const parsed = GetParamsSchema.safeParse(params);
      if (!parsed.success) {
        return `Invalid parameters: ${parsed.error.message}`;
      }

      if (!client.isAvailable()) {
        return 'engram is currently unreachable — vault get unavailable';
      }

      const cred = await client.getCredential(parsed.data.name);
      if (!cred) {
        return `Credential "${parsed.data.name}" not found — check name and scope`;
      }

      // Note: credential value is in tool output (conversation history).
      // Ensure session transcripts handle sensitive data appropriately.
      return `${cred.name}: ${cred.value}`;
    },
  };
}
