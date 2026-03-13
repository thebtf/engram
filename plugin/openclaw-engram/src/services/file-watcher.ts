/**
 * FileWatcherService — watches workspace memory files and syncs them to engram.
 *
 * Replaces memory-core chokidar watcher. Watches MEMORY.md and memory directory,
 * debounces changes per file, and bulk-imports updated chunks into engram.
 */

import chokidar, { type FSWatcher } from 'chokidar';
import { join, relative } from 'node:path';
import { createHash } from 'node:crypto';
import type { EngramRestClient, BulkImportRequest } from '../client.js';
import type { PluginConfig } from '../config.js';
import type { OpenClawPluginService, OpenClawPluginServiceContext, PluginLogger } from '../types/openclaw.js';
import { resolveIdentity } from '../identity.js';
import {
  splitIntoChunks,
  loadMarker,
  saveMarker,
  safeReadFile,
  MARKER_FILE,
  type MigrationMarker,
} from '../utils/memory-files.js';

const DEBOUNCE_MS = 1500;

class FileWatcherService implements OpenClawPluginService {
  readonly id = 'file-watcher';
  private watcher: FSWatcher | null = null;
  private readonly debounceTimers: Map<string, ReturnType<typeof setTimeout>> = new Map();
  private readonly inFlight: Set<string> = new Set();
  private stopped = false;
  private readonly projectId: string;

  constructor(
    private readonly workspaceDir: string,
    private readonly client: EngramRestClient,
    private readonly config: PluginConfig,
    private readonly logger: PluginLogger,
  ) {
    const identity = resolveIdentity('file-watcher', workspaceDir);
    this.projectId = config.project ?? identity.projectId;
  }

  start(_ctx: OpenClawPluginServiceContext): void {
    const watchPaths = [
      join(this.workspaceDir, 'MEMORY.md'),
      join(this.workspaceDir, 'memory'),
    ];
    this.watcher = chokidar.watch(watchPaths, {
      ignoreInitial: true,
      awaitWriteFinish: { stabilityThreshold: 500 },
      persistent: false,
    });
    this.watcher.on('add', (filePath: string) => this.scheduleSync(filePath));
    this.watcher.on('change', (filePath: string) => this.scheduleSync(filePath));
    this.watcher.on('unlink', (filePath: string) => {
      const relPath = relative(this.workspaceDir, filePath);
      this.logger.debug(
        `[file-watcher] file deleted: ${relPath} — sync skipped (server-side cleanup not yet supported)`,
      );
    });
    this.watcher.on('error', (err: unknown) => {
      this.logger.warn(`[file-watcher] watcher error: ${String(err)}`);
    });
    this.logger.debug(`[file-watcher] watching ${this.workspaceDir}`);
  }

  stop(_ctx: OpenClawPluginServiceContext): void {
    this.stopped = true;
    for (const timer of this.debounceTimers.values()) {
      clearTimeout(timer);
    }
    this.debounceTimers.clear();
    if (this.watcher) {
      void this.watcher.close();
      this.watcher = null;
    }
    this.logger.debug('[file-watcher] stopped');
  }

  private scheduleSync(filePath: string): void {
    const existing = this.debounceTimers.get(filePath);
    if (existing !== undefined) {
      clearTimeout(existing);
    }
    const timer = setTimeout(() => {
      this.debounceTimers.delete(filePath);
      void this.syncFile(filePath);
    }, DEBOUNCE_MS);
    this.debounceTimers.set(filePath, timer);
  }

  private async syncFile(filePath: string): Promise<void> {
    if (this.stopped) return;
    if (this.inFlight.has(filePath)) {
      this.scheduleSync(filePath); // retry after debounce — don't drop concurrent changes
      return;
    }
    if (!this.client.isAvailable()) return;
    this.inFlight.add(filePath);
    try {
      const content = await safeReadFile(filePath);
      if (content === null) {
        this.logger.debug(`[file-watcher] skipping unreadable file: ${filePath}`);
        return;
      }
      const hash = createHash('sha256').update(content).digest('hex');
      const markerPath = join(this.workspaceDir, MARKER_FILE);
      const marker = await loadMarker(markerPath);
      const relPath = relative(this.workspaceDir, filePath);
      if (marker?.files[relPath] === hash) {
        this.logger.debug(`[file-watcher] no changes: ${relPath}`);
        return;
      }
      const chunks = splitIntoChunks(content, relPath);
      if (chunks.length === 0) return;
      const project = this.projectId;
      const requests: BulkImportRequest[] = chunks.map((chunk) => ({
        title: chunk.title,
        content: chunk.content,
        type: chunk.type,
        project,
        scope: 'project' as const,
        tags: ['synced', `source:${relPath}`],
      }));
      const response = await this.client.bulkImport(requests);
      if (response) {
        this.logger.debug(`[file-watcher] synced ${relPath}: ${response.imported} imported`);
      }
      const updatedMarker: MigrationMarker = {
        lastMigrated: new Date().toISOString(),
        files: { ...(marker?.files ?? {}), [relPath]: hash },
      };
      await saveMarker(markerPath, updatedMarker);
    } catch (err: unknown) {
      this.logger.warn(`[file-watcher] sync failed for ${filePath}: ${String(err)}`);
    } finally {
      this.inFlight.delete(filePath);
    }
  }
}

export function createFileWatcherService(
  workspaceDir: string,
  client: EngramRestClient,
  config: PluginConfig,
  logger: PluginLogger,
): OpenClawPluginService {
  return new FileWatcherService(workspaceDir, client, config, logger);
}
