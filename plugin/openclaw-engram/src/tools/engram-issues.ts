/**
 * engram_issues — cross-project issue tracking between agents.
 *
 * WHEN TO USE: When you find a bug, need a feature, or want to leave a task
 * for agents working on another project. Do NOT use store/docs for issues —
 * they lack lifecycle management (status, priority, comments).
 *
 * Lifecycle: open → acknowledged (auto on injection) → resolved (explicit) → closed.
 * IMPORTANT: acknowledged is still active backlog for the target project, not a done state.
 */

import { z } from 'zod';
import { Type } from '@sinclair/typebox';
import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import type { AnyAgentTool, OpenClawPluginToolContext } from '../types/openclaw.js';

// ---------------------------------------------------------------------------
// Zod schemas for runtime validation
// ---------------------------------------------------------------------------

const CreateSchema = z.object({
  action: z.literal('create'),
  title: z.string().min(1),
  body: z.string().optional(),
  priority: z.enum(['critical', 'high', 'medium', 'low']).default('medium'),
  target_project: z.string().optional(),
  labels: z.array(z.string()).optional(),
});

const ListSchema = z.object({
  action: z.literal('list'),
  target_project: z.string().optional(),
  source_project: z.string().optional(),
  status: z.string().optional(),
  limit: z.number().int().positive().optional(),
});

const GetSchema = z.object({
  action: z.literal('get'),
  id: z.number().int().positive(),
});

const UpdateSchema = z.object({
  action: z.literal('update'),
  id: z.number().int().positive(),
  status: z.enum(['resolved']),
  comment: z.string().optional(),
});

const CommentSchema = z.object({
  action: z.literal('comment'),
  id: z.number().int().positive(),
  body: z.string().min(1),
});

const ReopenSchema = z.object({
  action: z.literal('reopen'),
  id: z.number().int().positive(),
  body: z.string().optional(),
});

const IssueParamsSchema = z.discriminatedUnion('action', [
  CreateSchema,
  ListSchema,
  GetSchema,
  UpdateSchema,
  CommentSchema,
  ReopenSchema,
]);

// ---------------------------------------------------------------------------
// TypeBox parameter schema (for tool registration / OpenClaw schema generation)
// ---------------------------------------------------------------------------

const issueParameters = Type.Object({
  action: Type.Union([
    Type.Literal('create'),
    Type.Literal('list'),
    Type.Literal('get'),
    Type.Literal('update'),
    Type.Literal('comment'),
    Type.Literal('reopen'),
  ], { description: 'Action to perform' }),
  id: Type.Optional(Type.Number({ description: 'Issue ID (for get, update, comment, reopen)' })),
  title: Type.Optional(Type.String({ description: 'Issue title (required for create)' })),
  body: Type.Optional(Type.String({ description: 'Issue body or comment text' })),
  priority: Type.Optional(Type.Union([
    Type.Literal('critical'), Type.Literal('high'),
    Type.Literal('medium'), Type.Literal('low'),
  ], { description: 'Priority (for create)', default: 'medium' })),
  target_project: Type.Optional(Type.String({ description: 'Target project slug (for target-project inbox view; defaults to current project)' })),
  source_project: Type.Optional(Type.String({ description: 'Source project slug (for follow-up on issues your project filed for other teams)' })),
  status: Type.Optional(Type.Union([
    Type.Literal('resolved'),
  ], { description: 'New status (for update — only resolved)' })),
  labels: Type.Optional(Type.Array(Type.String(), { description: 'Labels (bug, feature, etc.)' })),
});

// ---------------------------------------------------------------------------
// Tool factory
// ---------------------------------------------------------------------------

export function createEngramIssuesTool(
  ctx: OpenClawPluginToolContext,
  client: EngramRestClient,
  config: PluginConfig,
): AnyAgentTool {
  return {
    name: 'engram_issues',
    description:
      'Create, track, and resolve cross-project issues between agents. ' +
      'Issues are automatically shown to agents working on the target project. ' +
      'acknowledged and reopened are still active backlog, not done states. ' +
      'Use to report bugs, request features, or leave tasks for agents in other projects. ' +
      'Do NOT use store or docs for issues — use this tool instead.',
    parameters: issueParameters,

    async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
      const parsed = IssueParamsSchema.safeParse(params);
      if (!parsed.success) {
        return `Invalid parameters: ${parsed.error.message}`;
      }

      if (!client.isAvailable()) {
        return 'engram is currently unreachable — issues unavailable';
      }

      const identity = resolveIdentity(ctx.agentId ?? '', ctx.workspaceDir);
      const project = config.project ?? identity.projectId;

      switch (parsed.data.action) {
        case 'create': {
          const { title, body, priority, target_project, labels } = parsed.data;
          const resp = await client.createIssue({
            title,
            body,
            priority,
            source_project: project,
            target_project: target_project ?? project,
            source_agent: identity.agentId || 'openclaw',
            labels,
          });
          if (!resp) return 'Failed to create issue — server error';
          return `Issue #${resp.id} created: ${title}`;
        }

        case 'list': {
          const { target_project, source_project, status, limit } = parsed.data;
          const effectiveProject = target_project ?? project;
          const effectiveSourceProject = source_project ?? '';
          const effectiveStatus = status ?? 'open,acknowledged,reopened';
          const resp = await client.listIssues({
            ...(effectiveSourceProject ? { source_project: effectiveSourceProject } : { project: effectiveProject }),
            status: effectiveStatus,
            limit: limit ?? 20,
          });
          if (!resp) return 'Failed to list issues — server error';
          if (resp.issues.length === 0) {
            return effectiveSourceProject
              ? `No issues found for source project ${effectiveSourceProject} with statuses: ${effectiveStatus}.`
              : `No issues found for target project ${effectiveProject} with active statuses: ${effectiveStatus}.`;
          }

          const lines = resp.issues.map((i) => {
            const prio = i.priority.toUpperCase();
            return `#${i.id} [${prio}] [${i.status}] ${i.title} (from: ${i.source_project} → target: ${i.target_project})`;
          });
          return effectiveSourceProject
            ? [
                `Follow-up issues created by source project ${effectiveSourceProject} (${effectiveStatus}):`,
                `${resp.total} issue(s) total. These are issues your project filed for other teams — re-read status/comments, verify outcomes, test claims, then close, reopen, or comment with precise feedback.`,
                ...lines,
              ].join('\n')
            : [
                `Active issues for target project ${effectiveProject} (${effectiveStatus}):`,
                `${resp.total} issue(s) total. These are your project's active issues — read, investigate, comment, resolve, or reopen after verification; do not ignore acknowledged items.`,
                ...lines,
              ].join('\n');
        }

        case 'get': {
          const resp = await client.getIssue(parsed.data.id);
          if (!resp) return `Issue #${parsed.data.id} not found or server error`;

          const i = resp.issue;
          let out = `#${i.id} [${i.priority.toUpperCase()}] [${i.status}] ${i.title}\n`;
          out += `From: ${i.source_project} → ${i.target_project}\n`;
          if (i.body) out += `\n${i.body}\n`;
          if (resp.comments.length > 0) {
            out += `\n--- ${resp.comments.length} comment(s) ---\n`;
            for (const c of resp.comments) {
              out += `[${c.author_project}/${c.author_agent}] ${c.body}\n`;
            }
          }
          return out;
        }

        case 'update': {
          const resp = await client.updateIssue(parsed.data.id, {
            status: parsed.data.status,
            comment: parsed.data.comment,
            source_project: project,
            source_agent: identity.agentId || 'openclaw',
          });
          if (!resp) return `Failed to update issue #${parsed.data.id}`;
          return `Issue #${parsed.data.id} resolved.`;
        }

        case 'comment': {
          const resp = await client.updateIssue(parsed.data.id, {
            comment: parsed.data.body,
            source_project: project,
            source_agent: identity.agentId || 'openclaw',
          });
          if (!resp) return `Failed to comment on issue #${parsed.data.id}`;
          return `Comment added to issue #${parsed.data.id}.`;
        }

        case 'reopen': {
          const resp = await client.updateIssue(parsed.data.id, {
            status: 'reopened',
            comment: parsed.data.body ?? 'Reopened',
            source_project: project,
            source_agent: identity.agentId || 'openclaw',
          });
          if (!resp) return `Failed to reopen issue #${parsed.data.id}`;
          return `Issue #${parsed.data.id} reopened.`;
        }
      }
    },
  };
}
