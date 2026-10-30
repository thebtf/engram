/**
 * memory_migrate — import local memory files into engram.
 *
 * Reads MEMORY.md and memory/**\/*.md from the workspace, splits by ## headers,
 * and stores canonical memories with confirmed local progress checkpoints.
 */

import { z } from 'zod';
import { Type } from '@sinclair/typebox';
import { join, relative, resolve, normalize, isAbsolute } from 'node:path';
import { createHash } from 'node:crypto';
import { resolveProject } from '../client.js';
import { validateProjectID } from '../identity.js';
import type { EngramRestClient } from '../client.js';
import type { PluginConfig } from '../config.js';
import { quotedPromptScalar } from '../context/formatter.js';
import type {
 AnyAgentTool,
 OpenClawPluginToolContext,
 OpenClawPluginApi,
} from '../types/openclaw.js';
import {
 splitIntoChunks,
 loadMarker,
 syncMemoryFile,
 discoverMemoryFiles,
 safeReadFile,
 MARKER_FILE,
 type MemoryChunk,
} from '../utils/memory-files.js';

// Single source of truth: Zod schema defines shape, TypeBox schema exposes it to SDK.
// If you add/remove a field, update BOTH the Zod schema AND the TypeBox schema below.
const MigrateParamsSchema = z.object({
 dryRun: z.boolean().optional().default(false),
 path: z.string().optional(),
 force: z.boolean().optional().default(false),
});

type MigrateParams = z.infer<typeof MigrateParamsSchema>;

const migrateParameters = Type.Object({
 dryRun: Type.Optional(Type.Boolean({ description: 'Preview what would be imported without writing', default: false })),
 path: Type.Optional(Type.String({ description: 'Specific file path to migrate (default: MEMORY.md + memory/**/*.md)' })),
 force: Type.Optional(Type.Boolean({ description: 'Deliberately re-import confirmed or unknown writes; may create duplicates and never proves an earlier write was absent', default: false })),
});

// Compile-time drift guard: if Zod and TypeBox schemas diverge in field names,
// this assignment will produce a TypeScript error.
const _schemaCheck: Record<keyof MigrateParams, true> = {
 dryRun: true, path: true, force: true,
}; void _schemaCheck;

export function createMemoryMigrateTool(
 ctx: OpenClawPluginToolContext,
 client: EngramRestClient,
 config: PluginConfig,
 api: OpenClawPluginApi,
): AnyAgentTool {
 return {
  name: 'memory_migrate',
  label: 'Migrate Memory',
  description:
   'Import local workspace memory files (MEMORY.md, memory/**/*.md) into engram. ' +
   'Use dryRun=true to preview. Idempotent — skips already-migrated files unless force=true.',
  parameters: migrateParameters,

  async execute(_toolCallId: string, params: Record<string, unknown>): Promise<string> {
   const parsed = MigrateParamsSchema.safeParse(params);
   if (!parsed.success) {
    return `Invalid parameters: ${parsed.error.message}`;
   }

   if (!parsed.data.dryRun && !client.isAvailable()) {
    return 'engram is currently unreachable — migration unavailable';
   }

   return runMigration(parsed.data, ctx, client, config, api);
  },
 };
}

async function runMigration(
 params: { dryRun: boolean; path?: string; force: boolean },
 ctx: OpenClawPluginToolContext,
 client: EngramRestClient,
 config: PluginConfig,
 api: OpenClawPluginApi,
): Promise<string> {
 const workspaceDir = ctx.workspaceDir;
 if (!workspaceDir) {
  return 'No workspace directory available — cannot locate memory files.';
 }

 let previewProject: string | undefined;
 if (config.project !== undefined) {
  try { previewProject = validateProjectID(config.project); } catch { /* Preview remains offline with unverified checkpoint attribution. */ }
 }

 // Load existing marker
 const markerPath = join(workspaceDir, MARKER_FILE);
 const marker = await loadMarker(markerPath);

 // Discover files
 let filePaths: string[];
 if (params.path) {
  const resolved = normalize(resolve(api.resolvePath(params.path)));
  const normalizedWs = normalize(resolve(workspaceDir));
  const rel = relative(normalizedWs, resolved);
  if (rel.startsWith('..') || isAbsolute(rel)) {
   return `Path ${quotedPromptScalar(params.path)} resolves outside the workspace — access denied.`;
  }
  filePaths = [resolved];
 } else {
  filePaths = await discoverMemoryFiles(workspaceDir);
 }

 if (filePaths.length === 0) {
  return 'No memory files found in workspace (checked MEMORY.md and memory/**/*.md).';
 }

 // Read and chunk files
 const allChunks: MemoryChunk[] = [];
 const newFileHashes: Record<string, string> = {};
 const skippedFiles: string[] = [];

 for (const filePath of filePaths) {
  const relPath = relative(workspaceDir, filePath);
  const fileContent = await safeReadFile(filePath);
  if (!fileContent) continue;

  const hash = createHash('sha256').update(fileContent).digest('hex');
  newFileHashes[relPath] = hash;

  // Skip if already migrated with same hash (unless force)
  if (params.dryRun && previewProject !== undefined && !params.force && marker?.files[relPath] === hash && marker.fileProjects?.[relPath] === previewProject
   && ![...Object.values(marker.writes ?? {}), ...Object.values(marker.unresolvedAttempts ?? {})].some(attempt => attempt.project === previewProject && attempt.sourcePath === relPath && (attempt.state === 'pending' || attempt.state === 'unknown') && (!attempt.fingerprint || marker.acknowledgedAttempts?.[attempt.attemptId]?.fingerprint !== attempt.fingerprint))) {
   skippedFiles.push(relPath);
   continue;
  }

  const chunks = splitIntoChunks(fileContent, relPath);
  allChunks.push(...chunks);
 }

 if (allChunks.length === 0 && skippedFiles.length > 0) {
  return `All ${skippedFiles.length} file(s) already migrated. Use force=true to re-import.`;
 }

 if (allChunks.length === 0) {
  return 'No content to migrate (files were empty or could not be read).';
 }

 // Dry run — show summary with first 5 chunks
 if (params.dryRun) {
  const fileCount = filePaths.length - skippedFiles.length;
  const lines: string[] = [
   `Dry run: ${allChunks.length} candidate chunk(s) from ${fileCount} file(s); no writes sent.`,
  ];
  if (previewProject === undefined) lines.push('Checkpoint project attribution is unverified; no files are treated as already completed. Native resolution was not invoked.');
  if (skippedFiles.length > 0) {
   lines.push(`Skipped (already migrated): ${skippedFiles.length} file(s)`);
  }
  const unknown = [...Object.values(marker?.writes ?? {}), ...Object.values(marker?.unresolvedAttempts ?? {})].filter(attempt => previewProject !== undefined && attempt.project === previewProject && (attempt.state === 'pending' || attempt.state === 'unknown') && (!attempt.fingerprint || marker?.acknowledgedAttempts?.[attempt.attemptId]?.fingerprint !== attempt.fingerprint));
  if (unknown.length > 0) lines.push(`${unknown.length} unresolved write attempt(s): automatic import is blocked for their files; explicit force is a deliberate potentially duplicate reimport.`);
  lines.push('Chunks (first 5):');
  const preview = allChunks.slice(0, 5);
  for (const chunk of preview) {
   lines.push(formatDryRunChunkLine(chunk));
  }
  if (allChunks.length > 5) {
   lines.push(`... and ${allChunks.length - 5} more chunk(s)`);
  }
  return lines.join('\n');
 }

 const registration = await resolveProject(client, ctx.agentId ?? '', workspaceDir, config.project);
 if (!registration.ok) {
  return `Project identity unavailable: ${registration.error.code} (${registration.error.upgradeAction})`;
 }
 const project = registration.canonicalProject;


 let totalImported = 0;
 let totalConfirmed = 0;
 let totalUnresolved = 0;
 let totalAcknowledged = 0;
 const errors: string[] = [];
 for (const [sourcePath, hash] of Object.entries(newFileHashes)) {
  const chunks = allChunks.filter(chunk => chunk.sourcePath === sourcePath);
  if (chunks.length === 0) continue;
  const result = await syncMemoryFile(client, markerPath, {
   project, sourcePath, hash, chunks, tag: 'migrated', force: params.force,
  }, { sourceAgent: ctx.agentId ?? 'openclaw-migrate', sessionId: ctx.sessionId ?? ctx.sessionKey });
  totalImported += result.stored;
  totalConfirmed += result.alreadyStored;
  totalUnresolved += result.unresolved;
  totalAcknowledged += result.acknowledged ?? 0;
  if (result.error) errors.push(`${sourcePath}: ${result.error}`);
 }

 const status = errors.length ? 'Migration incomplete.' : totalUnresolved > 0 ? 'Current writes confirmed; earlier outcomes remain unresolved.' : totalAcknowledged > 0 ? 'Migration complete with acknowledged duplicate risk.' : 'Migration complete.';
 const lines = [status, `  Imported: ${totalImported} memory(s)`];
 if (totalConfirmed > 0) lines.push(`  Already confirmed: ${totalConfirmed} chunk(s)`);
 if (totalUnresolved > 0) lines.push(`  Unresolved earlier attempts: ${totalUnresolved}. Explicit force can create duplicates; a new confirmed write does not prove prior absence.`);
 if (params.force) lines.push('  Explicit force deliberately reimported all chunks; earlier rows may exist.');
 if (totalAcknowledged > 0) lines.push(`  Acknowledged prior attempts: ${totalAcknowledged}. Earlier outcomes remain unknown; duplicate risk was explicitly accepted, not disproved.`);
 if (errors.length > 0) {
  lines.push(`  Errors: ${quotedPromptScalar(errors.join(', '))}`);
  lines.push('  Confirmed chunks are checkpointed; incomplete files are not marked done. Inspect unknown outcomes before retrying.');
 }
 return lines.join('\n');
}

export function formatDryRunChunkLine(chunk: MemoryChunk): string {
 const contentPreview = chunk.content.length > 80
  ? chunk.content.slice(0, 77) + '...'
  : chunk.content;
 return [
  '- chunk:',
  `type=${quotedPromptScalar(chunk.type)}`,
  `title=${quotedPromptScalar(chunk.title)}`,
  `source=${quotedPromptScalar(chunk.sourcePath)}`,
  `preview=${quotedPromptScalar(contentPreview)}`,
 ].join(' ');
}
