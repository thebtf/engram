/**
 * memory_migrate — import local memory files into engram.
 *
 * Reads MEMORY.md and memory/**\/*.md from the workspace, splits by ## headers,
 * and bulk-imports into engram. Uses a marker file for idempotency.
 */

import { z } from 'zod';
import { Type } from '@sinclair/typebox';
import { join, relative, resolve, normalize, isAbsolute } from 'node:path';
import { createHash } from 'node:crypto';
import type { EngramRestClient, BulkImportRequest } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import type {
  AnyAgentTool,
  OpenClawPluginToolContext,
  OpenClawPluginApi,
} from '../types/openclaw.js';
import {
  splitIntoChunks,
  loadMarker,
  saveMarker,
  discoverMemoryFiles,
  safeReadFile,
  MARKER_FILE,
  type MigrationMarker,
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
  force: Type.Optional(Type.Boolean({ description: 'Ignore migration marker, re-import everything', default: false })),
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
      return `Path "${params.path}" resolves outside the workspace — access denied.`;
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
    if (!params.force && marker?.files[relPath] === hash) {
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
      `Dry run: ${allChunks.length} chunk(s) from ${fileCount} file(s) would be imported.`,
    ];
    if (skippedFiles.length > 0) {
      lines.push(`Skipped (already migrated): ${skippedFiles.length} file(s)`);
    }
    lines.push('Chunks (first 5):');
    const preview = allChunks.slice(0, 5);
    for (const chunk of preview) {
      const contentPreview = chunk.content.length > 80
        ? chunk.content.slice(0, 77) + '...'
        : chunk.content;
      lines.push(`- [${chunk.type}] "${chunk.title}" (from ${chunk.sourcePath}): ${contentPreview}`);
    }
    if (allChunks.length > 5) {
      lines.push(`... and ${allChunks.length - 5} more chunk(s)`);
    }
    return lines.join('\n');
  }

  // Import
  const identity = resolveIdentity(ctx.agentId ?? '', workspaceDir);
  const project = config.project ?? identity.projectId;

  const observations: BulkImportRequest[] = allChunks.map((chunk) => ({
    title: chunk.title,
    content: chunk.content,
    type: chunk.type,
    project,
    scope: 'project',
    tags: ['migrated', `source:${chunk.sourcePath}`],
  }));

  // Batch import (max 50 per request)
  let totalImported = 0;
  let totalSkipped = 0;
  const errors: string[] = [];
  let hasFailures = false;

  for (let i = 0; i < observations.length; i += 50) {
    const batch = observations.slice(i, i + 50);
    const response = await client.bulkImport(batch);
    if (response) {
      totalImported += response.imported;
      totalSkipped += response.skipped_duplicates;
      if (response.errors) {
        errors.push(...response.errors);
        hasFailures = true;
      }
    } else {
      errors.push(`Batch ${Math.floor(i / 50) + 1} failed — server returned no response`);
      hasFailures = true;
    }
  }

  // Save marker only if ALL batches succeeded — partial failure must not
  // mark files as "done" or subsequent runs will silently skip them.
  if (!hasFailures) {
    const updatedMarker: MigrationMarker = {
      lastMigrated: new Date().toISOString(),
      files: { ...(marker?.files ?? {}), ...newFileHashes },
    };
    await saveMarker(markerPath, updatedMarker);
  }

  // Report
  const lines: string[] = [];
  lines.push(`Migration complete.`);
  lines.push(`  Imported: ${totalImported} observation(s)`);
  if (totalSkipped > 0) lines.push(`  Skipped (duplicates): ${totalSkipped}`);
  if (skippedFiles.length > 0) lines.push(`  Skipped files (already migrated): ${skippedFiles.length}`);
  if (errors.length > 0) {
    lines.push(`  Errors: ${errors.join(', ')}`);
    lines.push(`  Note: marker NOT saved due to errors — re-run to retry failed chunks.`);
  }
  return lines.join('\n');
}
