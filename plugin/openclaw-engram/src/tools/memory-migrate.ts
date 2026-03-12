/**
 * memory_migrate — import local memory files into engram.
 *
 * Reads MEMORY.md and memory/**\/*.md from the workspace, splits by ## headers,
 * and bulk-imports into engram. Uses a marker file for idempotency.
 */

import { z } from 'zod';
import { Type } from '@sinclair/typebox';
import { readFile, readdir, writeFile, stat, lstat } from 'node:fs/promises';
import { join, relative, resolve, normalize } from 'node:path';
import { createHash } from 'node:crypto';
import type { EngramRestClient, BulkImportRequest } from '../client.js';
import type { PluginConfig } from '../config.js';
import { resolveIdentity } from '../identity.js';
import type {
  AnyAgentTool,
  OpenClawPluginToolContext,
  OpenClawPluginApi,
} from '../types/openclaw.js';

const MARKER_FILE = '.engram-migrated.json';
const CONTENT_MAX_CHARS = 900;
const MAX_FILE_SIZE = 50_000; // 50KB limit per file

const MigrateParamsSchema = z.object({
  dryRun: z.boolean().optional().default(false),
  path: z.string().optional(),
  force: z.boolean().optional().default(false),
});

const migrateParameters = Type.Object({
  dryRun: Type.Optional(Type.Boolean({ description: 'Preview what would be imported without writing', default: false })),
  path: Type.Optional(Type.String({ description: 'Specific file path to migrate (default: MEMORY.md + memory/**/*.md)' })),
  force: Type.Optional(Type.Boolean({ description: 'Ignore migration marker, re-import everything', default: false })),
});

interface MigrationMarker {
  lastMigrated: string;
  files: Record<string, string>; // path → content SHA256
}

interface MemoryChunk {
  title: string;
  content: string;
  sourcePath: string;
  type: string;
}

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
  const marker = params.force ? null : await loadMarker(markerPath);

  // Discover files
  let filePaths: string[];
  if (params.path) {
    const resolved = normalize(resolve(api.resolvePath(params.path)));
    const normalizedWs = normalize(resolve(workspaceDir));
    if (!resolved.startsWith(normalizedWs)) {
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

    // Skip if already migrated with same hash
    if (marker?.files[relPath] === hash) {
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

  // Dry run — just report
  if (params.dryRun) {
    const lines = [`Dry run: ${allChunks.length} chunk(s) from ${filePaths.length - skippedFiles.length} file(s) would be imported.\n`];
    if (skippedFiles.length > 0) {
      lines.push(`Skipped (already migrated): ${skippedFiles.length} file(s)\n`);
    }
    lines.push('Chunks:');
    let truncatedCount = 0;
    for (const chunk of allChunks) {
      const willTruncate = chunk.content.length > CONTENT_MAX_CHARS;
      if (willTruncate) truncatedCount++;
      const truncTag = willTruncate ? ` [TRUNCATED: ${chunk.content.length} → ${CONTENT_MAX_CHARS} chars]` : '';
      const preview = chunk.content.length > 80 ? chunk.content.slice(0, 77) + '...' : chunk.content;
      lines.push(`- [${chunk.type}] "${chunk.title}" (from ${chunk.sourcePath})${truncTag}: ${preview}`);
    }
    if (truncatedCount > 0) {
      lines.push(`\nNote: ${truncatedCount} chunk(s) exceed ${CONTENT_MAX_CHARS} chars and will be truncated on import.`);
    }
    return lines.join('\n');
  }

  // Import
  const identity = resolveIdentity(ctx.agentId ?? '', workspaceDir);
  const project = config.project ?? identity.projectId;

  const observations: BulkImportRequest[] = allChunks.map((chunk) => ({
    title: chunk.title,
    content: chunk.content.length > CONTENT_MAX_CHARS ? chunk.content.slice(0, CONTENT_MAX_CHARS) : chunk.content,
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
      totalSkipped += response.skipped;
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
  const truncated = observations.filter((o) => o.content.length === CONTENT_MAX_CHARS).length;
  if (truncated > 0) lines.push(`  Truncated: ${truncated} chunk(s) exceeded ${CONTENT_MAX_CHARS} chars`);
  return lines.join('\n');
}

// ---------------------------------------------------------------------------
// File discovery
// ---------------------------------------------------------------------------

async function discoverMemoryFiles(workspaceDir: string): Promise<string[]> {
  const files: string[] = [];

  // Check MEMORY.md
  const memoryMd = join(workspaceDir, 'MEMORY.md');
  if (await fileExists(memoryMd)) {
    files.push(memoryMd);
  }

  // Check memory/ directory recursively
  const memoryDir = join(workspaceDir, 'memory');
  if (await fileExists(memoryDir)) {
    const mdFiles = await findMdFiles(memoryDir);
    files.push(...mdFiles);
  }

  return files;
}

async function findMdFiles(dir: string, depth = 0): Promise<string[]> {
  if (depth > 10) return []; // Guard against excessive nesting
  const results: string[] = [];
  try {
    const entries = await readdir(dir, { withFileTypes: true });
    for (const entry of entries) {
      if (entry.isSymbolicLink()) continue; // Prevent symlink cycles
      const fullPath = join(dir, entry.name);
      if (entry.isDirectory()) {
        const nested = await findMdFiles(fullPath, depth + 1);
        results.push(...nested);
      } else if (entry.isFile() && entry.name.endsWith('.md')) {
        results.push(fullPath);
      }
    }
  } catch {
    // Directory not readable — skip
  }
  return results;
}

// ---------------------------------------------------------------------------
// Chunking
// ---------------------------------------------------------------------------

function splitIntoChunks(content: string, sourcePath: string): MemoryChunk[] {
  const chunks: MemoryChunk[] = [];
  const lines = content.split('\n');

  // Try splitting by ## headers
  const sections: Array<{ title: string; content: string }> = [];
  let currentTitle = '';
  let currentContent: string[] = [];

  let inFence = false;
  for (const line of lines) {
    if (line.startsWith('```')) {
      inFence = !inFence;
    }
    const headerMatch = !inFence ? line.match(/^##\s+(.+)/) : null;
    if (headerMatch) {
      if (currentContent.length > 0) {
        sections.push({
          title: currentTitle || sourcePath,
          content: currentContent.join('\n').trim(),
        });
      }
      currentTitle = headerMatch[1].trim();
      currentContent = [];
    } else {
      currentContent.push(line);
    }
  }
  // Push last section
  if (currentContent.length > 0) {
    sections.push({
      title: currentTitle || sourcePath,
      content: currentContent.join('\n').trim(),
    });
  }

  // If no ## headers found, treat whole file as one chunk
  if (sections.length === 0) {
    const trimmed = content.trim();
    if (trimmed) {
      chunks.push({
        title: sourcePath,
        content: trimmed,
        sourcePath,
        type: inferType(sourcePath, trimmed),
      });
    }
    return chunks;
  }

  // Filter out empty sections and create chunks
  for (const section of sections) {
    if (!section.content) continue;
    chunks.push({
      title: section.title,
      content: section.content,
      sourcePath,
      type: inferType(sourcePath, section.content),
    });
  }

  return chunks;
}

function inferType(sourcePath: string, content: string): string {
  const lower = content.toLowerCase();
  if (lower.includes('decision') || lower.includes('chose') || lower.includes('decided')) return 'decision';
  if (lower.includes('bug') || lower.includes('fix') || lower.includes('resolved')) return 'bugfix';
  if (lower.includes('pattern') || lower.includes('convention')) return 'discovery';
  return 'context';
}

// ---------------------------------------------------------------------------
// Marker file
// ---------------------------------------------------------------------------

async function loadMarker(path: string): Promise<MigrationMarker | null> {
  try {
    const raw = await readFile(path, 'utf-8');
    return JSON.parse(raw) as MigrationMarker;
  } catch {
    return null;
  }
}

async function saveMarker(path: string, marker: MigrationMarker): Promise<void> {
  try {
    await writeFile(path, JSON.stringify(marker, null, 2), 'utf-8');
  } catch {
    // Non-critical — migration still succeeded
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

async function fileExists(path: string): Promise<boolean> {
  try {
    await stat(path);
    return true;
  } catch {
    return false;
  }
}

async function safeReadFile(path: string): Promise<string | null> {
  try {
    const info = await stat(path);
    if (info.size > MAX_FILE_SIZE) return null;
    return await readFile(path, 'utf-8');
  } catch {
    return null;
  }
}
