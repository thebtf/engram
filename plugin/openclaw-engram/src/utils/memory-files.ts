/**
 * Shared memory file utilities — used by memory-migrate tool and file-watcher service.
 *
 * Provides chunking, marker file I/O, file discovery, and safe file reading.
 */

import { readFile, readdir, rename, stat, open, unlink } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { createHash, randomUUID } from 'node:crypto';
import { z } from 'zod';
import type { EngramRestClient, MemoryAttribution } from '../client.js';

export const MARKER_FILE = '.engram-migrated.json';
export const MAX_FILE_SIZE = 50_000; // 50KB limit per file

const WriteAttemptSchema = z.object({
 state: z.enum(['pending', 'unknown', 'rejected', 'not_sent']),
 attemptId: z.string(),
 fingerprint: z.string().optional(),
 project: z.string(),
 sourcePath: z.string(),
 createdAt: z.string(),
 error: z.string().optional(),
});

const MigrationMarkerSchema = z.object({
 lastMigrated: z.string(),
 files: z.record(z.string(), z.string()),
 fileProjects: z.record(z.string(), z.string()).optional(),
 chunks: z.record(z.string(), z.number().int().positive()).optional(),
 writes: z.record(z.string(), WriteAttemptSchema).optional(),
 unresolvedAttempts: z.record(z.string(), WriteAttemptSchema).optional(),
 acknowledgedAttempts: z.record(z.string(), z.object({
  mode: z.literal('force-reimport-with-duplicate-risk'),
  acknowledgedAt: z.string(),
  project: z.string(),
  sourcePath: z.string(),
  fileHash: z.string(),
  fingerprint: z.string().optional(),
  confirmedMemoryIds: z.array(z.number().int().positive()),
 })).optional(),
}).passthrough();

export type MigrationMarker = z.infer<typeof MigrationMarkerSchema>;

export interface MemoryChunk {
 title: string;
 content: string;
 sourcePath: string;
 type: string;
}

// ---------------------------------------------------------------------------
// Chunking
// ---------------------------------------------------------------------------

export function splitIntoChunks(content: string, sourcePath: string): MemoryChunk[] {
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

export function inferType(_sourcePath: string, content: string): string {
 const lower = content.toLowerCase();
 if (lower.includes('decision') || lower.includes('chose') || lower.includes('decided')) return 'decision';
 if (lower.includes('bug') || lower.includes('fix') || lower.includes('resolved')) return 'bugfix';
 if (lower.includes('pattern') || lower.includes('convention')) return 'discovery';
 return 'change';
}

// ---------------------------------------------------------------------------
// Marker file
// ---------------------------------------------------------------------------

export async function loadMarker(path: string): Promise<MigrationMarker | null> {
 try {
  const raw = await readFile(path, 'utf-8');
  const parsed = MigrationMarkerSchema.safeParse(JSON.parse(raw));
  return parsed.success ? parsed.data : null;
 } catch {
  return null;
 }
}

export async function saveMarker(markerPath: string, marker: MigrationMarker): Promise<void> {
 const tmp = `${markerPath}.${randomUUID()}.tmp`;
 try {
  const handle = await open(tmp, 'wx', 0o600);
  try {
   await handle.writeFile(JSON.stringify(marker, null, 2), 'utf-8');
   await handle.sync();
  } finally {
   await handle.close();
  }
  await rename(tmp, markerPath);
 } catch (error) {
  await unlink(tmp).catch(() => { });
  throw error;
 }
}

export interface MemoryFileSyncRequest {
 project: string;
 sourcePath: string;
 hash: string;
 chunks: MemoryChunk[];
 tag: 'migrated' | 'synced';
 force?: boolean;
}

export interface MemoryFileSyncResult {
 stored: number;
 alreadyStored: number;
 unresolved: number;
 acknowledged?: number;
 error?: string;
}

const markerQueues = new Map<string, Promise<void>>();

export async function syncMemoryFile(
 client: EngramRestClient,
 markerPath: string,
 file: MemoryFileSyncRequest,
 attribution: MemoryAttribution,
): Promise<MemoryFileSyncResult> {
 const absolute = resolve(markerPath);
 const key = process.platform === 'win32' ? absolute.toLowerCase() : absolute;
 const previous = markerQueues.get(key) ?? Promise.resolve();
 const run = previous.then(() => syncMemoryFileSerialized(client, markerPath, file, attribution));
 const idle = run.then(() => { }, () => { });
 markerQueues.set(key, idle);
 try {
  return await run;
 } finally {
  if (markerQueues.get(key) === idle) markerQueues.delete(key);
 }
}

async function syncMemoryFileSerialized(
 client: EngramRestClient,
 markerPath: string,
 file: MemoryFileSyncRequest,
 attribution: MemoryAttribution,
): Promise<MemoryFileSyncResult> {
 let stored = 0;
 let alreadyStored = 0;
 let unresolved = 0;
 try {
  const loaded = await loadMarker(markerPath);
  if (!loaded && await fileExists(markerPath)) {
   return { stored, alreadyStored, unresolved, error: 'Existing marker is unreadable or invalid; no writes sent and no progress overwritten' };
  }
  const marker: MigrationMarker = loaded ?? { lastMigrated: '', files: {} };
  const writes = marker.writes ?? {};
  const history = marker.unresolvedAttempts ?? {};
  const acknowledgements = marker.acknowledgedAttempts ?? {};
  const countUnresolved = () => new Set([...Object.values(writes), ...Object.values(history)]
   .filter(attempt => attempt.project === file.project && attempt.sourcePath === file.sourcePath && (attempt.state === 'pending' || attempt.state === 'unknown') && (!attempt.fingerprint || acknowledgements[attempt.attemptId]?.fingerprint !== attempt.fingerprint))
   .map(attempt => attempt.attemptId)).size;
  unresolved = countUnresolved();
  if (!file.force && unresolved > 0) {
   return { stored, alreadyStored, unresolved, error: 'Unresolved prior write blocks automatic resend. Inspect the server outcome; explicit force deliberately reimports and may duplicate an earlier committed row' };
  }
  const priorProject = marker.fileProjects?.[file.sourcePath];
  if (!file.force && marker.files[file.sourcePath] === file.hash) {
   if (priorProject === file.project) return { stored, alreadyStored: file.chunks.length, unresolved };
   if (!priorProject) return { stored, alreadyStored, unresolved, error: 'Legacy file checkpoint has no project attribution; inspect it or use explicit force for a deliberate potentially duplicate reimport' };
  }
  const confirmed = { ...(marker.chunks ?? {}) };
  const forcedIds: number[] | undefined = file.force ? [] : undefined;
  const forcedFingerprints = file.force ? new Set<string>() : undefined;
  for (const chunk of file.chunks) {
   const fingerprint = createHash('sha256').update(JSON.stringify([file.project, file.sourcePath, chunk.title, chunk.content, chunk.type])).digest('hex');
   if (!file.force && confirmed[fingerprint]) {
    alreadyStored++;
    continue;
   }
   const prior = writes[fingerprint];
   if (prior && (prior.state === 'pending' || prior.state === 'unknown')) {
    // Explicit force is a deliberate new write, not proof the earlier one
    // failed. Retain the earlier attempt even if this new write is confirmed.
    history[prior.attemptId] ??= { ...prior, fingerprint };
   }
   writes[fingerprint] = {
    state: 'pending', attemptId: randomUUID(), fingerprint, project: file.project,
    sourcePath: file.sourcePath, createdAt: new Date().toISOString(),
   };
   marker.writes = writes;
   marker.unresolvedAttempts = history;
   await saveMarker(markerPath, marker);
   unresolved = countUnresolved();
   const [result] = await client.storeMemories([{
    title: chunk.title, content: chunk.content, type: chunk.type,
    project: file.project, scope: 'project', tags: [file.tag, `source:${file.sourcePath}`],
   }], attribution);
   if (!result?.ok) {
    writes[fingerprint].state = result?.outcome ?? 'unknown';
    writes[fingerprint].error = result?.error ?? 'No memory write confirmation';
    await saveMarker(markerPath, marker);
    unresolved = countUnresolved();
    return { stored, alreadyStored, unresolved, error: result?.error ?? 'No memory write confirmation; outcome is unknown' };
   }
   stored++;
   confirmed[fingerprint] = result.memory.id;
   forcedIds?.push(result.memory.id);
   forcedFingerprints?.add(fingerprint);
   delete writes[fingerprint];
   marker.chunks = confirmed;
   marker.lastMigrated = new Date().toISOString();
   await saveMarker(markerPath, marker);
  }
  let acknowledged = 0;
  if (forcedIds && forcedIds.length > 0 && forcedIds.length === file.chunks.length) {
   for (const attempt of Object.values(history)) {
    if (attempt.project === file.project && attempt.sourcePath === file.sourcePath && (attempt.state === 'pending' || attempt.state === 'unknown') && attempt.fingerprint !== undefined && forcedFingerprints?.has(attempt.fingerprint) && !acknowledgements[attempt.attemptId]) {
     acknowledgements[attempt.attemptId] = {
      mode: 'force-reimport-with-duplicate-risk', acknowledgedAt: new Date().toISOString(),
      project: file.project, sourcePath: file.sourcePath, fileHash: file.hash,
      fingerprint: attempt.fingerprint,
      confirmedMemoryIds: forcedIds,
     };
     acknowledged++;
    }
   }
   marker.unresolvedAttempts = history;
   marker.acknowledgedAttempts = acknowledgements;
  }
  marker.files[file.sourcePath] = file.hash;
  marker.fileProjects = { ...(marker.fileProjects ?? {}), [file.sourcePath]: file.project };
  await saveMarker(markerPath, marker);
  unresolved = countUnresolved();
  return { stored, alreadyStored, unresolved, acknowledged };
 } catch (error) {
  return { stored, alreadyStored, unresolved, error: `Memory/checkpoint outcome needs inspection; do not blindly retry: ${String(error)}` };
 }
}

// ---------------------------------------------------------------------------
// File discovery
// ---------------------------------------------------------------------------

export async function discoverMemoryFiles(workspaceDir: string): Promise<string[]> {
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
// Helpers
// ---------------------------------------------------------------------------

export async function fileExists(path: string): Promise<boolean> {
 try {
  await stat(path);
  return true;
 } catch {
  return false;
 }
}

export async function safeReadFile(path: string): Promise<string | null> {
 try {
  const info = await stat(path);
  if (info.size > MAX_FILE_SIZE) return null;
  return await readFile(path, 'utf-8');
 } catch {
  return null;
 }
}
