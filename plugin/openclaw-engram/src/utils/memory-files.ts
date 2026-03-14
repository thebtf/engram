/**
 * Shared memory file utilities — used by memory-migrate tool and file-watcher service.
 *
 * Provides chunking, marker file I/O, file discovery, and safe file reading.
 */

import { readFile, readdir, writeFile, rename, stat } from 'node:fs/promises';
import { join } from 'node:path';

export const MARKER_FILE = '.engram-migrated.json';
export const MAX_FILE_SIZE = 50_000; // 50KB limit per file

export interface MigrationMarker {
  lastMigrated: string;
  files: Record<string, string>; // path → content SHA256
}

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
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object' || typeof parsed.files !== 'object') {
      return null; // corrupted marker, treat as fresh
    }
    return parsed as MigrationMarker;
  } catch {
    return null;
  }
}

export async function saveMarker(markerPath: string, marker: MigrationMarker): Promise<void> {
  // Atomic write via temp file + rename to reduce race window when
  // two agents migrate concurrently (last writer wins, no corruption).
  const tmp = markerPath + '.tmp';
  try {
    await writeFile(tmp, JSON.stringify(marker, null, 2), 'utf-8');
    await rename(tmp, markerPath);
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    console.warn(`[engram] failed to save migration marker: ${msg}`);
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
